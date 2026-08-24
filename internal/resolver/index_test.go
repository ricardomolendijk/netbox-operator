package resolver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestIndexValue pins the encoding, because it is a contract between two halves of the
// system that never meet: the index function that writes a key on every object write, and
// the map function that queries one on every target event. A change to either spelling that
// is not a change to both is a watch that silently matches nothing.
func TestIndexValue(t *testing.T) {
	tests := []struct {
		name      string
		gvk       schema.GroupVersionKind
		namespace string
		objName   string
		want      string
	}{
		{
			name: "kind, namespace and name", gvk: regionGVK,
			namespace: "netbox-catalog", objName: "emea",
			want: "netboxregion/netbox-catalog/emea",
		},
		{
			// The same spelling every reference message uses, so a key found in a log line
			// is greppable against the condition a human was reading.
			name: "the kind is lowercased", gvk: siteGVK,
			namespace: "team-a", objName: "ams",
			want: "netboxsite/team-a/ams",
		},
		{
			// Two Kinds may hold objects of one name, so the Kind is part of the key rather
			// than an assumption about it.
			name: "the same name under two kinds is two keys", gvk: tenantGVK,
			namespace: "team-a", objName: "ams",
			want: "netboxtenant/team-a/ams",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IndexValue(tc.gvk, tc.namespace, tc.objName); got != tc.want {
				t.Errorf("IndexValue = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRefIndexerKeys is the index itself: which references produce a key, and which
// deliberately produce none.
func TestRefIndexerKeys(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]any
		want []string
	}{
		{
			name: "a name reference into another namespace",
			spec: map[string]any{"regionRef": map[string]any{
				"name": "emea", "namespace": "netbox-catalog",
			}},
			want: []string{"netboxregion/netbox-catalog/emea"},
		},
		{
			// The terse form, and the one that has to default correctly: an omitted
			// namespace means the referrer's own, exactly as the resolver reads it.
			name: "an omitted namespace defaults to the referrer's",
			spec: map[string]any{"regionRef": map[string]any{"name": "emea"}},
			want: []string{"netboxregion/team-a/emea"},
		},
		{
			// The bound on the whole index. These three resolve against NetBox, where there
			// is no Kubernetes object an event could ever arrive for.
			name: "a slug is not indexed",
			spec: map[string]any{"regionRef": map[string]any{"slug": "emea"}},
			want: []string{},
		},
		{
			name: "a lookup is not indexed",
			spec: map[string]any{"regionRef": map[string]any{
				"lookup": map[string]any{"vid": "20"},
			}},
			want: []string{},
		},
		{
			name: "an id is not indexed",
			spec: map[string]any{"regionRef": map[string]any{"id": int64(12)}},
			want: []string{},
		},
		{
			name: "an object with no references at all",
			spec: map[string]any{"name": "Amsterdam"},
			want: []string{},
		},
		{
			// A Kind with no Descriptor and no CRD yet is still indexed: the reference is
			// declarable before its target is implemented, and the day the CRD lands the
			// index has to already hold the edge -- an object is not rewritten just because
			// somebody upgraded the operator.
			name: "a reference to a kind with no descriptor is indexed by its target kind",
			spec: map[string]any{"tenantRef": map[string]any{"name": "acme"}},
			want: []string{"netboxtenant/team-a/acme"},
		},
		{
			name: "two references produce two keys",
			spec: map[string]any{
				"regionRef": map[string]any{"name": "emea"},
				"tenantRef": map[string]any{"name": "acme"},
			},
			want: []string{"netboxregion/team-a/emea", "netboxtenant/team-a/acme"},
		},
	}

	indexer := refIndexer(siteDescriptor())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := indexer(referrer("ams", tc.spec))
			if !sameSet(got, tc.want) {
				t.Errorf("keys = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefIndexerDeduplicatesOneTarget is the "two ref fields, one target, one request" case
// at its source. Deduplicating in the index rather than in the map function is what keeps the
// workqueue depth and the enqueue metric honest, since a list matched twice is two requests
// before the queue collapses them.
func TestRefIndexerDeduplicatesOneTarget(t *testing.T) {
	d := registry.Descriptor{
		GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site",
		Fields: []registry.Field{
			{Spec: "regionRef", API: "region", Ref: true, Target: regionGVK},
			{Spec: "fallbackRegionRef", API: "fallback_region", Ref: true, Target: regionGVK},
		},
	}

	got := refIndexer(d)(referrer("ams", map[string]any{
		"regionRef":         map[string]any{"name": "emea"},
		"fallbackRegionRef": map[string]any{"name": "emea"},
	}))

	if want := []string{"netboxregion/team-a/emea"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v: two references to one target are one edge", got, want)
	}
}

// TestRefNamespaceIndexerKeys covers the grant watch's half of the index: which namespaces a
// referrer reaches into, and why its own is not one of them.
func TestRefNamespaceIndexerKeys(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]any
		want []string
	}{
		{
			name: "a reference into another namespace",
			spec: map[string]any{"regionRef": map[string]any{
				"name": "emea", "namespace": "netbox-catalog",
			}},
			want: []string{"netbox-catalog"},
		},
		{
			// A reference that stays put is never authorised against anything, so a grant in
			// this namespace cannot unblock it. Indexing it would make every grant wake every
			// object in its own namespace that holds any reference at all.
			name: "a reference that stays in the referrer's namespace is not indexed",
			spec: map[string]any{"regionRef": map[string]any{"name": "emea"}},
			want: []string{},
		},
		{
			name: "the referrer's namespace written out explicitly is still not indexed",
			spec: map[string]any{"regionRef": map[string]any{
				"name": "emea", "namespace": "team-a",
			}},
			want: []string{},
		},
		{
			name: "two references into one namespace are one key",
			spec: map[string]any{
				"regionRef": map[string]any{"name": "emea", "namespace": "netbox-catalog"},
				"tenantRef": map[string]any{"name": "acme", "namespace": "netbox-catalog"},
			},
			want: []string{"netbox-catalog"},
		},
		{
			name: "a slug reaches no namespace",
			spec: map[string]any{"regionRef": map[string]any{"slug": "emea"}},
			want: []string{},
		},
	}

	indexer := refNamespaceIndexer(siteDescriptor())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := indexer(referrer("ams", tc.spec))
			if !sameSet(got, tc.want) {
				t.Errorf("namespaces = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAddIndexes covers the registration pass: what it registers, what it deliberately does
// not, and that a failure is a failure rather than a silently missing index.
func TestAddIndexes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}

	regionDescriptor := registry.Descriptor{
		GVK: regionGVK, Endpoint: "dcim/regions", ObjectType: "dcim.region",
		Fields: []registry.Field{{Spec: "parentRef", API: "parent", Ref: true, Target: regionGVK}},
	}
	// dcim.Site declares no reference in this milestone, and a kind with none must not be
	// given an index: the index function encodes every object of its type to JSON on every
	// write, which is real work per write for a map that can never hold a key.
	siteNoRefs := registry.Descriptor{GVK: siteGVK, Endpoint: "dcim/sites", ObjectType: "dcim.site"}

	t.Run("both indexes are registered for a referring kind", func(t *testing.T) {
		indexer := &fakeFieldIndexer{}

		if err := AddIndexes(context.Background(), indexer, scheme,
			[]registry.Descriptor{regionDescriptor, siteNoRefs}); err != nil {
			t.Fatalf("AddIndexes: %v", err)
		}

		want := []string{
			"*v1alpha1.NetBoxRegion/" + RefIndex,
			"*v1alpha1.NetBoxRegion/" + RefNamespaceIndex,
		}
		if !reflect.DeepEqual(indexer.registered, want) {
			t.Errorf("registered %v, want %v", indexer.registered, want)
		}
	})

	t.Run("a duplicate registration fails", func(t *testing.T) {
		indexer := &fakeFieldIndexer{}

		err := AddIndexes(context.Background(), indexer, scheme,
			[]registry.Descriptor{regionDescriptor, regionDescriptor})
		if err == nil {
			t.Fatal("AddIndexes accepted the same (type, field) twice; it has to fail the boot")
		}
		if !errors.Is(err, errAlreadyIndexed) {
			t.Errorf("error = %v, want it to carry the indexer's own failure", err)
		}
		if !strings.Contains(err.Error(), RefIndex) {
			t.Errorf("error = %q, does not name the field that collided", err)
		}
	})

	t.Run("a kind with no go type fails", func(t *testing.T) {
		// The one case that must not be skipped: a descriptor is registered by this
		// operator's own code, so a Kind with no type in the scheme is a build that cannot
		// reconcile that kind at all -- and an index registered against nothing would leave
		// its watches quietly matching nothing.
		unknown := registry.Descriptor{
			GVK:    netboxv1alpha1.GroupVersion.WithKind("NetBoxNotAKind"),
			Fields: []registry.Field{{Spec: "regionRef", API: "region", Ref: true, Target: regionGVK}},
		}

		if err := AddIndexes(context.Background(), &fakeFieldIndexer{}, scheme,
			[]registry.Descriptor{unknown}); err == nil {
			t.Fatal("AddIndexes accepted a descriptor whose Kind is not in the scheme")
		}
	})
}

// TestRefTargets covers the watch's half of the descriptor read: one watch per distinct
// target Kind, and none for a reference that terminates in NetBox.
func TestRefTargets(t *testing.T) {
	tests := []struct {
		name   string
		fields []registry.Field
		want   []schema.GroupVersionKind
	}{
		{
			name: "one target per reference",
			fields: []registry.Field{
				{Spec: "regionRef", API: "region", Ref: true, Target: regionGVK},
				{Spec: "tenantRef", API: "tenant", Ref: true, Target: tenantGVK},
			},
			want: []schema.GroupVersionKind{regionGVK, tenantGVK},
		},
		{
			// Two references into one Kind need one watch: the map function behind it finds
			// referrers through the index, which does not care which field produced the key.
			name: "two references to one kind are one target",
			fields: []registry.Field{
				{Spec: "regionRef", API: "region", Ref: true, Target: regionGVK},
				{Spec: "fallbackRegionRef", API: "fallback_region", Ref: true, Target: regionGVK},
			},
			want: []schema.GroupVersionKind{regionGVK},
		},
		{
			name:   "a scalar field is not a target",
			fields: []registry.Field{{Spec: "name", API: "name"}},
			want:   []schema.GroupVersionKind{},
		},
		{
			name:   "a reference with no target kind is not watchable",
			fields: []registry.Field{{Spec: "regionRef", API: "region", Ref: true}},
			want:   []schema.GroupVersionKind{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RefTargets(registry.Descriptor{GVK: siteGVK, Fields: tc.fields})
			if len(got) != len(tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("target %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNameRefTargetsSurvivesAnUnencodableObject is the index function's failure mode. There
// is nowhere to report an error from an indexer, so the only acceptable behaviour is to
// index nothing and leave the object's own reconcile -- which reads the same spec through the
// same code -- to report it.
func TestNameRefTargetsSurvivesAnUnencodableObject(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "team-a", "name": "ams"},
		// A channel cannot be marshalled, which is what refsOf fails on.
		"spec": map[string]any{"regionRef": make(chan int)},
	}}
	broken.SetGroupVersionKind(siteGVK)

	if got := refIndexer(siteDescriptor())(broken); len(got) != 0 {
		t.Errorf("keys = %v, want none from an object that will not encode", got)
	}
}

// errAlreadyIndexed is what controller-runtime returns for a second registration of one
// (type, field) pair, which is the failure AddIndexes has to surface as a boot failure.
var errAlreadyIndexed = errors.New("indexer conflict")

// fakeFieldIndexer records what was registered and refuses a duplicate, which is the one
// behaviour of the real indexer this package depends on.
type fakeFieldIndexer struct {
	registered []string
}

func (f *fakeFieldIndexer) IndexField(
	_ context.Context, obj client.Object, field string, _ client.IndexerFunc,
) error {
	key := fmt.Sprintf("%T/%s", obj, field)
	for _, seen := range f.registered {
		if seen == key {
			return fmt.Errorf("%w: %s", errAlreadyIndexed, key)
		}
	}

	f.registered = append(f.registered, key)

	return nil
}

// sameSet compares two key lists ignoring order, since a spec is a map and the order two
// references come out in is not something the index promises.
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for _, value := range want {
		found := false
		for _, candidate := range got {
			if candidate == value {
				found = true

				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
