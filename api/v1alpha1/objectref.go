package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ObjectRef points at one NetBox object, in exactly one of four ways.
//
// The field name pins the target Kind, so there is no `kind` discriminator here. A
// discriminator would make every ref field accept every Kind and push the check to
// runtime; `parentRef` on a NetBoxRegion can only be a NetBoxRegion, statically, and the
// typed aliases below are what carry that in Go.
//
// The CEL rules live on this type rather than on each field that uses it, so a new ref
// field cannot forget them. They are enforced by the API server, which is the point: a
// malformed ref is rejected by `kubectl apply` instead of becoming a condition nobody
// reads.
//
// +kubebuilder:validation:XValidation:rule="[has(self.name) && size(self.name) > 0, has(self.slug) && size(self.slug) > 0, has(self.lookup) && size(self.lookup) > 0, has(self.id)].filter(x, x).size() == 1",message="exactly one of name, slug, lookup or id must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.namespace) || (has(self.name) && size(self.name) > 0)",message="namespace may only be set together with name"
// +kubebuilder:validation:XValidation:rule="!has(self.lookup) || self.lookup.all(k, k.matches('^[a-z][a-z0-9_]*$'))",message="lookup keys must be lowercase NetBox filter names"
// +kubebuilder:validation:XValidation:rule="!has(self.lookup) || self.lookup.all(k, !(k in ['limit','offset','format','brief','ordering','q']))",message="lookup may not set pagination, formatting or fuzzy-search parameters"
// +kubebuilder:validation:XValidation:rule="!has(self.lookup) || self.lookup.all(k, size(self.lookup[k]) <= 200)",message="lookup values are limited to 200 characters"
type ObjectRef struct {
	// Name of a CR of the target Kind, resolved through that CR's `.status.id`.
	//
	// This is the mode to prefer: it is the only one that expresses a dependency the
	// operator can wait on, which is what lets a graph applied in any order converge.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`

	// Namespace of that CR. Defaults to the referring object's namespace.
	//
	// Load-bearing on the common path rather than an edge case: every Kind is namespaced
	// in v1alpha1 (docs/decisions/0002-crd-scoping.md), so a team namespace pointing at a
	// shared catalogue namespace is the normal shape. Crossing a namespace requires a
	// NetBoxRefGrant in the target namespace (NBO-014).
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`

	// Slug looks the object up in NetBox directly, for objects the operator does not
	// manage. No CR needs to exist.
	//
	// Bounded at 100 characters because that is what NetBox's own column allows:
	// `slug SlugField len=100` on every OrganizationalModel, NestedGroupModel and
	// PrimaryModel (docs/netbox-schema.md -> dcim.Site, dcim.Region, extras.Tag).
	// +optional
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug,omitempty"`

	// Lookup is a raw NetBox API filter, for an object no slug identifies -- a VLAN is
	// `{vid: "20", site: "home"}` because its identity is a pair.
	//
	// `q` is rejected by the CEL rules along with the pagination and formatting
	// parameters. It is NetBox's fuzzy search, and a fuzzy filter behind a reference that
	// must identify exactly one object would make ambiguity the normal case rather than
	// an error.
	// +optional
	// +kubebuilder:validation:MaxProperties=8
	Lookup map[string]string `json:"lookup,omitempty"`

	// ID is a literal NetBox primary key: the escape hatch, and the only place in the API
	// where a raw id is accepted.
	//
	// A pointer so that `id: 0` is distinguishable from unset. NetBox primary keys start
	// at 1, so zero is never a real object and has to be rejected rather than silently
	// treated as "no id".
	// +optional
	// +kubebuilder:validation:Minimum=1
	ID *int64 `json:"id,omitempty"`
}

// RefTarget is implemented by every typed ref alias.
//
// It is what lets the resolver learn a reference's target Kind from its *type* rather
// than from a switch on the field name: the alias is the declaration, and a new ref field
// cannot forget to say what it points at.
//
// Excluded from deepcopy generation: an interface has no wire representation, and
// controller-gen rejects one outright rather than skipping it.
//
// +kubebuilder:object:generate=false
type RefTarget interface {
	// TargetGVK is the Kind this reference resolves against.
	TargetGVK() schema.GroupVersionKind

	// AsObjectRef returns the underlying reference, so callers work against one type.
	AsObjectRef() ObjectRef
}

