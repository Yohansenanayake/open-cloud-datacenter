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
	"k8s.io/apimachinery/pkg/types"
)

// MaxInstanceNameLength is the longest DBInstance name the operator
// accepts: "pg-" + name + "-metrics" must fit a 63-character Service name.
// The CRD's CEL rules hard-code it (markers take literals); a test keeps
// them in step.
const MaxInstanceNameLength = 52

// DBInstanceSpec defines the desired state of a managed PostgreSQL database.
// Class, storage, power state, deletion protection, and backup settings can be
// updated. Backup presence and database identity, network, engine version,
// storage class, and VM password are fixed after creation. Admission and
// preflight checks enforce these restrictions.
//
// +kubebuilder:validation:XValidation:rule="has(self.backup) == has(oldSelf.backup)",message="backup cannot be added or removed after creation"
// +kubebuilder:validation:XValidation:rule="has(self.restoredFrom) == has(oldSelf.restoredFrom)",message="restoredFrom cannot be added or removed after creation"
type DBInstanceSpec struct {
	// DBInstanceClass maps to VM CPU/RAM. e.g. "db.t3.medium", "db.m5.large".
	// Mutable: changing the class on an Available instance resizes the VM.
	// +required
	// +kubebuilder:validation:MinLength=1
	DBInstanceClass string `json:"dbInstanceClass"`

	// EngineVersion is the PostgreSQL major version, e.g. "16". Resolved
	// against the target baked image's supported versions (see
	// internal/catalog); the requested version is activated at boot by
	// bootstrap.sh, which drops the OS default cluster and creates one on
	// the requested version instead. Defaults to the baked image's
	// DefaultEngineVersion when unset.
	// Immutable after first reconcile.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="engineVersion is immutable after creation"
	EngineVersion string `json:"engineVersion,omitempty"`

	// DBName is the initial database name. Defaults to DefaultDBName of the
	// instance name, normalized to a valid, non-reserved PostgreSQL identifier.
	// Explicit values must be lowercase identifiers of at most 63 characters.
	// Immutable after first reconciliation.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	// +kubebuilder:validation:XValidation:rule="!(self in ['postgres', 'template0', 'template1'])",message="dbName must not be a built-in PostgreSQL database (postgres, template0, template1)"
	DBName string `json:"dbName,omitempty"`

	// Port for PostgreSQL. Default 5432.
	// Immutable after first reconcile.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int `json:"port,omitempty"`

	// MasterUsername is the database administrator role. Defaults to the
	// operator-configured user (dbadmin by default). It follows the DBName
	// identifier rules and cannot be postgres, postgres_exporter, or start
	// with pg_. Immutable after first reconciliation.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	// +kubebuilder:validation:XValidation:rule="!(self in ['postgres', 'postgres_exporter']) && !self.startsWith('pg_')",message="masterUsername must not be a reserved role (postgres, postgres_exporter, or a pg_ prefix)"
	MasterUsername string `json:"masterUsername,omitempty"`

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
	// interface: client traffic, package install during cloud-init, and the
	// Prometheus metrics scrape all go through it. The NAD must already exist
	// on the cluster (the controller does not create networks) and the VLAN
	// must have internet egress.
	// Immutable after first reconcile.
	// Example: "iaas-net/vm-subnet-001".
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="networkRef is immutable after creation"
	NetworkRef string `json:"networkRef"`

	// StaticNetwork, when set, configures the VM's data NIC with a static
	// IPv4 address, gateway, and DNS servers instead of running DHCP. Use
	// this on VLANs that don't have a DHCP server reachable from the VM.
	// When nil, cloud-init runs DHCP on the data NIC (the default).
	// Immutable after first reconcile (in-VM netplan reconfiguration is not
	// implemented).
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="staticNetwork is immutable after creation"
	StaticNetwork *NetworkConfig `json:"staticNetwork,omitempty"`

	// VMPassword sets the default console/SSH password for the VM user
	// (ubuntu). For development and debugging only — leave empty in
	// production. Immutable after first reconcile.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="vmPassword is immutable after creation"
	VMPassword string `json:"vmPassword,omitempty"`

	// Backup enables manual snapshots, WAL archiving, and configurable
	// automated snapshots. It cannot be added or removed after creation, but
	// its contents can be updated. Omission disables backup capability.
	// +optional
	Backup *BackupSpec `json:"backup,omitempty"`

	// RestoredFrom identifies the restore that created this instance. Set by
	// the restore controller; its value and presence are immutable.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="restoredFrom is immutable after creation"
	RestoredFrom *RestoredFromRef `json:"restoredFrom,omitempty"`
}

