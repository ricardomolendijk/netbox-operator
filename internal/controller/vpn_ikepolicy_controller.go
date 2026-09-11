package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxikepolicies: the operator reads CRs and writes their
// status and finalizers, and nothing else.
//
// No Secret grant either, and that is the point of #241 rather than an omission here: this
// kind holds no SecretRef, so the manager needs no read on Secrets to reconcile it
// (api/v1alpha1/vpn_ikepolicy.go, docs/operations/rbac.md).
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikepolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikepolicies/finalizers,verbs=update

// NetBoxIKEPolicy's controller is one line. Its endpoint, its single `name` candidate, its
// to-many `proposals` relation and its field map -- which deliberately omits `preshared_key`
// -- are data on its Descriptor (internal/registry/vpn_ikepolicy.go), and every create, adopt,
// update and delete decision is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIKEPolicy{}) }
