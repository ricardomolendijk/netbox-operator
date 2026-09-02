package registry

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

func virtualMachineDescriptor(t *testing.T) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxVirtualMachine")

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() in virtualization_virtualmachine.go did not run", gvk)
	}

	return d
}

// TestVirtualMachineDescriptorIsRegisteredAndValid is the boot check.
func TestVirtualMachineDescriptorIsRegisteredAndValid(t *testing.T) {
	d := virtualMachineDescriptor(t)

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "virtualization/virtual-machines" {
		t.Errorf("Endpoint = %q, want virtualization/virtual-machines", d.Endpoint)
	}

	if d.ObjectType != "virtualization.virtualmachine" {
		t.Errorf("ObjectType = %q, want virtualization.virtualmachine", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// Exactly one containment parent, and it is the cluster. `siteRef` is deliberately not a
	// second: garbage collection waits for every owner, so two would turn "delete the
	// cluster" into "delete the cluster and the site".
	// Empty. Every FK a VM could be owned by is on_delete=PROTECT -- cluster, site, device,
	// tenant -- so none cascades, and NBO-193 refuses a containment ref on a PROTECT-ed FK at
	// boot. This asserted "clusterRef" until that check landed, and the check was right: an
	// owner reference there would promise a cascade the server declines.
	if d.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want empty: no FK on a VM cascades", d.ContainmentRef)
	}
}

// TestVirtualMachineNaturalKeysAreTheFourConstraints is the identity claim, and it is the
// most detailed one in the registry because the constraints are.
//
// docs/netbox-schema.md -> virtualization.VirtualMachine, meta.constraints, in order:
//
//	UniqueConstraint(Lower('name'), 'cluster', 'tenant', name='..._unique_name_cluster_tenant')
//	UniqueConstraint(Lower('name'), 'cluster', name='..._unique_name_cluster',
//	                 condition=Q(tenant__isnull=True))
//	UniqueConstraint(Lower('name'), 'device', 'tenant', name='..._unique_name_device_tenant',
//	                 condition=Q(cluster__isnull=True, device__isnull=False))
//	UniqueConstraint(Lower('name'), 'device', name='..._unique_name_device',
//	                 condition=Q(cluster__isnull=True, device__isnull=False, tenant__isnull=True))
//
// Every `__isnull` in a condition is a NullField here, never an omitted filter, and every
// `Lower('name')` is a `name__ie` lookup. The fifth candidate is the site-only convention,
// which no constraint backs and which is asserted here so that its being a convention is
// written down in a place that fails when somebody changes it.
func TestVirtualMachineNaturalKeysAreTheFourConstraints(t *testing.T) {
	d := virtualMachineDescriptor(t)

	name := KeyField{Filter: "name", Spec: "name", Lookup: LookupIExact}
	want := []NaturalKey{
		{Fields: []KeyField{
			name,
			{Filter: "cluster_id", Spec: "clusterRef"},
			{Filter: "tenant_id", Spec: "tenantRef"},
		}},
		{
			Fields:     []KeyField{name, {Filter: "cluster_id", Spec: "clusterRef"}},
			NullFields: []NullField{{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef}},
		},
		{
			Fields: []KeyField{
				name,
				{Filter: "device_id", Spec: "deviceRef"},
				{Filter: "tenant_id", Spec: "tenantRef"},
			},
			NullFields: []NullField{{Filter: "cluster_id", Spec: "clusterRef", Column: NullColumnRef}},
		},
		{
			Fields: []KeyField{name, {Filter: "device_id", Spec: "deviceRef"}},
			NullFields: []NullField{
				{Filter: "cluster_id", Spec: "clusterRef", Column: NullColumnRef},
				{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef},
			},
		},
		{
			Fields: []KeyField{name, {Filter: "site_id", Spec: "siteRef"}},
			NullFields: []NullField{
				{Filter: "cluster_id", Spec: "clusterRef", Column: NullColumnRef},
				{Filter: "device_id", Spec: "deviceRef", Column: NullColumnRef},
			},
		},
	}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Fatalf("NaturalKeys = %+v,\nwant %+v", d.NaturalKeys, want)
	}
}

