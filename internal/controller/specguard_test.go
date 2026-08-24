package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// recordingClient is a client.Client that reaches no API server and remembers whether the
// call got past the guard. Everything the guard does not override panics if called, which
// is the assertion that the guard is not quietly delegating something it should refuse.
type recordingClient struct {
	client.Client

	scheme  *runtime.Scheme
	updates int
	patches int
}

func (c *recordingClient) Scheme() *runtime.Scheme { return c.scheme }

func (c *recordingClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	c.updates++

	return nil
}

func (c *recordingClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	c.patches++

	return nil
}

// finalizerPatch is the patch objectcontroller.go's finalizerWriter builds, reproduced here
// rather than shared: the guard has to keep working against the patch as it is written, and
// a helper both sides call would let the two drift apart without a failure.
func finalizerPatch(t *testing.T, obj client.Object) client.Patch {
	t.Helper()

	body, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"finalizers":      []string{netboxv1alpha1.Finalizer},
		"resourceVersion": obj.GetResourceVersion(),
	}})
	if err != nil {
		t.Fatalf("encoding the finalizer patch: %v", err)
	}

	return client.RawPatch(types.MergePatchType, body)
}

// specPatch is a merge patch that reaches the spec, which is what the guard exists to
// refuse -- and the shape a contributor reaching for "just patch the one field" would
// produce.
func specPatch(t *testing.T) client.Patch {
	t.Helper()

	body, err := json.Marshal(map[string]any{"spec": map[string]any{"color": "ff0000"}})
	if err != nil {
		t.Fatalf("encoding the spec patch: %v", err)
	}

	return client.RawPatch(types.MergePatchType, body)
}

