# Development Environment

The development environment is the first reproducible Host reference. It is
intended for functional development and validation, not production workloads.

The reference will cover:

- Provisioning a Host tenant space and downstream Kubernetes cluster.
- Installing and configuring Argo Workflows.
- Configuring MinIO as an internal S3-compatible artifact repository.
- Bootstrapping a private AWS S3 backend for encrypted, lock-protected CAP-002
  Terraform state.
- Creating scoped workflow service accounts and RBAC.
- Referencing Target credentials through Kubernetes Secrets.
- Running smoke workflows that validate parameters, DAG execution, artifacts,
  and unconditional cleanup.

Development defaults may use single replicas, modest resource requests,
cluster-internal services, and Harvester-backed persistent storage. Every such
choice must be identified as development-only in the corresponding manifest or
values file.

The CAP-002 backend bootstrap is available under
[`aws-state-backend/`](aws-state-backend/README.md). Its own local bootstrap
state, plans, credentials, live ConfigMap exports, and cluster-generated
metadata must not be committed.

MinIO and AWS S3 have separate responsibilities: MinIO receives sanitized Argo
results and future evidence, while AWS S3 receives Terraform state and lock
objects only.
