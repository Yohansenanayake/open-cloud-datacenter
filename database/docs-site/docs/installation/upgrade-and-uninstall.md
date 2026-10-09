---
title: Upgrade and uninstall
sidebar_position: 4
---

# Upgrade and uninstall

## Upgrade via the Addon

A change to `spec.version` makes Harvester run an in-place `helm upgrade --install`; no disable and re-enable is needed. CRDs and existing `DBInstance` objects are left in place.

1. Build and push the new manager image, and package and push the chart (see [Helm chart and Addon](/installation/helm-addon)). Keep the chart `version` and `appVersion` in step; an empty `manager.image.tag` follows `appVersion`.
2. Patch the Addon:

   ```sh
   kubectl patch addon dbaas-operator -n dbaas-system --type merge \
     -p '{"spec":{"version":"NEW_VERSION"}}'
   ```

3. Verify the Addon status, the running image and that existing instances stay healthy: `kubectl get dbinstance -A`.

During the rolling update two manager pods can briefly overlap. Leader election (`--operator.leaderElection.enabled=true`, a chart default) prevents both from reconciling.

Configuration is read only at startup, so any config or flag change restarts the manager pod.

## Upgrade via Helm

- Helm: rerun `helm upgrade --install` (or `make helm-deploy`). `make helm-rollback` reverts to the previous release.

## CRD caveats

- The chart ships the CRD as a template under `charts/chart/templates/crd/`. Helm does apply template changes on upgrade, unlike the `crds/` directory convention. The `crd.keep` value only adds `helm.sh/resource-policy: keep`, which protects against deletion, not upgrades.
- `crd.enable=false` omits the CRD from the chart entirely; you then own CRD installation.
- Some `DBInstance` spec fields are immutable after create (for example `vmPassword`; preflight reports `ImmutableFieldChanged`). The API is `v1alpha1`, so a future release may change the schema; review release notes before upgrading.
- Baked-image catalog entries are compiled into the binary, so a new operator version can change which image revision a stream resolves to. Already-running instances are not failed by a catalog change; drift is reported and repave is explicit. See [Images and repave](/operations/images-and-repave).

## Uninstall

Operator removal is not database deletion: VMs, disks and data of existing `DBInstance` objects are not touched.

### Addon

```sh
# 1. Disable: runs a real helm uninstall; reversible
kubectl patch addon dbaas-operator -n dbaas-system --type merge -p '{"spec":{"enabled":false}}'
# 2. Optionally remove the Addon object (allowed by the experimental label)
kubectl delete addon dbaas-operator -n dbaas-system
```

With `crd.keep: true` (the default) the `DBInstance`, `DBSnapshot` and `DBRestore` CRDs, and their objects, survive both steps.

### Helm

```sh
helm uninstall dbaas-operator --namespace dbaas-system    # or: make helm-uninstall
# only if you want all DBInstances, DBSnapshots and DBRestores gone (run it while the operator is still running)
kubectl delete crd dbinstances.dbaas.opencloud.wso2.com dbsnapshots.dbaas.opencloud.wso2.com dbrestores.dbaas.opencloud.wso2.com
```


:::caution
Deleting the CRD while the operator is already gone leaves `DBInstance` objects stuck on their finalizer, and the VMs they own are orphaned. Delete the `DBInstance` objects first.
:::
