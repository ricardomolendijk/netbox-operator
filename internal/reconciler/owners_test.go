package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// fakeOwners records the owner-reference list at each write, which is how a test asserts
// both what was written and that a second identical pass writes nothing.
type fakeOwners struct {
	writes [][]metav1.OwnerReference
	err    error
}

func (f *fakeOwners) UpdateOwnerReferences(_ context.Context, obj client.Object) error {
	if f.err != nil {
		return f.err
	}

	f.writes = append(f.writes, slices.Clone(obj.GetOwnerReferences()))

	return nil
}

// errOwnerWrite stands in for an API-server rejection of an owner-reference patch.
var errOwnerWrite = errors.New("owner reference update rejected")

// parentOwnedBy is a resolution that resolved `parentRef` to the parent CR "europe" in the
// given namespace, carrying the Kind and uid an owner reference is built from.
//
// The namespace is the parameter that matters: same as the referrer's is the cascade, anything
// else is the refusal. The uid is a parameter too, for the recreated-parent case.
func parentOwnedBy(namespace, uid string) resolver.Resolution {
	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{
		"parentRef": {{
			ID: 4, ObjectType: "extras.tag", Mode: resolver.ModeName,
			Target:    types.NamespacedName{Namespace: namespace, Name: "europe"},
			TargetGVK: fakeGVK, TargetUID: types.UID(uid),
		}},
	}}
}

// parentOwnedByNetBox is a resolution that resolved `parentRef` against NetBox rather than
// against a CR, which is every mode but `name` -- including the raw `id` escape hatch for a
// pre-existing object the operator does not manage.
func parentOwnedByNetBox(mode resolver.Mode) resolver.Resolution {
	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{
		"parentRef": {{ID: 4, ObjectType: "extras.tag", Mode: mode}},
	}}
}

// ownerEngine is an engine wired for the owner-reference path: a canned resolution in, a
// recorded owner-reference write out.
func ownerEngine(t *testing.T, resolution resolver.Resolution, owners *fakeOwners) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: parentedDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: &fakeClient{created: liveTag(7)}, Resync: testResync},
			ready:    true,
		},
		Refs:       &fakeRefs{resolution: resolution},
		Status:     &fakeStatus{},
		LiveStatus: &fakeLiveStatus{},
		Finalizers: &fakeFinalizers{},
		Owners:     owners,
		Scheme:     fakeScheme(t),
	}
}

// parentedObject is an object whose spec names a containment parent, which is what puts the
// reference in the declared set at all -- an absent ref is never resolved and so never owned.
func parentedObject() *fakeKind {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	return obj
}

