package controller

import (
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// unusedClient builds a client nothing calls. These tests are about the cache's
// bookkeeping, so the only thing asked of a client here is that it be a distinct pointer.
func unusedClient(t *testing.T) *netbox.Client {
	t.Helper()
	client, err := netbox.New(netbox.Config{URL: "http://netbox.example", Token: "unused"})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return client
}

func rotateKey(secretVersion string) clientKey {
	return clientKey{namespace: "ns", name: "rotate", generation: 1, secretVersion: secretVersion}
}

// TestClientCachePutEvictsThePreviousClient is the invariant token rotation rests on: one
// entry per endpoint, so a rotated Secret cannot leave the old client reachable by an object
// controller.
//
// It lives here rather than in TestSecretRotationRebuildsTheClientWithoutRestart, which used
// to assert it by comparing client pointers after waiting for a request bearing the new
// token. Reconcile sends that request before it calls put, so the comparison raced a
// reconcile still in flight (NBO-091). With no cluster and no concurrency in the way, the
// same invariant is checkable exactly: the old key is gone, not merely a different pointer.
func TestClientCachePutEvictsThePreviousClient(t *testing.T) {
	cache := NewClientCache()
	first, second := unusedClient(t), unusedClient(t)

	cache.put(rotateKey("1"), first, provenance.Stamp{})
	cache.put(rotateKey("2"), second, provenance.Stamp{})

	got, _, ok := cache.Lookup("ns", "rotate")
	if !ok {
		t.Fatal("no client for the endpoint after a rotation")
	}
	if got != second {
		t.Error("Lookup returned the pre-rotation client")
	}
	if _, stale := cache.clients[rotateKey("1")]; stale {
		t.Error("the pre-rotation entry is still in the cache, so the old token is still reachable")
	}
	if n := len(cache.clients); n != 1 {
		t.Errorf("cache holds %d entries for one endpoint, want 1", n)
	}
}

// TestClientCachePutLeavesOtherEndpointsAlone is the other half of the same loop: it evicts
// by (namespace, name), and an eviction that matched too widely would take out every other
// endpoint's client on any rotation -- with the only symptom being object controllers
// briefly finding no client for an endpoint that never changed.
func TestClientCachePutLeavesOtherEndpointsAlone(t *testing.T) {
	cache := NewClientCache()
	sibling := unusedClient(t)
	siblingKey := clientKey{namespace: "ns", name: "other", generation: 1, secretVersion: "1"}

	cache.put(siblingKey, sibling, provenance.Stamp{})
	cache.put(rotateKey("1"), unusedClient(t), provenance.Stamp{})
	cache.put(rotateKey("2"), unusedClient(t), provenance.Stamp{})

	got, _, ok := cache.Lookup("ns", "other")
	if !ok {
		t.Fatal("the sibling endpoint's client was evicted by another endpoint's rotation")
	}
	if got != sibling {
		t.Error("the sibling endpoint's entry was replaced")
	}
}
