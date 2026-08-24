package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so that adding a kind is a new file and never an edit to shared
// logic (CONTRIBUTING.md, "Extensibility").
func init() { MustRegister(ipamRouteTargetDescriptor()) }

// ipamRouteTargetDescriptor is ipam.RouteTarget as data.
//
// The plainest descriptor in the registry, and deliberately so. ipam.RouteTarget is the other
// end of ipam.VRF's two many-to-many relations, and it declares *nothing* about them: the
// ManyToManyFields live on ipam.VRF (docs/netbox-schema.md -> ipam.VRF), so every write to
// the relation goes through the VRF and this kind has no to-many field to classify. The
// relation direction is the easiest thing on this pair to get backwards, and the shape of
// this file is what it looks like when it is the right way round.
func ipamRouteTargetDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRouteTarget"),
		Endpoint:   "ipam/route-targets",
		ObjectType: "ipam.routetarget",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.RouteTarget is a PrimaryModel (docs/netbox-schema.md -> ipam.RouteTarget,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// No Ref entries. ipam.RouteTarget's only foreign key is `tenant`
		// (docs/netbox-schema.md -> ipam.RouteTarget, `-> tenancy.Tenant
		// on_delete=PROTECT`), which NBO-021 adds together with NetBoxTenant; declaring it
		// here would give the CRD a field NetBox never sees.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. `name` is column-unique on ipam.RouteTarget
		// (docs/netbox-schema.md -> ipam.RouteTarget, `name CharField REQ UNIQUE len=21`)
		// and the model declares no meta.constraints, so there is no conditional identity to
		// express as a second candidate.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never write
		// (docs/netbox-schema.md, preamble). This model has no `_`-prefixed cache and no
		// CounterCacheField.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
