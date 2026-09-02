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
// order-independent id set, an order-sensitive array -- and dcim.Site has exactly one of
// those: `asns`, whose ClassRefMany is the declaration that its two ends are a set of ids
// written and a list of nested objects read.
func dcimSiteDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxSite"),
		Endpoint:   "dcim/sites",
		ObjectType: "dcim.site",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Site is a PrimaryModel (docs/netbox-schema.md -> dcim.Site, bases), which
		// mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance
		// stamp.
		Taggable:        true,
		CustomFieldable: true,

		// CR spec names on the left, NetBox API names on the right.
		// `physicalAddress` -> `physical_address` and `shippingAddress` ->
		// `shipping_address` are the entries that earn the table: NetBox ignores a field
		// name it does not know rather than rejecting it, so a wrong one writes nothing and
		// reports success.
		//
		// One Ref entry. dcim.Site's `region`, `group` and `tenant` are optional foreign keys
		// (docs/netbox-schema.md -> dcim.Site) still left out of the CRD entirely rather than
		// declared and dropped, so the field map has nothing to declare for them either --
		// and the kind therefore reports RefsResolved=True/AllResolved rather than
		// NotImplemented when none of them is set.
		//
		// `asns` is the exception, and it is a to-many rather than a foreign key:
		// docs/netbox-schema.md -> dcim.Site records `asns ManyToManyField -> ipam.ASN`, and
		// the schema IR records `class: M2M`, `api.many: true`,
		// `api.serializer_field: SerializedPKRelatedField` and `in_write_path: true`
		// (hack/testdata/ir-4.6.8.json.gz). ClassRefMany is what that shape needs: the write
		// is a list of ids, the read is a list of nested objects, and M2MFields() is what
		// makes internal/netbox compare the two as sets so that a reordered manifest produces
		// no PATCH.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "status", API: "status"},
			{Spec: "facility", API: "facility"},
			{
				Spec: "asns", API: "asns", Class: ClassRefMany,
				Target: netboxv1alpha1.ASNRef{}.TargetGVK(),
			},
			{Spec: "physicalAddress", API: "physical_address"},
			{Spec: "shippingAddress", API: "shipping_address"},
			// EmptyIsNull on both: they are the only nullable non-text columns this kind
			// maps, and NetBox rejects `""` for a DecimalField outright, so an emptied
			// coordinate has to go over the wire as null to clear rather than to fail
			// (#170).
			{Spec: "latitude", API: "latitude", EmptyIsNull: true},
			{Spec: "longitude", API: "longitude", EmptyIsNull: true},
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
