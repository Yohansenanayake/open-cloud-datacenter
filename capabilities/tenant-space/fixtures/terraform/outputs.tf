output "project_id" {
  description = "Rancher ID of the temporary tenant project."
  value       = module.tenant_space.project_id
}

output "project_name" {
  description = "Name of the temporary tenant project."
  value       = module.tenant_space.project_name
}

output "namespace_ids" {
  description = "Rancher namespace IDs created for the tenant."
  value       = module.tenant_space.namespace_ids
}

output "network_names" {
  description = "Harvester VM networks created for the tenant."
  value = {
    tostring(var.vm_network_vlan_id) = "${module.tenant_space.network_namespace}/${var.project_name}-vlan${var.vm_network_vlan_id}"
  }
}
