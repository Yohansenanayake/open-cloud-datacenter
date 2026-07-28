# dbaas

**A Kubernetes operator for managed PostgreSQL on Harvester HCI / KubeVirt.**

One `DBInstance` custom resource maps to one VM with persistent storage,
SSL-only PostgreSQL, and tenant-facing credentials. `kubectl apply` to a working `psql` connection in
about 3 minutes.

## Prerequisites

- A [**Harvester HCI**](https://github.com/harvester/harvester/tree/v1.7) cluster (tested on 1.7.1 / RKE2 v1.34.3)
- A Multus **NetworkAttachmentDefinition** for the VM's data network already
  created on the cluster — the controller only attaches to `spec.networkRef`,
  it never creates networks.
- [**Rancher Monitoring**](https://docs.harvesterhci.io/v1.7/monitoring/harvester-monitoring)
  enabled on the cluster (it bundles the Prometheus Operator and the
  `ServiceMonitor` CRD) — per-instance monitoring resources are created
  unconditionally.
- `kubectl`, and a `KUBECONFIG` pointed at the Harvester cluster.
- To build from source: **Go 1.25+**, `make`, and `docker buildx` for
  cross-building the manager image.

## Quickstart

```sh
# From this directory, with kubectl + docker buildx pointed at your Harvester kubeconfig:
make docker-buildx IMG=<registry>/<name>:<tag>
KUBECONFIG=<harvester-kubeconfig> make install
KUBECONFIG=<harvester-kubeconfig> make deploy IMG=<registry>/<name>:<tag>

kubectl apply -f config/samples/dbaas_v1alpha1_dbinstance.yaml
kubectl get dbi -A -w
```

~3 minutes from `apply` to `phase: available` on a stock Ubuntu cloud image;
actual time depends on image pull and first-boot package-install speed.

## Operator configuration

The operator loads its typed JSON configuration through
[`nil-go/konf`](https://github.com/nil-go/konf). The default deployment does
not require a ConfigMap: it starts with built-in defaults and the flags in the
Deployment manifest.

Configuration precedence, from lowest to highest, is:

```text
built-in defaults < configuration file < environment variables < explicit flags
```

Environment variables use the `DBAAS_` prefix and `__` between hierarchy
segments, for example
`DBAAS_CONTROLLER__MAX_CONCURRENT_RECONCILES=4`. Flags use canonical dotted
paths, for example `--controller.maxConcurrentReconciles=4`.

Configuration changes require an operator Pod restart or Deployment rollout;
live reload and cloud-backed providers are not enabled in the first release.

An optional Kustomize overlay creates a `dbaas-operator-config` ConfigMap,
mounts its `config.json` key inside the controller Pod at
`/etc/dbaas/config.json`. The operator automatically loads that fixed path
when the file exists:

```sh
kubectl create namespace dbaas-system
kubectl apply -k config/overlays/operator-config
```

Edit
[`config/overlays/operator-config/operator_config.yaml`](config/overlays/operator-config/operator_config.yaml)
before applying the overlay. The regular `make deploy` path continues to use
`config/default`, creates the standard `dbaas-system` namespace automatically,
and does not create or mount this ConfigMap.

The installation namespace is Kubernetes deployment metadata, so it is not
part of `config.json`. It is defined once by `namespace` in
`config/overlays/operator-config/kustomization.yaml`. For example, to install
in `dbaas-system-v2`, change that one field, create the namespace, and apply
the same overlay:

```sh
kubectl create namespace dbaas-system-v2
kubectl apply -k config/overlays/operator-config
```

Kustomize assigns the selected namespace to every namespaced resource,
including the ConfigMap, Deployment, ServiceAccount, Service, Roles, and
RoleBindings. The Deployment injects its namespace into `POD_NAMESPACE`
through the Downward API, and the operator uses that value for its
controller-private Secrets. Reusable manifests do not create a fixed
Namespace resource; the target namespace must exist before applying the
configuration overlay. The separate `config/default` installation wrapper
does include the standard `dbaas-system` Namespace for a one-command default
installation.

## What it provisions

Each `DBInstance` (`dbaas.opencloud.wso2.com/v1alpha1`, namespaced) creates:

| Resource | Details |
| --- | --- |
| VM (KubeVirt) | One data-net NIC bridged onto the Multus NAD in `spec.networkRef` (must already exist). DHCP by default, or `spec.staticNetwork` for VLANs without one. Address published as `status.endpoint.address`. |
| `pg-<name>-credentials` (tenant Secret) | `admin_user` / `admin_password` only. |
| `pg-<name>-connect` (tenant Secret) | `host`, `port`, `dbname`, `jdbcUrl`, `sslmode`, `ca.crt` — no password material. |
| TLS | Per-instance CA + server cert. Private key material lives in a controller-private Secret in the operator namespace, never exposed to tenants. `pg_hba.conf` enforces `hostssl … scram-sha-256` only; the master role gets `CREATEDB`/`CREATEROLE` but not `SUPERUSER`. |
| Monitoring | Per-instance Prometheus `Service` + `ServiceMonitor` (exporter install is pending). |

`dbName` and `masterUsername` are validated against PostgreSQL identifier
rules (`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`) at apply time, so invalid names are
rejected up front instead of failing later inside cloud-init.

## How it works

- **Bounded ensure-step reconciler** — every reconcile walks the same fixed,
  ordered chain of steps, each re-observing real cluster/provider state , and stopping at the first step
  that isn't satisfied. It's idempotent and crash-safe via
  `status.resources`. Conditions are the *reported outcome* of a pass.

- **REST gateway** — a thin HTTP layer over the CRD (list/create/get/modify/
  delete/start/stop). Every request forwards the caller's bearer token, so
  the K8s API server enforces the same authn/RBAC/audit path as `kubectl` —
  there's no separate DBaaS login.
- **RBAC-native access control** — the scaffolded `dbinstance-admin/editor/
  viewer` ClusterRoles aggregate into the built-in `admin`/`edit`/`view`
  roles, so a Rancher project role (or any binding to those) is all a tenant
  needs.

## Not yet implemented

The CRD schema is broader than the implementation. These fields are
reserved for forward compatibility, but **the reconciler does not act on
them today**:

| Field | Status |
| --- | --- |
| `engineVersion` | Recorded but ignored; cloud-init installs the OS image's apt-default PostgreSQL (Ubuntu 22.04 → PG 14, Ubuntu 24.04 → PG 16). |
| `manageMasterUserPassword`, `masterUserPasswordRef` | Ignored; the controller always generates a random admin password. |
| `s3BackupConfig`, `backupRetentionPeriod`, `preferredBackupWindow` | Recorded but no pgBackRest install, schedule, or retention runs. |
| `multiAZ` | No Patroni / HA standby is created. |
| `dbParameterGroupRef` | No `DBParameterGroup` CRD exists in this module. |
| `tags` | Not propagated to child resource labels / annotations / dashboards. |
| `status.readReplicas` | Not populated — read replicas and `multiAZ` aren't implemented. |

Each limitation is also called out in the field's godoc (`kubectl explain
dbi.spec.<field>`). The schema shape is deliberately stable so manifests
written today keep working as these land.

## Build / test / develop

```sh
make manifests generate fmt vet build   # regenerate CRD + DeepCopy, build manager
make test                               # envtest-backed unit tests
make docker-buildx IMG=...              # cross-build linux/amd64, push
make install                            # apply CRD using current kubeconfig
make deploy IMG=...                     # apply manager + RBAC
make undeploy && make uninstall         # tear it all down
```

---

Part of the [WSO2 Open Cloud Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative. Licensed under Apache-2.0.
