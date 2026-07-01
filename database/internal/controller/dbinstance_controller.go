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
	"os"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

// Controller-side defaults for fields the user can leave blank on the
// DBInstance spec. Centralised here so phaseStorage, phaseVM, and
// immutableDrift can't drift apart over time. A change here should be
// rare and accompanied by docs/USAGE updates.
const (
	defaultStorageType = "longhorn"

	// AnnotationRepaveTrigger is set to "now" by an operator to trigger an OS
	// disk swap on the next reconcile. The controller clears it after repave
	// completes to prevent re-triggering on the next reconcile.
	AnnotationRepaveTrigger = "dbaas.opencloud.wso2.com/repave-trigger"
	defaultMasterUser       = "dbadmin"
	defaultPort             = 5432

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
}

// DBInstance CRD permissions.
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbinstances/finalizers,verbs=update

// Harvester resources the reconciler creates and tears down on behalf of callers.
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;create;update;delete;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get
// +kubebuilder:rbac:groups=subresources.kubevirt.io,resources=virtualmachines/start;virtualmachines/stop;virtualmachines/restart,verbs=update
// +kubebuilder:rbac:groups=cdi.kubevirt.io,resources=datavolumes,verbs=get;create;update;delete;patch
// +kubebuilder:rbac:groups=harvesterhci.io,resources=virtualmachineimages,verbs=get;list
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;delete;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;create;update;delete
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

	// --- Handle stop/start ---
	// spec.running is set by users
	if inst.Spec.Running != nil && !*inst.Spec.Running && inst.Status.Phase == dbaasv1.StatusAvailable {
		return r.reconcileStop(ctx, &inst)
	}
	if inst.Spec.Running != nil && *inst.Spec.Running && inst.Status.Phase == dbaasv1.StatusStopped {
		return r.reconcileStart(ctx, &inst)
	}

	// --- Handle spec changes on available or in-progress-modifying instance ---
	if (inst.Status.Phase == dbaasv1.StatusAvailable || inst.Status.Phase == dbaasv1.StatusModifying) &&
		inst.Generation != inst.Status.ObservedGeneration {
		return r.reconcileModify(ctx, &inst)
	}

	// --- Repave trigger (checked before phase dispatch so it fires from Available) ---
	if inst.Annotations[AnnotationRepaveTrigger] == "now" &&
		inst.Status.ProvisioningPhase == dbaasv1.PhaseAvailable {
		return r.phaseRepave(ctx, &inst)
	}

	// --- Phase-based provisioning ---
	switch inst.Status.ProvisioningPhase {
	case "", dbaasv1.PhasePending:
		return r.phaseNetwork(ctx, &inst)
	case dbaasv1.PhaseNetworkProvisioned:
		return r.phaseStorage(ctx, &inst)
	case dbaasv1.PhaseStorageProvisioned:
		return r.phaseVM(ctx, &inst)
	// PhaseWaitingForCloudInit is no longer written (phaseWaitReady uses a single
	// PhaseVMCreated phase and per-gate messages), but is still matched here so
	// CRs created before that change still route to the wait handler.
	case dbaasv1.PhaseVMCreated, dbaasv1.PhaseWaitingForCloudInit:
		return r.phaseWaitReady(ctx, &inst)
	case dbaasv1.PhaseDatabaseReady:
		return r.phaseMonitoring(ctx, &inst)
	case dbaasv1.PhaseMonitoringDeployed:
		return r.phaseAvailable(ctx, &inst)
	case dbaasv1.PhaseAvailable:
		return r.phaseAvailable(ctx, &inst)
	case dbaasv1.PhaseStopped:
		return r.phaseStopped(ctx, &inst)
	case dbaasv1.PhaseFailed:
		return r.phaseFailed(ctx, &inst)
	default:
		return ctrl.Result{}, fmt.Errorf("unknown phase: %s", inst.Status.ProvisioningPhase)
	}
}

// ============================================================
// Provisioning phases
// ============================================================

