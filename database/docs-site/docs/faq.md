---
title: FAQ
sidebar_position: 14
---

# Frequently asked questions

Answers describe what the v0.1.0 code does. Anything not implemented is stated as such.

## General

### What is DBaaS?

A Kubernetes operator that provisions managed PostgreSQL on Harvester HCI. You create a `DBInstance` (group `dbaas.opencloud.wso2.com`, version `v1alpha1`) and the operator creates a KubeVirt virtual machine from a pre-baked PostgreSQL image, a data disk, credentials, TLS material, a connection Secret and monitoring objects. See the [Architecture overview](/architecture/overview).

### Is it production ready?

No. v0.1.0 is an experimental release (chart and app version `0.1.0-experiment.2`) with API version `v1alpha1`. Backup and restore exist (see below), but there is no high availability and no supported upgrade path between releases yet. See [Release notes](/release-notes).

### Which PostgreSQL versions are supported?

Versions come from the baked-image catalog compiled into the operator.

| OS stream | Image revision | PostgreSQL versions | Default |
| --- | --- | --- | --- |
| `22.04` | `ubuntu-2204-postgres-v20260515` | 15, 16, 17 | 17 |
| `24.04` | `ubuntu-2404-postgres-v20260701` | 15, 16, 17, 18 | 17 |

`databaseDefaults.osVersion` selects the stream for all new instances and defaults to `22.04`.

### Which Harvester version is supported?

The repository documents testing on Harvester 1.7.1 on RKE2 v1.34.3. It is compiled against KubeVirt API v1.6.0 and controller-runtime v0.20.4. No other Harvester version has been certified.

## Installation

### How do I install it?

Through the Helm chart, normally wrapped by a Harvester `Addon`. See [Helm and Harvester Addon install](/installation/helm-addon).

### Does the operator import the PostgreSQL VM image?

No. The operator only reads existing `VirtualMachineImage` objects. The baked image must already be uploaded to Harvester, be Active, and have the exact name the catalog expects. Otherwise the instance is rejected with `OSImageNotFound`. See [Images and repave](/operations/images-and-repave).

### Can I install it in a single namespace?

No. The operator watches `DBInstance` objects in every namespace and needs cluster-wide RBAC.

### How do I upgrade the operator?

For an Addon install, change `spec.version` on the `Addon`. Harvester performs an in-place Helm upgrade and existing `DBInstance` objects are left alone. The CRD is kept on uninstall (`crd.keep: true`). Treat upgrades as untested between experimental releases.

## Using DBInstances

### What do I have to set at minimum?

`dbInstanceClass`, `allocatedStorage` (GiB) and `networkRef` (`namespace/nad-name` of a Multus NetworkAttachmentDefinition). Everything else has a default. See [DBInstance spec](/reference/dbinstance-spec).

### Which instance classes exist?

`db.t3.micro`, `db.t3.small`, `db.t3.medium`, `db.t3.large`, `db.t3.xlarge`, `db.m5.large`, `db.m5.xlarge`, `db.m5.2xlarge`, `db.m5.4xlarge`, `db.r5.large`, `db.r5.xlarge`, `db.r5.2xlarge`. The table is built into the API package and can be replaced through the operator configuration (`InstanceClasses`).

### Which fields can I change after creation?

Mutable: `dbInstanceClass`, `allocatedStorage` (grow only), `running` and `deletionProtection`. Immutable: `networkRef`, `dbName`, `masterUsername`, `port`, `storageType`, `vmPassword`, `engineVersion` and `credentials`. Edits to immutable fields are rejected, some by the API server and the rest by the operator with `ImmutableFieldChanged`.

### Can I resize a database without downtime?

No. Resize is a cold operation: the VM is stopped, the CPU, memory or disk is changed and the VM is started again. Storage can only grow. See [Resize and power](/operations/resize-and-power).

### Can I stop a database to save resources?

Yes. Set `spec.running: false`. The VM is stopped and storage is preserved. Phase becomes `stopped`. Setting it back to `true` starts it again.

### How do I upgrade the PostgreSQL major version?

There is no in-place major upgrade. `engineVersion` is immutable. Repave moves the instance to a newer baked image of the same PostgreSQL major, and is blocked when the new image no longer supports your major. To change major, create a new instance and migrate with `pg_dump` and `pg_restore`.

### What is a repave?

Replacing the VM's OS disk with a newer baked image while keeping the data disk. Drift is reported through the `ImageDrift` condition and you trigger the repave yourself by changing the annotation `dbaas.opencloud.wso2.com/repave-trigger`. It is never automatic. Expect a restart. See [Images and repave](/operations/images-and-repave).

