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
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
)

const preflightRequeue = 10 * time.Second

type preflightStep struct{ Dependencies }

func newPreflightStep(deps Dependencies) Step { return &preflightStep{Dependencies: deps} }

func (*preflightStep) Name() string { return "preflight" }

// ensurePreflight validates the spec references the runner cannot create on the
// user's behalf: the instance class must exist in InstanceClasses,
// spec.networkRef must be set, and the effective OS image must resolve. Invalid
// references are Terminal; an image that is still importing is Pending.
//
// NAD existence is not yet verified (no NAD type in the manager scheme); RBAC for
// get/list is already in place, so the check can be added once the scheme is.
func (r *preflightStep) Run(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	if _, ok := dbaasv1.InstanceClasses[inst.Spec.DBInstanceClass]; !ok {
		msg := fmt.Sprintf("unknown dbInstanceClass %q", inst.Spec.DBInstanceClass)
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonInvalidClass, msg)
		return Terminal(dbaasv1.ReasonInvalidClass, msg)
	}

	// Need to really check whether the NAD exists,
	if inst.Spec.NetworkRef == "" {
		msg := "spec.networkRef is required (namespace/nad of an existing Multus NetworkAttachmentDefinition)"
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonNetworkRefMissing, msg)
		return Terminal(dbaasv1.ReasonNetworkRefMissing, msg)
	}

	// Reject immutable edits first so an osImage change is reported as immutable
	// drift rather than an image lookup failure.
	if drift := immutableDrift(inst); drift != "" {
		msg := fmt.Sprintf("cannot modify immutable field(s) %s after create; revert the change or recreate the DBInstance", drift)
		inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse, dbaasv1.ReasonImmutableFieldChanged, msg)
		return Terminal(dbaasv1.ReasonImmutableFieldChanged, msg)
	}

	osImage := inst.Spec.OSImage
	if osImage == "" {
		osImage = defaultOSImage
	}
	if _, err := r.Harvester.ResolveVMImage(ctx, osImage); err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, harvester.ErrVMImageReferenceInvalid), errors.Is(err, harvester.ErrVMImageAmbiguous):
			inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse,
				dbaasv1.ReasonOSImageInvalid, msg)
			return Terminal(dbaasv1.ReasonOSImageInvalid, msg)
		case errors.Is(err, harvester.ErrVMImageNotFound):
			inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionFalse,
				dbaasv1.ReasonOSImageNotFound, msg)
			return Terminal(dbaasv1.ReasonOSImageNotFound, msg)
		case errors.Is(err, harvester.ErrVMImageNotReady):
			inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionUnknown,
				dbaasv1.ReasonOSImageNotReady, msg)
			return PendingAfter(dbaasv1.ReasonOSImageNotReady, msg, preflightRequeue)
		default:
			inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionUnknown,
				dbaasv1.ReasonValidationPending, "could not validate OS image")
			return Transient(err)
		}
	}

	inst.Status.Resources.NADName = inst.Spec.NetworkRef
	inst.SetCurrentCondition(dbaasv1.ConditionPreflightReady, metav1.ConditionTrue,
		dbaasv1.ReasonPreflightPassed, "instance class, network reference, and OS image are valid")
	return Satisfied()
}
