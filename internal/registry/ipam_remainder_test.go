package registry

import (
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// remainderKinds is the nine Kinds NBO-055 ships, with the two facts a Descriptor cannot
// derive: the endpoint (looked up, never pluralised) and the `app_label.model` string other
// Kinds point at through a generic FK.
//
// ipam.IPRange is deliberately absent. NBO-064 creates api/v1alpha1/ipam_iprange.go, its
// registry entry and its controller, because NetBoxIPRangeClaim allocates *from* a range and
// cannot ship without it; exactly one ticket may create that file and it is not this one.
var remainderKinds = []struct {
	kind, endpoint, objectType string
	stamped                    bool
}{
	{"NetBoxRIR", "ipam/rirs", "ipam.rir", true},
	{"NetBoxAggregate", "ipam/aggregates", "ipam.aggregate", true},
	{"NetBoxASN", "ipam/asns", "ipam.asn", true},
	{"NetBoxASNRange", "ipam/asn-ranges", "ipam.asnrange", true},
	{"NetBoxRole", "ipam/roles", "ipam.role", true},
	{"NetBoxFHRPGroup", "ipam/fhrp-groups", "ipam.fhrpgroup", true},
	// The one that is not a PrimaryModel: `bases: ChangeLoggedModel`, so neither TagsMixin
	// nor CustomFieldsMixin, so no provenance stamp (docs/netbox-schema.md).
	{"NetBoxFHRPGroupAssignment", "ipam/fhrp-group-assignments", "ipam.fhrpgroupassignment", false},
	{"NetBoxService", "ipam/services", "ipam.service", true},
	{"NetBoxServiceTemplate", "ipam/service-templates", "ipam.servicetemplate", true},
}

// TestRemainderDescriptorsAreRegisteredAndValid is the boot check for all nine at once.
//
// The endpoint assertions are the ones worth having: an endpoint is looked up from
// docs/netbox-schema.md's map and never derived by pluralising, and three of these would be
// wrong under any rule -- `ipam/rirs` (not `rirs`... but also not `ipam/r-i-rs`),
// `ipam/asn-ranges` and `ipam/fhrp-group-assignments`. NetBox answers a wrong path with a 404,
// which is at least loud; a wrong *object type* is silent, because it is written into a
// `*_type` column NetBox resolves through get_by_natural_key.
func TestRemainderDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range remainderKinds {
		t.Run(tc.kind, func(t *testing.T) {
			d := descriptorFor(t, tc.kind)

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

			if d.Taggable != tc.stamped || d.CustomFieldable != tc.stamped {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want %v/%v: read off the model's bases",
					d.Taggable, d.CustomFieldable, tc.stamped, tc.stamped)
			}
		})
	}
}

// TestRemainderNaturalKeys pins every candidate to the constraint behind it, because the
// difference between "a unique index says so" and "the ordering tuple says so" is the whole
// of whether an ambiguous match is a bug or a legitimate server state.
func TestRemainderNaturalKeys(t *testing.T) {
	for _, tc := range []struct {
		kind string
		// filters is each candidate's matched filters in order, and why is the line in
		// docs/netbox-schema.md that justifies it.
		filters [][]string
		why     string
	}{
		{
			kind:    "NetBoxRIR",
			filters: [][]string{{"slug"}},
			why:     "slug (OrganizationalModel) SlugField REQ UNIQUE len=100",
		},
		{
			kind:    "NetBoxRole",
			filters: [][]string{{"slug"}},
			why:     "slug (OrganizationalModel) SlugField REQ UNIQUE len=100",
		},
		{
			kind:    "NetBoxASN",
			filters: [][]string{{"asn"}},
			why:     "asn ASNField REQ UNIQUE -- a column-level unique index, no name and no slug exist",
		},
		{
			kind:    "NetBoxASNRange",
			filters: [][]string{{"slug"}},
			why:     "slug SlugField REQ UNIQUE len=100, redeclared on the model (shadows inherited)",
		},
		{
			kind:    "NetBoxAggregate",
			filters: [][]string{{"prefix", "rir_id"}},
			why:     "no meta.constraints at all: (prefix, rir) is the convention, ambiguity is a Conflict",
		},
		{
			kind:    "NetBoxFHRPGroup",
			filters: [][]string{{"protocol", "group_id"}},
			why:     "no meta.constraints: (protocol, group_id) is the ordering tuple and the convention",
		},
		{
			kind:    "NetBoxFHRPGroupAssignment",
			filters: [][]string{{"interface_type", "interface_id", "group_id"}},
			why:     "meta.constraints: UniqueConstraint(('interface_type', 'interface_id', 'group'))",
		},
		{
			kind:    "NetBoxService",
			filters: [][]string{{"parent_object_type", "parent_object_id", "name", "protocol"}},
			why:     "no meta.constraints: (parent, name, protocol) is the convention, and ports cannot be a filter",
		},
		{
			kind:    "NetBoxServiceTemplate",
			filters: [][]string{{"name"}},
			why:     "name CharField REQ UNIQUE len=100",
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d := descriptorFor(t, tc.kind)

			if len(d.NaturalKeys) != len(tc.filters) {
				t.Fatalf("%d candidates, want %d (%s)", len(d.NaturalKeys), len(tc.filters), tc.why)
			}

			for i, want := range tc.filters {
				got := make([]string, 0, len(d.NaturalKeys[i].Fields))
				for _, field := range d.NaturalKeys[i].Fields {
					got = append(got, field.Param())
				}

				if !slices.Equal(got, want) {
					t.Errorf("candidate %d matches %v, want %v (%s)", i, got, want, tc.why)
				}
			}
		})
	}
}

