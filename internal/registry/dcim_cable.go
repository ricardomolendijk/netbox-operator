package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimCableDescriptor()) }

// The four CableFilterSet parameters dcim.Cable's identity is built from, spelled once.
//
// Not columns. `dcim.Cable` has no termination columns at all -- the rows live on
// dcim.CableTermination -- and these are the filterset's names for reaching them from the
// cable's own endpoint (netbox/dcim/filtersets.py:2637):
//
//	termination_a_type  MultiValueContentTypeFilter over terminations__termination_type
//	termination_a_id    MultiValueNumberFilter, method filter_by_cable_end_a
//	termination_b_type  MultiValueContentTypeFilter over terminations__termination_type
//	termination_b_id    MultiValueNumberFilter, method filter_by_cable_end_b
//
// They are checked rather than assumed for the reason #206 exists: django-filter drops a
// parameter it does not recognise and answers with the *unfiltered* set, so a guessed filter
// name is a lookup that matches every cable in NetBox -- and on a kind that adopts what it
// finds, that is the worst possible failure.
//
// The `_type` halves take the `app_label.model` string and not a ContentType id:
// MultiValueContentTypeFilter splits on `.` and resolves through
// `ContentType.objects.get_by_natural_key` (netbox/utilities/filters.py:186-207), the same
// filter class ipam.VLANGroup's `scope_type` uses.
const (
	// CableTerminationATypeField is the A end's content-type filter.
	CableTerminationATypeField = "termination_a_type"

	// CableTerminationAIDField is the A end's object-id filter.
	CableTerminationAIDField = "termination_a_id"

	// CableTerminationBTypeField is the B end's content-type filter.
	CableTerminationBTypeField = "termination_b_type"

	// CableTerminationBIDField is the B end's object-id filter.
	CableTerminationBIDField = "termination_b_id"
)

// cabledObjectTypes is every object type NetBox 4.6.8 will accept in a cable termination: the
// nine models that mix in `dcim.CabledObjectModel`.
//
// Derived from NetBox rather than from the Members list below, deliberately, and that is the
// whole reason it is written out. Registry.Validate cross-checks every member whose Kind this
// build carries against this list (ErrMemberTypeNotAllowed), and the check only means
// something while the two are stated independently: a list computed from the members would
// make the boot check tautological.
//
// The gate is `CableTermination.clean()`, which refuses a termination whose object is not a
// CabledObjectModel, and the queryset on the termination view is limited the same way. One line
// per model with the `bases:` line naming the mixin, because "which models are cablable" is the
// fact a NetBox version bump changes and the citation is what makes the next diff reviewable.
// The count is 9 in 4.6.8.
func cabledObjectTypes() []string {
	return []string{
		"circuits.circuittermination", // circuits/models/circuits.py, bases: ... CabledObjectModel
		"dcim.consoleport",            // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.consoleserverport",      // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.frontport",              // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.interface",              // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.powerfeed",              // dcim/models/power.py, bases: ... CabledObjectModel
		"dcim.poweroutlet",            // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.powerport",              // dcim/models/device_components.py, bases: ... CabledObjectModel
		"dcim.rearport",               // dcim/models/device_components.py, bases: ... CabledObjectModel
	}
}

