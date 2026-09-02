package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModuleStatus is one value of NetBox's ModuleStatusChoices.
//
// Six values, read from `netbox/dcim/choices.py:244`, `ModuleStatusChoices`, in the same
// NetBox 4.6.8 tree docs/netbox-schema.md was taken from -- the digest records the choice
// *class* and not its members (docs/netbox-schema.md -> dcim.Module, `status CharField
// len=50 def=UNRESOLVED:ModuleStatusChoices.STATUS_ACTIVE choices=ModuleStatusChoices`).
//
// Not the same members as any status enum shipped so far and deliberately its own Go type:
// `decommissioning` is here and `reserved`, `available` and `deprecated` -- RackStatus's --
// are not. This ChoiceSet declares `key = 'Module.status'`
// (hack/testdata/api-schema-4.6.8.json.gz -> choices.ModuleStatusChoices), so a deployment
// can add values through FIELD_CHOICES and a closed enum would reject one at admission;
// enumerated anyway, following RackStatus, because a typo caught by `kubectl apply` is worth
// more than an extension nobody has made.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=offline;active;planned;staged;failed;decommissioning
type ModuleStatus string

const (
	// ModuleStatusOffline is a module that is installed but not in service.
	ModuleStatusOffline ModuleStatus = "offline"

	// ModuleStatusActive is a module in service, and NetBox's own default.
	ModuleStatusActive ModuleStatus = "active"

	// ModuleStatusPlanned is a module that does not physically exist yet.
	ModuleStatusPlanned ModuleStatus = "planned"

	// ModuleStatusStaged is a module racked and cabled but not yet in service.
	ModuleStatusStaged ModuleStatus = "staged"

	// ModuleStatusFailed is a module that has failed.
	ModuleStatusFailed ModuleStatus = "failed"

	// ModuleStatusDecommissioning is a module being removed from service.
	ModuleStatusDecommissioning ModuleStatus = "decommissioning"
)

