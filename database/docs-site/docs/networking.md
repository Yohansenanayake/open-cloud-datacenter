---
title: Networking
sidebar_position: 10
---

# Networking

A DBaaS database is a KubeVirt virtual machine on Harvester. Its network model is deliberately simple: **one network interface, on a VLAN you choose, and everything uses it.**

## One interface: `data-net`

The VM has a single virtio NIC named `data-net`, attached in **bridge mode** to a Multus `NetworkAttachmentDefinition` (NAD) named by `spec.networkRef`. The same interface carries:

- tenant client traffic (PostgreSQL on `spec.port`, default 5432),
- first-boot egress (the VLAN must have outbound connectivity, because cloud-init needs it),
- the Prometheus scrape of the exporter on port 9187.

The IP of this interface is what the operator publishes as `status.endpoint.address`.

## What you provide, what the controller does

| You must provide | The controller does |
| --- | --- |
| An existing NAD, referenced as `namespace/name` in `spec.networkRef` (pattern `^name/name$`, for example `iaas-net/vm-subnet-001`) | Attaches the VM's `data-net` NIC to that NAD and records it in `status.resources.nadName` |
| A VLAN with a route for clients to reach the VM and outbound access for first boot | Reads the guest IP from the VMI status and publishes it |
| A DHCP server on the VLAN | Writes cloud-init network config so the VM gets its address by DHCP |

The controller **does not create** NADs, VLANs, subnets, DHCP, firewalls or load balancers. If `networkRef` is empty the instance fails preflight with reason `NetworkRefMissing`.

`spec.networkRef` and `spec.port` are immutable after creation.

## IP addressing

The VM gets its address by DHCP, so the VLAN needs a reachable DHCP server. cloud-init configures `enp1s0` with `dhcp4: true`.

```yaml
spec:
  networkRef: iaas-net/vm-subnet-001
```

## The published endpoint

`status.endpoint` is updated on every healthy reconcile, so it follows the VM when its IP changes (restart, live migration).

```sh
kubectl get dbinstance orders-db -n tenant-acme -o jsonpath='{.status.endpoint}{"\n"}'
# {"address":"192.168.40.50","jdbcUrl":"jdbc:postgresql://192.168.40.50:5432/orders-db?ssl=true&sslmode=verify-ca","port":5432}
```

`kubectl get dbinstance` also shows the address in the `ENDPOINT` column. The same host and port are copied into the connection Secret (see [Connecting](/connecting)).

Clients must be on, or routed to, the VLAN behind the NAD. The address is **not** reachable through a Kubernetes Service or Ingress; there is no load balancer, proxy or pooler in the data path.

## The REST gateway

The operator binary can also run a small HTTP gateway over the `DBInstance` API (`server.gateway.enabled`, default `true`, `server.gateway.bindAddress` default `:8080`, `server.gateway.defaultNamespace` default `default`). It is a management API, **not** a database proxy: it never carries PostgreSQL traffic.

| Method and path | Action |
| --- | --- |
| `GET /healthz` | Unauthenticated liveness probe |
| `GET /dbinstances` | List instances in the gateway namespace |
| `POST /dbinstances` | Create (returns 202; the namespace in the body is overridden by the gateway's namespace) |
| `GET`, `PATCH`, `DELETE /dbinstances/{name}` | Describe, modify a subset of spec fields, delete |
| `POST /dbinstances/{name}/start`, `/stop` | Set `spec.running` |

Each request needs `Authorization: Bearer <token>`. The gateway builds a Kubernetes client with the caller's own token, so authentication, RBAC and audit are enforced by the Kubernetes API server as the caller, never as the operator's ServiceAccount. The listener is plain HTTP (no TLS in the gateway itself); put it behind your own TLS-terminating ingress if you expose it.
