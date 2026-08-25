package registry

import (
	"errors"
	"strings"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestRegisteredClaimsValidate is the boot check as a unit test.
//
// registry.Validate covers the claim registry too, so a malformed claim descriptor already
// fails the manager's boot. This is the same assertion without a manager, so it fails in
// `go test` first -- and it runs against the *shipped* descriptors rather than a fixture.
func TestRegisteredClaimsValidate(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("the shipped registry does not validate: %v", err)
	}

	claims := Claims()
	if len(claims) == 0 {
		t.Fatal("no claim kinds are registered; NBO-036 registers NetBoxIPAddressClaim")
	}

	for _, claim := range claims {
		if _, ok := Get(claim.Pool.Target); !ok {
			t.Errorf("%s allocates out of %s, which has no Descriptor to take an endpoint from",
				claim.GVK.Kind, claim.Pool.Target)
		}
	}
}

// TestClaimObjectTypesReachTheStamp is the assertion behind the one thing about a claim that
// is easy to get wrong and impossible to notice.
//
// A claim is a Kubernetes object with no NetBox counterpart, and it writes custom fields onto
// the model it allocates. If that model's `app_label.model` string never reaches
// extras.CustomField.object_types, the first allocating POST against a fresh NetBox is a 400
// naming a field the user can see perfectly well in the UI -- for every claim in the cluster.
func TestClaimObjectTypesReachTheStamp(t *testing.T) {
	types := ClaimObjectTypes()

	for _, claim := range Claims() {
		if !contains(types, claim.ObjectType) {
			t.Errorf("%s allocates %s, which is not in ClaimObjectTypes() = %v",
				claim.GVK.Kind, claim.ObjectType, types)
		}
	}

	if !sorted(types) {
		t.Errorf("ClaimObjectTypes() = %v, want it sorted: it drives the bootstrap's request"+
			" order and a condition message humans compare", types)
	}
}

// TestIPAddressClaimDescriptor pins the facts the allocation engine reads off the shipped
// descriptor, because every one of them is a silent failure when wrong: a wrong sub-path is a
// 404, a wrong result field is an empty status.address, and a missing refusal is an address
// allocated out of a container.
func TestIPAddressClaimDescriptor(t *testing.T) {
	desc, ok := Claim(netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddressClaim"))
	if !ok {
		t.Fatal("NetBoxIPAddressClaim is not registered")
	}

	if desc.Endpoint != "ipam/ip-addresses" || desc.ObjectType != "ipam.ipaddress" {
		t.Errorf("endpoint/objectType = %s/%s, want ipam/ip-addresses and ipam.ipaddress",
			desc.Endpoint, desc.ObjectType)
	}

	if desc.PoolSubPath != "available-ips" || desc.ResultField != "address" {
		t.Errorf("subPath/resultField = %s/%s, want available-ips and address",
			desc.PoolSubPath, desc.ResultField)
	}

	if desc.Pool.Spec != "prefixRef" || desc.Pool.Target.Kind != "NetBoxPrefix" {
		t.Errorf("pool = %s -> %s, want prefixRef -> NetBoxPrefix", desc.Pool.Spec, desc.Pool.Target.Kind)
	}

	if !contains(desc.PoolMustNotBeTrue, "mark_utilized") {
		t.Error("mark_utilized is not refused; available-ips would still hand out an address")
	}

	if !contains(desc.PoolForbiddenStatus, "container") {
		t.Error("status: container is not refused; a container's space is subdivided by child" +
			" prefixes rather than populated by addresses")
	}

	if !desc.CustomFieldable {
		t.Error("customFieldable is false, so there would be nowhere to store the allocation identity")
	}
}

// TestClaimDescriptorValidate covers each way a claim descriptor can be malformed, because
// each of them is a state the engine cannot report anything useful about at runtime.
func TestClaimDescriptorValidate(t *testing.T) {
	cases := map[string]struct {
		mutate func(*ClaimDescriptor)
		want   error
	}{
		"valid":           {mutate: func(*ClaimDescriptor) {}, want: nil},
		"no endpoint":     {mutate: func(c *ClaimDescriptor) { c.Endpoint = "" }, want: ErrIncompleteClaim},
		"no result field": {mutate: func(c *ClaimDescriptor) { c.ResultField = "" }, want: ErrIncompleteClaim},
		"no pool value":   {mutate: func(c *ClaimDescriptor) { c.PoolValueField = "" }, want: ErrIncompleteClaim},
		"no pool target": {
			mutate: func(c *ClaimDescriptor) { c.Pool.Target = netboxv1alpha1.GroupVersion.WithKind("") },
			want:   ErrIncompleteClaim,
		},
		"to-many pool": {
			mutate: func(c *ClaimDescriptor) { c.Pool.Class = ClassRefMany },
			want:   ErrIncompleteClaim,
		},
		"not custom-fieldable": {
			mutate: func(c *ClaimDescriptor) { c.CustomFieldable = false },
			want:   ErrIncompleteClaim,
		},
		"empty gvk": {
			mutate: func(c *ClaimDescriptor) { c.GVK = netboxv1alpha1.GroupVersion.WithKind("") },
			want:   ErrEmptyGVK,
		},
		"bad object type": {
			mutate: func(c *ClaimDescriptor) { c.ObjectType = "Ipam.IPAddress" },
			want:   ErrInvalidObjectType,
		},
		"unknown sub-path": {
			mutate: func(c *ClaimDescriptor) { c.PoolSubPath = "available-everything" },
			want:   ErrUnknownPoolSubPath,
		},
		"the unlocked placement path is known": {
			mutate: func(c *ClaimDescriptor) { c.PoolSubPath = "place-ip-range" },
			want:   nil,
		},
		"request field with no api name": {
			mutate: func(c *ClaimDescriptor) {
				c.RequestFields = []Field{{Spec: "prefixLength"}}
			},
			want: ErrIncompleteClaim,
		},
		"request field that is a reference": {
			mutate: func(c *ClaimDescriptor) {
				c.RequestFields = []Field{{
					Spec: "vrfRef", API: "vrf", Class: ClassRefOne,
					Target: netboxv1alpha1.VRFRef{}.TargetGVK(),
				}}
			},
			want: ErrIncompleteClaim,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			desc := validClaim()
			tc.mutate(&desc)

			err := desc.Validate()

			if tc.want == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}

			// A boot failure has to name the descriptor a human has to edit.
			if !strings.Contains(err.Error(), "claim descriptor") {
				t.Errorf("error %q does not say which descriptor is wrong", err)
			}
		})
	}
}

