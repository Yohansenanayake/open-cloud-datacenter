---
title: Labels, annotations, finalizers and Secrets
sidebar_position: 3
---

# Labels, annotations, finalizers and Secrets

What the operator reads or writes on Kubernetes objects, in the API group `dbaas.opencloud.wso2.com`.

## What you set

| Where | Key | Purpose |
| --- | --- | --- |
| `DBInstance` annotation | `dbaas.opencloud.wso2.com/repave-trigger` | Requests a repave onto the current database image. Set a **new value** each time (a timestamp works well). A repave starts only when the value differs from `status.lastAppliedRepaveTrigger`, and only when the instance is `available`. The operator never changes or clears it. |

```bash
kubectl annotate dbinstance dbinstance-sample \
  dbaas.opencloud.wso2.com/repave-trigger="$(date -u +%Y-%m-%dT%H:%M:%SZ)" --overwrite
```

See [Images and repave](/operations/images-and-repave).

## Finalizers

Each blocks deletion until cleanup is finished. See [Lifecycle and deletion](/operations/lifecycle-and-deletion), [Snapshots](/backup-restore/snapshots) and [Restore](/backup-restore/restore).

| Key | Object | Waits for |
| --- | --- | --- |
| `dbaas.opencloud.wso2.com/cleanup` | `DBInstance` | The VM and other resources to be deleted (and `deletionProtection` to be `false`) |
| `dbaas.opencloud.wso2.com/snapshot-cleanup` | `DBSnapshot` | The Harvester backup to be deleted, and restores reading the source to end |
| `dbaas.opencloud.wso2.com/restore-cleanup` | `DBRestore` | An unfinished new database to be removed |

## Labels the operator adds

| Key | On | Purpose |
| --- | --- | --- |
| `dbaas.opencloud.wso2.com/instance` | The VM and everything created for an instance | Links an object back to its `DBInstance` |
| `dbaas.opencloud.wso2.com/dbinstance-uid` | The two operator-namespace Secrets | Lets deletion find them (they can't have an owner in another namespace) |
| `dbaas.opencloud.wso2.com/metrics` | The metrics Service and Endpoints | Lets the ServiceMonitor select them |
| `dbaas.opencloud.wso2.com/snapshot-origin` | `DBSnapshot` | `Automated` on scheduled snapshots. Missing means manual. |
| `dbaas.opencloud.wso2.com/restore-uid` | The restore's data disk | Identifies the disk as this restore's |
| `dbaas.opencloud.wso2.com/source-uid`, `backup-slot`, `snapshot-namespace` | Backup and restore locks | Internal bookkeeping |

The ServiceMonitor also carries the labels from the `serviceMonitorLabels` setting (default `release: prometheus`) so Prometheus selects it. See [Operator configuration](/configuration/operator-config).

## Secrets the operator creates

`<uid>` is the instance's full UID.

| Secret | Namespace | Holds | Notes |
| --- | --- | --- | --- |
| `pg-<name>-credentials` | instance | `admin_user`, `admin_password` | Deleted with the instance. |
| `pg-<name>-connect` | instance | host, port, database, JDBC URL, `sslmode`, `ca.crt` | **No password.** See [Connecting](/connecting). |
| `pg-<name>-cloudinit` | instance | First-boot data | The sensitive part is wiped once the database is ready. The Secret stays, because the VM has it mounted. |
| `dbi-<uid>-internal` | operator | Internal replication and metrics passwords | Not for tenants |
| `dbi-<uid>-tls` | operator | The certificate authority and server certificate | Not for tenants. Only the CA certificate is published. |

These names are reserved. See [Credentials](/security/credentials).

## Other objects the operator creates

All are in the instance's namespace.

| Object | Name |
| --- | --- |
| VirtualMachine | `pg-<name>` |
| Data disk | `pg-<id>-data` (`<id>` is the first 8 characters of the UID) |
| OS disk | `pg-<id>-os`, or `pg-<id>-os-<image>` after a repave |
| Metrics Service and Endpoints | `pg-<name>-metrics` |
| ServiceMonitor | `pg-<name>-monitor` |

If one of these is deleted by hand, the operator puts it back on the next reconcile.

**On deletion,** the operator deletes the VM, the metrics objects, the Secrets above and the VM's **data and OS disks**. It marks the VM's disks for removal through Harvester (the `harvesterhci.io/removedPersistentVolumeClaims` annotation), waits for the VM to go (`DeletionWaitingForVM`), then deletes any disk left behind. Backups you keep as manual snapshots are not touched.

## Annotations the operator writes

| Key | On | Purpose |
| --- | --- | --- |
| `dbaas.opencloud.wso2.com/crash-loop-halted-vmi-uid` | The VM | Marks which VM instance was halted for crash-looping. Removed on recovery. Internal. |
| `harvesterhci.io/removedPersistentVolumeClaims` | The VM | Set during deletion so Harvester removes the VM's disks along with it |

The `dbaas.opencloud.wso2.com/role: primary` label on the VM is informational. The operator doesn't read it.
