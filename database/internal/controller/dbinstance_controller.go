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
	"strings"
	"time"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerpkg "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// Controller-side defaults for fields the user can leave blank on the
// DBInstance spec. Centralised here so phaseStorage, phaseVM, and
// immutableDrift can't drift apart over time. A change here should be
// rare and accompanied by docs/USAGE updates.
const (
	defaultOSImage     = "ubuntu-22.04-server-cloudimg-amd64.img"
	defaultStorageType = "longhorn"
	defaultMasterUser  = "dbadmin"
	defaultPort        = 5432

	// Liveness for phaseAvailable is report-only: the controller reacts to the
	// KubeVirt readiness probe's already-debounced Ready condition (tuned via
	// FailureThreshold/SuccessThreshold on the VM probe) and surfaces a Degraded
	// condition. It does NOT restart the VM on readiness failure — sustained
	// outages are an operator concern, and flap-rate detection belongs in
	// Prometheus alerting on the exporter metrics, not in this 60s loop. The
	// only controller-initiated halt is the crash-loop guard below.

	// Crash-loop detection for unplanned restarts (KI-006 Problem A). Under
	// RunStrategyAlways KubeVirt recreates the VMI on every guest exit, so a
	// crash-looping VM "recovers" forever on its own. A chain of unplanned
	// restarts (VMI UID changes), each within crashLoopWindow of the previous,
	// reaching crashLoopThreshold halts the VM and fails the instance.
	crashLoopThreshold = 3                // chained unplanned restarts before giving up
	crashLoopWindow    = 10 * time.Minute // max gap between restarts to extend the chain
)

// DBInstanceReconciler reconciles DBInstance CRDs.
// Each Reconcile call advances exactly one provisioning phase,
// updates the status, and requeues for the next phase.
type DBInstanceReconciler struct {
	client.Client
	Harvester harvester.ClientInterface
	Recorder  record.EventRecorder
	// MaxConcurrentReconciles bounds how many DBInstances reconcile in parallel.
	// Reconciles are serialized per object regardless, so raising this only adds
	// cross-instance parallelism (safe). <1 is treated as 1.
	MaxConcurrentReconciles int
}

// DBInstance CRD permissions.
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances/finalizers,verbs=update

// Harvester resources the reconciler creates and tears down on behalf of callers.
// list;watch added (alongside get;create;update;delete) so controller-runtime can
// run informers for Owns()/Watches() on these child types (PR1).
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups=subresources.kubevirt.io,resources=virtualmachines/start;virtualmachines/stop;virtualmachines/restart,verbs=update
// +kubebuilder:rbac:groups=cdi.kubevirt.io,resources=datavolumes,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=harvesterhci.io,resources=virtualmachineimages,verbs=get;list
// External references the controller never creates: preflight only validates they
// exist (read-only). The NAD is inline-declared by the VM, not created here.
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main entry point called by controller-runtime.
func (r *DBInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var inst dbaasv1.DBInstance
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil // ignore already deleted
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling", "name", inst.Name, "phase", inst.Status.ProvisioningPhase)

	// --- Handle deletion via finalizer ---
	if !inst.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&inst, dbaasv1.FinalizerName) {
			return r.reconcileDelete(ctx, &inst)
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&inst, dbaasv1.FinalizerName) {
		controllerutil.AddFinalizer(&inst, dbaasv1.FinalizerName)
		if err := r.Update(ctx, &inst); err != nil {
			return ctrl.Result{}, err
		}
		// Quesion : why we explicitly requeue after successfully adding finalizer? 2 Requeues qued now
		// 1. since resource was updated (implicit requeue by controller-runtime) and 2. explicit requeue here
		return ctrl.Result{Requeue: true}, nil
	}

	// --- Dispatch (PR5). Only two states stay on legacy handlers: parked-failed
	// (crash-loop recovery probing) and pure steady-state on a fully converged
	// generation (liveness / crash-loop detection / endpoint refresh) — both fold
	// into ensure steps in PR6. Everything else, including stopped instances and
	// ANY spec change on an Available instance (stop/start toggles, resizes,
	// drift), runs the bounded ensure-step runner: power and resize converge from
	// observed VM/VMI state, with no pre-dispatcher lifecycle branches. ---
	switch {
	case inst.Status.ProvisioningPhase == dbaasv1.PhaseFailed:
		return r.phaseFailed(ctx, &inst)
	case inst.Status.ProvisioningPhase == dbaasv1.PhaseAvailable &&
		inst.Generation == inst.Status.ObservedGeneration:
		return r.phaseAvailable(ctx, &inst)
	default:
		// Provisioning window ("", Pending, ..., MonitoringDeployed — any value
		// the runner re-derives from observed state), PhaseStopped (idles cold:
		// all steps Satisfied, no writes), and Available with a pending spec
		// change (generation != observedGeneration).
		return r.runProvisioning(ctx, &inst)
	}
}

