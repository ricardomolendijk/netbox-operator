package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxracks: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracks/finalizers,verbs=update

// NetBoxRack's controller is one line, and it is the case NBO-051 was expected to need code
// for: a rack is looked up by `(location, name)` when it names a location and by `(site, name)`
// with `location_id` pinned null when it does not, and an ambiguous match is a Conflict rather
// than an adoption. All three of those are natural-key candidates on its Descriptor
// (internal/registry/dcim_rack.go) -- NaturalKey.Applicable is what selects between the first
// two, from whether `locationRef` was declared -- and the ambiguity verdict is the engine's,
// shared with every other kind whose identity NetBox does not enforce.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRack{}) }
