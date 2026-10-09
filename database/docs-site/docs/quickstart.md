---
title: Quickstart
sidebar_position: 2
---

# Quickstart

This walks through creating one PostgreSQL instance, connecting with `psql`, and deleting it. It assumes the
operator is already installed (see [Helm and Addon install](/installation/helm-addon)) and that `kubectl` points at
the Harvester cluster.

Before you start, confirm:

- A Multus `NetworkAttachmentDefinition` exists. The sample uses `default/vm-net-100`; change `networkRef` to yours
  (`namespace/name`).
- The baked image for the default OS stream (`ubuntu-2204-postgres-v20260515` for stream `22.04`) is imported and
  ready in Harvester's image namespace (default `default`). Otherwise the instance stops at preflight with
  `OSImageNotFound`.
- Your machine can reach the data network the VM attaches to.

## 1. Create the instance

The repository sample is `database/config/samples/dbaas_v1alpha1_dbinstance.yaml`:

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
  masterUsername: dbadmin
  manageMasterUserPassword: true
  # networkRef points at an existing Harvester Multus NAD (namespace/name).
  networkRef: default/vm-net-100
  deletionProtection: true
  running: true
```

:::note
`manageMasterUserPassword` is accepted but not acted on in v0.1.0: the password is
always generated (unless you set `spec.credentials`). The sample has no `spec.backup`, so no backups are taken, and
that cannot be added later; to opt in, set `spec.backup` when you create the instance
(see [Backup and restore](/backup-restore/overview)). `deletionProtection: true` will block
deletion later; step 5 shows how to lift it.
:::

Apply it into a namespace of your choice (`NS` below), for example:

```sh
kubectl create namespace demo
kubectl apply -n demo -f database/config/samples/dbaas_v1alpha1_dbinstance.yaml
```

## 2. Watch it provision

```sh
kubectl get dbi -n demo -w
```

The columns are `PHASE`, `CLASS`, `ENDPOINT`, `IMAGEDRIFT` and `AGE`. The phase starts at `creating` and becomes
`available` once PostgreSQL's readiness probe passes and the endpoint is published; the README quotes about three
minutes on a stock image, depending on image clone and first-boot speed.

For detail, inspect conditions:

```sh
kubectl get dbi dbinstance-sample -n demo \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}){"\n"}{end}'
kubectl describe dbi dbinstance-sample -n demo
```

You should see `Accepted`, `PreflightReady`, `CredentialsReady`, `VMReady`, `PowerStateReady`, `DatabaseReady`,
`MonitoringReady` and finally `Ready` go `True`. If it sticks, see [Troubleshooting](/troubleshooting) and
[Status and conditions](/reference/status-and-conditions).

What was created (names derive from the instance name `dbinstance-sample`):

```sh
kubectl get vm,secret,svc,servicemonitor -n demo | grep dbinstance-sample
# virtualmachine pg-dbinstance-sample
# secret pg-dbinstance-sample-credentials, -connect, -cloudinit
# service pg-dbinstance-sample-metrics, servicemonitor pg-dbinstance-sample-monitor
```

See [Managed resources](/architecture/managed-resources).

## 3. Get connection details

The connection Secret carries no password; the credentials Secret does.

```sh
kubectl get secret pg-dbinstance-sample-connect -n demo -o jsonpath='{.data.host}' | base64 -d; echo
kubectl get secret pg-dbinstance-sample-connect -n demo -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt

kubectl get secret pg-dbinstance-sample-credentials -n demo -o jsonpath='{.data.admin_user}' | base64 -d; echo
kubectl get secret pg-dbinstance-sample-credentials -n demo -o jsonpath='{.data.admin_password}' | base64 -d; echo
```

## 4. Connect with psql

The server accepts only TLS with SCRAM; the published `sslmode` is `verify-ca`.

```sh
HOST=$(kubectl get secret pg-dbinstance-sample-connect -n demo -o jsonpath='{.data.host}' | base64 -d)
PORT=$(kubectl get secret pg-dbinstance-sample-connect -n demo -o jsonpath='{.data.port}' | base64 -d)
export PGPASSWORD=$(kubectl get secret pg-dbinstance-sample-credentials -n demo -o jsonpath='{.data.admin_password}' | base64 -d)

psql "host=$HOST port=$PORT dbname=myapp user=dbadmin sslmode=verify-ca sslrootcert=ca.crt"
```

Equivalent JDBC URL (also in the Secret and in `status.endpoint.jdbcUrl`):
`jdbc:postgresql://HOST:5432/myapp?ssl=true&sslmode=verify-ca`. The master role has `CREATEDB` and `CREATEROLE` but
is not a superuser. More options are in [Connecting](/connecting).

## 5. Stop, then delete

Stop the instance (storage is preserved) and start it again by toggling `spec.running`:

```sh
kubectl patch dbi dbinstance-sample -n demo --type merge -p '{"spec":{"running":false}}'
kubectl patch dbi dbinstance-sample -n demo --type merge -p '{"spec":{"running":true}}'
```

The sample sets `deletionProtection: true`. While it is true, `kubectl delete` marks the object for deletion but the
controller sets `DeletionBlocked=True` (reason `DeletionProtected`) and tears nothing down. Turn it off first:

```sh
kubectl patch dbi dbinstance-sample -n demo --type merge -p '{"spec":{"deletionProtection":false}}'
kubectl delete dbi dbinstance-sample -n demo
```

The finalizer then deletes the VM, the tenant Secrets, the metrics objects and the operator-namespace Secrets. The
data and OS disk PVCs are not deleted by the operator itself; check with `kubectl get pvc -n demo`. See
[Lifecycle and deletion](/operations/lifecycle-and-deletion). If you used your own password Secret, delete it
yourself.
