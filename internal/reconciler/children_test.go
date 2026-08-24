package reconciler

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
)

// TestMaterialiseNames is the acceptance criterion in one assertion: a VM `dns` with an
// interface `eth0` carrying `10.20.0.10/24` yields `dns-eth0` and `dns-eth0-ip-10-20-0-10-24`,
// and the two child kinds under one parent do not collide because the address set carries a
// discriminator.
func TestMaterialiseNames(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0", children: []inlineChild{
		{key: "10.20.0.10/24"}, {key: "10.20.0.10/25"},
	}})
	parent.Spec.Disks = []inlineChild{{key: "eth0"}}

	children := newFakeChildren()
	inlinePass(t, parent, children).materialise(context.Background())

	want := []string{
		"NetBoxChildFake/dns-eth0",
		"NetBoxOtherChildFake/dns-eth0-ip-10-20-0-10-24",
		"NetBoxOtherChildFake/dns-eth0-ip-10-20-0-10-25",
		// The `disks` set shares the key `eth0` with `interfaces` and does not collide with
		// it, because the set carries a discriminator. That is what Discriminator is for.
		"NetBoxChildFake/dns-disk-eth0",
	}

	if got := children.names(); !slices.Equal(got, want) {
		t.Errorf("materialised\n%v\nwant\n%v", got, want)
	}

	paths := make([]string, 0, len(parent.Status.Children))
	for _, child := range parent.Status.Children {
		paths = append(paths, child.Path)
	}

	wantPaths := []string{
		"spec.disks[eth0]",
		"spec.interfaces[eth0]",
		"spec.interfaces[eth0].addresses[10.20.0.10/24]",
		"spec.interfaces[eth0].addresses[10.20.0.10/25]",
	}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("status.children paths\n%v\nwant\n%v", paths, wantPaths)
	}
}

// TestMaterialiseMarkers is ADR-0005 §2: both annotations, both labels, and the controller
// owner reference of ADR-0003 rule 3 -- exactly one of it, with both flags set.
func TestMaterialiseMarkers(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()
	inlinePass(t, parent, children).materialise(context.Background())

	child := children.find("dns-eth0")
	if child == nil {
		t.Fatal("no child was applied")
	}

	wantLabels := map[string]string{
		netboxv1alpha1.ManagedByLabel: netboxv1alpha1.ManagedByValue,
		netboxv1alpha1.OwnerUIDLabel:  "parent-uid",
	}
	if got := child.GetLabels(); !maps.Equal(got, wantLabels) {
		t.Errorf("labels = %v, want %v", got, wantLabels)
	}

	annotations := child.GetAnnotations()
	for name, want := range map[string]string{
		netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]",
		// <lowercase kind>/<namespace>/<name> of the *parent*, which is the same spelling
		// the k8s_owner custom field uses -- so one string identifies a CR on both sides.
		netboxv1alpha1.GeneratedByAnnotation: "netboxinlinefake/team-a/dns",
	} {
		if annotations[name] != want {
			t.Errorf("%s = %q, want %q", name, annotations[name], want)
		}
	}

	owners := child.GetOwnerReferences()
	if len(owners) != 1 {
		t.Fatalf("want exactly one owner reference, got %v", owners)
	}

	owner := owners[0]
	if owner.Controller == nil || !*owner.Controller {
		t.Error("the owner reference is not a controller reference, which is the marker " +
			"pruning and specGuard both read")
	}

	if owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
		t.Error("blockOwnerDeletion is not set, so --cascade=foreground would not order the " +
			"NetBox deletes")
	}

	if owner.UID != types.UID("parent-uid") || owner.Kind != inlineGVK.Kind {
		t.Errorf("the owner reference names %s/%s uid %s, want %s/dns uid parent-uid",
			owner.Kind, owner.Name, owner.UID, inlineGVK.Kind)
	}
}