// TestDuplicateClaimRegistrationIsRejected keeps two files from claiming one Kind, which
// would make which of them wins depend on filename order.
func TestDuplicateClaimRegistrationIsRejected(t *testing.T) {
	registry := newClaims()

	if err := registry.add(validClaim()); err != nil {
		t.Fatalf("first registration: %v", err)
	}

	if err := registry.add(validClaim()); !errors.Is(err, ErrDuplicateClaimGVK) {
		t.Fatalf("second registration = %v, want ErrDuplicateClaimGVK", err)
	}

	// And it is reported again at boot, because a registration error is easy to drop in an
	// init().
	if err := registry.validate(New()); !errors.Is(err, ErrDuplicateClaimGVK) {
		t.Errorf("validate() = %v, want the duplicate reported", err)
	}
}

// TestClaimValidationNeedsItsPoolKind is the check that cannot live on the descriptor: it
// needs the pool's own Descriptor, which is what keeps the pool's REST path written down once.
func TestClaimValidationNeedsItsPoolKind(t *testing.T) {
	registry := newClaims()
	if err := registry.add(validClaim()); err != nil {
		t.Fatalf("registering: %v", err)
	}

	if err := registry.validate(New()); !errors.Is(err, ErrUnknownPoolKind) {
		t.Fatalf("validate() with no pool descriptor = %v, want ErrUnknownPoolKind", err)
	}
}

// TestRefDescriptorCarriesOnlyThePool pins the adapter the pool watch and the reference index
// are built on: those two read a Descriptor's GVK and its ref fields, and a claim has nothing
// else to give them.
func TestRefDescriptorCarriesOnlyThePool(t *testing.T) {
	refs := validClaim().RefDescriptor()

	if refs.GVK.Kind != "NetBoxIPAddressClaim" {
		t.Errorf("GVK = %s, want the claim's own", refs.GVK)
	}

	if len(refs.Fields) != 1 || refs.Fields[0].Spec != "prefixRef" {
		t.Errorf("fields = %+v, want the pool reference alone", refs.Fields)
	}

	// Deliberately not a reconcilable Descriptor: it has no endpoint and no natural key, so
	// anything that tried to drive the declarative engine with it fails loudly rather than
	// reconciling a claim as if it were an object.
	if err := refs.Validate(); err == nil {
		t.Error("the ref-only view validates as a full Descriptor; it must not be usable as one")
	}
}

// validClaim is the shipped descriptor's shape, built by hand so a case can break one thing.
func validClaim() ClaimDescriptor {
	return ClaimDescriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddressClaim"),
		Endpoint:   "ipam/ip-addresses",
		ObjectType: "ipam.ipaddress",
		Pool: Field{
			Spec: "prefixRef", Class: ClassRefOne,
			Target: netboxv1alpha1.PrefixRef{}.TargetGVK(),
		},
		PoolValueField:      "prefix",
		PoolSubPath:         "available-ips",
		PoolMustNotBeTrue:   []string{"mark_utilized"},
		PoolForbiddenStatus: []string{"container"},
		ResultField:         "address",
		Taggable:            true,
		CustomFieldable:     true,
	}
}

func contains(haystack []string, needle string) bool {
	for _, got := range haystack {
		if got == needle {
			return true
		}
	}

	return false
}

func sorted(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] > values[i] {
			return false
		}
	}

	return true
}

