---
title: Network policy
sidebar_position: 3
---

# Network policy

DBaaS ships one `NetworkPolicy`, and it protects the **operator's own** metrics endpoint. It does not restrict traffic to the database VMs.

## The shipped policy

`config/network-policy/allow-metrics-traffic.yaml` (name `allow-metrics-traffic`):

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-metrics-traffic
spec:
  podSelector:
    matchLabels:
      control-plane: controller-manager
      app.kubernetes.io/name: dbaas
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              metrics: enabled
      ports:
        - port: 8443
          protocol: TCP
```

Only pods in namespaces labelled `metrics: enabled` can scrape port 8443 of the controller manager pods. To allow a namespace (for example the one running Prometheus):

```sh
kubectl label namespace cattle-monitoring-system metrics=enabled
```

:::caution
This policy is a kustomize component that is **commented out** in `config/base/kustomization.yaml` (`#- ../network-policy`), and the Helm chart does not contain a NetworkPolicy template. It is not applied unless you enable it in kustomize or apply the file yourself.
:::

## Database VMs are not covered

The database runs in a KubeVirt VM attached to a VLAN through a Multus bridge interface, not in a pod network, so Kubernetes NetworkPolicy does not govern client traffic to it. Isolation is provided by:

- the VLAN you choose with `spec.networkRef` (only machines on that network can reach the VM), and
- PostgreSQL itself: TLS-only, SCRAM-SHA-256, see [TLS and access](/security/tls-and-access).

The controller does not create firewall rules or security groups.

:::info Verified against
- `database/config/network-policy/allow-metrics-traffic.yaml`
- `database/config/network-policy/kustomization.yaml`
- `database/config/base/kustomization.yaml`
- `database/charts/chart/templates/` (no NetworkPolicy template)
- `database/internal/harvester/typed_client.go`
:::