// The typed aliases: one defined type per referencable Kind, so the target is visible in
// Go, in the generated OpenAPI, and to the resolver.
//
// Hand-written rather than generated. This ticket's spec proposes a hack/gen-refs
// generator emitting zz_generated.refs.go, which is right at the ~40 aliases the full
// catalogue needs and premature at five: the generator plus its table is more code than
// the ten methods it would emit, and NBO-041/NBO-042 build the emitter that derives the
// rest from the FK targets in docs/netbox-schema.md regardless.
type (
	// TagRef points at a NetBoxTag (extras.Tag, extras/tags).
	TagRef ObjectRef

	// RegionRef points at a NetBoxRegion (dcim.Region, dcim/regions).
	RegionRef ObjectRef

	// SiteRef points at a NetBoxSite (dcim.Site, dcim/sites).
	SiteRef ObjectRef

	// SiteGroupRef points at a NetBoxSiteGroup (dcim.SiteGroup, dcim/site-groups).
	SiteGroupRef ObjectRef

	// LocationRef points at a NetBoxLocation (dcim.Location, dcim/locations).
	LocationRef ObjectRef

	// TenantRef points at a NetBoxTenant (tenancy.Tenant, tenancy/tenants).
	TenantRef ObjectRef

	// TenantGroupRef points at a NetBoxTenantGroup (tenancy.TenantGroup,
	// tenancy/tenant-groups).
	TenantGroupRef ObjectRef

	// InterfaceRef points at a NetBoxInterface (dcim.Interface, dcim/interfaces).
	//
	// A member of IPAssignment (genericref.go). Declared before the Kind exists, which is
	// deliberate: the alias is where the target Kind and therefore the `dcim.interface`
	// object type is written down, and a union member the API accepts and silently drops
	// would be worse than one that resolves to RefKindUnavailable and says so.
	InterfaceRef ObjectRef

	// VMInterfaceRef points at a NetBoxVMInterface (virtualization.VMInterface,
	// virtualization/interfaces -- the endpoint is looked up, never pluralised).
	VMInterfaceRef ObjectRef

	// FHRPGroupRef points at a NetBoxFHRPGroup (ipam.FHRPGroup, ipam/fhrp-groups).
	FHRPGroupRef ObjectRef

	// RoleRef points at a NetBoxRole (ipam.Role, ipam/roles).
	//
	// ipam.Role and not dcim.DeviceRole: they are separate models with separate endpoints,
	// and this is the one a prefix, a VLAN and an IP range carry. Declared before the Kind
	// exists (NBO-055), for the reason InterfaceRef is: the alias is where the target Kind
	// and therefore the `ipam.role` object type is written down, and a field the API accepts
	// and silently drops would be worse than one that reports RefKindUnavailable.
	RoleRef ObjectRef

	// VLANRef points at a NetBoxVLAN (ipam.VLAN, ipam/vlans).
	VLANRef ObjectRef
	// RouteTargetRef points at a NetBoxRouteTarget (ipam.RouteTarget, ipam/route-targets).
	//
	// The first alias used to-many: ipam.VRF's `import_targets` and `export_targets` are
	// lists of these. One alias either way -- the resolver takes the target Kind off
	// registry.Field.Target regardless of cardinality.
	RouteTargetRef ObjectRef

	// VRFRef points at a NetBoxVRF (ipam.VRF, ipam/vrfs).
	VRFRef ObjectRef

	// DeviceRoleRef points at a NetBoxDeviceRole (dcim.DeviceRole, dcim/device-roles).
	//
	// dcim.DeviceRole and not ipam.Role: two separate models with two separate endpoints,
	// and this is the one a device and a virtual machine carry.
	DeviceRoleRef ObjectRef

	// DeviceTypeRef points at a NetBoxDeviceType (dcim.DeviceType, dcim/device-types).
	DeviceTypeRef ObjectRef

	// ManufacturerRef points at a NetBoxManufacturer (dcim.Manufacturer,
	// dcim/manufacturers).
	ManufacturerRef ObjectRef

	// PlatformRef points at a NetBoxPlatform (dcim.Platform, dcim/platforms).
	PlatformRef ObjectRef
)

