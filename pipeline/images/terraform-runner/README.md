# Terraform Runner Image

This non-root image supplies Terraform, Git, jq, CA certificates, and a POSIX
shell. For the initial CAP-002 milestone, it also carries the tenant-space
Terraform fixture so the workflow does not need to clone Git at runtime.

Build and push the image to a registry reachable from the Host cluster:

```bash
docker build \
  --file pipeline/images/terraform-runner/Dockerfile \
  --tag REGISTRY/harvester-testsuite-terraform-runner:0.1.0 \
  .
docker push REGISTRY/harvester-testsuite-terraform-runner:0.1.0
```

Resolve the pushed digest, then replace `newName` and `digest` in
`capabilities/tenant-space/workflow/kustomization.yaml`. The deployed workflow
must use a digest, not a mutable tag.

Verify the local image before publishing it:

```bash
docker run --rm REGISTRY/harvester-testsuite-terraform-runner:0.1.0 terraform version
docker run --rm REGISTRY/harvester-testsuite-terraform-runner:0.1.0 git --version
docker run --rm REGISTRY/harvester-testsuite-terraform-runner:0.1.0 jq --version
```
