# registry

A Kubernetes operator that hands a team its own project in a shared container
registry.

The platform runs **one** Harbor, deployed and operated outside this operator.
Applying a `Registry` creates a project inside it, sets that project's storage
quota, mints two robot accounts, and writes their credentials into Secrets beside
the `Registry`. The operator installs nothing and manages no storage.

Runs on any conformant Kubernetes cluster — it uses no vendor APIs.

## Requirements

| Requirement | Notes |
|:---|:---|
| A running Harbor | Reachable from the operator over HTTP(S), addressed by `HARBOR_URL` |
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
| `.status.pushSecretName` | pull, push, tag | a build pipeline |

They are separate Harbor accounts. A pull credential is copied onto every cluster
that runs the images and ends up in many hands, so it must not be able to
overwrite what it reads.

Copying the pull Secret to another cluster means stripping the fields that tie it
to this one — `ownerReferences`, `uid`, `resourceVersion`, `creationTimestamp`
and `namespace`. A copied `ownerReferences` is the one that bites: the other
cluster's garbage collector looks for an owning `Registry`, does not find one,
and deletes the Secret.

### Project names are global

A `Registry` becomes the Harbor project of the same name, and every namespace
shares one Harbor. Names are therefore first-come, first-served across the whole
platform: a name already held by another `Registry` is refused, and the refusal
says so. A `Registry`'s name cannot be changed, so recovery is to delete it and
create another.

The operator records which `Registry` owns each project it creates. A project it
did not create is never adopted, even when the name is free to claim — nothing
establishes that those images belong to the team asking for them.

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
| `METRICS_CERT_DIR` | | — | Serving certificate for the metrics endpoint. Empty means a self-signed one only an unverifying scraper can read |

The credentials Secret is read on every reconcile, so rotating it takes effect
without restarting the operator.

## How it works

A controller-runtime operator with a single reconciler. The custom resource is
the source of truth, all work happens inside the reconcile loop, and the loop is
level-triggered: every pass re-asserts the desired state and does nothing when it
already holds. Leader election is on, so extra replicas act as hot standbys.

**Reconcile** resolves the project name, refuses it if it is reserved, malformed,
or held by another `Registry`, creates the project, records ownership, converges
the quota, mints each robot account exactly once, and writes the Secrets. The
quota is re-applied every pass, which is both how a plan change takes effect and
how drift is corrected.

**Credentials are minted once.** The Secret's existence is what makes it
once-only: re-minting would invalidate every copy already distributed. A robot
left behind by an attempt that died before its Secret was written is unusable —
its token was never stored — so it is replaced rather than failed against.

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
make docker-build docker-push IMG=<registry>/registry-provisioner:<tag>
KUBECONFIG=<kubeconfig> make install
KUBECONFIG=<kubeconfig> make deploy IMG=<registry>/registry-provisioner:<tag>

kubectl apply -n <your-namespace> -k config/samples/
kubectl get registries -A -w
```

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
