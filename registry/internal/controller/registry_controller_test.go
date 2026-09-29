package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const (
	testHarborNS  = "registry-system"
	testCredsName = "harbor-credentials"
	testHarborURL = "https://registry.example.com"
)

// testLogger is a discarding logger for functions that take one.
func testLogger() logr.Logger { return logr.Discard() }

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := registryv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add registry scheme: %v", err)
	}
	return s
}

func newRegistryReconciler(t *testing.T, fc client.WithWatch) *RegistryReconciler {
	return &RegistryReconciler{
		Client:   fc,
		Scheme:   newTestScheme(t),
		Recorder: events.NewFakeRecorder(64),
		HarborCfg: config.HarborConfig{
			URL:               testHarborURL,
			CredentialsSecret: testCredsName,
			Namespace:         testHarborNS,
		},
	}
}

func registryIn(namespace, name string) *registryv1alpha1.Registry {
	return &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       registryv1alpha1.RegistrySpec{Plan: "starter"},
	}
}

// harborCredsSecret is the Secret the operator authenticates to Harbor with.
func harborCredsSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testCredsName, Namespace: testHarborNS},
		Data:       data,
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.Registry{}).
		WithObjects(objs...).
		Build()
}

// The operator authenticates with credentials from its own namespace, so a
// tenant namespace is never read to reach Harbor.
func TestHarborCredentials_ReadsFromTheOperatorNamespace(t *testing.T) {
	fc := newFakeClient(t, harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("robot$system"),
		config.HarborPasswordKey: []byte("s3cret"),
	}))
	r := newRegistryReconciler(t, fc)

	user, pass, err := r.harborCredentials(context.Background())
	if err != nil {
		t.Fatalf("harborCredentials() error = %v", err)
	}
	if user != "robot$system" || pass != "s3cret" {
		t.Errorf("harborCredentials() = %q/%q, want robot$system/s3cret", user, pass)
	}
}

// A malformed or absent Secret must produce a message naming what is wrong.
// Every Registry fails the same way for the same reason, so a vague error here
// costs the same debugging effort once per Registry.
func TestHarborCredentials_ErrorsNameTheProblem(t *testing.T) {
	tests := []struct {
		name   string
		objs   []client.Object
		wantIn string
	}{
		{
			name:   "secret missing entirely",
			objs:   nil,
			wantIn: testCredsName,
		},
		{
			name:   "username key absent",
			objs:   []client.Object{harborCredsSecret(map[string][]byte{config.HarborPasswordKey: []byte("p")})},
			wantIn: config.HarborUsernameKey,
		},
		{
			name:   "password key absent",
			objs:   []client.Object{harborCredsSecret(map[string][]byte{config.HarborUsernameKey: []byte("u")})},
			wantIn: config.HarborPasswordKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRegistryReconciler(t, newFakeClient(t, tt.objs...))
			if _, _, err := r.harborCredentials(context.Background()); err == nil {
				t.Fatal("harborCredentials() error = nil, want an error")
			} else if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantIn)
			}
		})
	}
}

// Readiness must report a Harbor the operator cannot authenticate to. Failing
// to read credentials is exactly that case, and is reachable without a server.
func TestCheckHarborAccess_ReportsUnusableCredentials(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	if err := r.CheckHarborAccess(context.Background()); err == nil {
		t.Fatal("CheckHarborAccess() error = nil, want an error when the credentials Secret is missing")
	}
}

// The cached result is what keeps a readiness probe polled every few seconds
// from becoming steady load on Harbor.
func TestCheckHarborAccess_ReusesTheCachedResult(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))

	first := r.CheckHarborAccess(context.Background())
	if first == nil {
		t.Fatal("expected the first check to fail with no credentials Secret")
	}

	// Supplying the Secret now must NOT change the answer until the TTL lapses.
	if err := r.Create(context.Background(), harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("u"),
		config.HarborPasswordKey: []byte("p"),
	})); err != nil {
		t.Fatalf("create credentials Secret: %v", err)
	}
	second := r.CheckHarborAccess(context.Background())
	if second == nil || second.Error() != first.Error() {
		t.Errorf("CheckHarborAccess() = %v, want the cached %v; the cache is not being used", second, first)
	}
}

