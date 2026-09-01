package v1alpha1

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The unit tests for the VM's inline sugar. Everything here is a pure function of one spec:
// no client, no API server, no engine -- which is the cheapest place to hold the two
// properties the whole feature rests on, that the declared tree is derived only from keys and
// that reordering a list therefore changes nothing.

// inlineVM is the fixture the tests below vary: the `dns` VM of
// docs/concepts/inline-children.md, with one interface carrying one address, and one disk.
func inlineVM(mutate func(*NetBoxVirtualMachine)) *NetBoxVirtualMachine {
	enabled := true
	mtu := int32(1500)

	vm := &NetBoxVirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "homelab"},
		Spec: NetBoxVirtualMachineSpec{
			NetBoxObjectSpec: NetBoxObjectSpec{EndpointRef: "homelab", DeletionPolicy: DeletionDelete},
			Name:             "dns",
			Interfaces: []InlineVMInterface{{
				Name:            "eth0",
				Enabled:         &enabled,
				MTU:             &mtu,
				Mode:            InterfaceModeTagged,
				VRFRef:          &VRFRef{Name: "vrf-home"},
				UntaggedVLANRef: &VLANRef{Name: "vlan-mgmt"},
				TaggedVLANs:     []VLANRef{{Name: "vlan-guest"}},
				Description:     "mgmt",
				Addresses: []InlineVMAddress{{
					Address: "10.20.0.10/24",
					Primary: true,
					Status:  IPAddressStatusActive,
					DNSName: "dns.home.arpa",
				}},
			}},
			Disks: []InlineVirtualDisk{{Name: "scsi0", Size: 20, Description: "root"}},
		},
	}

	if mutate != nil {
		mutate(vm)
	}

	return vm
}

// declared flattens InlineChildren() into `path -> (kind, name)`, which is the shape the
// materialiser derives and the shape every assertion below is about.
func declared(t *testing.T, vm *NetBoxVirtualMachine) map[string][2]string {
	t.Helper()

	out := map[string][2]string{}

	var walk func(sets []InlineChildSet, path []ChildSegment)
	walk = func(sets []InlineChildSet, path []ChildSegment) {
		for _, set := range sets {
			for _, entry := range set.Entries {
				at := append(append([]ChildSegment{}, path...),
					ChildSegment{Field: set.Field, Discriminator: set.Discriminator, Key: entry.Key})

				if entry.Desired != nil {
					kind := reflect.TypeOf(entry.Desired).Elem().Name()
					out[ChildPath(at)] = [2]string{kind, ChildName(vm.GetName(), at)}
				}

				walk(entry.Children, at)
			}
		}
	}

	walk(vm.InlineChildren(), nil)

	return out
}

// TestInlineChildrenDeclaresTheWholeTree is the acceptance criterion of NBO-033 in its
// cheapest form: one manifest, three Kinds, three key-based paths and three derived names.
func TestInlineChildrenDeclaresTheWholeTree(t *testing.T) {
	t.Parallel()

	want := map[string][2]string{
		"spec.interfaces[eth0]": {"NetBoxVMInterface", "dns-eth0"},
		"spec.interfaces[eth0].addresses[10.20.0.10/24]": {
			"NetBoxIPAddress", "dns-eth0-ip-10-20-0-10-24",
		},
		"spec.disks[scsi0]": {"NetBoxVirtualDisk", "dns-disk-scsi0"},
	}

	got := declared(t, inlineVM(nil))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InlineChildren() declared\n\t%v\nwant\n\t%v", got, want)
	}
}

// TestInlineAddressIsAssignedToItsOwnInterface is the reason ChildName is exported from this
// package: an inline address has to name the interface child its own parent materialises, and
// it has to do so before either object exists.
func TestInlineAddressIsAssignedToItsOwnInterface(t *testing.T) {
	t.Parallel()

	address := addressChild(t, inlineVM(nil))

	assigned := address.Spec.AssignedObject
	switch {
	case assigned == nil:
		t.Fatal("the materialised address has no assignedObject, so netbox would hold an " +
			"address attached to nothing")
	case assigned.VMInterfaceRef == nil:
		t.Fatalf("assignedObject is %+v, want the vmInterfaceRef member", assigned)
	case assigned.VMInterfaceRef.Name != "dns-eth0":
		t.Errorf("assignedObject.vmInterfaceRef.name = %q, want the derived interface child "+
			"name dns-eth0", assigned.VMInterfaceRef.Name)
	}
}

