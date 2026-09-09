# Persistent guest-state storage

New DBInstances receive a dedicated PVC for the backup runtime. It is separate
from the replaceable OS disk and PostgreSQL's data disk. This change establishes
storage and bootstrap plumbing; agent, executor, SQLite and Redis runtime
installation are subsequent implementation steps.

| Property | Contract |
| --- | --- |
| PVC name | `pg-<instance-name>-<uid8>-state`, following the existing disk naming convention |
| Ownership | Controller owner reference to the full DBInstance UID, never the VM |
| Kubernetes binding | `status.resources.guestStatePVCName` and `guestStatePVCUID`, persisted before VM creation |
| Capacity | `databaseDefaults.guestStateSizeGB`, default 5 GiB; initial allocation only |
| Storage class | Instance `spec.storageType`, falling back to `databaseDefaults.storageClass` |
| Volume mode | Block, ReadWriteMany, matching the existing Harvester live-migration disk requirements |
| Guest device | `/dev/disk/by-id/virtio-dbaas-state`, from the explicit KubeVirt disk serial |
| Mount | `/var/lib/dbaas-state`, ext4 with `nodev,nosuid,noexec` |
| Guest binding | Root-owned mode-0600 `volume-identity`, containing the instance UID and PVC UID |

5 GiB is a configurable development starting point, not a qualified runtime
capacity limit. Measure Redis AOF rewrite, SQLite WAL, receipts and outbox usage
in runtime qualification. Changing the default affects new PVCs only.

## Provisioning and startup

The operator creates the PVC directly. It is deliberately absent from
Harvester's VM volume-claim templates: a missing accepted-work volume must not
be silently recreated by the VM controller. The operator refuses to recreate a
bound PVC, adopt another owner's PVC, or accept a changed PVC UID.

Initial cloud-init permits filesystem initialization only while provisioning the
first VM. The helper accepts only a blank disk without recognized signatures; filesystem probing
errors, existing signatures and unexpected filesystem types block formatting.
Before formatting it writes and synchronizes an exclusive attempt marker on
the OS disk. It then formats without a force flag, mounts the disk, and durably
writes the instance/PVC binding. Existing filesystems require a valid binding;
a crash before that write requires explicit reconciliation. Re-running initial
bootstrap cannot reformat a disk after its initialization attempt.

Bootstrap verifies state before PostgreSQL initialization. The installed
`dbaas-guest-state.service` mounts and verifies the disk on subsequent boots; it
never formats. It binds to the stable device unit. Missing disks, mismatched
identities, wrong filesystems and read-only mounts fail startup.

The future agent/executor/Redis units must require this service and the verified
mount, stop when it disappears, and validate their existing stores before
accepting work. Ordering (`Before=`) alone is not that dependency. Initializing
this filesystem does not initialize or authorize reinitialization of SQLite or
Redis. Keep store directories, policy, keys and safety records on this volume
with service-specific ownership; do not put them under PostgreSQL's recursively
chowned data mount.

## Repave, recovery and deletion

OS repave changes only the OS disk. Regenerated cloud-init carries the same
instance/PVC identity with formatting disabled. The state PVC and its serial
remain attached. VM recreation after a recorded provision also disables
formatting and requires the existing bound PVC.

Deleting the VM alone does not garbage-collect this PVC. Deleting the DBInstance
allows Kubernetes owner-reference garbage collection to remove it after the
operator's finalizer finishes; Kubernetes PVC protection handles any remaining
Pod use. No state-disk deletion is added to OS repave. Future backup deletion
and hold gates must finish before that finalizer is released.

Existing VMs without a guest-state disk are left unchanged; this change does not
hot-attach or migrate them. Recreating an already-provisioned VM without its
state disk requires an explicit migration/recovery decision. Restores into new
DBInstances must provision fresh state volumes and identities, never copy the
source queue. Full restore execution is not implemented here.

Treat interrupted initialization, missing/replaced disks and stale restored
state as recovery incidents. Preserve the disks and evidence, fence the old
executor/VM, and reconcile the instance/PVC identity and accepted work before
resuming. Do not clear the attempt marker, status binding or volume identity to
make automatic provisioning retry. Recovery tooling is a later plan step.

## Validation

Unit tests cover PVC ownership and binding-before-VM ordering, missing/replaced
volume rejection, disk attachment preservation during repave, bootstrap
rendering, and actual shell initialization policy with block-device operations
substituted. Kubernetes generation and the repository test suite validate the
API and reconciliation changes.

Before deploying, qualify a disposable Harvester VM: initial disk formatting,
udev serial resolution, service startup/reboot ordering, refusal of a missing
mount, survival of a sentinel across OS repave and VM recreation, and PVC
retention on VM deletion versus cleanup on DBInstance deletion. These checks
require real guest/storage execution; unit tests do not prove them. Once the
executor exists, extend the same fixture to accepted work, AOF/SQLite recovery,
quiescing, holds and runtime schema upgrades.
