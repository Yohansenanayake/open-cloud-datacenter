---
title: DBSnapshot and DBRestore
sidebar_position: 5
---

# DBSnapshot and DBRestore reference

Both are namespaced resources in group `dbaas.opencloud.wso2.com`, version `v1alpha1`, each with a `status` subresource, and they live in the same namespace as the database they belong to. Guides: [Snapshots](/backup-restore/snapshots) and [Restore](/backup-restore/restore). Field descriptions are also available with `kubectl explain dbsnapshot.spec` and `kubectl explain dbrestore.spec`.

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
| `conditions` | Only `Ready` is used. See [The `Ready` condition](#the-ready-condition). |
| `progress` | 0-100, for the whole VM backup |
| `source` | The source's settings when the backup started (database name, user, engine version, port, storage type, size, image). Restores use it. |
| `dataVolumeSnapshotName` | The stored copy of the data volume, set when the backup is ready |
| `startTime`, `completionTime` | When the backup started, and when it finished |

### The `Ready` condition

`Ready` is the only condition on a snapshot, and `phase` is derived from it. A terminal reason is never retried: create a new `DBSnapshot` to try again.

| Phase | Reason (`Ready` status) | Meaning |
| --- | --- | --- |
| `Queued` | `BackupQueued` (`False`) | Waiting for a free backup slot |
| `Queued` | `SnapshotHoldWaiting` (`False`) | Another snapshot or a repave holds the database. The message names it. |
| `InProgress` | `BackupInProgress` (`False`) | Harvester is taking the backup |
| `Ready` | `BackupReady` (`True`) | Done. The snapshot can be restored. |
| `Failed` | `SourceBackupDisabled` (`False`) | The database was created without `spec.backup`. Terminal. |
| `Failed` | `SourceNotReady` (`False`) | The database wasn't `available`. Terminal. |
| `Failed` | `SourceNotFound` (`False`) | The database doesn't exist, or a same-named one replaced it. Terminal. |
| `Failed` | `SourceDeleting` (`False`) | The database is being deleted. Terminal. |
| `Failed` | `BackupFailed` (`False`) | Harvester reported an error. The message is Harvester's. Terminal. |
| `Failed` | `BackupTimedOut` (`False`) | The backup took longer than `backup.timeout`. Terminal. |
| `Deleting` | `DeletionInProgress` (`False`) | The snapshot is being deleted |
| `Deleting` | `DeletionWaitingForRestore` (`False`) | A restore is reading a snapshot of the same source. Deletion waits. |

`status.source` records the source database's settings when the backup starts: `instanceUID`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `allocatedStorage` and `imageRevision`, plus `dbInstanceClass`, `networkRef` and `backup` as hints only. It is written once and never changed.

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
| `reason`, `message` | Why it is in this stage. See [Stages and reasons](#stages-and-reasons). |
| `resolved` | The settings taken from the snapshot (database name, user, engine version, port, storage type) |
| `snapshotUID` | The snapshot this restore resolved, recorded once. If another snapshot later takes the same name, the restore fails with `SnapshotReplaced`. |
| `sourceInstanceName`, `sourceInstanceUID` | The database the snapshot came from, copied from the snapshot. Kept even after the source is deleted. |
| `dataVolumeSnapshotName` | The stored copy of the data volume the new disk is created from |
| `targetInstanceUID` | The new database's UID, once created |
| `deadline` | When the restore times out |

There is no `conditions` list.

### Stages and reasons

`status.stage` is one of `Preparing`, `RestoringVolume`, `StartingDatabase`, `Succeeded` or `Failed`. `status.reason` says why. A `Failed` restore is final: create a new `DBRestore`.

| Stage | Reason | Meaning |
| --- | --- | --- |
| `Preparing` | `SnapshotNotReady` | Waiting for the snapshot (or its stored copy) to be ready |
| `RestoringVolume` | `RestoreHoldWaiting` | Waiting its turn to read the snapshot |
| `RestoringVolume` | `VolumeRestoring` | Creating the data disk from the snapshot |
| `StartingDatabase` | `TargetStarting` | The new database is created and starting |
| `Succeeded` | `Succeeded` | The new database is `Ready` |
| `Failed` | `SnapshotNotFound`, `SnapshotFailed`, `SnapshotReplaced`, `SnapshotDeleting` | The snapshot can't be used |
| `Failed` | `VolumeSnapshotMissing`, `VolumeSnapshotFailed` | The stored copy behind the snapshot is gone or broken |
| `Failed` | `InvalidSnapshotState` | The snapshot lacks recorded source data |
| `Failed` | `AllocatedStorageTooSmall` | `allocatedStorage` is below the snapshot's size |
| `Failed` | `TargetNameConflict` | A database with that name already exists |
| `Failed` | `TargetRejected`, `TargetInvalid` | The new database's settings were refused |
| `Failed` | `TargetLost` | The database this restore created is gone. It is not recreated. |
| `Failed` | `RestorePVCConflict`, `RestorePVCLost` | The restore disk is wrong or missing |
| `Failed` | `RestoreTimedOut` | Not finished within `restore.timeout`. The unfinished database is deleted. |
| (deleting) | `Cancelling` | An unfinished restore is being cancelled |

`RestorePVCDeleted` is an event, not a stage: the restore deleted its own disk because no database took it over.

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

## Events

Each time a snapshot's or restore's reason changes, the operator raises one event with that reason. Terminal failures and `DeletionWaitingForRestore` are warnings, and the rest are normal. Use `kubectl get events --field-selector involvedObject.name=<name>`.
