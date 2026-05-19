output "service_fqdn" {
  value       = "dc-api-webhook.${var.namespace}.svc"
  description = "In-cluster DNS name used by the apiserver to call the webhook."
}

output "image_deployed" {
  value       = var.webhook_image
  description = "Container image reference that was deployed (for downstream audit/reference)."
}

output "webhook_configuration_name" {
  value       = kubernetes_manifest.mutating_webhook.manifest.metadata.name
  description = "Name of the MutatingWebhookConfiguration created on the cluster."
}
