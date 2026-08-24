package registry

import (
	"errors"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func testGVK(kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "netbox.kubeforge.org", Version: "v1alpha1", Kind: kind}
}

// tagDescriptor is extras.Tag: the smallest real descriptor, and the one that exercises
// ObjectTypeLists. object_types is a ManyToManyField -> contenttypes.ContentType whose API
// values are "app_label.model" strings (docs/netbox-schema.md -> extras.Tag).
func tagDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxTag"),
		Endpoint:   "extras/tags",
		ObjectType: "extras.tag",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "description", API: "description"},
			{Spec: "weight", API: "weight"},
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
		},
		NaturalKeys:    []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		UpdateStrategy: UpdatePatch,
		ReadOnly:       []string{"created", "last_updated", "url", "display"},
	}
}

// vrfDescriptor is ipam.VRF: `rd` is column-unique and optional, `name` is required and
// not unique (docs/netbox-schema.md -> ipam.VRF), so identity is `rd` when set and `name`
// otherwise — the ordered-candidate case in its simplest form.
func vrfDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxVRF"),
		Endpoint:   "ipam/vrfs",
		ObjectType: "ipam.vrf",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "rd", API: "rd"},
			{Spec: "enforceUnique", API: "enforce_unique"},
			{Spec: "description", API: "description"},
			{Spec: "tenantRef", API: "tenant", Class: ClassRefOne},
			// The two to-many references NBO-088 was filed for. `import_targets` and
			// `export_targets` are ManyToManyFields onto ipam.RouteTarget
			// (docs/netbox-schema.md -> ipam.VRF), so one class says both that each is a list
			// of references to resolve and that its value compares as an id set.
			{Spec: "importTargets", API: "import_targets", Class: ClassRefMany},
			{Spec: "exportTargets", API: "export_targets", Class: ClassRefMany},
		},
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "rd", Spec: "rd"}}},
			{Fields: []KeyField{{Filter: "name", Spec: "name"}}},
		},
		UpdateStrategy: UpdatePatch,
		ReadOnly:       []string{"created", "last_updated", "url", "display"},
	}
}

// regionDescriptor is dcim.Region: unique on (parent, name) plus a separate
// `name WHERE parent IS NULL` (docs/netbox-schema.md -> dcim.Region.meta.constraints).
func regionDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxRegion"),
		Endpoint:   "dcim/regions",
		ObjectType: "dcim.region",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			// `on_delete=CASCADE`, which is what makes it the containment parent below.
			{Spec: "parentRef", API: "parent", Class: ClassRefOne, CascadeOnDelete: true},
		},
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{
				{Filter: "parent_id", Spec: "parentRef"},
				{Filter: "name", Spec: "name"},
			}},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},
		UpdateStrategy: UpdatePatch,
		Deferred:       []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}},
		ReadOnly:       []string{"_depth", "_children", "created", "last_updated", "url", "display"},
		ContainmentRef: "parentRef",
	}
}

// deviceDescriptor is dcim.Device: unique on (Lower('name'), site, tenant) with a
// tenant-is-null variant (docs/netbox-schema.md -> dcim.Device.meta.constraints), so the
// name filter needs the case-insensitive lookup and the tenant filter needs a null pin.
func deviceDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxDevice"),
		Endpoint:   "dcim/devices",
		ObjectType: "dcim.device",
		Scope:      apiextensionsv1.NamespaceScoped,
		// The four deferred fields are why this table cannot be a convention:
		// `primaryIP4Ref` camelCase-to-snake_case's to `primary_i_p4`, and NetBox ignores
		// an unknown field rather than rejecting it, so the mistake would be silent.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "status", API: "status"},
			{Spec: "siteRef", API: "site", Class: ClassRefOne},
			{Spec: "tenantRef", API: "tenant", Class: ClassRefOne},
			{Spec: "roleRef", API: "role", Class: ClassRefOne},
			{Spec: "deviceTypeRef", API: "device_type", Class: ClassRefOne},
			{Spec: "primaryIP4Ref", API: "primary_ip4", Class: ClassRefOne},
			{Spec: "primaryIP6Ref", API: "primary_ip6", Class: ClassRefOne},
			{Spec: "oobIPRef", API: "oob_ip", Class: ClassRefOne},
			{Spec: "virtualChassisRef", API: "virtual_chassis", Class: ClassRefOne},
		},
		NaturalKeys: []NaturalKey{
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
				NullFields: []NullField{{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef}},
			},
		},
		UpdateStrategy: UpdatePatch,
		Deferred: []DeferredField{
			{APIField: "primary_ip4", Mode: DeferAlways},
			{APIField: "primary_ip6", Mode: DeferAlways},
			{APIField: "oob_ip", Mode: DeferAlways},
			{APIField: "virtual_chassis", Mode: DeferAlways},
		},
		ReadOnly: []string{"console_port_count", "interface_count", "created", "last_updated", "url", "display"},

		// No containment parent, and this is the Kind that shows why the rule is mechanical
		// rather than intuitive. `site` reads like a device's container -- ADR-0003's prose
		// used to name it -- but `dcim.Device.site` is `on_delete=PROTECT`, and so is
		// `device_type`, `role`, `tenant` and `location`; `platform` is `SET_NULL`
		// (docs/netbox-schema.md -> dcim.Device). Not one of them cascades, so under the
		// cascade rule dcim.Device has no containment parent: NetBox refuses to delete a site
		// that still has devices, so there is no server-side deletion for an owner reference
		// to mirror, and an owner reference here would delete the CR while the row stayed
		// (docs/concepts/ownership.md, "when no foreign key qualifies").
	}
}

