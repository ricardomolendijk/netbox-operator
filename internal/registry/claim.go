package registry

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ClaimDescriptor validation failures, each a sentinel so callers classify by type.
var (
	// ErrDuplicateClaimGVK is returned when a claim Kind is registered twice.
	ErrDuplicateClaimGVK = errors.New("duplicate claim registration for GroupVersionKind")

	// ErrIncompleteClaim is returned for a claim descriptor missing something the
	// allocation engine cannot proceed without.
	ErrIncompleteClaim = errors.New("incomplete claim descriptor")

	// ErrUnknownPoolKind is returned when a claim's pool Kind has no Descriptor of its own.
	//
	// A boot failure rather than a runtime condition, unlike an ordinary reference to an
	// unregistered Kind (ErrRefKindUnavailable). A claim kind and its pool kind ship in the
	// same binary -- there is no build in which one is installed and the other is not -- so a
	// claim whose pool is unregistered is a wiring mistake in this repository, and the pool's
	// own Descriptor is where its NetBox endpoint is written down.
	ErrUnknownPoolKind = errors.New("claim's pool Kind has no descriptor")

	// ErrUnknownPoolSubPath is returned for an allocation sub-path that is not one of
	// NetBox's advisory-locked views.
	ErrUnknownPoolSubPath = errors.New("unknown allocation sub-path")
)

// knownPoolSubPaths is the closed set of allocation mechanisms a claim kind may name.
//
// The first two are NetBox's advisory-locked views. The third is not a NetBox URL at all: at
// 4.6.8 there is no `available-ranges` endpoint, so placing an ip-range inside a prefix is
// computed client-side and committed with a plain POST, whose safety comes from
// `ipam.IPRange.clean()` rejecting an overlap rather than from a lock (netbox.PlaceRange).
//
// The set is closed either way, and that is the property worth keeping: a claim kind must name
// which server-side guarantee it relies on -- a lock, or a rejection -- and a kind that can
// name neither does not boot.
//
// Spelled as netbox's own constants would be, but not imported: internal/registry deliberately
// depends on nothing but the API types, so that a Descriptor stays data a generator can emit.
var knownPoolSubPaths = []string{"available-ips", "available-prefixes", "place-ip-range"}