// TestContainmentOwnerReference is ADR-0003 rule 4, and the one row that matters most is the
// cross-namespace one: the same manifest cascades or does not depending on where the parent
// lives, and the only thing standing between that and a user discovering it on deletion day
// is the condition asserted here.
func TestContainmentOwnerReference(t *testing.T) {
	tests := []struct {
		name string

		// resolution is what the resolver reported for `parentRef`.
		resolution resolver.Resolution

		// annotations go on the object, for the opt-out row.
		annotations map[string]string

		// existing are the owner references the object already carries.
		existing []metav1.OwnerReference

		// wantReason is the ParentOwned condition reason, and "" means the condition must not
		// be set at all.
		wantReason string

		// wantStatus is the ParentOwned condition status.
		wantStatus metav1.ConditionStatus

		// wantOwners is the owner-reference list the object must end up with.
		wantOwners []metav1.OwnerReference

		// wantWrites is how many owner-reference patches the pass should have issued.
		wantWrites int

		// wantMessage is a substring the condition message must contain, because for the
		// refusals the message is the entire product.
		wantMessage string
	}{
		{
			// The happy path: parent and child in one namespace, so the owner reference is
			// legal and Kubernetes garbage collection will do the cascade.
			name:       "a same-namespace parent is owned",
			resolution: parentOwnedBy("team-a", "europe-uid"),
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
			}},
			wantWrites:  1,
			wantMessage: "garbage-collects",
		},
		{
			// The whole reason this is a condition. A grant makes the *reference* legal and
			// can do nothing for the owner reference, so the object is told, in the object,
			// that deleting its parent will leave it behind.
			name:        "a cross-namespace parent is not owned, and says so",
			resolution:  parentOwnedBy("netbox-catalogue", "europe-uid"),
			wantReason:  netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  0,
			wantMessage: "may not cross a namespace",
		},
		{
			// `id` is the escape hatch for a NetBox object the operator does not manage, so
			// there is no CR anywhere for an owner reference to name.
			name:        "a parent resolved by raw id is not owned",
			resolution:  parentOwnedByNetBox(resolver.ModeID),
			wantReason:  netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  0,
			wantMessage: "names a netbox object and not a CR",
		},
		{
			// Same shape as `id`: a slug is a NetBox lookup, not a Kubernetes name.
			name:        "a parent resolved by slug is not owned",
			resolution:  parentOwnedByNetBox(resolver.ModeSlug),
			wantReason:  netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  0,
			wantMessage: "names a netbox object and not a CR",
		},
		{
			name:        "the opt-out annotation declines the owner reference",
			resolution:  parentOwnedBy("team-a", "europe-uid"),
			annotations: map[string]string{netboxv1alpha1.ParentOwnershipAnnotation: "false"},
			wantReason:  netboxv1alpha1.ReasonParentOwnershipDisabled,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  0,
			wantMessage: netboxv1alpha1.ParentOwnershipAnnotation,
		},
		{
			// Only "false" opts out, so a typo leaves the documented behaviour standing
			// rather than silently switching off a cascade somebody relies on.
			name:        "an unrecognised annotation value does not opt out",
			resolution:  parentOwnedBy("team-a", "europe-uid"),
			annotations: map[string]string{netboxv1alpha1.ParentOwnershipAnnotation: "no"},
			wantReason:  netboxv1alpha1.ReasonParentOwned,
			wantStatus:  metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
			}},
			wantWrites: 1,
		},
		{
			// Somebody else's owner reference is not ours to remove. It survives because
			// addOwner only ever appends -- there is no code path that rewrites an entry it
			// did not recognise.
			name:       "a foreign owner reference survives",
			resolution: parentOwnedBy("team-a", "europe-uid"),
			existing: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: "foreign-uid",
			}},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: "foreign-uid"},
				{
					APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
					Name: "europe", UID: "europe-uid",
				},
			},
			wantWrites: 1,
		},
		{
			// Idempotence: the reference is already there, so the pass writes nothing. Without
			// this the operator would patch every object of every kind on every resync.
			name:       "an owner reference already present is not rewritten",
			resolution: parentOwnedBy("team-a", "europe-uid"),
			existing: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
			}},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
			}},
			wantWrites: 0,
		},
		{
			// ADR-0003's dedupe rule: a controller owner reference and a containment owner
			// reference naming the same parent are one reference, and the one they are is the
			// controller reference. Downgrading it would take away the marker child
			// materialisation prunes by and specGuard reads to decide whose spec it may write.
			name:       "a controller reference to the same parent is not downgraded",
			resolution: parentOwnedBy("team-a", "europe-uid"),
			existing: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
				Controller: ptrTo(true), BlockOwnerDeletion: ptrTo(true),
			}},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "europe-uid",
				Controller: ptrTo(true), BlockOwnerDeletion: ptrTo(true),
			}},
			wantWrites: 0,
		},
		{
			// A parent deleted and recreated under the same name has a new uid, and a stale
			// one is worse than none: the garbage collector reads an owner it cannot find as
			// an owner that is gone, and deletes the dependent. So the uid is refreshed --
			// and only the uid.
			name:       "a stale uid is refreshed in place",
			resolution: parentOwnedBy("team-a", "new-uid"),
			existing: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "old-uid", Controller: ptrTo(true),
			}},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake",
				Name: "europe", UID: "new-uid", Controller: ptrTo(true),
			}},
			wantWrites: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := parentedObject()
			obj.Annotations = tc.annotations
			obj.OwnerReferences = tc.existing

			owners := &fakeOwners{}

			if _, err := ownerEngine(t, tc.resolution, owners).Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			condition := conditionOf(obj, netboxv1alpha1.ConditionParentOwned)
			if condition.Reason != tc.wantReason || condition.Status != tc.wantStatus {
				t.Errorf("ParentOwned = %s/%s, want %s/%s",
					condition.Status, condition.Reason, tc.wantStatus, tc.wantReason)
			}

			if tc.wantMessage != "" && !strings.Contains(condition.Message, tc.wantMessage) {
				t.Errorf("ParentOwned message = %q, want it to contain %q",
					condition.Message, tc.wantMessage)
			}

			if !equalOwners(obj.OwnerReferences, tc.wantOwners) {
				t.Errorf("ownerReferences = %+v, want %+v", obj.OwnerReferences, tc.wantOwners)
			}

			if len(owners.writes) != tc.wantWrites {
				t.Errorf("owner reference writes = %d, want %d: %+v",
					len(owners.writes), tc.wantWrites, owners.writes)
			}
		})
	}
}

