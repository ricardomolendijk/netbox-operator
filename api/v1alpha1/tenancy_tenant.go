package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxTenantSpec describes one tenancy.Tenant.
//
// The kind almost every IPAM and DCIM model points at: `tenant ForeignKey ->
// tenancy.Tenant on_delete=PROTECT` appears on ipam.VRF, ipam.VLAN, ipam.Prefix,
// ipam.IPAddress, ipam.IPRange, ipam.ASN, dcim.Site, dcim.Device and more
// (docs/netbox-schema.md). PROTECT is the half worth knowing before you delete one -- see
// docs/reference/netboxtenant.md.
//
// Neither `name` nor `slug` is column-unique here, unlike on tenancy.TenantGroup: identity
// comes from `meta.constraints`, which scope both per group. That is why this kind has two
// natural-key candidates and a group pinned to null rather than omitted.
type NetBoxTenantSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the tenant's label in the NetBox UI.
	//
	// **Not** column-unique (docs/netbox-schema.md -> tenancy.Tenant, `name CharField REQ
	// len=100`). `meta.constraints` makes it unique per group, plus a separate constraint
	// for groupless tenants, so two tenants may share a name under different groups. It is
	// a candidate key and deliberately not the lookup key: `slug` is the stable identifier,
	// and a rename that collides therefore fails as Ready=False/Invalid carrying NetBox's
	// own 409 rather than being retried.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the tenant's URL-safe identifier, and this kind's natural key.
	//
	// Unique *per group* rather than globally (docs/netbox-schema.md -> tenancy.Tenant,
	// `meta.constraints`: `unique_group_slug` on `(group, slug)`, plus `unique_slug` on
	// `(slug)` where `group IS NULL`). Two NetBoxTenants in different namespaces claiming
	// one slug in one group is one tenant and a Conflict -- not two tenants
	// (docs/decisions/0002-crd-scoping.md). `onConflict` already defaults to Fail, so that
	// collision is loud unless somebody opted into Adopt; on a shared catalogue kind, do not.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// GroupRef files this tenant under a NetBoxTenantGroup
	// (docs/netbox-schema.md -> tenancy.Tenant, `group ForeignKey -> tenancy.TenantGroup
	// on_delete=SET_NULL`).
	//
	// Part of this kind's identity, not just an attribute of it: leaving it unset makes a
	// *groupless* tenant, which is a different natural key rather than the same key with a
	// filter omitted, so the lookup pins `group_id__isnull=true` instead of dropping the
	// filter (docs/concepts/lookups.md). Declaring it and having it not resolve yet makes
	// neither candidate applicable, and the engine waits rather than adopting an unrelated
	// groupless tenant of the same slug.
	//
	// SET_NULL rather than PROTECT, so deleting the group in NetBox clears this column
	// rather than refusing -- the next reconcile finds the drift and PATCHes it back.
	// +optional
	GroupRef *TenantGroupRef `json:"groupRef,omitempty"`

	// Description is free text shown next to the tenant.
	//
	// Inherited from PrimaryModel (docs/netbox-schema.md -> tenancy.Tenant,
	// `description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the tenant's long-form notes field.
	//
	// Also inherited from PrimaryModel, and a TextField rather than a CharField: it has no
	// max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxTenant is one tenancy.Tenant in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which is
// what makes a cross-namespace slug collision routine rather than exotic: NetBox's
// uniqueness is a database constraint and a namespace boundary does not partition it.
//
// A `tenantRef` on some other kind is **not** a containment reference
// (docs/decisions/0003-ownership-and-references.md rule 4): a tenant is an attribute of an
// object, not its container, so it acquires no owner references from the objects that name
// it and deleting it does not cascade to their prefixes and addresses. What happens instead
// is NetBox's PROTECT refusing the delete.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbtenant
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.groupRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxTenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxTenantSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxTenant) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxTenant) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxTenantList is a list of NetBoxTenant.
// +kubebuilder:object:root=true
type NetBoxTenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxTenant `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxTenant{}, &NetBoxTenantList{})
}
