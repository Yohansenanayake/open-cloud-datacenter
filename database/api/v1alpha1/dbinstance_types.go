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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DBInstanceSpec defines the desired state of a managed PostgreSQL database.
//
// Field support status (v1alpha1):
//   - implemented mutable post-create: dbInstanceClass, allocatedStorage,
//     running, deletionProtection
//   - implemented immutable post-create (modify is refused): networkRef,
//     dbName, masterUsername, port, storageType, staticNetwork,
//     vmPassword, engineVersion
//   - NOT IMPLEMENTED: manageMasterUserPassword, masterUserPasswordRef,
//     multiAZ, dbParameterGroupRef, tags, s3BackupConfig,
//     backupRetentionPeriod, preferredBackupWindow. These fields exist in
//     the schema for forward compatibility but the reconciler does not
//     apply them. See ARCHITECTURE.md for the roadmap.
type DBInstanceSpec struct {
	// DBInstanceClass maps to VM CPU/RAM. e.g. "db.t3.medium", "db.m5.large".
	// Mutable: changing the class on an Available instance resizes the VM.
	// +required
	// +kubebuilder:validation:MinLength=1
	DBInstanceClass string `json:"dbInstanceClass"`

	// EngineVersion is the PostgreSQL major version, e.g. "16".
	// Immutable after first reconcile. The default below will be removed once
	// the DBInstance defaulting webhook is deployed (dbinstance_webhook.go
	// DBInstanceDefaulter), which will set this dynamically from the
	// BakedImages catalog instead of a hardcoded value.
	// +optional
	// +kubebuilder:default="17"
	EngineVersion string `json:"engineVersion,omitempty"`

	// DBName is the initial database to create. Default: the instance name.
	// Must follow PostgreSQL identifier rules: start with a letter or
	// underscore, contain only letters, digits, underscores, or "$",
	// max 63 characters. The reconciler also double-quotes this identifier
	// when emitting CREATE DATABASE; the regex catches invalid values at
	// apply time so failures don't appear later inside cloud-init.
	// Immutable after first reconcile; modify is refused.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`
	DBName string `json:"dbName,omitempty"`

	// Port for PostgreSQL. Default 5432.
	// Immutable after first reconcile.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int `json:"port,omitempty"`

	// MasterUsername for the admin user. Default "dbadmin".
	// Must follow PostgreSQL identifier rules: start with a letter or
	// underscore, contain only letters, digits, underscores, or "$",
	// max 63 characters. The reconciler also double-quotes this identifier
	// when emitting CREATE ROLE; the regex catches invalid values at
	// apply time so failures don't appear later inside cloud-init.
	// Immutable after first reconcile.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`
	MasterUsername string `json:"masterUsername,omitempty"`

	// ManageMasterUserPassword: if true, auto-generate the admin password
	// and store it in the credentials Secret; if false, read it from
	// MasterUserPasswordRef.
	// NOT YET IMPLEMENTED: the controller always generates a random password
	// regardless of this field's value, and never reads
	// MasterUserPasswordRef. The fields are reserved.
	// +optional
	ManageMasterUserPassword bool `json:"manageMasterUserPassword,omitempty"`

	// MasterUserPasswordRef points to a K8s Secret containing the
	// user-supplied admin password.
	// NOT YET IMPLEMENTED — see ManageMasterUserPassword.
	// +optional
	MasterUserPasswordRef *SecretKeyRef `json:"masterUserPasswordRef,omitempty"`

	// AllocatedStorage in GiB.
	// Mutable but grow-only: changing this on an Available instance resizes the
	// pgdata volume. Only larger values are accepted — Harvester/Longhorn ignore
	// a request below the live PVC size, so a shrink would silently no-op. The
	// CEL transition rule below rejects shrinks at apply time (evaluated on
	// update only, so create is unaffected).
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self >= oldSelf",message="allocatedStorage can only grow; shrinking is not supported"
	AllocatedStorage int `json:"allocatedStorage"`

	// StorageType maps to a Longhorn StorageClass. Default "longhorn".
	// Immutable after first reconcile (StorageClass cannot change on a
	// bound PVC).
	// +optional
	StorageType string `json:"storageType,omitempty"`

	// BackupRetentionPeriod in days. 0 (default) = disabled.
	// NOT YET IMPLEMENTED: no pgBackRest install, schedule, or retention
	// enforcement runs today. The field is recorded but inert.
	// +optional
	// +kubebuilder:validation:Minimum=0
	BackupRetentionPeriod int `json:"backupRetentionPeriod,omitempty"`

	// PreferredBackupWindow in UTC, e.g. "02:00-03:00".
	// NOT YET IMPLEMENTED — see BackupRetentionPeriod.
	// +optional
	// +kubebuilder:validation:Pattern=`^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$`
	PreferredBackupWindow string `json:"preferredBackupWindow,omitempty"`

	// MultiAZ enables Patroni HA with a standby VM.
	// NOT YET IMPLEMENTED — no standby is created.
	// +optional
	MultiAZ bool `json:"multiAZ,omitempty"`

	// DBParameterGroupRef references a DBParameterGroup by name.
	// NOT YET IMPLEMENTED — the DBParameterGroup CRD does not exist in this
	// module.
	// +optional
	DBParameterGroupRef string `json:"dbParameterGroupRef,omitempty"`

	// DeletionProtection prevents accidental deletion. While true, the
	// finalizer refuses to tear the instance down.
	// Mutable.
	// +optional
	DeletionProtection bool `json:"deletionProtection,omitempty"`

	// Running controls the VM power state. false = stopped (storage preserved).
	// Mutable: toggling sets KubeVirt spec.running on the underlying VM.
	// +kubebuilder:default=true
	// +optional
	Running *bool `json:"running,omitempty"`

	// NetworkRef is a Harvester NAD reference (namespace/name) for the VLAN
	// network the database VM attaches to. This is the VM's only network
	// interface: client traffic and the Prometheus metrics scrape go through it.
	// The NAD must already exist on the cluster (the controller does not create
	// networks). No internet egress required — packages are pre-installed in the
	// baked image.
	// Immutable after first reconcile.
	// Example: "iaas-net/vm-subnet-001".
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?\/[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	NetworkRef string `json:"networkRef"`

	// StaticNetwork, when set, configures the VM's data NIC with a static
	// IPv4 address, gateway, and DNS servers instead of running DHCP. Use
	// this on VLANs that don't have a DHCP server reachable from the VM.
	// When nil, cloud-init runs DHCP on the data NIC (the default).
	// Immutable after first reconcile (in-VM netplan reconfiguration is not
	// implemented).
	// +optional
	StaticNetwork *NetworkConfig `json:"staticNetwork,omitempty"`

	// DNSServerIP, when set, pins the VM's resolver via KubeVirt
	// dnsPolicy=None + dnsConfig.nameservers. Required on Kube-OVN VPC
	// subnets: KubeVirt's bridge-mode virt-launcher runs an internal DHCP
	// server that otherwise copies the launcher pod's cluster resolv.conf
	// (unreachable cluster DNS) into the VM, so the VM can't resolve the apt
	// archive and cloud-init's package install fails. The control plane
	// (dc-api) supplies the per-VPC CoreDNS address here. Empty leaves
	// KubeVirt's default DNS behaviour (correct for cluster-routable VLANs).
	// +optional
	DNSServerIP string `json:"dnsServerIP,omitempty"`

	// VMPassword sets the default console/SSH password for the VM user
	// (ubuntu). For development and debugging only — leave empty in
	// production. Immutable after first reconcile.
	// +optional
	VMPassword string `json:"vmPassword,omitempty"`

	// S3BackupConfig for pgBackRest S3 target.
	// NOT YET IMPLEMENTED — values are written to /etc/dbaas/bootstrap.env
	// on the VM but no backup process consumes them.
	// +optional
	S3BackupConfig *S3BackupConfig `json:"s3BackupConfig,omitempty"`

	// Tags are user-defined labels.
	// NOT YET IMPLEMENTED — not propagated to child resources or dashboards.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

// SecretKeyRef points to a single key within a K8s Secret.
type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// NetworkConfig is a static IPv4 configuration for the database VM's data
// NIC. When set on DBInstanceSpec.StaticNetwork, these values are written
// into cloud-init's netplan in place of `dhcp4: true`.
type NetworkConfig struct {
	// Address is the IPv4 address with CIDR prefix, e.g. "192.168.40.50/24".
	// +required
	// +kubebuilder:validation:Pattern=`^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}\/(3[0-2]|[12]?\d)$`
	Address string `json:"address"`

	// Gateway is the IPv4 default gateway, e.g. "192.168.40.1".
	// +required
	// +kubebuilder:validation:Pattern=`^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}$`
	Gateway string `json:"gateway"`

	// Nameservers are the DNS server IPs the VM should use. Supply at
	// least one — cloud-init will fail to resolve apt mirrors without DNS.
	// +required
	// +kubebuilder:validation:MinItems=1
	Nameservers []string `json:"nameservers"`

	// SearchDomains are DNS search-domain suffixes. Optional.
	// +optional
	SearchDomains []string `json:"searchDomains,omitempty"`
}

// S3BackupConfig describes the pgBackRest S3 target.
type S3BackupConfig struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	// +optional
	Region string `json:"region,omitempty"`
	// SecretRef is a K8s Secret name with accessKey + secretKey.
	SecretRef string `json:"secretRef"`
}

// DBInstanceStatus defines the observed state of a DBInstance.
type DBInstanceStatus struct {
	// Phase matches RDS DBInstanceStatus strings.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ProvisioningPhase tracks which reconcile step we're on.
	// +optional
	ProvisioningPhase string `json:"provisioningPhase,omitempty"`

	// Conditions for each sub-resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is populated when the database is reachable.
	// +optional
	Endpoint *Endpoint `json:"endpoint,omitempty"`

	// MasterUserSecret references the K8s Secret with credentials.
	// +optional
	MasterUserSecret *MasterUserSecretRef `json:"masterUserSecret,omitempty"`

	// Resources tracks every Harvester object created for cleanup and idempotency.
	// +optional
	Resources ResourceRefs `json:"resources,omitempty"`

	// GrafanaURL is the per-instance Grafana dashboard URL.
	// +optional
	GrafanaURL string `json:"grafanaUrl,omitempty"`

	// PrometheusTarget is the scrape target for the instance's metrics exporter.
	// +optional
	PrometheusTarget string `json:"prometheusTarget,omitempty"`

	// CACertPEM is the generated CA for SSL verification.
	// +optional
	CACertPEM string `json:"caCertPem,omitempty"`

	// ReadReplicas tracks child replica identifiers.
	// +optional
	ReadReplicas []string `json:"readReplicas,omitempty"`

	// Message is a human-readable description of the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration tracks which spec version has been reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AppliedSpec is the snapshot of immutable-after-create spec fields
	// captured at first successful reconcile. The reconciler refuses to
	// advance ObservedGeneration when any of these fields differ from the
	// current spec, because the implementation cannot carry the change
	// through to the running database. Used for honest modify semantics.
	// +optional
	AppliedSpec *AppliedSpec `json:"appliedSpec,omitempty"`

	// RestartCount is the cumulative number of VM restarts detected or
	// initiated by the controller liveness loop (both planned and unplanned).
	// +optional
	RestartCount int `json:"restartCount,omitempty"`

	// LastKnownVMIUID is the UID of the VMI last recorded by the controller.
	// A UID change during phaseAvailable indicates an unplanned restart.
	// +optional
	LastKnownVMIUID string `json:"lastKnownVMIUID,omitempty"`

	// LastUnplannedRestartTime is when the controller last observed an
	// unplanned VMI restart (UID change). Input to crash-loop detection.
	// +optional
	LastUnplannedRestartTime *metav1.Time `json:"lastUnplannedRestartTime,omitempty"`
	// RecentUnplannedRestarts counts a chain of unplanned restarts where each
	// occurred within crashLoopWindow of the previous one. A quiet gap longer
	// than the window resets the chain to 1. Reaching crashLoopThreshold halts
	// the VM and sets phase=failed (crash-loop give-up — under
	// RunStrategyAlways KubeVirt would otherwise restart the VM forever).
	// +optional
	RecentUnplannedRestarts int `json:"recentUnplannedRestarts,omitempty"`
}

// AppliedSpec records the subset of DBInstanceSpec fields that are
// immutable after creation in this controller's implementation. Mutable
// fields (DBInstanceClass, AllocatedStorage, Running, DeletionProtection)
// are deliberately excluded — they're allowed to change at any time.
type AppliedSpec struct {
	// +optional
	NetworkRef string `json:"networkRef,omitempty"`
	// +optional
	DBName string `json:"dbName,omitempty"`
	// +optional
	MasterUsername string `json:"masterUsername,omitempty"`
	// +optional
	EngineVersion string `json:"engineVersion,omitempty"`
	// +optional
	Port int `json:"port,omitempty"`
	// +optional
	StorageType string `json:"storageType,omitempty"`
	// ImageRevision is the baked image revision the VM was provisioned or last
	// repaved with, e.g. "v20260615". Used for drift detection — when this
	// differs from the current LatestBakedImages revision, OSUpdateAvailable
	// is set to True.
	// +optional
	ImageRevision string `json:"imageRevision,omitempty"`
}

// Endpoint is the network address clients use to reach the database.
type Endpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	// +optional
	JDBCURL string `json:"jdbcUrl,omitempty"`
}

