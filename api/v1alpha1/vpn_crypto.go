package v1alpha1

// The three ChoiceSets the `vpn` crypto catalogue shares between two models each, declared
// once here rather than twice per kind.
//
// One Go type per NetBox ChoiceSet, and that is the rule rather than a convenience. Sharing
// an enum between two *different* ChoiceSets is the mistake dcim_location.go and
// virtualization_cluster.go call out -- a value added to one of them through `FIELD_CHOICES`
// would silently widen the other. The inverse is just as wrong: splitting one ChoiceSet into
// two Go types lets the two copies drift, and a member NetBox accepts on one kind would be
// rejected on the other for no reason a user could act on. `EncryptionAlgorithmChoices`,
// `AuthenticationAlgorithmChoices` and `DHGroupChoices` are each a single class in
// `netbox/vpn/choices.py`, used by two models apiece, so each is a single type here.

// EncryptionAlgorithm is one value of NetBox's EncryptionAlgorithmChoices: the symmetric
// cipher a proposal offers.
//
// Two users, both in `vpn`: `vpn.IKEProposal.encryption_algorithm` and
// `vpn.IPSecProposal.encryption_algorithm` (docs/netbox-schema.md -> vpn.IKEProposal,
// vpn.IPSecProposal). The eight members are read from `netbox/vpn/choices.py:117`
// (`EncryptionAlgorithmChoices`) in the same 4.6.8 tree the digest was taken from.
//
// A **closed** enum is safe here, and that is a fact about the source rather than an
// assumption: a ChoiceSet is extensible through a deployment's `FIELD_CHOICES` only when it
// declares a `key` (`netbox/utilities/choices.py:23-35`), and this one declares none
// (hack/testdata/ir-4.6.8.json.gz -> enums.EncryptionAlgorithmChoices, `"key": null`).
//
// The empty string is a member because one of the two columns is `blank=True, null=True`:
// `vpn.IPSecProposal.encryption_algorithm` is optional and clearing it is a legitimate
// intent. The other column is `REQ`, and NetBoxIKEProposal's field carries its own
// `MinLength=1` so the empty member cannot be written there.
//
// +kubebuilder:validation:Enum="";"aes-128-cbc";"aes-128-gcm";"aes-192-cbc";"aes-192-gcm";"aes-256-cbc";"aes-256-gcm";"3des-cbc";"des-cbc"
type EncryptionAlgorithm string

const (
	// EncryptionAlgorithmAES128CBC is 128-bit AES in CBC mode.
	EncryptionAlgorithmAES128CBC EncryptionAlgorithm = "aes-128-cbc"

	// EncryptionAlgorithmAES128GCM is 128-bit AES in GCM mode.
	EncryptionAlgorithmAES128GCM EncryptionAlgorithm = "aes-128-gcm"

	// EncryptionAlgorithmAES192CBC is 192-bit AES in CBC mode.
	EncryptionAlgorithmAES192CBC EncryptionAlgorithm = "aes-192-cbc"

	// EncryptionAlgorithmAES192GCM is 192-bit AES in GCM mode.
	EncryptionAlgorithmAES192GCM EncryptionAlgorithm = "aes-192-gcm"

	// EncryptionAlgorithmAES256CBC is 256-bit AES in CBC mode.
	EncryptionAlgorithmAES256CBC EncryptionAlgorithm = "aes-256-cbc"

	// EncryptionAlgorithmAES256GCM is 256-bit AES in GCM mode.
	EncryptionAlgorithmAES256GCM EncryptionAlgorithm = "aes-256-gcm"

	// EncryptionAlgorithm3DESCBC is 3DES in CBC mode.
	EncryptionAlgorithm3DESCBC EncryptionAlgorithm = "3des-cbc"

	// EncryptionAlgorithmDESCBC is single DES in CBC mode.
	EncryptionAlgorithmDESCBC EncryptionAlgorithm = "des-cbc"
)

// AuthenticationAlgorithm is one value of NetBox's AuthenticationAlgorithmChoices: the HMAC
// a proposal offers for integrity.
//
// Two users, both in `vpn`: `vpn.IKEProposal.authentication_algorithm` and
// `vpn.IPSecProposal.authentication_algorithm` (docs/netbox-schema.md -> vpn.IKEProposal,
// vpn.IPSecProposal). The five members are `netbox/vpn/choices.py:139` at 4.6.8.
//
// Closed, for the same reason EncryptionAlgorithm is: the class declares no `key`
// (hack/testdata/ir-4.6.8.json.gz -> enums.AuthenticationAlgorithmChoices).
//
// The empty string is a member because *both* columns are `blank=True, null=True`: neither
// model requires an integrity algorithm, and an AEAD cipher such as `aes-256-gcm` supplies
// its own.
//
// +kubebuilder:validation:Enum="";"hmac-sha1";"hmac-sha256";"hmac-sha384";"hmac-sha512";"hmac-md5"
type AuthenticationAlgorithm string

const (
	// AuthenticationAlgorithmHMACSHA1 is SHA-1 HMAC.
	AuthenticationAlgorithmHMACSHA1 AuthenticationAlgorithm = "hmac-sha1"

	// AuthenticationAlgorithmHMACSHA256 is SHA-256 HMAC.
	AuthenticationAlgorithmHMACSHA256 AuthenticationAlgorithm = "hmac-sha256"

	// AuthenticationAlgorithmHMACSHA384 is SHA-384 HMAC.
	AuthenticationAlgorithmHMACSHA384 AuthenticationAlgorithm = "hmac-sha384"

	// AuthenticationAlgorithmHMACSHA512 is SHA-512 HMAC.
	AuthenticationAlgorithmHMACSHA512 AuthenticationAlgorithm = "hmac-sha512"

	// AuthenticationAlgorithmHMACMD5 is MD5 HMAC.
	AuthenticationAlgorithmHMACMD5 AuthenticationAlgorithm = "hmac-md5"
)

// DHGroup is one value of NetBox's DHGroupChoices: a Diffie-Hellman group number.
//
// An integer rather than a string, and that is the column rather than a preference: both
// users are `PositiveSmallIntegerField` (docs/netbox-schema.md -> vpn.IKEProposal `group
// PositiveSmallIntegerField REQ choices=DHGroupChoices`, vpn.IPSecPolicy `pfs_group
// PositiveSmallIntegerField choices=DHGroupChoices`), so NetBox stores and returns a number
// and the operator compares a number. The RackWidth derivation.
//
// The 24 members are `netbox/vpn/choices.py:155` at 4.6.8: 1, 2, 5 and then 14 through 34.
// Not a range -- 3, 4 and 6 through 13 are absent, so a `Minimum`/`Maximum` pair would accept
// group numbers NetBox rejects. The class declares no `key`, so the set is closed
// (hack/testdata/ir-4.6.8.json.gz -> enums.DHGroupChoices).
//
// No Go constants: nothing in Go or in the tests names a DH group, and 24 constants would be
// 24 doc comments restating the integer they hold. The enum marker is what enforces the set,
// exactly as for RackWidth.
//
// Nullability is the pointer's job rather than a member's: `pfs_group` is nullable, so
// NetBoxIPSecPolicy holds a `*DHGroup`, while `group` is `REQ` and NetBoxIKEProposal holds
// the bare type.
//
// +kubebuilder:validation:Enum=1;2;5;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34
type DHGroup int32
