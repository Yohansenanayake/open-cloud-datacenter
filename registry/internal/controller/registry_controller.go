package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const registryFinalizer = "registry.opencloud.wso2.com/registry-cleanup"

// labelRegistry marks a Secret as belonging to a Registry, so the pair a
// Registry owns can be listed without parsing names.
const labelRegistry = "registry.opencloud.wso2.com/registry"

// RegistryReconciler serves one Registry: a project inside the central Harbor,
// with credentials written to a Secret beside the Registry.
//
// The operator does not deploy Harbor. It drives one that already exists,
// located by configuration, so every Registry on every cluster becomes a
// project inside that same Harbor.
type RegistryReconciler struct {
	client.Client
	// APIReader reads from the API server, bypassing the cache. Credential Secrets
	// are read through it, so one created moments ago is never mistaken for absent.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  events.EventRecorder
	HarborCfg config.HarborConfig

	// accessMu guards the cached VerifyAccess result behind CheckHarborAccess.
	accessMu  sync.Mutex
	accessErr error
	accessAt  time.Time
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile converges one Registry: create its project, quota, robot account,
// and credentials Secret inside the central Harbor.
func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr registryv1alpha1.Registry
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Registry: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &cr, log)
	}
	if !controllerutil.ContainsFinalizer(&cr, registryFinalizer) {
		controllerutil.AddFinalizer(&cr, registryFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 2. Build a client for the central Harbor. Credentials are read every
	// pass, so rotating the Secret takes effect without restarting.
	registryURL := r.HarborCfg.URL
	cli, err := r.harborClient(ctx)
	if err != nil {
		return r.transient(ctx, &cr, "read Harbor credentials", err)
	}

	// 3. Resolve the plan to its quota in bytes. The quota is re-applied every pass, so a plan change takes effect here.
	plan := cr.Spec.Plan
	if plan == "" {
		plan = planOrder[0]
	}
	quotaBytes, err := projectQuotaBytes(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}

	// 4. Create the Harbor project web-38cf3945
	projectName, err := harborProjectName(&cr)
	if err != nil {
		return r.fail(ctx, &cr, "resolve Harbor project", err)
	}

	// 5. Send request to Harbor to create the project. Nothing else can produce
	// this name, so 409 means an earlier pass already created it.
	created := true
	if err := cli.CreateHarborProject(ctx, projectName, quotaBytes); err != nil {
		if !errors.Is(err, harbor.ErrProjectExists) {
			return r.transient(ctx, &cr, "create Harbor project", err)
		}
		created = false
	}

	//6. Get the project to get projectID
	proj, err := cli.GetProject(ctx, projectName)
	if err != nil {
		return r.transient(ctx, &cr, "get Harbor project", err)
	}
	if proj.ProjectID == 0 {
		return r.transient(ctx, &cr, "get Harbor project", fmt.Errorf("Harbor returned a project with no project_id for %q", projectName))
	}

	// 6a. Harbor never reuses a project id, so one that changed under a name only
	// this Registry can produce means the project it created is gone and another
	// holds the name now. Converging quota or credentials into that project would
	// attach this tenant to contents it does not own.
	if !created && cr.Status.HarborProjectID != 0 && cr.Status.HarborProjectID != proj.ProjectID {
		return r.fail(ctx, &cr, "resolve Harbor project",
			fmt.Errorf("%w: %q is now id %d, but this Registry created id %d",
				errProjectReplaced, projectName, proj.ProjectID, cr.Status.HarborProjectID))
	}

	// 6b. A project created in this pass under an id this Registry did not have
	// means the old one was deleted out of band, and Harbor deleted its robots
	// with it. The Secrets still hold those robots, so they authenticate nothing:
	// drop them, and the credentials step below mints replacements in the new
	// project. Copies made onto other clusters stop working either way — the
	// images they were minted for are gone.
	if created && cr.Status.HarborProjectID != 0 && cr.Status.HarborProjectID != proj.ProjectID {
		if err := r.deleteCredentialSecrets(ctx, &cr); err != nil {
			return r.transient(ctx, &cr, "reissue credentials", err)
		}
		r.Recorder.Eventf(&cr, nil, corev1.EventTypeWarning, reasonReissued, actionProvision,
			"Harbor project %q was recreated as id %d; its credentials no longer authenticate and are being reissued",
			projectName, proj.ProjectID)
	}

	// 6c. Record the id as soon as it is known, so the finalizer can tell this
	// project from a later one sharing its name even if no pass reaches Ready.
	if cr.Status.HarborProjectID != proj.ProjectID {
		if err := r.patchStatus(ctx, req.NamespacedName, func(st *registryv1alpha1.RegistryStatus) {
			st.HarborProject, st.HarborProjectID = projectName, proj.ProjectID
		}); err != nil {
			return ctrl.Result{}, err
		}
		cr.Status.HarborProject, cr.Status.HarborProjectID = projectName, proj.ProjectID
	}

	// 7. Set the project quota to the plan's amount. This is idempotent.
	if err := cli.EnsureProjectQuota(ctx, proj.ProjectID, quotaBytes); err != nil {
		return r.transient(ctx, &cr, "set project quota", err)
	}

	// 8. Mint the robot accounts once and keep their credentials in Secrets.
	if err := r.ensureCredentials(ctx, &cr, cli, proj.ProjectID, projectName, registryURL); err != nil {
		if errors.Is(err, errSecretNameTaken) {
			return r.fail(ctx, &cr, "provision credentials", err)
		}
		return r.transient(ctx, &cr, "provision credentials", err)
	}

	// 9. Ready.
	pullName, pushName := pullSecretName(&cr), pushSecretName(&cr)
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.HarborProject = projectName
		s.HarborProjectID = proj.ProjectID
		s.RegistryURL = registryURL
		s.PullSecretName = pullName
		s.PushSecretName = pushName
		s.Message = fmt.Sprintf("registry %q ready", projectName)
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "registry ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(&cr, nil, corev1.EventTypeNormal, reasonReady, actionProvision,
		"registry ready; pull credentials in Secret %s, push credentials in Secret %s", pullName, pushName)

	// Steady-state: re-check for drift (project deleted out-of-band, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureCredentials provisions both credential Secrets for a Registry: one that
// can only pull, and one that can also push.
//
// They are separate accounts on purpose. A pull Secret is copied onto every
// cluster that runs the images and ends up in many hands, so it must not be
// able to publish; a push Secret belongs to a build pipeline. One credential
// doing both would mean any workload able to read its own pull Secret could
// overwrite the images it pulls.
func (r *RegistryReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client, projectID int64, projectName, registryURL string) error {
	for _, c := range []struct {
		secretName string
		robotName  string
		access     harbor.RobotAccess
	}{
		{pullSecretName(cr), robotAccountName(cr, harbor.AccessPull), harbor.AccessPull},
		{pushSecretName(cr), robotAccountName(cr, harbor.AccessPush), harbor.AccessPush},
	} {
		if err := r.ensureCredential(ctx, cr, cli, projectID, projectName, registryURL, c.secretName, c.robotName, c.access); err != nil {
			return err
		}
	}
	return nil
}

// errSecretNameTaken marks a credentials Secret name already held by an object
// this Registry does not own, so the caller can tell it apart from a write that
// merely failed.
var errSecretNameTaken = errors.New("credentials Secret name already in use")

// errProjectReplaced marks a Harbor project that carries this Registry's name
// but is not the project it created.
var errProjectReplaced = errors.New("harbor project was replaced")

// deleteCredentialSecrets removes both credential Secrets so the next pass mints
// them again. Only Secrets this Registry controls are touched: one it does not
// own belongs to whoever created it, exactly as when they are written.
func (r *RegistryReconciler) deleteCredentialSecrets(ctx context.Context, cr *registryv1alpha1.Registry) error {
	for _, name := range []string{pullSecretName(cr), pushSecretName(cr)} {
		var sec corev1.Secret
		err := r.secretReader().Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: name}, &sec)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !metav1.IsControlledBy(&sec, cr) {
			continue
		}
		if err := r.Delete(ctx, &sec); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ensureCredential mints one project robot account and writes its credentials
// to a Secret, once.
//
// The Secret's presence is what makes it once-only: re-minting would invalidate
// credentials already in use, including every copy made onto another cluster.
// When it is absent, any robot from a previous half-finished attempt is unusable
// — its secret was never stored — so EnsureProjectRobotAccount replaces it
// rather than failing against it.
//
// A Secret already at that name which this Registry does not own is reported
// rather than taken over: it belongs to whoever created it, and overwriting or
// deleting it would destroy something the operator never made.
func (r *RegistryReconciler) ensureCredential(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client,
	projectID int64, projectName, registryURL, secretName, robotName string, access harbor.RobotAccess) error {

	key := client.ObjectKey{Namespace: cr.Namespace, Name: secretName}
	var existing corev1.Secret
	switch err := r.secretReader().Get(ctx, key, &existing); {
	case err == nil:
		if !metav1.IsControlledBy(&existing, cr) {
			return fmt.Errorf("%w: Secret %s/%s exists and is not owned by this Registry",
				errSecretNameTaken, cr.Namespace, secretName)
		}
		return nil // already minted
	case !apierrors.IsNotFound(err):
		return err
	}

	robot, err := cli.EnsureProjectRobotAccount(ctx, projectID, projectName, robotName, access)
	if err != nil {
		return fmt.Errorf("create robot account %q: %w", robotName, err)
	}

	dockerConfig, err := dockerConfigJSON(registryURL, robot.Name, robot.Secret)
	if err != nil {
		return err
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				labelRegistry: cr.Name,
			},
		},
		// dockerconfigjson, not Opaque: a Secret in this shape can be named in a
		// pod's imagePullSecrets and read by docker login as-is. An Opaque Secret
		// with custom keys has to be converted by hand before either will use it.
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: dockerConfig,
		},
	}
	if err := controllerutil.SetControllerReference(cr, sec, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, sec); err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Another pass wrote the Secret after the read above. Minting just replaced the
	// robot it names, so its token no longer works: store the new one.
	if err := r.secretReader().Get(ctx, key, &existing); err != nil {
		return err
	}
	if !metav1.IsControlledBy(&existing, cr) {
		return fmt.Errorf("%w: Secret %s/%s exists and is not owned by this Registry",
			errSecretNameTaken, cr.Namespace, secretName)
	}
	existing.Data = sec.Data
	return r.Update(ctx, &existing)
}

