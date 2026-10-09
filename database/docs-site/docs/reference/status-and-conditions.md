---
title: Status and conditions
sidebar_position: 2
---

# Status and conditions reference

The operator reports what it sees in `status`. `status.phase` is a human-friendly summary derived from the conditions. It never controls anything. **Scripts should wait on conditions** (such as `Ready`), not on the phase.

## kubectl output

```text
$ kubectl get dbi
NAME                PHASE       CLASS          ENDPOINT        IMAGEDRIFT   AGE
dbinstance-sample   available   db.t3.medium   192.168.40.50   False        12m
```

`-o wide` adds `ImageDriftReason`. Wait for readiness with:

```bash
kubectl wait --for=condition=Ready dbinstance/dbinstance-sample --timeout=15m
```

## Status fields

| Field | Meaning |
| --- | --- |
| `phase`, `message` | A summary and a matching description. See [Phases](#phases). |
| `conditions` | The detailed state. See [Conditions](#conditions). |
| `observedGeneration` | Equals `metadata.generation` once the latest spec has been fully applied. If it is behind, a change is still being applied or was rejected. |
| `endpoint` | `address` (the VM's IP), `port`, and `jdbcUrl` (`jdbc:postgresql://<address>:<port>/<dbName>?ssl=true&sslmode=verify-ca`). Set once PostgreSQL is ready, and refreshed on restart or live migration. |
| `appliedSpec` | The fixed settings used when the VM was created (`networkRef`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `vmPassword`). Used to refuse edits to them. |
| `currentImageRevision` | The database image the VM runs. Compared with the catalog to compute `ImageDrift`. |
| `lastAppliedRepaveTrigger` | The last `repave-trigger` annotation value that was processed. |
| `backup.nextScheduledSnapshotTime` | When the next automated snapshot is due. See [Automated backups](/backup-restore/automated-backups). |
| `grafanaUrl`, `prometheusTarget` | The instance's dashboard and metrics address. |
| `restartCount`, `recentUnplannedRestarts` | Unplanned VM restart counts. Three restarts in a row halt the VM (see `CrashLoopHalted`). |
| `resources` | The names of the objects the operator created for this instance: `vmName`, `dataVolumeName`, `osDiskPVCName`, `nadName`, `adminCredentialsSecretName`, `connectionSecretName`, `cloudInitSecretName`, `metricsServiceName`, `serviceMonitor`, `internalSecretRef`, `privateTLSSecretRef`. Used for cleanup. |

## Phases

`status.phase` is the first row below that matches.

| Phase | Meaning |
| --- | --- |
| `deleting` | The instance is being deleted. |
| `crash-loop-halted` | The VM crashed repeatedly and was halted. Needs your help. See [Health checks](/operations/health-checks). |
| `incompatible-parameters` | A requested change was rejected. The existing database is unaffected. |
| `stopping` / `stopped` | `spec.running` is `false`. Storage is kept. |
| `degraded` | A running database has a problem, or its monitoring failed to deploy. |
| `modifying` | A resize or repave is in progress. |
| `starting` | The VM is starting, or the database isn't `Ready` yet after creation. |
| `available` | `Ready` is `True`. |
| `creating` | Initial provisioning. |

## Conditions

Each condition has `type`, `status`, `reason`, `message` and `lastTransitionTime`. Reasons are stable identifiers. The message has the detail.

| Type | Meaning |
| --- | --- |
| `Ready` | **The one to wait on.** `DatabaseReady` and `MonitoringReady` are both `True`. |
| `Accepted` | The current spec is valid. A rejected edit can leave `Ready=True`. |
| `PreflightReady` | The class, network, fixed fields, image and engine version are valid. |
| `CredentialsReady` | Credentials and certificates exist. |
| `VMReady` | The VM exists. |
| `PowerStateReady` | The VM's power state matches `spec.running`. |
| `StorageReady` | The VM size and disk match the spec. |
| `DatabaseReady` | PostgreSQL is reachable right now. |
| `MonitoringReady` | The metrics Service, Endpoints and ServiceMonitor exist. |
| `Degraded` | A running database is unhealthy. Reported only. The operator never restarts it. |
| `CrashLoopHalted` | The VM was halted after repeated crashes. |
| `InterventionRequired` | An administrator must act. Today this means a crash-loop halt. |
| `DeletionBlocked` | Deletion is blocked or in progress. |
| `ResizeInProgress` / `RepaveInProgress` | A resize or repave is running. Absent when idle. |
| `ImageDrift` | The VM's image differs from the current one. Reported only. |

### `PreflightReady`: reasons when it's `False`

| Reason | What to do |
| --- | --- |
| `InvalidClass` | The `dbInstanceClass` isn't in the class list. Use a listed one. |
| `NetworkRefMissing` | `spec.networkRef` is empty. |
| `ImmutableFieldChanged` | A fixed field was changed after creation. Revert it, or recreate the instance. |
| `OSImageInvalid` | The OS image or engine version isn't available. See [Prerequisites](/installation/prerequisites). |
| `OSImageNotFound` | The image doesn't exist in Harvester. |
| `OSImageNotReady` | The image is still importing. It retries by itself. |

### `CredentialsReady`

`True` (`CredentialsProvisioned`) when the credentials and certificates exist. `CredentialsCreated` appears briefly right after they are generated. `False` with `CredentialsResolveFailed` is a temporary error and the operator retries. See [Credentials](/security/credentials).

### `Accepted`

Reports whether the current spec is valid, separately from the health of a running database. A rejected edit can leave `Ready=True`.

| Status | Reason | Meaning |
| --- | --- | --- |
| `True` | `SpecAccepted` | The spec passed preflight and no storage change was refused |
| `False` | the failing `PreflightReady` reason, or `UnsupportedShrink` | See the `PreflightReady` table above |
| `Unknown` | `ValidationPending` | Validation hasn't finished for this generation |
| `False` | `UnknownValidationFailure` | A failure with an unrecognised reason. Read the message. |

### `VMReady` and `PowerStateReady`

| Condition | Status and reason | Meaning |
| --- | --- | --- |
| `VMReady` | `True`, `VMPresent` | The VM exists |
| `VMReady` | `False`, `VMCreated` | The VM was just created. Normal. |
| `VMReady` | `False`, `VMCreateFailed` | Creating the VM failed. The message has the error. It retries. |
| `PowerStateReady` | `True`, `Running` / `Stopped` | The VM matches `spec.running` |
| `PowerStateReady` | `False`, `Starting` / `StartWaitingForTeardown` | Starting, or waiting for the previous VM instance to go away |
| `PowerStateReady` | `False`, `Stopping` | Stopping |
| `PowerStateReady` | `False`, `CrashLoopHalted` | Power management is suspended after a crash-loop halt |

### `CrashLoopHalted` and `InterventionRequired`

`CrashLoopHalted` is `True` (`CrashLoopDetected`) after 3 unplanned restarts, each within 10 minutes of the previous one. The VM is stopped. `InterventionRequired` is then `True` with the same message, and `False` (`NoInterventionRequired`) otherwise. Recovery steps are in [Health checks](/operations/health-checks) and [Troubleshooting](/troubleshooting).

### `DatabaseReady`: reasons when it's `False`

| Reason | Meaning |
| --- | --- |
| `VMBooting` | The VM is starting. |
| `PostgresInitializing` | The VM is up and PostgreSQL isn't ready yet. |
| `Stopped` / `Stopping` | The VM is stopped or stopping. |
| `ResizeStopping`, `ResizeWaitingForTeardown`, `ResizeApplied` | A resize is in progress. |
| `RepaveStopping`, `RepaveWaitingForTeardown`, `RepaveWaitingForSnapshotHold`, `RepaveApplied` | A repave is in progress. |
| `CrashLoopDetected` | The VM was halted for crash-looping. |
| `PostgresUnreachable`, `VMRestarting`, `GuestAgentDisconnected` | A problem on a running database (the same reasons as `Degraded`). |

### `MonitoringReady`: reasons when it's `False`

| Reason | Meaning |
| --- | --- |
| `WaitingForEndpoint` | The database has no address yet. |
| `InstanceStopped` | The instance is stopped. |
| `MonitoringDeployFailed` | Creating a monitoring object failed, for example when the ServiceMonitor CRD is missing. |

### `Ready`

`True` (`DBInstanceReady`) when the database and monitoring are healthy. When `False`, its reason is `Deleting`, `Stopped`, or the reason of whichever condition isn't `True` (`Provisioning` if that is unknown).

### `Degraded` (only on running databases)

| Reason | Cause |
| --- | --- |
| `PostgresUnreachable` | The readiness probe is failing. |
| `VMRestarting` | The VM isn't running, or has no IP. |
| `GuestAgentDisconnected` | The guest agent is down, so health is unknown. |

It clears when the database is healthy, stopped, resizing or halted. See [Health checks](/operations/health-checks).

### `DeletionBlocked`

| Reason | Meaning |
| --- | --- |
| `DeletionProtected` | `spec.deletionProtection` is `true`. Set it to `false`. |
| `DeletionWaitingForSnapshot` | A backup of this instance is still running. Deletion waits. |
| `DeletionWaitingForVM` | The VM is still being removed. Its data and OS disks are deleted along with it. |
| `TeardownFailed` / `OperatorSecretCleanupFailed` | A cleanup step failed. It retries. |
| `DeletionProgressing` (`status=False`) | Cleanup is in progress. |

### `ImageDrift`

| Status and reason | Meaning |
| --- | --- |
| `True`, `OSUpdateAvailable` | A newer database image is available. Annotate with `repave-trigger` to apply it. |
| `True`, `EngineVersionEOL` | A newer image exists but no longer has this engine version. Repave is blocked. Migrate the data first. |
| `False`, `ImageUpToDate` | The VM runs the current image. |
| `Unknown` | The image catalog has no validated stream, or the VM's image isn't known yet. |

Branch on `status`, not on whether the condition exists. See [Images and repave](/operations/images-and-repave).

### `RepaveInProgress`

| Status and reason | Meaning |
| --- | --- |
| `True`: `RepaveStopping`, `RepaveWaitingForTeardown`, `RepaveWaitingForSnapshotHold`, `RepaveApplied` | A repave is under way. |
| `False`, `RepaveNotAvailable` | A trigger arrived while the instance wasn't `available`. It was ignored. |
| `False`, `RepaveBlockedEOL` | The engine version isn't in the new image. The trigger was ignored. |

### Resize conditions

`StorageReady` is `True` (`ShapeConverged`) when the size matches the spec. `ResizeInProgress` is `True` during a resize. A request to shrink storage sets `StorageChangeRejected=True` (`UnsupportedShrink`), and the phase becomes `incompatible-parameters`. See [Resize and power](/operations/resize-and-power).

## Events

The operator raises Kubernetes events (`kubectl get events --field-selector involvedObject.name=<name>`). Backup and restore events are described on their own pages.

| Type | Reason | Meaning |
| --- | --- | --- |
| Warning | `PostgresUnreachable`, `VMRestarting`, `GuestAgentDisconnected` | The instance became degraded, or the cause changed. |
| Warning | `CrashLoopDetected` | The VM was halted for crash-looping. |
| Normal | `Recovered` | The instance recovered after a crash-loop halt. |
| Normal | `DeletionProgressing` | Deletion started. |
| Warning | `TeardownFailed`, `OperatorSecretCleanupFailed` | A deletion step failed. |
| Normal | `ScheduledSnapshotCreated` | An automated snapshot was created. |
| Warning | `ScheduledSnapshotSkipped` | A scheduled snapshot was skipped because the instance wasn't `available`. |
| Normal | `ScheduledSnapshotPruned` | An old automated snapshot was deleted. |
| Normal | `DeletionWaitingForSnapshot` | Deletion is waiting for a running backup. |
| Warning | `StaleSnapshotHoldReleased` | A leftover backup lock was cleaned up during deletion. |

More in [Snapshots](/backup-restore/snapshots#the-ready-condition) and [Restore](/backup-restore/restore#reasons).
