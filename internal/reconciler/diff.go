package reconciler

import (
	"fmt"
	"strings"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// renderChanges renders a change set as "field: old → new", which is the form the
// populator's report.go used and the only form in which an Event about a PATCH is useful:
// "updated 3 fields" tells nobody what happened to their data.
func renderChanges(changes []netbox.Change) string {
	rendered := make([]string, 0, len(changes))

	for _, change := range changes {
		rendered = append(rendered,
			fmt.Sprintf("%s: %s → %s", change.Field, renderValue(change.Old), renderValue(change.New)))
	}

	return strings.Join(rendered, ", ")
}

// renderValue reduces a NetBox value to what a human needs to see.
//
// A foreign key reads back as a whole nested object and a choice field as a
// {value,label} pair, so printing them raw buries the one interesting field in a JSON dump
// -- in an Event message, which is truncated. Drift compares the same two shapes the same
// way (internal/netbox/drift.go, unwrapNested); this is the display half of it.
func renderValue(value any) string {
	nested, ok := value.(map[string]any)
	if !ok {
		if value == nil {
			return "unset"
		}

		return fmt.Sprint(value)
	}

	for _, key := range []string{"id", "value"} {
		if inner, ok := nested[key]; ok {
			return fmt.Sprint(inner)
		}
	}

	return fmt.Sprint(nested)
}
