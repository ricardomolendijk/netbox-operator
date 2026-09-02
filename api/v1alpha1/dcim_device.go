package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceStatus is one value of NetBox's DeviceStatusChoices: a device's lifecycle state.
//
// docs/netbox-schema.md -> dcim.Device records the column as
// `status CharField len=50 def=UNRESOLVED:DeviceStatusChoices.STATUS_ACTIVE
// choices=DeviceStatusChoices` -- the choice *class* and the fact that the AST walk could
// not resolve its default, never the members. The seven values are read from
// `netbox/dcim/choices.py` lines 192-198, `DeviceStatusChoices`, in the same 4.6.8 tree the
// digest was taken from.
//
// +kubebuilder:validation:Enum=offline;active;planned;staged;failed;inventory;decommissioning
type DeviceStatus string

const (
	// DeviceStatusOffline is a device that is racked and not powered.
	DeviceStatusOffline DeviceStatus = "offline"

	// DeviceStatusActive is a device in service, and NetBox's own default.
	DeviceStatusActive DeviceStatus = "active"

	// DeviceStatusPlanned is a device that does not physically exist yet.
	DeviceStatusPlanned DeviceStatus = "planned"

	// DeviceStatusStaged is a device being built out.
	DeviceStatusStaged DeviceStatus = "staged"

	// DeviceStatusFailed is a device that is broken.
	DeviceStatusFailed DeviceStatus = "failed"

	// DeviceStatusInventory is a spare, held but not deployed.
	DeviceStatusInventory DeviceStatus = "inventory"

	// DeviceStatusDecommissioning is a device being retired.
	DeviceStatusDecommissioning DeviceStatus = "decommissioning"
)

// DeviceAirflow is declared in dcim_devicetype.go, where NBO-027 landed it. NetBox declares
// the column on both dcim.Device and dcim.DeviceType with the same choice set
// (dcim/choices.py DeviceAirflowChoices), so one declaration shared by both kinds is the point.

