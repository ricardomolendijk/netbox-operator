// Package controller wires Kinds to the reconcile engine. Controllers are wiring: a
// controller containing business logic has taken work that belongs to the engine.
package controller

import (
	"sync"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// clientKey identifies a cached client. The Secret's resourceVersion is part of the key
// so that rotating a token invalidates the cache by construction: a rotated Secret gets
// a new resourceVersion, misses the cache, and the next reconcile builds a client with
// the new token. No invalidation logic, no restart.
type clientKey struct {
	namespace     string
	name          string
	generation    int64
	secretVersion string
}

// ClientCache hands out one client per (endpoint, spec generation, Secret version).
type ClientCache struct {
	mu      sync.RWMutex
	clients map[clientKey]*netbox.Client
}

// NewClientCache returns an empty cache.
func NewClientCache() *ClientCache {
	return &ClientCache{clients: map[clientKey]*netbox.Client{}}
}

// put stores client under key and drops any other entry for the same endpoint, so a
// rotated Secret or an edited spec cannot leave a stale client reachable.
func (c *ClientCache) put(key clientKey, client *netbox.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for existing := range c.clients {
		if existing.namespace == key.namespace && existing.name == key.name {
			delete(c.clients, existing)
		}
	}
	c.clients[key] = client
}

// Lookup returns the client for an endpoint by namespace and name, if one has been built.
// Object controllers use this; a miss means the endpoint is not Ready yet.
func (c *ClientCache) Lookup(namespace, name string) (*netbox.Client, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for key, client := range c.clients {
		if key.namespace == namespace && key.name == name {
			return client, true
		}
	}
	return nil, false
}

// Forget drops every client for an endpoint, used when it is deleted or stops being
// Ready. Leaving a client behind would let object controllers keep writing through a
// connection the endpoint has since rejected.
func (c *ClientCache) Forget(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.clients {
		if key.namespace == namespace && key.name == name {
			delete(c.clients, key)
		}
	}
}