func (r *DBInstanceReconciler) phaseNetwork(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// First entry: mark the instance as creating before doing any work.
	if inst.Status.Phase == "" {
		inst.Status.Phase = dbaasv1.StatusCreating
	}

	// Skip if already done.
	if inst.Status.Resources.NADName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseNetworkProvisioned
		return r.advance(ctx, inst)
	}

	// The controller doesn't create or own networks: spec.networkRef must
	// point at an existing Multus NAD (typically a Harvester VLAN network).
	if inst.Spec.NetworkRef == "" {
		return r.fail(ctx, inst, "NetworkRefMissing",
			fmt.Errorf("spec.networkRef is required (namespace/nad of an existing Multus NetworkAttachmentDefinition)"))
	}
	// No check for existence of the NAD; we should implement this
	inst.Status.Resources.NADName = inst.Spec.NetworkRef
	inst.Status.ProvisioningPhase = dbaasv1.PhaseNetworkProvisioned
	inst.Status.Message = fmt.Sprintf("Using network %s", inst.Spec.NetworkRef)

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseStorage(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// skip if already done
	if inst.Status.Resources.DataVolumeName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseStorageProvisioned
		return r.advance(ctx, inst)
	}

	id := inst.Name
	ns := inst.Namespace
	storageType := inst.Spec.StorageType //storage class of the data volume, should this be a spec ?
	if storageType == "" {
		storageType = defaultStorageType // longhorn or harvester-longhorn ?
	}

	dvName, err := r.Harvester.CreateDataVolume(ctx, id, ns, inst.Spec.AllocatedStorage, storageType)
	if err != nil {
		return r.fail(ctx, inst, "StorageFailed", err)
	}

	inst.Status.Resources.DataVolumeName = dvName
	inst.Status.ProvisioningPhase = dbaasv1.PhaseStorageProvisioned
	inst.Status.Message = "Encrypted storage provisioned" // Enctyption ? longhorn and harvester-longhorn storage class doesn't have volume encryption enabled by default.

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseVM(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Status.Resources.VMName != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
	}

	id := inst.Name
	ns := inst.Namespace

	classSpec, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		return r.fail(ctx, inst, "InvalidClass", fmt.Errorf("unknown class: %s", inst.Spec.DBInstanceClass))
	}

	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaultMasterUser
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = id
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaultStorageType
	}

	// Resolve image from baked catalog.
	osVersion := os.Getenv("BACKING_IMAGE_OS_VERSION")
	stream, ok := dbaasv1.LatestBakedImages[osVersion]
	if !ok || !stream.Validated {
		return r.fail(ctx, inst, "InvalidOSStream",
			fmt.Errorf("OS stream %q is not available or not validated; check BACKING_IMAGE_OS_VERSION env var", osVersion))
	}
	imageEntry := dbaasv1.BakedImages[stream.Revision]
	engineVersion := inst.Spec.EngineVersion
	if !slices.Contains(imageEntry.PGVersions, engineVersion) {
		return r.fail(ctx, inst, "UnsupportedEngineVersion",
			fmt.Errorf("engineVersion %q is not available in image revision %q (stream %q); supported: %v",
				engineVersion, stream.Revision, osVersion, imageEntry.PGVersions))
	}

	vmName, credSecretName, cloudInitSecretName, caCertPEM, err := r.Harvester.CreatePostgresVM(ctx, harvester.VMCreateParams{
		ID:                     id,
		Namespace:              ns,
		CPUCores:               classSpec.CPUCores,
		MemoryMB:               classSpec.MemoryMB,
		ImageName:              imageEntry.ImageName,
		EngineVersion:          engineVersion,
		DataVolumeRef:          inst.Status.Resources.DataVolumeName,
		DataVolumeSizeGB:       inst.Spec.AllocatedStorage,
		DataVolumeStorageClass: storageType,
		NADName:                inst.Status.Resources.NADName,
		MasterUser:             masterUser,
		DBName:                 dbName,
		Port:                   specPort(inst.Spec.Port),
		MaxConnections:         classSpec.MaxConnections,
		BackupEnabled:          inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:           inst.Spec.PreferredBackupWindow,
		S3Config:               inst.Spec.S3BackupConfig,
		VMPassword:             inst.Spec.VMPassword,
		StaticNetwork:          inst.Spec.StaticNetwork,
		DNSServerIP:            inst.Spec.DNSServerIP,
	})
	// Record resource refs even on partial failure: the names are deterministic
	// and returned regardless of err, so persisting them here lets the finalizer
	// (TeardownAll) clean up any Secret/VM created before the error instead of
	// leaking them. Deleting a not-yet-created name is a no-op (NotFound ignored).
	inst.Status.Resources.VMName = vmName
	inst.Status.Resources.SecretName = credSecretName
	inst.Status.Resources.CloudInitSecretName = cloudInitSecretName
	if err != nil {
		return r.fail(ctx, inst, "VMCreateFailed", err)
	}

	inst.Status.CACertPEM = caCertPEM
	inst.Status.MasterUserSecret = &dbaasv1.MasterUserSecretRef{
		Name:   credSecretName,
		Status: dbaasv1.SecretStatusActive,
	}
	// Snapshot the immutable fields as they were applied. reconcileModify
	// later refuses any spec change that drifts from this snapshot, so the
	// CR never reports observedGeneration=current for changes we silently
	// couldn't carry through to the running VM.
	inst.Status.AppliedSpec = &dbaasv1.AppliedSpec{
		NetworkRef:     inst.Spec.NetworkRef,
		DBName:         dbName,
		MasterUsername: masterUser,
		EngineVersion:  engineVersion,
		Port:           specPort(inst.Spec.Port),
		StorageType:    storageType,
		ImageRevision:  stream.Revision,
	}
	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.Message = "VM created, waiting for PostgreSQL to initialize"

	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
}

