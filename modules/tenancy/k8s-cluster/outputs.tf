output "cluster_id" {
  value       = rancher2_cluster_v2.this.id
  description = "Rancher v2 cluster ID (fleet-default/<name>)"
}

output "cluster_name" {
  value       = rancher2_cluster_v2.this.name
  description = "Name of the provisioned downstream cluster"
}

output "cluster_v3_id" {
  value       = rancher2_cluster_v2.this.cluster_v1_id
  description = "Legacy v3 cluster ID (c-m-xxxx) for use in role bindings"
}
