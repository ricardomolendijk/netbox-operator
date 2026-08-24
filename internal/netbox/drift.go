package netbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// FieldRules tells Drift how to compare the fields of one Kind whose representation on
// read differs from the representation on write.
//
// It is plain data supplied by the caller rather than a lookup into internal/registry,
// so Drift stays a pure function with no dependencies. That is what makes it
// exhaustively unit-testable and reusable by nbctl plan (NBO-038).
type FieldRules struct {
	// M2M fields come back as a list of nested objects and are written as a list of ids.
	// Compared as an order-independent id set. Examples: tags, VRF.import_targets,
	// Site.asns, Interface.wireless_lans, Service.ipaddresses.
	M2M map[string]bool

	// Arrays are Postgres ArrayFields, compared order-sensitively because the order is
	// data. Examples: VLANGroup.vid_ranges, Service.ports.
	Arrays map[string]bool

	// ObjectTypeLists hold Django ContentType strings ("dcim.device") rather than ids.
	// Compared as an order-independent string set. extras.Tag.object_types is the case.
	ObjectTypeLists map[string]bool

	// GenericFKs are (type, id) column pairs that form one logical reference and must be
	// diffed together. Examples: (scope_type, scope_id) on Prefix, Cluster, WirelessLAN
	// and VLANGroup; (assigned_object_type, assigned_object_id) on IPAddress.
	GenericFKs []GenericFK
}

// GenericFK names the two columns behind one polymorphic reference.
type GenericFK struct {
	TypeField string
	IDField   string
}

// Change is one field-level difference, kept in a form a renderer can align into
// "old → new" columns.
type Change struct {
	Field string
	Old   any
	New   any
}

// Drift returns the subset of desired whose values differ from live: exactly the payload
// to PATCH, and empty when nothing needs sending.
//
// Only fields present in desired are considered. A field the spec never sets is left as
// NetBox has it, which is what lets the operator co-exist with humans and with other
// tools editing the same object.
//
// Getting a normalisation wrong here does not fail loudly. It produces a diff that is
// never satisfied, so the operator PATCHes forever -- a hot loop against the NetBox API
// for as long as the object exists. Every rule below has a regression test that applies
// a payload, reads back a real NetBox response, and asserts the drift is empty.
func Drift(live, desired Object, rules FieldRules) Object {
	drift := Object{}
	for _, change := range Changes(live, desired, rules) {
		drift[change.Field] = change.New
	}
	return drift
}

