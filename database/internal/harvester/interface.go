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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
)

// ClientInterface is the controller-facing Harvester contract. It intentionally
// hides whether Harvester resources are managed through the dynamic client or
// future typed clients.
type ClientInterface interface {
	ResizeDataVolume(ctx context.Context, ns, vmName, dvName string, newSizeGB int) error

	// CreatePostgresVM creates the VM from an already-provisioned cloud-init
	// Secret (p.CloudInitSecretName) — credential/TLS material and the
	// cloud-init payload are resolved by internal/credentials and applied by
	// internal/resource before this is called (PR8); the provider only builds
	// VM shape.
	CreatePostgresVM(ctx context.Context, p VMCreateParams) (vmName string, err error)
	GetVMIReadiness(ctx context.Context, ns, vmName string) (VMIReadiness, error)
	StopVM(ctx context.Context, ns, vmName string) error
	StartVM(ctx context.Context, ns, vmName string) error
	ResizeVM(ctx context.Context, ns, vmName string, cpuCores, memoryMB int) error

	// RemoveCloudInitDisk patches the VM spec to remove the cloudinit disk and
	// volume so future VMI restarts don't try to mount the cloud-init secret.
	// The caller must remove this reference before deleting the cloud-init
	// Secret itself (a plain corev1.Secret, deleted directly via the
	// controller's own client — not a Harvester resource); otherwise
	// poweroff/restart leaves the VM stuck in Starting with FailedMount.
	RemoveCloudInitDisk(ctx context.Context, ns, vmName string) error

	// Monitoring objects (metrics Service/Endpoints, ServiceMonitor) are
	// builder-managed by the controller since PR7 (internal/resource) — no
	// provider method. TeardownAll still deletes them by ref as the finalizer's
	// authoritative cleanup; owner-ref GC is the backup.
	TeardownAll(ctx context.Context, id, ns string, refs dbaasv1.ResourceRefs) error
}

// VMCreateParams bundles everything needed to create a PostgreSQL VM. Fields
// used only for credential/cloud-init generation (DBName, MaxConnections,
// backup/S3, VMPassword, StaticNetwork) moved to internal/credentials.
// BootstrapParams in PR8 — the provider only builds VM shape and consumes an
// already-provisioned cloud-init Secret. Port and MasterUser stay: the VMI
// readiness probe embeds them directly (see buildPostgresVM).
type VMCreateParams struct {
	ID                     string
	Namespace              string
	CPUCores               int
	MemoryMB               int
	OSImage                string
	DataVolumeRef          string
	DataVolumeSizeGB       int
	DataVolumeStorageClass string
	NADName                string
	MasterUser             string
	Port                   int
	// CloudInitSecretName is the pre-created ephemeral Secret (userdata +
	// networkdata) this VM's cloudInitNoCloud volume references.
	CloudInitSecretName string
	// DNSServerIP, when non-empty, pins the VM's resolver via KubeVirt
	// dnsPolicy=None + dnsConfig.nameservers. Required on Kube-OVN VPC
	// subnets to defeat the virt-launcher internal-DHCP DNS race (it would
	// otherwise inject unreachable cluster DNS, breaking apt during
	// cloud-init). Supplied by the control plane (per-VPC CoreDNS address).
	DNSServerIP string
	// Owner, when non-nil, is stamped as the controller owner reference on the
	// VM this call creates, so Owns() watches fire and GC backs up the
	// finalizer teardown (PR7).
	Owner *metav1.OwnerReference
}

// VMIReadiness bundles the VMI state fields needed for phase gating and
// liveness monitoring from a single VMI fetch.
//
//   - Running: VMI phase is Running.
//   - IP: data-network IP reported by the guest agent; empty until QGA populates it.
//   - Ready: VMI readiness condition is True (readiness probe has passed).
//   - AgentConnected: QGA virtio channel is active.
//   - VMIUID: VMI object UID; a change across reconciles indicates an unplanned restart.
type VMIReadiness struct {
	Running        bool
	IP             string
	Ready          bool
	AgentConnected bool
	VMIUID         string
}