// secretReader is the uncached API reader when one is set, else the cached client.
func (r *RegistryReconciler) secretReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// dockerConfigJSON renders credentials in the shape kubelet and docker expect.
//
// registryURL carries a scheme so that status and events show a usable address,
// but a docker config is keyed by host alone: leaving the scheme in produces a
// Secret that silently never matches the registry it was minted for.
func dockerConfigJSON(registryURL, username, secret string) ([]byte, error) {
	cfg := map[string]any{
		"auths": map[string]any{
			dockerConfigHost(registryURL): map[string]string{
				"username": username,
				"password": secret,
				"auth":     base64.StdEncoding.EncodeToString([]byte(username + ":" + secret)),
			},
		},
	}
	return json.Marshal(cfg)
}

// dockerConfigHost is the key a docker config entry is stored under: the host
// alone, since that is what docker and the kubelet match a credential by.
func dockerConfigHost(registryURL string) string {
	if u, err := url.Parse(registryURL); err == nil && u.Host != "" {
		return u.Host
	}
	return registryURL
}

// handleDelete runs the Registry finalizer: the Harbor project and every image
// in it are removed. There is no retain option — deleting a Registry means
// deleting the registry.
//
// The credentials Secret is garbage-collected through its owner reference.
func (r *RegistryReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) (ctrl.Result, error) {
	// No finalizer means cleanup already finished and this is the last pass.
	if !controllerutil.ContainsFinalizer(cr, registryFinalizer) {
		return ctrl.Result{}, nil
	}

	// Keep the finalizer until the project is really gone, so a Harbor that is
	// merely unreachable cannot leave images behind that the user asked to
	// destroy. Every error retries: deleteHarborProject signals "nothing to
	// clean up" by returning nil, so an error here always means the project may
	// still exist.
	if err := r.deleteHarborProject(ctx, cr, log); err != nil {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonTransient, actionDelete,
			"waiting to delete Harbor project: %s", err.Error())
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	controllerutil.RemoveFinalizer(cr, registryFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteHarborProject removes the Harbor project backing this Registry, along
// with every repository inside it.
//
// The name is derived from the Registry rather than read from status, so a
// Registry deleted before its first status write still has its project removed.
// Harbor answering "no such project" is success: there is nothing to reclaim.
func (r *RegistryReconciler) deleteHarborProject(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) error {
	projectName, err := harborProjectName(cr)
	if err != nil {
		// A name Harbor cannot hold was never created under it either.
		log.Info("no Harbor project to delete", "registry", cr.Name, "reason", err.Error())
		return nil
	}

	cli, err := r.harborClient(ctx)
	if err != nil {
		return err
	}

	// The name says which project to look at; the id says whether it is the one
	// this Registry created. Harbor never reuses an id, so a project carrying a
	// different one was created by somebody else after this Registry's was gone,
	// and deleting it would destroy their repositories.
	proj, err := cli.GetProject(ctx, projectName)
	if errors.Is(err, harbor.ErrProjectNotFound) {
		return nil // already gone
	}
	if err != nil {
		return fmt.Errorf("get Harbor project %s: %w", projectName, err)
	}
	if cr.Status.HarborProjectID == 0 || proj.ProjectID != cr.Status.HarborProjectID {
		log.Info("leaving Harbor project in place: it is not the project this Registry created",
			"project", projectName, "id", proj.ProjectID, "createdID", cr.Status.HarborProjectID)
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonOrphaned, actionDelete,
			"Harbor project %q was left in place: it is id %d and this Registry created id %d",
			projectName, proj.ProjectID, cr.Status.HarborProjectID)
		return nil
	}

	// Harbor refuses to delete a project that still holds repositories (412), so
	// empty it first. Without this the finalizer retries that 412 forever and the
	// Registry never leaves Terminating.
	repos, err := cli.ListRepositories(ctx, projectName)
	if err != nil {
		return fmt.Errorf("list repositories in %s: %w", projectName, err)
	}
	for _, repo := range repos {
		log.Info("deleting Harbor repository", "project", projectName, "repository", repo)
		if err := cli.DeleteRepository(ctx, projectName, repo); err != nil {
			return fmt.Errorf("delete repository %s/%s: %w", projectName, repo, err)
		}
	}

	log.Info("deleting Harbor project", "project", projectName, "repositories", len(repos))
	if err := cli.DeleteProject(ctx, projectName); err != nil {
		return fmt.Errorf("delete Harbor project: %w", err)
	}
	return nil
}

// harborCredentials reads the operator's Harbor credentials.
//
// The Secret lives in the operator's own namespace, so authenticating never
// requires reading a tenant's namespace. Reading it per reconcile rather than
// caching at startup is what lets a rotated Secret take effect without a
// restart.
func (r *RegistryReconciler) harborCredentials(ctx context.Context) (username, password string, err error) {
	key := client.ObjectKey{Namespace: r.HarborCfg.Namespace, Name: r.HarborCfg.CredentialsSecret}
	var sec corev1.Secret
	if err := r.Get(ctx, key, &sec); err != nil {
		return "", "", fmt.Errorf("get Harbor credentials Secret %s: %w", key, err)
	}
	u, ok := sec.Data[config.HarborUsernameKey]
	if !ok {
		return "", "", fmt.Errorf("key %q not found in Secret %s", config.HarborUsernameKey, key)
	}
	pw, ok := sec.Data[config.HarborPasswordKey]
	if !ok {
		return "", "", fmt.Errorf("key %q not found in Secret %s", config.HarborPasswordKey, key)
	}
	return string(u), string(pw), nil
}

// harborClient returns a client for the central Harbor.
func (r *RegistryReconciler) harborClient(ctx context.Context) (*harbor.Client, error) {
	username, password, err := r.harborCredentials(ctx)
	if err != nil {
		return nil, err
	}
	return harbor.NewClient(r.HarborCfg.URL, username, password), nil
}

// accessTTL is how long CheckHarborAccess reuses a result. Readiness is polled
// every few seconds, so without it every probe would become a request to
// Harbor — turning a health check into load on the thing it checks.
const accessTTL = 30 * time.Second

// This simply created if there were two go routines checking same readiness probe
// This doesn't make two http requests it always check harbor for every 30s if many
// asked to check it only serves cached ready state without re pining for every case
func (r *RegistryReconciler) CheckHarborAccess(ctx context.Context) error {
	r.accessMu.Lock()
	defer r.accessMu.Unlock()

	if !r.accessAt.IsZero() && time.Since(r.accessAt) < accessTTL {
		return r.accessErr
	}

	cli, err := r.harborClient(ctx)
	if err != nil {
		r.accessErr, r.accessAt = err, time.Now()
		return r.accessErr
	}
	if err := cli.VerifyAccess(ctx); err != nil {
		// The kubelet cancels a probe well before the Harbor client's own
		// timeout. Caching that would keep readiness failed for the whole TTL
		// after Harbor recovered, so report it and leave the cache untouched.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("Harbor access check at %s did not complete: %w", r.HarborCfg.URL, err)
		}
		r.accessErr = fmt.Errorf("Harbor at %s did not accept the configured credentials: %w", r.HarborCfg.URL, err)
	} else {
		r.accessErr = nil
	}
	r.accessAt = time.Now()
	return r.accessErr
}