// ipAddressDescriptor is ipam.IPAddress, which has no meta.constraints at all
// (docs/netbox-schema.md -> ipam.IPAddress lists indexes only). Identity is a convention
// expressed as four candidates in priority order: the assignment disambiguates shared
// addresses, and every candidate pins `vrf_id` rather than omitting it, or a global
// address adopts the identical address out of some VRF.
func ipAddressDescriptor() Descriptor {
	address := KeyField{Filter: "address", Spec: "address"}
	vrf := KeyField{Filter: "vrf_id", Spec: "vrfRef"}
	noVRF := NullField{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef}
	assigned := []KeyField{
		{Filter: "assigned_object_type", Spec: "assignedObject"},
		{Filter: "assigned_object_id", Spec: "assignedObject"},
	}

	return Descriptor{
		GVK:        testGVK("NetBoxIPAddress"),
		Endpoint:   "ipam/ip-addresses",
		ObjectType: "ipam.ipaddress",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "address", API: "address"},
			{Spec: "status", API: "status"},
			{Spec: "dnsName", API: "dns_name"},
			{Spec: "vrfRef", API: "vrf", Class: ClassRefOne},
			{Spec: "natInsideRef", API: "nat_inside", Class: ClassRefOne},
		},
		NaturalKeys: []NaturalKey{
			{Fields: append([]KeyField{address, vrf}, assigned...)},
			{Fields: append([]KeyField{address}, assigned...), NullFields: []NullField{noVRF}},
			{Fields: []KeyField{address, vrf}},
			{Fields: []KeyField{address}, NullFields: []NullField{noVRF}},
		},
		UpdateStrategy: UpdatePatch,
		Deferred:       []DeferredField{{APIField: "nat_inside", Mode: DeferAlways}},
		ReadOnly:       []string{"created", "last_updated", "url", "display"},
		GenericFKs: []GenericFKSpec{{
			TypeField: "assigned_object_type",
			IDField:   "assigned_object_id",
			AllowedTypes: []string{
				"dcim.interface",
				"virtualization.vminterface",
				"ipam.fhrpgroup",
			},
			Spec: "assignedObject",
			// The union members as v1alpha1.IPAssignment declares them, each pinned to
			// the Kind by its own typed alias rather than by a GVK written out here --
			// so a renamed member or a re-aimed alias fails the descriptor rather than
			// silently resolving against the wrong Kind.
			//
			// NetBox deletes an interface's addresses with it, through the `ip_addresses`
			// GenericRelation on the interface models rather than through an `on_delete` on
			// this column. That is the cascade ADR-0003 rule 4 builds its whole argument on,
			// and it is stated per member because that is where NetBox declares it: all three
			// of dcim.Interface, virtualization.VMInterface and ipam.FHRPGroup carry
			// `ip_addresses GenericRelation` (docs/netbox-schema.md), so this union happens to
			// agree -- which the descriptor has to say member by member rather than assume.
			Members: []GenericFKMember{
				{Spec: "interfaceRef", Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
					CascadeOnDelete: ptr.To(true)},
				{Spec: "vmInterfaceRef", Target: netboxv1alpha1.VMInterfaceRef{}.TargetGVK(),
					CascadeOnDelete: ptr.To(true)},
				{Spec: "fhrpGroupRef", Target: netboxv1alpha1.FHRPGroupRef{}.TargetGVK(),
					CascadeOnDelete: ptr.To(true)},
			},
		}},

		// `assignedObject` and not `vrfRef`: `ipam.IPAddress.vrf` is `on_delete=PROTECT`
		// (docs/netbox-schema.md -> ipam.IPAddress), so NetBox refuses to delete a VRF that
		// still holds addresses and there is no server-side cascade for an owner reference to
		// mirror. The assignment is the one reference on this kind that does cascade.
		ContainmentRef: "assignedObject",
	}
}

