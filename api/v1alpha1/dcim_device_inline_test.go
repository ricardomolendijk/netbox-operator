package v1alpha1

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// device is a NetBoxDevice with a metadata.name and whatever inline interfaces the test
// needs. spec.name is set to something *different* from metadata.name on purpose: every
// derived child name has to come from metadata.name, and a fixture where the two agree cannot
// tell the two apart (api/v1alpha1/inline_children.go, ChildName).
func device(name string, entries ...InlineInterface) *NetBoxDevice {
	return &NetBoxDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "homelab"},
		Spec: NetBoxDeviceSpec{
			Name:       "NetBox-" + name,
			Interfaces: entries,
		},
	}
}

// declaredChild is one child as the materialiser will see it: the path it came from, the name
// it derives, and its spec. Everything the engine derives from an InlineChildSet, and nothing
// else -- so a test comparing these is asserting on what actually reaches the API server.
type declaredChild struct {
	name string
	spec string
}

// walk flattens what InlineChildren() returned exactly as internal/reconciler's desire() does:
// one segment per level, keyed by ChildPath, named by ChildName. Reimplemented rather than
// imported because api/v1alpha1 cannot import internal/reconciler -- and because a test that
// called the engine's own flattening could not show that this Kind's tree is the shape the
// engine expects.
func walk(t *testing.T, parent string, sets []InlineChildSet, path []ChildSegment) map[string]declaredChild {
	t.Helper()

	out := map[string]declaredChild{}

	for _, set := range sets {
		for _, entry := range set.Entries {
			at := append(slices.Clip(path), ChildSegment{
				Field: set.Field, Discriminator: set.Discriminator, Key: entry.Key,
			})

			if entry.Desired != nil {
				out[ChildPath(at)] = declaredChild{
					name: ChildName(parent, at),
					spec: specJSON(t, entry.Desired),
				}
			}

			maps.Copy(out, walk(t, parent, entry.Children, at))
		}
	}

	return out
}

// specJSON renders a declared child's spec, which is what the materialiser server-side-applies
// and therefore the only part of the object this Kind decides.
func specJSON(t *testing.T, obj client.Object) string {
	t.Helper()

	encoded, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("encoding %T: %v", obj, err)
	}

	var decoded struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding %T: %v", obj, err)
	}

	return string(decoded.Spec)
}

// TestDeviceDeclaresItsInterfacesAndTheirAddresses is the shape of the whole feature: two
// levels, key-based paths, and a child per entry of each.
func TestDeviceDeclaresItsInterfacesAndTheirAddresses(t *testing.T) {
	t.Parallel()

	d := device("rtmrpi0001", InlineInterface{
		Name: "eth0",
		Type: "10gbase-t",
		Addresses: []InlineIPAddress{
			{Address: "10.0.20.10/24"},
			{Address: "10.0.20.10/25"},
		},
	})

	children := walk(t, d.Name, d.InlineChildren(), nil)

	want := map[string]string{
		"spec.interfaces[eth0]":                          "rtmrpi0001-eth0",
		"spec.interfaces[eth0].addresses[10.0.20.10/24]": "rtmrpi0001-eth0-ip-10-0-20-10-24",
		"spec.interfaces[eth0].addresses[10.0.20.10/25]": "rtmrpi0001-eth0-ip-10-0-20-10-25",
	}

	if len(children) != len(want) {
		t.Fatalf("declared %d children, want %d: %v", len(children), len(want), slices.Sorted(maps.Keys(children)))
	}

	for path, name := range want {
		got, declared := children[path]
		if !declared {
			t.Errorf("nothing is declared at %s; the owned-by path is what pruning reads", path)

			continue
		}

		if got.name != name {
			t.Errorf("%s derives the name %q, want %q", path, got.name, name)
		}
	}
}

