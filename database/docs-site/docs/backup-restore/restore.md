---
title: Restore
sidebar_position: 5
---

# Restore (DBRestore)

A `DBRestore` turns one `Ready` snapshot into a **new, independent** `DBInstance`. It is a one-time request, so its spec can't be changed after you create it. The source database is never touched and doesn't need to exist.

## Restore a snapshot

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBRestore
metadata:
  name: mydb-restore-1
  namespace: tenant-acme
spec:
  snapshotRef:
    name: mydb-before-upgrade
  targetInstanceName: mydb-restored
  dbInstanceClass: db.t3.medium
  networkRef: default/vm-net-100
  allocatedStorage: 50
  backup: {}
```

```bash
kubectl apply -f restore.yaml
kubectl get dbrestore -w
kubectl get dbinstance mydb-restored
```

```text
NAME             SNAPSHOT              TARGET          STAGE       REASON      AGE
mydb-restore-1   mydb-before-upgrade   mydb-restored   Succeeded   Succeeded   21m
```

## What you provide

| | Fields |
| --- | --- |
| **Required** | `snapshotRef.name`, `targetInstanceName`, `dbInstanceClass`, `networkRef`, `allocatedStorage` |
| **Optional** | `backup`, `vmPassword` (development only) |
| **Taken from the snapshot** (can't be set) | database name, master username, engine version, port, storage type |
| **Never copied** | instance class, network, and `backup` (add `backup: {}` if you want the new database backed up) |

Rules:

- The snapshot must be in the **same namespace** and `Ready`.
- `allocatedStorage` must be **at least the snapshot's size**. Larger is fine. Smaller fails with `AllocatedStorageTooSmall`.
- `targetInstanceName` follows the `DBInstance` naming rule (max 52 characters, lowercase letters, digits and `-`). It is independent of the `DBRestore`'s own name.
- **The master password is the new database's own.** The source's password does not work on the restored database.
- **Data is restored as-is.** Roles and data the tenant created come back unchanged.

## Stages

| Stage | Meaning |
| --- | --- |
| `Preparing` | Checking the snapshot |
| `RestoringVolume` | Creating the data disk from the snapshot |
| `StartingDatabase` | The new database is created and starting |
| `Succeeded` | The new database is `Ready`. Done. |
| `Failed` | Terminal. `status.reason` and `status.message` say why. |

**A failed restore is final.** Fix the cause and create a **new** `DBRestore`. It can reuse the same `targetInstanceName`. A failed, cancelled or timed-out restore doesn't leave its disk behind.

## Reasons

| Reason | What it means / what to do |
| --- | --- |
| `SnapshotNotReady` | Waiting for the snapshot to finish. Not a failure. |
| `SnapshotNotFound` | No snapshot with that name. |
| `SnapshotFailed` | The snapshot failed and can never be used. Take a new one. |
| `SnapshotReplaced` | A different snapshot now has that name. Create a new restore. |
| `SnapshotDeleting` / `VolumeSnapshotMissing` / `VolumeSnapshotFailed` | The backing data is gone or broken. Use another snapshot. |
| `InvalidSnapshotState` | The snapshot lacks recorded data the restore needs. Take a new snapshot. |
| `AllocatedStorageTooSmall` | Raise `allocatedStorage` to at least the snapshot's size. |
| `TargetNameConflict` | A database with that name already exists. Choose another `targetInstanceName`. |
| `TargetRejected` / `TargetInvalid` | The new database's settings were refused. Fix them in a new restore. |
| `TargetLost` | The database this restore created is gone. It is not recreated. |
| `RestorePVCConflict` / `RestorePVCLost` | The restore disk is wrong or missing. Create a new restore. |
| `RestoreTimedOut` | Didn't finish in time. See below. |
| `RestoreHoldWaiting` | Waiting its turn. Not a failure. |

## Timeouts

| Setting | Default | Meaning |
| --- | --- | --- |
| `restore.timeout` | `6h` | Limit for the whole restore, counted from when you create the `DBRestore` |
| `restore.recoveryTimeout` | `1h` | Limit for the new database's first start (PostgreSQL crash recovery) |

- A restore that runs out of time ends as `RestoreTimedOut`, and its unfinished database is deleted.
- Recovery time grows with the amount of WAL the snapshot captured, so size `recoveryTimeout` for your largest database.
- `restore.timeout` must be greater than `restore.recoveryTimeout`, or the operator won't start. See [Operator configuration](/configuration/operator-config#backup-and-restore).

## Deleting a DBRestore

```bash
kubectl delete dbrestore mydb-restore-1
```

- **After it succeeded, or the new database is live: nothing happens to the database or its disk.** The new database is an ordinary, independent instance.
- **If it is unfinished:** the restore is cancelled. The partly created database and its disk are removed.

## Where did this database come from?

The new database remembers its origin in `spec.restoredFrom`, even after the restore, the snapshot and the source are all gone:

```bash
kubectl get dbinstance mydb-restored -o jsonpath='{.spec.restoredFrom}{"\n"}'
```

It can't be edited.

If the restored database never becomes ready, the restore ends as `RestoreTimedOut`. The VM records the reason in `/var/lib/dbaas/restore-failed`.

See [RBAC](/installation/rbac) for the permissions the restore controller needs.
