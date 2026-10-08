---
title: Troubleshooting
sidebar_position: 9
---

# Troubleshooting

This page is organised by what you see: the `phase`, the failing condition, or the condition `reason`. Every reason below is defined in `api/v1alpha1/dbinstance_conditions.go` and is set by a code path in `internal/`.

## Start here: read the status

Replace `NS` and `NAME` with the namespace and name of your `DBInstance`.

```sh
# One-line summary: phase and message
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.phase}{"  "}{.status.message}{"\n"}'

# Every condition with its reason and message
kubectl get dbinstance NAME -n NS \
  -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'

# Conditions, events and child resources together
kubectl describe dbinstance NAME -n NS

# Events only, oldest first
kubectl get events -n NS --field-selector involvedObject.name=NAME --sort-by=.lastTimestamp
```

Condition types owned by the operator: `Accepted`, `PreflightReady`, `CredentialsReady`, `VMReady`, `PowerStateReady`, `StorageReady`, `StorageChangeRejected`, `ResizeInProgress`, `DatabaseReady`, `MonitoringReady`, `Ready`, `InterventionRequired`, `CrashLoopHalted`, `Degraded`, `DeletionBlocked`. Two further report-only types, `ImageDrift` and `RepaveInProgress`, are written by the repave step. See [Status and conditions](/reference/status-and-conditions) for the full contract.

### How the reconciler stops

Each reconcile walks a fixed chain of steps and stops at the first step that is not satisfied: preflight, credentials, VM, resize, repave, power, health, connection secret, monitoring, bootstrap cleanup, generation. The failing step is almost always the one whose condition is `False` or `Unknown`. A step ends in one of four ways:

| Outcome | Meaning | What you do |
| --- | --- | --- |
| Pending | Waiting for something to converge. The step requeues itself on a timer or is woken by a watch event. | Usually nothing. Wait, then check events if it takes too long. |
| Terminal | The spec or environment is wrong. The step does not retry on a timer. | Fix the cause. A spec edit or an operator restart re-runs the chain. |
| Transient | An API call failed. The error is returned and controller-runtime retries with backoff. | Look at the operator log for the error. |
| Satisfied | Step done, the chain continues. | Nothing. |

### Phases

`status.phase` is descriptive only and never gates reconciliation. The values are `creating`, `available`, `stopping`, `stopped`, `starting`, `modifying`, `deleting`, `failed`, `degraded`, `incompatible-parameters` and `crash-loop-halted`. The phase is derived in this priority order: deleting, crash-loop halted, `Accepted=False` (`incompatible-parameters`), stopping or stopped, `degraded`, `modifying` (resize or repave in progress), `degraded` (monitoring deploy failed while the database is up), `starting`, `available`, otherwise `creating`.

## Operator logs

The operator runs as a Deployment named `dbaas-operator-controller-manager` in the install namespace (`dbaas-system` for the Helm chart and Harvester Addon install). Names can differ if you used another release name or the kustomize install.

```sh
kubectl -n dbaas-system get pods
kubectl -n dbaas-system logs deploy/dbaas-operator-controller-manager --tail=200
kubectl -n dbaas-system logs deploy/dbaas-operator-controller-manager -f | grep NAME
```

Logs are JSON by default. Increase verbosity with the `--logging.level` flag (see [Operator configuration](/configuration/operator-config)). At debug level the runner logs every step that stopped, with the step name, outcome, reason, message and requeue interval ("Ensure step stopped"). That line is the fastest way to see which step a stuck instance is parked on.

If the manager pod itself is not running:

```sh
kubectl -n dbaas-system describe pod -l control-plane=controller-manager
kubectl get addon dbaas-operator -n dbaas-system -o jsonpath='{.status.status}{"\n"}'
```

An `ImagePullBackOff` with `not found` means the image tag was never pushed. `unauthorized` means the registry package is private and no pull secret is configured. See [Installation known gotchas](/installation/known-gotchas) and [Helm and Harvester Addon install](/installation/helm-addon).

## Events

The controller emits events under the source `dbaas-controller`. Events are the place to look for one-shot signals that are not repeated every poll.

