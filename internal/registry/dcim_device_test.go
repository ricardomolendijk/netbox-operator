package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestDeviceDescriptorIsRegisteredAndValid is the boot check.
func TestDeviceDescriptorIsRegisteredAndValid(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "dcim/devices" {
		t.Errorf("Endpoint = %q, want dcim/devices (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "dcim.device" {
		t.Errorf("ObjectType = %q, want dcim.device", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	if !d.Taggable || !d.CustomFieldable {
		t.Errorf("Taggable = %v, CustomFieldable = %v, want both true: dcim.Device is a "+
			"PrimaryModel and carries the whole provenance stamp", d.Taggable, d.CustomFieldable)
	}
}

// TestDeviceHasNoContainmentParent is the interesting half of this descriptor, and it is
// asserted rather than assumed because NBO-030's own spec asks for the opposite.
//
// ADR-0003 rule 4 as amended by #202: the containment parent is whichever FK the *server*
// cascades. dcim.Device has none -- `device_type`, `role`, `site` and `tenant` are `PROTECT`,
// `platform`, `cluster`, `primary_ip4`, `primary_ip6` and `oob_ip` are `SET_NULL`
// (docs/netbox-schema.md -> dcim.Device). The spec names `siteRef`; validateContainment
// refuses it at boot with ErrContainmentNotCascade, because an owner reference on a
// PROTECT-ed FK promises a cascade NetBox declines: garbage collection removes the CR, the
// finalizer's DELETE is refused, and the row outlives the object that described it.
//
// Both directions, because either alone is satisfiable by a lie: no containment ref, *and*
// no reference on this Kind claiming to cascade.
func TestDeviceHasNoContainmentParent(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	if d.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want empty: no FK on dcim.Device cascades", d.ContainmentRef)
	}

	for _, field := range d.Fields {
		if field.CascadeOnDelete {
			t.Errorf("%s declares CascadeOnDelete, but dcim.Device.%s is PROTECT or SET_NULL "+
				"(docs/netbox-schema.md -> dcim.Device)", field.Spec, field.API)
		}
	}
}

// TestDeviceNaturalKeysAreTheThreeReachableIdentities pins the candidate list and the two
// things about it that a lookup gets silently wrong.
//
// The `__ie` spelling, because both table-level constraints this Kind can reach are over
// `Lower('name')`: an exact filter reports `sw1` absent while NetBox holds `SW1` at that site,
// and the create that follows is answered with a 400 -- a loop where the lookup and the write
// disagree about what exists.
//
// And `site_id` on both name candidates, because device names are unique *per site*: a lookup
// without it finds the wrong device, or several, and `sw1` is the most-reused device name
// there is.
func TestDeviceNaturalKeysAreTheThreeReachableIdentities(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	want := []NaturalKey{
		{Fields: []KeyField{{Filter: "asset_tag", Spec: "assetTag"}}},
		{Fields: []KeyField{
			{Filter: "name", Spec: "name", Lookup: LookupIExact},
			{Filter: "site_id", Spec: "siteRef"},
			{Filter: "tenant_id", Spec: "tenantRef"},
		}},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "site_id", Spec: "siteRef"},
			},
			NullFields: []NullField{{Filter: "tenant_id", Spec: "tenantRef"}},
		},
	}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}

	// The spelling, once, because it is the whole of the case-insensitive claim.
	if got := d.NaturalKeys[1].Fields[0].Param(); got != "name__ie" {
		t.Errorf("the name filter renders as %q, want name__ie", got)
	}
}

