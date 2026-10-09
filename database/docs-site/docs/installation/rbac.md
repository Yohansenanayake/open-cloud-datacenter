---
title: RBAC
sidebar_position: 5
---

# RBAC

Two sets of permissions matter: what the **operator** is allowed to do, and what your **users** need to manage databases.

## Give users access

Users manage `DBInstance`, `DBSnapshot` and `DBRestore` objects in their own namespace. The chart ships roles for this, but they are **off by default**:

```bash
helm upgrade --install dbaas-operator <chart> --set rbac.helpers.enable=true
```

Once enabled, anyone holding Kubernetes' built-in `admin`, `edit` or `view` role in a namespace automatically gets matching access to all three kinds there, with no extra bindings:

| Built-in role | Access to `DBInstance`, `DBSnapshot`, `DBRestore` |
| --- | --- |
| `admin` | Everything |
| `edit` | Create, delete, get, list, patch, update, watch |
| `view` | Get, list, watch |

Without these roles, tenants cannot see or create the three kinds.

## What the operator is allowed to do

The operator runs with a cluster-wide `ClusterRole`, because it manages databases in every tenant namespace. A single-namespace install is not supported: a namespaced `Role` would fail with `forbidden` errors on cluster-wide list and watch.

| Area | Access | Used for |
| --- | --- | --- |
| DBaaS resources (`dbinstances`, `dbsnapshots`, `dbrestores`, plus their `status` and `finalizers`) | Full | Reconciling databases, snapshots and restores. |
| KubeVirt (`virtualmachines`, `virtualmachineinstances`, power subresources) and `datavolumes` | Create, update, delete (VMs, data volumes); read-only for VM instances | Creating, resizing, starting, stopping and deleting the database VM. |
| Harvester `virtualmachineimages` | Read-only | Finding the baked image. The operator never creates images. |
| Harvester `virtualmachinebackups` | Create, update, delete, read | Backups behind each `DBSnapshot`. |
| `volumesnapshots` | Read-only | Checking the snapshot a restore reads from. |
| `persistentvolumeclaims` | Create, delete, get, list | Deleting the old OS disk after a repave; creating the data disk for a restore. |
| `leases` | Full | Backup slots and holds. |
| Multus `network-attachment-definitions` | Read-only | Referencing the VM network. |
| `servicemonitors` (Rancher Monitoring) | Full | The per-database `ServiceMonitor`. |
| `secrets` | Full | Credentials, connection details and TLS for each database. |
| `services`, `endpoints` | Full | The per-database metrics Service. |
| `pods`, `events` | Read pods; create events | Status and Kubernetes events. |

:::caution
The `secrets` permission is cluster-wide and can write. It is needed because the operator creates Secrets in tenant namespaces. Treat the operator's ServiceAccount as highly privileged.
:::

## Leader election and metrics

Created by the chart and bound to the operator's ServiceAccount:

- **Leader election:** a namespaced `Role` in the install namespace for `configmaps`, `leases` and `events`.
- **Metrics authentication:** a `ClusterRole` that lets the operator authenticate and authorize scrapers of its secure metrics endpoint.

To let Prometheus scrape the operator's metrics, bind the chart's `metrics-reader` ClusterRole (read access to `/metrics`) to the Prometheus ServiceAccount. It has no binding by default.
