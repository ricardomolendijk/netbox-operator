// Package registry is the only place per-kind NetBox facts live.
//
// One generic engine drives ~120 kinds, so everything that differs between kinds is data
// in a Descriptor rather than a branch in the engine: adding a kind is a new file
// registering a Descriptor, and never an edit to internal/reconciler.
//
// Nothing in a Descriptor is a func. A closure cannot be emitted by a template, printed
// in a diff, serialised or linted, so a Descriptor carrying one would put per-kind logic
// back into shared code through the back door — and the M7 generator could not produce it
// at all (NBO-069).
package registry

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Descriptor validation failures. Each is a distinct sentinel so callers and tests
// classify by type rather than by matching a message.
var (
	// ErrDuplicateGVK is returned when a GroupVersionKind is registered twice.
	ErrDuplicateGVK = errors.New("duplicate registration for GroupVersionKind")

	// ErrEmptyGVK is returned for a descriptor with no GroupVersionKind, which would
	// otherwise register itself under the zero key.
	ErrEmptyGVK = errors.New("empty GroupVersionKind")

	// ErrNoEndpoint is returned for a descriptor with no NetBox endpoint.
	ErrNoEndpoint = errors.New("empty endpoint")

	// ErrInvalidObjectType is returned for an object type that is not a Django
	// `app_label.model` string.
	ErrInvalidObjectType = errors.New("object type is not an app_label.model string")

	// ErrUnknownScope is returned for a resource scope that is not a CRD scope.
	ErrUnknownScope = errors.New("unknown resource scope")

	// ErrNoNaturalKey is returned for a descriptor with no lookup candidate, which the
	// engine cannot adopt with and would duplicate on every fresh cluster.
	ErrNoNaturalKey = errors.New("no natural key")

	// ErrDeferredReadOnly is returned for a field that is both deferred and read-only.
	ErrDeferredReadOnly = errors.New("field is both deferred and read-only")

	// ErrDeferredNaturalKey is returned for an unconditionally deferred field that a
	// natural-key candidate matches on.
	ErrDeferredNaturalKey = errors.New("natural-key field may not be deferred unconditionally")

	// ErrUnknownDeferMode is returned for a deferral mode the engine does not implement.
	ErrUnknownDeferMode = errors.New("unknown defer mode")

	// ErrUnknownUpdateStrategy is returned for an update strategy the engine does not
	// implement, the empty string included.
	ErrUnknownUpdateStrategy = errors.New("unknown update strategy")

	// ErrRecreateOnWithoutRecreate is returned when fields are declared identity-bearing
	// on a kind that updates in place.
	ErrRecreateOnWithoutRecreate = errors.New("recreateOn requires UpdateRecreate")

	// ErrFieldClassConflict is returned for a field declared in two field classes.
	ErrFieldClassConflict = errors.New("field is declared in two field classes")

	// ErrEmptyField is returned for an empty entry in a field list.
	ErrEmptyField = errors.New("empty field name")

	// ErrInvalidGenericFK is returned for a generic-FK spec missing a column or its
	// legal targets.
	ErrInvalidGenericFK = errors.New("incomplete generic-FK spec")

	// ErrCachedNotReadOnly is returned for a denormalised column a generic FK declares
	// that is not also in ReadOnly.
	//
	// The check exists because writing one is the failure this whole mechanism is about:
	// NetBox maintains `_site` from `(scope_type, scope_id)` and ignores an attempt to set
	// it, so a descriptor that treats it as writable produces a field the operator sends
	// forever and NetBox drops every time.
	ErrCachedNotReadOnly = errors.New("cached generic-FK column is not read-only")

	// ErrGenericFKTypeMismatch is returned when a union member's target kind reports an
	// object type the pair does not permit.
	//
	// Checked across the registry rather than on the descriptor, because the answer lives
	// on the *target's* descriptor. It is what stops `scope_type` from being written with a
	// spelling NetBox rejects, or -- worse -- one it accepts for a different model.
	ErrGenericFKTypeMismatch = errors.New("union member's object type is not permitted by the pair")
)

