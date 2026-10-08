# Installing DBaaS via Helm chart + Harvester Addon

> **Owner:** DBaaS operator maintainers · **Last updated:** 2026-09-28 · **Status:** experimental (repo-registered Addon)
> **Related:** [Discussion #303](https://github.com/wso2/open-cloud-datacenter/discussions/303) (release-process RFC — scope, GHCR namespace, versioning policy) · `database/charts/chart/` (chart source) · `.github/workflows/database-test-chart.yaml` (CI)
>
> Report installation issues in [Discussion #303](https://github.com/wso2/open-cloud-datacenter/discussions/303).

This guide covers installation with Helm and a Harvester `Addon`. The
[README Quickstart](./README.md) covers Kustomize. Choose one installation
method per deployment to avoid managing the same resources twice.

## Prerequisites

Beyond README.md's base prerequisites (Harvester cluster, NAD, `kubectl`):

1. **Go + kubebuilder** (matching `PROJECT`'s `cliVersion`) — to (re)generate the chart.
2. **Helm 3.8+** and **Docker**, authenticated against your target registry (`docker login ghcr.io ...`; Helm 4 reuses Docker's cached OCI credentials automatically).
3. Write access to whatever GHCR namespace you're publishing under.

## Testing with a custom registry

Use registry overrides for test deployments. Keep personal registry locations
out of committed chart defaults and Addon manifests:

- **Chart:** `helm upgrade --install dbaas-operator database/charts/chart --set manager.image.repository=ghcr.io/<you>/dbaas-operator --set manager.image.tag=<tag>`
- **Kustomize (`make deploy`):** `make deploy IMG=ghcr.io/<you>/dbaas-controller:<tag>` — mutates `config/manager/kustomization.yaml`; revert after: `git checkout -- config/manager/kustomization.yaml`.
- **Addon manifest:** copy `deploy/harvester-addon/dbaas-operator/dbaas-operator.yaml` to `dbaas-operator.local.yaml` (gitignored) and fill in your own values there. Never edit the committed one.

Record image digests and source commits when sharing test results.

---

## 1. Generate (or regenerate) the chart

```sh
cd database
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts
```

- Lands at `database/charts/chart/` (the plugin appends its own `chart/` under `--output-dir`).
- Generation resets the Kustomize image to `controller:latest` through `make build-installer`. Pass `IMG=` explicitly when building or deploying, and keep personal registry values out of the committed configuration.
- Only `Chart.yaml`, `values.yaml`, `NOTES.txt`, `_helpers.tpl`, `.helmignore`, and the test-chart workflow survive a re-run without `--force` — every other template regenerates from current `config/`, wiping any hand-fix (like step 2's RBAC fix) that isn't in this preserved list.
- The chart must include all three APIs (`DBInstance`, `DBSnapshot`, and `DBRestore`), their current schemas, and the generated manager permissions. After regeneration, run `helm lint charts/chart` and `go test ./test/chart/...` from `database/` to check that the rendered CRDs and RBAC match `config/`.

## 2. Fix the generated defaults (one-time per real change, not every regen)

- **`Chart.yaml`** is never auto-regenerated — set `name`/`description`/`version`/`appVersion` by hand. Keep the chart name consistent with `HELM_RELEASE` in `make helm-deploy`, or you get a double-barrelled `<release>-<chart>` resource prefix instead of a clean one.
- **`rbac.namespaced` toggle**: remove this generated toggle from `manager-role.yaml`, `manager-rolebinding.yaml`, and every `dbinstance-*-role.yaml`, `dbsnapshot-*-role.yaml`, and `dbrestore-*-role.yaml` helper. DBaaS reconciles these resources across every tenant namespace, so a namespaced `Role` would silently blind the manager outside its own install namespace. Hardcode `ClusterRole`/`ClusterRoleBinding` and drop `namespaced:` from `values.yaml`. Doesn't survive a bare regen — reapply after any template-affecting change. The helper roles remain opt-in through `rbac.helpers.enable`.
- **Image default**: point `values.yaml`'s `manager.image.repository`/`tag` at your real publish target — never the generic `controller`/`latest` placeholder, never a personal registry (see above).

## 3. Package the chart

```sh
helm package database/charts/chart --version <X.Y.Z>
# → dbaas-operator-<X.Y.Z>.tgz
```
Gitignored build output — never committed. For a real release it becomes a GitHub Release asset instead (see "Publishing a real release").

## 4. Build and push the controller image

```sh
cd database
docker build -t <registry>/dbaas-operator:<X.Y.Z> .
docker push <registry>/dbaas-operator:<X.Y.Z>
```
Use `docker build`/`push` directly, **not** `make docker-buildx` — its recipe lines are `-`-prefixed, so `make` silently ignores build/push failures instead of stopping on them.

If your build context is slow (Packer artifacts under `database/images/`, local binaries under `database/bin/` getting swept in), add a `database/.dockerignore`.

## 5. Push the chart as an OCI artifact

```sh
helm push dbaas-operator-<X.Y.Z>.tgz oci://<registry>/charts
```
No `helm repo add` needed — OCI charts are referenced directly by path + `--version`.

## 6. Make both GHCR packages public (or set a pull secret)

Both default to **Private**. For the Addon's install Job (runs with no credentials unless you configure `dockerRegistrySecret`) to pull anonymously: GitHub → profile → **Packages** → package → **Package settings** → **Danger Zone** → **Change visibility** → **Public**.

To keep them private instead, add `dockerRegistrySecret` (`kubernetes.io/dockerconfigjson`) to the `Addon` manifest (step 8) — that field exists on the underlying `HelmChart` CRD for this.

## 7. Baked-image bootstrap (one-time per Harvester cluster, independent of the operator install)

**The operator never imports VM images itself** (`ResolveVMImage` only reads existing `VirtualMachineImage`s). Before any `DBInstance` can provision, the relevant baked image(s) (`database/internal/catalog/baked_images.go`) must already exist and be `Active` in the `default` namespace.

- Upload via Harvester's UI or `virtctl image-upload`; **Name** must exactly match the catalog's `ImageName` — a typo fails silently the same way a missing image does.
- Hit `timeout waiting for the datasource file processing begin`? A Rancher-proxying load balancer's upload size cap (hit at ~700MB) — upload directly against Harvester instead.
- **Not automated**: no publish location or `sourceType: download` generator exists yet — open item, tracked in Discussion #303.

## 8. Apply the Harvester Addon manifest

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: dbaas-system
---
apiVersion: harvesterhci.io/v1beta1
kind: Addon
metadata:
  name: dbaas-operator
  namespace: dbaas-system
  labels:
    addon.harvesterhci.io/experimental: "true"
spec:
  enabled: true
  repo: ""   # required key, but must stay empty for an oci:// chart — see below
  chart: oci://<registry>/charts/dbaas-operator
  version: <X.Y.Z>
  valuesContent: |-
    {}
```
`repo` is schema-required but `helm repo add` (which the install job only runs when `repo` is non-empty) doesn't understand `oci://` — an explicit empty string satisfies the schema without breaking the OCI pull, which happens entirely through `chart`.

```sh
kubectl apply -f dbaas-operator-addon.yaml
```

## 9. Verify the install

```sh
kubectl get addon dbaas-operator -n dbaas-system -o jsonpath='{.status.status}{"\n"}'   # → AddonDeploySuccessful
kubectl get jobs -n dbaas-system                                                         # install Job (helm-delete-* on uninstall)
kubectl get pods -n dbaas-system                                                         # manager pod
kubectl get deployment dbaas-operator-controller-manager -n dbaas-system \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'                          # confirm the pushed image is running
```

## 10. Create a test `DBInstance`

Requires step 7 done, and a real `NetworkAttachmentDefinition` (`kubectl get network-attachment-definitions -A`):

```yaml
apiVersion: dbaas.opencloud.wso2.com/v1alpha1
kind: DBInstance
metadata:
  name: dbaas-test-01
  namespace: default
spec:
  dbInstanceClass: db.t3.micro
  allocatedStorage: 5
  dbName: testdb
  masterUsername: dbadmin
  networkRef: <namespace>/<nad-name>
  running: true
```
```sh
kubectl apply -f dbinstance-test.yaml
kubectl get dbinstance dbaas-test-01 -n default -w
```
Not `Available`? `kubectl describe dbinstance dbaas-test-01 -n default` — the condition maps to a specific `internal/ensure/preflight.go` check (usually `OSImageNotFound`/`OSImageInvalid` from step 7, not a manifest problem).

**Definition of done:** `Addon` status `AddonDeploySuccessful` **+** manager pod `Running` with the expected image **+** a test `DBInstance` reaching `Available`. All three — a healthy `Addon` status alone doesn't prove a database can provision.

## Upgrading an already-installed Addon

A version bump does **not** need disable/re-enable — Harvester's controller reacts to a `spec.version` change with a real `helm upgrade --install` in place, leaving CRDs and existing `DBInstance`s untouched.

1. Build + push the new image (`ghcr.io/wso2/dbaas-operator:<new-version>`).
2. Bump `Chart.yaml`'s `version`/`appVersion` together (chart's `values.yaml` tag `""` already tracks `appVersion`, no separate edit needed).
3. Package + push the new chart version (steps 3, 5).
4. `kubectl patch addon dbaas-operator -n dbaas-system --type merge -p '{"spec":{"version":"<new-version>"}}'`
5. Verify as in step 9, plus `kubectl get dbinstance -A` to confirm existing instances stayed `Available`.

During the rolling update, old and new manager pods may briefly overlap — this is exactly what leader election (`--operator.leaderElection.enabled=true` in `manager.args`, `values.yaml`) guards against.

## Uninstalling / rolling back

```sh
# 1. Disable first — real helm-uninstall, reversible
kubectl patch addon dbaas-operator -n dbaas-system --type merge -p '{"spec":{"enabled":false}}'
# 2. Only if you want the Addon object gone too (allowed — experimental label)
kubectl delete addon dbaas-operator -n dbaas-system
```
`values.yaml`'s default `crd.keep: true` (`helm.sh/resource-policy: keep`) retains the `DBInstance`, `DBSnapshot`, and `DBRestore` CRDs and their resources when uninstalling. It has no effect on normal `helm upgrade` schema changes, which still apply in place. Keep this setting enabled to preserve existing instances, snapshots, and restores.

## Publishing a real release

Once WSO2 registry access exists:

1. Bump `Chart.yaml`'s `version`/`appVersion` and the image tag together (`0.1.x` lockstep policy, Discussion #303 §"Versioning").
2. `git tag database/vX.Y.Z && git push origin database/vX.Y.Z`
3. Run steps 3–5 against `ghcr.io/wso2/...`.
4. Create a GitHub Release for that tag; attach the `.tgz` and (if generated) `dist/install.yaml` as **release assets** — `gh release upload database/vX.Y.Z dbaas-operator-<X.Y.Z>.tgz dist/install.yaml`. Neither is ever committed (same pattern cert-manager uses for `cert-manager.yaml`).
5. Reference the exact digests in the release notes, as Discussion #303's "Reproducibility record" does.

## Known gotchas

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spec.repo: Required value` | Harvester's CRD schema requires the `repo` key present | Set `repo: ""` explicitly (step 8) |
| `DBInstance` phase `incompatible-parameters` immediately | `Accepted=False` from `preflight.go` — check `.status.conditions` | Usually `OSImageNotFound`/`OSImageInvalid` (step 7), not a manifest problem |
| UI image upload times out at 60s with a Longhorn datasource error | Rancher's proxying load balancer has an upload size cap (~700MB) | Upload directly against Harvester, bypassing the proxy |