// cableTerminationFK is the union behind one cable end.
//
// **The first to-many polymorphic pair in the catalogue**, and the reason
// registry.GenericFKList exists. `a_terminations` is one serializer field carrying a list of
// `{object_type, object_id}` objects -- `GenericObjectSerializer(many=True, required=False)`
// (netbox/dcim/api/serializers_/cables.py:40) over
// netbox/netbox/api/serializers/generic.py:15 -- so the pair is nested inside a list element
// and there are no two columns of the cable to write. The to-one shape fits in neither its
// cardinality nor its nesting; see docs/concepts/generic-refs.md, "A to-many pair".
//
// Called twice, once per end, because the two ends are two independent pairs that happen to
// share a member list. A shared constructor rather than two literals for the reason
// registry.ScopeFK is one: the member list, the allowed types and the cascade answer are one
// fact each and must not be able to differ between the ends.
//
// **Every member declares `CascadeOnDelete: false`.** Stated rather than left unstated, because
// GenericFKMember.CascadeOnDelete is all-or-none at boot (ErrMemberCascadePartial) and
// "unstated" is a third answer that would leave the reason unrecorded. The reason: the cascade
// on this relation runs the *other way*. `dcim.CabledObjectModel.cable` is
// `on_delete=SET_NULL` (docs/netbox-schema.md -> dcim.CabledObjectModel), so deleting the
// interface clears that interface's own `cable` column and leaves the cable and its
// CableTermination rows standing -- the cable is not deleted, and a CR that took an owner
// reference on its terminations would be garbage-collected while NetBox still held the row.
// dcim.CableTermination's own `cable` FK is `CASCADE`, but that is the cable deleting *its*
// terminations, which is the opposite direction and not a fact about this union.
func cableTerminationFK(spec, typeFilter, idFilter, apiField string) GenericFKSpec {
	cascade := false
	members := []GenericFKMember{
		{Spec: "interfaceRef", Target: netboxv1alpha1.InterfaceRef{}.TargetGVK()},
		{Spec: "consolePortRef", Target: netboxv1alpha1.ConsolePortRef{}.TargetGVK()},
		{Spec: "consoleServerPortRef", Target: netboxv1alpha1.ConsoleServerPortRef{}.TargetGVK()},
		{Spec: "powerPortRef", Target: netboxv1alpha1.PowerPortRef{}.TargetGVK()},
		{Spec: "powerOutletRef", Target: netboxv1alpha1.PowerOutletRef{}.TargetGVK()},
		{Spec: "frontPortRef", Target: netboxv1alpha1.FrontPortRef{}.TargetGVK()},
		{Spec: "rearPortRef", Target: netboxv1alpha1.RearPortRef{}.TargetGVK()},
		{Spec: "powerFeedRef", Target: netboxv1alpha1.PowerFeedRef{}.TargetGVK()},
		{Spec: "circuitTerminationRef", Target: netboxv1alpha1.CircuitTerminationRef{}.TargetGVK()},
	}

	for i := range members {
		members[i].CascadeOnDelete = &cascade
	}

	return GenericFKSpec{
		TypeField:    typeFilter,
		IDField:      idFilter,
		Spec:         spec,
		AllowedTypes: cabledObjectTypes(),
		Members:      members,
		List: &GenericFKList{
			APIField: apiField,
			// GenericObjectSerializer's own names, not the model's: the columns behind them
			// are `termination_type` and `termination_id` on dcim.CableTermination, and the
			// serializer that writes them calls them `object_type` and `object_id`
			// (netbox/netbox/api/serializers/generic.py:15).
			TypeKey: "object_type",
			IDKey:   "object_id",
		},
		// No cached columns, and nowhere to put any: dcim.CableTermination's `_device`,
		// `_rack`, `_location` and `_site` are caches on the *termination row*, not on the
		// cable, so they are not columns of this kind at all. validateGenericFKList refuses a
		// to-many pair that declares caches for exactly that reason.
		Cached: nil,
	}
}

