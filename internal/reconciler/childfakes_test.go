package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The fake parent and child kinds exist only here, and that is the point of them: if the
// materialiser needed a real NetBoxVirtualMachine to be testable, it would not be the generic
// engine NBO-032 asks for and NBO-033 could not be per-kind wiring with no logic in it.
var (
	_ ChildWriter                 = (*fakeChildren)(nil)
	_ Object                      = (*inlineKind)(nil)
	_ netboxv1alpha1.InlineParent = (*inlineKind)(nil)
	_ Object                      = (*childKind)(nil)
)

var (
	inlineGVK = schema.GroupVersionKind{
		Group: netboxv1alpha1.GroupName, Version: "v1alpha1", Kind: "NetBoxInlineFake"}
	childGVK = schema.GroupVersionKind{
		Group: netboxv1alpha1.GroupName, Version: "v1alpha1", Kind: "NetBoxChildFake"}
	otherChildGVK = schema.GroupVersionKind{
		Group: netboxv1alpha1.GroupName, Version: "v1alpha1", Kind: "NetBoxOtherChildFake"}
)

// inlineChild is one inline entry as a user writes it: a key and the fields the child takes.
type inlineChild struct {
	key      string
	children []inlineChild
}

// inlineKind is a parent Kind that carries two inline lists, so that a test can prove the
// discriminator keeps two child kinds under one parent apart and that nesting works without
// the depth being baked in.
type inlineKind struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   inlineSpec                        `json:"spec,omitempty"`
	Status netboxv1alpha1.NetBoxObjectStatus `json:"status,omitempty"`
}

// inlineSpec carries the two inline lists.
type inlineSpec struct {
	netboxv1alpha1.NetBoxObjectSpec `json:",inline"`

	Name       string        `json:"name,omitempty"`
	Interfaces []inlineChild `json:"interfaces,omitempty"`
	Disks      []inlineChild `json:"disks,omitempty"`
}

func (i *inlineKind) NetBoxSpec() *netboxv1alpha1.NetBoxObjectSpec     { return &i.Spec.NetBoxObjectSpec }
func (i *inlineKind) NetBoxStatus() *netboxv1alpha1.NetBoxObjectStatus { return &i.Status }

func (i *inlineKind) DeepCopyObject() runtime.Object {
	out := *i
	i.DeepCopyInto(&out.ObjectMeta)
	out.Status = *i.Status.DeepCopy()
	out.Spec.Interfaces = slices.Clone(i.Spec.Interfaces)
	out.Spec.Disks = slices.Clone(i.Spec.Disks)

	return &out
}

// InlineChildren is the per-kind wiring NBO-033 will write for a real VM: two lists, one
// nesting a second level, and no engine logic anywhere in it.
func (i *inlineKind) InlineChildren() []netboxv1alpha1.InlineChildSet {
	sets := []netboxv1alpha1.InlineChildSet{{Field: "interfaces"}, {Field: "disks", Discriminator: "disk"}}

	for _, iface := range i.Spec.Interfaces {
		entry := netboxv1alpha1.InlineChildEntry{
			Key:     iface.key,
			Desired: newChild(childGVK, iface.key),
		}

		addresses := netboxv1alpha1.InlineChildSet{Field: "addresses", Discriminator: "ip"}
		for _, address := range iface.children {
			addresses.Entries = append(addresses.Entries, netboxv1alpha1.InlineChildEntry{
				Key:     address.key,
				Desired: newChild(otherChildGVK, address.key),
			})
		}

		if len(addresses.Entries) > 0 {
			entry.Children = []netboxv1alpha1.InlineChildSet{addresses}
		}

		sets[0].Entries = append(sets[0].Entries, entry)
	}

	for _, disk := range i.Spec.Disks {
		sets[1].Entries = append(sets[1].Entries, netboxv1alpha1.InlineChildEntry{
			Key:     disk.key,
			Desired: newChild(childGVK, disk.key),
		})
	}

	return sets
}

// childKind is a materialised child: the shared envelope, so that inheritance is testable,
// plus one field of its own so that a hand edit has something to land on.
type childKind struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   childSpec                         `json:"spec,omitempty"`
	Status netboxv1alpha1.NetBoxObjectStatus `json:"status,omitempty"`
}

// childSpec is a child's spec.
type childSpec struct {
	netboxv1alpha1.NetBoxObjectSpec `json:",inline"`

	Name string `json:"name,omitempty"`
}