// RestoredFromRef records restore identity and source provenance. Its values
// remain available after the restore, snapshot, or source instance is deleted.
type RestoredFromRef struct {
	// DBRestoreName is the name of the owning DBRestore, in the same
	// namespace — for display and lookup only.
	// +required
	DBRestoreName string `json:"dbRestoreName"`

	// DBRestoreUID is the DBRestore's UID, captured at Create() time — the
	// naming salt diskIdentifierFor uses for this instance's restore PVC
	// (prefixed "restore-"), matching the name DBRestoreReconciler already
	// created it under.
	// +required
	DBRestoreUID types.UID `json:"dbRestoreUID"`

	// DBSnapshotName and DBSnapshotUID identify the DBSnapshot restored from.
	// +optional
	DBSnapshotName string `json:"dbSnapshotName,omitempty"`
	// +optional
	DBSnapshotUID types.UID `json:"dbSnapshotUID,omitempty"`

	// SourceInstanceName and SourceInstanceUID identify the DBInstance that
	// snapshot was taken of, as the snapshot recorded it.
	// +optional
	SourceInstanceName string `json:"sourceInstanceName,omitempty"`
	// +optional
	SourceInstanceUID types.UID `json:"sourceInstanceUID,omitempty"`
}

// BackupSpec configures backup capability for a DBInstance. Continuous WAL
// archiving is always enabled whenever Backup is non-nil, independent of
// Automated.Enabled — there is no separate WAL on/off field.
type BackupSpec struct {
	// Automated controls scheduled daily snapshots. Defaulted when omitted
	// from an explicitly-supplied backup object.
	// +optional
	// +kubebuilder:default={}
	Automated AutomatedBackupSpec `json:"automated,omitempty"`
}

// AutomatedBackupSpec controls the daily snapshot schedule and retention.
type AutomatedBackupSpec struct {
	// Enabled starts/stops new daily snapshots and new automatic pruning.
	// Existing retained snapshots, manual snapshots, and WAL archiving are
	// unaffected by disabling this.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// RetainCount is how many of the newest successful automated snapshots
	// to keep; older ones are pruned. A restore hold can temporarily keep
	// an otherwise-prunable snapshot past this count.
	// +optional
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=1
	RetainCount int `json:"retainCount,omitempty"`

	// PreferredWindowUTC is the UTC window, e.g. "02:00-03:00", the daily
	// snapshot is scheduled inside. An end earlier than the start crosses
	// midnight ("23:00-01:00" is two hours ending the next UTC day); equal
	// start and end is not a window, and the default is used instead. The
	// controller derives one stable minute inside the window per instance
	// (hashed from the instance UID) rather than starting every instance at
	// the window's edge.
	// +optional
	// +kubebuilder:default="02:00-03:00"
	// +kubebuilder:validation:Pattern=`^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$`
	PreferredWindowUTC string `json:"preferredWindowUTC,omitempty"`
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
	// +kubebuilder:validation:items:Pattern=`^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}$`
	Nameservers []string `json:"nameservers"`

	// SearchDomains are DNS search-domain suffixes. Optional.
	// +optional
	// +kubebuilder:validation:items:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`
	SearchDomains []string `json:"searchDomains,omitempty"`
}

