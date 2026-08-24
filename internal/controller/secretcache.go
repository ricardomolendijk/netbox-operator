package controller

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CredentialLabel must be set to CredentialLabelValue on every Secret a NetBoxEndpoint
// references, or the operator cannot see it.
//
// The key is prefixed with this project's API group because a credential Secret is
// commonly shared with other consumers, and an unprefixed key such as
// `endpoint-credential` would collide with whatever else labels it. It names the role the
// Secret plays rather than the operator's identity, so it stays correct when a second
// kind starts reading endpoint credentials.
const CredentialLabel = "netbox.kubeforge.org/endpoint-credential"

// CredentialLabelValue is the only value of CredentialLabel the operator's cache selects.
const CredentialLabelValue = "true"

// AllNamespaces is the one credential-namespace list entry that is not a namespace name.
// It asks for a cluster-scoped Secret informer, which needs a cluster-wide `list` and
// `watch` on Secrets that nothing in config/rbac grants any more -- so it is an explicit
// opt-out of NBO-072, not a default anybody can fall into. See docs/operations/rbac.md.
const AllNamespaces = "*"

// errNamespaceNotGranted is the sentinel behind the "no Role in that namespace" condition,
// so callers classify it by type rather than by reading the message.
var errNamespaceNotGranted = errors.New("credential namespace not granted")

// SecretScope is the set of namespaces the operator is permitted to read credential
// Secrets in: the deploy-time list from config/rbac/credential-namespaces/namespaces.txt,
// carried into the process so the informer and the reconciler agree with the RBAC.
//
// It is deploy-time configuration rather than something derived from the NetBoxEndpoints
// in the cluster, because RBAC is granted before the operator runs and cannot be widened
// by the operator itself: a Role the deployer did not create is a Role that does not
// exist. The zero value is cluster-wide, which is what the tests and `AllNamespaces` use.
type SecretScope struct {
	// namespaces is sorted and deduplicated; nil means every namespace.
	namespaces []string
}

// NewSecretScope returns the scope covering exactly these namespaces. An empty list is
// cluster-wide.
func NewSecretScope(namespaces []string) SecretScope {
	if len(namespaces) == 0 {
		return SecretScope{}
	}
	unique := slices.Clone(namespaces)
	slices.Sort(unique)
	return SecretScope{namespaces: slices.Compact(unique)}
}

// ParseSecretScope reads the comma-separated credential namespace list the manager is
// deployed with. `*` is cluster-wide; an empty list is an error rather than a silent
// cluster-wide default, because the whole point of NBO-072 is that cluster-wide Secret
// access is something a deployment states out loud.
func ParseSecretScope(value string) (SecretScope, error) {
	fields := strings.Split(value, ",")
	namespaces := make([]string, 0, len(fields))
	for _, field := range fields {
		if namespace := strings.TrimSpace(field); namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	if len(namespaces) == 0 {
		return SecretScope{}, errors.New("no namespaces listed")
	}
	if slices.Contains(namespaces, AllNamespaces) {
		// Not combinable: "every namespace, and also this one" is a list whose author
		// misunderstood it, and silently widening to cluster-wide is the failure this
		// ticket exists to remove.
		if len(namespaces) > 1 {
			return SecretScope{}, fmt.Errorf("%q cannot be combined with a namespace name", AllNamespaces)
		}
		return SecretScope{}, nil
	}
	return NewSecretScope(namespaces), nil
}

// ClusterWide reports whether the scope covers every namespace, which requires a
// cluster-wide Secret grant that config/rbac does not ship.
func (s SecretScope) ClusterWide() bool { return len(s.namespaces) == 0 }

// Namespaces is the enumerated list, empty when the scope is cluster-wide.
func (s SecretScope) Namespaces() []string { return slices.Clone(s.namespaces) }

// String renders the scope for a log line or a condition message.
func (s SecretScope) String() string {
	if s.ClusterWide() {
		return "every namespace"
	}
	return strings.Join(s.namespaces, ", ")
}

// CacheOptions scopes the manager's Secret informer to credential Secrets in the granted
// namespaces, and is shared by the manager and its tests so both cache the same set.
//
// Both halves are load-bearing:
//
// The label selector is applied to the informer's LIST and its WATCH, so an unlabelled
// Secret is never held in memory -- and, because the controller reads Secrets through that
// cache, never readable either. Without it the informer holds every Secret in the granted
// namespaces, so the manager's memory scales with their Secret count rather than with the
// number of endpoints.
//
// The namespace map is what makes namespaced RBAC work at all. A cluster-scoped informer
// issues `GET /api/v1/secrets?watch=true`, which RBAC evaluates at the cluster scope and
// no `Role` can satisfy; per-namespace informers issue
// `GET /api/v1/namespaces/<ns>/secrets?watch=true` instead, which the namespace's `Role`
// grants. Proved by TestNamespacedRoleCarriesTheInformersWatch. See NBO-072 and
// docs/operations/rbac.md.
func (s SecretScope) CacheOptions() map[client.Object]cache.ByObject {
	byObject := cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{CredentialLabel: CredentialLabelValue}),
	}
	if !s.ClusterWide() {
		byObject.Namespaces = make(map[string]cache.Config, len(s.namespaces))
		for _, namespace := range s.namespaces {
			// The empty Config inherits the label selector above, so each namespace's
			// LIST and WATCH is both namespaced and label-selected.
			byObject.Namespaces[namespace] = cache.Config{}
		}
	}
	return map[client.Object]cache.ByObject{&corev1.Secret{}: byObject}
}

// Check reports whether the operator may read Secrets in this namespace at all.
//
// It runs before the read rather than interpreting its failure, because neither failure a
// read produces is legible: an ungranted namespace is not in the informer's namespace map,
// so the cache rejects the Get with a message about its own internals, and an uncached
// read would return a bare `Forbidden`. Both are the same misconfiguration -- a namespace
// missing from the deploy-time list -- and this is the only place that can name it.
func (s SecretScope) Check(namespace string) error {
	if s.ClusterWide() || slices.Contains(s.namespaces, namespace) {
		return nil
	}
	return fmt.Errorf("%w: the operator has no Role for Secrets in namespace %q and is "+
		"granted %s; add %q to config/rbac/credential-namespaces/namespaces.txt, run "+
		"`make manifests` and redeploy (see docs/operations/rbac.md)",
		errNamespaceNotGranted, namespace, s, namespace)
}
