package provenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// NetBox endpoints the bootstrap talks to. Looked up rather than derived, exactly as
// registry.Descriptor.Endpoint is: `extras/custom-fields` is not the pluralisation of
// anything (docs/netbox-schema.md, endpoint map).
const (
	tagsEndpoint         = "extras/tags"
	customFieldsEndpoint = "extras/custom-fields"
)

// customFieldType is the extras.CustomField `type` every definition is created with.
//
// Text for all of them, including the UID, which is a hyphenated hex string rather than a
// number. NetBox's other types buy validation the operator does not want here: an integer
// field would reject a UID, and a selection field would need a CustomFieldChoiceSet
// enumerating every cluster name in advance.
const customFieldType = "text"

// customFieldFilterLogic is the `filter_logic` every definition is created with.
//
// `exact`, not NetBox's own default of FILTER_LOOSE (extras.CustomField.filter_logic,
// docs/netbox-schema.md -> extras.CustomField). Loose filtering makes `?cf_<name>=<value>` a
// substring match, and every one of these fields is an identity: a claim searching for its
// own allocation identity, or NBO-036's successor searching by `k8s_uid`, is asking "which
// object *is* this one" and a substring answer to that question is a different object.
//
// Only applied to definitions this operator creates. An existing definition is never
// narrowed, for the same reason widen() only ever adds object types: the definition may be
// shared with a NetBox admin's own use of it, and changing somebody else's schema is not a
// thing an operator should do on a resync.
const customFieldFilterLogic = "exact"

// customFieldGroup groups the operator's definitions in NetBox's UI, so a user reading an
// object sees them together and under a name that says where they came from.
const customFieldGroup = "Kubernetes"

// ErrIncomplete is returned when a definition the stamp needs does not exist and the
// bootstrap was not permitted to create it.
//
// A sentinel rather than a message, so the endpoint controller classifies it by type and
// reports "you turned this off and here is what to create" separately from "NetBox refused"
// -- two states with different fixes (CONTRIBUTING.md: classified by type, never by string
// matching).
var ErrIncomplete = errors.New("a provenance definition is missing and bootstrap is disabled")

// Client is the part of the NetBox API the bootstrap needs.
//
// Consumer-defined and three methods, per CONTRIBUTING.md. Deliberately not
// reconciler.NetBoxClient: this never fetches by id and must never delete, and an interface
// that could delete a CustomField is a much worse thing to hand a bootstrap than a narrow
// one is to define.
type Client interface {
	List(ctx context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error)
	Create(ctx context.Context, endpoint string, payload netbox.Object) (netbox.Object, error)
	Patch(ctx context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error)
}

// Result is what one bootstrap pass concluded.
type Result struct {
	// Stamp is what the engine may write. Inert unless everything it needs exists.
	Stamp Stamp

	// Created names the definitions this pass created, for the log line and the Event.
	// Empty on every pass after the first, which is what "re-running changes nothing"
	// looks like from the outside.
	Created []string

	// Widened names the custom fields whose object_types this pass extended, because a
	// kind was registered after the definition was created.
	Widened []string

	// Missing names the definitions that do not exist and were not created.
	Missing []string

	// Suppressed reports that this endpoint cannot write, so nothing missing could be
	// created. It is not a failure: an endpoint that sends nothing cannot produce the 400
	// that the whole bootstrap exists to prevent.
	Suppressed bool
}

// ObjectTypes are the `app_label.model` strings the operator stamps custom fields onto,
// derived from the descriptor registry and sorted.
//
// Derived rather than listed because extras.CustomField.object_types is required
// (docs/netbox-schema.md -> extras.CustomField, `object_types ManyToManyField REQ`) and a
// definition declared for the wrong set makes every write to a type outside it a 400. A
// hand-maintained list would be correct exactly until the next kind lands, and the failure
// would surface as a hundred identical 400s on that kind rather than as a missing entry
// anybody could see.
//
// extra is for the types no Descriptor supplies: a claim kind allocates `ipam.ipaddress`
// while being a Kubernetes object with no NetBox counterpart of its own, so the model it
// writes into has to reach this list some other way (registry.ClaimObjectTypes). Variadic
// rather than a second parameter so that a caller with nothing to add is unchanged, and
// deduplicated against the derived set so a claim whose allocated Kind *is* registered adds
// nothing.
func ObjectTypes(descriptors []registry.Descriptor, extra ...string) []string {
	types := make([]string, 0, len(descriptors)+len(extra))

	for _, d := range descriptors {
		if d.CustomFieldable && !slices.Contains(types, d.ObjectType) {
			types = append(types, d.ObjectType)
		}
	}

	for _, objectType := range extra {
		if objectType != "" && !slices.Contains(types, objectType) {
			types = append(types, objectType)
		}
	}

	slices.Sort(types)

	return types
}

