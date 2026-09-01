// Package provenance is the operator's stamp on the NetBox objects it manages: one tag,
// and a small set of custom fields naming the cluster, the namespace and the CR.
//
// It is a package of its own rather than a few fields on the engine because the stamp is
// two problems, and only one of them is per-object. Writing it is trivial; making it
// writable is not -- NetBox answers a `custom_fields` key it has no `extras.CustomField`
// for with a 400, and a tag is written by id rather than by name, so both halves have to be
// provisioned against a live NetBox before any object reconciles. That provisioning is
// endpoint-shaped work (see Bootstrap), the stamp itself is object-shaped work (see
// Stamp.Apply), and keeping them in one package is what stops the two from disagreeing
// about which names they mean.
//
// Nothing here decides *whether* an object is managed. A stamp is a record, not a lock:
// see docs/operations/provenance.md for why stamping is not mandatory, and what
// NetBoxSweep (NBO-046) is therefore allowed to do with an unstamped object.
package provenance

import (
	"slices"
	"strings"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The two NetBox columns the stamp is written into.
//
// Spelled here as well as in internal/netbox because the two packages answer different
// questions about the same wire name and neither imports the other: internal/netbox owns
// how `custom_fields` is *compared*, this package owns what the operator *writes* into it.
// The strings are NetBox's, not a choice either package gets to make -- the same reason
// registry.Lookup and netbox.Lookup are spelled twice.
const (
	// TagsField is the M2M column every TagsMixin model carries. Written as a list of
	// NetBox ids and read back as a list of nested objects, which is why a kind that
	// declares Taggable needs `tags` compared as an M2M set rather than as a scalar.
	TagsField = "tags"

	// CustomFieldsField is the container every CustomFieldsMixin model carries. NetBox
	// merges a partial PATCH of it, so the operator sends only the keys it owns and leaves
	// everybody else's alone.
	CustomFieldsField = "custom_fields"
)

// Defaults for every configurable name. They are the values docs/reference/netboxendpoint.md
// documents and the CRD defaults with, and they exist in Go as well so that an endpoint
// stored before spec.managedBy existed -- or one built by hand in a test -- resolves to the
// same stamp the CRD would have produced.
const (
	DefaultTag                     = "k8s-managed"
	DefaultUIDField                = "k8s_uid"
	DefaultClusterField            = "k8s_cluster"
	DefaultOwnerField              = "k8s_owner"
	DefaultAllocationIdentityField = "k8s_allocation_identity"
)

// Config is spec.managedBy with every default applied: the resolved names, the cluster
// identifier, and whether the operator may create what is missing.
type Config struct {
	// ClusterID names the cluster. Empty means provenance is switched off entirely, and is
	// the only way to switch it off -- every other field has a working default, and the
	// cluster identifier deliberately does not (see ManagedBy.ClusterID).
	ClusterID string

	// Tag is the tag's name and slug.
	Tag string

	// UIDField, ClusterField, OwnerField are the custom fields the engine writes. An empty
	// name switches that one field off.
	UIDField     string
	ClusterField string
	OwnerField   string

	// AllocationIdentityField is bootstrapped and written by nothing in this milestone:
	// the value is the allocation engine's to compute (NBO-036,
	// docs/decisions/0005-gitops-coexistence.md section 3), and the definition has to
	// exist first or its first write is a 400.
	AllocationIdentityField string

	// Bootstrap permits creating a definition that does not exist.
	Bootstrap bool
}

// FromSpec resolves spec.managedBy into a Config. A nil spec yields the zero Config, whose
// ClusterID is empty and which therefore stamps nothing.
func FromSpec(spec *netboxv1alpha1.ManagedBy) Config {
	if spec == nil {
		return Config{}
	}

	return Config{
		ClusterID:               spec.ClusterID,
		Tag:                     orDefault(spec.Tag, DefaultTag),
		UIDField:                orDefault(spec.UIDField, DefaultUIDField),
		ClusterField:            orDefault(spec.ClusterField, DefaultClusterField),
		OwnerField:              orDefault(spec.OwnerField, DefaultOwnerField),
		AllocationIdentityField: orDefault(spec.AllocationIdentityField, DefaultAllocationIdentityField),
		// A nil pointer is an endpoint stored before the field existed, and the CRD
		// defaults it to true, so the two have to agree or an upgrade would silently stop
		// bootstrapping.
		Bootstrap: spec.Bootstrap == nil || *spec.Bootstrap,
	}
}

// Enabled reports whether this endpoint stamps anything at all.
func (c Config) Enabled() bool { return c.ClusterID != "" }

// CustomFieldNames are the custom-field definitions this config needs, deduplicated and
// sorted.
//
// Sorted because it drives the bootstrap's request order, the condition message and
// status.managedBy.customFields, all three of which are read by humans and compared in
// tests. Deduplicated because two names configured the same is a user's typo, not a reason
// to POST the same definition twice.
func (c Config) CustomFieldNames() []string {
	names := make([]string, 0, 4)

	for _, name := range []string{c.UIDField, c.ClusterField, c.OwnerField, c.AllocationIdentityField} {
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	return names
}

// The `app_label.model` strings of the two NetBox models the bootstrap writes. They match
// the Descriptor.ObjectType of NetBoxTag and NetBoxCustomField, which is what joins the two
// halves of the reservation: this package says which keys are reserved on which model, the
// descriptor says which CR spec field holds a key for that model, and neither has to know
// the other's Kind names.
const (
	tagObjectType         = "extras.tag"
	customFieldObjectType = "extras.customfield"
)

// Reserved are the lookup keys this endpoint's bootstrap owns, by the NetBox model they
// belong to.
//
// It exists because the operator is a writer of two NetBox models that also have CRDs, and
// two writers for one object is not something the engine can make safe. The bootstrap
// creates the tag and up to four custom fields before the endpoint reports Ready, derives
// `object_types` from the descriptor registry, and widens it whenever a kind is added
// (bootstrap.go). A CR claiming one of those objects would be the second writer of it, and
// the loser of that fight is not the CR -- it is every stamped object in the cluster:
// narrowing `object_types` on `k8s_uid` deletes the stored value from every object of the
// types removed (netbox/extras/signals.py, handle_cf_object_types_changed with post_remove),
// and deleting the definition deletes all of them.
//
// So the engine refuses such a CR outright (registry.Descriptor.ReservedKeySpec). This
// function is the list of what is refused, and it is per endpoint rather than a package
// constant for the reason the names are configurable at all: an endpoint that set
// `uidField: k8s_id` reserves `k8s_id` and leaves `k8s_uid` free, an endpoint that set
// `uidField: ""` bootstraps nothing under that name and reserves nothing, and an endpoint
// with no `spec.managedBy` at all reserves nothing whatsoever -- there is no second writer,
// so there is nothing to refuse.
//
// Nil when provenance is off, which callers read as "reserves nothing" without a special
// case: indexing a nil map yields the nil slice, and nothing is contained in that.
func (c Config) Reserved() map[string][]string {
	if !c.Enabled() {
		return nil
	}

	return map[string][]string{
		tagObjectType:         {c.Tag},
		customFieldObjectType: c.CustomFieldNames(),
	}
}

// Stamp is a Config the bootstrap has resolved against a live NetBox: the same names, plus
// the tag's id, which is the one part of the stamp that cannot be known from the spec.
//
// The zero value stamps nothing, so an endpoint whose bootstrap did not complete hands the
// engine a Stamp that is inert rather than one that is half-applied.
type Stamp struct {
	Config

	// TagID is the tag's NetBox id. A tag is written by id, so a stamp without one cannot
	// be applied.
	TagID int

	// Fields are the custom-field definitions that exist in NetBox, sorted. A configured
	// name absent from here was not created, and is not written: writing it would be the
	// 400 the bootstrap exists to prevent.
	Fields []string
}

// Applicable reports whether this stamp can be written.
func (s Stamp) Applicable() bool { return s.Enabled() && s.TagID != 0 }

// Owner identifies the CR behind one NetBox object.
type Owner struct {
	Kind      string
	Namespace string
	Name      string

	// UID is metadata.uid. It answers "is this the same object", which a name cannot: a CR
	// deleted and re-applied from the same manifest has the same name and a new UID.
	UID string
}

// Ref renders the owner as `<lowercased kind>/<namespace>/<name>`.
//
// The same spelling as the netbox.kubeforge.org/generated-by annotation in
// docs/decisions/0005-gitops-coexistence.md section 2, so that one string identifies a CR
// on the Kubernetes side and on the NetBox side.
func (o Owner) Ref() string {
	return strings.ToLower(o.Kind) + "/" + o.Namespace + "/" + o.Name
}

// Target says which of the two stamp columns a kind's NetBox model actually carries. It
// comes off the kind's registry.Descriptor, which is the only place per-kind facts live.
type Target struct {
	Taggable     bool
	CustomFields bool
}

// Stampable reports whether anything can be written onto this kind at all.
func (t Target) Stampable() bool { return t.Taggable || t.CustomFields }

// Apply writes the stamp into desired and reports what it wrote.
//
// It takes live as well as desired because `tags` is a full-replacement M2M: a PATCH
// carrying only the operator's tag would silently strip every tag a human applied in the
// UI. So the operator's tag is unioned into whatever is already on the object -- unless the
// spec declares `tags` itself, in which case the spec is authoritative and only the
// operator's own tag is added to it, or dropping a tag from the manifest could never remove
// it.
//
// The second result is false when nothing was written, which is the normal case for an
// endpoint with no spec.managedBy and for a kind whose model carries neither column.
func (s Stamp) Apply(desired, live netbox.Object, owner Owner, target Target) (netboxv1alpha1.ProvenanceStatus, bool) {
	if !s.Applicable() || !target.Stampable() || desired == nil {
		return netboxv1alpha1.ProvenanceStatus{}, false
	}

	applied := netboxv1alpha1.ProvenanceStatus{ClusterID: s.ClusterID}

	if target.Taggable {
		desired[TagsField] = mergeTags(desired[TagsField], live[TagsField], s.TagID)
		applied.Tag = s.Tag
	}

	if fields := s.customFields(owner); target.CustomFields && len(fields) > 0 {
		desired[CustomFieldsField] = mergeCustomFields(desired[CustomFieldsField], fields)
		applied.CustomFields = fields
	}

	return applied, applied.Tag != "" || len(applied.CustomFields) > 0
}

// customFields are the values to write, keyed by NetBox custom-field name.
//
// Only names the bootstrap confirmed exist, and only non-empty values: a UID is absent on
// an object built by hand in a test, and writing null into a field somebody may be filling
// by other means is not this operator's business. AllocationIdentityField is deliberately
// never written here -- see Config.
func (s Stamp) customFields(owner Owner) map[string]string {
	values := map[string]string{
		s.UIDField:     owner.UID,
		s.ClusterField: s.ClusterID,
		s.OwnerField:   owner.Ref(),
	}

	out := make(map[string]string, len(values))

	for name, value := range values {
		if name == "" || value == "" || !slices.Contains(s.Fields, name) {
			continue
		}
		out[name] = value
	}

	return out
}

// mergeTags returns the tag id list to send: the operator's tag added to what is already
// there, sorted and deduplicated.
//
// Sorted because the result is compared against NetBox's own list on the next pass. That
// comparison is order-independent, so the sort is not what makes it correct -- it is what
// makes a payload in a log line or a test failure readable, and it costs a list of at most
// a handful of ids nothing.
func mergeTags(fromSpec, fromLive any, id int) []any {
	existing := fromLive
	if fromSpec != nil {
		// The spec declares tags, so it owns the list. Anything on the live object that
		// the spec dropped is drift the engine is meant to correct.
		existing = fromSpec
	}

	ids := netbox.IDsOf(existing)
	if !slices.Contains(ids, id) {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	out := make([]any, 0, len(ids))
	for _, tagID := range slices.Compact(ids) {
		out = append(out, tagID)
	}

	return out
}

// mergeCustomFields overlays the operator's fields onto whatever the spec set.
//
// The spec's values are copied as they are, nil included: a nil is the user asking for that
// custom field's value to be removed (#196), and the stamp has no opinion about a key that
// is not one of its own. Where the spec and the stamp name the same key the stamp wins,
// because those keys are the operator's own record of what wrote this object.
//
// The result is a map[string]any rather than a map[string]string, and that is not
// cosmetic: netbox.Drift compares `custom_fields` by casting the desired value to
// map[string]any, and a map[string]string falls through to a whole-value string
// comparison that never matches -- a PATCH loop for the lifetime of the object.
func mergeCustomFields(fromSpec any, fields map[string]string) map[string]any {
	out := map[string]any{}

	if declared, ok := fromSpec.(map[string]any); ok {
		for name, value := range declared {
			out[name] = value
		}
	}

	for name, value := range fields {
		out[name] = value
	}

	return out
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
