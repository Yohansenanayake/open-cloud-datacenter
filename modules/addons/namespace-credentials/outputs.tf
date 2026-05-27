output "deployment_name" {
  value       = kubernetes_deployment_v1.provisioner.metadata[0].name
  description = "Name of the provisioner Deployment."
}

output "service_account_name" {
  value       = kubernetes_service_account_v1.provisioner.metadata[0].name
  description = "Name of the ServiceAccount used by the provisioner pod."
}