// ClaimDescriptor is everything the allocation engine needs to reconcile one claim kind, as
// data.
//
// It is separate from Descriptor rather than a mode of it, for the same reason a claim is a
// separate Kind rather than a mode of NetBoxIPAddress: the two lifecycles have almost
// nothing in common (docs/decisions/0004-claims-first-allocation.md). A Descriptor
// describes an object reconciled towards a fixed desired state forever -- natural keys,
// drift rules, deferred fields, an update strategy. A claim does one irreversible thing and
// then stops, so none of those apply, and a claim also cannot claim the ObjectType of the
// kind it allocates: `ipam.ipaddress` belongs to NetBoxIPAddress's own Descriptor, and
// Registry.Add rejects a second claimant deliberately.
//
// Nothing here is a func, exactly as on Descriptor: adding a claim kind (NBO-064's prefix
// and ip-range claims) is a new file registering one of these, and never an edit to
// internal/reconciler.
type ClaimDescriptor struct {
	// GVK is the claim Kind this descriptor drives.
	GVK schema.GroupVersionKind

	// Endpoint is the REST path the *allocated* object lives at, relative to /api:
	// `ipam/ip-addresses`. Where the read-after-write verification reads from, and where the
	// identity search searches.
	Endpoint string

	// ObjectType is the `app_label.model` spelling of the allocated model.
	//
	// Declared here even though the claim Kind itself is not a NetBox object, because the
	// provenance bootstrap needs it: the custom field carrying the allocation identity has
	// to list `ipam.ipaddress` in its object_types or the first allocating POST is a 400
	// naming a field the user can see in the UI (provenance.ObjectTypes).
	ObjectType string

	// Pool is the CR spec field naming the pool, and the Kind it points at.
	//
	// A registry.Field rather than a bespoke pair, so that resolving a pool is the same
	// code path -- and the same NetBoxRefGrant check -- as resolving any other reference.
	// Its API name is empty on purpose: the pool is not a column on the allocated object,
	// it is where the object comes from.
	Pool Field

	// PoolValueField is the pool object's own column holding the network it covers:
	// `prefix` on an ipam.Prefix. Read to display the pool and to prove an address is
	// inside it.
	PoolValueField string

	// PoolSubPath is the advisory-locked allocation view to POST to, one of
	// netbox.AvailableIPs or netbox.AvailablePrefixes.
	PoolSubPath string

	// PoolMustNotBeTrue are the pool object's boolean flags whose being set means this claim
	// kind refuses to allocate.
	//
	// `mark_utilized` for an address claim: it forces NetBox's utilisation gauge to 100%
	// without stopping `available-ips` from handing out an address, so it is the NetBox
	// operator declaring "the free space here is delegated elsewhere" and honouring it has
	// to be the operator's job rather than the server's.
	PoolMustNotBeTrue []string

	// PoolForbiddenStatus are the values of the pool's `status` choice column that forbid
	// allocation.
	//
	// `container` for an address claim, and the reason this is data rather than a check in
	// shared code: a container is exactly what NBO-064's NetBoxPrefixClaim will *require*,
	// so the same value is a refusal for one claim kind and a precondition for the next.
	PoolForbiddenStatus []string

	// RequestLengthField is the API name of the RequestFields entry carrying a mask length
	// that has to be *longer* than the pool's own: `prefix_length` on a prefix claim.
	//
	// Data because the check needs the resolved pool -- CEL cannot see it, so the CRD can only
	// bound the value statically -- and because the two things it catches are the two mistakes
	// a mask length can be. A length shorter than the parent's cannot fit inside it and NetBox
	// answers 409, which reads as an exhausted pool and sends the reader to look at
	// utilisation. A length *equal* to the parent's is worse: NetBox's `available-prefixes`
	// happily hands out the whole parent, so `prefixLength: 16` on a /16 creates a second /16
	// object identical to the first, in the same VRF, and reports success
	// (`GetAvailablePrefixesMixin.get_available_prefixes` subtracts child prefixes, and there
	// are none). A length wider than the address family -- 64 on an IPv4 parent -- is the third,
	// and the same comparison catches it.
	//
	// Empty for a claim kind whose request carries no mask length, which is both of the others.
	RequestLengthField string

	// PoolExpectedStatus are the values of the pool's `status` column this claim kind expects,
	// and allocating out of anything else is unusual enough to say so.
	//
	// Data rather than a rule, exactly like PoolForbiddenStatus, and the pair is where the
	// asymmetry between the claim kinds is written down: `container` is a *refusal* for an
	// address claim, because a container's free space is subdivided by child prefixes rather
	// than populated by addresses, and a *precondition* for a prefix claim, which subdivides
	// it. Neither is a rule shared code could hold.
	//
	// Unlike PoolForbiddenStatus this does not refuse. Carving a child prefix out of an
	// `active` prefix is unusual and legitimate -- somebody is subdividing a network that is
	// already in service -- so the allocation proceeds and a Warning Event records that the
	// operator noticed. Empty means "any status is ordinary", which is what an address claim
	// says: it lists its one refusal and expects everything else.
	PoolExpectedStatus []string

	// ResultField is the allocated object's field lifted into `status`: `address`.
	ResultField string

	// RequestFields are the claim spec fields copied into the allocating POST's body.
	//
	// The allocation *parameters*, as opposed to the provenance the engine adds itself: a
	// prefix claim has to say `prefix_length`, and a range claim has to say how many addresses
	// it wants and how they should be aligned. An address claim has none, which is why NBO-036
	// did not need this and why it is a list rather than a field.
	//
	// registry.Field for the same reason Pool is one: the spec name and the wire name are
	// declared once, in the table every other field of every other kind is declared in, so a
	// `prefixLength` sent verbatim -- which NetBox would accept and ignore -- is a boot failure
	// rather than a prefix of the wrong size. Scalars only: a reference here would need
	// resolving, and a claim resolves exactly one reference (its pool).
	//
	// An API name may be a placement input rather than a NetBox column
	// (netbox.PlacementSize), which is what the `@` prefix on those marks. Nothing in this
	// package decides that; the client is what removes them from the body.
	RequestFields []Field

	// Taggable and CustomFieldable report which of the two provenance columns the
	// *allocated* model carries, exactly as they do on Descriptor and for the same reason:
	// NetBox ignores a column it does not know rather than rejecting it, so a stamp written
	// to a model without the column vanishes silently.
	//
	// CustomFieldable is required rather than optional here, and Validate enforces it. An
	// allocation identity has to be stored somewhere, and a model with no `custom_fields`
	// column has nowhere -- which would make every allocation of that kind unrecoverable
	// after a lost response.
	Taggable        bool
	CustomFieldable bool
}

