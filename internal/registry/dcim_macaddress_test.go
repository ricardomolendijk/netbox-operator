package registry

import (
	"reflect"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func macAddressDescriptor(t *testing.T) Descriptor {
	t.Helper()

	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxMACAddress"))
	if !ok {
		t.Fatal("Get(NetBoxMACAddress) found no descriptor; the kind's init() did not run")
	}

	return d
}

// TestMACAddressUnionIsNarrowerThanIPAssignment is the assertion that stops the tempting
// shortcut, and it is a boot-failure guard rather than a style check.
//
// NetBox restricts this column to two content types --
// `MACADDRESS_ASSIGNMENT_MODELS = Q(app_label='dcim', model='interface') |
// Q(app_label='virtualization', model='vminterface')` (netbox/dcim/constants.py:156-159), used
// as the serializer field's queryset (netbox/dcim/api/serializers_/devices.py:318). Reusing
// ipam.IPAddress's three-member IPAssignment and narrowing only AllowedTypes would pass today
// and fail the *boot* the day NetBoxFHRPGroup is registered, because validateUnionTypes
// cross-checks every member whose Kind exists against AllowedTypes.
//
// So the assertion is on both halves, and specifically that `ipam.fhrpgroup` and
// `fhrpGroupRef` appear in neither.
func TestMACAddressUnionIsNarrowerThanIPAssignment(t *testing.T) {
	d := macAddressDescriptor(t)

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly one pair", d.GenericFKs)
	}

	pair := d.GenericFKs[0]

	if pair.TypeField != "assigned_object_type" || pair.IDField != "assigned_object_id" {
		t.Errorf("pair = (%q, %q), want (assigned_object_type, assigned_object_id)",
			pair.TypeField, pair.IDField)
	}

	wantTypes := []string{"dcim.interface", "virtualization.vminterface"}
	if !reflect.DeepEqual(pair.AllowedTypes, wantTypes) {
		t.Errorf("AllowedTypes = %v, want %v", pair.AllowedTypes, wantTypes)
	}

	wantMembers := []string{"interfaceRef", "vmInterfaceRef"}
	got := make([]string, 0, len(pair.Members))
	for _, member := range pair.Members {
		got = append(got, member.Spec)
	}

	if !reflect.DeepEqual(got, wantMembers) {
		t.Errorf("Members = %v, want %v -- fhrpGroupRef is legal on an IP address and illegal here", got, wantMembers)
	}

	// No cached columns: dcim.MACAddress maintains no denormalised caches from the pair,
	// unlike CachedScopeMixin's four.
	if len(pair.Cached) != 0 {
		t.Errorf("Cached = %v, want none", pair.Cached)
	}

	// Both members cascade, by `mac_addresses GenericRelation` on dcim.Interface
	// (netbox/dcim/models/device_components.py:966-971) and on virtualization.VMInterface
	// (netbox/virtualization/models/virtualmachines.py:507-512). Stated per member because the
	// cascade is a property of the referring kind per target, and a member left unstated is
	// ErrMemberCascadePartial at boot rather than a member that silently does not cascade.
	for _, member := range pair.Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("member %s CascadeOnDelete = %v, want true", member.Spec, member.CascadeOnDelete)
		}
	}

	// And the containment parent is that pair, so a MAC is owned by whichever interface it
	// actually resolved through (ADR-0003 rule 4, per member since #214).
	if d.ContainmentRef != pair.Spec {
		t.Errorf("ContainmentRef = %q, want %q", d.ContainmentRef, pair.Spec)
	}
}

