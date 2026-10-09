---
title: Update the OS (repave)
sidebar_position: 4
---

# Update the operating system (repave)

When a newer database image is available, the extension tells you, and you can apply the update with a few clicks. **Your data is kept.** The database restarts on the new image and is unavailable for a few minutes.

## 1. Spot an available update

In **Database Instances**, the **Image** column shows **OS update available** for databases that can be updated. The database's own page also shows a banner with an **Apply OS Update** button.

## 2. Take a snapshot (recommended)

If the database holds data you care about, take a snapshot first. See [Backup and restore](/rancher-ui-extension/backup-and-restore).

## 3. Apply the update

Open the database's action menu and choose **Apply OS Update** (or use the button in the banner). Rancher asks you to confirm:

![The Apply OS update confirmation dialog](@site/static/img/rancher-ui/apply-os-update-dialog.svg)

The dialog tells you:
- The image the database runs now, and the one it will move to.
- That the database is stopped, its OS disk is replaced and it starts again, so clients **can't connect for a few minutes**.
- That a running snapshot makes the update wait until the snapshot finishes.

Choose **Apply OS Update** to start.

## 4. Watch it finish

The database's state changes to **Modifying**. A banner says **Applying the OS update**, and the **OS Image** section on the **Basics** tab shows the new **Image Revision** and a **Status** of **Updating**:

![A database while its OS update is being applied](@site/static/img/rancher-ui/os-update-in-progress.svg)

In the **Database Instances** list, the **Image** column shows **Updating** for that database:

![The database list while an OS update is running](@site/static/img/rancher-ui/os-update-list.svg)

- While the database restarts, it can briefly show **Degraded** with "PostgreSQL readiness probe failing". This is expected during an update.
- When it is done, the state returns to **Available** and the Image column shows **Up to date**.

## When an update isn't possible

| What you see | What it means |
| --- | --- |
| **Apply OS Update** is greyed out | The database must be **Available** (running and healthy), and you need permission to update it. |
| A banner says the new image doesn't support your PostgreSQL version | The update is blocked. Create a new database with a supported version and move your data with `pg_dump` and `pg_restore`. Restoring a snapshot doesn't help, because it keeps the same PostgreSQL version. |
| A banner says the update was not applied | Read the message in the banner. It says why. |

See also [Images and repave](/operations/images-and-repave) for how updates work behind the scenes.