// Validate reports every way this claim descriptor is malformed.
func (c ClaimDescriptor) Validate() error {
	errs := make([]error, 0, 3)

	// A slice rather than a map, so the order a malformed descriptor is reported in is the
	// order it is declared in and a boot failure reads the same way twice.
	required := []struct{ name, value string }{
		{"endpoint", c.Endpoint},
		{"objectType", c.ObjectType},
		{"pool.spec", c.Pool.Spec},
		{"pool.target", c.Pool.Target.Kind},
		{"poolValueField", c.PoolValueField},
		{"poolSubPath", c.PoolSubPath},
		{"resultField", c.ResultField},
	}

	for _, field := range required {
		if field.value == "" {
			errs = append(errs, fmt.Errorf("%w: %s is empty", ErrIncompleteClaim, field.name))
		}
	}

	// Kind as well as Empty, for the reason validateGenericFKMembers gives: a GVK carrying a
	// group and a version and no Kind is not Empty, and it is the shape a half-written
	// descriptor actually has. Registered, it would sit under a key no object ever resolves
	// to, so every claim of that kind would report "no claim descriptor is registered".
	if c.GVK.Empty() || c.GVK.Kind == "" {
		errs = append(errs, ErrEmptyGVK)
	}

	if !objectTypePattern.MatchString(c.ObjectType) {
		errs = append(errs, fmt.Errorf("%w: %q", ErrInvalidObjectType, c.ObjectType))
	}

	if !c.Pool.Class.Ref() || c.Pool.Class.ToMany() {
		errs = append(errs, fmt.Errorf("%w: pool.class is %q, want %q",
			ErrIncompleteClaim, c.Pool.Class, ClassRefOne))
	}

	if !c.CustomFieldable {
		errs = append(errs, fmt.Errorf(
			"%w: customFieldable is false, so there is nowhere to store an allocation identity",
			ErrIncompleteClaim))
	}

	errs = append(errs, c.validateRequestFields()...)

	if c.PoolSubPath != "" && !slices.Contains(knownPoolSubPaths, c.PoolSubPath) {
		errs = append(errs, fmt.Errorf("%w: %q, known are %v",
			ErrUnknownPoolSubPath, c.PoolSubPath, knownPoolSubPaths))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("claim descriptor %s: %w", c.GVK, err)
	}

	return nil
}

// validateRequestFields reports every malformed allocation parameter.
//
// Its own function because Validate is at the complexity limit, and this is the part of it
// that reads as a list rather than as a sequence of conditions.
func (c ClaimDescriptor) validateRequestFields() []error {
	errs := make([]error, 0, len(c.RequestFields))

	for _, field := range c.RequestFields {
		if field.Spec == "" || field.API == "" {
			errs = append(errs, fmt.Errorf("%w: requestFields entry %+v has no spec or api name",
				ErrIncompleteClaim, field))

			continue
		}

		// A reference would have to be resolved to a NetBox id, and the engine resolves exactly
		// one reference per claim: its pool. Anything else here is a scalar the spec already
		// holds in the shape NetBox wants.
		if field.Class != ClassValue {
			errs = append(errs, fmt.Errorf("%w: requestFields entry %s is %q, want a scalar",
				ErrIncompleteClaim, field.Spec, field.Class))
		}
	}

	return errs
}

// RefDescriptor is this claim kind as a Descriptor carrying nothing but its pool reference.
//
// It exists so that a claim's pool watch and its reference index are the *same* code as every
// other kind's -- internal/controller.WatchRefs and resolver.AddIndexes -- rather than a
// parallel path that can drift from it. Both of those read exactly two things off a
// Descriptor, its GVK and its reference fields, and those two facts are all a claim has.
//
// Nothing else on the returned value is filled, and it is deliberately **not** a valid
// Descriptor to reconcile with: it has no endpoint, no natural key and no update strategy, so
// Validate rejects it and it is registered nowhere. A Descriptor-shaped view for the two
// functions that want one, and not a second registration of the same Kind.
func (c ClaimDescriptor) RefDescriptor() Descriptor {
	return Descriptor{GVK: c.GVK, Fields: []Field{c.Pool}}
}

