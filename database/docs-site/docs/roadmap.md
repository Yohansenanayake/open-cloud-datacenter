---
title: Roadmap
sidebar_position: 11
---

# Roadmap

:::caution Planned / not yet implemented
Everything below the first section is **not part of v0.1.0**. These items come from internal design notes. They are intentions, not commitments, and have no dates. Do not build on them. Where the notes and the code disagree, the code is the source of truth.
:::

## What v0.1.0 ships

For contrast, this is implemented and working in the code today:

- The `DBInstance` CRD and a bounded ensure-step reconciler that provisions a PostgreSQL VM on Harvester and KubeVirt.
- Baked PostgreSQL images from a compiled-in catalog, with image drift reporting and operator-triggered repave.
- Cold resize of CPU, memory and disk (grow only), and stop and start.
- Generated master password, or a user-supplied password through `spec.credentials`, with source-change reporting and a refusal to regenerate lost credentials.
- Per-instance TLS material and a tenant connection Secret.
- Prometheus monitoring objects per instance.
- Crash-loop detection and halt.
- Deletion protection and finalizer-based teardown.
- The `security.rejectVMPassword` policy switch.
- A Helm chart and a Harvester `Addon` manifest for installation.
- A thin REST gateway over the CRD.

## Planned / not yet implemented

### Credential management

| Item | State | Notes |
| --- | --- | --- |
| Certificate authentication for clients | Planned, paused | Designed in the credential notes. It depends on a runtime channel into the VM. |
| Platform credentials rotation (replication and exporter passwords, CA and server certificate renewal) | Planned, paused | v0.1.0 generates these once and never rotates them. |
| Runtime credential delivery to a running VM | Paused until Harvester 1.9.1 | The design needs a management network between the operator and the guest. |
| Master password reset and rotation | Planned, paused | Same dependency. Today a changed password Secret is only reported, never applied. A lost password on a hardened install cannot be reset. |
| Short-lived password reveal | Excluded from the current scope | Not planned for the near term. |

### Networking

| Item | State | Notes |
| --- | --- | --- |
| Dedicated management VLAN or network between operator and guest | Blocked on Harvester 1.9.1 | The notes record that the current Harvester cannot support it. The planned dependency is Kube-OVN and KubeVirt VLAN support in Harvester 1.9.1. |
| NetworkAttachmentDefinition existence check in preflight | Not implemented | The code has a note that it is missing. A wrong `networkRef` is only noticed at boot. |

### Database features reserved in the schema

These fields exist in the CRD for forward compatibility. The reconciler ignores them.

| Field | Planned capability |
| --- | --- |
| `s3BackupConfig`, `backupRetentionPeriod`, `preferredBackupWindow` | Scheduled backups and retention. |
| `multiAZ` | A high availability standby. |
| `status.readReplicas` | Read replicas. |
| `dbParameterGroupRef` | A parameter group resource. No such CRD exists. |
| `tags` | Propagating tags to child resources. |
| `manageMasterUserPassword`, `masterUserPasswordRef` | Superseded by `spec.credentials`; kept reserved. |

### Images and lifecycle

| Item | State |
| --- | --- |
| Automated baked-image publishing and import into Harvester | Not implemented. The image must be uploaded by hand today. |
| Per-instance image or OS stream selection | Not implemented. The stream is an operator-wide setting. |
| In-place PostgreSQL major version upgrade | Not implemented. Migrate with dump and restore. |
| Image catalog loaded at runtime instead of compiled in | Not implemented. A catalog change needs a new operator build. |
| Single-namespace operator install | Not supported. Needs a chart toggle and a cache-restricting operator setting. |

### Distribution and UI

| Item | State |
| --- | --- |
| Public release under the WSO2 GHCR namespace | Pending registry write access. v0.1.0 artifacts are experimental and were published from a personal registry for testing. |
| Harvester Addon as the supported distribution route | In progress. This is the primary release goal, currently an experimental, repository-registered Addon. |
| Upstream-bundled Harvester Addon | Depends on agreement with Harvester. Not a prerequisite for a release. |
| Rancher Partner Charts or Marketplace catalog | Decided against on 2026-09-15 because the operator runs inside the Harvester cluster. |
| Rancher UI extension for `DBInstance` | Researched and paused. The scaffold was removed. Priority is the Addon release first. |
| Tested upgrade and rollback path between releases | Not established. |

## How this page is maintained

Items move from this page into the [Release notes](/release-notes) only when the code implements them and the docs for that feature have been verified against the code.

:::info Source of this page
Derived from the internal design notes under `my-docs/release/`, `my-docs/Credintials/` and `my-docs/ui-extension/`, cross-checked against `database/README.md` ("Not yet implemented"), `database/CREDENTIALS.md`, `database/INSTALL.md` and the field support comments in `database/api/v1alpha1/dbinstance_types.go`. These are plans, so this page is excepted from the "Verified against" rule.
:::