// TestVirtualMachineNameFilterIsCaseInsensitive is the spelling assertion, separated out
// because it is the one that costs real data when it is wrong.
//
// `Lower('name')` in the constraint plus `?name=dns` in the lookup means the operator reports
// `dns` absent while NetBox holds `DNS` in that cluster, creates it, and NetBox answers 400 --
// a loop where the read and the write disagree about what exists
// (docs/concepts/lookups.md).
func TestVirtualMachineNameFilterIsCaseInsensitive(t *testing.T) {
	for i, key := range virtualMachineDescriptor(t).NaturalKeys {
		for _, field := range key.Fields {
			if field.Spec != "name" {
				continue
			}

			if got := field.Param(); got != "name__ie" {
				t.Errorf("candidate %d matches name as %q, want name__ie", i, got)
			}
		}
	}
}

// TestVirtualMachineCandidatesKeepTheHostingCasesApart is the behaviour behind the five
// candidates: exactly one applies to each real shape of VM, and the ambiguous states produce
// none so that the engine waits rather than adopting the wrong row.
func TestVirtualMachineCandidatesKeepTheHostingCasesApart(t *testing.T) {
	d := virtualMachineDescriptor(t)

	for _, tc := range []struct {
		name  string
		state SpecState
		want  int
	}{
		{
			name: "cluster and tenant: constraint 1",
			state: SpecState{
				Declared: []string{"name", "clusterRef", "tenantRef"},
				Resolved: []string{"name", "clusterRef", "tenantRef"},
			},
			want: 1,
		},
		{
			name: "cluster, no tenant: constraint 2, with tenant_id pinned null",
			state: SpecState{
				Declared: []string{"name", "clusterRef"},
				Resolved: []string{"name", "clusterRef"},
			},
			want: 1,
		},
		{
			name: "device and tenant, no cluster: constraint 3",
			state: SpecState{
				Declared: []string{"name", "deviceRef", "tenantRef"},
				Resolved: []string{"name", "deviceRef", "tenantRef"},
			},
			want: 1,
		},
		{
			name: "device only: constraint 4",
			state: SpecState{
				Declared: []string{"name", "deviceRef"},
				Resolved: []string{"name", "deviceRef"},
			},
			want: 1,
		},
		{
			name: "site only: the convention, and the reason it has to exist",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: 1,
		},
		{
			// The case that has to produce nothing. The cluster is wanted and does not
			// exist yet, so falling through to the tenant-is-null or the site candidate
			// would adopt a different VM and then PATCH a cluster onto it (NBO-015).
			name: "clusterRef declared and unresolved: none, so the engine waits",
			state: SpecState{
				Declared: []string{"name", "clusterRef", "siteRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: 0,
		},
		{
			// A tenant that has not been created yet must not fall through to candidate 2:
			// that candidate is the identity of the *tenant-less* VM of this name.
			name: "tenantRef declared and unresolved: none",
			state: SpecState{
				Declared: []string{"name", "clusterRef", "tenantRef"},
				Resolved: []string{"name", "clusterRef"},
			},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Candidates(tc.state); len(got) != tc.want {
				t.Errorf("Candidates() returned %d candidates (%+v), want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestVirtualMachineFieldMapCoversEverySpecField guards what the explicit table exists for:
// NetBox ignores a field name it does not recognise, so `startOnBoot` or `primaryIP4Ref` sent
// verbatim would write nothing and report success.
func TestVirtualMachineFieldMapCoversEverySpecField(t *testing.T) {
	d := virtualMachineDescriptor(t)

	want := map[string]string{
		"name":             "name",
		"status":           "status",
		"startOnBoot":      "start_on_boot",
		"vcpus":            "vcpus",
		"memory":           "memory",
		"disk":             "disk",
		"serial":           "serial",
		"description":      "description",
		"comments":         "comments",
		"localContextData": "local_context_data",
		"clusterRef":       "cluster",
		"siteRef":          "site",
		"deviceRef":        "device",
		"roleRef":          "role",
		"platformRef":      "platform",
		"tenantRef":        "tenant",
		"primaryIP4Ref":    "primary_ip4",
		"primaryIP6Ref":    "primary_ip6",
	}

	got := map[string]string{}
	for _, f := range d.Fields {
		got[f.Spec] = f.API
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("field map = %v, want %v", got, want)
	}

	// `roleRef` is dcim.DeviceRole and not ipam.Role. They are separate models at separate
	// endpoints, and getting it wrong produces a reference that resolves against the wrong
	// catalogue.
	role, ok := d.FieldFor("roleRef")
	if !ok {
		t.Fatal("roleRef is not in the field map")
	}
	if want := (netboxv1alpha1.DeviceRoleRef{}).TargetGVK(); role.Target != want {
		t.Errorf("roleRef targets %s, want %s (docs/netbox-schema.md -> "+
			"virtualization.VirtualMachine, `role ForeignKey -> dcim.DeviceRole`)", role.Target, want)
	}

	// Nothing here is a to-many reference, an array or a content-type list: the VM's
	// many-to-many relations are all declared from the other side.
	for _, got := range [][]string{d.M2MFields(), d.ArrayFields(), d.ObjectTypeListFields()} {
		if len(got) != 0 {
			t.Errorf("comparison class set = %v, want none", got)
		}
	}
}

// TestVirtualMachineCounterCachesAreReadOnly is the anti-PATCH-loop declaration.
// `interface_count` and `virtual_disk_count` are CounterCacheFields NetBox maintains from the
// child rows; an attempt to set one is dropped rather than refused, so an undeclared one is a
// difference found again on every resync.
func TestVirtualMachineCounterCachesAreReadOnly(t *testing.T) {
	d := virtualMachineDescriptor(t)

	writable := map[string]bool{}
	for _, f := range d.Fields {
		writable[f.API] = true
	}

	for _, column := range []string{"interface_count", "virtual_disk_count"} {
		if writable[column] {
			t.Errorf("%q is writable, but NetBox maintains it and drops the write", column)
		}
		if !slices.Contains(d.ReadOnly, column) {
			t.Errorf("%q is not in ReadOnly, so NetBox's value would be diffed and PATCHed forever", column)
		}
	}

	// `mac_address` and `primary_mac_address` are not this model's columns at all -- NetBox
	// 4.2 moved the MAC to dcim.MACAddress -- and neither is `virtual_machine_type`, whose
	// Kind has no ticket. A field for either would be accepted and silently dropped.
	for _, column := range []string{"mac_address", "primary_mac_address", "virtual_machine_type"} {
		if writable[column] {
			t.Errorf("%q is writable on virtualization.VirtualMachine, which has no such writable column", column)
		}
	}
}

// TestVirtualMachineDefersBothPrimaryAddresses is the ring assertion.
//
// `VM -> IPAddress -> VMInterface -> VM` has no apply order that satisfies it, so both
// columns are DeferAlways rather than DeferIfUnresolved: there is no first pass in which
// either could resolve. Validate accepts it because no candidate matches on either column --
// which is asserted from the other direction by flipping a *key* field to DeferAlways and
// requiring the rejection.
func TestVirtualMachineDefersBothPrimaryAddresses(t *testing.T) {
	d := virtualMachineDescriptor(t)

	want := []DeferredField{
		{APIField: "primary_ip4", Mode: DeferAlways},
		{APIField: "primary_ip6", Mode: DeferAlways},
	}
	if !reflect.DeepEqual(d.Deferred, want) {
		t.Fatalf("Deferred = %+v, want %+v", d.Deferred, want)
	}

	candidate := d
	candidate.Deferred = []DeferredField{{APIField: "cluster", Mode: DeferAlways}}

	if err := candidate.Validate(); !errors.Is(err, ErrDeferredNaturalKey) {
		t.Errorf("Validate() = %v, want %v: deferring `cluster` unconditionally would create "+
			"the VM under an identity the lookup never asked about", err, ErrDeferredNaturalKey)
	}
}

// TestVirtualMachineDriftsCleanlyAgainstNetBoxsReadShape is the anti-hot-loop assertion at
// the value level, and `vcpus` is the reason it exists.
//
// NetBox returns a DecimalField as a canonicalised string, so `"2"` comes back as `"2.00"`.
// They are the same number and different strings; Drift compares numerically, so a second
// reconcile finds nothing to do. A float64 spec field would have produced a third spelling of
// the same value and a PATCH every time.
func TestVirtualMachineDriftsCleanlyAgainstNetBoxsReadShape(t *testing.T) {
	sent := netbox.Object{
		"name": "dns", "status": "active", "start_on_boot": "off",
		"vcpus": "2", "memory": float64(2048), "disk": nil,
		"serial": "", "description": "", "comments": "",
		"cluster": float64(7), "site": float64(41),
		"device": nil, "role": nil, "platform": nil, "tenant": nil,
	}
	live := netbox.Object{
		"name":          "dns",
		"status":        map[string]any{"value": "active", "label": "Active"},
		"start_on_boot": map[string]any{"value": "off", "label": "Off"},
		// The whole point: a padded decimal against an unpadded spec.
		"vcpus":  "2.00",
		"memory": float64(2048), "disk": nil,
		"serial": "", "description": "", "comments": "",
		"cluster":  map[string]any{"id": float64(7), "name": "proxmox-home"},
		"site":     map[string]any{"id": float64(41), "name": "Home"},
		"device":   nil,
		"role":     nil,
		"platform": nil,
		"tenant":   nil,
		// The columns the operator must never manage, returned on every read.
		"interface_count":    float64(3),
		"virtual_disk_count": float64(1),
		"primary_ip4":        nil,
		"primary_ip6":        nil,
		"created":            "2026-08-21T10:00:00Z",
		"last_updated":       "2026-08-21T10:00:00Z",
	}

	if drift := netbox.Drift(live, sent, netbox.FieldRules{}); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}

// TestVirtualMachineInlineFieldsAreNoColumn is the descriptor half of NBO-033, and it is an
// assertion about an *absence*.
//
// `interfaces` and `disks` are inline child declarations rather than NetBox columns, and the
// engine learns which spec fields those are from the Kind's own InlineChildren() -- not from a
// list on the Descriptor (specFields.dropInlineChildren). So the thing to hold here is that the
// field map does not claim them: an entry for either would render a list of child objects into
// a payload as a column NetBox has never heard of, which NetBox drops rather than rejects, and
// the write would report success having stored nothing.
//
// The names are read off the Kind rather than written out, so a third inline list is covered
// without an edit here.
func TestVirtualMachineInlineFieldsAreNoColumn(t *testing.T) {
	d := virtualMachineDescriptor(t)

	sets := (&netboxv1alpha1.NetBoxVirtualMachine{}).InlineChildren()
	if len(sets) == 0 {
		t.Fatal("NetBoxVirtualMachine declares no inline child sets, so this proves nothing")
	}

	for _, set := range sets {
		if set.Field == "" {
			t.Error("an inline child set names no spec field, so nothing would exclude it")

			continue
		}

		if field, mapped := d.FieldFor(set.Field); mapped {
			t.Errorf("spec.%s is an inline child list *and* mapped onto the netbox column %q",
				set.Field, field.API)
		}
	}

	// The other half of the same fact: the inline lists are real fields on the CR, read through
	// the encoding the engine reads a spec with. A set naming a field that does not exist would
	// exclude nothing, and the real one would then be refused as unmapped.
	encoded, err := json.Marshal(netboxv1alpha1.NetBoxVirtualMachine{
		Spec: netboxv1alpha1.NetBoxVirtualMachineSpec{
			Interfaces: []netboxv1alpha1.InlineVMInterface{{Name: "eth0"}},
			Disks:      []netboxv1alpha1.InlineVirtualDisk{{Name: "scsi0", Size: 1}},
		},
	})
	if err != nil {
		t.Fatalf("encoding a virtual machine: %v", err)
	}

	var decoded struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding the spec: %v", err)
	}

	for _, set := range sets {
		if _, present := decoded.Spec[set.Field]; !present {
			t.Errorf("an inline child set names spec.%s, which is not a field on the CR: %v",
				set.Field, slices.Sorted(maps.Keys(decoded.Spec)))
		}
	}
}
