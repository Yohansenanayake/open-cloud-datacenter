# ── Project + VM namespace ────────────────────────────────────────────────────
# Assigns the VM namespace to a Rancher project, which causes Rancher to
# annotate the namespace with field.cattle.io/projectId. The
# namespace-credential-provisioner watches for that annotation (on namespaces
# without the network-namespace label) and creates two things:
#   1. harvester-cloud-provider-<ns> ServiceAccount + token in the namespace
#   2. harvesterconfig-<cluster> secret in fleet-default, referenced by the
#      cluster's cloud-provider-config
module "project" {
  source = "../../tenancy/tenant-space"

  providers = {
    kubernetes.harvester = kubernetes.harvester
  }

  cluster_id               = var.harvester_cluster_id
  project_name             = var.project_name
  create_default_namespace = true
  group_role_bindings      = []
}

# ── Management network NAD ────────────────────────────────────────────────────
# Untagged bridge on the mgmt cluster network. Cluster VMs live on the same
# L2 segment as the LoadBalancer IPPool so kube-vip can ARP-announce VIPs.
resource "harvester_network" "mgmt" {
  name                 = "${var.project_name}-mgmt"
  namespace            = var.project_name
  vlan_id              = 0
  cluster_network_name = var.mgmt_cluster_network
  route_mode           = "auto"

  lifecycle {
    ignore_changes = [labels]
  }

  depends_on = [module.project]
}

# ── LoadBalancer IPPool ───────────────────────────────────────────────────────
resource "harvester_ippool" "lb" {
  name = "${var.cluster_name}-lb"

  range {
    start   = var.lb_range_start
    end     = var.lb_range_end
    subnet  = var.lb_subnet
    gateway = var.lb_gateway
  }
}

# ── RKE2 cluster ─────────────────────────────────────────────────────────────
# cloud_provider_config_secret references the secret that the
# namespace-credential-provisioner creates automatically once the VM namespace
# is assigned to a project (via module.project above).
#
# The default machine_global_config tolerates the multi-hundred-ms etcd fsync
# latencies seen on Harvester/Longhorn-backed VM disks. Two adjustments:
#
#   * kube-apiserver: extend `etcd-healthcheck-timeout` from 2s → 10s so the
#     readyz etcd probe absorbs WAL fsync spikes instead of marking the
#     apiserver NotReady and oscillating Rancher between bootstrapping/active.
#
#   * kube-controller-manager / kube-scheduler: extend leader-election
#     lease/renew/retry from the 15s/10s/2s defaults to 60s/45s/10s so that
#     leader-renewal API calls (which write to etcd) tolerate slow fsyncs
#     instead of losing the lease and crashlooping every ~80 seconds.
#
# Note: the apiserver flag is `etcd-healthcheck-timeout` (one word).
# `etcd-health-check-timeout` is a typo and the binary rejects it on startup.
locals {
  machine_global_config = var.machine_global_config != null ? var.machine_global_config : <<-YAML
    cni: cilium
    disable-kube-proxy: false
    etcd-expose-metrics: false
    kube-apiserver-arg:
      - etcd-healthcheck-timeout=10s
    kube-controller-manager-arg:
      - leader-elect-lease-duration=60s
      - leader-elect-renew-deadline=45s
      - leader-elect-retry-period=10s
    kube-scheduler-arg:
      - leader-elect-lease-duration=60s
      - leader-elect-renew-deadline=45s
      - leader-elect-retry-period=10s
  YAML
}

module "cluster" {
  source = "../../tenancy/k8s-cluster"

  cluster_name        = var.cluster_name
  kubernetes_version  = var.kubernetes_version
  cloud_credential_id = var.cloud_credential_id

  cloud_provider_config_secret    = "harvesterconfig-${var.cluster_name}"
  enable_harvester_cloud_provider = true

  machine_global_config = local.machine_global_config

  machine_pools = [
    for pool in var.machine_pools : merge(pool, { vm_namespace = var.project_name })
  ]

  user_data         = var.user_data
  manage_rke_config = var.manage_rke_config

  depends_on = [
    harvester_network.mgmt,
    harvester_ippool.lb,
  ]
}

# ── IPPool scope patch (workaround) ──────────────────────────────────────────
# The IPPool is created without selector.scope. Without it, Harvester's LB
# controller times out with "no matched IPPool with requirement
# {Project:..., Namespace:<ns>, Cluster:kubernetes}".
#
# Format note: rancher2 outputs project_id as "cluster:project" (colon) but
# Harvester's LB matcher expects "cluster/project" (slash). Without the
# conversion below the patch is silently dropped by admission and only the
# namespace field survives, leaving the LB unable to match the pool.
#
# TODO: replace with a declarative harvester_ippool attribute once the
# Harvester provider exposes selector.scope.
locals {
  dcapi_project_id_slash = replace(module.project.project_id, ":", "/")
}

resource "null_resource" "ippool_scope_patch" {
  triggers = {
    cluster_id = module.cluster.cluster_id
    project_id = local.dcapi_project_id_slash
  }

  provisioner "local-exec" {
    command = <<-EOT
      kubectl --kubeconfig "${var.harvester_kubeconfig_path}" \
        patch ippool.loadbalancer.harvesterhci.io "${var.cluster_name}-lb" \
        --type=merge \
        -p '{"spec":{"selector":{"scope":[{"namespace":"${var.project_name}","project":"${local.dcapi_project_id_slash}"}]}}}'
    EOT
  }

  depends_on = [
    module.cluster,
    harvester_ippool.lb,
  ]
}
