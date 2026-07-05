/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// vmShapeDrift compares the VM's declared shape against the desired class/storage.
// Observed cpu/mem come from spec.template resources.limits (set by both the VM
// builder and ResizeVM); observed storage from the data-PVC entry of the
// volumeClaimTemplates annotation. A shape element that cannot be observed (missing
// limits/annotation — never true for VMs this controller built) is skipped rather
// than fought.
type vmShapeDrift struct {
	cpuMem        bool
	storage       bool
	storageShrink bool
}

func (d vmShapeDrift) any() bool { return d.cpuMem || d.storage }

func observeShapeDrift(vm *kubevirtv1.VirtualMachine, inst *dbaasv1.DBInstance, class dbaasv1.InstanceClassSpec) vmShapeDrift {
	var drift vmShapeDrift

	limits := vm.Spec.Template.Spec.Domain.Resources.Limits
	if limits != nil {
		if cpu, ok := limits[corev1.ResourceCPU]; ok && cpu.Value() != int64(class.CPUCores) {
			drift.cpuMem = true
		}
		if mem, ok := limits[corev1.ResourceMemory]; ok {
			desired := resource.MustParse(fmt.Sprintf("%dMi", class.MemoryMB))
			if mem.Cmp(desired) != 0 {
				drift.cpuMem = true
			}
		}
	}

	if pvcs, err := harvester.VolumeClaimTemplates(vm); err == nil {
		desired := resource.MustParse(fmt.Sprintf("%dGi", inst.Spec.AllocatedStorage))
		for _, pvc := range pvcs {
			if pvc == nil || pvc.Name != inst.Status.Resources.DataVolumeName {
				continue
			}
			observed := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			switch desired.Cmp(observed) {
			case 1:
				drift.storage = true
			case -1:
				drift.storageShrink = true
			}
		}
	}

	return drift
}

// ensureStorageResize converges the VM's declared shape (cpu/memory from the
// instance class, data-disk size from spec.allocatedStorage) via a cold resize:
// halt → apply → let ensurePowerState (ordered after this step) restart. Progress
// is re-derived from observed state each pass — no persisted phase pointer:
//
//	shape matches            → Satisfied
//	drift, runStrategy!=Halted → StopVM            → Pending
//	drift, VMI still running   → wait for teardown  → Pending (timer)
//	drift, VM down             → ResizeVM/ResizeDataVolume → Pending (re-observe)
//
// Ordered BEFORE ensurePowerState so the two never fight: this step holds the VM
// down only while drift exists; once the shape converges, the power step observes
// "desired running, declared Halted" and restarts.
func (r *DBInstanceReconciler) ensureStorageResize(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	class, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		// ensurePreflight terminals on this first; defensive.
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		markProvisioningFailed(inst, "InvalidClass", msg)
		return terminal("InvalidClass", msg)
	}

	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmNameFor(inst)}, &vm); err != nil {
		return transient(err) // ensureVM ran first; a miss is cache lag
	}

	drift := observeShapeDrift(&vm, inst, class)

	// The provider resize is grow-only (Harvester ignores shrink requests), so a
	// shrink would never converge — fail loudly instead of halt-looping forever.
	if drift.storageShrink {
		msg := fmt.Sprintf("allocatedStorage %dGi is below the currently provisioned size; storage shrink is not supported — revert the change", inst.Spec.AllocatedStorage)
		setStepCond(inst, dbaasv1.ConditionStorageReady, metav1.ConditionFalse, "UnsupportedShrink", msg)
		markProvisioningFailed(inst, "UnsupportedShrink", msg)
		return terminal("UnsupportedShrink", msg)
	}

	if !drift.any() {
		setStepCond(inst, dbaasv1.ConditionStorageReady, metav1.ConditionTrue,
			"ShapeConverged", "VM class and storage match the spec")
		return satisfied()
	}

	// Cold resize in progress.
	inst.Status.Phase = dbaasv1.StatusModifying
	halted := vm.Spec.RunStrategy != nil && *vm.Spec.RunStrategy == kubevirtv1.RunStrategyHalted

	if !halted {
		if err := r.Harvester.StopVM(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
			return transient(err)
		}
		msg := fmt.Sprintf("stopping VM for cold resize to %s / %dGi", inst.Spec.DBInstanceClass, inst.Spec.AllocatedStorage)
		setStepCond(inst, dbaasv1.ConditionStorageReady, metav1.ConditionFalse, "ResizeStopping", msg)
		inst.Status.Message = msg
		return pending("ResizeStopping", msg)
	}

	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, vmNameFor(inst))
	if err != nil && !apierrors.IsNotFound(err) {
		return transient(err)
	}
	if err == nil && readiness.Running {
		msg := "waiting for VM to stop before resize"
		setStepCond(inst, dbaasv1.ConditionStorageReady, metav1.ConditionFalse, "ResizeWaitingForTeardown", msg)
		inst.Status.Message = msg
		return pendingAfter("ResizeWaitingForTeardown", msg, powerRequeue)
	}

	// VM is down: apply whichever shape elements drifted. Both provider calls are
	// idempotent, so a crash between them re-applies safely next pass.
	if drift.cpuMem {
		if err := r.Harvester.ResizeVM(ctx, inst.Namespace, vmNameFor(inst), class.CPUCores, class.MemoryMB); err != nil {
			return transient(err)
		}
	}
	if drift.storage {
		if err := r.Harvester.ResizeDataVolume(ctx, inst.Namespace, vmNameFor(inst), inst.Status.Resources.DataVolumeName, inst.Spec.AllocatedStorage); err != nil {
			return transient(err)
		}
	}
	msg := fmt.Sprintf("applied resize to %s / %dGi", inst.Spec.DBInstanceClass, inst.Spec.AllocatedStorage)
	setStepCond(inst, dbaasv1.ConditionStorageReady, metav1.ConditionFalse, "ResizeApplied", msg)
	inst.Status.Message = msg
	// Next pass re-observes the shape: no drift → Satisfied → ensurePowerState restarts.
	return pending("ResizeApplied", msg)
}
