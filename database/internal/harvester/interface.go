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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ClientInterface is the controller-facing Harvester contract. It intentionally
// hides whether Harvester resources are managed through the dynamic client or
// future typed clients.
type ClientInterface interface {
	CreateDataVolume(ctx context.Context, id, ns string, sizeGB int, storageClass string) (string, error)
	ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error

	CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName, credSecretName, cloudInitSecretName, caCertPEM string, err error)
	GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error)
	DialVMListener(ctx context.Context, ns, vmName string, port int) error // not used anymore , will be removed in future
	StopVM(ctx context.Context, ns, vmName string) error
	StartVM(ctx context.Context, ns, vmName string) error
	ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error

	DeleteSecret(ctx context.Context, ns, name string) error
	// RemoveCloudInitDisk patches the VM spec to remove the cloudinit disk and
	// volume so future VMI restarts don't try to mount the cloud-init secret.
	// Must be called before DeleteSecret on the cloud-init secret; otherwise
	// poweroff/restart leaves the VM stuck in Starting with FailedMount.
	RemoveCloudInitDisk(ctx context.Context, ns, vmName string) error

	// Monitoring objects (metrics Service/Endpoints, ServiceMonitor) are
	// builder-managed by the controller since PR7 (internal/resource) — no
	// provider method. TeardownAll still deletes them by ref as the finalizer's
	// authoritative cleanup; owner-ref GC is the backup.
	TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error
}
