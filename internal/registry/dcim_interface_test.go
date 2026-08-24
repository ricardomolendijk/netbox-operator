package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestInterfaceDescriptorIsRegisteredAndValid is the boot check.
func TestInterfaceDescriptorIsRegisteredAndValid(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	// Looked up, not pluralised. dcim.Interface happens to live at the pluralisation of its
	// own name and virtualization.VMInterface does not, which is why Endpoint is data.
	if d.Endpoint != "dcim/interfaces" {
		t.Errorf("Endpoint = %q, want dcim/interfaces (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	if !d.Taggable || !d.CustomFieldable {
		t.Errorf("Taggable = %v, CustomFieldable = %v, want both true: dcim.ComponentModel is a "+
			"NetBoxModel, which mixes in TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
	}
}

// TestInterfaceClosesTheIPAssignmentUnionMember is what this Kind exists for, asserted through
// the reverse index rather than through the field map.
//
// `IPAssignment.interfaceRef` has named `NetBoxInterface` since NBO-011 and nothing registered
// it, so `ByObjectType("dcim.interface")` had no entry and every use of the member reported
// RefKindUnavailable. Registering the object type is the whole of the fix: Registry.Add builds
// the reverse index from it, and internal/resolver's RefTargets picks the Kind up as a watch
// target from there (docs/concepts/generic-refs.md, NBO-019).
//
// The other two members are asserted too, and one of them deliberately fails to resolve. That
// is the honest state of the union on this branch: `virtualization.vminterface` arrives with
// NBO-029 and `ipam.fhrpgroup` has no ticket in M4 at all, so this Kind closes *one* of the
// three and the test says which.
func TestInterfaceClosesTheIPAssignmentUnionMember(t *testing.T) {
	d, ok := ByObjectType("dcim.interface")
	if !ok {
		t.Fatal("ByObjectType(\"dcim.interface\") found nothing; IPAssignment.interfaceRef " +
			"still reports RefKindUnavailable")
	}

	if want := (netboxv1alpha1.InterfaceRef{}).TargetGVK(); d.GVK != want {
		t.Errorf("dcim.interface resolves to %s, want %s", d.GVK, want)
	}

	// The rest of the union, so the count is a fact in the repository rather than a claim in
	// a pull-request description.
	for objectType, wantRegistered := range map[string]bool{
		"dcim.interface": true,
		// NBO-029 landed while this branch was in flight, so this one is true now too. The
		// table is written to be updated as each member arrives rather than to assert a
		// moment -- two of three, and `ipam.fhrpgroup` has no Kind and no M4 ticket.
		"virtualization.vminterface": true,
		"ipam.fhrpgroup":             false,
	} {
		if _, got := ByObjectType(objectType); got != wantRegistered {
			t.Errorf("ByObjectType(%q) registered = %v, want %v", objectType, got, wantRegistered)
		}
	}
}

// TestInterfaceNaturalKeyComesFromTheParentModel pins the one candidate and the two things
// about it that are easy to get backwards.
//
// `dcim.Interface` lists no meta.constraints of its own -- only
// `meta.ordering: ('device', CollateAsChar('_name'))` -- and `dcim.ComponentModel` carries
// `UniqueConstraint(fields=('device', 'name'))` (docs/netbox-schema.md). So `device_id` is
// never omitted, and the name filter is **exact**: there is no `Lower()` in that constraint,
// unlike both of dcim.Device's, so `Eth0` and `eth0` are two interfaces on one device and the
// lookup must not merge them.
func TestInterfaceNaturalKeyComesFromTheParentModel(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	want := []NaturalKey{{Fields: []KeyField{
		{Filter: "device_id", Spec: "deviceRef"},
		{Filter: "name", Spec: "name"},
	}}}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	if got := d.NaturalKeys[0].Fields[1].Param(); got != "name" {
		t.Errorf("the name filter renders as %q, want an exact `name`: dcim.ComponentModel's "+
			"constraint has no Lower(), unlike dcim.Device's", got)
	}

	// Nothing is usable until the device resolves, which is what makes the operator wait
	// rather than look `eth0` up across every device in NetBox.
	if got := d.Candidates(SpecState{
		Declared: []string{"name", "deviceRef"},
		Resolved: []string{"name"},
	}); len(got) != 0 {
		t.Errorf("Candidates() returned %d candidates with the device unresolved, want 0", len(got))
	}
}

// TestInterfaceContainmentIsTheDeviceAndOnlyTheDevice is ADR-0003 rule 4 as amended by #202
// from the other side of the mirror from NetBoxDevice's.
//
// `dcim.ComponentModel.device` is `on_delete=CASCADE`, so it is the one FK here that qualifies
// and the one this descriptor declares. Every other reference is `SET_NULL`, `RESTRICT` or
// `PROTECT`, and declaring the flag truthfully on each is what makes validateContainment an
// enforcement rather than a convention -- a Kind whose only cascading FK is left undeclared is
// a Kind that silently gets no containment parent.
func TestInterfaceContainmentIsTheDeviceAndOnlyTheDevice(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	if d.ContainmentRef != "deviceRef" {
		t.Errorf("ContainmentRef = %q, want deviceRef", d.ContainmentRef)
	}

	for _, field := range d.Fields {
		cascades := field.Spec == "deviceRef"

		if field.CascadeOnDelete != cascades {
			t.Errorf("%s (-> %s) declares CascadeOnDelete = %v, want %v "+
				"(docs/netbox-schema.md -> dcim.Interface, dcim.BaseInterface, dcim.ComponentModel)",
				field.Spec, field.API, field.CascadeOnDelete, cascades)
		}
	}
}

// TestInterfaceDefersThreeSelfReferencesAndTheServiceVLAN is the deferral claim.
//
// `lag`, `parent` and `bridge` are all `-> dcim.Interface`, so two interfaces of one device
// naming each other cannot both be created with the reference in place. IfUnresolved rather
// than Always for each: unconditional deferral would turn every ordinary sub-interface and
// every ordinary LAG member into two writes with a visible intermediate state where it is
// briefly top-level or unbonded (NBO-015).
//
// The self-reference half is asserted through the field map rather than restated, so a target
// changed to something else fails here.
func TestInterfaceDefersThreeSelfReferencesAndTheServiceVLAN(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	want := []DeferredField{
		{APIField: "lag", Mode: DeferIfUnresolved},
		{APIField: "parent", Mode: DeferIfUnresolved},
		{APIField: "bridge", Mode: DeferIfUnresolved},
		{APIField: "qinq_svlan", Mode: DeferIfUnresolved},
	}

	if !reflect.DeepEqual(d.Deferred, want) {
		t.Fatalf("Deferred = %+v, want %+v", d.Deferred, want)
	}

	self := (netboxv1alpha1.InterfaceRef{}).TargetGVK()

	for _, spec := range []string{"lagRef", "parentRef", "bridgeRef"} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Errorf("no %s in the field map", spec)

			continue
		}

		if field.Target != self {
			t.Errorf("%s targets %s, want %s: all three are `-> dcim.Interface`",
				spec, field.Target, self)
		}
	}
}