// TestInlineChildrenCarriesTheParentReference holds the half of the shape the materialiser
// does *not* fill in. It sets the namespace, the name, the labels and the owner references;
// `virtualMachineRef` is the inline code's job, and a child without it would be a component
// of no VM.
func TestInlineChildrenCarriesTheParentReference(t *testing.T) {
	t.Parallel()

	vm := inlineVM(nil)

	for _, child := range vm.InlineChildren() {
		for _, entry := range child.Entries {
			switch desired := entry.Desired.(type) {
			case *NetBoxVMInterface:
				if desired.Spec.VirtualMachineRef.Name != "dns" {
					t.Errorf("the interface child's virtualMachineRef is %+v, want name dns",
						desired.Spec.VirtualMachineRef)
				}
			case *NetBoxVirtualDisk:
				if desired.Spec.VirtualMachineRef.Name != "dns" {
					t.Errorf("the disk child's virtualMachineRef is %+v, want name dns",
						desired.Spec.VirtualMachineRef)
				}
			}
		}
	}
}

// TestInlineChildrenSurvivesAReorder is the measurable form of "the path is key-based, so
// reordering is free". Reorder every list and the derived tree is byte-identical, which is
// what stops a reorder in Git from pruning and re-creating a NetBox object -- and, for an
// address, from re-rolling it.
func TestInlineChildrenSurvivesAReorder(t *testing.T) {
	t.Parallel()

	before := declared(t, inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces = append(vm.Spec.Interfaces, InlineVMInterface{
			Name:      "eth1",
			Addresses: []InlineVMAddress{{Address: "10.20.1.10/24"}, {Address: "10.20.1.11/24"}},
		})
		vm.Spec.Disks = append(vm.Spec.Disks, InlineVirtualDisk{Name: "scsi1", Size: 40})
	}))

	after := declared(t, inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces = append([]InlineVMInterface{{
			Name:      "eth1",
			Addresses: []InlineVMAddress{{Address: "10.20.1.11/24"}, {Address: "10.20.1.10/24"}},
		}}, vm.Spec.Interfaces...)
		vm.Spec.Disks = append([]InlineVirtualDisk{{Name: "scsi1", Size: 40}}, vm.Spec.Disks...)
	}))

	if !reflect.DeepEqual(before, after) {
		t.Errorf("reordering the inline lists changed the declared tree:\n\t%v\nbecame\n\t%v",
			before, after)
	}
}

// TestInlineChildrenTruncatesALongName is the 253-character case, reached through a real VM
// rather than through ChildName directly -- because the thing that has to hold is that a long
// *interface name* still derives a name the API server will accept, and that two of them do
// not collapse into one.
func TestInlineChildrenTruncatesALongName(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("eth-very-long-interface-name", 12) // 336 characters

	tree := declared(t, inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces = []InlineVMInterface{
			{Name: long + "-a"},
			{Name: long + "-b"},
		}
		vm.Spec.Disks = nil
	}))

	names := map[string]bool{}

	for path, child := range tree {
		name := child[1]

		if len(name) > 253 {
			t.Errorf("%s derives a %d-character name, which the api server refuses (the limit "+
				"is 253)", path, len(name))
		}

		if names[name] {
			t.Errorf("%s derives %q, which a sibling already took: two long entries sharing a "+
				"246-character prefix must not collapse into one child", path, name)
		}

		names[name] = true
	}

	if len(names) != 2 {
		t.Fatalf("two interfaces derived %d distinct names, want 2", len(names))
	}
}

// TestInlineChildrenRenamingAKeyMovesBothPathAndName is the third of the three renames: an
// entry's key changing is a prune plus a create, and the path and the name have to move
// together or the pruner and the applier disagree about which object is which.
func TestInlineChildrenRenamingAKeyMovesBothPathAndName(t *testing.T) {
	t.Parallel()

	renamed := declared(t, inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces[0].Name = "eth1"
	}))

	if _, still := renamed["spec.interfaces[eth0]"]; still {
		t.Error("the old path is still declared after the key was renamed, so nothing would be pruned")
	}

	child, declared := renamed["spec.interfaces[eth1]"]
	if !declared {
		t.Fatalf("the new path is not declared: %v", renamed)
	}

	if child[1] != "dns-eth1" {
		t.Errorf("the renamed interface derives %q, want dns-eth1", child[1])
	}

	if _, still := renamed["spec.interfaces[eth0].addresses[10.20.0.10/24]"]; still {
		t.Error("the address's path did not move with its interface's key")
	}
}