| Type | Reason | Emitted when |
| --- | --- | --- |
| Normal | `DeletionProgressing` | Teardown has started. Message: "Tearing down database resources". |
| Warning | `TeardownFailed` | Deleting the VM, monitoring objects or tenant Secrets failed. Retried. |
| Warning | `OperatorSecretCleanupFailed` | Deleting the two operator-namespace Secrets failed. Retried. |
| Warning | `CredentialsLost` | A saved credential Secret is missing for a provisioned database. Emitted once, on entering the state. |
| Warning | `PasswordSourceChanged` | The Secret named in `spec.credentials` changed after its password was accepted. Emitted once. The database password is not changed. |
| Warning | `VMRestarting` | A new VMI UID was observed: an unplanned restart. Also used as a `Degraded` reason. |
| Warning | `CrashLoopDetected` | Three chained unplanned restarts. The VM has been halted. |
| Normal | `Recovered` | After a crash-loop halt, a healthy VM was started out of band and reconciliation resumed. |
| Warning | `PostgresUnreachable`, `GuestAgentDisconnected`, `VMRestarting` | The instance entered `Degraded`, or the cause changed. Not repeated while the cause is unchanged. |

```sh
kubectl get events -n NS --field-selector reason=CrashLoopDetected
```

## Symptoms by condition reason

### `Accepted=False` and phase `incompatible-parameters`

`Accepted` copies the reason and message of a failing `PreflightReady` (or of `StorageChangeRejected`). The existing database, if there is one, is not touched by a rejected edit. Find the real reason:

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="PreflightReady")]}{"\n"}'
```

If the copied reason is not one the controller recognises, `Accepted` shows `UnknownValidationFailure`. Read the message.

| Reason | Cause | Fix |
| --- | --- | --- |
| `InvalidClass` | `spec.dbInstanceClass` is not in the operator's instance class table. Message: `unknown dbInstanceClass "..."`. | Use a class that exists. See [DBInstance spec](/reference/dbinstance-spec). Terminal. |
| `NetworkRefMissing` | `spec.networkRef` is empty. The operator does not check that the NAD exists, only that the field is set. | Set `networkRef` to `namespace/nad-name` of an existing Multus NetworkAttachmentDefinition (`kubectl get network-attachment-definitions -A`). A wrong name passes preflight and fails later at VM boot. Terminal. |
| `VMPasswordNotAllowed` | The operator runs with `security.rejectVMPassword=true`, the instance has never been provisioned, and `spec.vmPassword` is set. | Remove `spec.vmPassword` and recreate the `DBInstance`, or turn the setting off on a development install. See [Policy switches](/configuration/policy-switches). Terminal. |
| `ImmutableFieldChanged` | After the VM was created, one of `networkRef`, `dbName`, `masterUsername`, `engineVersion`, `port`, `storageType`, `staticNetwork` or `vmPassword` no longer matches `status.appliedSpec`. The CRD also rejects some of these edits at admission with `... is immutable after creation`. | Revert the field, or delete and recreate the instance. Terminal. |
| `OSImageInvalid` | One of: the OS stream selected by `databaseDefaults.osVersion` is not in the compiled-in catalog or is not validated (`OS stream "..." is not available or not validated`); `spec.engineVersion` is not supported by the catalog image (`engineVersion "..." is not available in image revision ...`); or the image reference is malformed or ambiguous in Harvester. | Pick a supported `engineVersion`, or set `databaseDefaults.osVersion` to a validated stream (`22.04` or `24.04` in v0.1.0). Terminal: the catalog is compiled into the binary, so only a new operator build changes it. |
| `OSImageNotFound` | The baked image named by the catalog does not exist as a `VirtualMachineImage` in the image namespace (default `default`). | Upload the image to Harvester with the exact catalog name and wait for it to become Active. See [Images and repave](/operations/images-and-repave). Terminal; restart the operator pod or edit the spec after fixing. |
| `OSImageNotReady` | The image exists but is still importing, or has no usable storage class. `PreflightReady` is `Unknown`. | Wait. This is Pending and retries every 10 seconds. |
| `ValidationPending` | `PreflightReady` is `Unknown` because the image lookup failed with an API error. The operator log has the error. | Check Harvester API reachability and operator RBAC. Retried with backoff. |
| `UnsupportedShrink` | `allocatedStorage` is below the provisioned size. Sets `StorageChangeRejected=True`. The CRD also rejects this at admission (`allocatedStorage can only grow`). | Revert to the current size or larger. Storage cannot shrink. Terminal. |

```sh
# List what the image catalog expects to find in Harvester
kubectl get virtualmachineimages.harvesterhci.io -n default
```

:::note
Preflight image checks only run before the first VM exists. Once a VM is provisioned, a catalog change never fails an existing instance. It is reported through `ImageDrift` instead.
:::

### `CredentialsReady`

| Reason | Status | Cause | Fix |
| --- | --- | --- | --- |
| `CredentialsCreated` | True | Credential material was just generated. The step waits for the next pass to observe it. | None. Appears briefly. |
| `CredentialsProvisioned` | True | Normal steady state. | None. |
| `PasswordSourceNotFound` | False | `spec.credentials` names a Secret that does not exist in the instance namespace. Pending, polled every 30 seconds. | Create the Secret. The operator continues within about 30 seconds. |
| `PasswordSourceInvalid` | False | The Secret exists but is unusable: missing key, empty value, not valid UTF-8, contains NUL, contains a line break, wrong length, or the master username is reserved (`postgres`, `postgres_exporter`, `replicator`, `repl`, `public`, `none`, or any name starting `pg_`). The message names the Secret and key and never the password. Also raised when the Secret name clashes with a DBaaS-owned Secret. | Fix the Secret or the spec. A trailing newline from `echo` is the usual cause: recreate with `kubectl create secret generic ... --from-literal=...`. See [Credentials](/security/credentials). |
| `CredentialsResolveFailed` | False | Any other error reading or creating credential Secrets. Transient, retried with backoff. | Read the message and the operator log. Check operator RBAC on Secrets. |
| `CredentialsLost` | False | A durable Secret (`pg-NAME-credentials`, or `dbi-UID-internal` or `dbi-UID-tls` in the operator namespace) is missing but the VM already exists. The operator refuses to regenerate because a new password or CA would not match the running database. Also sets `InterventionRequired=True` and emits a Warning event. The database keeps serving. | Restore the Secret. See the next section. |

#### Recovering from `CredentialsLost`

```sh
# Which Secret is missing? The message names it.
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="CredentialsReady")].message}{"\n"}'

