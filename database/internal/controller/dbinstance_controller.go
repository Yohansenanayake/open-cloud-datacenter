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
// DBInstance spec. Centralised here so the ensure steps and immutableDrift
// can't drift apart over time. A change here should be rare and accompanied
// by docs/USAGE updates. (Crash-loop detection constants live with their
// logic in ensure_health.go.)
const (
	defaultOSImage     = "ubuntu-22.04-server-cloudimg-amd64.img"
	defaultStorageType = "longhorn"
	defaultMasterUser  = "dbadmin"
	defaultPort        = 5432
)

// DBInstanceReconciler reconciles DBInstance CRDs.
// Each Reconcile call advances exactly one provisioning phase,
// updates the status, and requeues for the next phase.
type DBInstanceReconciler struct {
	client.Client
	Harvester harvester.ClientInterface
	Recorder  record.EventRecorder
	// GrafanaBaseURL is the cluster Grafana base used to render per-instance
	// dashboard links in status (from the --grafana-url flag).
	GrafanaBaseURL string
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

	// --- Dispatch (PR6): everything runs the bounded ensure-step runner. Legacy
	// phaseAvailable/phaseFailed are gone — steady-state liveness, crash-loop
	// halt/park/recovery, and Degraded reporting live in ensureDatabaseHealth;
	// bootstrap cleanup in ensureBootstrapCleanup. Steady state is event-driven
	// off the VMI watch: an all-Satisfied pass writes nothing (DeepEqual skip)
	// and requeues nothing. ---
	return r.runProvisioning(ctx, &inst)
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
