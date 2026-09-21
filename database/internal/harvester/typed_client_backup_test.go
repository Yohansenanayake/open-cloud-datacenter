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

package harvester

import (
	"context"
	"testing"

	harvesterhciov1beta1 "github.com/harvester/harvester/pkg/apis/harvesterhci.io/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

func testOwnerRef() *metav1.OwnerReference {
	return &metav1.OwnerReference{
		APIVersion: "dbaas.opencloud.wso2.com/v1alpha1",
		Kind:       "DBInstance",
		Name:       "orders",
		UID:        "orders-uid",
	}
}

func TestCreateVMBackupSetsDurableTypeAndSource(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	if err := client.CreateVMBackup(ctx, "tenant-a", "orders-daily-1", "pg-orders", testOwnerRef()); err != nil {
		t.Fatalf("CreateVMBackup: %v", err)
	}

	vmBackup, err := client.Clientset.HarvesterhciV1beta1().VirtualMachineBackups("tenant-a").Get(ctx, "orders-daily-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get VirtualMachineBackup: %v", err)
	}
	if vmBackup.Spec.Type != harvesterhciov1beta1.Backup {
		t.Fatalf("Spec.Type = %q, want %q (never Snapshot — that variant is local-only)", vmBackup.Spec.Type, harvesterhciov1beta1.Backup)
	}
	if vmBackup.Spec.Source.Kind != kubevirtv1.VirtualMachineGroupVersionKind.Kind || vmBackup.Spec.Source.Name != "pg-orders" {
		t.Fatalf("Spec.Source = %+v, want Kind=%q Name=%q", vmBackup.Spec.Source, kubevirtv1.VirtualMachineGroupVersionKind.Kind, "pg-orders")
	}
	if len(vmBackup.OwnerReferences) != 1 || vmBackup.OwnerReferences[0].Name != "orders" {
		t.Fatalf("OwnerReferences = %+v, want the supplied owner", vmBackup.OwnerReferences)
	}
}

func TestCreateVMBackupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	if err := client.CreateVMBackup(ctx, "tenant-a", "orders-daily-1", "pg-orders", testOwnerRef()); err != nil {
		t.Fatalf("first CreateVMBackup: %v", err)
	}
	if err := client.CreateVMBackup(ctx, "tenant-a", "orders-daily-1", "pg-orders", testOwnerRef()); err != nil {
		t.Fatalf("second CreateVMBackup (re-entry) returned an error, want AlreadyExists swallowed: %v", err)
	}
}

func vmBackupFixture(name, ns string, readyToUse bool, errMsg string, volumeBackups ...harvesterhciov1beta1.VolumeBackup) *harvesterhciov1beta1.VirtualMachineBackup {
	vb := &harvesterhciov1beta1.VirtualMachineBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: harvesterhciov1beta1.VirtualMachineBackupSpec{
			Source: corev1.TypedLocalObjectReference{Kind: "VirtualMachine", Name: "pg-orders"},
			Type:   harvesterhciov1beta1.Backup,
		},
		Status: harvesterhciov1beta1.VirtualMachineBackupStatus{
			ReadyToUse:    &readyToUse,
			VolumeBackups: volumeBackups,
		},
	}
	if errMsg != "" {
		vb.Status.Error = &harvesterhciov1beta1.Error{Message: &errMsg}
	}
	return vb
}

func volumeBackup(snapshotName, pvcName string) harvesterhciov1beta1.VolumeBackup {
	name := snapshotName
	return harvesterhciov1beta1.VolumeBackup{
		Name: &name,
		PersistentVolumeClaim: harvesterhciov1beta1.PersistentVolumeClaimSourceSpec{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName},
		},
	}
}

func TestGetVMBackupStatusFindsTheDataVolumeSnapshotAmongSeveral(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(vmBackupFixture("orders-daily-1", "tenant-a", true, "",
		volumeBackup("orders-daily-1-volume-pg-orders-os", "pg-orders-os"),
		volumeBackup("orders-daily-1-volume-pg-orders-data", "pg-orders-data"),
	))

	status, err := client.GetVMBackupStatus(ctx, "tenant-a", "orders-daily-1", "pg-orders-data")
	if err != nil {
		t.Fatalf("GetVMBackupStatus: %v", err)
	}
	if !status.ReadyToUse {
		t.Fatal("ReadyToUse = false, want true")
	}
	if status.DataVolumeSnapshotName != "orders-daily-1-volume-pg-orders-data" {
		t.Fatalf("DataVolumeSnapshotName = %q, want the data volume's snapshot, not the OS disk's", status.DataVolumeSnapshotName)
	}
}