// TestDeviceCandidatesDistinguishTheTenantVariants is the acceptance criterion about two
// devices sharing a name in one site, one with a tenant and one without.
//
// Candidate 2 needs `tenantRef` resolved; candidate 3 asserts it was never declared. So the
// two shapes select different candidate sets and neither can adopt the other -- and a device
// whose tenant exists in the manifest but not yet in NetBox matches *nothing*, which is the
// correct outcome: with `tenant_id` merely omitted it would match by name and site, adopt the
// tenant-less device, and the follow-up PATCH would move somebody else's device into this
// tenant.
func TestDeviceCandidatesDistinguishTheTenantVariants(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	for _, tc := range []struct {
		name  string
		state SpecState
		want  []NaturalKey
	}{
		{
			name: "no tenant declared",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: []NaturalKey{d.NaturalKeys[2]},
		},
		{
			name: "tenant declared and resolved",
			state: SpecState{
				Declared: []string{"name", "siteRef", "tenantRef"},
				Resolved: []string{"name", "siteRef", "tenantRef"},
			},
			want: []NaturalKey{d.NaturalKeys[1]},
		},
		{
			name: "tenant declared and not resolved yet",
			state: SpecState{
				Declared: []string{"name", "siteRef", "tenantRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: nil,
		},
		{
			name: "asset tag set, and it wins",
			state: SpecState{
				Declared: []string{"name", "siteRef", "assetTag"},
				Resolved: []string{"name", "siteRef", "assetTag"},
			},
			want: []NaturalKey{d.NaturalKeys[0], d.NaturalKeys[2]},
		},
		{
			name: "site not resolved yet: nothing is usable",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Candidates(tc.state)

			if len(got) != len(tc.want) {
				t.Fatalf("Candidates() returned %d candidates, want %d: %+v", len(got), len(tc.want), got)
			}

			for i := range got {
				if !reflect.DeepEqual(got[i], tc.want[i]) {
					t.Errorf("candidate %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDeviceDefersAllThreeAddressesUnconditionally is the payload claim: none of the three
// one-to-ones reaches the POST.
//
// The ring is `Device -> IPAddress -> Interface -> Device`, so DeferAlways rather than
// DeferIfUnresolved -- there is no first pass in which any of them could resolve. Legal
// because none of the three columns is matched on by any candidate, which is what
// validateDeferred enforces and what the natural-key assertion above pins from the other side.
func TestDeviceDefersAllThreeAddressesUnconditionally(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	want := []DeferredField{
		{APIField: "primary_ip4", Mode: DeferAlways},
		{APIField: "primary_ip6", Mode: DeferAlways},
		{APIField: "oob_ip", Mode: DeferAlways},
	}

	if !reflect.DeepEqual(d.Deferred, want) {
		t.Errorf("Deferred = %+v, want %+v", d.Deferred, want)
	}
}

// TestDeviceReadOnlyExcludesEveryCounterAndTheReverseRelation is the PATCH-loop guard.
//
// NetBox maintains each counter from the child rows and ignores an attempt to set one, so
// writing it does not fail -- it silently no-ops, the next reconcile finds the same
// difference, and the operator PATCHes forever. `services` is a GenericRelation and goes the
// same way.
//
// Ten counters and not the eleven NBO-030's spec counts: the eleventh is dcim.DeviceType's
// `device_count`, which lives on the other model
// (`netbox/dcim/models/devices.py` lines 694-733 against line 188).
func TestDeviceReadOnlyExcludesEveryCounterAndTheReverseRelation(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	for _, column := range []string{
		"created", "last_updated", "url", "display", "services",
		"console_port_count", "console_server_port_count", "power_port_count",
		"power_outlet_count", "interface_count", "front_port_count", "rear_port_count",
		"device_bay_count", "module_bay_count", "inventory_item_count",
	} {
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%s is not in ReadOnly; writing it is a PATCH the operator repeats forever", column)
		}
	}

	counters := 0

	for _, column := range d.ReadOnly {
		if len(column) > 6 && column[len(column)-6:] == "_count" {
			counters++
		}
	}

	if counters != 10 {
		t.Errorf("ReadOnly names %d *_count columns, want 10 (docs/netbox-schema.md -> dcim.Device)",
			counters)
	}
}

// TestDeviceFieldMapNamesTheColumnsAConventionGetsWrong is the reason Descriptor.Fields is a
// table rather than a naming rule.
//
// A camelCase-to-snake_case convention renders `primaryIP4Ref` as `primary_i_p4` and `oobIPRef`
// as `oob_i_p`, and NetBox answers a column it does not know with a 201 that writes nothing --
// so the operator would report success forever while the device had no primary address.
// `roleRef` is the other kind of wrong: the name is right and the target Kind is not, because
// `dcim.DeviceRole` and `ipam.Role` are two models with two endpoints.
func TestDeviceFieldMapNamesTheColumnsAConventionGetsWrong(t *testing.T) {
	d := descriptorFor(t, "NetBoxDevice")

	for spec, api := range map[string]string{
		"deviceTypeRef": "device_type",
		"primaryIP4Ref": "primary_ip4",
		"primaryIP6Ref": "primary_ip6",
		"oobIPRef":      "oob_ip",
		"assetTag":      "asset_tag",
		"roleRef":       "role",
	} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Errorf("no %s in the field map", spec)

			continue
		}

		if field.API != api {
			t.Errorf("%s writes %q, want %q", spec, field.API, api)
		}
	}

	role, _ := d.FieldFor("roleRef")
	if role.Target.Kind != "NetBoxDeviceRole" {
		t.Errorf("roleRef targets %s, want NetBoxDeviceRole: dcim.Device.role is a ForeignKey "+
			"to dcim.DeviceRole, not to the ipam.Role that RoleRef names", role.Target.Kind)
	}

	// The clearable nullable columns. `assetTag` is the one that is not obvious: the column is
	// UNIQUE and nullable, so two devices with an empty-string tag would collide where two
	// with null do not.
	for _, spec := range []string{"assetTag", "latitude", "longitude"} {
		field, _ := d.FieldFor(spec)
		if !field.EmptyIsNull {
			t.Errorf("%s does not declare EmptyIsNull, so an emptied value is sent as \"\" "+
				"to a nullable column", spec)
		}
	}
}