// TestServiceTemplateMatchesNameExactly is the one lookup modifier decision in this group, and
// it goes the opposite way from dcim.Device's.
//
// `ipam.ServiceTemplate.name` is a plain `unique=True` on the column, not a UniqueConstraint
// over `Lower('name')` (docs/netbox-schema.md -> ipam.ServiceTemplate). So `SSH` and `ssh` are
// two legal rows, and a case-insensitive lookup would adopt one for the other and PATCH
// somebody else's template.
func TestServiceTemplateMatchesNameExactly(t *testing.T) {
	d := descriptorFor(t, "NetBoxServiceTemplate")

	if got := d.NaturalKeys[0].Fields[0].Lookup; got != LookupExact {
		t.Errorf("lookup = %q, want exact: the constraint is unique=True, not Lower('name')", got)
	}
}

// TestRemainderContainment states each Kind's containment parent and the `on_delete` that
// settled it. Under docs/decisions/0003-ownership-and-references.md rule 4 this is not a
// judgement per Kind: it is whichever FK the *server* cascades, and validateContainment
// rejects anything else at boot.
func TestRemainderContainment(t *testing.T) {
	for _, tc := range []struct {
		kind, want, why string
	}{
		{"NetBoxRIR", "", "no mapped foreign key at all"},
		{"NetBoxRole", "", "no mapped foreign key at all"},
		{"NetBoxASN", "", "rir PROTECT, tenant PROTECT, role SET_NULL -- none cascades"},
		{"NetBoxASNRange", "", "rir PROTECT, tenant PROTECT -- neither cascades"},
		{"NetBoxAggregate", "", "rir PROTECT, tenant PROTECT -- neither cascades"},
		{"NetBoxFHRPGroup", "", "no mapped foreign key at all"},
		{"NetBoxServiceTemplate", "", "no mapped foreign key at all"},
		{
			"NetBoxFHRPGroupAssignment", "groupRef",
			"group ForeignKey REQ -> ipam.FHRPGroup on_delete=CASCADE, declared on this model",
		},
		{
			"NetBoxService", "parent",
			"all three parent targets declare a `services` GenericRelation, so every member cascades",
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := descriptorFor(t, tc.kind).ContainmentRef; got != tc.want {
				t.Errorf("ContainmentRef = %q, want %q (%s)", got, tc.want, tc.why)
			}
		})
	}
}

// TestServiceParentUnionCascadesFromEveryMember is the per-member half of #214: the cascade is
// a property of the referring model *per target*, so it is stated per member and read back per
// member -- the owner reference is decided from whichever member an object actually resolved
// through.
func TestServiceParentUnionCascadesFromEveryMember(t *testing.T) {
	d := descriptorFor(t, "NetBoxService")

	pair, ok := d.GenericFKFor("parent")
	if !ok {
		t.Fatal("no generic FK behind `parent`; one spec field writing two columns is a GenericFKSpec")
	}

	if pair.TypeField != "parent_object_type" || pair.IDField != "parent_object_id" {
		t.Errorf("columns = %s/%s, want parent_object_type/parent_object_id",
			pair.TypeField, pair.IDField)
	}

	want := []string{"dcim.device", "virtualization.virtualmachine", "ipam.fhrpgroup"}
	if !slices.Equal(pair.AllowedTypes, want) {
		t.Errorf("AllowedTypes = %v, want %v (docs/netbox-schema.md -> ipam.Service)",
			pair.AllowedTypes, want)
	}

	for _, member := range pair.Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("member %s does not state a cascade; %s declares a `services` GenericRelation",
				member.Spec, member.Target.Kind)
		}
	}
}

