# Repave E2E Testing

`repave-e2e.sh` is a stage-based test runner for the baked-image + repave
feature. Unlike `test/e2e/`, this needs a real Harvester cluster with
baked images already published.

## Test matrix

| ID | Scenario | Pass condition |
|---|---|---|
| T1 | Provision on stream's current revision | VM boots on the baked image's StorageClass; PG accepts connections without an `apt-get` in cloud-init logs |
| T2 | Register a new revision, don't trigger repave | `ConditionImageDrift=True`/`Reason=OSUpdateAvailable`, VM/data untouched |
| T3 | Trigger repave | VM restarts once, ~30-90s downtime, `pgdata` PVC name unchanged, connection Secret unchanged, TLS cert unchanged |
| T4 | Data survives repave | row counts / canary table written pre-repave present and correct post-repave |
| T5 | Repave trigger while `Stopped` | `ReasonRepaveNotAvailable`, no VM mutation |
| T6 | Old OS-disk PVC deleted post-swap | `kubectl get pvc` shows no pre-swap name lingering |
| T7 | No-op repave (already on latest) | annotation clears, `Satisfied`, zero VM restarts |
| T8 | `databaseDefaults.osVersion` set to a stream not in `LatestBakedImages` (manager misconfiguration) | preflight Terminal on new instances; existing instances' `repave` step `Satisfied`-no-ops (doesn't crash-loop on a bad config value) |
| T9 | Repave failure mid-swap (kill controller between `SwapVMOSDisk` and `DeletePVC`) | re-running the reconcile completes the swap without manual intervention |
| T10 | Concurrent repave triggers, two instances, same namespace | no OS-disk PVC name collision |
| T11 | `kubectl get dbi` shows the new printcolumns and `kubectl describe dbi <name>` shows full condition detail | `ImageDrift` shows `True`/`<none>` in the default table; `ImageDriftReason` appears only with `-o wide`; `describe` shows the standard condition's status, reason, message, observed generation, and transition time |
| E1 | New revision drops the instance's PG major | `ConditionImageDrift=True`/`Reason=EngineVersionEOL` |
| E2 | Repave triggered while blocked (`Reason=EngineVersionEOL`) | Terminal, `ReasonRepaveBlockedEOL`, no destructive op executed |
| E3 | New instance requesting an EOL'd `engineVersion` | `preflight` Terminal, VM never created |
| E4 | Manual `pg_dump`/`pg_restore` migration off an EOL'd instance | row-count-verified data parity |

## Sample manifest

Save as e.g. `test.yaml`, adjusting `networkRef` to a real
NetworkAttachmentDefinition in your cluster:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: repave-e2e-test
  namespace: tenant-acme
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  engineVersion: "16"
  dbName: repavetest
  masterUsername: dbadmin
  manageMasterUserPassword: true
  networkRef: default/vm-network
  backupRetentionPeriod: 0
  deletionProtection: false # stage3 deletes this instance as part of its teardown check
  running: true
```

## Running the stages

1. Deploy the manager on the default OS stream (e.g. `22.04`): `make deploy`
2. Provision the baseline instance:
   `NS=tenant-acme ID=repave-e2e-test YAML=./test.yaml ./repave-e2e.sh stage1`
3. Point `databaseDefaults.osVersion` at the next stream (e.g. `24.04`) and redeploy: `make deploy`
4. Verify drift detection + repave:
   `NS=tenant-acme ID=repave-e2e-test ./repave-e2e.sh stage2`
5. Verify teardown + re-apply doesn't reattach the old disk:
   `NS=tenant-acme ID=repave-e2e-test YAML=./test.yaml ./repave-e2e.sh stage3`
6. Publish a new image revision that keeps the same OS stream but drops
   this instance's `engineVersion`, point `LatestBakedImages` at it
   (`ValidationState: Validated`), and redeploy: `make deploy`
7. Verify the EOL-blocked repave path:
   `NS=tenant-acme ID=repave-e2e-test ./repave-e2e.sh stage4`

Stages 5-7 are optional, each testing an independent edge case — see the
usage comment at the top of `repave-e2e.sh` for their manual preconditions:

- `stage5` — manager misconfigured with an unresolvable `osVersion`
- `stage6` — manager killed mid-swap, verifies recovery
- `stage7` — two instances repaved concurrently, no PVC name collision

Every run tees output to `results/<ns>.<id>.log` and appends a row to
`results/summary.tsv`.