// Project names are the Registry's own name, which is unique only within one
// namespace. Two Registries in different namespaces resolve to the SAME Harbor
// project — with one shared Harbor that is a cross-tenant collision, and it is
// what project ownership verification has to close.
func TestHarborProjectName_IsNotUniqueAcrossNamespaces(t *testing.T) {
	a := harborProjectName(registryIn("team-a", "web"))
	b := harborProjectName(registryIn("team-b", "web"))
	if a != b {
		t.Fatalf("harborProjectName() = %q and %q; expected both to be %q", a, b, "web")
	}
	if a != "web" {
		t.Errorf("harborProjectName() = %q, want web", a)
	}
}

func TestHarborProjectName_ReservedNamesAreRejected(t *testing.T) {
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "library"))] {
		t.Error(`a Registry named "library" resolves to Harbor's public built-in project and must be refused`)
	}
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "LIBRARY"))] {
		t.Error("the reserved-name check must survive case folding, since harborProjectName lowercases")
	}
	for _, ok := range []string{"web", "api", "libraries", "my-library"} {
		if reservedProjectNames[harborProjectName(registryIn("acme-project-1", ok))] {
			t.Errorf("%q is not reserved and must be allowed", ok)
		}
	}
}

// A pull Secret is copied onto every cluster that runs these images, so it must
// be usable as-is in imagePullSecrets — which requires the dockerconfigjson
// shape and the host without a scheme.
func TestDockerConfigJSON_IsUsableAsAnImagePullSecret(t *testing.T) {
	raw, err := dockerConfigJSON("https://registry.example.com", "robot$web+pull-web", "tok")
	if err != nil {
		t.Fatalf("dockerConfigJSON() error = %v", err)
	}

	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal docker config: %v", err)
	}

	entry, ok := cfg.Auths["registry.example.com"]
	if !ok {
		t.Fatalf("auths keys = %v, want the bare host; a scheme here never matches the registry", keysOf(cfg.Auths))
	}
	if entry.Username != "robot$web+pull-web" || entry.Password != "tok" {
		t.Errorf("auths entry = %q/%q, want the robot credentials", entry.Username, entry.Password)
	}
	want := base64.StdEncoding.EncodeToString([]byte("robot$web+pull-web:tok"))
	if entry.Auth != want {
		t.Errorf("auth = %q, want %q; docker reads this field, not username/password", entry.Auth, want)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The two Secrets must be different accounts: a pull credential that could also
// push would let any workload holding it overwrite the images it consumes.
func TestRobotAccountName_PullAndPushAreDistinctAccounts(t *testing.T) {
	cr := registryIn("team-a", "web")
	pull := robotAccountName(cr, harbor.AccessPull)
	push := robotAccountName(cr, harbor.AccessPush)
	if pull == push {
		t.Fatalf("robotAccountName() = %q for both access levels; they would collide in Harbor", pull)
	}
	if pullSecretName(cr) == pushSecretName(cr) {
		t.Error("pull and push Secrets resolve to the same name")
	}
}

func TestProjectQuotaBytes(t *testing.T) {
	for plan, wantGi := range map[string]int64{"starter": 5, "professional": 20, "enterprise": 100} {
		got, err := projectQuotaBytes(plan)
		if err != nil {
			t.Fatalf("projectQuotaBytes(%q) error = %v", plan, err)
		}
		if want := wantGi * 1024 * 1024 * 1024; got != want {
			t.Errorf("projectQuotaBytes(%q) = %d, want %d", plan, got, want)
		}
	}
	if _, err := projectQuotaBytes("gigantic"); err == nil {
		t.Error("projectQuotaBytes() error = nil, want an unknown plan rejected")
	}
}

// Deleting a Registry that never got a Harbor project must not hang: there is
// nothing to remove.
func TestHandleDelete_RegistryWithoutProjectReleasesImmediately(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	fc := newFakeClient(t, reg)
	r := newRegistryReconciler(t, fc)

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("handleDelete() requeued after %v for a Registry with no Harbor project", res.RequeueAfter)
	}
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			t.Error("finalizer still present; a Registry with no Harbor project has nothing to clean up")
		}
	}
}

