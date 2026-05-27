# ─────────────────────────────────────────────────────────────────────────────
# DC-API Mutating Admission Webhook
#
# Deploys the dc-api-webhook binary onto a Harvester host cluster.
# The webhook intercepts KubeVirt VirtualMachine CREATE/UPDATE requests and
# injects MAC-pinning + KubeOVN annotations, which is the prerequisite for
# L2 return-path delivery when RKE2 node VMs live on KubeOVN overlay networks.
#
# TLS: a self-signed CA + server cert are generated in TF state via the
# hashicorp/tls provider. cert-manager is NOT required or used.
#
# No provider blocks here — all provider config lives in the calling layer.
# ─────────────────────────────────────────────────────────────────────────────

# ── TLS: CA ──────────────────────────────────────────────────────────────────

resource "tls_private_key" "ca" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "tls_self_signed_cert" "ca" {
  private_key_pem = tls_private_key.ca.private_key_pem

  subject {
    common_name  = "dc-api-webhook-ca"
    organization = "WSO2 DC"
  }

  # 10 years — rotated by replacing TF state, not by short-lived certs.
  validity_period_hours = 87600
  is_ca_certificate     = true

  allowed_uses = [
    "cert_signing",
    "key_encipherment",
    "digital_signature",
  ]
}

# ── TLS: server cert ─────────────────────────────────────────────────────────

resource "tls_private_key" "server" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "tls_cert_request" "server" {
  private_key_pem = tls_private_key.server.private_key_pem

  subject {
    common_name  = "dc-api-webhook.${var.namespace}.svc"
    organization = "WSO2 DC"
  }

  # The apiserver calls the webhook by Service DNS name; both short and FQDN
  # forms must be in the SAN list.
  dns_names = [
    "dc-api-webhook",
    "dc-api-webhook.${var.namespace}",
    "dc-api-webhook.${var.namespace}.svc",
    "dc-api-webhook.${var.namespace}.svc.cluster.local",
  ]
}

resource "tls_locally_signed_cert" "server" {
  cert_request_pem   = tls_cert_request.server.cert_request_pem
  ca_private_key_pem = tls_private_key.ca.private_key_pem
  ca_cert_pem        = tls_self_signed_cert.ca.cert_pem

  validity_period_hours = 87600

  allowed_uses = [
    "key_encipherment",
    "digital_signature",
    "server_auth",
  ]
}

# ── Namespace ─────────────────────────────────────────────────────────────────

resource "kubernetes_namespace" "webhook" {
  metadata {
    name = var.namespace
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

# ── Secrets ───────────────────────────────────────────────────────────────────

# TLS cert + key for the webhook HTTPS server. Mounted read-only at /tls.
resource "kubernetes_secret" "tls" {
  metadata {
    name      = "dc-api-webhook-tls"
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }
  type = "kubernetes.io/tls"
  data = {
    "tls.crt" = tls_locally_signed_cert.server.cert_pem
    "tls.key" = tls_private_key.server.private_key_pem
  }
}

# Webhook-specific env secrets: only the Harvester kubeconfig is sensitive at
# runtime. Keeping it separate from the TLS secret so rotation of one doesn't
# force a pod restart for the other.
#
# Note on the base64 dance: the consumer passes the kubeconfig pre-encoded
# (`harvester_kubeconfig_b64`) so it can flow through TF remote_state /
# outputs without literal YAML embedding. Terraform's `kubernetes_secret`
# `data` field auto-base64-encodes whatever string it receives — so we
# decode here first, otherwise the value in the live Secret ends up double-
# encoded and the webhook pod reads back base64-of-base64 instead of the
# raw kubeconfig.
resource "kubernetes_secret" "webhook" {
  metadata {
    name      = "dc-api-webhook-secrets"
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }
  data = {
    DCWEBHOOK_KUBECONFIG = base64decode(var.harvester_kubeconfig_b64)
  }
}

# GHCR image pull secret — same pattern as dc-controlplane-services.
resource "kubernetes_secret" "ghcr_pull" {
  metadata {
    name      = "ghcr-pull-secret"
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }
  type = "kubernetes.io/dockerconfigjson"
  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        "ghcr.io" = {
          username = var.ghcr_username
          password = var.ghcr_pat
          auth     = base64encode("${var.ghcr_username}:${var.ghcr_pat}")
        }
      }
    })
  }
}

# ── RBAC ──────────────────────────────────────────────────────────────────────

resource "kubernetes_service_account" "webhook" {
  metadata {
    name      = "dc-api-webhook"
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }
}

# ClusterRole: read NADs from any namespace (tenant namespaces are dc-<id>,
# not predictable at deploy time, so ClusterRole is required).
resource "kubernetes_cluster_role" "webhook" {
  metadata {
    name = "dc-api-webhook"
  }

  rule {
    api_groups = ["k8s.cni.cncf.io"]
    resources  = ["network-attachment-definitions"]
    verbs      = ["get"]
  }
}

