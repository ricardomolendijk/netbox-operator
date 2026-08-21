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
    Null("tenant_id")                          // ?tenant_id__isnull=true

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

`?vrf_id__isnull=true` and *no* `vrf_id` at all are different questions. The first asks
for the address that is in no VRF; the second asks for that address in any VRF.

`ipam.IPAddress` has no `meta.constraints` at all (`docs/netbox-schema.md` →
`ipam.IPAddress`), so `10.0.0.1/32` can exist once globally and once in every VRF. A
lookup that omits `vrf_id` matches all of them, and a global address adopts a copy out of
some VRF — or, when more than one comes back, fails as an
[`AmbiguousError`](errors-and-retries.md#why-ambiguity-is-an-error), which is the good
outcome of the two. The same reasoning covers the nested-group kinds, where
`name WHERE parent IS NULL` is a constraint of its own: an omitted `parent_id` makes every
top-level Region adopt an unrelated one, and the follow-up `PATCH` reparents it.

So `vrf_id` is always either matched against a value or pinned to null, never left out,
and the value NetBox wants for that pin is the literal `true`. `1`, `True` and an empty
string leave the filter silently unapplied, which looks exactly like the omitted case.
