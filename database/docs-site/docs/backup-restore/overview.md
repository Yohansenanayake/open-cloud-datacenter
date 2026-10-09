---
title: Overview
sidebar_position: 1
---

# Backup and restore overview

Backups are opt-in per database. Two resources do the work, both in the same namespace as the database:

| Kind | Short name | What it is |
| --- | --- | --- |
| `DBSnapshot` | `dbsnap` | One backup of one `DBInstance`. You create it for a manual backup; the operator creates it for scheduled ones. |
| `DBRestore` | `dbrestore` | One restore: a snapshot plus the settings of a new database in, a new `DBInstance` out. |

## How it works

- **Backup.** A snapshot is a Harvester `VirtualMachineBackup` of the database VM. It covers every disk of the VM.
- **Restore.** A restore creates a new disk from the backed-up data volume and starts a **new, independent** `DBInstance` on it. The source database is never modified, and it doesn't need to exist any more.

## What you can do

| Task | Page |
| --- | --- |
| Turn on backups for a database | [Automated backups](/backup-restore/automated-backups) |
| Take a manual snapshot | [Snapshots](/backup-restore/snapshots) |
| Get a daily snapshot with retention | [Automated backups](/backup-restore/automated-backups) |
| Restore a snapshot into a new database | [Restore](/backup-restore/restore) |
| Understand queueing and limits | [Concurrency and holds](/backup-restore/concurrency-and-holds) |

Field details are in the [DBSnapshot and DBRestore reference](/reference/dbsnapshot-and-dbrestore) and the [DBInstance spec](/reference/dbinstance-spec#specbackup).

## What is not implemented

- **No point-in-time recovery.** Restoring recovers only the data the snapshot captured. Continuous WAL archiving is not configured in this release.
- **Backups stay in Harvester.** The operator creates `VirtualMachineBackup` objects only. It does not set up or check Harvester's backup target, and it has no S3 or export setting.
- **Restore always makes a new database.** There is no restore in place.
- **`spec.backup` is fixed at creation.** You can't add it to or remove it from an existing database. A database created without it can never be snapshotted.
- **You choose the snapshot.** There is no automatic "latest snapshot".
- **Use `kubectl`.** The REST gateway handles `DBInstance` only.

## Requirements

- Harvester must support `VirtualMachineBackup` of type `Backup` for the VM, and the storage driver must provide volume snapshots. The operator relies on this and does not check it.
- The extra RBAC rules ([RBAC](/installation/rbac)) and the two extra CRDs must be installed. The Helm chart includes both.