// An unreachable Harbor must keep the finalizer rather than release it, or
// images a user asked to destroy are silently left behind.
func TestHandleDelete_KeepsFinalizerWhenHarborIsUnreachable(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	reg.Status.HarborProject = "web"

	// No credentials Secret, so the Harbor client cannot even be built.
	r := newRegistryReconciler(t, newFakeClient(t, reg))

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err == nil {
		t.Fatal("handleDelete() error = nil, want the failure surfaced for backoff")
	}
	if res.RequeueAfter == 0 {
		t.Error("handleDelete() did not requeue; an unreachable Harbor must be retried")
	}
	found := false
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			found = true
		}
	}
	if !found {
		t.Error("finalizer was removed while the Harbor project may still exist")
	}
}

// harborStub serves the project lookup and the ownership marker that
// claimProjectName reads. owner is the Registry recorded against the project;
// empty means the project carries no marker at all.
func harborStub(t *testing.T, status int, owner string) *harbor.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2.0/labels") {
			w.WriteHeader(http.StatusOK)
			if owner == "" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"name":"registry.opencloud.wso2.com.owner","description":"` + owner + `"}]`))
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"project_id":7}`))
		}
	}))
	t.Cleanup(srv.Close)
	return harbor.NewClient(srv.URL, "u", "p")
}

// Project names are global: the first Registry to claim one holds it, and a
// second Registry anywhere must be refused rather than share the project.
func TestClaimProjectName_RefusesANameAlreadyInUse(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-b", "web")

	err := r.claimProjectName(context.Background(), harborStub(t, http.StatusOK, "team-a/web"), cr, "web")
	if err == nil {
		t.Fatal("claimProjectName() error = nil, want the taken name refused")
	}
	if !errors.Is(err, errProjectNameTaken) {
		t.Errorf("error = %v, want it to wrap errProjectNameTaken so the caller can fail terminally", err)
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error = %v, want it to name the conflicting project", err)
	}
	if !strings.Contains(err.Error(), "global") {
		t.Errorf("error = %v, want it to say names are global, or the refusal reads as a bug", err)
	}
}

func TestClaimProjectName_AllowsAFreeName(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-a", "web")

	if err := r.claimProjectName(context.Background(), harborStub(t, http.StatusNotFound, ""), cr, "web"); err != nil {
		t.Errorf("claimProjectName() error = %v, want a free name accepted", err)
	}
}

// Without this the second reconcile would reject the project the first one
// created, and no Registry would ever reach Ready.
func TestClaimProjectName_SkipsTheCheckForANameItAlreadyHolds(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-a", "web")
	cr.Status.HarborProject = "web"

	// Harbor would report the project exists; holding the claim must win.
	if err := r.claimProjectName(context.Background(), harborStub(t, http.StatusOK, "team-a/web"), cr, "web"); err != nil {
		t.Errorf("claimProjectName() error = %v, want the Registry's own project accepted", err)
	}
}

// A reconcile that died between creating the project and writing status finds
// its own project on the next pass. Without the ownership marker it would
// refuse that project forever and the Registry could never reach Ready.
func TestClaimProjectName_ResumesAgainstItsOwnProject(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-a", "web")

	cli := harborStub(t, http.StatusOK, "team-a/web")
	if err := r.claimProjectName(context.Background(), cli, cr, "web"); err != nil {
		t.Errorf("claimProjectName() error = %v, want the Registry's own project accepted", err)
	}
}

