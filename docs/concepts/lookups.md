# Lookups

A lookup is the query the operator sends to find out whether the object in a CR already
exists in NetBox. Get it wrong in the "found nothing" direction and the engine creates a
duplicate; get it wrong in the "found something" direction and the engine adopts, and
then rewrites, an object belonging to somebody else. Both failures are silent.

The filters come from the kind's natural key
([`internal/registry`](../../internal/registry/naturalkey.go)); rendering them into a
query string is [`internal/netbox`](../../internal/netbox/query.go):

```go
params := netbox.Params{}.
    Match("name", netbox.LookupIExact, "dns"). // ?name__ie=dns
    Match("site_id", netbox.LookupExact, "3"). // ?site_id=3
    Null("tenant_id", netbox.NullColumnRef)    // ?tenant_id=null

device, err := client.GetOne(ctx, "dcim/devices", params)
```

`Params` is a `map[string]string` on purpose. In NetBox's filter language the lookup
modifier is part of the parameter *name* — `?name__ie=dns` is one key and one value,
structurally identical to `?name=dns` — so a modifier needs no extra axis in the type.
`Match` and `Null` exist only so the `__` spellings live in one place instead of being
concatenated at every call site.

## Why case-insensitive lookup exists

`dcim.Device` and `virtualization.VirtualMachine` are unique on `Lower('name')`, not on
`name` (`docs/netbox-schema.md` → `dcim.Device.meta.constraints`,
`virtualization.VirtualMachine.meta.constraints`). To NetBox, a device named `DNS` and one
named `dns` are the same device.

With an exact lookup a CR named `dns` therefore plays out like this:

1. `GET /api/dcim/devices/?name=dns&site_id=3` → **0 results**. The device stored as `DNS`
   does not match.
2. The engine concludes the object does not exist and `POST`s a new one.
3. NetBox's unique constraint rejects the write — a `ValidationError`, a long backoff, and
   a CR that never becomes ready. On an endpoint where the constraint does not apply the
   worse outcome happens instead: the create succeeds and there are now two objects
   differing only in case, one of them unmanaged.

`?name__ie=dns` finds the existing device and the engine adopts it. Only equality
modifiers are allowed — a natural key has to identify at most one object, which a
substring, prefix or negation lookup cannot promise.

## Why a null filter is pinned and never omitted

`?vrf_id=null` and *no* `vrf_id` at all are different questions. The first asks
for the address that is in no VRF; the second asks for that address in any VRF.

