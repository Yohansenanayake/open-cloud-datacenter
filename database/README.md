# DBaaS

**Managed PostgreSQL on Harvester, from a Kubernetes operator.**

Create a `DBInstance` and the operator gives you a PostgreSQL database in its own virtual machine: persistent storage, TLS-only access, generated credentials, backups and restores. Manage it with `kubectl` or from the Rancher UI.

> Early release (v0.1.0). API: `dbaas.opencloud.wso2.com/v1alpha1`.

![DBaaS architecture](docs-site/static/img/Architecture%20overview.svg)

**Who is this for?**
- **Platform administrators** install the operator and prepare Harvester. Start with [Install](#2-install-the-operator).
- **Developers and tenants** create and use databases. Start with [Create a database](#3-create-a-database).

## Get started

### 1. Check the requirements

- A **Harvester** cluster (tested on 1.7.1 with RKE2 v1.34.3), and `kubectl` with its kubeconfig.
- An existing Multus **NetworkAttachmentDefinition** for the database VM's network. The operator never creates networks. The network needs outbound internet access for first boot.
- The **database images** uploaded to Harvester and `Active`. Without them, new instances stop with `OSImageNotFound`. See [Prerequisites](docs-site/docs/installation/prerequisites.md).
- **Rancher Monitoring**, or the Prometheus Operator CRDs, because each database gets a `ServiceMonitor`.

### 2. Install the operator

Install with the Helm chart as a Harvester Addon. The steps are in [`INSTALL.md`](./INSTALL.md) and [Helm and Harvester Addon install](docs-site/docs/installation/helm-addon.md).

You know it worked when the Addon is `AddonDeploySuccessful`, the manager pod is `Running`, and a test database reaches `available`.

### 3. Create a database

Only three fields are required:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: orders-db
  namespace: default
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  networkRef: default/vm-net-100   # namespace/name of your network
  backup: {}                       # optional: turns on backups. Can't be added later
```

```sh
kubectl apply -f orders-db.yaml
kubectl wait --for=condition=Ready dbinstance/orders-db --timeout=20m
```

> **Decide on backups now.** `spec.backup` can only be set when the database is created.

More samples are in [`config/samples/`](config/samples/) and [Samples](docs-site/docs/reference/samples.md).

### 4. Connect

The connection details are in two Secrets in the same namespace. The password is only in the credentials Secret.

```sh
NAME=orders-db; NS=default
HOST=$(kubectl get secret pg-$NAME-connect -n $NS -o jsonpath='{.data.host}' | base64 -d)
PORT=$(kubectl get secret pg-$NAME-connect -n $NS -o jsonpath='{.data.port}' | base64 -d)
DB=$(kubectl get secret pg-$NAME-connect -n $NS -o jsonpath='{.data.dbname}' | base64 -d)
kubectl get secret pg-$NAME-connect -n $NS -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
USER=$(kubectl get secret pg-$NAME-credentials -n $NS -o jsonpath='{.data.admin_user}' | base64 -d)
export PGPASSWORD=$(kubectl get secret pg-$NAME-credentials -n $NS -o jsonpath='{.data.admin_password}' | base64 -d)

psql "host=$HOST port=$PORT dbname=$DB user=$USER sslmode=verify-ca sslrootcert=ca.crt"
```

Your machine must be on, or routed to, the database's network. More in [Connecting from an application](docs-site/docs/connecting.md). Prefer a UI? The Rancher extension has a **Connection** tab with the same details.

## Everyday tasks

```sh
# Resize: change the class or grow the storage (the database restarts)
kubectl patch dbinstance orders-db --type merge -p '{"spec":{"dbInstanceClass":"db.m5.large"}}'
kubectl patch dbinstance orders-db --type merge -p '{"spec":{"allocatedStorage":100}}'

# Stop and start (data is kept)
kubectl patch dbinstance orders-db --type merge -p '{"spec":{"running":false}}'
kubectl patch dbinstance orders-db --type merge -p '{"spec":{"running":true}}'

# Update the OS image (when status shows an update is available). Use a new value each time
kubectl annotate dbinstance orders-db dbaas.opencloud.wso2.com/repave-trigger="$(date +%s)" --overwrite

# Delete (turn deletion protection off first, if it's on)
kubectl patch dbinstance orders-db --type merge -p '{"spec":{"deletionProtection":false}}'
kubectl delete dbinstance orders-db
```

**Back up and restore.** A manual snapshot, then a restore into a *new* database:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBSnapshot
metadata:
  name: orders-db-before-upgrade
spec:
  sourceInstanceRef:
    name: orders-db
---
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBRestore
metadata:
  name: orders-db-restore-1
spec:
  snapshotRef:
    name: orders-db-before-upgrade   # must be Ready
  targetInstanceName: orders-db-restored
  dbInstanceClass: db.t3.medium
  networkRef: default/vm-net-100
  allocatedStorage: 50
```

Apply the snapshot first and wait until it is `Ready` before applying the restore.

**Something wrong?** Start with:

```sh
kubectl get dbinstance orders-db -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}): {.message}{"\n"}{end}'
```

Then see [Troubleshooting](docs-site/docs/troubleshooting.md).

## What you get

| | |
| --- | --- |
| **Provisioning** | A KubeVirt VM and a data volume (Longhorn by default). 12 instance classes, `db.t3.micro` to `db.r5.2xlarge`. PostgreSQL 15 to 18, depending on the database image. |
| **Secure by default** | TLS-only with SCRAM-SHA-256, a private CA per database, an admin user that is not a superuser, and no VM password login unless allowed. |
| **Connection details** | A generated admin password, and a password-free connection Secret with host, port, JDBC URL and CA certificate. |
| **Resize, start, stop** | Edit the resource. Resizing restarts the database. |
| **Health protection** | Readiness checked inside the VM. Degraded databases are reported, and a VM that keeps crashing is halted. |
| **OS updates** | The operator detects newer database images and applies them on request. Data stays. |
| **Backup and restore** | Manual and daily snapshots, retention, backup limits, and restore into a new database, even after the source is deleted. |
| **Safe deletion** | Deletion protection. |
| **Monitoring** | A per-instance metrics Service and `ServiceMonitor`. |
| **Rancher UI** | Create, connect, resize, update, back up, restore and delete from Rancher. See [Rancher UI extension](docs-site/docs/rancher-ui-extension/). |
| **RBAC-native** | Admin, editor and viewer roles for the three resources aggregate into Kubernetes' built-in roles. |

## Limits

- **No point-in-time recovery.** A restore returns the data as of the snapshot.
- **No password rotation.** The operator always generates the admin password.
- **No standby or replicas.** There is one VM per instance.
- **Restore always makes a new database.** There is no in-place restore.
- **Some settings are fixed at creation:** the network, port, database name, admin user, storage class, PostgreSQL version, and whether backups are on.

See the [DBInstance spec](docs-site/docs/reference/dbinstance-spec.md) for every field.

## Documentation

The full docs are in [`docs-site/`](docs-site/). To read them locally: `cd docs-site && npm install && npm start`.

| I want to... | Go to |
| --- | --- |
| Try it step by step | [Quickstart](docs-site/docs/quickstart.md) |
| Install the operator | [Installation](docs-site/docs/installation/) |
| Use the Rancher UI | [Rancher UI extension](docs-site/docs/rancher-ui-extension/) |
| Connect an application | [Connecting](docs-site/docs/connecting.md) |
| Resize, update, delete | [Operations](docs-site/docs/operations/) |
| Back up and restore | [Backup and restore](docs-site/docs/backup-restore/) |
| Understand credentials and TLS | [Security](docs-site/docs/security/) |
| Configure the operator | [Configuration](docs-site/docs/configuration/) |
| Fix a problem | [Troubleshooting](docs-site/docs/troubleshooting.md) |
| Look up a field | [API reference](docs-site/docs/reference/) |

## For contributors

| Folder | Contents |
| --- | --- |
| `api/` | The `DBInstance`, `DBSnapshot` and `DBRestore` types |
| `cmd/`, `internal/` | The operator: controllers, reconcile steps, Harvester client |
| `charts/` | The Helm chart |
| `config/` | Kustomize manifests and samples |
| `deploy/` | The Harvester Addon manifest |
| `images/` | Database image build files |
| `test/` | End-to-end test material |
| `docs-site/` | The documentation site |

```sh
make manifests generate fmt vet build   # regenerate the CRDs and DeepCopy, build the manager
make test                               # envtest-backed unit tests
make docker-buildx IMG=...              # cross-build linux/amd64 and push
make install                            # apply the CRDs with the current kubeconfig
make deploy IMG=...                     # apply the manager and RBAC
make undeploy && make uninstall         # remove everything
```

**Development install (kustomize).** This is a developer path. Don't combine it with the Helm install on the same cluster.

```sh
make docker-buildx IMG=<registry>/<name>:<tag>
KUBECONFIG=<harvester-kubeconfig> make install
KUBECONFIG=<harvester-kubeconfig> make deploy IMG=<registry>/<name>:<tag>
```

`make install` installs only the CRDs. `make deploy IMG=...` also records the image in `config/manager/kustomization.yaml`.

To load settings from a config file, edit [`config/overlays/operator-config/operator_config.yaml`](config/overlays/operator-config/operator_config.yaml) and apply the overlay. The install namespace is set once, by `namespace` in `config/overlays/operator-config/kustomization.yaml`, and it must exist first.

```sh
kubectl create namespace dbaas-system
kubectl apply -k config/overlays/operator-config
```

Operator settings (a config file at `/etc/dbaas/config.json`, `DBAAS_` environment variables and flags) are described in [Operator configuration](docs-site/docs/configuration/operator-config.md). Changes need an operator restart.

---

Part of the [WSO2 Open Cloud Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative. Licensed under Apache-2.0.