# Operator-namespace Secrets are labelled with the instance UID
kubectl -n dbaas-system get secrets -l dbaas.opencloud.wso2.com/dbinstance-uid=$(kubectl get dbinstance NAME -n NS -o jsonpath='{.metadata.uid}')
```

If the missing Secret is `pg-NAME-credentials` and you know the password, recreate it with keys `admin_user` and `admin_password`. Otherwise restore from a cluster backup. With no backup, recreate the instance and restore data from a `pg_dump`. The full runbook is in [Credentials](/security/credentials).

### `VMReady`

| Reason | Cause | Fix |
| --- | --- | --- |
| `VMCreated` | The VirtualMachine was just created and the step is waiting to observe it. Normal. | None. |
| `VMPresent` | The VM exists. Normal. | None. |
| `VMCreateFailed` | The call that creates the VM, disks or cloud-init objects failed. The message is the Harvester or Kubernetes error. Retried with backoff. | Typical causes: the StorageClass (default `longhorn`) is missing, quota or capacity is exhausted, or the image is not clonable. Read the message, then `kubectl get pvc,vm,vmi -n NS`. |

```sh
kubectl get vm,vmi,pvc,datavolume -n NS
kubectl describe vm pg-NAME -n NS
```

### Stuck in `creating`, `DatabaseReady=False`

These reasons come from the health step and are Pending.

| Reason | Cause | Fix |
| --- | --- | --- |
| `VMBooting` | The VMI is not running, or has no IP, or the guest agent has not connected. Message: `VM booting; waiting for guest agent and data-net IP`. | Normal for the first few minutes. If it persists, check the VMI and the NAD below. |
| `PostgresInitializing` | The VM has an IP but the KubeVirt readiness probe (`pg_isready` through the guest agent) is not passing. | Normal while cloud-init bootstraps PostgreSQL. If it persists, see the guest checks below. |
| `PostgresReady` | PostgreSQL is ready. | None. |

```sh
kubectl get vmi pg-NAME -n NS -o wide
kubectl describe vmi pg-NAME -n NS
kubectl get vmi pg-NAME -n NS -o jsonpath='{.status.interfaces}{"\n"}'
```

Things that commonly keep the IP empty: a `networkRef` that points at the wrong NAD, no DHCP on that network, or a `staticNetwork` that does not match the network. Because the operator does not validate the NAD, a wrong `networkRef` shows up here and not as `NetworkRefMissing`. The VM data-network IP is what the operator reports in `status.endpoint`.

For guest-side problems, use the Harvester console on the VM (only possible if you set `spec.vmPassword` on a development install) and look at `/var/log/cloud-init-output.log`. With `security.rejectVMPassword` on there is no console login through the operator.

### `Degraded` (phase `degraded`)

The instance was provisioned and was serving, and now a probe is failing. This is report-only: the operator never restarts the VM because of it. `Degraded` and `DatabaseReady` carry the same reason.

| Reason | Cause | Fix |
| --- | --- | --- |
| `PostgresUnreachable` | The VMI is running and the guest agent is connected, but the readiness probe fails. PostgreSQL is down or not accepting connections. | Investigate inside the guest, or restart the VM through the power controls. |
| `GuestAgentDisconnected` | The guest agent is not connected so health cannot be determined. | Check that the VM is running and the agent service is up. |
| `VMRestarting` | The VMI is not running: it is restarting, or was halted out of band. | `kubectl get vm,vmi -n NS`. If the VM was halted by hand, start it. |

`MonitoringDeployFailed` while the database is up also yields phase `degraded`. See the monitoring section.

### `CrashLoopHalted` and `InterventionRequired`

Three unplanned restarts (VMI UID changes), each within 10 minutes of the previous one, make the operator halt the VM. Power management is then suspended: the operator will not start the VM while the condition is set. Phase is `crash-loop-halted`, and `InterventionRequired=True` carries the same message. The operator polls every 30 seconds.

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.restartCount}{" "}{.status.recentUnplannedRestarts}{"\n"}'
kubectl get events -n NS --field-selector reason=VMRestarting
```

