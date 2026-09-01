package resolver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestResolveGenericFKPairs is the whole contract of a polymorphic reference: a union
// resolves to an (object type, id) pair, one member at a time, in every mode.
//
// Every row asserts both halves. A row asserting only the id would pass for a resolver that
// wrote the wrong content type, which is the one failure NetBox does not reject -- it attaches
// the object to whatever row of the wrong model holds that primary key.
func TestResolveGenericFKPairs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		union    map[string]any
		objects  []target
		netbox   *fakeNetBox
		wantType string
		wantID   int64
		wantMode Mode
	}{
		{
			name:     "member resolved by name",
			union:    map[string]any{"regionRef": map[string]any{"name": "emea"}},
			objects:  []target{readyTarget()},
			wantType: "dcim.region",
			wantID:   12,
			wantMode: ModeName,
		},
		{
			name:  "a different member resolves against a different kind",
			union: map[string]any{"siteRef": map[string]any{"name": "ams"}},
			objects: []target{{
				gvk: siteGVK, namespace: "team-a", name: "ams", id: 31,
				ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			}},
			wantType: "dcim.site",
			wantID:   31,
			wantMode: ModeName,
		},
		{
			name:     "member resolved by slug needs no CR at all",
			union:    map[string]any{"siteRef": map[string]any{"slug": "ams"}},
			netbox:   &fakeNetBox{list: []netbox.Object{{"id": float64(44)}}},
			wantType: "dcim.site",
			wantID:   44,
			wantMode: ModeSlug,
		},
		{
			name:     "member resolved by lookup needs no CR at all",
			union:    map[string]any{"regionRef": map[string]any{"lookup": map[string]any{"name": "emea"}}},
			netbox:   &fakeNetBox{list: []netbox.Object{{"id": float64(9)}}},
			wantType: "dcim.region",
			wantID:   9,
			wantMode: ModeLookup,
		},
		{
			name:     "member resolved by id is verified and kept",
			union:    map[string]any{"regionRef": map[string]any{"id": int64(4)}},
			netbox:   &fakeNetBox{get: netbox.Object{"id": float64(4)}},
			wantType: "dcim.region",
			wantID:   4,
			wantMode: ModeID,
		},
		{
			// The instruction to clear both columns. Distinct from a spec that never wrote
			// the field, which never reaches the resolver at all.
			name:  "an empty union resolves to no type and no id",
			union: map[string]any{},
		},
		{
			// `regionRef: null` is not a member that was set. Read as one it would count
			// towards the "exactly one" rule and be refused as malformed.
			name:  "a null member is not a member that was set",
			union: map[string]any{"regionRef": nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{
				Objects: &fakeReader{objects: tc.objects}, Kinds: kinds(), Grants: &fakeGrants{},
			}

			got, err := r.ResolveGenericFK(context.Background(), genericRequest(tc.netbox, tc.union))
			if err != nil {
				t.Fatalf("ResolveGenericFK: %v", err)
			}

			if got.ObjectType != tc.wantType || got.ID != tc.wantID {
				t.Errorf("(objectType, id) = (%q, %d), want (%q, %d)",
					got.ObjectType, got.ID, tc.wantType, tc.wantID)
			}

			if got.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
		})
	}
}

