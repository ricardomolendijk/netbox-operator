package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LinkStatus is declared in dcim_cable.go: `wireless.WirelessLink.status` points at the same
// LinkStatusChoices dcim.Cable uses (netbox/wireless/models.py:155-160), which is why the type
// is named for the concept and not for either kind.

// DistanceUnit is one value of NetBox's DistanceUnitChoices.
//
// The four values are read from `netbox/netbox/choices.py:166-181` (`DistanceUnitChoices`) in
// the NetBox 4.6.8 tree. Two metric and two imperial; NetBox normalises whatever is given into
// the read-only `_abs_distance` column in metres, which is why that column is never written
// (netbox/netbox/models/mixins.py:92-117).
//
// +kubebuilder:validation:Enum=km;m;mi;ft
type DistanceUnit string

const (
	// DistanceUnitKilometer is kilometres.
	DistanceUnitKilometer DistanceUnit = "km"

	// DistanceUnitMeter is metres.
	DistanceUnitMeter DistanceUnit = "m"

	// DistanceUnitMile is miles.
	DistanceUnitMile DistanceUnit = "mi"

	// DistanceUnitFoot is feet.
	DistanceUnitFoot DistanceUnit = "ft"
)

// NetBoxWirelessLinkSpec describes one wireless.WirelessLink -- a point-to-point connection
// between two wireless interfaces, which NetBox models as an object of its own rather than as
// a field on either interface.
//
// **The identity is the *ordered* pair.** The single constraint is
// `UniqueConstraint(fields=('interface_a', 'interface_b'), name='%(app_label)s_%(class)s_unique_interfaces')`
// (netbox/wireless/models.py:190-195), and there is nothing symmetric about it: Postgres will
// store `(a,b)` and `(b,a)` as two distinct rows, and `WirelessLink.clean` checks only that
// both interfaces are of a wireless *type* -- it says nothing about the reverse pair already
// existing (netbox/wireless/models.py:205-220). So a link from A to B and a link from B to A
// are two objects to NetBox and one physical link to everybody else.
//
// The operator resolves that with **two natural-key candidates, the declared orientation and
// its reverse** (internal/registry/wireless_wirelesslink.go). The reverse candidate is what
// stops a duplicate: a second CR declaring `(b,a)` finds the row the first CR created rather
// than concluding no such link exists, so it never POSTs a second row for one radio path. What
// it does instead is the ordinary adoption rule -- `Conflict` with nothing written under the
// default `onConflict: Fail`, one orientation-normalising `PATCH` under `Adopt`. One physical
// link, one NetBox row, and the second CR says why it is not Ready.
//
// Both endpoints are plain foreign keys, unlike `dcim.Cable`'s terminations, so changing one
// is an ordinary PATCH and not a recreate -- `UpdateStrategy` stays `Patch`. NetBox
// recomputes `_interface_a_device` and `_interface_b_device` on save
// (netbox/wireless/models.py:222-227); both are in `ReadOnly` and neither is ever written.
//
// **Both halves of the natural key are references, which is what makes an unresolved one
// block everything.** `interfaceARef` and `interfaceBRef` resolve against NetBoxInterface
// (NBO-030), and while either is unresolved neither candidate is applicable, so the engine
// performs no lookup and writes nothing -- it does not create a link with an endpoint it could
// not name. The object reports `RefsResolved=False` naming the field
// (docs/concepts/references.md).
//
// `authPSK` is absent by design; see api/v1alpha1/wireless_auth.go for why.
type NetBoxWirelessLinkSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// InterfaceARef is the A-side wireless interface
	// (`interface_a ForeignKey -> dcim.Interface on_delete=PROTECT`,
	// netbox/wireless/models.py:138-143).
	//
	// Required, and half of the natural key. `PROTECT`, so this reference blocks deletion of
	// the interface in NetBox -- and therefore of its device, except that
	// `_interface_a_device` is `on_delete=CASCADE` (netbox/wireless/models.py:171-177), which
	// is exactly what makes a device deletion collect the link first instead of hitting the
	// PROTECT. That cascade is on a read-only cache column with no spec field behind it, so
	// it cannot be a containment reference; see the registry file for what follows.
	InterfaceARef InterfaceRef `json:"interfaceARef"`

	// InterfaceBRef is the B-side wireless interface
	// (`interface_b ForeignKey -> dcim.Interface on_delete=PROTECT`,
	// netbox/wireless/models.py:144-149).
	//
	// Required, and the other half of the natural key. Which interface is A and which is B is
	// NetBox's ordering rather than a physical fact, which is why the lookup also tries the
	// reverse pair -- see the type comment.
	InterfaceBRef InterfaceRef `json:"interfaceBRef"`

	// SSID is the network name carried over the link (netbox/wireless/models.py:150-154).
	//
	// Optional here where it is required on a NetBoxWirelessLAN: the column is `blank=True`,
	// and a backhaul link commonly has no SSID at all.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=32
	// +optional
	SSID string `json:"ssid,omitempty"`

	// Status is the link's lifecycle state: connected, planned or decommissioning.
	//
	// Defaulted to NetBox's own default (netbox/wireless/models.py:155-160) so the operator
	// manages the field from the first reconcile: a defaulted field that never reaches a
	// payload is a field the operator can never correct.
	// +kubebuilder:default=connected
	// +optional
	Status LinkStatus `json:"status,omitempty"`

	// TenantRef assigns the link to a tenant
	// (`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`,
	// netbox/wireless/models.py:161-167).
	//
	// Not part of the natural key, unlike a NetBoxWirelessLAN's: this kind *has* a real
	// uniqueness constraint and the interface pair is the whole of it, so there is nothing
	// for a tenant term to disambiguate.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Distance is the span between the two endpoints, as a string in the unit DistanceUnit
	// names.
	//
	// A string and not a float64, for the reason dcim.Site.latitude is one: NetBox stores it
	// as `DecimalField(max_digits=8, decimal_places=2)`
	// (netbox/netbox/models/mixins.py:78-84) and returns it as a string, and an OpenAPI
	// `number` round-trips through IEEE-754 on its way in and out of the API server. The
	// engine compares it numerically (internal/netbox/drift.go, scalarEqual), so `"1.5"` and
	// NetBox's `"1.50"` are the same value and produce no PATCH.
	//
	// The pattern caps the fraction at two digits and the integer part at six, which is
	// `decimal(8,2)` written out. It admits no sign: a negative distance is not a value NetBox
	// rejects, it is a value NetBox would happily normalise into a negative `_abs_distance`
	// and sort by, so the bound is the operator's and is stated here rather than discovered.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. A cleared distance is written as `null` rather than as an empty string, which
	// is what NetBox's nullable DecimalField takes -- see registry.Field.EmptyIsNull. NetBox
	// clears `distance_unit` by itself whenever `distance` is null
	// (netbox/netbox/models/mixins.py:115-117).
	// +kubebuilder:validation:Pattern=`^$|^[0-9]{1,6}(\.[0-9]{1,2})?$`
	// +optional
	Distance string `json:"distance,omitempty"`

	// DistanceUnit is the unit Distance is expressed in: km, m, mi or ft
	// (netbox/netbox/models/mixins.py:85-91).
	//
	// Meaningless without Distance, and NetBox enforces that from its side by nulling the
	// unit on save whenever the distance is null. Undefaulted: the column is nullable with no
	// Django default, and there is no unit that is right by default.
	// +optional
	DistanceUnit DistanceUnit `json:"distanceUnit,omitempty"`

	// AuthType is the authentication method: open, wep, wpa-personal or wpa-enterprise
	// (netbox/wireless/models.py:25-31, via WirelessAuthenticationBase).
	// +optional
	AuthType WirelessAuthType `json:"authType,omitempty"`

	// AuthCipher is the encryption cipher: auto, tkip or aes
	// (netbox/wireless/models.py:32-38, via WirelessAuthenticationBase).
	// +optional
	AuthCipher WirelessAuthCipher `json:"authCipher,omitempty"`

	// Description is free text shown next to the link
	// (`description (PrimaryModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the link's long-form notes field, inherited from PrimaryModel. A TextField
	// rather than a CharField: it has no max_length, so there is no MaxLength marker to
	// derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxWirelessLink is one wireless.WirelessLink in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbwlink
// +kubebuilder:printcolumn:name="SSID",type=string,JSONPath=`.spec.ssid`
// +kubebuilder:printcolumn:name="A",type=string,JSONPath=`.spec.interfaceARef.name`
// +kubebuilder:printcolumn:name="B",type=string,JSONPath=`.spec.interfaceBRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxWirelessLink struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxWirelessLinkSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus     `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (l *NetBoxWirelessLink) NetBoxSpec() *NetBoxObjectSpec { return &l.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (l *NetBoxWirelessLink) NetBoxStatus() *NetBoxObjectStatus { return &l.Status }

// NetBoxWirelessLinkList is a list of NetBoxWirelessLink.
// +kubebuilder:object:root=true
type NetBoxWirelessLinkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxWirelessLink `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxWirelessLink{}, &NetBoxWirelessLinkList{})
}
