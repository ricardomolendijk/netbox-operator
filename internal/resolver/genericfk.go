package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// Generic foreign keys: a `*_type` / `*_id` column pair written from one CR spec field.
//
// The pair is one reference with two halves, and every failure mode here is a half written
// without the other -- a new id against the old type is not a partial update, it is a
// reference to a different object that happens to share a primary key. So this file resolves
// a union to *one* Result and the engine writes both columns from it or neither; there is no
// path that produces an id without an object type.
//
// Dispatch is a table lookup keyed on the union's JSON member name, taken from
// GenericFKSpec.Members. Not a type switch and not a switch on Kind: the member's target Kind
// arrives as data, and everything else about that Kind -- its endpoint, its `app_label.model`
// spelling -- comes off its own Descriptor, exactly as it does for an ordinary reference.

// GenericRequest is one polymorphic reference to resolve.
type GenericRequest struct {
	// NetBox is the referrer's NetBox, for the three modes that query it.
	NetBox LookupClient

	// Referrer is the object holding the reference, which supplies the default namespace.
	Referrer types.NamespacedName

	// ReferrerGVK is the referrer's Kind, so a self-reference is recognisable.
	ReferrerGVK schema.GroupVersionKind

	// Pair is the descriptor entry behind the two columns: which spec field they are
	// written from, which members the union offers, and which object types NetBox accepts.
	Pair registry.GenericFKSpec

	// Union is the members the object set, keyed by JSON member name. Empty means the field
	// is present and selects nothing, which clears both columns.
	Union map[string]netboxv1alpha1.ObjectRef

	// Index is which element of a to-many pair this union is. Ignored on a to-one pair,
	// where there is only ever the one.
	//
	// It exists for the message rather than for the resolution: a cable end may carry
	// several terminations, and "aTerminations: waiting for a reference" would not say which
	// one.
	Index int
}

// path is the spec field a refusal is reported against, indexed on a to-many pair.
func (req GenericRequest) path() string {
	if !req.Pair.ToMany() {
		return req.Pair.Spec
	}

	return ElementPath(req.Pair.Spec, req.Index)
}

// ResolveGenericFK resolves one union to the (object type, id) pair its columns are written
// from.
//
// An empty union is the zero Result and no error: the field was written and selects nothing,
// so both columns are cleared. That is distinct from a field the spec never set, which never
// reaches here at all -- spec omission means "do not manage", and conflating the two would
// make the operator clear a foreign key somebody else owns.
func (r *Resolver) ResolveGenericFK(ctx context.Context, req GenericRequest) (Result, error) {
	// Sorted, because the members named in a rejection message must not reorder between
	// passes: the message is what a human diffs against the manifest they just wrote.
	set := slices.Sorted(maps.Keys(req.Union))

	if len(set) == 0 {
		return Result{}, nil
	}

	if len(set) > 1 {
		// Unreachable through the API server: the union's CEL rule caps it at one. Reported
		// rather than resolved to the first member, because picking one would attach the
		// object to a target the user did not unambiguously ask for.
		return Result{}, req.refused(ErrRefMalformed, fmt.Sprintf(
			"%d members are set (%s), and a polymorphic reference selects exactly one target",
			len(set), strings.Join(set, ", ")))
	}

	member, declared := req.Pair.MemberFor(set[0])
	if !declared {
		return Result{}, req.refused(ErrRefTypeNotAllowed, fmt.Sprintf(
			"%q is not a member of this union; it accepts %v", set[0], req.Pair.MemberSpecs()))
	}

	return r.resolveMember(ctx, req, member)
}

