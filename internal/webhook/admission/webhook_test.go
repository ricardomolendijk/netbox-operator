package admission

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// endpointName is the endpoint every fixture below writes through. It does not have to exist
// for most of these tests -- an absent endpoint is a warning, not a rejection -- and where it
// does, the test makes it.
const endpointName = "homelab"

// region builds a NetBoxRegion fixture.
func region(ns, name string, mutate ...func(*netboxv1alpha1.NetBoxRegion)) *netboxv1alpha1.NetBoxRegion {
	r := &netboxv1alpha1.NetBoxRegion{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: netboxv1alpha1.NetBoxRegionSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: endpointName},
			Name:             name,
			Slug:             name,
		},
	}

	for _, m := range mutate {
		m(r)
	}

	return r
}

// tag builds a NetBoxTag fixture, whose natural key is one scalar.
func tag(ns, name string, mutate ...func(*netboxv1alpha1.NetBoxTag)) *netboxv1alpha1.NetBoxTag {
	t := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: netboxv1alpha1.NetBoxTagSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: endpointName},
			Name:             name,
			Slug:             name,
		},
	}

	for _, m := range mutate {
		m(t)
	}

	return t
}

// TestSelfReferenceIsDenied is the shallowest cycle and the mistake people actually make:
// parentRef naming the object it is written on.
//
// It is the one depth CEL could express -- a root-level rule sees both self.metadata.name and
// self.spec -- and it is here rather than there so that one implementation answers "is this a
// cycle" for every depth. Two implementations of that question is two answers.
func TestSelfReferenceIsDenied(t *testing.T) {
	ns := newNamespace(t)

	message := refuses(t, region(ns, "loop", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "loop"}
	}))

	for _, want := range []string{"reference cycle", "netboxregion/" + ns + "/loop -> itself"} {
		if !strings.Contains(message, want) {
			t.Errorf("rejection message %q does not mention %q", message, want)
		}
	}
}

// TestCycleAtDepthTwoIsDenied is the rule CEL cannot reach: `a -> b -> a` needs to read b.
func TestCycleAtDepthTwoIsDenied(t *testing.T) {
	ns := newNamespace(t)

	// Admitted, because b does not exist yet -- a forward reference is the ordinary shape of
	// an order-independent apply and must not be rejected (NBO-017).
	mustCreate(t, region(ns, "a", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "b"}
	}))

	awaitCached(t, &netboxv1alpha1.NetBoxRegionList{}, ns)

	message := refuses(t, region(ns, "b", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "a"}
	}))

	want := "netboxregion/" + ns + "/b -> netboxregion/" + ns + "/a -> netboxregion/" + ns + "/b"
	if !strings.Contains(message, want) {
		t.Errorf("rejection message %q does not carry the path %q", message, want)
	}
}

// TestForwardReferenceIsAdmitted is the property the whole grant-and-cycle design is held to:
// admission must not become order-sensitive, or NBO-017's random-order convergence gate is a
// lie.
func TestForwardReferenceIsAdmitted(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, region(ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent-applied-later"}
	}))
}

// TestSameNamespaceCollisionIsDenied is the rule that needs the siblings: a NetBoxTag's
// identity is its slug, so two of them claiming one slug are two CRs managing one NetBox
// object, and the second to reconcile reports Conflict forever.
func TestSameNamespaceCollisionIsDenied(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, tag(ns, "first", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "shared" }))
	awaitCached(t, &netboxv1alpha1.NetBoxTagList{}, ns)

	message := refuses(t, tag(ns, "second", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "shared" }))

	for _, want := range []string{`netboxtag "first"`, "slug=\"shared\"", "Conflict"} {
		if !strings.Contains(message, want) {
			t.Errorf("rejection message %q does not mention %q", message, want)
		}
	}
}

// TestDifferentEndpointIsNotACollision: a different endpoint is a different NetBox, so an
// identical natural key is a different object.
func TestDifferentEndpointIsNotACollision(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, tag(ns, "here", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "shared" }))
	awaitCached(t, &netboxv1alpha1.NetBoxTagList{}, ns)

	mustCreate(t, tag(ns, "there", func(x *netboxv1alpha1.NetBoxTag) {
		x.Spec.Slug = "shared"
		x.Spec.EndpointRef = "other-netbox"
	}))
}