// TestInlineAddressNeverAsksForADuplicate is issue #167, held at the type level.
//
// `spec.allowDuplicate` makes the provenance stamp part of an address's identity, and a
// stamped child that loses status.id creates a *second* address rather than finding its own
// (internal/reconciler/duplicate.go). A materialised child is re-created from an unchanged
// manifest by design, so the flag it must never carry is a field the inline entry does not
// have -- which is a stronger statement than any check, and this is what pins it there.
func TestInlineAddressNeverAsksForADuplicate(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"AllowDuplicate", "allowDuplicate"} {
		if _, exists := reflect.TypeFor[InlineVMAddress]().FieldByName(field); exists {
			t.Errorf("InlineVMAddress carries %s: a materialised address must not be able to ask "+
				"for a duplicate (issue #167)", field)
		}
	}

	encoded, err := json.Marshal(InlineVMAddress{Address: "10.20.0.10/24"})
	if err != nil {
		t.Fatalf("encoding an inline address: %v", err)
	}

	if strings.Contains(string(encoded), "allowDuplicate") {
		t.Errorf("an inline address serialises %s, so the CRD would accept the field", encoded)
	}

	if addressChild(t, inlineVM(nil)).Spec.AllowDuplicate {
		t.Error("the materialised address sets spec.allowDuplicate")
	}
}

// TestInlineClaimFromMaterialisesAClaim is ADR-0004's "the inline form is sugar over a real
// claim, not a second allocation path": the child is an ordinary NetBoxIPAddressClaim, keyed
// by its pool, with the same derived name and the same markers as any other child.
func TestInlineClaimFromMaterialisesAClaim(t *testing.T) {
	t.Parallel()

	tree := declared(t, inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces[0].Addresses = []InlineVMAddress{{
			ClaimFrom: &InlineAddressClaim{PrefixRef: PrefixRef{Name: "mgmt-net"}},
		}}
	}))

	child, ok := tree["spec.interfaces[eth0].addresses[mgmt-net]"]
	if !ok {
		t.Fatalf("claimFrom declared no child at the pool's key: %v", tree)
	}

	if child != [2]string{"NetBoxIPAddressClaim", "dns-eth0-claim-mgmt-net"} {
		t.Errorf("claimFrom declared %v, want a NetBoxIPAddressClaim named dns-eth0-claim-mgmt-net", child)
	}
}