Fix the underlying fault (guest out of memory, a bad disk, a failing image), then start the VM yourself, outside the operator, for example with `virtctl start pg-NAME -n NS` or through the Harvester UI. When the operator sees a new, healthy VMI with a guest agent and IP it clears the halt, emits a `Recovered` event and resumes. Setting `spec.running` does not clear a halt.

### Resize problems

Resize is a cold operation: the VM is stopped, resized and started again. See [Resize and power](/operations/resize-and-power).

| Reason | Status of `ResizeInProgress` | Meaning |
| --- | --- | --- |
| `ResizeStopping` | True | The operator asked the VM to stop. |
| `ResizeWaitingForTeardown` | True | Waiting for the VMI to disappear. Polled on a timer. |
| `ResizeApplied` | True | The new CPU, memory or size has been written. The condition stays until the database is `Ready` again. |
| `ShapeConverged` | (`StorageReady=True`) | The VM and disk match the spec. |

The condition is removed only when the new shape is converged and the instance is `Ready` (or `PowerStateReady` when the instance is stopped). If it stays True for a long time, look at which condition is not True: usually `DatabaseReady`, so follow the health section above. A resize with an unknown class ends in `InvalidClass`, and a shrink in `UnsupportedShrink`, both described above.

### Power and start/stop problems

| Reason | Meaning |
| --- | --- |
| `Stopping` | Stop requested or waiting for VMI teardown. |
| `Stopped` | VM stopped, storage preserved. Phase `stopped`. `Ready=False` with reason `Stopped` is expected and not an error. |
| `StartWaitingForTeardown` | A start was requested while the previous VMI was still going away. The operator waits and retries. |
| `Starting` | Start requested, waiting for the VMI to run. Phase `starting`. |
| `Running` | VM running (`PowerStateReady=True`). |