// TestLowerPriorityKeyIsNotACollision is the false positive the candidate index exists to
// prevent.
//
// A region with a parent is identified by `(parent, name)` and a top-level one by `(name)`
// with the parent pinned null. Two such regions agree on `name` and are nonetheless two
// legitimate dcim.Regions, because the engine looks each up by its own first applicable
// candidate. Comparing on any shared candidate would reject a correct manifest.
func TestLowerPriorityKeyIsNotACollision(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, region(ns, "top", func(r *netboxv1alpha1.NetBoxRegion) { r.Spec.Name = "emea" }))
	awaitCached(t, &netboxv1alpha1.NetBoxRegionList{}, ns)

	mustCreate(t, region(ns, "nested", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.Name = "emea"
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "somewhere"}
	}))
}

// TestUngrantedCrossNamespaceRefWarns: warned about and admitted, deliberately.
//
// Denying would make admission order-sensitive -- a grant legitimately arrives after the
// object needing it -- and a control a different apply order bypasses is not a control.
// Enforcement is at reconcile, as RefsResolved=False/RefDenied with zero NetBox writes.
func TestUngrantedCrossNamespaceRefWarns(t *testing.T) {
	catalogue := newNamespace(t)
	ns := newNamespace(t)

	warned.reset()

	mustCreate(t, region(ns, "borrower", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "emea", Namespace: catalogue}
	}))

	for _, want := range []string{"RefDenied", "NetBoxRefGrant", catalogue} {
		if !warned.contains(want) {
			t.Errorf("no warning mentioned %q; got %q", want, warned.all())
		}
	}
}

// TestGrantedCrossNamespaceRefIsSilent is the other half: a covering grant means no warning,
// which is what makes the warning worth reading.
func TestGrantedCrossNamespaceRefIsSilent(t *testing.T) {
	catalogue := newNamespace(t)
	ns := newNamespace(t)

	mustCreate(t, &netboxv1alpha1.NetBoxRefGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: catalogue, Name: "public"},
		Spec: netboxv1alpha1.NetBoxRefGrantSpec{
			From: []netboxv1alpha1.RefGrantFrom{{Namespaces: netboxv1alpha1.NamespacesAll}},
		},
	})

	awaitCached(t, &netboxv1alpha1.NetBoxRefGrantList{}, catalogue)
	warned.reset()

	mustCreate(t, region(ns, "borrower", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "emea", Namespace: catalogue}
	}))

	if warned.contains("RefDenied") {
		t.Errorf("a granted reference produced a denial warning: %q", warned.all())
	}
}

// TestAbsentEndpointWarns: a whole namespace applied in one go legitimately has the endpoint
// arrive after the objects, so this is fast feedback rather than a gate.
func TestAbsentEndpointWarns(t *testing.T) {
	ns := newNamespace(t)

	warned.reset()
	mustCreate(t, tag(ns, "orphan"))

	if !warned.contains("WaitingForEndpoint") {
		t.Errorf("no warning about the absent endpoint; got %q", warned.all())
	}
}

// TestNotReadyEndpointWarns quotes the endpoint's own Ready condition, because "not ready" on
// its own sends a reader to the wrong object.
func TestNotReadyEndpointWarns(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: endpointName},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            "https://netbox.invalid",
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "netbox-token"},
		},
	}
	mustCreate(t, endpoint)

	endpoint.Status.Conditions = []metav1.Condition{{
		Type: netboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: netboxv1alpha1.ReasonSecretMissing, Message: "no such Secret",
		LastTransitionTime: metav1.Now(), ObservedGeneration: endpoint.Generation,
	}}
	if err := k8sClient.Status().Update(ctx, endpoint); err != nil {
		t.Fatalf("writing the endpoint status: %v", err)
	}

	awaitReady(t, ns, endpointName, metav1.ConditionFalse)
	warned.reset()

	mustCreate(t, tag(ns, "waiting"))

	if !warned.contains(netboxv1alpha1.ReasonSecretMissing) {
		t.Errorf("the warning did not quote the endpoint's own reason; got %q", warned.all())
	}
}

