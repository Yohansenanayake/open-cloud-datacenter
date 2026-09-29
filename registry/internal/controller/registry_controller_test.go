package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

// harborProjectDeleteStub serves the two calls the finalizer makes, and records
// them so a test can assert what was asked of Harbor.
func harborProjectDeleteStub(t *testing.T, projectExists bool, repos []string) (baseURL string, requests *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if !projectExists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			// Harbor reports a repository as "<project>/<repo>", which the client
			// strips back to the name the delete endpoint expects.
			project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2.0/projects/"), "/repositories")
			w.WriteHeader(http.StatusOK)
			body := "["
			for i, repo := range repos {
				if i > 0 {
					body += ","
				}
				body += fmt.Sprintf(`{"name":%q}`, project+"/"+repo)
			}
			_, _ = w.Write([]byte(body + "]"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &seen
}

// The finalizer derives the project name from the Registry rather than reading
// it from status, so a Registry deleted before its first status write still has
// its project removed instead of leaking one nothing records.
func TestDeleteHarborProject_DerivesTheProjectFromTheRegistry(t *testing.T) {
	reg := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	reg.Status = registryv1alpha1.RegistryStatus{} // nothing was ever recorded
	projectName, err := harborProjectName(reg)
	if err != nil {
		t.Fatalf("harborProjectName() error = %v", err)
	}
	url, requests := harborProjectDeleteStub(t, true, []string{"app"})

	r := deleteReconciler(t, url, reg)
	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Fatalf("deleteHarborProject() error = %v", err)
	}
	if !called(*requests, "DELETE /api/v2.0/projects/"+projectName) {
		t.Errorf("requests = %v, want the derived project %q deleted", *requests, projectName)
	}
	// Harbor refuses to delete a project that still holds repositories.
	if !called(*requests, "DELETE /api/v2.0/projects/"+projectName+"/repositories/app") {
		t.Errorf("requests = %v, want the project emptied first", *requests)
	}
}

// A Registry whose project was never created must still release its finalizer:
// Harbor reporting no such project is the answer, not a failure.
func TestDeleteHarborProject_ReleasesWhenHarborHasNoProject(t *testing.T) {
	reg := registryWithUID("acme-project-1", "web", "3f6b1c22-8e4a-4d1b-9f2c-5a7e0b1d4c93")
	url, _ := harborProjectDeleteStub(t, false, nil)

	r := deleteReconciler(t, url, reg)
	if err := r.deleteHarborProject(context.Background(), reg, testLogger()); err != nil {
		t.Errorf("deleteHarborProject() error = %v, want nil for a project Harbor does not hold", err)
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

	if err := r.ensureCredential(context.Background(), cr, cli, "web-30cf39a6",
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
		if err := r.ensureCredential(context.Background(), cr, cli, "web-30cf39a6",
			testHarborURL, "web-pull", "pull-web", harbor.AccessPull); err != nil {
			t.Fatalf("ensureCredential() pass %d error = %v", i+1, err)
		}
	}
	if len(*requests) != 1 {
		t.Errorf("requests = %v, want exactly one robot minted across repeated passes", *requests)
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

	err := r.ensureCredential(context.Background(), cr, cli, "web-30cf39a6",
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
