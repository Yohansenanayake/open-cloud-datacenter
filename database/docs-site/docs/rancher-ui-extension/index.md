---
title: Overview
sidebar_position: 1
---

# Rancher UI extension

The DBaaS extension lets you manage databases from the Rancher UI, with no `kubectl` or YAML. It works on the same `DBInstance`, `DBSnapshot` and `DBRestore` resources described elsewhere in these docs.

**You need:** Rancher 2.15 or later, and a Harvester cluster with the DBaaS operator installed. Clusters without the operator don't appear in the list.

## Find your way around

1. In the left menu, choose **DBaaS**. You see the Harvester clusters where DBaaS is available to you.
2. Choose **Manage** on a cluster.
3. The cluster's side menu has:
   - **Database Instances**: your databases.
   - **Snapshots**: backups.
   - **Restores**: restore operations.
   - **Database Images**: administrators only.

Use the namespace filter at the top to choose which namespaces (tenants) you see.

## What you can do

| Task | Page |
| --- | --- |
| Create a database | [Create a database](/rancher-ui-extension/create) |
| Connect to it | [Connect to a database](/rancher-ui-extension/connect) |
| Resize, start, stop and delete | [Manage a database](/rancher-ui-extension/manage) |
| Update the operating system (repave) | [Update the OS](/rancher-ui-extension/update-os) |
| Back up and restore | [Backup and restore](/rancher-ui-extension/backup-and-restore) |
| Upload database images (administrators) | [Database images](/rancher-ui-extension/database-images) |

## What the extension can't do yet

- **No password rotation**, and you can't choose your own password.
- **No point-in-time restore.** A restore returns the data as of the snapshot.
- **Restore onto VLAN networks only.**
- **Some settings are fixed at creation** (see [Create a database](/rancher-ui-extension/create)) and can't be edited later.