// TargetGVK reports the Kind this reference resolves against.
func (r TagRef) TargetGVK() schema.GroupVersionKind { return GroupVersion.WithKind("NetBoxTag") }

// AsObjectRef returns the underlying reference.
func (r TagRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r RegionRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxRegion")
}

// AsObjectRef returns the underlying reference.
func (r RegionRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r SiteRef) TargetGVK() schema.GroupVersionKind { return GroupVersion.WithKind("NetBoxSite") }

// AsObjectRef returns the underlying reference.
func (r SiteRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r SiteGroupRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxSiteGroup")
}

// AsObjectRef returns the underlying reference.
func (r SiteGroupRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r LocationRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxLocation")
}

// AsObjectRef returns the underlying reference.
func (r LocationRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r TenantRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxTenant")
}

// AsObjectRef returns the underlying reference.
func (r TenantRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r TenantGroupRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxTenantGroup")
}

// AsObjectRef returns the underlying reference.
func (r TenantGroupRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r InterfaceRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxInterface")
}

// AsObjectRef returns the underlying reference.
func (r InterfaceRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r VMInterfaceRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxVMInterface")
}

// AsObjectRef returns the underlying reference.
func (r VMInterfaceRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r FHRPGroupRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxFHRPGroup")
}

// AsObjectRef returns the underlying reference.
func (r FHRPGroupRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r RoleRef) TargetGVK() schema.GroupVersionKind { return GroupVersion.WithKind("NetBoxRole") }

// AsObjectRef returns the underlying reference.
func (r RoleRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r VLANRef) TargetGVK() schema.GroupVersionKind { return GroupVersion.WithKind("NetBoxVLAN") }

// AsObjectRef returns the underlying reference.
func (r VLANRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r RouteTargetRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxRouteTarget")
}

// AsObjectRef returns the underlying reference.
func (r RouteTargetRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r VRFRef) TargetGVK() schema.GroupVersionKind { return GroupVersion.WithKind("NetBoxVRF") }

// AsObjectRef returns the underlying reference.
func (r VRFRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r DeviceRoleRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxDeviceRole")
}

// AsObjectRef returns the underlying reference.
func (r DeviceRoleRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r DeviceTypeRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxDeviceType")
}

// AsObjectRef returns the underlying reference.
func (r DeviceTypeRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r ManufacturerRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxManufacturer")
}

// AsObjectRef returns the underlying reference.
func (r ManufacturerRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// TargetGVK reports the Kind this reference resolves against.
func (r PlatformRef) TargetGVK() schema.GroupVersionKind {
	return GroupVersion.WithKind("NetBoxPlatform")
}

// AsObjectRef returns the underlying reference.
func (r PlatformRef) AsObjectRef() ObjectRef { return ObjectRef(r) }

// Compile-time proof that every alias satisfies RefTarget. An alias that forgets its
// methods fails the build here rather than at the first reconcile that needs it.
var (
	_ RefTarget = TagRef{}
	_ RefTarget = RegionRef{}
	_ RefTarget = SiteRef{}
	_ RefTarget = SiteGroupRef{}
	_ RefTarget = LocationRef{}
	_ RefTarget = TenantRef{}
	_ RefTarget = TenantGroupRef{}
	_ RefTarget = InterfaceRef{}
	_ RefTarget = VMInterfaceRef{}
	_ RefTarget = FHRPGroupRef{}
	_ RefTarget = RoleRef{}
	_ RefTarget = VLANRef{}
	_ RefTarget = RouteTargetRef{}
	_ RefTarget = VRFRef{}
	_ RefTarget = DeviceRoleRef{}
	_ RefTarget = DeviceTypeRef{}
	_ RefTarget = ManufacturerRef{}
	_ RefTarget = PlatformRef{}
)