// NetBoxDeviceSpec describes one dcim.Device.
//
// Three of NetBox's own columns are deliberately narrowed or absent, and each is a decision
// rather than an omission:
//
//   - `name` is **required here and nullable in NetBox**. NetBox holds unnamed devices --
//     blade chassis members, unracked spares -- and an unnamed device has no natural key at
//     all: every constraint on the model is over `Lower('name')` or over a rack position
//     (docs/netbox-schema.md -> dcim.Device, meta.constraints). It could not be looked up,
//     adopted or reconciled idempotently, and a lost `status.id` would make the operator
//     create a second one on the next pass. See docs/reference/netboxdevice.md.
//   - `location`, `rack`, `position` and `face` are absent. dcim.Location has a Kind
//     (NBO-048 lands the rest of it) and dcim.Rack does not (NBO-051), and
//     `position`/`face` are meaningless without a rack -- so the
//     `('rack', 'position', 'face')` constraint is unreachable and all four are left out
//     rather than accepted and dropped.
//   - `virtual_chassis`, `vc_position`, `vc_priority` and `config_template` are absent for
//     the same reason: NBO-053 and NBO-059 own the Kinds behind them, and NetBox ignores a
//     column it does not know rather than rejecting it, so a field accepted and silently
//     dropped reports success while writing nothing. `local_context_data` was in that list
//     until #241 and is not any more: it is the one ConfigContextModel column that
//     references no other Kind, so nothing was ever blocking it (LocalContextData below).
//
// There is no `tags` field, on this Kind or any other in v1alpha1: `tags` is written by the
// engine from Descriptor.Taggable as the provenance stamp (internal/reconciler, fieldRules),
// and a user-facing tag list arrives with NBO-055's NetBoxTag references.
type NetBoxDeviceSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the device's name (docs/netbox-schema.md -> dcim.Device,
	// `name CharField len=64`).
	//
	// **Required in this CRD although NetBox's column is nullable** -- see the type comment.
	//
	// Matched **case-insensitively**: both of the constraints this Kind can reach are over
	// `Lower('name')`, so `SW1` and `sw1` in one site are one device to NetBox and the
	// lookup must not treat them as two (internal/registry/dcim_device.go, LookupIExact).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// DeviceTypeRef is the model of hardware this device is (docs/netbox-schema.md ->
	// dcim.Device, `device_type ForeignKey REQ -> dcim.DeviceType on_delete=PROTECT`).
	//
	// Required, because NetBox's column is. It is not part of the identity, and it does not
	// cascade: `PROTECT` means NetBox refuses to delete a device type that still has
	// devices, so there is no server-side deletion for an owner reference to mirror
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	DeviceTypeRef DeviceTypeRef `json:"deviceTypeRef"`

	// RoleRef is the functional role the device plays (docs/netbox-schema.md -> dcim.Device,
	// `role ForeignKey REQ -> dcim.DeviceRole on_delete=PROTECT`).
	//
	// `dcim.DeviceRole` and not the `ipam.Role` that `RoleRef` names -- two separate models
	// with two separate endpoints, which is why the field is typed `DeviceRoleRef`.
	RoleRef DeviceRoleRef `json:"roleRef"`

	// SiteRef is the site the device is installed at (docs/netbox-schema.md -> dcim.Device,
	// `site ForeignKey REQ -> dcim.Site on_delete=PROTECT`).
	//
	// Required, and half of this Kind's identity: device names are unique **per site**, so
	// an unresolved `siteRef` means the operator cannot tell whether this device exists and
	// it waits rather than looking `sw1` up across every site in NetBox
	// (docs/concepts/lookups.md).
	//
	// It is **not** the containment parent, which is the one place this Kind departs from
	// every other required reference in the project: `on_delete=PROTECT` means NetBox
	// refuses to delete a site that still has devices, so an owner reference here would
	// promise a cascade the server declines. dcim.Device has no cascading foreign key at
	// all, so it has no containment parent -- see docs/reference/netboxdevice.md.
	SiteRef SiteRef `json:"siteRef"`

	// TenantRef is the tenant the device belongs to (docs/netbox-schema.md -> dcim.Device,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	//
	// Optional, and part of the identity when it is set: `(Lower('name'), 'site', 'tenant')`
	// is a constraint in its own right, so two tenants sharing a site may each have an `sw1`
	// (docs/netbox-schema.md -> dcim.Device, meta.constraints). Declared and unresolved, it
	// makes the operator wait rather than adopt the tenant-less device of the same name.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// PlatformRef is the operating system running on the device (docs/netbox-schema.md ->
	// dcim.Device, `platform ForeignKey -> dcim.Platform on_delete=SET_NULL`).
	// +optional
	PlatformRef *PlatformRef `json:"platformRef,omitempty"`

	// ClusterRef is the virtualization cluster this device is a host in
	// (docs/netbox-schema.md -> dcim.Device,
	// `cluster ForeignKey -> virtualization.Cluster on_delete=SET_NULL`).
	//
	// A containment-shaped reference that is deliberately **not** a containment parent, and
	// the reason is `SET_NULL` rather than taste: deleting the cluster leaves the device row
	// alive with the column cleared, so an owner reference would garbage-collect a CR whose
	// NetBox object still exists (docs/decisions/0003-ownership-and-references.md rule 4,
	// registry.ErrContainmentNotCascade).
	// +optional
	ClusterRef *ClusterRef `json:"clusterRef,omitempty"`

	// Serial is the chassis serial number assigned by the manufacturer
	// (docs/netbox-schema.md -> dcim.Device, `serial CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Serial string `json:"serial,omitempty"`

	// AssetTag is the tag used to identify this device, and the only **globally unique**
	// column on the model (docs/netbox-schema.md -> dcim.Device,
	// `asset_tag CharField UNIQUE len=50`).
	//
	// That makes it the strongest natural key this Kind has and the first one tried, and it
	// is also the one field whose collision is cluster-wide rather than site-local: two CRs
	// in two namespaces claiming one asset tag are one device, so the loser reports
	// `Conflict` naming the winner.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. Cleared as `null` rather than as an empty string -- the column is unique and
	// nullable (`null=True, unique=True`, `netbox/dcim/models/devices.py` lines 555-562), so
	// two devices with an empty-string tag would collide where two with `null` do not. See
	// registry.Field.EmptyIsNull.
	// +kubebuilder:validation:MaxLength=50
	// +optional
	AssetTag string `json:"assetTag,omitempty"`

	// Status is the device's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status DeviceStatus `json:"status,omitempty"`

	// Airflow is which way air moves through the chassis (docs/netbox-schema.md ->
	// dcim.Device, `airflow CharField len=50 choices=DeviceAirflowChoices`).
	//
	// Not defaulted: the column carries no `def=`, and an unset airflow is a real state in
	// NetBox -- a device type may declare one the device inherits -- so defaulting it would
	// make every adopted device drift towards a value nobody chose.
	// +optional
	Airflow DeviceAirflow `json:"airflow,omitempty"`

	// Latitude is the device's GPS latitude in decimal degrees, as a string.
	//
	// A string and not a float64, for the reason NetBoxSite.Latitude gives: NetBox stores it
	// as a DecimalField and returns it as a string, and an OpenAPI `number` round-trips
	// through IEEE-754 on its way in and out of the API server. The engine compares it
	// numerically (internal/netbox/drift.go, scalarEqual), so `"51.9244"` and NetBox's
	// `"51.924400"` are the same value and produce no PATCH.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). A cleared coordinate is written as `null` rather
	// than as an empty string, which is what NetBox's nullable DecimalField takes -- see
	// registry.Field.EmptyIsNull.
	//
	// Two integer digits and six fractional, off docs/netbox-schema.md -> dcim.Device:
	// `latitude DecimalField decimal(8,6)`. The `^$` alternative is the clear, and the CEL
	// rule has to admit it too -- `double("")` is an error, so a rule that did not
	// short-circuit would reject at admission the one value clearing uses.
	// +kubebuilder:validation:Pattern=`^$|^-?[0-9]{1,2}(\.[0-9]{1,6})?$`
	// +kubebuilder:validation:XValidation:rule="self == \"\" || (double(self) >= -90.0 && double(self) <= 90.0)",message="latitude must be between -90 and 90 degrees"
	// +optional
	Latitude string `json:"latitude,omitempty"`

	// Longitude is the device's GPS longitude in decimal degrees, as a string. A string for
	// the same reason as Latitude, and cleared the same way.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	//
	// Three integer digits, not two: `longitude DecimalField decimal(9,6)`
	// (docs/netbox-schema.md -> dcim.Device) is nine digits with six after the point, and
	// longitude runs to +-180.
	// +kubebuilder:validation:Pattern=`^$|^-?[0-9]{1,3}(\.[0-9]{1,6})?$`
	// +kubebuilder:validation:XValidation:rule="self == \"\" || (double(self) >= -180.0 && double(self) <= 180.0)",message="longitude must be between -180 and 180 degrees"
	// +optional
	Longitude string `json:"longitude,omitempty"`

	// PrimaryIP4Ref is the device's primary IPv4 address (docs/netbox-schema.md ->
	// dcim.Device, `primary_ip4 OneToOneField -> ipam.IPAddress on_delete=SET_NULL`).
	//
	// **Always deferred.** The ring is `Device -> IPAddress -> Interface -> Device`: the
	// address is assigned to an interface, the interface belongs to this device, and this
	// device points back at the address. No apply order satisfies it, so the column is
	// stripped from the create and applied by a follow-up PATCH -- unconditionally, because
	// there is no first pass in which it could resolve and `IfUnresolved` would spend a
	// reconcile discovering that every time (NBO-015). `DeferredFieldPending` is what the
	// object reports in between, and `status.deferredPending` names the field.
	// +optional
	PrimaryIP4Ref *IPAddressRef `json:"primaryIP4Ref,omitempty"`

	// PrimaryIP6Ref is the device's primary IPv6 address (docs/netbox-schema.md ->
	// dcim.Device, `primary_ip6 OneToOneField -> ipam.IPAddress on_delete=SET_NULL`).
	// Deferred on the same terms as PrimaryIP4Ref, and independently of it.
	// +optional
	PrimaryIP6Ref *IPAddressRef `json:"primaryIP6Ref,omitempty"`

	// OOBIPRef is the device's out-of-band management address (docs/netbox-schema.md ->
	// dcim.Device, `oob_ip OneToOneField -> ipam.IPAddress on_delete=SET_NULL`). Deferred on
	// the same terms as PrimaryIP4Ref.
	//
	// The spec name is `oobIPRef` and the column is `oob_ip`, which is one of the pairs a
	// camelCase-to-snake_case convention gets wrong -- `oob_i_p` -- and NetBox would answer
	// with a 201 that wrote nothing. The registry's field map is where the two are joined
	// (internal/registry/fields.go, Field).
	// +optional
	OOBIPRef *IPAddressRef `json:"oobIPRef,omitempty"`

	// Description is free text shown next to the device. Declared on PrimaryModel rather
	// than on dcim.Device (docs/netbox-schema.md -> dcim.Device,
	// `description (PrimaryModel) CharField len=200`); an inherited column is as writable as
	// a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the device's long-form notes field. Also inherited from PrimaryModel, and
	// a TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`

	// LocalContextData is this device's own slice of config context: the JSON document NetBox
	// merges **last** when it renders the device's configuration, after every
	// extras.ConfigContext whose selectors matched (docs/netbox-schema.md -> dcim.Device,
	// `local_context_data (ConfigContextModel) JSONField`;
	// `netbox/extras/models/configs.py`, `ConfigContextModel.get_config_context()`).
	//
	// It is a column on the device and not a reference to one, which is why it is a spec
	// field here rather than a NetBoxConfigContext with a selector that picks out one device.
	// The two are different mechanisms in NetBox and only this one is part of the device: it
	// is created, updated and deleted with the row, so a device whose overrides live here
	// cannot be left behind by a config context somebody else deleted. It is also the highest
	// precedence in the merge, so what goes here is the per-object exception rather than the
	// shared policy -- policy belongs in a NetBoxConfigContext, where it can be reviewed once
	// and applied to everything it selects.
	//
	// Compared as a whole document rather than as a scalar (registry.ClassJSON). The scalar
	// rule unwraps any JSON object carrying an `id` or a `value` key, because that is how
	// NetBox renders a foreign key and a choice on read -- and an `id` key is ordinary inside
	// inventory data, so a local context carrying one would differ from itself on every read
	// and be PATCHed forever (docs/concepts/drift.md).
	//
	// An object rather than any JSON value, which is NetBox's rule and not this operator's:
	// `ConfigContextModel.clean()` refuses a `local_context_data` that is not a mapping,
	// because rendering merges it into a dict. Declaring the type here turns that 400 into a
	// rejection at admission, where the message names the field.
	//
	// Omit it to leave NetBox's own value alone; set it to `{}` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). `{}` and not `null`, although the column itself is
	// nullable: the API server prunes a null under a schema that is not marked nullable,
	// before validation and before the operator ever reads the object back
	// (hack/crd-nullable.sh states the same rule from the other side), so a `null` here would
	// be indistinguishable from omitting the field. An empty document merges to nothing,
	// which is what clearing it is asking for.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	LocalContextData *JSONDocument `json:"localContextData,omitempty"`

	// Interfaces are this device's dcim.Interface components, declared inline and
	// materialised as real NetBoxInterface CRs -- with their addresses as real
	// NetBoxIPAddress CRs under them (ADR-0003 rule 5, docs/concepts/inline-children.md).
	//
	// **Not a NetBox column, and the only field on this spec that is not.** `dcim.Device` has
	// no `interfaces` column: the foreign key points the other way, from each interface at
	// its device, so nothing here reaches the device's own payload and the field is
	// deliberately absent from the descriptor's field map
	// (internal/registry/dcim_device.go). What it produces is child CRs, each of which
	// writes its own NetBox object -- the device never writes NetBox on a child's behalf.
	//
	// **Sugar, and optional, which is the term on which it is in v1alpha1 at all**
	// (ADR-0003 rule 5). Every entry is equally expressible as a NetBoxInterface with a
	// `deviceRef` naming this device, the longhand kind stays the complete one -- an inline
	// entry offers a subset of its fields -- and the two coexist on one device: a
	// hand-written NetBoxInterface pointing at this device is never pruned, never adopted and
	// absent from `status.children`.
	//
	// **Omitting it and writing `[]` are the same instruction**, unlike every other optional
	// field on this spec. There is no NetBox value to leave alone, so there is no third
	// state: both mean "declare no children", and both prune the children a previous spec
	// declared. Removing one entry prunes exactly that entry's child and its addresses.
	//
	// A list with a key rather than an ordered one, so the API server rejects two entries
	// named the same, and so reordering the list changes no child's name, path or
	// resourceVersion -- identity is the key, never the index
	// (docs/concepts/inline-children.md).
	// +kubebuilder:validation:MaxItems=128
	// +listType=map
	// +listMapKey=name
	// +optional
	Interfaces []InlineInterface `json:"interfaces,omitempty"`
}

// NetBoxDevice is one dcim.Device in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), which is what
// makes a cross-namespace `assetTag` collision possible: NetBox's uniqueness on that column
// is global and a namespace boundary does not partition it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbdev
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.siteRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.deviceTypeRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="Primary-IP",type=string,JSONPath=`.spec.primaryIP4Ref.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxDeviceSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (d *NetBoxDevice) NetBoxSpec() *NetBoxObjectSpec { return &d.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (d *NetBoxDevice) NetBoxStatus() *NetBoxObjectStatus { return &d.Status }

// NetBoxDeviceList is a list of NetBoxDevice.
// +kubebuilder:object:root=true
type NetBoxDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxDevice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxDevice{}, &NetBoxDeviceList{})
}
