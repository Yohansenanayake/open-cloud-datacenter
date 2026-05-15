# ── ServiceAccount for the provisioner pod ────────────────────────────────────

resource "kubernetes_service_account_v1" "provisioner" {
  metadata {
    name      = "namespace-credential-provisioner"
    namespace = var.namespace
  }
}

# ── ClusterRole — cluster-wide namespace watch + cross-namespace writes ────────

resource "kubernetes_cluster_role_v1" "provisioner" {
  metadata {
    name = "namespace-credential-provisioner"
  }

  # Watch all namespaces to detect new tenant namespaces.
  rule {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["get", "list", "watch"]
  }

  # Create/patch/delete ServiceAccounts and Secrets in any namespace.
  rule {
    api_groups = [""]
    resources  = ["serviceaccounts", "secrets"]
    verbs      = ["get", "create", "patch", "update", "delete"]
  }

  # Read ConfigMaps (kube-root-ca.crt in kube-system for CA cert).
  rule {
    api_groups = [""]
    resources  = ["configmaps"]
    verbs      = ["get"]
  }

  # Create/patch/delete RoleBindings in any namespace.
  # escalate is required so the provisioner can create RoleBindings that grant
  # permissions it doesn't itself hold (e.g. the built-in view ClusterRole for
  # the net-read binding). Without escalate, Kubernetes blocks the creation as
  # an RBAC escalation attempt.
  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["rolebindings"]
    verbs      = ["get", "create", "patch", "update", "delete", "escalate"]
  }

  # Needed to create RoleBindings that reference ClusterRoles. bind allows
  # referencing a specific ClusterRole without holding all its permissions.
  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["clusterroles"]
    verbs      = ["bind"]
  }
}

resource "kubernetes_cluster_role_binding_v1" "provisioner" {
  metadata {
    name = "namespace-credential-provisioner"
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.provisioner.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.provisioner.metadata[0].name
    namespace = var.namespace
  }
}

# ── Secret — Rancher kubeconfig ───────────────────────────────────────────────
# The reconciler writes harvesterconfig-<ns> into Rancher's fleet-default.
# This kubeconfig is mounted read-only into the pod.

resource "kubernetes_secret_v1" "rancher_kubeconfig" {
  metadata {
    name      = "namespace-credential-provisioner-rancher-kubeconfig"
    namespace = var.namespace
  }

  data = {
    "kubeconfig" = var.rancher_kubeconfig
  }
}

# ── ConfigMap — reconciler shell script ───────────────────────────────────────
# Loaded from an external file to keep shell syntax out of HCL parsing.

resource "kubernetes_config_map_v1" "script" {
  metadata {
    name      = "namespace-credential-provisioner-script"
    namespace = var.namespace
  }

  data = {
    "reconcile.sh" = file("${path.module}/scripts/reconcile.sh")
  }
}

# ── Deployment ─────────────────────────────────────────────────────────────────

resource "kubernetes_deployment_v1" "provisioner" {
  metadata {
    name      = "namespace-credential-provisioner"
    namespace = var.namespace
    labels = {
      app = "namespace-credential-provisioner"
    }
  }

  spec {
    replicas = 1

    selector {
      match_labels = {
        app = "namespace-credential-provisioner"
      }
    }

    template {
      metadata {
        labels = {
          app = "namespace-credential-provisioner"
        }
      }

      spec {
        service_account_name            = kubernetes_service_account_v1.provisioner.metadata[0].name
        automount_service_account_token = true

        container {
          name    = "provisioner"
          image   = var.image
          command = ["/bin/bash", "/scripts/reconcile.sh"]

          env {
            name  = "HARVESTER_API_SERVER"
            value = var.harvester_api_server
          }

          env {
            name  = "RANCHER_KUBECONFIG"
            value = "/rancher/kubeconfig"
          }

          volume_mount {
            name       = "script"
            mount_path = "/scripts"
          }

          volume_mount {
            name       = "rancher-kubeconfig"
            mount_path = "/rancher"
            read_only  = true
          }

          resources {
            requests = {
              cpu    = "50m"
              memory = "64Mi"
            }
            limits = {
              cpu    = "100m"
              memory = "128Mi"
            }
          }
        }

        volume {
          name = "script"
          config_map {
            name         = kubernetes_config_map_v1.script.metadata[0].name
            default_mode = "0755"
          }
        }

        volume {
          name = "rancher-kubeconfig"
          secret {
            secret_name = kubernetes_secret_v1.rancher_kubeconfig.metadata[0].name
          }
        }
      }
    }
  }

  depends_on = [kubernetes_cluster_role_binding_v1.provisioner]
}
