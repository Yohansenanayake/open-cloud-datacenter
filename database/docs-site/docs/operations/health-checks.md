---
title: Health checks
sidebar_position: 5
---

# Health checks

The operator judges health from the VM's Kubernetes and KubeVirt state. It never logs in to PostgreSQL.

## How health is checked

A readiness probe runs **inside the VM** through the QEMU guest agent. It passes only when two things are true:

- the bootstrap has finished (a marker file exists), and
- PostgreSQL accepts connections (`pg_isready`).

Timing:
- The first check runs 30 seconds after boot, then every 10 seconds.
- It takes about 30 seconds of passes before `Ready` returns.
- It takes about 2 minutes of failures before `Ready` goes false, so brief blips don't flip it.
- If the guest agent disconnects, the probe fails.

## What you will see

| State | Meaning |
| --- | --- |
| `Ready=True`, phase `available` | PostgreSQL and monitoring are healthy |
| `DatabaseReady=False` while creating | The VM is booting or PostgreSQL is initializing. Wait. |
| `Degraded=True` | A running instance has a problem. See the reasons below. |
| phase `crash-loop-halted` | The VM kept crashing and was halted. It needs your help. |

**Degraded reasons**

| Reason | Cause |
| --- | --- |
| `VMRestarting` | The VM is not running (restarting, or stopped outside the operator) |
| `GuestAgentDisconnected` | The guest agent is down, so health is unknown |
| `PostgresUnreachable` | The VM and agent are fine, but PostgreSQL is not responding |

- **The operator reports, it doesn't restart.** It never restarts a VM just because a probe fails.
- **Warning events** are raised when an instance becomes degraded or the reason changes, not on every check.
- **`Ready` needs monitoring too.** `DatabaseReady` covers PostgreSQL only. `Ready` also requires monitoring to be deployed.
- **A rejected edit doesn't make a healthy database unhealthy.** The `Accepted` condition and phase report the rejection, while `Ready` stays true.

When healthy, `status.endpoint` holds the VM's IP, the port and a JDBC URL with `ssl=true&sslmode=verify-ca`. The address is refreshed on every healthy check, because it can change after a restart or live migration. See [connecting](/connecting).

## Crash-loop protection

Without a limit, a VM that keeps crashing would restart forever. So:

- **Unplanned restarts are counted.** A planned start or a live migration does not count.
- **After 3 restarts within 10 minutes of each other**, the operator halts the VM and sets `CrashLoopHalted=True` and `InterventionRequired=True`. The phase becomes `crash-loop-halted`.
- **While halted**, setting `spec.running: true` does **not** restart the VM.
- **To recover:** fix the cause and start the VM yourself, for example in Harvester. Once the VM is running, ready and has a new identity, the operator clears the halt on its own and emits a `Recovered` event.

```bash
kubectl get dbinstance mydb -o jsonpath='{.status.conditions[?(@.type=="CrashLoopHalted")].message}{"\n"}'
kubectl get dbinstance mydb -o jsonpath='{.status.restartCount} {.status.recentUnplannedRestarts}{"\n"}'
```

## Checking health

```bash
kubectl get dbinstance mydb
kubectl wait dbinstance/mydb --for=condition=Ready --timeout=10m
kubectl get dbinstance mydb -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl get events --field-selector involvedObject.name=mydb
```

A healthy instance shows:

| Field | Value |
| --- | --- |
| `status.phase` | `available` |
| `DatabaseReady` | `True` (`PostgresReady`) |
| `Ready` | `True` (`DBInstanceReady`) |
| `Degraded`, `CrashLoopHalted` | absent |
| `status.endpoint.address` | the VM's IP |

For diagnosing failures, see [troubleshooting](/troubleshooting).