// resolveMember resolves the one selected member as an ordinary reference and then holds the
// answer to the pair's allow list.
//
// An ordinary reference is exactly what a member is once its target Kind is known, so it goes
// through the same four modes, the same grant check and the same typed errors -- there is no
// second resolution path to keep in step with the first.
func (r *Resolver) resolveMember(
	ctx context.Context, req GenericRequest, member registry.GenericFKMember,
) (Result, error) {
	result, err := r.Resolve(ctx, Request{
		NetBox: req.NetBox, Referrer: req.Referrer, ReferrerGVK: req.ReferrerGVK,
		Field: registry.Field{
			Spec:  memberPath(req.Pair, req.Index, member.Spec),
			Class: registry.ClassRefOne, Target: member.Target,
		},
		Ref: req.Union[member.Spec],
	})
	if err != nil {
		return Result{}, err
	}

	// After resolution rather than before, because the object type is the target
	// Descriptor's answer and not something this pair gets to assert. Registry.Validate
	// already refuses a descriptor whose members disagree with its allowed types, so
	// reaching this is either a stored object whose CRD has since narrowed, or a build
	// where the two were changed apart -- both of which have to be reported, not written.
	if !slices.Contains(req.Pair.AllowedTypes, result.ObjectType) {
		return Result{}, req.refused(ErrRefTypeNotAllowed, fmt.Sprintf(
			"%s resolves to object type %q, and %s accepts only %v",
			member.Spec, result.ObjectType, req.Pair.TypeField, req.Pair.AllowedTypes))
	}

	return result, nil
}

// refused builds the typed error for a union this request will not write, reported against
// the union's own spec field so the condition names what the user typed.
func (req GenericRequest) refused(cause error, detail string) *Error {
	return &Error{Cause: cause, Field: req.path(), Detail: detail}
}

// declaredGeneric is one polymorphic reference the descriptor declares and the object set.
type declaredGeneric struct {
	pair registry.GenericFKSpec

	// unions are the union values written under the pair's spec field: exactly one for a
	// to-one pair, and one per list element for a to-many one, in the order the manifest
	// listed them.
	//
	// A to-one pair written empty is one empty union, which is the instruction to clear both
	// columns. A to-many pair written `[]` is *no* unions, which is the instruction to clear
	// the whole list -- the same distinction one level out, and the reason this is a slice
	// rather than an optional single value.
	unions []map[string]netboxv1alpha1.ObjectRef
}

// memberPath is the spec path a union member is reported under, which is the spelling a
// condition names and the spelling to grep a manifest for.
//
// Indexed on a to-many pair -- `aTerminations[1].interfaceRef` -- because "one of the
// terminations did not resolve" is not an actionable message when a cable end may carry
// several. Unindexed on a to-one pair, so no existing message changes.
func memberPath(pair registry.GenericFKSpec, index int, member string) string {
	if !pair.ToMany() {
		return pair.Spec + "." + member
	}

	// ElementPath rather than a second Sprintf: the indexed form appears in three messages a
	// human greps for now -- a blocker's refusal, a resolved-but-unready note, and this -- and
	// two spellings of one path is one of them being wrong.
	return ElementPath(pair.Spec, index) + "." + member
}

// genericFKsOf reads the polymorphic unions out of obj's spec, in descriptor order.
//
// Through the object's JSON form, like refsOf and like the engine's payload builder. That is
// what lets the same code read a typed CR out of a cache and an *unstructured.Unstructured out
// of the cycle walk, and it is why the union's member names are declared as data on the
// GenericFKSpec: a Go type assertion would need the concrete union type, which an unstructured
// object does not have.
//
// A union that is absent is not returned. A union that is present and empty is, with an empty
// map -- that is the instruction to clear both columns.
func genericFKsOf(obj client.Object, d registry.Descriptor) ([]declaredGeneric, error) {
	if len(d.GenericFKs) == 0 {
		return nil, nil
	}

	spec, err := SpecMap(obj)
	if err != nil {
		return nil, err
	}

	generics := make([]declaredGeneric, 0, len(d.GenericFKs))

	for _, pair := range d.GenericFKs {
		raw, set := spec[pair.Spec]
		if !set || string(raw) == "null" {
			continue
		}

		unions, err := decodeUnions(pair, raw)
		if err != nil {
			return nil, fmt.Errorf("decoding %s of %s/%s: %w",
				pair.Spec, obj.GetNamespace(), obj.GetName(), err)
		}

		generics = append(generics, declaredGeneric{pair: pair, unions: unions})
	}

	return generics, nil
}

