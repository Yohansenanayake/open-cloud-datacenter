# registry

A Kubernetes operator that hands a team its own project in a shared container
registry.

The platform runs **one** Harbor, deployed and operated outside this operator.
Applying a `Registry` creates a project inside it, sets that project's storage
quota, mints two robot accounts, and writes their credentials into Secrets beside
the `Registry`. The operator installs nothing and manages no storage.

Runs on any conformant Kubernetes cluster — it uses no vendor APIs.

| To | Read |
|:---|:---|
| Install a release (Helm chart, Harvester Addon) | [INSTALL.md](./INSTALL.md) |
| See what changed in each release | [CHANGELOG.md](./CHANGELOG.md) |
| Manage registries from Rancher | the Rancher UI extension in `registry-ui/` |
| Understand and develop the operator | this README |

## Requirements

| Requirement | Notes |
|:---|:---|
| A running Harbor | Reachable from the operator over HTTPS, addressed by `HARBOR_URL`. Plain HTTP is refused unless `HARBOR_ALLOW_PLAINTEXT_URL=true` |
| A Harbor account | Stored in a Secret in the operator's namespace; needs to create projects, quotas and robot accounts |
| Trust in Harbor's certificate | A publicly-rooted certificate needs nothing. A private CA must be mounted into the operator pod — the client verifies, and has no skip-verification option |
| A route to Harbor | Where the operator and Harbor sit on different networks, see `config/local/manager_local_patch.yaml.example` |

## Using it

```yaml
apiVersion: registry.opencloud.wso2.com/v1alpha1
kind: Registry
metadata:
  name: web
spec:
  plan: starter
```

```sh
kubectl apply -n acme-project-1 -f registry.yaml
kubectl get registries -n acme-project-1 -w
```

Once `Ready`, two Secrets exist in the same namespace, both
`kubernetes.io/dockerconfigjson`:

| Status field | Grants | Use it for |
|:---|:---|:---|
| `.status.pullSecretName` | pull only | `imagePullSecrets` on the clusters that run these images |
| `.status.pushSecretName` | pull, push, tag, delete | a build pipeline |

They are separate Harbor accounts. A pull credential is copied onto every cluster
that runs the images and ends up in many hands, so it must not be able to
overwrite what it reads. The push credential can also delete images, so a
pipeline can clean up after itself; treat it as able to destroy everything in
the project.

Copying the pull Secret to another cluster means stripping the fields that tie it
to this one — `ownerReferences`, `uid`, `resourceVersion`, `creationTimestamp`
and `namespace`. A copied `ownerReferences` is the one that bites: the other
cluster's garbage collector looks for an owning `Registry`, does not find one,
and deletes the Secret.

### Project names identify one Registry

A `Registry` becomes a Harbor project named after it, with a short digest of its
UID: `web` in namespace `acme-project-1` becomes something like `web-30cf39a6`.
`.status.harborProject` reports the name, and the push and pull Secrets already
carry the full path, so nothing has to be typed out by hand.

The suffix is what makes the name belong to one object:

- **No two `Registry` objects can want the same project.** Names are not global
  and not first-come, first-served — a team is never blocked by a name another
  namespace took first.
- **Nothing outside the cluster can predict the name.** A project under it was
  created for this `Registry`, so the operator never has to ask who owns a
  project, mark one, or work out what an interrupted creation left behind.
  Creating the project is simply idempotent: `409 Conflict` means it is already
  there.

Deleting is authorised by Harbor's project **id**, not by the name. The id is
recorded in `.status.harborProjectID` as soon as Harbor reports it, and Harbor
never reuses one, so a project that carries this name with a different id was
created by somebody else after this Registry's was gone. The finalizer leaves
that project alone and raises an `Orphaned` warning event; a reconcile that finds
it refuses to converge quota or credentials into it. The name is unpredictable,
not secret — it appears in status, in the Secrets and in every image path — so
the id is what proves identity.

The trade-off is deliberate: a `Registry` that is **deleted and recreated** under
the same name is a new object with a new UID, so it gets a new, empty project.
Deleting a `Registry` already destroys its images, so nothing survives that the
old name would have addressed — but a pipeline holding the old path has to read
the new one from `.status.harborProject`.

Two assumptions hold this up, and both are worth stating plainly. The digest is
four bytes, so it separates `Registry` objects that share a name; two sharing a
name *and* a namespace cannot exist. And anyone who can both read a `Registry`
object and create projects in Harbor could create that project first — which
means holding the operator's own Harbor credentials, at which point they can
reach every project anyway.

## API

Group `registry.opencloud.wso2.com/v1alpha1`.

| Kind | Scope | Created by | Purpose |
|:---|:---|:---|:---|
| `Registry` | Namespaced | users | One Harbor project plus its credentials. `plan` sets the project's storage quota |

`plan` is the only sizing concept: it sizes a project's quota. There is nothing
to size about the Harbor deployment, because the operator does not own one.

## Configuration

Set on the manager Deployment (`config/manager/manager.yaml`), or layer
`config/local` over it — see `config/local/manager_local_patch.yaml.example` —
to keep cluster-specific values out of the tracked defaults.

| Variable | Required | Default | Purpose |
|:---|:---:|:---|:---|
| `HARBOR_URL` | ✅ | — | Base URL of the central Harbor. Validated at startup. Also what a `Registry` reports as its push/pull address, so it must be the name clients resolve |
| `POD_NAMESPACE` | ✅ | — | The operator's own namespace, supplied by the downward API. Where the credentials Secret is read from |
| `HARBOR_CREDENTIALS_SECRET` | | `harbor-credentials` | Secret holding `username` and `password` |
| `HARBOR_ALLOW_PLAINTEXT_URL` | | `false` | Permits an `http://` `HARBOR_URL`. The Harbor password travels as Basic Auth on every request, so this sends it in clear — for a throwaway cluster only |
| `METRICS_CERT_DIR` | | — | Serving certificate for the metrics endpoint. Empty means a self-signed one only an unverifying scraper can read |

