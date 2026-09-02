package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// `create` and `delete` on netboxvlantranslationrules, unlike most kinds, and it is not this
// controller that needs them: a NetBoxVLANTranslationPolicy's `spec.rules` materialises these
// as owned children, so the materialiser applies and prunes them (NBO-033). The verbs go on
// this Kind's own marker rather than on the policy's, because controller-gen silently drops
// `resources=*` and the alternative is one group rule carrying a hand-maintained list of every
// materialisable Kind -- exactly the thing that goes stale when a Kind is added.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationrules/finalizers,verbs=update

// NetBoxVLANTranslationRule's controller is one line. Its two natural-key candidates, its
// `policyRef` containment parent and its field map are data on its Descriptor
// (internal/registry/ipam_vlantranslationrule.go), and every create, adopt, update and delete
// decision is the engine's (internal/reconciler) -- including the Conflict that a duplicate
// `local_vid` or `remote_vid` inside one policy produces.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVLANTranslationRule{}) }
