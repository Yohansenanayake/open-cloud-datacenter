---
title: Samples
sidebar_position: 4
---

# Samples

Replace `networkRef: default/vm-net-100` with the network of your own VLAN, and create the instance in the namespace you want. Shipped manifests are in `database/config/samples/`.

## Minimal manifest

Only three fields are required:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: minimal
spec:
  dbInstanceClass: db.t3.micro
  allocatedStorage: 10
  networkRef: default/vm-net-100
```

## Standard instance

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
  networkRef: default/vm-net-100
  deletionProtection: true
  running: true
```

This creates:
- A 2 vCPU, 4 GiB VM and a 50 GiB data disk.
- PostgreSQL 16 with a database `myapp` and administrator `dbadmin`.
- A generated password. Read it from the Secret `pg-dbinstance-sample-credentials`.

Things to know:
- It has no `spec.backup`, so it can **never** be snapshotted. Backup is chosen at creation. See the backup example below.
- `deletionProtection: true` blocks `kubectl delete` until you set it to `false`.

Apply and watch:

```bash
kubectl apply -f database/config/samples/dbaas_v1alpha1_dbinstance.yaml
kubectl get dbi -w
kubectl wait --for=condition=Ready dbinstance/dbinstance-sample --timeout=15m
```

Remove:

```bash
kubectl patch dbinstance dbinstance-sample --type merge -p '{"spec":{"deletionProtection":false}}'
kubectl delete dbinstance dbinstance-sample
```

## Backup and restore

These manifests are used throughout [Backup and restore](/backup-restore/overview). They are not shipped as files.

An instance with backups (the defaults spelled out):

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: mydb
  namespace: tenant-acme
spec:
  dbInstanceClass: db.t3.medium
  allocatedStorage: 50
  engineVersion: "16"
  networkRef: default/vm-net-100
  backup:
    automated:
      enabled: true
      retainCount: 7
      preferredWindowUTC: "02:00-03:00"
```

A manual snapshot of it:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBSnapshot
metadata:
  name: mydb-before-upgrade
  namespace: tenant-acme
spec:
  sourceInstanceRef:
    name: mydb
```

Restoring that snapshot into a new instance. The snapshot must be `Ready`. The database name, user, engine version, port and storage type come from the snapshot. You choose the rest:

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBRestore
metadata:
  name: mydb-restore-1
  namespace: tenant-acme
spec:
  snapshotRef:
    name: mydb-before-upgrade
  targetInstanceName: mydb-restored
  dbInstanceClass: db.t3.medium
  networkRef: default/vm-net-100
  allocatedStorage: 50
```
