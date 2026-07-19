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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

const (
	// healthRequeue is the timer fallback while waiting for the VM/PostgreSQL to
	// come up. The VMI watch usually re-triggers sooner; the timer covers windows
	// with no VMI events (e.g. before the VMI object exists).
	healthRequeue = 10 * time.Second

	// crashLoopParkRequeue is the cold re-probe cadence while parked under
	// CrashLoopHalted. Status is unchanged between probes, so the DeepEqual skip
	// keeps the loop write-free.
	crashLoopParkRequeue = 30 * time.Second

	// Crash-loop detection for unplanned restarts (KI-006 Problem A). Under
	// RunStrategyAlways KubeVirt recreates the VMI on every guest exit, so a
	// crash-looping VM "recovers" forever on its own. A chain of unplanned
	// restarts (VMI UID changes), each within crashLoopWindow of the previous,
	// reaching crashLoopThreshold halts the VM and parks the instance under the
	// CrashLoopHalted condition.
	crashLoopThreshold = 3                // chained unplanned restarts before giving up
	crashLoopWindow    = 10 * time.Minute // max gap between restarts to extend the chain
)

// ensureDatabaseHealth is both the provisioning readiness gate and the
// steady-state liveness monitor, all from one VMI observation per pass:
//
//  1. while parked under CrashLoopHalted it re-probes every 30s and
//     auto-recovers when an operator brings the VM back healthy out-of-band;
//  2. otherwise, the crash-loop guard runs FIRST — a gate can never starve it;
//  3. while catching up (observedGeneration != generation) it GATES: booting /
//     probe-not-passing → Pending;
//  4. once caught up, a probe blip is REPORT-ONLY: Degraded is set with
//     attribution, phase turns "degraded", and the step returns Satisfied so the
//     pass finishes and Ready is re-derived (never left stale-True).
//
// Liveness is report-only by design: the KubeVirt readiness probe (pg_isready via
// the guest agent, already debounced by its FailureThreshold) is authoritative,
// and the controller never restarts on readiness failure — the only
// controller-initiated halt is the crash-loop guard.
func (r *DBInstanceReconciler) ensureDatabaseHealth(ctx context.Context, inst *dbaasv1.DBInstance) StepResult {
	// Desired stopped: there is nothing to gate on
	if !wantRunning(inst) {
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse,
			dbaasv1.ReasonStopped, "instance is stopped")
		return satisfied()
	}

	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, inst.Status.Resources.VMName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			// An unobserved VMI is not a health signal: Degraded is left untouched
			// and no VM operation is issued — the error just backs off.
			return transient(err)
		}
		readiness = harvester.VMIReadiness{} // VMI object gone: boot gate / parked
	}

	// Parked under CrashLoopHalted: recovery is an out-of-band operator start.
	// Handle the parked state before restart-counting:
	// the recovery VMI has a new UID by definition and must get a chance to prove
	// healthy instead of being counted as another crash-loop restart and halted
	// immediately.
	if inst.Status.IsConditionTrue(dbaasv1.ConditionCrashLoopHalted) {
		if readiness.Running && readiness.Ready && readiness.AgentConnected && readiness.IP != "" {
			r.Recorder.Eventf(inst, corev1.EventTypeNormal, string(dbaasv1.ReasonRecovered),
				"VM healthy again after crash-loop halt; resuming reconciliation")
			removeCondition(inst, dbaasv1.ConditionCrashLoopHalted)
			// Re-snapshot the recovered VMI so its UID is not counted as another
			// unplanned restart.
			inst.Status.LastKnownVMIUID = readiness.VMIUID
			inst.Status.RecentUnplannedRestarts = 0
			// Falls through rather than returning: readiness was already fetched
			// once above and just proved fully healthy, so the checks below
			// (using that same snapshot) re-derive Endpoint/DatabaseReady/Phase in
			// this same pass instead of wasting a reconcile on a "Recovering"
			// state no observer could ever reliably catch. The Recorder event
			// above is the durable record that a recovery happened.
		} else if readiness.Running && readiness.Ready && readiness.AgentConnected {
			msg := "recovery VM healthy; waiting for data-net IP"
			return pendingAfter(dbaasv1.ReasonVMBooting, msg, healthRequeue)
		} else {
			msg := "crash-loop halted; VM kept down — start the VM out-of-band once repaired to recover"
			return pendingAfter(dbaasv1.ReasonCrashLoopHalted, msg, crashLoopParkRequeue)
		}
	}

	// Crash-loop guard: first on every non-parked path, before readiness gates.
	if res, halted := r.trackRestarts(ctx, inst, readiness); halted {
		return res
	}

	port := specPort(inst.Spec.Port)

	// While catching up (spec change / provisioning in flight) the step GATES so
	// downstream steps and Ready wait for real readiness. Once caught up, blips
	// are report-only (below) so the pass always finishes.
	caughtUp := inst.Status.ObservedGeneration == inst.Generation

	if !readiness.Running || readiness.IP == "" {
		if caughtUp {
			r.reportDegraded(inst, readiness)
			return satisfied() //Report only by design, nothing left controller can do
		}
		msg := "VM booting; waiting for guest agent and data-net IP"
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonVMBooting, msg)
		return pendingAfter(dbaasv1.ReasonVMBooting, msg, healthRequeue)
	}

	if !readiness.Ready {
		if caughtUp {
			r.reportDegraded(inst, readiness)
			return satisfied()
		}
		msg := fmt.Sprintf("PostgreSQL initializing; readiness probe not passing at %s:%d", readiness.IP, port)
		setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonPostgresInitializing, msg)
		return pendingAfter(dbaasv1.ReasonPostgresInitializing, msg, healthRequeue)
	}

	// Healthy: clear any Degraded, refresh the endpoint (the data-net IP can
	// change after a restart or live migration), report ready.
	removeCondition(inst, dbaasv1.ConditionDegraded)
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	inst.Status.Endpoint = &dbaasv1.Endpoint{
		Address: readiness.IP,
		Port:    port,
		JDBCURL: fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-ca", readiness.IP, port, dbName),
	}
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionTrue,
		dbaasv1.ReasonPostgresReady, "PostgreSQL is ready")
	return satisfied()
}

