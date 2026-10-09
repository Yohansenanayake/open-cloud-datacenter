---
title: Policy switches
sidebar_position: 2
---

# Policy switches

Policy switches are platform-wide settings under the `security` key. They restrict what a `DBInstance` may request. All are off by default. This release has one.

## `security.rejectVMPassword`

When on, the operator rejects new `DBInstance`s that set `spec.vmPassword`. That field enables password login (console and SSH) to the database VM and is meant for development only.

**What happens to a rejected instance**
- `PreflightReady` is `False` with reason `VMPasswordNotAllowed`.
- Nothing is created and there is no retry.
- Remove `spec.vmPassword` and recreate the instance.

**What to know**
- Existing instances are not affected. Their VMs keep the password they were created with.
- New VMs have no console or SSH login at all (no password and no injected key). Plan your access path first. See [TLS and access](/security/tls-and-access) and [Credentials](/security/credentials).
- An existing instance can drop `vmPassword` only by being recreated. Take a `pg_dump` first.
- Recommended for production.

**Turn it on**

| Method | Setting |
| --- | --- |
| Flag | `--security.rejectVMPassword=true` |
| Environment | `DBAAS_SECURITY__REJECT_VM_PASSWORD=true` |
| Config file | `{"security": {"rejectVMPassword": true}}` |

With Helm or the add-on, add the flag to `manager.args`. Repeat the default args, because a list replaces the default:

```yaml
manager:
  args:
    - --operator.leaderElection.enabled=true
    - --observability.metrics.bindAddress=:8443
    - --security.rejectVMPassword=true
```

Check that it is active:

```sh
kubectl get deployment dbaas-operator-controller-manager -n dbaas-system \
  -o jsonpath='{.spec.template.spec.containers[0].args}'
```
