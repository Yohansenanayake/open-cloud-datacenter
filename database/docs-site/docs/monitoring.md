---
title: Monitoring
sidebar_position: 9
---

# Monitoring

Each database VM runs `prometheus-postgres-exporter`. The operator creates, per instance, the Kubernetes objects that let a Prometheus (Prometheus Operator) scrape it.

## What is deployed

Inside the VM, bootstrap writes `/etc/default/prometheus-postgres-exporter` and restarts the service:

- connects as role `postgres_exporter` (member of `pg_monitor`) to `127.0.0.1:<port>`, database `postgres`, `sslmode=require`,
- listens on `:9187` (plain HTTP, `/metrics`).

In the tenant namespace the operator creates three objects (all owned by the `DBInstance`, so they are garbage-collected with it and re-created if deleted):

| Object | Name | Notes |
| --- | --- | --- |
| Service | `pg-<name>-metrics` | Headless (`clusterIP: None`), **no selector**, port `metrics` 9187/TCP. Labels `dbaas.opencloud.wso2.com/instance: <name>` and `dbaas.opencloud.wso2.com/metrics: "true"` |
| Endpoints | `pg-<name>-metrics` | Same name, same labels. One address: the VM's data-net IP, port `metrics` 9187. Re-applied when the IP changes |
| ServiceMonitor | `pg-<name>-monitor` | Selects Services labelled `dbaas.opencloud.wso2.com/metrics: "true"` and `dbaas.opencloud.wso2.com/instance: <name>`. One endpoint: port `metrics`, path `/metrics`, interval from configuration |

A manual Endpoints object is used because the database is a VM, not a pod, so no selector can find it.

The ServiceMonitor carries the labels from `observability.monitoring.serviceMonitorLabels` (default `release: prometheus`) plus `dbaas.opencloud.wso2.com/instance`. The scrape interval is `observability.monitoring.scrapeInterval` (default 15s). Adjust both in [Operator configuration](/configuration/operator-config) to match your Prometheus's `serviceMonitorSelector`.

```sh
kubectl get svc,endpoints,servicemonitor -n tenant-acme -l dbaas.opencloud.wso2.com/instance=orders-db
```

## Dependencies

- The `monitoring.coreos.com` `ServiceMonitor` CRD must exist (Prometheus Operator, for example through **Rancher Monitoring**). The operator is granted RBAC for `servicemonitors`. Without the CRD the monitoring step fails and `MonitoringReady` is `False` with reason `MonitoringDeployFailed`.
- Prometheus must select the ServiceMonitor: its `serviceMonitorSelector` and namespace selector must match the labels above. Whether the default `release: prometheus` matches your Rancher Monitoring release is installation-specific; verify it on your cluster.
- Prometheus must be able to **route** to the VM's IP on port 9187. The exporter has no authentication or TLS.

## What is exported

The metrics are whatever `postgres_exporter` provides for the packaged version (for example `pg_up`, `pg_stat_database_*`, `pg_stat_activity_*`, `pg_settings_*`). The operator does not define, rename or add metrics. Because the VM image installs a stock package, metric names depend on the exporter version in the image.

## Status fields

```sh
kubectl get dbinstance orders-db -n tenant-acme -o jsonpath='{.status.prometheusTarget}{"\n"}'
# pg-orders-db-metrics.tenant-acme.svc:9187
kubectl get dbinstance orders-db -n tenant-acme -o jsonpath='{.status.grafanaUrl}{"\n"}'
```

| Field | Value |
| --- | --- |
| `status.prometheusTarget` | `pg-<name>-metrics.<namespace>.svc:9187` |
| `status.resources.metricsServiceName` | `pg-<name>-metrics` |
| `status.resources.serviceMonitor` | `pg-<name>-monitor` |
| `status.grafanaUrl` | `<grafana baseURL>/d/dbaas-<name>/postgresql-<name>`, empty if no base URL is configured. Default base URL is `https://grafana.monitoring.svc` |

The operator only computes this URL. It does **not** create the Grafana dashboard; you must provide a dashboard with that UID.

## Condition behaviour

- `MonitoringReady=True`, reason `MonitoringDeployed`, once the three objects exist.
- `MonitoringReady=False`, reason `InstanceStopped`, while `spec.running` is false. The objects are kept, but the target is inactive.
- `MonitoringReady=False`, reason `WaitingForEndpoint`, before the VM has an IP.
- If monitoring setup keeps failing while the database is up, the phase is reported as `degraded` ("Database is available, but monitoring is not ready"). The database is not restarted.

## The operator's own metrics

Separate from per-database monitoring, the operator exposes its own controller metrics. The Helm chart passes `--observability.metrics.bindAddress=:8443` with `secure: true` (HTTPS plus authn/authz), and has an optional ServiceMonitor template (`prometheus.enable`, default `false`). The kustomize ServiceMonitor `controller-manager-metrics-monitor` scrapes `https` with the pod's ServiceAccount token. See [Network policy](/security/network-policy) for restricting who can scrape it.

:::info Verified against
- `database/internal/ensure/monitoring.go`
- `database/internal/resource/metrics_service.go`
- `database/internal/resource/metrics_endpoints.go`
- `database/internal/resource/servicemonitor.go`
- `database/internal/credentials/cloudinit.go`
- `database/internal/config/defaults.go`
- `database/api/v1alpha1/dbinstance_types.go`
- `database/api/v1alpha1/dbinstance_conditions.go`
- `database/config/prometheus/monitor.yaml`
- `database/charts/chart/values.yaml`
:::