// TestDeviceInlineInterfaceCarriesTheParentAndTheEntry is the two halves of a materialised
// interface's spec: what the parent contributes and what the entry does.
func TestDeviceInlineInterfaceCarriesTheParentAndTheEntry(t *testing.T) {
	t.Parallel()

	enabled := false
	mtu := int32(9000)

	d := device("sw1", InlineInterface{
		Name:        "ge-0/0/1",
		Type:        "10gbase-t",
		Label:       "Port 1",
		Enabled:     &enabled,
		MTU:         &mtu,
		Mode:        InterfaceModeTagged,
		Description: "Uplink",
		TaggedVLANs: []VLANRef{{Name: "vlan-guest"}},
		VRFRef:      &VRFRef{Name: "vrf-home"},
	})

	child, ok := d.InlineChildren()[0].Entries[0].Desired.(*NetBoxInterface)
	if !ok {
		t.Fatalf("an inline interface declared a %T, want a *NetBoxInterface", d.InlineChildren()[0].Entries[0].Desired)
	}

	// metadata.name, not spec.name: a `name`-mode reference resolves through the CR, and the
	// CR's name is the one of the two that cannot change under a live object.
	if child.Spec.DeviceRef.Name != "sw1" {
		t.Errorf("deviceRef.name = %q, want the parent's metadata.name sw1", child.Spec.DeviceRef.Name)
	}

	if child.Spec.Name != "ge-0/0/1" || child.Spec.Type != "10gbase-t" {
		t.Errorf("name/type = %q/%q, want ge-0/0/1 and 10gbase-t", child.Spec.Name, child.Spec.Type)
	}

	if child.Spec.Enabled == nil || *child.Spec.Enabled {
		t.Error("enabled: false did not reach the child; a pointer exists so that false is not unset")
	}

	// Nothing the materialiser owns may be set here, or the apply would fight it.
	if child.Name != "" || child.Namespace != "" || len(child.OwnerReferences) != 0 {
		t.Error("the entry set an identity of its own; the materialiser owns name, namespace and owner references")
	}

	if child.Spec.EndpointRef != "" || child.Spec.DeletionPolicy != "" {
		t.Error("the entry set endpointRef or deletionPolicy; both are inherited from the parent")
	}
}

// TestDeviceInlineAddressIsAssignedToItsSiblingInterface is the criterion NetBox sees as
// `assigned_object_type: "dcim.interface"`: the address names the interface *child*, so the
// generic pair is written once that child has an id.
func TestDeviceInlineAddressIsAssignedToItsSiblingInterface(t *testing.T) {
	t.Parallel()

	d := device("rtmrpi0001", InlineInterface{
		Name:      "eth0",
		Type:      "10gbase-t",
		Addresses: []InlineIPAddress{{Address: "10.0.20.10/24", DNSName: "rtmrpi0001.home.arpa"}},
	})

	child, ok := d.InlineChildren()[0].Entries[0].Children[0].Entries[0].Desired.(*NetBoxIPAddress)
	if !ok {
		t.Fatal("an inline address did not declare a *NetBoxIPAddress")
	}

	assigned := child.Spec.AssignedObject
	if assigned == nil || assigned.InterfaceRef == nil {
		t.Fatal("the address declares no assignedObject.interfaceRef, so NetBox would hold it unassigned")
	}

	if assigned.InterfaceRef.Name != "rtmrpi0001-eth0" {
		t.Errorf("interfaceRef.name = %q, want the sibling child rtmrpi0001-eth0",
			assigned.InterfaceRef.Name)
	}

	if assigned.VMInterfaceRef != nil || assigned.FHRPGroupRef != nil {
		t.Error("more than one union member is set, which the CRD's own CEL rule rejects")
	}
}