### Does it back up my database?

Yes, if you opt in when you create the instance. Set `spec.backup` and the operator takes one automated snapshot a day in a UTC window (default `02:00-03:00`) and keeps the newest seven. You can also take a manual snapshot at any time by applying a `DBSnapshot`. A snapshot is a Harvester `VirtualMachineBackup`. `spec.backup` cannot be added to an existing instance. See [Backup and restore](/backup-restore/overview).

### How do I restore a backup?

Apply a `DBRestore` that names a `Ready` `DBSnapshot` and gives the new instance's name, class, network and storage. The operator creates a new, independent `DBInstance` from the snapshot's data. It never overwrites an existing instance, and it works after the source instance was deleted. See [Restore](/backup-restore/restore).

### Can I restore to a point in time?

No. A restore returns the database as of the snapshot. Continuous WAL archiving is not configured by the operator in this release, and `DBRestore.spec.mode` accepts only `Snapshot`.

### What survives if I delete the source instance?

Manual snapshots do, and can still be restored. Automated snapshots are owned by their instance and are garbage-collected with it. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).

### Is there high availability or read replicas?

No. There is one VM per instance, with no standby or replicas. If the VM fails the database is unavailable until it is restarted. Repeated unplanned restarts halt the VM (see [Troubleshooting](/troubleshooting)).

### What happens to my data when I delete a DBInstance?

The VM, the monitoring objects, the Secrets the operator created and the VM's data and OS disks are deleted. Automated snapshots are deleted with the instance. Manual snapshots are kept. Whether the underlying storage volume is erased depends on the StorageClass reclaim policy (use `Retain` to keep it). Set `spec.deletionProtection: true` to make deletion block until you turn it off. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).

## Credentials and security

### Where is the master password?

By default the operator generates it and stores it in the Secret `pg-NAME-credentials` in the instance namespace, keys `admin_user` and `admin_password`. You can't choose it yourself. See [Credentials](/security/credentials).

### Can I change the password later?

No. The operator generates the password once, and changing the password of a running database is not supported in v0.1.0. If you change it inside PostgreSQL yourself, update `admin_password` in `pg-NAME-credentials` so the record stays correct.

### I deleted a credential Secret. What now?

Don't delete these Secrets. The operator generates them once, and a replacement wouldn't match the running database. Restore `pg-NAME-credentials` from a backup, or recreate it with keys `admin_user` and `admin_password` holding the password the database really uses. The two `dbi-` Secrets in the operator namespace must be restored from a cluster backup. See [Credentials](/security/credentials).

### Is the connection encrypted?

The operator generates a per-instance CA and server certificate and the connection Secret carries the CA certificate. The JDBC URL reported in status uses `ssl=true&sslmode=verify-ca`. See [TLS and access](/security/tls-and-access) and [Connecting](/connecting). Client certificate authentication is not implemented.

### Should I allow `vmPassword`?

Only for development. `spec.vmPassword` gives console and SSH password login to the VM, and anyone who can create a `DBInstance` can set it. Leave it empty in production. Without it the VM has no password login and no SSH keys, so there is no way to log in to the VM through the operator. Do all administration as the master user over SQL.

## Operations

### How do I know a database is ready?

`kubectl wait --for=condition=Ready dbinstance/NAME -n NS --timeout=20m`. `Ready` requires both PostgreSQL reachability (`DatabaseReady`) and monitoring (`MonitoringReady`). Use `DatabaseReady` if you only care about PostgreSQL. See [Status and conditions](/reference/status-and-conditions).

### Why is my instance `Ready=False` but the database works?

Most often monitoring: `MonitoringReady` is false because the Prometheus Operator `ServiceMonitor` CRD is missing or creation failed. The phase is then `degraded`. See [Troubleshooting](/troubleshooting).

### Does the operator restart an unhealthy database?

No, health failures are reported as `Degraded` only. The one exception is the crash-loop guard: three unplanned VM restarts within 10 minutes of each other halt the VM and require you to start it manually.

### Is there a REST API?

Yes, a thin HTTP gateway over the CRD, enabled by default on `:8080`. It forwards the caller's bearer token so Kubernetes RBAC applies. It is configured under `server.gateway.*`. See [Operator configuration](/configuration/operator-config).

### Is there a UI?

Yes. The Rancher UI extension lets you create, resize, back up, restore and delete databases. See [Rancher UI extension](/rancher-ui-extension).
