package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

// An unreachable Harbor must keep the finalizer rather than release it, or
// images a user asked to destroy are silently left behind.
func TestHandleDelete_KeepsFinalizerWhenHarborIsUnreachable(t *testing.T) {
	reg := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}

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

// operatorUserID is the Harbor account the stubs report the operator as, and the
// creator of the projects they serve.
const operatorUserID = 3

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

// registryWithUID is a Registry as the API server hands one back: with a UID,
// which is what its project name is derived from.
func registryWithUID(namespace, name, uid string) *registryv1alpha1.Registry {
	cr := registryIn(namespace, name)
	cr.UID = types.UID(uid)
	return cr
}

// The project name is a property of one object: derived from its own name and
// UID, so it is stable for that object and reachable without reading status.
func TestHarborProjectName_IsDerivedFromTheRegistryAndItsUID(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")

	first, err := harborProjectName(cr)
	if err != nil {
		t.Fatalf("harborProjectName() error = %v", err)
	}
	second, err := harborProjectName(cr)
	if err != nil {
		t.Fatalf("harborProjectName() error = %v", err)
	}
	if first != second {
		t.Errorf("harborProjectName() = %q then %q, want the same name every time", first, second)
	}
	if !strings.HasPrefix(first, "web-") {
		t.Errorf("harborProjectName() = %q, want the Registry's own name as its prefix so a user recognises it", first)
	}
	if got := len(first) - len("web-"); got != projectNameDigestLen {
		t.Errorf("digest is %d characters, want %d", got, projectNameDigestLen)
	}
}

// Two Registries sharing a name cannot share a project. This is what removes
// the global name conflict: neither has to claim the name from the other.
func TestHarborProjectName_DiffersForEveryRegistry(t *testing.T) {
	a := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	b := registryWithUID("beta-project-1", "web", "9c2d5e77-1a3b-4c8d-8e5f-2b6a9c0d3e17")
	// A Registry deleted and recreated under the same name is a new object with
	// a new UID, and so a new project.
	recreated := registryWithUID("acme-project-1", "web", "b41f7e4a-9ace-4d5f-9a65-ec32305a9e0e")

	names := map[string]string{}
	for _, cr := range []*registryv1alpha1.Registry{a, b, recreated} {
		name, err := harborProjectName(cr)
		if err != nil {
			t.Fatalf("harborProjectName() error = %v", err)
		}
		if owner, clash := names[name]; clash {
			t.Fatalf("%s/%s and %s resolve to the same project %q", cr.Namespace, cr.Name, owner, name)
		}
		names[name] = cr.Namespace + "/" + cr.Name
	}
}

// Kubernetes accepts object names Harbor will not take, and a Registry's name
// cannot be changed, so this has to be reported rather than retried.
func TestHarborProjectName_RejectsWhatHarborCannotHold(t *testing.T) {
	cases := []struct {
		name     string
		crName   string
		uid      string
		wantHint string
	}{
		{"consecutive separators", "my--app", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93", "valid Harbor project name"},
		{"trailing separator", "app-", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93", "valid Harbor project name"},
		{"longer than Harbor allows", strings.Repeat("a", maxProjectNameLen), "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93", "at most"},
		{"no UID to derive from", "web", "", "no UID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := registryWithUID("acme-project-1", tc.crName, tc.uid)

			name, err := harborProjectName(cr)
			if err == nil {
				t.Fatalf("harborProjectName() = %q, want an error", name)
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantHint)
			}
		})
	}
}

// A name Harbor could never have held was never created under it either, so the
// finalizer has nothing to wait for.
func TestDeleteHarborProject_ReleasesWhenNoNameCanBeDerived(t *testing.T) {
	reg := registryIn("acme-project-1", "web") // no UID
	r := newRegistryReconciler(t, newFakeClient(t, reg))

	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Errorf("deleteHarborProject() error = %v, want nil when no project name can be derived", err)
	}
}

// secretFor reads the credentials Secret a Registry owns.
func secretFor(t *testing.T, r *RegistryReconciler, cr *registryv1alpha1.Registry, name string) *corev1.Secret {
	t.Helper()
	var sec corev1.Secret
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: name}, &sec); err != nil {
		t.Fatalf("get Secret %s: %v", name, err)
	}
	return &sec
}

