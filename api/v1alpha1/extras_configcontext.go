package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxConfigContextSpec describes one extras.ConfigContext: a JSON document NetBox merges
// into the rendered configuration of every object the context's assignment sets select.
//
// **The widest many-to-many surface in the catalogue** -- thirteen sets, every one of them a
// `ClassRefMany` entry on the descriptor and nothing else (docs/netbox-schema.md ->
// extras.ConfigContext). They are what makes this kind interesting and they are not the
// object's own attributes: a config context has no site, it *applies to* sites. Each set is
// resolved, sorted, deduplicated and compared as an order-independent id set, so reordering
// one in the manifest produces no API write at all
// (docs/concepts/drift.md, docs/concepts/references.md).
//
// `tags` is the trap in that list. On every other kind `tags` is `TagsMixin` -- the object's
// own tags, and where the operator writes its provenance stamp. Here it is a plain
// `ManyToManyField -> extras.Tag` declared on the model itself, with
// `related_name='+'` and a `SlugRelatedField` serializer rather than
// `NetBoxTaggableManagerField` (docs/netbox-schema.md -> extras.ConfigContext; the digest
// records no `TagsMixin` in the bases). It selects *which tagged objects the context applies
// to*. So `tagRefs` below is a user field, this kind is **not** `Taggable`, and the stamp
// stays off it -- writing the `k8s-managed` tag into that set would silently change which
// objects in NetBox get this configuration, which is the loudest possible way to get a
// boolean wrong. `internal/registry` refuses a descriptor that declares both
// (`ErrTagsFieldOnTaggableKind`), so the mistake cannot be made twice.
//
// It follows that a NetBoxConfigContext carries **no provenance at all**: no `TagsMixin` and
// no `CustomFieldsMixin`, so neither half of the stamp has a column to go in. A sweep will
// not find one and multi-writer detection is blind to one
// (docs/operations/provenance.md, docs/operations/multi-writer.md).
//
// The `SyncedDataMixin` columns are absent for the reason they are absent from every template
// kind: NetBox overwrites `data` from a `core.DataSource` itself, so declaring both would be
// two writers for one column.
type NetBoxConfigContextSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the context's name, and this kind's natural key.
	//
	// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md -> extras.ConfigContext),
	// so the database enforces the identity: a `?name=` lookup matches at most one row and
	// there is no ambiguity to report.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Data is the JSON document merged into the configuration of every selected object.
	//
	// Required by NetBox (`data JSONField REQ`), so it has no empty state to document: a
	// context with nothing to contribute is expressed by `isActive: false`, not by an absent
	// document.
	//
	// Compared as a whole document rather than as a scalar, which is the difference
	// `ClassJSON` exists for: the scalar rule unwraps any JSON object carrying an `id` or a
	// `value` key because that is how NetBox renders a foreign key on read, so a `data`
	// document that happens to contain an `id` key would never settle and the operator would
	// PATCH it forever (docs/concepts/drift.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Data JSONDocument `json:"data"`

	// ProfileRef points at the NetBoxConfigContextProfile whose JSON Schema NetBox validates
	// Data against (`profile ForeignKey -> extras.ConfigContextProfile on_delete=PROTECT`).
	//
	// Not this kind's containment parent, and no owner reference is taken on it: `PROTECT`
	// means NetBox does not cascade, so promising a cluster-side cascade would leave the CR
	// garbage-collected and the NetBox row refusing to go
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	ProfileRef *ConfigContextProfileRef `json:"profileRef,omitempty"`

	// Weight orders contexts when more than one applies to an object: a higher weight is
	// merged later and therefore wins.
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Weight *int32 `json:"weight,omitempty"`

	// IsActive turns the context off without deleting it. An inactive context is merged into
	// nothing.
	// +kubebuilder:default=true
	// +optional
	IsActive *bool `json:"isActive,omitempty"`

	// RegionRefs selects objects by region. Like every set on this kind it is a full
	// replacement: the list is the whole membership, so removing an entry removes the
	// assignment.
	//
	// A partially resolvable list writes nothing at all -- writing the entries that did
	// resolve would be a full-list replacement with a shorter list, which is a deletion
	// reported as a success. The object reports `RefsResolved=False` naming the entry that
	// failed (docs/concepts/references.md).
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	//
	// MaxItems is not a NetBox limit. `ObjectRef` carries five CEL rules and the API server
	// costs each at the list's maximum length, so an unbounded list of refs is refused at
	// install; 256 is the project standard (docs/concepts/references.md, "A list needs a
	// bound").
	// +optional
	// +kubebuilder:validation:MaxItems=256
	RegionRefs []RegionRef `json:"regions,omitempty"`

	// SiteGroupRefs selects objects by site group. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	SiteGroupRefs []SiteGroupRef `json:"siteGroups,omitempty"`

	// SiteRefs selects objects by site. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	SiteRefs []SiteRef `json:"sites,omitempty"`

	// LocationRefs selects objects by location. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	LocationRefs []LocationRef `json:"locations,omitempty"`

	// DeviceTypeRefs selects devices by type. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	DeviceTypeRefs []DeviceTypeRef `json:"deviceTypes,omitempty"`

	// RoleRefs selects devices and virtual machines by role. NetBox spells the column
	// `roles`, not `device_roles`, even though the target is `dcim.DeviceRole` -- one role
	// serves both (docs/netbox-schema.md -> extras.ConfigContext).
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	RoleRefs []DeviceRoleRef `json:"roles,omitempty"`

	// PlatformRefs selects objects by platform. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	PlatformRefs []PlatformRef `json:"platforms,omitempty"`

	// ClusterTypeRefs selects virtual machines by cluster type. See RegionRefs for how a set
	// behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ClusterTypeRefs []ClusterTypeRef `json:"clusterTypes,omitempty"`

	// ClusterGroupRefs selects virtual machines by cluster group. See RegionRefs for how a
	// set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ClusterGroupRefs []ClusterGroupRef `json:"clusterGroups,omitempty"`

	// ClusterRefs selects virtual machines by cluster. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	ClusterRefs []ClusterRef `json:"clusters,omitempty"`

	// TenantGroupRefs selects objects by tenant group. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	TenantGroupRefs []TenantGroupRef `json:"tenantGroups,omitempty"`

	// TenantRefs selects objects by tenant. See RegionRefs for how a set behaves.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	TenantRefs []TenantRef `json:"tenants,omitempty"`

	// TagRefs selects objects by the tags *they* carry. **Not this object's own tags** --
	// see the type comment: `extras.ConfigContext` has no `TagsMixin`, so there is no column
	// for the provenance stamp and this set is a user field the operator never touches.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	TagRefs []TagRef `json:"tags,omitempty"`

	// Description is free text shown next to the context.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxConfigContext is one extras.ConfigContext in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// No containment parent: every one of this model's foreign keys is either a to-many
// assignment set -- which cannot be a containment parent, because Kubernetes garbage
// collection waits for *every* owner and a list of parents is that mistake unbounded -- or
// `profile`, which is `PROTECT` and therefore does not cascade
// (docs/decisions/0003-ownership-and-references.md rule 4).
//
// Deleting one destroys nothing beyond the context itself. Nothing in NetBox points at a
// config context, so there is no `PROTECT` to refuse the delete and no data-loss guard: the
// merged configuration it contributed simply stops being contributed.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcc
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxConfigContext struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxConfigContextSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus      `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (c *NetBoxConfigContext) NetBoxSpec() *NetBoxObjectSpec { return &c.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxConfigContext) NetBoxStatus() *NetBoxObjectStatus { return &c.Status }

// NetBoxConfigContextList is a list of NetBoxConfigContext.
// +kubebuilder:object:root=true
type NetBoxConfigContextList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxConfigContext `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxConfigContext{}, &NetBoxConfigContextList{})
}
