package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so that adding a kind is a new file and never an edit to shared
// logic (CONTRIBUTING.md, "Extensibility").
func init() { MustRegister(dcimSiteDescriptor()) }

// dcimSiteDescriptor is dcim.Site as data.
//
// It is the second kind, and the one that proves the engine's two value normalisations on a
// real model rather than in a unit test. Neither of them appears below, and that is the
// point:
//
//   - `status` is a choice column, which NetBox returns as {"value","label"} and takes as
//     the bare value. internal/netbox/drift.go's unwrapNested reduces the read shape by the
//     absence of an "id" key, so a choice needs no field class.
//   - `latitude` and `longitude` are DecimalFields, which NetBox returns as strings padded
//     to their decimal_places ("51.924400" for a spec that said "51.9244"). scalarEqual
//     compares two numeric strings numerically, so they need no field class either.
//
// A field class exists for a difference the comparison cannot infer from the value -- an
// order-independent id set, an order-sensitive array -- and dcim.Site has none of those
// once its foreign keys are out of scope.
func dcimSiteDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxSite"),
		Endpoint:   "dcim/sites",
		ObjectType: "dcim.site",
		Scope:      apiextensionsv1.NamespaceScoped,

		// CR spec names on the left, NetBox API names on the right.
		// `physicalAddress` -> `physical_address` and `shippingAddress` ->
		// `shipping_address` are the entries that earn the table: NetBox ignores a field
		// name it does not know rather than rejecting it, so a wrong one writes nothing and
		// reports success.
		//
		// No Ref entries at all. dcim.Site's `region`, `group`, `tenant` and `asns` are
		// optional foreign keys (docs/netbox-schema.md -> dcim.Site) that this milestone
		// leaves out of the CRD entirely rather than declaring and dropping, so the field
		// map has nothing to declare for them either -- and the kind therefore reports
		// RefsResolved=True/AllResolved rather than NotImplemented.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "status", API: "status"},
			{Spec: "facility", API: "facility"},
			{Spec: "physicalAddress", API: "physical_address"},
			{Spec: "shippingAddress", API: "shipping_address"},
			{Spec: "latitude", API: "latitude"},
			{Spec: "longitude", API: "longitude"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, because `slug` is column-unique on dcim.Site
		// (docs/netbox-schema.md -> dcim.Site, `slug SlugField REQ UNIQUE len=100`): it
		// identifies at most one site on its own, with no conditional constraint to express
		// as a second candidate and no parent to pin to null. `name` is column-unique too
		// and is deliberately not a candidate -- a kind gets one identity, and `slug` is the
		// one the spec calls the site's identifier.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never
		// write (docs/netbox-schema.md, preamble). Writing one does not fail -- it silently
		// no-ops, so the next reconcile finds the same difference and PATCHes again,
		// forever.
		//
		// dcim.Site's serializer also returns counts (`device_count`, `rack_count`,
		// `prefix_count`, ...) which are equally unwritable. They are not listed because
		// this list guards the field map, and Drift only ever compares fields the spec
		// declares: a column no spec field maps onto cannot reach a payload, so listing it
		// would document rather than prevent.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
