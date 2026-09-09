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
	"fmt"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

func guestStateBlocked(inst *dbaasv1.DBInstance, message string) Result {
	inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionFalse, dbaasv1.ReasonGuestStateRecoveryRequired, message)
	return Terminal(dbaasv1.ReasonGuestStateRecoveryRequired, message)
}

// The PVC belongs to the DBInstance, not to a replaceable VM or its OS disk.
// Its UID is persisted before a VM may use it. No recreation after binding.
func (r *vmStep) ensureGuestState(ctx context.Context, inst *dbaasv1.DBInstance) Result {
	name := harvester.GuestStateVolumeName(diskIdentifierFor(inst))
	refs := &inst.Status.Resources
	if refs.GuestStatePVCName != "" && refs.GuestStatePVCName != name {
		return guestStateBlocked(inst, "guest-state PVC name differs from this instance's identity")
	}
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: name}, &pvc)
	if apierrors.IsNotFound(err) {
		if refs.GuestStatePVCName != "" || refs.GuestStatePVCUID != "" || refs.VMName != "" || inst.Status.AppliedSpec != nil || inst.Status.CurrentImageRevision != "" {
			return guestStateBlocked(inst, "guest-state PVC is missing; explicit recovery is required")
		}
		if inst.UID == "" {
			return guestStateBlocked(inst, "instance UID is required for guest-state ownership")
		}
		defaults := r.databaseDefaults()
		if defaults.GuestStateSizeGB < 1 {
			return guestStateBlocked(inst, "guest-state disk size must be positive")
		}
		storageClass := inst.Spec.StorageType
		if storageClass == "" {
			storageClass = defaults.StorageClass
		}
		mode := corev1.PersistentVolumeBlock
		pvc = corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: inst.Namespace, OwnerReferences: []metav1.OwnerReference{*ownerRefFor(inst)}},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  &mode, StorageClassName: &storageClass,
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", defaults.GuestStateSizeGB))}},
			},
		}
		if err := r.Create(ctx, &pvc); err != nil && !apierrors.IsAlreadyExists(err) {
			return Transient(err)
		}
		// Observe the API-assigned UID; an AlreadyExists race is validated below
		// on the next pass rather than adopting the object we tried to create.
		inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionFalse, dbaasv1.ReasonGuestStatePending, "waiting to bind the guest-state PVC")
		return PendingAfter(dbaasv1.ReasonGuestStatePending, "waiting to bind the guest-state PVC", time.Second)
	}
	if err != nil {
		return Transient(err)
	}
	owner := metav1.GetControllerOf(&pvc)
	if owner == nil || owner.UID != inst.UID || owner.Kind != "DBInstance" || owner.APIVersion != dbaasv1.GroupVersion.String() {
		return guestStateBlocked(inst, "guest-state PVC is not owned by this DBInstance UID")
	}
	if !pvc.DeletionTimestamp.IsZero() || pvc.Status.Phase == corev1.ClaimLost {
		return guestStateBlocked(inst, "guest-state PVC is deleting or lost")
	}
	if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeBlock {
		return guestStateBlocked(inst, "guest-state PVC must be a block volume")
	}
	if pvc.UID == "" {
		return PendingAfter(dbaasv1.ReasonGuestStatePending, "waiting for guest-state PVC identity", time.Second)
	}
	if refs.GuestStatePVCUID != "" && refs.GuestStatePVCUID != string(pvc.UID) {
		return guestStateBlocked(inst, "guest-state PVC was replaced; refusing an empty replacement store")
	}
	if refs.GuestStatePVCUID == "" || refs.GuestStatePVCName == "" {
		refs.GuestStatePVCName, refs.GuestStatePVCUID = name, string(pvc.UID)
		inst.SetCurrentCondition(dbaasv1.ConditionVMReady, metav1.ConditionFalse, dbaasv1.ReasonGuestStatePending, "persisting guest-state PVC identity before VM creation")
		return PendingAfter(dbaasv1.ReasonGuestStatePending, "persisting guest-state PVC identity before VM creation", time.Second)
	}
	return Satisfied()
}

func guestStateAttached(vm *kubevirtv1.VirtualMachine, name string) bool {
	if vm.Spec.Template == nil {
		return false
	}
	volumeOK, diskOK := false, false
	for _, v := range vm.Spec.Template.Spec.Volumes {
		if v.Name == "guest-state" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == name && !v.PersistentVolumeClaim.ReadOnly {
			volumeOK = true
		}
	}
	for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if d.Name == "guest-state" && d.Serial == "dbaas-state" && d.Disk != nil && d.Disk.Bus == kubevirtv1.DiskBusVirtio {
			diskOK = true
		}
	}
	return volumeOK && diskOK
}

func hasGuestStateDisk(vm *kubevirtv1.VirtualMachine) bool {
	if vm.Spec.Template == nil {
		return false
	}
	for _, v := range vm.Spec.Template.Spec.Volumes {
		if v.Name == "guest-state" {
			return true
		}
	}
	for _, d := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		if d.Name == "guest-state" {
			return true
		}
	}
	return false
}
