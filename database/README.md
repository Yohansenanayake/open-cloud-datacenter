# dbaas

A Kubernetes operator that provisions managed PostgreSQL databases as
KubeVirt VMs on a Harvester HCI cluster. One `DBInstance` custom resource
maps to one VM with persistent storage, SSL-only PostgreSQL, an admin
credentials Secret plus a password-free connection Secret, and provisioning
of per-instance Prometheus monitoring resources.

Tested on **Harvester 1.7.1** (RKE2 v1.34.3) — full end-to-end from
`kubectl apply` to `psql` round-trip in ~3 minutes.

## What it does

- **API**: `DBInstance` in group `dbaas.opencloud.wso2.com/v1alpha1`,
  namespaced, with the standard `kubectl get dbi -A` printer columns.
  `dbName` and `masterUsername` are validated against the PostgreSQL
  identifier rules at apply time (`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`), so
  invalid names are rejected up front instead of failing later inside
  cloud-init.
- **Reconciler**: condition-based, bounded ensure-step reconciliation, with
  progress exposed through conditions including `Accepted`, `VMReady`,
  `DatabaseReady`, and `Ready`. It is idempotent and crash-safe through observed
  cluster/provider state, deterministic resource names, and durable
  `status.resources` references. PostgreSQL readiness is checked with
  `pg_isready` against its TCP listener inside the guest via a KubeVirt exec
  readiness probe — no helper Pod.
- **REST gateway**: a thin HTTP layer over the CRD exposing list, create, get,
  modify, delete, start, and stop operations. Mutations are authenticated by
  forwarding the caller's bearer token to the K8s API server (the same
  authn/RBAC/audit path as `kubectl`).
- **Network model**: each VM gets one **data-net** NIC bridged onto the Multus
  `NetworkAttachmentDefinition` supplied via `spec.networkRef`. The referenced
  network must already exist before reconciliation; the controller does not
  create the network. Its address is published as `status.endpoint.address`;
  DHCP is the default, with `spec.staticNetwork` available for VLANs without
  DHCP. Package installation, tenant traffic, and provisioned monitoring
  resources all use this network, so it must provide the required first-boot
  egress.
- **Access control**: the scaffolded `dbinstance-admin/editor/viewer`
  ClusterRoles carry `rbac.authorization.k8s.io/aggregate-to-*` labels,
  so they fold into the built-in `admin`/`edit`/`view` roles. A user
  granted a Rancher project role (or any binding to those K8s roles)
  can manage `DBInstance`s in their namespace with no per-tenant wiring.
  Authorization is pure Kubernetes RBAC — there is no separate DBaaS
  login.
- **Per-instance TLS**: a CA and server certificate are generated once for each
  `DBInstance`.
  The CA's private key and the server key live in a controller-private
  Secret in the operator namespace (default `dbaas-system`); the public
  `ca.crt` is pinned via the tenant-facing connection Secret
  (`pg-<name>-connect`), alongside `host`/`port`/`dbname`/`jdbcUrl`/`sslmode`.
  `pg_hba.conf` enforces `hostssl … scram-sha-256` only. The master role is
  created with `CREATEDB`/`CREATEROLE` but **not** `SUPERUSER`.
- **Credentials Secret model**: the tenant-namespace `pg-<name>-credentials`
  Secret holds only `admin_user`/`admin_password`. DBaaS-internal operational
  credentials (`repl_password`, `exporter_password`) and the TLS private
  material live in two controller-private Secrets in the operator namespace,
  named from the DBInstance's UID and never exposed to tenants.

## What's NOT in this version

The CRD schema is broader than the implementation. The following fields and
capabilities are reserved for forward compatibility but **the reconciler does
not act on them today**:

| Field | Status |
| --- | --- |
| `engineVersion` | Recorded but ignored; cloud-init installs the OS image's apt-default PostgreSQL (Ubuntu 22.04 → PG 14, Ubuntu 24.04 → PG 16). |
| `manageMasterUserPassword`, `masterUserPasswordRef` | Ignored; the controller always generates a random admin password into the credentials Secret. |
| `s3BackupConfig`, `backupRetentionPeriod`, `preferredBackupWindow` | Values are recorded but no pgBackRest install, schedule, or retention runs. |
| `multiAZ` | No Patroni / HA standby is created. |
| `dbParameterGroupRef` | No `DBParameterGroup` CRD exists in this module. |
| `tags` | Not propagated to child resource labels / annotations / dashboards. |
| `status.readReplicas` | Not populated because read replicas and `multiAZ` are not implemented. |

The spec limitations are called out in each field's godoc
(`kubectl explain dbi.spec.<field>`). They will be implemented incrementally; the
schema shape is deliberately stable so users can write manifests today
that work later.

## Quickstart

```sh
# From inside the dbaas/ directory, with kubectl + docker buildx available:
make docker-buildx IMG=<registry>/<name>:<tag>
KUBECONFIG=<your-harvester-kubeconfig> make install
KUBECONFIG=<your-harvester-kubeconfig> make deploy IMG=<registry>/<name>:<tag>

# Then apply a DBInstance; start from config/samples/dbaas_v1alpha1_dbinstance.yaml
kubectl get dbi -A -w
```

Expected time from `apply` to `phase=available`: about **3 minutes** on
the tested stock Ubuntu cloud image; actual time depends on image provisioning
and first-boot package-download speed.

## Build / test / develop

```sh
make manifests generate fmt vet build   # regenerate CRD + DeepCopy, build manager
make test                               # envtest-backed unit tests
make docker-buildx IMG=...              # cross-build linux/amd64, push
make install                            # apply CRD using current kubeconfig
make deploy IMG=...                     # apply manager + RBAC
make undeploy && make uninstall         # tear it all down
```

## Part of Open Cloud Datacenter

This component lives in the [WSO2 Open Cloud
Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative,
providing managed database services on Harvester HCI.

## License

Apache-2.0
