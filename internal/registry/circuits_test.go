package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// circuitsKinds is NBO-057's catalogue slice: the five kinds of the `circuits` app whose
// identities can be confirmed against a committed artefact, and whose every reference has a
// Descriptor in this build.
//
// The six that are absent -- CircuitTermination, CircuitGroup, CircuitGroupAssignment,
// VirtualCircuitType, VirtualCircuit and VirtualCircuitTermination -- are deferred, not
// forgotten; TestDeferredCircuitsKindsAreAbsent below says so out loud.
var circuitsKinds = []struct {
	kind       string
	endpoint   string
	objectType string
}{
	{"NetBoxProvider", "circuits/providers", "circuits.provider"},
	{"NetBoxProviderAccount", "circuits/provider-accounts", "circuits.provideraccount"},
	{"NetBoxProviderNetwork", "circuits/provider-networks", "circuits.providernetwork"},
	{"NetBoxCircuitType", "circuits/circuit-types", "circuits.circuittype"},
	{"NetBoxCircuit", "circuits/circuits", "circuits.circuit"},
}

// TestCircuitsDescriptorsAreRegisteredAndValid is the boot check for NBO-057's five kinds.
func TestCircuitsDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range circuitsKinds {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// The endpoint is looked up in docs/netbox-schema.md's endpoint map, never
			// derived: `circuits/provider-accounts` is not the pluralisation of
			// `circuits.ProviderAccount`, and `circuits/circuit-types` is not
			// `circuits/circuittypes`.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch: a circuit and its catalogue are "+
					"ordinary PATCHable objects, and nothing here has the recreate semantics "+
					"dcim.Cable needs", d.UpdateStrategy)
			}

			// All five carry both mixins: four are PrimaryModels and circuits.CircuitType is an
			// OrganizationalModel through BaseCircuitType (docs/netbox-schema.md, bases).
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}

			// A provider, an account and a circuit are configuration a manifest recreates;
			// nothing here frees a resource when it is deleted, which is what #176 reserved
			// Retain for.
			if d.RetainOnDelete {
				t.Errorf("RetainOnDelete = true; a circuit is configuration a manifest " +
					"recreates (#176, docs/concepts/deletion.md)")
			}
		})
	}
}

// TestCircuitsNaturalKeysComeFromTheEvidence is NBO-057's central claim, and the five kinds
// arrive at their identities by three different routes over one app:
//
//   - circuits.Provider and circuits.CircuitType declare **no** meta.constraints, so the key is
//     a column-level UNIQUE on `slug` -- declared on the model itself for the provider, and two
//     base classes up (OrganizationalModel, through BaseCircuitType) for the type.
//   - circuits.ProviderNetwork and circuits.ProviderAccount each have a real UniqueConstraint
//     pair starting at `provider`.
//   - circuits.Circuit has two usable constraints and declares only the first as a candidate;
//     TestCircuitDeclaresOnlyTheProviderCandidate is the reason.
func TestCircuitsNaturalKeysComeFromTheEvidence(t *testing.T) {
	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"a provider is keyed on its column-unique slug alone": {
			kind: "NetBoxProvider",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		},
		"a circuit type is too, off OrganizationalModel two base classes up": {
			kind: "NetBoxCircuitType",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		},
		"a provider account is keyed on (provider, account), never on (provider, name)": {
			kind: "NetBoxProviderAccount",
			want: []NaturalKey{{Fields: []KeyField{
				{Filter: "provider_id", Spec: "providerRef"},
				{Filter: "account", Spec: "account"},
			}}},
		},
		"a provider network is keyed on (provider, name), which has no condition clause": {
			kind: "NetBoxProviderNetwork",
			want: []NaturalKey{{Fields: []KeyField{
				{Filter: "provider_id", Spec: "providerRef"},
				{Filter: "name", Spec: "name"},
			}}},
		},
		"a circuit is keyed on (provider, cid) and on nothing else": {
			kind: "NetBoxCircuit",
			want: []NaturalKey{{Fields: []KeyField{
				{Filter: "provider_id", Spec: "providerRef"},
				{Filter: "cid", Spec: "cid"},
			}}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !reflect.DeepEqual(d.NaturalKeys, tc.want) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.want)
			}
		})
	}
}