// TestDeviceInlineAddressIsNeverAllowedToDuplicate is issue #167 as a structural guarantee
// rather than a rule somebody has to remember.
//
// spec.allowDuplicate makes the provenance stamp part of an address's identity, so a
// materialised child that lost status.id would create a *second* NetBox address instead of
// adopting its own (internal/reconciler/duplicate.go). The inline entry has no field that
// could set it, which is why this test asserts on the built child rather than on the input:
// the only way to reintroduce the bug is to add the field, and this fails when somebody does.
func TestDeviceInlineAddressIsNeverAllowedToDuplicate(t *testing.T) {
	t.Parallel()

	d := device("sw1", InlineInterface{
		Name:      "eth0",
		Type:      "10gbase-t",
		Addresses: []InlineIPAddress{{Address: "10.0.20.10/24"}},
	})

	child := d.InlineChildren()[0].Entries[0].Children[0].Entries[0].Desired.(*NetBoxIPAddress)
	if child.Spec.AllowDuplicate {
		t.Error("a materialised address declares allowDuplicate; losing status.id would then " +
			"create a second NetBox object rather than adopting its own (#167)")
	}
}

// TestDeviceInlineSiblingKeysBecomeDerivedNames is the LAG design note as an assertion: the
// user writes a key, the child gets a reference, and the naming algorithm stays an
// implementation detail of the operator.
func TestDeviceInlineSiblingKeysBecomeDerivedNames(t *testing.T) {
	t.Parallel()

	d := device("sw1",
		InlineInterface{Name: "bond0", Type: "lag"},
		InlineInterface{Name: "eth0", Type: "10gbase-t", LAG: "bond0"},
		InlineInterface{Name: "eth0.100", Type: "virtual", Parent: "eth0", Bridge: "bond0"},
	)

	entries := d.InlineChildren()[0].Entries

	member := entries[1].Desired.(*NetBoxInterface)
	if member.Spec.LagRef == nil || member.Spec.LagRef.Name != "sw1-bond0" {
		t.Errorf("lagRef = %v, want the sibling's derived name sw1-bond0", member.Spec.LagRef)
	}

	sub := entries[2].Desired.(*NetBoxInterface)
	if sub.Spec.ParentRef == nil || sub.Spec.ParentRef.Name != "sw1-eth0" {
		t.Errorf("parentRef = %v, want sw1-eth0", sub.Spec.ParentRef)
	}

	if sub.Spec.BridgeRef == nil || sub.Spec.BridgeRef.Name != "sw1-bond0" {
		t.Errorf("bridgeRef = %v, want sw1-bond0", sub.Spec.BridgeRef)
	}

	// A nil pointer rather than an empty reference: an ObjectRef with no mode set is a value
	// the API server rejects outright (objectref.go).
	bond := entries[0].Desired.(*NetBoxInterface)
	if bond.Spec.LagRef != nil || bond.Spec.ParentRef != nil || bond.Spec.BridgeRef != nil {
		t.Error("an entry that named no sibling still declared a self-reference")
	}
}

// TestDeviceInlineSiblingKeyAgreesWithTheDerivedNameWhenItIsTruncated is why the sibling
// lookup goes through ChildName rather than through string concatenation.
//
// A device name long enough to push its children past 253 characters takes the
// truncate-and-hash path, and a `lag` resolved by concatenating would name something that does
// not exist -- a child stuck at RefNotFound, on a manifest that never mentioned a name.
func TestDeviceInlineSiblingKeyAgreesWithTheDerivedNameWhenItIsTruncated(t *testing.T) {
	t.Parallel()

	long := ""
	for range 250 {
		long += "d"
	}

	d := device(long,
		InlineInterface{Name: "bond0", Type: "lag"},
		InlineInterface{Name: "eth0", Type: "10gbase-t", LAG: "bond0"},
	)

	entries := d.InlineChildren()[0].Entries

	bond := ChildName(d.Name, []ChildSegment{{Field: "interfaces", Key: "bond0"}})
	if len(bond) > 253 {
		t.Fatalf("the fixture's derived name is %d characters, so it never exercised truncation", len(bond))
	}

	member := entries[1].Desired.(*NetBoxInterface)
	if member.Spec.LagRef == nil || member.Spec.LagRef.Name != bond {
		t.Errorf("lagRef = %v, want the truncated sibling name %q", member.Spec.LagRef, bond)
	}
}

