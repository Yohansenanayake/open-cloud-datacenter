# Installing the registry operator

> **Version:** 0.1.0 (preview) · **Release notes:** [CHANGELOG.md](CHANGELOG.md) · **Chart source:** `charts/chart/`

This is the install path for a Harvester administrator: the published Helm chart, applied through a Harvester `Addon`. [`README.md`](./README.md)'s Quickstart covers the kustomize path for development; the two are independent, don't co-install both over the same resources.

| Artifact | Reference |
| --- | --- |
| Image | `ghcr.io/wso2/registry-operator:<version>` |
| Chart | `oci://ghcr.io/wso2/charts/registry-operator`, version `<version>` |
| Addon example | [`deploy/harvester-addon/registry-operator/`](deploy/harvester-addon/registry-operator/) |

## What runs where

| Component | Cluster | Namespace |
| --- | --- | --- |
| Harbor | Its own cluster (e.g. a VM on Harvester), deployed and operated separately | — |
| Operator, `Registry` CRD, RBAC | Harvester, installed by the Addon | `registry-system` |
| `Registry` objects and their `<name>-pull` / `<name>-push` Secrets | Harvester | one namespace per team |
| Copies of the pull Secret | Every cluster that runs the images | wherever the workloads are |

The operator does not deploy Harbor. It needs Harbor's API reachable from its pod, a Harbor account, and trust in Harbor's certificate — the prerequisites below.

## Prerequisites

1. **A running Harbor** reachable over HTTPS at one hostname that every client resolves — the operator, developers, CI, and the nodes of every cluster that pulls. That hostname is `harbor.url`, and Harbor's own `externalURL` must equal it. The operator writes it into every credential Secret, so a second name for the same Harbor breaks pulls with 401.
2. **A certificate for that hostname.** A publicly-rooted one needs nothing further. One from a private CA must also be trusted by the operator (step 1) and by the container runtime on **every node of every consumer cluster** and every developer machine — the operator cannot do that part.
3. **A Harbor account for the operator** with system-administrator rights. It creates projects, quotas and robot accounts in projects it does not own.
4. **A network path** from a Harvester pod to Harbor's API. Where there is none, a Multus attachment with a few reserved addresses (step 1).
5. **`kubectl` access to Harvester** as a cluster administrator.

---

## 1. Prepare `registry-system` on Harvester

The `Addon` lives in this namespace, and Harvester installs the chart into the `Addon`'s own namespace, so it must exist first:

```sh
kubectl create namespace registry-system --dry-run=client -o yaml | kubectl apply -f -
```

The operator's Harbor login and Harbor's CA go in the `Addon`'s values (step 2); the chart creates the Secrets from them. Harbor's CA certificate is public data. If it was issued inside the Harbor cluster by cert-manager, export it from there:

```sh
kubectl --kubeconfig <harbor-cluster-kubeconfig> -n harbor get secret <harbor-tls-secret> \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > harbor-ca.crt
```

**Keeping the password out of the `Addon`.** Values are stored in plain text in the `Addon`, its HelmChart and the Helm release. Where the `Addon` manifest is kept in Git, create the Secret yourself instead and leave `harbor.credentials` empty:

```sh
kubectl -n registry-system create secret generic harbor-credentials \
  --from-literal=username=<harbor-user> --from-literal=password=<password>
```

The same applies to a CA held in an existing Secret: set `harbor.caSecret.name` and leave `harbor.caBundle` empty. A Secret the chart did not create cannot later be taken over by it: switching from one form to the other means deleting the Secret first.

### Reaching Harbor from Harvester

Skip this when a Harvester pod can already reach Harbor:

```sh
kubectl run netcheck -n registry-system --rm -i --restart=Never --image=curlimages/curl:8.11.1 \
  --command -- curl -sS -m 8 -o /dev/null -w '%{http_code}\n' https://<harbor-host>/api/v2.0/ping
```

An HTTP code means there is a route. With a private CA the check prints `000` and `SSL certificate problem` instead: that also means there is a route, since this pod lacks the CA the operator gets from the chart. A timeout means there is not: Harvester carries VM VLANs as bridges and gives its own hosts no address on them. Give the operator a second interface on Harbor's VLAN with [`harbor-network.yaml`](deploy/harvester-addon/registry-operator/harbor-network.yaml):