// TestProviderAccountNeverKeysOnName is the #206/#216 guard, spelled as its own case because it
// is the one identity in this slice that a reader would otherwise expect to see.
//
// `(provider, name)` *is* a UniqueConstraint on circuits.ProviderAccount, and it is unusable:
// its condition restricts it to rows whose `name` is not the empty string, and there is no
// NetBox filter for that. Unlike a null pin -- `?location_id=null`, which NBO-051's dcim.Rack
// uses and #216 made expressible -- the condition cannot be reproduced, so a candidate that
// dropped it would query the *unconstrained* set and adopt whatever came back first.
//
// The extractor agrees, which is what makes this evidence rather than an opinion: the committed
// IR records `unusable: "constraint condition is more than a null pin: ['name']"` for it
// (hack/testdata/ir-4.6.8.json.gz), surfaced as a row in docs/coverage.md.
func TestProviderAccountNeverKeysOnName(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxProviderAccount"))

	for _, key := range d.NaturalKeys {
		for _, f := range key.Fields {
			if f.Filter == "name" || f.Spec == "name" {
				t.Errorf("NetBoxProviderAccount has a natural-key term on `name` (%+v); the "+
					"constraint that names it carries condition=~Q(name='') and the IR marks it "+
					"unusable", f)
			}
		}

		for _, f := range key.NullFields {
			if f.Filter == "name" {
				t.Errorf("NetBoxProviderAccount pins `name` to null (%+v); ~Q(name='') is not a "+
					"null pin -- the column is NOT NULL and blank, so `name=''` and `name=null` "+
					"are different questions", f)
			}
		}
	}

	// `name` is still a writable field. Not being in the identity is not the same as not being
	// managed, and dropping the column would be a different bug.
	if !slices.ContainsFunc(d.Fields, func(f Field) bool { return f.Spec == "name" }) {
		t.Error("NetBoxProviderAccount does not map `name` at all; it is unusable as an " +
			"identity, not unwritable")
	}
}

// TestCircuitDeclaresOnlyTheProviderCandidate is the one judgement call in NBO-057's slice, so
// it is asserted rather than left to a comment.
//
// Both of circuits.Circuit's constraints are usable in the IR's terms -- `unusable: null` on
// each -- and only `(provider, cid)` is declared. The reason is that the two are keyed on
// *different* references: candidates are tried in order, so `(provider_account, cid)` can only
// fire once `(provider, cid)` has matched nothing, and the object it would then find is by
// construction a circuit sold by another provider. Adopting it means PATCHing `provider`.
//
// This is the opposite reading from NBO-051's dcim.RackType, whose fallback is safe precisely
// because both of its candidates share their leading `manufacturer` term.
func TestCircuitDeclaresOnlyTheProviderCandidate(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCircuit"))

	if len(d.NaturalKeys) != 1 {
		t.Fatalf("NaturalKeys has %d candidates, want 1", len(d.NaturalKeys))
	}

	for _, key := range d.NaturalKeys {
		for _, f := range key.Fields {
			if f.Spec == "providerAccountRef" {
				t.Errorf("NetBoxCircuit keys on providerAccountRef (%+v); the second constraint "+
					"is deliberately not a candidate, because it can only ever match a circuit "+
					"belonging to a different provider", f)
			}
		}
	}

	// With `providerRef` unresolved there is no applicable candidate at all, so the engine waits
	// rather than creating a circuit against whichever provider NetBox defaulted to. The
	// `providerAccountRef`-only row is what a second candidate would have made non-empty.
	for name, state := range map[string]SpecState{
		"a circuit whose provider has not been created yet": {
			Declared: []string{"cid", "providerRef", "typeRef"},
			Resolved: []string{"cid", "typeRef"},
		},
		"a circuit with an account but no resolved provider": {
			Declared: []string{"cid", "providerRef", "typeRef", "providerAccountRef"},
			Resolved: []string{"cid", "typeRef", "providerAccountRef"},
		},
	} {
		if got := d.Candidates(state); len(got) != 0 {
			t.Errorf("%s: Candidates(%+v) = %v, want none", name, state, got)
		}
	}

	// And with it resolved, exactly the one pair.
	state := SpecState{
		Declared: []string{"cid", "providerRef", "typeRef", "providerAccountRef"},
		Resolved: []string{"cid", "providerRef", "typeRef", "providerAccountRef"},
	}

	candidates := d.Candidates(state)

	got := make([][]string, 0, len(candidates))
	for _, key := range candidates {
		got = append(got, params(key))
	}

	if want := [][]string{{"provider_id", "cid"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Candidates(%+v) = %v, want %v", state, got, want)
	}
}

// TestCircuitNeverWritesTheTerminationPointers is NBO-057's read-only acceptance criterion,
// asserted where it can actually be enforced.
//
// `termination_a` and `termination_z` are real foreign keys back to
// circuits.CircuitTermination, and the IR records both as `read_only: true` while still listing
// them in the serializer's write path -- so a payload carrying one is accepted and dropped, and
// the engine would find the same difference on every reconcile. The authoritative relationship
// is `CircuitTermination.circuit` plus `term_side`, which is what carries
// `unique(circuit, term_side)`.
//
// Two writers for one relationship is how you get flapping, so the operator writes the
// termination side only. Nothing in the spec can express these columns; with them in ReadOnly,
// a field map that ever reaches for one fails Validate at boot (ErrFieldReadOnly) instead of
// PATCHing forever.
func TestCircuitNeverWritesTheTerminationPointers(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCircuit"))

	for _, column := range []string{"termination_a", "termination_z", "_abs_distance"} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("ReadOnly does not carry %q; a write to it is accepted and dropped, which "+
				"is a PATCH loop rather than an error", column)
		}

		if slices.ContainsFunc(d.Fields, func(f Field) bool { return f.API == column }) {
			t.Errorf("a spec field is mapped onto %q; NetBox writes it from the termination's "+
				"side and the operator must not be the second writer", column)
		}
	}
}

