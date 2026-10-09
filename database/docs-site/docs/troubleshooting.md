---
title: Troubleshooting
sidebar_position: 13
---

# Troubleshooting

Find your symptom below. Replace `NS` and `NAME` with your instance's namespace and name.

## Start here

```sh
# Phase and message
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.phase}{"  "}{.status.message}{"\n"}'

# Every condition, with its reason and message
kubectl get dbinstance NAME -n NS \
  -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'

# Conditions, events and child objects together
kubectl describe dbinstance NAME -n NS

# Events, oldest first
kubectl get events -n NS --field-selector involvedObject.name=NAME --sort-by=.lastTimestamp
```

**How to read it:**
- Look for the condition whose status is `False` or `Unknown`. Its **reason** and **message** say what's wrong.
- The phase is only a summary. See [Status and conditions](/reference/status-and-conditions).
- Many reasons are just "waiting" and fix themselves. The tables below say which ones need you.

## Operator logs

The operator is the Deployment `dbaas-operator-controller-manager` in `dbaas-system`.

```sh
kubectl -n dbaas-system get pods
kubectl -n dbaas-system logs deploy/dbaas-operator-controller-manager --tail=200
kubectl -n dbaas-system logs deploy/dbaas-operator-controller-manager -f | grep NAME
```

- Logs are JSON. Set `--logging.level=debug` for more detail. See [Operator configuration](/configuration/operator-config).
- If the operator pod isn't running, check `kubectl -n dbaas-system describe pod -l control-plane=controller-manager`. `ImagePullBackOff` with `not found` means the image tag was never pushed. `unauthorized` means the registry is private and no pull secret is set. See [Helm and Harvester Addon install](/installation/helm-addon).

## My change was rejected

**Symptom:** phase `incompatible-parameters`, or `Accepted=False`. The existing database is **not** affected.

Find the real reason:

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="PreflightReady")]}{"\n"}'
```

| Reason | Cause | Fix |
| --- | --- | --- |
| `InvalidClass` | `dbInstanceClass` isn't in the class list | Use a listed class. See [DBInstance spec](/reference/dbinstance-spec). |
| `NetworkRefMissing` | `networkRef` is empty | Set it to `namespace/name` of an existing network (`kubectl get network-attachment-definitions -A`). A wrong name passes this check and fails later, when the VM boots. |
| `VMPasswordNotAllowed` | The platform forbids `vmPassword` | Remove `spec.vmPassword` and recreate the instance. See [Policy switches](/configuration/policy-switches). |
| `ImmutableFieldChanged` | You changed a fixed field after creation (`networkRef`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `vmPassword`) | Revert it, or recreate the instance. |
| `OSImageInvalid` | The OS stream isn't available, or the engine version isn't in the image | Use a supported `engineVersion`, or set `databaseDefaults.osVersion` to a validated stream (`22.04` or `24.04`). |
| `OSImageNotFound` | The database image doesn't exist in Harvester | Upload it with the exact name and wait for it to become Active. See [Images and repave](/operations/images-and-repave). Then edit the spec or restart the operator. |
| `OSImageNotReady` | The image is still importing | Wait. It retries every 10 seconds. |
| `ValidationPending` | The image lookup hit a temporary API error | Check Harvester API access and operator permissions. It retries. |
| `UnsupportedShrink` | `allocatedStorage` is smaller than the current size | Set it back to the current size or larger. |

```sh
kubectl get virtualmachineimages.harvesterhci.io -n default
```

:::note
These image checks run only before the first VM exists. After that, a newer image shows up as `ImageDrift` instead of an error.
:::

## Stuck in `creating`

| Condition and reason | Meaning | What to do |
| --- | --- | --- |
| `VMReady`, `VMCreateFailed` | Creating the VM or disks failed. The message has the error. | Common causes: the StorageClass (default `longhorn`) is missing, quota or capacity is used up, or the image can't be cloned. Check `kubectl get vm,vmi,pvc,datavolume -n NS`. |
| `DatabaseReady`, `VMBooting` | The VM isn't running yet, or has no IP | Normal for a few minutes. If it lasts, see below. |
| `DatabaseReady`, `PostgresInitializing` | The VM has an IP but PostgreSQL isn't ready | Normal while first-boot setup runs. If it lasts, see below. |
| `CredentialsReady`, `CredentialsResolveFailed` | A temporary error creating credentials | Read the message and the operator log. Check the operator's permission on Secrets. |

**If `VMBooting` or `PostgresInitializing` lasts more than about 10 minutes:**

```sh
kubectl get vmi pg-NAME -n NS -o wide
kubectl describe vmi pg-NAME -n NS
kubectl get vmi pg-NAME -n NS -o jsonpath='{.status.interfaces}{"\n"}'
```

- **No IP:** the `networkRef` usually points at the wrong network, or the network has no DHCP.
- **Inside the VM:** the Harvester console and `/var/log/cloud-init-output.log` help, but console login exists only if you set `spec.vmPassword`.

## The database is `Degraded`

The database was working and now a check is failing. The operator only reports this. It never restarts the VM because of it.

| Reason | Cause | What to do |
| --- | --- | --- |
| `PostgresUnreachable` | The VM and agent are fine, but PostgreSQL isn't responding | Look inside the VM, or restart it from the power controls |
| `GuestAgentDisconnected` | The guest agent is down, so health is unknown | Check the VM is running and the agent is up |
| `VMRestarting` | The VM isn't running | `kubectl get vm,vmi -n NS`. If it was stopped by hand, start it. |

`Ready` is `False` while the database works? Check `MonitoringReady`. It needs the Prometheus `ServiceMonitor` CRD to exist (`kubectl get crd servicemonitors.monitoring.coreos.com`). The reason `MonitoringDeployFailed` also makes the phase `degraded`.

See [Health checks](/operations/health-checks).

## The VM keeps crashing (`crash-loop-halted`)

After 3 unplanned restarts, each within 10 minutes of the previous one, the operator halts the VM. The phase is `crash-loop-halted`, and `InterventionRequired` is `True`. Setting `spec.running` does **not** clear it.

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.restartCount}{" "}{.status.recentUnplannedRestarts}{"\n"}'
kubectl get events -n NS --field-selector reason=VMRestarting
```

