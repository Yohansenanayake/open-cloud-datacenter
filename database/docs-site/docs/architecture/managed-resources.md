---
title: Managed resources
sidebar_position: 3
---

# Managed resources

For a `DBInstance` named `NAME` in namespace `NS` with UID `UID`, the operator creates the objects below. All names
are deterministic and recomputable from the instance, so a lost `status` never orphans anything.

![Managed resources](@site/static/img/Managed%20resources.svg)

Objects in the operator namespace are linked to the instance by label rather than by owner reference, because owner
references cannot cross namespaces. The PVCs are created by Harvester from the VM's volume claim templates, not
directly by the operator.

## Tenant namespace objects

| Kind | Name | Created by step | Contents / purpose |
| --- | --- | --- | --- |
| `VirtualMachine` (KubeVirt) | `pg-NAME` | `vm` | Labels `dbaas.opencloud.wso2.com/instance=NAME` and `dbaas.opencloud.wso2.com/role=primary`. CPU and memory from the instance class; one data NIC on the Multus NAD from `spec.networkRef`; run strategy follows `spec.running`. The VMI template carries the instance label so VMI events map back to the owner. Controller-owned. |
| PVC (OS disk) | `pg-NAME-UID8-os` | `vm` (via Harvester) | Cloned from the baked image's storage class. After a repave the name carries a revision suffix; the current name is `status.resources.osDiskPVCName`. `UID8` is the first 8 hex characters of the instance UID, so a recreated instance never reattaches an old disk. |
| PVC (data disk) | `pg-NAME-UID8-data` | `vm` (via Harvester) | Size `spec.allocatedStorage` GiB, storage class `spec.storageType` or `databaseDefaults.storageClass` (default `longhorn`). Grow-only. |
| `Secret` (cloud-init) | `pg-NAME-cloudinit` | `vm` | Keys `userdata` and `networkdata`, read by KubeVirt's cloud-init NoCloud datasource. Contains secrets while bootstrapping; `userdata` is replaced with a no-op cloud-config by `bootstrap-cleanup` once the database is ready. The object is kept, because the running VMI keeps it mounted. Regenerated on repave. |
| `Secret` (credentials) | `pg-NAME-credentials` | `credentials` | Keys `admin_user` and `admin_password`. Generated once, or copied once from your own Secret. See [Credentials](/security/credentials). |
| `Secret` (connection) | `pg-NAME-connect` | `connection-secret` | Password-free: `host`, `port`, `dbname`, `jdbcUrl`, `sslmode` (`verify-ca`), `ca.crt`. Reconciled every pass so the address follows IP changes. See [Connecting](/connecting). |
| `Service` (headless, no selector) | `pg-NAME-metrics` | `monitoring` | Labels `dbaas.opencloud.wso2.com/instance` and `dbaas.opencloud.wso2.com/metrics=true`; port `metrics` 9187/TCP. |
| `Endpoints` | `pg-NAME-metrics` | `monitoring` | Manually binds the Service to the VM's data-network IP (the database is a VM, not a pod). Subsets stay empty until the IP is known. |
| `ServiceMonitor` | `pg-NAME-monitor` | `monitoring` | Selects the metrics Service, port `metrics`, path `/metrics`, labelled `release=prometheus` unless `observability.monitoring.serviceMonitorLabels` says otherwise; scrape interval 15 s unless configured. |

All builder-managed objects (cloud-init, connection, Service, Endpoints, ServiceMonitor) also carry the label
`dbaas.opencloud.wso2.com/instance=NAME`. The credentials Secret is created with a controller owner reference.

The metrics endpoint is served by `prometheus-postgres-exporter` inside the VM, which the cloud-init payload
configures to listen on `:9187`. Stopping an instance deactivates the scrape target but keeps the monitoring objects
until deletion.

## Operator namespace objects

Two controller-private Secrets, never exposed to tenants, live in the operator namespace (`POD_NAMESPACE`, usually
`dbaas-system`):