// TestNoContainmentRefReportsNoOwnership is the guard against a condition on every object in
// the cluster: a kind with no containment parent -- every catalogue kind -- has nothing to
// say about a cascade, and saying "not applicable" a hundred and twenty times is worse than
// silence.
func TestNoContainmentRefReportsNoOwnership(t *testing.T) {
	obj := parentedObject()
	owners := &fakeOwners{}

	engine := ownerEngine(t, parentOwnedBy("team-a", "europe-uid"), owners)
	engine.Descriptors = fakeDescriptors{descriptor: fakeDescriptor(), registered: true}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if condition := conditionOf(obj, netboxv1alpha1.ConditionParentOwned); condition.Type != "" {
		t.Errorf("ParentOwned = %+v, want no condition: the descriptor names no containment ref", condition)
	}

	if len(obj.OwnerReferences) != 0 {
		t.Errorf("ownerReferences = %+v, want none", obj.OwnerReferences)
	}
}

// TestUnresolvedParentIsNotAnOwnershipReport keeps the two reports apart. A reference that
// did not resolve is already RefsResolved=False naming itself; a second condition saying the
// cascade is unavailable would report one fact twice and be free to disagree with the first.
func TestUnresolvedParentIsNotAnOwnershipReport(t *testing.T) {
	obj := parentedObject()
	owners := &fakeOwners{}

	engine := ownerEngine(t, blockedOnParent(resolver.ErrRefNotReady, 0), owners)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if condition := conditionOf(obj, netboxv1alpha1.ConditionParentOwned); condition.Type != "" {
		t.Errorf("ParentOwned = %+v, want no condition: RefsResolved already reports this", condition)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved); got.Status != metav1.ConditionFalse {
		t.Errorf("RefsResolved = %s, want False: the parent did not resolve", got.Status)
	}
}

// TestOwnerWriteFailureStopsThePass asserts the ordering claim. The owner reference is
// metadata the engine has to get onto the object, so a rejected write is a returned error and
// controller-runtime backoff -- exactly what a rejected finalizer write gets -- rather than a
// pass that carries on and reports a cascade it never established.
func TestOwnerWriteFailureStopsThePass(t *testing.T) {
	obj := parentedObject()
	owners := &fakeOwners{err: errOwnerWrite}

	engine := ownerEngine(t, parentOwnedBy("team-a", "europe-uid"), owners)

	_, err := engine.Reconcile(context.Background(), obj)
	if !errors.Is(err, errOwnerWrite) {
		t.Fatalf("Reconcile() = %v, want the owner write failure", err)
	}

	client, ok := engine.Endpoints.(fakeEndpoints).endpoint.Client.(*fakeClient)
	if !ok {
		t.Fatalf("endpoint client is %T, want *fakeClient", engine.Endpoints.(fakeEndpoints).endpoint.Client)
	}

	if methods := client.methods(); slices.Contains(methods, "Create") {
		t.Errorf("netbox calls = %v, want no Create: the pass stopped before writing", methods)
	}
}

