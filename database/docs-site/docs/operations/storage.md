---
title: Storage
sidebar_position: 3
---

# Storage

Each DBInstance owns one VM with two persistent disks plus a cloud-init disk. Volumes are Harvester-managed PVCs created from the VM's `harvesterhci.io/volumeClaimTemplates` annotation.

## Disk layout

| Disk | VM volume name | Guest device | PVC name | Size | Contents |
| --- | --- | --- | --- | --- | --- |
| OS | `os-disk` | `/dev/vda` | `pg-<name>-<uid8>-os` (after a repave: `pg-<name>-<uid8>-os-<image-object-name>`) | 20 GiB request in the VM builder, cloned from the baked image | Ubuntu, PostgreSQL binaries |
| Data | `pgdata-disk` | `/dev/vdb` | `pg-<name>-<uid8>-data` | `spec.allocatedStorage` GiB | PostgreSQL data, mounted at `/var/lib/postgresql` |
| Cloud-init | `cloudinit` | `/dev/vdc` | none (Secret-backed) | n/a | Bootstrap data, redacted after first success |

`uid8` in these names is the first 8 hex characters of the DBInstance UID. Including it means a deleted and re-created instance with the same name never reattaches the previous instance's disks.

Both PVCs use volume mode `Block` and access mode `ReadWriteMany`, so the VM can be live-migrated by Harvester. The OS disk's storage class is taken from the Harvester image; the data disk's class comes from `spec.storageType`.

On first boot, cloud-init formats `/dev/vdb` as ext4 (label `pgdata`) only if it has no filesystem, copies the freshly installed cluster onto it, adds a UUID-based `/etc/fstab` entry with `nofail`, and mounts it at `/var/lib/postgresql`. On later boots and after a repave, an existing disk is kept as is.

## Storage class

`spec.storageType` selects the data disk's StorageClass. If unset, the operator default `databaseDefaults.storageClass` is used (default `longhorn`). See [operator configuration](/configuration/operator-config).

`storageType` is **immutable** after creation (a bound PVC cannot change class). Changing it is refused with `PreflightReady=False`, reason `ImmutableFieldChanged`, and phase `incompatible-parameters`.

## Size and expansion

`spec.allocatedStorage` is the data disk size in GiB (minimum 1). It is **grow-only**:

- The CRD enforces `self >= oldSelf`, so a shrink is refused at apply time.
- If a smaller value still reaches the controller, it is rejected with `StorageChangeRejected=True` and reason `UnsupportedShrink`.
- Growth is applied as a cold resize: the VM is stopped, the claim template's requested size is raised, Harvester expands the live PVC, and the VM is started. See [resize and power](/operations/resize-and-power).

The OS disk is not user-sized.

### How to trigger an expansion

```bash
kubectl patch dbinstance mydb --type merge -p '{"spec":{"allocatedStorage":200}}'
kubectl get dbinstance mydb -w
```

## What is recorded in status

| Field | Meaning |
| --- | --- |
| `status.resources.dataVolumeName` | Data PVC name |
| `status.resources.osDiskPVCName` | Current OS PVC; authoritative, observed from the VM |
| `status.resources.pendingDeleteOSDiskPVCName` | Old OS PVC awaiting deletion after a repave (normally empty) |
| `status.resources.vmName` | `pg-<name>` |
| Condition `StorageReady` | `True` (`ShapeConverged`) when VM class and storage match the spec |

## What to observe

```bash
kubectl get dbinstance mydb -o jsonpath='{.status.resources}' | jq
kubectl get pvc -n <namespace>
```

Use your tenant namespace in place of the `<namespace>` placeholder.

:::info Verified against
- `database/internal/harvester/typed_client.go` (`buildPostgresVM`, `ResizeDataVolume`, `SwapVMOSDisk`)
- `database/internal/ensure/vm.go`
- `database/internal/ensure/resize.go`
- `database/internal/ensure/defaults.go`
- `database/internal/credentials/cloudinit.go`
- `database/internal/config/defaults.go`
- `database/api/v1alpha1/dbinstance_types.go`
:::
