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

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// DBSnapshotReconciler reconciles DBSnapshot CRDs.
//
// This is a scaffold: it observes generation only. Admission (§3.1 of the
// backup spec), snapshot-hold acquisition, Harvester VirtualMachineBackup
// creation, and protected deletion are added in a later phase — see
// yohan-docs/backups/harvester-vm-backup/dbaas-backup-implementation-plan.md.
type DBSnapshotReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dbaas.opencloud.wso2.com,resources=dbsnapshots/finalizers,verbs=update

// Reconcile is the main entry point called by controller-runtime.
func (r *DBSnapshotReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var snap dbaasv1.DBSnapshot
	if err := r.Get(ctx, req.NamespacedName, &snap); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil // ignore already deleted
		}
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling", "name", snap.Name, "sourceInstanceRef", snap.Spec.SourceInstanceRef.Name)

	if snap.Status.ObservedGeneration == snap.Generation {
		return ctrl.Result{}, nil
	}
	snap.Status.ObservedGeneration = snap.Generation
	if err := r.Status().Update(ctx, &snap); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DBSnapshotReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbaasv1.DBSnapshot{}).
		Named("dbsnapshot").
		Complete(r)
}
