# CAP-002: Tenant Space

CAP-002 is the first runnable capability in the Harvester Upgrade Test Suite. It
creates a uniquely named, quota-enabled tenant space, records sanitized
Terraform outputs, and destroys the tenant through an Argo exit handler.

The capability is intentionally limited to tenant-space provisioning. It does
not provision an RKE2 cluster or run the future Go behavioral assertions.

## Resources

- `capability.yaml` defines the stable capability metadata.
- `fixtures/terraform` contains the Terraform root module.
- `workflow/workflow-template.yaml` contains the normal lifecycle.
- `workflow/recovery-workflow-template.yaml` provides manual state recovery.
- `workflow/configmap.example.yaml` is the development target-profile example.
- `workflow/kustomization.yaml` pins the operator-built runner image.

## Prerequisites

Before installing this capability:

1. Bootstrap the AWS state backend described in
   [`infra/development/aws-state-backend`](../../infra/development/aws-state-backend/README.md).
2. Build and push the
   [Terraform runner](../../pipeline/images/terraform-runner/README.md).
3. Copy `workflow/configmap.example.yaml` outside the repository and replace
   every `REPLACE_...` value after the implementation commit exists.
4. Set the runner repository and digest in `workflow/kustomization.yaml`.
5. Create the two credential Secrets without committing their values.

Create the Target credential Secret:

```bash
kubectl -n argo create secret generic cap-002-target-credentials \
  --from-literal=rancher-api-token='REPLACE_WITH_RANCHER_TOKEN' \
  --from-file=harvester-kubeconfig=/absolute/path/to/target-harvester.yaml
```

The AWS bootstrap guide creates `terraform-state-aws` separately.

Create the non-secret target profile without changing the committed example:

```bash
cp capabilities/tenant-space/workflow/configmap.example.yaml \
  /tmp/cap-002-tenant-space-config.yaml
# Replace all REPLACE_ values. repositoryCommit must be the full SHA of the
# approved commit that contains this capability.
kubectl apply -f /tmp/cap-002-tenant-space-config.yaml
```

## Install and run

Render the manifests first and inspect the pinned image reference:

```bash
kubectl kustomize capabilities/tenant-space/workflow
kubectl apply -k capabilities/tenant-space/workflow
```

Submit the independently runnable workflow:

```bash
argo submit --from workflowtemplate/cap-002-tenant-space \
  --namespace argo \
  --watch
```

Cleanup runs by default after both successful and failed provisioning. To keep a
failed tenant temporarily for investigation:

```bash
argo submit --from workflowtemplate/cap-002-tenant-space \
  --namespace argo \
  --parameter preserve-on-failure=true \
  --watch
```

The final `result.json` is published through Argo's configured artifact
repository. Terraform state and the saved plan are never uploaded as artifacts.

## Recover failed cleanup

Find the state key in the failed workflow's final result or reconstruct it as:

```text
<statePrefix>/tenant-space/<targetId>/<original-workflow-uid>/terraform.tfstate
```

Submit the recovery template before the S3 lifecycle expires the state:

```bash
argo submit --from workflowtemplate/cap-002-tenant-space-recovery \
  --namespace argo \
  --parameter state-key='capabilities/tenant-space/TARGET/WORKFLOW_UID/terraform.tfstate' \
  --watch
```

The recovery workflow verifies that the key belongs to the configured Target
and state prefix, reconstructs the original project name from the workflow UID,
and runs `terraform destroy`. Never recover a key from an untrusted source.