// --- naming ---

// maxProjectNameLen is Harbor's documented limit for project_name.
const maxProjectNameLen = 255

// projectNameDigestLen is how many hex characters of the UID digest the project
// name carries. Four bytes distinguish Registries that share a name, and two
// that share a name and a namespace cannot exist.
const projectNameDigestLen = 8

// harborProjectNamePattern mirrors Harbor's project-name rule: lowercase
// alphanumeric segments joined by a single ".", "_" or "-".
//
// Harbor's OpenAPI spec pins only the length, so Harbor stays the final arbiter;
// checking here turns a round trip into a clear message. It is a real check
// rather than a formality, because Kubernetes names are laxer than Harbor's:
// "a--b" is a valid object name and not a valid project name.
var harborProjectNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// harborProjectName returns the Harbor project for a Registry: its own name
// followed by a short digest of its UID.
//
// The UID makes the name a property of this one object. The API server assigns
// it at creation and never reuses it, so no other Registry can resolve to this
// project and nothing outside the cluster can predict the name. A project under
// it was therefore created for this Registry, which is what makes ownership
// structural: there is no name to claim from another tenant, no marker to write
// on the project, and no state to reconstruct when a reconcile is interrupted
// between creating the project and recording anything about it.
//
// The cost is that the name cannot be reproduced for a Registry that is deleted
// and recreated — a new object has a new UID, so it gets a new, empty project.
// Deleting a Registry already destroys its images, so nothing survives that the
// old name would have addressed.
//
// A digest rather than the raw UID keeps the name short and independent of how
// UIDs are formatted.
func harborProjectName(cr *registryv1alpha1.Registry) (string, error) {
	if cr.UID == "" {
		// Every object read from the API server has one. Without it the digest
		// would be the same for every Registry of the same name, and two of them
		// would share a project.
		return "", fmt.Errorf("Registry %s/%s has no UID, so no project name can be derived for it", cr.Namespace, cr.Name)
	}
	digest := sha256.Sum256([]byte(cr.UID))
	name := cr.Name + "-" + hex.EncodeToString(digest[:])[:projectNameDigestLen]

	if len(name) > maxProjectNameLen {
		return "", fmt.Errorf("project name %q is %d characters; Harbor allows at most %d, so this Registry needs a shorter name",
			name, len(name), maxProjectNameLen)
	}
	if !harborProjectNamePattern.MatchString(name) {
		return "", fmt.Errorf("project name %q is not a valid Harbor project name: a Registry name must be "+
			"lowercase alphanumeric segments joined by a single '.', '_' or '-'", name)
	}
	return name, nil
}

