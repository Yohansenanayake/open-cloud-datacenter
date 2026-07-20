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
	goerrors "errors"
	"fmt"
	"strings"

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
	// OperatorNamespace holds the two controller-private Secrets (internal DB
	// credentials, TLS) — outside every tenant namespace. From the
	// --operator-namespace flag (default POD_NAMESPACE env, fallback
	// dbaas-system — see operatorNamespace()).
	OperatorNamespace string
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
// run informers for Owns()/Watches() on these child types.
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

	logger.Info("Reconciling", "name", inst.Name, "phase", inst.Status.Phase)

	// --- Handle deletion via finalizer ---
	if !inst.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&inst, dbaasv1.FinalizerName) {
			return r.reconcileDelete(ctx, &inst)
		}
		return ctrl.Result{}, nil
	}

	// Every DBInstance, in every state, runs the same bounded ensure-step
	// runner — there's no separate dispatch for provisioning vs. steady-state
	// vs. crash-loop-parked, including finalizer add: ensureFinalizer (first
	// in the chain) handles that on the very first pass. Steady-state
	// liveness, crash-loop halt/park/recovery, and Degraded reporting live in
	// ensureDatabaseHealth; bootstrap cleanup in ensureBootstrapCleanup.
	// Steady state is event-driven off the VMI watch: an all-Satisfied pass
	// writes nothing (DeepEqual skip) and requeues nothing.
	return r.reconcileInstance(ctx, &inst)
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
		appliedPort = defaultPort
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
	port := specPort(inst.Spec.Port)
	if appliedPort != port {
		changed = append(changed, "port")
	}
	if appliedStorageType != storageType {
		changed = append(changed, "storageType")
	}
	if a.VMPassword != inst.Spec.VMPassword {
		changed = append(changed, "vmPassword")
	}
	if !equality.Semantic.DeepEqual(a.StaticNetwork, inst.Spec.StaticNetwork) {
		changed = append(changed, "staticNetwork")
	}
	return strings.Join(changed, ",")
}

func (r *DBInstanceReconciler) reconcileDelete(ctx context.Context, inst *dbaasv1.DBInstance) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := inst.Namespace
	// Single snapshot reused across every patchStatusIfChanged call below; the
	// helper re-fetches and retries conflicts for each attempted status write.
	original := inst.DeepCopy()

	if inst.Spec.DeletionProtection {
		setStepCond(inst, dbaasv1.ConditionDeletionBlocked, metav1.ConditionTrue,
			dbaasv1.ReasonDeletionProtected, "Cannot delete: DeletionProtection is enabled")
		r.finalizeStatus(inst)
		if err := r.patchStatusIfChanged(ctx, original, inst); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	setStepCond(inst, dbaasv1.ConditionDeletionBlocked, metav1.ConditionFalse,
		dbaasv1.ReasonDeletionProgressing, "Tearing down resources")
	r.finalizeStatus(inst)
	if err := r.patchStatusIfChanged(ctx, original, inst); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Tearing down child resources", "namespace", ns)
	if err := r.Harvester.TeardownAll(ctx, inst.Name, ns, inst.Status.Resources); err != nil {
		setStepCond(inst, dbaasv1.ConditionDeletionBlocked, metav1.ConditionTrue,
			dbaasv1.ReasonTeardownFailed, fmt.Sprintf("Teardown failed, will retry: %v", err))
		r.finalizeStatus(inst)
		return ctrl.Result{}, goerrors.Join(err, r.patchStatusIfChanged(ctx, original, inst))
	}

	if err := r.deleteOperatorSecrets(ctx, inst); err != nil {
		setStepCond(inst, dbaasv1.ConditionDeletionBlocked, metav1.ConditionTrue,
			dbaasv1.ReasonOperatorSecretCleanupFailed, fmt.Sprintf("Operator-namespace cleanup failed, will retry: %v", err))
		r.finalizeStatus(inst)
		return ctrl.Result{}, goerrors.Join(err, r.patchStatusIfChanged(ctx, original, inst))
	}

	return ctrl.Result{}, r.removeDBInstanceFinalizer(ctx, client.ObjectKeyFromObject(inst))
}

// removeDBInstanceFinalizer re-fetches on every retry so the full-object update
// never combines a fresh resourceVersion with stale spec or metadata.
func (r *DBInstanceReconciler) removeDBInstanceFinalizer(ctx context.Context, key client.ObjectKey) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dbaasv1.DBInstance{}
		if err := r.Get(ctx, key, latest); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !controllerutil.ContainsFinalizer(latest, dbaasv1.FinalizerName) {
			return nil
		}
		controllerutil.RemoveFinalizer(latest, dbaasv1.FinalizerName)
		return r.Update(ctx, latest)
	})
}

// deleteOperatorSecrets removes the two controller-private, cross-namespace
// Secrets. It deletes by the recorded ref first, then sweeps the operator
// namespace by the DBInstance-UID label as a backstop for refs lost to a
// status reset or created before the ref was recorded — the label is the
// only durable link once status is gone.
func (r *DBInstanceReconciler) deleteOperatorSecrets(ctx context.Context, inst *dbaasv1.DBInstance) error {
	var errs []error

	deleteRef := func(ref string) {
		ns, name, ok := strings.Cut(ref, "/")
		if !ok || name == "" {
			return // If namesapce/name reference is malformed, skip deletion.
		}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
		if err := r.Delete(ctx, sec); err != nil && !errors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	deleteRef(inst.Status.Resources.InternalSecretRef)
	deleteRef(inst.Status.Resources.PrivateTLSSecretRef)

	var list corev1.SecretList
	if err := r.List(ctx, &list, client.InNamespace(r.operatorNamespace()),
		client.MatchingLabels{dbaasv1.LabelDBInstanceUID: string(inst.UID)},
	); err != nil {
		errs = append(errs, err)
	} else {
		for i := range list.Items {
			if err := r.Delete(ctx, &list.Items[i]); err != nil && !errors.IsNotFound(err) {
				errs = append(errs, err)
			}
		}
	}
	return goerrors.Join(errs...)
}

// ============================================================
// Helpers
// ============================================================

// specPort returns 5432 if port is 0, otherwise port.
func specPort(port int) int {
	if port == 0 {
		return defaultPort
	}
	return port
}

// operatorNamespace returns the configured operator namespace, defaulting to
// "dbaas-system" so tests and any deployment that omits the flag still work.
func (r *DBInstanceReconciler) operatorNamespace() string {
	if r.OperatorNamespace == "" {
		return "dbaas-system"
	}
	return r.OperatorNamespace
}

// hasConditionReason reports whether a condition of condType is present, True,
// and carries the given reason. Used to emit Warning events only on a Degraded
// transition (entry or cause change) rather than on every reconcile.
func hasConditionReason(inst *dbaasv1.DBInstance, condType string, reason dbaasv1.ConditionReason) bool {
	for _, c := range inst.Status.Conditions {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue && c.Reason == string(reason)
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
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Endpoints{}).
		Owns(&kubevirtv1.VirtualMachine{}).
		Owns(&monitoringv1.ServiceMonitor{}).
		Watches(&kubevirtv1.VirtualMachineInstance{},
			handler.EnqueueRequestsFromMapFunc(mapVMIToInstance),
			builder.WithPredicates(vmiHealthChangedPredicate)).
		WithOptions(controllerpkg.Options{MaxConcurrentReconciles: maxConcurrent}).
		Named("dbinstance").
		Complete(r)
}
