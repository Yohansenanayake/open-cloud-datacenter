# CAP-002: Tenant Space

This CAP-002 milestone provisions one quota-enabled tenant space through
Terraform orchestrated by Argo Workflows and destroys it through an
unconditional exit handler.

Output collection, result artifacts, behavioral assertions, and failed-run
recovery automation are separate follow-up milestones.

## Workflow

The main workflow runs four sequential steps:

```text
prepare → terraform init → terraform plan → terraform apply
                                                    │
                                                    └─ always → terraform destroy
```

Argo creates a 2 Gi `ReadWriteOnce` PVC for each workflow. All steps, including
the exit handler, mount that same claim. Terraform uses the local backend in a
run-specific directory:

```text
/workspace/runs/<workflow-uid>/state/terraform.tfstate
```

The Terraform working directory and saved plan use the same workflow UID. The
exit handler treats missing state as a safe no-op and otherwise runs
`terraform destroy`. Argo deletes the PVC only when the complete workflow,
including destroy, succeeds. Failed workflows retain their PVC so the state is
available for investigation and recovery. A workflow mutex serializes CAP-002
runs in the initial shared VLAN environment.

## Build the runner image

The runner image carries Terraform 1.15.8 and the CAP-002 fixture:

```bash
docker build \
  --file pipeline/images/terraform-runner/Dockerfile \
  --tag REGISTRY/harvester-testsuite-terraform-runner:0.1.0 \
  .

docker push REGISTRY/harvester-testsuite-terraform-runner:0.1.0
```

Resolve the pushed digest and replace `newName` and `digest` in
`workflow/kustomization.yaml`.

## Configure the Target

Create the credential Secret directly in the Host cluster:

```bash
kubectl -n argo create secret generic cap-002-target-credentials \
  --from-literal=rancher-api-token='REPLACE_WITH_RANCHER_TOKEN' \
  --from-file=harvester-kubeconfig=/absolute/path/to/target-harvester.yaml
```

Copy and edit the non-secret workflow parameter example:

```bash
cp capabilities/tenant-space/workflow/parameters/development.example.yaml \
  /tmp/cap-002-tenant-space-parameters.yaml
```

Replace the Rancher URL, cluster ID, role binding, and any development defaults
that differ in the Target environment. Values containing `REPLACE_` are rejected
by the `prepare` step before Terraform runs.

The workflow appends the first eight characters of its unique UID to
`project-name-prefix`. For example, the prefix `cap002-tenant-space-dev` can
produce:

```text
cap002-tenant-space-dev-a1b2c3d4
```

The generated name is printed by the `prepare` step. Each workflow submission
therefore creates an independently named tenant and stores its Terraform state
under that workflow's UID.

## Install and submit

```bash
kubectl apply -k capabilities/tenant-space/workflow

argo submit \
  --namespace argo \
  --from workflowtemplate/cap-002-tenant-space \
  --parameter-file /tmp/cap-002-tenant-space-parameters.yaml \
  --watch
```

The WorkflowTemplate contains development defaults for the project-name prefix,
VLAN, and resource quotas. A value can be overridden directly for a run, for
example:

```bash
argo submit \
  --namespace argo \
  --from workflowtemplate/cap-002-tenant-space \
  --parameter-file /tmp/cap-002-tenant-space-parameters.yaml \
  -p project-name-prefix=cap002-tenant-space-test \
  -p cpu-limit=12 \
  --watch
```

Workflow parameters are stored in the submitted Workflow and visible in Argo.
Only non-sensitive configuration belongs in the parameter file. The Rancher
token and Harvester kubeconfig remain in `cap-002-target-credentials`.

During the run, Terraform logs show the generated project, namespaces, quota,
role binding, and VM network being created and then removed. After a successful
workflow, verify that the tenant resources and workflow PVC no longer exist.

```bash
kubectl -n argo get pvc
```

If the workflow fails, do not delete its PVC until the tenant has been confirmed
absent or destroyed using the retained Terraform state. Automated recovery for
that case is a later milestone.