// TestInlineClaimKeyIsDeterministicInEveryMode is what makes a cluster rebuild give back the
// same address: the claim's allocation identity is derived from its name, its name from this
// key, and a key that moved between passes would be a released address and a newly allocated
// one (docs/decisions/0005-gitops-coexistence.md).
func TestInlineClaimKeyIsDeterministicInEveryMode(t *testing.T) {
	t.Parallel()

	id := int64(7)

	cases := []struct {
		name string
		ref  PrefixRef
		want string
	}{
		{name: "name mode", ref: PrefixRef{Name: "mgmt-net"}, want: "mgmt-net"},
		{name: "slug mode", ref: PrefixRef{Slug: "mgmt_net"}, want: "mgmt_net"},
		{name: "id mode is prefixed", ref: PrefixRef{ID: &id}, want: "id-7"},
		{
			name: "a lookup is sorted, so a map's order cannot reach the name",
			ref:  PrefixRef{Lookup: map[string]string{"vrf": "home", "prefix": "10.0.0.0/8"}},
			want: "prefix-10.0.0.0/8-vrf-home",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			claim := InlineAddressClaim{PrefixRef: tc.ref}

			for range 8 {
				if got := claim.key(); got != tc.want {
					t.Fatalf("key() = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// TestInlineChildrenDeclaresNoClaimSpecFields is the other half of the CEL rule on
// claimFrom: NetBoxIPAddressClaim carries no dnsName, no role and no assignedObject, so the
// materialised claim must carry nothing but its pool -- and a reader has to be able to see
// that the sugar is not quietly filling those in somewhere else.
func TestInlineChildrenDeclaresNoClaimSpecFields(t *testing.T) {
	t.Parallel()

	vm := inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces[0].Addresses = []InlineVMAddress{{
			ClaimFrom: &InlineAddressClaim{PrefixRef: PrefixRef{Name: "mgmt-net"}},
		}}
	})

	claim := childOfKind[*NetBoxIPAddressClaim](t, vm)

	if claim.Spec.PrefixRef.Name != "mgmt-net" {
		t.Errorf("the claim's prefixRef is %+v, want name mgmt-net", claim.Spec.PrefixRef)
	}

	// Left empty for the materialiser to inherit from the parent, which is what makes the
	// chain "VM deleted -> claim deleted -> netbox address freed" the parent's decision.
	if claim.Spec.EndpointRef != "" || claim.Spec.DeletionPolicy != "" {
		t.Errorf("the claim child sets endpointRef=%q deletionPolicy=%q; both are the "+
			"materialiser's to inherit", claim.Spec.EndpointRef, claim.Spec.DeletionPolicy)
	}
}

// TestDerivedRefsBackPatchesThePrimaryAddress is the `primary` half of the ticket. The VM's
// payload has to end up naming a child it materialised, without the operator ever writing the
// VM's spec.
func TestDerivedRefsBackPatchesThePrimaryAddress(t *testing.T) {
	t.Parallel()

	refs, err := inlineVM(nil).DerivedRefs()
	if err != nil {
		t.Fatalf("DerivedRefs() = %v", err)
	}

	want := []DerivedRef{{
		Field: "primaryIP4Ref",
		Ref:   ObjectRef{Name: "dns-eth0-ip-10-20-0-10-24"},
	}}

	if !reflect.DeepEqual(refs, want) {
		t.Errorf("DerivedRefs() = %+v, want %+v", refs, want)
	}
}

// TestDerivedRefsSplitsTheTwoFamilies holds that the two columns resolve independently: a VM
// whose IPv6 address is missing still gets its IPv4 one written, which is the property that
// makes them two deferred fields rather than one.
func TestDerivedRefsSplitsTheTwoFamilies(t *testing.T) {
	t.Parallel()

	refs, err := inlineVM(func(vm *NetBoxVirtualMachine) {
		vm.Spec.Interfaces[0].Addresses = append(vm.Spec.Interfaces[0].Addresses,
			InlineVMAddress{Address: "2001:db8::10/64", Primary: true})
	}).DerivedRefs()
	if err != nil {
		t.Fatalf("DerivedRefs() = %v", err)
	}

	got := map[string]string{}
	for _, ref := range refs {
		got[ref.Field] = ref.Ref.Name
	}

	want := map[string]string{
		"primaryIP4Ref": "dns-eth0-ip-10-20-0-10-24",
		"primaryIP6Ref": "dns-eth0-ip-2001-db8-10-64",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("DerivedRefs() = %v, want %v", got, want)
	}
}

// TestDerivedRefsRefusesTwoSourcesForOneColumn is the Conflict, in both of its shapes. Two
// answers to one question is refused rather than resolved by precedence: choosing one would
// make the other a lie that no condition mentions.
func TestDerivedRefsRefusesTwoSourcesForOneColumn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*NetBoxVirtualMachine)
		names  []string
	}{{
		name: "two inline primaries of one family, on two different interfaces",
		mutate: func(vm *NetBoxVirtualMachine) {
			vm.Spec.Interfaces = append(vm.Spec.Interfaces, InlineVMInterface{
				Name:      "eth1",
				Addresses: []InlineVMAddress{{Address: "10.20.1.10/24", Primary: true}},
			})
		},
		names: []string{
			"spec.interfaces[eth0].addresses[10.20.0.10/24]",
			"spec.interfaces[eth1].addresses[10.20.1.10/24]",
		},
	}, {
		name: "an explicit primaryIP4Ref beside an inline primary",
		mutate: func(vm *NetBoxVirtualMachine) {
			vm.Spec.PrimaryIP4Ref = &IPAddressRef{Name: "somebody-elses-address"}
		},
		names: []string{"spec.interfaces[eth0].addresses[10.20.0.10/24]", "spec.primaryIP4Ref"},
	}, {
		name: "an explicit primaryIP6Ref beside an inline v6 primary",
		mutate: func(vm *NetBoxVirtualMachine) {
			vm.Spec.PrimaryIP6Ref = &IPAddressRef{Name: "somebody-elses-address"}
			vm.Spec.Interfaces[0].Addresses = []InlineVMAddress{
				{Address: "2001:db8::10/64", Primary: true},
			}
		},
		names: []string{"spec.interfaces[eth0].addresses[2001:db8::10/64]", "spec.primaryIP6Ref"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			refs, err := inlineVM(tc.mutate).DerivedRefs()
			if err == nil {
				t.Fatalf("DerivedRefs() = %+v, want a refusal", refs)
			}

			if !errors.Is(err, ErrDerivedRefConflict) {
				t.Errorf("DerivedRefs() = %v, which does not unwrap to ErrDerivedRefConflict, so "+
					"the engine would report it as an api failure rather than a Conflict", err)
			}

			for _, name := range tc.names {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the refusal does not name %s: %v", name, err)
				}
			}
		})
	}
}