// TestMaterialiseInherits is the two fields a child takes from its parent, and the three it
// must not: inheriting free text and tag sets makes a drift report lie about where a value
// came from.
func TestMaterialiseInherits(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()
	inlinePass(t, parent, children).materialise(context.Background())

	child := children.find("dns-eth0")

	spec, ok := child.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("the applied child has no spec: %v", child.Object)
	}

	if spec["endpointRef"] != "homelab" {
		t.Errorf("endpointRef = %v, want homelab", spec["endpointRef"])
	}

	if spec["deletionPolicy"] != string(netboxv1alpha1.DeletionRetain) {
		t.Errorf("deletionPolicy = %v, want Retain", spec["deletionPolicy"])
	}

	if _, inherited := spec["customFields"]; inherited {
		t.Error("customFields was inherited; it must not be")
	}

	if child.GetNamespace() != parent.GetNamespace() {
		t.Errorf("the child is in %q, want the parent's namespace %q",
			child.GetNamespace(), parent.GetNamespace())
	}
}

// TestMaterialiseGitOps is ADR-0005 §5: the annotation set is configurable, every entry can
// be disabled, and the two markers pruning reads cannot be.
func TestMaterialiseGitOps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		gitops *GitOps
		want   map[string]string
		absent []string
	}{{
		name: "the default annotates for argo and not for flux",
		want: map[string]string{
			netboxv1alpha1.ArgoCDCompareOptionsAnnotation: netboxv1alpha1.ArgoCDIgnoreExtraneous,
		},
		absent: []string{netboxv1alpha1.FluxReconcileAnnotation},
	}, {
		name:   "enabling flux adds its annotation",
		gitops: &GitOps{ArgoCD: true, Flux: true},
		want: map[string]string{
			netboxv1alpha1.ArgoCDCompareOptionsAnnotation: netboxv1alpha1.ArgoCDIgnoreExtraneous,
			netboxv1alpha1.FluxReconcileAnnotation:        netboxv1alpha1.FluxReconcileDisabled,
		},
	}, {
		name:   "disabling argo removes its annotation and changes nothing else",
		gitops: &GitOps{},
		absent: []string{
			netboxv1alpha1.ArgoCDCompareOptionsAnnotation,
			netboxv1alpha1.FluxReconcileAnnotation,
		},
	}, {
		name:   "extra annotations are applied verbatim",
		gitops: &GitOps{Extra: map[string]string{"example.com/team": "platform"}},
		want:   map[string]string{"example.com/team": "platform"},
	}, {
		// The two markers are how the operator recognises its own output, so disabling them
		// would not quieten a tool, it would break pruning.
		name: "extra annotations cannot override the two markers",
		gitops: &GitOps{Extra: map[string]string{
			netboxv1alpha1.OwnedByPathAnnotation: "spec.somewhere[else]",
			netboxv1alpha1.GeneratedByAnnotation: "someone/else/entirely",
		}},
		want: map[string]string{
			netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]",
			netboxv1alpha1.GeneratedByAnnotation: "netboxinlinefake/team-a/dns",
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent := inlineParent(inlineChild{key: "eth0"})
			children := newFakeChildren()
			p := inlinePass(t, parent, children)
			p.engine.GitOps = tc.gitops

			p.materialise(context.Background())

			annotations := children.find("dns-eth0").GetAnnotations()

			for name, want := range tc.want {
				if annotations[name] != want {
					t.Errorf("%s = %q, want %q", name, annotations[name], want)
				}
			}

			for _, name := range tc.absent {
				if value, present := annotations[name]; present {
					t.Errorf("%s should be absent, got %q", name, value)
				}
			}

			// Always, whatever the configuration: the label is not part of the GitOps set.
			if annotations[netboxv1alpha1.GeneratedByAnnotation] == "" {
				t.Error("generated-by is missing, which no configuration may cause")
			}
		})
	}
}