// trackRestarts detects unplanned restarts (VMI UID changes — distinct from live
// migration, which preserves the UID) and chain-counts them into the crash-loop
// guard. It runs before any gate so a Pending return can never starve it. The
// restart history lives in status because it cannot be reconstructed from one
// VMI snapshot — it is an accumulated observation, never a step gate.
//
// At the threshold the VM is halted HERE, at detection (under RunStrategyAlways
// KubeVirt would otherwise restart it forever); ensurePowerState then refuses to
// start it while CrashLoopHalted but never fights an out-of-band recovery start.
func (r *DBInstanceReconciler) trackRestarts(ctx context.Context, inst *dbaasv1.DBInstance, readiness harvester.VMIReadiness) (StepResult, bool) {
	if readiness.VMIUID == "" {
		return StepResult{}, false
	}
	if inst.Status.LastKnownVMIUID == "" {
		// First observation of a running VMI — snapshot the baseline.
		inst.Status.LastKnownVMIUID = readiness.VMIUID
		return StepResult{}, false
	}
	if inst.Status.LastKnownVMIUID == readiness.VMIUID {
		return StepResult{}, false
	}

	log.FromContext(ctx).Info("unplanned VMI restart detected",
		"oldUID", inst.Status.LastKnownVMIUID, "newUID", readiness.VMIUID)
	r.Recorder.Eventf(inst, corev1.EventTypeWarning, string(dbaasv1.ReasonVMRestarting),
		"Unplanned VMI restart detected (UID %s → %s)", inst.Status.LastKnownVMIUID, readiness.VMIUID)
	inst.Status.RestartCount++ // observability only
	inst.Status.LastKnownVMIUID = readiness.VMIUID

	// Chain arithmetic: each restart within crashLoopWindow of the previous
	// extends the chain; a longer quiet gap starts a new one.
	now := metav1.Now()
	if inst.Status.LastUnplannedRestartTime != nil &&
		now.Sub(inst.Status.LastUnplannedRestartTime.Time) < crashLoopWindow {
		inst.Status.RecentUnplannedRestarts++
	} else {
		inst.Status.RecentUnplannedRestarts = 1
	}
	inst.Status.LastUnplannedRestartTime = &now

	if inst.Status.RecentUnplannedRestarts < crashLoopThreshold {
		return StepResult{}, false // absorbed; gates/Degraded reflect the reboot
	}

	// Threshold reached: halt now, then park under CrashLoopHalted. If the halt
	// fails nothing is recorded — the whole detection re-runs next pass.
	if err := r.Harvester.StopVM(ctx, inst.Namespace, inst.Status.Resources.VMName); err != nil {
		log.FromContext(ctx).Error(err, "StopVM failed during crash-loop halt (will retry)")
		return transient(err), true
	}
	msg := fmt.Sprintf("VM crash loop: %d unplanned restarts, each within %s of the previous; VM halted, manual intervention required",
		inst.Status.RecentUnplannedRestarts, crashLoopWindow)
	r.Recorder.Eventf(inst, corev1.EventTypeWarning, string(dbaasv1.ReasonCrashLoopDetected), "%s", msg)
	setStepCond(inst, dbaasv1.ConditionCrashLoopHalted, metav1.ConditionTrue, dbaasv1.ReasonCrashLoopDetected, msg)
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, dbaasv1.ReasonCrashLoopDetected, msg)
	removeCondition(inst, dbaasv1.ConditionDegraded)
	return pendingAfter(dbaasv1.ReasonCrashLoopHalted, msg, crashLoopParkRequeue), true
}