// TestMissingOwnerWriterIsAWiringBug: a kind whose descriptor names a containment ref and an
// engine with no OwnerWriter must fail loudly. The alternative is an operator that silently
// never cascades, which is the state this whole ticket exists to end.
func TestMissingOwnerWriterIsAWiringBug(t *testing.T) {
	obj := parentedObject()

	engine := ownerEngine(t, parentOwnedBy("team-a", "europe-uid"), nil)
	engine.Owners = nil

	_, err := engine.Reconcile(context.Background(), obj)
	if !errors.Is(err, errNotConfigured) {
		t.Fatalf("Reconcile() = %v, want errNotConfigured", err)
	}
}

// equalOwners compares two owner-reference lists, treating nil and empty as the same thing --
// an object with no owners either way.
func equalOwners(got, want []metav1.OwnerReference) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i].APIVersion != want[i].APIVersion || got[i].Kind != want[i].Kind ||
			got[i].Name != want[i].Name || got[i].UID != want[i].UID ||
			!equalFlag(got[i].Controller, want[i].Controller) ||
			!equalFlag(got[i].BlockOwnerDeletion, want[i].BlockOwnerDeletion) {
			return false
		}
	}

	return true
}

// equalFlag compares two optional booleans, where nil and false are different states: nil is
// "this reference says nothing about it", which is what a containment reference sets.
func equalFlag(got, want *bool) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}

	return *got == *want
}

// ptrTo is the address of a value, for the optional booleans an owner reference carries.
func ptrTo[T any](v T) *T { return &v }

// unionOwner is the owner reference an object carries for one member of the scope union.
func unionOwner(kind, name, uid string, controller *bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: kind,
		Name: name, UID: types.UID(uid), Controller: controller,
	}
}

// scopeResolvedTo is a resolution that resolved the scope union to one CR: the member is
// identified by its target Kind, because that is all a resolved reference carries -- and all
// the owner-reference decision has to go on (#214).
func scopeResolvedTo(gvk schema.GroupVersionKind, name, uid string) resolver.Resolution {
	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{
		"scope": {{
			ID: 5, ObjectType: "dcim." + strings.ToLower(strings.TrimPrefix(gvk.Kind, "NetBox")),
			Mode:      resolver.ModeName,
			Target:    types.NamespacedName{Namespace: "team-a", Name: name},
			TargetGVK: gvk, TargetUID: types.UID(uid),
		}},
	}}
}

// disagreeingScopeDescriptor is the scoped kind whose union members disagree: NetBox deletes it
// with a region or a site group and not with a site or a location.
//
// The shape #202 could not express and #210 paid for. Written out as a fake rather than taken
// from a shipped Kind because every scoped Kind NetBox 4.6.8 ships cascades from all four
// members -- two by GenericRelation and two by dcim.CachedScopeMixin's CASCADE columns -- and
// the mechanism still has to be right for the Kind that does not.
func disagreeingScopeDescriptor() registry.Descriptor {
	d := scopedDescriptor()
	d.GenericFKs = []registry.GenericFKSpec{registry.ScopeFK("scope", map[string]bool{
		"regionRef": true, "siteGroupRef": true, "siteRef": false, "locationRef": false,
	})}

	return d
}

