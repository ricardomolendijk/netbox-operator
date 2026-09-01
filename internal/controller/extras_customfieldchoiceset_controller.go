package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfieldchoicesets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfieldchoicesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfieldchoicesets/finalizers,verbs=update

// The choice-set half of the pair. Its own file rather than a second init() in the custom
// field's, so that adding a kind stays "three new files" and a diff shows one kind per file
// (CONTRIBUTING.md, "Extensibility").
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCustomFieldChoiceSet{}) }
