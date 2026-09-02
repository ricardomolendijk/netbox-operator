package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// credentialRBAC locates the generated grant, which several tests read.
func credentialRBAC(name string) string {
	return filepath.Join("..", "..", "config", "rbac", "credential-namespaces", name)
}

// grantSecretReader creates a namespaced Role and RoleBinding matching the ones
// generated into config/rbac/credential-namespaces, bound to the impersonated
// ServiceAccount.
func grantSecretReader(t *testing.T, namespace, serviceAccountNamespace string, verbs ...string) {
	t.Helper()
	ctx := context.Background()
	if err := apiClient.Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "endpoint-credential-reader", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: verbs,
		}},
	}); err != nil {
		t.Fatalf("creating the Role in %s: %v", namespace, err)
	}
	if err := apiClient.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "endpoint-credential-reader", Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "endpoint-credential-reader"},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: "manager", Namespace: serviceAccountNamespace,
		}},
	}); err != nil {
		t.Fatalf("creating the RoleBinding in %s: %v", namespace, err)
	}
}

// asServiceAccount returns clients acting as the operator's ServiceAccount, so RBAC is
// evaluated against the Roles the test granted rather than against envtest's admin
// certificate. Impersonation rather than a real token because it needs no TokenRequest and
// the authorizer cannot tell the difference.
func asServiceAccount(t *testing.T, namespace string) (*rest.Config, *kubernetes.Clientset) {
	t.Helper()
	cfg := rest.CopyConfig(testEnv.Config)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + namespace + ":manager",
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("building an impersonating clientset: %v", err)
	}
	return cfg, clientset
}

// TestNamespacedRoleCarriesTheInformersWatch is the load-bearing question of NBO-072: the
// operator's Secret access can only be narrowed to namespaces if a namespaced Role can
// authorise everything the informer does, and an informer does not `get` -- it LISTs and
// then WATCHes, forever.
//
// Every assertion here is against a real API server with RBAC enabled, acting as a
// ServiceAccount that holds exactly one namespaced Role, because the answer depends on how
// the API server scopes an authorization check and not on anything this repository
// controls. It is also the test that fails if a future controller-runtime stops issuing
// per-namespace requests for a namespace-scoped cache.
func TestNamespacedRoleCarriesTheInformersWatch(t *testing.T) {
	ctx := context.Background()
	granted := newNamespaceSuffixed(t, "-granted")
	other := newNamespaceSuffixed(t, "-other")
	selector := CredentialLabel + "=" + CredentialLabelValue

	makeSecret(t, apiClient, granted, "nb-token", "valid-token")
	makeSecret(t, apiClient, other, "nb-token", "valid-token")
	grantSecretReader(t, granted, granted, "get", "list", "watch")
	cfg, clientset := asServiceAccount(t, granted)

	// The RBAC authorizer watches Roles through an informer of its own, so a Role created
	// a moment ago is not necessarily in force yet.
	eventually(t, "a namespaced WATCH to be authorised by the namespaced Role", func() bool {
		watch, err := clientset.CoreV1().Secrets(granted).Watch(ctx,
			metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false
		}
		watch.Stop()
		return true
	})

	// The other half of the same fact: the request the informer must NOT make. A
	// cluster-scoped LIST or WATCH is authorised at the cluster scope, where a Role does
	// not reach -- so a namespace-scoped cache is not an optimisation here, it is the only
	// way the manager can start at all.
	if _, err := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("cluster-scoped LIST under a namespaced Role: err = %v, want Forbidden", err)
	}
	if _, err := clientset.CoreV1().Secrets("").Watch(ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("cluster-scoped WATCH under a namespaced Role: err = %v, want Forbidden", err)
	}

	// And `watch` is not implied by `get` and `list`, which is why the generated Role
	// carries all three: dropping it would cost token rotation without a restart.
	grantSecretReader(t, other, granted, "get", "list")
	if _, err := clientset.CoreV1().Secrets(other).Watch(ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("WATCH under a get+list Role: err = %v, want Forbidden", err)
	}

	// End to end: the cache the manager actually builds, under nothing but that Role.
	informers, err := cache.New(cfg, cache.Options{
		Scheme:   scheme,
		ByObject: NewSecretScope([]string{granted}).CacheOptions(),
	})
	if err != nil {
		t.Fatalf("building the scoped cache: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if err := informers.Start(ctx); err != nil {
			t.Logf("scoped cache stopped: %v", err)
		}
	}()
	if !informers.WaitForCacheSync(ctx) {
		t.Fatal("the scoped cache never synced, so its LIST was refused")
	}

	key := client.ObjectKey{Namespace: granted, Name: "nb-token"}
	if err := informers.Get(ctx, key, &corev1.Secret{}); err != nil {
		t.Fatalf("reading the granted namespace through the scoped cache: %v", err)
	}

	// Rotation, which is what `watch` buys: the informer is already synced, so seeing the
	// new value can only have arrived over the WATCH.
	rotated := &corev1.Secret{}
	if err := apiClient.Get(ctx, key, rotated); err != nil {
		t.Fatalf("fetching the secret to rotate: %v", err)
	}
	rotated.Data = map[string][]byte{"token": []byte("rotated-token")}
	if err := apiClient.Update(ctx, rotated); err != nil {
		t.Fatalf("rotating the token: %v", err)
	}
	eventually(t, "the rotated token to arrive over the namespaced WATCH", func() bool {
		seen := &corev1.Secret{}
		if err := informers.Get(ctx, key, seen); err != nil {
			return false
		}
		return string(seen.Data["token"]) == "rotated-token"
	})

	// A namespace the cache was not told about is not reachable through it, whatever RBAC
	// says -- the reason SecretScope.Check exists rather than an error-message reading.
	if err := informers.Get(ctx, client.ObjectKey{Namespace: other, Name: "nb-token"},
		&corev1.Secret{}); err == nil {
		t.Error("the scoped cache served a Secret from a namespace it does not watch")
	}
}

