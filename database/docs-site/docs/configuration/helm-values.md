---
title: Helm values
sidebar_position: 3
---

# Helm values

Reference for `charts/chart/values.yaml` (chart `dbaas-operator`, version `0.1.0`). Resource names are `RELEASE-NAME-SUFFIX` unless `fullnameOverride` is set; if the release name already contains the chart name it is used as is.

## Naming

| Key | Default | Meaning |
| --- | --- | --- |
| `nameOverride` | unset | Partially override the chart name used in names and labels. |
| `fullnameOverride` | unset | Fully replace the generated base name. |

## manager

| Key | Default | Meaning |
| --- | --- | --- |
| `manager.enabled` | `true` | Set to `false` to skip the Deployment. |
| `manager.replicas` | `1` | Replica count. More than one needs leader election (on by default). |
| `manager.image.repository` | `ghcr.io/wso2/dbaas-operator` | Image repository. If it contains `@` (a digest) no tag is appended. |
| `manager.image.tag` | `""` | Empty uses `Chart.appVersion`. |
| `manager.image.pullPolicy` | `IfNotPresent` | Pull policy. |
| `manager.args` | `--operator.leaderElection.enabled=true`, `--observability.metrics.bindAddress=:8443` | Flags for `/manager`. Replaces rather than merges. Any key in [Operator configuration](/configuration/operator-config) can be set this way. |
| `manager.env` | `POD_NAMESPACE` from `metadata.namespace` | Container environment. `POD_NAMESPACE` is required by the operator. |
| `manager.envOverrides` | `{}` | Map of `NAME: value` appended to the environment (use for `DBAAS_*` variables). The chart comment says a same-named entry takes precedence over `env`; the template simply appends them after `env`. |
| `manager.imagePullSecrets` | unset (commented) | List of pull secret names. |
| `manager.podSecurityContext` | `runAsNonRoot: true`, `seccompProfile: RuntimeDefault` | Pod security context. |
| `manager.securityContext` | `allowPrivilegeEscalation: false`, drop `ALL`, `readOnlyRootFilesystem: true` | Container security context. |
| `manager.resources` | limits `500m` / `128Mi`; requests `10m` / `64Mi` | Resources. |
| `manager.affinity` | `{}` | Pod affinity. |
| `manager.nodeSelector` | `{}` | Node selector. |
| `manager.tolerations` | `[]` | Tolerations. |
| `manager.terminationGracePeriodSeconds` | `10` | Grace period. |
| `manager.strategy` | unset (commented) | Deployment strategy. |
| `manager.priorityClassName` | unset (commented) | Priority class. |
| `manager.topologySpreadConstraints` | unset (commented) | Topology spread. |
| `manager.labels` | unset (commented) | Extra Deployment labels. |
| `manager.annotations` | unset (commented) | Extra Deployment annotations. |
| `manager.pod.labels`, `manager.pod.annotations` | unset (commented) | Extra pod labels and annotations. |

Template-supported keys that are absent from `values.yaml`: `manager.extraVolumes` and `manager.extraVolumeMounts` (both default to empty lists). Use them to mount a config file at `/etc/dbaas/config.json`, or metrics TLS material.

The container always runs `/manager`, exposes port 8081 (`health`), and uses `/healthz` (liveness, 15s delay, 20s period) and `/readyz` (readiness, 5s delay, 10s period) on that port. These probe settings are not configurable via values.

## rbac and serviceAccount

| Key | Default | Meaning |
| --- | --- | --- |
| `rbac.helpers.enable` | `false` | Install the `dbinstance-admin`, `-editor` and `-viewer` ClusterRoles. |
| `serviceAccount.enable` | `true` | Create the ServiceAccount. When `false` and `serviceAccount.name` is set, that existing account is used. |
| `serviceAccount.name` | unset | Existing ServiceAccount name (only with `enable: false`). |
| `serviceAccount.annotations`, `serviceAccount.labels` | unset | Extra metadata. |

RBAC is always cluster-wide; there is no namespaced option. See [RBAC](/installation/rbac).

## crd

| Key | Default | Meaning |
| --- | --- | --- |
| `crd.enable` | `true` | Install the `DBInstance` CRD with the chart. |
| `crd.keep` | `true` | Adds `helm.sh/resource-policy: keep`, so uninstall does not delete the CRD. |

## metrics, certManager, prometheus

| Key | Default | Meaning |
| --- | --- | --- |
| `metrics.enable` | `true` | Create the `...-controller-manager-metrics-service` Service. |
| `metrics.port` | `8443` | Service port and target port. Does not change what the manager listens on. |
| `metrics.secure` | `true` | Selects `https` vs `http` for the Service port name and for the `ServiceMonitor`. Does not change the manager; set `--observability.metrics.secure` separately. |
| `certManager.enable` | `false` | Only switches the `ServiceMonitor` TLS config to use a `metrics-server-cert` Secret instead of `insecureSkipVerify`. No `Certificate` resource is created. |
| `prometheus.enable` | `false` | Create the operator's own `ServiceMonitor` (needs the Prometheus Operator CRDs). When secure, it uses the pod ServiceAccount token. |

`metrics.secure` and the `ServiceMonitor` use `metrics-auth-role` for token review; grant the Prometheus ServiceAccount the `metrics-reader` ClusterRole to allow scraping.

## Example

```yaml
manager:
  image:
    repository: ghcr.io/YOU/dbaas-operator
    tag: "0.1.0"
  args:
    - --operator.leaderElection.enabled=true
    - --observability.metrics.bindAddress=:8443
  envOverrides:
    DBAAS_LOGGING__LEVEL: debug
prometheus:
  enable: true
```