| Secret | Name | Keys | Purpose |
| --- | --- | --- | --- |
| Internal credentials | `dbi-UID-internal` | `repl_password`, `exporter_password` | Passwords for internal database roles (the exporter role is granted `pg_monitor`). |
| TLS | `dbi-UID-tls` | `ca.crt`, `ca.key`, server cert and key | Per-instance CA and server certificate. Only the CA certificate is published to tenants, in the connection Secret. |

Both carry labels `dbaas.opencloud.wso2.com/instance` and `dbaas.opencloud.wso2.com/dbinstance-uid`. They are
recorded in `status.resources.internalSecretRef` and `privateTLSSecretRef` as `namespace/name`.

The operator namespace also holds the backup-slot `Lease`s (see below).

## Backup and restore objects

Created only when a backup or restore is in use. See [Backup and restore](/backup-restore/overview).

| Kind | Name | Namespace | Created by | Owner reference | Notes |
| --- | --- | --- | --- | --- | --- |
| `DBSnapshot` | `<instance>-auto-<YYYYMMDD>` for automated ones | instance | the instance's scheduler | the `DBInstance` (controller) | Label `dbaas.opencloud.wso2.com/snapshot-origin: Automated`. Manual snapshots are named and created by you and have no owner. |
| `VirtualMachineBackup` (Harvester) | same name as the `DBSnapshot` | instance | `DBSnapshot` controller | the `DBSnapshot` (controller) | `spec.type: Backup`. Produces the `VolumeSnapshot` of the data volume. |
| `Lease` (snapshot hold) | `dbaas-snapshot-hold-<instance UID>` | instance | `DBSnapshot` controller or repave | the `DBInstance` (controller) | Holder `snapshot:<name>` or `repave`. |
| `Lease` (backup slot) | `dbaas-backup-slot-<index>` | operator namespace | `BackupDispatcher` | none | Holder is the `DBSnapshot` UID. Labels `dbaas.opencloud.wso2.com/backup-slot` and `dbaas.opencloud.wso2.com/snapshot-namespace`; annotation `dbaas.opencloud.wso2.com/snapshot`. Exists only while granted. |
| `Lease` (restore hold) | `dbaas-restore-hold-<DBRestore UID>` | restore | `DBRestore` controller | the `DBRestore` (controller) | Label `dbaas.opencloud.wso2.com/source-uid`. |
| PVC (restore data disk) | `pg-<target>-restore-<DBRestore UID8>-data` | restore | `DBRestore` controller | **none** | Label `dbaas.opencloud.wso2.com/restore-uid`. Becomes the target's data volume and outlives the `DBRestore`. |
| `DBInstance` (restore target) | `spec.targetInstanceName` | restore | `DBRestore` controller | none | Created with `spec.restoredFrom`. An ordinary, independent instance afterward. |

## Where the operator records what it made

`status.resources` holds: `nadName`, `dataVolumeName`, `osDiskPVCName`, `pendingDeleteOSDiskPVCName` (during a
repave), `vmName`, `adminCredentialsSecretName`, `cloudInitSecretName`, `serviceMonitor`, `metricsServiceName`,
`connectionSecretName`, `internalSecretRef` and `privateTLSSecretRef`. The finalizer's teardown reads these refs.
Fields that can be observed from the live cluster (VM name, disk names) are re-recorded on each pass, so
`status.resources` self-heals.

## Garbage collection and deletion

1. The finalizer `dbaas.opencloud.wso2.com/cleanup` runs `TeardownAll`, which explicitly deletes the
   `ServiceMonitor`, `Endpoints`, metrics `Service`, `VirtualMachine`, and the three tenant Secrets. It then removes the
   VM's disks, removes the operator-namespace Secrets, and releases the finalizer.
2. Controller owner references on same-namespace children let Kubernetes garbage collection remove anything left over.
3. Objects you supplied are never deleted: the NAD and the Harvester image.
4. The data and OS disks are deleted along with the VM. The operator marks them for removal through Harvester, waits for
   the VM to go (`DeletionWaitingForVM`), then deletes any disk left behind. Whether the underlying volume is erased
   depends on the StorageClass reclaim policy.
