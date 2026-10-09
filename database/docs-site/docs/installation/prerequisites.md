---
title: Prerequisites
sidebar_position: 1
---

# Prerequisites

The operator runs inside a Harvester HCI cluster and drives KubeVirt, CDI and Harvester APIs. Check the following before installing.

## Cluster requirements

| Requirement | Detail |
| --- | --- |
| Harvester HCI | v0.1.0 is tested on Harvester 1.9.0. No version check exists in the operator code, so other versions are untested rather than blocked. |
| Multus `NetworkAttachmentDefinition` | A NAD for the VM data network must already exist. A `DBInstance` references it through `spec.networkRef` (`namespace/name`). The operator never creates networks. |
| Rancher Monitoring | Provides the Prometheus Operator and the `ServiceMonitor` CRD. The operator creates a `ServiceMonitor` per instance and holds RBAC for `monitoring.coreos.com/servicemonitors`, so the CRD must exist. |
| Baked VM image | A `VirtualMachineImage` matching the catalog name must exist and be ready (see below). |
| `kubectl` | With a kubeconfig for the Harvester cluster. |
| Rancher (optional) | Only for the [Rancher UI extension](/rancher-ui-extension): Rancher 2.15 or later, tested on 2.15.2. |

To build from source you additionally need Go 1.25+ (the `Dockerfile` builder is `golang:1.25`), `make`, and Docker.

:::note
Preflight does not verify that the NAD exists. The check is not yet implemented; only a non-empty `spec.networkRef` is enforced (`NetworkRefMissing`). A wrong NAD surfaces later, when the VM is created.
:::

## Baked-image bootstrap

The operator never imports VM images. It only reads existing Harvester `VirtualMachineImage` objects. During preflight for a never-provisioned instance the operator:

1. Looks up the OS stream configured by [`databaseDefaults.osVersion`](/configuration/operator-config) (default `22.04`) in the compiled-in catalog.
2. Requires the stream to be in state `Validated`.
3. Checks that the requested `engineVersion` is supported by that image revision.
4. Resolves the image by name with `ResolveVMImage`, in the namespace set by `infrastructure.harvester.imageNamespace` (default `default`) unless the name carries an explicit `namespace/name` prefix.

Catalog contents in this release:

| OS stream (`osVersion`) | Active image name | Engine versions | State |
| --- | --- | --- | --- |
| `22.04` | `ubuntu-2204-postgres-v20260515` | 15, 16, 17 (default 17) | Validated |
| `24.04` | `ubuntu-2404-postgres-v20260701` | 15, 16, 17, 18 (default 17) | Validated |

See [Images and repave](/operations/images-and-repave).

Upload the image in the namespace given by `imageNamespace`, and name it exactly as in the table; a misspelt name fails the same way a missing one does.

| Failure | Condition reason | Behaviour |
| --- | --- | --- |
| Image not found | `OSImageNotFound` | Terminal |
| Stream unknown or not validated, or reference ambiguous or invalid | `OSImageInvalid` | Terminal |
| Image present but still importing | `OSImageNotReady` | Retried every 10 seconds |

## Build and upload the baked image

If the image for your OS stream is not in Harvester yet, build it once and upload it by hand. The operator does not build, publish or import images.

### What a baked image is

An Ubuntu cloud image with PostgreSQL pre-installed, so a new database does not install packages at boot. The build:

- starts from the official Ubuntu cloud image for the stream and verifies it against Ubuntu's published checksums;
- applies the latest security updates;
- installs the PostgreSQL versions you ask for from the official PostgreSQL apt repository, plus the guest agent, the Prometheus PostgreSQL exporter and `jq`;
- leaves every PostgreSQL cluster disabled. The instance's first-boot script starts only the one for the requested `engineVersion`;
- disables unattended upgrades and background apt timers, so patches arrive through a new image and a [repave](/operations/images-and-repave), not through changes on running VMs;
- seals the image (cloud-init state and machine ID are cleared, the build SSH key is removed) so every VM created from it runs its first-boot setup.

The result is a compressed `qcow2` file.

### 1. Build the image

You need a Linux build host with KVM available and sudo rights. The build script installs Packer, QEMU and `yq` itself if they are missing. From `database/images/packer` in the repository:

```bash
./build.sh <build_date> "<postgres versions>" <os version>
```

Use the values from the [catalog table](#baked-image-bootstrap) so the image name matches what the operator expects:

```bash
# Ubuntu 22.04 stream, PostgreSQL 15, 16, 17
./build.sh 20260515 "15 16 17" "22.04"

# Ubuntu 24.04 stream, PostgreSQL 15, 16, 17, 18
./build.sh 20260701 "15 16 17 18" "24.04"
```

The script refuses to build an OS stream or PostgreSQL major that has passed its end-of-life date listed in `images.yaml`. The finished file is written to:

```text
output-ubuntu-<os>-postgres-v<build_date>/ubuntu-<os>-postgres-v<build_date>.qcow2
# for example
output-ubuntu-2204-postgres-v20260515/ubuntu-2204-postgres-v20260515.qcow2
```

The image **name** is `ubuntu-<os>-postgres-v<build_date>` with the dot removed from the OS version (`22.04` becomes `2204`). The operator only accepts the exact names in the catalog, so build with the catalog's date, or upload an already-built file under the catalog's name.

### 2. Upload it to Harvester

In the Harvester UI, go to **Images** and choose **Create**:

1. **Namespace:** the namespace set by `infrastructure.harvester.imageNamespace` (default `default`).
2. **Name:** exactly the catalog name, for example `ubuntu-2204-postgres-v20260515`. A misspelt name fails the same way a missing image does.
3. **Source:** upload the `.qcow2` file (or give a URL if you host the file somewhere Harvester can reach).
4. Create it and wait until the image shows as ready.

Optionally, add the labels `dbaas.opencloud.wso2.com/baked-image: "true"` and `dbaas.opencloud.wso2.com/os-version: "22.04"` (your OS stream) to the image when you create it, so it appears on the [Rancher UI extension's](/rancher-ui-extension/database-images) **Database Images** page; the operator itself doesn't need them.

`virtctl image-upload` is the command-line alternative; the name and namespace rules are the same.

:::tip[Upload fails around 700 MB]
If the UI upload times out with `timeout waiting for the datasource file processing begin`, a Rancher proxying load balancer is capping the upload size. Upload directly against Harvester instead of through Rancher.
:::

### 3. Check it

```bash
kubectl get virtualmachineimages.harvesterhci.io -n default
```

The image must be listed under the catalog name and be ready. Then create your `DBInstance`. If the image is missing or not ready, preflight reports it as shown in the failure table above: `OSImageNotFound` (terminal), or `OSImageNotReady` while it is still importing (retried every 10 seconds).

### Using a different PostgreSQL set or OS stream

Only the revisions compiled into the operator are usable (see [Images and repave](/operations/images-and-repave)). A differently named or differently built image is not picked up until a new operator release lists it. To add a new stream or PostgreSQL major to the build itself, add it to `images.yaml` first; the build script reads its end-of-life dates and Ubuntu image URLs from there.

