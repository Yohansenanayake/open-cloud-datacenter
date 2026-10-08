---
title: FAQ
sidebar_position: 10
---

# Frequently asked questions

Answers describe what the v0.1.0 code does. Anything not implemented is stated as such and listed on the [Roadmap](/roadmap).

## General

### What is DBaaS?

A Kubernetes operator that provisions managed PostgreSQL on Harvester HCI. You create a `DBInstance` (group `dbaas.opencloud.wso2.com`, version `v1alpha1`) and the operator creates a KubeVirt virtual machine from a pre-baked PostgreSQL image, a data disk, credentials, TLS material, a connection Secret and monitoring objects. See the [Architecture overview](/architecture/overview).

### Is it production ready?

No. v0.1.0 is an experimental release (chart and app version `0.1.0-experiment.2`) with API version `v1alpha1`. There is no backup, no high availability and no supported upgrade path between releases yet. See [Release notes](/release-notes).

### Which PostgreSQL versions are supported?

Versions come from the baked-image catalog compiled into the operator.

| OS stream | Image revision | PostgreSQL versions | Default |
| --- | --- | --- | --- |
| `22.04` | `ubuntu-2204-postgres-v20260515` | 15, 16, 17 | 17 |
| `24.04` | `ubuntu-2404-postgres-v20260701` | 15, 16, 17, 18 | 17 |

`databaseDefaults.osVersion` selects the stream for all new instances and defaults to `22.04`. A third revision, `ubuntu-2404-postgres-v20260815`, is in the catalog as a test fixture that simulates a PostgreSQL major going end of life. It supports only 18 and is not the active revision of any stream.

### Which Harvester version is supported?

The repository documents testing on Harvester 1.7.1 on RKE2 v1.34.3. It is compiled against KubeVirt API v1.6.0 and controller-runtime v0.20.4. No other Harvester version has been certified.

## Installation

### How do I install it?

Through the Helm chart, normally wrapped by a Harvester `Addon`. See [Helm and Harvester Addon install](/installation/helm-addon) and [Known gotchas](/installation/known-gotchas).

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

Mutable: `dbInstanceClass`, `allocatedStorage` (grow only), `running` and `deletionProtection`. Immutable: `networkRef`, `dbName`, `masterUsername`, `port`, `storageType`, `staticNetwork`, `vmPassword`, `engineVersion` and `credentials`. Edits to immutable fields are rejected, some by the API server and the rest by the operator with `ImmutableFieldChanged`.

### Can I resize a database without downtime?

No. Resize is a cold operation: the VM is stopped, the CPU, memory or disk is changed and the VM is started again. Storage can only grow. See [Resize and power](/operations/resize-and-power).

### Can I stop a database to save resources?

Yes. Set `spec.running: false`. The VM is stopped and storage is preserved. Phase becomes `stopped`. Setting it back to `true` starts it again.

### How do I upgrade the PostgreSQL major version?

There is no in-place major upgrade. `engineVersion` is immutable. Repave moves the instance to a newer baked image of the same PostgreSQL major, and is blocked when the new image no longer supports your major. To change major, create a new instance and migrate with `pg_dump` and `pg_restore`.

### What is a repave?

Replacing the VM's OS disk with a newer baked image while keeping the data disk. Drift is reported through the `ImageDrift` condition and you trigger the repave yourself by changing the annotation `dbaas.opencloud.wso2.com/repave-trigger`. It is never automatic. Expect a restart. See [Images and repave](/operations/images-and-repave).

### Does it back up my database?

No. The fields `s3BackupConfig`, `backupRetentionPeriod` and `preferredBackupWindow` exist in the schema but the reconciler does nothing with them. Take your own `pg_dump` backups.

### Is there high availability or read replicas?

No. `multiAZ` is accepted by the schema and ignored. There is one VM per instance, and `status.readReplicas` is never populated. If the VM fails the database is unavailable until it is restarted. Repeated unplanned restarts halt the VM (see [Troubleshooting](/troubleshooting)).

### What happens to my data when I delete a DBInstance?

The VM, monitoring objects and the Secrets the operator created are deleted. Your own password Secret is never deleted. The operator does not delete the data and OS disk PVCs itself, and the code does not verify whether Harvester removes them with the VM, so check with `kubectl get pvc`. Set `spec.deletionProtection: true` to make deletion block until you turn it off. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).

## Credentials and security

### Where is the master password?

By default the operator generates it and stores it in the Secret `pg-NAME-credentials` in the instance namespace, keys `admin_user` and `admin_password`. To choose your own, use `spec.credentials` with a Secret reference. See [Credentials](/security/credentials).

### What are `manageMasterUserPassword` and `masterUserPasswordRef`?

Reserved fields that do nothing. Use `spec.credentials`. The API rejects combining `credentials` with those two.

### Can I change the password later by editing my Secret?

No. The password is read once at creation. A later change to your Secret does not change the database; the operator emits a `PasswordSourceChanged` warning and sets `status.credentials.sourceChanged`. Changing a password on a running database is not supported in v0.1.0.

### I deleted a credential Secret. What now?

If the database is already provisioned the operator will not regenerate it, because a new value would not match the running database. The instance shows `CredentialsReady=False` with reason `CredentialsLost` and `InterventionRequired=True`. See the recovery steps in [Troubleshooting](/troubleshooting).

### Is the connection encrypted?

The operator generates a per-instance CA and server certificate and the connection Secret carries the CA certificate. The JDBC URL reported in status uses `ssl=true&sslmode=verify-ca`. See [TLS and access](/security/tls-and-access) and [Connecting](/connecting). Client certificate authentication is not implemented.

### Should I allow `vmPassword`?

Only for development. `spec.vmPassword` gives console and SSH password login to the VM. Production installs should set `security.rejectVMPassword=true`, which rejects it for new instances. See [Policy switches](/configuration/policy-switches). The trade-off is that a hardened install has no way to log in to the VM through the operator.

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

Not yet. A Rancher UI extension has been researched but is paused. See the [Roadmap](/roadmap).

:::info Verified against
- `database/README.md`
- `database/INSTALL.md`
- `database/CREDENTIALS.md`
- `database/go.mod`
- `database/api/v1alpha1/dbinstance_types.go`
- `database/api/v1alpha1/dbinstance_conditions.go`
- `database/internal/catalog/baked_images.go`
- `database/internal/config/defaults.go`
- `database/internal/config/flags.go`
- `database/internal/ensure/health.go`
- `database/internal/ensure/preflight.go`
- `database/internal/ensure/repave.go`
- `database/internal/ensure/credentials.go`
- `database/internal/controller/dbinstance_controller.go`
- `database/internal/harvester/typed_client.go`
:::