// reportDegraded records a report-only degradation on a caught-up instance.
//
// ASSUMPTION (must hold for this design): our readiness probe is an Exec probe,
// which KubeVirt runs *inside the guest via the qemu-guest-agent*. So when
// AgentConnected=False the probe physically cannot execute, KubeVirt scores those
// attempts as failures, and Ready flips False after FailureThreshold. We therefore
// treat Ready as the single health signal and use AgentConnected / Running only to
// *attribute* a failure, never as separately-debounced signals. If a future
// KubeVirt version froze Ready stale-True (or "Unknown") on agent loss instead of
// failing the probe, this would under-report a pure guest-agent outage and would
// need an AgentConnected-based debounce of its own.
func (r *DBInstanceReconciler) reportDegraded(inst *dbaasv1.DBInstance, readiness harvester.VMIReadiness) {
	reason := dbaasv1.ReasonPostgresUnreachable
	msg := "PostgreSQL readiness probe failing; database not accepting connections"
	switch {
	case !readiness.Running:
		reason = dbaasv1.ReasonVMRestarting
		msg = "VMI not running; VM restarting or halted out-of-band"
	case !readiness.AgentConnected:
		reason = dbaasv1.ReasonGuestAgentDisconnected
		msg = "Guest agent disconnected; cannot run readiness probe — database health unknown"
	}
	// Emit a Warning only when entering Degraded or when the cause changes, not on
	// every pass: the condition carries the persistent signal, and spamming status
	// would defeat the DeepEqual write-skip and self-trigger reconciles.
	if !hasConditionReason(inst, dbaasv1.ConditionDegraded, reason) {
		r.Recorder.Eventf(inst, corev1.EventTypeWarning, string(reason), "%s", msg)
	}
	setStepCond(inst, dbaasv1.ConditionDegraded, metav1.ConditionTrue, reason, msg)
	setStepCond(inst, dbaasv1.ConditionDatabaseReady, metav1.ConditionFalse, reason, msg)
}