// The whole job: mint one robot account, write its credentials to a Secret the
// Registry owns, in the shape a pod and docker login read without conversion.
func TestEnsureCredential_MintsTheRobotAndWritesTheSecret(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	r := newRegistryReconciler(t, newFakeClient(t, cr))
	cli, requests := harborRobotStub(t)

	if err := r.ensureCredential(context.Background(), cr, cli, 7, "web-30cf39a6",
		testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
		t.Fatalf("ensureCredential() error = %v", err)
	}
	if !called(*requests, "POST /api/v2.0/robots") {
		t.Errorf("requests = %v, want a robot account minted", *requests)
	}

	sec := secretFor(t, r, cr, "web-pull")
	if sec.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("Secret type = %q, want %q", sec.Type, corev1.SecretTypeDockerConfigJson)
	}
	if !metav1.IsControlledBy(sec, cr) {
		t.Error("Secret has no controller reference to its Registry, so it would outlive it")
	}
	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatalf("unmarshal docker config: %v", err)
	}
	entry, ok := cfg.Auths[dockerConfigHost(testHarborURL)]
	if !ok {
		t.Fatalf("auths keys = %v, want the bare host of %s", keysOf(cfg.Auths), testHarborURL)
	}
	if entry.Username != "robot$web+pull-web" {
		t.Errorf("username = %q, want the minted robot", entry.Username)
	}
}

// Minting is once-only: a second pass must not rotate a credential that copies
// on other clusters are already using.
func TestEnsureCredential_DoesNotMintAgainForItsOwnSecret(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	r := newRegistryReconciler(t, newFakeClient(t, cr))
	cli, requests := harborRobotStub(t)

	for i := 0; i < 3; i++ {
		if err := r.ensureCredential(context.Background(), cr, cli, 7, "web-30cf39a6",
			testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
			t.Fatalf("ensureCredential() pass %d error = %v", i+1, err)
		}
	}
	if len(*requests) != 1 {
		t.Errorf("requests = %v, want exactly one robot minted across repeated passes", *requests)
	}
}

// staleOnceReader misses Secrets on its first read, as a cache that has not yet
// seen a Secret created moments ago does.
type staleOnceReader struct {
	client.Reader
	missed bool
}

func (s *staleOnceReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isSecret := obj.(*corev1.Secret); isSecret && !s.missed {
		s.missed = true
		return apierrors.NewNotFound(corev1.Resource("secrets"), key.Name)
	}
	return s.Reader.Get(ctx, key, obj, opts...)
}

// A read that misses an existing Secret mints again, which replaces the robot and
// revokes the token that Secret holds. The Secret must end up with the new token.
func TestEnsureCredential_StoresTheNewTokenWhenTheSecretAppearedMeanwhile(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	fc := newFakeClient(t, cr)
	r := newRegistryReconciler(t, fc)
	old := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)},
	}
	if err := controllerutil.SetControllerReference(cr, old, r.Scheme); err != nil {
		t.Fatal(err)
	}
	if err := fc.Create(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	r.APIReader = &staleOnceReader{Reader: fc}
	cli, _ := harborRobotStub(t)

	if err := r.ensureCredential(context.Background(), cr, cli, 7, "web-30cf39a6",
		testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
		t.Fatalf("ensureCredential() error = %v", err)
	}

	var cfg struct {
		Auths map[string]struct {
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatalf("unmarshal docker config: %v", err)
	}
	if got := cfg.Auths[dockerConfigHost(testHarborURL)].Password; got != "fresh" {
		t.Errorf("password = %q, want the token minted in this pass", got)
	}
}

// A Secret at that name which this Registry does not own belongs to whoever
// created it. Taking it over or deleting it would destroy something the
// operator never made, so the collision is reported and nothing is touched.
func TestEnsureCredential_RefusesASecretItDoesNotOwn(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	theirs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"note": []byte("not the operator's")},
	}
	r := newRegistryReconciler(t, newFakeClient(t, cr, theirs))
	cli, requests := harborRobotStub(t)

	err := r.ensureCredential(context.Background(), cr, cli, 7, "web-30cf39a6",
		testHarborURL, "web-pull", "pull-web", harbor.AccessPull)
	if !errors.Is(err, errSecretNameTaken) {
		t.Fatalf("ensureCredential() error = %v, want errSecretNameTaken", err)
	}
	if len(*requests) != 0 {
		t.Errorf("requests = %v, want no robot minted for a Secret this Registry cannot write", *requests)
	}

	kept := secretFor(t, r, cr, "web-pull")
	if string(kept.Data["note"]) != "not the operator's" || kept.Type != corev1.SecretTypeOpaque {
		t.Error("the existing Secret was modified; it belongs to whoever created it")
	}
}

// --- an in-memory Harbor, for tests that drive the whole reconcile loop ---

