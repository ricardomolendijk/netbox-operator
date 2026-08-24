package resolver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// rawID is ObjectRef.ID's pointer, which exists so `id: 0` is distinguishable from unset.
func rawID(id int64) *int64 { return &id }

// TestResolve covers every mode and every way each of them can fail to produce an id.
//
// One table rather than one test per mode, because the interesting assertion is the same
// throughout: which of the seven classifications came back, and what it says. A mode that
// resolved is checked by value, since a resolver that returns the right id with the wrong
// object type is a bug NBO-019's generic FKs would inherit.
func TestResolve(t *testing.T) {
	tests := []struct {
		name string

		field       registry.Field
		ref         netboxv1alpha1.ObjectRef
		referrer    types.NamespacedName
		referrerGVK schema.GroupVersionKind

		objects []target
		readErr error
		netBox  *fakeNetBox

		want Result

		// wantCause is the sentinel the reference is expected to be blocked by. Zero means
		// it is expected to resolve.
		wantCause error

		// wantFailure marks the cases that are not about the reference at all: NetBox being
		// unavailable, or the API server refusing. Those come back as ordinary errors, so the
		// engine backs off rather than reporting the object as merely waiting.
		wantFailure bool

		// wantMessage is a substring the rendered error must contain, because the message is
		// the whole product for a state a human has to act on.
		wantMessage string

		wantCalls []call
	}{
		{
			name:     "name mode takes the target's status.id",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			objects:  []target{readyTarget()},
			want: Result{
				ID: 12, ObjectType: "dcim.region", Mode: ModeName,
				Target: types.NamespacedName{Namespace: "team-a", Name: "emea"},
			},
		},
		{
			// The normal shape for a shared catalogue rather than an edge case: every Kind is
			// namespaced, so a team namespace pointing at a catalogue namespace is what a
			// reference usually looks like (docs/decisions/0002-crd-scoping.md).
			name:     "name mode reads the namespace the reference names",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Name: "emea", Namespace: "catalogue"},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			objects: []target{{
				gvk: regionGVK, namespace: "catalogue", name: "emea", id: 31,
				ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
			}},
			want: Result{
				ID: 31, ObjectType: "dcim.region", Mode: ModeName,
				Target: types.NamespacedName{Namespace: "catalogue", Name: "emea"},
			},
		},
		{
			name:        "name mode with no such CR is not found",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			wantCause:   ErrRefNotFound,
			wantMessage: "regionRef -> netboxregion/team-a/emea: not found",
		},
		{
			// The first-apply case, and the one that has to wait rather than fail: the target
			// is there and has simply not been written to NetBox yet.
			name:        "name mode with a target that has no id yet is not ready",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			objects:     []target{{gvk: regionGVK, namespace: "team-a", name: "emea"}},
			wantCause:   ErrRefNotReady,
			wantMessage: "the target has no status.id yet",
		},
		{
			// A target that is failing is still just a wait for the referrer -- but the
			// message has to quote the target's own reason, or a human debugs the referrer
			// for an hour before noticing the target is the broken one.
			name:     "name mode quotes a failing target's own reason",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			objects: []target{{
				gvk: regionGVK, namespace: "team-a", name: "emea", id: 12,
				ready: metav1.ConditionFalse, reason: netboxv1alpha1.ReasonInvalid,
				message: "slug must be unique",
			}},
			wantCause:   ErrRefNotReady,
			wantMessage: `target Ready=False, Reason=Invalid: "slug must be unique"`,
		},
		{
			// The target is on its way out. Its id is still valid, and pointing a live FK at
			// an object that is being deleted is not something to do quietly -- the reference
			// stops being written and the object says so.
			name:     "name mode with a terminating target is not ready",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			objects: []target{{
				gvk: regionGVK, namespace: "team-a", name: "emea", id: 12,
				ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced, terminating: true,
			}},
			wantCause:   ErrRefNotReady,
			wantMessage: "the target is being deleted",
		},
		{
			// The one cycle a single resolution can see. Reporting it as "not ready" would
			// leave the object waiting for itself forever; longer cycles are NBO-016.
			name:        "a reference to the referring object is a cycle",
			field:       registry.Field{Spec: "parentRef", API: "parent", Ref: true, Target: regionGVK},
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "emea"},
			referrerGVK: regionGVK,
			objects:     []target{readyTarget()},
			wantCause:   ErrRefCycle,
			wantMessage: "the reference points at the referring object itself",
		},
		{
			// The manifest is correct and the fix is installing a CRD, so this must not read
			// as "not found": that sends whoever reads it looking for a CR they were right
			// not to have written.
			name:        "name mode against an uninstalled kind is unavailable",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			readErr:     noKindMatch(regionGVK),
			wantCause:   ErrRefKindUnavailable,
			wantMessage: "NetBoxRegion is not installed in this cluster",
		},
		{
			name:        "a target kind with no descriptor is unavailable",
			field:       tenantField(),
			ref:         netboxv1alpha1.ObjectRef{Name: "acme"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			wantCause:   ErrRefKindUnavailable,
			wantMessage: "no descriptor is registered for",
		},
		{
			name:        "a reference with no target kind is unavailable",
			field:       registry.Field{Spec: "regionRef", API: "region", Ref: true},
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			wantCause:   ErrRefKindUnavailable,
			wantMessage: "the descriptor declares no target kind for regionRef",
		},
		{
			name:        "a cluster read that failed for its own reasons is a failure",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Name: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			readErr:     errAPIServer,
			wantFailure: true,
		},
		{
			name:      "slug mode resolves against netbox",
			field:     regionField(),
			ref:       netboxv1alpha1.ObjectRef{Slug: "emea"},
			referrer:  types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:    &fakeNetBox{list: []netbox.Object{{"id": float64(12)}}},
			want:      Result{ID: 12, ObjectType: "dcim.region", Mode: ModeSlug},
			wantCalls: []call{{method: "GETONE", endpoint: "dcim/regions", params: netbox.Params{"slug": "emea"}}},
		},
		{
			// `slug` is only globally unique on some models -- ipam.VLANGroup is unique on
			// (scope_type, scope_id, slug) (docs/netbox-schema.md) -- so several matches is a
			// real answer, and naming both ids is the only useful thing to say about it.
			name:     "slug mode with two matches is ambiguous and names both",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Slug: "emea"},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox: &fakeNetBox{list: []netbox.Object{
				{"id": float64(12), "display": "EMEA"},
				{"id": float64(19), "display": "Emea"},
			}},
			wantCause:   ErrRefAmbiguous,
			wantMessage: "2 netbox dcim/regions match map[slug:emea]: id 12 (EMEA), id 19 (Emea)",
			wantCalls:   []call{{method: "GETONE", endpoint: "dcim/regions", params: netbox.Params{"slug": "emea"}}},
		},
		{
			name:        "slug mode with no match is not found",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Slug: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:      &fakeNetBox{},
			wantCause:   ErrRefNotFound,
			wantMessage: "no netbox dcim/regions matches",
			wantCalls:   []call{{method: "GETONE", endpoint: "dcim/regions", params: netbox.Params{"slug": "emea"}}},
		},
		{
			// A VLAN's identity is a pair, which is the whole reason `lookup` exists. The
			// filter reaches the client as the map the user wrote; the client renders it in
			// sorted key order, so the request is `?site=home&vid=20` either way.
			name:     "lookup mode sends the filter the user wrote",
			field:    regionField(),
			ref:      netboxv1alpha1.ObjectRef{Lookup: map[string]string{"vid": "20", "site": "home"}},
			referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:   &fakeNetBox{list: []netbox.Object{{"id": float64(7)}}},
			want:     Result{ID: 7, ObjectType: "dcim.region", Mode: ModeLookup},
			wantCalls: []call{{
				method: "GETONE", endpoint: "dcim/regions",
				params: netbox.Params{"site": "home", "vid": "20"},
			}},
		},
		{
			name:        "netbox being unavailable is a failure rather than a blocker",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{Slug: "emea"},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:      &fakeNetBox{listErr: &netbox.TransientError{Status: 503}},
			wantFailure: true,
			wantCalls:   []call{{method: "GETONE", endpoint: "dcim/regions", params: netbox.Params{"slug": "emea"}}},
		},
		{
			name:      "id mode verifies the object exists",
			field:     regionField(),
			ref:       netboxv1alpha1.ObjectRef{ID: rawID(12)},
			referrer:  types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:    &fakeNetBox{get: netbox.Object{"id": float64(12)}},
			want:      Result{ID: 12, ObjectType: "dcim.region", Mode: ModeID},
			wantCalls: []call{{method: "GET", endpoint: "dcim/regions", id: 12}},
		},
		{
			// The escape hatch is the one place a user can be wrong in a way NetBox cannot
			// reject, so it is verified rather than trusted.
			name:        "id mode against a deleted object is not found",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{ID: rawID(12)},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:      &fakeNetBox{getErr: &netbox.NotFoundError{Endpoint: "dcim/regions", ID: 12}},
			wantCause:   ErrRefNotFound,
			wantMessage: "netbox dcim/regions/12 does not exist",
			wantCalls:   []call{{method: "GET", endpoint: "dcim/regions", id: 12}},
		},
		{
			// A 200 with nothing in the body. Treating it as found would write an id nothing
			// answers for, which is exactly what verifying is supposed to prevent.
			name:        "id mode with an empty response is not found",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{ID: rawID(12)},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			netBox:      &fakeNetBox{},
			wantCause:   ErrRefNotFound,
			wantMessage: "netbox dcim/regions/12 does not exist",
			wantCalls:   []call{{method: "GET", endpoint: "dcim/regions", id: 12}},
		},
		{
			// Unreachable through the API server, where CEL requires exactly one mode. It
			// must not resolve to nothing and have the field quietly dropped.
			name:        "a reference with no mode set is malformed",
			field:       regionField(),
			ref:         netboxv1alpha1.ObjectRef{},
			referrer:    types.NamespacedName{Namespace: "team-a", Name: "ams"},
			wantCause:   ErrRefMalformed,
			wantMessage: "no mode set",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{objects: tc.objects, err: tc.readErr}
			nb := tc.netBox
			if nb == nil {
				nb = &fakeNetBox{}
			}

			resolver := &Resolver{Objects: reader, Kinds: kinds()}

			got, err := resolver.Resolve(context.Background(), Request{
				NetBox: nb, Referrer: tc.referrer, ReferrerGVK: tc.referrerGVK,
				Field: tc.field, Ref: tc.ref,
			})

			assertCalls(t, nb, tc.wantCalls)

			switch {
			case tc.wantFailure:
				assertFailure(t, err)
			case tc.wantCause != nil:
				assertBlocked(t, err, tc.wantCause, tc.field.Spec, tc.field.Target, tc.wantMessage)
			default:
				if err != nil {
					t.Fatalf("Resolve() = %v, want it to resolve", err)
				}

				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("Resolve() = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

// assertBlocked checks that err is a typed resolution failure of the expected cause, and that
// errors.As recovers the field and target it came from -- the reason the error is a type and
// not a string.
func assertBlocked(
	t *testing.T, err error, cause error, field string, gvk schema.GroupVersionKind, message string,
) {
	t.Helper()

	if !errors.Is(err, cause) {
		t.Fatalf("Resolve() = %v, want %v", err, cause)
	}

	var refErr *Error
	if !errors.As(err, &refErr) {
		t.Fatalf("Resolve() = %v, want an *Error errors.As can recover", err)
	}

	if refErr.Field != field {
		t.Errorf("Error.Field = %q, want %q", refErr.Field, field)
	}

	if refErr.TargetGVK != gvk {
		t.Errorf("Error.TargetGVK = %s, want %s", refErr.TargetGVK, gvk)
	}

	if message != "" && !strings.Contains(err.Error(), message) {
		t.Errorf("Resolve() = %q, want it to contain %q", err.Error(), message)
	}
}

// assertFailure checks that err is not a resolution failure: the caller has to be able to tell
// "this reference is waiting" from "NetBox is down", because only one of them is a state.
func assertFailure(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("Resolve() = nil, want a failure")
	}

	var refErr *Error
	if errors.As(err, &refErr) {
		t.Errorf("Resolve() = %v, want a plain failure rather than a blocked reference", err)
	}
}

// assertCalls checks the NetBox requests made, in order. It is also how "zero NetBox calls"
// is asserted for the modes that must not make any.
func assertCalls(t *testing.T, nb *fakeNetBox, want []call) {
	t.Helper()

	if len(nb.calls) == 0 && len(want) == 0 {
		return
	}

	if !reflect.DeepEqual(nb.calls, want) {
		t.Errorf("netbox calls = %+v, want %+v", nb.calls, want)
	}
}

// TestResolveAll is the acceptance case: a referrer with one reference that resolves and one
// that cannot, which must report the second without losing the first and without returning an
// error -- a returned error would be controller-runtime backoff on a normal wait.
func TestResolveAll(t *testing.T) {
	obj := referrer("ams", map[string]any{
		"name":      "Amsterdam",
		"regionRef": map[string]any{"name": "emea"},
		"tenantRef": map[string]any{"name": "acme"},
	})

	reader := &fakeReader{objects: []target{readyTarget()}}
	resolver := &Resolver{Objects: reader, Kinds: kinds()}

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v, want no error: an unresolved reference is a state", err)
	}

	want := Result{
		ID: 12, ObjectType: "dcim.region", Mode: ModeName,
		Target: types.NamespacedName{Namespace: "team-a", Name: "emea"},
	}
	if got := resolution.ByField["regionRef"]; !reflect.DeepEqual(got, want) {
		t.Errorf("ByField[regionRef] = %+v, want %+v", got, want)
	}

	if len(resolution.Blocked) != 1 || resolution.Blocked[0].Field != "tenantRef" {
		t.Fatalf("Blocked = %+v, want one blocker for tenantRef", resolution.Blocked)
	}

	if got := resolution.Blocked[0].Reason; got != netboxv1alpha1.ReasonRefKindUnavailable {
		t.Errorf("Blocked[0].Reason = %q, want %q", got, netboxv1alpha1.ReasonRefKindUnavailable)
	}

	if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefKindUnavailable {
		t.Errorf("Reason() = %q, want %q", got, netboxv1alpha1.ReasonRefKindUnavailable)
	}

	// One read for the one reference that had a Kind to read. A reference to an unavailable
	// Kind costs nothing at all, which is what keeps a kind declaring five references it
	// cannot resolve yet from costing five requests per pass.
	if reader.reads != 1 {
		t.Errorf("cluster reads = %d, want 1", reader.reads)
	}
}

// TestResolveAllSkipsWhatTheSpecDoesNotSet is the other half of the contract: spec omission
// means "do not manage" (plan.md 2.6), so an unset reference is not a blocker and not a
// resolution -- it simply is not there.
func TestResolveAllSkipsWhatTheSpecDoesNotSet(t *testing.T) {
	tests := map[string]map[string]any{
		"no reference fields at all": {"name": "Amsterdam"},
		"an explicit null":           {"name": "Amsterdam", "regionRef": nil},

		// A to-many reference. Neither ObjectRef nor Field carries a cardinality, so this is
		// left alone rather than decoded as one reference: the caller reports it as declared
		// and not resolved, which keeps the object off Ready instead of writing one id where
		// a list belongs.
		"a list of references": {"regionRef": []any{
			map[string]any{"name": "emea"}, map[string]any{"name": "apac"},
		}},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

			resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{},
				referrer("ams", spec), siteDescriptor())
			if err != nil {
				t.Fatalf("ResolveAll() = %v", err)
			}

			if len(resolution.ByField) != 0 || len(resolution.Blocked) != 0 {
				t.Errorf("resolution = %+v, want nothing resolved and nothing blocked", resolution)
			}
		})
	}
}

