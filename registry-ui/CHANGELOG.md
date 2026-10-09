# Changelog

Notable changes to the Registries Rancher UI extension, newest first. Versions follow
[Semantic Versioning](https://semver.org); each release is tagged `registry-ui/vX.Y.Z` and published as
the Extension Catalog Image `ghcr.io/wso2/ui-extension-registry-ui:X.Y.Z`.

## 0.1.0 — preview

First release. Needs Rancher 2.15 or later, and the registry operator 0.1.0 or later on the Harvester
clusters it manages.

### Added

- **Registries** in Rancher's left menu, listing the Harvester clusters that have the registry
  operator installed, with **Manage** and the cluster's kubectl tools.
- Per-cluster **Overview**: registry totals, registries that are not Ready and why, quota committed
  per plan, and the operator's health (pod, Harbor URL, Harvester Addon state).
- Registries list grouped by namespace, with plan, quota, Harbor project and registry URL.
- Create a registry (namespace, name, plan), change its plan, clone, download or delete it.
- Registry details with its Harbor project, URL, quota and credential Secret names, and a **Connect**
  tab with ready-to-copy commands to read the credentials, push an image, and use the pull credential
  on another cluster.
- Access follows Rancher RBAC; the extension never reads Secrets.
- [User guide](docs/USER-GUIDE.md) with screenshots of every screen.

### Tested with

| Rancher | Harvester | Registry operator |
| --- | --- | --- |
| v2.15.2 | v1.9.0 | 0.1.0 |