// decodeUnions decodes the pair's spec value into one union per polymorphic reference it
// carries.
//
// This is the only place in the resolver that reads GenericFKSpec.List. Everything downstream
// works a single union at a time, so a cable's list of terminations resolves through exactly
// the code one prefix's scope does -- same four modes, same grant check, same typed errors.
func decodeUnions(
	pair registry.GenericFKSpec, raw json.RawMessage,
) ([]map[string]netboxv1alpha1.ObjectRef, error) {
	if !pair.ToMany() {
		union, err := decodeUnion(raw)
		if err != nil {
			return nil, err
		}

		return []map[string]netboxv1alpha1.ObjectRef{union}, nil
	}

	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, fmt.Errorf("reading the list of unions: %w", err)
	}

	unions := make([]map[string]netboxv1alpha1.ObjectRef, 0, len(elements))

	for i, element := range elements {
		union, err := decodeUnion(element)
		if err != nil {
			return nil, fmt.Errorf("reading element %d: %w", i, err)
		}

		unions = append(unions, union)
	}

	return unions, nil
}

// decodeUnion decodes one union, dropping the members that are present and null.
//
// Present-and-null is dropped rather than kept as an empty reference because that is what
// `interfaceRef: null` means in a patch: this member is not the one selected. Kept, it would
// read as two members set and be refused as malformed.
func decodeUnion(raw json.RawMessage) (map[string]netboxv1alpha1.ObjectRef, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, fmt.Errorf("reading the union's members: %w", err)
	}

	union := make(map[string]netboxv1alpha1.ObjectRef, len(members))

	for name, value := range members {
		if string(value) == "null" {
			continue
		}

		var ref netboxv1alpha1.ObjectRef
		if err := json.Unmarshal(value, &ref); err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}

		union[name] = ref
	}

	return union, nil
}

// genericMemberRefs is every set union member as an ordinary reference, so the ref index and
// the watches behind it treat a polymorphic target exactly like a typed one.
//
// The spec field it reports is the union's own path -- `assignedObject.interfaceRef` -- which
// is the spelling a condition names and the spelling `kubectl explain` accepts.
func genericMemberRefs(generics []declaredGeneric) []fieldRefs {
	refs := make([]fieldRefs, 0, len(generics))

	for _, generic := range generics {
		for i, union := range generic.unions {
			for _, name := range slices.Sorted(maps.Keys(union)) {
				member, declared := generic.pair.MemberFor(name)
				if !declared {
					continue
				}

				// One reference under one field, always: a union selects one member, so
				// ClassRefOne is the whole of its cardinality and FieldRefs.elements() yields
				// exactly the single element the index keys on. A to-many pair is N such
				// fields rather than one field carrying N, so every termination gets its own
				// index key and any one of them wakes the cable.
				refs = append(refs, fieldRefs{
					field: registry.Field{
						Spec:   memberPath(generic.pair, i, member.Spec),
						Class:  registry.ClassRefOne,
						Target: member.Target,
					},
					refs: []netboxv1alpha1.ObjectRef{union[name]},
				})
			}
		}
	}

	return refs
}

// objectTypes is the reverse of Descriptor.ObjectType: an `app_label.model` string back to
// the Kind that answers for it. Satisfied by *registry.Registry.
//
// A generic FK is declared in NetBox's vocabulary and watched in Kubernetes's, so something
// has to join the two. Without it AllowedTypes named no Kind at all, so a polymorphic
// reference had no Kind to watch and no key to index, and converged only on the referrer's
// resync (NBO-013, #25).
type objectTypes interface {
	ByObjectType(objectType string) (registry.Descriptor, bool)
}

// genericTargets are the distinct Kinds d's polymorphic pairs may point at, in descriptor
// order, and only the ones this build carries a Descriptor for.
//
// Derived from GenericFKSpec.AllowedTypes through the reverse index rather than from the union
// members, because AllowedTypes is the statement about what NetBox accepts in that column and
// therefore about what an event could ever arrive for. A type nothing is registered for is
// left out: there is no informer to watch and no key an index could produce, and the resolver
// already reports such a member as RefKindUnavailable.
func genericTargets(lookup objectTypes, d registry.Descriptor) []schema.GroupVersionKind {
	targets := make([]schema.GroupVersionKind, 0, len(d.GenericFKs))

	for _, pair := range d.GenericFKs {
		for _, objectType := range pair.AllowedTypes {
			target, registered := lookup.ByObjectType(objectType)
			if !registered || slices.Contains(targets, target.GVK) {
				continue
			}

			targets = append(targets, target.GVK)
		}
	}

	return targets
}