// ============================================================
// Steady-state and lifecycle phases (legacy dispatch)
//
// Provisioning (network/storage/VM create/readiness/monitoring) moved to the
// bounded ensure-step runner — see ensure_steps.go and the ensure_*.go files.
// ============================================================

func (r *DBInstanceReconciler) phaseAvailable(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// Snapshot before any mutation so we can skip the kube-apiserver round-trip when nothing changed. This runs on every ~60 s requeue for every Available DBInstance.
	prev := inst.Status.DeepCopy()
	ns := inst.Namespace
	vmName := inst.Status.Resources.VMName

	inst.Status.Phase = dbaasv1.StatusAvailable
	inst.Status.ProvisioningPhase = dbaasv1.PhaseAvailable
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.Message = "Database instance is available"

	// On first entry to Available, scrub the ephemeral cloud-init Secret to prevent failed mount scenario
	// If disk removal fails we leave the secret in place and retry next reconcile.
	if ciName := inst.Status.Resources.CloudInitSecretName; ciName != "" {
		if removeErr := r.Harvester.RemoveCloudInitDisk(ctx, ns, vmName); removeErr != nil {
			log.FromContext(ctx).Error(removeErr, "failed to remove cloud-init disk from VM spec (will retry)", "vm", vmName)
		} else if delErr := r.Harvester.DeleteSecret(ctx, ns, ciName); delErr != nil {
			log.FromContext(ctx).Error(delErr, "failed to delete cloud-init secret (non-fatal)", "secret", ciName)
		} else {
			inst.Status.Resources.CloudInitSecretName = ""
		}
	}

	// Single VMI fetch used for both liveness monitoring and endpoint refresh.
	readiness, readinessErr := r.Harvester.GetVMIReadiness(ctx, ns, vmName)
	if readinessErr != nil {
		log.FromContext(ctx).Error(readinessErr, "GetVMIReadiness failed (non-fatal)")
	}

	// -------------------------------------------------------------------------
	// Liveness monitoring
	// -------------------------------------------------------------------------

	// UID check: detect unplanned restarts. A new UID means the VMI object was
	// deleted and recreated (QEMU crash, OS panic, RunStrategyAlways auto-recovery).
	// This is distinct from a live migration, which does not change the UID.(Evidence ?)
	if readiness.VMIUID != "" {
		if inst.Status.LastKnownVMIUID == "" {
			// First entry to phaseAvailable — snapshot the running VMI's UID.
			inst.Status.LastKnownVMIUID = readiness.VMIUID

		} else if inst.Status.LastKnownVMIUID != readiness.VMIUID {
			log.FromContext(ctx).Info("unplanned VMI restart detected", "oldUID", inst.Status.LastKnownVMIUID, "newUID", readiness.VMIUID)
			r.Recorder.Eventf(inst, corev1.EventTypeWarning, dbaasv1.ReasonVMRestarting, "Unplanned VMI restart detected (UID %s → %s)", inst.Status.LastKnownVMIUID, readiness.VMIUID)
			inst.Status.RestartCount++ //for observerability only , no-op
			inst.Status.LastKnownVMIUID = readiness.VMIUID

			// Crash-loop detection: chain-count unplanned restarts
			// Each one within crashLoopWindow of the previous extends the chain.
			// quiet gap longer than the window starts a new chain.
			now := metav1.Now()
			if inst.Status.LastUnplannedRestartTime != nil &&
				now.Sub(inst.Status.LastUnplannedRestartTime.Time) < crashLoopWindow {
				inst.Status.RecentUnplannedRestarts++
			} else {
				inst.Status.RecentUnplannedRestarts = 1
			}
			inst.Status.LastUnplannedRestartTime = &now

			if inst.Status.RecentUnplannedRestarts >= crashLoopThreshold {
				termMsg := fmt.Sprintf("VM crash loop: %d unplanned restarts, each within %s of the previous; VM halted, manual intervention required",
					inst.Status.RecentUnplannedRestarts, crashLoopWindow)
				// Halt the VM before declaring failed: under RunStrategyAlways KubeVirt keep restarting it forever.
				// If StopVM fails , return without no status update , retry with next reconcile.
				if err := r.Harvester.StopVM(ctx, ns, vmName); err != nil {
					log.FromContext(ctx).Error(err, "StopVM failed during crash-loop halt (will retry)")
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				}
				r.Recorder.Eventf(inst, corev1.EventTypeWarning, dbaasv1.ReasonCrashLoopDetected, "%s", termMsg)
				setCondition(inst, dbaasv1.ConditionFailed, metav1.ConditionTrue,
					dbaasv1.ReasonCrashLoopDetected, termMsg)
				removeCondition(inst, dbaasv1.ConditionDegraded)
				inst.Status.Phase = dbaasv1.StatusFailed
				inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
				inst.Status.Message = termMsg
				_ = r.statusUpdate(ctx, inst)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}

			inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
			inst.Status.Message = "Unplanned VM restart detected; waiting for readiness"
			_ = r.statusUpdate(ctx, inst)                           // go back to phaseVMCreated -> phaseWaitReady
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil // Do we need to requeueAfter ? or just watch ?
		}
	}

	// Liveness (report-only). The KubeVirt readiness probe (pg_isready via the
	// guest agent, FailureThreshold=12 @ 10s ≈ 2 min) already debounces transient
	// failures, so Ready is authoritative — no controller-side counting. We never
	// restart the VM here.
	//
	// ASSUMPTION (must hold for this design): our readiness probe is an Exec
	// probe, which KubeVirt runs *inside the guest via the qemu-guest-agent*.
	// So when AgentConnected=False the probe physically cannot execute, KubeVirt
	// scores those attempts as failures, and Ready flips False after
	// FailureThreshold. We therefore treat Ready as the single health signal and
	// use AgentConnected only to *attribute* a failure (agent fault vs DB fault),
	// never as a separately-debounced signal. If a future KubeVirt version froze
	// Ready stale-True (or "Unknown") on agent loss instead of failing the probe,
	// this block would under-report a pure guest-agent outage and would need an
	// AgentConnected-based debounce of its own. Verify on cluster: kill
	// qemu-guest-agent in a Ready VM and confirm .status.conditions[Ready] flips
	// False within ~FailureThreshold*PeriodSeconds.
	//
	// Skip entirely when the VMI fetch failed: a missing observation is not a
	// health signal, so we leave any existing Degraded condition untouched.
	if readinessErr == nil {
		if readiness.Ready {
			removeCondition(inst, dbaasv1.ConditionDegraded) // healthy — clear Degraded if it was set
		} else {
			reason := dbaasv1.ReasonPostgresUnreachable
			unhealthyMsg := "PostgreSQL readiness probe failing; database not accepting connections"
			if !readiness.AgentConnected {
				reason = dbaasv1.ReasonGuestAgentDisconnected
				unhealthyMsg = "Guest agent disconnected; cannot run readiness probe — database health unknown"
			}
			// Emit a Warning only when entering Degraded or when the cause changes,
			// not on every reconcile. The condition (and its LastTransitionTime)
			// carries the persistent signal; spamming events/status each pass would
			// also defeat the DeepEqual write-skip and self-trigger reconciles.
			if !hasConditionReason(inst, dbaasv1.ConditionDegraded, reason) {
				r.Recorder.Eventf(inst, corev1.EventTypeWarning, reason, "%s", unhealthyMsg)
			}
			setCondition(inst, dbaasv1.ConditionDegraded, metav1.ConditionTrue, reason, unhealthyMsg)
			inst.Status.Message = unhealthyMsg
		}
	}

	// -------------------------------------------------------------------------
	// Endpoint refresh and Prometheus monitoring
	// -------------------------------------------------------------------------

	// Re-check the data-net IP on every requeue — it can change after a VM
	// restart or live migration. update the Endpoint if it changed.
	if readiness.IP != "" && (inst.Status.Endpoint == nil || inst.Status.Endpoint.Address != readiness.IP) {
		port := specPort(inst.Spec.Port)
		dbName := inst.Spec.DBName
		if dbName == "" {
			dbName = inst.Name
		}
		inst.Status.Endpoint = &dbaasv1.Endpoint{
			Address: readiness.IP,
			Port:    port,
			JDBCURL: fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-ca", readiness.IP, port, dbName),
		}
		log.FromContext(ctx).Info("endpoint updated", "ip", readiness.IP)
	}

	if inst.Status.Endpoint != nil && inst.Status.Endpoint.Address != "" {
		// update the monitoring stack with the new endpoint IP if it changed. DeployMonitoring is idempotent and handles the case where the Service already exists, so we can call it on every Available reconcile to ensure the monitoring stack tracks the current endpoint.
		svcName, smName, grafanaURL, promTarget, err := r.Harvester.DeployMonitoring(ctx, inst.Name, ns, inst.Status.Endpoint.Address)
		if err != nil {
			log.FromContext(ctx).Error(err, "monitoring refresh failed (non-fatal)")
		} else {
			inst.Status.Resources.MetricsServiceName = svcName
			inst.Status.Resources.ServiceMonitor = smName
			inst.Status.GrafanaURL = grafanaURL
			inst.Status.PrometheusTarget = promTarget
		}
	}

	if equality.Semantic.DeepEqual(prev, &inst.Status) {
		// No status drift this cycle — skip the Update entirely.
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 60 * time.Second}, r.statusUpdate(ctx, inst)
}

