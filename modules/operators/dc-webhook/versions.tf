terraform {
  required_providers {
    kubernetes = {
      source = "hashicorp/kubernetes"
      # See the layer's versions.tf for the 2.38 Resource Identity caveat.
      version = "~> 2.30"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}
