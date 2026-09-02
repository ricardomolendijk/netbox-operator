# `NetBoxIPAddress`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPAddress` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbip` |
| Status subresource | yes |
| Lands with | NBO-025 (M3) |

A `NetBoxIPAddress` is one `ipam.IPAddress` in NetBox: an address, its mask, and what it is
attached to.

Three things about this kind are unlike every kind shipped before it, and each is a way to be
subtly wrong:

- **`role` is a string here and a reference on its neighbours.** `ipam.IPAddress.role` is a
  `CharField` with choices, while `ipam.Prefix.role` and `ipam.VLAN.role` are
  `ForeignKey -> ipam.Role` (`docs/netbox-schema.md`). Same JSON key, different type, adjacent
  Kinds. So it is `role: vrrp` here and `roleRef: {name: ...}` there.
- **Host bits are preserved.** `10.0.20.1/24` is the point of an address record: it holds the
  host *and* the prefix the host sits in. [`NetBoxPrefix`](https://github.com/ricardomolendijk/netbox-operator/issues/36)
  masks; this Kind must not, and nothing here requires the host portion to be set either — a
  `/32` or a `/128` is how a loopback is recorded.
- **Its identity is a convention, not a constraint.** `ipam.IPAddress` has no
  `meta.constraints` at all (`docs/netbox-schema.md` lists only indexes). Whether a duplicate
  address is legal is NetBox's decision, taken from configuration this operator does not own —
  which is what [`allowDuplicate`](#allowduplicate) exists for.

Looking for "give me a free address"? That is `NetBoxIPAddressClaim`, a separate Kind
([ADR-0004](../decisions/0004-claims-first-allocation.md),
[NBO-036](https://github.com/ricardomolendijk/netbox-operator/issues/73)). This Kind states an
address; it never picks one. There is no `fromPrefixRef` and no allocation mode.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddress
metadata:
  name: gateway
  namespace: netbox-demo
spec:
  endpointRef: homelab
  address: 10.0.20.1/24
```

`status` defaults to `active`. Everything else is optional and, if omitted, is left exactly as
NetBox has it.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddress
metadata:
  name: vrrp-virtual
  namespace: netbox-demo
spec:
  endpointRef: homelab

  # Defaults, written out.
  onConflict: Fail
  deletionPolicy: Retain        # the default *on this Kind* -- see below
  status: active                # NetBox's own default

  address: 2001:db8:20::1/64
  role: vrrp
  dnsName: gw.home.arpa
  description: House gateway
  comments: |
    Shared between router-01 and router-02.

  # The VRF this address lives in. Omitting it is the global table, which is a different
  # identity rather than the same one with a filter left out.
  vrfRef:
    name: house

  # A VRRP virtual address exists once per participating router, so several NetBox objects
  # legitimately hold it. Needs the endpoint's spec.managedBy.
  allowDuplicate: true

  # The polymorphic (assigned_object_type, assigned_object_id) pair: at most one member, and
  # both columns are written together or neither is.
  assignedObject:
    interfaceRef:
      lookup:
        device: router-01
        name: vlan20

  # The address this one is the outside NAT address for. Self-referential.
  natInsideRef:
    name: gateway-inside
```

## `spec`

### `endpointRef`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1` |

The [`NetBoxEndpoint`](netboxendpoint.md) to write through, in this object's own namespace.
There is no cluster-wide default.

**If it is wrong:** `Ready=False, Reason=WaitingForEndpoint`, retried every 30s. Nothing is
written.

### `address`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=4`, `MaxLength=43`, `Pattern=^[0-9A-Fa-f.:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$` |

The address and its mask, IPv4 or IPv6. Written to NetBox exactly as given: the operator does
not mask, does not fill in a mask, and does not reject a set host bit.

The pattern is deliberately loose. It fixes the shape and the character set and leaves the
rest to NetBox, which validates an `IPAddressField` properly — a stricter regex here would be
a second, worse IP parser, and every disagreement between the two would reject an address
NetBox accepts.

**If it is wrong:** a malformed shape (`10.0.20.1`, `10.0.20.1/129`) is rejected by
`kubectl apply`. Anything the pattern allows and NetBox does not — `10.0.20.1/33`,
`2001:db8::gg/64` — reaches NetBox and comes back as `Ready=False, Reason=Invalid` carrying
NetBox's own message, retried on the endpoint's resync because retrying an unchanged payload
cannot succeed.

### `allowDuplicate`

| | |
|---|---|
| Type | `boolean` |
| Required | no |
| Default | `false` |

Declares that this address may legitimately exist in NetBox more than once, so several
objects matching the natural key stop being an error.

**Read [duplicate addresses](#duplicate-addresses) before setting it.** It changes this
object's identity, it requires the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby), and it turns off adoption of an
unstamped address.

**If it is wrong:** set on an endpoint with no `spec.managedBy`, it is
`Ready=False, Reason=Invalid` and **nothing is created** — not even a first copy, because an
object the operator cannot recognise again is one it would duplicate on the next reconcile
that lost `status.id`.

### `vrfRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxVRF` |
| Required | no |

The VRF this address belongs to. Unset means NetBox's global table, and that is a **different
natural key** rather than the same key with a filter omitted — see
[identity](#identity-and-the-two-natural-keys).

**If it is wrong:** `RefsResolved=False` naming the field, and `Ready=False,
Reason=WaitingForRef`. Because `vrfRef` is part of the identity, a declared-but-unresolved VRF
means no natural-key candidate applies at all, so the operator **waits** rather than creating:
falling through to the global-table candidate would adopt a different address that happens to
share the value. `NetBoxVRF` is [NBO-022](https://github.com/ricardomolendijk/netbox-operator/issues/34);
until it lands, `name` mode reports `RefKindUnavailable` and `slug`, `lookup` and `id` resolve
against NetBox directly.

### `status`

| | |
|---|---|
| Type | `string`, one of `active` `reserved` `deprecated` `dhcp` `slaac` |
| Required | no |
| Default | `active` |

The address's lifecycle state, from NetBox's `IPAddressStatusChoices`. Defaulted to NetBox's
own default so the operator manages the field from the first reconcile — a defaulted field
that never reaches a payload is one the operator can never correct.

**If it is wrong:** an unlisted value is rejected by `kubectl apply` with no controller
involvement.

### `role`

| | |
|---|---|
| Type | `string`, one of `""` `loopback` `secondary` `anycast` `vip` `vrrp` `hsrp` `glbp` `carp` |
| Required | no |
| Default | none |

What the address is for, from NetBox's `IPAddressRoleChoices`. **A string, not a reference** —
see the top of this page, and note that `NetBoxPrefix` and `NetBoxVLAN` will have `roleRef`
for the same JSON key.

Three-state like the other optional fields: omit it and NetBox's value is left alone, write
`role: ""` and it is cleared. The empty value is a member of the enum for exactly that reason
([field ownership](../concepts/field-ownership.md)).

Six of the eight roles — everything but `loopback` and `secondary` — exist *in order to* be
duplicated. They are the reason [`allowDuplicate`](#allowduplicate) exists, though setting one
does not imply it: NetBox does not treat a role as permission to duplicate, and neither does
the operator.

**If it is wrong:** an unlisted value is rejected at admission.

### `assignedObject`

| | |
|---|---|
| Type | `IPAssignment` — at most one of `interfaceRef`, `vmInterfaceRef`, `fhrpGroupRef` ([generic references](genericref.md)) |
| Required | no |
| Validation | CEL: `[has(self.interfaceRef), has(self.vmInterfaceRef), has(self.fhrpGroupRef)].filter(x, x).size() <= 1` |

What the address is attached to. One CR field writing the two columns
`assigned_object_type` and `assigned_object_id`, **atomically or not at all**: an id written
against the wrong type is not a partial update, it is a reference to a different object that
happens to share a primary key.

| Written as | Payload |
|---|---|
| omitted | neither column — spec omission means *do not manage* |
| `assignedObject: {}` | **both** columns set to `null`, clearing the assignment |
| one member | **both** columns, from one resolved reference |

`<= 1` rather than `== 1`, and none is legal: both columns are nullable, so an unassigned
address is an ordinary state. (The `REQ` the schema digest prints against the
`assigned_object` row is an extractor artefact — see
[the `REQ` trap](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest).)

**If it is wrong:** two members is rejected at admission. A member whose Kind this build does
not carry is `RefsResolved=False, Reason=RefKindUnavailable`, naming the field and the missing
Kind — the object is still created, with **neither** column written. All three target Kinds
are M4 ([NBO-029](https://github.com/ricardomolendijk/netbox-operator/issues/42),
[NBO-030](https://github.com/ricardomolendijk/netbox-operator/issues/43),
NBO-055), so today every mode reports `RefKindUnavailable`: `slug`, `lookup` and `id` need the
target's NetBox endpoint, which only its Descriptor holds.

### `natInsideRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxIPAddress` |
| Required | no |

The address this one is the outside NAT address for (`nat_inside`,
`on_delete=SET_NULL`). Self-referential, so two addresses applied together converge on the
second pass: the reference is left out of the create, reported on `RefsResolved`, and PATCHed
in once the target holds an id.

`nat_outside` is **not** a field here and never will be. It is the reverse accessor of this
foreign key, not a column — there is nothing to write and nothing to drift.

NetBox does not constrain the address families of a NAT pair, so a v6 `natInsideRef` on a v4
address (NAT64) is accepted. The operator adds no validation NetBox does not have.

**If it is wrong:** an unresolved target is `RefsResolved=False, Reason=RefNotFound` or
`RefNotReady` and `Ready=False, Reason=WaitingForRef`; the object is created without it. Two
addresses each naming the other is a ring: `Reason=RefCycle`, naming the path, on **both**
objects, with no retry — only an edit can clear it
([cycles](../concepts/references.md#cycles)).

### `dnsName`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=255` |

The hostname this address resolves to. Omit it to leave NetBox's own value alone; set it to
`""` to clear it ([field ownership](../concepts/field-ownership.md)).

### `description`, `comments`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `description`: `MaxLength=200`. `comments`: none — it is a `TextField` |

Inherited from `PrimaryModel`, which is why `docs/netbox-schema.md` lists them under the base
rather than under `ipam.IPAddress`. Both are three-state: omit to leave alone, `""` to clear.

### `onConflict`

| | |
|---|---|
| Type | `string`, one of `Fail` `Adopt` `AdoptOnly` |
| Required | no |
| Default | `Fail` |

What to do when NetBox already holds an object matching this one's natural key.

With `allowDuplicate` set this field is not consulted: identity is the provenance stamp, and a
match that is not this CR's is either somebody else's (create another) or unidentifiable
(refuse). To *adopt* a pre-existing address, leave `allowDuplicate` unset and use
`onConflict: Adopt`.

### `deletionPolicy`

| | |
|---|---|
| Type | `string`, one of `Delete` `Retain` |
| Required | no |
| Default | `Delete` |
| Validation | `Enum=Delete;Retain` |

Whether deleting the CR deletes the NetBox address.

**`Delete`, like every kind** since
[#304](https://github.com/ricardomolendijk/netbox-operator/issues/304), which reverses
[#176](https://github.com/ricardomolendijk/netbox-operator/issues/176). This kind used to
default to `Retain` on the argument that it holds state rather than configuration — deleting
an `ipam.IPAddress` frees the address for reallocation.

That cost is real and the default was still the wrong place to answer it: `Retain` deleted the
CR, kept the address, and left an unmanaged object that NetBox then cites with a `PROTECT` to
refuse the delete of the thing above it — with no CR left to fix. The risk is answered by
NetBox instead: the `DELETE` goes out, anything still referenced is refused, and the CR stays
saying what is in the way ([deletion](../concepts/deletion.md#why-this-reversed)).

Write `deletionPolicy: Retain` where this address should outlive its CR. It is one line, in
Git, where the next reader can see it.

There is no CRD default to read: `deletionPolicy` is declared once on the shared envelope
every object kind embeds, so `kubectl explain netboxipaddress.spec.deletionPolicy` describes the
field and prints no default.

## `status`

| Field | Type | Meaning |
|---|---|---|
| `id` | `integer` | The NetBox object id. Set only once the object provably exists server-side. `0` means nothing was created. **Not cleared on failure** — except when NetBox 404s it, which is how a deleted object gets recreated or re-adopted. |
| `url` | `string` | The object's NetBox API URL. Only overwritten when a response carries one. |
| `naturalKey` | `object` | The query that located the object, filter by filter — recorded even when nothing matched, and the first thing to read when asking why an address was adopted, or was not. |
| `adopted` | `boolean` | Whether this address was taken over rather than created. **False** for an address reclaimed by its provenance stamp: an object carrying this CR's own `metadata.uid` was created by this CR. |
| `provenance` | `object` | The stamp this address carries in NetBox. `ipam.IPAddress` is a `PrimaryModel`, so it is stamped in full — and under `allowDuplicate` the stamp *is* the identity. See [provenance](../operations/provenance.md). |
| `deferredPending` | `[]string` | Spec fields whose value NetBox does not hold yet. |
| `lastAppliedHash` | `string` | Digest of the last payload NetBox accepted. Diagnostic only. |
| `lastSyncTime` | `string` | When the engine last wrote. Untouched by a pass that found nothing to do. |
| `deletionAttempts` | `integer` | How many times a blocked delete has been retried, so backoff survives a restart. |
| `observedGeneration` | `integer` | The spec generation this status describes. |
| `conditions` | `[]Condition` | Below. |

## Conditions

| Type | `True` | `False` |
|---|---|---|
| `Ready` | The address exists in NetBox and matches the spec | any failure or wait — the `Reason` says which |
| `Synced` | The last comparison found no difference, or the difference was corrected | drift was found and not corrected (`DryRun`, `driftMode: Report`) |
| `RefsResolved` | Every reference resolved, or none was declared | a reference did not resolve; the `Reason` says why and the message names the field |
| `DriftDetected` | NetBox differs from the spec and nothing was sent | no difference, or it was corrected |
| `Deleting` | never `True` — the finalizer comes off as soon as the NetBox side settles | the delete is blocked; `Protected` or `WaitingForEndpoint` |

Reasons on `Ready`:

| `Reason` | Meaning | What to do |
|---|---|---|
| `Synced` | Created or updated successfully | nothing |
| `WaitingForEndpoint` | `endpointRef` is missing or not `Ready` | fix the endpoint. Retried in 30s |
| `WaitingForKey` | No natural-key candidate is usable — a declared `vrfRef` that has not resolved | resolve the VRF. Retried on the resync |
| `WaitingForRef` | A reference did not resolve; `RefsResolved` says which and why | see [stuck references](../operations/stuck-references.md) |
| `Conflict` | NetBox holds objects this CR cannot claim: several match its key, one matches and adoption was not asked for, or `allowDuplicate` is set and a match carries no stamp | the message names every candidate id. See [duplicate addresses](#duplicate-addresses) |
| `Invalid` | NetBox rejected the payload, or the spec cannot be turned into one — including `allowDuplicate` with no provenance stamp | read the message; retrying unchanged cannot help |
| `Truncated` | The lookup paginated past the page cap | narrow the filter or raise `maxPages`. Nothing was written |
| `APIError` | NetBox was unreachable, rate-limiting or failing | it retries with backoff |
| `DryRunPending` / `ReportPending` | The write was reported and not sent | expected on such an endpoint |

Retry intervals: 30s for a missing endpoint, 5s for a rate limit without `Retry-After`, 2m
after a 401/403, 10m after a truncated lookup, and the endpoint's own `resyncPeriod` for
everything a human has to fix. `RefCycle` and `RefTypeNotAllowed` have no timer at all —
nothing appearing anywhere clears them.

## Kind-specific behaviour

### Identity, and the two natural keys

`ipam.IPAddress` has **no** `meta.constraints`, so there is no database uniqueness to key on.
The candidates are a convention, tried in order:

| # | Filters | When it applies |
|---|---|---|
| 1 | `address`, `vrf_id=<id>` | `vrfRef` is set **and resolved** |
| 2 | `address`, `vrf_id__isnull=true` | `vrfRef` was never declared |

The second one **pins** `vrf_id` to null rather than omitting it. An omitted filter matches
more objects, not fewer: a global address would find the identical address in some VRF and
adopt it. See [lookups](../concepts/lookups.md).

A declared-but-unresolved `vrfRef` matches **neither** candidate, and the operator waits
(`WaitingForKey`). That is the correct outcome: candidate 2 asserts the address is in the
global table, which is not what the manifest says.

**The assignment is deliberately not a third, narrower candidate**, even though NetBox indexes
`(assigned_object_type, assigned_object_id)`. A natural key filters on one value per field and
a polymorphic pair offers none, so such a candidate would be
[refused loudly](../concepts/generic-refs.md) rather than sent as half an identity. Two
addresses that share an address and a VRF are told apart by `allowDuplicate` and the
provenance stamp instead.

### Duplicate addresses

Whether a second identical address is an error is **NetBox's** decision, and it is taken from
configuration this operator does not own: `ipam.VRF.enforce_unique` defaults to true
(`docs/netbox-schema.md` → `ipam.VRF`), and the global table depends on the instance-wide
`ENFORCE_GLOBAL_UNIQUE`. So the operator defers
(decision [#177](https://github.com/ricardomolendijk/netbox-operator/issues/177)).

**Without `allowDuplicate`** — the default:

- One match: adopted if `onConflict` permits it, `Conflict` if not.
- Several matches: `Ready=False, Reason=Conflict`, **zero writes**, and the message names
  every candidate id and NetBox's `display` for it. Not a count — the next step is a human
  choosing between them.
- Creating a duplicate in a VRF that enforces uniqueness is a NetBox **400**, which is
  `Reason=Invalid` with NetBox's own message. That is a different condition from `Conflict` on
  purpose: `Invalid` is "NetBox refused this write", `Conflict` is "the natural key matched
  more than one object", and they need different actions.

**With `allowDuplicate`**, the natural key gains the provenance stamp — the `k8s_uid` custom
field, which holds the CR's own `metadata.uid`, so exactly one row can be this CR's and it
says so on itself:

| What matched | What happens |
|---|---|
| nothing | create |
| one match carrying this CR's `k8s_uid` | that is ours: claimed, PATCHed if it drifted, **not** adopted |
| several carrying it | `Conflict`: two objects claim one identity, and the message says to delete all but one |
| none carries it, every match carries somebody else's | those belong to other CRs, so this CR's object does not exist yet: **create another** |
| none carries it, some match carries **no** stamp | `Conflict`, zero writes |

The last row is the one that had to be decided rather than derived. An unstamped match is an
address made before the operator or by another tool, and it may well be the one this CR meant;
creating a third copy beside it is the worst available outcome. The way out is in the message:
unset `allowDuplicate` and use `onConflict: Adopt` to take it over — once it is stamped, it is
identifiable.

`allowDuplicate` with no `spec.managedBy` on the endpoint is `Invalid` and writes **nothing**,
including the first copy. Without a stamp the operator could not recognise its own object
again, and the next reconcile that lost `status.id` would create another — which is the shape
of the hazard in [#167](https://github.com/ricardomolendijk/netbox-operator/issues/167).

### `role` is a string here and a reference next door

Stated at the top of this page and repeated here because it is the mistake to expect:

| Kind | Field | NetBox column |
|---|---|---|
| `NetBoxIPAddress` | `role: vrrp` | `CharField` with `IPAddressRoleChoices` |
| `NetBoxPrefix`, `NetBoxVLAN` | `roleRef: {name: mgmt}` | `ForeignKey -> ipam.Role` |

The engine already handles the difference without being told — a choice column is compared on
its `value` and a related field on its `id` — but a human reading two adjacent manifests will
not.

### Host bits, and masks the operator will not second-guess

`10.0.20.1/24` round-trips unchanged, and the second reconcile finds nothing to correct.
`NetBoxPrefix` masks its value; this Kind does not, and no rule requires the host portion to
be set: `10.255.0.1/32` and `2001:db8::1/128` are how loopbacks are recorded.

### What is not here yet

- **`tenantRef`.** `ipam.IPAddress.tenant` is a real column, and the field waits on
  [NBO-021](https://github.com/ricardomolendijk/netbox-operator/issues/33), which owns
  `TenantRef`. It is left out rather than accepted and dropped: a field that reports success
  and writes nothing is worse than a missing one.
- **`tags` and `customFields`.** Both columns exist on this model — the provenance stamp
  writes them — but no shipped Kind declares them as spec fields yet.
- **An `ASSIGNED` printer column.** Rendering `dcim.interface/42` needs the *resolved* pair,
  and a resolved generic FK is not recorded on the status; a JSONPath into the spec could only
  ever show one of the three members.
- **Owner references.** Deleting an interface in NetBox deletes its addresses server-side, so
  `assignedObject` should contribute a non-controller owner reference to keep the CR from
  outliving what it described. Nothing in the engine writes owner references yet, for typed
  references either.

## Printer columns

```console
$ kubectl get netboxipaddress -n netbox-demo
NAME           ADDRESS            VRF     STATUS   ROLE   DNS             ID   READY   AGE
gateway        10.0.20.1/24               active          gw.home.arpa    41   True    4m
vrrp-virtual   2001:db8:20::1/64  house   active   vrrp                   42   True    4m
loopback0      10.255.0.1/32              active   loopback               43   False   9s
```

| Column | JSONPath |
|---|---|
| `ADDRESS` | `.spec.address` |
| `VRF` | `.spec.vrfRef.name` |
| `STATUS` | `.spec.status` |
| `ROLE` | `.spec.role` |
| `DNS` | `.spec.dnsName` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`VRF` is blank for an address in the global table, and also for one whose `vrfRef` uses
`slug`, `lookup` or `id` mode — the column reads one JSON path, and `name` is the mode that
has one.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Nothing in NetBox, `Ready=False` | `Reason=WaitingForKey` | `vrfRef` is declared and has not resolved, so no candidate applies | `kubectl describe` the VRF, or the target it points at |
| Nothing in NetBox, `Ready=False` | `Reason=Conflict`, message names several ids | Several addresses match, and `allowDuplicate` is unset | Set `allowDuplicate` if the duplicates are intended, or resolve them in NetBox, or point at one with `onConflict: Adopt` |
| Nothing in NetBox, `Ready=False` | `Reason=Conflict`, message names one unstamped id | `allowDuplicate` is set and a match carries no provenance stamp | Unset `allowDuplicate` and set `onConflict: Adopt` to take it over |
| Nothing in NetBox, `Ready=False` | `Reason=Invalid`, message names `managedBy` | `allowDuplicate` on an endpoint that stamps nothing | Set [`spec.managedBy`](netboxendpoint.md#specmanagedby) on the endpoint, or unset `allowDuplicate` |
| `Ready=False` with a NetBox message about duplicates | `Reason=Invalid` | The VRF has `enforce_unique: true` and NetBox refused the create | The duplicate is not allowed by NetBox: change the VRF, or do not duplicate |
| Address created, `Ready=False` | `Reason=WaitingForRef`, `RefsResolved` names `assignedObject` | The target Kind does not exist in this build | Expected until M4. Use `slug`, `lookup` or `id` once the Kind is registered |
| Two objects, one address, both `Ready` | none | Two CRs with `allowDuplicate`, each holding its own stamped object | Working as intended. If it was not intended, unset the field on one and reconcile |
| `Ready=False` naming a ring | `Reason=RefCycle` | Two addresses each name the other in `natInsideRef` | Break the ring; NAT is directional |
| Deleting the CR left the address in NetBox | `Retained` Event | `deletionPolicy: Retain` is set on this CR | Remove it if freeing the address is what you want. `Delete` is the default |

## Related

- [Generic references](../concepts/generic-refs.md) — the `assignedObject` mechanism, the
  `app_label.model` spelling, and why the pair is atomic
- [`IPAssignment` and `ScopeRef`](genericref.md) — the union shapes in a spec
- [Lookups](../concepts/lookups.md) — why the global-table candidate pins `vrf_id` instead of
  omitting it
- [Field ownership](../concepts/field-ownership.md) — omitting `dnsName` versus clearing it
- [Provenance](../operations/provenance.md) — the stamp `allowDuplicate` turns into an
  identity
- [Deletion](../concepts/deletion.md) — the per-Kind `deletionPolicy` defaults
- [ADR-0004: claims-first allocation](../decisions/0004-claims-first-allocation.md) — why
  "allocate me an address" is a different Kind
