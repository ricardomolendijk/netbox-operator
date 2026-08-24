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

## Duplicate addresses, and what `allowDuplicate` does to the natural key

**Decided** on
[#177](https://github.com/ricardomolendijk/netbox-operator/issues/177), and not built yet.

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