// ============================================================
// Stop / Start / Modify / Delete
// ============================================================

// phaseFailed parks an instance that gave up (crash-loop halt, or a fatal
// provisioning/lifecycle error via fail()). Instead of dead-ending (RF-6), it
// probes the VMI every 30s: if an operator repairs the guest or starts the VM
// out-of-band and it comes back fully healthy, the instance clears Failed and
// re-enters the provisioning chain. A crash-loop-halted VM stays halted until
// that out-of-band start, by design. No status writes happen while still
// unhealthy, so the loop stays cold (30s requeues only).
func (r *DBInstanceReconciler) phaseFailed(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	readiness, err := r.Harvester.GetVMIReadiness(ctx, inst.Namespace, inst.Status.Resources.VMName)
	if err != nil || !readiness.Running || !readiness.Ready || !readiness.AgentConnected {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	r.Recorder.Eventf(inst, corev1.EventTypeNormal, dbaasv1.ReasonRecovered,
		"VM healthy again after failed state; re-entering provisioning chain")
	removeCondition(inst, dbaasv1.ConditionFailed)
	inst.Status.LastKnownVMIUID = "" // re-snapshot the recovered VMI on Available re-entry
	inst.Status.Phase = dbaasv1.StatusStarting
	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.Message = "VM healthy again; re-validating readiness"
	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
}

// immutableDrift returns a comma-separated list of immutable spec fields
// that have drifted from the snapshot recorded at create time, or "" if no
// drift exists. If the snapshot is missing (older instances created before
// the snapshot was introduced), drift is treated as zero so we don't break
// existing deployments.
func immutableDrift(inst *dbaasv1.DBInstance) string {
	a := inst.Status.AppliedSpec
	if a == nil {
		return ""
	}
	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = defaultOSImage
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaultMasterUser
	}
	port := specPort(inst.Spec.Port)
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaultStorageType
	}

	appliedOSImage := a.OSImage
	if appliedOSImage == "" {
		appliedOSImage = defaultOSImage
	}
	appliedDBName := a.DBName
	if appliedDBName == "" {
		appliedDBName = inst.Name
	}
	appliedMasterUser := a.MasterUsername
	if appliedMasterUser == "" {
		appliedMasterUser = defaultMasterUser
	}
	appliedPort := a.Port
	if appliedPort == 0 {
		appliedPort = 5432
	}
	appliedStorageType := a.StorageType
	if appliedStorageType == "" {
		appliedStorageType = defaultStorageType
	}

	var changed []string
	if a.NetworkRef != inst.Spec.NetworkRef {
		changed = append(changed, "networkRef")
	}
	if appliedOSImage != osImage {
		changed = append(changed, "osImage")
	}
	if appliedDBName != dbName {
		changed = append(changed, "dbName")
	}
	if appliedMasterUser != masterUser {
		changed = append(changed, "masterUsername")
	}
	if a.EngineVersion != inst.Spec.EngineVersion {
		changed = append(changed, "engineVersion")
	}
	if appliedPort != port {
		changed = append(changed, "port")
	}
	if appliedStorageType != storageType {
		changed = append(changed, "storageType")
	}
	return strings.Join(changed, ",")
}