1. Reserve one or more addresses on Harbor's subnet and have them excluded from DHCP.
2. Fill in the placeholders and `kubectl apply -f` it on Harvester.
3. Repeat the check above with `--overrides='{"metadata":{"annotations":{"k8s.v1.cni.cncf.io/networks":"registry-system/<attachment>"}}}'`. Expect an HTTP code, and `ip route` showing the default route still on `eth0`.

Never add `gateway` to the attachment's IPAM: the second interface would take the default route and cut the operator off from the Kubernetes API.

With **one** reserved address, keep `replicas: 1` and `strategy: Recreate` (the example Addon sets both). A rolling update starts the new pod before stopping the old one; the new pod can never get the address and the upgrade stalls.

## 2. Apply the Harvester Addon

Copy [`registry-operator.yaml`](deploy/harvester-addon/registry-operator/registry-operator.yaml) to `registry-operator.local.yaml` (gitignored) and fill it in:

| Field | Value |
| --- | --- |
| `version` | the release to install, e.g. `0.1.0` |
| `harbor.url` | `https://<harbor-host>` — the one hostname from the prerequisites |
| `harbor.credentials` | the operator's Harbor user and password, or remove the block when the Secret was created by hand |
| `harbor.caBundle` | the full contents of `harbor-ca.crt`, or remove it for a publicly-rooted certificate |
| `harbor.networkAttachment` | `registry-system/<attachment>`, or remove it when there is a route |

Never commit the filled-in file: it holds the password.

```sh
kubectl apply -f registry-operator.local.yaml
```

`repo: ""` must stay: Harvester's schema requires the key, and a non-empty value makes the install job run `helm repo add`, which does not understand `oci://`.

### Without Harvester

The chart installs on any Kubernetes cluster with Helm 3.8 or later:

```sh
helm install registry-operator oci://ghcr.io/wso2/charts/registry-operator --version <version> \
  -n registry-system --create-namespace \
  --set harbor.url=https://<harbor-host> \
  --set harbor.credentials.username=<harbor-user> --set harbor.credentials.password=<password> \
  --set-file harbor.caBundle=harbor-ca.crt
```

### From your own registry

Where the cluster cannot reach `ghcr.io`, copy the image and the chart into a registry it can reach, then point the `Addon`'s `chart` at your copy and set `manager.image.repository` (and `manager.image.tag`) in its values. For a private registry, add `dockerRegistrySecret` to the `Addon` and `manager.imagePullSecrets` to its values.

## 3. Verify the install

```sh
kubectl -n registry-system get addon registry-operator -o jsonpath='{.status.status}{"\n"}'   # AddonDeploySuccessful
kubectl -n registry-system rollout status deploy/registry-operator-controller-manager --timeout=180s
kubectl -n registry-system logs deploy/registry-operator-controller-manager | grep 'starting registry operator'
                                                     # "version":"<version>", "harbor":"https://<harbor-host>"
```

**Ready is the Harbor check.** The pod turns Ready only once Harbor answers and accepts the operator's credentials, so a NotReady pod is a Harbor problem, not a crash:

| Symptom | Cause |
| --- | --- |
| Install fails: `harbor.url is required` or a schema error | `harbor.url` missing, or not `http(s)://<host>` |
| `ContainerCreating` for long | `harbor-ca` Secret or the network attachment missing — `kubectl describe pod` names which |
| Whereabouts could not allocate | The reserved address is still held by an old pod, or leaked after an unclean node failure |
| NotReady, log shows `x509: certificate signed by unknown authority` | `harbor.caBundle` (or the Secret in `harbor.caSecret`) holds the wrong CA, or neither is set |
| NotReady, log shows a timeout | No route to Harbor — see step 1 |
| NotReady, log shows `401` | `harbor.credentials` (or the `harbor-credentials` Secret) does not match the Harbor account |
| `CrashLoopBackOff`, log says `HARBOR_URL … uses http` | `harbor.url` must be https |

The image is distroless, so there is no shell to `exec` into. Multus records the addresses it attached on the pod:

```sh
kubectl -n registry-system get pod -l control-plane=controller-manager \
  -o jsonpath='{.items[0].metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
```

## 4. Create a test `Registry`

```sh
kubectl create namespace team-a
kubectl -n team-a apply -f - <<'YAML'
apiVersion: registry.opencloud.wso2.com/v1alpha1
kind: Registry
metadata: {name: web}
spec: {plan: starter}
YAML
kubectl -n team-a get registry web -w                  # PHASE Ready
kubectl -n team-a get secret web-pull web-push         # both kubernetes.io/dockerconfigjson
```

