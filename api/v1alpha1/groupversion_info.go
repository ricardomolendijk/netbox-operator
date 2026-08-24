// Package v1alpha1 contains the netbox.kubeforge.org/v1alpha1 API.
//
// Every NetBox object is represented by one Kind in this group, and every NetBox
// foreign key by a reference to another object of this group. See
// docs/decisions/0001-api-group-and-kind-naming.md for why the group is not
// netbox.dev and why Kind names are prefixed NetBox.
//
// +kubebuilder:object:generate=true
// +groupName=netbox.kubeforge.org
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupName is the API group for every Kind in this package. It is deliberately
// declared exactly once: changing it is a one-line change here and a migration
// everywhere else.
const GroupName = "netbox.kubeforge.org"

var (
	// GroupVersion is the group and version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

	// SchemeBuilder registers the Go types in this package with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
