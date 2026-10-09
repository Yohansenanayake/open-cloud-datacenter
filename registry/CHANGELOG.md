# Changelog

Notable changes to the registry operator, newest first. Versions follow [Semantic Versioning](https://semver.org);
each release is tagged `registry/vX.Y.Z`. While the API is `v1alpha1`, a minor release may change it.

## 0.1.0 — preview

First release. A `Registry` in any namespace becomes a project, a storage quota and two credentials in
one shared Harbor, which the operator does not deploy.

**Install:** the Helm chart `oci://ghcr.io/wso2/charts/registry-operator`, version `0.1.0`, or the
Harvester Addon in [`deploy/harvester-addon/`](deploy/harvester-addon/). See [INSTALL.md](INSTALL.md).
**UI:** the Rancher extension `registry-ui/`, released separately under `registry-ui/vX.Y.Z` tags.

### Added

- One `Registry` → one Harbor project named `<name>-<8 hex of the Registry's UID>`, with the plan's
  storage quota: `starter` 5 GiB (default), `professional` 20 GiB, `enterprise` 100 GiB.
- Two `kubernetes.io/dockerconfigjson` Secrets beside it: `<name>-pull` (pull only) and `<name>-push`
  (pull, push, tag, delete — for build pipelines). Usable directly in `imagePullSecrets` and by
  `docker login`.
- Users with the built-in `edit` role in a namespace can manage Registries there; `view` can read them.
- Deleting a `Registry` empties and deletes its Harbor project.
- Deleting a credential Secret revokes its robot in Harbor and issues a new one — the response to a
  leaked credential.
- The operator pod is Ready only while Harbor answers and accepts its credentials.
- Helm chart with `harbor.*` values validated by a JSON schema, inline or pre-created Harbor
  credentials and CA, and an optional Multus attachment for reaching Harbor on another network.
- Harvester Addon example and a network attachment example.
- Uninstalling the operator keeps the CRD, every `Registry` and every Harbor project.

### Breaking changes from pre-release builds

- **`.status.credentialsSecretName` is replaced by `.status.pullSecretName` and
  `.status.pushSecretName`.** Anything reading the old field must read the new ones.
- **The single Opaque credentials Secret is gone.** Its replacements are the two `dockerconfigjson`
  Secrets above.
- **`RegistryBackend` no longer exists**, and the operator no longer deploys Harbor. `BASE_DOMAIN`,
  `STORAGE_CLASS`, `INGRESS_CLASS`, `CERT_ISSUER`, `HARBOR_HELM_REPO` and `HARBOR_CHART_VERSION` are no
  longer read.
- **`HARBOR_URL` must be https.** Plain http requires `HARBOR_ALLOW_PLAINTEXT_URL=true` (chart:
  `harbor.allowPlaintextURL`), and the operator warns on every start while it is set.

### Tested with

| Harbor (chart) | Kubernetes | Covered |
| --- | --- | --- |
| 2.15.2 (1.19.2) | 1.36 (kind) | Helm install, Registry to Ready, tenant RBAC, push/pull, pull refused a push, uninstall/reinstall with nothing recreated |
| 2.15.2 (1.19.2) | Harvester v1.9.0 (1.36); pulls from RKE2 v1.36 | Addon install over a Multus attachment, push/pull/delete, credential revoke, plan change, Harbor outage recovery, uninstall keeps data |

### Known limitations

- **The operator authenticates to Harbor as a system administrator** (#300). Keep its credentials
  Secret readable only by cluster administrators.
- **Credentials do not expire.** Revoke one by deleting its Secret; every copy on other clusters stops
  working at once.
- **Changing `harbor.url` does not rewrite existing Secrets** — they keep the old host. Treat the
  Harbor hostname as permanent.
- **A private CA must be trusted by every consumer node**; the operator cannot distribute it.
- **CI covers install, a `Registry` reaching Ready, and delete clean-up** against a real Harbor on kind.
  Push/pull, credential revoke and plan change were verified manually, as above.
