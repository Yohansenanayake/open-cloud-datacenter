---
title: Connecting from an application
sidebar_position: 5
---

# Connecting from an application

A database is ready to connect to when its phase is `available`. Everything a client needs is in two Secrets in the **same namespace as the `DBInstance`**:

| Secret | Keys | Has a password? |
| --- | --- | --- |
| `pg-<name>-connect` | `host`, `port`, `dbname`, `jdbcUrl`, `sslmode`, `ca.crt` | No |
| `pg-<name>-credentials` | `admin_user`, `admin_password` | Yes |

The examples use an instance `orders-db` in namespace `tenant-acme`.

## Before you connect

- **Network.** The client must be on, or routed to, the database's VLAN (`spec.networkRef`). Having access to the Secrets does **not** give a pod network access. Pods in your cluster may not be able to reach the VLAN. Ask your administrator.
- **TLS only.** Plain-text connections are refused. Use `sslmode=verify-ca` with the CA certificate from the connection Secret.
- **Same namespace.** A pod can only read these Secrets from its own namespace. For an application in another namespace, copy the values you need into that namespace.
- **The user is the master user.** It can create databases and roles, but it is not a superuser. For applications, see [Use a less privileged user](#use-a-less-privileged-user).

## What the connection Secret contains

| Key | Value |
| --- | --- |
| `host` | The database's IP address (`status.endpoint.address`) |
| `port` | `spec.port` (default 5432) |
| `dbname` | The database name: `spec.dbName`, or the instance name made valid (`orders-db` becomes `orders_db`) |
| `jdbcUrl` | `jdbc:postgresql://<host>:<port>/<dbname>?ssl=true&sslmode=verify-ca` |
| `sslmode` | Always `verify-ca` |
| `ca.crt` | The CA certificate that signed the server's certificate. Each instance has its own. |

**The address can change.** After a restart or live migration, the Secret is updated with the new IP. Running clients must re-read it, or restart, to pick it up.

The user name isn't in this Secret. Take it from `admin_user` in `pg-<name>-credentials`.

## In the Rancher UI

Open the database and choose the **Connection** tab. It shows the endpoint, JDBC URL, a `psql` command, the username and password, and a **Download ca.crt** button. See [Rancher UI extension](/rancher-ui-extension/create-and-connect#connect-to-a-database).

## Connect with psql

From a machine on the database VLAN, with `kubectl` access to the cluster:

```sh
NS=tenant-acme; DB=orders-db

export PGHOST=$(kubectl get secret pg-$DB-connect -n $NS -o jsonpath='{.data.host}' | base64 -d)
export PGPORT=$(kubectl get secret pg-$DB-connect -n $NS -o jsonpath='{.data.port}' | base64 -d)
export PGDATABASE=$(kubectl get secret pg-$DB-connect -n $NS -o jsonpath='{.data.dbname}' | base64 -d)
export PGSSLMODE=$(kubectl get secret pg-$DB-connect -n $NS -o jsonpath='{.data.sslmode}' | base64 -d)
kubectl get secret pg-$DB-connect -n $NS -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
export PGSSLROOTCERT=$PWD/ca.crt

export PGUSER=$(kubectl get secret pg-$DB-credentials -n $NS -o jsonpath='{.data.admin_user}' | base64 -d)
export PGPASSWORD=$(kubectl get secret pg-$DB-credentials -n $NS -o jsonpath='{.data.admin_password}' | base64 -d)

psql -c '\conninfo'
```

Or as one connection string:

```sh
psql "host=$PGHOST port=$PGPORT dbname=$PGDATABASE user=$PGUSER sslmode=verify-ca sslrootcert=ca.crt"
```

**Quick connectivity test.** `sslmode=require` encrypts the connection but doesn't check the server. Use it for a test only:

```sh
psql "host=$PGHOST port=$PGPORT dbname=$PGDATABASE user=$PGUSER sslmode=require"
```

## From an application on Kubernetes

Inject the two Secrets into the pod, and mount the CA certificate:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: orders-app
  namespace: tenant-acme
spec:
  containers:
    - name: app
      image: example/orders-app:1.0
      env:
        - name: PGHOST
          valueFrom: {secretKeyRef: {name: pg-orders-db-connect, key: host}}
        - name: PGPORT
          valueFrom: {secretKeyRef: {name: pg-orders-db-connect, key: port}}
        - name: PGDATABASE
          valueFrom: {secretKeyRef: {name: pg-orders-db-connect, key: dbname}}
        - name: PGSSLMODE
          valueFrom: {secretKeyRef: {name: pg-orders-db-connect, key: sslmode}}
        - name: PGSSLROOTCERT
          value: /etc/dbaas/ca.crt
        - name: PGUSER
          valueFrom: {secretKeyRef: {name: pg-orders-db-credentials, key: admin_user}}
        - name: PGPASSWORD
          valueFrom: {secretKeyRef: {name: pg-orders-db-credentials, key: admin_password}}
      volumeMounts:
        - name: ca
          mountPath: /etc/dbaas
          readOnly: true
  volumes:
    - name: ca
      secret:
        secretName: pg-orders-db-connect
        items:
          - key: ca.crt
            path: ca.crt
```

- **Libpq-based clients** (`psql`, Python's `psycopg`, and others) read these `PG*` variables by themselves.
- **Other drivers** (such as Node.js or Go) may not. Set the host, port, user, password and the CA file path in the driver's own SSL options.

### JDBC

The `jdbcUrl` in the Secret doesn't include the CA. Add `sslrootcert` so the `verify-ca` check can find it:

```text
jdbc:postgresql://<host>:<port>/<dbname>?ssl=true&sslmode=verify-ca&sslrootcert=/etc/dbaas/ca.crt
```

Pass the user and password as the connection's `user` and `password` properties, not in the URL.

## Use a less privileged user

`pg-<name>-credentials` is the **master** user. For applications, connect as the master user once and create a limited role and database for each application:

```sql
CREATE ROLE orders_app LOGIN PASSWORD '...';
CREATE DATABASE orders_prod OWNER orders_app;
```

Then give the application those credentials, in your own Secret.

## If the connection fails

| Symptom | Likely cause and fix |
| --- | --- |
| **Timeout** | The client isn't on, or routed to, the database VLAN. Test with `nc -vz <host> <port>`. |
| `connection requires SSL` or `no pg_hba.conf entry` | The client isn't using TLS. Set `sslmode=verify-ca` (or `require`). |
| **Certificate verify failed** | The `ca.crt` is for a different instance, or the file path is wrong. Each instance has its own CA. |
| `password authentication failed` | Re-read `admin_password` from `pg-<name>-credentials`. See [Credentials](/security/credentials). |
| **Worked before, now times out** | The address may have changed after a restart. Re-read `host` from the connection Secret. |
| **Connection Secret is missing** | The database has no address yet. Check `kubectl get dbinstance orders-db -n tenant-acme` and its conditions. See [Troubleshooting](/troubleshooting). |