// TestSpecGuardRefusesSpecWrites is the runtime half of the never-write-spec invariant:
// belt and braces against a future contributor, not a substitute for review
// (NBO-065, docs/decisions/0005-gitops-coexistence.md §1).
func TestSpecGuardRefusesSpecWrites(t *testing.T) {
	tests := []struct {
		name  string
		obj   client.Object
		write func(t *testing.T, guard specGuard, obj client.Object) error

		wantRefused bool
	}{
		{
			name: "an Update on a registered kind is refused",
			obj:  &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "managed"}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
			wantRefused: true,
		},
		{
			name: "a patch that reaches the spec is refused",
			obj:  &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "managed"}},
			write: func(t *testing.T, guard specGuard, obj client.Object) error {
				return guard.Patch(context.Background(), obj, specPatch(t))
			},
			wantRefused: true,
		},
		{
			// The finalizer is the one metadata write the engine makes, and it has to keep
			// working: a guard that blocked it would leave every NetBox object orphaned on
			// its CR's deletion.
			name: "the finalizer patch is allowed",
			obj:  &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "managed"}},
			write: func(t *testing.T, guard specGuard, obj client.Object) error {
				return guard.Patch(context.Background(), obj, finalizerPatch(t, obj))
			},
		},
		{
			// A CR the operator materialised is the operator's own output rather than Git's
			// input, so nothing reverts it and there is no fight to prevent (NBO-032,
			// NBO-036). Keyed on the *controller* owner reference, which only the operator's
			// own materialisation sets (ADR-0003 rule 3).
			name: "an Update on a CR the operator generated is allowed",
			obj: &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{
				Namespace: "team-a", Name: "generated",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: netboxv1alpha1.GroupVersion.String(),
					Kind:       "NetBoxVirtualMachine",
					Name:       "vm",
					Controller: ptr.To(true),
				}},
			}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
		},
		{
			// The materialiser's own write, in the shape it actually makes it: an apply patch
			// carrying the whole object. It is admitted for the same reason the Update above
			// is -- the controller owner reference says the operator created this -- and it
			// is a separate row because an apply patch is the one write in the operator whose
			// body always contains a spec, so a guard that refused it would break NBO-032
			// outright.
			name: "an apply patch of a materialised child is allowed",
			obj: &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{
				Namespace: "team-a", Name: "vm-eth0",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: netboxv1alpha1.GroupVersion.String(),
					Kind:       "NetBoxVirtualMachine",
					Name:       "vm",
					Controller: ptr.To(true),
				}},
			}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Patch(context.Background(), obj, client.Apply,
					client.FieldOwner(reconciler.ChildFieldManager))
			},
		},
		{
			// The other half, and the backstop children.go's non-hijacking check is the front
			// of: if that GET-before-write were ever removed, the materialiser applying over a
			// hand-written CR would be refused here rather than silently taking it over.
			name: "an apply patch of a CR the operator did not create is refused",
			obj: &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{
				Namespace: "team-a", Name: "handwritten",
			}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Patch(context.Background(), obj, client.Apply,
					client.FieldOwner(reconciler.ChildFieldManager))
			},
			wantRefused: true,
		},
		{
			// The row that keeps ADR-0003 rule 4 from disabling this guard across a cluster.
			// A containment owner reference is in this API group and is *not* the controller:
			// it goes on an ordinary hand-written CR whose parent happens to be in the same
			// namespace, and that CR's spec is still Git's. Before the controller check, every
			// prefix with a same-namespace siteRef would have looked operator-generated.
			name: "a non-controller owner reference in our own group is not our doing",
			obj: &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{
				Namespace: "team-a", Name: "contained",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: netboxv1alpha1.GroupVersion.String(),
					Kind:       "NetBoxSite",
					Name:       "home",
				}},
			}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
			wantRefused: true,
		},
		{
			// An owner reference to something outside this API group is somebody else's
			// ownership, not evidence that the operator created the object -- even when it is
			// that object's controller.
			name: "an owner reference from another group is not our doing",
			obj: &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{
				Namespace: "team-a", Name: "adopted",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1", Kind: "Deployment", Name: "something",
					Controller: ptr.To(true),
				}},
			}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
			wantRefused: true,
		},
		{
			// The guard is scoped to the descriptor registry: NetBoxEndpoint is not in it,
			// because it describes a connection rather than a NetBox object, and nothing
			// writes its spec either.
			name: "a kind that is not in the descriptor registry passes through",
			obj:  &netboxv1alpha1.NetBoxEndpoint{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "homelab"}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
		},
		{
			// Guarding every type would break every other controller sharing this client.
			name: "an unrelated type passes through",
			obj:  &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "nb-token"}},
			write: func(_ *testing.T, guard specGuard, obj client.Object) error {
				return guard.Update(context.Background(), obj)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingClient{scheme: scheme}
			guard := specGuard{recorder}

			err := test.write(t, guard, test.obj)

			if test.wantRefused {
				if !errors.Is(err, ErrSpecWriteForbidden) {
					t.Fatalf("write = %v, want ErrSpecWriteForbidden", err)
				}

				if recorder.updates+recorder.patches != 0 {
					t.Errorf("the refused write still reached the API server")
				}

				// The message has to name the object, or a rejection in a log is
				// unattributable to any of the CRs in the cluster.
				if !strings.Contains(err.Error(), test.obj.GetName()) ||
					!strings.Contains(err.Error(), test.obj.GetNamespace()) {
					t.Errorf("error = %q, want it to name %s/%s",
						err, test.obj.GetNamespace(), test.obj.GetName())
				}

				return
			}

			if err != nil {
				t.Fatalf("write = %v, want it allowed", err)
			}

			if recorder.updates+recorder.patches != 1 {
				t.Errorf("the allowed write did not reach the API server")
			}
		})
	}
}

// TestSpecGuardRefusesAnUnreadablePatch covers the shape nobody has thought about: the
// operator sends exactly one kind of patch, so a body this cannot inspect is refused rather
// than guessed at.
func TestSpecGuardRefusesAnUnreadablePatch(t *testing.T) {
	recorder := &recordingClient{scheme: scheme}
	guard := specGuard{recorder}
	tag := &netboxv1alpha1.NetBoxTag{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "managed"}}

	jsonPatch := client.RawPatch(types.JSONPatchType,
		[]byte(`[{"op":"replace","path":"/spec/color","value":"ff0000"}]`))

	if err := guard.Patch(context.Background(), tag, jsonPatch); !errors.Is(err, ErrSpecWriteForbidden) {
		t.Fatalf("patch = %v, want ErrSpecWriteForbidden", err)
	}

	if recorder.patches != 0 {
		t.Error("the refused patch still reached the API server")
	}
}
