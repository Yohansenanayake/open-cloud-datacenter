package controller

import (
	"context"
	"errors"
	"fmt"
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

// RegistryReconciler serves one Registry: a project inside the central Harbor,
// with credentials written to a Secret beside the Registry.
//
// The operator does not deploy Harbor. It drives one that already exists,
// located by configuration, so every Registry on every cluster becomes a
// project inside that same Harbor.
type RegistryReconciler struct {
	client.Client
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

	// 1. Build a client for the central Harbor. Credentials are read every
	// pass, so rotating the Secret takes effect without restarting.
	registryURL := r.HarborCfg.URL
	cli, err := r.harborClient(ctx)
	if err != nil {
		return r.transient(ctx, &cr, "read Harbor credentials", err)
	}

	// 2. Create the Harbor project with this Registry's quota. Creation is
	// idempotent: Harbor answers 409 once the project exists.
	plan := cr.Spec.Plan
	if plan == "" {
		plan = planOrder[0]
	}
	// An unrecognized plan is a spec error, not transient — retrying can't fix it.
	quotaBytes, err := projectQuotaBytes(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}

	// 2b. Check Project name valid
	projectName := harborProjectName(&cr)
	// Terminal, not transient: retrying resolves none of these. A Registry's
	// name is immutable, so recovery is to delete and recreate under a
	// different one — the same thing an admission webhook would force, which is
	// why refusing outright rather than waiting for the name to free is the
	// consistent behaviour.
	// 2b.1 Check if name valid
	if err := validateProjectName(projectName); err != nil {
		return r.fail(ctx, &cr, "resolve Harbor project", err)
	}
	// Harbor's own built-in project is a name a user could plausibly pick, and
	// adopting it would be silent (see reservedProjectNames).
	// 2b.2 Check if it is a reserved name (Eg: "library")
	if reservedProjectNames[projectName] {
		return r.fail(ctx, &cr, "resolve Harbor project",
			fmt.Errorf("%q is a Harbor built-in project name; rename this Registry", projectName))
	}
	// Claim the name before creating anything. A name already held by someone
	// else is terminal; a Harbor that cannot answer is not.
	if err := r.claimProjectName(ctx, cli, &cr, projectName); err != nil {
		if errors.Is(err, errProjectNameTaken) {
			return r.fail(ctx, &cr, "claim Harbor project", err)
		}
		return r.transient(ctx, &cr, "check Harbor project", err)
	}

	if err := cli.CreateHarborProject(ctx, projectName, quotaBytes); err != nil {
		return r.transient(ctx, &cr, "create Harbor project", err)
	}

	// 2c. Converge the quota every reconcile — this is how a plan change takes
	// effect, and it doubles as drift detection.
	proj, err := cli.GetProject(ctx, projectName)
	if err != nil {
		return r.transient(ctx, &cr, "get Harbor project", err)
	}
	if proj.ProjectID == 0 {
		return r.transient(ctx, &cr, "get Harbor project", fmt.Errorf("Harbor returned a project with no project_id for %q", projectName))
	}
	if err := cli.EnsureProjectQuota(ctx, proj.ProjectID, quotaBytes); err != nil {
		// Transient, not terminal — a quota-below-usage rejection can resolve
		// once images are removed or the plan changes again.
		return r.transient(ctx, &cr, "set project quota", err)
	}

	// 3. Mint the robot account once and keep its credentials in a Secret.
	credName := credentialsSecretName(&cr)
	if err := r.ensureCredentials(ctx, &cr, cli, projectName, registryURL, credName); err != nil {
		return r.transient(ctx, &cr, "provision credentials", err)
	}

	// 4. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.HarborProject = projectName
		s.RegistryURL = registryURL
		s.CredentialsSecretName = credName
		s.Message = fmt.Sprintf("registry %q ready", projectName)
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "registry ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(&cr, nil, corev1.EventTypeNormal, reasonReady, actionProvision,
		"registry ready; credentials in Secret %s", credName)

	// Steady-state: re-check for drift (project deleted out-of-band, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureCredentials mints a project robot account only if the credentials
// Secret does not already exist, then writes an owned Secret. The Secret's
// presence is what makes this once-only: re-minting would invalidate
// credentials already in use. When it is absent, any robot from a previous
// half-finished attempt is unusable — its secret was never stored — so
// EnsureProjectRobotAccount replaces it rather than failing against it.
func (r *RegistryReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client, projectName, registryURL, credName string) error {
	// 1. check if secret already available
	key := client.ObjectKey{Namespace: cr.Namespace, Name: credName}
	var existing corev1.Secret
	if err := r.Get(ctx, key, &existing); err == nil {
		return nil // already provisioned
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	robot, err := cli.EnsureProjectRobotAccount(ctx, projectName, robotAccountName(cr))
	if err != nil {
		return fmt.Errorf("create robot account: %w", err)
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: credName, Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"robot_username": []byte(robot.Name),
			"robot_secret":   []byte(robot.Secret),
			"registry_url":   []byte(registryURL),
			"project":        []byte(projectName),
			"robot_id":       []byte(fmt.Sprintf("%d", robot.ID)),
		},
	}
	if err := controllerutil.SetControllerReference(cr, sec, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, sec); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
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
// status.harborProject is the gate: it is written only once the project really
// exists in Harbor, so an empty value means nothing was created and there is
// nothing to reclaim.
func (r *RegistryReconciler) deleteHarborProject(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) error {
	projectName := cr.Status.HarborProject
	if projectName == "" {
		return nil
	}

	cli, err := r.harborClient(ctx)
	if err != nil {
		return err
	}

	// Harbor refuses to delete a project that still holds repositories (412),
	// so empty it first. Without this the finalizer retries that 412 forever and
	// the Registry never leaves Terminating.
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

// errProjectNameTaken marks a name already held in the central Harbor, so the
// caller can tell it apart from a Harbor that simply could not answer.
var errProjectNameTaken = errors.New("harbor project name already taken")

// claimProjectName refuses a Registry whose project name is already in use.
//
// Project names are global. Every namespace on every cluster shares one Harbor,
// so the first Registry to claim a name holds it until that Registry is
// deleted. status.harborProject records the claim: a Registry already holding
// the name skips the check, which is what keeps repeated reconciles idempotent
// rather than having the second pass reject the project the first one created.
//
// An existing project is never adopted. The operator has no way yet to tell its
// own project from another tenant's, and adopting one would hand this Registry
// credentials on someone else's images. Refusing is the safe direction, and it
// stays correct once ownership is recorded explicitly.
func (r *RegistryReconciler) claimProjectName(ctx context.Context, cli *harbor.Client, cr *registryv1alpha1.Registry, projectName string) error {
	// Operator add projectName as status also after projcet provisioned. So if cr.Status.HarborProject == projectName
	// means already provisioned project i  before cycle
	// This checked since if not added like that operator would give already taken error for good project
	if cr.Status.HarborProject == projectName {
		return nil // already ours
	}

	_, err := cli.GetProject(ctx, projectName)
	switch {
	case errors.Is(err, harbor.ErrProjectNotFound):
		return nil // free to claim
	case err != nil:
		// Unreachable, unauthenticated, or any other failure. Never reported as
		// "name taken" — the name may well be free.
		return fmt.Errorf("check whether project %q exists: %w", projectName, err)
	}

	return fmt.Errorf("%w: %q already exists in the registry. Registry names are "+
		"global across every namespace and cluster, so rename this Registry",
		errProjectNameTaken, projectName)
}

// maxProjectNameLen is Harbor's documented limit for project_name.
const maxProjectNameLen = 255

// harborProjectNamePattern mirrors Harbor's project-name rule: lowercase
// alphanumeric segments joined by a single ".", "_" or "-".
//
// Harbor's OpenAPI spec pins only the length, so Harbor stays the final
// arbiter; checking here turns a round trip into a clear message. It is a real
// check rather than a formality, because Kubernetes names are laxer than
// Harbor's: "a--b" is a valid object name and not a valid project name.
var harborProjectNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// validateProjectName reports why a resolved name cannot be a Harbor project.
func validateProjectName(name string) error {
	// 1. Length check
	if len(name) > maxProjectNameLen {
		return fmt.Errorf("project name %q is %d characters; Harbor allows at most %d",
			name, len(name), maxProjectNameLen)
	}
	// 2. Pattern check
	if !harborProjectNamePattern.MatchString(name) {
		return fmt.Errorf("project name %q is not a valid Harbor project name: it must be "+
			"lowercase alphanumeric segments joined by a single '.', '_' or '-'", name)
	}
	return nil
}

// reservedProjectNames are Harbor project names a Registry must never resolve
// to, whether or not they currently exist. Harbor's built-in "library" project
// is PUBLIC, so a Registry named "library" that reached it would publish its
// images world-readable.
//
// claimProjectName already refuses any name that exists, which covers every
// other pre-existing project. This list is for names that must stay refused
// even when absent — "library" is recreated by Harbor, so a gap between its
// deletion and recreation must not become a window to claim it.
var reservedProjectNames = map[string]bool{"library": true}

// harborProjectName returns the Harbor project for a Registry: its own name.
//
// A Registry's name is a DNS label, which always satisfies Harbor's project
// naming rules.
//
// It is NOT unique across the central Harbor: two Registries in different
// namespaces, or on different clusters, resolve to the same project name.
// claimProjectName refuses the second one rather than letting it share the
// first one's project, which makes names global and first-come-first-served.
//
// That check is not atomic. Two Registries reconciling at the same instant can
// both find the name free, and CreateHarborProject still treats 409 as success,
// so the loser would proceed against the winner's project. Closing that needs
// ownership recorded on the project itself.
func harborProjectName(cr *registryv1alpha1.Registry) string {
	return strings.ToLower(cr.Name)
}

// credentialsSecretName returns the Secret holding this Registry's robot credentials.
func credentialsSecretName(cr *registryv1alpha1.Registry) string {
	return "registry-credentials-" + cr.Name
}

// robotAccountName returns the robot account name for a Registry. Harbor
// prefixes it with "robot$<project>+", so the suffix is kept short.
func robotAccountName(cr *registryv1alpha1.Registry) string {
	s := strings.ToLower(cr.Name)
	if len(s) > 12 {
		s = s[:12]
	}
	return "ci-" + s
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

// provisioning records a wait state and requeues after the given delay.
func (r *RegistryReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.Registry, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
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
