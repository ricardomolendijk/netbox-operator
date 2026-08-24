package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The one collaborator interface this file adds exists to be faked, and is only worth
// having if the real client satisfies it too.
var _ GrantReader = (*fakeGrants)(nil)

// endpointGVK is the Kind that is deliberately not covered by an empty kind list.
var endpointGVK = netboxv1alpha1.GroupVersion.WithKind(netboxv1alpha1.EndpointKind)

// crossRef is the reference every case here makes: from team-a into the catalogue namespace.
func crossRef(name string) netboxv1alpha1.ObjectRef {
	return netboxv1alpha1.ObjectRef{Name: name, Namespace: "catalogue"}
}

// endpointField is a reference to a NetBoxEndpoint, written the way a Descriptor would write
// one. `spec.endpointRef` is a plain string today and resolves in the object's own namespace,
// so this is the shape the endpoint work will use rather than one in the tree -- the rule has
// to be in place before the field that can trip it, or the exception lands after the hole.
func endpointField() registry.Field {
	return registry.Field{Spec: "endpointRef", API: "endpoint", Class: registry.ClassRefOne, Target: endpointGVK}
}

// kindsWithEndpoint is the descriptor source plus NetBoxEndpoint, so a reference to one can
// be dispatched at all.
func kindsWithEndpoint() fakeDescriptors {
	known := kinds()
	known.byGVK[endpointGVK] = registry.Descriptor{
		GVK: endpointGVK, Endpoint: "status", ObjectType: "netbox.endpoint",
	}

	return known
}