// TestPrefixClaimDescriptor is the asymmetry NBO-064 exists for, asserted rather than assumed.
//
// `status: container` is in the address claim's PoolForbiddenStatus and in the prefix claim's
// PoolExpectedStatus, and those are two lists of *data* rather than two branches -- which is
// the property that lets one value be a refusal for one kind and a precondition for the next.
func TestPrefixClaimDescriptor(t *testing.T) {
	desc, ok := Claim(netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefixClaim"))
	if !ok {
		t.Fatal("NetBoxPrefixClaim is not registered")
	}

	if desc.Endpoint != "ipam/prefixes" || desc.ObjectType != "ipam.prefix" {
		t.Errorf("endpoint/objectType = %s/%s, want ipam/prefixes and ipam.prefix",
			desc.Endpoint, desc.ObjectType)
	}

	if desc.PoolSubPath != "available-prefixes" || desc.ResultField != "prefix" {
		t.Errorf("subPath/resultField = %s/%s, want available-prefixes and prefix",
			desc.PoolSubPath, desc.ResultField)
	}

	if contains(desc.PoolForbiddenStatus, "container") {
		t.Error("status: container is refused, and it is what this kind expects to find:" +
			" carving a child prefix is exactly what a container is for")
	}

	if !contains(desc.PoolExpectedStatus, "container") {
		t.Error("status: container is not expected, so allocating out of an active prefix" +
			" would pass unremarked")
	}

	if len(desc.RequestFields) != 1 || desc.RequestFields[0].API != "prefix_length" {
		t.Errorf("requestFields = %+v, want one entry writing prefix_length", desc.RequestFields)
	}

	if desc.RequestLengthField != "prefix_length" {
		t.Errorf("requestLengthField = %q; without it a /16 asked for out of a /16 is accepted"+
			" by netbox and duplicates the container", desc.RequestLengthField)
	}
}

// TestIPRangeClaimDescriptor pins the kind whose allocation NetBox does not serialise.
//
// Two assertions matter beyond the wiring. Its sub-path is not a NetBox URL, which is how a
// reader learns that this kind's safety argument is the overlap rejection rather than a lock.
// And both its request fields are `@`-prefixed placement inputs: `size` is derived by NetBox
// and `alignment` is not a NetBox concept, so neither may ever be sent as a column.
func TestIPRangeClaimDescriptor(t *testing.T) {
	desc, ok := Claim(netboxv1alpha1.GroupVersion.WithKind("NetBoxIPRangeClaim"))
	if !ok {
		t.Fatal("NetBoxIPRangeClaim is not registered")
	}

	if desc.Endpoint != "ipam/ip-ranges" || desc.ObjectType != "ipam.iprange" {
		t.Errorf("endpoint/objectType = %s/%s, want ipam/ip-ranges and ipam.iprange",
			desc.Endpoint, desc.ObjectType)
	}

	if desc.Pool.Spec != "parentPrefixRef" || desc.Pool.Target.Kind != "NetBoxPrefix" {
		t.Errorf("pool = %s -> %s, want parentPrefixRef -> NetBoxPrefix",
			desc.Pool.Spec, desc.Pool.Target.Kind)
	}

	if desc.PoolSubPath != "place-ip-range" || desc.ResultField != "start_address" {
		t.Errorf("subPath/resultField = %s/%s, want place-ip-range and start_address",
			desc.PoolSubPath, desc.ResultField)
	}

	if len(desc.RequestFields) != 2 {
		t.Fatalf("requestFields = %+v, want size and alignment", desc.RequestFields)
	}

	for _, field := range desc.RequestFields {
		if !strings.HasPrefix(field.API, "@") {
			t.Errorf("requestField %s writes %q as a netbox column; size is derived and"+
				" alignment is not a netbox concept", field.Spec, field.API)
		}
	}

	if desc.RequestLengthField != "" {
		t.Errorf("requestLengthField = %q, and a range request carries no mask length",
			desc.RequestLengthField)
	}
}

// TestIPRangeDescriptorNeverWritesSize is the one thing about NetBoxIPRange that is easy to get
// wrong and impossible to notice.
//
// `ipam.IPRange.size` is `editable=False` and set in `save()` from the two endpoints, so a
// `size` in a payload is dropped without complaint -- and a `size` in a diff would be a PATCH
// that changes nothing, computed again on every resync, forever.
func TestIPRangeDescriptorNeverWritesSize(t *testing.T) {
	desc, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxIPRange"))
	if !ok {
		t.Fatal("NetBoxIPRange is not registered")
	}

	for _, field := range desc.Fields {
		if field.API == "size" {
			t.Errorf("field %s writes `size`, which netbox derives", field.Spec)
		}
	}

	if !contains(desc.ReadOnly, "size") {
		t.Error("`size` is not read-only, so a drift comparison would try to correct it")
	}

	if len(desc.NaturalKeys) != 2 {
		t.Fatalf("%d natural-key candidates, want two: a range in a VRF and the same addresses"+
			" in the global table are different objects", len(desc.NaturalKeys))
	}
}