1. Fix the cause (out of memory, a bad disk, a failing image).
2. Start the VM yourself: `virtctl start pg-NAME -n NS` or use the Harvester UI.
3. When the VM is running and healthy, the operator clears the halt on its own.

## Credentials are lost (`CredentialsLost`)

A required Secret (`pg-NAME-credentials`, or `dbi-UID-internal` or `dbi-UID-tls` in the operator namespace) is missing for a database that already exists. The operator won't regenerate it, because a new password wouldn't match. The database keeps running, and `InterventionRequired` is `True`.

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="CredentialsReady")].message}{"\n"}'
kubectl -n dbaas-system get secrets -l dbaas.opencloud.wso2.com/dbinstance-uid=$(kubectl get dbinstance NAME -n NS -o jsonpath='{.metadata.uid}')
```

If `pg-NAME-credentials` is missing and you know the password, recreate it with keys `admin_user` and `admin_password`. Otherwise restore the Secret from a cluster backup. See [Credentials](/security/credentials#if-something-is-lost).

## Resize, power and repave

**Resize** is a cold resize: the VM stops, changes, and starts again. See [Resize and power](/operations/resize-and-power).

| What you see | Meaning |
| --- | --- |
| `ResizeInProgress` with `ResizeStopping`, `ResizeWaitingForTeardown` or `ResizeApplied` | Normal. It clears when the database is `Ready` again. |
| `ResizeInProgress` stays on for a long time | Look at which condition isn't `True`. It is usually `DatabaseReady`, so follow the steps above. |

**Power**

| What you see | Meaning |
| --- | --- |
| `Stopped` (`Ready=False`) | You stopped it. Expected, not an error. |
| `Stopping`, `Starting`, `StartWaitingForTeardown` | Normal. The phase stays `starting` until PostgreSQL is ready, not only until the VM runs. |

**Repave (OS update).** See [Images and repave](/operations/images-and-repave). To start one, set the `dbaas.opencloud.wso2.com/repave-trigger` annotation to a **new value** each time:

```sh
kubectl annotate dbinstance NAME -n NS dbaas.opencloud.wso2.com/repave-trigger="$(date +%s)" --overwrite
```

| `ImageDrift` | Meaning |
| --- | --- |
| `True`, `OSUpdateAvailable` | A newer image exists. Trigger a repave. |
| `True`, `EngineVersionEOL` | The newer image no longer has your PostgreSQL version. Repave is blocked. Migrate your data first. |
| `False`, `ImageUpToDate` | Nothing to do |
| `Unknown` | No validated image stream is configured, or the VM's image isn't known yet |

| Repave reason | What to do |
| --- | --- |
| `RepaveNotAvailable` | The trigger came while the instance wasn't `available`, so it was ignored. Wait for `available`, then use a **new** value. |
| `RepaveBlockedEOL` | Your PostgreSQL version isn't in the new image. Migrate first. |
| `RepaveStopping`, `RepaveWaitingForTeardown`, `RepaveApplied` | Repave is in progress. |

## Connection Secret is missing

`pg-NAME-connect` is created once the database has an IP. If it's missing, the database has no address yet, so look at "Stuck in `creating`". See [Connecting](/connecting).

## Deletion is stuck

`DeletionBlocked` explains why an instance won't go away.

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="DeletionBlocked")]}{"\n"}'
```

