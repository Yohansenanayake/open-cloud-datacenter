---
title: Known gotchas
sidebar_position: 6
---

# Known gotchas

Items carried over from `INSTALL.md`, rechecked against the code, plus gaps found in the chart.

| Symptom | Cause | Fix |
| --- | --- | --- |
| Addon rejected with `spec.repo: Required value` | Harvester's Addon schema requires the `repo` key | Set `repo: ""` explicitly. |
| Manager pod in `ImagePullBackOff` | Default image `ghcr.io/wso2/dbaas-operator` not available, tag not pushed, or package private | Override `manager.image.*`, push the tag, make the package public or add a pull secret. |
| `DBInstance` phase `incompatible-parameters` right after create | `Accepted=False` from preflight | Read `.status.conditions`; usually `OSImageNotFound` or `OSImageInvalid`. See [Prerequisites](/installation/prerequisites). |
| `Accepted` reason `VMPasswordNotAllowed` | `security.rejectVMPassword=true` and the manifest sets `spec.vmPassword` | Remove `vmPassword` and recreate, or turn the switch off on a dev install. See [Policy switches](/configuration/policy-switches). |
| Leader election and metrics vanish after setting `manager.args` | Lists replace chart defaults | Repeat both default flags. |
| Image upload to Harvester times out around 700 MB | A Rancher proxying load balancer caps upload size | Upload directly against Harvester. |
| Manager exits at start with `POD_NAMESPACE must be set` | The variable is required and comes from the Downward API | Keep the default `manager.env` entry. |
| Config file not picked up | Only the fixed path `/etc/dbaas/config.json` is read; `DBAAS_CONFIG_FILE` and a `--config-file` flag do not exist | Mount the file at that path. |
| `operator.namespace is installation metadata` at start | The key is explicitly rejected in any source | Remove it; the namespace is the Pod namespace. |
| REST gateway on port 8080 not reachable through a Service | The gateway is enabled by default but neither the chart nor kustomize creates a Service for it (only the health and metrics ports are declared) | Disable it with `server.gateway.enabled=false` if unused, or add your own Service. |
| `metrics.secure=false` in chart values but metrics stay HTTPS | The value only changes the chart Service and `ServiceMonitor`; the manager reads `observability.metrics.secure` (default true) | Also pass `--observability.metrics.secure=false`. |
| `certManager.enable=true` creates no certificates | The chart has no `Certificate` template; the value only changes `ServiceMonitor` TLS to reference a `metrics-server-cert` Secret | Provide that Secret and the cert path yourself, or leave it off. |

Chart value `metrics.port` likewise only changes the Service; the manager listens on whatever `--observability.metrics.bindAddress` says (chart default `:8443`).

:::info Verified against
- `INSTALL.md` (Known gotchas)
- `internal/config/load.go`, `load_test.go`, `namespace.go`, `defaults.go`
- `internal/ensure/preflight.go`
- `charts/chart/values.yaml`, `charts/chart/templates/**`
- `cmd/main.go`, `internal/gateway/gateway.go`
:::