// TestSecretInAnUnlistedNamespaceIsForbidden proves the negative case NBO-072 asks for:
// with the shipped grant, the operator's ServiceAccount cannot read a Secret in a
// namespace nobody listed. Asserted in envtest rather than as a documented
// `kubectl auth can-i` run, because envtest is a real API server with RBAC on, so the
// check runs in CI on every commit instead of being a paragraph somebody has to trust.
func TestSecretInAnUnlistedNamespaceIsForbidden(t *testing.T) {
	ctx := context.Background()
	listed := newNamespaceSuffixed(t, "-listed")
	unlisted := newNamespaceSuffixed(t, "-unlisted")

	makeSecret(t, apiClient, unlisted, "someone-elses", "not-for-the-operator")
	grantSecretReader(t, listed, listed, "get", "list", "watch")
	_, clientset := asServiceAccount(t, listed)

	eventually(t, "the Role in the listed namespace to take effect", func() bool {
		_, err := clientset.CoreV1().Secrets(listed).List(ctx, metav1.ListOptions{})
		return err == nil
	})

	// The same three verbs `kubectl auth can-i` would ask about, in the namespace nobody
	// granted. This is the assertion that fails if somebody puts the cluster-wide rule
	// back into config/rbac.
	if _, err := clientset.CoreV1().Secrets(unlisted).Get(ctx, "someone-elses",
		metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("GET in an unlisted namespace: err = %v, want Forbidden", err)
	}
	if _, err := clientset.CoreV1().Secrets(unlisted).List(ctx,
		metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("LIST in an unlisted namespace: err = %v, want Forbidden", err)
	}
	if _, err := clientset.CoreV1().Secrets(unlisted).Watch(ctx,
		metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("WATCH in an unlisted namespace: err = %v, want Forbidden", err)
	}
}

