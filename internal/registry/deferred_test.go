package registry

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestRegisteredDescriptorRejectsAnUnconditionallyDeferredKey exercises NBO-015's identity
// guard against the descriptors the manager actually boots with, rather than against a
// fixture written next to the check it is meant to catch.
//
// The distinction is the whole reason for this test. registry_test.go's regionDescriptor()
// is a copy of dcim.Region maintained by hand; if the shipped descriptor's natural key or
// field map ever stopped agreeing with it, the guard would still pass there and would stop
// protecting the real kind. This reads the registry.
//
// What it protects: stripping a resolved `parent` from a create changes the object's natural
// key from `(parent, name)` to `(name)`, so the lookup that decided to create would have been
// asking a different question from the create it decided on -- and the follow-up PATCH would
// reparent whatever a `(name)`-only lookup adopted. DeferIfUnresolved exists for exactly this
// case and is asserted below.
func TestRegisteredDescriptorRejectsAnUnconditionallyDeferredKey(t *testing.T) {
	exercised := 0

	for _, d := range List() {
		for _, api := range keyFieldsWrittenAsReferences(d) {
			exercised++

			t.Run(d.GVK.Kind+"/"+api, func(t *testing.T) {
				candidate := d
				candidate.Deferred = []DeferredField{{APIField: api, Mode: DeferAlways}}

				if err := candidate.Validate(); !errors.Is(err, ErrDeferredNaturalKey) {
					t.Errorf("Validate() = %v, want %v: deferring %s would create %s under the wrong identity",
						err, ErrDeferredNaturalKey, api, d.GVK.Kind)
				}

				// The same field, conditionally, is legal and is the mode an MPTT kind has to
				// use: it is included in the create whenever it resolves, and when it does not
				// the engine has no applicable candidate and waits rather than writing.
				candidate.Deferred = []DeferredField{{APIField: api, Mode: DeferIfUnresolved}}

				if err := candidate.Validate(); err != nil {
					t.Errorf("Validate() = %v, want nil: %s may be deferred conditionally", err, api)
				}
			})
		}
	}

	// Without this the loop above passes on an empty registry, or on one where no shipped
	// kind happens to key on a reference -- which is the state the guard was written in and
	// the reason it had never actually fired.
	if exercised == 0 {
		t.Fatal("no registered descriptor keys on a reference, so the identity guard was not exercised")
	}
}

// keyFieldsWrittenAsReferences are the NetBox columns a natural-key candidate matches on that
// the field map writes as a reference.
//
// References only, because they are the only fields a deferral can name at all
// (ErrDeferredNotRef), and matched-on only: a candidate that pins a filter to null asserts the
// field is unset, which is precisely the state a create with the field stripped is in, so
// deferral cannot corrupt that identity.
func keyFieldsWrittenAsReferences(d Descriptor) []string {
	var out []string

	for _, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			// A foreign key is filtered as `parent_id` and written as `parent`.
			api := strings.TrimSuffix(field.Filter, "_id")

			for _, mapped := range d.Fields {
				if mapped.Class.Ref() && mapped.API == api && !slices.Contains(out, api) {
					out = append(out, api)
				}
			}
		}
	}

	return out
}
