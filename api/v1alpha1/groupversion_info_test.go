package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// TestGroupName pins the API group string. It is in the apiVersion of every manifest a
// user ever writes, so changing it is a breaking change and should require editing a
// test that says so. See docs/decisions/0001-api-group-and-kind-naming.md.
func TestGroupName(t *testing.T) {
	const want = "netbox.kubeforge.org"
	if GroupName != want {
		t.Errorf("GroupName = %q, want %q -- this is a breaking API change", GroupName, want)
	}
	if GroupVersion.Group != want {
		t.Errorf("GroupVersion.Group = %q, want %q", GroupVersion.Group, want)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("GroupVersion.Version = %q, want %q", GroupVersion.Version, "v1alpha1")
	}
}

// TestAddToScheme checks that the SchemeBuilder is wired up. It deliberately does not
// assert IsVersionRegistered: a scheme reports a group version as registered only once a
// type is registered in it, and no Kind exists yet. Each Kind asserts its own
// registration in its own test, starting with NetBoxTag.
func TestAddToScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	if got := SchemeBuilder.GroupVersion; got != GroupVersion {
		t.Errorf("SchemeBuilder.GroupVersion = %v, want %v", got, GroupVersion)
	}
}