// TestMACAddressKeySelectionFollowsTheAssignment walks the three states of the union, which
// select three different outcomes.
//
// The middle one is the reason the assignment is in the key at all: duplicate MACs are legal
// in NetBox -- dcim.MACAddress declares no meta.constraints, only indexes
// (netbox/dcim/models/devices.py:1380-1385) -- so a candidate on `mac_address` alone would
// match every copy of the address in the database and report Conflict where the narrower one
// finds the single row this CR describes.
func TestMACAddressKeySelectionFollowsTheAssignment(t *testing.T) {
	d := macAddressDescriptor(t)

	// What applyGenericFK puts in the state: the union's own spec field always, plus the two
	// column names once the member resolves (internal/reconciler/refs.go).
	resolvedPair := []string{"macAddress", "assignedObject", "assigned_object_type", "assigned_object_id"}

	for _, tc := range []struct {
		name   string
		state  SpecState
		want   int
		pinned bool
	}{
		{
			name:  "a resolved assignment takes the (type, id, mac) candidate",
			state: SpecState{Declared: []string{"macAddress", "assignedObject"}, Resolved: resolvedPair},
			want:  1,
		},
		{
			name:   "an undeclared assignment takes the unassigned candidate",
			state:  SpecState{Declared: []string{"macAddress"}, Resolved: []string{"macAddress"}},
			want:   1,
			pinned: true,
		},
		{
			// The interface exists in the manifest but not yet in NetBox. Falling through to
			// the null-pinned candidate would find an *unattached* MAC of the same address and
			// PATCH this assignment onto it, which is somebody else's row.
			name: "a declared but unresolved assignment takes neither, and the engine waits",
			state: SpecState{
				Declared: []string{"macAddress", "assignedObject"},
				Resolved: []string{"macAddress", "assignedObject"},
			},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)
			if len(got) != tc.want {
				t.Fatalf("Candidates() = %+v (%d), want %d", got, len(got), tc.want)
			}
			if tc.want == 0 {
				return
			}
			if pinned := len(got[0].NullFields) == 1; pinned != tc.pinned {
				t.Errorf("candidate %+v pins a null filter = %v, want %v", got[0], pinned, tc.pinned)
			}
		})
	}
}

// TestMACAddressNullPinIsOnTheIDHalfOnly is #206's rule applied to a generic FK, and the one
// thing about a null pin that cannot be guessed from the descriptor.
//
// `assigned_object_id` is a plain PositiveBigIntegerField (netbox/dcim/models/devices.py:1371),
// so the pin is the `__empty=true` suffix -- the sentinel `null` fails IntegerField validation
// and the request is rejected outright. `assigned_object_type` gets no pin at all: it is a
// ForeignKey to contenttypes.ContentType behind MultiValueContentTypeFilter, for which NetBox
// registers neither spelling, and the sentinel is worse than dropped -- it makes the request
// match nothing and the engine create a duplicate. Pinning the paired `_id` asks the same
// question, because NetBox rejects one half of the pair without the other.
func TestMACAddressNullPinIsOnTheIDHalfOnly(t *testing.T) {
	d := macAddressDescriptor(t)

	pins := make(map[string]NullColumn)
	for _, key := range d.NaturalKeys {
		for _, pin := range key.NullFields {
			pins[pin.Filter] = pin.Column
		}
	}

	if got, want := pins["assigned_object_id"], NullColumnNumeric; got != want {
		t.Errorf("assigned_object_id pin = %q, want %q", got, want)
	}

	if column, pinned := pins["assigned_object_type"]; pinned {
		t.Errorf("assigned_object_type is pinned as %q; a content-type filter registers no null spelling", column)
	}
}

// TestMACAddressHasNoDuplicateSpec is the difference from ipam.IPAddress, which has the same
// absence of constraints and answers it differently.
//
// Neither model is policed by the database, but only one of them was asked to make several
// rows legal at once: decision #177 gave ipam.IPAddress `spec.allowDuplicate` for VRRP and
// anycast. Nothing about a MAC address asks for that, so an ambiguous lookup here is only ever
// a Conflict -- and the engine already decides that once, in the client.
func TestMACAddressHasNoDuplicateSpec(t *testing.T) {
	d := macAddressDescriptor(t)

	if d.DuplicateSpec != "" {
		t.Errorf("DuplicateSpec = %q, want empty", d.DuplicateSpec)
	}

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	// `mac_address` is the filter MACAddressFilterSet declares
	// (netbox/dcim/filtersets.py:2030); every candidate matches on it, because an address is
	// the one thing a MAC is always identified by.
	for _, key := range d.NaturalKeys {
		if !slices.Contains(specFiltersOf(key), "mac_address") {
			t.Errorf("candidate %+v does not filter on mac_address", key)
		}
	}
}
