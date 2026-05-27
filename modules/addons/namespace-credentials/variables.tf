variable "namespace" {
  type        = string
  description = "Namespace to deploy the provisioner into."
  default     = "kube-system"
}

variable "harvester_api_server" {
  type        = string
  description = "Harvester Kubernetes API server URL (e.g. https://192.168.1.10:6443). Written into every harvesterconfig secret."
  sensitive   = true
}

variable "rancher_kubeconfig" {
  type        = string
  description = "Kubeconfig for the Rancher cluster. The reconciler uses this to write harvesterconfig secrets into Rancher's fleet-default namespace."
  sensitive   = true
}

variable "image" {
  type        = string
  description = "Container image for the provisioner. Must have kubectl, bash, and jq available."
  default     = "alpine/k8s:1.32.3"
}