// Bootstrap makes the tag and the custom-field definitions cfg asks for exist, and resolves
// the tag's id.
//
// Idempotent by construction: every step looks the definition up first and acts only on
// what is absent, so a second call against the same NetBox issues the same reads and no
// writes. It returns an error only for NetBox refusing something -- a missing definition it
// was not allowed to create is ErrIncomplete, and an endpoint that cannot write at all is a
// Result, not a failure.
func Bootstrap(ctx context.Context, client Client, cfg Config, objectTypes []string) (Result, error) {
	if !cfg.Enabled() {
		return Result{}, nil
	}

	result := Result{}

	tagID, err := bootstrapTag(ctx, client, cfg, &result)
	if err != nil {
		return Result{}, err
	}

	fields, err := bootstrapFields(ctx, client, cfg, objectTypes, &result)
	if err != nil {
		return Result{}, err
	}

	if len(result.Missing) > 0 {
		if result.Suppressed {
			// Nothing could be created because nothing can be written. The endpoint is
			// still usable -- it writes nothing either -- so this is reported and not
			// returned as a failure.
			return result, nil
		}

		return result, fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(result.Missing, ", "))
	}

	result.Stamp = Stamp{Config: cfg, TagID: tagID, Fields: fields}

	return result, nil
}

// bootstrapTag resolves the tag's id, creating the tag when it is absent.
//
// The tag's own object_types is left unset on purpose. It is the column that would restrict
// which NetBox models the tag may be applied to, and restricting it to the kinds registered
// today would make the first object of a kind added tomorrow unstampable against a NetBox
// nobody thought to widen (docs/netbox-schema.md -> extras.Tag, object_types is optional).
func bootstrapTag(ctx context.Context, client Client, cfg Config, result *Result) (int, error) {
	found, err := lookup(ctx, client, tagsEndpoint, netbox.Params{}.Match("slug", netbox.LookupExact, cfg.Tag))
	if err != nil {
		return 0, err
	}

	if found != nil {
		id, ok := found.ID()
		if !ok {
			return 0, fmt.Errorf("netbox tag %q came back without an id", cfg.Tag)
		}

		return id, nil
	}

	if !cfg.Bootstrap {
		result.Missing = append(result.Missing, "tag "+cfg.Tag)

		return 0, nil
	}

	created, err := client.Create(ctx, tagsEndpoint, netbox.Object{
		// Name and slug are the same string: spec.managedBy.tag is constrained to the slug
		// alphabet precisely so that one value can be both, and a derived-but-different
		// name is a second thing to search NetBox by.
		"name": cfg.Tag,
		"slug": cfg.Tag,
		"description": "Managed by the netbox-operator. Objects carrying this tag are " +
			"reconciled from Kubernetes; see docs/operations/provenance.md.",
	})
	if err != nil {
		return 0, fmt.Errorf("creating the netbox tag %q: %w", cfg.Tag, err)
	}

	if netbox.Suppressed(created) {
		result.Suppressed = true
		result.Missing = append(result.Missing, "tag "+cfg.Tag)

		return 0, nil
	}

	id, ok := created.ID()
	if !ok {
		return 0, fmt.Errorf("netbox returned no id for the created tag %q", cfg.Tag)
	}
	result.Created = append(result.Created, "tag "+cfg.Tag)

	return id, nil
}

// bootstrapFields makes every configured custom-field definition exist, and returns the
// names that do.
//
// With no object type to declare there is nothing to create: extras.CustomField.object_types
// is required, so a definition cannot be made at all until some registered kind carries
// custom fields. That is a coherent state rather than an error -- the tag still works, and
// Stamp.Apply writes no custom fields to a kind whose Target says it has none.
func bootstrapFields(ctx context.Context, client Client, cfg Config, objectTypes []string, result *Result) ([]string, error) {
	names := cfg.CustomFieldNames()
	if len(names) == 0 || len(objectTypes) == 0 {
		return nil, nil
	}

	present := make([]string, 0, len(names))

	for _, name := range names {
		exists, err := bootstrapField(ctx, client, cfg, name, objectTypes, result)
		if err != nil {
			return nil, err
		}
		if exists {
			present = append(present, name)
		}
	}

	return present, nil
}