// TestUngrantedNamespaceIsAConditionNotAForbidden is the usability half of option B. The
// cost of namespaced RBAC is that forgetting a namespace is invisible until an endpoint
// lands in it, so the failure has to say which namespace and what to add -- the same
// standard the missing-label message already meets.
//
// Over the fake client rather than envtest: the package's shared manager is cluster-wide by
// design and would reconcile the same endpoint successfully a moment later, so the
// assertion has to be about one reconcile in isolation.
func TestUngrantedNamespaceIsAConditionNotAForbidden(t *testing.T) {
	ctx := context.Background()
	reconciler, _ := fakeReconciler(t, endpointFor("http://netbox.invalid"), credentialSecret())
	// The Secret is right there in the same namespace, carrying the label, with a valid
	// token in it: the only thing wrong is that nobody granted the operator a Role in
	// "default".
	reconciler.Secrets = NewSecretScope([]string{"somewhere-else"})

	reconcileOnce(t, reconciler)

	endpoint := &netboxv1alpha1.NetBoxEndpoint{}
	key := client.ObjectKey{Namespace: "default", Name: "homelab"}
	if err := reconciler.Get(ctx, key, endpoint); err != nil {
		t.Fatalf("fetching the endpoint: %v", err)
	}

	condition := conditionOf(endpoint, netboxv1alpha1.ConditionReady)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition = %v, want False", condition)
	}
	if condition.Reason != netboxv1alpha1.ReasonSecretMissing {
		t.Errorf("Ready reason = %q, want %q", condition.Reason, netboxv1alpha1.ReasonSecretMissing)
	}
	// The namespace, and the fix in both install paths' own terms. `--set
	// credentialNamespaces={default,somewhere-else}` is the whole list rather than the
	// missing entry, because Helm replaces a list value: the message a reader pastes must
	// not silently revoke the namespaces already granted (#300).
	for _, want := range []string{
		"default",
		"--set credentialNamespaces={default,somewhere-else}",
		"namespaces.txt",
		"docs/operations/rbac.md",
	} {
		if !strings.Contains(condition.Message, want) {
			t.Errorf("Ready message = %q, want it to name %q", condition.Message, want)
		}
	}
	// And no client was handed out, so nothing downstream writes to NetBox with a token
	// this reconcile never read.
	if _, _, ok := reconciler.Cache.Lookup("default", "homelab"); ok {
		t.Error("an endpoint in an ungranted namespace still handed out a client")
	}
}

// TestCheckNamesBothInstallPaths is the rest of #300: the operator cannot tell whether it
// was installed by Helm or by kustomize, and the fix is a different artefact in each, so
// the one message it can emit has to name both. The Helm half carries the *whole* list,
// because `--set credentialNamespaces={team-a}` replaces the value rather than adding to
// it and would revoke every namespace already granted.
func TestCheckNamesBothInstallPaths(t *testing.T) {
	if err := NewSecretScope([]string{"default", "prod"}).Check("prod"); err != nil {
		t.Fatalf("Check on a granted namespace = %v, want nil", err)
	}
	// Cluster-wide is every namespace, so it never rejects and never suggests anything.
	if err := (SecretScope{}).Check("anywhere"); err != nil {
		t.Fatalf("Check under a cluster-wide scope = %v, want nil", err)
	}

	err := NewSecretScope([]string{"prod", "default"}).Check("team-a")
	if !errors.Is(err, errNamespaceNotGranted) {
		t.Fatalf("Check on an ungranted namespace = %v, want errNamespaceNotGranted", err)
	}
	for _, want := range []string{
		`namespace "team-a"`,
		"granted default, prod",
		"--set credentialNamespaces={default,prod,team-a}",
		"config/rbac/credential-namespaces/namespaces.txt",
		"docs/operations/rbac.md",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check() = %q, want it to name %q", err, want)
		}
	}
}

func TestParseSecretScope(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		want       []string
		wantAll    bool
		wantErrror bool
	}{
		{name: "one namespace", value: "default", want: []string{"default"}},
		{name: "several, sorted and deduplicated", value: "b, a ,a", want: []string{"a", "b"}},
		{name: "trailing separator", value: "a,", want: []string{"a"}},
		{name: "every namespace", value: AllNamespaces, wantAll: true},
		// Unset is the case that matters: a manager that quietly read every Secret in the
		// cluster when nobody configured it would be NBO-072 all over again.
		{name: "unset", value: "", wantErrror: true},
		{name: "blank", value: " , ", wantErrror: true},
		{name: "every namespace and one more", value: "*,a", wantErrror: true},
		// A trailing separator is a typo, not a second namespace.
		{name: "every namespace, trailing separator", value: "*,", wantAll: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := ParseSecretScope(tc.value)
			if tc.wantErrror {
				if err == nil {
					t.Fatalf("ParseSecretScope(%q) = %v, want an error", tc.value, scope)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSecretScope(%q): %v", tc.value, err)
			}
			if scope.ClusterWide() != tc.wantAll {
				t.Errorf("ClusterWide() = %v, want %v", scope.ClusterWide(), tc.wantAll)
			}
			if !slices.Equal(scope.Namespaces(), tc.want) {
				t.Errorf("Namespaces() = %v, want %v", scope.Namespaces(), tc.want)
			}
		})
	}
}

