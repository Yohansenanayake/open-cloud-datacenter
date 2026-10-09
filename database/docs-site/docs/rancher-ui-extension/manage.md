---
title: Manage a database
sidebar_position: 4
---

# Resize, start, stop and delete

Most actions are in the database's **action menu** (the three dots) on the list or on the database's own page.

## Resize

1. Open the action menu and choose **Edit Config**.
2. Change **Instance Class** (CPU and memory) and/or **Storage**.
3. Save.

- **The database restarts.** A resize stops and restarts the database, so it is briefly unavailable.
- **Storage can only grow.** You can't make it smaller.

## Start and stop

Use **Stop** or **Start** in the action menu. Stopping shuts the database down, so clients can't connect, but your data is kept. Rancher asks you to confirm a stop. Hold **Shift** while clicking **Stop** to skip the prompt.

While a database is stopped, its automated snapshots are skipped.

## Delete

1. If **Deletion protection** is on, turn it off first. Choose **Turn Off Deletion Protection** in the action menu. A protected database can't be deleted.
2. Choose **Delete** from the action menu and confirm.

**This permanently deletes the database, its data, its credentials and its automated snapshots. It can't be undone.** Manual snapshots are kept. To keep a copy, take a snapshot before deleting.

If a deletion is stuck, the database shows a banner with the reason. For example, a backup may still be running, and the deletion continues when it finishes.

:::tip
Leave **Deletion protection** on for important databases. You can turn it on again from the same action menu.
:::
