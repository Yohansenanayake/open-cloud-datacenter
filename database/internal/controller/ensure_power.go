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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// powerRequeue is the timer fallback while a power transition (VMI appearing or
// tearing down) is in flight. The VMI watch usually re-triggers sooner; the
// timer covers windows with no VMI events.
const powerRequeue = 5 * time.Second

// wantRunning is the desired power state from the user's spec: running unless
// explicitly stopped. Crash-loop handling overrides this in ensurePowerState only —
// steps that merely skip work while stopped (health/monitoring/ready) key on the
// spec alone.
func wantRunning(inst *dbaasv1.DBInstance) bool {
	return inst.Spec.Running == nil || *inst.Spec.Running
}

// ensurePowerState converges the VM's power state onto spec.running. Two observed
// layers drive the outcome:
//
//	declared — vm.spec.runStrategy (what we write via StartVM/StopVM);
//	runtime  — whether a VMI is actually running (GetVMIReadiness; NotFound = gone).
//
// declared wrong → request start/stop → Pending (event-driven: the status write
// re-triggers). declared right but runtime catching up → Pending (timer). Both
// agree → Satisfied.
func (r *DBInstanceReconciler) ensurePowerState(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	// Crash-loop halt: ensureDatabaseHealth halted the VM at detection and owns
	// park + recovery. This step only REFUSES TO START (spec.running=true must not
	// resurrect a crash-looper) — it must not actively stop either, because
	// recovery is an out-of-band operator start that power would otherwise fight
	// before health can observe it healthy. Satisfied lets the pass reach health.
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse,
			dbaasv1.ReasonCrashLoopHalted, "power management suspended during crash-loop halt")
		return satisfied()
	}

	desiredRunning := wantRunning(inst)

	// OBSERVE the declared layer: the VM's runStrategy.
	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmNameFor(inst)}, &vm); err != nil {
		return transient(err) // ensureVM ran first; a miss is cache lag
	}
	declaredRunning := vm.Spec.RunStrategy != nil && *vm.Spec.RunStrategy == kubevirtv1.RunStrategyAlways

	// OBSERVE the runtime layer: is a VMI actually running? NotFound means the
	// VMI is gone (normal for a stopped VM), not an infrastructure failure.
	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, vmNameFor(inst))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return transient(err)
		}
		//VMI is gone (not found) - treat as stopped.
		readiness = harvester.VMIReadiness{}
	}

	if desiredRunning {
		switch {
		// Declared layer wrong, but the previous VMI is still tearing down: the
		// KubeVirt start subresource rejects while a VMI object exists, so wait.
		case !declaredRunning && readiness.Running:
			msg := "waiting for previous VMI to finish stopping before start"
			setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStartWaitingForTeardown, msg)
			return pendingAfter(dbaasv1.ReasonStartWaitingForTeardown, msg, powerRequeue)

		// Declared layer wrong → request the start.
		case !declaredRunning:
			if err := r.Harvester.StartVM(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
				return transient(err)
			}
			// Planned start: reset the UID baseline so the new VMI is not
			// counted as an unplanned restart by the liveness monitor, and
			// clear the crash-loop chain so restart history from before this
			// deliberate start doesn't carry over into a fresh halt threshold.
			inst.Status.LastKnownVMIUID = ""
			inst.Status.RecentUnplannedRestarts = 0
			inst.Status.LastUnplannedRestartTime = nil
			msg := "requested VM start"
			setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStarting, msg)
			return pending(dbaasv1.ReasonStarting, msg)

		// Declared right, runtime catching up (boot in progress).
		case !readiness.Running:
			msg := "VM starting; waiting for VMI to run"
			setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStarting, msg)
			return pendingAfter(dbaasv1.ReasonStarting, msg, 2*powerRequeue)

		default:
			setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionTrue, dbaasv1.ReasonRunning, "VM running")
			return satisfied()
		}
	}

	// desired stopped (spec.running=false) — crash-loop halt already returned above.
	// This step runs before ensureDatabaseHealth, so health won't get a chance to
	// re-observe the VM this pass while the stop is in flight — the two Pending
	// branches below set DatabaseReady=False themselves so it (and Ready, derived
	// from it) don't stay stale-True for the whole stop transition (same
	// reasoning as the crash-loop halt and the cold-resize halt).
	switch {
	case declaredRunning:
		if err := r.Harvester.StopVM(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
			return transient(err)
		}
		msg := "requested VM stop"
		setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		return pending(dbaasv1.ReasonStopping, msg)

	case readiness.Running:
		msg := "VM stopping; waiting for VMI teardown"
		setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		return pendingAfter(dbaasv1.ReasonStopping, msg, powerRequeue)

	default:
		// Fully stopped: clear a Degraded left over from the running steady state —
		// a stopped database is not degraded.
		removeCondition(inst, dbaasv1.ConditionDegraded)
		setStepCond(inst, dbaasv1.ConditionPowerStateReady, metav1.ConditionTrue, dbaasv1.ReasonStopped, "VM stopped")
		return satisfied()
	}
}