// phaseWaitReady is a single "waiting for the VM to become ready" phase: the
// instance stays in PhaseVMCreated throughout and the two readiness gates are
// distinguished only by status.Message, not by a separate provisioning phase.
// (PhaseWaitingForCloudInit is no longer written — it is still recognised by the
// dispatcher so any in-flight CR carrying the old value still routes here — and
// the name was misleading anyway: this phase is also re-entered on restarts,
// where cloud-init never re-runs.)
func (r *DBInstanceReconciler) phaseWaitReady(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Namespace

	// First gate: VMI is Running and the qemu-guest-agent has reported a data-net IP.
	readiness, err := r.Harvester.GetVMIReadiness(ctx, ns, inst.Status.Resources.VMName)
	if err != nil || !readiness.Running || readiness.IP == "" {
		inst.Status.Message = "VM booting; waiting for guest agent and data-net IP"
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	port := specPort(inst.Spec.Port)
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}

	// Second gate: KubeVirt's VMI readiness probe (pg_isready via QGA virtio
	// channel) has passed. The probe is defined on the VM spec and runs inside
	// the guest — no pod-network port exposure required.
	if !readiness.Ready {
		inst.Status.Message = fmt.Sprintf("PostgreSQL initializing; readiness probe not passing at %s:%d", readiness.IP, port)
		_ = r.statusUpdate(ctx, inst)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	inst.Status.Endpoint = &dbaasv1.Endpoint{
		Address: readiness.IP,
		Port:    port,
		JDBCURL: fmt.Sprintf("jdbc:postgresql://%s:%d/%s?ssl=true&sslmode=verify-ca", readiness.IP, port, dbName),
	}
	inst.Status.ProvisioningPhase = dbaasv1.PhaseDatabaseReady
	inst.Status.Message = "PostgreSQL is ready"

	return r.advance(ctx, inst)
}

func (r *DBInstanceReconciler) phaseMonitoring(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Status.Resources.ServiceMonitor != "" {
		inst.Status.ProvisioningPhase = dbaasv1.PhaseMonitoringDeployed
		return r.advance(ctx, inst)
	}

	id := inst.Name
	ns := inst.Namespace

	if inst.Status.Endpoint == nil || inst.Status.Endpoint.Address == "" {
		inst.Status.Message = "Waiting for database endpoint before monitoring setup"
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
	}

	svcName, smName, grafanaURL, promTarget, err := r.Harvester.DeployMonitoring(ctx, id, ns, inst.Status.Endpoint.Address)
	if err != nil {
		// Non-fatal — DB works without monitoring. Track the Service name
		// regardless: DeployMonitoring creates the Service first, so a
		// partial failure may leave the Service behind for the finalizer
		// to clean up.
		log.FromContext(ctx).Error(err, "monitoring setup failed (non-fatal)")
		inst.Status.Message = "Available (monitoring setup failed, will retry)"
		inst.Status.Resources.MetricsServiceName = svcName
	} else {
		inst.Status.Resources.ServiceMonitor = smName
		inst.Status.Resources.MetricsServiceName = svcName
		inst.Status.GrafanaURL = grafanaURL
		inst.Status.PrometheusTarget = promTarget
	}

	inst.Status.ProvisioningPhase = dbaasv1.PhaseMonitoringDeployed
	return r.advance(ctx, inst)
}

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
			_ = r.statusUpdate(ctx, inst) // go back to phaseVMCreated -> phaseWaitReady
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
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

	// -------------------------------------------------------------------------
	// Baked image drift detection
	// -------------------------------------------------------------------------
	// When the applied revision differs from the current stream revision a new
	// image has been published. There are two distinct cases:
	//   OSUpdateAvailable — engineVersion is still in the new image → safe repave
	//   PGVersionEOL      — engineVersion was removed from the new image → customer
	//                       must migrate data before repaving
	osVersion := os.Getenv("BACKING_IMAGE_OS_VERSION")
	if stream, ok := dbaasv1.LatestBakedImages[osVersion]; ok && stream.Validated && inst.Status.AppliedSpec != nil {
		if inst.Status.AppliedSpec.ImageRevision != stream.Revision {
			appliedEntry := dbaasv1.BakedImages[inst.Status.AppliedSpec.ImageRevision]
			currentEntry := dbaasv1.BakedImages[stream.Revision]
			if slices.Contains(currentEntry.PGVersions, inst.Spec.EngineVersion) {
				// PG version present in new image — repave is safe.
				setCondition(inst, dbaasv1.ConditionOSUpdateAvailable, metav1.ConditionTrue,
					"ImageRevisionDrift",
					fmt.Sprintf("VM is on image %q (OS %s); new image %q (OS %s) available — annotate with %s=now to repave",
						inst.Status.AppliedSpec.ImageRevision, appliedEntry.OSVersion,
						stream.Revision, currentEntry.OSVersion, AnnotationRepaveTrigger))
				removeCondition(inst, dbaasv1.ConditionPGVersionEOL)
			} else {
				// PG version removed from new image — customer must migrate data first.
				setCondition(inst, dbaasv1.ConditionPGVersionEOL, metav1.ConditionTrue,
					"PGVersionRemovedFromCatalog",
					fmt.Sprintf("engineVersion %q is not available in new image %q (available: %v) — "+
						"create a new DBInstance with a supported version and migrate data before repaving",
						inst.Spec.EngineVersion, stream.Revision, currentEntry.PGVersions))
				removeCondition(inst, dbaasv1.ConditionOSUpdateAvailable)
			}
		} else {
			removeCondition(inst, dbaasv1.ConditionOSUpdateAvailable)
			removeCondition(inst, dbaasv1.ConditionPGVersionEOL)
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

func (r *DBInstanceReconciler) reconcileStop(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// A user who flips spec.running and also edits an immutable field in
	// the same kubectl apply hits this path before reconcileModify (the
	// dispatcher routes running-toggles first). Without this guard,
	// observedGeneration silently catches up — same lie reconcileModify
	// used to tell. Refuse the whole change with a clear message.
	if drift := immutableDrift(inst); drift != "" {
		return r.fail(ctx, inst, "ImmutableFieldChanged",
			fmt.Errorf("cannot modify field(s) %s while stopping; revert or recreate the DBInstance", drift))
	}

	if err := r.Harvester.StopVM(ctx, inst.Namespace, inst.Status.Resources.VMName); err != nil {
		return r.fail(ctx, inst, "StopFailed", err)
	}

	// Single atomic status write — no persistent phase=stopping intermediate.
	// KI-005: a stale "stopping" snapshot in statusUpdate's retry loop could
	// overwrite "stopped" and leave the dispatcher with no matching branch.
	// ProvisioningPhase=Stopped moves the instance out of phaseAvailable's
	// dispatch domain, so the RF-1 resurrection loop cannot fire.
	inst.Status.Phase = dbaasv1.StatusStopped
	inst.Status.ProvisioningPhase = dbaasv1.PhaseStopped
	inst.Status.Message = "Stopped. Storage preserved."
	inst.Status.ObservedGeneration = inst.Generation
	removeCondition(inst, dbaasv1.ConditionDegraded)
	return ctrl.Result{}, r.statusUpdate(ctx, inst)
}

// phaseStopped is the idle state for a halted instance. The only exit is
// spec.running flipping back to true, which re-enters the provisioning chain
// via reconcileStart. No requeue while halted — spec edits trigger
// event-driven reconciles.
func (r *DBInstanceReconciler) phaseStopped(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	if inst.Spec.Running == nil || *inst.Spec.Running {
		return r.reconcileStart(ctx, inst)
	}
	return ctrl.Result{}, nil
}

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

// phaseRepave swaps the OS disk for the latest baked image while leaving the
// pgdata disk untouched. Triggered by the repave-trigger=now annotation.
//
// Step 0: Clear pgdata ownerRef (safety — prevents cascade delete if VM CR is patched)
// Step 1: Stop VM, wait for VMI to disappear
// Step 2: Delete old OS disk DataVolume
// Step 3: Resolve new image from catalog
// Step 4: Patch VM spec with new OS disk DataVolumeTemplate
// Step 5: Start VM → re-enter phaseWaitReady
// Step 7: imageRevision + conditions updated in phaseAvailable after ready
func (r *DBInstanceReconciler) phaseRepave(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Namespace
	vmName := inst.Status.Resources.VMName
	osDVName := fmt.Sprintf("pg-%s-os", inst.Name)
	pgdataDVName := inst.Status.Resources.DataVolumeName

	inst.Status.Phase = dbaasv1.StatusModifying

	// Step 0: protect pgdata from cascade deletion.
	if err := r.Harvester.ClearDataVolumeOwnerRef(ctx, ns, pgdataDVName); err != nil {
		return r.fail(ctx, inst, "RepavePgdataProtectFailed",
			fmt.Errorf("could not clear pgdata ownerRef before repave: %w", err))
	}

	// Step 1: stop VM; wait for VMI to disappear before touching disks.
	if err := r.Harvester.StopVM(ctx, ns, vmName); err != nil {
		return r.fail(ctx, inst, "RepaveStopFailed", err)
	}
	readiness, vmiErr := r.Harvester.GetVMIReadiness(ctx, ns, vmName)
	if vmiErr != nil && !errors.IsNotFound(vmiErr) {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if readiness.Running {
		inst.Status.Message = "Repave: waiting for VM to stop"
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.statusUpdate(ctx, inst)
	}

	// Step 2: delete old OS disk; pgdata is already ownerRef-free so it is safe.
	if err := r.Harvester.DeleteDataVolume(ctx, ns, osDVName); err != nil {
		return r.fail(ctx, inst, "RepaveDeleteOSDiskFailed", err)
	}

	// Step 3: resolve new image from catalog and verify the VM's engineVersion
	// is still present. If the PG version was removed (EOL) the repave must be
	// blocked — the data directory format is version-specific and pg_createcluster
	// would fail on boot if the requested version is not in the new image.
	osVersion := os.Getenv("BACKING_IMAGE_OS_VERSION")
	stream, ok := dbaasv1.LatestBakedImages[osVersion]
	if !ok || !stream.Validated {
		return r.fail(ctx, inst, "InvalidOSStream",
			fmt.Errorf("OS stream %q is not available or not validated", osVersion))
	}
	entry := dbaasv1.BakedImages[stream.Revision]
	if !slices.Contains(entry.PGVersions, inst.Spec.EngineVersion) {
		return r.fail(ctx, inst, "RepaveBlockedPGVersionEOL",
			fmt.Errorf("engineVersion %q is not available in new image %q (available: %v) — "+
				"migrate data to a supported version before repaving",
				inst.Spec.EngineVersion, stream.Revision, entry.PGVersions))
	}

	// Step 4: patch VM spec with new OS disk DataVolumeTemplate.
	// SwapVMOSDisk resolves the image by name or displayName and reads
	// storageClassName from status — works with both kubectl-named and
	// UI-uploaded (auto-named) VirtualMachineImages.
	if err := r.Harvester.SwapVMOSDisk(ctx, ns, vmName, inst.Name, entry.ImageName); err != nil {
		return r.fail(ctx, inst, "RepaveSwapOSDiskFailed", err)
	}

	classSpec, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		return r.fail(ctx, inst, "InvalidClass", fmt.Errorf("unknown class: %s", inst.Spec.DBInstanceClass))
	}
	masterUser := inst.Spec.MasterUsername
	if masterUser == "" {
		masterUser = defaultMasterUser
	}
	dbName := inst.Spec.DBName
	if dbName == "" {
		dbName = inst.Name
	}
	storageType := inst.Spec.StorageType
	if storageType == "" {
		storageType = defaultStorageType
	}
	credSecretName := inst.Status.Resources.SecretName
	if credSecretName == "" {
		credSecretName = fmt.Sprintf("pg-%s-credentials", inst.Name)
	}
	cloudInitSecretName := fmt.Sprintf("pg-%s-cloudinit", inst.Name)
	if err := r.Harvester.PrepareCloudInitForRepave(ctx, harvester.VMCreateParams{
		ID:                     inst.Name,
		Namespace:              ns,
		CPUCores:               classSpec.CPUCores,
		MemoryMB:               classSpec.MemoryMB,
		ImageName:              entry.ImageName,
		EngineVersion:          inst.Spec.EngineVersion,
		DataVolumeRef:          inst.Status.Resources.DataVolumeName,
		DataVolumeSizeGB:       inst.Spec.AllocatedStorage,
		DataVolumeStorageClass: storageType,
		NADName:                inst.Status.Resources.NADName,
		MasterUser:             masterUser,
		DBName:                 dbName,
		Port:                   specPort(inst.Spec.Port),
		MaxConnections:         classSpec.MaxConnections,
		BackupEnabled:          inst.Spec.BackupRetentionPeriod > 0,
		BackupWindow:           inst.Spec.PreferredBackupWindow,
		S3Config:               inst.Spec.S3BackupConfig,
		VMPassword:             inst.Spec.VMPassword,
		StaticNetwork:          inst.Spec.StaticNetwork,
		DNSServerIP:            inst.Spec.DNSServerIP,
	}, vmName, credSecretName, cloudInitSecretName); err != nil {
		return r.fail(ctx, inst, "RepaveCloudInitFailed", err)
	}

	// Step 5: start VM and re-enter the wait-ready chain.
	if err := r.Harvester.StartVM(ctx, ns, vmName); err != nil {
		return r.fail(ctx, inst, "RepaveStartFailed", err)
	}

	// Update imageRevision and clear annotation + OSUpdateAvailable.
	if inst.Status.AppliedSpec != nil {
		inst.Status.AppliedSpec.ImageRevision = stream.Revision
	}
	removeCondition(inst, dbaasv1.ConditionOSUpdateAvailable)
	if inst.Annotations != nil {
		patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"dbaas.opencloud.wso2.com/repave-trigger":null}}}`))
		if err := r.Patch(ctx, inst, patch); err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
	}
	inst.Status.Resources.CloudInitSecretName = cloudInitSecretName

	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.LastKnownVMIUID = ""
	inst.Status.Message = fmt.Sprintf("Repave in progress: OS disk swapped to %s, waiting for VM ready", stream.Revision)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) reconcileStart(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	// See reconcileStop above — same drift guard for the start path.
	if drift := immutableDrift(inst); drift != "" {
		return r.fail(ctx, inst, "ImmutableFieldChanged",
			fmt.Errorf("cannot modify field(s) %s while starting; revert or recreate the DBInstance", drift))
	}

	ns := inst.Namespace
	vmName := inst.Status.Resources.VMName

	// StopVM only sets RunStrategy=Halted; the VMI tears down asynchronously
	// and KubeVirt's start subresource rejects calls while a VMI object still
	// exists. Wait for teardown to finish (same guard as reconcileModify).
	// Phase stays stopped/Stopped so the dispatcher re-enters here.
	readiness, vmiErr := r.Harvester.GetVMIReadiness(ctx, ns, vmName)
	if vmiErr != nil && !errors.IsNotFound(vmiErr) {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if readiness.Running {
		inst.Status.Message = "Waiting for VM to finish stopping before start"
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.statusUpdate(ctx, inst)
	}

	if err := r.Harvester.StartVM(ctx, ns, vmName); err != nil {
		return r.fail(ctx, inst, "StartFailed", err)
	}

	// Re-enter the provisioning chain instead of declaring available here:
	// phaseWaitReady gates on VMI readiness + the PostgreSQL probe, and
	// phaseAvailable stamps phase=available only once the DB is actually up.
	inst.Status.Phase = dbaasv1.StatusStarting
	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.LastKnownVMIUID = "" // planned start — don't count it as an unplanned restart
	inst.Status.Message = "Starting; waiting for VM to become ready"
	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.statusUpdate(ctx, inst)
}

func (r *DBInstanceReconciler) reconcileModify(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	ns := inst.Namespace

	// Refuse the modify if any field we can't re-apply has drifted from the
	// snapshot taken at create time. Without this guard we silently set
	// observedGeneration = generation even when a user changed networkRef
	// or dbName, leaving them to believe the controller honoured it.
	if drift := immutableDrift(inst); drift != "" {
		return r.fail(ctx, inst, "ImmutableFieldChanged",
			fmt.Errorf("cannot modify field(s) %s after create; revert or recreate the DBInstance", drift))
	}

	inst.Status.Phase = dbaasv1.StatusModifying
	_ = r.statusUpdate(ctx, inst)

	classSpec, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]
	if !ok {
		return r.fail(ctx, inst, "InvalidClass", fmt.Errorf("unknown class: %s", inst.Spec.DBInstanceClass))
	}

	vmName := inst.Status.Resources.VMName

	// Cold resize: ensure VM is halted (idempotent — safe to call on an already-halted VM).
	if err := r.Harvester.StopVM(ctx, ns, vmName); err != nil {
		return r.fail(ctx, inst, "ResizeStopFailed", err)
	}

	// Wait for the VMI to actually terminate before applying spec changes.
	// Setting RunStrategy=Halted is asynchronous: the VMI can take 5–30s to
	// stop. KubeVirt's start subresource rejects calls while a VMI object
	// exists. StopVM is idempotent so re-calling on each re-queue is safe.
	readiness, vmiErr := r.Harvester.GetVMIReadiness(ctx, ns, vmName)
	if vmiErr != nil && !errors.IsNotFound(vmiErr) {
		// Transient API error — re-queue without failing.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if readiness.Running {
		inst.Status.Phase = dbaasv1.StatusModifying
		inst.Status.Message = fmt.Sprintf("Waiting for VM to stop before resize to %s", inst.Spec.DBInstanceClass)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.statusUpdate(ctx, inst)
	}

	if err := r.Harvester.ResizeVM(ctx, ns, vmName, classSpec.CPUCores, classSpec.MemoryMB); err != nil {
		return r.fail(ctx, inst, "ResizeVMFailed", fmt.Errorf("VM is halted; %w", err))
	}
	if err := r.Harvester.ResizeDataVolume(ctx, ns, vmName, inst.Status.Resources.DataVolumeName, inst.Spec.AllocatedStorage); err != nil {
		return r.fail(ctx, inst, "ResizeStorageFailed", fmt.Errorf("VM is halted; %w", err))
	}

	if err := r.Harvester.StartVM(ctx, ns, vmName); err != nil {
		return r.fail(ctx, inst, "ResizeStartFailed", fmt.Errorf("resize applied but VM failed to start; %w", err))
	}

	// Wait for the VM to become ready via the normal provisioning path.
	inst.Status.ObservedGeneration = inst.Generation
	inst.Status.LastKnownVMIUID = ""
	inst.Status.ProvisioningPhase = dbaasv1.PhaseVMCreated
	inst.Status.Message = fmt.Sprintf("Resized to %s, %dGiB; waiting for VM", inst.Spec.DBInstanceClass, inst.Spec.AllocatedStorage)
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

func (r *DBInstanceReconciler) advance(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, inst) // Requeue : true is depreceated need to decide on timing.
}

func (r *DBInstanceReconciler) fail(ctx context.Context, inst *dbaasv1.DBInstance, reason string, err error) (ctrl.Result, error) {
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.Message = fmt.Sprintf("%s: %v", reason, err)
	_ = r.statusUpdate(ctx, inst)
	// Return zero Result: controller-runtime ignores Result when error is
	// non-nil and applies exponential backoff requeue instead.
	return ctrl.Result{}, err
}

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
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBInstance{}).
		Named("dbinstance").
		Complete(r)
}