// Changes is Drift with the old values retained, for reporting and for
// "old → new" rendering. Results are sorted by field name so output is stable.
func Changes(live, desired Object, rules FieldRules) []Change {
	handled := map[string]bool{}
	var changes []Change

	// Generic FKs first: a (type, id) pair is one reference, so a half-changed scope has
	// to be caught as a unit rather than produce two independent diffs that a partial
	// PATCH could apply inconsistently.
	for _, pair := range rules.GenericFKs {
		change, ok := genericFKChange(live, desired, pair)
		handled[pair.TypeField], handled[pair.IDField] = true, true
		if ok {
			changes = append(changes, change...)
		}
	}

	for field, want := range desired {
		if handled[field] {
			continue
		}
		have := live[field]
		if !fieldEqual(field, have, want, rules) {
			changes = append(changes, Change{Field: field, Old: have, New: want})
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
	return changes
}

// fieldEqual dispatches on the rules for one field. Guard clauses in precedence order:
// the most specific rule wins, and the scalar comparison is the fallback.
func fieldEqual(field string, have, want any, rules FieldRules) bool {
	if field == customFieldsKey {
		return customFieldsEqual(have, want)
	}
	if rules.M2M[field] {
		return sameIDSet(have, want)
	}
	if rules.ObjectTypeLists[field] {
		return sameStringSet(have, want)
	}
	if rules.Arrays[field] {
		return sameOrderedList(have, want)
	}
	return scalarEqual(unwrapNested(have), want)
}

// customFieldsKey is NetBox's custom-field container, present on every PrimaryModel.
const customFieldsKey = "custom_fields"

// customFieldsEqual compares only the keys the spec actually sets.
//
// NetBox returns *every* custom field defined for the object type, including ones this
// operator knows nothing about. Diffing the whole map would make the operator try to
// null out every unmanaged custom field on every reconcile -- and NetBox merges a partial
// custom_fields PATCH, so sending only the managed subset is both correct and sufficient.
func customFieldsEqual(have, want any) bool {
	desired, ok := want.(map[string]any)
	if !ok {
		return scalarEqual(have, want)
	}
	live, ok := have.(map[string]any)
	if !ok {
		live = map[string]any{}
	}
	for key, value := range desired {
		if !scalarEqual(unwrapNested(live[key]), value) {
			return false
		}
	}
	return true
}

// genericFKChange diffs a (type, id) pair as one unit. It returns the changes for both
// columns when either differs, so the PATCH can never move the id without the type.
func genericFKChange(live, desired Object, pair GenericFK) ([]Change, bool) {
	wantType, hasType := desired[pair.TypeField]
	wantID, hasID := desired[pair.IDField]
	if !hasType && !hasID {
		return nil, false
	}

	haveType, haveID := live[pair.TypeField], live[pair.IDField]
	typeSame := !hasType || scalarEqual(unwrapNested(haveType), wantType)
	idSame := !hasID || scalarEqual(unwrapNested(haveID), wantID)
	if typeSame && idSame {
		return nil, false
	}

	// Both columns are emitted even when only one changed: NetBox validates the pair
	// together, and a scope_id sent without its scope_type is rejected or, worse,
	// interpreted against the old type.
	changes := make([]Change, 0, 2)
	if hasType {
		changes = append(changes, Change{Field: pair.TypeField, Old: haveType, New: wantType})
	}
	if hasID {
		changes = append(changes, Change{Field: pair.IDField, Old: haveID, New: wantID})
	}
	return changes, true
}

// unwrapNested reduces NetBox's read representation of a related or choice field to the
// value that gets written.
//
// A foreign key reads back as a nested object and is written as an id. A choice field
// reads back as {"value","label"} and is written as the value. Both are the same shape
// on the wire -- a JSON object -- so the presence of an "id" key is what distinguishes
// them.
func unwrapNested(have any) any {
	nested, ok := have.(map[string]any)
	if !ok {
		return have
	}
	if id, ok := nested["id"]; ok {
		return id
	}
	if value, ok := nested["value"]; ok {
		return value
	}
	return have
}

// scalarEqual compares two values the way NetBox means them.
//
// Numbers are compared numerically because NetBox returns decimals as strings:
// u_height "1.00", vcpus "2.00", weight "10.50". A string compare would see "1.00" !=
// "1" and PATCH forever. Everything else falls back to a string compare, which handles
// bool, string and null uniformly.
func scalarEqual(have, want any) bool {
	if have == nil || want == nil {
		return have == nil && want == nil
	}

	if equal, decided := boolEqual(have, want); decided {
		return equal
	}

	haveNum, haveOK := toFloat(have)
	wantNum, wantOK := toFloat(want)
	if haveOK && wantOK {
		return haveNum == wantNum
	}

	// String comparison is the remaining case, and it is deliberate rather than lazy:
	// NetBox returns choice values, slugs and free text as strings, and a spec supplies
	// the same. A stricter same-type rule here would break the numeric widening above,
	// which is what keeps an int32 from the CRD comparing equal to the float64 that
	// comes back out of JSON.
	return fmt.Sprint(have) == fmt.Sprint(want)
}

// boolEqual settles a comparison where either side is a boolean, reporting whether it
// applied. It runs before the numeric and string paths because the fmt.Sprint fallback
// otherwise makes the bool true and the string "true" compare equal -- so a field NetBox
// returns as a bool and a spec supplying a string would silently agree, and the reverse
// mismatch would silently disagree forever.
func boolEqual(have, want any) (equal, decided bool) {
	haveBool, haveIsBool := have.(bool)
	wantBool, wantIsBool := want.(bool)
	if !haveIsBool && !wantIsBool {
		return false, false
	}
	return haveIsBool && wantIsBool && haveBool == wantBool, true
}

// sameIDSet compares an M2M field: a list of nested objects on read against a list of
// bare ids on write, order-independent because NetBox does not preserve the order.
func sameIDSet(have, want any) bool {
	return equalInts(sortedInts(nestedIDs(have)), sortedInts(IDsOf(want)))
}

// sameStringSet compares an object-type list ("dcim.device"), order-independent.
func sameStringSet(have, want any) bool {
	haveList, wantList := sortedStrings(ObjectTypesOf(have)), sortedStrings(ObjectTypesOf(want))
	if len(haveList) != len(wantList) {
		return false
	}
	for i := range haveList {
		if haveList[i] != wantList[i] {
			return false
		}
	}
	return true
}

// sameOrderedList compares an ArrayField, order-sensitively: for vid_ranges and ports
// the order is data, not incidental.
func sameOrderedList(have, want any) bool {
	haveList, haveOK := have.([]any)
	wantList, wantOK := want.([]any)
	if !haveOK || !wantOK {
		return scalarEqual(have, want)
	}
	if len(haveList) != len(wantList) {
		return false
	}
	for i := range haveList {
		if !scalarEqual(haveList[i], wantList[i]) {
			return false
		}
	}
	return true
}

// Hash returns a stable digest of obj, for use as a cheap short-circuit: if the payload
// has not changed since the last successful write, there is nothing to compare.
//
// It exists because NetBox canonicalises some values on write -- mac_address is
// upper-cased, a prefix is masked down to its network address, a hostname is lowercased
// -- so the request and the response legitimately differ. Comparing a stored hash of the
// last *applied* payload avoids having to teach Drift every canonicalisation NetBox
// performs, which is a list nobody can be sure is complete.
func Hash(obj Object) (string, error) {
	canonical, err := json.Marshal(canonicalise(obj))
	if err != nil {
		return "", fmt.Errorf("hashing payload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalise rewrites obj so that encoding/json emits a byte-stable form: maps are
// sorted by encoding/json already, but numbers must be normalised so that 2 and 2.00
// hash identically, matching scalarEqual's numeric comparison.
func canonicalise(value any) any {
	switch typed := value.(type) {
	case Object:
		return canonicaliseMap(typed)
	case map[string]any:
		return canonicaliseMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = canonicalise(item)
		}
		return out
	case string:
		// A numeric string is the same value as the number: NetBox returns decimals as
		// strings, and the hash must not depend on which side produced it.
		if number, err := strconv.ParseFloat(typed, 64); err == nil {
			return number
		}
		return typed
	default:
		if number, ok := toFloat(typed); ok {
			return number
		}
		return typed
	}
}

func canonicaliseMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, item := range in {
		out[key] = canonicalise(item)
	}
	return out
}