// clusterDescriptor is virtualization.Cluster, unique on (group, name) and (_site, name)
// (docs/netbox-schema.md -> virtualization.Cluster.meta.constraints). `_site` is a
// CachedScopeMixin column the operator must never write but has to filter on, as
// `site_id` — so a natural key legitimately overlaps ReadOnly.
func clusterDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxCluster"),
		Endpoint:   "virtualization/clusters",
		ObjectType: "virtualization.cluster",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "status", API: "status"},
			{Spec: "typeRef", API: "type", Class: ClassRefOne},
			{Spec: "groupRef", API: "group", Class: ClassRefOne},
		},
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{
				{Filter: "group_id", Spec: "groupRef"},
				{Filter: "name", Spec: "name"},
			}},
			{Fields: []KeyField{
				{Filter: "site_id", Spec: "scope"},
				{Filter: "name", Spec: "name"},
			}},
		},
		UpdateStrategy: UpdatePatch,
		ReadOnly:       append(ScopeCacheColumns(), "created", "last_updated", "url", "display"),
		GenericFKs:     []GenericFKSpec{ScopeFK("scope", ScopeCascadesFromEvery())},

		// The containment parent, and every member of the union cascades -- by two mechanisms,
		// which is why the descriptor states it per member (#214). dcim.Region and
		// dcim.SiteGroup declare a `clusters` GenericRelation; dcim.Site and dcim.Location do
		// not need one, because dcim.CachedScopeMixin's `_site` and `_location` are
		// `on_delete=CASCADE` on this very model (docs/netbox-schema.md).
		ContainmentRef: "scope",
	}
}

// tenantGroupDescriptor is tenancy.TenantGroup: a NestedGroupModel with no
// meta.constraints, whose `name` and `slug` are column-level unique
// (docs/netbox-schema.md -> tenancy.TenantGroup). So it keys on `slug` alone and has no
// parent variant, which is why the parent-null case cannot be inferred from the base class.
func tenantGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxTenantGroup"),
		Endpoint:   "tenancy/tenant-groups",
		ObjectType: "tenancy.tenantgroup",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "parentRef", API: "parent", Class: ClassRefOne, CascadeOnDelete: true},
		},
		NaturalKeys:    []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		UpdateStrategy: UpdatePatch,
		Deferred:       []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}},
		ReadOnly:       []string{"_depth", "created", "last_updated", "url", "display"},
		ContainmentRef: "parentRef",
	}
}

func schemaDescriptors() []Descriptor {
	return []Descriptor{
		tagDescriptor(),
		vrfDescriptor(),
		regionDescriptor(),
		deviceDescriptor(),
		ipAddressDescriptor(),
		clusterDescriptor(),
		tenantGroupDescriptor(),
	}
}

func TestDescriptorValidateAcceptsSchemaDescriptors(t *testing.T) {
	for _, d := range schemaDescriptors() {
		t.Run(d.GVK.Kind, func(t *testing.T) {
			if err := d.Validate(); err != nil {
				t.Fatalf("validating %s: %v", d.GVK.Kind, err)
			}
		})
	}
}

func TestDescriptorValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Descriptor)
		wantErr error
	}{
		{
			name:   "unmodified",
			mutate: func(*Descriptor) {},
		},
		{
			name:    "empty endpoint",
			mutate:  func(d *Descriptor) { d.Endpoint = "" },
			wantErr: ErrNoEndpoint,
		},
		{
			name:    "no natural key",
			mutate:  func(d *Descriptor) { d.NaturalKeys = nil },
			wantErr: ErrNoNaturalKey,
		},
		{
			name: "candidate with only a null pin",
			mutate: func(d *Descriptor) {
				d.NaturalKeys = []NaturalKey{{NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}}}}
			},
			wantErr: ErrNoKeyFields,
		},
		{
			name: "field deferred and read-only",
			mutate: func(d *Descriptor) {
				d.ReadOnly = append(d.ReadOnly, "primary_ip4")
				d.Deferred = []DeferredField{{APIField: "primary_ip4", Mode: DeferAlways}}
			},
			wantErr: ErrDeferredReadOnly,
		},
		{
			name:    "empty update strategy",
			mutate:  func(d *Descriptor) { d.UpdateStrategy = "" },
			wantErr: ErrUnknownUpdateStrategy,
		},
		{
			name:    "invented update strategy",
			mutate:  func(d *Descriptor) { d.UpdateStrategy = "Upsert" },
			wantErr: ErrUnknownUpdateStrategy,
		},
		{
			name: "recreate strategy with identity fields",
			mutate: func(d *Descriptor) {
				d.UpdateStrategy = UpdateRecreate
				d.RecreateOn = []string{"a_terminations", "b_terminations"}
			},
		},
		{
			name:    "identity fields without the recreate strategy",
			mutate:  func(d *Descriptor) { d.RecreateOn = []string{"a_terminations"} },
			wantErr: ErrRecreateOnWithoutRecreate,
		},
		{
			name: "unknown defer mode",
			mutate: func(d *Descriptor) {
				d.Deferred = []DeferredField{{APIField: "primary_ip4", Mode: "Sometimes"}}
			},
			wantErr: ErrUnknownDeferMode,
		},
		{
			name: "unknown lookup modifier",
			mutate: func(d *Descriptor) {
				d.NaturalKeys[0].Fields[0].Lookup = "icontains"
			},
			wantErr: ErrUnknownLookup,
		},
		{
			name:    "empty GVK",
			mutate:  func(d *Descriptor) { d.GVK = schema.GroupVersionKind{} },
			wantErr: ErrEmptyGVK,
		},
		{
			name:    "object type in model case",
			mutate:  func(d *Descriptor) { d.ObjectType = "virtualization.VMInterface" },
			wantErr: ErrInvalidObjectType,
		},
		{
			name:    "unknown scope",
			mutate:  func(d *Descriptor) { d.Scope = "Global" },
			wantErr: ErrUnknownScope,
		},
		{
			name:    "deferred field with no name",
			mutate:  func(d *Descriptor) { d.Deferred = []DeferredField{{Mode: DeferAlways}} },
			wantErr: ErrEmptyField,
		},
		{
			name:    "empty read-only field name",
			mutate:  func(d *Descriptor) { d.ReadOnly = append(d.ReadOnly, "") },
			wantErr: ErrEmptyField,
		},
		{
			name: "generic FK missing its id column",
			mutate: func(d *Descriptor) {
				d.GenericFKs = []GenericFKSpec{{TypeField: "scope_type", AllowedTypes: []string{"dcim.site"}}}
			},
			wantErr: ErrInvalidGenericFK,
		},
		{
			name: "generic FK target in model case",
			mutate: func(d *Descriptor) {
				d.GenericFKs = []GenericFKSpec{{
					TypeField:    "scope_type",
					IDField:      "scope_id",
					AllowedTypes: []string{"dcim.Site"},
				}}
			},
			wantErr: ErrInvalidObjectType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tagDescriptor()
			tc.mutate(&d)

			err := d.Validate()

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestDescriptorValidateDeferredNaturalKey covers NBO-015's identity guard: deferring a
// field a candidate matches on would create the object under the wrong identity, while
// deferring one a candidate pins to null cannot, and a read-only column a candidate
// filters on is legal (virtualization.Cluster keys on the `_site` cache).
func TestDescriptorValidateDeferredNaturalKey(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Descriptor)
		wantErr error
	}{
		{
			name: "always-deferred field matched by a candidate",
			mutate: func(d *Descriptor) {
				d.Deferred = []DeferredField{{APIField: "parent", Mode: DeferAlways}}
			},
			wantErr: ErrDeferredNaturalKey,
		},
		{
			name: "conditionally deferred field matched by a candidate",
			mutate: func(d *Descriptor) {
				d.Deferred = []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}}
			},
		},
		{
			name: "always-deferred field only ever pinned to null",
			mutate: func(d *Descriptor) {
				d.NaturalKeys = d.NaturalKeys[1:]
				d.Deferred = []DeferredField{{APIField: "parent", Mode: DeferAlways}}
			},
		},
		{
			name: "read-only column matched by a candidate",
			mutate: func(d *Descriptor) {
				cluster := clusterDescriptor()
				// The field map comes along with the keys: a candidate may only match on
				// a spec field the descriptor declares.
				d.NaturalKeys, d.Fields, d.GenericFKs = cluster.NaturalKeys, cluster.Fields, cluster.GenericFKs
				d.Deferred = nil
				d.ContainmentRef = cluster.ContainmentRef
				// The pair's caches come along too: GenericFKSpec.Cached insists every one
				// of them is read-only, and `_site` -- the column this case is about -- is
				// one of the four.
				d.ReadOnly = append(d.ReadOnly, ScopeCacheColumns()...)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := regionDescriptor()
			tc.mutate(&d)

			err := d.Validate()

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRegistryAddAndGet(t *testing.T) {
	r := New()

	for _, d := range schemaDescriptors() {
		if err := r.Add(d); err != nil {
			t.Fatalf("Add(%s): %v", d.GVK.Kind, err)
		}
	}

	got, ok := r.Get(testGVK("NetBoxRegion"))
	if !ok {
		t.Fatal("Get(NetBoxRegion) reported not found")
	}

	if got.Endpoint != "dcim/regions" {
		t.Fatalf("Get(NetBoxRegion).Endpoint = %q, want dcim/regions", got.Endpoint)
	}

	if _, ok := r.Get(testGVK("NetBoxUnregistered")); ok {
		t.Fatal("Get(NetBoxUnregistered) reported found")
	}
}

// TestRegistryListIsOrdered pins the ordering because callers log, validate and generate
// from List, and map order makes all three unreviewable.
func TestRegistryListIsOrdered(t *testing.T) {
	r := New()

	for _, d := range schemaDescriptors() {
		if err := r.Add(d); err != nil {
			t.Fatalf("Add(%s): %v", d.GVK.Kind, err)
		}
	}

	want := []string{
		"NetBoxCluster",
		"NetBoxDevice",
		"NetBoxIPAddress",
		"NetBoxRegion",
		"NetBoxTag",
		"NetBoxTenantGroup",
		"NetBoxVRF",
	}

	got := make([]string, 0, len(want))
	for _, d := range r.List() {
		got = append(got, d.GVK.Kind)
	}

	if len(got) != len(want) {
		t.Fatalf("List() returned %d descriptors, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestRegistryAddDuplicate(t *testing.T) {
	r := New()
	first := tagDescriptor()
	second := tagDescriptor()
	second.Endpoint = "extras/tags-impostor"

	if err := r.Add(first); err != nil {
		t.Fatalf("Add(first): %v", err)
	}

	err := r.Add(second)
	if !errors.Is(err, ErrDuplicateGVK) {
		t.Fatalf("Add(second) = %v, want ErrDuplicateGVK", err)
	}

	got, _ := r.Get(first.GVK)
	if got.Endpoint != first.Endpoint {
		t.Fatalf("duplicate overwrote the first registration: endpoint = %q", got.Endpoint)
	}

	if err := r.Validate(); !errors.Is(err, ErrDuplicateGVK) {
		t.Fatalf("Validate() = %v, want ErrDuplicateGVK", err)
	}
}

func TestRegistryValidateReportsBadDescriptor(t *testing.T) {
	r := New()
	d := tagDescriptor()
	d.Endpoint = ""

	if err := r.Add(d); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := r.Validate(); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("Validate() = %v, want ErrNoEndpoint", err)
	}
}

// TestMustRegisterPanicsOnDuplicate covers the boot-time contract: a duplicate kind is a
// programming error that must stop the process, never surface as a reconcile failure.
func TestMustRegisterPanicsOnDuplicate(t *testing.T) {
	d := tagDescriptor()
	d.GVK = testGVK("NetBoxMustRegisterFixture")
	// Its own object type as well as its own Kind: the reverse index is one-to-one, so a
	// fixture borrowing extras.tag would be refused for that instead of for its GVK.
	d.ObjectType = "extras.mustregisterfixture"

	MustRegister(d)

	if _, ok := Get(d.GVK); !ok {
		t.Fatal("Get after MustRegister reported not found")
	}

	if !slices.ContainsFunc(List(), func(listed Descriptor) bool { return listed.GVK == d.GVK }) {
		t.Fatal("List after MustRegister omitted the descriptor")
	}

	if err := Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister of a duplicate GVK did not panic")
		}
	}()

	MustRegister(d)
}
