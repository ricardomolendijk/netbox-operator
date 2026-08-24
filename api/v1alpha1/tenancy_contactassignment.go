package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContactPriority is one value of NetBox's ContactPriorityChoices.
//
// The values are read from `netbox/tenancy/choices.py:10-21`, `ContactPriorityChoices`, in the
// 4.6.8 tree: `primary`, `secondary`, `tertiary`, `inactive`.
//
// The empty value is in the enum on purpose. `priority CharField len=50
// choices=ContactPriorityChoices` is `blank=True, null=True`
// (netbox/tenancy/models/contacts.py:145-151) and its serializer declares
// `ChoiceField(..., allow_blank=True, default=lambda: ”)`
// (netbox/tenancy/api/serializers_/contacts.py:73), so `""` is how NetBox spells "no
// priority" and the only way to clear one that has been set.
//
// It carries no tri-state note, and cannot: an `enum` is exactly the validation
// TestClearableFieldsDocumentBothStatesInTheSchema treats as forbidding the empty value, so
// the note and the enum contradict each other in the generated schema. The empty member is
// the statement instead.
//
// +kubebuilder:validation:Enum="";primary;secondary;tertiary;inactive
type ContactPriority string

const (
	// ContactPriorityPrimary is the contact to reach first.
	ContactPriorityPrimary ContactPriority = "primary"

	// ContactPrioritySecondary is the fallback when the primary does not answer.
	ContactPrioritySecondary ContactPriority = "secondary"

	// ContactPriorityTertiary is the third line.
	ContactPriorityTertiary ContactPriority = "tertiary"

	// ContactPriorityInactive is a contact kept on the object for the record and not to be
	// called.
	ContactPriorityInactive ContactPriority = "inactive"
)

// ContactAssignmentTarget selects the object a contact is assigned to.
//
// `tenancy.ContactAssignment.object` is a generic foreign key over `object_type` /
// `object_id` (docs/netbox-schema.md -> tenancy.ContactAssignment), and it is the **first
// `REQ` pair to ship**: unlike `assigned_object_*` on ipam.IPAddress and `scope_*` on
// CachedScopeMixin, both columns are non-nullable -- `object_type ForeignKey REQ` and
// `object_id PositiveBigIntegerField REQ` (netbox/tenancy/models/contacts.py:124-132, neither
// carrying `null=True`). So the rule is `== 1` rather than `<= 1`, and the union field itself
// is **required** on the spec: a CEL rule on an absent field is never evaluated, so an
// optional `== 1` union would be satisfied by leaving it out (docs/concepts/generic-refs.md).
//
// The members are the models that mix in `netbox.models.features.ContactsMixin` **and** have
// a Kind in this build to point at. NetBox 4.6.8 permits 25 of them -- the mixin is what
// `ContactAssignment.clean()` checks, through `has_feature(self.object_type, 'contacts')`
// (netbox/tenancy/models/contacts.py:173-179); the serializer's `ContentTypeField` has an
// unfiltered `ContentType.objects.all()` queryset and restricts nothing
// (netbox/tenancy/api/serializers_/contacts.py:68-70). The other fourteen are absent rather
// than stubbed, because a member with no typed alias has nothing to write the target Kind
// down on. `deviceRef` is the one member whose alias exists and whose Kind does not: it
// reports `RefsResolved=False, Reason=RefKindUnavailable` in all four ref modes until
// NetBoxDevice lands, which is the correct answer and is asserted as such rather than worked
// around.
//
// The type strings in the comments below are not written down against the members. Each is
// the target Kind's own `Descriptor.ObjectType`, so `virtualization.clustergroup` is spelled
// once in the codebase -- lowercased and unpunctuated, never `virtualization.ClusterGroup`.
//
// +kubebuilder:validation:XValidation:rule="[has(self.regionRef), has(self.siteGroupRef), has(self.siteRef), has(self.locationRef), has(self.deviceRef), has(self.prefixRef), has(self.ipAddressRef), has(self.tenantRef), has(self.clusterRef), has(self.clusterGroupRef), has(self.virtualMachineRef)].filter(x, x).size() == 1",message="exactly one of regionRef, siteGroupRef, siteRef, locationRef, deviceRef, prefixRef, ipAddressRef, tenantRef, clusterRef, clusterGroupRef or virtualMachineRef must be set"
type ContactAssignmentTarget struct {
	// RegionRef assigns the contact to a region -> `dcim.region`.
	// +optional
	RegionRef *RegionRef `json:"regionRef,omitempty"`

	// SiteGroupRef assigns the contact to a site group -> `dcim.sitegroup`.
	// +optional
	SiteGroupRef *SiteGroupRef `json:"siteGroupRef,omitempty"`

	// SiteRef assigns the contact to a site -> `dcim.site`.
	// +optional
	SiteRef *SiteRef `json:"siteRef,omitempty"`

	// LocationRef assigns the contact to a location -> `dcim.location`.
	// +optional
	LocationRef *LocationRef `json:"locationRef,omitempty"`

	// DeviceRef assigns the contact to a device -> `dcim.device`.
	//
	// The member whose Kind this build does not carry. NetBoxDevice lands in M4; until then
	// this member is declared, admissible and reported as
	// `RefsResolved=False, Reason=RefKindUnavailable` -- never silently dropped
	// (docs/concepts/generic-refs.md, "Kinds that do not exist yet").
	// +optional
	DeviceRef *DeviceRef `json:"deviceRef,omitempty"`

	// PrefixRef assigns the contact to a prefix -> `ipam.prefix`.
	// +optional
	PrefixRef *PrefixRef `json:"prefixRef,omitempty"`

	// IPAddressRef assigns the contact to an IP address -> `ipam.ipaddress`.
	// +optional
	IPAddressRef *IPAddressRef `json:"ipAddressRef,omitempty"`

	// TenantRef assigns the contact to a tenant -> `tenancy.tenant`.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// ClusterRef assigns the contact to a cluster -> `virtualization.cluster`.
	// +optional
	ClusterRef *ClusterRef `json:"clusterRef,omitempty"`

	// ClusterGroupRef assigns the contact to a cluster group ->
	// `virtualization.clustergroup`.
	// +optional
	ClusterGroupRef *ClusterGroupRef `json:"clusterGroupRef,omitempty"`

	// VirtualMachineRef assigns the contact to a virtual machine ->
	// `virtualization.virtualmachine`.
	// +optional
	VirtualMachineRef *VirtualMachineRef `json:"virtualMachineRef,omitempty"`
}

