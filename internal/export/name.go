package export

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// maxNameLength is what a CR name and an ObjectRef.Name may hold: a DNS subdomain
// (api/v1alpha1/objectref.go, MaxLength=253).
const maxNameLength = 253

// suffixLength is the hex digest appended when a name is truncated or collides.
const suffixLength = 8

// assignNames gives every object a CR name, deterministically.
//
// A NetBox name is free text and a CR name is a DNS subdomain, so the two cannot simply
// be the same string: `Home Lab / Rack 3` and `10.0.20.0/24` are both legal in NetBox and
// neither is a legal object name. The name is derived from the object's slug where it has
// one, its name where it has one, and its natural key otherwise -- in that order, because
// a slug is already the identifier NetBox itself uses in a URL.
//
// Collisions are made explicit rather than silently overwritten. Two NetBox objects can
// legitimately reduce to one CR name -- the same CIDR in two VRFs, the same VLAN name in
// two sites -- and one file with two identical metadata.name entries is a manifest whose
// second `kubectl apply` deletes the first object's spec. So objects are walked in NetBox
// id order, the first keeps the plain name, and every later one takes a hash suffix
// derived from its object type and id. Both are stable across runs, and every collision is
// reported.
func assignNames(objects []object, result *Result) {
	slices.SortFunc(objects, func(a, b object) int { return a.id - b.id })

	taken := map[string]bool{}
	for i := range objects {
		base := truncate(baseName(objects[i]), objects[i])
		name := base
		if taken[name] {
			name = suffixed(base, objects[i])
			result.Notes = append(result.Notes, fmt.Sprintf(
				"%s %q collides on name %q with another object; exported as %q",
				objects[i].desc.GVK.Kind, display(objects[i].raw), base, name))
		}
		taken[name] = true
		objects[i].name = name
	}
}

// nameIndex maps an object type and NetBox id to the CR name that now describes it. It is
// what turns a NetBox foreign key into a reference by name without a second fetch: every
// exported object was listed once in the index pass, so the answer is already in memory.
func nameIndex(objects []object) map[string]string {
	out := make(map[string]string, len(objects))
	for _, obj := range objects {
		out[indexKey(obj.desc.ObjectType, obj.id)] = obj.name
	}

	return out
}

func indexKey(objectType string, id int) string {
	return fmt.Sprintf("%s/%d", objectType, id)
}

// baseName is the name to derive from, before sanitising.
//
// Read off the field map rather than decided per kind: a descriptor that maps a spec field
// called `slug` or `name` has that column, and one that maps neither -- ipam.Prefix -- is
// identified by its natural key instead. Reference-valued key fields are skipped, because
// their value is an id whose own CR name is not assigned yet and which would not be
// readable anyway.
func baseName(obj object) string {
	for _, spec := range []string{"slug", "name"} {
		field, ok := obj.desc.FieldFor(spec)
		if !ok {
			continue
		}
		if value := sanitise(fmt.Sprint(obj.raw[field.API])); value != "" {
			return value
		}
	}

	return sanitise(naturalKeyName(obj))
}

// naturalKeyName joins the scalar halves of the first natural-key candidate. That is the
// identity NetBox itself uses for the object, so a prefix becomes `10-0-20-0-24` and a
// VLAN with no name becomes its vid.
func naturalKeyName(obj object) string {
	if len(obj.desc.NaturalKeys) == 0 {
		return ""
	}

	parts := make([]string, 0, len(obj.desc.NaturalKeys[0].Fields))
	for _, key := range obj.desc.NaturalKeys[0].Fields {
		field, ok := obj.desc.FieldFor(key.Spec)
		if !ok || field.Class.Ref() {
			continue
		}
		if value := fmt.Sprint(netbox.Unwrap(obj.raw[field.API])); value != "" {
			parts = append(parts, value)
		}
	}

	return strings.Join(parts, "-")
}

// sanitise reduces free text to the DNS subdomain a CR name has to be: lowercase, dots
// separating labels, every label starting and ending alphanumeric
// (api/v1alpha1/objectref.go's Name pattern, which the same string has to satisfy when it
// is referenced).
func sanitise(in string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(in)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			out.WriteRune(r)
		default:
			out.WriteByte('-')
		}
	}

	labels := make([]string, 0, 4)
	for _, label := range strings.Split(out.String(), ".") {
		if trimmed := strings.Trim(collapse(label), "-"); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}

	return strings.Join(labels, ".")
}

// collapse squeezes runs of `-` so that `Home  Lab / Rack` does not become
// `home--lab---rack`.
func collapse(in string) string {
	for strings.Contains(in, "--") {
		in = strings.ReplaceAll(in, "--", "-")
	}

	return in
}

