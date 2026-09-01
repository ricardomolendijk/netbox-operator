package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WirelessLANStatus is one value of NetBox's WirelessLANStatusChoices.
//
// The four values are read from `netbox/wireless/choices.py:16-29`
// (`WirelessLANStatusChoices`) in the NetBox 4.6.8 tree, because the schema digest records the
// choice *class* and not its members. Four, not three: unlike `ipam.VLAN` this set has a
// `disabled` state as well as `deprecated`, which is exactly the sort of near-miss a shared
// status enum would have papered over.
//
// +kubebuilder:validation:Enum=active;reserved;disabled;deprecated
type WirelessLANStatus string

const (
	// WirelessLANStatusActive is an SSID in service, and NetBox's own default.
	WirelessLANStatusActive WirelessLANStatus = "active"

	// WirelessLANStatusReserved is an SSID set aside for future use.
	WirelessLANStatusReserved WirelessLANStatus = "reserved"

	// WirelessLANStatusDisabled is an SSID configured but switched off.
	WirelessLANStatusDisabled WirelessLANStatus = "disabled"

	// WirelessLANStatusDeprecated is an SSID being retired.
	WirelessLANStatusDeprecated WirelessLANStatus = "deprecated"
)

// NetBoxWirelessLANSpec describes one wireless.WirelessLAN.
//
// **A scoped kind, so `scope` and never `siteRef`.** `wireless.WirelessLAN` mixes in
// `CachedScopeMixin` (netbox/wireless/models.py:80), which means the writable pair is
// `scope_type` + `scope_id` and `_region`, `_site_group`, `_site` and `_location` are
// read-only caches NetBox maintains from it (netbox/dcim/models/mixins.py:41-89). Writing
// `site` to this model does not fail -- NetBox drops the unknown key, returns 201, and the
// object is created unscoped while every subsequent read agrees with the spec, so it reports
// Ready=True forever and never drifts. That is the bug `netbox-populator` shipped and the
// reason no scoped kind here has a `siteRef`, not even as sugar
// (docs/concepts/generic-refs.md#the-scope-pair,
// docs/decisions/0003-ownership-and-references.md decision 2).
//
// **This kind's identity is not enforced by NetBox.** wireless.WirelessLAN declares no
// `meta.constraints` -- only two indexes, on `(ssid, id)` and on `(scope_type, scope_id)`
// (netbox/wireless/models.py:118-125). Two identical SSIDs in one scope are legal in NetBox,
// so `(ssid, scope, tenant)` is a lookup convention rather than a key: a lookup matching more
// than one row is an `*netbox.AmbiguousError` and becomes `Conflict` with nothing written,
// the same engine-wide rule ipam.IPAddress and ipam.VLANGroup rely on. The `scope` term is
// not decoration -- it is what keeps two houses' `Donkersloot` SSIDs apart.
//
// `authPSK` is absent by design; see api/v1alpha1/wireless_auth.go for why and for what it
// costs.
type NetBoxWirelessLANSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// SSID is the network name (netbox/wireless/models.py:84-87,
	// `SSID_MAX_LENGTH = 32  # Per IEEE 802.11-2007`, netbox/wireless/constants.py:1).
	//
	// Part of this kind's identity, but only together with the scope and the tenant: see the
	// type comment for why an SSID alone does not identify a wireless LAN and why a duplicate
	// is a Conflict rather than an adoption.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	SSID string `json:"ssid"`

	// GroupRef files the SSID under a wireless LAN group
	// (`group ForeignKey -> wireless.WirelessLANGroup on_delete=SET_NULL`,
	// netbox/wireless/models.py:88-94).
	//
	// `SET_NULL` and not `CASCADE`, which is why this is not the containment reference:
	// deleting the group in NetBox leaves the SSID behind with no group, so an owner
	// reference here would delete a CR describing a row that still exists.
	// +optional
	GroupRef *WirelessLANGroupRef `json:"groupRef,omitempty"`

	// Status is the SSID's lifecycle state: active, reserved, disabled or deprecated.
	//
	// Defaulted to NetBox's own default (netbox/wireless/models.py:95-100) so the operator
	// manages the field from the first reconcile: a defaulted field that never reaches a
	// payload is a field the operator can never correct.
	// +kubebuilder:default=active
	// +optional
	Status WirelessLANStatus `json:"status,omitempty"`

	// VLANRef bridges the SSID onto a VLAN
	// (`vlan ForeignKey -> ipam.VLAN on_delete=PROTECT`, netbox/wireless/models.py:101-107).
	//
	// `PROTECT`, so a NetBoxWirelessLAN holding this reference blocks deletion of that VLAN
	// in NetBox and the VLAN's owner gets `Deleting=False, Reason=Protected` naming this
	// object (docs/concepts/deletion.md).
	// +optional
	VLANRef *VLANRef `json:"vlanRef,omitempty"`

	// Scope attaches the SSID to a Region, SiteGroup, Site or Location, and is **part of this
	// object's identity** -- an SSID is only unique within its scope, and only by convention
	// even there.
	//
	// Omit it for an SSID with no scope, which is a normal state rather than a missing value:
	// neither column carries a real `REQ`
	// (docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest). An empty
	// `scope: {}` clears the scope by writing both columns as null; an absent one leaves
	// whatever NetBox holds alone.
	//
	// Written as the `(scope_type, scope_id)` pair and diffed as a unit, so moving an SSID
	// from a Site to a Region is one change and one PATCH carrying both keys. Drift is keyed
	// on the pair only: a change to the cached `_site` NetBox recomputed is not a difference
	// the operator can see or correct, because the four caches are in `ReadOnly`.
	// +optional
	Scope *ScopeRef `json:"scope,omitempty"`

	// TenantRef assigns the SSID to a tenant
	// (`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`,
	// netbox/wireless/models.py:108-114), and is the third term of the natural key.
	//
	// `PROTECT`, so this reference blocks deletion of the tenant in NetBox. Not a containment
	// reference for the same reason: a `PROTECT` foreign key cascades nothing, so it can
	// contribute no owner reference (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// AuthType is the authentication method: open, wep, wpa-personal or wpa-enterprise
	// (netbox/wireless/models.py:25-31).
	//
	// Undefaulted, unlike `status`. The column is nullable with no Django default, so
	// defaulting it would assert a security posture nobody described.
	// +optional
	AuthType WirelessAuthType `json:"authType,omitempty"`

	// AuthCipher is the encryption cipher: auto, tkip or aes
	// (netbox/wireless/models.py:32-38). Nullable with no Django default, so undefaulted here
	// too.
	// +optional
	AuthCipher WirelessAuthCipher `json:"authCipher,omitempty"`

	// Description is free text shown next to the SSID
	// (`description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the SSID's long-form notes field, inherited from PrimaryModel. A TextField
	// rather than a CharField: it has no max_length, so there is no MaxLength marker to
	// derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxWirelessLAN is one wireless.WirelessLAN in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbwlan
// +kubebuilder:printcolumn:name="SSID",type=string,JSONPath=`.spec.ssid`
// +kubebuilder:printcolumn:name="VLAN",type=string,JSONPath=`.spec.vlanRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxWirelessLAN struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxWirelessLANSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (l *NetBoxWirelessLAN) NetBoxSpec() *NetBoxObjectSpec { return &l.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (l *NetBoxWirelessLAN) NetBoxStatus() *NetBoxObjectStatus { return &l.Status }

// NetBoxWirelessLANList is a list of NetBoxWirelessLAN.
// +kubebuilder:object:root=true
type NetBoxWirelessLANList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxWirelessLAN `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxWirelessLAN{}, &NetBoxWirelessLANList{})
}
