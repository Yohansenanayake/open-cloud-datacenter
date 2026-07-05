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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ensurePreflight validates the spec references the runner cannot create on the
// user's behalf: the instance class must exist in InstanceClasses and
// spec.networkRef must be set (the controller never creates networks; the VM
// inline-declares the NAD). Both failures are Terminal — retrying the same spec
// cannot succeed — and park the instance until a spec edit re-runs the runner.
//
// NAD existence is not yet verified (no NAD type in the manager scheme); RBAC for
// get/list is already in place, so the check can be added once the scheme is.
func (r *DBInstanceReconciler) ensurePreflight(_ context.Context, inst *dbaasv1.DBInstance) StepResult {
	if inst.Status.Phase == "" {
		inst.Status.Phase = dbaasv1.StatusCreating
	}

	if _, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]; !ok {
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		setStepCond(inst, dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, "InvalidClass", msg)
		markProvisioningFailed(inst, "InvalidClass", msg)
		return terminal("InvalidClass", msg)
	}

	if inst.Spec.NetworkRef == "" {
		msg := "spec.networkRef is required (namespace/nad of an existing Multus NetworkAttachmentDefinition)"
		setStepCond(inst, dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, "NetworkRefMissing", msg)
		markProvisioningFailed(inst, "NetworkRefMissing", msg)
		return terminal("NetworkRefMissing", msg)
	}

	// Immutable drift: a spec edit to a field the controller cannot re-apply
	// (networkRef, dbName, osImage, ...) is refused loudly rather than silently
	// advancing observedGeneration. Guards every runner entry — provisioning,
	// stop/start toggles, and modifies alike (it sat on each legacy path before).
	if drift := immutableDrift(inst); drift != "" {
		msg := fmt.Sprintf("cannot modify immutable field(s) %s after create; revert the change or recreate the DBInstance", drift)
		setStepCond(inst, dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, "ImmutableFieldChanged", msg)
		markProvisioningFailed(inst, "ImmutableFieldChanged", msg)
		return terminal("ImmutableFieldChanged", msg)
	}

	// A previous pass parked Terminal and the user has since fixed the spec:
	// clear the failure so status reflects the recovery.
	if inst.Status.GetCondition(dbaasv1.ConditionFailed) != nil {
		removeCondition(inst, dbaasv1.ConditionFailed)
		if inst.Status.Phase == dbaasv1.StatusFailed {
			inst.Status.Phase = dbaasv1.StatusCreating
		}
	}

	inst.Status.Resources.NADName = inst.Spec.NetworkRef
	setStepCond(inst, dbaasv1.ConditionPreflightReady, metav1.ConditionTrue,
		"PreflightPassed", "instance class and network reference are valid")
	return satisfied()
}
