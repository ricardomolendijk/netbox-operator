package netbox

import (
	"encoding/json"
	"maps"
	"testing"
)

// siteRules and prefixRules mirror what internal/registry will supply per Kind.
var (
	tagRules = FieldRules{M2M: map[string]bool{"tags": true}}

	prefixRules = FieldRules{
		M2M:        map[string]bool{"tags": true},
		GenericFKs: []GenericFK{{TypeField: "scope_type", IDField: "scope_id"}},
	}

	vrfRules = FieldRules{
		M2M: map[string]bool{
			"tags": true, "import_targets": true, "export_targets": true,
		},
	}
)

func TestDriftNormalisations(t *testing.T) {
	tests := []struct {
		name    string
		live    Object
		desired Object
		rules   FieldRules
		want    []string // field names expected in the drift, empty for none
	}{
		{
			name:    "identical scalars produce no drift",
			live:    Object{"name": "home", "description": ""},
			desired: Object{"name": "home", "description": ""},
			want:    nil,
		},
		{
			name:    "changed scalar drifts",
			live:    Object{"name": "home"},
			desired: Object{"name": "away"},
			want:    []string{"name"},
		},
		{
			name:    "1. related field read as a nested object compares by id",
			live:    Object{"tenant": map[string]any{"id": float64(3), "name": "acme", "slug": "acme"}},
			desired: Object{"tenant": 3},
			want:    nil,
		},
		{
			name:    "1b. related field with a different id drifts",
			live:    Object{"tenant": map[string]any{"id": float64(3)}},
			desired: Object{"tenant": 4},
			want:    []string{"tenant"},
		},
		{
			name:    "1c. related field cleared to null drifts",
			live:    Object{"tenant": map[string]any{"id": float64(3)}},
			desired: Object{"tenant": nil},
			want:    []string{"tenant"},
		},
		{
			name:    "1d. null stays null",
			live:    Object{"tenant": nil},
			desired: Object{"tenant": nil},
			want:    nil,
		},
		{
			name:    "2. choice field read as {value,label} compares by value",
			live:    Object{"status": map[string]any{"value": "active", "label": "Active"}},
			desired: Object{"status": "active"},
			want:    nil,
		},
		{
			name:    "2b. changed choice drifts",
			live:    Object{"status": map[string]any{"value": "active", "label": "Active"}},
			desired: Object{"status": "planned"},
			want:    []string{"status"},
		},
		{
			name: "3. tags compare as an order-independent id set",
			live: Object{"tags": []any{
				map[string]any{"id": float64(2), "name": "managed"},
				map[string]any{"id": float64(1), "name": "homelab"},
			}},
			desired: Object{"tags": []any{float64(1), float64(2)}},
			rules:   tagRules,
			want:    nil,
		},
		{
			name: "3b. a removed tag drifts",
			live: Object{"tags": []any{
				map[string]any{"id": float64(1)}, map[string]any{"id": float64(2)},
			}},
			desired: Object{"tags": []any{float64(1)}},
			rules:   tagRules,
			want:    []string{"tags"},
		},
		{
			name:    "3c. empty tag list against no tags is equal",
			live:    Object{"tags": []any{}},
			desired: Object{"tags": []any{}},
			rules:   tagRules,
			want:    nil,
		},
		{
			name:    "4. decimal returned as a string compares numerically",
			live:    Object{"u_height": "1.00", "vcpus": "2.00", "weight": "10.50"},
			desired: Object{"u_height": 1, "vcpus": 2, "weight": 10.5},
			want:    nil,
		},
		{
			name:    "4b. a changed decimal drifts",
			live:    Object{"u_height": "1.00"},
			desired: Object{"u_height": 2},
			want:    []string{"u_height"},
		},
		{
			name: "5. M2M generalised beyond tags: import_targets",
			live: Object{"import_targets": []any{
				map[string]any{"id": float64(7)}, map[string]any{"id": float64(5)},
			}},
			desired: Object{"import_targets": []any{float64(5), float64(7)}},
			rules:   vrfRules,
			want:    nil,
		},
		{
			name:    "5b. a changed M2M member drifts",
			live:    Object{"export_targets": []any{map[string]any{"id": float64(7)}}},
			desired: Object{"export_targets": []any{float64(8)}},
			rules:   vrfRules,
			want:    []string{"export_targets"},
		},
		{
			name: "6. unmanaged custom fields on the live object are ignored",
			live: Object{"custom_fields": map[string]any{
				"managed_by":    "netbox-operator",
				"someone_elses": "do not touch",
				"another_teams": float64(42),
			}},
			desired: Object{"custom_fields": map[string]any{"managed_by": "netbox-operator"}},
			want:    nil,
		},
		{
			name: "6b. a managed custom field that differs drifts",
			live: Object{"custom_fields": map[string]any{
				"managed_by": "someone-else", "unrelated": "x",
			}},
			desired: Object{"custom_fields": map[string]any{"managed_by": "netbox-operator"}},
			want:    []string{"custom_fields"},
		},
		{
			name:    "6c. a managed custom field absent from live drifts",
			live:    Object{"custom_fields": map[string]any{"unrelated": "x"}},
			desired: Object{"custom_fields": map[string]any{"managed_by": "netbox-operator"}},
			want:    []string{"custom_fields"},
		},
		{
			name:    "6d. a nil asks for a value NetBox holds to be removed, so it drifts",
			live:    Object{"custom_fields": map[string]any{"audit_ticket": "NET-42"}},
			desired: Object{"custom_fields": map[string]any{"audit_ticket": nil}},
			want:    []string{"custom_fields"},
		},
		{
			name:    "6e. a nil against the null NetBox returns is settled, not a hot loop",
			live:    Object{"custom_fields": map[string]any{"audit_ticket": nil, "other": "x"}},
			desired: Object{"custom_fields": map[string]any{"audit_ticket": nil}},
			want:    nil,
		},
		{
			// The whole point of the null being a *different* value from the empty string:
			// a spec asking for "" against a NetBox holding null has work to do, and the
			// reverse does too. Collapsing the two would make one of them unexpressible.
			name:    "6f. an empty string is not the same request as a removal",
			live:    Object{"custom_fields": map[string]any{"audit_ticket": nil}},
			desired: Object{"custom_fields": map[string]any{"audit_ticket": ""}},
			want:    []string{"custom_fields"},
		},
		{
			name:    "6g. a removal of a custom field that never had a value is no work at all",
			live:    Object{"custom_fields": map[string]any{}},
			desired: Object{"custom_fields": map[string]any{"audit_ticket": nil}},
			want:    nil,
		},
		{
			name: "7. generic FK pair equal produces no drift",
			live: Object{
				"scope_type": "dcim.site",
				"scope_id":   float64(4),
			},
			desired: Object{"scope_type": "dcim.site", "scope_id": 4},
			rules:   prefixRules,
			want:    nil,
		},
		{
			name: "7b. a changed scope_id emits both halves of the pair",
			live: Object{"scope_type": "dcim.site", "scope_id": float64(4)},
			// Only the id moved, but scope_type must go with it: NetBox validates the
			// pair together and an id sent alone is read against the old type.
			desired: Object{"scope_type": "dcim.site", "scope_id": 9},
			rules:   prefixRules,
			want:    []string{"scope_id", "scope_type"},
		},
		{
			name:    "7c. a changed scope_type emits both halves",
			live:    Object{"scope_type": "dcim.site", "scope_id": float64(4)},
			desired: Object{"scope_type": "dcim.region", "scope_id": 4},
			rules:   prefixRules,
			want:    []string{"scope_id", "scope_type"},
		},
		{
			name:    "7d. scope cleared to global drifts",
			live:    Object{"scope_type": "dcim.site", "scope_id": float64(4)},
			desired: Object{"scope_type": nil, "scope_id": nil},
			rules:   prefixRules,
			want:    []string{"scope_id", "scope_type"},
		},
		{
			name:    "7e. a global prefix staying global produces no drift",
			live:    Object{"scope_type": nil, "scope_id": nil},
			desired: Object{"scope_type": nil, "scope_id": nil},
			rules:   prefixRules,
			want:    nil,
		},
		{
			name:    "8. ArrayField compares order-sensitively",
			live:    Object{"ports": []any{float64(80), float64(443)}},
			desired: Object{"ports": []any{float64(80), float64(443)}},
			rules:   FieldRules{Arrays: map[string]bool{"ports": true}},
			want:    nil,
		},
		{
			name:    "8b. a reordered ArrayField drifts, because the order is data",
			live:    Object{"ports": []any{float64(443), float64(80)}},
			desired: Object{"ports": []any{float64(80), float64(443)}},
			rules:   FieldRules{Arrays: map[string]bool{"ports": true}},
			want:    []string{"ports"},
		},
		{
			name:    "object type list compares as an unordered string set",
			live:    Object{"object_types": []any{"dcim.device", "virtualization.virtualmachine"}},
			desired: Object{"object_types": []any{"virtualization.virtualmachine", "dcim.device"}},
			rules:   FieldRules{ObjectTypeLists: map[string]bool{"object_types": true}},
			want:    nil,
		},
		{
			name:    "fields absent from desired are never touched",
			live:    Object{"name": "home", "description": "set by a human", "comments": "keep me"},
			desired: Object{"name": "home"},
			want:    nil,
		},
		{
			name:    "booleans compare correctly, including false",
			live:    Object{"is_pool": false, "mark_utilized": true},
			desired: Object{"is_pool": false, "mark_utilized": true},
			want:    nil,
		},
		{
			name:    "false against true drifts",
			live:    Object{"is_pool": true},
			desired: Object{"is_pool": false},
			want:    []string{"is_pool"},
		},
		{
			name:    "zero is not the same as null",
			live:    Object{"vid": nil},
			desired: Object{"vid": 0},
			want:    []string{"vid"},
		},
		{
			name:    "empty string is not the same as null",
			live:    Object{"description": nil},
			desired: Object{"description": ""},
			want:    []string{"description"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Drift(tc.live, tc.desired, tc.rules)
			assertFields(t, got, tc.want)
		})
	}
}

