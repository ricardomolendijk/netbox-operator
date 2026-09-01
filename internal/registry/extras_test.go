package registry

import (
	"errors"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestExtrasDescriptorsValidate is the boot check, run as a test: every descriptor NBO-059
// adds has to pass registry validation, because a malformed one fails the whole manager's boot
// rather than one reconcile.
//
// registry.Validate() over the package-level registry already covers this
// (TestRegistryValidateReportsBadDescriptor), and this exists anyway because a table naming the
// endpoint and the object type is the thing a reviewer compares against
// docs/netbox-schema.md's endpoint map -- where `extras/custom-field-choice-sets` is not the
// pluralisation of anything and a wrong endpoint is a 404 on every object of the kind.
func TestExtrasDescriptorsValidate(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxCustomField", "extras/custom-fields", "extras.customfield"},
		{"NetBoxCustomFieldChoiceSet", "extras/custom-field-choice-sets", "extras.customfieldchoiceset"},
		{"NetBoxCustomLink", "extras/custom-links", "extras.customlink"},
		{"NetBoxExportTemplate", "extras/export-templates", "extras.exporttemplate"},
		{"NetBoxSavedFilter", "extras/saved-filters", "extras.savedfilter"},
		{"NetBoxConfigTemplate", "extras/config-templates", "extras.configtemplate"},
		{"NetBoxConfigContextProfile", "extras/config-context-profiles", "extras.configcontextprofile"},
		{"NetBoxConfigContext", "extras/config-contexts", "extras.configcontext"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d, ok := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))
			if !ok {
				t.Fatalf("no descriptor is registered for %s", tc.kind)
			}

			if err := d.Validate(); err != nil {
				t.Errorf("Validate() = %v", err)
			}

			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)", d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}
		})
	}
}

// TestCustomFieldDeclaresItsCollision is NBO-059's core assertion, at the level the decision
// was actually made: on the descriptor.
//
// Both flags are declarations that something the engine does *not* do by default has to happen
// for this kind, and both fail silently in the dangerous direction if they are dropped -- a
// missing ReservedKeySpec makes a CR the second writer of `k8s_uid`, and a missing
// DataLossOnDelete makes deleting the CR strip that field's value off every object in NetBox.
// Neither shows up as a failure anywhere else.
func TestCustomFieldDeclaresItsCollision(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCustomField"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxCustomField")
	}

	if d.ReservedKeySpec != "name" {
		t.Errorf("ReservedKeySpec = %q, want \"name\": the provenance bootstrap looks its own "+
			"definitions up by name, so that is the spec field a CR could collide on",
			d.ReservedKeySpec)
	}

	if !d.DataLossOnDelete {
		t.Error("DataLossOnDelete = false: deleting an extras.CustomField strips its stored value " +
			"from every object in netbox that has one, and netbox does not refuse the delete")
	}

	// Not custom-fieldable, which is what keeps this kind out of the object_types list the
	// bootstrap derives -- otherwise `k8s_uid` would be widened to cover the very objects this
	// Kind manages.
	if d.CustomFieldable || d.Taggable {
		t.Errorf("Taggable/CustomFieldable = %v/%v, want both false: extras.CustomField's bases are "+
			"CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel", d.Taggable, d.CustomFieldable)
	}
}

// TestTagDeclaresItsCollision covers the half of the collision that shipped first.
//
// The provenance bootstrap has created `spec.managedBy.tag` since NBO-004, and NetBoxTag has
// existed since NBO-008 -- so for the whole of that time a CR could claim the tag NetBoxSweep
// keys on, and deleting the CR would take the tag off every object the operator manages. Fixing
// it needed nothing new once NBO-059's mechanism existed: one line of data.
func TestTagDeclaresItsCollision(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxTag"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxTag")
	}

	if d.ReservedKeySpec != "slug" {
		t.Errorf("ReservedKeySpec = %q, want \"slug\": the bootstrap looks the tag up by slug",
			d.ReservedKeySpec)
	}

	// Deleting a tag loses the tag assignments and nothing else -- no column on another object
	// is destroyed -- so this is not the data-loss case and must not claim to be.
	if d.DataLossOnDelete {
		t.Error("DataLossOnDelete = true on NetBoxTag: deleting a tag destroys no column on any " +
			"other object, and blocking a deletion that is safe trains people to set the annotation")
	}
}

// TestReservedKeySpecMustNameASpecField is the boot check behind those two declarations.
//
// The value is read out of the decoded spec by JSON name (reconciler.pass.reserved), so a
// misspelled one finds nothing, compares nothing and reserves nothing -- a guard that silently
// does not guard. Which is exactly the failure mode ErrUnknownSpecField exists for on natural
// keys and on the containment ref.
func TestReservedKeySpecMustNameASpecField(t *testing.T) {
	d := extrasCustomFieldDescriptor()
	d.ReservedKeySpec = "Name"

	err := d.Validate()
	if !errors.Is(err, ErrUnknownSpecField) {
		t.Errorf("Validate() = %v, want ErrUnknownSpecField", err)
	}
}

// TestSavedFilterFallsBackFromSlugToName pins the ordering of the two candidates, because the
// order is the behaviour.
//
// Both columns are unique and both are required, so both candidates are always applicable and
// the first one always answers. The second is not dead: it is what makes editing `slug` in Git
// a rename rather than a failed create. Reverse them and editing `name` becomes the rename and
// editing `slug` becomes a create NetBox refuses on the unique `name` -- so this is worth
// pinning rather than leaving to whoever next reads the file.
func TestSavedFilterFallsBackFromSlugToName(t *testing.T) {
	d, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxSavedFilter"))
	if !ok {
		t.Fatal("no descriptor is registered for NetBoxSavedFilter")
	}

	filters := make([]string, 0, len(d.NaturalKeys))
	for _, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			filters = append(filters, field.Filter)
		}
	}

	if want := []string{"slug", "name"}; !slices.Equal(filters, want) {
		t.Errorf("natural-key filters = %v, want %v", filters, want)
	}
}

// TestExtrasJSONColumnsAreClassJSON is the regression guard for the class NBO-059 added.
//
// Left at ClassValue a JSONField is compared with the scalar rule, which unwraps any object
// carrying an `id` or a `value` key -- so `parameters: {"id": ["3"]}`, an ordinary saved filter,
// would differ from itself forever and be PATCHed on every reconcile. Nothing about that failure
// is visible in a diff of the descriptor, which is why it is asserted from the outside: these
// are the columns docs/netbox-schema.md marks JSONField on the kinds this ticket added.
func TestExtrasJSONColumnsAreClassJSON(t *testing.T) {
	for kind, columns := range map[string][]string{
		"NetBoxCustomField":          {"default", "related_object_filter", "validation_schema"},
		"NetBoxCustomFieldChoiceSet": {"choice_colors"},
		"NetBoxSavedFilter":          {"parameters"},
		"NetBoxExportTemplate":       {"environment_params"},
		"NetBoxConfigTemplate":       {"environment_params"},
		"NetBoxConfigContext":        {"data"},
		"NetBoxConfigContextProfile": {"schema"},
	} {
		t.Run(kind, func(t *testing.T) {
			d, ok := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
			if !ok {
				t.Fatalf("no descriptor is registered for %s", kind)
			}

			declared := d.JSONFields()
			for _, column := range columns {
				if !slices.Contains(declared, column) {
					t.Errorf("%s is a JSONField and is not ClassJSON; JSONFields() = %v", column, declared)
				}
			}
		})
	}
}