func (r *DBInstanceReconciler) reconcileDelete(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := inst.Namespace

	if inst.Spec.DeletionProtection {
		inst.Status.Message = "Cannot delete: DeletionProtection is enabled"
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{}, fmt.Errorf("deletion protection enabled")
	}

	inst.Status.Phase = dbaasv1.StatusDeleting
	inst.Status.Message = "Tearing down resources"
	_ = r.statusUpdate(ctx, inst)

	logger.Info("Tearing down child resources", "namespace", ns)
	if err := r.Harvester.TeardownAll(ctx, inst.Name, ns, inst.Status.Resources); err != nil {
		// Surface the failure on the CR and requeue. The finalizer stays
		// in place so a partial cleanup can't leave the CR garbage-collected
		// with live Harvester children behind it.
		inst.Status.Message = fmt.Sprintf("Teardown failed, will retry: %v", err)
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}
	// The tenant namespace is owned by the cluster operator (created during
	// onboarding) — never delete it. We only remove the resources we created.

	controllerutil.RemoveFinalizer(inst, dbaasv1.FinalizerName)
	return ctrl.Result{}, r.Update(ctx, inst)
}

// ============================================================
// Helpers
// ============================================================

func (r *DBInstanceReconciler) statusUpdate(ctx context.Context, inst *dbaasv1.DBInstance) error {
	desired := inst.Status.DeepCopy()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Re-fetch on every attempt to get a current resourceVersion.
		// The informer cache typically reflects writes within a few hundred ms;
		// DefaultRetry's backoff (10ms → 160ms, 5 attempts) covers that window.
		if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
			return err
		}
		inst.Status = *desired
		return r.Status().Update(ctx, inst)
	})
}