func TestGetVMBackupStatusTranslatesError(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(vmBackupFixture("orders-daily-1", "tenant-a", false, "backup target unreachable"))

	status, err := client.GetVMBackupStatus(ctx, "tenant-a", "orders-daily-1", "pg-orders-data")
	if err != nil {
		t.Fatalf("GetVMBackupStatus: %v", err)
	}
	if status.ReadyToUse {
		t.Fatal("ReadyToUse = true, want false")
	}
	if status.ErrorMessage != "backup target unreachable" {
		t.Fatalf("ErrorMessage = %q, want the recorded backup error", status.ErrorMessage)
	}
}

func TestGetVMBackupStatusEmptySnapshotNameWhenPVCNotAmongVolumes(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(vmBackupFixture("orders-daily-1", "tenant-a", true, "",
		volumeBackup("orders-daily-1-volume-pg-orders-os", "pg-orders-os"),
	))

	status, err := client.GetVMBackupStatus(ctx, "tenant-a", "orders-daily-1", "pg-orders-data")
	if err != nil {
		t.Fatalf("GetVMBackupStatus: %v", err)
	}
	if status.DataVolumeSnapshotName != "" {
		t.Fatalf("DataVolumeSnapshotName = %q, want empty when the requested PVC isn't among the backup's volumes", status.DataVolumeSnapshotName)
	}
}

func TestDeleteVMBackupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient(vmBackupFixture("orders-daily-1", "tenant-a", true, ""))

	if err := client.DeleteVMBackup(ctx, "tenant-a", "orders-daily-1"); err != nil {
		t.Fatalf("first DeleteVMBackup: %v", err)
	}
	if err := client.DeleteVMBackup(ctx, "tenant-a", "orders-daily-1"); err != nil {
		t.Fatalf("second DeleteVMBackup (already gone) returned an error, want NotFound swallowed: %v", err)
	}
	if _, err := client.Clientset.HarvesterhciV1beta1().VirtualMachineBackups("tenant-a").Get(ctx, "orders-daily-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("VirtualMachineBackup still exists after delete: %v", err)
	}
}

func TestCreateRestorePVCPointsDataSourceAtTheSnapshot(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	err := client.CreateRestorePVC(ctx, "tenant-a", "pg-restored-data", "orders-daily-1-volume-pg-orders-data", 20, "longhorn", testOwnerRef())
	if err != nil {
		t.Fatalf("CreateRestorePVC: %v", err)
	}

	pvc, err := client.KubeClient.CoreV1().PersistentVolumeClaims("tenant-a").Get(ctx, "pg-restored-data", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get PVC: %v", err)
	}
	ds := pvc.Spec.DataSource
	if ds == nil || ds.Kind != "VolumeSnapshot" || ds.Name != "orders-daily-1-volume-pg-orders-data" {
		t.Fatalf("Spec.DataSource = %+v, want a VolumeSnapshot reference to the data volume's snapshot", ds)
	}
	if ds.APIGroup == nil || *ds.APIGroup != restoreVolumeSnapshotAPIGroup {
		t.Fatalf("Spec.DataSource.APIGroup = %v, want %q", ds.APIGroup, restoreVolumeSnapshotAPIGroup)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "longhorn" {
		t.Fatalf("Spec.StorageClassName = %v, want %q", pvc.Spec.StorageClassName, "longhorn")
	}
	if len(pvc.OwnerReferences) != 1 || pvc.OwnerReferences[0].Name != "orders" {
		t.Fatalf("OwnerReferences = %+v, want the supplied owner", pvc.OwnerReferences)
	}
	wantSize := resource.MustParse("20Gi")
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(wantSize) != 0 {
		t.Fatalf("requested storage = %s, want %s", got.String(), wantSize.String())
	}
}

func TestCreateRestorePVCIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()

	if err := client.CreateRestorePVC(ctx, "tenant-a", "pg-restored-data", "snap-1", 20, "longhorn", testOwnerRef()); err != nil {
		t.Fatalf("first CreateRestorePVC: %v", err)
	}
	if err := client.CreateRestorePVC(ctx, "tenant-a", "pg-restored-data", "snap-1", 20, "longhorn", testOwnerRef()); err != nil {
		t.Fatalf("second CreateRestorePVC (re-entry) returned an error, want AlreadyExists swallowed: %v", err)
	}
}

func TestGetPVCPhaseReturnsCurrentPhase(t *testing.T) {
	ctx := context.Background()
	client := newTestTypedClient()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pg-restored-data", Namespace: "tenant-a"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	if _, err := client.KubeClient.CoreV1().PersistentVolumeClaims("tenant-a").Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed PVC: %v", err)
	}

	phase, err := client.GetPVCPhase(ctx, "tenant-a", "pg-restored-data")
	if err != nil {
		t.Fatalf("GetPVCPhase: %v", err)
	}
	if phase != corev1.ClaimBound {
		t.Fatalf("phase = %q, want %q", phase, corev1.ClaimBound)
	}
}
