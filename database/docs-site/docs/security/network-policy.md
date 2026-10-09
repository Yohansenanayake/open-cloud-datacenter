---
title: Network policy
sidebar_position: 3
---

# Network policy

DBaaS ships one `NetworkPolicy`. It protects the **operator's own metrics endpoint** (port 8443). It does not restrict traffic to database VMs.

:::caution
The Helm chart has no NetworkPolicy template. This policy is **not applied** unless you apply it yourself from `config/network-policy/allow-metrics-traffic.yaml`.
:::

## What the policy does

Only pods in namespaces labelled `metrics: enabled` can reach port 8443 of the operator pods.

To allow a namespace, for example the one running Prometheus:

```sh
kubectl label namespace cattle-monitoring-system metrics=enabled
```

## Database VMs are not covered

The database runs in a KubeVirt VM on a VLAN, not in the pod network, so Kubernetes NetworkPolicy doesn't apply to it. Access is limited by:

- **The VLAN you choose** with `spec.networkRef`. Only machines on that network can reach the VM.
- **PostgreSQL itself:** TLS only, with SCRAM-SHA-256. See [TLS and access](/security/tls-and-access).

The operator creates no firewall rules or security groups. Put the database on a VLAN that only your applications can reach.