// TestGrantsAuthoriseACrossNamespaceReference is the whole decision table: which grants admit
// which reference, in the three axes a grant has.
//
// One table because the assertion is always the same -- resolved or ErrRefDenied -- and the
// interesting content is the grant. The target CR is present and Ready in every row, so a
// denial is never a missing object wearing a different hat.
func TestGrantsAuthoriseACrossNamespaceReference(t *testing.T) {
	tests := []struct {
		name string

		field  registry.Field
		ref    netboxv1alpha1.ObjectRef
		grants []netboxv1alpha1.NetBoxRefGrant

		// referrerLabels are the labels on the referring namespace, for the selector form.
		referrerLabels map[string]string

		wantAllowed bool
		wantMessage string
	}{
		{
			// The form ADR-0002 requires, and the reason this feature is usable at all: one
			// object makes a catalogue namespace readable by every namespace in the cluster.
			name:        "namespaces All admits every namespace",
			grants:      []netboxv1alpha1.NetBoxRefGrant{catalogueGrant("catalogue")},
			wantAllowed: true,
		},
		{
			name:        "no grant at all is denied",
			wantAllowed: false,
			wantMessage: `namespace "team-a" is not permitted to reference namespace "catalogue"`,
		},
		{
			// A grant in the *referring* namespace authorises nothing. It would be a claim
			// anybody could write about somebody else's objects rather than a capability the
			// target namespace handed out, and the list is scoped to the target namespace so
			// it is never even read.
			name:        "a grant in the referring namespace is not read",
			grants:      []netboxv1alpha1.NetBoxRefGrant{catalogueGrant("team-a")},
			wantAllowed: false,
		},
		{
			name: "a selector matching the referring namespace's labels admits it",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "gold",
				[]netboxv1alpha1.RefGrantFrom{fromLabelled("tier", "gold")}, nil)},
			referrerLabels: map[string]string{"tier": "gold"},
			wantAllowed:    true,
		},
		{
			name: "a selector that does not match is denied",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "gold",
				[]netboxv1alpha1.RefGrantFrom{fromLabelled("tier", "gold")}, nil)},
			referrerLabels: map[string]string{"tier": "silver"},
			wantAllowed:    false,
		},
		{
			// The one-namespace form, and the reason there is no separate list of namespace
			// names: the API server labels every Namespace with its own name.
			name: "a selector on the name label is the single-namespace form",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "team-a-only",
				[]netboxv1alpha1.RefGrantFrom{fromNamespaceNamed("team-a")}, nil)},
			wantAllowed: true,
		},
		{
			name: "a selector naming another namespace is denied",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "team-b-only",
				[]netboxv1alpha1.RefGrantFrom{fromNamespaceNamed("team-b")}, nil)},
			wantAllowed: false,
		},
		{
			// Several audiences in one object, which is what stops a grant per team.
			name: "any from entry is enough",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "several",
				[]netboxv1alpha1.RefGrantFrom{
					fromNamespaceNamed("team-b"), fromNamespaceNamed("team-a"),
				}, nil)},
			wantAllowed: true,
		},
		{
			name: "a to entry naming the kind admits it",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "regions",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Kinds: []string{"NetBoxRegion"}}})},
			wantAllowed: true,
		},
		{
			name: "a to entry naming another kind is denied",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "tags-only",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Kinds: []string{"NetBoxTag"}}})},
			wantAllowed: false,
		},
		{
			// A grant may name a Kind this build has never heard of, so it can be written
			// before the kind it points at exists. It is inert rather than an error.
			name: "an unknown kind in a to entry is inert",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "future",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{
					{Kinds: []string{"NetBoxWirelessLAN"}}, {Kinds: []string{"NetBoxRegion"}},
				})},
			wantAllowed: true,
		},
		{
			name: "a to entry naming the object admits it",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "emea-only",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Names: []string{"emea"}}})},
			wantAllowed: true,
		},
		{
			name: "a to entry naming another object is denied",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "apac-only",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Names: []string{"apac"}}})},
			wantAllowed: false,
		},
		{
			name: "a star name admits every object",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "any-name",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Names: []string{"*"}}})},
			wantAllowed: true,
		},
		{
			// A whole entry and never a prefix: a glob would make a grant's meaning depend on
			// naming discipline, and turn a rename into a silent permission change.
			name: "a star is not a prefix",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "prefix",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{{Names: []string{"em*"}}})},
			wantAllowed: false,
		},
		{
			// The security boundary of the feature. An empty kind list is the ergonomic
			// default and it stops short of the one reference that lends a token Secret.
			name:        "an empty kind list does not cover NetBoxEndpoint",
			field:       endpointField(),
			ref:         crossRef("shared"),
			grants:      []netboxv1alpha1.NetBoxRefGrant{catalogueGrant("catalogue")},
			wantAllowed: false,
			wantMessage: "an empty kind list never covers NetBoxEndpoint",
		},
		{
			name:  "NetBoxEndpoint named explicitly is admitted",
			field: endpointField(),
			ref:   crossRef("shared"),
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "lend-endpoint",
				[]netboxv1alpha1.RefGrantFrom{fromNamespaceNamed("team-a")},
				[]netboxv1alpha1.RefGrantTo{{
					Kinds: []string{netboxv1alpha1.EndpointKind}, Names: []string{"shared"},
				}})},
			wantAllowed: true,
		},
		{
			// Both halves have to match in the *same* entry, or "team-a may read tags" and
			// "everyone may read the endpoint" would compose into "everyone may read the
			// endpoint under any name".
			name:  "the kind and the name must match in one to entry",
			field: endpointField(),
			ref:   crossRef("shared"),
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "split",
				[]netboxv1alpha1.RefGrantFrom{fromAll()},
				[]netboxv1alpha1.RefGrantTo{
					{Kinds: []string{netboxv1alpha1.EndpointKind}, Names: []string{"lab"}},
					{Kinds: []string{"NetBoxRegion"}, Names: []string{"shared"}},
				})},
			wantAllowed: false,
		},
		{
			// A grant nothing can evaluate must not be a silent denial: the message names it,
			// because there is nowhere else the cause is written down.
			name: "a selector that cannot be compiled is reported rather than swallowed",
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "broken",
				[]netboxv1alpha1.RefGrantFrom{{
					Namespaces: netboxv1alpha1.NamespacesSelector,
					Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "tier", Operator: "Nonsense"},
					}},
				}}, nil)},
			wantAllowed: false,
			wantMessage: "netboxrefgrant catalogue/broken has a selector nothing can evaluate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field := tc.field
			if field.Spec == "" {
				field = regionField()
			}

			ref := tc.ref
			if ref.Name == "" {
				ref = crossRef("emea")
			}

			reader := &fakeReader{objects: []target{{
				gvk: field.Target, namespace: "catalogue", name: ref.Name, id: 31,
				ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			}}}
			grants := &fakeGrants{
				grants: tc.grants,
				labels: map[string]map[string]string{"team-a": tc.referrerLabels},
			}

			resolver := &Resolver{Objects: reader, Kinds: kindsWithEndpoint(), Grants: grants}

			result, err := resolver.Resolve(context.Background(), Request{
				NetBox:   &fakeNetBox{},
				Referrer: namespacedName("team-a", "ams"),
				Field:    field, Ref: ref,
			})

			if tc.wantAllowed {
				if err != nil {
					t.Fatalf("Resolve() = %v, want it to resolve", err)
				}

				if result.ID != 31 {
					t.Errorf("Resolve().ID = %d, want 31", result.ID)
				}

				return
			}

			assertBlocked(t, err, ErrRefDenied, field.Spec, field.Target, tc.wantMessage)

			// A denied reference must not have been read. Otherwise the difference between
			// "not found" and "denied" is an existence oracle for a namespace the referrer
			// has no access to.
			if reader.reads != 0 {
				t.Errorf("cluster reads = %d, want 0 for a denied reference", reader.reads)
			}
		})
	}
}

