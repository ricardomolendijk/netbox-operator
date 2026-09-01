package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasConfigContextDescriptor()) }

// extrasConfigContextDescriptor is extras.ConfigContext as data.
//
// **Thirteen to-many references on one kind**, which is what makes it the proof NBO-088's
// cardinality work asked for: the widest many-to-many surface in the catalogue is thirteen
// ClassRefMany entries below and no engine code at all. M2MFields() derives the comparison set
// from them, internal/resolver resolves each element, and internal/reconciler writes each
// sorted, deduplicated id list -- so reordering any set in a manifest produces zero API
// writes.
//
// `tags` is a user field here and not the provenance stamp's column, which is the one fact
// about this model worth stating twice. The digest records no `TagsMixin` in the bases and
// records `tags` as a plain `ManyToManyField -> extras.Tag` with `related_name='+'`
// (docs/netbox-schema.md -> extras.ConfigContext): it selects *which tagged objects the
// context applies to*. Declaring this kind Taggable would append `k8s-managed` to that set and
// silently change which objects in NetBox receive this configuration, so Taggable is false and
// Validate refuses the pair outright (ErrTagsFieldOnTaggableKind).
//
// Both flags are therefore false and a NetBoxConfigContext carries no provenance whatsoever:
// no `TagsMixin` and no `CustomFieldsMixin` means neither half of the stamp has a column to go
// in. NetBoxSweep will not find one and multi-writer detection is blind to one
// (docs/operations/provenance.md).
func extrasConfigContextDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigContext"),
		Endpoint:   "extras/config-contexts",
		ObjectType: "extras.configcontext",
		Scope:      apiextensionsv1.NamespaceScoped,

		Taggable:        false,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "data", API: "data", Class: ClassJSON},
			{
				Spec: "profileRef", API: "profile", Class: ClassRefOne,
				Target: netboxv1alpha1.ConfigContextProfileRef{}.TargetGVK(),
			},
			{Spec: "weight", API: "weight"},
			{Spec: "isActive", API: "is_active"},
			{
				Spec: "regions", API: "regions", Class: ClassRefMany,
				Target: netboxv1alpha1.RegionRef{}.TargetGVK(),
			},
			{
				Spec: "siteGroups", API: "site_groups", Class: ClassRefMany,
				Target: netboxv1alpha1.SiteGroupRef{}.TargetGVK(),
			},
			{
				Spec: "sites", API: "sites", Class: ClassRefMany,
				Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
			},
			{
				Spec: "locations", API: "locations", Class: ClassRefMany,
				Target: netboxv1alpha1.LocationRef{}.TargetGVK(),
			},
			{
				Spec: "deviceTypes", API: "device_types", Class: ClassRefMany,
				Target: netboxv1alpha1.DeviceTypeRef{}.TargetGVK(),
			},
			// NetBox spells the column `roles` and points it at dcim.DeviceRole: one role
			// serves devices and virtual machines both (docs/netbox-schema.md ->
			// extras.ConfigContext).
			{
				Spec: "roles", API: "roles", Class: ClassRefMany,
				Target: netboxv1alpha1.DeviceRoleRef{}.TargetGVK(),
			},
			{
				Spec: "platforms", API: "platforms", Class: ClassRefMany,
				Target: netboxv1alpha1.PlatformRef{}.TargetGVK(),
			},
			{
				Spec: "clusterTypes", API: "cluster_types", Class: ClassRefMany,
				Target: netboxv1alpha1.ClusterTypeRef{}.TargetGVK(),
			},
			{
				Spec: "clusterGroups", API: "cluster_groups", Class: ClassRefMany,
				Target: netboxv1alpha1.ClusterGroupRef{}.TargetGVK(),
			},
			{
				Spec: "clusters", API: "clusters", Class: ClassRefMany,
				Target: netboxv1alpha1.ClusterRef{}.TargetGVK(),
			},
			{
				Spec: "tenantGroups", API: "tenant_groups", Class: ClassRefMany,
				Target: netboxv1alpha1.TenantGroupRef{}.TargetGVK(),
			},
			{
				Spec: "tenants", API: "tenants", Class: ClassRefMany,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			{
				Spec: "tags", API: "tags", Class: ClassRefMany,
				Target: netboxv1alpha1.TagRef{}.TargetGVK(),
			},
			{Spec: "description", API: "description"},
		},

		// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md -> extras.ConfigContext),
		// so a `?name=` filter matches at most one row and there is no second candidate to
		// fall through to. No `meta.constraints` on the model, and none needed: the column's
		// own UNIQUE is stronger than a constraint line, because it holds unconditionally.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef. Twelve of this model's foreign keys are to-many assignment sets,
		// which cannot be a containment parent at all -- Kubernetes garbage collection waits
		// for every owner, so a list of parents turns "delete the site" into "delete the site
		// or the tenant" with no upper bound -- and the thirteenth, `profile`, is
		// `on_delete=PROTECT` and therefore does not cascade
		// (docs/decisions/0003-ownership-and-references.md rule 4).
		ReadOnly: []string{"created", "last_updated", "url", "display", "data_synced"},
	}
}
