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

package ensure

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

type powerStep struct{ Dependencies }

func newPowerStep(deps Dependencies) Step { return &powerStep{Dependencies: deps} }

func (*powerStep) Name() string { return "power" }

// ensurePowerState converges the VM's power state onto spec.running. Two observed
// layers drive the outcome:
//
//	declared — vm.spec.runStrategy (what we write via StartVM/StopVM);
//	runtime  — whether a VMI is actually running (GetVMIReadiness; NotFound = gone).
//
// declared wrong → request start/stop → Pending (event-driven: the status write
// re-triggers). declared right but runtime catching up → Pending (timer). Both
// agree → Satisfied.
func (r *powerStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	// Crash-loop halt: ensureDatabaseHealth halted the VM at detection and owns
	// park + recovery. This step only REFUSES TO START (spec.running=true must not
	// resurrect a crash-looper) — it must not actively stop either, because
	// recovery starts through manual administrator action that power would otherwise fight
	// before health can observe it healthy. Satisfied lets the pass reach health.
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse,
			dbaasv1.ReasonCrashLoopHalted, "power management suspended during crash-loop halt")
		return Satisfied()
	}

	desiredRunning := inst.Spec.WantRunning()

	// OBSERVE the declared layer: the VM's runStrategy.
	var vm kubevirtv1.VirtualMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: vmNameFor(inst)}, &vm); err != nil {
		return Transient(err) // ensureVM ran first; a miss is cache lag
	}
	declaredRunning := vm.Spec.RunStrategy != nil && *vm.Spec.RunStrategy == kubevirtv1.RunStrategyAlways

	// OBSERVE the runtime layer: is a VMI actually running? NotFound means the
	// VMI is gone (normal for a stopped VM), not an infrastructure failure.
	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, vmNameFor(inst))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return Transient(err)
		}
		// An absent VMI is the runtime representation of a fully stopped VM. Zero value represents that.
		readiness = harvester.VMIReadiness{}
	}

	// Normally the persisted CrashLoopHalted condition returns above. This annotation
	// recovers that status if the VM halt succeeded but its status write did not;
	// its VMI UID also prevents health step from mistaking teardown for recovery.
	if haltedVMIUID := vm.Annotations[dbaasv1.AnnotationCrashLoopHaltedVMIUID]; haltedVMIUID != "" {
		msg := "VM halted after repeated unplanned restarts; manual intervention required"
		inst.Status.LastKnownVMIUID = haltedVMIUID
		inst.SetCurrentCondition(dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue,
			dbaasv1.ReasonCrashLoopDetected, msg)
		inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse,
			dbaasv1.ReasonCrashLoopHalted, "power management suspended during crash-loop halt")
		inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
			dbaasv1.ReasonCrashLoopDetected, msg)
		return Satisfied()
	}

	if desiredRunning {
		switch {
		// Declared layer wrong, but the previous VMI is still tearing down: the
		// KubeVirt start subresource rejects while a VMI object exists, so wait.
		case !declaredRunning && readiness.Running:
			msg := "waiting for previous VMI to finish stopping before start"
			inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStartWaitingForTeardown, msg)
			return PendingAfter(dbaasv1.ReasonStartWaitingForTeardown, msg, powerRequeue)

		// Declared layer wrong → request the start.
		case !declaredRunning:
			if err := r.Harvester.StartVM(ctx, inst.Namespace, vmNameFor(inst)); err != nil {
				return Transient(err)
			}
			// Planned start: reset the UID baseline so the new VMI is not
			// counted as an unplanned restart by the liveness monitor, and
			// clear the crash-loop chain so restart history from before this
			// deliberate start doesn't carry over into a fresh halt threshold.
			inst.Status.LastKnownVMIUID = ""
			inst.Status.RecentUnplannedRestarts = 0
			inst.Status.LastUnplannedRestartTime = nil
			msg := "requested VM start"
			inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStarting, msg)
			return Pending(dbaasv1.ReasonStarting, msg)

		// Declared right, runtime catching up (boot in progress).
		case !readiness.Running:
			msg := "VM starting; waiting for VMI to run"
			inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStarting, msg)
			return PendingAfter(dbaasv1.ReasonStarting, msg, 2*powerRequeue)

		default:
			inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionTrue, dbaasv1.ReasonRunning, "VM running")
			return Satisfied()
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
			return Transient(err)
		}
		msg := "requested VM stop"
		inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		return Pending(dbaasv1.ReasonStopping, msg)

	case readiness.Running:
		msg := "VM stopping; waiting for VMI teardown"
		inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		inst.SetCurrentCondition(dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonStopping, msg)
		return PendingAfter(dbaasv1.ReasonStopping, msg, powerRequeue)

	default:
		// Fully stopped: clear a Degraded left over from the running steady state —
		// a stopped database is not degraded.
		inst.Status.RemoveCondition(dbaasv1.ConditionDegraded)
		inst.SetCurrentCondition(dbaasv1.ConditionPowerStateReady, metav1.ConditionTrue, dbaasv1.ReasonStopped, "VM stopped")
		return Satisfied()
	}
}
