package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// specMap is one object's spec as JSON names to undecoded values, which is the one
// representation registry.Field.Spec, KeyField.Spec and the payload builder all agree on.
type specMap map[string]json.RawMessage

// endpointRefSpec is the envelope field naming the NetBox an object is written to. Two
// objects at different endpoints are at different NetBoxes, so an identical natural key is
// two different objects and not a collision.
const endpointRefSpec = "endpointRef"

// declared reports the value obj set for a spec field, and false for one it did not.
//
// Empty counts as not declared, for every spelling of empty the API admits: `""`, `null`,
// `{}` (an OptionalRef saying "explicitly none") and `[]`. That matches what
// registry.NaturalKey.Applicable means by declared -- a candidate pinning a filter to null is
// usable exactly while the spec field behind it is empty -- and it is applied identically to
// both objects being compared, so the comparison stays symmetric.
func (s specMap) declared(name string) (string, bool) {
	raw, present := s[name]
	if !present {
		return "", false
	}

	switch value := string(raw); value {
	case "", "null", `""`, "{}", "[]":
		return "", false
	default:
		return value, true
	}
}

// state is what registry.NaturalKey.Applicable is evaluated against.
//
// Declared and Resolved are deliberately the same set, which is admission's honest position:
// it compares the values two manifests *wrote* rather than the NetBox ids they will resolve
// to, because resolving would mean calling NetBox from the admission path. That makes the
// check conservative -- two objects naming one site, one by `name` and one by `slug`, do
// collide in NetBox and are not reported here -- and conservative is the only safe direction
// for a rule whose verdict is Deny.
func (s specMap) state() registry.SpecState {
	declared := make([]string, 0, len(s))

	for name := range s {
		if _, set := s.declared(name); set {
			declared = append(declared, name)
		}
	}

	slices.Sort(declared)

	return registry.SpecState{Declared: declared, Resolved: declared}
}

// naturalKey is the identity the engine would look one object up in NetBox by.
type naturalKey struct {
	// candidate is the index of the candidate used, in the Descriptor's priority order.
	//
	// Part of the identity rather than incidental, because the engine looks an object up by
	// its *first* applicable candidate: two objects agreeing on a lower-priority one are not
	// reading the same NetBox row. An ipam.VRF with `(rd, name)` is found by `rd` while a
	// sibling carrying only `name` is found by `name`, and those are two legitimate VRFs.
	//
	// With today's descriptors the rendered values happen to distinguish the same pairs on
	// their own -- a null pin renders as `parentRef=null`, so dcim.Region's two candidates
	// cannot collide by accident -- so this is the explicit statement of a property that is
	// currently also true by construction. It is kept because the direction of the error
	// matters: a missing guard here rejects a correct manifest, which is the worse half of a
	// Deny rule getting it wrong.
	candidate int

	// values renders the candidate's matched values and its null pins, in descriptor order.
	values string
}

// String renders the key the way a rejection message needs to quote it.
func (k naturalKey) String() string { return k.values }

// keyOf is the natural key the engine would identify obj by, and false when it could not
// identify it yet.
func keyOf(d registry.Descriptor, spec specMap) (naturalKey, bool) {
	state := spec.state()

	for index, candidate := range d.NaturalKeys {
		if !candidate.Applicable(state) {
			continue
		}

		values, ok := identity(candidate, spec)

		return naturalKey{candidate: index, values: values}, ok
	}

	return naturalKey{}, false
}

// identity renders the spec values one candidate matches on.
func identity(candidate registry.NaturalKey, spec specMap) (string, bool) {
	parts := make([]string, 0, len(candidate.Fields)+len(candidate.NullFields))

	for _, field := range candidate.Fields {
		value, set := spec.declared(field.Spec)
		if !set {
			return "", false
		}

		parts = append(parts, field.Spec+"="+value)
	}

	for _, pinned := range candidate.NullFields {
		if _, set := spec.declared(pinned.Spec); set {
			return "", false
		}

		parts = append(parts, pinned.Spec+"=null")
	}

	if len(parts) == 0 {
		return "", false
	}

	return strings.Join(parts, ", "), true
}

// allowsDuplicates reports that this object opted out of having a natural key at all.
//
// Registry-derived, which is exactly why it cannot be CEL: Descriptor.DuplicateSpec names the
// spec field that declares several NetBox objects may match this one, with the provenance
// stamp deciding which is the CR's own (#177, NBO-025). A CRD schema has no view of that, so
// a CEL rule could only either ignore the field or hard-code the one Kind that has it.
func allowsDuplicates(d registry.Descriptor, spec specMap) bool {
	if d.DuplicateSpec == "" {
		return false
	}

	value, set := spec.declared(d.DuplicateSpec)

	return set && value == "true"
}