func TestSecretScopeCacheOptions(t *testing.T) {
	scoped, ok := secretByObject(NewSecretScope([]string{"a", "b"}).CacheOptions())
	if !ok {
		t.Fatal("CacheOptions() configured nothing for Secrets")
	}
	if scoped.Label == nil || !scoped.Label.Matches(labels.Set{
		CredentialLabel: CredentialLabelValue,
	}) {
		t.Errorf("Label = %v, want it to select the credential label", scoped.Label)
	}
	namespaces := make([]string, 0, len(scoped.Namespaces))
	for namespace := range scoped.Namespaces {
		namespaces = append(namespaces, namespace)
	}
	slices.Sort(namespaces)
	if !slices.Equal(namespaces, []string{"a", "b"}) {
		t.Errorf("Namespaces = %v, want [a b]", namespaces)
	}

	// Cluster-wide must stay expressible: the tests in this package use it, and it is the
	// documented escape hatch for a deployment that keeps a cluster-wide grant.
	wide, _ := secretByObject(NewSecretScope(nil).CacheOptions())
	if wide.Namespaces != nil {
		t.Errorf("cluster-wide scope produced namespaces %v", wide.Namespaces)
	}
}

// secretByObject picks the Secret entry out of a ByObject map, whose keys are pointers and
// so cannot be looked up with a freshly made one.
func secretByObject(byObject map[client.Object]cache.ByObject) (cache.ByObject, bool) {
	for object, config := range byObject {
		if _, ok := object.(*corev1.Secret); ok {
			return config, true
		}
	}
	return cache.ByObject{}, false
}

// TestGeneratedGrantMatchesTheNamespaceList holds the two generated files and the
// ClusterRole to the one list they all come from. Three ways they can disagree, all of
// them silent: a Role in a namespace the manager builds no informer for is a grant nobody
// uses, an informer with no Role is a manager that will not start, and a `secrets` rule
// back in the ClusterRole makes the whole exercise decorative.
func TestGeneratedGrantMatchesTheNamespaceList(t *testing.T) {
	namespaces := listedNamespaces(t)
	if len(namespaces) == 0 {
		t.Fatal("namespaces.txt lists nothing, so the shipped operator could read no Secret")
	}

	roles, bindings := generatedRoles(t)
	if !slices.Equal(roles, namespaces) {
		t.Errorf("Roles cover %v, namespaces.txt lists %v; run `make manifests`", roles, namespaces)
	}
	if !slices.Equal(bindings, namespaces) {
		t.Errorf("RoleBindings cover %v, namespaces.txt lists %v; run `make manifests`",
			bindings, namespaces)
	}

	if got := patchedNamespaceList(t); got != strings.Join(namespaces, ",") {
		t.Errorf("the manager is told %q, namespaces.txt lists %q; run `make manifests`",
			got, strings.Join(namespaces, ","))
	}

	for _, rule := range clusterRoleRules(t) {
		if slices.Contains(rule.Resources, "secrets") {
			t.Errorf("the generated ClusterRole grants %v on secrets cluster-wide, which is "+
				"the whole of NBO-072; the grant belongs in credential-namespaces", rule.Verbs)
		}
	}
}

// listedNamespaces reads namespaces.txt the way the generator does.
func listedNamespaces(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(credentialRBAC("namespaces.txt"))
	if err != nil {
		t.Fatalf("reading namespaces.txt: %v", err)
	}
	namespaces := []string{}
	for line := range strings.Lines(string(body)) {
		name := strings.TrimSpace(strings.Split(line, "#")[0])
		if name != "" {
			namespaces = append(namespaces, name)
		}
	}
	slices.Sort(namespaces)
	return slices.Compact(namespaces)
}