Phase stays `starting` until PostgreSQL is ready, not just until the VMI runs. If it sticks, treat it as `VMBooting` or `PostgresInitializing`.

### Monitoring: `MonitoringReady`

| Reason | Cause | Fix |
| --- | --- | --- |
| `WaitingForEndpoint` | The database has no IP yet, so no metrics Service can be built. Also used by the connection Secret step. | Resolve the health issue first. |
| `MonitoringDeployed` | The metrics Service, Endpoints and ServiceMonitor exist. | None. |
| `MonitoringDeployFailed` | Creating the metrics Service, Endpoints or `ServiceMonitor` failed. A frequent cause is that the Prometheus Operator CRDs (`ServiceMonitor`) are not installed. Phase becomes `degraded` while the database is up. | `kubectl get crd servicemonitors.monitoring.coreos.com`. Install the monitoring stack or read the error in the condition message. See [Monitoring](/monitoring). |
| `InstanceStopped` | The instance is stopped, so the monitoring target is intentionally inactive. | None. |

`Ready` needs both `DatabaseReady=True` and `MonitoringReady=True`. If `Ready` is `False` while the database works, check `MonitoringReady`. The reason on `Ready` is copied from whichever of the two is failing, or is `Provisioning` if the condition has not been written yet.

### Repave and image drift

`ImageDrift` is three-valued. Branch on `status`, not on presence. See [Images and repave](/operations/images-and-repave).

| `ImageDrift` status and reason | Meaning |
| --- | --- |
| True, `OSUpdateAvailable` | A newer validated image exists. Repave by annotating the instance. The message gives the exact annotation. |
| True, `EngineVersionEOL` | The newer image no longer supports your `engineVersion`. Repave is blocked. Migrate data first. |
| False, `ImageUpToDate` | No drift. |
| Unknown, `ImageCatalogUnresolved` | No validated stream for `databaseDefaults.osVersion`, so drift cannot be evaluated. |
| Unknown, `CurrentImageRevisionUnknown` | `status.currentImageRevision` is empty and could not be derived from the VM disk. |

To trigger a repave, set the annotation `dbaas.opencloud.wso2.com/repave-trigger` to any new value, for example `now` the first time and a different value after that. The operator compares it to `status.lastAppliedRepaveTrigger`:

```sh
kubectl annotate dbinstance NAME -n NS dbaas.opencloud.wso2.com/repave-trigger="$(date +%s)" --overwrite
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.lastAppliedRepaveTrigger}{"\n"}'
```

| Reason | Meaning and fix |
| --- | --- |
| `RepaveNotAvailable` | The trigger was ignored because the phase was not `available`. The trigger value is marked handled, so it will not retry on its own. Wait for `available`, then use a new trigger value. |
| `RepaveBlockedEOL` | Your `engineVersion` is not in the target image revision. The trigger is consumed and nothing was changed. Migrate the data to a supported version first. |
| `RepaveStopping`, `RepaveWaitingForTeardown`, `RepaveApplied` | Repave in progress (`RepaveInProgress=True`, phase `modifying`). The condition clears when drift is gone and the instance is `Ready`. |
| `RepaveInvalidStream` | Defined in the API but not set by any code path in v0.1.0. You should not see it. |

### Connection Secret

`pg-NAME-connect` is created once the database has an IP. If it is missing, the step is waiting on `WaitingForEndpoint`, or on `ConnectionSecretReconciled` (a one-pass wait after writing it). See [Connecting](/connecting).

### Deletion stuck

`DeletionBlocked` is the condition that explains a `DBInstance` that does not go away. The finalizer is `dbaas.opencloud.wso2.com/cleanup`.