// TestResolveGenericFKRefusals covers every way a union is refused, the reason each is
// reported under, and whether it comes back on a timer.
//
// The requeue is asserted alongside the reason because it is the difference between a
// condition a human fixes and a retry loop: an illegal target retried every minute is the
// operator arguing with a manifest it cannot win against.
func TestResolveGenericFKRefusals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		union       map[string]any
		wantErr     error
		wantReason  string
		wantRequeue bool
		wantDetail  []string
	}{
		{
			name: "two members set",
			union: map[string]any{
				"regionRef": map[string]any{"name": "emea"},
				"siteRef":   map[string]any{"name": "ams"},
			},
			wantErr:    ErrRefMalformed,
			wantReason: netboxv1alpha1.ReasonInvalid,
			wantDetail: []string{"regionRef", "siteRef", "exactly one"},
		},
		{
			name:       "a member the union does not declare",
			union:      map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			wantErr:    ErrRefTypeNotAllowed,
			wantReason: netboxv1alpha1.ReasonRefTypeNotAllowed,
			wantDetail: []string{"deviceRef", "regionRef", "siteRef", "tenantRef"},
		},
		{
			// tenantRef is a declared member whose Kind is registered nowhere, which is the
			// M3/M4 ordering: the union names Kinds that land two milestones later.
			name:        "a declared member whose kind has no descriptor",
			union:       map[string]any{"tenantRef": map[string]any{"name": "acme"}},
			wantErr:     ErrRefKindUnavailable,
			wantReason:  netboxv1alpha1.ReasonRefKindUnavailable,
			wantRequeue: true,
			wantDetail:  []string{"no descriptor is registered"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{Objects: &fakeReader{}, Kinds: kinds(), Grants: &fakeGrants{}}

			_, err := r.ResolveGenericFK(context.Background(), genericRequest(nil, tc.union))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveGenericFK error = %v, want %v", err, tc.wantErr)
			}

			outcome := Classify(err)
			if outcome.Reason != tc.wantReason {
				t.Errorf("Classify reason = %q, want %q", outcome.Reason, tc.wantReason)
			}

			if (outcome.Requeue != 0) != tc.wantRequeue {
				t.Errorf("Classify requeue = %v, want a timer: %v", outcome.Requeue, tc.wantRequeue)
			}

			for _, want := range tc.wantDetail {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// TestResolveGenericFKEnforcesAllowedTypes covers the second of the pair's two declarations:
// a member whose Kind is registered but whose object type the column will not take.
//
// It is not reachable through a well-formed descriptor -- Registry.Validate refuses one --
// and that is exactly why it is tested here: it is what a stored object hits after a CRD or a
// Descriptor narrows under it, and the answer has to be a refusal rather than a write.
func TestResolveGenericFKEnforcesAllowedTypes(t *testing.T) {
	pair := scopePair()
	pair.AllowedTypes = []string{"dcim.region"}

	r := &Resolver{
		Objects: &fakeReader{objects: []target{{
			gvk: siteGVK, namespace: "team-a", name: "ams", id: 31,
			ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
		}}},
		Kinds: kinds(), Grants: &fakeGrants{},
	}

	_, err := r.ResolveGenericFK(context.Background(), GenericRequest{
		Referrer: namespacedName("team-a", "prefix"), ReferrerGVK: siteGVK, Pair: pair,
		Union: unionOf(map[string]any{"siteRef": map[string]any{"name": "ams"}}),
	})
	if !errors.Is(err, ErrRefTypeNotAllowed) {
		t.Fatalf("ResolveGenericFK error = %v, want ErrRefTypeNotAllowed", err)
	}

	// Both halves in the message: what was given and what the column accepts. Either one
	// alone leaves the reader guessing at the other.
	for _, want := range []string{"siteRef", "dcim.site", "scope_type", "dcim.region"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	if requeue := Classify(err).Requeue; requeue != 0 {
		t.Errorf("Classify requeue = %v, want none: no event makes an illegal target legal", requeue)
	}
}

// TestResolveAllReportsGenericFKUnderItsUnionField pins the key the engine looks the pair up
// under: the union's own spec field, not the member that resolved.
//
// The engine writes both columns from one entry, so one entry is what it has to find. Keying
// on the member instead would leave the pair unwritten for every union but the first.
func TestResolveAllReportsGenericFKUnderItsUnionField(t *testing.T) {
	r := &Resolver{
		Objects: &fakeReader{objects: []target{readyTarget()}}, Kinds: kinds(), Grants: &fakeGrants{},
	}

	obj := referrer("prefix", map[string]any{
		"name":  "prefix",
		"scope": map[string]any{"regionRef": map[string]any{"name": "emea"}},
	})

	resolution, err := r.ResolveAll(context.Background(), nil, obj, genericDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}

	if len(resolution.Blocked) != 0 {
		t.Fatalf("Blocked = %v, want none", resolution.Message())
	}

	got, ok := resolution.ByField["scope"]
	if !ok {
		t.Fatalf("ByField = %v, want an entry under the union field", resolution.ByField)
	}

	// One element, always: a union selects one target, so there is no list here and no
	// partial-list rule for the engine to apply (NBO-088).
	if len(got) != 1 {
		t.Fatalf("ByField[scope] = %v, want exactly one result", got)
	}

	if got[0].ObjectType != "dcim.region" || got[0].ID != 12 {
		t.Errorf("(objectType, id) = (%q, %d), want (dcim.region, 12)", got[0].ObjectType, got[0].ID)
	}
}

// TestGenericTargetsComeFromAllowedTypes covers the reverse index: an `app_label.model`
// string in AllowedTypes becomes a Kind to watch.
//
// Without it a polymorphic reference had no watch target at all and converged only on the
// referrer's resync (#25). The unregistered type is left out on purpose: there is no informer
// to watch and no key an index could produce.
func TestGenericTargetsComeFromAllowedTypes(t *testing.T) {
	pair := scopePair()
	pair.AllowedTypes = append(pair.AllowedTypes, "tenancy.tenant")

	d := genericDescriptor()
	d.GenericFKs = []registry.GenericFKSpec{pair}

	got := refTargets(kinds(), d)

	want := []schema.GroupVersionKind{regionGVK, siteGVK}
	if len(got) != len(want) {
		t.Fatalf("refTargets = %v, want %v", got, want)
	}

	for i, gvk := range want {
		if got[i] != gvk {
			t.Errorf("refTargets[%d] = %s, want %s", i, got[i], gvk)
		}
	}
}

// TestGenericFKMembersAreIndexed proves the other half of the watch: an event on the target
// finds this object, because the union member is indexed under the object it selected.
func TestGenericFKMembersAreIndexed(t *testing.T) {
	obj := referrer("prefix", map[string]any{
		"scope": map[string]any{"regionRef": map[string]any{"name": "emea", "namespace": "catalogue"}},
	})

	keys := refIndexer(genericDescriptor())(obj)

	want := IndexValue(regionGVK, "catalogue", "emea")
	if len(keys) != 1 || keys[0] != want {
		t.Errorf("index keys = %v, want [%s]", keys, want)
	}
}

// TestGenericFKSlugMemberIsNotIndexed keeps the index bounded: a member that terminates in
// NetBox has no Kubernetes object an event could arrive for, so a key for it is one nothing
// ever queries.
func TestGenericFKSlugMemberIsNotIndexed(t *testing.T) {
	obj := referrer("prefix", map[string]any{
		"scope": map[string]any{"siteRef": map[string]any{"slug": "ams"}},
	})

	if keys := refIndexer(genericDescriptor())(obj); len(keys) != 0 {
		t.Errorf("index keys = %v, want none", keys)
	}
}

// genericRequest is one union to resolve, in the shape a manifest writes it.
func genericRequest(nb *fakeNetBox, members map[string]any) GenericRequest {
	req := GenericRequest{
		Referrer: namespacedName("team-a", "prefix"), ReferrerGVK: siteGVK,
		Pair: scopePair(), Union: unionOf(members),
	}

	if nb != nil {
		req.NetBox = nb
	}

	return req
}

// unionOf decodes a union written as YAML would write it, through the same path a reconcile
// reads it: so a table row that decodes to nothing fails the test rather than passing it.
func unionOf(members map[string]any) map[string]netboxv1alpha1.ObjectRef {
	generics, err := genericFKsOf(
		referrer("prefix", map[string]any{"scope": members}), genericDescriptor())
	if err != nil {
		panic(err)
	}

	if len(generics) != 1 || len(generics[0].unions) != 1 {
		panic("the fixture union did not decode")
	}

	return generics[0].unions[0]
}

// TestEveryAllowedTypeIsWatched walks the registry rather than naming kinds, so a descriptor
// that grows a polymorphic pair gets its watches checked without anybody remembering to add a
// case here.
//
// The criterion is the one #25 asked for: every entry in every AllowedTypes list that has a
// registered Descriptor is a watch target. Vacuous while no shipped kind declares a pair, and
// that is fine -- it is the guard for the first one that does, whose absence would show up as
// "that kind converges only on its resync", which the resync then hides.
func TestEveryAllowedTypeIsWatched(t *testing.T) {
	for _, d := range registry.List() {
		targets := RefTargets(d)

		for _, pair := range d.GenericFKs {
			for _, objectType := range pair.AllowedTypes {
				target, registered := registry.ByObjectType(objectType)
				if !registered {
					continue
				}

				if !slices.Contains(targets, target.GVK) {
					t.Errorf("%s.%s allows %s and does not watch %s",
						d.GVK.Kind, pair.Spec, objectType, target.GVK.Kind)
				}
			}
		}
	}
}

// TestUnavailableMemberKindIsReportedInEveryMode is the answer to "surely `slug` still works":
// it does not, and the reason is structural rather than an omission.
//
// All four modes need the target's REST endpoint -- `slug` and `lookup` to query it, `id` to
// verify the row is there, `name` to read the CR that holds the id -- and the only thing that
// holds an endpoint is the target's Descriptor. So a member whose Kind this build does not
// carry is RefKindUnavailable in every mode, not just the two that read the cluster.
//
// NBO-018 and NBO-019 reached this independently for their own unions; it is asserted once,
// here, because it is a property of a member with no Descriptor and not of any one union.
func TestUnavailableMemberKindIsReportedInEveryMode(t *testing.T) {
	// tenantRef is the declared member kinds() registers no Descriptor for. NetBoxSiteGroup
	// and NetBoxLocation were the shipped example until NBO-066 gave them Descriptors; the
	// property is about a member with no Descriptor, so it is asserted on a union member that
	// still has none rather than restated per union.
	for name, ref := range map[string]any{
		"name":   map[string]any{"name": "acme"},
		"slug":   map[string]any{"slug": "acme"},
		"lookup": map[string]any{"lookup": map[string]any{"name": "acme"}},
		"id":     map[string]any{"id": int64(4)},
	} {
		t.Run(name, func(t *testing.T) {
			nb := &fakeNetBox{
				list: []netbox.Object{{"id": float64(4)}}, get: netbox.Object{"id": float64(4)},
			}
			r := &Resolver{Objects: &fakeReader{}, Kinds: kinds(), Grants: &fakeGrants{}}

			_, err := r.ResolveGenericFK(context.Background(),
				genericRequest(nb, map[string]any{"tenantRef": ref}))
			if !errors.Is(err, ErrRefKindUnavailable) {
				t.Fatalf("ResolveGenericFK error = %v, want ErrRefKindUnavailable", err)
			}

			// And not one request on the way to that answer: without a Descriptor there is
			// no endpoint to send it to, so a request here would be aimed at a guess.
			if len(nb.calls) != 0 {
				t.Errorf("netbox calls = %v, want none", nb.calls)
			}
		})
	}
}

// A **to-many** polymorphic pair: one API field carrying a list of nested `(type, id)`
// objects, which is what `dcim.Cable.a_terminations` is. Everything below is the second half
// of NBO-049's finding -- that the union shape survives dcim.Cable on the CR side and not on
// the descriptor side, so the pair grew a cardinality (registry.GenericFKList).
//
// The fixture reuses the scope pair's members rather than the cable's, deliberately: what these
// cases are about is the cardinality, and the cable's own union is exercised end to end over
// its real Descriptor in internal/reconciler/dcim_cable_test.go.

// scopeListPair is scopePair as a to-many pair.
func scopeListPair() registry.GenericFKSpec {
	pair := scopePair()
	pair.Spec = "scopes"
	pair.List = &registry.GenericFKList{
		APIField: "scope_list", TypeKey: "object_type", IDKey: "object_id",
	}

	return pair
}

// genericListDescriptor is a referrer carrying one to-many polymorphic pair and nothing else.
func genericListDescriptor() registry.Descriptor {
	d := genericDescriptor()
	d.GenericFKs = []registry.GenericFKSpec{scopeListPair()}

	return d
}

// TestDecodeUnionsReadsAToManyPairAsOnePerElement is the only place the resolver reads
// GenericFKSpec.List: one union per element, in the order the manifest wrote them.
//
// The order is kept here and sorted later, by the engine, because the *message* a blocked
// element produces has to name the index the user wrote -- `scopes[1]` -- while the *payload*
// must not depend on it.
func TestDecodeUnionsReadsAToManyPairAsOnePerElement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  any
		want  int
		first string
	}{
		{
			name: "two elements",
			spec: []any{
				map[string]any{"regionRef": map[string]any{"name": "emea"}},
				map[string]any{"siteRef": map[string]any{"name": "ams"}},
			},
			want: 2, first: "regionRef",
		},
		{
			name: "one element",
			spec: []any{map[string]any{"regionRef": map[string]any{"name": "emea"}}},
			want: 1, first: "regionRef",
		},
		{
			// The instruction to clear the whole list, which is distinct from an absent field:
			// NetBox rebuilds the termination rows from what the field carries, so `[]` removes
			// them and omitting the field leaves them alone.
			name: "an empty list is declared and selects nothing",
			spec: []any{},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := referrer("cable", map[string]any{"scopes": tc.spec})

			generics, err := genericFKsOf(obj, genericListDescriptor())
			if err != nil {
				t.Fatalf("genericFKsOf: %v", err)
			}

			if len(generics) != 1 {
				t.Fatalf("genericFKsOf = %v, want one declared pair", generics)
			}

			if got := len(generics[0].unions); got != tc.want {
				t.Fatalf("decoded %d unions, want %d", got, tc.want)
			}

			if tc.first == "" {
				return
			}

			if _, set := generics[0].unions[0][tc.first]; !set {
				t.Errorf("element 0 = %v, want %s set", generics[0].unions[0], tc.first)
			}
		})
	}
}

// TestGenericFKsOfIgnoresAnAbsentToManyPair keeps "absent means do not manage" true one level
// out: a field the spec never wrote is not a list to clear.
func TestGenericFKsOfIgnoresAnAbsentToManyPair(t *testing.T) {
	obj := referrer("cable", map[string]any{"name": "cable"})

	generics, err := genericFKsOf(obj, genericListDescriptor())
	if err != nil {
		t.Fatalf("genericFKsOf: %v", err)
	}

	if len(generics) != 0 {
		t.Errorf("genericFKsOf = %v, want none: an absent list is not an instruction", generics)
	}
}

// TestResolveAllFilesEveryElementOfAToManyPair is the shape the engine reads: one FieldRefs
// under the union's own spec field, carrying one Result per element.
//
// FieldRefs was already a slice, which is why the to-many pair needed no new carrier -- the
// to-one case files a one-element one and this files N.
func TestResolveAllFilesEveryElementOfAToManyPair(t *testing.T) {
	r := &Resolver{
		Objects: &fakeReader{objects: []target{
			readyTarget(),
			{
				gvk: siteGVK, namespace: "team-a", name: "ams", id: 31,
				ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			},
		}},
		Kinds: kinds(), Grants: &fakeGrants{},
	}

	obj := referrer("cable", map[string]any{"scopes": []any{
		map[string]any{"regionRef": map[string]any{"name": "emea"}},
		map[string]any{"siteRef": map[string]any{"name": "ams"}},
	}})

	resolution, err := r.ResolveAll(context.Background(), nil, obj, genericListDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}

	if len(resolution.Blocked) != 0 {
		t.Fatalf("Blocked = %v, want none", resolution.Message())
	}

	got := resolution.ByField["scopes"]
	if len(got) != 2 {
		t.Fatalf("ByField[scopes] = %v, want two results", got)
	}

	if got[0].ObjectType != "dcim.region" || got[0].ID != 12 {
		t.Errorf("element 0 = (%q, %d), want (dcim.region, 12)", got[0].ObjectType, got[0].ID)
	}

	if got[1].ObjectType != "dcim.site" || got[1].ID != 31 {
		t.Errorf("element 1 = (%q, %d), want (dcim.site, 31)", got[1].ObjectType, got[1].ID)
	}
}

// TestResolveAllIsAllOrNothingForAToManyPair is the precondition rule for a list NetBox
// replaces wholesale.
//
// One of two elements resolving is worse than none: `a_terminations` is a full replacement, so
// a half list is a cable connected at one end -- and on a Recreate kind, correcting that means
// deleting and re-creating rather than PATCHing.
func TestResolveAllIsAllOrNothingForAToManyPair(t *testing.T) {
	r := &Resolver{
		Objects: &fakeReader{objects: []target{readyTarget()}}, Kinds: kinds(), Grants: &fakeGrants{},
	}

	obj := referrer("cable", map[string]any{"scopes": []any{
		map[string]any{"regionRef": map[string]any{"name": "emea"}},
		map[string]any{"regionRef": map[string]any{"name": "apac"}},
	}})

	resolution, err := r.ResolveAll(context.Background(), nil, obj, genericListDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}

	if _, filed := resolution.ByField["scopes"]; filed {
		t.Errorf("ByField[scopes] = %v, want nothing: one of two elements resolved",
			resolution.ByField["scopes"])
	}

	// The message names the element by its index, because "scopes did not resolve" does not
	// say which of several.
	if want := "scopes[1].regionRef"; !strings.Contains(resolution.Message(), want) {
		t.Errorf("Message() = %q, want it to name %s", resolution.Message(), want)
	}
}

// TestEveryToManyElementIsIndexed proves the watch: an event on any one of the objects a list
// points at wakes the referrer, rather than only the first.
func TestEveryToManyElementIsIndexed(t *testing.T) {
	obj := referrer("cable", map[string]any{"scopes": []any{
		map[string]any{"regionRef": map[string]any{"name": "emea", "namespace": "catalogue"}},
		map[string]any{"siteRef": map[string]any{"name": "ams"}},
	}})

	keys := refIndexer(genericListDescriptor())(obj)

	want := []string{
		IndexValue(regionGVK, "catalogue", "emea"),
		IndexValue(siteGVK, "team-a", "ams"),
	}

	slices.Sort(keys)
	slices.Sort(want)

	if !slices.Equal(keys, want) {
		t.Errorf("index keys = %v, want %v", keys, want)
	}
}