The credentials Secret is read on every reconcile, so rotating it takes effect
without restarting the operator.

## How it works

A controller-runtime operator with a single reconciler. The custom resource is
the source of truth, all work happens inside the reconcile loop, and the loop is
level-triggered: every pass re-asserts the desired state and does nothing when it
already holds. Leader election is on, so extra replicas act as hot standbys.

**Reconcile** derives the project name, refuses it if Harbor cannot hold it,
creates the project, converges the quota, mints each robot account exactly once,
and writes the Secrets. The quota is re-applied every pass, which is both how a
plan change takes effect and how drift is corrected.

**Credentials are minted once.** The Secret's existence is what makes it
once-only: re-minting would invalidate every copy already distributed. A robot
left behind by an attempt that died before its Secret was written is unusable —
its token was never stored — so it is replaced rather than failed against.

A Secret already at that name which the `Registry` does not own is refused, not
taken over: the `Registry` reports the collision and stops. Nothing the operator
did not create is ever written to or deleted.

Because credentials are minted once, they carry the address `HARBOR_URL` had at
the time. Changing that address is a platform migration rather than something a
reconcile absorbs — every pull Secret copied onto another cluster, every CI
login and every image reference changes with it. To re-issue credentials for the
new address, delete the two Secrets; the operator mints them again on its next
pass, and every copy of the old ones has to be replaced.

**Readiness is the Harbor check.** The pod turns Ready only once Harbor answers
an authenticated call with the configured credentials, so an unusable Harbor
shows up as a pod that is not Ready rather than only in logs. It is deliberately
not a liveness check: restarting the operator would not fix a Harbor that is down.

**Deletion destroys data.** Deleting a `Registry` removes its Harbor project and
every image in it, emptying the repositories first because Harbor refuses to
delete a non-empty project. There is no retain option. The finalizer is held
until Harbor confirms the project is gone, so an unreachable Harbor is never
mistaken for a completed deletion.

Deletion also invalidates every **copy** of the credentials made onto another
cluster. Those pods fail their next pull with an authentication error, and
nothing on that cluster explains why — the `Registry` that would have explained
it lives somewhere else and no longer exists.

**Vulnerability scanning** is left to Harbor: projects are created with
`auto_scan` enabled, so an image is scanned as it arrives.

**Lifecycle** is phase-based (`Provisioning → Ready → Failed / Terminating`,
empty until the first reconcile) with standard conditions, `observedGeneration`
and Events. Failures are separated into terminal ones, which retrying cannot fix
and which stop at `Failed`, and transient ones, which requeue.

**Access control** is plain Kubernetes RBAC. The `registry-editor-role` and
`registry-viewer-role` ClusterRoles carry aggregation labels, so a user granted
the built-in `edit` role in a namespace can manage `Registry` objects there with
no further binding, and `view` can read them.

Note what `edit` already implies: it grants read access to **every** Secret in
that namespace, including these credentials. That is the intended grant — the
team owning the namespace owns its registry credentials — but it is a namespace
boundary, not a per-`Registry` one.

## Quickstart

```sh
make docker-build docker-push IMG=<registry>/registry-operator:<tag>
KUBECONFIG=<kubeconfig> make install
KUBECONFIG=<kubeconfig> make deploy IMG=<registry>/registry-operator:<tag>

kubectl apply -n <your-namespace> -k config/samples/
kubectl get registries -A -w
```

The above is the development path (kustomize + `make deploy`). For installing a
released version via Helm and a Harvester `Addon` — the path an administrator
uses — see [`INSTALL.md`](./INSTALL.md).

## Uninstalling

Removing the operator does not remove anybody's registry:

```sh
make undeploy      # manager, RBAC, namespace — the CRD and every Registry stay
```

The CRD is not part of that overlay, and it carries `helm.sh/resource-policy:
keep` so Helm leaves it alone too. Deleting a CRD deletes every custom resource
defined by it, and each of those deletions runs the finalizer that destroys a
Harbor project and its images — an uninstall must not be able to reach that.

Reinstalling adopts what is already there. The project name is derived from the
`Registry`, an existing project is recognised by its recorded id, and
credentials are only minted when their Secret is absent, so nothing is recreated
and no copied credential stops working.

While the operator is uninstalled, deleting a `Registry` **hangs in
Terminating**: its finalizer has nobody to run it. Reinstall the operator and
the deletion completes. That is deliberate — the alternative is a Harbor project
nothing owns.

### Removing everything, deliberately

Destroying tenant data is a separate, explicit act. Delete the registries first,
let the operator clean Harbor up, and only then remove the operator and the CRD:

```sh
kubectl delete registries --all -A     # deletes the Harbor projects and every image
kubectl get registries -A              # wait until this is empty
make undeploy
make uninstall                         # removes the CRD
```

Running `make uninstall` while registries still exist has the same effect
through a path nobody reviews: the CRD goes, the objects go with it, and their
finalizers destroy the projects.

## Build / test / develop

```sh
make manifests generate fmt vet build   # regenerate CRDs + DeepCopy, build binary
make test                               # unit tests
make docker-build IMG=...               # build image
make install                            # apply CRDs using current kubeconfig
make deploy IMG=...                     # apply manager + RBAC
```

## Part of Open Cloud Datacenter

This component lives in the [WSO2 Open Cloud
Datacenter](https://github.com/wso2/open-cloud-datacenter) initiative, providing
managed container registry services.

## License

Apache-2.0
