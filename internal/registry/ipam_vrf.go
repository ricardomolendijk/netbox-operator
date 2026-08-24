package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so that adding a kind is a new file and never an edit to shared
// logic (CONTRIBUTING.md, "Extensibility").
func init() { MustRegister(ipamVRFDescriptor()) }

// ipamVRFDescriptor is ipam.VRF as data.
//
// The first shipped kind with a real to-many reference, and therefore the first proof that
// NBO-088's cardinality work is data rather than engine code: `importTargets` and
// `exportTargets` are two ClassRefMany entries below and nothing else. M2MFields() derives
// the comparison set from them, internal/resolver resolves each element, and
// internal/reconciler writes the sorted id list -- with no diff in either package.
//
// It is also the first kind whose fallback natural key is not unique, which is the more
// dangerous of the two facts. See NaturalKeys below.
func ipamVRFDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVRF"),
		Endpoint:   "ipam/vrfs",
		ObjectType: "ipam.vrf",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.VRF is a PrimaryModel (docs/netbox-schema.md -> ipam.VRF, bases), which mixes
		// in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `enforceUnique` needs no field class: it is a bool, compared as a value. The
		// pointer in the CRD is about telling "omitted" from "false" before the payload is
		// built (api/v1alpha1/ipam_vrf.go), which is a spec-representation question rather
		// than a comparison one.
		//
		// `tenant` is absent for the same reason as on ipam.RouteTarget: NBO-021 adds it with
		// NetBoxTenant.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "rd", API: "rd"},
			{Spec: "enforceUnique", API: "enforce_unique"},
			{
				Spec: "importTargets", API: "import_targets", Class: ClassRefMany,
				Target: netboxv1alpha1.RouteTargetRef{}.TargetGVK(),
			},
			{
				Spec: "exportTargets", API: "export_targets", Class: ClassRefMany,
				Target: netboxv1alpha1.RouteTargetRef{}.TargetGVK(),
			},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// Two candidates, and unlike dcim.Region's pair they do not come from
		// meta.constraints -- ipam.VRF declares none (docs/netbox-schema.md -> ipam.VRF).
		// They come from the one column that carries UNIQUE and from the fact that the other
		// one does not:
		//
		//  1. `rd` is `CharField UNIQUE len=21`, so it identifies at most one VRF on its own.
		//  2. `name` is `CharField REQ len=100` -- no UNIQUE -- so a name filter can
		//     legitimately match several rows. It is a convention, not a constraint.
		//
		// `rd__isnull=true` is pinned on the second candidate rather than the candidate being
		// `name` alone as NBO-022's spec table has it, and the spec's own reasoning is why.
		// Candidates are tried in order and the engine falls through when one matches
		// nothing, so a name-only second candidate would be reached by a VRF that *does*
		// declare an `rd` whose object does not exist yet -- it would find an unrelated VRF
		// of the same name, adopt it, and PATCH somebody else's `rd` onto it, silently
		// reparenting every prefix and address keyed on that VRF. The pin makes the second
		// candidate the identity of a different object -- the VRF of this name with no route
		// distinguisher -- exactly as it does for a top-level dcim.Region.
		//
		// What the pin does not do is make `name` unique. Two RD-less VRFs sharing a name
		// still match, and that is reported as a Conflict naming both ids rather than
		// resolved by taking the first row.
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "rd", Spec: "rd"}}},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "rd", Spec: "rd"}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never write
		// (docs/netbox-schema.md, preamble). ipam.VRF has no `_`-prefixed cache and no
		// CounterCacheField; its serializer's `prefix_count` and `ipaddress_count` are
		// unwritable too and are not listed, because this list guards the field map and no
		// spec field maps onto them.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