// TestCircuitsCountersAreReadOnly guards the two counters this slice's serializers return.
//
// NetBox ignores a write to a counter instead of refusing it, so with the column in ReadOnly a
// field map that ever reaches for one fails Validate at boot rather than PATCHing forever
// (docs/netbox-schema.md, preamble on every CounterCacheField).
func TestCircuitsCountersAreReadOnly(t *testing.T) {
	for kind, counter := range map[string]string{
		"NetBoxProvider":    "circuit_count",
		"NetBoxCircuitType": "circuit_count",
	} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

		if !slices.Contains(d.ReadOnly, counter) {
			t.Errorf("%s ReadOnly does not carry %q", kind, counter)
		}
	}
}

// TestCircuitsHaveNoContainmentParent reads the cascade direction, and contradicts nothing in
// the ticket only because the ticket does not ask for one here.
//
// Every foreign key in this slice is `on_delete=PROTECT` (docs/netbox-schema.md ->
// circuits.ProviderAccount, ProviderNetwork and Circuit), so none of them qualifies: an owner
// reference on a PROTECTed foreign key promises a cluster-side cascade NetBox refuses to
// perform, which deletes the CR and leaves the row
// (registry.ErrContainmentNotCascade, docs/decisions/0003-ownership-and-references.md rule 4).
//
// The `CASCADE` in this app is `CircuitTermination.circuit`, which runs from a Kind this
// milestone defers *into* NetBoxCircuit. When it ships, the containment ref goes on the
// termination and not here.
func TestCircuitsHaveNoContainmentParent(t *testing.T) {
	for _, tc := range circuitsKinds {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

		if d.ContainmentRef != "" {
			t.Errorf("%s ContainmentRef = %q, want empty: every FK it holds is PROTECT, so "+
				"nothing cascades server-side", tc.kind, d.ContainmentRef)
		}
	}
}