| Reason | Cause | Fix |
| --- | --- | --- |
| `DeletionProtected` | `spec.deletionProtection` is `true`. Message: `Cannot delete: DeletionProtection is enabled`. The object stays in `Terminating` with the finalizer held. | `kubectl patch dbinstance NAME -n NS --type merge -p '{"spec":{"deletionProtection":false}}'`. Teardown then proceeds. |
| `DeletionProgressing` | Teardown is running (status `False`). | Wait. |
| `TeardownFailed` | Deleting the VM, monitoring objects or tenant Secrets failed. Retried with backoff. A Warning event is emitted. | Read the message, then `kubectl get vm,secret -n NS`. Fix RBAC or a stuck object. |
| `OperatorSecretCleanupFailed` | Deleting `dbi-UID-internal` or `dbi-UID-tls` in the operator namespace failed. Retried. | Check the operator ClusterRole on Secrets and the operator namespace. |

```sh
kubectl get dbinstance NAME -n NS -o jsonpath='{.metadata.finalizers}{"\n"}'
kubectl get dbinstance NAME -n NS -o jsonpath='{.status.conditions[?(@.type=="DeletionBlocked")]}{"\n"}'
```

:::warning
Deletion removes the VM, monitoring objects and the Secrets. It does not delete your own password Secret. The operator does not delete the data and OS disk PVCs itself, and whether Harvester removes them with the VM is not verified in the code. Check `kubectl get pvc -n NS` after deletion. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).
:::

Removing the finalizer by hand skips teardown and can leave the VM, disks and the two operator-namespace Secrets behind. Only do this as a last resort, and clean those up yourself.

## Known gotchas

These are real in the v0.1.0 code.

- **Image names must match exactly.** The operator never imports images. The `VirtualMachineImage` must exist and be Active in the image namespace (default `default`, setting `infrastructure.harvester.imageNamespace`) and its name must match the compiled-in catalog name. A typo behaves the same as a missing image (`OSImageNotFound`).
- **The OS stream is an operator setting, not a per-instance field.** `databaseDefaults.osVersion` (default `22.04`) picks the image for every new instance. There is no per-instance image field.
- **Large image uploads through a Rancher proxy time out** at roughly 700 MB. Upload directly against Harvester.
- **Helm `manager.args` replaces, it does not merge.** If you override it, repeat `--operator.leaderElection.enabled=true` and `--observability.metrics.bindAddress=:8443` or leader election and metrics are silently lost.
- **Single-namespace installs are not supported.** The operator needs cluster-wide RBAC to reconcile `DBInstance` objects in tenant namespaces.
- **A wrong `networkRef` is not caught by preflight.** It surfaces as `VMBooting` with no IP.
- **Several spec fields are accepted but ignored.** `manageMasterUserPassword`, `masterUserPasswordRef`, `s3BackupConfig`, `backupRetentionPeriod`, `preferredBackupWindow`, `multiAZ`, `dbParameterGroupRef` and `tags` do nothing in v0.1.0. Setting backup fields does not give you backups.
- **A changed password Secret does not change the database password.** The operator warns with `PasswordSourceChanged` and keeps the password it accepted. Updating a password on a running database is not supported.
- **A lost password cannot be reset on a hardened install.** With `security.rejectVMPassword` on there is no console or SSH login, so an in-database reset is not possible. Keep a copy of the password.

:::info Verified against
- `database/api/v1alpha1/dbinstance_conditions.go`
- `database/api/v1alpha1/dbinstance_types.go`
- `database/internal/ensure/preflight.go`
- `database/internal/ensure/credentials.go`
- `database/internal/ensure/vm.go`
- `database/internal/ensure/resize.go`
- `database/internal/ensure/repave.go`
- `database/internal/ensure/power.go`
- `database/internal/ensure/health.go`
- `database/internal/ensure/monitoring.go`
- `database/internal/ensure/connection_secret.go`
- `database/internal/ensure/runner.go`
- `database/internal/ensure/result.go`
- `database/internal/controller/dbinstance_controller.go`
- `database/internal/controller/status_conditions.go`
- `database/internal/controller/status_ready.go`
- `database/internal/credentials/passwordsource.go`
- `database/internal/credentials/resolver.go`
- `database/internal/config/flags.go`
- `database/INSTALL.md`
- `database/CREDENTIALS.md`
- `database/README.md`
:::