func assertFields(t *testing.T, got Object, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("drift = %v (%d fields), want %v (%d fields)", got, len(got), want, len(want))
	}
	for _, field := range want {
		if _, ok := got[field]; !ok {
			t.Errorf("drift is missing %q; got %v", field, got)
		}
	}
}

// TestNoHotLoopOnRealResponses is the regression test that matters most.
//
// A wrong normalisation does not fail loudly: it produces a diff that is never satisfied,
// so the operator PATCHes the same object forever. Each case here is a payload the
// operator would send paired with the response NetBox 4.6.8 actually returns for it, and
// asserts the second reconcile finds nothing to do.
//
// The responses are hand-built from docs/netbox-schema.md rather than captured from a
// live server, so they are only as good as that reference. NBO-017's e2e gate replays
// these against a real NetBox and is what will prove them.
func TestNoHotLoopOnRealResponses(t *testing.T) {
	cases := []struct {
		kind     string
		sent     string
		response string
		rules    FieldRules
	}{
		{
			kind: "dcim.Site",
			sent: `{"name":"Home","slug":"home","status":"active","tenant":3,
			        "description":"","tags":[1,2],"custom_fields":{"managed_by":"netbox-operator"}}`,
			response: `{"id":1,"url":"https://nb/api/dcim/sites/1/","name":"Home","slug":"home",
			        "status":{"value":"active","label":"Active"},
			        "tenant":{"id":3,"url":"https://nb/api/tenancy/tenants/3/","display":"acme","name":"acme","slug":"acme"},
			        "region":null,"group":null,"facility":"","time_zone":null,"description":"",
			        "physical_address":"","shipping_address":"","latitude":null,"longitude":null,
			        "comments":"","asns":[],
			        "tags":[{"id":2,"name":"managed","slug":"managed","color":"9e9e9e"},
			                {"id":1,"name":"homelab","slug":"homelab","color":"2196f3"}],
			        "custom_fields":{"managed_by":"netbox-operator","audit_ticket":null,"owner_team":null},
			        "created":"2026-08-21T10:00:00Z","last_updated":"2026-08-21T10:00:00Z",
			        "device_count":9,"rack_count":0,"prefix_count":5,"vlan_count":5}`,
			rules: tagRules,
		},
		{
			kind: "ipam.Prefix scoped to a site",
			sent: `{"prefix":"10.20.0.0/24","status":"active","vrf":2,"tenant":3,"vlan":5,
			        "scope_type":"dcim.site","scope_id":1,"is_pool":false,"mark_utilized":false,
			        "description":"","tags":[1]}`,
			// _site is the read-only cached column. Writing "site" silently no-ops on
			// 4.6, which is the populator bug this operator must not inherit; the pair
			// that matters is (scope_type, scope_id).
			response: `{"id":11,"family":{"value":4,"label":"IPv4"},"prefix":"10.20.0.0/24",
			        "vrf":{"id":2,"name":"vrf-home","rd":null},"tenant":{"id":3,"name":"acme","slug":"acme"},
			        "vlan":{"id":5,"vid":20,"name":"vlan-mgmt"},
			        "status":{"value":"active","label":"Active"},"role":null,
			        "scope_type":"dcim.site","scope_id":1,
			        "scope":{"id":1,"name":"Home","slug":"home"},
			        "_site":{"id":1,"name":"Home","slug":"home"},"_region":null,
			        "is_pool":false,"mark_utilized":false,"description":"","comments":"",
			        "tags":[{"id":1,"name":"homelab","slug":"homelab"}],
			        "custom_fields":{},"children":0,"_depth":0}`,
			rules: prefixRules,
		},
		{
			kind: "ipam.Prefix, global (no scope)",
			sent: `{"prefix":"192.0.2.0/24","status":"container","scope_type":null,"scope_id":null,
			        "is_pool":true,"description":"","tags":[]}`,
			response: `{"id":12,"prefix":"192.0.2.0/24","status":{"value":"container","label":"Container"},
			        "scope_type":null,"scope_id":null,"scope":null,"_site":null,"_region":null,
			        "vrf":null,"tenant":null,"vlan":null,"role":null,
			        "is_pool":true,"mark_utilized":false,"description":"","tags":[],"custom_fields":{}}`,
			rules: prefixRules,
		},
		{
			kind: "ipam.VRF with route targets",
			sent: `{"name":"vrf-home","rd":"65000:1","enforce_unique":true,"tenant":3,
			        "import_targets":[7,5],"export_targets":[5],"description":"","tags":[]}`,
			response: `{"id":2,"name":"vrf-home","rd":"65000:1","tenant":{"id":3,"name":"acme"},
			        "enforce_unique":true,"description":"","comments":"",
			        "import_targets":[{"id":5,"name":"65000:5"},{"id":7,"name":"65000:7"}],
			        "export_targets":[{"id":5,"name":"65000:5"}],
			        "tags":[],"custom_fields":{},"ipaddress_count":12,"prefix_count":5}`,
			rules: vrfRules,
		},
		{
			kind: "dcim.DeviceType with decimal u_height",
			sent: `{"model":"DCS-7050","slug":"dcs-7050","manufacturer":4,"u_height":1,
			        "is_full_depth":true,"description":"","tags":[]}`,
			response: `{"id":6,"model":"DCS-7050","slug":"dcs-7050",
			        "manufacturer":{"id":4,"name":"Arista","slug":"arista"},
			        "u_height":"1.00","exclude_from_utilization":false,"is_full_depth":true,
			        "airflow":null,"weight":null,"weight_unit":null,"description":"","comments":"",
			        "tags":[],"custom_fields":{},"device_count":2}`,
			rules: tagRules,
		},
		{
			kind: "virtualization.VirtualMachine with decimal vcpus",
			sent: `{"name":"dns","status":"active","cluster":1,"role":2,"vcpus":2,"memory":2048,
			        "disk":20,"description":"","tags":[1]}`,
			response: `{"id":3,"name":"dns","status":{"value":"active","label":"Active"},
			        "site":{"id":1,"name":"Home"},"cluster":{"id":1,"name":"proxmox-home"},
			        "device":null,"role":{"id":2,"name":"server","slug":"server"},
			        "tenant":null,"platform":null,"primary_ip":null,"primary_ip4":null,"primary_ip6":null,
			        "vcpus":"2.00","memory":2048,"disk":20,"description":"","comments":"",
			        "config_template":null,"local_context_data":null,
			        "tags":[{"id":1,"name":"homelab","slug":"homelab"}],"custom_fields":{},
			        "interface_count":1,"virtual_disk_count":1}`,
			rules: tagRules,
		},
		{
			kind: "ipam.IPAddress assigned to a VM interface",
			sent: `{"address":"10.20.0.10/24","status":"active","vrf":2,"tenant":3,
			        "assigned_object_type":"virtualization.vminterface","assigned_object_id":8,
			        "dns_name":"dns.home.arpa","description":"","tags":[]}`,
			response: `{"id":21,"family":{"value":4,"label":"IPv4"},"address":"10.20.0.10/24",
			        "vrf":{"id":2,"name":"vrf-home"},"tenant":{"id":3,"name":"acme"},
			        "status":{"value":"active","label":"Active"},"role":null,
			        "assigned_object_type":"virtualization.vminterface","assigned_object_id":8,
			        "assigned_object":{"id":8,"name":"eth0","virtual_machine":{"id":3,"name":"dns"}},
			        "nat_inside":null,"nat_outside":[],"dns_name":"dns.home.arpa",
			        "description":"","comments":"","tags":[],"custom_fields":{}}`,
			rules: FieldRules{
				M2M:        map[string]bool{"tags": true},
				GenericFKs: []GenericFK{{TypeField: "assigned_object_type", IDField: "assigned_object_id"}},
			},
		},
		{
			kind: "extras.Tag with object_types",
			sent: `{"name":"managed","slug":"managed","color":"9e9e9e","weight":1000,
			        "description":"","object_types":["dcim.device","virtualization.virtualmachine"]}`,
			response: `{"id":2,"name":"managed","slug":"managed","color":"9e9e9e","weight":1000,
			        "description":"","object_types":["virtualization.virtualmachine","dcim.device"],
			        "tagged_items":11}`,
			rules: FieldRules{ObjectTypeLists: map[string]bool{"object_types": true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			var sent, live Object
			if err := json.Unmarshal([]byte(tc.sent), &sent); err != nil {
				t.Fatalf("sent fixture: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.response), &live); err != nil {
				t.Fatalf("response fixture: %v", err)
			}
			if got := Drift(live, sent, tc.rules); len(got) != 0 {
				t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", got)
			}
		})
	}
}

// TestScalarEqualTypeConfusion pins the boundary between the three comparison paths.
// The fmt.Sprint fallback is load-bearing for strings and for integer widths, so it
// cannot simply be replaced with a same-type rule -- these cases fix its edges.
func TestScalarEqualTypeConfusion(t *testing.T) {
	tests := []struct {
		name       string
		have, want any
		equal      bool
	}{
		// The reported defect: stringifying made these agree.
		{"bool true vs string true", true, "true", false},
		{"string true vs bool true", "true", true, false},
		{"bool false vs string false", false, "false", false},
		{"bool false vs zero", false, 0, false},
		{"bool true vs one", true, 1, false},
		{"bool matches bool", true, true, true},
		{"false matches false", false, false, true},
		{"bool differs from bool", true, false, false},

		// Integer widening must keep working, or a CRD int32 against a JSON float64
		// becomes a permanent diff -- the hot loop this file exists to prevent.
		{"int32 vs float64", int32(20), float64(20), true},
		{"int64 vs float64", int64(20), float64(20), true},
		{"uint32 vs float64", uint32(20), float64(20), true},
		{"int vs numeric string", 20, "20", true},
		{"float32 vs float64", float32(1.5), float64(1.5), true},
		{"decimal string vs int", "1.00", 1, true},

		// Strings stay strings.
		{"choice value matches", "active", "active", true},
		{"choice value differs", "active", "planned", false},
		{"empty string is not zero", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scalarEqual(tc.have, tc.want); got != tc.equal {
				t.Errorf("scalarEqual(%#v, %#v) = %v, want %v", tc.have, tc.want, got, tc.equal)
			}
		})
	}
}

// TestDriftDoesNotConfuseBoolWithString is the same defect at the level Drift is used at:
// NetBox returns is_pool as a bool, and a spec that supplied the string would previously
// have looked equal.
func TestDriftDoesNotConfuseBoolWithString(t *testing.T) {
	got := Drift(Object{"is_pool": true}, Object{"is_pool": "true"}, FieldRules{})
	if len(got) != 1 {
		t.Errorf("drift = %v, want is_pool to differ: a bool and a string are not the same value", got)
	}
}

func TestChangesCarryOldAndNewInFieldOrder(t *testing.T) {
	live := Object{"name": "home", "status": map[string]any{"value": "active"}, "description": "old"}
	desired := Object{"name": "away", "status": "planned", "description": "new"}

	changes := Changes(live, desired, FieldRules{})
	if len(changes) != 3 {
		t.Fatalf("got %d changes, want 3: %v", len(changes), changes)
	}
	// Sorted by field name, so rendered output is stable between runs.
	want := []string{"description", "name", "status"}
	for i, field := range want {
		if changes[i].Field != field {
			t.Errorf("changes[%d].Field = %q, want %q", i, changes[i].Field, field)
		}
	}
	if changes[1].Old != "home" || changes[1].New != "away" {
		t.Errorf("name change = %v -> %v, want home -> away", changes[1].Old, changes[1].New)
	}
	// The old value keeps NetBox's read shape, so a renderer can show what was really
	// there rather than a normalised approximation.
	if _, ok := changes[2].Old.(map[string]any); !ok {
		t.Errorf("status Old = %T, want the nested read shape", changes[2].Old)
	}
}

func TestHashIsStableAndNumericInsensitive(t *testing.T) {
	// NetBox returns decimals as strings, so the hash must not depend on which side
	// produced the value -- otherwise the short-circuit never hits.
	a := Object{"u_height": "1.00", "vcpus": 2, "name": "dns"}
	b := Object{"vcpus": "2.00", "name": "dns", "u_height": 1}

	first, err := Hash(a)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := Hash(b)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if first != second {
		t.Errorf("hashes differ across equivalent numeric shapes:\n %s\n %s", first, second)
	}

	changed, err := Hash(Object{"u_height": 2, "vcpus": 2, "name": "dns"})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if changed == first {
		t.Error("hash did not change when a value changed")
	}
}

func TestHashIsOrderIndependentForMapKeys(t *testing.T) {
	first, err := Hash(Object{"a": 1, "b": 2, "c": 3})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	for range 20 {
		got, err := Hash(Object{"c": 3, "a": 1, "b": 2})
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		if got != first {
			t.Fatalf("hash not stable across map iteration order: %s then %s", first, got)
		}
	}
}

// Drift takes only plain data and returns plain data. Stated as a compile-time
// assertion because it is a design constraint, not an implementation detail: the moment
// Drift needs a *Client, nbctl plan (NBO-038) can no longer reuse it and it stops being
// exhaustively unit-testable. Breaking it fails the build rather than a test.
var _ func(Object, Object, FieldRules) Object = Drift

// TestCustomFieldRemovalSettles is the acceptance test for #196: a removal has to reach
// NetBox once and then stop.
//
// The table cases above check one comparison each. This one runs the loop the operator
// actually runs -- diff, PATCH, read back, diff again -- because the failure this feature
// can produce is not a wrong comparison, it is a *pair* of correct-looking comparisons that
// never agree: a null that NetBox stores as something the next read reports as different
// PATCHes the same object for as long as the CR exists.
//
// netbox mirrors NetBox 4.6's own two steps, both quoted where they live:
//
//   - Write: CustomFieldsDataField.to_internal_value merges the submitted map over the
//     stored one -- `data = {**self.parent.instance.custom_field_data, **data}`
//     (extras/api/customfields.py) -- so a key the PATCH does not name keeps its value and
//     a key it sets to null becomes null. CustomFieldsMixin.clean then validates it, and
//     `validate` returns immediately for `None` (extras/models/customfields.py); save()
//     re-applies a custom field's default only `if cf.name not in custom_field_data`
//     (netbox/models/features.py), and the key *is* in it, so a null is not defaulted back.
//   - Read: CustomFieldsDataField.to_representation emits every custom field defined for
//     the object type, each as `cf.deserialize(obj.get(cf.name))`, and deserialize returns
//     None unchanged. So a removed value reads back as an explicit null, indistinguishable
//     from one that was never set.
func TestCustomFieldRemovalSettles(t *testing.T) {
	// Every custom field defined for this object type in NetBox. to_representation answers
	// all of them, which is why the operator can never treat the container as its own.
	defined := []string{"audit_ticket", "managed_by", "someone_elses"}

	// What NetBox holds before the removal: a value the operator wrote, its own stamp, and
	// somebody else's field it must not touch.
	stored := map[string]any{
		"audit_ticket": "NET-42", "managed_by": "netbox-operator", "someone_elses": "keep me",
	}

	desired := Object{"custom_fields": map[string]any{
		"audit_ticket": nil, "managed_by": "netbox-operator",
	}}

	drift := Drift(read(defined, stored), desired, FieldRules{})
	sent, ok := drift["custom_fields"].(map[string]any)
	if !ok {
		t.Fatalf("first pass did not PATCH custom_fields, so the removal never leaves: %v", drift)
	}
	if value, present := sent["audit_ticket"]; !present || value != nil {
		t.Fatalf("the PATCH carries audit_ticket=%v (present=%t), want an explicit null", value, present)
	}
	if _, present := sent["someone_elses"]; present {
		t.Fatalf("the PATCH names someone_elses; unmanaged custom fields must be left alone")
	}

	stored = write(stored, sent)

	if got := stored["someone_elses"]; got != "keep me" {
		t.Errorf("someone_elses = %v after the PATCH, want it untouched", got)
	}

	// Twice, because a diff that alternates rather than converges passes a single check.
	for pass := 2; pass <= 3; pass++ {
		if got := Drift(read(defined, stored), desired, FieldRules{}); len(got) != 0 {
			t.Fatalf("pass %d would PATCH %v -- the removal never settles", pass, got)
		}
	}
}

// write is CustomFieldsDataField.to_internal_value: the submitted keys merged over the
// stored ones, so a null overwrites and an unnamed key survives.
func write(stored, patch map[string]any) map[string]any {
	merged := maps.Clone(stored)
	maps.Copy(merged, patch)

	return merged
}

// read is CustomFieldsDataField.to_representation: every custom field defined for the object
// type, with null for the ones holding no value -- whether they were emptied or never set.
func read(defined []string, stored map[string]any) Object {
	fields := make(map[string]any, len(defined))
	for _, name := range defined {
		fields[name] = stored[name]
	}

	return Object{"custom_fields": fields}
}

// cableRules are the comparison rules internal/registry supplies for dcim.Cable: two to-many
// polymorphic fields over GenericObjectSerializer's `{object_type, object_id}`.
var cableRules = FieldRules{
	M2M: map[string]bool{"tags": true},
	GenericFKLists: map[string]GenericFKItem{
		"a_terminations": {TypeKey: "object_type", IDKey: "object_id"},
		"b_terminations": {TypeKey: "object_type", IDKey: "object_id"},
	},
}

// termination is one element of `a_terminations` as NetBox returns it: the two written keys
// plus the read-only `object` expansion GenericObjectSerializer adds
// (netbox/netbox/api/serializers/generic.py:15).
func termination(objectType string, id float64, expand bool) map[string]any {
	element := map[string]any{"object_type": objectType, "object_id": id}
	if expand {
		element["object"] = map[string]any{"id": id, "name": "eth0"}
	}

	return element
}

// TestGenericFKListComparison is the rule that decides whether a cable survives a resync.
//
// Getting it wrong is worse here than anywhere else in this file: dcim.Cable is
// `UpdateStrategy: Recreate` with both these fields in `RecreateOn`, so a diff that never
// settles is not a PATCH loop but a cable deleted and re-created on every resync -- which
// churns the changelog and rebuilds every CablePath through it each time.
func TestGenericFKListComparison(t *testing.T) {
	tests := []struct {
		name    string
		live    Object
		desired Object
		want    []string
	}{
		{
			name:    "identical single termination produces no drift",
			live:    Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{termination("dcim.interface", 41, false)}},
			want:    nil,
		},
		{
			name: "a reordered list is the same set",
			live: Object{"a_terminations": []any{
				termination("dcim.interface", 41, true), termination("dcim.interface", 17, true),
			}},
			desired: Object{"a_terminations": []any{
				termination("dcim.interface", 17, false), termination("dcim.interface", 41, false),
			}},
			want: nil,
		},
		{
			name: "an id the operator wrote as an int64 compares numerically",
			live: Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{
				map[string]any{"object_type": "dcim.interface", "object_id": int64(41)},
			}},
			want: nil,
		},
		{
			name:    "a different endpoint drifts",
			live:    Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{termination("dcim.interface", 42, false)}},
			want:    []string{"a_terminations"},
		},
		{
			name:    "the same id on a different model drifts",
			live:    Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{termination("dcim.rearport", 41, false)}},
			want:    []string{"a_terminations"},
		},
		{
			name: "adding a termination drifts",
			live: Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{
				termination("dcim.interface", 41, false), termination("dcim.interface", 17, false),
			}},
			want: []string{"a_terminations"},
		},
		{
			name:    "clearing the list drifts",
			live:    Object{"a_terminations": []any{termination("dcim.interface", 41, true)}},
			desired: Object{"a_terminations": []any{}},
			want:    []string{"a_terminations"},
		},
		{
			name:    "an already-empty list produces no drift",
			live:    Object{"a_terminations": []any{}},
			desired: Object{"a_terminations": []any{}},
			want:    nil,
		},
		{
			name: "the two ends are compared independently",
			live: Object{
				"a_terminations": []any{termination("dcim.interface", 41, true)},
				"b_terminations": []any{termination("dcim.interface", 42, true)},
			},
			desired: Object{
				"a_terminations": []any{termination("dcim.interface", 41, false)},
				"b_terminations": []any{termination("dcim.interface", 18, false)},
			},
			want: []string{"b_terminations"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Drift(tc.live, tc.desired, cableRules)

			if len(got) != len(tc.want) {
				t.Fatalf("Drift() = %v, want fields %v", got, tc.want)
			}

			for _, field := range tc.want {
				if _, present := got[field]; !present {
					t.Errorf("Drift() = %v, want it to carry %s", got, field)
				}
			}
		})
	}
}

// TestGenericFKListSurvivesAResync applies the payload, reads back the response NetBox would
// send for it, and asserts the second pass writes nothing -- the same shape as
// TestNoHotLoopOnRealResponses, for the rule this file's newest arm covers.
func TestGenericFKListSurvivesAResync(t *testing.T) {
	desired := Object{
		"a_terminations": []any{
			map[string]any{"object_type": "dcim.interface", "object_id": int64(17)},
			map[string]any{"object_type": "dcim.interface", "object_id": int64(41)},
		},
		"label": "patch-14",
	}

	// NetBox's response: row order rather than the order it was sent, ids as JSON numbers, and
	// the `object` expansion on each element.
	live := Object{
		"a_terminations": []any{
			termination("dcim.interface", 41, true), termination("dcim.interface", 17, true),
		},
		"label": "patch-14",
	}

	if got := Drift(live, desired, cableRules); len(got) != 0 {
		t.Fatalf("a second pass would write %v; the cable would be re-created every resync", got)
	}
}
