# `NetBoxIPRange`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPRange` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short name | `nbrange` |
| Status subresource | yes |
| NetBox model | `ipam.IPRange` (`ipam/ip-ranges`) |
| Lands with | NBO-064 (M6), pulled forward from NBO-055 |

A `NetBoxIPRange` is a run of consecutive addresses: `10.0.30.128/24` through
`10.0.30.191/24` is 64 of them. It is what you write down when *something else* hands those
addresses out — a DHCP server, a load balancer, another team's IPAM — so that this operator and
that something else do not both give out `10.0.30.150`.

It is **not** a prefix. A range need not be aligned, need not be a power of two long, and
cannot contain child prefixes. If the block you mean is a subnet, you want
[`NetBoxPrefix`](netboxprefix.md).

This kind is scheduled in NBO-055 and shipped early, because
[`NetBoxIPRangeClaim`](netboxiprangeclaim.md) creates `ipam.IPRange` objects and a kind whose
result cannot be reconciled declaratively is a result nobody can correct.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPRange
metadata:
  name: dhcp-clients
  namespace: homelab
spec:
  endpointRef: homelab
  startAddress: 10.0.30.128/24
  endAddress: 10.0.30.191/24
```

Deleting this CR leaves the NetBox range in place: `deletionPolicy` defaults to `Retain` on this
kind — see [`spec.deletionPolicy`](#specdeletionpolicy).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPRange
metadata:
  name: dhcp-clients
  namespace: homelab
spec:
  endpointRef: homelab
  deletionPolicy: Retain      # default *on this kind* -- see below

  startAddress: 10.0.30.128/24
  endAddress: 10.0.30.191/24

  vrfRef: {name: vrf-home}
  tenantRef: {name: household}
  roleRef: {slug: dhcp}

  status: active
  markPopulated: true
  markUtilized: false

  description: "DHCP scope handed out by the router"
  comments: |
    Kept in step with the router's own dhcp-range. Widen there first, then here.

  tags:
    - {name: managed}
  customFields:
    audit_ticket: "OPS-1183"
```

## `spec`

### `spec.startAddress`, `spec.endAddress`

Both required, both carrying a mask, and both masks must be the same one.