// TestSameNamespaceReferencesConsultNoGrant is the common case, and it has to stay free: a
// grant list per same-namespace reference would put a LIST on the hot path of almost every
// object in the cluster.
//
// Asserted on the read counts rather than on the outcome, because a resolver that consults a
// grant and then allows the reference anyway would pass an outcome-only test while still
// costing the request.
func TestSameNamespaceReferencesConsultNoGrant(t *testing.T) {
	grants := &fakeGrants{}
	resolver := &Resolver{
		Objects: &fakeReader{objects: []target{readyTarget()}}, Kinds: kinds(), Grants: grants,
	}

	for _, ref := range []netboxv1alpha1.ObjectRef{
		{Name: "emea"},
		// The namespace written out in full is still the referrer's own, so it is still not
		// crossing one. The check keys on the resolved namespace and not on whether the field
		// was set.
		{Name: "emea", Namespace: "team-a"},
	} {
		if _, err := resolver.Resolve(context.Background(), Request{
			NetBox: &fakeNetBox{}, Referrer: namespacedName("team-a", "ams"),
			Field: regionField(), Ref: ref,
		}); err != nil {
			t.Fatalf("Resolve(%+v) = %v, want it to resolve", ref, err)
		}
	}

	if grants.lists != 0 || grants.nsReads != 0 {
		t.Errorf("grant reads = %d lists and %d namespace reads, want none of either",
			grants.lists, grants.nsReads)
	}
}

// TestNamespacesAllReadsNoNamespace is why the wildcard form is the cheap one as well as the
// terse one: a cluster whose grants all say `namespaces: All` never reads a Namespace, and so
// never needs the cluster-wide informer that reading one through a cache starts.
func TestNamespacesAllReadsNoNamespace(t *testing.T) {
	grants := &fakeGrants{grants: []netboxv1alpha1.NetBoxRefGrant{catalogueGrant("catalogue")}}

	resolver := &Resolver{
		Objects: &fakeReader{objects: []target{{
			gvk: regionGVK, namespace: "catalogue", name: "emea", id: 31,
			ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
		}}},
		Kinds: kinds(), Grants: grants,
	}

	if _, err := resolver.Resolve(context.Background(), Request{
		NetBox: &fakeNetBox{}, Referrer: namespacedName("team-a", "ams"),
		Field: regionField(), Ref: crossRef("emea"),
	}); err != nil {
		t.Fatalf("Resolve() = %v, want it to resolve", err)
	}

	if grants.nsReads != 0 {
		t.Errorf("namespace reads = %d, want 0 for a grant that selects no labels", grants.nsReads)
	}
}