// TestMaterialiseNeverHijacks is the guard clause of ADR-0003 rule 5: a CR at the derived name
// that we do not control is left completely untouched -- no PATCH, no label, no owner
// reference -- and the *parent* reports Conflict naming it and its actual controller.
func TestMaterialiseNeverHijacks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		plant  func(*fakeChildren)
		expect string
	}{{
		name: "a hand-written CR with no owner at all",
		plant: func(f *fakeChildren) {
			f.plant(childGVK, "dns-eth0", nil, nil)
		},
		expect: "unowned",
	}, {
		name: "a CR another controller owns",
		plant: func(f *fakeChildren) {
			other := ourOwnerRef("someone-else")
			other.Name = "web"
			f.plant(childGVK, "dns-eth0", ourMarkers("someone-else"), nil, other)
		},
		expect: "controlled by NetBoxInlineFake web",
	}, {
		// Our label and no controller reference: a manifest copied out of
		// `kubectl get -o yaml`, which is exactly how this happens in practice. One marker
		// is not enough, deliberately.
		name: "our label without our controller reference",
		plant: func(f *fakeChildren) {
			f.plant(childGVK, "dns-eth0", ourMarkers("parent-uid"), nil)
		},
		expect: "unowned",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent := inlineParent(inlineChild{key: "eth0"})
			children := newFakeChildren()
			tc.plant(children)

			before := *children.store["NetBoxChildFake/dns-eth0"].DeepCopy()

			inlinePass(t, parent, children).materialise(context.Background())

			if len(children.applied) != 0 {
				t.Errorf("wrote to an object it does not own: %v", children.names())
			}

			if len(children.deleted) != 0 {
				t.Errorf("deleted an object it does not own: %v", children.deleted)
			}

			after := children.store["NetBoxChildFake/dns-eth0"]
			if !maps.Equal(before.GetLabels(), after.GetLabels()) ||
				!maps.Equal(before.GetAnnotations(), after.GetAnnotations()) {
				t.Error("the pre-existing object's metadata changed")
			}

			condition := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady)
			if condition.Reason != netboxv1alpha1.ReasonConflict {
				t.Fatalf("ChildrenReady reason = %q, want Conflict", condition.Reason)
			}

			for _, want := range []string{"team-a/dns-eth0", tc.expect} {
				if !strings.Contains(condition.Message, want) {
					t.Errorf("the Conflict message does not name %q: %s", want, condition.Message)
				}
			}

			// A conflicting child is not claimed as ours, so it never reaches status.children.
			if len(parent.Status.Children) != 0 {
				t.Errorf("status.children claims a child we did not write: %v", parent.Status.Children)
			}
		})
	}
}

// TestMaterialisePrunes is the three cases pruning has to tell apart, in one pass: a child
// whose path is still declared is kept, one whose path is gone is deleted, and one with no
// marker -- a human's -- is never touched even though it carries our label.
func TestMaterialisePrunes(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()

	// Still declared.
	children.plant(childGVK, "dns-eth0",
		ourMarkers("parent-uid"),
		map[string]string{netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]"},
		ourOwnerRef("parent-uid"))

	// Declared last time and not now: the user removed `eth1`.
	children.plant(childGVK, "dns-eth1",
		ourMarkers("parent-uid"),
		map[string]string{netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth1]"},
		ourOwnerRef("parent-uid"))

	// A human's, carrying our label but no marker.
	children.plant(childGVK, "handwritten", ourMarkers("parent-uid"), nil)

	// A child of a different parent that happens to be in the namespace. Not selected at all,
	// because the selector is on the uid.
	children.plant(childGVK, "other-eth0",
		ourMarkers("other-uid"),
		map[string]string{netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]"},
		ourOwnerRef("other-uid"))

	p := inlinePass(t, parent, children)
	p.before.Children = []netboxv1alpha1.ChildStatus{
		{Path: "spec.interfaces[eth0]", Kind: childGVK.Kind, Name: "dns-eth0"},
		{Path: "spec.interfaces[eth1]", Kind: childGVK.Kind, Name: "dns-eth1"},
	}

	p.materialise(context.Background())

	if want := []string{"NetBoxChildFake/dns-eth1"}; !slices.Equal(children.deleted, want) {
		t.Errorf("deleted %v, want exactly %v", children.deleted, want)
	}

	for _, survivor := range []string{"NetBoxChildFake/dns-eth0", "NetBoxChildFake/handwritten",
		"NetBoxChildFake/other-eth0"} {
		if _, ok := children.store[survivor]; !ok {
			t.Errorf("%s should have survived the prune", survivor)
		}
	}
}