| Reason | What to do |
| --- | --- |
| `DeletionProtected` | Turn protection off: `kubectl patch dbinstance NAME -n NS --type merge -p '{"spec":{"deletionProtection":false}}'` |
| `DeletionProgressing` | Cleanup is running. Wait. |
| `DeletionWaitingForSnapshot` | A backup of this instance is still running. Wait, or delete that `DBSnapshot` (`kubectl get dbsnap -n NS`). |
| `TeardownFailed` | Deleting the VM, monitoring objects or Secrets failed. It retries. Read the message, then `kubectl get vm,secret -n NS`. |
| `OperatorSecretCleanupFailed` | Deleting the operator-namespace Secrets failed. Check the operator's permissions. |

:::warning
**Check your disks after deleting.** The operator deletes the VM but doesn't delete the data and OS disks itself. Run `kubectl get pvc -n NS` and clean up leftovers. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).
:::

Removing the finalizer by hand skips cleanup and can leave the VM, disks and operator-namespace Secrets behind. Use it only as a last resort.

## Backup and restore

Look at the snapshot or restore object itself: `kubectl get dbsnap,dbrestore -n NS`, then `kubectl describe` the stuck one. All reasons are in [Snapshots](/backup-restore/snapshots#the-ready-condition) and [Restore](/backup-restore/restore#reasons).

### Snapshots

| What you see | Cause | What to do |
| --- | --- | --- |
| `BackupQueued` for a long time | All backup slots are busy, or your namespace used its share | Wait, or raise `backup.maxConcurrent`. `kubectl get lease -n <operator-namespace> -l dbaas.opencloud.wso2.com/backup-slot` shows who holds the slots. |
| `SnapshotHoldWaiting` | A repave or another snapshot is running on the instance | Wait. The message names it. |
| `SourceBackupDisabled` | The instance was created without `spec.backup` | Create a new instance with backups, or restore a snapshot into one. |
| `SourceNotReady` | The instance wasn't `available` | Create a new `DBSnapshot` when it is. Failures are never retried. |
| `SourceNotFound` / `SourceDeleting` | The instance is missing or being deleted | Check `spec.sourceInstanceRef.name`. |
| `BackupFailed` | Harvester reported an error | Read the message, fix it, and create a new snapshot. |
| `BackupTimedOut` | The backup took longer than `backup.timeout` (default 6 h) | Raise `backup.timeout`, or find out why Harvester is slow. |
| `DeletionWaitingForRestore` | A restore is reading a snapshot of the same source | Wait for it to finish, or delete it. |
| No automated snapshot appeared | Automated backups are off, the instance wasn't `available` at the due time (`ScheduledSnapshotSkipped` event), or the time hasn't come yet | Check `status.backup.nextScheduledSnapshotTime`. Missed runs aren't made up. See [Automated backups](/backup-restore/automated-backups). |

### Restores

| What you see | Cause | What to do |
| --- | --- | --- |
| `SnapshotNotReady` | The snapshot isn't ready yet | Wait. |
| `SnapshotNotFound`, `SnapshotFailed`, `SnapshotReplaced`, `SnapshotDeleting`, `VolumeSnapshotMissing`, `VolumeSnapshotFailed` | The snapshot can't be read | Use another `Ready` snapshot in a new `DBRestore`. |
| `InvalidSnapshotState` | The snapshot lacks recorded source data | Take a new snapshot. |
| `AllocatedStorageTooSmall` | `allocatedStorage` is below the snapshot's size | Create a new `DBRestore` with a larger value. The spec can't be edited. |
| `VolumeRestoring` for a long time | The restore disk isn't bound yet | `kubectl get pvc -n NS` and describe the PVC named in the status message. |
| `TargetNameConflict` | An instance with that name already exists | Use another name in a new `DBRestore`. |
| `TargetStarting` for a long time | The new database is booting or recovering. First start can take up to `restore.recoveryTimeout` (default 1 h). | Watch the new instance's conditions. If it never becomes ready, the reason is in `/var/lib/dbaas/restore-failed` on the VM. |
| `RestoreTimedOut` | Not finished within `restore.timeout` (default 6 h). The unfinished database was deleted. | Find the cause in the status message, then create a new `DBRestore`. |
| `TargetRejected`, `TargetInvalid` | The new database's settings were refused (for example the network or class) | Fix them in a new `DBRestore`. |
| `TargetLost`, `RestorePVCLost`, `RestorePVCConflict` | The new database or its disk was deleted or replaced | Create a new `DBRestore`. |
| A `DBRestore` stays in deletion (`Cancelling`) | An unfinished restore is being cancelled, and its database is removed first | Wait. |

**A failed restore is final.** Create a new `DBRestore`. It can reuse the same `targetInstanceName`.
