package v1alpha1

// NetBox's `wireless.WirelessAuthenticationBase` is an abstract model carrying three columns
// -- `auth_type`, `auth_cipher` and `auth_psk` -- onto both `wireless.WirelessLAN` and
// `wireless.WirelessLink` (netbox/wireless/models.py:21-46). The two enums are declared here
// rather than on either kind for the same reason `registry.ScopeFK` is one function: the
// second kind to use them must restate nothing, or the two copies drift and the losing one is
// invisible.
//
// **`auth_psk` is deliberately not a field on either kind.** It is a pre-shared key, so it may
// never be inline in a spec (NBO-050): the required shape is
// `authPSKSecretRef` -> a key of a Secret. That needs the engine to source one payload value
// from a Secret rather than from the spec, which is a new `registry.FieldClass` plus a Secret
// read in the payload path -- shared machinery, not descriptor data, and so out of scope for a
// kind that is meant to cost nothing but data. Omitting the field is the safe half of the
// deferral: a spec omission means "do not manage this column"
// (docs/concepts/field-ownership.md), so NetBox keeps whatever PSK it holds and the operator
// neither reads nor writes it. The log half is already built and needs nothing: `auth_psk` is
// in `internal/netbox/do.go`'s `secretFields`, so it is masked in every request and response
// line at every level.

// WirelessAuthType is one value of NetBox's WirelessAuthTypeChoices.
//
// The four values are read from `netbox/wireless/choices.py:460-472` (`WirelessAuthTypeChoices`)
// in the NetBox 4.6.8 tree, because the schema digest records the choice *class* and not its
// members. Note that the wire value is `wpa-personal` while the label NetBox renders is
// "WPA Personal (PSK)" -- the operator sends and compares the value, never the label
// (docs/concepts/drift.md).
//
// +kubebuilder:validation:Enum=open;wep;wpa-personal;wpa-enterprise
type WirelessAuthType string

const (
	// WirelessAuthTypeOpen is an unauthenticated network.
	WirelessAuthTypeOpen WirelessAuthType = "open"

	// WirelessAuthTypeWEP is WEP, which is broken and named here only because NetBox
	// models it.
	WirelessAuthTypeWEP WirelessAuthType = "wep"

	// WirelessAuthTypeWPAPersonal is WPA with a pre-shared key.
	WirelessAuthTypeWPAPersonal WirelessAuthType = "wpa-personal"

	// WirelessAuthTypeWPAEnterprise is WPA with 802.1X.
	WirelessAuthTypeWPAEnterprise WirelessAuthType = "wpa-enterprise"
)

// WirelessAuthCipher is one value of NetBox's WirelessAuthCipherChoices.
//
// The three values are read from `netbox/wireless/choices.py:474-483`
// (`WirelessAuthCipherChoices`) in the NetBox 4.6.8 tree.
//
// +kubebuilder:validation:Enum=auto;tkip;aes
type WirelessAuthCipher string

const (
	// WirelessAuthCipherAuto lets the equipment negotiate the cipher.
	WirelessAuthCipherAuto WirelessAuthCipher = "auto"

	// WirelessAuthCipherTKIP is TKIP.
	WirelessAuthCipherTKIP WirelessAuthCipher = "tkip"

	// WirelessAuthCipherAES is AES/CCMP.
	WirelessAuthCipherAES WirelessAuthCipher = "aes"
)
