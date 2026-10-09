---
title: Snapshots
sidebar_position: 2
---

# Snapshots (DBSnapshot)

A `DBSnapshot` is one backup of one `DBInstance`. Applying one makes a **manual** snapshot. The operator makes **automated** ones itself (see [Automated backups](/backup-restore/automated-backups)).

## Take a manual snapshot

The database must have `spec.backup` set and be in phase `available`.

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBSnapshot
metadata:
  name: mydb-before-upgrade
  namespace: tenant-acme
spec:
  sourceInstanceRef:
    name: mydb
```

```bash
kubectl apply -f snapshot.yaml
kubectl get dbsnap -w
kubectl wait dbsnapshot/mydb-before-upgrade --for=condition=Ready --timeout=2h
```

```text
NAME                  SOURCE   ORIGIN   PHASE   REASON        AGE
mydb-before-upgrade   mydb     Manual   Ready   BackupReady   14m
```

- `sourceInstanceRef` names a database in the **same namespace** and can't be changed.
- The snapshot's name is also the name of the Harvester backup.
- `kubectl get dbsnap -o wide` adds a `Progress` column (0-100).

## What to expect

A snapshot goes through these phases:

| Phase | Meaning |
| --- | --- |
| `Queued` | Waiting for a backup slot, or for another operation on the same database to finish |
| `InProgress` | Harvester is taking the backup |
| `Ready` | Done. The snapshot can be restored. |
| `Failed` | Terminal. See the reason below. |

- **Failures are never retried.** To try again, create a new `DBSnapshot`.
- **One operation at a time per database.** A snapshot waits while a repave or another snapshot is running on the same database. See [Concurrency and holds](/backup-restore/concurrency-and-holds).
- **Once a backup has started, it runs to the end,** even if the database's phase changes. A database that starts being deleted waits for it.
- **Time limit.** A backup must finish within `backup.timeout` (default 6 hours, counted from when Harvester starts it).

## The Ready condition

| Reason | What to do |
| --- | --- |
| `SourceBackupDisabled` | The database was created without `spec.backup`. This can't be added later; create a new database with it. |
| `SourceNotReady` | The database wasn't `available` (stopped, resizing, repaving...). Try again when it is. |
| `SourceNotFound` | The database doesn't exist, or a same-named one replaced it. |
| `SourceDeleting` | The database is being deleted. |
| `BackupFailed` | Harvester reported an error. The message is Harvester's. |
| `BackupTimedOut` | Raise `backup.timeout`, or find out why Harvester is slow. |

Waiting reasons that are not failures: `BackupQueued` (all slots busy, or this namespace used its share) and `SnapshotHoldWaiting` (a repave or another snapshot is running; the message names it).

## What a snapshot records

When the backup starts, the snapshot records the database's settings at that moment in `status.source` (database name, user, engine version, port, storage size, and so on). A restore uses this record, not the live database, so **restores keep working after the source is deleted**. Check progress and details with `kubectl get dbsnap <name> -o yaml`.

## Delete a snapshot

```bash
kubectl delete dbsnapshot mydb-before-upgrade
```

- Deleting removes the Harvester backup too.
- **Deletion waits while a restore is reading the same source's snapshots.** This includes a restore from a sibling snapshot of the same database. The status shows `DeletionWaitingForRestore`.

**When the source database is deleted:**

| Snapshot type | Result |
| --- | --- |
| Automated | Deleted with the database |
| Manual | Kept, and still restorable |

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Stuck in `BackupQueued` | All slots are in use, or this namespace used its share. See [Concurrency and holds](/backup-restore/concurrency-and-holds). |
| Stuck in `SnapshotHoldWaiting` | A repave or another snapshot holds the database. |
| `Failed` | Check the reason above. |

More in [Troubleshooting](/troubleshooting#backup-and-restore).