// TestOwnerReferenceFollowsTheResolvedUnionMember is #214: the containment owner reference is
// decided per pass from the member the object actually resolved through, and an object that
// moves between members must not be left carrying the one it left.
//
// The stale entry is the failure that matters, and it is worse than a missing cascade in both
// of its forms. Left beside a new one it gives the object two containment owners, which
// garbage collection ANDs -- "delete the region *or* the site" silently becomes "delete both".
// Left alone it is a promise about an object this one no longer references: deleting that
// former parent collects this object, and its finalizer then deletes a NetBox row that was
// never in its scope.
func TestOwnerReferenceFollowsTheResolvedUnionMember(t *testing.T) {
	region := netboxv1alpha1.RegionRef{}.TargetGVK()
	siteGroup := netboxv1alpha1.SiteGroupRef{}.TargetGVK()
	site := netboxv1alpha1.SiteRef{}.TargetGVK()

	tests := []struct {
		name string

		// descriptor is the scoped kind: every member cascading, or the union that disagrees.
		descriptor registry.Descriptor

		// resolved is the member the object's scope resolved to.
		resolved schema.GroupVersionKind

		existing    []metav1.OwnerReference
		wantReason  string
		wantStatus  metav1.ConditionStatus
		wantOwners  []metav1.OwnerReference
		wantWrites  int
		wantMessage string
	}{
		{
			// The cascade #210 was denied. A region-scoped object under a union that
			// disagrees still gets its owner reference: the member it uses is one NetBox
			// deletes through, and refusing the whole Kind for the sake of the other two
			// members is what cost this Kind its parent in the first place.
			name:       "a cascading member is owned even when a sibling does not cascade",
			descriptor: disagreeingScopeDescriptor(),
			resolved:   region,
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", nil)},
			wantWrites: 1,
		},
		{
			// The other half of the same union, and the reason one flag per pair could not
			// serve: an owner reference here would promise a deletion NetBox never performs,
			// so garbage collection would delete the CR and leave the row.
			name:        "a member that does not cascade is refused, and says which",
			descriptor:  disagreeingScopeDescriptor(),
			resolved:    site,
			wantReason:  netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  0,
			wantMessage: "netbox does not delete this object when that is deleted",
		},
		{
			// Moving *out* of a cascade: the region entry has to go, or deleting that region
			// deletes an object that is now scoped to a site inside it.
			name:        "moving from a cascading member to one that does not cascade disowns",
			descriptor:  disagreeingScopeDescriptor(),
			resolved:    site,
			existing:    []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", nil)},
			wantReason:  netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus:  metav1.ConditionFalse,
			wantOwners:  nil,
			wantWrites:  1,
			wantMessage: "netbox does not delete this object when that is deleted",
		},
		{
			// And back again, in one pass: the stale entry is dropped and the new one added
			// together, so the object is never observed carrying both.
			name:       "moving to a cascading member replaces the stale owner reference",
			descriptor: disagreeingScopeDescriptor(),
			resolved:   region,
			existing:   []metav1.OwnerReference{unionOwner("NetBoxSite", "hq", "hq-uid", nil)},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", nil)},
			wantWrites: 1,
		},
		{
			// A move between two members that both cascade, which is every scope move a
			// shipped Kind can make today. One owner reference out, one in -- not two.
			name:       "moving between two cascading members leaves exactly one owner",
			descriptor: scopedDescriptor(),
			resolved:   siteGroup,
			existing:   []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", nil)},
			wantReason: netboxv1alpha1.ReasonParentOwned,
			wantStatus: metav1.ConditionTrue,
			wantOwners: []metav1.OwnerReference{unionOwner("NetBoxSiteGroup", "eu-dc", "dc-uid", nil)},
			wantWrites: 1,
		},
		{
			// Somebody else's owner reference is outside the containment slot, so the removal
			// cannot see it: it names a Kind this reference does not point at.
			name:       "a foreign owner reference survives a disown",
			descriptor: disagreeingScopeDescriptor(),
			resolved:   site,
			existing: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: "foreign-uid",
			}},
			wantReason: netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus: metav1.ConditionFalse,
			wantOwners: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-else", UID: "foreign-uid",
			}},
			wantWrites: 0,
		},
		{
			// A controller reference belongs to whatever created the object (ADR-0003 rule 3)
			// and is the marker child materialisation prunes by. Removing it here would take
			// that away over a scope change that has nothing to do with it.
			name:       "a controller reference to a union member survives a disown",
			descriptor: disagreeingScopeDescriptor(),
			resolved:   site,
			existing:   []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", ptrTo(true))},
			wantReason: netboxv1alpha1.ReasonCascadeUnavailable,
			wantStatus: metav1.ConditionFalse,
			wantOwners: []metav1.OwnerReference{unionOwner("NetBoxRegion", "eu", "eu-uid", ptrTo(true))},
			wantWrites: 0,
		},
	}

	names := map[string]struct{ name, uid string }{
		"NetBoxRegion":    {"eu", "eu-uid"},
		"NetBoxSiteGroup": {"eu-dc", "dc-uid"},
		"NetBoxSite":      {"hq", "hq-uid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := fakeObject()
			// Set so that `scope` is a declared reference at all: an absent ref is never
			// resolved and so never owned. Which member is set does not decide the answer --
			// the resolution does -- but the field has to be there for the pass to look.
			obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}
			obj.OwnerReferences = tc.existing

			target := names[tc.resolved.Kind]
			owners := &fakeOwners{}

			engine := ownerEngine(t, scopeResolvedTo(tc.resolved, target.name, target.uid), owners)
			engine.Descriptors = fakeDescriptors{descriptor: tc.descriptor, registered: true}

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			condition := conditionOf(obj, netboxv1alpha1.ConditionParentOwned)
			if condition.Reason != tc.wantReason || condition.Status != tc.wantStatus {
				t.Errorf("ParentOwned = %s/%s, want %s/%s",
					condition.Status, condition.Reason, tc.wantStatus, tc.wantReason)
			}

			if tc.wantMessage != "" && !strings.Contains(condition.Message, tc.wantMessage) {
				t.Errorf("ParentOwned message = %q, want it to contain %q",
					condition.Message, tc.wantMessage)
			}

			if !equalOwners(obj.OwnerReferences, tc.wantOwners) {
				t.Errorf("ownerReferences = %+v, want %+v", obj.OwnerReferences, tc.wantOwners)
			}

			if len(owners.writes) != tc.wantWrites {
				t.Errorf("owner reference writes = %d, want %d: %+v",
					len(owners.writes), tc.wantWrites, owners.writes)
			}
		})
	}
}

