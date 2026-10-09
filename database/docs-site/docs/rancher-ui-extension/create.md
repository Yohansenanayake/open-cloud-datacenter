---
title: Create a database
sidebar_position: 2
---

# Create a database

1. Open **Database Instances** and choose **Create**.
2. At the top, set the **Namespace** and the **Name**. A **Description** is optional.
3. Go through the tabs on the left, as described below.
4. Choose **Create**.

:::caution[Decide these now]
These can't be changed after the database is created: PostgreSQL version, storage class, database name, admin username, port, network, and **whether backups are allowed**. If you might ever want snapshots, tick **Allow backups** now.
:::

## Basics

![The Basics tab of the create form](@site/static/img/rancher-ui/create-1-basics.svg)

- **Instance Class**: the size of the database. Each option shows its CPU, memory and connection limit.
- **PostgreSQL Version**: **Operator default** uses the platform's default version. You can pick another.
- **Storage**: the data disk size in GiB. You can grow it later, but never shrink it.
- **Storage Class**: **Operator default** is fine for most uses.

## Database

![The Database tab of the create form](@site/static/img/rancher-ui/create-2-database.svg)

- **Database Name**: the first database created. It is filled in from the instance name.
- **Admin Username**: the administrator for this database.
- **Port**: 5432 unless you need another.

The operator generates the admin password for you and stores it in a Secret. You'll find it on the **Connection** tab once the database is available. You don't choose it.

## Network

![The Network tab of the create form](@site/static/img/rancher-ui/create-3-network.svg)

Choose the Harvester network the database attaches to. It is required. Networks are listed as `namespace/name`. The network must give the VM internet access while it is being set up. Only machines on, or routed to, this network can connect to the database.

## Backup

![The Backup tab of the create form](@site/static/img/rancher-ui/create-4-backup.svg)

- **Allow backups for this instance**: required for any snapshot, manual or automated. **You can only choose this now.**
- **Automated daily snapshots**: can be turned on or off at any time.
- **Backup Window (UTC)**: when the daily snapshot is taken, for example `02:00-03:00`.
- **Snapshots to Keep**: how many automated snapshots are kept. Older ones are deleted.

See [Backup and restore](/rancher-ui-extension/backup-and-restore).

## Advanced

![The Advanced tab of the create form](@site/static/img/rancher-ui/create-5-advanced.svg)

**Deletion protection** blocks deleting the database until you turn it off. Tick it for any database you can't afford to lose by accident. You can change it later from the database's action menu.

## Labels and annotations

![The Labels and Annotations tab of the create form](@site/static/img/rancher-ui/create-6-labels.svg)

Optional. Add your own labels or annotations, for example to track who owns the database.

## Prefer YAML?

Choose **Edit as YAML** at the bottom of the form to see or edit the resource directly. Use **Cancel** to leave without creating anything.

## After you choose Create

The database's page opens in the **Creating** state. A banner says the database isn't ready yet, and the **Connection** tab says its details appear once the database has an endpoint.

![A database that has just been created](@site/static/img/rancher-ui/create-7-creating.png)

In the **Database Instances** list, the new database shows **Creating**, with **Database not yet ready** under its name:

![The database list with the new database creating](@site/static/img/rancher-ui/create-8-list.svg)

## When it is ready

Setup takes a few minutes, depending on how fast the image is cloned and the VM boots. When the state changes to **Available**, the database has an address in the **Endpoint** column, and the **Connection** tab fills in:

![A database that is available, with its connection details](@site/static/img/rancher-ui/create-9-available.svg)

**Next:** [Connect to the database](/rancher-ui-extension/connect).

If it stays in **Creating** for a long time, open the **Events** tab. See also [Troubleshooting](/troubleshooting).
