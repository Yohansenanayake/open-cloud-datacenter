---
title: Introduction
slug: /
sidebar_position: 1
---

# DBaaS for Harvester

DBaaS is a Kubernetes operator that provides **managed PostgreSQL on Harvester HCI**. You declare a
`DBInstance` custom resource (`dbaas.opencloud.wso2.com/v1alpha1`, short name `dbi`); the operator turns it into a
KubeVirt virtual machine with persistent storage, an SSL-only PostgreSQL server, generated credentials, a
tenant-facing connection Secret and Prometheus scrape objects.

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: dbinstance-sample
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  engineVersion: "16"
  dbName: myapp
  networkRef: default/vm-net-100
```

Release version: `v0.1.0` (experimental). The class names (`db.t3.medium`) and the phase strings
(`creating`, `available`, `stopped`, ...) intentionally mirror Amazon RDS vocabulary.

## What it is

- **One `DBInstance` = one VM = one PostgreSQL server.** The database runs inside a VM on Harvester, not in a pod.
- **Declarative and idempotent.** A fixed, ordered chain of "ensure" steps re-observes real cluster state on every
  reconcile and repairs drift, for example an out-of-band `kubectl delete vm`.
- **Secure by default.** TLS with a per-instance CA, `hostssl ... scram-sha-256` only, and the master password is
  never placed in the connection Secret (see [Credentials](/security/credentials) and
  [TLS and access](/security/tls-and-access)).
- **Lifecycle operations.** Stop/start (`spec.running`), cold resize of CPU/memory/storage, and OS-image "repave"
  onto a newer baked image (see [Resize and power](/operations/resize-and-power) and
  [Images and repave](/operations/images-and-repave)).
- **Backup and restore.** Opt an instance into daily automated snapshots with retention (`spec.backup`) and take manual
  ones with a `DBSnapshot`; restore any `Ready` snapshot into a new, independent instance with a `DBRestore`
  (see [Backup and restore](/backup-restore/overview)).
- **Observable.** Conditions, a derived `status.phase`, and a per-instance metrics `Service` plus `ServiceMonitor`.
- **Optional REST gateway.** A thin HTTP layer over the CRD that forwards the caller's bearer token to the
  Kubernetes API server, so RBAC and audit are the same as `kubectl`. It is enabled by default on `:8080`.

## Use cases

- Give tenants self-service PostgreSQL on a private Harvester cloud, using ordinary Kubernetes RBAC for access
  control.
- Provision per-application databases from GitOps manifests.
- Take scheduled and on-demand snapshots, and clone or recover a database from one into a new instance.
- Run dev/test databases that can be stopped (storage preserved) and started again.

## What it is not

DBaaS v0.1.0 is deliberately narrow. The CRD schema is broader than the implementation, so the following are **not**
available yet:

- High availability or replicas: there is one VM per instance and no standby is created.
- Point-in-time recovery or continuous archiving. Backups are snapshots; a restore returns the database as of the
  snapshot (see [what is not implemented](/backup-restore/overview#what-is-not-implemented)). Backup is opt-in when
  the instance is created: `spec.backup` cannot be added later.
- Custom PostgreSQL parameter groups and resource tags.
- Choosing your own master password. The operator always generates it.
- Changing the password of a running database. The operator never alters it.
- Creating networks. The operator only attaches to an existing Multus `NetworkAttachmentDefinition`.
- A single-namespace install: the manager watches `DBInstance`s cluster-wide.

## Prerequisites

| Requirement | Details |
| --- | --- |
| Harvester HCI | Tested on Harvester 1.7.1 (RKE2 v1.34.3). KubeVirt, CDI and Harvester's `VirtualMachineImage` API must be present. |
| Network | A Multus `NetworkAttachmentDefinition` already exists; `spec.networkRef` is `namespace/name` of it. Preflight requires the field to be set but does not yet verify that the NAD exists. |
| Baked OS image | The catalog compiled into the operator names the images it will use (default OS stream `22.04` resolves to `ubuntu-2204-postgres-v20260515`). That image must be imported into Harvester and ready, in the configured image namespace (default `default`). See [Images and repave](/operations/images-and-repave). |
| Storage class | `longhorn` unless `databaseDefaults.storageClass` or `spec.storageType` says otherwise. |
| Monitoring stack | Rancher Monitoring (Prometheus Operator) with the `ServiceMonitor` CRD. The controller watches `ServiceMonitor`s and creates one per instance unconditionally. |
| Tooling | `kubectl` with a kubeconfig for the Harvester cluster. |
| Reachability | Clients must be able to reach the VM's data-network IP; the database is not exposed through a pod or Service. |

Installation is covered in [Helm and Addon install](/installation/helm-addon) and operator flags in
[Operator configuration](/configuration/operator-config). To try it right away, go to the [Quickstart](/quickstart).
