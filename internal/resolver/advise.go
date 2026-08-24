package resolver

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// UngrantedRef is one cross-namespace `name` reference that no NetBoxRefGrant permits.
//
// It exists for the admission webhook (NBO-044) and for nothing else. Resolution reports the
// same fact as an *Error carrying ErrRefDenied, on one reference at a time, and stops at the
// first: that is the right shape for a condition, whose job is to name the one thing to fix
// next. Admission is the opposite shape -- the whole object is in front of it exactly once,
// and a manifest with four unauthorised references should hear about four rather than be
// told three more times.
type UngrantedRef struct {
	// Field is the spec field the reference is written under, which is the thing a reader
	// can edit.
	Field string

	// Target is the object it points at.
	Target RefNode

	// Detail is the same remedy the RefDenied condition carries: the grant to create, in
	// the namespace to create it in. One rendering for both, so a warning at apply time and
	// a condition at reconcile time do not read as two different problems.
	Detail string
}

// String renders the finding the way every other reference message renders one:
// `tenantRef -> netboxtenant/catalogue/acme: <remedy>`.
func (u UngrantedRef) String() string {
	return fmt.Sprintf("%s -> %s: %s", u.Field, u.Target, u.Detail)
}

// UngrantedRefs reports every `name`-mode reference in obj's spec that leaves its namespace
// with no NetBoxRefGrant covering it.
//
// Reads only grants and, for a `namespaces: Selector` grant, the referring namespace's
// labels -- never a target object and never NetBox. That is what makes it usable from
// admission: it is bounded by the number of *namespaces* the object points into rather than
// by the number of references it holds, and a same-namespace reference costs a string
// compare (see permits).
//
// It deliberately does not report a reference whose target does not exist. That is the
// ordinary WaitingForRef state of an order-independent apply -- NBO-017's whole property is
// that 500 manifests converge in any order -- so it is not a finding, and checking it would
// put one object read per reference on the admission path to say so.
func (r *Resolver) UngrantedRefs(
	ctx context.Context, obj client.Object, d registry.Descriptor,
) ([]UngrantedRef, error) {
	fields, err := refsOf(obj, d)
	if err != nil {
		return nil, err
	}

	from := nodeOf(d.GVK, obj)

	var found []UngrantedRef

	for _, declared := range fields {
		for _, element := range declared.elements() {
			ungranted, err := r.ungranted(ctx, from, element)
			if err != nil {
				return nil, err
			}

			if ungranted != nil {
				found = append(found, *ungranted)
			}
		}
	}

	return found, nil
}

// ungranted is UngrantedRefs for one reference, and nil for one there is nothing to say
// about.
func (r *Resolver) ungranted(ctx context.Context, from RefNode, element refElement) (*UngrantedRef, error) {
	if modeOf(element.ref) != ModeName {
		// The other three modes reach NetBox directly, with the referring namespace's own
		// token, and no grant anywhere gates them (see NetBoxRefGrant).
		return nil, nil
	}

	target := targetNode(from, element)
	if target.Key.Namespace == from.Key.Namespace {
		return nil, nil
	}

	permitted, check, err := r.permits(ctx, from.Key.Namespace, target.GVK.Kind, target.Key)
	if err != nil {
		return nil, fmt.Errorf("authorising %s -> %s: %w", element.field.Spec, target, err)
	}

	if permitted {
		return nil, nil
	}

	return &UngrantedRef{
		Field:  element.field.Spec,
		Target: target,
		Detail: check.detail(target.Key),
	}, nil
}

// nodeOf is obj as a node of the reference graph.
func nodeOf(gvk schema.GroupVersionKind, obj client.Object) RefNode {
	return RefNode{
		GVK: gvk,
		Key: types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()},
	}
}