resource "kubernetes_cluster_role_binding" "webhook" {
  metadata {
    name = "dc-api-webhook"
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.webhook.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.webhook.metadata[0].name
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }
}

# ── Deployment + Service ──────────────────────────────────────────────────────

resource "kubernetes_deployment" "webhook" {
  metadata {
    name      = "dc-api-webhook"
    namespace = kubernetes_namespace.webhook.metadata[0].name
    labels = {
      app = "dc-api-webhook"
    }
  }

  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }

  spec {
    replicas = var.replicas

    selector {
      match_labels = {
        app = "dc-api-webhook"
      }
    }

    template {
      metadata {
        labels = {
          app = "dc-api-webhook"
        }
      }

      spec {
        service_account_name = kubernetes_service_account.webhook.metadata[0].name

        security_context {
          run_as_non_root = true
          run_as_user     = 65532
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        image_pull_secrets {
          name = kubernetes_secret.ghcr_pull.metadata[0].name
        }

        container {
          name              = "webhook"
          image             = var.webhook_image
          image_pull_policy = "Always"

          port {
            name           = "https"
            container_port = 9443
          }

          env {
            name  = "DCWEBHOOK_LISTEN_ADDR"
            value = ":9443"
          }

          env {
            name  = "DCWEBHOOK_CERT_FILE"
            value = "/tls/tls.crt"
          }

          env {
            name  = "DCWEBHOOK_KEY_FILE"
            value = "/tls/tls.key"
          }

          env {
            name  = "DCWEBHOOK_LOG_LEVEL"
            value = var.log_level
          }

          env {
            name = "DCWEBHOOK_KUBECONFIG"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.webhook.metadata[0].name
                key  = "DCWEBHOOK_KUBECONFIG"
              }
            }
          }

          volume_mount {
            name       = "tls"
            mount_path = "/tls"
            read_only  = true
          }

          liveness_probe {
            http_get {
              path   = "/healthz"
              port   = 9443
              scheme = "HTTPS"
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }

          readiness_probe {
            http_get {
              path   = "/healthz"
              port   = 9443
              scheme = "HTTPS"
            }
            initial_delay_seconds = 2
            period_seconds        = 5
          }

          resources {
            requests = {
              cpu    = "10m"
              memory = "32Mi"
            }
            limits = {
              cpu    = "100m"
              memory = "128Mi"
            }
          }

          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities {
              drop = ["ALL"]
            }
          }
        }

        volume {
          name = "tls"
          secret {
            secret_name = kubernetes_secret.tls.metadata[0].name
          }
        }
      }
    }
  }

  depends_on = [kubernetes_secret.tls, kubernetes_secret.webhook]
}

resource "kubernetes_service" "webhook" {
  metadata {
    name      = "dc-api-webhook"
    namespace = kubernetes_namespace.webhook.metadata[0].name
  }

  spec {
    type = "ClusterIP"
    selector = {
      app = "dc-api-webhook"
    }
    port {
      name        = "https"
      port        = 443
      target_port = 9443
    }
  }
}

# ── MutatingWebhookConfiguration ─────────────────────────────────────────────
#
# failurePolicy: Ignore is intentional. If the webhook is down, non-OVN VMs
# must not be blocked. OVN VMs without the annotations will simply fail to get
# the correct MAC allocation — the same outcome as before F14.
#
# No namespaceSelector: the handler filters internally by NAD type and no-ops
# on non-OVN VMs regardless of namespace.

resource "kubernetes_manifest" "mutating_webhook" {
  manifest = {
    apiVersion = "admissionregistration.k8s.io/v1"
    kind       = "MutatingWebhookConfiguration"
    metadata = {
      name = "dc-api-ovn-mac-webhook"
    }
    webhooks = [
      {
        name                    = "virtualmachines.kubevirt.io.dc-api.${var.webhook_domain}"
        admissionReviewVersions = ["v1"]
        clientConfig = {
          service = {
            name      = kubernetes_service.webhook.metadata[0].name
            namespace = kubernetes_namespace.webhook.metadata[0].name
            path      = "/mutate"
            port      = 443
          }
          # Base64-encoded CA cert that signed the server TLS cert.
          # The apiserver uses this to verify the webhook's TLS handshake.
          caBundle = base64encode(tls_self_signed_cert.ca.cert_pem)
        }
        rules = [
          {
            apiGroups   = ["kubevirt.io"]
            apiVersions = ["v1"]
            resources   = ["virtualmachines"]
            operations  = ["CREATE", "UPDATE"]
            scope       = "Namespaced"
          }
        ]
        failurePolicy      = "Ignore"
        sideEffects        = "None"
        timeoutSeconds     = 5
        reinvocationPolicy = "Never"
      }
    ]
  }

  depends_on = [kubernetes_service.webhook]
}