// DBInstanceStatus defines the observed state of a DBInstance.
type DBInstanceStatus struct {
	// Phase summarizes Conditions using RDS-compatible status strings.
	// DerivePhaseSummary computes it; it is not independent controller state.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Conditions for each sub-resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Endpoint is populated when the database is reachable.
	// +optional
	Endpoint *Endpoint `json:"endpoint,omitempty"`

	// Resources tracks managed resource references used by clients and cleanup.
	// +optional
	Resources ResourceRefs `json:"resources,omitempty"`

	// GrafanaURL is the per-instance Grafana dashboard URL.
	// +optional
	GrafanaURL string `json:"grafanaUrl,omitempty"`

	// PrometheusTarget is the scrape target for the instance's metrics exporter.
	// +optional
	PrometheusTarget string `json:"prometheusTarget,omitempty"`

	// Message is a human-readable description of the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration tracks which spec version has been reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AppliedSpec records immutable settings at successful provisioning.
	// Changes to those settings block reconciliation and prevent advancement
	// of ObservedGeneration.
	// +optional
	AppliedSpec *AppliedSpec `json:"appliedSpec,omitempty"`

	// CurrentImageRevision is the baked image revision (internal/catalog
	// key) the VM is currently running, updated after each successful
	// repave. Used for drift detection — when this differs from the
	// catalog's current revision for the instance's stream,
	// ConditionImageDrift is set to True; when it matches, to False.
	// +optional
	CurrentImageRevision string `json:"currentImageRevision,omitempty"`

	// LastAppliedRepaveTrigger records the last accepted, rejected, or applied
	// repave trigger. The controller processes a trigger only when its value
	// differs from this field; it does not modify the annotation.
	// +optional
	LastAppliedRepaveTrigger string `json:"lastAppliedRepaveTrigger,omitempty"`

	// RestartCount is the cumulative number of VM restarts detected or
	// initiated by the controller liveness loop (both planned and unplanned).
	// +optional
	RestartCount int `json:"restartCount,omitempty"`

	// LastKnownVMIUID is the UID of the VMI last recorded by the controller.
	// A UID change while the instance is Available indicates an unplanned restart.
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

	// Backup tracks automated snapshot scheduling (spec.backup.automated).
	// Populated only once spec.backup is set — never carries over from a
	// prior instance, since backup presence is immutable after creation.
	// +optional
	Backup *BackupStatus `json:"backup,omitempty"`
}

// BackupStatus records automated snapshot scheduling state.
type BackupStatus struct {
	// NextScheduledSnapshotTime is the next future UTC snapshot time. It is
	// recomputed when scheduling is enabled, its window changes, or an attempt
	// fires. Missed runs are not replayed. Nil while automated backups are disabled.
	// +optional
	NextScheduledSnapshotTime *metav1.Time `json:"nextScheduledSnapshotTime,omitempty"`
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
	// +optional
	VMPassword string `json:"vmPassword,omitempty"`
	// +optional
	StaticNetwork *NetworkConfig `json:"staticNetwork,omitempty"`
}

// Endpoint is the network address clients use to reach the database.
type Endpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	// +optional
	JDBCURL string `json:"jdbcUrl,omitempty"`
}