// dcimCableDescriptor is dcim.Cable as data.
//
// The model that stresses this operator's shape hardest, and it does so in three places.
//
// **Its identity is a convention, not a constraint.** `dcim.Cable`'s entire Meta is
// `meta.ordering: ('pk',)` -- **no `meta.constraints` whatsoever** (docs/netbox-schema.md ->
// dcim.Cable). There is no natural key on the cable row: not `label`, which NetBox lets any
// number of cables share, and not a `(type, length)` pair, which is not identity at all. The
// key below is therefore the strongest question NetBox will answer rather than a constraint it
// enforces, and that has a consequence a user meets: a `Conflict` on this kind means "some
// other cable is already terminated on the endpoint you asked for", not "two objects claim one
// unique key". docs/reference/netboxcable.md states it in those words.
//
// What makes the convention sound is a constraint one model over.
// `unique(termination_type, termination_id)` on dcim.CableTermination (docs/netbox-schema.md ->
// dcim.CableTermination, meta.constraints) means an object is terminated by **at most one
// cable, globally** -- so an A-end termination identifies one cable or none, and adoption by
// termination cannot pick the wrong cable. Both ends are filtered on anyway, because
// `termination_a_type` carries no `filter_by_cable_end_a` method of its own and so is not
// pinned to the A end on its own; the four together are as narrow as the API gets.
//
// **Its terminations are not PATCHable.** They are CableTermination rows rather than columns,
// and the unique constraint above keeps the wanted endpoint occupied by the *old* cable until
// it is deleted -- so the replacement cannot be created first. Hence `UpdateRecreate` with
// `RecreateOn` naming the two termination fields and nothing else: a cable's `label` is an
// ordinary PATCH, and a strategy without the field list would make every edit destructive.
// The engine's recreate step (reconciler/generic.go, pass.recreate) does the DELETE-then-POST
// and reports it as `Recreated`; this descriptor is the whole of the per-kind input to it.
//
// **The A/B ends are not symmetric to the operator even though they are to a cable.** A user
// who swaps `aTerminations` and `bTerminations` in a manifest describes the same physical link
// and a *different* natural key, so the lookup misses and NetBox refuses the create on its
// unique constraint -- a loud 400 surfaced as `Ready=False, Reason=Invalid`, not a duplicate.
// That is the honest trade for stating identity as a query instead of a hash, and it is
// documented rather than papered over.
func dcimCableDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCable"),
		Endpoint:   "dcim/cables",
		ObjectType: "dcim.cable",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A PrimaryModel mixes in TagsMixin and CustomFieldsMixin
		// (docs/netbox-schema.md -> dcim.Cable, bases), so it carries the whole provenance
		// stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `aTerminations` and `bTerminations` are absent from this table on purpose: a spec
		// field writing a polymorphic pair is a GenericFKSpec, not a Field.
		Fields: []Field{
			// EmptyIsNull, because NetBox's ChoiceField renders an empty choice as `null` on
			// read: a `""` sent for a cleared type would be compared against that `null`, and
			// on a Recreate kind a diff that never settles is a cable deleted and re-created
			// on every resync rather than merely a PATCH loop. The column is
			// `blank=True, null=True` (docs/netbox-schema.md -> dcim.Cable), so the null is
			// accepted.
			{Spec: "type", API: "type", EmptyIsNull: true},
			{Spec: "status", API: "status"},
			// No EmptyIsNull: `profile` is `blank=True` and *not* nullable, so there is no
			// null to send -- which is why CableProfile offers no empty member and a profile
			// cannot be cleared through the operator.
			{Spec: "profile", API: "profile"},
			// Written as `tenant`, filtered as `tenant_id`. No CascadeOnDelete:
			// `on_delete=PROTECT`, so deleting the tenant is refused while a cable names it.
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			// `on_delete=SET_NULL`: deleting the bundle clears this column rather than taking
			// the cable with it, so no cascade and no owner reference.
			{
				Spec: "bundleRef", API: "bundle", Class: ClassRefOne,
				Target: netboxv1alpha1.CableBundleRef{}.TargetGVK(),
			},
			{Spec: "label", API: "label"},
			{Spec: "color", API: "color"},
			// EmptyIsNull for the reason dcim.Site's coordinates have it: NetBox rejects `""`
			// for a DecimalField outright, so an emptied length has to go over the wire as
			// null to clear rather than to fail (#170).
			{Spec: "length", API: "length", EmptyIsNull: true},
			// EmptyIsNull, and here NetBox says so itself: the ChoiceField is declared
			// `allow_null=True` (netbox/dcim/api/serializers_/cables.py:40, `length_unit`).
			{Spec: "lengthUnit", API: "length_unit", EmptyIsNull: true},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, four filters, no null pin and no fallback.
		//
		// The two halves of each end are matched by the names applyGenericFKList writes into
		// the decoded spec once the list resolves -- the pair's TypeField and IDField, which on
		// a to-many pair are filter names rather than columns (registry.GenericFKList). The
		// representative element is the *first after sorting*, so the question is the same one
		// on every pass regardless of the order the manifest listed the terminations in, which
		// is what makes "reordering entries produces zero API writes" true of the lookup as
		// well as of the diff.
		//
		// Both halves of an end or neither: a generic FK's id is only unique within its type,
		// so `?termination_a_id=5` alone matches the interface with id 5 and the console port
		// with id 5 alike. And both *ends*, for the reason the type comment gives.
		//
		// There is no weaker candidate to fall back to, and that is not a gap: `label` is not
		// unique, and a candidate on it would adopt somebody else's cable. A cable whose
		// terminations have not resolved yet matches no candidate, and the engine waits
		// (`Ready=False, Reason=WaitingForKey`) rather than creating a second cable.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: CableTerminationATypeField, Spec: CableTerminationATypeField},
				{Filter: CableTerminationAIDField, Spec: CableTerminationAIDField},
				{Filter: CableTerminationBTypeField, Spec: CableTerminationBTypeField},
				{Filter: CableTerminationBIDField, Spec: CableTerminationBIDField},
			},
		}},

		GenericFKs: []GenericFKSpec{
			cableTerminationFK("aTerminations",
				CableTerminationATypeField, CableTerminationAIDField, "a_terminations"),
			cableTerminationFK("bTerminations",
				CableTerminationBTypeField, CableTerminationBIDField, "b_terminations"),
		},

		// No deferral, and none possible on the terminations: both ends are matched on by the
		// one candidate, so DeferAlways there is ErrDeferredNaturalKey at boot -- and it would
		// be wrong anyway, since a cable created without terminations is a cable with no
		// identity. `tenant` and `bundle` are not deferred either: neither can be part of a
		// reference ring, because nothing in NetBox reaches a cable from a tenant or a bundle.
		UpdateStrategy: UpdateRecreate,

		// The two fields a PATCH cannot reach. Everything else on this kind is patchable, and
		// stating the list rather than the strategy alone is what keeps it that way.
		RecreateOn: []string{"a_terminations", "b_terminations"},

		// No containment parent. Every foreign key this kind has is PROTECT or SET_NULL, and
		// the termination union cascades in the wrong direction -- see NetBoxCable's own doc
		// comment, and cableTerminationFK on why the answer is stated per member.
		//
		// `_abs_length` is listed with the four ChangeLoggedModel columns because it is a
		// denormalised cache NetBox maintains from `length` and `length_unit`
		// (docs/netbox-schema.md preamble: every `_`-prefixed column is), and it is the one
		// such column on this model that is tempting to map -- a spec field for "length in
		// metres" would be exactly the PATCH-forever mistake the read-only list exists to
		// refuse.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_abs_length"},
	}
}