**Definition of done:** `Addon` status `AddonDeploySuccessful` **+** the operator pod Ready with the expected version in its log **+** a `Registry` reaching `Ready` **+** an image pushed with `web-push` and pulled by a pod on another cluster with a copy of `web-pull`. A healthy Addon alone proves nothing about Harbor.

Copying `web-pull` to another cluster means stripping `ownerReferences`, `uid`, `resourceVersion`, `creationTimestamp` and `namespace` — see README, "Using it". For a UI, install the Rancher extension in `registry-ui/`.

## Upgrading

A version bump needs no disable/re-enable: Harvester runs `helm upgrade --install` when `spec.version` changes. The CRD, every `Registry`, every Harbor project and every credential are untouched — credentials are minted only when their Secret is absent.

1. Read the new version's section in [CHANGELOG.md](CHANGELOG.md) for breaking changes.
2. `kubectl -n registry-system patch addon registry-operator --type merge -p '{"spec":{"version":"<new-version>"}}'`
3. Verify as in step 3, plus `kubectl get registries -A` to confirm every `Registry` stayed `Ready`.

## Uninstalling

```sh
# Disable — a real helm uninstall, reversible by setting enabled back to true
kubectl -n registry-system patch addon registry-operator --type merge -p '{"spec":{"enabled":false}}'
# Only if the Addon object should go too
kubectl -n registry-system delete addon registry-operator
```

The `Registry` CRD carries `helm.sh/resource-policy: keep`, so neither step deletes it, any `Registry`, or any Harbor project. Re-enabling adopts them all without recreating anything.

Reinstall under the **same release name and namespace** (`registry-operator` in `registry-system`; the Addon always uses these). The kept CRD still carries that release's ownership annotations, and Helm refuses to adopt it into a release with a different name.

While the operator is uninstalled, deleting a `Registry` hangs in `Terminating`: its finalizer has nobody to run it. Re-enable the Addon and the deletion completes.

Removing everything, including every image, is a separate deliberate act — README, "Removing everything, deliberately". `helm.sh/resource-policy: keep` means the CRD must then be deleted by hand: `kubectl delete crd registries.registry.opencloud.wso2.com`, **only after** `kubectl get registries -A` is empty.

## Compatibility

Combinations tested together. Add a row for every release.

| Operator | Harbor (chart) | Kubernetes | Where |
| --- | --- | --- | --- |
| 0.1.0 | 2.15.2 (1.19.2) | 1.36 (kind) | Helm install, Registry to Ready, push/pull, uninstall/reinstall |
| 0.1.0 | 2.15.2 (1.19.2) | Harvester v1.9.0 (1.36); pulls from RKE2 v1.36 | Addon install over a Multus attachment, Registry to Ready, push/pull/delete, credential revoke, plan change, Harbor outage recovery, uninstall keeps data |