`ipam.IPAddress` has no `meta.constraints` at all (`docs/netbox-schema.md` →
`ipam.IPAddress`), so `10.0.0.1/32` can exist once globally and once in every VRF. A
lookup that omits `vrf_id` matches all of them, and a global address adopts a copy out of
some VRF — or, when more than one comes back, fails as an
[`AmbiguousError`](errors-and-retries.md#why-ambiguity-is-an-error), which is the good
outcome of the two. The same reasoning covers the nested-group kinds, where
`name WHERE parent IS NULL` is a constraint of its own: an omitted `parent_id` makes every
top-level Region adopt an unrelated one, and the follow-up `PATCH` reparents it.

So `vrf_id` is always either matched against a value or pinned to null, never left out.

## How a null pin is spelled, and why it depends on the column

There is no single spelling. NetBox builds its filters per column from the column's Django
field class, and the classes do not all register the same lookups — so a pin has to say
which class its column has. That is `NullField.Column`, one of three
values, checked at boot by `registry.Validate()`:

| `Column` | Digest field class | On the wire | What NetBox does with it |
|---|---|---|---|
| `NullColumnRef` | `ForeignKey` | `?parent_id=null` | The `null` sentinel becomes a `NULL` predicate |
| `NullColumnChar` | `CharField` | `?rd=null` | Same sentinel, same predicate |
| `NullColumnNumeric` | `PositiveBigIntegerField` and friends | `?scope_id__empty=true` | `empty` → the ORM's `isnull` |

**A foreign key has no lookup suffix to use.** NetBox's own reference says foreign keys
"support just the negation expression: `n`" (`netbox/docs/reference/filtering.md` →
"Foreign Keys & Other Fields"), because `BaseFilterSet` hands a model-choice filter the
negation-only lookup map (`netbox/netbox/filtersets.py:164-170` →
`FILTER_NEGATION_LOOKUP_MAP`). What it does accept is the **null sentinel**: the literal
value `null`, `FILTERS_NULL_CHOICE_VALUE` (`netbox/netbox/settings.py:771`). `django-filter`
turns it into a `NULL` predicate in `MultipleChoiceFilter.filter` (`if v ==
self.null_value: v = None`), and NetBox's `TreeNodeMultipleChoiceFilter` — which is what a
tenant group or a location's parent is filtered by — overrides `get_filter_predicate` so
that `None` renders as `__isnull=True` rather than `__in=None`
(`netbox/utilities/filters.py:135-138`).

**A char column takes the sentinel too, because the suffix asks a wider question.** A char
column does register `__empty`: `FILTER_CHAR_BASED_LOOKUP_MAP` has `empty='empty'`
(`netbox/utilities/constants.py:15`). But that `empty` is a *string-length* test —
`CAST(LENGTH(col) AS BOOLEAN) IS NOT TRUE` (`netbox/extras/lookups.py:69-73`) — which is
true for `NULL` **and** for the empty string. `ipam.VRF.rd` is
`CharField UNIQUE blank=True null=True` with no normalisation on save
(`netbox/ipam/models/vrfs.py:24-31`), so `''` is a reachable value distinct from `NULL` and
`?rd__empty=true` would match it. `?rd=null` means `NULL` and only `NULL`, so that is what
a null pin sends.

**A numeric column is the one case that takes the suffix.** `?scope_id__empty=true`, from
`FILTER_NUMERIC_BASED_LOOKUP_MAP`'s `empty='isnull'` (`netbox/utilities/constants.py:26`).
The sentinel is not available here: a numeric filter's form field casts each value with the
real field class (`netbox/utilities/filters.py:39-46`), so `null` fails `IntegerField`
validation and the whole request is rejected. The value is `true` because the suffix
resolves to a `BooleanFilter` (`netbox/netbox/filtersets.py:264-266`) over Django's
`forms.NullBooleanField`, which accepts `true`, `True` and `1` and silently turns anything
else into `None` (`django/forms/fields.py:838-852`) — an empty value, which leaves the
filter unapplied and looks exactly like the omitted case.

**A content-type column has no spelling at all, and must not be pinned.** `scope_type` is a
`ForeignKey` to `contenttypes.ContentType` behind a `MultiValueContentTypeFilter`. `__empty`
is never registered on it, because the `empty` ORM lookup exists only on `CharField` and
`JSONField` (`netbox/extras/lookups.py:128-129`) and `BaseFilterSet` skips a filter whose
lookup will not resolve (`netbox/netbox/filtersets.py:232-234`). The sentinel is worse than
dropped: `'null'.lower().split('.')` raises, the filter ends up as `scope_type__in=[]`, and
the request matches **nothing** (`netbox/utilities/filters.py:190-207`) — so the engine
would conclude the object does not exist and create a duplicate. Pin the paired `_id` column
instead, which for a generic FK asks the same question, because NetBox refuses one half of
the pair without the other (`netbox/ipam/models/vlans.py:105-109`).

`NullColumn` has no zero value, so a pin that does not declare its class fails
`registry.Validate()` at boot rather than picking a default — there is no default that is
safe for all three.

## Why an unregistered filter is the hazard, not any one parameter

`django-filter` answers a query parameter it does not recognise **as if it were absent**,
and NetBox 4.6.8 has no strict-filter validation. A misspelled filter therefore does not
fail: it returns the *unfiltered* set, which is the worst possible direction for a lookup —
the engine adopts an object it should never have matched and rewrites it.

That is exactly how [#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)
happened. Every null pin was sent as `?<filter>__isnull=true`, a parameter NetBox registers
nowhere: `isnull` is the *ORM lookup* the `empty` suffix maps to
(`netbox/utilities/constants.py:26`), not a parameter name. Every null-pinned candidate
therefore matched every row the value filters allowed, so a CR meaning "the region named
`emea` with no parent" matched a *nested* region of that name, adopted it, and `PATCH`ed
`parent` — reparenting somebody else's region, which is the precise failure the pin was
introduced to prevent.

Nothing caught it for the same reason it was easy to write: every test asserted the query
the operator *built*, against fakes that answered whatever they were asked. So the fakes now
reject a parameter whose lookup NetBox does not register, via
`netbox.LookupRegistered` — the union of the four lookup maps in
`netbox/utilities/constants.py`. That check is coarse: it catches a bad suffix, not a
misspelled column or a suffix used on a column whose filter class does not carry it. A
per-column check needs the filter registry NetBox builds at import time, and the honest
close on the whole class of gap is end-to-end coverage against a live instance
([#29](https://github.com/ricardomolendijk/netbox-operator/issues/29)).

## Duplicate addresses, and what `allowDuplicate` does to the natural key

**Decided** on
[#177](https://github.com/ricardomolendijk/netbox-operator/issues/177), and shipped on
`NetBoxIPAddress` only — see [the recipe below](#giving-another-kind-allowduplicate) for what a
second kind would add, and when it must not.

`ipam.VRF.enforce_unique` is a boolean with **`default=True`** (`docs/netbox-schema.md` →
`ipam.VRF`). In a VRF with it set, NetBox rejects a second identical address. With it false —
and in the global table, where the instance-wide `ENFORCE_GLOBAL_UNIQUE` setting decides —
NetBox accepts duplicates, and some networks need that: anycast addresses, VRRP/HSRP virtual
addresses, and the same address legitimately present in two disconnected L3 domains. So a
duplicate is sometimes an error and sometimes the whole point, and **NetBox decides which**,
through configuration the operator does not own.

**The operator therefore has no opinion by default.** It sends the create and NetBox accepts or
rejects it. A lookup that matches more than one object stays an
[`AmbiguousError`](errors-and-retries.md#why-ambiguity-is-an-error) naming the candidates —
never a guess, because picking one means adopting and then rewriting an address that belongs to
somebody else, which is the worst outcome available.

That leaves anycast expressible only by never letting two CRs describe the same address, so
there is one field on top: **`allowDuplicate`**. A CR that sets it declares that it expects
company at that address, which turns a multi-match from an error into "create another one" and
puts the intent in the manifest where a reader can see it.

**The reason the field is decided now rather than added later: it changes the natural key.** For
an address that may legitimately exist several times, the address is not an identity —
`ipam.IPAddress` has no `meta.constraints` at all, and nothing else about two identical
addresses distinguishes them. So a duplicate-permitting CR identifies its object by the
[provenance stamp](../operations/provenance.md#what-gets-written) as well, because that is the
only thing that says *which of these is mine*. Natural keys are the hardest thing to change
after objects exist in the wild — a CR that has been adopting by address cannot start adopting
by address-plus-provenance without re-adopting everything it owns — which is why this had to be
settled before `NetBoxIPAddress` shipped rather than after.

**`allowDuplicate` with no provenance stamp to match on refuses.** An object created before the
operator, or by another tool, carries no stamp; the operator then cannot tell which of the
matches is its own, and that is exactly the moment when creating one more copy is worst. It
reports and waits for a human instead.

Rejected: **reading the VRF's `enforce_unique` and behaving accordingly.** It reads well and it
cannot work — the flag is visible for a VRF and `ENFORCE_GLOBAL_UNIQUE` is not visible at all,
so the operator would be right in the VRF case and guessing in the global one, with a silent
failure and no way for a reader to tell the two apart.

## Giving another kind `allowDuplicate`

**Decided** on [#194](https://github.com/ricardomolendijk/netbox-operator/issues/194):
`allowDuplicate` stays a **per-kind spec field** and does not move onto `NetBoxObjectSpec`, the
envelope every kind embeds. The engine is already generic over it —
[`Descriptor.DuplicateSpec`](../../internal/registry/registry.go) names the field and
[`internal/reconciler`](../../internal/reconciler/duplicate.go) reads it off the decoded spec by
that name, with no branch on Kind — so the field costs three additions per kind and no shared
edit, which is the [extensibility contract](../../CONTRIBUTING.md) rather than an exception to
it. On the envelope it would instead appear in `kubectl explain` for around 120 kinds where a
duplicate is impossible, and per-kind truth would live somewhere the CRD schema cannot show it,
which is the shape that made [#186](https://github.com/ricardomolendijk/netbox-operator/issues/186)
awkward.

The three additions, for a kind that needs it:

1. **The spec field**, on that kind's own spec struct: an `+optional` `bool` tagged
   `json:"allowDuplicate,omitempty"`. It documents no empty state — an `omitempty` bool has two
   states, not three ([field ownership](field-ownership.md)).
2. **`DuplicateSpec: "allowDuplicate"`** on that kind's `Descriptor`. That is the whole wiring:
   the engine looks the field up by this name, and `internal/reconciler`'s payload builder keeps
   it out of the NetBox body for the same reason it keeps the envelope's own fields out — it
   configures the operator and describes no column, and NetBox drops a column it does not know
   rather than rejecting it.
3. **One sentence on the kind's reference page**, saying which NetBox configuration decides
   whether the duplicate is legal for that model, and repeating the two consequences from
   [`NetBoxIPAddress`](../reference/netboxipaddress.md#allowduplicate): it needs the endpoint's
   `spec.managedBy`, and an unstamped match is refused rather than duplicated.

**When not to: a kind whose natural key a `UNIQUE` constraint backs must never declare it.**
`allowDuplicate` makes the [provenance stamp](../operations/provenance.md#what-gets-written) part
of the natural key, which is worth doing only where nothing else tells two matching objects
apart. Where the database keeps the key unique — every `OrganizationalModel.slug`, `ipam.VRF.rd`,
anything with a `meta.constraints` entry (`docs/netbox-schema.md`) — the second row cannot exist,
so the field could only ever turn a `Conflict` a reader can act on into a rejected write. That is
also why `NetBoxContact` has no such field even though two contacts of one name are legal server
state: only an index backs `name` there, but a duplicate contact is somebody declaring the same
contact twice rather than something the data model requires
([`NetBoxContact`](../reference/netboxcontact.md)).

Both halves are checked at build time rather than at admission, in
`internal/registry/duplicatespec_test.go`: for every registered kind, a non-empty
`DuplicateSpec` has to name a `boolean` property of that kind's generated CRD, and the kind's
natural key has to be one the schema digest records no `UNIQUE` constraint for. The check is
against the `Descriptor` and not against the user, so a wrong declaration fails `make test`
instead of somebody's manifest.