// pullSecretName returns the Secret carrying pull-only credentials. This is the
// one copied onto clusters that run the images.
func pullSecretName(cr *registryv1alpha1.Registry) string {
	return cr.Name + "-pull"
}

// pushSecretName returns the Secret carrying credentials that can also publish.
func pushSecretName(cr *registryv1alpha1.Registry) string {
	return cr.Name + "-push"
}

// robotAccountName returns the robot account name for one access level. Harbor
// prefixes it with "robot$<project>+", so the suffix is kept short.
//
// The access level is part of the name because the two accounts live in the
// same project and would otherwise collide.
func robotAccountName(cr *registryv1alpha1.Registry, access harbor.RobotAccess) string {
	s := strings.ToLower(cr.Name)
	if len(s) > 12 {
		s = s[:12]
	}
	if access == harbor.AccessPull {
		return "pull-" + s
	}
	return "push-" + s
}

// projectQuotaGi maps a plan to a registry's Harbor storage quota in gibibytes.
// This is separate from the backend's deployment sizing.
var projectQuotaGi = map[string]int64{
	"starter":      5,
	"professional": 20,
	"enterprise":   100,
}

// projectQuotaBytes resolves a plan to a storage quota in bytes. projectQuotaGi
// is the single source of truth for which plan names are valid.
func projectQuotaBytes(plan string) (int64, error) {
	gi, ok := projectQuotaGi[plan]
	if !ok {
		return 0, fmt.Errorf("unknown plan %q; valid: starter, professional, enterprise", plan)
	}
	return gi * 1024 * 1024 * 1024, nil
}

// --- status helpers ---

// patchStatus applies mutate to the latest status, retrying once on conflict.
func (r *RegistryReconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.Registry
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		if err := r.Status().Update(ctx, &fresh); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("status update: too many conflicts")
}

// transient records a retryable failure and returns the error for backoff.
func (r *RegistryReconciler) transient(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonTransient, actionReconcile, "%s", msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonTransient, msg)
	})
	return ctrl.Result{}, cause
}

// fail marks a spec error that retrying cannot resolve: sets Failed and stops
// requeueing. Only for causes traceable to the spec, since editing it
// re-triggers reconcile through the watch.
func (r *RegistryReconciler) fail(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonError, actionReconcile, "%s", msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseFailed
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonError, msg)
	})
	return ctrl.Result{}, reconcile.TerminalError(cause)
}

// SetupWithManager registers the controller with the manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.Registry{}).
		Owns(&corev1.Secret{}).
		Named("registry").
		Complete(r)
}
