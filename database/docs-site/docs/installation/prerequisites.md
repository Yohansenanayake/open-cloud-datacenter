---
title: Prerequisites
sidebar_position: 1
---

# Prerequisites

The operator runs inside a Harvester HCI cluster and drives KubeVirt, CDI and Harvester APIs. Check the following before installing.

## Cluster requirements

| Requirement | Detail |
| --- | --- |
| Harvester HCI | The README states it was tested on Harvester 1.7.1 (RKE2 v1.34.3). No version check exists in the operator code, so other versions are untested rather than blocked. |
| Multus `NetworkAttachmentDefinition` | A NAD for the VM data network must already exist. A `DBInstance` references it through `spec.networkRef` (`namespace/name`). The operator never creates networks. |
| Rancher Monitoring | Provides the Prometheus Operator and the `ServiceMonitor` CRD. The operator creates a `ServiceMonitor` per instance and holds RBAC for `monitoring.coreos.com/servicemonitors`, so the CRD must exist. |
| Baked VM image | A `VirtualMachineImage` matching the catalog name must exist and be ready (see below). |
| `kubectl` | With a kubeconfig for the Harvester cluster. |

To build from source you additionally need Go 1.25+ (the `Dockerfile` builder is `golang:1.25`), `make`, and Docker.

:::note
Preflight does not verify that the NAD exists. A comment in `internal/ensure/preflight.go` records that the check is not yet implemented; only a non-empty `spec.networkRef` is enforced (`NetworkRefMissing`). A wrong NAD surfaces later, when the VM is created.
:::

## Baked-image bootstrap

The operator never imports VM images. It only reads existing Harvester `VirtualMachineImage` objects. During preflight for a never-provisioned instance the operator:

1. Looks up the OS stream configured by [`databaseDefaults.osVersion`](/configuration/operator-config) (default `22.04`) in the compiled-in catalog (`internal/catalog/baked_images.go`).
2. Requires the stream to be in state `Validated`.
3. Checks that the requested `engineVersion` is supported by that image revision.
4. Resolves the image by name with `ResolveVMImage`, in the namespace set by `infrastructure.harvester.imageNamespace` (default `default`) unless the name carries an explicit `namespace/name` prefix.

Catalog contents in this release:

| OS stream (`osVersion`) | Active image name | Engine versions | State |
| --- | --- | --- | --- |
| `22.04` | `ubuntu-2204-postgres-v20260515` | 15, 16, 17 (default 17) | Validated |
| `24.04` | `ubuntu-2404-postgres-v20260701` | 15, 16, 17, 18 (default 17) | Validated |

A third revision, `ubuntu-2404-postgres-v20260815` (engine 18 only), is registered but is not the active revision of any stream; it exists to exercise the end-of-life and repave paths. See [Images and repave](/operations/images-and-repave).

Upload the image in the namespace given by `imageNamespace`, and name it exactly as in the table; a misspelt name fails the same way a missing one does.

| Failure | Condition reason | Behaviour |
| --- | --- | --- |
| Image not found | `OSImageNotFound` | Terminal |
| Stream unknown or not validated, or reference ambiguous or invalid | `OSImageInvalid` | Terminal |
| Image present but still importing | `OSImageNotReady` | Retried every 10 seconds |

Image publication and automatic import are not implemented: there is no download-based generator, so you upload images by hand (Harvester UI or `virtctl image-upload`).

:::info Verified against
- `README.md` (Prerequisites section)
- `INSTALL.md` (step 7)
- `internal/ensure/preflight.go`
- `internal/catalog/baked_images.go`
- `internal/config/defaults.go`
- `Dockerfile`
- `config/rbac/role.yaml`
:::