// TestGrantReadFailuresAreNotDenials keeps the two apart. A namespace this operator cannot
// read, or an API server that said no, is not a statement about anybody's grants: reporting
// it as RefDenied would send a team off to write one that was never missing.
func TestGrantReadFailuresAreNotDenials(t *testing.T) {
	tests := map[string]*fakeGrants{
		"the grant list failed": {listErr: errAPIServer},
		"the namespace read failed": {
			grants: []netboxv1alpha1.NetBoxRefGrant{grantIn("catalogue", "gold",
				[]netboxv1alpha1.RefGrantFrom{fromLabelled("tier", "gold")}, nil)},
			nsErr: errAPIServer,
		},
	}

	for name, grants := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds(), Grants: grants}

			_, err := resolver.Resolve(context.Background(), Request{
				NetBox: &fakeNetBox{}, Referrer: namespacedName("team-a", "ams"),
				Field: regionField(), Ref: crossRef("emea"),
			})

			assertFailure(t, err)
		})
	}
}

// TestNoGrantReaderFailsClosed is the wiring bug. A default-deny feature that switches itself
// off when a field is left unset is not one -- but it is a bug in this operator rather than a
// statement about a manifest, so it is a returned error and not a denial.
func TestNoGrantReaderFailsClosed(t *testing.T) {
	resolver := &Resolver{Objects: &fakeReader{objects: []target{readyTarget()}}, Kinds: kinds()}

	_, err := resolver.Resolve(context.Background(), Request{
		NetBox: &fakeNetBox{}, Referrer: namespacedName("team-a", "ams"),
		Field: regionField(), Ref: crossRef("emea"),
	})

	if !errors.Is(err, ErrNoGrantReader) {
		t.Fatalf("Resolve() = %v, want %v", err, ErrNoGrantReader)
	}

	assertFailure(t, err)
}

// TestDenialNamesWhatToCreate is the acceptance criterion for the message: somebody meeting
// default deny for the first time must get unstuck from the condition alone.
//
// Pinned verbatim, because a message that is nearly right is a message somebody has to go and
// read the docs for anyway -- which is the thing this is supposed to make unnecessary.
func TestDenialNamesWhatToCreate(t *testing.T) {
	resolver := &Resolver{
		Objects: &fakeReader{}, Kinds: kinds(),
		Grants: &fakeGrants{},
	}

	_, err := resolver.Resolve(context.Background(), Request{
		NetBox: &fakeNetBox{}, Referrer: namespacedName("team-a", "ams"),
		Field: regionField(), Ref: crossRef("emea"),
	})

	want := `regionRef -> netboxregion/catalogue/emea: denied ` +
		`(namespace "team-a" is not permitted to reference namespace "catalogue": ` +
		`create a NetBoxRefGrant in "catalogue" with ` +
		`spec.from: [{namespaces: Selector, selector: {matchLabels: {kubernetes.io/metadata.name: team-a}}}] ` +
		`and spec.to: [{kinds: [NetBoxRegion]}], ` +
		`or spec.from: [{namespaces: All}] to admit every namespace)`

	if err == nil || err.Error() != want {
		t.Errorf("Resolve() = %v,\nwant %s", err, want)
	}
}