// TestFHRPInterfaceUnionCascadesFromEveryMember is the same assertion for the assignment's
// interface pair. Both members cascade through an `fhrp_group_assignments` GenericRelation on
// the target, which is why the containment parent had to be *chosen* rather than derived --
// and `groupRef` won because it is the declared `on_delete=CASCADE` foreign key on this model.
func TestFHRPInterfaceUnionCascadesFromEveryMember(t *testing.T) {
	d := descriptorFor(t, "NetBoxFHRPGroupAssignment")

	pair, ok := d.GenericFKFor("interface")
	if !ok {
		t.Fatal("no generic FK behind `interface`")
	}

	if pair.TypeField != "interface_type" || pair.IDField != "interface_id" {
		t.Errorf("columns = %s/%s, want interface_type/interface_id", pair.TypeField, pair.IDField)
	}

	want := []string{"dcim.interface", "virtualization.vminterface"}
	if !slices.Equal(pair.AllowedTypes, want) {
		t.Errorf("AllowedTypes = %v, want %v", pair.AllowedTypes, want)
	}

	for _, member := range pair.Members {
		if member.CascadeOnDelete == nil || !*member.CascadeOnDelete {
			t.Errorf("member %s does not state a cascade", member.Spec)
		}
	}
}

// TestServiceFieldClassesSplitTheTwoListShapes is the distinction the engine cannot guess.
//
// `ports` and `ipaddresses` both arrive as JSON lists and compare by opposite rules: a
// Postgres ArrayField's order is data, a many-to-many's is not. Comparing the array
// order-independently misses a reordering the user asked for; comparing the M2M
// order-sensitively PATCHes the same list forever (internal/netbox/drift.go).
func TestServiceFieldClassesSplitTheTwoListShapes(t *testing.T) {
	d := descriptorFor(t, "NetBoxService")

	ports, ok := d.FieldFor("ports")
	if !ok {
		t.Fatal("no ports in the field map")
	}

	if ports.Class != ClassArray {
		t.Errorf("ports class = %q, want Array: an ArrayField's order is data", ports.Class)
	}

	addresses, ok := d.FieldFor("ipAddresses")
	if !ok {
		t.Fatal("no ipAddresses in the field map")
	}

	if addresses.Class != ClassRefMany {
		t.Errorf("ipAddresses class = %q, want RefMany", addresses.Class)
	}

	// The snake_case spelling matters more than it looks: NetBox's column is `ipaddresses`
	// with no separator, so the camelCase-to-snake_case convention a naming rule would apply
	// produces `ip_addresses` -- which NetBox ignores rather than rejects, so the field would
	// write nothing and report success.
	if addresses.API != "ipaddresses" {
		t.Errorf("ipAddresses writes %q, want ipaddresses (docs/netbox-schema.md -> ipam.Service)",
			addresses.API)
	}

	if !slices.Contains(d.ReadOnly, "_ports_lowest") {
		t.Error("_ports_lowest is not read-only; NetBox recomputes it from ports on every save")
	}
}

// TestFHRPGroupNeverWritesAuthKey is the security assertion, and it is about the field map
// rather than about a payload: a column no spec field maps onto cannot reach a request body
// at all.
//
// plan.md §15 permits the value only as `spec.authKeySecretRef`, and reading a Secret into a
// NetBox payload field is a capability the engine does not have. So the column is absent
// rather than inline -- an inline pre-shared key in a CR every namespace reader can `get` is
// the failure this avoids.
func TestFHRPGroupNeverWritesAuthKey(t *testing.T) {
	d := descriptorFor(t, "NetBoxFHRPGroup")

	for _, field := range d.Fields {
		if field.API == "auth_key" || field.Spec == "authKey" || field.Spec == "authKeySecretRef" {
			t.Errorf("field map declares %s -> %s; auth_key must never be written inline (plan.md §15)",
				field.Spec, field.API)
		}
	}

	// `auth_type` *is* written: it is an ordinary choice column and says nothing secret.
	if _, ok := d.FieldFor("authType"); !ok {
		t.Error("no authType in the field map; auth_type is an ordinary choice column")
	}
}

// TestAggregateClearsDateAddedWithNull is the one EmptyIsNull decision in this group.
//
// `date_added` is a nullable DateField, and NetBox rejects `""` for a DateField outright, so
// an emptied value has to go over the wire as `null` to clear rather than to fail -- the same
// handling dcim.Site's coordinates got in #170. Without it, clearing the field is a 400 on
// every reconcile forever.
func TestAggregateClearsDateAddedWithNull(t *testing.T) {
	field, ok := descriptorFor(t, "NetBoxAggregate").FieldFor("dateAdded")
	if !ok {
		t.Fatal("no dateAdded in the field map")
	}

	if field.API != "date_added" {
		t.Errorf("dateAdded writes %q, want date_added", field.API)
	}

	if !field.EmptyIsNull {
		t.Error("dateAdded is not EmptyIsNull; NetBox rejects \"\" for a DateField")
	}
}
