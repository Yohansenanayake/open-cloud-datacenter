# Architecture: Open Cloud Data Center on Harvester HCI

## Overview

The Open Cloud Data Center (OCDC) Terraform framework deploys and manages a full cloud-datacenter stack on top of [Harvester HCI](https://harvesterhci.io/). Harvester provides the hypervisor layer (built on KubeVirt). Rancher provides the Kubernetes management plane. Tenant workload clusters are provisioned as RKE2 clusters running as virtual machines inside Harvester. The optional DC-API layer sits on top of Rancher and exposes a self-service API for end-users.

```text
┌─────────────────────────────────────────────────────────────────┐
│                        Physical Nodes                           │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                   Harvester HCI (KubeVirt)                │  │
│  │                                                           │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────┐    │  │
│  │  │   Rancher   │  │   DC-API    │  │ Tenant Clusters │    │  │
│  │  │   (RKE2)    │  │  (3-node    │  │ (RKE2 VMs)      │    │  │
│  │  │             │  │   HA RKE2)  │  │                 │    │  │
│  │  │ cert-mgr    │  │             │  │ Cluster A ...   │    │  │
│  │  │ Rancher UI  │  │ dc-api      │  │ Cluster B ...   │    │  │
│  │  │             │  │ postgres    │  │                 │    │  │
│  │  └──────┬──────┘  │ cloud-ui    │  └─────────────────┘    │  │
│  │         │         └──────┬──────┘                         │  │
│  │         │ manages         │ provisions via                │  │
│  │         └───────────────► │ Rancher API                   │  │
│  └─────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

---

## Module groups

The modules are organized into five purpose-driven groups. Each group has a clear "when do I need this?" answer:

| Group | Required? | When to apply |
|---|---|---|
| `platform/` | Always | First. Brings up Rancher, Harvester integration, identity, observability. |
| `tenancy/` | When onboarding users | After platform. Provides the tenant-facing toolkit. |
| `operators/` | Optional | After platform. Adds extra capabilities (admission, secrets, …). |
| `cloud/` | Optional | After tenancy. Layers the DC-API self-service experience on top. |
| `addons/` | Only if needed | At any point. Glue for specific gaps in vanilla Rancher+Harvester. |

---

## Deployment phases

Each phase consumes outputs from earlier phases. The phases map cleanly to Terraform workspaces or directories in your consumer repository.

### Phase 0 — Bootstrap (`platform/rancher`)

**Purpose:** stand up the Rancher management server inside Harvester.

**What it does:**
- Generates an RSA SSH key pair and registers it as a Harvester SSH key.
- Creates a `harvester_cloudinit_secret` embedding the bootstrap script.
- The cloud-init script installs RKE2, waits for the cluster, then installs cert-manager and Rancher via Helm — all inside the VM, with no external Terraform provider access to the cluster required.
- Creates the Rancher `harvester_virtualmachine` on the masquerade network.
- Creates a Harvester IP pool and LoadBalancer exposing ports 80/443 of the Rancher VM.

**Outputs used downstream:**
- `rancher_hostname` — the FQDN to map in DNS.
- `rancher_lb_ip` — the LoadBalancer IP.

**Providers:** `harvester/harvester ~> 1.7`, `hashicorp/tls`, `hashicorp/kubernetes ~> 2.30`, `hashicorp/null`

---

### Phase 1 — Rancher auth (no module)

**Purpose:** authenticate Terraform sessions against the newly bootstrapped Rancher.

Configure the `rancher2` provider at the consumer layer:

```hcl
provider "rancher2" {
  api_url   = "https://rancher.example.internal"
  bootstrap = true
  # ...
}
```

---

### Phase 2 — Platform

Apply these together; they're independent of each other but all depend on Phase 1.

#### 2a. harvester-integration (`platform/harvester-integration`)

- Enables the Harvester feature flag in Rancher.
- Installs the Harvester UI extension via the official Helm chart.
- Creates a `rancher2_cloud_credential` storing the Harvester kubeconfig — used by tenant cluster provisioning later.
- Creates a `rancher2_cluster` importing Harvester as a virtualization management cluster.
- Patches Harvester's CoreDNS ConfigMap so Harvester nodes can resolve the internal Rancher hostname before registration runs.
- Applies the Rancher registration manifest to Harvester via `local-exec` + `kubectl`.

**Providers:** `rancher/rancher2 ~> 13.1`, `harvester/harvester ~> 1.7`, `hashicorp/kubernetes ~> 2.30`

#### 2b. networking (`platform/networking`)

- Creates `harvester_network` resources for each VLAN defined in the `vlans` input map.
- Networks are referenced by name when provisioning tenant clusters and VMs.

**Providers:** `harvester/harvester ~> 0.6.0`

#### 2c. storage (`platform/storage`)

- Downloads OS images from public URLs into Harvester via `harvester_image`.
- Images are referenced by name from `tenancy/vm` and `tenancy/k8s-cluster`.

**Providers:** `harvester/harvester ~> 0.6.0`

#### 2d. monitoring (`platform/monitoring`)

- Deploys `calert` and the `google-chat-notifications` integration on top of the `rancher-monitoring` stack.
- Configures PrometheusRules and Alertmanager routes to send critical alerts to Google Chat Spaces.
- Installs curated Grafana dashboards for Harvester nodes, storage, and VMs.

**Providers:** `hashicorp/kubernetes`, `hashicorp/null`

#### 2e. identity (`platform/identity/*`)

- **rancher-oidc** — configures Rancher to delegate authentication to any external OIDC provider.
- **providers/asgardeo** — pre-creates the OIDC app in WSO2 Asgardeo and outputs the OIDC endpoints needed by `rancher-oidc`.
- **providers/azure-ad** — bring-your-own-app preset for Azure AD; computes OIDC endpoints from a tenant ID. App registration is manual today.

**Providers:** `rancher/rancher2`, `hiranadikari/asgardeo` (Asgardeo preset)

---

### Phase 3 — Tenancy

The tenancy modules expose a vanilla Rancher+Harvester multi-tenant cloud. They work whether or not the DC-API layer is installed.

#### 3a. rbac (`tenancy/rbac`)

- Bulk-creates `rancher2_project` resources with CPU / memory / storage quotas.
- Creates a default namespace per project.

#### 3b. cluster-roles (`tenancy/cluster-roles`)

- Defines custom Rancher role templates (e.g. `vm-manager`, `vm-metrics-observer`) consumed by `tenant-space`.

#### 3c. tenant-space (`tenancy/tenant-space`)

- Single entry point for onboarding a team: Rancher project, namespaces, quotas, role bindings, and optional VLAN attachment.
- Internally composes `tenancy/cluster-roles` and an experimental L3 gateway integration.

#### 3d. vm (`tenancy/vm`)

- Provisions standalone Harvester VMs with cloud-init, multi-disk support, and custom network interfaces.
- Manages SSH keys and cloud-init secrets automatically.

#### 3e. k8s-cluster (`tenancy/k8s-cluster`)

- Fetches the Harvester cloud credential via `data.rancher2_cloud_credential`.
- Defines `rancher2_machine_config_v2` per node pool (CPU, memory, disk, image, network).
- Provisions an RKE2 `rancher2_cluster_v2` with one or more machine pools combining control-plane, etcd, and worker roles as needed.
- Supports custom container registries, OS image overrides, and SSH key injection.

**Providers:** `rancher/rancher2 ~> 13.1`, `harvester/harvester ~> 0.6.0`, `hashicorp/kubernetes ~> 2.30`

---

### Phase 4 — Operators (optional)

Reconcilers that extend the platform with new capabilities. Apply only the ones you need.

#### 4a. dc-webhook (`operators/dc-webhook`)

- Mutating admission webhook for Harvester VirtualMachines.
- Injects MAC pinning and KubeOVN annotations needed for stable tenant networking.

**Providers:** `hashicorp/kubernetes`, `hashicorp/tls ~> 4.0`

> Coming: `operators/keyvault` (OpenBao-backed Key Vault operator, planned for `terraform/v0.2.0`) and `operators/db` (managed Postgres, planned).

---

### Phase 5 — Cloud (optional)

The DC-API self-service layer. Skip this entire phase if you only need Rancher-driven tenant management.

#### 5a. dc-controlplane (`cloud/dc-controlplane`)

- Provisions a 3-node HA RKE2 cluster (via Rancher machine provisioning) that hosts the DC-API stack.
- Sets up a Rancher project and namespace dedicated to DC-API workloads.
- Internally composes `tenancy/tenant-space` (for project setup) and `tenancy/k8s-cluster` (for cluster provisioning).

**Providers:** `rancher/rancher2 ~> 13.1`, `harvester/harvester ~> 1.7`, `hashicorp/kubernetes ~> 2.30`, `hashicorp/null`

#### 5b. dc-services (`cloud/dc-services`)

- Deploys the DC-API runtime onto the control-plane cluster: PostgreSQL (Bitnami chart), the DC-API service, optional Cloud UI, and a GitHub Actions runner for cluster-side workflows.

**Providers:** `hashicorp/kubernetes`, `hashicorp/helm`, `hashicorp/random`, `hashicorp/tls`, `hashicorp/local`, `hashicorp/null`

---

### Phase 6 — Addons (as needed)

Modules for specific gaps that the vanilla Rancher+Harvester experience doesn't close cleanly. Use only if you hit the scenario — and expect some of these to be deprecated as upstream gaps close.

| Module | When you need it |
|---|---|
| `addons/namespace-credentials` | You want tenant namespaces to auto-receive scoped SAs and kubeconfig Secrets without manual handover. |
| `addons/harvester-cloud-credential` | A downstream cluster needs to act as a Rancher cloud credential pointing back at Harvester. |
| `addons/harvester-vm-access` | You need a namespace-scoped ServiceAccount + kubeconfig to delegate Harvester VM access to a tenant. |

---

## Module dependency graph

```text
                    ┌────────────────────┐
                    │ platform/rancher   │  Phase 0
                    └─────────┬──────────┘
                              │ rancher_hostname, rancher_lb_ip
                              ▼
                    ┌────────────────────┐
                    │   rancher auth     │  Phase 1 (provider config)
                    └─────────┬──────────┘
        ┌─────────────────────┼─────────────────────┐
        ▼                     ▼                     ▼
┌────────────────┐  ┌────────────────┐  ┌──────────────────────┐
│ platform/      │  │ platform/      │  │ platform/storage     │
│ harvester-     │  │ networking     │  │ + platform/monitoring│
│ integration    │  │                │  │ + platform/identity  │
└───────┬────────┘  └────────────────┘  └──────────────────────┘
        │ cloud_credential_id, cluster registered
        ▼
┌────────────────────────────────────────────────────┐
│                  tenancy/* (Phase 3)               │
│        rbac, cluster-roles, tenant-space           │
│              vm, k8s-cluster                       │
└─────────────────┬──────────────────────────────────┘
                  │ tenant projects + workloads ready
       ┌──────────┼──────────────┐
       ▼                         ▼
┌────────────────┐    ┌─────────────────────────────────┐
│ operators/*    │    │ cloud/dc-controlplane (HA RKE2) │
│  (Phase 4)     │    │       │                         │
│                │    │       ▼                         │
└────────────────┘    │ cloud/dc-services (DC-API)      │
                      │  (Phase 5)                      │
                      └─────────────────────────────────┘

addons/* can be applied at any phase as needed.
```

---

## Network architecture

Harvester offers two networking modes for VMs:

- **Masquerade** — VMs receive NAT'd outbound internet access via the Harvester node. The Phase 0 Rancher bootstrap VM uses this mode; external access comes via a Harvester LoadBalancer with an IP pool from a routable subnet.
- **VLAN (bridge)** — VMs are bridged directly onto a physical VLAN, giving them routable IPs in the datacenter network fabric. Tenant cluster nodes and tenant VMs use this mode.

The `platform/networking` module creates VLAN-backed networks in Harvester. Tenant modules (`tenancy/k8s-cluster`, `tenancy/vm`) reference them by name. An experimental L3 gateway integration is available for configuring the routing/DHCP/NAT side of tenant VLANs; it is not promoted for general use yet.

---

## Security considerations

- Sensitive variables (`vm_password`, `rancher_admin_password`, `harvester_kubeconfig`, `db_password`, etc.) are marked `sensitive = true` throughout. Supply them via `*.secret.tfvars`, environment variables, or a secrets-manager integration — never commit them.
- The bootstrap Rancher uses a self-signed certificate by default (managed by cert-manager). In production, configure an ACME issuer or bring your own certificate.
- Rancher's RBAC (projects + namespaces + quotas) provides soft multi-tenancy. For stronger isolation, give each tenant a dedicated cluster via `tenancy/k8s-cluster`.
- The `operators/dc-webhook` admission webhook is on the critical path for VM creation once installed — failures block VM creation by design. Monitor it accordingly.

---

## Provider version summary

| Provider | Used by | Pinned version |
|---|---|---|
| `harvester/harvester` | `platform/rancher`, `platform/networking`, `platform/storage`, `platform/harvester-integration`, `tenancy/vm`, `tenancy/tenant-space`, `cloud/dc-controlplane` | `~> 0.6.0` to `~> 1.7` (per module) |
| `rancher/rancher2` | `platform/harvester-integration`, `platform/identity/rancher-oidc`, `tenancy/rbac`, `tenancy/tenant-space`, `tenancy/cluster-roles`, `tenancy/k8s-cluster`, `cloud/dc-controlplane` | `~> 13.1` |
| `hashicorp/kubernetes` | `platform/harvester-integration`, `platform/monitoring`, `operators/dc-webhook`, `cloud/dc-services`, `addons/*` | `~> 2.30` to `~> 2.35` (per module) |
| `hashicorp/helm` | `cloud/dc-services` | `~> 2.x` |
| `hashicorp/tls` | `platform/rancher`, `cloud/dc-services`, `operators/dc-webhook` | `~> 4.0` |
| `hashicorp/null` | `platform/rancher`, `platform/monitoring`, `cloud/dc-controlplane`, `cloud/dc-services` | `~> 3.0` |
| `hashicorp/random` | `cloud/dc-services` | (latest) |
| `hashicorp/local` | `cloud/dc-services` | (latest) |
| `hashicorp/http` | `tenancy/k8s-cluster` | `~> 3.6` |
| `hiranadikari/asgardeo` | `platform/identity/providers/asgardeo` | per module |

The exact pinned version per module lives in each module's `versions.tf`.
