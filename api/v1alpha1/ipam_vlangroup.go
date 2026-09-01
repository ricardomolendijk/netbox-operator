package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VIDRange is one inclusive `[start, end]` VLAN ID range.
//
// NetBox stores `ipam.VLANGroup.vid_ranges` as a Postgres `ArrayField` of ranges
// (docs/netbox-schema.md -> ipam.VLANGroup, `vid_ranges ArrayField
// def=UNRESOLVED:default_vid_ranges`) and returns them in stored order, so the differ
// compares the field as an *ordered* list (registry.ClassArray). That is not a style choice:
// compared order-independently, `[[1,100],[200,300]]` and `[[200,300],[1,100]]` would look
// equal while NetBox holds two different values, and a real difference would stay hidden.
// Compared in order, a user who reorders two ranges gets exactly one corrective PATCH and
// then silence.
//
// A struct rather than `[][]int32`, so that `start` and `end` are named in
// `kubectl explain` and each carries its own 1-4094 bound. A bare pair of numbers would put
// the meaning of each position in the documentation only.
//
// +kubebuilder:validation:XValidation:rule="self.start <= self.end",message="start must not be greater than end"
type VIDRange struct {
	// Start is the first VLAN ID in the range, inclusive.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	Start int32 `json:"start"`

	// End is the last VLAN ID in the range, inclusive. May equal Start, which is a range of
	// one VLAN.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	End int32 `json:"end"`
}