// truncate keeps a name inside the API's length limit, and keeps it unique while doing so
// by ending it in a digest of the object it names. A plain cut would map every object
// sharing a long prefix onto one name.
func truncate(base string, obj object) string {
	if base == "" {
		return suffixed(strings.ToLower(obj.desc.GVK.Kind), obj)
	}
	if len(base) <= maxNameLength {
		return base
	}

	return suffixed(base[:maxNameLength-suffixLength-1], obj)
}

// suffixed appends a digest of the object's NetBox identity. Derived from the object type
// and id rather than from a counter, so the same NetBox always produces the same name.
func suffixed(base string, obj object) string {
	sum := sha256.Sum256([]byte(indexKey(obj.desc.ObjectType, obj.id)))
	digest := hex.EncodeToString(sum[:])[:suffixLength]

	return strings.Trim(base, "-.") + "-" + digest
}

// display is how an object is named in a message to a human: NetBox's own `display`
// column, which every object has.
func display(raw netbox.Object) string {
	if value, ok := raw["display"].(string); ok && value != "" {
		return value
	}

	return fmt.Sprint(raw["id"])
}

// provenanceFields are the custom fields the operator writes about itself, resolved
// through internal/provenance so this list cannot drift from the one the engine stamps.
// None of them is desired state and one of them -- the allocation identity -- is a
// claim's private bookkeeping, so exporting it into Git would pin one allocation into a
// manifest and then have the operator argue with it
// (docs/decisions/0005-gitops-coexistence.md section 3).
func provenanceFields() []string {
	return provenance.FromSpec(&netboxv1alpha1.ManagedBy{}).CustomFieldNames()
}

// managed reports whether the operator already owns this object.
//
// Three signals, because any one of them alone can be switched off on an endpoint: the
// `k8s_uid` custom field, the provenance tag, and being the provenance tag. An object
// carrying any of them is already described by something in the cluster, so exporting it
// would produce a second writer for one NetBox object -- which is a Conflict, not a backup.
func managed(desc registry.Descriptor, raw netbox.Object, tag string) bool {
	if isProvenanceTag(desc, raw, tag) {
		return true
	}

	if fields, ok := raw[provenance.CustomFieldsField].(map[string]any); ok {
		for _, name := range provenanceFields() {
			if value, present := fields[name]; present && value != nil && value != "" {
				return true
			}
		}
	}

	if tag == "" {
		return false
	}

	return slices.Contains(tagSlugs(raw), tag)
}

// isProvenanceTag reports whether this object *is* the operator's own tag.
//
// internal/provenance's bootstrap creates and maintains it (bootstrap.go), so a NetBoxTag
// CR describing it would be a second writer for the operator's own bookkeeping -- and
// deleting that CR would delete the tag every stamped object depends on. The Kind is taken
// from the typed ref alias rather than compared as a string, so it stays whatever
// api/v1alpha1 says a tag is.
func isProvenanceTag(desc registry.Descriptor, raw netbox.Object, tag string) bool {
	if tag == "" || desc.GVK != (netboxv1alpha1.TagRef{}).TargetGVK() {
		return false
	}

	slug, _ := raw["slug"].(string)

	return slug == tag
}

// tagSlugs are the slugs of an object's tags, as NetBox returns them: a list of nested
// objects.
func tagSlugs(raw netbox.Object) []string {
	list, ok := raw[provenance.TagsField].([]any)
	if !ok {
		return nil
	}

	slugs := make([]string, 0, len(list))
	for _, item := range list {
		nested, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if slug, ok := nested["slug"].(string); ok {
			slugs = append(slugs, slug)
		}
	}

	return slugs
}

// scopeMemberFor is the reverse of the generic-FK dispatch table: given the
// `app_label.model` string in a `*_type` column, the CR spec field that selects it.
//
// registry.ByObjectType is the index that makes this possible without a switch -- it
// answers "which Kind is `dcim.location`", and the union member pointing at that Kind is
// the field to emit. Reading the cached `_site` column instead would report a
// Location-scoped object as Site-scoped, which is the bug the scope union exists to make
// unrepresentable (docs/concepts/generic-refs.md).
func scopeMemberFor(generic registry.GenericFKSpec, objectType string) (registry.GenericFKMember, bool) {
	target, ok := registry.ByObjectType(objectType)
	if !ok {
		return registry.GenericFKMember{}, false
	}

	for _, member := range generic.Members {
		if member.Target == target.GVK {
			return member, true
		}
	}

	return registry.GenericFKMember{}, false
}
