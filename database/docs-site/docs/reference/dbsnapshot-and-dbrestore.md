---
title: DBSnapshot and DBRestore
sidebar_position: 5
---

# DBSnapshot and DBRestore reference

Both are namespaced resources (`dbaas.opencloud.wso2.com/v1alpha1`). Guides: [Snapshots](/backup-restore/snapshots) and [Restore](/backup-restore/restore).

## DBSnapshot

Short name `dbsnap`. `kubectl get dbsnap` shows `Source`, `Origin`, `Phase`, `Reason` and `Age`. `-o wide` adds `Progress`.

### spec

| Field | Required | Change later? | Notes |
| --- | --- | --- | --- |
| `sourceInstanceRef.name` | yes | no | The `DBInstance` to snapshot, in the same namespace |

### status

| Field | Meaning |
| --- | --- |
| `phase` | `Queued`, `InProgress`, `Ready`, `Failed` or `Deleting` |
| `origin` | `Manual` or `Automated` |
| `conditions` | Only `Ready` is used. See [reasons](/backup-restore/snapshots#the-ready-condition). |
| `progress` | 0-100, for the whole VM backup |
| `source` | The source's settings when the backup started (database name, user, engine version, port, storage type, size, image). Restores use it. |
| `dataVolumeSnapshotName` | The stored copy of the data volume, set when the backup is ready |
| `startTime`, `completionTime` | When the backup started, and when it finished |

### Other things to know

| Item | Value |
| --- | --- |
| Finalizer | `dbaas.opencloud.wso2.com/snapshot-cleanup`. Deletion waits until the Harvester backup is removed and no restore is reading the source. |
| Label | `dbaas.opencloud.wso2.com/snapshot-origin: Automated` on snapshots the operator creates |
| Ownership | Automated snapshots belong to their `DBInstance` and are deleted with it. Manual ones don't. |

## DBRestore

Short name `dbrestore`. **The whole spec is fixed after creation.** `kubectl get dbrestore` shows `Snapshot`, `Target`, `Stage`, `Reason` and `Age`.

### spec

| Field | Required | Notes |
| --- | --- | --- |
| `snapshotRef.name` | yes | A `Ready` snapshot in the same namespace |
| `targetInstanceName` | yes | Name of the new database. At most 52 characters: lowercase letters, digits and `-`. |
| `dbInstanceClass` | yes | Size of the new database |
| `networkRef` | yes | Network of the new database. Never copied from the source. |
| `allocatedStorage` | yes | Disk size in GiB. At least the snapshot's size. |
| `backup` | no | Same as `DBInstance.spec.backup`. Leave it out for no backups on the new database. |
| `vmPassword` | no | Development only. Passed to the new database. |
| `mode` | no | `Snapshot` (the default, and the only value) |

### status

| Field | Meaning |
| --- | --- |
| `stage` | `Preparing`, `RestoringVolume`, `StartingDatabase`, `Succeeded` or `Failed` |
| `reason`, `message` | Why it is in this stage. See [reasons](/backup-restore/restore#reasons). |
| `resolved` | The settings taken from the snapshot (database name, user, engine version, port, storage type) |
| `targetInstanceUID` | The new database's UID, once created |
| `deadline` | When the restore times out |

There is no `conditions` list.

### Other things to know

| Item | Value |
| --- | --- |
| Finalizer | `dbaas.opencloud.wso2.com/restore-cleanup`. Deletion waits until any unfinished new database is removed. |
| Restore disk | `pg-<targetInstanceName>-restore-<8 characters of the restore's UID>-data`. Labelled `dbaas.opencloud.wso2.com/restore-uid=<UID>`. |

## Related DBInstance fields

| Field | Notes |
| --- | --- |
| `spec.backup` | See [DBInstance spec](/reference/dbinstance-spec#specbackup) |
| `spec.restoredFrom` | Set by the restore process. See [DBInstance spec](/reference/dbinstance-spec#specrestoredfrom). |
| `status.backup.nextScheduledSnapshotTime` | When the next automated snapshot is due |