// TestInterfaceTaggedVLANsIsTheOnlyToManyReference pins the cardinality that decides the
// comparison rule.
//
// M2MFields() is derived from the field class rather than declared beside it, which is what
// makes the NBO-088 contradiction unrepresentable: there is no second list to disagree with a
// field about how many objects it holds. NetBox does not preserve M2M order, so comparing
// `tagged_vlans` order-sensitively would PATCH forever.
func TestInterfaceTaggedVLANsIsTheOnlyToManyReference(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	if got := d.M2MFields(); !slices.Equal(got, []string{"tagged_vlans"}) {
		t.Errorf("M2MFields() = %v, want [tagged_vlans]", got)
	}

	if got := d.ArrayFields(); len(got) != 0 {
		t.Errorf("ArrayFields() = %v, want none: `cable_positions` is the only ArrayField on "+
			"dcim.Interface and NetBoxCable owns it (NBO-049), so it is read-only here", got)
	}
}

// TestInterfaceReadOnlyCoversTheCachesTheCableAndEveryReverseRelation is the PATCH-loop guard,
// and it is longer here than on any other Kind because dcim.Interface has twenty such columns.
//
// Three groups, three reasons. The underscore-prefixed columns are server-maintained caches --
// `_name` is the natural ordering NetBox derives from `name` so `eth10` sorts after `eth9`,
// `_site`/`_location`/`_rack` are denormalised from the device, `_path` is recomputed from the
// cable graph. The `cable*` columns are writable and belong to NetBoxCable (NBO-049): an
// interface that adopted a cabled peer must not PATCH the cable away. The rest are
// GenericRelations, which are queries rather than columns.
func TestInterfaceReadOnlyCoversTheCachesTheCableAndEveryReverseRelation(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	for _, column := range []string{
		"created", "last_updated", "url", "display",
		"_name", "_site", "_location", "_rack", "_path",
		"cable", "cable_end", "cable_connector", "cable_positions", "cable_terminations",
		"ip_addresses", "mac_addresses", "fhrp_group_assignments",
		"tunnel_terminations", "l2vpn_terminations", "inventory_items",
	} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%s is not in ReadOnly; NetBox drops it on write, so the next reconcile "+
				"finds the same difference and the operator PATCHes forever", column)
		}
	}

	// `ip_addresses` is the reverse of the union this Kind exists to make resolvable, and the
	// direction is the point: an address points at an interface, an interface does not list
	// its addresses. A field map entry for it would be a write NetBox silently discards.
	if _, mapped := d.FieldFor("ipAddresses"); mapped {
		t.Error("the field map declares ipAddresses; dcim.Interface.ip_addresses is a " +
			"GenericRelation, which is a query rather than a column")
	}
}

// TestInterfaceOmitsTheColumnsWhoseKindsDoNotExist is the "absent, not accepted-and-dropped"
// rule, asserted as absence.
//
// NetBox ignores a field name it does not know rather than rejecting it, so a spec field for a
// column this Kind cannot yet write would report success while writing nothing -- which is
// strictly worse than the field not being there. Each of these waits on a Kind: dcim.Module
// (NBO-053), dcim.VirtualDeviceContext (NBO-060), wireless.WirelessLink and
// wireless.WirelessLAN (NBO-050), ipam.VLANTranslationPolicy (NBO-055), dcim.MACAddress
// (NBO-048).
func TestInterfaceOmitsTheColumnsWhoseKindsDoNotExist(t *testing.T) {
	d := descriptorFor(t, "NetBoxInterface")

	for _, api := range []string{
		"module", "vdcs", "wireless_link", "wireless_lans",
		"vlan_translation_policy", "primary_mac_address", "mac_address",
	} {
		if slices.ContainsFunc(d.Fields, func(f Field) bool { return f.API == api }) {
			t.Errorf("the field map writes %q, whose target Kind does not exist yet", api)
		}
	}
}
