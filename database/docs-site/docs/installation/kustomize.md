---
title: Kustomize (make deploy)
sidebar_position: 3
---

# Kustomize install

The internal and team path installs from `config/` with kustomize. It is independent of the [Helm path](/installation/helm-addon); do not apply both over the same resources.

## Steps

```sh
# build and push the manager image
make docker-buildx IMG=REGISTRY/NAME:TAG

# install the CRD, then the manager
KUBECONFIG=HARVESTER_KUBECONFIG make install
KUBECONFIG=HARVESTER_KUBECONFIG make deploy IMG=REGISTRY/NAME:TAG

kubectl apply -f config/samples/dbaas_v1alpha1_dbinstance.yaml
kubectl get dbi -A -w
```

## What each target does

| Target | Behaviour (from the Makefile) |
| --- | --- |
| `make install` | Regenerates manifests, builds `config/crd` and applies it. Installs the CRD only. |
| `make deploy` | Runs `kustomize edit set image controller=$(IMG)` in `config/manager`, then applies `config/default`. |
| `make undeploy` | Deletes everything in `config/default`. Pass `ignore-not-found=true` to tolerate missing objects. |
| `make uninstall` | Deletes the CRD (and therefore all `DBInstance` objects). |
| `make build-installer` | Writes a consolidated `dist/install.yaml` from `config/default` for the given `IMG`. |
| `make docker-build` / `docker-push` | Build and push `$(IMG)` using `CONTAINER_TOOL`. |
| `make docker-buildx` | Multi-platform build and push (default `linux/arm64,linux/amd64,linux/s390x,linux/ppc64le`). |

`IMG` defaults to `controller:latest`, which is not pullable. Always pass your own image.

:::caution
`make deploy` modifies `config/manager/kustomization.yaml` in place. Revert it before committing: `git checkout -- config/manager/kustomization.yaml`.
:::

## What gets installed

`config/default` sets `namespace: dbaas-system`, includes the `dbaas-system` Namespace, and builds on `config/base`, which uses `namePrefix: dbaas-` and includes the CRD, RBAC (including leader-election and metrics-auth roles, and the optional DBInstance admin/editor/viewer helper roles), the manager Deployment and the metrics Service. Webhook, cert-manager, Prometheus and network-policy components are present in `config/` but commented out of `config/base/kustomization.yaml`.

The manager runs with the args `--operator.leaderElection.enabled=true` and `--observability.metrics.bindAddress=:8443`, and `POD_NAMESPACE` injected from the Downward API (the operator refuses to start without it).

## Optional: operator config file overlay

`config/overlays/operator-config` adds a ConfigMap named `dbaas-operator-config` with a `config.json` key mounted at `/etc/dbaas/config.json`, which the operator loads automatically when present.

```sh
kubectl create namespace dbaas-system
kubectl apply -k config/overlays/operator-config
```

Edit `config/overlays/operator-config/operator_config.yaml` first. The overlay's patch sets the container `args` to an empty list, so flags then come from the file rather than the Deployment manifest. The overlay uses whatever image `config/manager/kustomization.yaml` points at. Field reference: [Operator configuration](/configuration/operator-config).

:::info Verified against
- `Makefile` (`install`, `uninstall`, `deploy`, `undeploy`, `build-installer`, `docker-*`)
- `config/default/kustomization.yaml`, `config/base/kustomization.yaml`, `config/manager/manager.yaml`
- `config/overlays/operator-config/*`
- `README.md`
- `internal/config/namespace.go`
:::
