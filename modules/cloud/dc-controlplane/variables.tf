variable "cluster_name" {
  type        = string
  description = "Name of the RKE2 cluster for the control plane (e.g. 'dcapi-controlplane-rke2')."
}

variable "project_name" {
  type        = string
  description = "Rancher project name. Also used as the VM namespace and NAD namespace."
  default     = "dc-api"
}

variable "harvester_cluster_id" {
  type        = string
  description = "Rancher cluster ID of the Harvester HCI cluster."
}

variable "cloud_credential_id" {
  type        = string
  description = "Rancher cloud credential ID for provisioning VMs on Harvester."
}

variable "kubernetes_version" {
  type        = string
  description = "RKE2 version string (e.g. 'v1.33.10+rke2r3')."
}

variable "mgmt_cluster_network" {
  type        = string
  description = "Harvester cluster network for the management NAD."
  default     = "mgmt"
}

variable "lb_range_start" {
  type        = string
  description = "First IP in the LoadBalancer VIP range for the control-plane cluster."
}

variable "lb_range_end" {
  type        = string
  description = "Last IP in the LoadBalancer VIP range."
}

variable "lb_subnet" {
  type        = string
  description = "Subnet CIDR containing the LoadBalancer VIP range."
}

variable "lb_gateway" {
  type        = string
  description = "Default gateway for the LoadBalancer VIP subnet."
}

variable "machine_pools" {
  type = list(object({
    name           = string
    quantity       = number
    cpu_count      = string
    memory_size    = string
    disk_size      = number
    image_name     = string
    networks       = list(string)
    control_plane  = bool
    etcd           = bool
    worker         = bool
    machine_labels = optional(map(string), {})
    taints = optional(list(object({
      key    = string
      value  = string
      effect = string
    })), [])
  }))
  description = "Machine pool definitions. vm_namespace is set automatically to var.project_name."
}

variable "user_data" {
  type        = string
  description = "Cloud-init user_data for cluster nodes."
  sensitive   = true
}

variable "manage_rke_config" {
  type        = bool
  description = "When true, Terraform manages the full RKE2 machine configuration."
  default     = true
}

variable "machine_global_config" {
  type        = string
  description = "Full machine_global_config YAML for the cluster. Defaults to a config with extended kube-apiserver etcd healthcheck timeout and extended kube-controller-manager / kube-scheduler leader-election timeouts, to tolerate higher disk I/O latency on Harvester/Longhorn-backed storage."
  default     = null
}

variable "harvester_kubeconfig_path" {
  type        = string
  description = "Path to the Harvester kubeconfig file. Used to run the kubectl patch that sets the IPPool selector.scope after cluster creation."
}