func (c *childKind) NetBoxSpec() *netboxv1alpha1.NetBoxObjectSpec     { return &c.Spec.NetBoxObjectSpec }
func (c *childKind) NetBoxStatus() *netboxv1alpha1.NetBoxObjectStatus { return &c.Status }

func (c *childKind) DeepCopyObject() runtime.Object {
	out := *c
	c.DeepCopyInto(&out.ObjectMeta)
	out.Status = *c.Status.DeepCopy()

	return &out
}

// newChild is what a per-kind InlineChildren() hands over: everything but the name, the
// labels, the annotations and the owner references, which the materialiser owns.
func newChild(gvk schema.GroupVersionKind, name string) *childKind {
	child := &childKind{}
	child.SetGroupVersionKind(gvk)
	child.Spec.Name = name

	return child
}

// inlineScheme knows the parent kind and both child kinds.
func inlineScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(fakeGVK, &fakeKind{})
	scheme.AddKnownTypeWithName(inlineGVK, &inlineKind{})
	scheme.AddKnownTypeWithName(childGVK, &childKind{})
	scheme.AddKnownTypeWithName(otherChildGVK, &childKind{})

	return scheme
}

// inlineParent is a parent with a NetBox id, an endpoint and a deletion policy to inherit,
// and free text that must *not* be inherited.
func inlineParent(interfaces ...inlineChild) *inlineKind {
	parent := &inlineKind{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a", Name: "dns", UID: types.UID("parent-uid"), Generation: 2,
		},
		Spec: inlineSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{
				EndpointRef:    "homelab",
				DeletionPolicy: netboxv1alpha1.DeletionRetain,
				CustomFields:   map[string]*string{"owner": nil},
			},
			Name:       "dns",
			Interfaces: interfaces,
		},
	}
	parent.SetGroupVersionKind(inlineGVK)
	parent.Status.ID = 7

	return parent
}

// fakeChildren is the API server as far as the materialiser is concerned: a store keyed by
// kind and name, and a record of every call.
//
// Hand-written rather than controller-runtime's fake client, because the fake client's apply
// support does not write the response back into the object and clears its TypeMeta on the way
// through -- so it can neither answer "is this child Ready" nor be applied twice. The
// server-side-apply behaviour that needs a real API server is asserted in envtest instead
// (internal/controller/children_test.go); this fake is for the decisions, which are all here.
type fakeChildren struct {
	// store is what the API server holds, as unstructured so that a hand-written object can
	// be planted without a Go type for it.
	store map[string]*unstructured.Unstructured

	// applied records every object applied, in order, as it was applied.
	applied []*unstructured.Unstructured

	// forced records which of those applies carried client.ForceOwnership.
	forced []string

	// deleted records every DELETE as "Kind/name".
	deleted []string

	// conflictOn makes the first unforced apply of that name fail with a 409, which is how a
	// hand-edited field reaches the API server's conflict message.
	conflictOn string
	conflicted map[string]bool

	// listErr fails every List, for the "the API server is down" path.
	listErr error
}

func newFakeChildren() *fakeChildren {
	return &fakeChildren{
		store:      map[string]*unstructured.Unstructured{},
		conflicted: map[string]bool{},
	}
}

// plant puts an object into the store as if a human had applied it.
func (f *fakeChildren) plant(gvk schema.GroupVersionKind, name string,
	labels map[string]string, annotations map[string]string, owners ...metav1.OwnerReference,
) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace("team-a")
	obj.SetName(name)
	obj.SetLabels(labels)
	obj.SetAnnotations(annotations)
	obj.SetOwnerReferences(owners)
	f.store[gvk.Kind+"/"+name] = obj
}

func (f *fakeChildren) Get(_ context.Context, key client.ObjectKey, obj client.Object,
	_ ...client.GetOption,
) error {
	kind := obj.GetObjectKind().GroupVersionKind().Kind

	held, ok := f.store[kind+"/"+key.Name]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Group: netboxv1alpha1.GroupName,
			Resource: strings.ToLower(kind)}, key.Name)
	}

	target, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("fakeChildren.Get wants an *unstructured.Unstructured, got %T", obj)
	}

	held.DeepCopyInto(target)

	return nil
}

