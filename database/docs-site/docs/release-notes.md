---
title: Release notes
sidebar_position: 15
---

# Release notes

## v0.1.0 (experiment)

Chart version and app version: `0.1.0-experiment.2`. CRDs: `dbaas.opencloud.wso2.com/v1alpha1`, kinds `DBInstance`, `DBSnapshot` and `DBRestore`.

:::caution[Experimental]
This is a preview. The API is `v1alpha1`, there is no tested upgrade path between experimental builds, and no high availability. Backup and restore are snapshot-based with no point-in-time recovery. Do not use it for data you cannot lose.
:::

### Supported and tested versions

| Component | Version |
| --- | --- |
| Harvester | 1.7.1 (tested) |
| RKE2 | v1.34.3 (tested) |
| KubeVirt API and client | v1.6.0 (built against) |
| controller-runtime | v0.20.4 (built against) |
| Kubernetes client libraries | v0.32.5 (built against) |
| Go | 1.25.7 |
| Helm | 3.8 or newer |
| PostgreSQL | 15, 16, 17 on Ubuntu 22.04; 15, 16, 17, 18 on Ubuntu 24.04 |
| Storage | Longhorn (default StorageClass `longhorn`) |

Only Harvester 1.7.1 has been exercised. Other versions are untested and unsupported. The Harvester `Addon` route was validated on the maintainer's cluster, not on a range of versions.

### Highlights

**Provisioning and lifecycle**

- `DBInstance` custom resource. The operator creates a KubeVirt VM from a baked PostgreSQL image, a data volume, cloud-init, credentials and TLS Secrets, a connection Secret and monitoring objects.
- A bounded ensure-step reconciler with explicit condition types and reasons. See [Status and conditions](/reference/status-and-conditions).
- Cold resize of instance class and storage (grow only), and stop and start through `spec.running`. See [Resize and power](/operations/resize-and-power).
- Deletion protection and finalizer-based teardown. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).
- Crash-loop detection: three chained unplanned restarts halt the VM and require manual recovery.
- Degraded reporting (report-only, no automatic restart on probe failure).

**Backup and restore**

- `DBSnapshot`: a manual snapshot of an instance, taken as a Harvester `VirtualMachineBackup` (`type: Backup`). See [Snapshots](/backup-restore/snapshots).
- `spec.backup` on a `DBInstance`: daily automated snapshots in a UTC window (default `02:00-03:00`, windows may cross midnight), one stable minute per instance, newest `retainCount` (default 7) kept, failed automated snapshots capped the same way. Opt-in at creation only. See [Automated backups](/backup-restore/automated-backups).
- Backup concurrency control: a cluster-wide cap (`backup.maxConcurrent`, default 4), a per-namespace share, oldest-first queueing and a per-backup timeout (`backup.timeout`, default 6 h). See [Concurrency and holds](/backup-restore/concurrency-and-holds).
- Mutual exclusion between backups, repave, instance deletion and restores through `Lease`-based holds. A repave waits for a running backup; instance deletion waits for it too.
- `DBRestore`: restore a `Ready` snapshot into a new, independent `DBInstance` (`mode: Snapshot`), also after the source is deleted. Restore deadline (`restore.timeout`, default 6 h), recovery bound (`restore.recoveryTimeout`, default 1 h), cancellation by deletion, and cleanup of unfinished targets and PVCs. See [Restore](/backup-restore/restore).
- Tenant RBAC roles (`admin`, `edit`, `view` aggregation) for `DBSnapshot` and `DBRestore`. See [RBAC](/installation/rbac).
- Name rules at creation: instance names are limited to 52 characters, `dbName` and `masterUsername` are lowercase identifiers with reserved names rejected. See [DBInstance spec](/reference/dbinstance-spec).

**Images**

- Baked-image catalog for Ubuntu 22.04 and 24.04 with PostgreSQL 15 to 18.
- `ImageDrift` reporting and operator-triggered repave that swaps the OS disk and keeps the data disk. Engine end-of-life blocks a repave. See [Images and repave](/operations/images-and-repave).
- `databaseDefaults.osVersion` operator setting selects the stream.

**Credentials and security**

- Generated master password stored in `pg-NAME-credentials`.
- Password verification inside the VM during bootstrap, and redaction of the cloud-init payload once the database is up.
- Immutable-field protection through CEL rules and an operator check.

**Operations and installation**

- Helm chart generated through the kubebuilder helm plugin, plus a Harvester `Addon` manifest. See [Helm and Harvester Addon install](/installation/helm-addon).
- Centralised operator configuration (flags and environment) with `databaseDefaults`, `infrastructure`, `observability`, `security` and `logging` sections. See [Operator configuration](/configuration/operator-config).
- Prometheus `ServiceMonitor` per instance and a metrics endpoint for the operator.
- Thin REST gateway over the CRD that forwards the caller's token.
- A chart-test CI workflow.


### Known limitations

- No point-in-time recovery and no continuous WAL archiving. A restore returns the database as of the snapshot; `DBRestore.spec.mode` accepts only `Snapshot`.
- `spec.backup` must be set when the instance is created; it cannot be added or removed later.
- Backups use Harvester `VirtualMachineBackup`. The operator does not configure or validate Harvester's backup target.
- No high availability or replicas.
- No custom parameter groups or resource tags, and you can't choose your own master password.
- Passwords cannot be changed or reset on a running database through the operator.
- No certificate rotation or client certificate authentication.
- Baked images must be uploaded to Harvester by hand.
- The image catalog is compiled into the operator binary.
- The NetworkAttachmentDefinition named by `networkRef` is not validated.
- Single-namespace installs are not supported.

See [Troubleshooting](/troubleshooting) for problems you may hit.