// NetBoxModuleSpec describes one dcim.Module.
//
// A physical module installed in a bay: the instance to `NetBoxModuleType`'s catalogue entry,
// the way `NetBoxDevice` is the instance to `NetBoxDeviceType`.
//
// **The bay is the identity.** `dcim.Module` declares no `meta.constraints` at all, and it
// does not need one: `module_bay` is a `OneToOneField`
// (docs/netbox-schema.md -> dcim.Module, `module_bay OneToOneField REQ -> dcim.ModuleBay
// on_delete=CASCADE`), which is a foreign key Django declares `unique=True` on, so the
// database already holds at most one module per bay. That single column is the natural key --
// see internal/registry/dcim_module.go, which also records the one way the *filter* behind it
// is wider than the column.
type NetBoxModuleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// DeviceRef is the device the module is installed in. Required, because NetBox's column
	// is (`device ForeignKey REQ -> dcim.Device on_delete=CASCADE`).
	//
	// Redundant with `moduleBayRef` in the data -- a bay already names its device -- and
	// still required by NetBox, which denormalises it so that a module can be filtered by
	// device without a join. The operator writes what the column asks for rather than
	// deriving it, because a derived value that disagrees with the bay's device is a 400 the
	// user cannot see the cause of.
	//
	// Not part of the natural key: `module_bay` alone is unique, so adding `device_id` to the
	// lookup would narrow it below what the database enforces.
	DeviceRef DeviceRef `json:"deviceRef"`

	// ModuleBayRef is the bay the module occupies, and this kind's entire natural key.
	// Required, because NetBox's column is
	// (`module_bay OneToOneField REQ -> dcim.ModuleBay on_delete=CASCADE`).
	//
	// One module per bay, enforced by the database: a `OneToOneField` is a `ForeignKey` with
	// `unique=True`. Installing a second module into an occupied bay is a NetBox 400 naming
	// the field, surfaced verbatim in the Ready message.
	//
	// CASCADE, and the containment parent: deleting the NetBoxModuleBay deletes this CR with
	// it (docs/decisions/0003-ownership-and-references.md rule 4).
	//
	// Until it resolves there is no applicable candidate, so the object writes nothing at all
	// rather than being created into a bay the operator has not identified.
	ModuleBayRef ModuleBayRef `json:"moduleBayRef"`

	// ModuleTypeRef is the catalogue entry this module is an instance of. Required, because
	// NetBox's column is (`module_type ForeignKey REQ -> dcim.ModuleType
	// on_delete=PROTECT`).
	//
	// PROTECT, so NetBox refuses to delete a module type while any module points at it; that
	// surfaces on the *module type* as Deleting=False, Reason=Protected.
	//
	// Writing it is also what makes NetBox instantiate the type's component templates into
	// this module -- see the type's note about `replicate_components` on NetBoxModule itself.
	ModuleTypeRef ModuleTypeRef `json:"moduleTypeRef"`

	// Status is the module's operational status
	// (docs/netbox-schema.md -> dcim.Module, `status CharField len=50
	// def=UNRESOLVED:ModuleStatusChoices.STATUS_ACTIVE`).
	//
	// The column is NOT NULL with a server-side default, so omitting the field leaves NetBox
	// to choose `active` on create and leaves an adopted module's own status alone. There is
	// no `""` member for the same reason.
	// +optional
	Status ModuleStatus `json:"status,omitempty"`

	// Serial is the manufacturer's serial number
	// (docs/netbox-schema.md -> dcim.Module, `serial CharField len=50`).
	//
	// Not unique in NetBox and so not a lookup candidate.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Serial string `json:"serial,omitempty"`

	// AssetTag is the deployment's own inventory tag
	// (docs/netbox-schema.md -> dcim.Module, `asset_tag CharField UNIQUE len=50`).
	//
	// Globally `UNIQUE` across the whole install, and deliberately **not** a natural-key
	// candidate -- the dcim.Rack argument, unchanged: an asset tag identifies the piece of
	// hardware, and this CR describes the slot it is installed in. Adopting by asset tag
	// would let moving a card between chassis rewrite the device and bay of somebody else's
	// module. A duplicate comes back as NetBox's own 400.
	//
	// The column is `null=True` as well as unique, so an emptied field is sent as `null`
	// rather than as `""`: two modules with an empty-string asset tag would collide on the
	// unique index, where two NULLs do not (registry.Field.EmptyIsNull).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	AssetTag string `json:"assetTag,omitempty"`

	// Description is free text shown next to the module.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the module's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxModule is one dcim.Module in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Absent deliberately:
//
//   - `replicate_components` and `adopt_components`. Both are `write_only` BooleanFields
//     declared on `ModuleSerializer` rather than columns on the model
//     (hack/testdata/api-schema-4.6.8.json.gz -> ModuleSerializer, `declared`), and both are
//     *actions* taken once at write time rather than state an object holds: NetBox defaults
//     `replicate_components` to true, so creating a module instantiates its type's component
//     templates, and `adopt_components` to false. A write-only field cannot be read back, so
//     mapping one would put a key in every payload that never appears in the response and
//     the drift comparison would never settle. They arrive with the component kinds and the
//     adopt-not-duplicate work, which is the rest of #54.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind.
//   - `bookmarks`, `journal_entries` and `subscriptions` are GenericRelations, not columns.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmodule
// +kubebuilder:printcolumn:name="Device",type=string,JSONPath=`.spec.deviceRef.name`
// +kubebuilder:printcolumn:name="Bay",type=string,JSONPath=`.spec.moduleBayRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.moduleTypeRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxModule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxModuleSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (m *NetBoxModule) NetBoxSpec() *NetBoxObjectSpec { return &m.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (m *NetBoxModule) NetBoxStatus() *NetBoxObjectStatus { return &m.Status }

// NetBoxModuleList is a list of NetBoxModule.
// +kubebuilder:object:root=true
type NetBoxModuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxModule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxModule{}, &NetBoxModuleList{})
}
