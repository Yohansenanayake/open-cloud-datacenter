---
title: Create and connect
sidebar_position: 2
---

# Create a database and connect to it

## Create a database

1. Open **Database Instances** and choose **Create**.
2. Fill in the tabs:

| Tab | What to set |
| --- | --- |
| **Basics** | Name, namespace, **Instance Class** (CPU and memory), **PostgreSQL Version**, **Storage** in GiB |
| **Database** | Database name, admin username, port, storage class. The defaults are fine for most uses. |
| **Network** | The Harvester network the database attaches to. It must give the VM internet access during setup. |
| **Backup** | **Allow backups for this instance**, plus the daily schedule (see [Backup and restore](/rancher-ui-extension/backup-and-restore)) |
| **Advanced** | **Deletion protection** |

3. Choose **Create**. The state changes from creating to **Available** when it is ready.

:::caution[Decide these now]
These can't be changed after the database is created: PostgreSQL version, storage class, database name, admin username, port, network, and **whether backups are allowed**. If you might ever want snapshots, tick **Allow backups** now.
:::

The operator generates the admin password for you. You don't pick it.

## Connect to a database

When the database is **Available**, open it. The **Connection** tab is the first thing you see.

![The Connection tab of a database instance](@site/static/img/rancher-ui/connection-tab.svg)

It shows:
- **Endpoint**: host, port, database and **SSL mode**
- A ready-to-use **JDBC URL** and **psql** command (use the copy icon next to each)
- **Admin Credentials**: the username and password (use **Show** and **Copy**)
- The **CA certificate** (**Download ca.crt**), which your client needs to verify the server

The database's **Events** tab lists recent events if something looks wrong.

- Clients must be on, or routed to, the database's network.
- If you can't see the password, you don't have permission to read Secrets in that namespace. Ask your administrator.

For connecting from your own application, see [Connecting from an application](/connecting).