// A project with no marker was created outside the operator. Adopting it would
// hand this Registry credentials on images nobody has said belong to it.
func TestClaimProjectName_RefusesAnUnmarkedProject(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-a", "web")

	err := r.claimProjectName(context.Background(), harborStub(t, http.StatusOK, ""), cr, "web")
	if !errors.Is(err, errProjectNameTaken) {
		t.Fatalf("claimProjectName() error = %v, want an unmarked project refused", err)
	}
	if !strings.Contains(err.Error(), "created directly in Harbor") {
		t.Errorf("error = %v, want it to say the project was not created by the operator", err)
	}
}

// A Harbor that cannot answer says nothing about whether the name is free, so
// it must be retryable rather than reported to the user as "name taken".
func TestClaimProjectName_UnreachableHarborIsNotReportedAsTaken(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	cr := registryIn("team-a", "web")

	err := r.claimProjectName(context.Background(), harborStub(t, http.StatusInternalServerError, ""), cr, "web")
	if err == nil {
		t.Fatal("claimProjectName() error = nil, want the failure surfaced")
	}
	if errors.Is(err, errProjectNameTaken) {
		t.Error("an unreachable Harbor was reported as a taken name; this would fail the Registry terminally instead of retrying")
	}
}

func TestValidateProjectName(t *testing.T) {
	valid := []string{"web", "my-registry", "my.registry", "my_registry", "a1", "team-a.web"}
	for _, name := range valid {
		if err := validateProjectName(name); err != nil {
			t.Errorf("validateProjectName(%q) error = %v, want it accepted", name, err)
		}
	}

	// "a--b" is a legal Kubernetes object name but not a legal Harbor project
	// name, which is why this check is not a formality.
	invalid := []string{"a--b", "-web", "web-", ".web", "Web", "", strings.Repeat("a", maxProjectNameLen+1)}
	for _, name := range invalid {
		if err := validateProjectName(name); err == nil {
			t.Errorf("validateProjectName(%q) error = nil, want it rejected", name)
		}
	}
}

// harborProjectStub serves what deleteHarborProject reads: the project lookup,
// its ownership marker, an empty repository listing, and the delete itself. It
// records every request so a test can assert what was and was not called.
func harborProjectStub(t *testing.T, owner string) (baseURL string, requests *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/labels"):
			w.WriteHeader(http.StatusOK)
			if owner == "" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"name":"registry.opencloud.wso2.com.owner","description":"` + owner + `"}]`))
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"project_id":7}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &seen
}

// deleteReconciler is a reconciler pointed at stubURL with credentials in place,
// for the finalizer path.
func deleteReconciler(t *testing.T, stubURL string, objs ...client.Object) *RegistryReconciler {
	t.Helper()
	objs = append(objs, harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("admin"),
		config.HarborPasswordKey: []byte("s3cret"),
	}))
	r := newRegistryReconciler(t, newFakeClient(t, objs...))
	r.HarborCfg.URL = stubURL
	return r
}

func called(requests []string, want string) bool {
	for _, req := range requests {
		if req == want {
			return true
		}
	}
	return false
}

func TestDeleteHarborProject_DeletesAProjectItOwns(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Status.HarborProject = "web"
	url, requests := harborProjectStub(t, "acme-project-1/web")

	r := deleteReconciler(t, url, reg)
	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	if !called(*requests, "DELETE /api/v2.0/projects/web") {
		t.Errorf("requests = %v, want the project this Registry owns to be deleted", *requests)
	}
}

// A name recorded in status can be held by a different project later: one
// deleted and recreated in Harbor belongs to whoever created it, and deleting it
// would destroy another tenant's images.
func TestDeleteHarborProject_LeavesAProjectItNoLongerOwnsAlone(t *testing.T) {
	for _, owner := range []string{"team-b/web", ""} {
		t.Run("owner="+owner, func(t *testing.T) {
			reg := registryIn("acme-project-1", "web")
			reg.Status.HarborProject = "web"
			url, requests := harborProjectStub(t, owner)

			r := deleteReconciler(t, url, reg)
			if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
				t.Fatalf("deleteHarborProject() error = %v, want the finalizer released", err)
			}
			if called(*requests, "DELETE /api/v2.0/projects/web") {
				t.Errorf("requests = %v, want no delete against a project owned by %q", *requests, owner)
			}
		})
	}
}

// Marking is only allowed on a project this reconcile created. Adopting an
// unmarked project that was already there would hand this Registry a robot
// account and a quota on images created outside the operator.
func TestEnsureOwnership(t *testing.T) {
	cases := []struct {
		name    string
		owner   string
		created bool
		wantErr error
	}{
		{"marks a project it just created", "", true, nil},
		{"refuses an unmarked project it did not create", "", false, errProjectNameTaken},
		{"accepts its own marker", "acme-project-1/web", false, nil},
		{"refuses a foreign marker", "team-b/web", false, errProjectNameTaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistryReconciler(t, newFakeClient(t))
			cr := registryIn("acme-project-1", "web")

			err := r.ensureOwnership(context.Background(), harborStub(t, http.StatusOK, tc.owner), cr, 7, "web", tc.created)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("ensureOwnership() error = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("ensureOwnership() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// harborRobotStub answers the robot-account creation ensureCredential makes.
func harborRobotStub(t *testing.T) (cli *harbor.Client, requests *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"robot$web+pull-web","secret":"fresh"}`))
	}))
	t.Cleanup(srv.Close)
	return harbor.NewClient(srv.URL, "admin", "s3cret"), &seen
}

