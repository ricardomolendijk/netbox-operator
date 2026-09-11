package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvlantranslationpolicies: the operator reads CRs and
// writes their status and finalizers, and nothing else. The create/delete pair its
// `spec.rules` needs is on the *rule's* own marker, for the reason
// virtualization_vminterface_controller.go gives.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlantranslationpolicies/finalizers,verbs=update

// NetBoxVLANTranslationPolicy's controller is one line. Its endpoint, its hand-declared `name`
// candidate and its field map are data on its Descriptor
// (internal/registry/ipam_vlantranslationpolicy.go), the inline expansion of `spec.rules` is
// the InlineParent implementation on the type
// (api/v1alpha1/ipam_vlantranslationpolicy_inline.go), and every create, adopt, update,
// materialise, prune and delete decision is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVLANTranslationPolicy{}) }
