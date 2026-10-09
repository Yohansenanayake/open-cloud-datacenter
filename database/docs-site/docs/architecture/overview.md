---
title: Architecture overview
sidebar_position: 1
---

# Architecture overview

The operator is a single controller-runtime manager that reconciles `DBInstance`, `DBSnapshot` and `DBRestore` resources, plus one internal dispatcher that grants backup slots. It talks
to the Kubernetes API for ordinary objects and, through a small Harvester client abstraction, to KubeVirt and
Harvester APIs for the VM.

![DBaaS architecture overview](@site/static/img/Architecture%20overview.svg)

## Components

**Controller**. `DBInstanceReconciler.Reconcile` loads the `DBInstance`, handles deletion
through a finalizer, adds the finalizer on first sight, and otherwise runs the ensure runner. A deferred function
derives the aggregate conditions and `status.phase` and patches status once per pass.

**Backup and restore controllers**. Three more reconcilers run in the same manager:

| Reconciler | Reconciles | Role |
| --- | --- | --- |
| `DBSnapshotReconciler` | `DBSnapshot` | Admits a snapshot, creates the Harvester `VirtualMachineBackup`, tracks it to a terminal state, handles protected deletion. Watches `DBInstance` so a source becoming `available` re-triggers waiting snapshots. |
| `BackupDispatcher` | no kind of its own | Grants backup slots (`Lease`s in the operator namespace) to waiting snapshots, oldest first, within the global cap and per-namespace share. Woken by `DBSnapshot` changes. |
| `DBRestoreReconciler` | `DBRestore` | Creates the data PVC from the snapshot's `VolumeSnapshot`, then the target `DBInstance`, and tracks it to ready. Watches `DBInstance`, mapped through `spec.restoredFrom`. |

The `DBInstance` reconciler also schedules automated snapshots and prunes old ones at the end of each pass outside the ensure chain. See [Backup and restore](/backup-restore/overview).

**Ensure runner**. An ordered list of idempotent steps. Each step re-observes real state and
returns one of four outcomes; the runner stops at the first step that is not satisfied.

**Resource builders**. Small `Build`/`Update` pairs applied with `CreateOrUpdate` and a
controller owner reference, for owned declarative children: the cloud-init Secret, connection Secret, metrics
`Service`, `Endpoints` and `ServiceMonitor`. The VM itself is not built here; it belongs to the Harvester client.

**Credentials resolver**. Generates (once) or accepts the master password, generates the
per-instance TLS material, and renders the cloud-init payload that bootstraps PostgreSQL in the VM.

**Baked image catalog**. A registry compiled into the binary mapping OS stream (for example
`22.04`) to a validated image revision and the PostgreSQL major versions it supports.

**Harvester client**. See below.

**REST gateway**. Optional HTTP front end (`/healthz`, `/dbinstances`, `/dbinstances/{name}`
and sub-routes). It builds a per-request client from the caller's bearer token, so Kubernetes enforces authn, authz
and audit. Mutations return `202 Accepted`; poll `GET /dbinstances/{name}` for status.

## The Harvester client abstraction

`harvester.ClientInterface` is the only contract the controller uses for Harvester-specific work, so Harvester API
types never leak into reconcile logic. The production implementation is `TypedClient` (typed Harvester and KubeVirt
clientsets). The interface covers:

| Group | Methods |
| --- | --- |
| Images | `ResolveVMImage`, `ResolveVMImageDisplayName` |
| VM create | `CreatePostgresVM` (idempotent; consumes an already-created cloud-init Secret) |
| Power | `StartVM`, `StopVM`, `StopVMForCrashLoop`, `ClearCrashLoopHalt` |
| Shape | `ResizeVM`, `ResizeDataVolume` |
| OS disk / repave | `SwapVMOSDisk`, `DeletePVC`, `GetVMOSDiskImageID`, `GetVMOSDiskPVCName` |
| Health | `GetVMIReadiness` (running, data-net IP, ready, guest-agent connected, VMI UID) |
| Backup | `CreateVMBackup` (always `spec.type: Backup`), `GetVMBackupStatus`, `DeleteVMBackup` |
| Restore | `CreateRestorePVC`, `GetPVC`, `DeletePVCWithUID`, `GetVolumeSnapshotState` |
| Delete | `TeardownAll` |

Semantic errors (`ErrVMImageNotFound`, `ErrVMImageNotReady`, `ErrVMImageAmbiguous`,
`ErrVMImageReferenceInvalid`) let preflight choose between a terminal failure and a timed retry without parsing
strings.

## Watches

The controller is registered with `For(DBInstance)` plus:

| Watch | Mechanism | Purpose |
| --- | --- | --- |
| `Secret`, `Service`, `Endpoints`, `VirtualMachine`, `ServiceMonitor` | `Owns()` | Drift on a child (for example a deleted Secret) re-triggers reconcile of its owner. |
| `VirtualMachineInstance` | `Watches()` mapped by the `dbaas.opencloud.wso2.com/instance` label | VMIs are owned by the VM, not the `DBInstance`, so they are mapped by label. A predicate only passes create/delete, UID change, phase change, `Ready` or `AgentConnected` flips, and interface IP changes. |

Steady state is event driven: a pass in which every step is satisfied writes nothing and requeues nothing. Timed
requeues are used only while waiting (for example 10 s for an image still importing, 5 s while credentials are
observed, 30 s while a user password Secret is missing). `maxConcurrentReconciles` defaults to 1.

Secrets the user may fix (your own password Secret) are not watched, which is why the credentials step polls.

## Finalizer and ownership

- Finalizers: `dbaas.opencloud.wso2.com/cleanup` on a `DBInstance`, added before any child is created; `dbaas.opencloud.wso2.com/snapshot-cleanup` on a `DBSnapshot`; `dbaas.opencloud.wso2.com/restore-cleanup` on a `DBRestore`.
- Same-namespace children carry a controller owner reference to the `DBInstance`, so Kubernetes garbage collection
  backs up the finalizer's explicit teardown.
- Two controller-private Secrets live in the **operator namespace**, where owner references are not allowed. They
  are tracked by `status.resources.internalSecretRef`/`privateTLSSecretRef` and by the label
  `dbaas.opencloud.wso2.com/dbinstance-uid`, and the finalizer sweeps them by that label.

Backup objects follow different ownership rules: an automated `DBSnapshot` is owned by its instance, a manual one is not, a snapshot hold `Lease` is owned by its instance, a restore hold by its `DBRestore`, and a restore's data PVC deliberately has no owner. See [Managed resources](/architecture/managed-resources#backup-and-restore-objects).

Details of each object are on [Managed resources](/architecture/managed-resources).