// TestMaterialisePrunesWithNoDeclarationsLeft is the case status.children exists for: with
// every inline entry removed there is no desired child left to read a GVK off, so a pruner
// that could only look at the desired set would find nothing to list and leave the children
// behind forever.
func TestMaterialisePrunesWithNoDeclarationsLeft(t *testing.T) {
	t.Parallel()

	parent := inlineParent()
	children := newFakeChildren()
	children.plant(childGVK, "dns-eth0",
		ourMarkers("parent-uid"),
		map[string]string{netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]"},
		ourOwnerRef("parent-uid"))

	p := inlinePass(t, parent, children)
	p.before.Children = []netboxv1alpha1.ChildStatus{
		{Path: "spec.interfaces[eth0]", Kind: childGVK.Kind, Name: "dns-eth0"},
	}

	p.materialise(context.Background())

	if want := []string{"NetBoxChildFake/dns-eth0"}; !slices.Equal(children.deleted, want) {
		t.Errorf("deleted %v, want %v", children.deleted, want)
	}
}

// TestMaterialisePruneBlocked is the blast-radius cap: a prune set past the margin deletes
// nothing and says so, because a prune that wants far more than the parent declares is a bug
// in the operator rather than a user's intent.
func TestMaterialisePruneBlocked(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()

	stale := make([]netboxv1alpha1.ChildStatus, 0, pruneMargin+4)

	for i := range pruneMargin + 4 {
		path := "spec.interfaces[gone" + string(rune('a'+i)) + "]"
		name := "dns-gone" + string(rune('a'+i))
		children.plant(childGVK, name, ourMarkers("parent-uid"),
			map[string]string{netboxv1alpha1.OwnedByPathAnnotation: path},
			ourOwnerRef("parent-uid"))
		stale = append(stale, netboxv1alpha1.ChildStatus{Path: path, Kind: childGVK.Kind, Name: name})
	}

	p := inlinePass(t, parent, children)
	p.before.Children = stale

	p.materialise(context.Background())

	if len(children.deleted) != 0 {
		t.Errorf("a blocked prune deleted %v", children.deleted)
	}

	condition := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady)
	if condition.Reason != netboxv1alpha1.ReasonPruneBlocked {
		t.Errorf("ChildrenReady reason = %q, want PruneBlocked (%s)", condition.Reason, condition.Message)
	}
}

