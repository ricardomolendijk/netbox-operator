package reconciler

import (
	"context"
	"slices"
	"strings"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// reservingDescriptor is the fake kind with the collision declared on it: the extras.Tag
// shape, keyed on `slug`, whose NetBox model the provenance bootstrap also writes.
//
// The real pairing is NetBoxTag/`extras.tag` and NetBoxCustomField/`extras.customfield`, and
// fakeDescriptor already claims `extras.tag` as its object type -- so this is the real
// collision with a fake Kind bolted onto it, which is exactly what the engine sees.
func reservingDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.ReservedKeySpec = "slug"

	return d
}

// stampingEndpoint is an endpoint whose spec.managedBy is set, resolved the way the endpoint
// controller resolves it. `managed` is the slug fakeObject() claims, so the two collide.
func stampingEndpoint(client *fakeClient, tag string) Endpoint {
	return Endpoint{
		Client: client,
		Resync: testResync,
		Provenance: provenance.Stamp{
			Config: provenance.Config{ClusterID: "prod-eu", Tag: tag, UIDField: provenance.DefaultUIDField},
			TagID:  4,
		},
	}
}

// TestReservedByProvenanceWritesNothing is the collision NBO-059 exists to answer: a CR
// naming a NetBox object the operator's own bootstrap writes.
//
// Two writers for one object is not a state the engine can make safe, and the CR would win
// every fight -- it PATCHes on every resync, and its `objectTypes` is a static list where the
// bootstrap's is derived from the descriptor registry and widens with every kind added. So
// the answer is a refusal, and this is the assertion that it is a refusal *before* any
// request rather than a merge afterwards: zero calls of any method, on an endpoint whose
// client is perfectly capable of writing.
func TestReservedByProvenanceWritesNothing(t *testing.T) {
	tests := []struct {
		name       string
		descriptor func() registry.Descriptor
		endpoint   func(*fakeClient) Endpoint

		wantMethods []string
		wantReason  string
	}{
		{
			name:       "a CR for a name the bootstrap owns is refused and nothing is sent",
			descriptor: reservingDescriptor,
			endpoint:   func(c *fakeClient) Endpoint { return stampingEndpoint(c, "managed") },
			// The whole point. Not "created and then reverted", not "adopted and left alone":
			// the engine never looked the object up, so it cannot have taken it over.
			wantMethods: nil,
			wantReason:  netboxv1alpha1.ReasonReservedByOperator,
		},
		{
			name:       "the same CR against an endpoint that renamed its tag is an ordinary object",
			descriptor: reservingDescriptor,
			endpoint:   func(c *fakeClient) Endpoint { return stampingEndpoint(c, "k8s-managed") },
			// Reserved is per endpoint because the names are per endpoint. This endpoint
			// bootstraps `k8s-managed`, so `managed` is nobody's but the CR's.
			wantMethods: []string{"GETONE", "POST"},
			wantReason:  "",
		},
		{
			name:       "an endpoint that stamps nothing reserves nothing",
			descriptor: reservingDescriptor,
			endpoint: func(c *fakeClient) Endpoint {
				// No spec.managedBy at all: Config.Enabled() is false, so there is no second
				// writer and nothing to refuse.
				return Endpoint{Client: c, Resync: testResync}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantReason:  "",
		},
		{
			name:       "a kind that declares no reserved key is never refused",
			descriptor: fakeDescriptor,
			endpoint:   func(c *fakeClient) Endpoint { return stampingEndpoint(c, "managed") },
			// Same endpoint, same slug, same collision -- and no ReservedKeySpec, so the
			// engine has nothing to compare and writes as usual. That is what keeps this a
			// per-kind declaration rather than a rule about every kind with a `slug`.
			wantMethods: []string{"GETONE", "POST"},
			wantReason:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{created: liveTag(7)}
			obj := fakeObject()

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: tc.descriptor(), registered: true},
				Endpoints:   fakeEndpoints{endpoint: tc.endpoint(client), ready: true},
				Status:      &fakeStatus{},
				Finalizers:  &fakeFinalizers{},
				Events:      &fakeRecorder{},
				Scheme:      fakeScheme(t),
			}

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v, want no error: a refused object is a condition", err)
			}

			if got := client.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if tc.wantReason == "" {
				if ready.Reason == netboxv1alpha1.ReasonReservedByOperator {
					t.Fatalf("Ready reason = %q, want anything but a refusal", ready.Reason)
				}

				return
			}

			if ready.Reason != tc.wantReason {
				t.Fatalf("Ready reason = %q, want %q", ready.Reason, tc.wantReason)
			}

			// The message has to name the spec field, the value and the endpoint, because the
			// fix is on the endpoint's spec.managedBy or on the CR's own name and the reader
			// has no way to know which without all three.
			for _, want := range []string{"slug", `"managed"`, `"homelab"`, "spec.managedBy"} {
				if !strings.Contains(ready.Message, want) {
					t.Errorf("Ready message = %q, want it to mention %s", ready.Message, want)
				}
			}
		})
	}
}

