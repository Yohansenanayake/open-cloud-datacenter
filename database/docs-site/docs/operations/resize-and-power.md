---
title: Resize and power
sidebar_position: 2
---

# Resize and power

Change a database's size or power state by editing its `DBInstance`:

| Setting | Field | Notes |
| --- | --- | --- |
| CPU and memory | `spec.dbInstanceClass` | Any known class, up or down |
| Disk size | `spec.allocatedStorage` | Grow only |
| Power | `spec.running` | `true` (default) or `false` |

All three can be changed after creation. `storageType` cannot. See [storage](/operations/storage).

## Resize

```bash
# CPU and memory: change the class
kubectl patch dbinstance mydb --type merge -p '{"spec":{"dbInstanceClass":"db.m5.large"}}'

# Disk: grow only
kubectl patch dbinstance mydb --type merge -p '{"spec":{"allocatedStorage":100}}'

# Watch progress
kubectl get dbinstance mydb -w
```

What you need to know:

- **Expect downtime.** Every resize is a cold resize: the VM stops, the new size is applied, and the VM starts again. There is no online resize.
- **Disks can't shrink.** A smaller `allocatedStorage` is refused, and the database keeps running unchanged. Set the value back and the rejection clears.
- **Unknown classes are rejected** with reason `InvalidClass`. The classes available are set in the [operator configuration](/configuration/operator-config#instance-classes).
- **Stopped instances can be resized too.** The new size applies on the next start.

## Power on and off

```bash
# Stop
kubectl patch dbinstance mydb --type merge -p '{"spec":{"running":false}}'

# Start
kubectl patch dbinstance mydb --type merge -p '{"spec":{"running":true}}'
```

What you need to know:

- **Data is kept.** A stopped instance keeps its disks. Stopping is a full power off, and there is no memory-state hibernate. Starting boots the VM again.
- **A planned start is not a crash.** It is never counted as an unplanned restart. See [health checks](/operations/health-checks).
- **A stopped instance is not degraded.**
- **Repave needs a running instance.** An instance with `running: false` can't be repaved.
- **Crash-loop halt overrides this.** If the instance has halted after repeated crashes, setting `running: true` won't restart it. Start the VM manually, as described in [health checks](/operations/health-checks).
