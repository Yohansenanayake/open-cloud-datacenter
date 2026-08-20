# CAP-002 Tenant-Space Provisioning with Argo and Terraform

## Summary

Implement the first runnable capability as `capabilities/tenant-space`, retaining `CAP-002` as its stable metadata ID. An independently runnable Argo `WorkflowTemplate` will clone an approved repository commit, plan and apply the tenant Terraform fixture, publish sanitized results, and destroy the tenant through an exit handler.

AWS S3 will hold encrypted, locked Terraform state. MinIO remains dedicated to Argo results and evidence. A per-run PVC will hold the checked-out source, provider cache, and saved plan.

## Implementation Changes

### Capability and Terraform fixture

- Add `capability.yaml` with ID `CAP-002`, name `tenant-space`, workflow reference `cap-002-tenant-space`, timeout, inputs, outputs, and labels.
- Implement the fixture using `modules/tenancy/tenant-space?ref=terraform/v0.1.2`.
- Reproduce the manual tenant configuration:
  - Rancher project and default namespace.
  - CPU `24`, memory `32Gi`, and storage `256Gi` development defaults.
  - Configurable VM VLAN, initially `700`.
  - Configurable project role bindings.
  - Existing module defaults for shared-image behavior.
- Mount the Harvester kubeconfig as a file and pass the Rancher token only through `TF_VAR_rancher_api_token`.
- Commit the provider lock file and pin Terraform `1.15.8` in both `required_version` and the runner image.
- Expose only non-sensitive project, namespace, and network outputs. Never publish state, kubeconfigs, credentials, or the binary plan.

### AWS state backend

- Add a separately executed bootstrap Terraform stack under `infra/development` that creates:
  - A private, versioned S3 bucket.
  - S3 Block Public Access.
  - SSE-S3 default encryption.
  - A bucket policy denying non-TLS requests.
  - Current and noncurrent object expiration after 30 days.
  - A dedicated IAM user and prefix-scoped IAM policy.
- Keep the bootstrap stack’s own state local and ignored by Git.
- Do not create the IAM access key through Terraform, preventing the secret from entering bootstrap state. Document creating one key after bootstrap and immediately placing it in the Host Kubernetes Secret.
- Grant only:
  - `s3:ListBucket` for the CAP-002 prefix.
  - `s3:GetObject` and `s3:PutObject` for state.
  - `s3:GetObject`, `s3:PutObject`, and `s3:DeleteObject` for `.tflock` objects.
- Configure Terraform with `use_lockfile=true`; do not deploy DynamoDB.
- Use the state key:

```text
capabilities/tenant-space/<target-id>/<workflow-uid>/terraform.tfstate
```

### Runner and source delivery

- Add a minimal non-root runner image containing Terraform 1.15.8, Git, jq, CA certificates, and a POSIX shell.
- Keep capability source out of the image. Clone the public repository onto the PVC and check out an exact install-time commit SHA in detached mode.
- Provide build and push instructions for an operator-selected registry.
- Require the development deployment overlay to pin the resulting runner image by digest. Do not add GHCR publishing in this milestone.
- Use a 2 Gi per-run `ReadWriteOnce` PVC with the development storage class defaulting to `harvester`. Delete it after workflow completion.

### Argo workflow lifecycle

Implement `cap-002-tenant-space` as an independent `WorkflowTemplate` with:

1. `prepare`: validate mounted configuration and credentials, generate non-sensitive `.auto.tfvars.json`, and clone/verify the approved commit.
2. `init`: initialize the S3 backend using bucket, region, and the run-specific key supplied through `-backend-config`; credentials remain in AWS environment variables.
3. `validate`: run `terraform fmt -check` and `terraform validate`.
4. `plan`: run non-interactively and save the binary plan on the PVC.
5. `apply`: apply that exact saved plan without recalculating it.
6. `outputs`: create a sanitized result containing project, namespace, and network identifiers.
7. `onExit`: reinitialize from S3 and destroy resources unless a failed run explicitly requested preservation.
8. `publish`: always upload the final sanitized result to the existing MinIO-backed Argo artifact repository, including cleanup status.

Additional behavior:

- Default `preserve-on-failure` to `false`.
- Serialize CAP-002 runs with an Argo mutex for the initial shared VLAN environment.
- Retry only clone/init operations; do not automatically retry apply or destroy.
- Mark the workflow failed when cleanup fails and retain the S3 state for manual recovery.
- Treat a missing state object during cleanup as a safe no-op.
- Provide a documented recovery submission that reuses the original state key and performs destroy before the 30-day expiry.

## Public Configuration

Create these Host resources in namespace `argo`:

- `cap-002-tenant-space-config` ConfigMap:
  - `targetId`
  - `rancherUrl`
  - `harvesterClusterId`
  - `vmNetworkVlanId`
  - `cpuLimit`
  - `memoryLimit`
  - `storageLimit`
  - `groupRoleBindingsJson`
  - `repositoryUrl`
  - `repositoryCommit`
  - `stateBucket`
  - `stateRegion`
  - `statePrefix`
- `cap-002-target-credentials` Secret:
  - `rancher-api-token`
  - `harvester-kubeconfig`
- `terraform-state-aws` Secret:
  - `aws-access-key-id`
  - `aws-secret-access-key`

The submission interface exposes only `preserve-on-failure`, defaulting to `false`. Environment-specific values and credentials are not repeated as workflow arguments.

Update the repository documentation to use `capabilities/tenant-space` instead of `capabilities/CAP-XXX` and clarify that Terraform state is remote, encrypted, excluded from artifacts, and retained for 30 days rather than purely ephemeral.

## Test Plan

- Static checks:
  - Terraform formatting, initialization without backend access, validation, and provider lock consistency.
  - Argo workflow linting and Kubernetes manifest schema validation.
  - Runner image build and checks for the pinned Terraform, Git, and jq versions.
- AWS bootstrap checks:
  - Confirm versioning, encryption, public-access blocking, TLS policy, and 30-day lifecycle rules.
  - Confirm the IAM user can access only the CAP-002 prefix and manage lock files.
- Successful lab run:
  - Verify plan and apply use the same saved plan.
  - Observe project, namespace, quota, role binding, and VM network creation.
  - Confirm sanitized results reach MinIO.
  - Confirm exit-handler destroy removes the tenant and PVC.
- Failure scenarios:
  - Invalid configuration fails before target mutation.
  - Failure after apply triggers destroy.
  - `preserve-on-failure=true` leaves the tenant and S3 state for investigation.
  - A forced destroy failure marks cleanup failed and can be recovered using the documented destroy procedure.
  - Two concurrent submissions serialize through the mutex.
- Security verification:
  - Confirm tokens, kubeconfig contents, AWS keys, state, and binary plans are absent from Git, logs, parameters, and MinIO artifacts.

## Assumptions and Boundaries

- The Host cluster can reach AWS S3, GitHub, Terraform’s provider registry, Rancher, and Harvester over HTTPS.
- The operator has temporary AWS permissions to create the bucket, IAM user, and policy.
- The existing Argo namespace is `argo`, MinIO remains the artifact store, and Harvester provides the development PVC storage class.
- The current scaffold changes are preserved and extended.
- RKE2 cluster provisioning, Go acceptance tests, JUnit output, aggregate workflows, cleanup janitors, OIDC-based AWS authentication, KMS encryption, and production infrastructure are deferred.
- Preserved or unsuccessfully cleaned runs must be manually destroyed before their state reaches the 30-day expiry.
