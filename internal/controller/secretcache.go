package controller

import (
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

// SecretCacheOptions scopes the manager's Secret informer to credential Secrets, and is
// shared by the manager and its tests so both cache the same set.
//
// Without it the informer holds every Secret in the cluster, so the manager's memory
// scales with the cluster's Secret count rather than with the number of endpoints. The
// selector is applied to the informer's LIST and its WATCH, so an unlabelled Secret is
// never held in memory -- and, because the controller reads Secrets through that cache,
// never readable either. See NBO-072 and docs/operations/rbac.md.
func SecretCacheOptions() map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{
		&corev1.Secret{}: {
			Label: labels.SelectorFromSet(labels.Set{CredentialLabel: CredentialLabelValue}),
		},
	}
}
