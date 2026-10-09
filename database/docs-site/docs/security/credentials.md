---
title: Credentials
sidebar_position: 1
---

# Credentials

Every `DBInstance` has one **master user** (default `dbadmin`, set with `spec.masterUsername`) and one **master password**. The operator generates the password: 32 random URL-safe characters.

## Where to find the password

The password is in the Secret `pg-<name>-credentials`, in the instance's namespace (keys `admin_user` and `admin_password`):

```sh
kubectl get secret pg-orders-db-credentials -n tenant-acme -o jsonpath='{.data.admin_user}' | base64 -d; echo
kubectl get secret pg-orders-db-credentials -n tenant-acme -o jsonpath='{.data.admin_password}' | base64 -d; echo
```

`masterUsername` can't be `postgres`, `postgres_exporter`, `replicator`, `repl`, `public` or `none`, and can't start with `pg_`.

## After creation

- **There is no password rotation.** The operator can't change the password of a running database. If you change it inside PostgreSQL (`ALTER ROLE`), update `admin_password` in `pg-<name>-credentials` yourself so the record stays correct.
- **A repave keeps the password.**
- **Credentials are generated once and reused.** Keep these Secrets and back them up. A replacement wouldn't match the running database.

## Secrets created for each instance

| Secret | Namespace | Holds |
| --- | --- | --- |
| `pg-<name>-credentials` | tenant | `admin_user`, `admin_password` |
| `pg-<name>-connect` | tenant | host, port, database, JDBC URL, `ca.crt`. **No password.** See [Connecting](/connecting). |
| `pg-<name>-cloudinit` | tenant | First-boot data. Its contents are wiped once the database is ready. |
| `dbi-<uid>-internal` | operator namespace | Internal replication and metrics passwords |
| `dbi-<uid>-tls` | operator namespace | The certificate authority and server certificate |

The two `dbi-` Secrets are not for tenants. Your RBAC decides who can read them.

## Check the status

```sh
kubectl get dbinstance orders-db -n tenant-acme \
  -o jsonpath='{.status.conditions[?(@.type=="CredentialsReady")]}{"\n"}'
```

The status never contains the password.

| `CredentialsReady` | Meaning |
| --- | --- |
| `True` | All good |
| `False`, `CredentialsResolveFailed` | A temporary error. It retries. |

See [Status and conditions](/reference/status-and-conditions).

## If you lose a password or Secret

| Situation | What to do |
| --- | --- |
| Forgot the password, instance is fine | Read `pg-<name>-credentials` |
| `pg-<name>-credentials` was deleted | Restore it from a backup, or recreate it with keys `admin_user` and `admin_password` holding the password the database really uses |
| `dbi-<uid>-internal` or `dbi-<uid>-tls` is missing | A platform admin must restore it from a cluster backup. |

If no copy of the password exists, it can't be recovered. It can only be reset inside PostgreSQL, which needs a shell on the VM. That is possible only when the instance was created with `spec.vmPassword` (see [TLS and access](/security/tls-and-access)). There is no operator-driven password reset.

## Deleting an instance

Deletion removes the VM, the monitoring objects and all five Secrets above.
