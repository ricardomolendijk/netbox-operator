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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the API group for every Kind in this package. It is deliberately
// declared exactly once: changing it is a one-line change here and a migration
// everywhere else.
const GroupName = "netbox.kubeforge.org"

var (
	// GroupVersion is the group and version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

	// SchemeBuilder registers the Go types in this package with a scheme.
	SchemeBuilder = &schemeBuilder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// schemeBuilder collects the Go types of this API group, so that AddToScheme can register
// all of them in one call.
//
// It stands in for controller-runtime's scheme.Builder, which is deprecated on the
// grounds that an api package should be cheap to import and so should depend on nothing
// beyond the standard library, apimachinery and other api packages -- the same layering
// this repository already holds itself to. It is not a like-for-like copy of the upstream
// helper: only Register is kept, because RegisterAll and Build had no caller here, and
// Register no longer returns the builder, because nothing chained off it.
//
// +kubebuilder:object:generate=false
type schemeBuilder struct {
	// GroupVersion is the group and version every registered type is registered under.
	GroupVersion schema.GroupVersion

	runtime.SchemeBuilder
}

// Register adds one or more objects to the builder, to be registered with a scheme when
// AddToScheme is called. It mutates the builder, and is called from the init of the file
// that declares each Kind.
func (b *schemeBuilder) Register(objects ...runtime.Object) {
	b.SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(b.GroupVersion, objects...)
		metav1.AddToGroupVersion(s, b.GroupVersion)

		return nil
	})
}
