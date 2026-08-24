package resolver

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The two field indexes that make the reverse edge of a reference queryable.
//
// Without them the only way to answer "who points at this object" is to list every object
// of every kind and decode each one's spec, which is a full cache walk per target event --
// so the watch that makes a graph converge would cost more than the resync it replaces.
const (
	// RefIndex indexes every `name`-mode reference an object holds, as the target's
	// `<kindlower>/<namespace>/<name>` -- the same spelling RefNode renders, so a key and
	// the message a human reads cannot drift apart.
	RefIndex = "spec.refs"

	// RefNamespaceIndex indexes the *other* namespaces an object's references reach into,
	// which is what a NetBoxRefGrant event needs: a grant lives in the namespace being
	// referenced and authorises referrers that are somewhere else, so the query is by
	// namespace and not by object.
	RefNamespaceIndex = "spec.refNamespaces"
)

// IndexValue is the RefIndex key for one target object.
//
// Kind names cannot contain `/`, and neither can a namespace or a name, so the three-part
// encoding is unambiguous without escaping.
func IndexValue(gvk schema.GroupVersionKind, namespace, name string) string {
	return RefNode{GVK: gvk, Key: types.NamespacedName{Namespace: namespace, Name: name}}.String()
}

// AddIndexes registers both indexes for every descriptor that declares a reference.
//
// One field name reused across every referring kind, each index holding that kind's own
// references: the alternative -- one index per (kind, ref field) -- multiplies the
// registrations by the number of reference fields a kind has and buys nothing, because the
// precision is in the *value*. A key names the target's Kind, namespace and name, so a
// lookup is already exact; knowing which field matched would tell the referrer's reconcile
// nothing it does not re-derive from the spec anyway.
//
// A kind with no references is skipped rather than given an empty index. An index function
// runs on every write of every object of its type and this one encodes the object to JSON
// to read its spec, so registering it for a kind that can never yield a key is real work
// per write for a permanently empty map.
//
// Called once, before the cache starts. Registering the same (type, field) twice is a
// controller-runtime error, and it has to surface as a boot failure: an index that is
// silently absent turns every watch built on it into a reconcile that never happens.
func AddIndexes(ctx context.Context, fi client.FieldIndexer, s *runtime.Scheme, ds []registry.Descriptor) error {
	for _, d := range ds {
		if len(refFields(d)) == 0 {
			continue
		}

		obj, err := newObject(s, d.GVK)
		if err != nil {
			return err
		}

		if err := fi.IndexField(ctx, obj, RefIndex, refIndexer(d)); err != nil {
			return fmt.Errorf("indexing %s by %s: %w", d.GVK.Kind, RefIndex, err)
		}

		if err := fi.IndexField(ctx, obj, RefNamespaceIndex, refNamespaceIndexer(d)); err != nil {
			return fmt.Errorf("indexing %s by %s: %w", d.GVK.Kind, RefNamespaceIndex, err)
		}
	}

	return nil
}

// refFields are the reference fields of d that a Kubernetes event can ever arrive for: a
// declared reference with a target Kind to watch.
//
// A Ref with no Target is left out because there is nothing to index it under. The resolver
// reports such a field as RefKindUnavailable, which is a descriptor gap rather than a
// manifest error, and it is not made better by an index key of `//ns/name`.
//
// A polymorphic reference contributes one entry per union member. They share the union's
// spec name, which is right: only the Target is read from here -- by RefTargets, to know
// what to watch -- and a NetBoxSite becoming Ready has to wake every object that could be
// scoped to it, whichever member of the union names it.
func refFields(d registry.Descriptor) []registry.Field {
	fields := make([]registry.Field, 0, len(d.Fields))

	for _, field := range d.Fields {
		if field.Ref && !field.Target.Empty() {
			fields = append(fields, field)
		}
	}

	for _, generic := range d.GenericFKs {
		for _, member := range generic.Members {
			fields = append(fields, registry.Field{Spec: generic.Spec, Ref: true, Target: member.Target})
		}
	}

	return fields
}

// RefTargets are the distinct Kinds d references by name, in descriptor order.
//
// Distinct, because a kind with three references into one catalogue Kind needs one watch on
// it and not three: the map function behind the watch finds referrers through the index,
// which does not care which field produced the key.
func RefTargets(d registry.Descriptor) []schema.GroupVersionKind {
	targets := make([]schema.GroupVersionKind, 0, len(d.Fields))

	for _, field := range refFields(d) {
		if !slices.Contains(targets, field.Target) {
			targets = append(targets, field.Target)
		}
	}

	return targets
}

// refIndexer indexes one object's `name`-mode references by the object each points at.
//
// Only `name` mode is indexed, and that is what bounds the whole index: a `slug`, a
// `lookup` or an `id` terminates in NetBox, where there is no Kubernetes object an event
// could arrive for. Indexing one would create a key nothing ever queries.
func refIndexer(d registry.Descriptor) client.IndexerFunc {
	return func(obj client.Object) []string {
		targets := nameRefTargets(obj, d)
		keys := make([]string, 0, len(targets))

		for _, target := range targets {
			// Deduplicated here rather than in the map function: two references on one
			// object pointing at one target are one edge, and one key keeps the stored
			// index honest as well as the requests that come out of it.
			if key := target.String(); !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}

		return keys
	}
}

// refNamespaceIndexer indexes one object by the namespaces its references cross into.
//
// Its own namespace is excluded, because a reference that stays put is never authorised
// against anything (grants.go): including it would make a grant in a busy namespace wake
// every object in that namespace that holds any reference at all, for a grant that changes
// nothing about them.
func refNamespaceIndexer(d registry.Descriptor) client.IndexerFunc {
	return func(obj client.Object) []string {
		namespaces := make([]string, 0, len(d.Fields))

		for _, target := range nameRefTargets(obj, d) {
			namespace := target.Key.Namespace
			if namespace == obj.GetNamespace() || slices.Contains(namespaces, namespace) {
				continue
			}

			namespaces = append(namespaces, namespace)
		}

		return namespaces
	}
}

// nameRefTargets is every object obj points at by name, each in the namespace its reference
// resolves in.
//
// An object whose spec will not decode indexes as nothing. There is no way to report an
// error from an index function and nothing useful to do with one here: the same object's own
// reconcile reads the same spec through the same code and reports it there, where there is a
// condition to write it to.
func nameRefTargets(obj client.Object, d registry.Descriptor) []RefNode {
	refs, err := refsOf(obj, d)
	if err != nil {
		return nil
	}

	from := RefNode{GVK: d.GVK, Key: types.NamespacedName{
		Namespace: obj.GetNamespace(), Name: obj.GetName(),
	}}
	targets := make([]RefNode, 0, len(refs))

	for _, ref := range refs {
		if modeOf(ref.ref) != ModeName || ref.field.Target.Empty() {
			continue
		}

		targets = append(targets, targetNode(from, ref))
	}

	return targets
}

// newObject returns an empty typed object of gvk from the scheme.
//
// The typed object and not an unstructured one: an index is registered against the informer
// for a type, and an unstructured registration would build a second cache that the typed
// client's List -- and therefore every map function -- would never query.
func newObject(s *runtime.Scheme, gvk schema.GroupVersionKind) (client.Object, error) {
	made, err := s.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("resolving the go type of %s: %w", gvk, err)
	}

	obj, ok := made.(client.Object)
	if !ok {
		return nil, fmt.Errorf("%s is registered in the scheme as %T, which is not a client.Object", gvk, made)
	}

	return obj, nil
}