// TestPresentButEmptyRefIsNotAbsent keeps the two apart. An absent reference is nobody's
// request and is not reported; a present `{}` names no object and is refused. Server-side
// apply makes the distinction readable at the API level too (#121), so nothing here may
// depend on the two being one state.
func TestPresentButEmptyRefIsNotAbsent(t *testing.T) {
	resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

	obj := referrer("ams", map[string]any{"regionRef": map[string]any{}})

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	if len(resolution.Blocked) != 1 || !errors.Is(resolution.Blocked[0].Err, ErrRefMalformed) {
		t.Fatalf("Blocked = %+v, want one malformed reference", resolution.Blocked)
	}

	if got := resolution.Blocked[0].Reason; got != netboxv1alpha1.ReasonInvalid {
		t.Errorf("Blocked[0].Reason = %q, want %q", got, netboxv1alpha1.ReasonInvalid)
	}
}

// TestResolveAllReportsInDescriptorOrder pins the order the blockers come back in. The order
// decides which reason a condition carries and how its message reads, and a map-ordered list
// makes both unreviewable.
func TestResolveAllReportsInDescriptorOrder(t *testing.T) {
	d := siteDescriptor()
	d.Fields = append(d.Fields,
		registry.Field{Spec: "groupRef", API: "group", Ref: true, Target: siteGVK})

	obj := referrer("ams", map[string]any{
		"groupRef":  map[string]any{"name": "west"},
		"regionRef": map[string]any{"name": "emea"},
		"tenantRef": map[string]any{"name": "acme"},
	})

	resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, d)
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	fields := make([]string, 0, len(resolution.Blocked))
	for _, blocker := range resolution.Blocked {
		fields = append(fields, blocker.Field)
	}

	if want := []string{"regionRef", "tenantRef", "groupRef"}; !reflect.DeepEqual(fields, want) {
		t.Errorf("blocked fields = %v, want %v", fields, want)
	}
}

