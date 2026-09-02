package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimPowerPanelDescriptor()) }

// dcimPowerPanelDescriptor is dcim.PowerPanel as data.
//
// The identity is a real database constraint, which after NBO-051 is worth saying out loud:
//
//	meta.constraints: (models.UniqueConstraint(fields=('site', 'name'),
//	   name='%(app_label)s_%(class)s_unique_site_name'),)
//
// (docs/netbox-schema.md -> dcim.PowerPanel.) The committed IR agrees and resolves it to the
// filters as well as the columns -- `dcim.PowerPanel.natural_keys` is a single candidate over
// `{column: site, filter: site_id}` and `{column: name, filter: name}`, with `null_fields: []`
// and `unusable: null` (hack/testdata/ir-4.6.8.json.gz). Both filters are registered on
// `PowerPanelFilterSet`: `site_id` is declared and `name` is in `meta_fields`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.PowerPanel.filters).
//
// So there is no pin, no fallback and no conditional candidate. `location` is optional and
// nothing is constrained on it, so -- unlike dcim.Rack one source file over, whose *every*
// constraint is keyed on the optional `location` -- a location-less panel is as findable as
// any other, and an ambiguous match is impossible rather than reported: Postgres will not
// hold two.
//
// **No containment parent.** `site` and `location` are both `on_delete=PROTECT`
// (docs/netbox-schema.md -> dcim.PowerPanel), so validateContainment would refuse either
// (ErrContainmentNotCascade) and rightly: NetBox declines to delete a site that still has
// panels, so a cluster-side cascade would garbage-collect the CR and leave the row.
func dcimPowerPanelDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxPowerPanel"),
		Endpoint:   "dcim/power-panels",
		ObjectType: "dcim.powerpanel",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.PowerPanel is a PrimaryModel (docs/netbox-schema.md -> dcim.PowerPanel,
		// bases), so it mixes in both TagsMixin and CustomFieldsMixin and carries the whole
		// provenance stamp. ContactsMixin and ImageAttachmentsMixin contribute
		// GenericRelations only.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// Written as `site`, filtered as `site_id`. PROTECT, so no cascade to declare.
			{
				Spec: "siteRef", API: "site", Class: ClassRefOne,
				Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
			},
			// PROTECT here where dcim.Rack's `location` is SET_NULL -- two kinds in the same
			// app pointing at the same model with different cascades, which is why the answer
			// is read per column rather than per target.
			{
				Spec: "locationRef", API: "location", Class: ClassRefOne,
				Target: netboxv1alpha1.LocationRef{}.TargetGVK(),
			},
		},

		// One candidate, straight off the constraint.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "site_id", Spec: "siteRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef -- see the doc comment.
		//
		// No DataLossOnDelete either: a panel is configuration a manifest recreates, not
		// allocated state, so a delete destroys nothing a finalizer should refuse (#304,
		// docs/concepts/deletion.md). NetBox refuses the delete anyway while feeds still
		// point at it, which is what actually keeps a live path together.

		// The four columns every ChangeLoggedModel carries, plus the one counter.
		//
		// `powerfeed_count` is a RelatedObjectCountField the serializer returns and the API
		// refuses (hack/testdata/api-schema-4.6.8.json.gz ->
		// serializers.PowerPanelSerializer.declared; hack/testdata/ir-4.6.8.json.gz ->
		// dcim.PowerPanel.write_path, where it sits in the write path and is read-only there).
		// NetBox maintains it from the feeds and ignores an attempt to set it, so writing it
		// does not fail -- it silently no-ops, the next reconcile finds the same difference,
		// and the operator PATCHes forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display", "powerfeed_count",
		},
	}
}