// harborFake answers the Harbor calls a reconcile makes, keeping enough state
// to tell a first pass from a repeat one. It records every request so a test
// can assert what the operator did rather than only what it ended up with.
type harborFake struct {
	mu           sync.Mutex
	projects     map[string]int64    // name -> id
	quota        map[int64]int64     // project id -> storage limit
	robots       map[string]int64    // full robot name -> id
	robotProject map[string]int64    // full robot name -> project id
	repos        map[string][]string // project -> repositories
	requests     []string
	failPath     string // every call under this path prefix fails
	nextID       int64
	down         bool // every call fails, as an unreachable Harbor does
	URL          string
}

func newHarborFake(t *testing.T) *harborFake {
	t.Helper()
	h := &harborFake{
		projects:     map[string]int64{},
		quota:        map[int64]int64{},
		robots:       map[string]int64{},
		robotProject: map[string]int64{},
		repos:        map[string][]string{},
		nextID:       1,
	}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	h.URL = srv.URL
	return h
}

func (h *harborFake) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, r.Method+" "+r.URL.Path)

	if h.down || (h.failPath != "" && strings.HasPrefix(r.URL.Path, h.failPath)) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/v2.0/users/current":
		writeJSON(w, http.StatusOK, `{"user_id":3,"username":"admin"}`)

	case p == "/api/v2.0/projects" && r.Method == http.MethodPost:
		var body struct {
			ProjectName  string `json:"project_name"`
			StorageLimit int64  `json:"storage_limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, exists := h.projects[body.ProjectName]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		id := h.nextID
		h.nextID++
		h.projects[body.ProjectName] = id
		h.quota[id] = body.StorageLimit
		w.WriteHeader(http.StatusCreated)

	case strings.HasPrefix(p, "/api/v2.0/projects/") && strings.HasSuffix(p, "/repositories"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v2.0/projects/"), "/repositories")
		if _, ok := h.projects[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		out := make([]string, 0, len(h.repos[name]))
		for _, repo := range h.repos[name] {
			out = append(out, fmt.Sprintf(`{"name":%q}`, name+"/"+repo))
		}
		writeJSON(w, http.StatusOK, "["+strings.Join(out, ",")+"]")

	case strings.HasPrefix(p, "/api/v2.0/projects/"):
		rest := strings.TrimPrefix(p, "/api/v2.0/projects/")
		if name, repo, found := strings.Cut(rest, "/repositories/"); found {
			repo, _ = url.PathUnescape(repo)
			kept := []string{}
			for _, existing := range h.repos[name] {
				if existing != repo {
					kept = append(kept, existing)
				}
			}
			h.repos[name] = kept
			w.WriteHeader(http.StatusOK)
			return
		}
		id, ok := h.projects[rest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			delete(h.projects, rest)
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"project_id":%d}`, id))

	case p == "/api/v2.0/quotas":
		id, _ := strconv.ParseInt(r.URL.Query().Get("reference_id"), 10, 64)
		writeJSON(w, http.StatusOK, fmt.Sprintf(`[{"id":%d,"hard":{"storage":%d}}]`, id, h.quota[id]))

	case strings.HasPrefix(p, "/api/v2.0/quotas/") && r.Method == http.MethodPut:
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/api/v2.0/quotas/"), 10, 64)
		var body struct {
			Hard struct {
				Storage int64 `json:"storage"`
			} `json:"hard"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.quota[id] = body.Hard.Storage
		w.WriteHeader(http.StatusOK)

	case p == "/api/v2.0/robots" && r.Method == http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Permissions []struct {
				Namespace string `json:"namespace"`
			} `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		full := "robot$" + body.Permissions[0].Namespace + "+" + body.Name
		if _, exists := h.robots[full]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		id := h.nextID
		h.nextID++
		h.robots[full] = id
		h.robotProject[full] = h.projects[body.Permissions[0].Namespace]
		writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"id":%d,"name":%q,"secret":"s3cret-%d"}`, id, full, id))

	case p == "/api/v2.0/robots" && r.Method == http.MethodGet:
		// Harbor 2.15.2 lists only system-level robots unless the query scopes
		// the listing to a project, and rejects Level=project without an id.
		q := r.URL.Query().Get("q")
		if !strings.Contains(q, "Level=project") {
			writeJSON(w, http.StatusOK, "[]")
			return
		}
		if !strings.Contains(q, "ProjectID=") {
			writeJSON(w, http.StatusBadRequest, `{"errors":[{"code":"BAD_REQUEST","message":"must with project ID when to query project robots"}]}`)
			return
		}
		out := []string{}
		for name, id := range h.robots {
			if h.robotProject[name] == h.projectOfQuery(q) {
				out = append(out, fmt.Sprintf(`{"id":%d,"name":%q}`, id, name))
			}
		}
		writeJSON(w, http.StatusOK, "["+strings.Join(out, ",")+"]")

	case strings.HasPrefix(p, "/api/v2.0/robots/") && r.Method == http.MethodDelete:
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/api/v2.0/robots/"), 10, 64)
		for name, known := range h.robots {
			if known == id {
				delete(h.robots, name)
				delete(h.robotProject, name)
			}
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// projectOfQuery reads ProjectID=<n> out of Harbor's q parameter.
func (h *harborFake) projectOfQuery(q string) int64 {
	_, rest, found := strings.Cut(q, "ProjectID=")
	if !found {
		return 0
	}
	if cut, _, more := strings.Cut(rest, ","); more {
		rest = cut
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	return id
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// countRequests returns how many recorded requests match a method and path prefix.
func (h *harborFake) countRequests(method, prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, req := range h.requests {
		if strings.HasPrefix(req, method+" "+prefix) {
			n++
		}
	}
	return n
}

func (h *harborFake) projectID(name string) (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.projects[name]
	return id, ok
}

func (h *harborFake) storage(id int64) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quota[id]
}

// deleteProjectOutOfBand is an administrator removing the project in Harbor.
// Harbor deletes a project's robots along with it.
func (h *harborFake) deleteProjectOutOfBand(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.projects[name]
	delete(h.projects, name)
	delete(h.repos, name)
	for robot, project := range h.robotProject {
		if project == id {
			delete(h.robotProject, robot)
			delete(h.robots, robot)
		}
	}
}

func (h *harborFake) robotCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.robots)
}

func (h *harborFake) setRepos(project string, repos ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repos[project] = repos
}

func (h *harborFake) setFailPath(prefix string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failPath = prefix
}

// replaceProject stands in for somebody deleting the project and creating
// another under the same name, which Harbor gives a new id.
func (h *harborFake) replaceProject(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.projects[name] = h.nextID
	h.nextID++
}

func (h *harborFake) setDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down = down
}

// reconcilerFor wires a reconciler to a fake Harbor, with the operator's
// credentials in place and the Registry already admitted.
func reconcilerFor(t *testing.T, h *harborFake, objs ...client.Object) (*RegistryReconciler, client.WithWatch) {
	t.Helper()
	objs = append(objs, harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("admin"),
		config.HarborPasswordKey: []byte("s3cret"),
	}))
	fc := newFakeClient(t, objs...)
	r := newRegistryReconciler(t, fc)
	r.HarborCfg.URL = h.URL
	return r, fc
}

// reconcileUntilSettled runs Reconcile until it stops asking to be requeued
// immediately, which is how the finalizer pass hands over to the real work.
func reconcileUntilSettled(t *testing.T, r *RegistryReconciler, cr *registryv1alpha1.Registry) (ctrl.Result, error) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}}
	var res ctrl.Result
	var err error
	for i := 0; i < 5; i++ {
		res, err = r.Reconcile(context.Background(), req)
		if err != nil || !res.Requeue {
			return res, err
		}
	}
	t.Fatal("Reconcile kept asking for an immediate requeue")
	return res, err
}

func registryState(t *testing.T, r *RegistryReconciler, cr *registryv1alpha1.Registry) *registryv1alpha1.Registry {
	t.Helper()
	var fresh registryv1alpha1.Registry
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
	if err := r.Get(context.Background(), key, &fresh); err != nil {
		t.Fatalf("get Registry: %v", err)
	}
	return &fresh
}

// --- the reconcile loop itself ---

// One apply must produce everything a user was promised: a private project
// sized to the plan, two scoped credentials, and a status that names them.
func TestReconcile_ProvisionsFromScratch(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	projectName, err := harborProjectName(cr)
	if err != nil {
		t.Fatalf("harborProjectName() error = %v", err)
	}

	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	id, ok := h.projectID(projectName)
	if !ok {
		t.Fatalf("Harbor holds no project %q", projectName)
	}
	if want := int64(5) * 1024 * 1024 * 1024; h.storage(id) != want {
		t.Errorf("quota = %d, want %d for the starter plan", h.storage(id), want)
	}

	got := registryState(t, r, cr)
	if got.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q (message %q)", got.Status.Phase, phaseReady, got.Status.Message)
	}
	if got.Status.HarborProject != projectName {
		t.Errorf("status.harborProject = %q, want %q", got.Status.HarborProject, projectName)
	}
	if got.Status.RegistryURL != h.URL {
		t.Errorf("status.registryURL = %q, want %q", got.Status.RegistryURL, h.URL)
	}
	if got.Status.PullSecretName != "web-pull" || got.Status.PushSecretName != "web-push" {
		t.Errorf("status secrets = %q/%q, want web-pull/web-push", got.Status.PullSecretName, got.Status.PushSecretName)
	}
	if !controllerutil.ContainsFinalizer(got, registryFinalizer) {
		t.Error("finalizer missing; deleting this Registry would leave its project behind")
	}
	for _, name := range []string{"web-pull", "web-push"} {
		sec := secretFor(t, r, cr, name)
		if sec.Type != corev1.SecretTypeDockerConfigJson {
			t.Errorf("Secret %s type = %q, want dockerconfigjson", name, sec.Type)
		}
	}
	if n := h.countRequests("POST", "/api/v2.0/robots"); n != 2 {
		t.Errorf("minted %d robots, want 2 — one pull, one push", n)
	}
}

// Every pass re-asserts the same state, so repeating it must create nothing
// new: re-minting a robot would invalidate credentials already distributed.
func TestReconcile_IsIdempotent(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)

	for i := 0; i < 3; i++ {
		if _, err := reconcileUntilSettled(t, r, cr); err != nil {
			t.Fatalf("Reconcile() pass %d error = %v", i+1, err)
		}
	}

	if n := h.countRequests("POST", "/api/v2.0/robots"); n != 2 {
		t.Errorf("minted %d robots across three passes, want 2", n)
	}
	if n := h.countRequests("PUT", "/api/v2.0/quotas/"); n != 0 {
		t.Errorf("wrote the quota %d times, want 0 — it already held the right value", n)
	}
	if got := registryState(t, r, cr); got.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q", got.Status.Phase, phaseReady)
	}
}

// Changing the plan is the one thing that resizes a project, and it takes
// effect through the same convergence that corrects drift.
func TestReconcile_PlanChangeConvergesTheQuota(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	live := registryState(t, r, cr)
	live.Spec.Plan = "professional"
	if err := r.Update(context.Background(), live); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	if _, err := reconcileUntilSettled(t, r, live); err != nil {
		t.Fatalf("Reconcile() after plan change error = %v", err)
	}

	projectName, _ := harborProjectName(cr)
	id, _ := h.projectID(projectName)
	if want := int64(20) * 1024 * 1024 * 1024; h.storage(id) != want {
		t.Errorf("quota = %d, want %d after moving to professional", h.storage(id), want)
	}
	if n := h.countRequests("POST", "/api/v2.0/robots"); n != 2 {
		t.Errorf("minted %d robots, want 2 — a plan change must not rotate credentials", n)
	}
}

// A spec the operator cannot act on is terminal: retrying resolves nothing, and
// leaving it Provisioning would hide the reason.
func TestReconcile_UnknownPlanIsTerminal(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	cr.Spec.Plan = "gigantic"
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)

	_, err := reconcileUntilSettled(t, r, cr)
	if !errors.Is(err, reconcile.TerminalError(nil)) {
		t.Fatalf("Reconcile() error = %v, want a terminal error so the manager stops requeueing", err)
	}
	got := registryState(t, r, cr)
	if got.Status.Phase != phaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, phaseFailed)
	}
	if !strings.Contains(got.Status.Message, "gigantic") {
		t.Errorf("message = %q, want it to name the rejected plan", got.Status.Message)
	}
	if n := h.countRequests("POST", "/api/v2.0/projects"); n != 0 {
		t.Errorf("made %d project calls, want 0 before the spec is usable", n)
	}
}

// An unreachable Harbor is not the user's fault: the Registry must keep trying
// rather than settle into Failed.
func TestReconcile_UnreachableHarborIsTransient(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	h.setDown(true)
	r, _ := reconcilerFor(t, h, cr)

	_, err := reconcileUntilSettled(t, r, cr)
	if err == nil {
		t.Fatal("Reconcile() error = nil, want the failure surfaced so the manager backs off")
	}
	if errors.Is(err, reconcile.TerminalError(nil)) {
		t.Errorf("Reconcile() error = %v, want a retryable error rather than a terminal one", err)
	}
	got := registryState(t, r, cr)
	if got.Status.Phase != phaseProvisioning {
		t.Errorf("phase = %q, want %q while Harbor is merely down", got.Status.Phase, phaseProvisioning)
	}

	// Recovery needs no intervention.
	h.setDown(false)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() after recovery error = %v", err)
	}
	if got := registryState(t, r, cr); got.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q once Harbor answers again", got.Status.Phase, phaseReady)
	}
}

// A Secret this Registry does not own cannot be written to, and no amount of
// retrying changes that, so the Registry reports it and stops.
func TestReconcile_SecretNameCollisionIsTerminal(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	theirs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"note": []byte("not the operator's")},
	}
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr, theirs)

	_, err := reconcileUntilSettled(t, r, cr)
	if !errors.Is(err, reconcile.TerminalError(nil)) {
		t.Fatalf("Reconcile() error = %v, want a terminal error: retrying cannot free the name", err)
	}
	got := registryState(t, r, cr)
	if got.Status.Phase != phaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, phaseFailed)
	}
	if !strings.Contains(got.Status.Message, "web-pull") {
		t.Errorf("message = %q, want it to name the Secret in the way", got.Status.Message)
	}
	if n := h.countRequests("POST", "/api/v2.0/robots"); n != 0 {
		t.Errorf("minted %d robots, want 0 for a credential it cannot store", n)
	}
	kept := secretFor(t, r, cr, "web-pull")
	if string(kept.Data["note"]) != "not the operator's" {
		t.Error("the existing Secret was modified")
	}
}

// Deleting a Registry destroys its project and everything in it, and only then
// releases the finalizer.
func TestReconcile_DeleteRemovesTheProjectThenTheFinalizer(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	projectName, _ := harborProjectName(cr)
	h.setRepos(projectName, "app", "sidecar")

	live := registryState(t, r, cr)
	if err := r.Delete(context.Background(), live); err != nil {
		t.Fatalf("delete Registry: %v", err)
	}
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() during deletion error = %v", err)
	}

	if _, ok := h.projectID(projectName); ok {
		t.Errorf("project %q still exists in Harbor after the Registry was deleted", projectName)
	}
	if n := h.countRequests("DELETE", "/api/v2.0/projects/"+projectName+"/repositories/"); n != 2 {
		t.Errorf("deleted %d repositories, want 2 — Harbor refuses to delete a non-empty project", n)
	}
	var gone registryv1alpha1.Registry
	err := r.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}, &gone)
	if !apierrors.IsNotFound(err) {
		t.Errorf("Registry still present with finalizers %v, want it released", gone.Finalizers)
	}
}

// patchStatus reads the latest object before writing, so a status update that
// races with another writer lands instead of failing the reconcile.
func TestPatchStatus_RetriesOnConflict(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	r := newRegistryReconciler(t, newFakeClient(t, cr))
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}

	// Another writer bumps the object between the read and the write.
	first := true
	err := r.patchStatus(context.Background(), key, func(s *registryv1alpha1.RegistryStatus) {
		if first {
			first = false
			live := registryState(t, r, cr)
			live.Labels = map[string]string{"touched": "by-someone-else"}
			_ = r.Update(context.Background(), live)
		}
		s.Phase = phaseReady
	})
	if err != nil {
		t.Fatalf("patchStatus() error = %v", err)
	}
	if got := registryState(t, r, cr); got.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q", got.Status.Phase, phaseReady)
	}
}

// Readiness must not cache a probe the kubelet cancelled: doing so would keep
// the pod NotReady for the whole TTL after Harbor recovered.
func TestCheckHarborAccess_DoesNotCacheACancelledProbe(t *testing.T) {
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.CheckHarborAccess(ctx); err == nil {
		t.Fatal("CheckHarborAccess() error = nil, want the cancellation reported")
	}
	if err := r.CheckHarborAccess(context.Background()); err != nil {
		t.Errorf("CheckHarborAccess() error = %v, want nil: the cancelled probe must not have been cached", err)
	}
}

// The id is recorded as soon as Harbor reports it, before anything later in the
// pass can fail. That is what keeps the window in which a project exists that
// status cannot identify down to a single call.
func TestReconcile_RecordsTheProjectIDBeforeLaterStepsCanFail(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	h.setFailPath("/api/v2.0/quotas") // the pass dies just after the project exists
	r, _ := reconcilerFor(t, h, cr)
	projectName, _ := harborProjectName(cr)

	if _, err := reconcileUntilSettled(t, r, cr); err == nil {
		t.Fatal("Reconcile() error = nil, want the quota failure surfaced")
	}
	got := registryState(t, r, cr)
	if got.Status.HarborProjectID == 0 {
		t.Fatal("status.harborProjectID = 0; the finalizer would not be able to identify the project")
	}

	// Deletion identifies it and cleans up, despite never having reached Ready.
	h.setFailPath("")
	if err := r.Delete(context.Background(), got); err != nil {
		t.Fatalf("delete Registry: %v", err)
	}
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() during deletion error = %v", err)
	}
	if _, ok := h.projectID(projectName); ok {
		t.Error("project left behind although status recorded its id")
	}
}

// Harbor never reuses a project id, so one that differs from the recorded id is
// somebody else's project wearing this Registry's name. Deleting it would
// destroy their repositories.
func TestDeleteHarborProject_LeavesAProjectWithADifferentID(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	projectName, _ := harborProjectName(cr)
	h.replaceProject(projectName) // deleted and recreated by somebody else

	live := registryState(t, r, cr)
	if err := r.deleteHarborProject(context.Background(), live, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v, want the finalizer released", err)
	}
	if _, ok := h.projectID(projectName); !ok {
		t.Error("a project this Registry did not create was deleted")
	}
	if n := h.countRequests("DELETE", "/api/v2.0/projects/"+projectName); n != 0 {
		t.Errorf("issued %d project deletes, want 0", n)
	}
}

// Without a recorded id nothing identifies the project, so the finalizer leaves
// it and says so rather than deleting whatever holds the name.
func TestDeleteHarborProject_LeavesAProjectItCannotIdentify(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	projectName, _ := harborProjectName(cr)
	h.replaceProject(projectName) // a project under the name, created by nobody we know

	if err := r.deleteHarborProject(context.Background(), cr, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	if _, ok := h.projectID(projectName); !ok {
		t.Error("project deleted although status recorded no id for it")
	}
}

// A project replaced while the Registry still exists must not be converged
// into: its quota and credentials belong to whoever created it.
func TestReconcile_RefusesAProjectThatWasReplaced(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	projectName, _ := harborProjectName(cr)
	h.replaceProject(projectName)

	_, err := reconcileUntilSettled(t, r, cr)
	if !errors.Is(err, reconcile.TerminalError(nil)) {
		t.Fatalf("Reconcile() error = %v, want a terminal error", err)
	}
	got := registryState(t, r, cr)
	if got.Status.Phase != phaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, phaseFailed)
	}
	if !strings.Contains(got.Status.Message, "created id") {
		t.Errorf("message = %q, want it to explain that the project was replaced", got.Status.Message)
	}
}

// The project name comes from the Registry and the id authorises the delete.
// Harbor refuses to remove a project that still holds repositories, so they go
// first.
func TestDeleteHarborProject_EmptiesThenDeletesTheProjectItCreated(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	projectName, _ := harborProjectName(cr)
	h.setRepos(projectName, "app")

	live := registryState(t, r, cr)
	if err := r.deleteHarborProject(context.Background(), live, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	if n := h.countRequests("DELETE", "/api/v2.0/projects/"+projectName+"/repositories/app"); n != 1 {
		t.Errorf("emptied the project %d times, want 1", n)
	}
	if _, ok := h.projectID(projectName); ok {
		t.Error("project still exists after its Registry was deleted")
	}
}

// Harbor reporting no such project is the answer, not a failure: there is
// nothing left to reclaim and the finalizer must be released.
func TestDeleteHarborProject_ReleasesWhenHarborHasNoProject(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	cr.Status.HarborProjectID = 42 // recorded, but the project is long gone
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)

	if err := r.deleteHarborProject(context.Background(), cr, testLogger()); err != nil {
		t.Errorf("deleteHarborProject() error = %v, want nil", err)
	}
}

// Uninstalling the operator leaves the CRD, the Registry objects and their
// Harbor projects in place, so reinstalling meets state it did not create in
// this process. It must adopt that state rather than provision over it: a new
// project would orphan the images, and a new robot would invalidate every
// credential already copied onto other clusters.
func TestReconcile_ReinstallAdoptsExistingState(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, fc := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	before := registryState(t, r, cr)
	projectName := before.Status.HarborProject
	projectID := before.Status.HarborProjectID
	pullBefore := secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey]
	robotsBefore := h.countRequests("POST", "/api/v2.0/robots")

	// The operator is removed and installed again: a new manager process, with
	// nothing remembered, meeting the same cluster and the same Harbor.
	reinstalled := newRegistryReconciler(t, fc)
	reinstalled.HarborCfg.URL = h.URL
	if _, err := reconcileUntilSettled(t, reinstalled, cr); err != nil {
		t.Fatalf("Reconcile() after reinstall error = %v", err)
	}

	after := registryState(t, reinstalled, cr)
	if after.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q after reinstall (message %q)", after.Status.Phase, phaseReady, after.Status.Message)
	}
	if after.Status.HarborProject != projectName || after.Status.HarborProjectID != projectID {
		t.Errorf("project = %q/%d, want the original %q/%d — a new project would orphan the images",
			after.Status.HarborProject, after.Status.HarborProjectID, projectName, projectID)
	}
	if got := h.countRequests("POST", "/api/v2.0/robots"); got != robotsBefore {
		t.Errorf("minted %d robots during reinstall, want none: every copied credential would stop working", got-robotsBefore)
	}
	if got := secretFor(t, reinstalled, cr, "web-pull").Data[corev1.DockerConfigJsonKey]; string(got) != string(pullBefore) {
		t.Error("the pull credential was rewritten during reinstall")
	}
	if id, ok := h.projectID(projectName); !ok || id != projectID {
		t.Errorf("Harbor project id = %d (exists %v), want the original %d", id, ok, projectID)
	}
}

// Deleting a credentials Secret is how a leaked credential is revoked: the
// operator mints a replacement, and Harbor's 409 against the robot left behind
// is resolved by replacing it. That recovery reaches Harbor through the robot
// listing, which returns nothing at all unless it is scoped to the project — so
// this is the test that proves the revocation procedure works end to end.
func TestReconcile_DeletingASecretRevokesAndReplacesTheCredential(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	before := secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey]

	// The compromised credential is destroyed; its robot stays in Harbor.
	if err := r.Delete(context.Background(), secretFor(t, r, cr, "web-pull")); err != nil {
		t.Fatalf("delete Secret: %v", err)
	}
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() after revocation error = %v", err)
	}

	after := secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey]
	if string(after) == string(before) {
		t.Error("the credential is unchanged; the leaked one would still be valid")
	}
	if got := registryState(t, r, cr); got.Status.Phase != phaseReady {
		t.Errorf("phase = %q, want %q (message %q)", got.Status.Phase, phaseReady, got.Status.Message)
	}
	// The orphan must be gone, not merely shadowed: one robot per credential.
	if n := h.countRequests("DELETE", "/api/v2.0/robots/"); n != 1 {
		t.Errorf("deleted %d robots, want 1 — the leaked robot must be revoked in Harbor", n)
	}
	if n := h.robotCount(); n != 2 {
		t.Errorf("Harbor holds %d robots for this Registry, want 2", n)
	}
}

// An administrator deleting the project in Harbor takes its robots with it, so
// the credentials in the Secrets authenticate nothing. Recreating the project
// without reissuing them would report Ready while every pull and push 401s.
func TestReconcile_ProjectDeletedOutOfBandReissuesCredentials(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	h := newHarborFake(t)
	r, _ := reconcilerFor(t, h, cr)
	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	before := registryState(t, r, cr)
	pullBefore := secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey]
	pushBefore := secretFor(t, r, cr, "web-push").Data[corev1.DockerConfigJsonKey]

	h.deleteProjectOutOfBand(before.Status.HarborProject)

	if _, err := reconcileUntilSettled(t, r, cr); err != nil {
		t.Fatalf("Reconcile() after the project was deleted error = %v", err)
	}

	after := registryState(t, r, cr)
	if after.Status.Phase != phaseReady {
		t.Fatalf("phase = %q, want %q (message %q)", after.Status.Phase, phaseReady, after.Status.Message)
	}
	if after.Status.HarborProjectID == before.Status.HarborProjectID {
		t.Errorf("project id = %d, want a new one after the project was recreated", after.Status.HarborProjectID)
	}
	if string(secretFor(t, r, cr, "web-pull").Data[corev1.DockerConfigJsonKey]) == string(pullBefore) {
		t.Error("pull credential unchanged; it names a robot Harbor deleted with the old project")
	}
	if string(secretFor(t, r, cr, "web-push").Data[corev1.DockerConfigJsonKey]) == string(pushBefore) {
		t.Error("push credential unchanged; it names a robot Harbor deleted with the old project")
	}
	if n := h.robotCount(); n != 2 {
		t.Errorf("Harbor holds %d robots, want 2 minted in the new project", n)
	}
}

// Reissuing must not reach a Secret this Registry does not control, for the
// same reason writing one does not.
func TestDeleteCredentialSecrets_LeavesSecretsItDoesNotOwn(t *testing.T) {
	cr := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	theirs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pull", Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"note": []byte("not the operator's")},
	}
	r := newRegistryReconciler(t, newFakeClient(t, cr, theirs))

	if err := r.deleteCredentialSecrets(context.Background(), cr); err != nil {
		t.Fatalf("deleteCredentialSecrets() error = %v", err)
	}
	kept := secretFor(t, r, cr, "web-pull")
	if string(kept.Data["note"]) != "not the operator's" {
		t.Error("a Secret this Registry does not own was deleted")
	}
}