// claims maps a claim Kind to its ClaimDescriptor.
type claims struct {
	// mu guards the state below, for the same reason Registry's does: Claim is called from
	// every reconcile goroutine while add is called from init().
	mu         sync.RWMutex
	byGVK      map[schema.GroupVersionKind]ClaimDescriptor
	duplicates []schema.GroupVersionKind
}

func newClaims() *claims {
	return &claims{byGVK: make(map[schema.GroupVersionKind]ClaimDescriptor)}
}

func (c *claims) add(d ClaimDescriptor) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.byGVK[d.GVK]; exists {
		c.duplicates = append(c.duplicates, d.GVK)

		return fmt.Errorf("%w: %s", ErrDuplicateClaimGVK, d.GVK)
	}

	c.byGVK[d.GVK] = d

	return nil
}

func (c *claims) get(gvk schema.GroupVersionKind) (ClaimDescriptor, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	d, ok := c.byGVK[gvk]

	return d, ok
}

// list returns every claim descriptor, ordered by GVK, for the same reason Registry.List is
// ordered: callers log, validate and generate from it.
func (c *claims) list() []ClaimDescriptor {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]ClaimDescriptor, 0, len(c.byGVK))
	for _, d := range c.byGVK {
		out = append(out, d)
	}

	slices.SortFunc(out, func(a, b ClaimDescriptor) int {
		return cmp.Compare(a.GVK.String(), b.GVK.String())
	})

	return out
}

// validate reports every malformed claim descriptor, every duplicate registration, and
// every pool Kind this build does not carry a Descriptor for.
//
// The pool check has to live here rather than on ClaimDescriptor, and for the same reason
// Registry.validateUnionTypes does: it needs the *pool's* descriptor, which is what keeps
// the pool's REST path written down exactly once. It also has to run after every init(),
// which is why it is a boot-time validation and not a registration-time one -- init order
// inside a package is by filename, so a claim can legally be registered before its pool.
func (c *claims) validate(pools Descriptors) error {
	descriptors := c.list()

	c.mu.RLock()
	duplicates := slices.Clone(c.duplicates)
	c.mu.RUnlock()

	errs := make([]error, 0, len(descriptors)+len(duplicates))

	for _, gvk := range duplicates {
		errs = append(errs, fmt.Errorf("%w: %s", ErrDuplicateClaimGVK, gvk))
	}

	for _, d := range descriptors {
		errs = append(errs, d.Validate())

		if _, ok := pools.Get(d.Pool.Target); !ok {
			errs = append(errs, fmt.Errorf("claim descriptor %s: %w: %s",
				d.GVK, ErrUnknownPoolKind, d.Pool.Target))
		}
	}

	return errors.Join(errs...)
}

// Descriptors resolves a Kind to its Descriptor. Declared here rather than imported so that
// claim validation can be handed the package-level registry or a test's own.
type Descriptors interface {
	Get(gvk schema.GroupVersionKind) (Descriptor, bool)
}

// defaultClaims is what the per-claim-kind init() functions register into.
var defaultClaims = newClaims()

// MustRegisterClaim adds d to the package-level claim registry and panics if it cannot.
//
// One init() per claim kind, and a duplicate is a programming error that has to stop the
// process at boot rather than surface as a reconcile failure hours later.
func MustRegisterClaim(d ClaimDescriptor) {
	if err := defaultClaims.add(d); err != nil {
		panic(fmt.Sprintf("registry: %v", err))
	}
}

// Claim returns the claim descriptor registered for gvk.
func Claim(gvk schema.GroupVersionKind) (ClaimDescriptor, bool) { return defaultClaims.get(gvk) }

// Claims returns every registered claim descriptor, ordered by GVK.
func Claims() []ClaimDescriptor { return defaultClaims.list() }

// ClaimObjectTypes are the `app_label.model` strings claim kinds allocate into,
// deduplicated and sorted.
//
// It exists for the provenance bootstrap. The custom field a claim writes its allocation
// identity into must list the allocated model in its object_types, and no ordinary
// Descriptor supplies `ipam.ipaddress` until NBO-025 lands NetBoxIPAddress -- so without
// this the first allocating POST on a fresh NetBox is a 400, which is precisely the failure
// the bootstrap exists to prevent.
func ClaimObjectTypes() []string {
	descriptors := defaultClaims.list()
	types := make([]string, 0, len(descriptors))

	for _, d := range descriptors {
		if !slices.Contains(types, d.ObjectType) {
			types = append(types, d.ObjectType)
		}
	}

	slices.Sort(types)

	return types
}
