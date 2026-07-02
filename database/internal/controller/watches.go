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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// mapVMIToInstance maps a VirtualMachineInstance event to a reconcile request for
// the owning DBInstance. VMIs are created by KubeVirt (owned by the VM, not the
// DBInstance), so they cannot be wired via Owns(); we map by the dbaas instance
// label the VM template stamps onto the VMI. That label value is the DBInstance
// name (see buildPostgresVM's VirtualMachineInstanceTemplateLabels).
func mapVMIToInstance(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[dbaasv1.LabelInstance]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name},
	}}
}

// vmiHealthChangedPredicate limits VMI-driven reconciles to changes that affect
// liveness/readiness: an unplanned restart (UID change), a phase change, or a flip
// of the Ready / AgentConnected conditions. Without it, every VMI status heartbeat
// would enqueue a (no-op) reconcile.
var vmiHealthChangedPredicate = predicate.Funcs{
	CreateFunc: func(event.CreateEvent) bool { return true },
	DeleteFunc: func(event.DeleteEvent) bool { return true },
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldVMI, ok1 := e.ObjectOld.(*kubevirtv1.VirtualMachineInstance)
		newVMI, ok2 := e.ObjectNew.(*kubevirtv1.VirtualMachineInstance)
		if !ok1 || !ok2 {
			return true // unexpected type: reconcile rather than silently drop
		}
		if oldVMI.UID != newVMI.UID || oldVMI.Status.Phase != newVMI.Status.Phase {
			return true
		}
		return vmiConditionTrue(oldVMI, kubevirtv1.VirtualMachineInstanceReady) != vmiConditionTrue(newVMI, kubevirtv1.VirtualMachineInstanceReady) ||
			vmiConditionTrue(oldVMI, kubevirtv1.VirtualMachineInstanceAgentConnected) != vmiConditionTrue(newVMI, kubevirtv1.VirtualMachineInstanceAgentConnected)
	},
}

// vmiConditionTrue reports whether the named VMI condition is currently True.
func vmiConditionTrue(vmi *kubevirtv1.VirtualMachineInstance, t kubevirtv1.VirtualMachineInstanceConditionType) bool {
	for _, c := range vmi.Status.Conditions {
		if c.Type == t {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
