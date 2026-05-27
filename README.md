# Open Cloud Data Center — Terraform Modules

> **Branch:** `terraform`
> This branch holds the Terraform module catalog for the Open Cloud Data Center (OCDC) stack. Operator source code lives on the [`operators`](../../tree/operators) branch and the control-plane services live on the [`controlplane`](../../tree/controlplane) branch. The legacy unified layout remains available on [`main`](../../tree/main) for existing consumers.

The Open Cloud Data Center initiative provides a standardized, modular foundation for building a self-hosted cloud-style datacenter on top of [Harvester HCI](https://harvesterhci.io/) and [Rancher](https://rancher.com/). The Terraform modules in this branch cover everything from bootstrapping a Rancher control plane to onboarding tenants, provisioning Kubernetes clusters and VMs, wiring in identity and observability, and — optionally — layering the DC-API self-service experience on top.

**Why OCDC**
- **Sovereignty** — full control over data, network, and identity. No external SaaS dependencies are required at runtime.
- **Portability** — runs on commodity hardware; the same modules apply to lab, edge, and core sites.
- **Composable** — each module owns a single concern and can be adopted incrementally.
- **Open** — Apache 2.0, community-driven.

---

## Quick start

Pin a module by its branch-scoped release tag (see [Releases & versioning](#releases--versioning) below):

```hcl
module "rancher" {
  source = "github.com/wso2/open-cloud-datacenter//modules/platform/rancher?ref=terraform/v0.1.0"

  vm_password            = var.vm_password
  rancher_hostname       = "rancher.example.internal"
  rancher_admin_password = var.rancher_admin_password
  ippool_subnet          = "192.168.10.0/24"
  ippool_gateway         = "192.168.10.1"
  ippool_start           = "192.168.10.10"
  ippool_end             = "192.168.10.20"
}
```

For the full picture of how modules fit together and which to apply when, see [`docs/architecture.md`](docs/architecture.md).

---

## Mental model

The catalog is split into five purpose-driven groups. The grouping reflects *when you need a module*, not *who built it*:

| Group | What it gives you | Required? |
|---|---|---|
| **`platform/`** | A working Rancher + Harvester platform with identity and observability. | Always |
| **`tenancy/`** | Day-2 tenant operations — projects, quotas, VMs, clusters, VLAN routing. | When you want to onboard users |
| **`operators/`** | Managed-service operators that extend the platform with new capabilities. | Optional, additive |
| **`cloud/`** | The DC-API self-service layer that turns the stack into a sovereign cloud. | Optional |
| **`addons/`** | Glue modules that paper over gaps in vanilla Rancher+Harvester. May be deprecated as those gaps close. | Only if you hit the scenario |

You can stop at any layer and still have a useful system. `platform` alone gives you a managed virtualization stack. Add `tenancy` and you have a multi-tenant private cloud operable via Rancher. Add `cloud` and your users get a self-service API.

---

## Repository layout

```text
modules/
├── platform/                            # Foundation — run once
│   ├── rancher/                         # bootstrap RKE2 + Rancher on Harvester
│   ├── harvester-integration/           # register Harvester into Rancher
│   ├── networking/                      # VLAN-backed Harvester networks
│   ├── storage/                         # Harvester OS-image catalog
│   ├── monitoring/                      # Prometheus + Alertmanager + Calert
│   └── identity/
│       ├── rancher-oidc/                # generic OIDC broker for Rancher
│       └── providers/
│           ├── asgardeo/                # WSO2 Asgardeo preset
│           └── azure-ad/                # Azure AD preset (BYO app)
│
├── tenancy/                             # Day-2 tenant operations
│   ├── tenant-space/                    # full onboarding bundle
│   ├── rbac/                            # Rancher projects + quotas
│   ├── cluster-roles/                   # custom Rancher role templates
│   ├── vm/                              # provision a Harvester VM
│   └── k8s-cluster/                     # provision a tenant RKE2 cluster
│
├── operators/                           # Managed-service operators
│   └── dc-webhook/                      # KubeOVN admission webhook
│   # (keyvault/, db/ — coming)
│
├── cloud/                               # DC-API self-service layer (optional)
│   ├── dc-controlplane/                 # HA RKE2 cluster hosting DC-API
│   └── dc-services/                     # DC-API + Postgres + UI + runner
│
└── addons/                              # Niche glue; may be deprecated
    ├── namespace-credentials/           # auto-issue scoped SA + kubeconfig
    ├── harvester-cloud-credential/      # cloud-cred Secret bridge
    └── harvester-vm-access/             # scoped tenant kubeconfig
```

---

## Module catalog

### Platform — foundation

| Module | Purpose |
|---|---|
| [`modules/platform/rancher`](modules/platform/rancher/README.md) | Bring up an RKE2 + Rancher VM on Harvester via cloud-init, fronted by a Harvester LoadBalancer. |
| [`modules/platform/harvester-integration`](modules/platform/harvester-integration/README.md) | Register the Harvester cluster into Rancher, enable the UI extension, create the cloud credential. |
| [`modules/platform/networking`](modules/platform/networking/README.md) | Create and manage VLAN-backed Harvester networks. |
| [`modules/platform/storage`](modules/platform/storage/README.md) | Download and register OS images into Harvester for VM provisioning. |
| [`modules/platform/monitoring`](modules/platform/monitoring/README.md) | Layer Calert + Google Chat alert routing and curated dashboards on top of `rancher-monitoring`. |
| [`modules/platform/identity/rancher-oidc`](modules/platform/identity/rancher-oidc) | Configure Rancher to delegate authentication to any generic OIDC provider. |
| [`modules/platform/identity/providers/asgardeo`](modules/platform/identity/providers/asgardeo) | Pre-create the OIDC application in WSO2 Asgardeo and emit OIDC endpoints for `rancher-oidc`. |
| [`modules/platform/identity/providers/azure-ad`](modules/platform/identity/providers/azure-ad) | Bring-your-own-app preset for Azure AD; computes OIDC endpoints from a tenant ID. |

### Tenancy — day-2 operations

| Module | Purpose |
|---|---|
| [`modules/tenancy/tenant-space`](modules/tenancy/tenant-space/README.md) | Full tenant onboarding: Rancher project, namespaces, quotas, RBAC, and optional VLAN attachment. |
| [`modules/tenancy/rbac`](modules/tenancy/rbac/README.md) | Bulk-create Rancher projects + namespaces with CPU / memory / storage quotas. |
| [`modules/tenancy/cluster-roles`](modules/tenancy/cluster-roles/README.md) | Define shared custom Rancher role templates (e.g. `vm-manager`, `vm-metrics-observer`). |
| [`modules/tenancy/vm`](modules/tenancy/vm/README.md) | Provision standalone Harvester VMs with multi-disk, cloud-init, and custom networks. |
| [`modules/tenancy/k8s-cluster`](modules/tenancy/k8s-cluster/README.md) | Provision a tenant RKE2 cluster via Rancher machine provisioning with multi-pool support. |

### Operators — managed-service extensions

| Module | Purpose |
|---|---|
| [`modules/operators/dc-webhook`](modules/operators/dc-webhook/README.md) | Mutating admission webhook that injects MAC pinning / KubeOVN annotations on Harvester VirtualMachines. |
| _coming: `modules/operators/keyvault`_ | OpenBao-backed Key Vault operator deployment. Tracked in the `feature/keyvault-operator-module` branch; will land in `terraform/v0.2.0`. |
| _coming: `modules/operators/db`_ | Managed PostgreSQL operator deployment. Planned. |

### Cloud — DC-API self-service layer

| Module | Purpose |
|---|---|
| [`modules/cloud/dc-controlplane`](modules/cloud/dc-controlplane) | Provision the 3-node HA RKE2 cluster that hosts the DC-API and tooling. |
| [`modules/cloud/dc-services`](modules/cloud/dc-services) | Deploy the DC-API stack (PostgreSQL, API, optional Cloud UI, GitHub Actions runner) onto the control-plane cluster. |

### Addons — niche glue

| Module | Purpose |
|---|---|
| [`modules/addons/namespace-credentials`](modules/addons/namespace-credentials/README.md) | Long-running reconciler that auto-creates scoped ServiceAccounts and kubeconfig Secrets per tenant namespace. |
| [`modules/addons/harvester-cloud-credential`](modules/addons/harvester-cloud-credential/README.md) | Materialize a Harvester kubeconfig Secret so a downstream cluster can act as a Rancher cloud credential. |
| [`modules/addons/harvester-vm-access`](modules/addons/harvester-vm-access/README.md) | Issue a namespace-scoped ServiceAccount + kubeconfig for delegated tenant access to Harvester VMs. |

> Modules whose name links above are not file-link targets are on the docs backlog — see [Contributing](#contributing).

---

## Deployment phases

Apply the modules in this order. Each phase consumes outputs from earlier phases.

| Phase | Modules | Outcome |
|---|---|---|
| **0 — Bootstrap** | `platform/rancher` | Rancher + RKE2 running inside Harvester, reachable via LoadBalancer. |
| **1 — Rancher auth** | _(provider config only)_ | `rancher2` provider authenticated against the bootstrapped Rancher. |
| **2 — Platform** | `platform/harvester-integration`, `platform/networking`, `platform/storage`, `platform/monitoring`, `platform/identity/*` | Harvester registered, shared infra ready, OIDC and observability wired up. |
| **3 — Tenancy** | `tenancy/*` | Tenant projects, RBAC, VMs, and clusters provisioned on demand. |
| **4 — Operators** _(optional)_ | `operators/*` | Capability extensions installed (e.g. admission webhook, KV operator). |
| **5 — Cloud** _(optional)_ | `cloud/dc-controlplane`, `cloud/dc-services` | DC-API self-service layer live on a dedicated HA cluster. |
| **6 — Addons** _(as needed)_ | `addons/*` | Glue applied to specific scenarios. |

The per-module breakdown — including outputs consumed downstream, provider versions, and the dependency graph — lives in [`docs/architecture.md`](docs/architecture.md).

---

## Releases & versioning

Each branch in this repository ships independently. Tags are namespaced by the branch they belong to so you can tell at a glance which artifact a release belongs to:

```text
terraform/v0.1.0       ← Terraform module catalog (this branch)
operators/v0.1.0       ← Operator source code (operators branch)
controlplane/v0.1.0    ← Control-plane services (controlplane branch)
```

The `terraform/` portion is a branch namespace; the `vX.Y.Z` portion follows [Semantic Versioning](https://semver.org/) strictly. The `0.x` line signals "pre-stable — API may change at any time" per SemVer §4.

**Bump rules (this branch)**

| Bump | Trigger |
|---|---|
| **MAJOR** | Breaking variable or output changes; removed modules; provider major bumps requiring consumer migration. |
| **MINOR** | New modules; new variables or outputs with safe defaults; additive features. |
| **PATCH** | Bug fixes; documentation-only changes; internal refactors with no surface change. |

**Pre-release suffixes** (`-rc.N`, `-beta.N`, `-alpha.N`) are reserved for candidate builds of a named upcoming stable version — e.g. `terraform/v1.0.0-rc.1` while preparing the `v1.0.0` cut.

**Consuming a release**

```hcl
module "vm" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/vm?ref=terraform/v0.1.0"
  # ...
}
```

The `?ref=` token accepts any valid git ref — a tag (recommended for production), a branch (`terraform` for the latest tip), or a commit SHA (for forensic pinning).

> **Migrating from `main`** — the legacy unified module layout still works at `?ref=v0.4.5` on `main`. New consumers should pin to `terraform/vX.Y.Z` tags going forward; `main` will not receive further module changes.

---

## CI & quality gates

Pull requests against this branch run two checks:

| Workflow | Trigger | Purpose |
|---|---|---|
| [`linter.yml`](.github/workflows/linter.yml) | PR + on approved review | Super-Linter pass over Terraform, YAML, Markdown, Shell. |
| [`terraform-scan.yml`](.github/workflows/terraform-scan.yml) | PR touching `*.tf` / `*.tfvars` / `*.hcl` | Trivy IaC scan; findings posted as PR comments. |

Local equivalents:

```bash
terraform fmt -recursive
terraform validate         # run inside each module directory
```

`.checkov.yaml` configures the secondary Checkov policy set for module authors who want to run it locally.

---

## Contributing

1. **File an issue first.** Every change starts as an issue or epic — see [`issue_template.md`](issue_template.md).
2. **Branch.** Use `feature/<issue-id>-<short-desc>` or `fix/<issue-id>-<short-desc>` off `terraform`.
3. **Open a PR against `terraform`.** Use Conventional Commits for the title, link the issue with `Closes #ID`, and fill in the [PR template](pull_request_template.md).
4. **Wait for review.** CodeRabbit comments first; address its feedback before human review.
5. **Tag a release.** Once merged, maintainers cut a `terraform/vX.Y.Z` tag and publish a GitHub release with the changelog.

Full guidelines: [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md). Community standards: [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

---

## Reporting issues

- **Bugs / feature requests** — [GitHub Issues](https://github.com/wso2/open-cloud-datacenter/issues)
- **Security disclosures** — see [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for the responsible-disclosure address.

---

## License

Apache License 2.0 — see [`LICENSE`](LICENSE).
