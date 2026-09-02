package v1alpha1

import (
	"encoding/json"
	"testing"
)

// TestIPRangeMirrorsTheSizeNetBoxComputed is the read-back in one assertion: the count NetBox
// answered with reaches `status.size`, which is the only place it can go. `ipam.IPRange.size`
// is `editable=False` and set in `IPRange.save()` as `end - start + 1`
// (netbox/ipam/models/ip.py, NetBox 4.6.8), so it is in the descriptor's ReadOnly list and can
// never be a spec field.
func TestIPRangeMirrorsTheSizeNetBoxComputed(t *testing.T) {
	// The shape a real response has: decoded by encoding/json, so every number is a float64.
	// Built by decoding rather than by writing float64 literals, because the whole point of
	// the coercion is that the wire shape is not the Go shape.
	var live map[string]any
	if err := json.Unmarshal([]byte(`{
		"id": 31,
		"start_address": "10.0.30.128/24",
		"end_address": "10.0.30.191/24",
		"size": 64
	}`), &live); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	obj := &NetBoxIPRange{}

	if changed := obj.ObserveColumns(live); !changed {
		t.Error("ObserveColumns() = false on the pass that learned the value; the engine skips " +
			"the status write on a false, so the count would never be persisted")
	}

	if obj.Status.Size != 64 {
		t.Errorf("status.size = %d, want 64: .128 through .191 inclusive", obj.Status.Size)
	}

	// Idempotent: the same answer on the next pass is not a change, so a quiet resync does
	// not force a status write of its own.
	if changed := obj.ObserveColumns(live); changed {
		t.Error("ObserveColumns() = true for a value the status already carried; every resync " +
			"would then churn the object's resourceVersion for no new information")
	}
}

// TestIPRangeSizeSurvivesAResponseWithoutIt is the guard the SIZE printer column depends on.
// A suppressed DryRun write and a 204 with an empty body both arrive as objects that never
// mentioned the column, and blanking a count that is still correct would make the column
// blink to empty on every dry run.
func TestIPRangeSizeSurvivesAResponseWithoutIt(t *testing.T) {
	tests := map[string]map[string]any{
		"absent":      {"id": float64(31)},
		"null":        {"size": nil},
		"a string":    {"size": "64"},
		"an object":   {"size": map[string]any{"value": float64(64)}},
		"a dry run":   {"__dryRun": true},
		"a bare list": {"size": []any{float64(64)}},
	}

	for name, live := range tests {
		t.Run(name, func(t *testing.T) {
			obj := &NetBoxIPRange{}
			obj.Status.Size = 64

			if changed := obj.ObserveColumns(live); changed {
				t.Errorf("ObserveColumns() = true for a response carrying %s, so a status "+
					"write would be forced for a value nothing answered", name)
			}

			if obj.Status.Size != 64 {
				t.Errorf("status.size = %d after a response carrying %s, want the 64 the "+
					"previous pass learned", obj.Status.Size, name)
			}
		})
	}
}

// TestObserveColumnsIsAnOptIn is the capability half. A Kind that mirrors nothing does not
// implement the method, so the engine's entry point is a no-op on it -- there is no branch on
// Kind anywhere, and no Kind has to declare that it mirrors nothing.
func TestObserveColumnsIsAnOptIn(t *testing.T) {
	if _, mirrors := any(&NetBoxSite{}).(ObservedColumns); mirrors {
		t.Error("NetBoxSite implements ObservedColumns; dcim.Site has no computed column to " +
			"mirror, so this is a capability declared for nothing")
	}

	if _, mirrors := any(&NetBoxIPRange{}).(ObservedColumns); !mirrors {
		t.Error("NetBoxIPRange does not implement ObservedColumns, so status.size is never " +
			"written and the SIZE printer column is empty on every object")
	}

	// Neither arm may panic: `live` is nil on the paths that never reached NetBox, and the
	// safety belongs at the one entry point rather than in each implementation.
	ObserveColumns(&NetBoxSite{}, nil)
	ObserveColumns(&NetBoxIPRange{}, nil)

	obj := &NetBoxIPRange{}
	ObserveColumns(obj, map[string]any{"size": float64(1)})

	if obj.Status.Size != 1 {
		t.Errorf("status.size = %d after the entry point, want 1: a one-address range is "+
			"legal and is the smallest NetBox can compute", obj.Status.Size)
	}
}

// TestIPRangeStatusKeepsTheSharedEnvelopeWhereItWas is what makes the first non-plain status
// on a managed Kind safe: NetBoxObjectStatus is embedded inline, so every field the engine and
// every existing manifest reads is at the same JSON path it was before.
func TestIPRangeStatusKeepsTheSharedEnvelopeWhereItWas(t *testing.T) {
	obj := &NetBoxIPRange{}
	obj.Status.ID = 31
	obj.Status.Adopted = true
	obj.Status.Size = 64

	if obj.NetBoxStatus().ID != 31 {
		t.Errorf("NetBoxStatus().ID = %d, want 31: the engine writes through this accessor",
			obj.NetBoxStatus().ID)
	}

	encoded, err := json.Marshal(obj.Status)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}

	for _, key := range []string{"id", "adopted", "size"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("status.%s is not at the top level of the status: %s", key, encoded)
		}
	}
}