// TestDerivedRefsIgnoresANonPrimaryAndAClaim holds the negative: a VM that asked for no
// primary address contributes no derived reference at all, so `status.deferredPending` stays
// empty and the VM is Ready on its first pass. A `claimFrom` cannot be primary either -- the
// claim materialises no address CR for the VM to point at yet (NBO-036).
func TestDerivedRefsIgnoresANonPrimaryAndAClaim(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*NetBoxVirtualMachine){
		"no primary at all": func(vm *NetBoxVirtualMachine) {
			vm.Spec.Interfaces[0].Addresses[0].Primary = false
		},
		"no interfaces at all": func(vm *NetBoxVirtualMachine) { vm.Spec.Interfaces = nil },
		"a claimFrom marked primary, which the CRD rejects first": func(vm *NetBoxVirtualMachine) {
			vm.Spec.Interfaces[0].Addresses = []InlineVMAddress{{
				Primary:   true,
				ClaimFrom: &InlineAddressClaim{PrefixRef: PrefixRef{Name: "mgmt-net"}},
			}}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refs, err := inlineVM(mutate).DerivedRefs()
			if err != nil {
				t.Fatalf("DerivedRefs() = %v", err)
			}

			if len(refs) != 0 {
				t.Errorf("DerivedRefs() = %+v, want none", refs)
			}
		})
	}
}

// TestInlineChildrenIsPure holds the contract the engine relies on by calling this on every
// reconcile: two calls over one unchanged spec produce identical trees, and neither call
// writes into the spec it read.
func TestInlineChildrenIsPure(t *testing.T) {
	t.Parallel()

	vm := inlineVM(nil)
	before, err := json.Marshal(vm.Spec)
	if err != nil {
		t.Fatalf("encoding the spec: %v", err)
	}

	first := declared(t, vm)
	second := declared(t, vm)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls declared different trees:\n\t%v\n\t%v", first, second)
	}

	after, err := json.Marshal(vm.Spec)
	if err != nil {
		t.Fatalf("re-encoding the spec: %v", err)
	}

	if string(before) != string(after) {
		t.Errorf("InlineChildren() mutated the spec it read:\n\t%s\n\t%s", before, after)
	}
}

// addressChild is the one materialised NetBoxIPAddress in the fixture.
func addressChild(t *testing.T, vm *NetBoxVirtualMachine) *NetBoxIPAddress {
	t.Helper()

	return childOfKind[*NetBoxIPAddress](t, vm)
}

// childOfKind is the single declared child of one Go type, and fails when there is not
// exactly one -- so a test asserting about "the address" cannot silently assert about the
// first of several.
func childOfKind[T client.Object](t *testing.T, vm *NetBoxVirtualMachine) T {
	t.Helper()

	var found []T

	var walk func(sets []InlineChildSet)
	walk = func(sets []InlineChildSet) {
		for _, set := range sets {
			for _, entry := range set.Entries {
				if typed, ok := entry.Desired.(T); ok {
					found = append(found, typed)
				}

				walk(entry.Children)
			}
		}
	}

	walk(vm.InlineChildren())

	if len(found) != 1 {
		t.Fatalf("the fixture declared %d children of type %T, want exactly 1", len(found), *new(T))
	}

	return found[0]
}
