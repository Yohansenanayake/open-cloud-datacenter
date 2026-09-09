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
	"testing"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func seedGuestState(t *testing.T, r *testHarness, inst *dbaasv1.DBInstance) {
	t.Helper()
	name := harvester.GuestStateVolumeName(diskIdentifierFor(inst))
	mode := corev1.PersistentVolumeBlock
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inst.Namespace, UID: types.UID("state-" + string(inst.UID)), OwnerReferences: []metav1.OwnerReference{*ownerRefFor(inst)}},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeMode: &mode},
	}
	if err := r.Create(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	inst.Status.Resources.GuestStatePVCName = name
	inst.Status.Resources.GuestStatePVCUID = string(pvc.UID)
}

func TestGuestStatePersistsBindingBeforeVMCreation(t *testing.T) {
	ctx := context.Background()
	inst := newProvisionInst()
	stub := &stubHarvester{}
	r := newTestHarness(t, stub, inst)
	step := &vmStep{r.Dependencies}
	if res := step.ensureGuestState(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("create: %+v", res)
	}
	var pvc corev1.PersistentVolumeClaim
	key := types.NamespacedName{Namespace: inst.Namespace, Name: harvester.GuestStateVolumeName(diskIdentifierFor(inst))}
	if err := r.Get(ctx, key, &pvc); err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(&pvc)
	if owner == nil || owner.UID != inst.UID || owner.Kind != "DBInstance" {
		t.Fatalf("owner: %+v", owner)
	}
	size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if size.String() != "5Gi" || *pvc.Spec.VolumeMode != corev1.PersistentVolumeBlock || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Fatalf("PVC spec: %+v", pvc.Spec)
	}
	// The fake API does not assign UIDs. Emulate only that server behavior.
	pvc.UID = "pvc-incarnation-1"
	if err := r.Update(ctx, &pvc); err != nil {
		t.Fatal(err)
	}
	if res := step.ensureGuestState(ctx, inst); res.Outcome != OutcomePending {
		t.Fatalf("binding must stop before VM create: %+v", res)
	}
	if inst.Status.Resources.GuestStatePVCUID != string(pvc.UID) || stub.CreateVMCalls != 0 {
		t.Fatal("binding or ordering incorrect")
	}
	if res := step.ensureGuestState(ctx, inst); res.Outcome != OutcomeSatisfied {
		t.Fatalf("bound: %+v", res)
	}
}

func TestGuestStateRejectsMissingReplacedAndForeignVolumes(t *testing.T) {
	for _, scenario := range []string{"missing", "replaced", "foreign-owner", "wrong-mode", "wrong-name"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			inst := newProvisionInst()
			r := newTestHarness(t, &stubHarvester{}, inst)
			seedGuestState(t, r, inst)
			key := types.NamespacedName{Namespace: inst.Namespace, Name: inst.Status.Resources.GuestStatePVCName}
			var pvc corev1.PersistentVolumeClaim
			if err := r.Get(ctx, key, &pvc); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing":
				if err := r.Delete(ctx, &pvc); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				pvc.UID = "different-pvc"
				if err := r.Update(ctx, &pvc); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				pvc.OwnerReferences[0].UID = "different-instance"
				if err := r.Update(ctx, &pvc); err != nil {
					t.Fatal(err)
				}
			case "wrong-mode":
				mode := corev1.PersistentVolumeFilesystem
				pvc.Spec.VolumeMode = &mode
				if err := r.Update(ctx, &pvc); err != nil {
					t.Fatal(err)
				}
			case "wrong-name":
				inst.Status.Resources.GuestStatePVCName = "another-pvc"
			}
			res := (&vmStep{r.Dependencies}).ensureGuestState(ctx, inst)
			if res.Outcome != OutcomeTerminal || res.Reason != "GuestStateRecoveryRequired" {
				t.Fatalf("result: %+v", res)
			}
			if scenario == "missing" && !apierrors.IsNotFound(r.Get(ctx, key, &pvc)) {
				t.Fatal("missing volume was recreated")
			}
		})
	}
}

func TestGuestStateMountMustRemainAttached(t *testing.T) {
	inst := newProvisionInst()
	vm := testVM("pg-orders", "tenant-a")
	r := newTestHarness(t, &stubHarvester{}, inst, vm)
	seedGuestState(t, r, inst)
	res := r.ensureVM(context.Background(), inst)
	if res.Outcome != OutcomeTerminal {
		t.Fatalf("VM missing its bound state disk must be blocked: %+v", res)
	}
}

func TestExistingVMCannotBypassGuestStateWithoutStatus(t *testing.T) {
	inst := newProvisionInst()
	vm := testVM("pg-orders", "tenant-a")
	r := newTestHarness(t, &stubHarvester{}, inst, vm)
	res := r.ensureVM(context.Background(), inst)
	if res.Outcome != OutcomeTerminal || res.Reason != dbaasv1.ReasonGuestStateRecoveryRequired {
		t.Fatalf("VM without state must be rejected even without status refs: %+v", res)
	}
	var pvc corev1.PersistentVolumeClaim
	key := types.NamespacedName{Namespace: inst.Namespace, Name: harvester.GuestStateVolumeName(diskIdentifierFor(inst))}
	if err := r.Get(context.Background(), key, &pvc); !apierrors.IsNotFound(err) {
		t.Fatalf("must not provision empty state for an existing VM: %v", err)
	}
}
