---
title: Backup and restore
sidebar_position: 5
---

# Back up and restore

## Back up a database

**Turn on backups when you create the database.** On the **Backup** tab:
- **Allow backups for this instance** is required for any snapshot, and can't be added later.
- **Automated daily snapshots** can be turned on or off at any time. Set the **Backup Window (UTC)**, for example `02:00-03:00`, and **Snapshots to Keep**.

**Take a manual snapshot**
1. In the database's action menu, choose **Take Snapshot**.
2. Name it and confirm.

The database must be **Available**. Manual snapshots are kept until you delete them, **even after the database is deleted**. Automated snapshots follow the **Snapshots to Keep** setting, and are deleted with the database.

**See your snapshots**
- The database's **Backup** tab shows the next automated snapshot and its snapshots.
- **Snapshots** in the side menu lists all of them. Filter by **Origin** (manual or automated), and watch the **Progress** column.

**Delete a snapshot** from its action menu. This removes its backup data for good, so you can no longer restore from it.

## Restore a database

A restore creates a **new, separate database** from a snapshot. The original is never changed.

1. Open **Snapshots**, find a snapshot that is **Ready**, and choose **Restore to New Instance** from its action menu.
2. Fill in the new database:
   - **Instance Name**
   - **Instance Class**
   - **Storage**: at least the snapshot's size
   - **Network**: only VLAN networks are offered
   - **Allow backups for the new instance**, if you want it backed up
3. Choose **Create**. The page shows the restore's progress through its stages: **Preparing**, **Restoring Volume**, **Starting Database**, then **Succeeded**.

Things to know:
- The new database keeps the snapshot's database name, admin username, PostgreSQL version, port and storage class. These can't be changed.
- It gets **its own admin password**. See its **Connection** tab. Roles and data you created are restored as they were.
- Restores appear in **Restores**. When one succeeds, use **Go to Instance** to open the new database.
- **If a restore fails,** use **Retry** from its action menu. It opens the form with your earlier choices filled in.
- Deleting a restore that is **still running cancels it** and deletes the unfinished database. Deleting a finished restore removes only the record. The new database stays.

A restored database shows a banner at the top of its page: "Restored from snapshot ... of instance ... by restore ...", with links to each.
