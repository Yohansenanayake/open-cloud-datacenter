---
title: Status and conditions
sidebar_position: 2
---

# Status and conditions reference

The operator reports everything it observes in `status`. `status.phase` is a human-friendly summary that is always derived from the conditions; it is never an independent state machine and never gates reconciliation. Scripts should wait on conditions (for example `Ready`), not on the phase.

## kubectl output

The CRD defines these printer columns:

| Column | Source | Priority |
| --- | --- | --- |
| `Phase` | `.status.phase` | default |
| `Class` | `.spec.dbInstanceClass` | default |
| `Endpoint` | `.status.endpoint.address` | default |
| `ImageDrift` | status of the `ImageDrift` condition | default |
| `ImageDriftReason` | reason of the `ImageDrift` condition | 1 (shown with `-o wide`) |
| `Age` | `.metadata.creationTimestamp` | default |

```text
$ kubectl get dbi
NAME                PHASE       CLASS          ENDPOINT        IMAGEDRIFT   AGE
dbinstance-sample   available   db.t3.medium   192.168.40.50   False        12m
```

Wait for readiness with:

```bash
kubectl wait --for=condition=Ready dbinstance/dbinstance-sample --timeout=15m
```

## status fields

| Field | Type | Meaning |
| --- | --- | --- |
| `phase` | string | Derived summary, see [Phases](#phases). |
| `message` | string | Human-readable description matching the phase, written together with it on every reconcile. |
| `conditions` | list of `metav1.Condition` (map keyed by `type`) | See [Conditions](#conditions). Each condition carries `observedGeneration` of the spec it describes. |
| `observedGeneration` | integer | Advanced to `metadata.generation` only after every ensure step has converged for that generation. A value behind `metadata.generation` means a change is still being applied or was rejected. |
| `endpoint.address` | string | Data-network IP of the VM, set once PostgreSQL is ready and refreshed on restart or live migration. |
| `endpoint.port` | integer | PostgreSQL port (`spec.port` or the configured default). |
| `endpoint.jdbcUrl` | string | `jdbc:postgresql://<address>:<port>/<dbName>?ssl=true&sslmode=verify-ca`. |
| `appliedSpec` | object | Snapshot of immutable fields captured when the VM is created: `networkRef`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `vmPassword`, `staticNetwork`. Used to detect and refuse immutable edits. Absent until the VM has been created. |
| `currentImageRevision` | string | Baked image revision the VM runs. Set at VM creation and after each repave; compared with the catalog to compute `ImageDrift`. |
| `lastAppliedRepaveTrigger` | string | Last value of the `dbaas.opencloud.wso2.com/repave-trigger` annotation that was processed (applied, rejected or no-op). A repave starts only when the annotation differs from this field. |
| `credentials` | object | Where the accepted master password came from, see [Credentials status](#credentials-status). Never contains the password. |
| `resources` | object | Names of managed child objects, see [Resource references](#resource-references). |
| `grafanaUrl` | string | Per-instance dashboard URL: `<grafana.baseUrl>/d/dbaas-<name>/postgresql-<name>`. Empty when no Grafana base URL is configured. Set when monitoring converges. |
| `prometheusTarget` | string | Scrape target `pg-<name>-metrics.<namespace>.svc:9187`. Set when monitoring converges. |
| `readReplicas` | list of string | Declared in the schema; the controller never writes it in v0.1.0. |
| `restartCount` | integer | Cumulative count of unplanned VM restarts detected by the liveness loop (VMI UID changes). Observability only. |
| `lastKnownVMIUID` | string | UID of the VMI last observed. A change while running indicates an unplanned restart. Cleared on a controller-initiated start. |
| `lastUnplannedRestartTime` | time | When the last unplanned restart was seen. Input to crash-loop detection. |
| `recentUnplannedRestarts` | integer | Length of the current chain of unplanned restarts, each within 10 minutes of the previous. Reaching 3 halts the VM (see `CrashLoopHalted`). Reset by a recovery or a controller-initiated start. |

### Credentials status

| Field | Meaning |
| --- | --- |
| `source` | `Generated` (controller-generated password) or `UserProvidedSecret` (copied from `spec.credentials.passwordSource.secretRef`). Enum-validated. |
| `sourceSecretName` | Name of the user's Secret, when `source` is `UserProvidedSecret`. |
| `sourceUID` | UID of that Secret when the password was accepted. |
| `sourceResourceVersion` | Its `resourceVersion` when accepted. |
| `sourceChanged` | `true` once the source Secret's UID or `resourceVersion` differs from what was recorded. The database password is not affected. Set once, with one `Warning` event. Includes label and annotation edits and delete-and-recreate; it does not necessarily mean the password changed. A deleted or unreadable source is not reported. |

### Resource references

| Field | Meaning |
| --- | --- |
| `nadName` | The `spec.networkRef` the VM attaches to (recorded after preflight passes). |
| `dataVolumeName` | Name of the data volume, `pg-<id>-data`, where `<id>` is the first 8 characters of the DBInstance UID without dashes. |
| `osDiskPVCName` | Exact current OS disk PVC: `pg-<id>-os` at first provision, `pg-<id>-os-<image>` after a repave. Authoritative for teardown. |
| `pendingDeleteOSDiskPVCName` | Old OS disk PVC recorded after a repave swap and cleared once its deletion succeeds. Non-empty means a deletion is pending retry. |
| `vmName` | Name of the VirtualMachine, `pg-<name>`. |
| `adminCredentialsSecretName` | Tenant Secret `pg-<name>-credentials`. |
| `cloudInitSecretName` | Ephemeral Secret `pg-<name>-cloudinit`. |
| `serviceMonitor` | ServiceMonitor `pg-<name>-monitor`. |
| `metricsServiceName` | Headless Service `pg-<name>-metrics`. |
| `connectionSecretName` | Tenant Secret `pg-<name>-connect`, recorded once the endpoint is known. |
| `internalSecretRef` | `namespace/name` of the operator-namespace Secret `dbi-<uid>-internal`. |
| `privateTLSSecretRef` | `namespace/name` of the operator-namespace Secret `dbi-<uid>-tls`. |

## Phases

`status.phase` takes one of the following lowercase values. The controller picks the first matching rule from the top.

| Phase | Chosen when |
| --- | --- |
| `deleting` | `metadata.deletionTimestamp` is set. Message comes from `DeletionBlocked`. |
| `crash-loop-halted` | Condition `CrashLoopHalted` is `True`. |
| `incompatible-parameters` | `Accepted` is `False` for the current generation: a requested change was rejected. The existing database, if any, is unaffected. |
| `stopping` | `spec.running` is `false` and `PowerStateReady` is `False` for the current generation. |
| `stopped` | `spec.running` is `false` and `PowerStateReady` is `True`. Storage is preserved. |
| `degraded` | `Degraded` is `True`. Report only; the controller never restarts on degradation. Also used when the database is available (`DatabaseReady=True`) but `MonitoringReady` is `False` with reason `MonitoringDeployFailed`. |
| `modifying` | `ResizeInProgress` or `RepaveInProgress` is `True`. |
| `starting` | `spec.running` is `true` and has been reconciled at least once, and either `PowerStateReady` is `False` with reason `Starting` or `StartWaitingForTeardown`, or `Ready` is `False` for the current generation. |
| `available` | `Ready` is `True`. |
| `creating` | None of the above (initial provisioning). |

The constant `failed` is defined in the API package but is never produced by `DerivePhaseSummary`.

## Conditions

All conditions use the standard `metav1.Condition` shape (`type`, `status`, `reason`, `message`, `observedGeneration`, `lastTransitionTime`). `lastTransitionTime` changes only when `status` changes. Reasons are stable CamelCase identifiers; the message carries the detail.

The reconcile runs an ordered chain of steps and stops at the first step that is not satisfied: preflight, credentials, VM, resize, repave, power, health, connection secret, monitoring, bootstrap cleanup, generation. Conditions owned by later steps therefore stay unset until earlier steps pass.

### Summary

| Type | Meaning | Set by |
| --- | --- | --- |
| `Accepted` | The current spec is valid and supported. | derived from `PreflightReady` and `StorageChangeRejected` |
| `PreflightReady` | Instance class, network reference, immutability, OS image and engine version are valid. | preflight |
| `CredentialsReady` | Credential and TLS material exists. | credentials |
| `VMReady` | The VirtualMachine exists. | VM |
| `PowerStateReady` | VM power state matches `spec.running`. | power |
| `StorageReady` | VM class and storage match the spec. | resize |
| `StorageChangeRejected` | A storage change was refused (abnormal-only). | resize |
| `ResizeInProgress` | A cold resize is in flight (activity condition). | resize |
| `DatabaseReady` | PostgreSQL is reachable. | health, resize, repave, power |
| `MonitoringReady` | Metrics Service, Endpoints and ServiceMonitor exist. | monitoring |
| `Ready` | Aggregate: `DatabaseReady` and `MonitoringReady` are both `True`. | derived every pass |
| `InterventionRequired` | An administrator must act. | derived |
| `CrashLoopHalted` | VM was halted after repeated unplanned restarts. | health, power |
| `Degraded` | Provisioned and expected to serve, but unhealthy (report only). | health |
| `DeletionBlocked` | Teardown is blocked or in progress. | deletion handler |
| `ImageDrift` | The VM image differs from the catalog (report only, three-valued). | repave |
| `RepaveInProgress` | A repave is in flight (activity condition). | repave |

### `Accepted`

Derived each pass. It reports spec validation, separate from the health of the running database, so a rejected edit can leave `Ready=True`.

| Status | Reason | When |
| --- | --- | --- |
| `Unknown` | `ValidationPending` | `PreflightReady` is absent or `Unknown` for this generation, or `StorageChangeRejected` is `Unknown`. |
| `False` | the reason of the failing `PreflightReady` (for example `InvalidClass`), or `UnsupportedShrink` | Preflight failed, or a storage change was rejected. Reasons not in the known list are reported as `UnknownValidationFailure`. |
| `True` | `SpecAccepted` | Preflight passed and no storage change is rejected. |

### `PreflightReady`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `PreflightPassed` | Instance class, network reference and OS image are valid. |
| `False` | `InvalidClass` | `spec.dbInstanceClass` is not in the instance class catalog. |
| `False` | `NetworkRefMissing` | `spec.networkRef` is empty. |
| `False` | `VMPasswordNotAllowed` | `spec.vmPassword` is set on a never-provisioned instance while `security.rejectVMPassword` is enabled. |
| `False` | `ImmutableFieldChanged` | One of `networkRef`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `vmPassword`, `staticNetwork` differs from `status.appliedSpec`. Message lists the fields. |
| `False` | `OSImageInvalid` | The configured OS stream is not available or not validated, `spec.engineVersion` is not supported by the image, or the image reference is invalid or ambiguous. Only evaluated before the VM exists. |
| `False` | `OSImageNotFound` | The Harvester image does not exist. Only evaluated before the VM exists. |
| `Unknown` | `OSImageNotReady` | The Harvester image is still importing; retried every poll. |
| `Unknown` | `ValidationPending` | The image could not be validated because of a transient API error; retried with backoff. |

### `CredentialsReady`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `CredentialsCreated` | Credential or TLS material was just created; the controller waits one cycle to observe it. |
| `True` | `CredentialsProvisioned` | Admin credentials, internal credentials and TLS material exist and were observed. |
| `False` | `CredentialsLost` | A durable Secret is missing for an already-provisioned database. A `Warning` event is emitted once. `InterventionRequired` becomes `True`. Polled every 30 seconds. |
| `False` | `PasswordSourceNotFound` | `spec.credentials` Secret does not exist in the namespace. Polled every 30 seconds. |
| `False` | `PasswordSourceInvalid` | The referenced Secret has the wrong type, lacks the key, the value breaks the password rules, the Secret name is reserved, or the master username is reserved. Polled every 30 seconds. |
| `False` | `CredentialsResolveFailed` | Any other error while resolving credentials; retried with backoff. |

### `VMReady`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `VMPresent` | The VirtualMachine exists. |
| `False` | `VMCreated` | VM was just created; waiting for it to register. |
| `False` | `VMCreateFailed` | Creating the VM failed (message holds the error); retried with backoff. |

The VM step also re-checks `OSImageInvalid`, `InvalidClass` and `VMPasswordNotAllowed` defensively and reports them on `PreflightReady`.

### `PowerStateReady`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `Running` | `spec.running` is `true` and the VMI is running. |
| `True` | `Stopped` | `spec.running` is `false` and the VM is stopped. |
| `False` | `Starting` | Start requested, or waiting for the VMI to run. |
| `False` | `StartWaitingForTeardown` | Waiting for the previous VMI to finish stopping before starting. |
| `False` | `Stopping` | Stop requested, or waiting for VMI teardown. |
| `False` | `CrashLoopHalted` | Power management is suspended while the instance is crash-loop halted. |

### `StorageReady`, `StorageChangeRejected`, `ResizeInProgress`

| Type | Status | Reason | When |
| --- | --- | --- | --- |
| `StorageReady` | `True` | `ShapeConverged` | VM class and storage match the spec. |
| `StorageReady` | `False` | `ResizeStopping` | Stopping the VM for a cold resize. |
| `StorageReady` | `False` | `ResizeWaitingForTeardown` | Waiting for the VM to stop. |
| `StorageReady` | `False` | `ResizeApplied` | Resize applied; waiting for the next pass to confirm. |
| `StorageReady` | `Unknown` | `UnsupportedShrink` | A shrink was rejected. |
| `StorageChangeRejected` | `True` | `UnsupportedShrink` | `spec.allocatedStorage` is below the provisioned size. Removed as soon as the requested shape is supported. Its absence is the healthy state. |
| `ResizeInProgress` | `True` | `ResizeStopping`, `ResizeWaitingForTeardown`, `ResizeApplied` | A resize is in flight. Removed once the new shape has converged and the instance has settled (`Ready=True` when running, `PowerStateReady=True` when stopped). Absent while idle. |

`PreflightReady=False` with reason `InvalidClass` is also written by the resize step as a defensive check.

### `DatabaseReady`

The narrow signal for "PostgreSQL is reachable right now". It is set by the step that takes the VM down, or by the health step.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `PostgresReady` | The readiness probe passes. Any `Degraded` is cleared and `status.endpoint` is refreshed. |
| `False` | `VMBooting` | Provisioning or catching up a change: VMI not running or no data-network IP yet. |
| `False` | `PostgresInitializing` | VMI running, readiness probe not passing yet, while a generation is still being applied. |
| `False` | `Stopped` | `spec.running` is `false`. |
| `False` | `Stopping` | Power step is stopping the VM. |
| `False` | `ResizeStopping`, `ResizeWaitingForTeardown`, `ResizeApplied` | Set by the resize step. |
| `False` | `RepaveStopping`, `RepaveWaitingForTeardown`, `RepaveApplied` | Set by the repave step. |
| `False` | `CrashLoopDetected` | The VM was halted for a crash loop. |
| `False` | `PostgresUnreachable`, `VMRestarting`, `GuestAgentDisconnected` | Steady-state failure; same reasons as `Degraded`. |

### `MonitoringReady`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `MonitoringDeployed` | Metrics Service, Endpoints and ServiceMonitor were reconciled. |
| `False` | `WaitingForEndpoint` | No database endpoint yet. |
| `False` | `InstanceStopped` | `spec.running` is `false`. Monitoring objects are retained but not retargeted. |
| `False` | `MonitoringDeployFailed` | Creating or updating a monitoring object failed (for example the ServiceMonitor CRD is missing). |

### `Ready`

Derived from `DatabaseReady` and `MonitoringReady` on every pass, regardless of where the step chain stopped. It deliberately ignores spec validation conditions.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `DBInstanceReady` | `DatabaseReady` and `MonitoringReady` are both `True`. |
| `False` | `Deleting` | The instance is being deleted. |
| `False` | `Stopped` | `spec.running` is `false`. |
| `False` | the reason of `DatabaseReady` or `MonitoringReady` | Whichever is not `True`, with its message. If the condition is missing: `Provisioning`. |

### `InterventionRequired`

Always present once derived.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `InterventionRequired` | `CrashLoopHalted` is `True`, or `CredentialsReady` is `False` with reason `CredentialsLost`. The message is copied from that condition. |
| `False` | `NoInterventionRequired` | Otherwise. |

### `CrashLoopHalted`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `CrashLoopDetected` | 3 unplanned restarts, each within 10 minutes of the previous, were detected. The VM is stopped and the controller parks (rechecks every 30 seconds). A `Warning` event is emitted. Recovery: repair the VM and start it out of band; once a new healthy VMI with an IP is observed the condition is removed and a `Normal` event with reason `Recovered` is emitted. |

The condition is removed rather than set to `False`.

### `Degraded`

Report only: the controller never restarts the VM because of degradation. Set only after the current generation has been reconciled; during provisioning the same failures are shown on `DatabaseReady` instead.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `PostgresUnreachable` | Readiness probe failing; database not accepting connections. |
| `True` | `VMRestarting` | VMI not running or has no IP; VM restarting or halted out of band. |
| `True` | `GuestAgentDisconnected` | Guest agent is disconnected, so the readiness probe cannot run and database health is unknown. |

A `Warning` event with the same reason is emitted when entering `Degraded` or when the cause changes. The condition is removed when the database is healthy, when the VM is deliberately stopped, during a resize and when a crash-loop halt begins.

### `DeletionBlocked`

| Status | Reason | When |
| --- | --- | --- |
| `True` | `DeletionProtected` | Deletion was requested but `spec.deletionProtection` is `true`. |
| `True` | `TeardownFailed` | Deleting Harvester resources failed; retried. `Warning` event. |
| `True` | `OperatorSecretCleanupFailed` | Deleting the operator-namespace private Secrets failed; retried. `Warning` event. |
| `False` | `DeletionProgressing` | Teardown is under way. `Normal` event. |

### `ImageDrift`

Report only and three-valued. Consumers must branch on `status`, never on the condition's presence.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `OSUpdateAvailable` | A newer validated image revision is available and the instance's engine version exists in it. Annotate with `dbaas.opencloud.wso2.com/repave-trigger` to repave. |
| `True` | `EngineVersionEOL` | A newer revision exists but no longer carries the instance's engine version. Repave is blocked; migrate data first. |
| `False` | `ImageUpToDate` | The VM runs the current revision (also before the VM exists, and after a repave). |
| `Unknown` | `ImageCatalogUnresolved` | No validated image stream for the configured OS version. |
| `Unknown` | `CurrentImageRevisionUnknown` | The VM exists but its revision has not been observed yet. |

### `RepaveInProgress`

Activity condition, independent of `ImageDrift`. Removed once `ImageDrift` is no longer `True` and the instance has settled (`Ready=True` when running, `PowerStateReady=True` when stopped). Absent while idle.

| Status | Reason | When |
| --- | --- | --- |
| `True` | `RepaveStopping` | Stopping the VM for a repave. |
| `True` | `RepaveWaitingForTeardown` | Waiting for the VM to stop. |
| `True` | `RepaveApplied` | OS disk swapped; waiting for the VM to restart on the new image. |
| `False` | `RepaveNotAvailable` | A trigger arrived while the phase was not `available`; it is recorded as handled and ignored. |
| `False` | `RepaveBlockedEOL` | The engine version is not available in the new revision; trigger recorded and ignored. |

See [Images and repave](/operations/images-and-repave).

## Reasons used only for events or step results

| Reason | Used for |
| --- | --- | 
| `PasswordSourceChanged` | `Warning` event when `status.credentials.sourceChanged` flips to `true`. |
| `Recovered` | `Normal` event after crash-loop recovery. |
| `VMRestarting` | Also a `Warning` event on each unplanned VMI restart (UID change). |
| `CrashLoopDetected` | Also a `Warning` event when the VM is halted. |
| `ConnectionSecretReconciled`, `BootstrapCleanupReconciled` | Pending results of the connection-secret and bootstrap-cleanup steps; logged only, no condition is written. |
| `WaitingForEndpoint`, `CredentialsCreated` | Also returned as step results by the connection-secret step. |

The following reasons are defined in the API package but not written anywhere in the controller: `FinalizerAdded`, `RepaveInvalidStream`.

## Condition ownership

The DBInstance controller owns and patches these types: `Accepted`, `PreflightReady`, `CredentialsReady`, `VMReady`, `PowerStateReady`, `StorageReady`, `StorageChangeRejected`, `ResizeInProgress`, `DatabaseReady`, `MonitoringReady`, `Ready`, `InterventionRequired`, `CrashLoopHalted`, `Degraded`, `DeletionBlocked`. `ImageDrift` and `RepaveInProgress` are written by the repave step but are not in that ownership list.

## Events

The controller emits Kubernetes events under the component `dbaas-controller`:

| Type | Reason | Meaning |
| --- | --- | --- |
| Normal | `DeletionProgressing` | Teardown started. |
| Warning | `TeardownFailed`, `OperatorSecretCleanupFailed` | Teardown error. |
| Warning | `CredentialsLost` | A durable credential Secret is missing (once per episode). |
| Warning | `PasswordSourceChanged` | The source Secret changed after acceptance. |
| Warning | `VMRestarting` | Unplanned VMI restart detected. |
| Warning | `CrashLoopDetected` | Crash-loop halt. |
| Normal | `Recovered` | Recovered after a crash-loop halt. |
| Warning | `PostgresUnreachable`, `VMRestarting`, `GuestAgentDisconnected` | Entering or changing `Degraded`. |

:::info Verified against
- `database/api/v1alpha1/dbinstance_types.go`
- `database/api/v1alpha1/dbinstance_conditions.go`
- `database/config/crd/bases/dbaas.opencloud.wso2.com_dbinstances.yaml`
- `database/internal/controller/status.go`
- `database/internal/controller/status_conditions.go`
- `database/internal/controller/status_ready.go`
- `database/internal/controller/dbinstance_controller.go`
- `database/internal/ensure/preflight.go`
- `database/internal/ensure/credentials.go`
- `database/internal/ensure/vm.go`
- `database/internal/ensure/resize.go`
- `database/internal/ensure/repave.go`
- `database/internal/ensure/power.go`
- `database/internal/ensure/health.go`
- `database/internal/ensure/monitoring.go`
- `database/internal/ensure/connection_secret.go`
- `database/internal/ensure/bootstrap_cleanup.go`
- `database/internal/ensure/runner.go`
:::