// NetBoxVLANGroupSpec describes one ipam.VLANGroup.
//
// The kind whose **identity includes a polymorphic pair**. `ipam.VLANGroup` is unique on
// `(scope_type, scope_id, slug)` and on `(scope_type, scope_id, name)`
// (docs/netbox-schema.md -> ipam.VLANGroup, meta.constraints: `unique_scope_slug`,
// `unique_scope_name`) -- so unlike every other OrganizationalModel in the API, **`slug` is
// not globally unique here**. `extras.Tag`, `dcim.Site` and `tenancy.TenantGroup` all carry
// `UNIQUE` on the column; this model carries none, and two VLAN groups may share a slug as
// long as their scopes differ.
//
// Worse, with both scope columns null Postgres treats the NULLs as distinct, so neither
// unique constraint fires at all: **two globally-scoped VLAN groups can share a slug** and
// the database will not stop them. The lookup therefore treats more than one match as a
// Conflict even on the constraint-backed candidate, and writes nothing
// (docs/concepts/lookups.md).
//
// It is also the kind that carries the scope pair *without* the caches. `ipam.Prefix` gets
// `scope_type` / `scope_id` from `dcim.CachedScopeMixin`, which brings `_region`,
// `_site_group`, `_site` and `_location` with it; `ipam.VLANGroup` declares the two columns
// on the model itself and has no cached columns at all (docs/netbox-schema.md ->
// ipam.VLANGroup). The two scoped kinds in this milestone differ there and nowhere else.
//
// `deletionPolicy` defaults to `Delete` here, and this is the one kind in `ipam` where that is
// deliberate rather than inherited (#186). The rule the table in
// docs/concepts/deletion.md#the-default-depends-on-the-kind turns on is whether deletion
// destroys *state*: a VLAN group is an organisational container over `vid_ranges`, not an
// allocation, so deleting one frees nothing and hands nobody an address. Its neighbour
// `NetBoxVLAN` -- the thing a group contains -- defaults to `Retain`.
type NetBoxVLANGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's display name (docs/netbox-schema.md -> ipam.VLANGroup,
	// `name CharField REQ len=100`).
	//
	// Not unique on its own. `unique_scope_name` makes it unique only *within* a scope, so
	// renaming a group into a name another group in the same scope already holds is a 409
	// from NetBox, surfaced verbatim as `Invalid` with a long backoff rather than retried.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the URL-safe identifier (docs/netbox-schema.md -> ipam.VLANGroup,
	// `slug SlugField REQ len=100`).
	//
	// Part of this kind's identity, but only together with the scope: see the type comment
	// for why a slug alone does not identify a VLAN group and why a duplicate is a Conflict
	// rather than an adoption.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Scope attaches the group to a Region, SiteGroup, Site or Location, and is **half of
	// this object's identity** -- which is what makes this kind different from every other
	// scoped one. The lookup filters on `scope_type` and `scope_id` alongside `slug` when
	// the scope resolves, and pins both to null when the field was never written.
	//
	// Omit it for a globally-scoped group, which is a normal state rather than a missing
	// value: neither column carries a real `REQ`
	// (docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest). An empty
	// `scope: {}` clears the scope by writing both columns as null; an absent one leaves
	// whatever NetBox holds alone.
	//
	// Written as the `(scope_type, scope_id)` pair and diffed as a unit, so moving a group
	// from a Site to a Region is one change and one PATCH carrying both keys.
	// +optional
	Scope *ScopeRef `json:"scope,omitempty"`

	// VIDRanges is the span of VLAN IDs this group governs
	// (docs/netbox-schema.md -> ipam.VLANGroup, `vid_ranges ArrayField
	// def=UNRESOLVED:default_vid_ranges`).
	//
	// A pointer to a slice, and the reason is the Django default. `default_vid_ranges` is
	// the whole 1-4094 space, so **omitting this field is not the same as sending `[]`**:
	// nil leaves NetBox's default alone, `[]` writes an empty array and leaves the group
	// governing no VLAN IDs at all. Both are legal instructions and they are different ones.
	//
	// Compared in order, because NetBox stores and returns the ranges in order -- see
	// VIDRange.
	//
	// MaxItems is not a NetBox limit. Every element carries a CEL rule
	// (`self.start <= self.end`) and the API server costs a rule on a list item at the
	// list's maximum length, so an unbounded validated list is rejected outright with
	// "estimated rule cost exceeds budget". 256 is far above any real group's range count --
	// 2047 disjoint ranges is the arithmetic ceiling for a 1-4094 space -- and well inside
	// the budget for a two-integer item with one rule.
	// +optional
	// +kubebuilder:validation:MaxItems=256
	VIDRanges *[]VIDRange `json:"vidRanges,omitempty"`

	// TenantRef assigns the group to a tenant (docs/netbox-schema.md -> ipam.VLANGroup,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// `PROTECT`, which has a consequence worth knowing before you use it: a NetBoxVLANGroup
	// holding this reference **blocks deletion of that tenant in NetBox**, and because a VLAN
	// group is catalogue-shaped it usually lives in a shared namespace. The team that owns the
	// tenant then gets `Deleting=False, Reason=Protected` naming a group in a namespace they
	// cannot see. The condition names the blocker for exactly that reason
	// (docs/concepts/deletion.md).
	//
	// Not a containment reference: a VLAN group outliving its tenant is a normal state, so
	// this contributes no owner reference
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Description is free text shown next to the group. Declared on OrganizationalModel
	// rather than on ipam.VLANGroup (docs/netbox-schema.md -> ipam.VLANGroup,
	// `description (OrganizationalModel) CharField len=200`); an inherited column is as
	// writable as a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field. Also inherited from
	// OrganizationalModel, and a TextField rather than a CharField: it has no max_length, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxVLANGroup is one ipam.VLANGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Catalogue-
// shaped in practice -- one shared namespace, reachable from the rest of the cluster through
// a NetBoxRefGrant -- which is also why its `tenantRef` `PROTECT` blocker is so easy to
// miss.
//
// The SCOPE printer column reads `.status.naturalKey` rather than the spec, because on this
// kind the question it answers is "which of the two identities did the lookup actually use"
// -- and only the status can answer that.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvlangroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.status.naturalKey.scope`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVLANGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVLANGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxVLANGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxVLANGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxVLANGroupList is a list of NetBoxVLANGroup.
// +kubebuilder:object:root=true
type NetBoxVLANGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVLANGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVLANGroup{}, &NetBoxVLANGroupList{})
}