// TestProviderASNsAreAToManyReference pins the one field class in this slice that is not the
// default, because getting it wrong is silent in both directions.
//
// `asns` is a ManyToManyField onto ipam.ASN (docs/netbox-schema.md -> circuits.Provider).
// ClassRefMany is what makes the comparison an order-independent id set: NetBox does not
// preserve M2M order, so comparing it as an array would PATCH forever, and comparing it as a
// scalar would compare a list against a list of objects and never settle (fields.go).
func TestProviderASNsAreAToManyReference(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxProvider"))

	i := slices.IndexFunc(d.Fields, func(f Field) bool { return f.Spec == "asns" })
	if i < 0 {
		t.Fatal("NetBoxProvider does not map `asns`")
	}

	if got := d.Fields[i].Class; got != ClassRefMany {
		t.Errorf("asns Class = %q, want %q", got, ClassRefMany)
	}

	want := (netboxv1alpha1.ASNRef{}).TargetGVK()
	if d.Fields[i].Target != want {
		t.Errorf("asns Target = %s, want %s", d.Fields[i].Target, want)
	}

	if !slices.Contains(d.M2MFields(), "asns") {
		t.Error("asns is not in M2MFields(); the comparison would be order-sensitive")
	}
}

// TestCircuitTerminationIsStillOnlyACableUnionMember records what NBO-057 did *not* close, in
// the one place a future reader would look for it.
//
// `circuits.circuittermination` has been in `cabledObjectTypes()` since NBO-049, and
// `circuitTerminationRef` has been a member of `cableTerminationFK` for just as long -- both
// declared ahead of their Kind on purpose, so the union's `app_label.model` spelling is written
// down once. Registering the Descriptor is what would make a cable resolve one *by name*, and
// this milestone does not do it: the kind's generic foreign key and its `(circuit, term_side)`
// claim semantics are their own piece of work.
//
// Until then the member resolves to nothing and reports RefKindUnavailable, which is the
// designed behaviour and not a regression.
func TestCircuitTerminationIsStillOnlyACableUnionMember(t *testing.T) {
	if !slices.Contains(cabledObjectTypes(), "circuits.circuittermination") {
		t.Error("cabledObjectTypes() lost circuits.circuittermination; it is one of the nine " +
			"CabledObjectModel subclasses in NetBox 4.6.8")
	}

	gvk := (netboxv1alpha1.CircuitTerminationRef{}).TargetGVK()
	if _, ok := Get(gvk); ok {
		t.Errorf("%s now has a Descriptor. That is progress, not a failure -- but this test and "+
			"the deferral notes in internal/registry/circuits_circuit.go and "+
			"docs/reference/netboxcircuit.md were written on the assumption it does not, so "+
			"update them together with the cable-by-name assertion NBO-057 could not make", gvk)
	}
}

// TestDeferredCircuitsKindsAreAbsent states the boundary of this slice, so that "not shipped"
// is a checked fact rather than something a reader infers from a missing file.
//
// Six of NBO-057's eleven kinds are deferred. Each has a reason, and none of them is that the
// identity could not be confirmed:
//
//   - CircuitTermination: a generic foreign key over five targets plus cable-union membership.
//   - CircuitGroup: identity is fine (OrganizationalModel `slug`), but it exists to be pointed
//     at by CircuitGroupAssignment, which is the hard half.
//   - CircuitGroupAssignment: a `(member_type, member_id, group)` union whose members are
//     Circuit and VirtualCircuit -- and VirtualCircuit is not in this slice, so half the union
//     would report RefKindUnavailable for ever.
//   - VirtualCircuitType, VirtualCircuit, VirtualCircuitTermination: the overlay trio.
//     VirtualCircuitTermination has no meta.constraints at all -- `interface` is a
//     OneToOneField, so the interface *is* the key -- which is a claim shape of its own.
func TestDeferredCircuitsKindsAreAbsent(t *testing.T) {
	for _, kind := range []string{
		"NetBoxCircuitTermination",
		"NetBoxCircuitGroup",
		"NetBoxCircuitGroupAssignment",
		"NetBoxVirtualCircuitType",
		"NetBoxVirtualCircuit",
		"NetBoxVirtualCircuitTermination",
	} {
		gvk := netboxv1alpha1.GroupVersion.WithKind(kind)
		if _, ok := Get(gvk); ok {
			t.Errorf("%s is registered; NBO-057 defers it, so either the deferral notes in "+
				"docs/reference/netboxcircuit.md and docs/examples/circuits.yaml are stale or "+
				"this test is", kind)
		}
	}
}