// TestDeviceInlineReorderChangesNothing is the measurable half of "the path is key-based".
//
// Reversing both lists must produce the identical set of (path, name, spec) triples, because
// that is what the materialiser applies: an identical apply does not bump resourceVersion, so
// a reorder costs zero writes to Kubernetes and zero to NetBox.
func TestDeviceInlineReorderChangesNothing(t *testing.T) {
	t.Parallel()

	forward := device("sw1",
		InlineInterface{
			Name: "eth0", Type: "10gbase-t", LAG: "bond0",
			Addresses: []InlineIPAddress{
				{Address: "10.0.20.10/24"},
				{Address: "2001:db8::1/64"},
			},
		},
		InlineInterface{Name: "bond0", Type: "lag"},
	)

	backward := device("sw1",
		InlineInterface{Name: "bond0", Type: "lag"},
		InlineInterface{
			Name: "eth0", Type: "10gbase-t", LAG: "bond0",
			Addresses: []InlineIPAddress{
				{Address: "2001:db8::1/64"},
				{Address: "10.0.20.10/24"},
			},
		},
	)

	first := walk(t, forward.Name, forward.InlineChildren(), nil)
	second := walk(t, backward.Name, backward.InlineChildren(), nil)

	if !maps.Equal(first, second) {
		t.Errorf("reordering the inline lists changed the declared children:\n%v\n%v",
			slices.Sorted(maps.Keys(first)), slices.Sorted(maps.Keys(second)))
	}
}

// TestDeviceInlineInterfacesDifferingOnlyInCaseDeriveOneName pins the one place where NetBox's
// case-sensitivity and Kubernetes' naming rules genuinely disagree.
//
// `dcim.ComponentModel`'s constraint is over ('device', 'name') with no Lower(), so `Eth0` and
// `eth0` are two interfaces on one device -- while a device's own name is matched with
// `?name__ie=` and is one object either way. A derived child name is slugified, which
// lowercases, so the two entries derive **one** CR name.
//
// That is asserted here rather than worked around, because it is what makes the materialiser's
// collision check fire: two declared children of one kind deriving one name is reported as
// `ChildrenReady=False, Reason=Conflict` and *nothing at all is written* -- not even the
// entries that did not collide (internal/reconciler/children.go, collision). The two paths
// stay distinct, which is what puts both keys in the message. Failing closed is the only safe
// answer: two entries applying one name in turn would each overwrite the other forever.
func TestDeviceInlineInterfacesDifferingOnlyInCaseDeriveOneName(t *testing.T) {
	t.Parallel()

	d := device("sw1",
		InlineInterface{Name: "eth0", Type: "10gbase-t"},
		InlineInterface{Name: "Eth0", Type: "10gbase-t"},
	)

	entries := d.InlineChildren()[0].Entries

	lower := ChildName(d.Name, []ChildSegment{{Field: "interfaces", Key: entries[0].Key}})
	upper := ChildName(d.Name, []ChildSegment{{Field: "interfaces", Key: entries[1].Key}})

	if lower != upper {
		t.Fatalf("%q and %q derive %q and %q; if the derived name has learnt to keep the two "+
			"apart, the Conflict this collapse causes is gone and the reference page and "+
			"the envtest that assert it both need updating",
			entries[0].Key, entries[1].Key, lower, upper)
	}

	// The keys, and therefore the owned-by paths, must stay distinct: they are what names
	// both offending entries in the Conflict message, and what pruning tells apart.
	if entries[0].Key == entries[1].Key {
		t.Error("the two entries share a key, so the Conflict could not name which two collided")
	}
}

