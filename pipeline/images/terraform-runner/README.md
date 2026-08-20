# Terraform Runner Image

This non-root image supplies Terraform, Git, jq, CA certificates, and a POSIX
shell. Capability source is cloned at runtime and is not baked into the image.

Build and push the image to a registry reachable from the Host cluster:

```bash
docker build \
  --tag REGISTRY/harvester-testsuite-terraform-runner:0.1.0 \
  pipeline/images/terraform-runner
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