// docker and the kubelet match a credential by host, so a Secret still keyed to
// an address the operator no longer drives authenticates nothing.
func TestEnsureCredential_ReplacesACredentialKeyedToAnotherHost(t *testing.T) {
	cr := registryIn("acme-project-1", "web")
	stale, err := dockerConfigJSON("https://old.example.com", "robot$web+pull-web", "stale")
	if err != nil {
		t.Fatalf("dockerConfigJSON() error = %v", err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: stale},
	}
	r := newRegistryReconciler(t, newFakeClient(t, cr, sec))
	cli, requests := harborRobotStub(t)

	if err := r.ensureCredential(context.Background(), cr, cli, "web",
		"https://new.example.com", "web-pull", "pull-web", harbor.AccessPull); err != nil {
		t.Fatalf("ensureCredential() error = %v", err)
	}
	if !called(*requests, "POST /api/v2.0/robots") {
		t.Errorf("requests = %v, want a robot minted for the current address", *requests)
	}

	var updated corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: "web-pull"}, &updated); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if got := credentialHost(&updated); got != "new.example.com" {
		t.Errorf("credential host = %q, want new.example.com", got)
	}
}

// The steady state must not mint a new robot on every reconcile: a credential
// already keyed to the current address is left exactly as it is.
func TestEnsureCredential_LeavesACurrentCredentialAlone(t *testing.T) {
	cr := registryIn("acme-project-1", "web")
	current, err := dockerConfigJSON(testHarborURL, "robot$web+pull-web", "keep")
	if err != nil {
		t.Fatalf("dockerConfigJSON() error = %v", err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: current},
	}
	r := newRegistryReconciler(t, newFakeClient(t, cr, sec))
	cli, requests := harborRobotStub(t)

	if err := r.ensureCredential(context.Background(), cr, cli, "web",
		testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
		t.Fatalf("ensureCredential() error = %v", err)
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %v, want none for a credential already keyed to this address", *requests)
	}

	var unchanged corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: "web-pull"}, &unchanged); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if string(unchanged.Data[corev1.DockerConfigJsonKey]) != string(current) {
		t.Error("credential was rewritten although its host still matches")
	}
}

