package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxwirelesslinks: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslinks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslinks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslinks/finalizers,verbs=update

// NetBoxWirelessLink's controller is one line, and the symmetry problem did not change that.
// "A link between A and B may already exist as a link between B and A" is expressed as a
// second natural-key candidate with its two filters crossed, so the reverse pair is found by
// the ordinary lookup and refused by the ordinary adoption check
// (internal/registry/wireless_wirelesslink.go). No canonicalisation step, and no engine code.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxWirelessLink{}) }