// specPort returns 5432 if port is 0, otherwise port.
func specPort(port int) int {
	if port == 0 {
		return defaultPort
	}
	return port
}

// setCondition adds or updates a condition in inst.Status.Conditions.
// LastTransitionTime is only bumped when Status changes.
func setCondition(inst *dbaasv1.DBInstance, condType string, status metav1.ConditionStatus, reason, msg string) {
	now := metav1.Now()
	for i, c := range inst.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				inst.Status.Conditions[i].LastTransitionTime = now
			}
			inst.Status.Conditions[i].Status = status
			inst.Status.Conditions[i].Reason = reason
			inst.Status.Conditions[i].Message = msg
			return
		}
	}
	inst.Status.Conditions = append(inst.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: now,
	})
}

// hasConditionReason reports whether a condition of condType is present, True,
// and carries the given reason. Used to emit Warning events only on a Degraded
// transition (entry or cause change) rather than on every reconcile.
func hasConditionReason(inst *dbaasv1.DBInstance, condType, reason string) bool {
	for _, c := range inst.Status.Conditions {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue && c.Reason == reason
		}
	}
	return false
}

// removeCondition removes a condition by type from inst.Status.Conditions.
func removeCondition(inst *dbaasv1.DBInstance, condType string) {
	for i, c := range inst.Status.Conditions {
		if c.Type == condType {
			inst.Status.Conditions = append(inst.Status.Conditions[:i], inst.Status.Conditions[i+1:]...)
			return
		}
	}
}

// SetupWithManager registers the reconciler with controller-runtime.
func (r *DBInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("dbaas-controller")

	maxConcurrent := r.MaxConcurrentReconciles
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBInstance{}).
		// Children the controller reconciles to desired state. Owner references
		// are wired in a later PR; until then these Owns watches are inert (no
		// owned object maps back), but registering the informers now keeps
		// SetupWithManager stable and is harmless (idempotent reconciles).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&kubevirtv1.VirtualMachine{}).
		Owns(&monitoringv1.ServiceMonitor{}).
		// VMIs are created by KubeVirt (owned by the VM, not the DBInstance), so
		// they are mapped by the dbaas instance label rather than via Owns(). This
		// makes liveness/endpoint refresh event-driven (UID/phase/Ready changes)
		// instead of relying solely on the periodic requeue.
		Watches(&kubevirtv1.VirtualMachineInstance{},
			handler.EnqueueRequestsFromMapFunc(mapVMIToInstance),
			builder.WithPredicates(vmiHealthChangedPredicate)).
		WithOptions(controllerpkg.Options{MaxConcurrentReconciles: maxConcurrent}).
		Named("dbinstance").
		Complete(r)
}