// bootstrapField makes one definition exist, and widens an existing one that does not cover
// every object type the operator now stamps.
func bootstrapField(ctx context.Context, client Client, cfg Config, name string,
	objectTypes []string, result *Result,
) (bool, error) {
	found, err := lookup(ctx, client, customFieldsEndpoint,
		netbox.Params{}.Match("name", netbox.LookupExact, name))
	if err != nil {
		return false, err
	}

	if found != nil {
		return true, widen(ctx, client, name, found, objectTypes, result)
	}

	if !cfg.Bootstrap {
		result.Missing = append(result.Missing, "custom field "+name)

		return false, nil
	}

	created, err := client.Create(ctx, customFieldsEndpoint, netbox.Object{
		"object_types": objectTypes,
		"type":         customFieldType,
		"filter_logic": customFieldFilterLogic,
		"name":         name,
		"label":        label(name),
		"group_name":   customFieldGroup,
		"description":  "Written by the netbox-operator; see docs/operations/provenance.md.",
	})
	if err != nil {
		return false, fmt.Errorf("creating the netbox custom field %q: %w", name, err)
	}

	if netbox.Suppressed(created) {
		result.Suppressed = true
		result.Missing = append(result.Missing, "custom field "+name)

		return false, nil
	}

	result.Created = append(result.Created, "custom field "+name)

	return true, nil
}

// widen adds the object types this definition does not yet cover.
//
// This is what happens when a kind is registered after the definition was created: a new
// operator version knows about a type the CustomField in NetBox does not, and the first
// write to that type would be a 400 naming a custom field the user can see perfectly well
// in the UI. Types are only ever added -- never removed -- because the definition may be
// shared with a NetBox admin's own use of it, and narrowing somebody else's schema is not
// a thing an operator should do on a resync.
func widen(ctx context.Context, client Client, name string, found netbox.Object,
	objectTypes []string, result *Result,
) error {
	id, ok := found.ID()
	if !ok {
		return fmt.Errorf("netbox custom field %q came back without an id", name)
	}

	covered := netbox.ObjectTypesOf(found["object_types"])

	union := slices.Clone(covered)
	for _, objectType := range objectTypes {
		if !slices.Contains(union, objectType) {
			union = append(union, objectType)
		}
	}

	if len(union) == len(covered) {
		return nil
	}

	slices.Sort(union)

	patched, err := client.Patch(ctx, customFieldsEndpoint, id, netbox.Object{"object_types": union})
	if err != nil {
		return fmt.Errorf("widening the netbox custom field %q to %v: %w", name, union, err)
	}

	if netbox.Suppressed(patched) {
		// A non-writing endpoint. The definition is still usable for the types it does
		// cover, so this is reported rather than claimed as done -- and reported as
		// suppressed rather than as widened, because nothing was sent.
		result.Suppressed = true
		result.Missing = append(result.Missing, "object types on custom field "+name)

		return nil
	}

	result.Widened = append(result.Widened, name)

	return nil
}

// lookup returns the single object matching params, or nil.
//
// It lists rather than calling GetOne so that more than one match is a named failure
// instead of an *AmbiguousError the caller would have to translate: `name` is unique on
// extras.CustomField and `slug` on extras.Tag (docs/netbox-schema.md), so two matches means
// NetBox is not the NetBox this filter was written for.
func lookup(ctx context.Context, client Client, endpoint string, params netbox.Params) (netbox.Object, error) {
	matches, err := client.List(ctx, endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("looking up netbox %s by %v: %w", endpoint, params, err)
	}

	if len(matches) > 1 {
		return nil, fmt.Errorf("netbox %s has %d objects matching %v, on a column it declares unique",
			endpoint, len(matches), params)
	}

	if len(matches) == 0 {
		return nil, nil
	}

	return matches[0], nil
}

// label renders a custom-field name as the label NetBox shows in its UI: `k8s_uid` becomes
// `K8s uid`. Cosmetic, and cheaper than a second configurable field per custom field.
func label(name string) string {
	words := strings.ReplaceAll(name, "_", " ")
	if words == "" {
		return words
	}

	return strings.ToUpper(words[:1]) + words[1:]
}