// awaitReady blocks until the webhook's reader sees the endpoint's Ready condition at status.
func awaitReady(t *testing.T, ns, name string, status metav1.ConditionStatus) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)

	for time.Now().Before(deadline) {
		live := &netboxv1alpha1.NetBoxEndpoint{}
		if err := cached.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, live); err == nil {
			if ready := readyCondition(live); ready != nil && ready.Status == status {
				return
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for netboxendpoint %s/%s to report Ready=%s", ns, name, status)
}

// TestGrantNamingUnknownKindWarns is NBO-044's one deliberate departure: it asked for a
// denial, and NetBoxRefGrant's own API contract says an unknown Kind is inert rather than an
// error so that a grant may be written before the Kind it names ships. A warning keeps the
// typo visible without breaking that.
func TestGrantNamingUnknownKindWarns(t *testing.T) {
	ns := newNamespace(t)

	warned.reset()

	mustCreate(t, &netboxv1alpha1.NetBoxRefGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "typo"},
		Spec: netboxv1alpha1.NetBoxRefGrantSpec{
			From: []netboxv1alpha1.RefGrantFrom{{Namespaces: netboxv1alpha1.NamespacesAll}},
			To:   []netboxv1alpha1.RefGrantTo{{Kinds: []string{"NetBoxSite", "NetBoxTypo"}}},
		},
	})

	if !warned.contains(`"NetBoxTypo"`) {
		t.Errorf("no warning named the unknown kind; got %q", warned.all())
	}

	if warned.contains(`"NetBoxSite"`) {
		t.Errorf("a known kind was warned about: %q", warned.all())
	}
}

// TestUnregisteredKindIsAdmitted: the configuration matches `resources: ['*']`, so a
// NetBoxEndpoint reaches the webhook too. It has no Descriptor and nothing here has a second
// object to check it against, so it is admitted by a guard clause rather than by an edit.
func TestUnregisteredKindIsAdmitted(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "unreviewed"},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            "https://netbox.invalid",
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "netbox-token"},
		},
	})
}

// TestDryRunIsHonoured holds the webhook to its own `sideEffects: None` declaration, which the
// API server trusts: a dry-run review must reach the same verdict and store nothing.
func TestDryRunIsHonoured(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	mustCreate(t, tag(ns, "real", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "taken" }))
	awaitCached(t, &netboxv1alpha1.NetBoxTagList{}, ns)

	colliding := tag(ns, "dry", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "taken" })
	if err := k8sClient.Create(ctx, colliding, client.DryRunAll); err == nil {
		t.Fatal("a dry-run create of a colliding object was admitted")
	}

	fine := tag(ns, "dry-ok", func(x *netboxv1alpha1.NetBoxTag) { x.Spec.Slug = "free" })
	if err := k8sClient.Create(ctx, fine, client.DryRunAll); err != nil {
		t.Fatalf("a dry-run create of a valid object was refused: %v", err)
	}

	stored := &netboxv1alpha1.NetBoxTagList{}
	if err := k8sClient.List(ctx, stored, client.InNamespace(ns)); err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(stored.Items) != 1 {
		t.Errorf("%d tags stored; a dry run must store nothing", len(stored.Items))
	}
}

// TestEndpointRefIsImmutable is a **layer 1** assertion, deliberately in this package: it
// proves the rule is CEL by being the one check here that does not need the webhook. The API
// server enforces it whether or not anything is serving admission.
func TestEndpointRefIsImmutable(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	live := tag(ns, "pinned")
	mustCreate(t, live)

	live.Spec.EndpointRef = "somewhere-else"

	err := k8sClient.Update(ctx, live)
	if err == nil {
		t.Fatal("endpointRef was mutable")
	}

	if !strings.Contains(err.Error(), "endpointRef is immutable") {
		t.Errorf("rejection message %q is not the CEL rule's", err)
	}
}