func TestCredentialHost(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"reads the single auths entry", []byte(`{"auths":{"registry.example.com":{"username":"u"}}}`), "registry.example.com"},
		{"unreadable JSON reports no host", []byte(`not json`), ""},
		{"absent entry reports no host", []byte(`{"auths":{}}`), ""},
		{"several entries report no host", []byte(`{"auths":{"a.example.com":{},"b.example.com":{}}}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sec := &corev1.Secret{Data: map[string][]byte{corev1.DockerConfigJsonKey: tc.data}}
			if got := credentialHost(sec); got != tc.want {
				t.Errorf("credentialHost() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A Secret's type is immutable, so one of the wrong type has to be replaced:
// docker config bytes inside an Opaque Secret are read by nothing.
func TestEnsureCredential_ReplacesASecretOfTheWrongType(t *testing.T) {
	cr := registryIn("acme-project-1", "web")
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"username": []byte("robot$web+pull-web")},
	}
	r := newRegistryReconciler(t, newFakeClient(t, cr, sec))
	cli, _ := harborRobotStub(t)

	if err := r.ensureCredential(context.Background(), cr, cli, "web",
		testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
		t.Fatalf("ensureCredential() error = %v", err)
	}

	var replaced corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: "web-pull"}, &replaced); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if replaced.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("Secret type = %q, want %q", replaced.Type, corev1.SecretTypeDockerConfigJson)
	}
	if credentialHost(&replaced) != dockerConfigHost(testHarborURL) {
		t.Errorf("credential host = %q, want %q", credentialHost(&replaced), dockerConfigHost(testHarborURL))
	}
}

// Creating a project and marking it are two Harbor calls. A reconcile
// interrupted between them leaves an unmarked project that the operator would
// otherwise refuse as somebody else's, stranding the Registry at Failed with
// its own project blocking its own name. status.pendingProject is the evidence
// that closes that gap.
func TestInterruptedCreation_RecognisesItsOwnUnmarkedProject(t *testing.T) {
	cr := registryIn("acme-project-1", "web")
	cr.Status.PendingProject = "web"
	r := newRegistryReconciler(t, newFakeClient(t))
	cli := harborStub(t, http.StatusOK, "") // the project exists, unmarked

	if err := r.claimProjectName(context.Background(), cli, cr, "web"); err != nil {
		t.Fatalf("claimProjectName() error = %v, want the interrupted creation resumed", err)
	}
	if err := r.ensureOwnership(context.Background(), cli, cr, 7, "web", false); err != nil {
		t.Fatalf("ensureOwnership() error = %v, want the project marked on resume", err)
	}
}

// The intent is specific to one name: it must not become a way to adopt any
// unmarked project that happens to be there.
func TestInterruptedCreation_DoesNotAdoptAnotherName(t *testing.T) {
	cr := registryIn("acme-project-1", "web")
	cr.Status.PendingProject = "other"
	r := newRegistryReconciler(t, newFakeClient(t))
	cli := harborStub(t, http.StatusOK, "")

	if err := r.claimProjectName(context.Background(), cli, cr, "web"); !errors.Is(err, errProjectNameTaken) {
		t.Fatalf("claimProjectName() error = %v, want errProjectNameTaken", err)
	}
	if err := r.ensureOwnership(context.Background(), cli, cr, 7, "web", false); !errors.Is(err, errProjectNameTaken) {
		t.Fatalf("ensureOwnership() error = %v, want errProjectNameTaken", err)
	}
}

// Deleting such a Registry must free the name, or the project it created stays
// in Harbor and blocks every later Registry from using that name.
func TestDeleteHarborProject_ReclaimsAnEmptyPendingProject(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Status.PendingProject = "web" // interrupted before the marker was written
	url, requests := harborProjectStub(t, "")

	r := deleteReconciler(t, url, reg)
	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	if !called(*requests, "DELETE /api/v2.0/projects/web") {
		t.Errorf("requests = %v, want the empty project this Registry created reclaimed", *requests)
	}
}

// An unmarked project holding images is only probably this Registry's. Nothing
// proves those images are this tenant's, so the name stays blocked rather than
// the images being destroyed.
func TestDeleteHarborProject_LeavesAnUnmarkedProjectHoldingImages(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Status.PendingProject = "web"

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2.0/labels"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"web/app"}]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"project_id":7}`))
		}
	}))
	t.Cleanup(srv.Close)

	r := deleteReconciler(t, srv.URL, reg)
	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	for _, req := range seen {
		if strings.HasPrefix(req, "DELETE") {
			t.Errorf("requests = %v, want nothing deleted from an unmarked project holding images", seen)
			break
		}
	}
}
