---
title: Automated backups
sidebar_position: 3
---

# Automated backups

Setting `spec.backup` on a `DBInstance` turns on backups. By default you get one automated snapshot a day, and the newest seven are kept.

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: mydb
  namespace: tenant-acme
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  networkRef: default/vm-net-100
  backup:
    automated:
      enabled: true
      retainCount: 7
      preferredWindowUTC: "02:00-03:00"
```

`backup: {}` gives you all the defaults.

:::caution[Set it at creation]
`spec.backup` can't be added to or removed from an existing database. A database created without it can never be snapshotted. You can still edit the settings inside it later.
:::

## Settings

| Field | Default | Notes |
| --- | --- | --- |
| `automated.enabled` | `true` | Turns new daily snapshots and pruning on or off. Existing and manual snapshots are unaffected. |
| `automated.retainCount` | `7` | At least 1. How many of the newest successful automated snapshots to keep. |
| `automated.preferredWindowUTC` | `02:00-03:00` | `HH:MM-HH:MM`, 24-hour UTC. An end before the start crosses midnight (`23:00-01:00`). |

## When it runs

- **One snapshot per day,** requested at a fixed minute inside your window. Each database gets its own minute, so they don't all start at the same moment.
- **The backup may start later** than the window, if it has to wait for a free backup slot. See [Concurrency and holds](/backup-restore/concurrency-and-holds).
- **Missed runs are not made up.** If the operator was down at the scheduled time, it makes one attempt when it's back, then moves on to the next day. A failed or skipped attempt isn't retried the same day.
- **The database must be `available`.** If it's stopped, resizing, repaving or degraded at that time, the day's snapshot is skipped and a `ScheduledSnapshotSkipped` warning is raised.
- **Disabled means nothing runs.** With `enabled: false`, there is no scheduling and no pruning.

Snapshots are named `<database>-auto-<YYYYMMDD>`. See the next scheduled time with:

```bash
kubectl get dbinstance mydb -o jsonpath='{.status.backup.nextScheduledSnapshotTime}{"\n"}'
```

## Retention

The operator keeps the newest `retainCount` snapshots of each kind and deletes older automated ones:

- **Successful snapshots:** the newest `retainCount` are kept.
- **Failed snapshots:** the newest `retainCount` are kept too, so you can diagnose them. Failures can't pile up forever.
- **Manual snapshots are never deleted** by pruning.
- **A snapshot a restore is using** is deleted only after that restore ends, so it can outlive the retention count for a while.

## Verify it

```bash
kubectl get dbinstance mydb -o jsonpath='{.status.backup}{"\n"}'
kubectl get dbsnap -l dbaas.opencloud.wso2.com/snapshot-origin=Automated
kubectl get events --field-selector involvedObject.name=mydb | grep ScheduledSnapshot
```

Events on the database: `ScheduledSnapshotCreated`, `ScheduledSnapshotSkipped` (warning) and `ScheduledSnapshotPruned`.
