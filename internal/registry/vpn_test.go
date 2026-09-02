package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// vpnKinds are the eight Kinds #59's first half ships, with the two facts a descriptor cannot
// derive: the endpoint, which is looked up in docs/netbox-schema.md's endpoint map and never
// pluralised from the model name (`vpn/ike-policies`, not `vpn/ikepolicys`), and the object
// type NetBox stamps a generic foreign key with.
var vpnKinds = []struct {
	kind       string
	endpoint   string
	objectType string
}{
	{"NetBoxIKEProposal", "vpn/ike-proposals", "vpn.ikeproposal"},
	{"NetBoxIKEPolicy", "vpn/ike-policies", "vpn.ikepolicy"},
	{"NetBoxIPSecProposal", "vpn/ipsec-proposals", "vpn.ipsecproposal"},
	{"NetBoxIPSecPolicy", "vpn/ipsec-policies", "vpn.ipsecpolicy"},
	{"NetBoxIPSecProfile", "vpn/ipsec-profiles", "vpn.ipsecprofile"},
	{"NetBoxTunnelGroup", "vpn/tunnel-groups", "vpn.tunnelgroup"},
	{"NetBoxTunnel", "vpn/tunnels", "vpn.tunnel"},
	{"NetBoxL2VPN", "vpn/l2vpns", "vpn.l2vpn"},
}

// TestVPNDescriptorsAreRegisteredAndValid is the boot check for #59's eight kinds.
func TestVPNDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range vpnKinds {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// Seven PrimaryModels and one OrganizationalModel (docs/netbox-schema.md, bases),
			// and both mix in TagsMixin and CustomFieldsMixin, so all eight are stamped in
			// full.
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}

			// Crypto parameters and tunnels are configuration a manifest recreates, not
			// allocated state: nothing here frees a resource when it is deleted, which is what
			// #176 reserved Retain for.
			if d.RetainOnDelete {
				t.Errorf("RetainOnDelete = true; a vpn catalogue object is configuration a " +
					"manifest recreates (#176, docs/concepts/deletion.md)")
			}

			// Every foreign key in the app bar the two termination models' parents is PROTECT
			// (docs/netbox-schema.md -> vpn.*), so no kind here has a containment parent: an
			// owner reference on a PROTECTed key promises a cascade NetBox refuses to perform,
			// which would delete the CR and leave the row
			// (docs/decisions/0003-ownership-and-references.md rule 4).
			if d.ContainmentRef != "" {
				t.Errorf("ContainmentRef = %q, want empty: every FK on this model is PROTECT",
					d.ContainmentRef)
			}
		})
	}
}

// TestVPNNaturalKeysComeFromTheSchema is the central claim of the change, and the eight kinds
// derive their identity in three different ways over one app:
//
//   - The five crypto kinds declare **no** meta.constraints at all
//     (hack/testdata/ir-4.6.8.json.gz -> vpn.*.natural_keys, `[]`), so the key is the one
//     column carrying UNIQUE: `name`. The ipam.RouteTarget derivation.
//   - vpn.TunnelGroup and vpn.L2VPN declare none either, and carry UNIQUE on *two* columns;
//     `slug` is the candidate and `name` is deliberately not a fallback.
//   - vpn.Tunnel is the only one with meta.constraints, and it has two of them: the pair and
//     the null-pinned conditional variant.
func TestVPNNaturalKeysComeFromTheSchema(t *testing.T) {
	name := []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}}
	slug := []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}}

	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"an IKE proposal is keyed on its column-unique name alone": {
			kind: "NetBoxIKEProposal", want: name,
		},
		"an IKE policy is too":     {kind: "NetBoxIKEPolicy", want: name},
		"an IPSec proposal is too": {kind: "NetBoxIPSecProposal", want: name},
		"an IPSec policy is too":   {kind: "NetBoxIPSecPolicy", want: name},
		"an IPSec profile is keyed on name and not on its two policies": {
			kind: "NetBoxIPSecProfile", want: name,
		},
		"a tunnel group is keyed on slug, because it is an OrganizationalModel": {
			kind: "NetBoxTunnelGroup", want: slug,
		},
		"an L2VPN is keyed on slug, with no name fallback": {
			kind: "NetBoxL2VPN", want: slug,
		},
		"a tunnel pins group_id when it names no group": {
			kind: "NetBoxTunnel",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "group_id", Spec: "groupRef"},
					{Filter: "name", Spec: "name"},
				}},
				{
					Fields: []KeyField{{Filter: "name", Spec: "name"}},
					NullFields: []NullField{
						{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !reflect.DeepEqual(d.NaturalKeys, tc.want) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.want)
			}
		})
	}
}