// MasterUserSecretRef references the K8s Secret holding the master credentials.
type MasterUserSecretRef struct {
	Name string `json:"name"`
	// Status is "active" or "impaired".
	Status string `json:"status"`
}

// ResourceRefs tracks every Harvester resource the controller created.
// Each field is populated as the corresponding phase completes. On controller
// restart, the reconciler reads these to skip completed phases. All resources
// live in the DBInstance's own namespace — read it via inst.Namespace, not
// from this struct.
type ResourceRefs struct {
	// NADName is the Multus NetworkAttachmentDefinition the VM's data NIC
	// attaches to. The controller does not create the NAD; this just records
	// the reference from spec.networkRef so callers can see it on the CR.
	// +optional
	NADName string `json:"nadName,omitempty"`
	// +optional
	DataVolumeName string `json:"dataVolumeName,omitempty"`
	// +optional
	VMName string `json:"vmName,omitempty"`
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// CloudInitSecretName is the ephemeral Secret that holds cloud-init
	// userdata and networkdata. It is deleted by the controller as soon as
	// the VM reaches Available so the installation script and embedded
	// passwords are not left on-cluster indefinitely.
	// +optional
	CloudInitSecretName string `json:"cloudInitSecretName,omitempty"`
	// +optional
	ServiceMonitor string `json:"serviceMonitor,omitempty"`
	// MetricsServiceName is the headless Service Prometheus scrapes through.
	// Tracked separately from ServiceMonitor so the finalizer's TeardownAll
	// can delete it (forgetting it leaves orphan Services in the tenant ns).
	// +optional
	MetricsServiceName string `json:"metricsServiceName,omitempty"`
	// OSDiskPVCName is the exact current name of the OS disk PVC: pg-<id>-os
	// at first provision, or a revision-suffixed pg-<id>-os-<rev> after a
	// repave. Authoritative — TeardownAll and repave's old-disk cleanup read
	// this instead of deriving the name by string-prefix matching against a
	// namespace-wide PVC scan, which is ambiguous whenever one instance's
	// name is itself a prefix of another's (e.g. "orders" and "orders-os").
	// Empty on instances provisioned before this field existed; callers fall
	// back to reading the live VM's own volumeClaimTemplates annotation (see
	// collectDiskPVCNames), which is exact but requires the VM to still exist.
	// +optional
	OSDiskPVCName string `json:"osDiskPVCName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dbi
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.dbInstanceClass`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint.address`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DBInstance represents a managed PostgreSQL database on Harvester HCI.
// Namespaced — each DBInstance lives in a tenant namespace. All Harvester
// child resources (VM, DataVolume, Secret, Service, ServiceMonitor) are
// created in the same namespace as the DBInstance.
type DBInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBInstanceSpec   `json:"spec,omitempty"`
	Status DBInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DBInstanceList contains a list of DBInstance.
type DBInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBInstance `json:"items"`
}

const (
	// Status.ProvisioningPhase values (internal reconcile steps).
	PhasePending             = "Pending"
	PhaseNetworkProvisioned  = "NetworkProvisioned"
	PhaseStorageProvisioned  = "StorageProvisioned"
	PhaseVMCreated           = "VMCreated"
	PhaseWaitingForCloudInit = "WaitingForCloudInit"
	PhaseDatabaseReady       = "DatabaseReady"
	PhaseMonitoringDeployed  = "MonitoringDeployed"
	PhaseAvailable           = "Available"
	PhaseStopped             = "Stopped"
	PhaseFailed              = "Failed"

	// Status.Phase values (RDS-compatible lowercase strings).
	StatusCreating  = "creating"
	StatusAvailable = "available"
	StatusStopping  = "stopping"
	StatusStopped   = "stopped"
	StatusStarting  = "starting"
	StatusModifying = "modifying"
	StatusDeleting  = "deleting"
	StatusFailed    = "failed"

	// MasterUserSecretRef.Status values.
	SecretStatusActive   = "active"
	SecretStatusImpaired = "impaired"

	// Condition type constants for DBInstance liveness monitoring.
	// These are the Type field of entries in Status.Conditions.
	ConditionDegraded          = "Degraded"          // readiness probe failing or guest agent disconnected (report-only)
	ConditionFailed            = "Failed"            // crash-loop give-up, or a fatal provisioning error
	ConditionOSUpdateAvailable = "OSUpdateAvailable" // VM is running an older image revision; repave available
	ConditionPGVersionEOL      = "PGVersionEOL"      // VM's engineVersion has reached upstream EOL; customer migration required

	// Condition reason constants (Conditions[].Reason).
	ReasonPostgresUnreachable    = "PostgresUnreachable"
	ReasonGuestAgentDisconnected = "GuestAgentDisconnected"
	ReasonVMRestarting           = "VMRestarting"
	ReasonCrashLoopDetected      = "CrashLoopDetected"
	ReasonRecovered              = "Recovered"

	// Label keys applied to all Harvester resources owned by a DBInstance.
	LabelInstance = "dbaas.opencloud.wso2.com/instance"
	LabelRole     = "dbaas.opencloud.wso2.com/role"
	LabelMetrics  = "dbaas.opencloud.wso2.com/metrics"

	// FinalizerName triggers controller-side teardown of Harvester resources.
	FinalizerName = "dbaas.opencloud.wso2.com/cleanup"
)

// InstanceClassSpec maps RDS-style class names to Harvester VM resources.
type InstanceClassSpec struct {
	CPUCores       int
	MemoryMB       int
	MaxConnections int
}

// InstanceClasses is the catalog of supported instance classes.
var InstanceClasses = map[string]InstanceClassSpec{
	"db.t3.micro":   {1, 1024, 50},
	"db.t3.small":   {1, 2048, 100},
	"db.t3.medium":  {2, 4096, 150},
	"db.t3.large":   {2, 8192, 200},
	"db.t3.xlarge":  {4, 16384, 300},
	"db.m5.large":   {2, 8192, 200},
	"db.m5.xlarge":  {4, 16384, 400},
	"db.m5.2xlarge": {8, 32768, 600},
	"db.m5.4xlarge": {16, 65536, 1000},
	"db.r5.large":   {2, 16384, 300},
	"db.r5.xlarge":  {4, 32768, 500},
	"db.r5.2xlarge": {8, 65536, 800},
}

// BakedImageEntry describes a single baked image revision.
type BakedImageEntry struct {
	ImageName  string   // Harvester VirtualMachineImage name, e.g. "ubuntu-2204-postgres-v20260615"
	OSVersion  string   // Ubuntu LTS stream, e.g. "22.04"
	PGVersions []string // PG major versions pre-installed, e.g. ["15", "16", "17"]
}

// BakedImageStream is the per-OS-stream entry in LatestBakedImages.
// Validated must be true before the operator uses this stream for new VMs.
type BakedImageStream struct {
	Revision  string // e.g. "v20260615"
	Validated bool   // false = image under test, not safe to provision against
}

// BakedImages is the catalog of all baked image revisions ever published.
// Old entries are kept so existing VMs can reference their creation-time revision
// in status.appliedSpec.imageRevision for drift detection.
// To add a new revision: add one entry here and bump LatestBakedImages.
var BakedImages = map[string]BakedImageEntry{
	"v20260515": {
		ImageName:  "ubuntu-2204-postgres-v20260515",
		OSVersion:  "22.04",
		PGVersions: []string{"15", "16", "17"},
	},
	"v20260701": {
		ImageName:  "ubuntu-2404-postgres-v20260701",
		OSVersion:  "24.04",
		PGVersions: []string{"15", "16", "17", "18"},
	},
}

// LatestBakedImages maps OS stream → current validated revision. Exactly ONE
// entry per OS stream — this is a pointer to the active revision, not a
// history (that's what BakedImages is for). To publish a new revision for a
// stream you already have, EDIT that stream's existing entry in place; do not
// add a second map entry for the same key. Go map literals reject duplicate
// keys at compile time anyway ("duplicate key ... in map literal"), so this
// self-enforces — but the failure mode to avoid is reasoning about it as
// "add a row" when it's really "update the pointer".
//
// DefaultOSVersion is read from the BACKING_IMAGE_OS_VERSION operator env var —
// flip the env var to change the default stream without a code change.
// Both new-instance provisioning (phaseVM) and drift detection (phaseAvailable)
// gate on the SAME Validated flag for a stream, so add new streams with
// Validated: false until the image has been smoke-tested outside the operator
// (e.g. a VM created directly in Harvester), then flip true — there is no
// partial state where one path sees it validated and the other doesn't.
var LatestBakedImages = map[string]BakedImageStream{
	"22.04": {Revision: "v20260515", Validated: true},
	"24.04": {Revision: "v20260701", Validated: true},
}

func init() {
	SchemeBuilder.Register(&DBInstance{}, &DBInstanceList{})
}
