---
title: Concurrency and holds
sidebar_position: 4
---

# Concurrency and holds

To keep Harvester from being overloaded and to stop conflicting operations, backups and restores follow a few simple limits. You don't configure these, but they explain why a snapshot might wait.

## Backup limits

| Rule | Default |
| --- | --- |
| **Total backups running at once** (whole cluster) | `4` (`backup.maxConcurrent`) |
| **Per namespace** | at most half of the total, rounded up, never below 1. With the default, that is **2** per namespace. |
| **Order** | Oldest first. A namespace that has used its share is skipped, so one tenant's backlog doesn't block another. |
| **Time limit per backup** | `6h` (`backup.timeout`), counted from when Harvester starts the backup, not from queue time |

- **A waiting snapshot** shows `Ready=False`, reason `BackupQueued`. It starts automatically when a slot is free.
- **A backup that runs out of time** is deleted, ends as `BackupTimedOut`, and frees its slot. One that finishes right at the deadline counts as a success.
- **Lowering `maxConcurrent`** lets running backups finish. It doesn't kill them.

See the settings in [Operator configuration](/configuration/operator-config#backup-and-restore).

## One disruptive operation per database

Only one of these can run on a database at a time: a snapshot, a repave, or deletion.

| Operation | If another one is running |
| --- | --- |
| **Snapshot** | Waits (`SnapshotHoldWaiting`), giving its slot back to others while it waits |
| **Repave** | Waits for a running snapshot (`RepaveWaitingForSnapshotHold`). See [Images and repave](/operations/images-and-repave). |
| **Deleting the database** | Waits for a running snapshot to finish (`DeletionWaitingForSnapshot`). No new backup can start once deletion has begun. |

Nothing times out on its own: each operation releases its claim when it finishes or fails, or when a stale claim is cleaned up.

## Restores

- **Deleting a snapshot waits while a restore is reading that database's snapshots** (`DeletionWaitingForRestore`). This applies to the same source, not only the same snapshot, so it can wait a bit longer than strictly needed. It is never unsafe.
- **Deleting the source database does not wait for restores.** A restore reads the snapshot's saved data, which outlives the source.
- **A failed or cancelled restore** removes its own disk before releasing its claim. See [Restore](/backup-restore/restore).

## Check what is running

```bash
kubectl get dbsnap
kubectl get lease -n <operator-namespace> -l dbaas.opencloud.wso2.com/backup-slot
```

One lease per running backup slot (named `dbaas-backup-slot-<index>`). Each carries an annotation naming the snapshot that holds it. If a snapshot seems stuck waiting, count the slots in use against `backup.maxConcurrent`.

## Settings

| Key | Default | Meaning |
| --- | --- | --- |
| `backup.maxConcurrent` | `4` | Cluster-wide cap on running backups. At least 1. |
| `backup.timeout` | `6h` | Per-backup limit. Must be positive. |
