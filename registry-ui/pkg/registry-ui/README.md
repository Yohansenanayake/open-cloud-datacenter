# Registries

Private container registries for Harvester, from Rancher. A team requests a registry in its namespace;
the registry operator (`registry.opencloud.wso2.com`) creates its Harbor project, storage quota and
credentials.

## Features

- **Registries** entry in the left navigation, listing the Harvester clusters that have the registry
  operator installed.
- Overview per cluster: registry totals, registries that are not Ready and why, quota committed per
  plan, and the operator's health (pod, Harbor URL, Harvester Addon state).
- Registries: list, create (name, namespace, plan), edit the plan, view and delete.
- Each registry's Harbor project, URL, quota and credential Secret names, plus ready-to-copy
  commands to log in, push, and use the pull credential on another cluster.

The extension reads and writes only `Registry` resources and reads the operator's pod, Deployment
and Addon. It never reads Secrets.

## Requirements

- Rancher 2.15 or later.
- A Harvester cluster with the registry operator 0.1.0 or later (API
  `registry.opencloud.wso2.com/v1alpha1`) installed. Clusters without the operator do not appear in
  the list.

## Development

Node 24 and Yarn 1. From `registry-ui/`:

```sh
yarn install --frozen-lockfile
yarn lint
yarn test
API=https://<rancher-host> yarn dev          # https://127.0.0.1:8005
yarn build-pkg registry-ui && yarn serve-pkgs   # then Extensions → Developer load
```

Developer load URL: `http://127.0.0.1:4500/registry-ui-0.1.0/registry-ui-0.1.0.umd.min.js`.

## Release

Bump `version` in `registry-ui/package.json` and `pkg/registry-ui/package.json`, merge, then push
the tag `registry-ui/v<version>`. The workflow publishes an Extension Catalog Image,
`ghcr.io/<owner>/ui-extension-registry-ui:<version>`; import it in Rancher under **Extensions → ⋮ →
Manage Extension Catalogs**, then install **Registries** from the **Available** tab.
