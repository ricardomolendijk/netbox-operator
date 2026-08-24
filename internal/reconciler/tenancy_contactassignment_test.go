package reconciler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// tenancy.ContactAssignment through the engine, over the **real** Descriptor rather than a
// fake one. That is the point of this file: every other test in this package builds a
// descriptor to exercise a mechanism, and what is worth checking here is that the mechanism
// plus the descriptor NBO-056 actually ships produce the right bytes.
//
// Two things about this kind cannot be checked any other way. The polymorphic pair's column
// names -- `object_type` / `object_id`, not the scope pair's -- reach both the request body and
// the lookup, and NetBox answers a misspelled filter with the *unfiltered* set rather than an
// error (#206), so a wrong name is a lookup that adopts a stranger's assignment. And the
// natural key mixes the pair with `?contact_id=` and `?role_id=`, which is the first key in the
// catalogue to do both at once.

// contactAssignmentGVKs are the Kinds this file resolves against, so the spellings are in one
// place.
var (
	contactAssignmentGVK = netboxv1alpha1.GroupVersion.WithKind("NetBoxContactAssignment")
	contactGVK           = netboxv1alpha1.ContactRef{}.TargetGVK()
	contactRoleGVK       = netboxv1alpha1.ContactRoleRef{}.TargetGVK()
)

// contactAssignmentEngine is the engine wired to the shipped Descriptor and to a cluster
// holding the three objects one assignment points at.
//
// The registry is a private one carrying the real descriptors of the assignment and of every
// Kind it resolves against, because the resolver reads the *target's* Descriptor to learn the
// `app_label.model` string to write -- which is the property that keeps `dcim.site` spelled in
// exactly one place in the codebase.
func contactAssignmentEngine(t *testing.T, nb NetBoxClient, targets ...*unstructured.Unstructured) *Engine {
	t.Helper()

	reg := registry.New()

	for _, gvk := range []schema.GroupVersionKind{
		contactAssignmentGVK, contactGVK, contactRoleGVK,
		netboxv1alpha1.SiteRef{}.TargetGVK(), netboxv1alpha1.TenantRef{}.TargetGVK(),
	} {
		d, ok := registry.Get(gvk)
		if !ok {
			t.Fatalf("no descriptor for %s; this test needs the shipped one", gvk)
		}

		if err := reg.Add(d); err != nil {
			t.Fatalf("registering %s: %v", gvk, err)
		}
	}

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(contactAssignmentGVK, &netboxv1alpha1.NetBoxContactAssignment{})

	assignment, _ := registry.Get(contactAssignmentGVK)

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: assignment, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        &resolver.Resolver{Objects: fakeCluster{objects: targets}, Kinds: reg},
		Status:      &fakeStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      scheme,
	}
}

// contactAssignmentOn is one CR: a contact in a role on whatever the caller points it at.
func contactAssignmentOn(target netboxv1alpha1.ContactAssignmentTarget) *netboxv1alpha1.NetBoxContactAssignment {
	return &netboxv1alpha1.NetBoxContactAssignment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "noc-technical", Generation: 1},
		Spec: netboxv1alpha1.NetBoxContactAssignmentSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			ObjectRef:        target,
			ContactRef:       netboxv1alpha1.ContactRef{Name: "noc"},
			RoleRef:          netboxv1alpha1.ContactRoleRef{Name: "technical"},
			Priority:         netboxv1alpha1.ContactPriorityPrimary,
		},
	}
}

// contactAndRole are the two ordinary references every assignment carries.
func contactAndRole() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		readyTarget(contactGVK, "team-a", "noc", 11),
		readyTarget(contactRoleGVK, "team-a", "technical", 12),
	}
}

