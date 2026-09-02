package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxpowerfeeds: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerfeeds,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerfeeds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerfeeds/finalizers,verbs=update

// NetBoxPowerFeed's controller is one line, and it is the case NBO-052 was expected to need
// code for: `voltage`, `amperage` and `maxUtilization` default to the *target NetBox's* own
// configuration rather than to a model constant, so an unset one must reach neither the create
// body nor the drift comparison.
//
// No branch here does that, and none needs to. The three are optional pointers with no
// `+kubebuilder:default` (api/v1alpha1/dcim_powerfeed.go), so a nil marshals to nothing;
// specFields.restoreEmpty has no empty form for a pointer, so field ownership never puts one
// back; payload.desired skips a spec key with no value; and netbox.Drift considers only fields
// present in desired. The behaviour is four existing engine properties lining up, which is why
// internal/reconciler/dcim_powerfeed_test.go pins each of them separately rather than only the
// end-to-end result.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxPowerFeed{}) }
