module "tenant_space" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/tenant-space?ref=terraform/v0.1.2"

  providers = {
    kubernetes.harvester = kubernetes.harvester
    harvester            = harvester
  }

  cluster_id   = var.harvester_cluster_id
  project_name = var.project_name

  cpu_limit     = var.cpu_limit
  memory_limit  = var.memory_limit
  storage_limit = var.storage_limit

  group_role_bindings = var.group_role_bindings
  vm_network_vlan_id  = var.vm_network_vlan_id
}