// ResourceRefs tracks managed resources associated with the DBInstance.
type ResourceRefs struct {
	// NADName is the Multus NetworkAttachmentDefinition the VM's data NIC
	// attaches to. The controller does not create the NAD; this just records
	// the reference from spec.networkRef so callers can see it on the CR.
	// +optional
	NADName string `json:"nadName,omitempty"`
	// +optional
	DataVolumeName string `json:"dataVolumeName,omitempty"`
	// OSDiskPVCName is the exact current name of the OS disk PVC:
	// pg-<id>-os at first provision, or a revision-suffixed
	// pg-<id>-os-<rev> after a repave. Authoritative — TeardownAll and
	// repave's old-disk cleanup read this instead of deriving the name by
	// string-prefix matching.
	// +optional
	OSDiskPVCName string `json:"osDiskPVCName,omitempty"`
	// PendingDeleteOSDiskPVCName records the old OS disk after a successful
	// swap. It is cleared after deletion succeeds, allowing interrupted
	// cleanup to resume on the next reconciliation.
	// +optional
	PendingDeleteOSDiskPVCName string `json:"pendingDeleteOSDiskPVCName,omitempty"`
	// +optional
	VMName string `json:"vmName,omitempty"`
	// AdminCredentialsSecretName is the tenant-facing Secret containing the
	// administrator username and password.
	// +optional
	AdminCredentialsSecretName string `json:"adminCredentialsSecretName,omitempty"`
	// CloudInitSecretName is the ephemeral Secret that holds cloud-init
	// userdata and networkdata. Once PostgreSQL is ready, the controller scrubs
	// sensitive userdata but retains the object because the running VMI keeps the
	// Secret volume mounted.
	// +optional
	CloudInitSecretName string `json:"cloudInitSecretName,omitempty"`
	// +optional
	ServiceMonitor string `json:"serviceMonitor,omitempty"`
	// MetricsServiceName is the headless Service Prometheus scrapes through.
	// Tracked separately from ServiceMonitor so the finalizer's TeardownAll
	// can delete it (forgetting it leaves orphan Services in the tenant ns).
	// +optional
	MetricsServiceName string `json:"metricsServiceName,omitempty"`
	// ConnectionSecretName is the tenant-facing Secret with connection
	// metadata (host/port/dbname/jdbcUrl/sslmode/ca.crt) and no password
	// material. Lives in the DBInstance's own namespace.
	// +optional
	ConnectionSecretName string `json:"connectionSecretName,omitempty"`
	// InternalSecretRef is "namespace/name" of the controller-private Secret
	// holding DBaaS-internal credentials (repl_password, exporter_password).
	// Namespace-qualified because it lives in the operator namespace, not the
	// DBInstance's own namespace, and so cannot carry an owner reference —
	// cleanup is finalizer-driven by this ref plus a UID-label sweep backstop.
	// +optional
	InternalSecretRef string `json:"internalSecretRef,omitempty"`
	// PrivateTLSSecretRef is "namespace/name" of the controller-private
	// Secret holding the CA and server TLS material. Same cross-namespace
	// cleanup model as InternalSecretRef.
	// +optional
	PrivateTLSSecretRef string `json:"privateTLSSecretRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dbi
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.dbInstanceClass`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint.address`
// +kubebuilder:printcolumn:name="ImageDrift",type=string,JSONPath=`.status.conditions[?(@.type=='ImageDrift')].status`
// +kubebuilder:printcolumn:name="ImageDriftReason",type=string,JSONPath=`.status.conditions[?(@.type=='ImageDrift')].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue() || (size(self.metadata.name) <= 52 && self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'))",message="metadata.name must be at most 52 characters of lowercase letters, digits and '-' (no '.'): it names the instance's VM and its pg-<name>-metrics Service",optionalOldSelf=true

// DBInstance represents a managed PostgreSQL database on Harvester HCI.
//
// Its name is limited to MaxInstanceNameLength characters and the DNS-label
// alphabet (no dots), checked at creation only: child objects are named
// from it, and the strictest is the pg-<name>-metrics Service, a 63-character
// DNS-1035 label. Create-only on purpose — the name never changes, so a
// re-check on update could only ever block an object created before the
// rule, including its finalizer removal.
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
	// Status.Phase values (RDS-compatible lowercase strings).
	StatusCreating               = "creating"
	StatusAvailable              = "available"
	StatusStopping               = "stopping"
	StatusStopped                = "stopped"
	StatusStarting               = "starting"
	StatusModifying              = "modifying"
	StatusDeleting               = "deleting"
	StatusFailed                 = "failed"
	StatusDegraded               = "degraded"                // Report-only — the controller never restarts on degradation.
	StatusIncompatibleParameters = "incompatible-parameters" // A requested change was rejected; the existing database, if any, is unaffected.
	StatusCrashLoopHalted        = "crash-loop-halted"

	// Label keys applied to all Harvester resources owned by a DBInstance.
	LabelInstance = "dbaas.opencloud.wso2.com/instance"
	LabelRole     = "dbaas.opencloud.wso2.com/role"
	LabelMetrics  = "dbaas.opencloud.wso2.com/metrics"
	// LabelDBInstanceUID marks the two controller-private, cross-namespace
	// Secrets (operator-namespace internal-credentials and TLS Secrets) with
	// the owning DBInstance's UID. Cross-namespace objects can't carry owner
	// references, so this label is the backstop cleanup sweep alongside the
	// recorded ref in status.resources.
	LabelDBInstanceUID = "dbaas.opencloud.wso2.com/dbinstance-uid"

	// AnnotationCrashLoopHaltedVMIUID is stored on a VM when the controller
	// halts it after repeated unplanned restarts. The value is the UID of the
	// VMI being halted, allowing reconciliation to distinguish that VMI tearing
	// down from a later out-of-band recovery VMI.
	AnnotationCrashLoopHaltedVMIUID = "dbaas.opencloud.wso2.com/crash-loop-halted-vmi-uid"

	// AnnotationRepaveTrigger, when its value differs from
	// Status.LastAppliedRepaveTrigger, triggers a repave — swapping the
	// VM's OS disk onto the catalog's current validated revision for its
	// stream. The controller never modifies or clears this annotation; set
	// a fresh, unique value (e.g. an RFC3339 timestamp) to trigger a new
	// repave, mirroring Flux's reconcile.fluxcd.io/requestedAt convention.
	AnnotationRepaveTrigger = "dbaas.opencloud.wso2.com/repave-trigger"

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

func init() {
	SchemeBuilder.Register(&DBInstance{}, &DBInstanceList{})
}
