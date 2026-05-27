output "vm_name" {
  value       = harvester_virtualmachine.vyos.name
  description = "VyOS VM name. Open the Harvester console for this VM to install VyOS from ISO on first deploy."
}

output "vm_namespace" {
  value       = harvester_virtualmachine.vyos.namespace
  description = "Harvester namespace the VyOS VM is deployed in."
}

output "trunk_network_name" {
  value       = harvester_network.eth1_trunk.name
  description = "Name of the eth1 trunk harvester_network resource."
}

output "trunk_network_namespace" {
  value       = harvester_network.eth1_trunk.namespace
  description = "Namespace of the eth1 trunk harvester_network resource."
}

output "trunk_network_ref" {
  value       = "${harvester_network.eth1_trunk.namespace}/${harvester_network.eth1_trunk.name}"
  description = "Full network ref (namespace/name) for the eth1 trunk network."
}

output "image_id" {
  value       = harvester_image.vyos.id
  description = "Harvester image ID for the VyOS ISO."
}
