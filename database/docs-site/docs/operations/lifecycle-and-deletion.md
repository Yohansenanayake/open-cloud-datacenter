---
title: Lifecycle and deletion
sidebar_position: 4
---

# Lifecycle and deletion

## Create a database

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: mydb
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

```bash
kubectl apply -f mydb.yaml
kubectl wait dbinstance/mydb --for=condition=Ready --timeout=20m
kubectl get dbinstance mydb
```

**What happens.** The operator validates the request, creates the credentials, the VM and the metrics objects, waits for PostgreSQL to come up, and then marks the instance `Ready` (phase `available`). Once `Ready`, the connection details are in `status.endpoint`.

- **Invalid requests stop early.** A bad class, network or immutable field is rejected before anything is created. The phase shows `incompatible-parameters` and the condition message says what to fix.
- **Provisioning takes minutes.** The first boot of a new VM is the slow part, which is why the example waits up to 20 minutes.
- **Bootstrap data is wiped after start-up.** Once the database is ready, the operator replaces the sensitive cloud-init data with an empty config. The Secret itself stays, because the running VM has it mounted.

## Delete a database

```bash
kubectl delete dbinstance mydb
```

**With `deletionProtection: true`, deletion is blocked.** The request is accepted, but nothing is deleted and the condition `DeletionBlocked` shows `DeletionProtected`. To go ahead:

```bash
kubectl patch dbinstance mydb --type merge -p '{"spec":{"deletionProtection":false}}'
```

The pending deletion then continues.

**What gets deleted:** the VM, the metrics objects, and the credentials, connection and cloud-init Secrets, plus the internal Secrets in the operator namespace. A password Secret you supplied yourself (`spec.credentials`) is never changed or deleted.

**Check your disks after deleting.** The operator deletes the VM but does not explicitly delete its data and OS disks. Whether they are removed depends on Harvester and the StorageClass reclaim policy.
- Run `kubectl get pvc` after a delete and clean up leftovers by hand.
- To keep the data for sure, use a StorageClass with reclaim policy `Retain`.
- A new `DBInstance` with the same name gets new disks and never reattaches an old one.

**Backups and deletion**
- If a backup of the instance is still running, deletion **waits** for it to finish and leaves the VM alone (`DeletionWaitingForSnapshot`).
- Automated snapshots are deleted with the instance.
- Manual snapshots stay, and you can still restore from them. See [Restore](/backup-restore/restore).

**Watch it**

```bash
kubectl get dbinstance mydb -o jsonpath='{.status.phase}{"\n"}'
kubectl get events --field-selector involvedObject.name=mydb
```

If deletion seems stuck, check the `DeletionBlocked` condition. `TeardownFailed` and `OperatorSecretCleanupFailed` mean the operator hit an error and is retrying.