// TestTunnelCandidatesByState is the acceptance criterion #59 states as "a groupless
// NetBoxTunnel is looked up with `group_id=null` and adopted, not duplicated", plus the row
// that criterion does not mention and that matters more.
//
// The `want: nil` row is that one. A tunnel whose `groupRef` names a NetBoxTunnelGroup that
// has not reconciled yet must **not** fall through to the null-pinned variant: that candidate
// would find a groupless tunnel of the same name and adopt it, and the follow-up PATCH would
// move somebody else's tunnel into this group. With nothing applicable the engine waits, which
// is the correct outcome (NBO-015).
func TestTunnelCandidatesByState(t *testing.T) {
	tests := map[string]struct {
		state SpecState
		want  [][]string
	}{
		"a tunnel in a group is keyed on the constraint": {
			state: SpecState{
				Declared: []string{"name", "groupRef"},
				Resolved: []string{"name", "groupRef"},
			},
			want: [][]string{{"group_id", "name"}},
		},
		"a groupless tunnel pins group_id to null": {
			state: SpecState{Declared: []string{"name"}, Resolved: []string{"name"}},
			want:  [][]string{{"name", "group_id=null"}},
		},
		"a tunnel whose group has not been created yet has no candidate": {
			state: SpecState{
				Declared: []string{"name", "groupRef"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTunnel"))

			var got [][]string
			for _, key := range d.Candidates(tc.state) {
				got = append(got, params(key))
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Candidates(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestNoDescriptorMapsASecretColumn is the rule #59 exists to keep, asserted over the *whole*
// registry rather than over the kind that prompted it.
//
// A secret may never be inline in a spec. The only permitted shape is a SecretRef, and the
// engine has no FieldClass that reads a Secret into a payload -- that is #241 -- so every
// secret-valued column NetBox exposes is left unmapped until the mechanism exists. Three
// columns are in that position today: `vpn.IKEPolicy.preshared_key` (this change),
// `ipam.FHRPGroup.auth_key` and `wireless.WirelessLAN.auth_psk` /
// `wireless.WirelessLink.auth_psk`, each recorded in hack/coverage-exclusions.yaml's `notes`
// as a gap rather than an excuse.
//
// The walk is over every registered descriptor on purpose: the failure this guards against is
// a future kind mapping one of these columns because it looked like an ordinary string, and a
// test that only checked NetBoxIKEPolicy would not see it. The names are
// internal/netbox/do.go's `secretFields` -- the same list that masks these values in every log
// line -- restated here because neither package imports the other.
func TestNoDescriptorMapsASecretColumn(t *testing.T) {
	secretColumns := []string{"preshared_key", "auth_key", "auth_psk", "psk", "password"}

	descriptors := List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	for _, d := range descriptors {
		for _, field := range d.Fields {
			if slices.Contains(secretColumns, field.API) {
				t.Errorf("%s maps spec.%s onto the secret-valued column %q. A secret may never "+
					"be inline in a spec: the only permitted shape is a SecretRef, and the "+
					"engine has no FieldClass that reads a Secret into a payload (#241). "+
					"Leave the column unmapped and record it in hack/coverage-exclusions.yaml, "+
					"as ipam.FHRPGroup.auth_key and vpn.IKEPolicy.preshared_key are",
					d.GVK.Kind, field.Spec, field.API)
			}
		}
	}
}

// TestIKEPolicyLeavesThePresharedKeyAlone is the same rule from the other side: not "no field
// maps it" but "this kind's field map is otherwise complete", so the omission cannot be read
// as an oversight that also lost three other columns.
//
// vpn.IKEPolicy's writable columns are `name`, `version`, `mode`, `proposals`,
// `preshared_key`, `description`, `comments` and `owner`
// (hack/testdata/ir-4.6.8.json.gz -> vpn.IKEPolicy.write_path, less the envelope). Six of the
// eight are mapped; `owner` is a ForeignKey to users.Owner, which has no Kind and so cannot be
// resolved at all, and `preshared_key` is the secret.
func TestIKEPolicyLeavesThePresharedKeyAlone(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxIKEPolicy"))
	if !ok {
		t.Fatal("NetBoxIKEPolicy is not registered")
	}

	want := []string{"name", "version", "mode", "proposals", "description", "comments"}

	got := make([]string, 0, len(d.Fields))
	for _, field := range d.Fields {
		got = append(got, field.API)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapped columns = %v, want %v", got, want)
	}
}

// TestVPNToManyRelationsAreDeclaredOnce checks the three many-to-many relations this change
// carries are RefMany and point at the right Kind, because the direction of an M2M is the
// easiest thing about it to get backwards.
//
// `proposals` is declared on the two policy kinds and not on the proposal kinds -- NetBox
// declares the ManyToManyField on `IKEPolicy` and `IPSecPolicy` (docs/netbox-schema.md), so
// the write goes through the policy and a proposal has no to-many field at all. The
// ipam.VRF / ipam.RouteTarget pairing, twice over, plus L2VPN's two route-target relations,
// which are literally the ipam.VRF ones.
func TestVPNToManyRelationsAreDeclaredOnce(t *testing.T) {
	tests := map[string]struct {
		kind  string
		field string
		want  string
	}{
		"an IKE policy owns its proposals relation": {
			kind: "NetBoxIKEPolicy", field: "proposals", want: "NetBoxIKEProposal",
		},
		"an IPSec policy owns its proposals relation": {
			kind: "NetBoxIPSecPolicy", field: "proposals", want: "NetBoxIPSecProposal",
		},
		"an L2VPN owns its import targets": {
			kind: "NetBoxL2VPN", field: "import_targets", want: "NetBoxRouteTarget",
		},
		"an L2VPN owns its export targets": {
			kind: "NetBoxL2VPN", field: "export_targets", want: "NetBoxRouteTarget",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !slices.Contains(d.M2MFields(), tc.field) {
				t.Fatalf("M2MFields() = %v, want it to contain %q", d.M2MFields(), tc.field)
			}

			for _, field := range d.Fields {
				if field.API != tc.field {
					continue
				}

				if field.Class != ClassRefMany {
					t.Errorf("%s.%s Class = %q, want %q", tc.kind, tc.field, field.Class, ClassRefMany)
				}

				if field.Target.Kind != tc.want {
					t.Errorf("%s.%s Target = %q, want %q", tc.kind, tc.field, field.Target.Kind, tc.want)
				}
			}
		})
	}

	// The other half of the claim: neither proposal kind declares a to-many relation of its
	// own. A second writer for one relation is how two objects end up fighting over it.
	for _, kind := range []string{"NetBoxIKEProposal", "NetBoxIPSecProposal"} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
		if fields := d.M2MFields(); len(fields) != 0 {
			t.Errorf("%s M2MFields() = %v, want none: the relation is written from the policy",
				kind, fields)
		}
	}
}

// TestVPNNullableChoiceColumnsAreClearedWithNull is the #170 check.
//
// NetBox returns `null` rather than `""` for an unset choice, so an emptied enum sent as `""`
// differs from the value read back on every pass and the operator PATCHes forever. Every
// nullable choice column in this change therefore carries EmptyIsNull, and every non-nullable
// one must not: the flag on a `REQ` column would turn a legitimately empty string into a null
// NetBox rejects.
func TestVPNNullableChoiceColumnsAreClearedWithNull(t *testing.T) {
	nullable := map[string][]string{
		// `authentication_algorithm` is `blank=True, null=True`; the other three enums on
		// these two kinds are REQ (docs/netbox-schema.md -> vpn.IKEProposal).
		"NetBoxIKEProposal": {"authentication_algorithm"},
		// `mode` is IKEv1-only and nullable; `version` has a Django default and is not.
		"NetBoxIKEPolicy": {"mode"},
		// Both algorithm columns are nullable here, which is the one way this model differs
		// from vpn.IKEProposal.
		"NetBoxIPSecProposal": {"encryption_algorithm", "authentication_algorithm"},
		// vpn.IPSecPolicy's `pfs_group` is nullable and an *integer*: its spec field is a
		// pointer, so there is no empty string to translate.
		"NetBoxIPSecPolicy":  {},
		"NetBoxIPSecProfile": {},
		"NetBoxTunnelGroup":  {},
		// `status` and `encapsulation` are both non-nullable.
		"NetBoxTunnel": {},
		// `type` and `status` are both non-nullable.
		"NetBoxL2VPN": {},
	}

	for kind, want := range nullable {
		t.Run(kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

			got := make([]string, 0, len(want))

			for _, field := range d.Fields {
				if field.EmptyIsNull {
					got = append(got, field.API)
				}
			}

			if !slices.Equal(got, want) {
				t.Errorf("columns carrying EmptyIsNull = %v, want %v", got, want)
			}
		})
	}
}
