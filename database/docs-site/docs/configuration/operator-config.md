---
title: Operator configuration
sidebar_position: 1
---

# Operator configuration

The operator works out of the box. Every setting has a built-in default, so you only set the values you want to change.

## How settings are loaded

![Configuration sources and precedence](@site/static/img/config-sources-precedence.svg)

Settings come from four places. When the same setting appears in more than one, the later one wins:

1. Built-in defaults
2. Config file: `/etc/dbaas/config.json`
3. Environment variables
4. Command-line flags

Good to know:

- The operator reads its configuration **once at startup**. Restart it to apply a change.
- If a value is invalid, the operator refuses to start and logs `validate configuration: ...`.
- The config file path is fixed, and the file is optional. If it exists but cannot be parsed, startup fails.
- `operator.namespace` is not a setting. The operator takes its namespace from the `POD_NAMESPACE` environment variable (set through the Downward API, and required). Setting `operator.namespace` anywhere fails startup.

## Changing a setting

Every setting has a key such as `controller.maxConcurrentReconciles`. You can set it three ways:

| Method | Example |
| --- | --- |
| Config file | `{ "controller": { "maxConcurrentReconciles": 4 } }` |
| Environment variable | `DBAAS_CONTROLLER__MAX_CONCURRENT_RECONCILES=4` |
| Flag | `--controller.maxConcurrentReconciles=4` |

**Naming rules**

- **Flag:** `--` plus the key, exactly as written.
- **Environment variable:** `DBAAS_` plus the key in upper case. Use `__` between levels and `_` between words. So `databaseDefaults.storageClass` becomes `DBAAS_DATABASE_DEFAULTS__STORAGE_CLASS`.
- **Flags win even when equal to the default.** A flag you pass explicitly always overrides the file and environment, even if its value matches the default.

**Example config file**

```json
{
  "operator": { "leaderElection": { "enabled": true } },
  "databaseDefaults": { "osVersion": "24.04", "storageClass": "longhorn" }
}
```

**With Helm.** The chart passes flags through `manager.args` and environment variables through `manager.env` or `manager.envOverrides` (see [Helm values](/configuration/helm-values)). The chart has no value that mounts a config file for you. If you need one, use `manager.extraVolumes` and `manager.extraVolumeMounts`.

## Settings most people change

| Key | Default | Why change it |
| --- | --- | --- |
| `databaseDefaults.osVersion` | `22.04` | Choose the OS image stream for new databases (`22.04` or `24.04`) |
| `databaseDefaults.storageClass` | `longhorn` | Use a different storage class for database disks |
| `controller.maxConcurrentReconciles` | `1` | Process more `DBInstance`s in parallel (raise for many tenants) |
| `backup.maxConcurrent` | `4` | Limit how many backups run at once |
| `observability.monitoring.serviceMonitorLabels` | `{release: prometheus}` | Match your Prometheus `ServiceMonitor` selector |
| `observability.grafana.baseURL` | `https://grafana.monitoring.svc` | Point per-instance Grafana links at your Grafana |
| `logging.level` | `info` | Turn on `debug` logs when troubleshooting |

## All settings