// NetBoxContactAssignmentSpec describes one tenancy.ContactAssignment.
//
// A **join object**: it has no name and no slug, and its whole content is the pair it joins
// plus the role it joins them under. Its identity is therefore the constraint itself --
// `UniqueConstraint(fields=('object_type', 'object_id', 'contact', 'role'))`
// (docs/netbox-schema.md -> tenancy.ContactAssignment, `meta.constraints`;
// netbox/tenancy/models/contacts.py:159-164) -- which is the first natural key in the
// catalogue to combine a generic-FK pair *and* two ordinary references.
//
// The consequence worth knowing before writing manifests: the role is part of the identity,
// so the **same contact may be assigned to the same object twice** under different roles, and
// neither assignment is drift on the other. Two CRs differing only in `roleRef` are two rows.
// Two CRs agreeing on all four are one row and a Conflict.
type NetBoxContactAssignmentSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ObjectRef is what the contact is assigned to (`object_type`, `object_id`).
	//
	// Required, because the column pair is: both halves are `REQ`, so there is no such thing
	// as an unattached contact assignment and the union carries the `== 1` rule rather than
	// `<= 1`. That is also why this field has no "empty clears it" state -- an empty union
	// means "write both columns null", which NetBox's own NOT NULL would refuse.
	//
	// Part of the natural key, as two filters rather than one: `?object_type=dcim.site` and
	// `?object_id=7`. Both halves or neither -- an id is only unique *within* its type, so
	// `?object_id=7` alone matches the site with id 7 and the tenant with id 7 alike
	// (docs/concepts/generic-refs.md, "Natural keys").
	ObjectRef ContactAssignmentTarget `json:"objectRef"`

	// ContactRef is the contact being assigned
	// (docs/netbox-schema.md -> tenancy.ContactAssignment, `contact ForeignKey REQ ->
	// tenancy.Contact on_delete=PROTECT`).
	//
	// Required, and part of the identity: filtered as `?contact_id=`. PROTECT rather than
	// CASCADE, which is why this is *not* the containment reference -- deleting the contact
	// in NetBox does not delete its assignments, it is refused while they exist. Delete the
	// assignments first.
	ContactRef ContactRef `json:"contactRef"`

	// RoleRef is the role the contact is assigned in
	// (`role ForeignKey REQ -> tenancy.ContactRole on_delete=PROTECT`).
	//
	// Required by the database even though the serializer marks it `required=False`: the
	// Django column has no `null=True` (netbox/tenancy/models/contacts.py:138-143), so an
	// assignment without a role is an integrity error rather than a null. Part of the
	// identity, filtered as `?role_id=`, and the reason one contact can be attached to one
	// object more than once.
	RoleRef ContactRoleRef `json:"roleRef"`

	// Priority ranks this assignment against the object's other contacts.
	//
	// `priority CharField len=50 choices=ContactPriorityChoices`, blank-able and nullable
	// (docs/netbox-schema.md -> tenancy.ContactAssignment). Not part of the identity: two
	// assignments differing only in priority are one row, and changing it is a PATCH.
	//
	// `""` is the cleared value, which is why it is a member of the enum -- see
	// ContactPriority for why this field carries no tri-state note.
	// +optional
	Priority ContactPriority `json:"priority,omitempty"`
}

// NetBoxContactAssignment is one tenancy.ContactAssignment in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// `objectRef` is the containment reference, and it is the first polymorphic one to be
// load-bearing on a Kind that is nothing but references. `ContactsMixin` declares
// `contacts = GenericRelation('tenancy.ContactAssignment', ...)`
// (netbox/netbox/models/features.py:392-396), and Django deletes the rows behind a
// GenericRelation when the object owning it is deleted -- so deleting a site in NetBox takes
// its contact assignments with it, for every member of the union. Without the owner reference
// the assignment CR would outlive the row, find nothing at `status.id` on the next reconcile,
// and the engine's create-if-absent step would recreate an assignment NetBox deliberately
// deleted (docs/decisions/0003-ownership-and-references.md rule 4).
//
// `contactRef` is deliberately not the containment reference, even though deleting the
// contact is the obvious way to want the assignment gone: `on_delete=PROTECT` means nothing
// disappears server-side, so an owner reference there would delete the CR and leave the row
// (docs/concepts/ownership.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcontactassignment
// +kubebuilder:printcolumn:name="Contact",type=string,JSONPath=`.spec.contactRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.roleRef.name`
// +kubebuilder:printcolumn:name="Priority",type=string,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxContactAssignment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxContactAssignmentSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus          `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxContactAssignment) NetBoxSpec() *NetBoxObjectSpec {
	return &a.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxContactAssignment) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxContactAssignmentList is a list of NetBoxContactAssignment.
// +kubebuilder:object:root=true
type NetBoxContactAssignmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxContactAssignment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxContactAssignment{}, &NetBoxContactAssignmentList{})
}