// TestResolveAllFailsOnACorruptSpec is the one case that is a returned error: a spec that will
// not decode is a programming or storage fault, and pretending the reference is merely
// unresolved would leave it silently dropped.
func TestResolveAllFailsOnACorruptSpec(t *testing.T) {
	obj := referrer("ams", map[string]any{"regionRef": "emea"})

	resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

	if _, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor()); err == nil {
		t.Fatal("ResolveAll() = nil, want an error for a reference that is not an object")
	}
}

// TestResolveAllUsesTheRegistryByDefault is what makes the production wiring one line: a
// resolver with no descriptor source reads the package-level registry every kind's init()
// filled, so NetBoxRegion's own parentRef resolves with nothing configured.
func TestResolveAllUsesTheRegistryByDefault(t *testing.T) {
	regionDescriptor, ok := registry.Get(regionGVK)
	if !ok {
		t.Fatal("NetBoxRegion is not registered")
	}

	obj := referrer("child", map[string]any{"parentRef": map[string]any{"name": "emea"}})
	obj.SetGroupVersionKind(regionGVK)

	reader := &fakeReader{objects: []target{readyTarget()}}
	resolver := &Resolver{Objects: reader}

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, regionDescriptor)
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	if got := resolution.ByField["parentRef"].ID; got != 12 {
		t.Errorf("ByField[parentRef].ID = %d, want 12", got)
	}

	if got := resolution.ByField["parentRef"].ObjectType; got != "dcim.region" {
		t.Errorf("ByField[parentRef].ObjectType = %q, want dcim.region", got)
	}
}
