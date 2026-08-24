package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimSiteGroupDescriptor()) }

// dcimSiteGroupDescriptor is dcim.SiteGroup as data.
//
// The same shape as dcim.Region's descriptor, because the two models are the same model:
// both are NestedGroupModels with the same inherited columns and the same four unique
// constraints (docs/netbox-schema.md -> dcim.SiteGroup, bases and meta.constraints). That is
// the point worth recording -- a second self-referential kind needed no new engine
// behaviour, only a second file.
//
// It is also half of what NBO-018's scope union was missing: `siteGroupRef` resolved to
// RefKindUnavailable in every mode until this Descriptor existed, since the endpoint every
// mode needs is only held here.
func dcimSiteGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxSiteGroup"),
		Endpoint:   "dcim/site-groups",
		ObjectType: "dcim.sitegroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.SiteGroup is a NestedGroupModel (docs/netbox-schema.md -> dcim.SiteGroup,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			// A foreign key is written as `parent` and filtered as `parent_id`; the field
			// map carries the write name, the natural keys below carry the filter name.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.SiteGroupRef{}.TargetGVK(),
			},
		},

		// Two candidates, in this order, straight out of
		// docs/netbox-schema.md -> dcim.SiteGroup.meta.constraints: unique on
		// (parent, name), plus a separate unique on (name) conditioned on
		// parent IS NULL.
		//
		// The order is not a fallback. A group with a parent is identified by the first
		// candidate and a top-level one by the second, and NaturalKey.Applicable is what
		// keeps them apart: the second asserts `parentRef` was never declared, so a child
		// whose parent has not been created yet matches neither and the engine waits.
		// Falling through would find an unrelated top-level group of the same name, adopt
		// it, and the follow-up PATCH would reparent somebody else's data (NBO-015).
		//
		// `name` and not `slug`, as on dcim.Region: NetBox constrains (parent, slug) too,
		// so a slug identifies a group no better than a name does, and a kind gets one
		// identity.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "name", Spec: "name"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does
		// not fail, it silently no-ops, so the next reconcile finds the same difference
		// and PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