// objectTypePattern is the Django ContentType spelling: `model` is lowercased and
// unpunctuated, so it is `virtualization.vminterface`, never
// `virtualization.VMInterface` (docs/netbox-schema.md, generic-FK note).
var objectTypePattern = regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]+$`)

// UpdateStrategy is how the engine turns a diff into writes.
type UpdateStrategy string

const (
	// UpdatePatch PATCHes the diff, which is what every kind does unless its identity
	// lives somewhere a PATCH cannot reach.
	UpdatePatch UpdateStrategy = "Patch"

	// UpdateRecreate deletes the object and creates a replacement. dcim.Cable needs it:
	// its identity lives in its terminations, `unique(termination_type, termination_id)`
	// keeps the wanted endpoint occupied by the old cable until it is deleted
	// (docs/netbox-schema.md -> dcim.Cable.meta.constraints), so the replacement cannot be
	// created first. Declared as data because the alternative is `if kind == Cable` in the
	// engine.
	UpdateRecreate UpdateStrategy = "Recreate"
)

// knownUpdateStrategies deliberately excludes the zero value: whether an update is
// destructive is too important to default silently.
var knownUpdateStrategies = []UpdateStrategy{UpdatePatch, UpdateRecreate}

// DeferMode is when a reference is left out of the create payload and applied by a
// follow-up PATCH.
type DeferMode string

const (
	// DeferAlways is for a reference that cannot exist at create time by construction: a
	// Device's `primary_ip4` needs an address that needs an interface that needs the
	// Device (docs/netbox-schema.md -> dcim.Device.primary_ip4). No apply order fixes it.
	DeferAlways DeferMode = "Always"

	// DeferIfUnresolved includes the field in the create payload when it resolves and
	// defers only when it does not. Deferring a `parent` unconditionally would create the
	// object as top-level, where it can adopt an unrelated top-level object of the same
	// name, and the follow-up PATCH would then reparent that object (NBO-015).
	DeferIfUnresolved DeferMode = "IfUnresolved"
)

var knownDeferModes = []DeferMode{DeferAlways, DeferIfUnresolved}

// DeferredField is one field the engine may leave out of a create payload.
type DeferredField struct {
	// APIField is the NetBox field name as written, not the filter name.
	APIField string

	// Mode is when the deferral applies.
	Mode DeferMode
}

// GenericFKSpec describes one polymorphic foreign key: a `*_type` / `*_id` column pair
// whose type half is written as an `app_label.model` string over the REST API
// (docs/netbox-schema.md, generic-FK note).
type GenericFKSpec struct {
	// TypeField is the content-type column, e.g. `assigned_object_type`.
	TypeField string

	// IDField is the object-id column, e.g. `assigned_object_id`.
	IDField string

	// AllowedTypes are the object types this pair may point at, in the same spelling as
	// Descriptor.ObjectType. It drives resolver dispatch and ref watches, so a new union
	// member stays a data change.
	AllowedTypes []string

	// Spec is the CR spec field behind the pair (`scopeRef`, `assignedObject`). One spec
	// field writes both columns, which is why it is declared here rather than in Fields:
	// a Field maps one spec name to one API name, and this reference has two.
	Spec string

	// Members are the union's own fields, each naming the Kind it resolves against. It is
	// what turns the union from a shape the API server validates into one the resolver can
	// dispatch on, and it is data so that a new member is a descriptor edit rather than a
	// branch in internal/resolver.
	//
	// Optional, like Field.Target and for the same reason: a pair whose legal targets have
	// no CRD yet -- ipam.IPAddress.assigned_object points at interfaces and FHRP groups --
	// would otherwise have to name GVKs nobody has built. A pair with no members resolves
	// to nothing and is reported unresolved, which is what the engine did before this
	// existed.
	Members []GenericFKMember

	// Cached are the read-only denormalised columns NetBox maintains from this pair:
	// `_region`, `_site_group`, `_site` and `_location` for CachedScopeMixin
	// (docs/netbox-schema.md -> dcim.CachedScopeMixin). Each must also appear in
	// Descriptor.ReadOnly, which Validate enforces.
	//
	// Declared per pair rather than as a constant because not every scoped model has them:
	// ipam.VLANGroup declares `scope_type` / `scope_id` on the model itself and carries no
	// caches at all.
	Cached []string
}

// GenericFKMember is one arm of a polymorphic reference: the CR spec field, and the Kind it
// points at.
type GenericFKMember struct {
	// Field is the union's own field name, which is its JSON name -- `siteRef`, not
	// `scope.siteRef` and not `site`.
	Field string

	// Target is the Kind this member resolves against. The object type written to the
	// pair's type column is that Kind's own Descriptor.ObjectType, never a string repeated
	// here, so `dcim.sitegroup` is spelled once in the codebase.
	//
	// Write it as the matching typed alias's own answer,
	// `v1alpha1.SiteRef{}.TargetGVK()`.
	Target schema.GroupVersionKind
}

// Descriptor is everything the engine needs to reconcile one kind, as data.
type Descriptor struct {
	// GVK is the Kubernetes kind this descriptor drives.
	GVK schema.GroupVersionKind

	// Endpoint is the NetBox REST path relative to /api, e.g. `ipam/prefixes`. It is
	// looked up, never derived by pluralising: virtualization.VMInterface lives at
	// `virtualization/interfaces` and dcim.VirtualChassis at `dcim/virtual-chassis`
	// (docs/netbox-schema.md, endpoint map).
	Endpoint string

	// ObjectType is the `app_label.model` spelling other kinds use to point at this one
	// through a generic FK. It is written down here so there is exactly one source for it.
	ObjectType string

	// Scope is the CRD scope. Every kind is NamespaceScoped in v1alpha1
	// (docs/decisions/0002-crd-scoping.md); the field exists because CRD scope is
	// immutable, so promoting a kind is a new API version rather than a redesign, and the
	// M7 generator carries it as a per-kind attribute.
	Scope apiextensionsv1.ResourceScope

	// Fields maps this kind's CR spec fields to the NetBox fields they are written as. It
	// is the only bridge between the two vocabularies every other list here uses — see
	// Field for why it is a table and not a naming convention.
	Fields []Field

	// NaturalKeys are the lookup candidates, tried in the order given. More than one is
	// the normal case, not a fallback: ipam.Prefix and ipam.IPAddress have no
	// meta.constraints at all (docs/netbox-schema.md), so their identity is a convention
	// expressed as a priority list.
	NaturalKeys []NaturalKey

	// UpdateStrategy is how an update is written. Validate rejects the zero value:
	// whether an update destroys the object first is not a thing to default silently.
	UpdateStrategy UpdateStrategy

	// RecreateOn are the API fields whose change forces delete-then-create. Only
	// meaningful with UpdateRecreate, and load-bearing there: a cable's `label` is an
	// ordinary PATCH while the membership of its termination lists is not, so a strategy
	// alone would make every edit destructive.
	RecreateOn []string

	// Deferred are fields kept out of the create payload and applied by a second PATCH.
	Deferred []DeferredField

	// ReadOnly are fields the operator must never write: every `_`-prefixed cached
	// column, every CounterCacheField, `created`, `last_updated`, `url`, `display`
	// (docs/netbox-schema.md preamble). Writing one silently no-ops, which is a PATCH
	// loop rather than an error.
	ReadOnly []string

	// M2M are many-to-many fields written as a list of NetBox object IDs.
	M2M []string

	// Arrays are Postgres ArrayFields, whose order is data rather than incidental:
	// ipam.VLANGroup.vid_ranges and ipam.Service.ports (docs/netbox-schema.md). They are
	// a separate class from M2M because comparing them order-independently would miss a
	// reordering the user asked for, and comparing an M2M order-sensitively would PATCH
	// forever — NetBox does not preserve M2M order.
	Arrays []string

	// ObjectTypeLists are many-to-many fields onto contenttypes.ContentType, whose
	// values are `app_label.model` strings rather than references to NetBox objects:
	// extras.Tag.object_types is the first (docs/netbox-schema.md -> extras.Tag). They are
	// a separate class from M2M because a resolver told to resolve them would look for
	// CRs that cannot exist.
	ObjectTypeLists []string

	// GenericFKs are the polymorphic foreign keys on this kind.
	GenericFKs []GenericFKSpec

	// Taggable reports that this kind's NetBox model mixes in TagsMixin, so `tags` is a
	// writable column on it.
	//
	// It exists so the engine can stamp a provenance tag (NBO-075) without knowing what
	// kind it is holding. Not derivable and not defaultable: extras.Tag inherits
	// django-taggit's TagBase and *is* the tag, so it carries no `tags` of its own
	// (docs/netbox-schema.md -> extras.Tag, bases). NetBox ignores a column it does not
	// know rather than rejecting it, so writing `tags` to such a kind would not fail --
	// the value would vanish, the next read would find it absent, and the engine would
	// PATCH it again on every resync forever.
	Taggable bool

	// CustomFieldable reports that this kind's NetBox model mixes in CustomFieldsMixin, so
	// `custom_fields` is a writable column on it.
	//
	// Separate from Taggable because the two mixins are independent in NetBox, and
	// load-bearing twice over: it gates the stamp exactly as Taggable does, and it is what
	// the provenance bootstrap derives extras.CustomField's required `object_types` list
	// from -- a CustomField declared for the wrong set of types makes every write to a type
	// outside it a 400.
	CustomFieldable bool

	// ContainmentRef is the one spec field whose target gets a non-controller owner
	// reference, so deleting the parent cascades. Exactly one, because Kubernetes garbage
	// collection waits for every owner and two containment owners silently turn "delete
	// the site or the VRF" into "delete both"
	// (docs/decisions/0003-ownership-and-references.md rule 4). Empty when the kind has no
	// containment parent, which is every catalogue kind.
	ContainmentRef string
}

// Candidates are the natural keys usable for state, in priority order. The engine tries
// them in turn; an empty result means identity cannot be established yet, and the engine
// must wait rather than create — see NaturalKey.Applicable.
func (d Descriptor) Candidates(state SpecState) []NaturalKey {
	usable := make([]NaturalKey, 0, len(d.NaturalKeys))

	for _, key := range d.NaturalKeys {
		if key.Applicable(state) {
			usable = append(usable, key)
		}
	}

	return usable
}

// Validate reports every way this descriptor is malformed. It runs at manager start, so
// a bad descriptor fails the boot rather than one reconcile.
func (d Descriptor) Validate() error {
	checks := []func() error{
		d.validateIdentity,
		d.validateNaturalKeys,
		d.validateDeferred,
		d.validateFieldSets,
		d.validateFieldMap,
		d.validateGenericFKs,
		d.validateGenericFKSpecFields,
		d.validateUpdates,
	}

	errs := make([]error, 0, len(checks))

	for _, check := range checks {
		errs = append(errs, check())
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("descriptor %s: %w", d.GVK, err)
	}

	return nil
}

func (d Descriptor) validateIdentity() error {
	errs := make([]error, 0, 4)

	if d.GVK.Empty() {
		errs = append(errs, ErrEmptyGVK)
	}

	if d.Endpoint == "" {
		errs = append(errs, ErrNoEndpoint)
	}

	if !objectTypePattern.MatchString(d.ObjectType) {
		errs = append(errs, fmt.Errorf("%w: %q", ErrInvalidObjectType, d.ObjectType))
	}

	if d.Scope != apiextensionsv1.NamespaceScoped && d.Scope != apiextensionsv1.ClusterScoped {
		errs = append(errs, fmt.Errorf("%w: %q", ErrUnknownScope, d.Scope))
	}

	return errors.Join(errs...)
}

func (d Descriptor) validateNaturalKeys() error {
	if len(d.NaturalKeys) == 0 {
		return ErrNoNaturalKey
	}

	errs := make([]error, 0, len(d.NaturalKeys))

	for i, key := range d.NaturalKeys {
		if err := key.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("natural key %d: %w", i, err))
		}
	}

	return errors.Join(errs...)
}

// validateDeferred also enforces NBO-015's identity guard. Note what it does not check:
// a natural key naming a read-only column is legal and necessary —
// virtualization.Cluster is unique on `(_site, name)` (docs/netbox-schema.md ->
// virtualization.Cluster.meta.constraints), and `_site` is a cache the operator must
// never write but must be able to filter on as `site_id`.
func (d Descriptor) validateDeferred() error {
	readOnly := make(map[string]struct{}, len(d.ReadOnly))
	for _, field := range d.ReadOnly {
		readOnly[field] = struct{}{}
	}

	errs := make([]error, 0, len(d.Deferred))

	for _, deferred := range d.Deferred {
		if deferred.APIField == "" {
			errs = append(errs, fmt.Errorf("%w: deferred", ErrEmptyField))
		}

		if !slices.Contains(knownDeferModes, deferred.Mode) {
			errs = append(errs, fmt.Errorf("%w: %q on %q", ErrUnknownDeferMode, deferred.Mode, deferred.APIField))
		}

		if _, dup := readOnly[deferred.APIField]; dup {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDeferredReadOnly, deferred.APIField))
		}

		if deferred.Mode == DeferAlways && d.matchedByNaturalKey(deferred.APIField) {
			errs = append(errs, fmt.Errorf("%w: %s", ErrDeferredNaturalKey, deferred.APIField))
		}
	}

	return errors.Join(errs...)
}

// matchedByNaturalKey reports whether any candidate matches on apiField by value. Null
// pins are exempt: such a candidate asserts the field is unset, which is exactly the state
// a create with the field deferred is in, so deferral cannot corrupt that identity.
func (d Descriptor) matchedByNaturalKey(apiField string) bool {
	for _, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			// A foreign key is written as `parent` and filtered as `parent_id`, so the
			// two spellings have to be reconciled before they can be compared.
			if field.Filter == apiField || strings.TrimSuffix(field.Filter, "_id") == apiField {
				return true
			}
		}
	}

	return false
}

func (d Descriptor) validateFieldSets() error {
	lists := []struct {
		name   string
		fields []string
	}{
		{"readOnly", d.ReadOnly},
		{"m2m", d.M2M},
		{"objectTypeLists", d.ObjectTypeLists},
		{"arrays", d.Arrays},
		{"recreateOn", d.RecreateOn},
	}

	errs := make([]error, 0, len(lists))

	for _, list := range lists {
		if slices.Contains(list.fields, "") {
			errs = append(errs, fmt.Errorf("%w: %s", ErrEmptyField, list.name))
		}
	}

	for _, field := range d.ObjectTypeLists {
		if slices.Contains(d.M2M, field) {
			errs = append(errs, fmt.Errorf("%w: %s is both m2m and objectTypeList", ErrFieldClassConflict, field))
		}
	}

	for _, field := range d.Arrays {
		if slices.Contains(d.M2M, field) || slices.Contains(d.ObjectTypeLists, field) {
			errs = append(errs, fmt.Errorf("%w: %s is an array and a list of references", ErrFieldClassConflict, field))
		}
	}

	return errors.Join(errs...)
}

func (d Descriptor) validateGenericFKs() error {
	errs := make([]error, 0, len(d.GenericFKs))

	for _, generic := range d.GenericFKs {
		if generic.TypeField == "" || generic.IDField == "" || len(generic.AllowedTypes) == 0 {
			errs = append(errs, fmt.Errorf("%w: %+v", ErrInvalidGenericFK, generic))

			continue
		}

		for _, objectType := range generic.AllowedTypes {
			if !objectTypePattern.MatchString(objectType) {
				errs = append(errs, fmt.Errorf("%w: %q on %s", ErrInvalidObjectType, objectType, generic.TypeField))
			}
		}

		errs = append(errs, d.validateGenericFKMembers(generic), d.validateGenericFKCaches(generic))
	}

	return errors.Join(errs...)
}

// validateGenericFKMembers checks the union's arms. A member with no target Kind cannot be
// dispatched on, and a duplicate member field would make which Kind a spec value resolves
// against depend on slice order.
func (d Descriptor) validateGenericFKMembers(generic GenericFKSpec) error {
	errs := make([]error, 0, len(generic.Members))
	seen := make(map[string]struct{}, len(generic.Members))

	for _, member := range generic.Members {
		// Keyed on Kind rather than on GVK.Empty(): a target carrying a group and a
		// version and no Kind is not empty and is not resolvable either, and it is the
		// shape a half-written member actually has.
		if member.Field == "" || member.Target.Kind == "" {
			errs = append(errs, fmt.Errorf("%w: member %+v of %s", ErrInvalidGenericFK, member, generic.Spec))

			continue
		}

		if _, dup := seen[member.Field]; dup {
			errs = append(errs, fmt.Errorf("%w: %s.%s is declared twice",
				ErrInvalidGenericFK, generic.Spec, member.Field))
		}

		seen[member.Field] = struct{}{}
	}

	return errors.Join(errs...)
}

// validateGenericFKCaches ties the pair's denormalised columns to ReadOnly, so that a kind
// declaring `_site` as a cache and forgetting it in ReadOnly fails the boot rather than
// PATCHing a column NetBox ignores on every resync.
func (d Descriptor) validateGenericFKCaches(generic GenericFKSpec) error {
	errs := make([]error, 0, len(generic.Cached))

	for _, column := range generic.Cached {
		if column == "" {
			errs = append(errs, fmt.Errorf("%w: cached on %s", ErrEmptyField, generic.Spec))

			continue
		}

		if !slices.Contains(d.ReadOnly, column) {
			errs = append(errs, fmt.Errorf("%w: %s", ErrCachedNotReadOnly, column))
		}
	}

	return errors.Join(errs...)
}

func (d Descriptor) validateUpdates() error {
	if !slices.Contains(knownUpdateStrategies, d.UpdateStrategy) {
		return fmt.Errorf("%w: %q", ErrUnknownUpdateStrategy, d.UpdateStrategy)
	}

	if len(d.RecreateOn) > 0 && d.UpdateStrategy != UpdateRecreate {
		return fmt.Errorf("%w: %v", ErrRecreateOnWithoutRecreate, d.RecreateOn)
	}

	return nil
}

// Registry maps a GroupVersionKind to its Descriptor.
type Registry struct {
	// mu guards the state below. Get is called from every reconcile goroutine while Add
	// is called from init(), and an invariant held only by documentation is not one.
	mu         sync.RWMutex
	byGVK      map[schema.GroupVersionKind]Descriptor
	duplicates []schema.GroupVersionKind
}

// New returns an empty registry. Tests use it to stay off the package-level one.
func New() *Registry {
	return &Registry{byGVK: make(map[schema.GroupVersionKind]Descriptor)}
}

// Add registers d. A GVK that is already registered is rejected and recorded, so the
// first registration wins and Validate reports the collision too: registration happens in
// one init() per kind, where a returned error is easy to drop.
func (r *Registry) Add(d Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byGVK[d.GVK]; exists {
		r.duplicates = append(r.duplicates, d.GVK)

		return fmt.Errorf("%w: %s", ErrDuplicateGVK, d.GVK)
	}

	r.byGVK[d.GVK] = d

	return nil
}

// Get returns the descriptor for gvk.
func (r *Registry) Get(gvk schema.GroupVersionKind) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.byGVK[gvk]

	return d, ok
}

// List returns every descriptor, ordered by GVK. The order is deterministic because
// callers log, validate and generate from it, and a map-ordered list makes all three
// unreviewable.
func (r *Registry) List() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.list()
}

// Validate reports every malformed descriptor and every duplicate registration. The
// manager calls it at start, so the process fails to boot rather than failing a reconcile.
func (r *Registry) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	descriptors := r.list()
	errs := make([]error, 0, len(descriptors)+len(r.duplicates))

	for _, gvk := range r.duplicates {
		errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateGVK, gvk))
	}

	for _, d := range descriptors {
		errs = append(errs, d.Validate(), r.validateUnionTypes(d))
	}

	return errors.Join(errs...)
}

// validateUnionTypes checks each union member's target kind against the object types its
// pair permits.
//
// Cross-descriptor, so it lives here rather than on Descriptor: the object type written to
// `scope_type` comes off the *target's* descriptor, and AllowedTypes is the referring kind's
// statement of what NetBox will accept there. A member whose target reports a type the pair
// does not list is a descriptor bug that would otherwise surface as a 400 on the first write.
//
// A target with no descriptor is skipped rather than rejected. Declaring a member before its
// Kind exists is the normal state through M2-M9 -- NetBoxSiteGroup and NetBoxLocation are
// still to come -- and the resolver already reports such a member as RefKindUnavailable.
func (r *Registry) validateUnionTypes(d Descriptor) error {
	var errs []error

	for _, generic := range d.GenericFKs {
		for _, member := range generic.Members {
			target, registered := r.byGVK[member.Target]
			if !registered || slices.Contains(generic.AllowedTypes, target.ObjectType) {
				continue
			}

			errs = append(errs, fmt.Errorf("descriptor %s: %w: %s.%s -> %s is %q, permitted are %v",
				d.GVK, ErrGenericFKTypeMismatch, generic.Spec, member.Field,
				member.Target.Kind, target.ObjectType, generic.AllowedTypes))
		}
	}

	return errors.Join(errs...)
}

// list is List without the lock, for callers that already hold it.
func (r *Registry) list() []Descriptor {
	descriptors := make([]Descriptor, 0, len(r.byGVK))
	for _, d := range r.byGVK {
		descriptors = append(descriptors, d)
	}

	slices.SortFunc(descriptors, func(a, b Descriptor) int {
		return cmp.Compare(a.GVK.String(), b.GVK.String())
	})

	return descriptors
}

// defaultRegistry is what the per-kind init() functions register into. It is not exported
// so it cannot be swapped out from under a running manager.
var defaultRegistry = New()

// MustRegister adds d to the package-level registry and panics if it cannot. It is called
// from one init() per kind: a duplicate kind is a programming error that must stop the
// process at boot, never surface as a reconcile failure hours later.
func MustRegister(d Descriptor) {
	if err := defaultRegistry.Add(d); err != nil {
		panic(fmt.Sprintf("registry: %v", err))
	}
}

// Get returns the descriptor registered for gvk.
func Get(gvk schema.GroupVersionKind) (Descriptor, bool) {
	return defaultRegistry.Get(gvk)
}

// List returns every registered descriptor, ordered by GVK.
func List() []Descriptor {
	return defaultRegistry.List()
}

// Validate validates every registered descriptor.
func Validate() error {
	return defaultRegistry.Validate()
}
