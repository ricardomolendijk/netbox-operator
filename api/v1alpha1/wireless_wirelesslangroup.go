package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxWirelessLANGroupSpec describes one wireless.WirelessLANGroup.
//
// The third NestedGroupModel to ship, and the one that settles that the tree shape never
// decides the natural key -- the *constraint lines* do, and this model's are a third
// arrangement again:
//
//   - dcim.Region: `meta.constraints` on `(parent, name)` **and** on `(name)` with
//     `condition=Q(parent__isnull=True)` (netbox/dcim/models/sites.py:62-82). `parent` is
//     part of its identity, so a top-level region is a different natural key and needs the
//     `parent_id=null` pin.
//   - tenancy.TenantGroup: no `meta.constraints` at all, column-level `UNIQUE` on `name` and
//     `slug`. Global uniqueness, one candidate, no pin.
//   - wireless.WirelessLANGroup: **both**, and it lands where TenantGroup does.
//     `name = CharField(max_length=100, unique=True)` (netbox/wireless/models.py:53-58) and
//     `slug = SlugField(max_length=100, unique=True)` (netbox/wireless/models.py:59-63) are
//     column-level `UNIQUE`, and the one table constraint is
//     `UniqueConstraint(fields=('parent', 'name'), name='%(app_label)s_%(class)s_unique_parent_name')`
//     (netbox/wireless/models.py:70-75) -- **with no `condition=` clause of any kind.**
//
// So there is no `parent IS NULL` variant to model, and `(parent, name)` is subsumed by the
// column-level `UNIQUE` on `name` that already makes a name globally unique. `slug` alone
// identifies at most one group whatever its parent is. Adding the `?parent_id=null` pin that
// plan.md §8.1 asserts every MPTT kind needs would be wrong the same two ways it would be
// wrong on a NetBoxTenantGroup: it would make a nested group's slug unfindable, and it would
// express a constraint the database does not have.
type NetBoxWirelessLANGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's label in the NetBox UI.
	//
	// Column-unique across NetBox (netbox/wireless/models.py:53-58), so two groups may not
	// share a name even under different parents -- which is what makes the
	// `unique(parent, name)` table constraint redundant rather than identity-bearing.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the group's URL-safe identifier, and this kind's whole natural key.
	//
	// NetBox enforces uniqueness on it globally (netbox/wireless/models.py:59-63) while this
	// CRD is namespaced (docs/decisions/0002-crd-scoping.md), so two
	// NetBoxWirelessLANGroups in different namespaces claiming one slug is one group and a
	// Conflict -- not two groups.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this group under another one.
	//
	// Self-referential: `parent TreeForeignKey -> self on_delete=CASCADE` on
	// NestedGroupModel (netbox/netbox/models/__init__.py:171-178). Not part of the natural
	// key, for the reason in the type comment, so a group whose parent does not exist yet is
	// still identifiable: the engine creates it top-level and PATCHes `parent` on once the
	// reference resolves (Descriptor.Deferred, DeferIfUnresolved).
	//
	// In between, the object reports Ready=False with RefsResolved naming this field and
	// `parentRef` in status.deferredPending. The MPTT parent-cycle check is NBO-044's
	// admission webhook.
	// +optional
	ParentRef *WirelessLANGroupRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the group
	// (`description (NestedGroupModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field, inherited from NestedGroupModel. A
	// TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxWirelessLANGroup is one wireless.WirelessLANGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Catalogue-
// shaped: the convention is one shared namespace holding the groups and a NetBoxRefGrant
// letting team namespaces point at them.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbwlangroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxWirelessLANGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxWirelessLANGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus         `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxWirelessLANGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxWirelessLANGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxWirelessLANGroupList is a list of NetBoxWirelessLANGroup.
// +kubebuilder:object:root=true
type NetBoxWirelessLANGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxWirelessLANGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxWirelessLANGroup{}, &NetBoxWirelessLANGroupList{})
}
