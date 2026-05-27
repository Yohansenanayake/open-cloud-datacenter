# dc-webhook

Deploys the DC-API mutating admission webhook onto a Harvester host cluster.
The webhook intercepts KubeVirt VirtualMachine CREATE/UPDATE requests and
injects MAC-pinning and KubeOVN annotations, which is required for L2
return-path delivery when RKE2 node VMs live on KubeOVN overlay networks.

## What this module creates

- Self-signed CA + server TLS cert (hashicorp/tls) — stored in TF state, no
  cert-manager required. Rotate by `terraform taint` + `apply`.
- `kubernetes_namespace` — `dc-system` (configurable via `namespace`).
- `kubernetes_secret` — TLS cert+key (`dc-api-webhook-tls`).
- `kubernetes_secret` — Harvester kubeconfig (`dc-api-webhook-secrets`).
- `kubernetes_secret` — GHCR image pull secret (`ghcr-pull-secret`).
- `kubernetes_service_account` — `dc-api-webhook`.
- `kubernetes_cluster_role` + `kubernetes_cluster_role_binding` — read-only
  access to NetworkAttachmentDefinitions in any namespace.
- `kubernetes_deployment` — 1 replica (configurable), port 9443 HTTPS.
- `kubernetes_service` — ClusterIP, port 443 → 9443.
- `kubernetes_manifest` — `MutatingWebhookConfiguration` named
  `dc-api-ovn-mac-webhook`. The webhook handler's name uses
  `var.webhook_domain` as its suffix so operators in shared clusters
  don't collide.

## Apply order

Single-phase apply — no `-target` tricks needed. All resources are concrete
Kubernetes objects; the cluster endpoint is known before apply.

```bash
./tf.sh apply --region <env> --layer dc-webhooks
```

## Updating the image tag

```bash
./tf.sh apply --region <env> --layer dc-webhooks \
  -- -var='webhook_image=<registry>/<owner>/dc-api-webhook:<new-sha>'
```

Or update `webhook_image` in the layer's `terraform.tfvars` and re-apply.

## TLS certificate rotation

Certs are valid for 10 years (87600 hours) and stored in TF state. To force
regeneration:

```bash
cd environments/<env>/02-dc-webhooks
terraform taint 'module.dc_webhook.tls_private_key.ca'
terraform taint 'module.dc_webhook.tls_self_signed_cert.ca'
terraform taint 'module.dc_webhook.tls_private_key.server'
./tf.sh apply --region <env> --layer dc-webhooks
```

A new CA and server cert will be generated, the TLS Secret will be updated, and
the MutatingWebhookConfiguration's caBundle will be updated atomically. No
manual kubectl steps needed.

## Failure policy

`failurePolicy: Ignore` — if the webhook pod is down, VM admission succeeds
without annotation injection. OVN VMs without annotations will fall back to
the pre-webhook behaviour (no MAC pinning). Not a security issue; just means
OVN MAC alignment relies on kube-ovn's own defaults.

## No namespaceSelector

The webhook handler filters by NAD type internally (non-OVN NADs are no-ops).
Adding a `namespaceSelector` would require labeling tenant namespaces from
the dc-api Go code, coupling two repos unnecessarily. Add it later as a
2-line TF change if namespace scoping is ever required.
