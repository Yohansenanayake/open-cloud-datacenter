---
title: Release notes
sidebar_position: 12
---

# Release notes

## v0.1.0 (experiment)

Chart version and app version: `0.1.0-experiment.2` (`database/charts/chart/Chart.yaml`). CRD: `dbaas.opencloud.wso2.com/v1alpha1`, kind `DBInstance`.

:::caution Experimental
This is a preview. The API is `v1alpha1`, there is no tested upgrade path between experimental builds, and no backup or high availability. Do not use it for data you cannot lose.
:::

### Supported and tested versions

| Component | Version | Source |
| --- | --- | --- |
| Harvester | 1.7.1 (tested) | `database/README.md` |
| RKE2 | v1.34.3 (tested) | `database/README.md` |
| KubeVirt API and client | v1.6.0 (built against) | `database/go.mod` |
| controller-runtime | v0.20.4 (built against) | `database/go.mod` |
| Kubernetes client libraries | v0.32.5 (built against) | `database/go.mod` |
| Go | 1.25.7 | `database/go.mod` |
| Helm | 3.8 or newer | `database/INSTALL.md` |
| PostgreSQL | 15, 16, 17 on Ubuntu 22.04; 15, 16, 17, 18 on Ubuntu 24.04 | `database/internal/catalog/baked_images.go` |
| Storage | Longhorn (default StorageClass `longhorn`) | `database/internal/config/defaults.go` |

Only Harvester 1.7.1 has been exercised. Other versions are untested and unsupported. The Harvester `Addon` route was validated on the maintainer's cluster, not on a range of versions.

### Highlights

**Provisioning and lifecycle**

- `DBInstance` custom resource. The operator creates a KubeVirt VM from a baked PostgreSQL image, a data volume, cloud-init, credentials and TLS Secrets, a connection Secret and monitoring objects.
- A bounded ensure-step reconciler with explicit condition types and reasons. See [Status and conditions](/reference/status-and-conditions).
- Cold resize of instance class and storage (grow only), and stop and start through `spec.running`. See [Resize and power](/operations/resize-and-power).
- Deletion protection and finalizer-based teardown. See [Lifecycle and deletion](/operations/lifecycle-and-deletion).
- Crash-loop detection: three chained unplanned restarts halt the VM and require manual recovery.
- Degraded reporting (report-only, no automatic restart on probe failure).

**Images**

- Baked-image catalog for Ubuntu 22.04 and 24.04 with PostgreSQL 15 to 18. A Packer build and seed scripts live under `database/images/packer`.
- `ImageDrift` reporting and operator-triggered repave that swaps the OS disk and keeps the data disk. Engine end-of-life blocks a repave. See [Images and repave](/operations/images-and-repave).
- `databaseDefaults.osVersion` operator setting selects the stream.

**Credentials and security**

- Generated master password stored in `pg-NAME-credentials`, or a user-supplied password through `spec.credentials` with validation (length, encoding, reserved usernames, no line breaks).
- Recorded password source and reporting of later changes (`PasswordSourceChanged`).
- Refusal to regenerate durable credentials of a provisioned instance (`CredentialsLost`, `InterventionRequired`).
- Password verification inside the VM during bootstrap, and redaction of the cloud-init payload once the database is up.
- `security.rejectVMPassword` policy to refuse VM password login for new instances. See [Policy switches](/configuration/policy-switches).
- Immutable-field protection through CEL rules and an operator check.

**Operations and installation**

- Helm chart generated through the kubebuilder helm plugin, plus a Harvester `Addon` manifest. See [Helm and Harvester Addon install](/installation/helm-addon).
- Centralised operator configuration (flags and environment) with `databaseDefaults`, `infrastructure`, `observability`, `security` and `logging` sections. See [Operator configuration](/configuration/operator-config).
- Prometheus `ServiceMonitor` per instance and a metrics endpoint for the operator. See [Monitoring](/monitoring).
- Thin REST gateway over the CRD that forwards the caller's token.
- A chart-test CI workflow.

### Commit history

Condensed from `git log` for `database/` (about 120 commits between 2026-05-28 and 2026-10-08), grouped by theme.

| Period | Theme | Representative commits |
| --- | --- | --- |
| 2026-05-28 to 2026-06 | Initial operator, Harvester client, split credential and cloud-init Secrets | `feat(operators): add database operator`; `Feature(database): split cloudinit and credentials into separate Secrets`; typed Harvester client |
| 2026-07 | Configuration and status handling | Centralised configuration with konf; status patch fixes |
| 2026-08 to 2026-09 | Image lifecycle | Baked-image catalog; `ImageDrift` and repave; Packer build scripts; self-healing of `CurrentImageRevision`; repave E2E script |
| 2026-10-02 | Helm release | Helm chart via kubebuilder plugin; `INSTALL.md`; chart-test workflow (merged in PR 305) |
| 2026-10 | Credential management | Credentials validation and immutability; password source tracking; lost-credentials intervention; `PasswordSourceChanged`; reject VM password policy; `CREDENTIALS.md`; chart bumped to `0.1.0-experiment.2` |

Most recent commits at the time of writing:

```text
4523b3e feat(chart): update version and appVersion to 0.1.0-experiment.2
a6703aa feat(documentation): add CREDENTIALS.md for master password management and usage guidelines
ab54cc5 feat(database): enhance password source validation to prevent name clashes with DBaaS-owned secrets
4a9813c feat(security): implement policy to reject VM password login for new DBInstances
7b85c15 feat(database): add handling for changed password source and enhance reporting in credentials management
38731d3 feat(database): Implement intervention handling for lost credentials and enhance credential resolution logic
15781c7 feat(database): enhance credentials management with source tracking and annotations
4341349 feat(database): add password source validation and management for DBInstance credentials
b6e03b8 feat(database): implement credentials management with validation and immutability rules
4be772f feat(database): enhance role setup and password handling in cloud-init scripts
```

### Known limitations

- No backup or restore. `s3BackupConfig`, `backupRetentionPeriod` and `preferredBackupWindow` are ignored.
- No high availability or replicas. `multiAZ` is ignored.
- `manageMasterUserPassword`, `masterUserPasswordRef`, `dbParameterGroupRef` and `tags` are ignored.
- Passwords cannot be changed or reset on a running database through the operator.
- No certificate rotation or client certificate authentication.
- Baked images must be uploaded to Harvester by hand.
- The image catalog is compiled into the operator binary.
- The NetworkAttachmentDefinition named by `networkRef` is not validated.
- Single-namespace installs are not supported.
- Whether data and OS disk PVCs are removed on instance deletion is not verified by the operator. Check after deleting.

See the [Roadmap](/roadmap) for what is planned, and [Troubleshooting](/troubleshooting) for problems you may hit.

:::info Verified against
- `database/charts/chart/Chart.yaml`
- `database/go.mod`
- `database/README.md`
- `database/INSTALL.md`
- `database/CREDENTIALS.md`
- `database/internal/catalog/baked_images.go`
- `database/internal/config/defaults.go`
- `database/internal/config/flags.go`
- `database/api/v1alpha1/dbinstance_types.go`
- `database/api/v1alpha1/dbinstance_conditions.go`
- `database/internal/ensure/` (step implementations)
- `git log` in `database/`
:::
