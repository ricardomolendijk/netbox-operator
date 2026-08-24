// Package controller wires Kinds to the reconcile engine. Controllers are wiring: a
// controller containing business logic has taken work that belongs to the engine.
package controller

import (
	"sync"

	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// clientKey identifies a cached client.
//
// The reconciler does not read through this cache before building -- it builds a client
// every reconcile and calls put, which evicts any previous entry for the same endpoint.
// So invalidation comes from that eviction, not from a key miss. The generation and
// secretVersion are carried anyway because they make an entry self-describing in a dump,
// and because a future read-through path would need exactly this key.
type clientKey struct {
	namespace     string
	name          string
	generation    int64
	secretVersion string
}

// cached is everything the endpoint controller proved about one endpoint and object
// controllers need back: the client, and the provenance stamp its bootstrap resolved.
//
// The stamp travels with the client rather than being looked up per pass because its tag id
// can only come from NetBox: resolving it here means two extra requests per endpoint per
// resync instead of two per object per reconcile. It is also the invalidation the stamp
// needs for free -- put evicts the whole entry, so an edit to spec.managedBy replaces the
// stamp at the same moment it replaces the client.
type cached struct {
	client *netbox.Client
	stamp  provenance.Stamp
}

// ClientCache hands out one client per (endpoint, spec generation, Secret version).
type ClientCache struct {
	mu      sync.RWMutex
	clients map[clientKey]cached
}

// NewClientCache returns an empty cache.
func NewClientCache() *ClientCache {
	return &ClientCache{clients: map[clientKey]cached{}}
}

// put stores client under key and drops any other entry for the same endpoint, so a
// rotated Secret or an edited spec cannot leave a stale client reachable.
//
// The evicted client's idle connections are released. Without that, every reconcile --
// including every resync tick -- would leave behind a transport holding an idle
// keep-alive pool, so connection pools would accumulate for the lifetime of the process.
func (c *ClientCache) put(key clientKey, client *netbox.Client, stamp provenance.Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for existing, previous := range c.clients {
		if existing.namespace == key.namespace && existing.name == key.name {
			previous.client.CloseIdleConnections()
			delete(c.clients, existing)
		}
	}
	c.clients[key] = cached{client: client, stamp: stamp}
	metrics.ClientCacheSize.Set(float64(len(c.clients)))
}

// Lookup returns the client for an endpoint by namespace and name, together with the
// provenance stamp its bootstrap resolved. Object controllers use this; a miss means the
// endpoint is not Ready yet.
func (c *ClientCache) Lookup(namespace, name string) (*netbox.Client, provenance.Stamp, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for key, entry := range c.clients {
		if key.namespace == namespace && key.name == name {
			return entry.client, entry.stamp, true
		}
	}
	return nil, provenance.Stamp{}, false
}

// Forget drops every client for an endpoint, used when it is deleted or stops being
// Ready. Leaving a client behind would let object controllers keep writing through a
// connection the endpoint has since rejected.
func (c *ClientCache) Forget(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.clients {
		if key.namespace == namespace && key.name == name {
			entry.client.CloseIdleConnections()
			delete(c.clients, key)
		}
	}
	metrics.ClientCacheSize.Set(float64(len(c.clients)))
}