// TestDataLossBlockedKeepsTheFinalizer is the other half of NBO-059's answer, and the one
// that survives a user deciding to delete the CR anyway.
//
// Deleting an extras.CustomField strips its stored value from every object in NetBox that has
// one, and NetBox performs the delete without complaint -- so the engine's usual safety net,
// letting NetBox refuse with a PROTECT, cannot fire. The refusal has to be the operator's,
// and it has to be reversible: the finalizer stays on, so the CR and the NetBox object are
// both still there when somebody decides.
func TestDataLossBlockedKeepsTheFinalizer(t *testing.T) {
	destructive := func() registry.Descriptor {
		d := fakeDescriptor()
		d.DataLossOnDelete = true

		return d
	}

	tests := []struct {
		name       string
		descriptor func() registry.Descriptor
		object     func() *fakeKind

		wantMethods    []string
		wantFinalizers []string
		wantReason     string
	}{
		{
			name:       "the delete is refused and both sides are left intact",
			descriptor: destructive,
			object:     deletingObject,
			// No DELETE at all, and the finalizer stays: nothing has been destroyed and
			// nothing has been orphaned.
			wantMethods:    nil,
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantReason:     netboxv1alpha1.ReasonDataLossBlocked,
		},
		{
			name:       "the annotation is the way through",
			descriptor: destructive,
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Annotations = map[string]string{netboxv1alpha1.AllowDataLossAnnotation: "true"}

				return obj
			},
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
		},
		{
			name:       "any value but true blocks, so a typo is safe in the direction that keeps the data",
			descriptor: destructive,
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Annotations = map[string]string{netboxv1alpha1.AllowDataLossAnnotation: "yes"}

				return obj
			},
			wantMethods:    nil,
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantReason:     netboxv1alpha1.ReasonDataLossBlocked,
		},
		{
			name:       "Retain is the other way out, and needs no annotation",
			descriptor: destructive,
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain

				return obj
			},
			// Checked before the guard on purpose: a CR that never meant to delete the NetBox
			// object should not have to make a decision about data it is not going to destroy.
			wantMethods:    nil,
			wantFinalizers: []string{},
		},
		{
			name:       "the break-glass annotation still wins, because a stuck finalizer is worse",
			descriptor: destructive,
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Annotations = map[string]string{netboxv1alpha1.SkipFinalizerAnnotation: "true"}

				return obj
			},
			wantMethods:    nil,
			wantFinalizers: []string{},
		},
		{
			name:           "a kind that destroys nothing is unaffected",
			descriptor:     fakeDescriptor,
			object:         deletingObject,
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{}
			obj := tc.object()

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: tc.descriptor(), registered: true},
				Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
				Status:      &fakeStatus{},
				Finalizers:  &fakeFinalizers{},
				Events:      &fakeRecorder{},
				Scheme:      fakeScheme(t),
			}

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v, want no error: a blocked delete is a condition", err)
			}

			if got := client.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			if got := obj.GetFinalizers(); !slices.Equal(got, tc.wantFinalizers) {
				t.Errorf("finalizers = %v, want %v", got, tc.wantFinalizers)
			}

			deleting := conditionOf(obj, netboxv1alpha1.ConditionDeleting)
			if deleting.Reason != tc.wantReason {
				t.Fatalf("Deleting reason = %q, want %q", deleting.Reason, tc.wantReason)
			}

			if tc.wantReason == "" {
				return
			}

			// Both ways out, named in the message: whoever is reading this condition is
			// deciding whether to destroy data, and a condition that says "blocked" without
			// saying how to proceed makes them go and read the source.
			for _, want := range []string{netboxv1alpha1.AllowDataLossAnnotation, "deletionPolicy: Retain"} {
				if !strings.Contains(deleting.Message, want) {
					t.Errorf("Deleting message = %q, want it to mention %s", deleting.Message, want)
				}
			}
		})
	}
}