// TestClearedContainmentRefDropsTheOwnerReference is the transition nothing used to handle:
// the reference stops resolving, and the owner reference outlives it.
//
// The engine cannot tell "the ref was deleted from the spec" from "the ref has not resolved
// yet", and it does not need to: removing is the safe direction either way. An object with no
// owner reference is not collected, so over-removing costs a cascade the next pass restores,
// while under-removing leaves a promise about a parent this object may no longer reference at
// all. The ParentOwned condition stays absent -- RefsResolved already reports the unresolved
// reference, and two conditions for one fact are free to disagree.
func TestClearedContainmentRefDropsTheOwnerReference(t *testing.T) {
	obj := parentedObject()
	obj.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake", Name: "europe", UID: "europe-uid"},
	}

	owners := &fakeOwners{}
	engine := ownerEngine(t, blockedOnParent(resolver.ErrRefNotReady, 0), owners)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(obj.OwnerReferences) != 0 {
		t.Errorf("ownerReferences = %+v, want none: the containment ref no longer resolves",
			obj.OwnerReferences)
	}

	if len(owners.writes) != 1 {
		t.Errorf("owner reference writes = %d, want 1: the removal has to be persisted",
			len(owners.writes))
	}

	if condition := conditionOf(obj, netboxv1alpha1.ConditionParentOwned); condition.Type != "" {
		t.Errorf("ParentOwned = %+v, want no condition: RefsResolved already reports this", condition)
	}
}

// TestOptingOutRemovesAnOwnerReferenceAlreadySet: the annotation is a statement about the
// object as it is now, not only about the pass that first saw it.
//
// Without the removal, `parent-ownership: "false"` would work on an object that never had the
// reference and do nothing at all for the one that did -- which is the object whose owner
// actually wants it off.
func TestOptingOutRemovesAnOwnerReferenceAlreadySet(t *testing.T) {
	obj := parentedObject()
	obj.Annotations = map[string]string{netboxv1alpha1.ParentOwnershipAnnotation: "false"}
	obj.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: "netbox.kubeforge.org/v1alpha1", Kind: "NetBoxFake", Name: "europe", UID: "europe-uid"},
	}

	owners := &fakeOwners{}

	if _, err := ownerEngine(t, parentOwnedBy("team-a", "europe-uid"), owners).
		Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(obj.OwnerReferences) != 0 {
		t.Errorf("ownerReferences = %+v, want none: ownership was declined", obj.OwnerReferences)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionParentOwned); got.Reason !=
		netboxv1alpha1.ReasonParentOwnershipDisabled {
		t.Errorf("ParentOwned = %s/%s, want False/%s", got.Status, got.Reason,
			netboxv1alpha1.ReasonParentOwnershipDisabled)
	}
}