func (f *fakeChildren) Apply(_ context.Context, obj client.Object, opts ...client.PatchOption) error {
	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Empty() {
		return errors.New("fakeChildren.Apply was handed an object with no apiVersion or kind, " +
			"which a real apply patch is refused for")
	}

	forced := slices.ContainsFunc(opts, func(o client.PatchOption) bool {
		return o == client.ForceOwnership
	})

	if obj.GetName() == f.conflictOn && !forced && !f.conflicted[obj.GetName()] {
		f.conflicted[obj.GetName()] = true

		return apierrors.NewConflict(schema.GroupResource{Group: netboxv1alpha1.GroupName,
			Resource: strings.ToLower(gvk.Kind)}, obj.GetName(),
			errors.New(`Apply failed with 1 conflict: conflict with "kubectl-edit": .spec.name`))
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return fmt.Errorf("converting %T: %w", obj, err)
	}

	stored := &unstructured.Unstructured{Object: raw}
	f.store[gvk.Kind+"/"+obj.GetName()] = stored
	f.applied = append(f.applied, stored.DeepCopy())

	if forced {
		f.forced = append(f.forced, gvk.Kind+"/"+obj.GetName())
	}

	return nil
}

func (f *fakeChildren) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if f.listErr != nil {
		return f.listErr
	}

	items, ok := list.(*unstructured.UnstructuredList)
	if !ok {
		return fmt.Errorf("fakeChildren.List wants an *unstructured.UnstructuredList, got %T", list)
	}

	kind := strings.TrimSuffix(items.GetObjectKind().GroupVersionKind().Kind, "List")

	selector := &client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(selector)
	}

	for key, held := range f.store {
		if !strings.HasPrefix(key, kind+"/") || !matches(selector, held) {
			continue
		}

		items.Items = append(items.Items, *held.DeepCopy())
	}

	slices.SortFunc(items.Items, func(a, b unstructured.Unstructured) int {
		return strings.Compare(a.GetName(), b.GetName())
	})

	return nil
}

// matches applies the namespace and label selector the materialiser passed, because the
// selector being right is the assertion in the prune tests.
func matches(selector *client.ListOptions, obj *unstructured.Unstructured) bool {
	if selector.Namespace != "" && selector.Namespace != obj.GetNamespace() {
		return false
	}

	if selector.LabelSelector == nil {
		return false
	}

	return selector.LabelSelector.Matches(labels.Set(obj.GetLabels()))
}

func (f *fakeChildren) Delete(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
	key := obj.GetObjectKind().GroupVersionKind().Kind + "/" + obj.GetName()
	if _, ok := f.store[key]; !ok {
		return apierrors.NewNotFound(schema.GroupResource{Group: netboxv1alpha1.GroupName}, obj.GetName())
	}

	delete(f.store, key)
	f.deleted = append(f.deleted, key)

	return nil
}

// names is what was applied, as "Kind/name", in order.
func (f *fakeChildren) names() []string {
	out := make([]string, 0, len(f.applied))
	for _, obj := range f.applied {
		out = append(out, obj.GetKind()+"/"+obj.GetName())
	}

	return out
}

// find returns the last apply of one name, or nil.
func (f *fakeChildren) find(name string) *unstructured.Unstructured {
	for i := len(f.applied) - 1; i >= 0; i-- {
		if f.applied[i].GetName() == name {
			return f.applied[i]
		}
	}

	return nil
}

// inlinePass builds the one pass under test, with the parent's stored status as `before`.
func inlinePass(t *testing.T, parent *inlineKind, children *fakeChildren) *pass {
	t.Helper()

	engine := &Engine{
		Children: children,
		Events:   &fakeRecorder{},
		Scheme:   inlineScheme(t),
	}

	return &pass{
		engine: engine,
		obj:    parent,
		before: parent.Status.DeepCopy(),
		desc:   registry.Descriptor{GVK: inlineGVK, Endpoint: "virtualization/virtual-machines"},
	}
}

// inlineConditionOf is conditionOf for the parent kind.
func inlineConditionOf(parent *inlineKind, condType string) metav1.Condition {
	for _, condition := range parent.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// ourMarkers is the label set a materialised child carries, for planting one.
func ourMarkers(uid string) map[string]string {
	return map[string]string{
		netboxv1alpha1.ManagedByLabel: netboxv1alpha1.ManagedByValue,
		netboxv1alpha1.OwnerUIDLabel:  uid,
	}
}

// ourOwnerRef is the controller owner reference a materialised child carries.
func ourOwnerRef(uid string) metav1.OwnerReference {
	yes := true

	return metav1.OwnerReference{
		APIVersion: inlineGVK.GroupVersion().String(), Kind: inlineGVK.Kind,
		Name: "dns", UID: types.UID(uid), Controller: &yes, BlockOwnerDeletion: &yes,
	}
}