// TestContactAssignmentReachesThePayloadAsAPairPlusTwoIDs is the acceptance criterion of this
// kind, asserted on the **request body** and not on a condition.
//
// A resolved reference that never reaches the payload is invisible from RefsResolved=True: the
// operator would report every reference resolved and POST an assignment NetBox rejects for a
// missing object_type. The pair is atomic -- one function writes both columns from one resolved
// result -- and this is where that shows.
//
// The `dcim.site` string in the body comes from NetBoxSite's own Descriptor.ObjectType through
// the real resolver, not from anything written down on the union member, which is the whole
// spelling rule (docs/concepts/generic-refs.md).
func TestContactAssignmentReachesThePayloadAsAPairPlusTwoIDs(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	nb := &fakeClient{created: netbox.Object{"id": float64(31)}}
	targets := append(contactAndRole(), readyTarget(siteGVK, "team-a", "hq", 5))
	engine := contactAssignmentEngine(t, nb, targets...)

	obj := contactAssignmentOn(netboxv1alpha1.ContactAssignmentTarget{
		SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"},
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	for _, tc := range []struct {
		key  string
		want any
	}{
		// The pair, by the column names on tenancy.ContactAssignment -- not `scope_type` /
		// `scope_id`, which belong to a different pair on different models.
		{key: registry.ContactAssignmentTypeField, want: "dcim.site"},
		{key: registry.ContactAssignmentIDField, want: int64(5)},
		// The two ordinary references, written under their *write* names. `contact_id` is the
		// filter name and writing it would be silently dropped by NetBox.
		{key: "contact", want: int64(11)},
		{key: "role", want: int64(12)},
		{key: "priority", want: "primary"},
	} {
		if got := payload[tc.key]; got != tc.want {
			t.Errorf("payload[%s] = %#v, want %#v", tc.key, got, tc.want)
		}
	}

	// The names that must never appear. `object` is the GenericForeignKey the serializer
	// returns as a read-only nested view of the pair, and `contact_id` / `role_id` are filter
	// names: NetBox drops a column it does not know rather than rejecting it, so either would
	// return 201 and write nothing.
	for _, forbidden := range []string{"object", "contact_id", "role_id", "scope_type", "scope_id"} {
		if _, present := payload[forbidden]; present {
			t.Errorf("payload carries %q, which NetBox would silently ignore: %v", forbidden, payload)
		}
	}

	if got := conditionOfAssignment(obj, netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}
}

// TestContactAssignmentLooksItselfUpByAllFourColumns is the other half, and the half a guessed
// filterset name would break silently.
//
// django-filter ignores a parameter it does not recognise and NetBox 4.6.8 has no strict-filter
// validation, so a lookup naming a filter that does not exist returns the *unfiltered* set: the
// engine would adopt the first contact assignment in NetBox and PATCH it into this CR's shape.
// Each of these four is registered on ContactAssignmentFilterSet
// (netbox/tenancy/filtersets.py:119, :153, :120-124, :138-142).
func TestContactAssignmentLooksItselfUpByAllFourColumns(t *testing.T) {
	tenantGVK := netboxv1alpha1.TenantRef{}.TargetGVK()

	nb := &fakeClient{created: netbox.Object{"id": float64(31)}}
	targets := append(contactAndRole(), readyTarget(tenantGVK, "team-a", "acme", 7))
	engine := contactAssignmentEngine(t, nb, targets...)

	obj := contactAssignmentOn(netboxv1alpha1.ContactAssignmentTarget{
		TenantRef: &netboxv1alpha1.TenantRef{Name: "acme"},
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	var lookup netbox.Params
	for _, c := range nb.calls {
		if c.method == "GETONE" {
			lookup = c.params
		}
	}

	if lookup == nil {
		t.Fatal("the engine never looked the assignment up; it would duplicate on every fresh cluster")
	}

	want := netbox.Params{
		"object_type": "tenancy.tenant",
		"object_id":   "7",
		"contact_id":  "11",
		"role_id":     "12",
	}
	if got, wanted := sortedPairs(lookup), sortedPairs(want); !equalStrings(got, wanted) {
		t.Errorf("lookup = %v, want %v", got, wanted)
	}

	// `priority` is deliberately absent: it is not in NetBox's uniqueness constraint, so two
	// assignments differing only in priority are one row and including it would make the
	// engine create a second.
	if _, present := lookup["priority"]; present {
		t.Error("the lookup filters on priority, which is outside the natural key")
	}
}

// TestContactAssignmentMemberWithNoRegisteredKindWaits is the RefKindUnavailable outcome at the
// engine level, asserted rather than worked around.
//
// `deviceRef` is a legal member -- `dcim.device` carries ContactsMixin -- and NetBoxDevice has
// no Descriptor in this build. All four ref modes need the target's Descriptor for its
// endpoint, so all four wait. The two things that must be true are that nothing is written and
// that the object says why: a stub that accepted the member and dropped it would report Ready
// while NetBox held no assignment at all.
func TestContactAssignmentMemberWithNoRegisteredKindWaits(t *testing.T) {
	nb := &fakeClient{}
	engine := contactAssignmentEngine(t, nb, contactAndRole()...)

	obj := contactAssignmentOn(netboxv1alpha1.ContactAssignmentTarget{
		DeviceRef: &netboxv1alpha1.DeviceRef{Name: "sw1"},
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if payload := nb.lastPayload(); payload != nil {
		t.Errorf("the engine wrote %v with an unresolvable target; both columns are REQ", payload)
	}

	refs := conditionOfAssignment(obj, netboxv1alpha1.ConditionRefsResolved)
	if refs.Status != metav1.ConditionFalse || refs.Reason != netboxv1alpha1.ReasonRefKindUnavailable {
		t.Errorf("RefsResolved = %s/%s, want False/%s",
			refs.Status, refs.Reason, netboxv1alpha1.ReasonRefKindUnavailable)
	}
}

// TestContactAssignmentMemberResolvesInTheReferringNamespace is the namespace default, on the
// one field where getting it wrong is expensive.
//
// A union member is an ordinary reference in every respect, and that includes defaulting its
// namespace to the referring object's (docs/concepts/references.md). The assignment lives in
// `team-a` and the only NetBoxSite called `hq` lives in `catalogue`, so the member does not
// resolve -- rather than reaching across and pointing the assignment at somebody else's site.
// Crossing a namespace needs `namespace:` on the member *and* a NetBoxRefGrant in the target
// namespace; neither is here.
//
// Nothing is written, which is the half that matters: `object_type` and `object_id` are both
// REQ, so an assignment whose target did not resolve has no payload NetBox would accept.
func TestContactAssignmentMemberResolvesInTheReferringNamespace(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	nb := &fakeClient{}
	targets := append(contactAndRole(), readyTarget(siteGVK, "catalogue", "hq", 5))
	engine := contactAssignmentEngine(t, nb, targets...)

	obj := contactAssignmentOn(netboxv1alpha1.ContactAssignmentTarget{
		SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"},
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if payload := nb.lastPayload(); payload != nil {
		t.Errorf("the engine wrote %v pointing at a site in another namespace", payload)
	}

	if got := conditionOfAssignment(obj, netboxv1alpha1.ConditionRefsResolved); got.Status != metav1.ConditionFalse {
		t.Errorf("RefsResolved = %s/%s, want False", got.Status, got.Reason)
	}
}

// conditionOfAssignment reads one condition off the CR, as conditionOfClaim does for a claim:
// the shared conditionOf is typed to this package's fake kind.
func conditionOfAssignment(obj *netboxv1alpha1.NetBoxContactAssignment, condType string) metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// equalStrings is slices.Equal without the import, kept local so the assertion above reads as
// one line.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