Defaults are shown in the tables. Flags and environment variables follow the [naming rules](#changing-a-setting) above.

### Operator and controller

| Key | Default | Notes |
| --- | --- | --- |
| `operator.leaderElection.enabled` | `false` | Run only one active operator replica. The Helm chart turns it on. |
| `operator.leaderElection.id` | `734f9ee3.opencloud.wso2.com` | Must not be blank when leader election is on |
| `controller.maxConcurrentReconciles` | `1` | At least 1 |

### Servers

| Key | Default | Notes |
| --- | --- | --- |
| `server.enableHTTP2` | `false` | Leave off unless you need HTTP/2 |
| `server.health.bindAddress` | `:8081` | Serves `/healthz` and `/readyz`, which the Deployment probes use |
| `server.gateway.enabled` | `true` | The optional REST gateway |
| `server.gateway.bindAddress` | `:8080` | Checked only when the gateway is enabled |
| `server.gateway.defaultNamespace` | `default` | Namespace used when a request names none |
| `server.webhook.tls.certDir` | empty | Certificate folder for the webhook server |
| `server.webhook.tls.certFile` | `tls.crt` | Required when `certDir` is set |
| `server.webhook.tls.keyFile` | `tls.key` | Required when `certDir` is set |

The REST gateway (`/healthz`, `/dbinstances`, `/dbinstances/...`) forwards the caller's bearer token to the API server, so Kubernetes still decides who may do what.

:::note
This release registers no admission or conversion webhooks. The `server.webhook.tls.*` keys only set where the webhook server looks for its certificate.
:::

### Harvester

| Key | Default | Notes |
| --- | --- | --- |
| `infrastructure.harvester.imageNamespace` | `default` | Where image names without a `namespace/name` prefix are looked up. Must be a valid namespace name. |

### Database defaults

Applied when a `DBInstance` leaves the field out.

| Key | Default | Notes |
| --- | --- | --- |
| `databaseDefaults.storageClass` | `longhorn` | Not empty |
| `databaseDefaults.masterUsername` | `dbadmin` | Not empty |
| `databaseDefaults.port` | `5432` | 1-65535 |
| `databaseDefaults.osVersion` | `22.04` | OS stream for the whole platform; no per-instance override |

:::caution
`osVersion` accepts any non-blank text, but an unknown or not-yet-validated stream makes every new instance fail with `OSImageInvalid`. See [Prerequisites](/installation/prerequisites).
:::

### Observability

| Key | Default | Notes |
| --- | --- | --- |
| `observability.grafana.baseURL` | `https://grafana.monitoring.svc` | Builds per-instance Grafana links. Empty is allowed; otherwise a full URL. |
| `observability.metrics.bindAddress` | `0` (off) | `0` or `host:port` |
| `observability.metrics.secure` | `true` | Serve metrics over TLS with authentication |
| `observability.metrics.tls.certDir` | empty | If unset while secure, a self-generated certificate is used |
| `observability.metrics.tls.certFile` | `tls.crt` | Required when `certDir` is set |
| `observability.metrics.tls.keyFile` | `tls.key` | Required when `certDir` is set |
| `observability.monitoring.scrapeInterval` | `15s` | Per-instance `ServiceMonitor` interval; must be above zero |
| `observability.monitoring.serviceMonitorLabels` | `{release: prometheus}` | Must match your Prometheus selector. **Config file only** (see below). |

### Backup and restore

| Key | Default | Notes |
| --- | --- | --- |
| `backup.maxConcurrent` | `4` | Backups running at once, cluster-wide. At least 1. |
| `backup.timeout` | `6h` | Limit for one backup |
| `restore.recoveryTimeout` | `1h` | How long a restored instance waits for PostgreSQL recovery |
| `restore.timeout` | `6h` | Limit for a whole restore. Must be greater than `restore.recoveryTimeout`. |

- **Concurrency.** Extra backups wait their turn, oldest first. Each namespace may use at most half the slots (rounded up, never below 1), so the default of 4 gives 2 per namespace. See [Concurrency and holds](/backup-restore/concurrency-and-holds).
- **Backup timeout.** Counted from when the Harvester backup is created, not from queue time. On expiry the snapshot fails as `BackupTimedOut`, the backup is deleted and the slot is freed.
- **Recovery timeout.** Recovery time grows with the amount of WAL the snapshot captured, so size it for your largest database.
- **Restore timeout.** Counted from when the `DBRestore` is created. On expiry the restore fails as `RestoreTimedOut` and its unfinished target is deleted.
- **Duration format.** Values use Go syntax, such as `90m` or `3h`. See [Restore](/backup-restore/restore#timeouts).

### Logging

| Key | Default | Allowed values |
| --- | --- | --- |
| `logging.development` | `false` | `true`, `false` |
| `logging.encoder` | `json` | `json`, `console` |
| `logging.level` | `info` | `debug`, `info`, `warn`, `error`, `panic` |
| `logging.stacktraceLevel` | `error` | same as `level` |
| `logging.timeEncoding` | `rfc3339` | `epoch`, `millis`, `nano`, `iso8601`, `rfc3339`, `rfc3339nano` |

### Instance classes

An instance class sets a database VM's size. The default is the built-in catalog:

| Class | CPU cores | Memory (MB) | Max connections |
| --- | --- | --- | --- |
| `db.t3.micro` | 1 | 1024 | 50 |
| `db.t3.small` | 1 | 2048 | 100 |
| `db.t3.medium` | 2 | 4096 | 150 |
| `db.t3.large` | 2 | 8192 | 200 |
| `db.t3.xlarge` | 4 | 16384 | 300 |
| `db.m5.large` | 2 | 8192 | 200 |
| `db.m5.xlarge` | 4 | 16384 | 400 |
| `db.m5.2xlarge` | 8 | 32768 | 600 |
| `db.m5.4xlarge` | 16 | 65536 | 1000 |
| `db.r5.large` | 2 | 16384 | 300 |
| `db.r5.xlarge` | 4 | 32768 | 500 |
| `db.r5.2xlarge` | 8 | 65536 | 800 |

- Each class needs `cpuCores`, `memoryMB` and `maxConnections`, all at least 1. At least one class must exist, and no name may be blank.
- A `DBInstance` that names a class not in the list fails with `InvalidClass`.
- See [DBInstance spec](/reference/dbinstance-spec) for how instances pick a class.

To define your own classes, put them in the config file as `instanceClasses`. There is no flag.

```json
{
  "instanceClasses": {
    "db.custom.small": { "cpuCores": 2, "memoryMB": 4096, "maxConnections": 150 }
  }
}
```

:::caution
It is not confirmed whether a class list in the config file replaces the built-in list or merges with it. Include every class you need to be safe.
:::

## Settings that must go in the config file

Names that contain dots or are case sensitive can't be set reliably through environment variables:

- `instanceClasses` (class names contain dots, and there is no flag)
- `observability.monitoring.serviceMonitorLabels` (map keys are case sensitive, and there is no flag)