// TestDeniedReferenceIsBlockedRatherThanFailed pins the reported outcome: a denial is a
// blocker on RefsResolved with its own reason, and the object is created without the field
// rather than not created at all.
func TestDeniedReferenceIsBlockedRatherThanFailed(t *testing.T) {
	obj := referrer("ams", map[string]any{
		"regionRef": map[string]any{"name": "emea", "namespace": "catalogue"},
	})

	resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds(), Grants: &fakeGrants{}}

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v, want a blocker rather than an error", err)
	}

	if len(resolution.Blocked) != 1 || resolution.Blocked[0].Field != "regionRef" {
		t.Fatalf("Blocked = %+v, want one blocker for regionRef", resolution.Blocked)
	}

	if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefDenied {
		t.Errorf("Reason() = %q, want %q", got, netboxv1alpha1.ReasonRefDenied)
	}

	// Nothing here improves on a timer: writing the grant is the fix, and the event that
	// arrives when somebody does is what wakes the object up (the grant watch, NBO-013).
	if got := resolution.Requeue(); got != 0 {
		t.Errorf("Requeue() = %s, want no timer for a state a grant clears", got)
	}
}

// TestOnlyNameModeIsAuthorised is the honest limit of the feature, and it has to be provable
// rather than merely documented: a slug reaches NetBox with the referring namespace's own
// endpoint and token, so there is no namespace on the far side to authorise against and no
// grant that could gate it.
func TestOnlyNameModeIsAuthorised(t *testing.T) {
	grants := &fakeGrants{}
	nb := &fakeNetBox{list: []netbox.Object{{"id": float64(31)}}}

	resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds(), Grants: grants}

	for _, ref := range []netboxv1alpha1.ObjectRef{
		{Slug: "emea"},
		{Lookup: map[string]string{"name": "emea"}},
	} {
		if _, err := resolver.Resolve(context.Background(), Request{
			NetBox: nb, Referrer: namespacedName("team-a", "ams"),
			Field: regionField(), Ref: ref,
		}); err != nil {
			t.Fatalf("Resolve(%+v) = %v, want it to resolve", ref, err)
		}
	}

	if grants.lists != 0 {
		t.Errorf("grant lists = %d, want 0: no grant can gate a reference that never enters a namespace",
			grants.lists)
	}
}

// TestGrantIsNotAnObjectKind guards the shape of this kind against the pattern every other
// one follows. NetBoxRefGrant is read by the resolver and reconciled by nothing: a Descriptor
// would claim it has a NetBox endpoint, and a controller would give it a reconcile loop with
// nothing to do in it.
func TestGrantIsNotAnObjectKind(t *testing.T) {
	grantGVK := netboxv1alpha1.GroupVersion.WithKind("NetBoxRefGrant")

	if d, registered := registry.Get(grantGVK); registered {
		t.Errorf("NetBoxRefGrant is registered as %+v; it is not a NetBox object", d)
	}
}

// TestTargetsOfDefaultsToEverythingButTheEndpoint pins the default that makes the three-line
// catalogue grant possible, at the boundary where it is decided.
func TestTargetsOfDefaultsToEverythingButTheEndpoint(t *testing.T) {
	bare := catalogueGrant("catalogue")

	entries := targetsOf(&bare)
	if len(entries) != 1 || len(entries[0].Kinds) != 0 || len(entries[0].Names) != 0 {
		t.Fatalf("targetsOf() = %+v, want one entry naming no kinds and no names", entries)
	}

	check := &grantCheck{from: "team-a", kind: netboxv1alpha1.EndpointKind, name: "shared"}
	if check.kindExposed(nil) {
		t.Error("kindExposed(nil) = true for NetBoxEndpoint, want false")
	}

	if !check.endpointExcluded {
		t.Error("the endpoint refusal was not recorded, so the message cannot explain itself")
	}

	if detail := check.detail(types.NamespacedName{Namespace: "catalogue", Name: "shared"}); !strings.Contains(
		detail, "lending an endpoint lends the token Secret behind it") {
		t.Errorf("detail() = %q, want it to say why the endpoint is excluded", detail)
	}
}