// generatedRoles returns the namespaces the generated Roles and RoleBindings cover, having
// checked that each grants what the informer needs and binds the operator's own
// ServiceAccount.
func generatedRoles(t *testing.T) (roles, bindings []string) {
	t.Helper()
	wantVerbs := []string{"get", "list", "watch"}
	subject := operatorServiceAccount(t)

	for _, doc := range documents(t, credentialRBAC("rbac.yaml")) {
		switch doc.kind {
		case "Role":
			role := &rbacv1.Role{}
			decode(t, doc.body, role)
			roles = append(roles, role.Namespace)
			for _, rule := range role.Rules {
				if !slices.Equal(rule.Resources, []string{"secrets"}) ||
					!slices.Equal(rule.Verbs, wantVerbs) {
					t.Errorf("Role in %s grants %v on %v, want %v on [secrets]",
						role.Namespace, rule.Verbs, rule.Resources, wantVerbs)
				}
			}
		case "RoleBinding":
			binding := &rbacv1.RoleBinding{}
			decode(t, doc.body, binding)
			bindings = append(bindings, binding.Namespace)
			if !slices.Contains(binding.Subjects, subject) {
				t.Errorf("RoleBinding in %s binds %v, want %v -- the ServiceAccount name and "+
					"namespace config/base's transformers produce",
					binding.Namespace, binding.Subjects, subject)
			}
		default:
			t.Errorf("unexpected %s in the generated grant", doc.kind)
		}
	}
	slices.Sort(roles)
	slices.Sort(bindings)
	return roles, bindings
}

// operatorServiceAccount is the identity the RoleBindings must name: the ServiceAccount as
// config/base's namePrefix and namespace transformers leave it. The RoleBindings live
// outside config/base -- that is what keeps their own namespaces intact -- so they are not
// transformed and have to name the result themselves. This is the check that catches a
// rename of either.
func operatorServiceAccount(t *testing.T) rbacv1.Subject {
	t.Helper()
	base := struct {
		NamePrefix string `json:"namePrefix"`
		Namespace  string `json:"namespace"`
	}{}
	body, err := os.ReadFile(filepath.Join("..", "..", "config", "base", "kustomization.yaml"))
	if err != nil {
		t.Fatalf("reading config/base/kustomization.yaml: %v", err)
	}
	decode(t, string(body), &base)

	account := &corev1.ServiceAccount{}
	body, err = os.ReadFile(filepath.Join("..", "..", "config", "rbac", "service_account.yaml"))
	if err != nil {
		t.Fatalf("reading service_account.yaml: %v", err)
	}
	decode(t, string(body), account)

	return rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      base.NamePrefix + account.Name,
		Namespace: base.Namespace,
	}
}

// patchedNamespaceList is the namespace list the generated patch hands the manager.
func patchedNamespaceList(t *testing.T) string {
	t.Helper()
	patch := &appsv1.Deployment{}
	body, err := os.ReadFile(credentialRBAC("manager_env_patch.yaml"))
	if err != nil {
		t.Fatalf("reading manager_env_patch.yaml: %v", err)
	}
	decode(t, string(body), patch)

	for _, container := range patch.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "NETBOX_CREDENTIAL_NAMESPACES" {
				return env.Value
			}
		}
	}
	t.Fatal("the generated patch sets no NETBOX_CREDENTIAL_NAMESPACES")
	return ""
}

// clusterRoleRules returns the rules of the marker-generated manager ClusterRole.
func clusterRoleRules(t *testing.T) []rbacv1.PolicyRule {
	t.Helper()
	role := &rbacv1.ClusterRole{}
	body, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatalf("reading role.yaml: %v", err)
	}
	decode(t, string(body), role)
	return role.Rules
}

type manifestDoc struct {
	kind string
	body string
}

// documents splits a multi-document manifest, skipping the comment-only leader.
func documents(t *testing.T, path string) []manifestDoc {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	docs := []manifestDoc{}
	for _, doc := range strings.Split(string(body), "\n---") {
		header := struct {
			Kind string `json:"kind"`
		}{}
		decode(t, doc, &header)
		if header.Kind != "" {
			docs = append(docs, manifestDoc{kind: header.Kind, body: doc})
		}
	}
	return docs
}

func decode(t *testing.T, doc string, into any) {
	t.Helper()
	if err := yaml.Unmarshal([]byte(doc), into); err != nil {
		t.Fatalf("decoding a manifest: %v", err)
	}
}
