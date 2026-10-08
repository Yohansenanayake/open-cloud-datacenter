---
title: Connecting from an application
sidebar_position: 10
---

# Connecting from an application

A database becomes reachable when `status.phase` is `available`. The operator then publishes everything a client needs, split across two Secrets in the **same namespace as the `DBInstance`**:

| Secret | Keys | Contains password? |
| --- | --- | --- |
| `pg-<name>-connect` | `host`, `port`, `dbname`, `jdbcUrl`, `sslmode`, `ca.crt` | No |
| `pg-<name>-credentials` | `admin_user`, `admin_password` | Yes |

The exact names are also recorded in `status.resources.connectionSecretName` and `status.resources.adminCredentialsSecretName`. In the examples below the instance is `orders-db` in namespace `tenant-acme`.

## Connection Secret

```sh
kubectl get secret pg-orders-db-connect -n tenant-acme -o jsonpath='{.data}' | head
```

| Key | Value |
| --- | --- |
| `host` | The VM's data-net IPv4 address (`status.endpoint.address`) |
| `port` | `spec.port` (default 5432) |
| `dbname` | `spec.dbName`, or the `DBInstance` name if empty |
| `jdbcUrl` | `jdbc:postgresql://<host>:<port>/<dbname>?ssl=true&sslmode=verify-ca` |
| `sslmode` | Always `verify-ca` |
| `ca.crt` | PEM CA certificate that signed the server certificate |

The Secret is re-reconciled whenever the address changes, so after a restart or live migration it points at the new IP. Existing clients must re-read it (or restart) to pick up a new address.

The user name is **not** in this Secret: take it from `admin_user` in `pg-<name>-credentials`.

## Connect with psql

From a machine on the database VLAN (or routed to it), with `kubectl` access to the cluster:

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

Or as a single connection string:

```sh
psql "host=$PGHOST port=$PGPORT dbname=$PGDATABASE user=$PGUSER sslmode=verify-ca sslrootcert=ca.crt"
```

### Quick test without the CA

`sslmode=require` encrypts the connection but does not verify the server. It is acceptable for a connectivity test only:

```sh
psql "host=$PGHOST port=$PGPORT dbname=$PGDATABASE user=$PGUSER sslmode=require"
```

Plain-text connections are refused (`hostssl` rules only). `sslmode=verify-full` fails when connecting by IP, because the server certificate only names `pg-<name>`; see [TLS and access](/security/tls-and-access).

## From an application

Mount or inject the two Secrets. Runtime pods must be able to route to the VLAN behind `spec.networkRef`; a Secret in the same namespace does not imply network reachability.

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

For JDBC, use `jdbcUrl` and add `sslrootcert=/etc/dbaas/ca.crt` so the `verify-ca` check can find the CA. The `jdbcUrl` value alone does not carry the CA.

:::note
The credentials Secret holds the **master** user, which can create databases and roles. For applications, create a less privileged role and database user with the master account and give the application those credentials instead.
:::

## Troubleshooting a failing connection

- **Timeout:** the client is not on, or routed to, the database VLAN. Test with `nc -vz <host> <port>`.
- **`connection requires SSL` or `no pg_hba.conf entry`:** the client is not using TLS; set `sslmode=verify-ca` (or `require`).
- **`password authentication failed`:** re-read `admin_password` from `pg-<name>-credentials`. Password edits to a BYO source Secret after creation do not change the database; see [Credentials](/security/credentials).
- **Connection Secret missing:** the instance has not reached a reachable endpoint yet. Check `kubectl get dbinstance orders-db -n tenant-acme` and `status.conditions`. See [Troubleshooting](/troubleshooting).
- **Certificate verify failed:** the `ca.crt` is for this instance only; each instance has its own CA.

:::info Verified against
- `database/internal/resource/connection_secret.go`
- `database/internal/ensure/connection_secret.go`
- `database/internal/ensure/credentials.go`
- `database/internal/ensure/health.go`
- `database/internal/credentials/resolver.go`
- `database/internal/credentials/cloudinit.go`
- `database/api/v1alpha1/dbinstance_types.go`
:::
