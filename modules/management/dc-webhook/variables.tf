variable "webhook_image" {
  type        = string
  description = "Full container image reference for the dc-api-webhook (e.g. <registry>/<owner>/dc-api-webhook:<sha>)."
}

variable "harvester_kubeconfig_b64" {
  type        = string
  sensitive   = true
  description = "Base64-encoded Harvester kubeconfig. The webhook uses this to resolve NetworkAttachmentDefinitions at admission time."
}

variable "namespace" {
  type        = string
  description = "Kubernetes namespace where the webhook runs."
  default     = "dc-system"
}

variable "replicas" {
  type        = number
  description = "Number of webhook pod replicas."
  default     = 1
}

variable "log_level" {
  type        = string
  description = "Webhook log level (debug | info | warn | error)."
  default     = "info"
  validation {
    condition     = contains(["debug", "info", "warn", "error"], var.log_level)
    error_message = "log_level must be one of: debug, info, warn, error."
  }
}

variable "webhook_domain" {
  type        = string
  description = "Domain suffix used in the MutatingWebhookConfiguration webhook names (convention: <handler>.<resource>.<group>.<domain>). Operators typically set this to their organization's DNS domain so webhook names don't collide with other operators in shared clusters."
  default     = "example.com"
}

variable "ghcr_username" {
  type        = string
  description = "GitHub username (or org) that owns the webhook image on ghcr.io. Used as the imagePullSecrets auth subject."
}

variable "ghcr_pat" {
  type        = string
  sensitive   = true
  description = "GitHub Personal Access Token with read:packages scope for pulling the webhook image from ghcr.io."
}
