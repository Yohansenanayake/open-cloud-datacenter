---
title: TLS and access
sidebar_position: 2
---

# TLS and access

## TLS is always on

Every database accepts **TLS connections only**, with passwords checked using SCRAM-SHA-256. A client that connects without TLS is rejected.

There is no source-address filter inside PostgreSQL. Who can reach the port is decided by the VLAN the VM is attached to. See [Networking](/networking).

## Certificates

The operator creates a private certificate authority and a server certificate for each database, once, and stores them in the operator-namespace Secret `dbi-<uid>-tls`. Both are valid for 10 years.

The CA **certificate** is published to you in the connection Secret `pg-<name>-connect`, under `ca.crt`. The CA **key** is never shared with tenants.

Which `sslmode` to use:

| `sslmode` | Works? |
| --- | --- |
| `verify-ca` | **Yes. Recommended.** This is what the connection Secret advertises. The client checks the server against `ca.crt`. |
| `require` | Encrypts, but doesn't verify the server |

Not available in this release: certificate rotation, and client-certificate authentication (mTLS).

## What the operator creates inside PostgreSQL

| Role | Purpose |
| --- | --- |
| Master user (`spec.masterUsername`, default `dbadmin`) | Your administrator. It can create databases and roles, but it is **not** a superuser. It owns the database. |
| `postgres_exporter` | Metrics only |

Passwords are never passed on command lines. The bootstrap check confirms the master user can log in over TLS before the instance can become `Ready`.

## VM login

By default the VM has **no password login, and no SSH keys are installed**. Tenants are not expected to log in to the VM. Do all database administration as the master user over SQL.

`spec.vmPassword` is the exception. It enables console and SSH password login for the VM's OS user. It is meant for **development and debugging only**, and can't be changed after creation.

Anyone who can create a `DBInstance` can set it, so leave it empty in production.

Without `vmPassword`, the only ways into the VM are Harvester's own console and tools, which are outside the operator.

## What a tenant can and cannot do

| Can | Cannot |
| --- | --- |
| Create, modify and delete `DBInstance`s in their namespace (per RBAC) | Read other tenants' Secrets |
| Read their own `pg-<name>-credentials` and `pg-<name>-connect` Secrets (if RBAC allows) | Read the operator-namespace `dbi-<uid>-*` Secrets |
| Create databases and roles as the master user | Become a PostgreSQL superuser |
| Connect over TLS with SCRAM-SHA-256 | Connect without TLS |
| | Change `networkRef`, `port`, `masterUsername`, `dbName` or `vmPassword` after creation |

The operator ships view, editor and admin roles for `DBInstance`. Access to Secrets comes from your own cluster roles, not from these.