// TestMaterialiseSkips is the three guards: a terminating parent, an endpoint that sent
// nothing, and a parent with no NetBox id. All three write nothing and all three say why --
// except the first, which has nothing to say because the cascade is already under way.
func TestMaterialiseSkips(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*inlineKind, *pass)
		reason string
	}{{
		name: "a terminating parent",
		mutate: func(parent *inlineKind, _ *pass) {
			parent.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
		},
		reason: "",
	}, {
		name:   "a dry-run endpoint",
		mutate: func(_ *inlineKind, p *pass) { p.result = metrics.ResultDryRun },
		reason: netboxv1alpha1.ReasonDryRunPending,
	}, {
		name: "an endpoint whose driftMode is Report",
		mutate: func(_ *inlineKind, p *pass) {
			p.result = metrics.ResultReported
			p.endpoint.DriftMode = netboxv1alpha1.DriftReport
		},
		reason: netboxv1alpha1.ReasonReportPending,
	}, {
		name:   "a parent with no netbox id",
		mutate: func(parent *inlineKind, _ *pass) { parent.Status.ID = 0 },
		reason: netboxv1alpha1.ReasonPendingChildren,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent := inlineParent(inlineChild{key: "eth0"})
			children := newFakeChildren()
			p := inlinePass(t, parent, children)
			tc.mutate(parent, p)

			if wait := p.materialise(context.Background()); wait != 0 {
				t.Errorf("a skipped materialisation asked to come back in %s", wait)
			}

			if len(children.applied)+len(children.deleted) != 0 {
				t.Errorf("wrote %v and deleted %v", children.names(), children.deleted)
			}

			got := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady).Reason
			if got != tc.reason {
				t.Errorf("ChildrenReady reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// TestMaterialiseNameCollision is fail-closed: two entries deriving one name would each apply
// it in turn and the object would flap between two specs forever, so nothing is written and
// the condition names both entries.
func TestMaterialiseNameCollision(t *testing.T) {
	t.Parallel()

	// Two keys that slugify to the same thing. `eth0/1` and `eth0.1` are different NetBox
	// interfaces and the same DNS name.
	parent := inlineParent(inlineChild{key: "eth0/1"}, inlineChild{key: "eth0.1"})
	children := newFakeChildren()

	inlinePass(t, parent, children).materialise(context.Background())

	if len(children.applied) != 0 {
		t.Errorf("wrote %v; a name collision must write nothing at all", children.names())
	}

	condition := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady)
	if condition.Reason != netboxv1alpha1.ReasonConflict {
		t.Fatalf("ChildrenReady reason = %q, want Conflict", condition.Reason)
	}

	for _, want := range []string{"spec.interfaces[eth0/1]", "spec.interfaces[eth0.1]", "dns-eth0-1"} {
		if !strings.Contains(condition.Message, want) {
			t.Errorf("the message does not name %q: %s", want, condition.Message)
		}
	}
}

// TestMaterialiseReorderIsInert is the idempotency claim, measured rather than judged:
// reordering the inline lists produces the same names, the same paths and byte-identical
// applies, so a real server-side apply has nothing to change and bumps no resourceVersion.
func TestMaterialiseReorderIsInert(t *testing.T) {
	t.Parallel()

	declared := []inlineChild{{key: "eth0"}, {key: "eth1"}, {key: "eth2"}}
	reordered := []inlineChild{{key: "eth2"}, {key: "eth0"}, {key: "eth1"}}

	applied := func(order []inlineChild) map[string]string {
		parent := inlineParent(order...)
		children := newFakeChildren()
		inlinePass(t, parent, children).materialise(context.Background())

		out := make(map[string]string, len(children.applied))
		for _, obj := range children.applied {
			out[obj.GetName()] = obj.GetAnnotations()[netboxv1alpha1.OwnedByPathAnnotation]
		}

		return out
	}

	before, after := applied(declared), applied(reordered)
	if !maps.Equal(before, after) {
		t.Errorf("reordering changed what was applied:\n%v\n%v", before, after)
	}
}

// TestMaterialiseRevertsAField is the hand-edit case. The unforced apply is refused with the
// fields it was refused over, which is the only way those names reach the Event; the forced
// retry then takes them back, because the parent's inline entry is the declared source of
// truth for the fields the materialiser sets.
func TestMaterialiseRevertsAField(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()
	children.conflictOn = "dns-eth0"
	children.plant(childGVK, "dns-eth0",
		ourMarkers("parent-uid"),
		map[string]string{netboxv1alpha1.OwnedByPathAnnotation: "spec.interfaces[eth0]"},
		ourOwnerRef("parent-uid"))

	p := inlinePass(t, parent, children)
	recorder, ok := p.engine.Events.(*fakeRecorder)
	if !ok {
		t.Fatalf("the engine's recorder is a %T", p.engine.Events)
	}

	p.materialise(context.Background())

	if want := []string{"NetBoxChildFake/dns-eth0"}; !slices.Equal(children.forced, want) {
		t.Errorf("forced applies = %v, want %v", children.forced, want)
	}

	if !slices.Contains(recorder.events, "Warning/"+netboxv1alpha1.EventChildFieldReverted) {
		t.Errorf("no ChildFieldReverted Event: %v", recorder.events)
	}
}

// TestMaterialiseReadyGating is ADR-0003 rule 5's "kubectl wait on a VM means the VM and its
// interfaces": a Ready=True is downgraded while a child is not ready, and a Ready=False that
// a step already set for its own reason is left alone -- it is the more specific answer.
func TestMaterialiseReadyGating(t *testing.T) {
	t.Parallel()

	t.Run("a pending child downgrades Ready", func(t *testing.T) {
		t.Parallel()

		parent := inlineParent(inlineChild{key: "eth0"})
		p := inlinePass(t, parent, newFakeChildren())
		p.condition(netboxv1alpha1.ConditionReady, true, netboxv1alpha1.ReasonSynced, "synced")

		if wait := p.materialise(context.Background()); wait != childRetry {
			t.Errorf("requeue = %s, want %s", wait, childRetry)
		}

		ready := inlineConditionOf(parent, netboxv1alpha1.ConditionReady)
		if ready.Status != metav1.ConditionFalse ||
			ready.Reason != netboxv1alpha1.ReasonPendingChildren {
			t.Errorf("Ready = %s/%s, want False/PendingChildren", ready.Status, ready.Reason)
		}
	})

	t.Run("an existing Ready=False keeps its own reason", func(t *testing.T) {
		t.Parallel()

		parent := inlineParent(inlineChild{key: "eth0"})
		p := inlinePass(t, parent, newFakeChildren())
		p.condition(netboxv1alpha1.ConditionReady, false, netboxv1alpha1.ReasonWaitingForRef, "a ref")

		p.materialise(context.Background())

		if got := inlineConditionOf(parent, netboxv1alpha1.ConditionReady).Reason; got !=
			netboxv1alpha1.ReasonWaitingForRef {
			t.Errorf("Ready reason = %q, want the more specific WaitingForRef", got)
		}
	})

	t.Run("a ready child settles the parent", func(t *testing.T) {
		t.Parallel()

		parent := inlineParent(inlineChild{key: "eth0"})
		children := newFakeChildren()
		p := inlinePass(t, parent, children)

		// The API server returns the live object from an apply, so a child that is already
		// Ready reports Ready in the same pass. Reproduced here by having the apply write
		// the condition onto the object it was handed, which is what `.Into(obj)` does.
		p.engine.Children = &readyChildren{fakeChildren: children}

		if wait := p.materialise(context.Background()); wait != 0 {
			t.Errorf("requeue = %s, want none", wait)
		}

		condition := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady)
		if condition.Status != metav1.ConditionTrue ||
			condition.Reason != netboxv1alpha1.ReasonAllReady {
			t.Errorf("ChildrenReady = %s/%s, want True/AllReady", condition.Status, condition.Reason)
		}

		if len(parent.Status.Children) != 1 || !parent.Status.Children[0].Ready {
			t.Errorf("status.children = %v, want one ready entry", parent.Status.Children)
		}
	})
}

// readyChildren is fakeChildren with an API server that hands back a Ready child, which is
// what a real apply's response does.
type readyChildren struct{ *fakeChildren }

func (r *readyChildren) Apply(ctx context.Context, obj client.Object, opts ...client.PatchOption) error {
	if err := r.fakeChildren.Apply(ctx, obj, opts...); err != nil {
		return err
	}

	child, ok := obj.(Object)
	if !ok {
		return nil
	}

	child.NetBoxStatus().Conditions = []metav1.Condition{{
		Type: netboxv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		Reason: netboxv1alpha1.ReasonSynced, LastTransitionTime: metav1.Now(),
	}}

	return nil
}

// TestMaterialiseReportsAPIFailures is the read that failed: an unreadable name is not an
// absence, so nothing is written to it, and a failed list is not an empty prune set.
func TestMaterialiseReportsAPIFailures(t *testing.T) {
	t.Parallel()

	parent := inlineParent(inlineChild{key: "eth0"})
	children := newFakeChildren()
	children.listErr = errors.New("the api server is unavailable")

	p := inlinePass(t, parent, children)
	p.before.Children = []netboxv1alpha1.ChildStatus{
		{Path: "spec.interfaces[gone]", Kind: childGVK.Kind, Name: "dns-gone"},
	}

	p.materialise(context.Background())

	if len(children.deleted) != 0 {
		t.Errorf("a failed list deleted %v; it must be treated as unknown, not as empty",
			children.deleted)
	}

	condition := inlineConditionOf(parent, netboxv1alpha1.ConditionChildrenReady)
	if condition.Reason != netboxv1alpha1.ReasonAPIError {
		t.Errorf("ChildrenReady reason = %q, want APIError (%s)", condition.Reason, condition.Message)
	}
}

// TestMaterialiseIgnoresPlainKinds is the criterion that keeps this feature off every other
// kind's path: a Kind that does not implement InlineParent reconciles exactly as before, with
// no ChildrenReady condition and no list call.
func TestMaterialiseIgnoresPlainKinds(t *testing.T) {
	t.Parallel()

	obj := fakeObject()
	obj.Status.ID = 7

	children := newFakeChildren()
	p := &pass{
		engine: &Engine{Children: children, Scheme: inlineScheme(t)},
		obj:    obj,
		before: obj.Status.DeepCopy(),
		desc:   fakeDescriptor(),
	}

	if wait := p.materialise(context.Background()); wait != 0 {
		t.Errorf("a kind with no inline children asked to come back in %s", wait)
	}

	if len(children.applied)+len(children.deleted) != 0 {
		t.Error("a kind with no inline children touched the API server")
	}

	if conditionOf(obj, netboxv1alpha1.ConditionChildrenReady).Type != "" {
		t.Error("a kind with no inline children carries a ChildrenReady condition")
	}
}