The mask is the *containing prefix's*, not `/32`. `10.0.30.128/24` is an address inside
`10.0.30.0/24` and its host bits are the whole point, which is why this kind carries none of
the masked-form validation [`NetBoxPrefix.prefix`](netboxprefix.md#specprefix) does: a value
that rule would accept is a value this field cannot use.

The end address is **inclusive**, so `.128` to `.191` is 64 addresses and not 63. Equal
endpoints are legal and mean a one-address range.

Four rules are NetBox's rather than the CRD's, and all four surface as
`Ready=False, Reason=Invalid` with NetBox's own words:

| Rejected by `IPRange.clean()` | Message |
|---|---|
| the two families differ | "Starting and ending IP address versions must match" |
| the two masks differ | "Starting and ending IP address masks must match" |
| the end is below the start | "Ending address must be greater than or equal to the starting address" |
| the range overlaps another in the same VRF | "Defined addresses overlap with range … in VRF …" |

None of them is re-implemented as CEL. The fourth cannot be — CEL cannot see the other ranges
— and a rule that caught three of four would read as if it caught all of them.

### There is no `spec.size`

`ipam.IPRange.size` is `editable=False` and computed in `IPRange.save()` as
`end - start + 1` (`netbox/ipam/models/ip.py`, NetBox 4.6.8). It is not in the write
serializer at all, so a `size` in a payload is dropped without complaint — and a `size` in a
drift comparison would be a PATCH that changes nothing, recomputed on every resync, forever.

The schema digest records it as `REQ` because the *column* is not nullable, which is the same
trap [the scope pair](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest) sets. The
two endpoints are the input; the count is NetBox's answer, and it appears in
`status.naturalKey` and in NetBox rather than in this file.

`size` is in the descriptor's `ReadOnly` list, which is what stops a future field map from
mapping onto it.

### `spec.vrfRef`

Part of this kind's identity, and more sharply than for a prefix. NetBox's overlap check is
`filter(vrf=self.vrf)`, so the *same* block of addresses may legitimately be a range once
globally and once in every VRF — and two ranges that overlap in different VRFs do not conflict
at all.

The natural-key lookup therefore either matches `vrf_id` against a value or
[pins it to null](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted), and
never omits it. Leaving `vrfRef` unset means the global table, which is a different range from
the same addresses inside a VRF rather than the same range with a field missing.

### `spec.tenantRef`, `spec.roleRef`

Ordinary references. `role` is `ipam.Role` — a real NetBox model with a slug — and is the same
object a prefix's `roleRef` points at.

### `spec.status`

One of `active`, `reserved`, `deprecated`, defaulted to `active`.

**There is no `container`.** `IPRangeStatusChoices` has three values
(`netbox/ipam/choices.py`, NetBox 4.6.8), and a range is not a container for anything: the
addresses inside it are inside it because of what they are. So the `status: container` guard
that stops a [`NetBoxIPAddressClaim`](netboxipaddressclaim.md#poolnotallocatable) allocating
out of a container prefix has no analogue here, and it is skipped rather than faked.

### `spec.markPopulated`

Stops NetBox creating `ipam.IPAddress` objects inside the range. Usually the point of
recording one: a DHCP scope's leases are not NetBox's to enumerate, and marking the range
populated says so rather than leaving a hole that looks free.

A pointer, so it has three states: absent leaves NetBox's value alone, `false` writes false,
`true` writes true. A plain `bool` could not tell "not managed" from "managed as false", and
adopting a range somebody had marked populated would silently clear it on the first reconcile.

### `spec.markUtilized`

Forces NetBox to report the range as 100% utilised. Three states, for the same reason.

It is also the one flag that stops a claim allocating an address out of this range: the flag
means the free space here is not really free, NetBox's `available-ips` hands one out anyway,
so honouring it is this operator's job.

### `spec.description`, `spec.comments`

Inherited from `PrimaryModel` and as writable as a declared column. Omit either to leave
NetBox's own value alone; set it to `""` to clear it. The two are different intents and the
operator can tell them apart ([field ownership](../concepts/field-ownership.md)).

### `spec.deletionPolicy`

Defaults to **`Retain`** on this kind (decision
[#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)). Deleting a range frees
every address in the block for reallocation at once and destroys the record of who it belonged
to, and that record is not recoverable by re-creating the object: the change log, the journal
entries and the id go with it, and a fresh range over the same addresses is a different object.
Set `deletionPolicy: Delete` explicitly where `kubectl delete` really should remove the range.

The default is not a CRD marker and cannot be — `deletionPolicy` is declared once on the shared
envelope, so a marker there is one answer for every kind, and redeclaring it here makes
controller-gen emit `allOf: [{default: Retain}, {default: Delete}]`, a schema the API server
rejects. It is data on this kind's Descriptor (`registry.Descriptor.RetainOnDelete`), so
`kubectl explain` prints no default. Same story as
[`NetBoxPrefix`](netboxprefix.md#specdeletionpolicy).

## Natural key

`ipam.IPRange` declares **no `meta.constraints` at all**. Its only table-level lines are an
ordering and two non-unique host indexes (`docs/netbox-schema.md` → `ipam.IPRange`). So the
natural key is the tuple NetBox's own `clean()` uses to reject a duplicate:

| Candidate | Filters | Applicable when |
|---|---|---|
| 1 | `start_address`, `end_address`, `vrf_id=<id>` | `vrfRef` is declared and resolved |
| 2 | `start_address`, `end_address`, `vrf_id__isnull=true` | `vrfRef` was never declared |

Two candidates rather than one with a fallback, for the reason
[`NetBoxPrefix`](netboxprefix.md#natural-keys) has two: a range in a VRF and the same addresses
in the global table are different objects, and candidate 2 asserts `vrfRef` was never declared
so that a range whose VRF has not been created yet matches *neither* and the engine waits.
Falling through would adopt the global range and then PATCH a VRF onto somebody else's row.

**The address filters are exact, and that was checked rather than assumed.** NetBox's
`IPRangeFilterSet.start_address` is a `MultiValueCharFilter` routed to `__net_in`, which reads
like a containment query — and `NetIn.as_sql` compiles a value *carrying a mask* to
`start_address IN ('10.0.30.128/24')` (`netbox/ipam/lookups.py`, NetBox 4.6.8), an equality
test including the mask. A value *without* a mask would instead match on `HOST()` alone, which
is a second reason `startAddress` requires the mask: the same string is the natural key and the
payload.

More than one match is a legitimate server state — the tuple is a convention, not a database
constraint — so it is reported as `Ready=False, Reason=Conflict` and nothing is written. Once
`status.id` is set the natural key is not consulted again.

## `status`

The standard [object status](../concepts/object-lifecycle.md). `status.naturalKey` records the
lookup that located the object, filter by filter, which is where the derived `size` becomes
visible from Kubernetes.

## Printer columns

```console
$ kubectl get nbrange -n homelab
NAME           START            END              VRF        STATUS   ID   READY   ADOPTED   AGE
dhcp-clients   10.0.30.128/24   10.0.30.191/24   vrf-home   active   31   True    false     4m
```

| Column | JSONPath |
|---|---|
| `START` | `.spec.startAddress` |
| `END` | `.spec.endAddress` |
| `VRF` | `.spec.vrfRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `ADOPTED` | `.status.adopted` |
| `AGE` | `.metadata.creationTimestamp` |

There is no `SIZE` column, for the reason there is no `size` field: it would read the spec, and
the spec does not have it.

## Ownership

No owner reference, and no `ContainmentRef` on the descriptor. Every foreign key
`ipam.IPRange` has is `PROTECT` or `SET_NULL` (`docs/netbox-schema.md` → `ipam.IPRange`), so no
parent's deletion cascades to a range and there is no server-side cascade for an owner
reference to mirror ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

A range created by a [`NetBoxIPRangeClaim`](netboxiprangeclaim.md) is a different matter: the
claim retains it, and nothing in Kubernetes owns it until the child materialiser lands.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `READY` False, message names an overlap | `Reason=Invalid` | another range in the same VRF covers some of these addresses | narrow this range, or find the other one with `?contains=` |
| `READY` False, "masks must match" | `Reason=Invalid` | `startAddress` and `endAddress` carry different prefix lengths | use the containing prefix's mask on both |
| `READY` False, "must be greater than or equal" | `Reason=Invalid` | the endpoints are the wrong way round | swap them |
| `READY` False, `ID` empty, more than one match | `Reason=Conflict` | two ranges in NetBox share this `(start, end, vrf)` | delete the duplicate in NetBox, or point `spec` at the one you meant |
| a range keeps being PATCHed with nothing changing | — | not this kind: `size` is read-only and never diffed | report it, because it should be impossible |
| `kubectl apply` rejected: "must be an address with a mask" | — | a bare `10.0.30.128` | add the containing prefix's mask |

## Related

- [`NetBoxIPRangeClaim`](netboxiprangeclaim.md) — reserve a range without naming its addresses.
- [`NetBoxPrefix`](netboxprefix.md) — the kind for a subnet, and the pool a range claim carves
  out of.
- [Lookups](../concepts/lookups.md) — why `vrf_id` is pinned to null rather than omitted.
- [Field ownership](../concepts/field-ownership.md) — absent versus empty.