## Known gotchas

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spec.repo: Required value` | Harvester's Addon schema requires the key | Set `repo: ""` explicitly |
| Upgrade stalls with the new pod in `ContainerCreating` | A one-address whereabouts pool and a rolling update | `manager.strategy.type: Recreate` |
| Pulls fail `x509: certificate signed by unknown authority` on a consumer cluster | That cluster's nodes do not trust a private CA | Distribute the CA to every node's container runtime, or use a publicly-rooted certificate |
| Pulls fail with 401 although the Secret was copied | The image host differs from `harbor.url`'s host, so the credential is never sent | Use exactly the host in `.status.registryURL` in image references |
| A leaked credential must stop working | — | Delete that Secret (`<name>-pull` or `<name>-push`): the operator deletes its robot in Harbor and issues a new one. Every copy of the old Secret stops working |
| `Secret … exists and cannot be imported into the current release` | A Secret of that name was created by hand, and `harbor.credentials` or `harbor.caBundle` now asks the chart to create it | Delete the hand-made Secret, or remove the inline value |
| Addon stays `AddonUpdating` and later edits to `spec` have no effect | An install Job failed (e.g. invalid `valuesContent`); Harvester stops passing changes to the HelmChart | Fix the values, then disable and re-enable the Addon. Existing `Registry` objects and their Secrets are untouched |
| Every `Registry` goes `Provisioning` with `no route to host`, operator `0/1` | Harbor's address is down. On a guest cluster this can be its load-balancer VIP: kube-vip drops the VIP when it loses its lease and may not re-add it | Restore Harbor's address (on the guest cluster, restart the kube-vip pod). The operator retries and every `Registry` returns to `Ready` with the same project and credentials |

---

## Building and publishing (maintainers)

Needs **Helm 3.8+** and **Docker**, authenticated against the registry you publish to (`docker login ghcr.io`; Helm reuses Docker's OCI credentials). To test a build before release, push it to a registry of your own and install it with the overrides in "From your own registry" — never by editing the committed defaults.

### Regenerating the chart

The chart was generated with kubebuilder's helm plugin and then corrected by hand. Regenerate only when `config/` changes:

```sh
cd registry
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts
```

A re-run without `--force` keeps `Chart.yaml`, `values.yaml`, `NOTES.txt`, `_helpers.tpl` and `.helmignore`, and rewrites every other template from `config/`. After a regeneration, restore these hand corrections — review `git diff charts/` before committing:

| File | Correction | Why |
| --- | --- | --- |
| every template | `app.kubernetes.io/name: {{ include "registry.name" . }}` | The generator mis-derives this from the `regi-` name prefix and emits `{{ include "registry.name" . }}stry` |
| `rbac/manager-role.yaml`, `rbac/manager-rolebinding.yaml` | `ClusterRole` / `ClusterRoleBinding` only; no `rbac.namespaced` | A namespaced Role leaves the operator blind outside its own namespace |
| `rbac/registry-editor-role.yaml`, `rbac/registry-viewer-role.yaml` | gated on `rbac.tenantRoles.enable` (default `true`) | They carry the aggregation labels without which only cluster administrators can create a `Registry` |
| `manager/manager.yaml` | env, CA volume and Multus annotation rendered from `harbor.*` | The operator's configuration is first-class values, validated by `values.schema.json` |
| `manager/harbor-secrets.yaml`, `registry.harborCASecretName` in `_helpers.tpl` | added by hand | Secrets for `harbor.credentials` and `harbor.caBundle`; the generator has no equivalent |
| `crd/registries.registry.opencloud.wso2.com.yaml` | added by hand from `config/crd/bases/` | `config/default` deliberately excludes the CRD, so the generator never sees it |
| `.github/` | delete | The generator scaffolds a chart-test workflow into `registry/.github/`, where GitHub never runs it |

The plugin also rewrites `config/manager/kustomization.yaml`'s image; restore it with `git checkout -- config/manager/kustomization.yaml`.

### Build and push the image

```sh
cd registry
VERSION=<X.Y.Z>
docker build --build-arg VERSION=$VERSION -t <registry>/registry-operator:$VERSION .
docker push <registry>/registry-operator:$VERSION
```

`VERSION` is compiled into the binary and printed in the operator's first log line, so a running pod says which release it is. Keep it equal to the image tag and to `Chart.yaml`'s `appVersion`.

### Package and push the chart

```sh
helm package charts/chart --version <X.Y.Z> --app-version <X.Y.Z>    # still in registry/
# → registry-operator-<X.Y.Z>.tgz   (gitignored; a release asset, never committed)
helm push registry-operator-<X.Y.Z>.tgz oci://<registry>/charts
```

GHCR packages default to **Private**, and the Addon's install Job and the kubelet pull anonymously. Make both packages public after their first push: GitHub → **Packages** → package → **Package settings** → **Change visibility**.

### Publishing a release

1. Bump `Chart.yaml`'s `version`/`appVersion` and the image tag together — they move in lockstep — and add the version's section to [CHANGELOG.md](CHANGELOG.md).
2. `git tag registry/vX.Y.Z && git push origin registry/vX.Y.Z`
3. Build, package and push as above against `ghcr.io/wso2`, with `VERSION=X.Y.Z`.
4. Create a GitHub Release for the tag, with the version's CHANGELOG section as its text, and attach the chart archive as a release asset: `gh release upload registry/vX.Y.Z registry-operator-X.Y.Z.tgz`.
5. Record the image and chart digests in the release text. For a digest-pinned install, set `manager.image.repository` to `ghcr.io/wso2/registry-operator@sha256:<digest>`.
6. Add the release's row to "Compatibility" above.