// collision denies a natural-key collision with another CR of the same Kind, in the same
// namespace, at the same endpoint.
//
// **Deny.** Two CRs identifying one NetBox object is not a state that converges: whichever
// reconciles second reports `Conflict` and writes nothing, forever, and its manifest looks
// correct in isolation. Only a sibling makes it wrong, which is why CEL cannot see it.
//
// A collision across *namespaces* is deliberately not checked -- neither denied nor warned.
// Telling a namespaced actor, through a rejection message, what exists in a namespace they
// cannot read is an information leak, and it is the residual footgun ADR-0002 already accepts
// and the runtime `Conflict` condition already reports.
func (r *objectReview) collision(ctx context.Context) (string, error) {
	if allowsDuplicates(r.desc, r.spec) {
		return "", nil
	}

	mine, identified := keyOf(r.desc, r.spec)
	if !identified {
		return "", nil
	}

	siblings, err := r.siblings(ctx)
	if err != nil {
		return "", err
	}

	for _, sibling := range siblings {
		clashes, err := r.clashes(sibling, mine)
		if err != nil {
			return "", err
		}

		if clashes {
			return fmt.Sprintf(
				"%s %q in namespace %q already identifies the same NetBox object: both match on "+
					"%s through spec.endpointRef %q. Whichever reconciles second will report "+
					"Conflict and write nothing, so change one of the two keys.",
				strings.ToLower(r.desc.GVK.Kind), sibling.GetName(), r.obj.GetNamespace(),
				mine, endpointOf(r.spec)), nil
		}
	}

	return "", nil
}

// clashes reports whether sibling would be looked up in NetBox by the same key.
func (r *objectReview) clashes(sibling client.Object, mine naturalKey) (bool, error) {
	if sibling.GetName() == r.obj.GetName() {
		return false, nil
	}

	if !sibling.GetDeletionTimestamp().IsZero() {
		// On its way out, and its finalizer is what keeps it here. Denying against an object
		// being deleted would make replacing a CR -- delete, re-apply -- fail for as long as
		// NetBox takes to answer the delete.
		return false, nil
	}

	spec, err := resolver.SpecMap(sibling)
	if err != nil {
		return false, fmt.Errorf("reading the spec of %s: %w", sibling.GetName(), err)
	}

	if endpointOf(spec) != endpointOf(r.spec) {
		return false, nil
	}

	theirs, identified := keyOf(r.desc, spec)

	return identified && theirs == mine, nil
}

// endpointOf is the endpoint a spec writes through.
func endpointOf(spec specMap) string {
	value, _ := spec.declared(endpointRefSpec)

	return value
}

// endpointRefOf is endpointOf with the JSON quoting stripped, for a message and a lookup key.
func endpointRefOf(spec specMap) string {
	var name string
	if err := json.Unmarshal([]byte(endpointOf(spec)), &name); err != nil {
		return ""
	}

	return name
}

// siblings are the other objects of this Kind in this namespace.
//
// Typed, so the read goes through the manager's informer cache: the identical read as an
// *unstructured.UnstructuredList would go live to the API server, and a live list per apply is
// how p99 admission latency becomes API-server latency (NBO-044, the no-live-reads rule).
//
// The List Kind is derived from the object Kind rather than looked up per Kind, which is what
// keeps this package free of a per-Kind table: every generated `NetBoxXList` is registered by
// the same SchemeBuilder as its `NetBoxX`.
func (r *objectReview) siblings(ctx context.Context) ([]client.Object, error) {
	listGVK := r.desc.GVK.GroupVersion().WithKind(r.desc.GVK.Kind + "List")

	typed, err := r.scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("resolving %s against the scheme: %w", listGVK.Kind, err)
	}

	list, ok := typed.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("%s is not a cluster object list", listGVK.Kind)
	}

	if err := r.read.List(ctx, list, client.InNamespace(r.obj.GetNamespace())); err != nil {
		return nil, fmt.Errorf("listing %s in %s: %w", listGVK.Kind, r.obj.GetNamespace(), err)
	}

	items, err := apimeta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("reading the items of %s: %w", listGVK.Kind, err)
	}

	objects := make([]client.Object, 0, len(items))

	for _, item := range items {
		if object, isObject := item.(client.Object); isObject {
			objects = append(objects, object)
		}
	}

	return objects, nil
}
