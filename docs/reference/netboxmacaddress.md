# `NetBoxMACAddress`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxMACAddress` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmac` |
| Status subresource | yes |
| Lands with | NBO-048 (M9) |

A `NetBoxMACAddress` is one `dcim.MACAddress` in NetBox: an EUI-48 address, optionally attached
to a device interface or a virtual-machine interface.

NetBox 4.2 moved the MAC **off** the interface and into a model of its own.
`BaseInterface.primary_mac_address` is a `OneToOneField` *to* this model while
`MACAddress.assigned_object` points back at the interface, so one interface may hold many MACs
and designate one of them as primary (`docs/netbox-schema.md` → `dcim.MACAddress`,
`dcim.BaseInterface`).

> ### The narrowest union in the catalogue, and the first that is *not* a reuse
>
> `spec.assignedObject` is a polymorphic pair like
> [`NetBoxIPAddress.spec.assignedObject`](netboxipaddress.md#assignedobject) — same two typed
> refs, same machinery — with **two** members where that one has three.
> [`MACAssignment`](genericref.md#macassignment) is its own union rather than a reuse of
> `IPAssignment`, and the reason is a boot failure rather than tidiness: see
> [why this is not `IPAssignment`](#why-this-is-not-ipassignment).
>
> ### And NetBox enforces no identity for it whatsoever
>
> `dcim.MACAddress` declares no `meta.constraints` — only indexes. Two identical MACs on one
> interface are legal server state, so the natural key here is a **lookup convention** and more
> than one match is a `Conflict` rather than a guess. See [natural keys](#natural-keys).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxMACAddress
metadata:
  name: rtmap0003-wlan0
  namespace: default
spec:
  endpointRef: homelab
  macAddress: AA:BB:CC:DD:EE:FF
```

An **unattached** MAC. That is a legitimate shape rather than a half-filled one — both
assignment columns are nullable — and it is the state the second natural-key candidate exists
for.

The address must be written in NetBox's own canonical spelling: uppercase hex octets separated
by colons. See [`spec.macAddress`](#specmacaddress) for why a lowercase one is rejected at
`kubectl apply` instead of being normalised.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxMACAddress
metadata:
  # A DNS-1123 label, and unrelated to spec.macAddress below.
  name: rtmap0003-wlan0
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail
  deletionPolicy: Delete

  macAddress: AA:BB:CC:DD:EE:FF

  # At most one member. Omit the whole block for an unattached MAC; write it empty to
  # detach one. Half of this object's identity -- see natural keys.
  assignedObject:
    vmInterfaceRef:
      name: dns-eth0

  description: Mesh AP radio
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy`, the `driftMode` override, `tags` and
`customFields` come from the shared envelope and behave identically on every kind — see
[`NetBoxTag`](netboxtag.md#spec) for the full treatment of each.

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `macAddress` | `string` | **yes** | `mac_address`, `MACAddressField REQ` |
| `assignedObject` | [union](#specassignedobject) | no | `assigned_object_type` + `assigned_object_id`, both nullable |
| `description` | `string` | no | `description`, `CharField len=200` |
| `comments` | `string` | no | `comments`, `TextField` |

That is the whole of it. `dcim.MACAddress` is a `PrimaryModel`, so it carries `TagsMixin` and
`CustomFieldsMixin` and is stamped in full ([provenance](../operations/provenance.md)) — but it
has no name, no slug and no tenant of its own.

### `spec.macAddress`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Default | — |
| Validation | `Pattern=^[0-9A-F]{2}(:[0-9A-F]{2}){5}$` |

The EUI-48 address. Half of this object's identity.

**The pattern is narrower than what NetBox accepts on write, and that is the point.**
`dcim.MACAddressField.to_python` parses whatever `netaddr` can parse and stores
`EUI(value, version=48, dialect=mac_unix_expanded_uppercase)` (`netbox/dcim/fields.py:40-48`),
so every read comes back uppercase and colon-separated whatever was sent. The differ compares
strings and normalises no case ([drift detection](../concepts/drift.md)), so a spec holding
`aa:bb:cc:dd:ee:ff` would differ from NetBox's `AA:BB:CC:DD:EE:FF` on **every** pass and `PATCH`
forever without converging — while reporting `Ready=True` the whole time. The only symptom is
NetBox's changelog filling up.

Rejecting `aa-bb-cc-dd-ee-ff`, `aabb.ccdd.eeff` and lowercase at admission turns an invisible
hot loop into an error message:

```
The NetBoxMACAddress "wlan0" is invalid: spec.macAddress: Invalid value: "aa:bb:cc:dd:ee:ff":
spec.macAddress in body should match '^[0-9A-F]{2}(:[0-9A-F]{2}){5}$'
```

**If it is wrong:** admission rejects it and nothing is stored. There is no reconcile-time
failure mode for this field, because there is no spelling that passes the pattern and NetBox
then refuses.

### `spec.assignedObject`

| | |
|---|---|
| Type | [`MACAssignment`](genericref.md#macassignment) union |
| Required | no |
| Default | — |
| Validation | `[has(self.interfaceRef), has(self.vmInterfaceRef)].filter(x, x).size() <= 1` |

What the address is attached to, written as the `(assigned_object_type, assigned_object_id)`
pair and diffed as a unit — so moving a MAC from one interface to another is one change and one
`PATCH` carrying both keys ([why the pair is
atomic](../concepts/generic-refs.md#why-the-pair-is-atomic)).

| Member | Target Kind | NetBox object type |
|---|---|---|
| `interfaceRef` | [`NetBoxInterface`](netboxinterface.md) | `dcim.interface` |
| `vmInterfaceRef` | [`NetBoxVMInterface`](netboxvminterface.md) | `virtualization.vminterface` |

Both Kinds have a Descriptor as of NBO-030 and NBO-029, so the union resolves end to end in all
four reference modes.

`<= 1` and not `== 1`: both columns are `blank=True, null=True`
(`netbox/dcim/models/devices.py:1364-1374`), so an unattached MAC is a legal row. The `REQ`
printed against the `assigned_object` row above them in the schema digest is the extractor
artefact every generic FK has — a `GenericForeignKey` takes no `null=` kwarg — and must be
ignored ([the `REQ` trap](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest)).

The three states are the ordinary three, and on this kind the first two are not
interchangeable because the field is part of the identity:

| Written as | Means | Natural key used |
|---|---|---|
| omitted entirely | do not manage the attachment | candidate 2, which pins `assigned_object_id` null |
| `assignedObject: {}` | detach it — write both columns `null` | candidate 2 |
| one member set and **resolved** | attach it here | candidate 1 |
| one member set and **not resolved** | nothing is written at all | none applicable; the engine waits |

**If it is wrong:**

- Two members set → rejected at admission, `at most one of interfaceRef or vmInterfaceRef may
  be set`. Nothing is stored.
- The member's target does not exist, is not `Ready`, is ambiguous, or crosses a namespace with
  no grant → `RefsResolved=False` naming `assignedObject.interfaceRef` (the **member's** path),
  and — unlike on [`NetBoxIPAddress`](netboxipaddress.md) — **zero writes**. See
  [a declared-but-unresolved union writes nothing](#a-declared-but-unresolved-union-writes-nothing).
- Clearing the attachment of a MAC that is still some interface's `primary_mac_address` →
  NetBox refuses it (`netbox/dcim/models/devices.py:1406-1424`) and the object reports
  `Ready=False, Reason=Invalid` with NetBox's own message naming that interface. Not a silent
  no-op.

### `spec.description`, `spec.comments`

| | `description` | `comments` |
|---|---|---|
| Type | `string` | `string` |
| Required | no | no |
| Validation | `MaxLength=200` | — |

Both from `PrimaryModel`. `comments` is a `TextField` with no `max_length`, so there is no
`MaxLength` marker to derive.

Both have all three states: omit to leave NetBox's own value alone, set to `""` to clear it, set
to a string to write it ([field ownership](../concepts/field-ownership.md)).

### What is deliberately absent

| Column | Why |
|---|---|
| `primary_mac_address` | it is on the **interface**, not here. Modelling both directions as required references is the unresolvable cycle [NBO-016](../concepts/references.md#cycles) rejects; the reverse half is a deferred field on the two component specs and belongs to NBO-053. See [the reverse half](#the-reverse-half-is-not-here) |
| `is_primary` | a `cached_property` on the model, not a column (`netbox/dcim/models/devices.py:1399-1404`) |
| `owner` | `users.Owner` is an excluded endpoint ([coverage](../coverage.md)) |
| `tags` | the systematic gap NBO-073 covers, on every `TagsMixin` model |

## Natural keys

Two candidates:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(assigned_object_type, assigned_object_id, mac_address)` | `?assigned_object_type=<app.model>&assigned_object_id=<id>&mac_address=<mac>` | `assignedObject` **resolves** to a type and an id |
| 2 | `mac_address` where the assignment `IS NULL` | `?mac_address=<mac>&assigned_object_id__empty=true` | `assignedObject` was **never declared** |

### Neither is backed by a constraint

`dcim.MACAddress` has **no `meta.constraints` at all** — only two indexes, on
`(mac_address, id)` for the default ordering and on
`(assigned_object_type, assigned_object_id)` (`netbox/dcim/models/devices.py:1380-1385;`
`docs/netbox-schema.md` → `dcim.MACAddress`). Duplicate MACs are legal in NetBox, **including
two copies of one address on one interface.**

So the key above is a convention, and more than one match is a real server state rather than
proof of a mistake. The engine has exactly one answer for that and it is not per-kind: the
client returns an `AmbiguousError` naming every match, the engine reports
`Ready=False, Reason=Conflict`, and **nothing is written**
([why ambiguity is an error](../concepts/errors-and-retries.md#why-ambiguity-is-an-error)). The
same rule [`NetBoxIPAddress`](netboxipaddress.md) and
[`NetBoxVLANGroup`](netboxvlangroup.md#neither-candidate-is-guaranteed-unique) rely on.

There is no `allowDuplicate` here, unlike on `NetBoxIPAddress`: nothing about a MAC asks for
several rows to be legal *at once* under one CR's management.

### The filters are real, and that was checked

django-filter **ignores** a parameter it does not recognise and NetBox 4.6.8 has no strict-filter
validation, so a guessed filter name is a lookup that returns the *unfiltered* set — the engine
would adopt the first MAC in NetBox and `PATCH` it into this CR's shape
([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)):

| Filter | Declaration | Line |
|---|---|---|
| `mac_address` | `MultiValueMACAddressFilter()` | `netbox/dcim/filtersets.py:2030` |
| `assigned_object_type` | `MultiValueContentTypeFilter()` | `netbox/dcim/filtersets.py:2031` |
| `assigned_object_id` | `Meta.fields` | `netbox/dcim/filtersets.py:2086` |

`assigned_object_type` takes the `app_label.model` **string**, which the filter splits on `.`
and resolves through `ContentType.objects.get_by_natural_key`
(`netbox/utilities/filters.py:186-207`) — never a numeric `ContentType` id.

### The unattached candidate pins the id half only

Candidate 2 pins `assigned_object_id` and says nothing about `assigned_object_type`. That is
not an oversight — it is the same reading as
[`NetBoxVLANGroup`'s scope pin](netboxvlangroup.md#the-unscoped-candidate-pins-one-scope-column-not-both):

- `assigned_object_id` is a plain `PositiveBigIntegerField`
  (`netbox/dcim/models/devices.py:1371-1374`), so it gets the numeric lookup map and
  `?assigned_object_id__empty=true` resolves to the ORM's `isnull`.
- `assigned_object_type` is a `ForeignKey` to `contenttypes.ContentType` behind a
  `MultiValueContentTypeFilter`, for which NetBox registers **neither** `__empty` nor the `null`
  sentinel. `?assigned_object_type=null` is worse than dropped: it ends up as
  `assigned_object_type__in=[]` and the request matches *nothing at all*, so the engine would
  conclude the MAC does not exist and create a duplicate.

Pinning the paired `_id` asks the same question, because NetBox rejects one half of the pair
without the other.

### Why `mac_address` alone is not a third candidate

Because duplicate MACs *across* interfaces are the normal shape — a NIC replaced, a MAC cloned
onto a standby. `?mac_address=AA:BB:CC:DD:EE:FF` with the assignment merely omitted would match
every copy of the address in NetBox and report `Conflict` where the narrower candidate would
have found the one row this CR describes.

### The order is not a fallback chain

Candidate 2 is the identity of a *different* object — an unattached MAC rather than an attached
one. `NaturalKey.Applicable` matches only on a **resolved** field and pins only a
**never-declared** one, so exactly one candidate applies to any given spec.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared.

**Nothing is cleared on failure.** `status.id` in particular survives, which is what lets a
failing object keep reconciling by id rather than re-deriving an identity it cannot build.

`status.naturalKey` is the only place that records *which* identity was used, and on this kind
that is worth reading:
`{"assigned_object_type": "virtualization.vminterface", "assigned_object_id": "8", "mac_address":
"AA:BB:CC:DD:EE:FF"}` says the MAC was found as an attached object;
`{"mac_address": "…", "assigned_object_id__empty": "true"}` says it was found as an unattached
one. The `ASSIGNED` printer column reads out of here for the same reason.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the address exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefTypeNotAllowed`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the resolved union member's CR owns this one | it cannot | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`DeferredFieldPending` is absent from `Ready`'s list: this kind declares no deferred fields.
There is nothing to defer — `assignedObject` is matched on by candidate 1, and
`validateDeferred` refuses to defer a field a natural key matches on.

Reason glossary and retry intervals are shared across every object kind; see
[errors and retries](../concepts/errors-and-retries.md). The three that mean something
particular here:

- **`Conflict`** on `Ready`: more than one MAC matched. On this kind that is legitimate NetBox
  state — see [neither is backed by a constraint](#neither-is-backed-by-a-constraint).
- **`WaitingForRef`** on `Ready` with `assignedObject.<member>` named: the union is declared and
  unresolved, so **no lookup and no write happened at all**. Designed, not a stall.
- **`Invalid`** on `Ready` after detaching: NetBox refuses to unassign a MAC that is still an
  interface's `primary_mac_address`. The message is NetBox's own and names the interface.

## Kind-specific behaviour

### Why this is not `IPAssignment`

`ipam.IPAddress` carries a three-member union over the same two typed refs plus
`fhrpGroupRef`. Reusing it here and merely narrowing the pair's `AllowedTypes` would not work,
and the failure is not cosmetic.

NetBox restricts this column to two models:

```python
MACADDRESS_ASSIGNMENT_MODELS = Q(app_label='dcim', model='interface') | \
                               Q(app_label='virtualization', model='vminterface')
```

(`netbox/dcim/constants.py:156-159`, applied to the serializer's `assigned_object_type` queryset
at `netbox/dcim/api/serializers_/devices.py:318`.) `ipam.fhrpgroup` is legal for an IP address
and illegal for a MAC.

`Registry.validateUnionTypes` cross-checks every union member **whose Kind is registered**
against the pair's `AllowedTypes` and returns `ErrMemberTypeNotAllowed`, which fails the boot of
the whole manager rather than one kind. A shared three-member union with two allowed types would
pass today only because `NetBoxFHRPGroup` does not exist yet, and the day NBO-055 registers it
the operator would stop starting — for every kind.

What *is* reused is everything that matters: the two typed ref aliases, `GenericFKSpec`, the
resolver's dispatch table, the atomic pair, the ref watches, and the CEL shape. Only the member
set differs ([generic references](../concepts/generic-refs.md)).

### A declared-but-unresolved union writes nothing

This is the difference from [`NetBoxIPAddress`](netboxipaddress.md), which carries the same
union and still creates its row when a member does not resolve.

There, the assignment is *not* part of the natural key, so an unresolved member is simply left
out of the payload and filled in later. Here `(assigned_object_type, assigned_object_id,
mac_address)` **is** the identity, so a declared-but-unresolved union leaves no applicable
candidate and the engine has nothing to look the object up by.

Creating anyway would mean `POST`ing an unattached MAC and then attaching it — and for an
address NetBox does not police, that intermediate state is exactly how a duplicate row gets
made. So `status.id` stays `0` and no request is sent.

### `assignedObject` is the containment parent

Both members cascade, and by the *first* of the two mechanisms a generic FK can cascade
through: `mac_addresses` is a `GenericRelation` on `dcim.Interface`
(`netbox/dcim/models/device_components.py:966-971`) and on `virtualization.VMInterface`
(`netbox/virtualization/models/virtualmachines.py:507-512`), so deleting either interface
deletes its MAC addresses server-side. There is no denormalised `CASCADE` column to read as
well, because this model maintains no caches at all.

By [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 that makes `assignedObject`
the containment reference: the MAC CR carries a non-controller owner reference to whichever
member it actually resolved through, decided per pass rather than per Kind
([#214](https://github.com/ricardomolendijk/netbox-operator/issues/214)). It is the only
reference this kind has, so there is no tiebreak to make.

**It is load-bearing rather than tidy.** Candidate 2 stays applicable when the union is
undeclared, so a MAC CR outliving the interface NetBox cascade-deleted would find nothing on
`?mac_address=…&assigned_object_id__empty=true` and create-if-absent would recreate it —
unattached, which is not what anybody wrote. Exactly the resurrection
[#203](https://github.com/ricardomolendijk/netbox-operator/issues/203) found on
`NetBoxTenantGroup`.

An owner reference is only filed when it is legal — same namespace, and the member resolved by
`name` rather than by `id`. Otherwise the CR reports
`ParentOwned=False, Reason=CascadeUnavailable` naming `assignedObject`
([ownership](../concepts/ownership.md)).

### The reverse half is not here

`MACAddress.assigned_object` points at the interface; `BaseInterface.primary_mac_address` points
back at the MAC. That is the same chicken-and-egg as `Device.primary_ip4`, and it is resolved
the same way: **the MAC is created first with its `assignedObject`, and the interface side is a
deferred field** on the interface spec (NBO-053).

Modelling it as a mutual *required* reference would be an unresolvable cycle
([NBO-016](../concepts/references.md#cycles)) rather than a deferred field, which is why
`primaryMACAddressRef` is absent from [`NetBoxInterface`](netboxinterface.md) and
[`NetBoxVMInterface`](netboxvminterface.md) rather than stubbed. It is recorded as a
[coverage](../coverage.md) note, not as a silent omission.

### Renaming changes identity

Editing `spec.macAddress`, or moving `spec.assignedObject` to a different member, changes what
this CR *is*. Once `status.id` is set the object is reconciled by id, so the edit is a `PATCH`
onto the same row — the natural key is not consulted again. Before the first successful
reconcile, an edit is simply a different lookup.

## Printer columns

```
NAME               MAC                 ASSIGNED   ID   READY   AGE
rtmap0003-wlan0    AA:BB:CC:DD:EE:FF   8          71   True    5m
spare-nic          AA:BB:CC:DD:EE:02              72   True    5m
```

| Column | JSONPath |
|---|---|
| `MAC` | `.spec.macAddress` |
| `ASSIGNED` | `.status.naturalKey.assigned_object_id` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`ASSIGNED` reads `.status.naturalKey` rather than the spec on purpose. The spec's answer is a
union *member name*; the question a human is asking is which object the lookup actually resolved
to, and only the status knows that. It is empty for an unattached MAC, which is also how the
null-pinned candidate shows itself.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `macAddress` pattern | admission, nothing stored | lowercase, hyphens, or Cisco dotted-quad | Uppercase, colon-separated: `AA:BB:CC:DD:EE:FF`. See [`spec.macAddress`](#specmacaddress) |
| `kubectl apply` rejected, "at most one of interfaceRef or vmInterfaceRef" | admission | two members of `spec.assignedObject` | A MAC has one assignment. Pick one |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `assignedObject.<member>` | reconcile, **zero writes and zero lookups** | the interface CR does not exist or is not `Ready` | Expected while the interface is being created; the MAC re-enqueues on its own. Do **not** delete `assignedObject` to unblock it — that changes the object's identity |
| `Ready=False`, `Reason=Conflict` | reconcile, zero writes | more than one MAC matched | Legitimate: NetBox has no constraint here. `status.naturalKey` shows what was searched. Narrow it by attaching the MAC, or adopt deliberately by `id` |
| `Ready=False`, `Reason=Invalid` after emptying `assignedObject` | reconcile, long backoff | the MAC is still an interface's `primary_mac_address` | Clear it on the interface first, in NetBox. The message names the interface |
| `Ready=False`, `Reason=RefTypeNotAllowed` | reconcile, **terminal** | the member resolved to an object type this column refuses | Only `dcim.interface` and `virtualization.vminterface` are accepted here — an FHRP group is not, even though it is for an IP address |
| A second MAC row appeared | none | `spec.macAddress` or the union member was edited before the first successful reconcile | See [renaming changes identity](#renaming-changes-identity) |
| NetBox's description will not clear | none | the field was removed from the manifest rather than emptied | Absent means "do not manage". Write `description: ""` |
| `ParentOwned=False`, `Reason=CascadeUnavailable` | reconcile | the interface is in another namespace, or referenced by `id` | Expected for a shared interface. An owner reference cannot cross a namespace ([ADR-0003](../decisions/0003-ownership-and-references.md)) |
| The MAC CR outlived a deleted interface and a new row appeared | none | the owner reference was never filed — see the row above | Delete the MAC CR. The containment reference is what normally prevents this |
| `is_primary` never changes | none | it is a `cached_property`, not a column | NetBox derives it from the interface's `primary_mac_address` |

## Related

- [`MACAssignment`](genericref.md#macassignment) — the union's shape in a spec
- [Generic references](../concepts/generic-refs.md) — the mechanism, the `app_label.model`
  spelling rule, the `REQ` trap and why the pair is atomic
- [`NetBoxIPAddress`](netboxipaddress.md) — the same two typed refs in a wider union, and the
  kind whose unresolved assignment *does* still write
- [`NetBoxInterface`](netboxinterface.md) — the `dcim.interface` end, and where the reverse half
  of the pair will live
- [`NetBoxVMInterface`](netboxvminterface.md) — the `virtualization.vminterface` end
- [`NetBoxVLANGroup`](netboxvlangroup.md) — the other identity built on a polymorphic pair, and
  the same one-half null pin
- [Lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted) — why the
  assignment column is pinned rather than omitted
- [Errors and retries](../concepts/errors-and-retries.md#why-ambiguity-is-an-error) — why more
  than one match is an error rather than a guess
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Ownership](../concepts/ownership.md) — containment references and `CascadeUnavailable`
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — rule 4
  and the cascade
- [Coverage](../coverage.md) — `primary_mac_address` as a recorded gap rather than a silent one