// TestDeviceWithNoInlineInterfacesDeclaresAnEmptySet is the two states an inline list has,
// which are not the three every other optional field has.
//
// An omitted `interfaces` and `interfaces: []` are the same instruction -- there is no NetBox
// value to leave alone -- and both must still return the set, because that is what the engine
// reads to know the field is sugar rather than an unmapped column, and what tells the pruner
// which paths are no longer declared.
func TestDeviceWithNoInlineInterfacesDeclaresAnEmptySet(t *testing.T) {
	t.Parallel()

	for name, d := range map[string]*NetBoxDevice{
		"omitted": device("sw1"),
		"empty":   device("sw1", []InlineInterface{}...),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sets := d.InlineChildren()
			if len(sets) != 1 || sets[0].Field != "interfaces" {
				t.Fatalf("declared %d sets, want one named interfaces", len(sets))
			}

			if len(sets[0].Entries) != 0 {
				t.Errorf("declared %d entries, want none", len(sets[0].Entries))
			}
		})
	}
}

// TestNetBoxDeviceIsAnInlineParent is the capability the whole feature hangs off. A compile-time
// assertion, because the engine's only per-kind branch is this type assertion: a device that
// stopped satisfying it would materialise nothing, silently, and every child would be pruned.
func TestNetBoxDeviceIsAnInlineParent(t *testing.T) {
	t.Parallel()

	var _ InlineParent = &NetBoxDevice{}

	if _, ok := any(&NetBoxDevice{}).(InlineParent); !ok {
		t.Error("a *NetBoxDevice is not an InlineParent, so the engine would materialise nothing")
	}
}

// TestInlineListBoundsAreTheDocumentedOnes reads the three bounds off the generated CRD.
//
// A kubebuilder marker cannot read a Go constant, so the numbers are literals in three
// separate doc comments and the arithmetic that justifies them is in a fourth
// (api/v1alpha1/dcim_device_inline.go). This is what stops the four from drifting: a bound
// raised without redoing the cost multiplication fails here, next to the sentence that says
// why the product matters.
func TestInlineListBoundsAreTheDocumentedOnes(t *testing.T) {
	t.Parallel()

	nodes := schemaNodes(t, filepath.Join("..", "..", "config", "crd", "bases",
		"netbox.kubeforge.org_netboxdevices.yaml"))

	for at, want := range map[string]float64{
		"v1alpha1.spec.interfaces":               128,
		"v1alpha1.spec.interfaces[].taggedVLANs": 128,
		"v1alpha1.spec.interfaces[].addresses":   16,
	} {
		node, found := nodes[at]
		if !found {
			t.Errorf("%s is not in the generated schema", at)

			continue
		}

		if got := node["maxItems"]; got != want {
			t.Errorf("%s has maxItems %v, want %v (see the cost arithmetic in "+
				"api/v1alpha1/dcim_device_inline.go)", at, got, want)
		}
	}
}

// TestInlineListsRejectADuplicateKey is how "duplicate inline interface keys, and duplicate
// address keys within one interface, are rejected" is implemented.
//
// A map-typed list rather than a CEL rule, and the difference is measurable: the obvious rule
// -- every entry's key matched against every other's -- is quadratic in the list's bound, so
// at 128 items it costs the API server 16 384 comparisons per admission, where the map key
// costs nothing and is enforced by the same code path that enforces it on a Pod's containers.
// It also makes server-side apply merge the list by key rather than replacing it atomically,
// which is what an inline list wants.
func TestInlineListsRejectADuplicateKey(t *testing.T) {
	t.Parallel()

	nodes := schemaNodes(t, filepath.Join("..", "..", "config", "crd", "bases",
		"netbox.kubeforge.org_netboxdevices.yaml"))

	for at, key := range map[string]string{
		"v1alpha1.spec.interfaces":             "name",
		"v1alpha1.spec.interfaces[].addresses": "address",
	} {
		node, found := nodes[at]
		if !found {
			t.Errorf("%s is not in the generated schema", at)

			continue
		}

		if node["x-kubernetes-list-type"] != "map" {
			t.Errorf("%s is not a map-typed list, so the API server would accept two entries "+
				"with one key and the materialiser would report a Conflict instead", at)
		}

		keys, _ := node["x-kubernetes-list-map-keys"].([]any)
		if len(keys) != 1 || keys[0] != key {
			t.Errorf("%s has list-map-keys %v, want [%s] -- the key the child's name and path "+
				"are both derived from", at, keys, key)
		}
	}
}
