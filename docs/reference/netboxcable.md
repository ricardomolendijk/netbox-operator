# `NetBoxCable`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCable` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcable` |
| Status subresource | yes |
| Lands with | NBO-049 (M10) |

A `NetBoxCable` is one `dcim.Cable` in NetBox: a physical link between two things that can be
plugged into.

It is the hardest kind in the catalogue, and all three of the reasons are worth knowing before
you write one.

**Its identity is a convention, not a constraint.** `dcim.Cable`'s entire `Meta` is
`meta.ordering: ('pk',)` — it has **no `meta.constraints` at all**
(`docs/netbox-schema.md` → `dcim.Cable`). There is no natural key on the cable row: not
`label`, which any number of cables may share. Identity lives in the terminations, and the
[natural key](#natural-key) below is the strongest question NetBox will answer rather than
something the database enforces.

**Its terminations cannot be PATCHed.** They are `dcim.CableTermination` rows rather than
columns of the cable, and `unique(termination_type, termination_id)` keeps the endpoint you
want occupied by the *old* cable until it is deleted. So changing either end means
[delete-then-create](#changing-a-termination-replaces-the-cable), with a visible gap. Every
other field is an ordinary PATCH.

**It is the first kind with a to-many polymorphic reference.** `aTerminations` and
`bTerminations` are bounded lists of a union, one member per legal target. The union itself is
the ordinary shape ([generic references](genericref.md#cableterminationtarget)); the list is
what is new.

## Minimal example

A cable needs both ends to exist. The two `NetBoxInterface` CRs are the prerequisite, and
`name` mode is the one the operator can wait on — apply all three at once in any order and it
converges.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCable
metadata:
  # A DNS-1123 label. A cable has no name in NetBox, so this is yours alone.
  name: sw1-eth0-sw2-eth0
  namespace: team-a
spec:
  endpointRef: homelab
  aTerminations:
    - interfaceRef:
        name: sw1-eth0
  bTerminations:
    - interfaceRef:
        name: sw2-eth0
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCable
metadata:
  name: sw1-eth0-sw2-eth0
  namespace: team-a
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly  (default Fail)
  deletionPolicy: Delete      # Delete | Retain           (default Delete)

  # Identity. Changing the membership of either list replaces the cable; reordering
  # entries within one changes nothing at all.
  aTerminations:
    - interfaceRef:
        name: sw1-eth0
        # Defaults to this object's own namespace. Crossing one needs a NetBoxRefGrant.
        namespace: team-a
  bTerminations:
    - interfaceRef:
        lookup:               # an interface NetBox already has, and no CR for
          device: sw2
          name: Ethernet1/1

  # Everything below is an ordinary PATCH.
  type: cat6
  status: connected           # default
  profile: single-1c1p
  label: patch-14
  color: 2196f3
  length: "3.5"
  lengthUnit: m
  tenantRef:
    name: infra
  bundleRef:
    name: riser-a
  description: Rack 3 to rack 4, top of rack
  comments: |
    Replaced 2026-04-02 after the original failed link training.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

### `spec.aTerminations`

| Type | list of [`CableTerminationTarget`](genericref.md#cableterminationtarget) |
|---|---|
| Required | **yes** |
| Validation | `minItems: 1`, `maxItems: 16`; `exactly one of interfaceRef, consolePortRef, consoleServerPortRef, powerPortRef, powerOutletRef, frontPortRef, rearPortRef, powerFeedRef or circuitTerminationRef must be set` per element |
| NetBox field | `a_terminations`, `GenericObjectSerializer(many=True)` (`netbox/dcim/api/serializers_/cables.py:40`) |

What the cable's A end is plugged into. A list, because NetBox 4.x permits several
terminations per end — a trunk lands on several connectors of one patch panel.

**Required and non-empty, which is stricter than NetBox.** An unterminated cable is legal
server state — `CableFilterSet.unterminated` exists to find them — and it is state this
operator cannot manage: a cable with no terminations has no natural key, so on the next
reconcile it cannot find itself. Refusing that at admission is more useful than creating an
anonymous row and then reporting `Ready=False, Reason=WaitingForKey` about it forever.

**A set, not a list.** Order inside it is not data. NetBox stores the elements as
`dcim.CableTermination` rows and returns them in
`('cable', 'cable_end', 'connector', 'pk')` order (`docs/netbox-schema.md` →
`dcim.CableTermination`, `meta.ordering`), so the operator sorts and deduplicates before
writing and compares as a set of pairs ([drift rule 9](../concepts/drift.md)). Reordering
entries produces **zero API writes** — no PATCH, no lookup change, no new
`status.lastAppliedHash`.

**Why 16.** `unique(cable, cable_end, connector)` on `dcim.CableTermination` allows one
termination row per connector per end, and the widest geometry `CableProfileChoices` knows is
`trunk-8c4p` — eight connectors (`netbox/dcim/choices.py:1764`). Sixteen is twice that, so a
cable that declares no `profile` still has room, and a manifest above it is a modelling
mistake rather than a cable. The marker is not optional either way: a list whose items carry
CEL rules and declares no `maxItems` makes the **whole CRD refused at install**
([a list needs a bound](../concepts/references.md#a-list-needs-a-bound)).

**If it is wrong.** An empty list, a missing list, more than 16 entries or an element with
zero or two members set is rejected by `kubectl apply` — admission, no condition. An element
naming a CR that does not exist reports `RefsResolved=False, Reason=RefNotFound` against the
**indexed** path, `aTerminations[0].interfaceRef`, and **nothing is written at all**: a cable
with one end resolved would be a half-cable, and correcting one means delete-and-create rather
than a PATCH. An element naming one of the eight Kinds that have not landed reports
`RefsResolved=False, Reason=RefKindUnavailable` in all four ref modes.

### `spec.bTerminations`

Identical to `aTerminations` in every respect, for the cable's other end
(`b_terminations`).

**The two ends are not interchangeable to the operator, even though they are to a cable.** A
manifest that swaps them describes the same physical link and asks a *different* natural-key
query — see [why swapping the ends is a 400](#why-swapping-the-ends-is-a-400).

### `spec.type`

| Type | `string`, enum: `""` plus the 33 `CableTypeChoices` values |
|---|---|
| Required | no |
| Default | none |
| NetBox column | `type`, `CharField len=50 choices=CableTypeChoices`, `blank=True, null=True` |

What the cable physically is: `cat6`, `mmf-om4`, `dac-passive`, `power`, `usb`. Not part of
the identity — changing it is a PATCH.

`""` is in the enum because it is how NetBox spells "unknown type" and the only way to clear
one that has been set. The field carries no absent-versus-empty note and cannot: an `enum` is
exactly the validation `TestClearableFieldsDocumentBothStatesInTheSchema` treats as forbidding
the empty value, so the note and the enum would contradict each other in the generated schema.
The empty member is the statement instead. It travels as `null` rather than as `""`
(`registry.Field.EmptyIsNull`), because NetBox's `ChoiceField` renders an empty choice as
`null` on read and a `""` compared against that `null` would be a diff that never settles.

The 33 values are checked against `hack/testdata/api-schema-4.6.8.json.gz` — machine-extracted
from `netbox/dcim/choices.py:1840` — by `TestCableEnumsMatchTheSchema`, so the marker is
compared with NetBox rather than with a transcription of NetBox.

**If it is wrong.** A value outside the enum is rejected by `kubectl apply`.

### `spec.status`

| Type | `string`, enum: `connected`, `planned`, `decommissioning` |
|---|---|
| Required | no |
| Default | `connected` |
| NetBox column | `status`, `CharField len=50 choices=LinkStatusChoices def=STATUS_CONNECTED` |

Whether the link carries traffic. `planned` documents a link that is not installed and
contributes no active `dcim.CablePath`.

Defaulted to NetBox's own default, so the operator manages the field from the first reconcile:
a defaulted field that never reaches a payload is a field the operator can never correct.

### `spec.profile`

| Type | `string`, enum: the 26 `CableProfileChoices` values |
|---|---|
| Required | no |
| Default | none |
| NetBox column | `profile`, `CharField len=50 choices=CableProfileChoices`, `blank=True` |

The connector-and-position geometry of a multi-strand cable, read as
`<connectors>C<positions>P`: `trunk-8c4p` is eight connectors of four positions each.

**There is no way to clear it once set, and that is a real limitation rather than an
oversight.** The column is `blank=True` and *not* `null=True`, so the empty value NetBox
stores is `""` while the value it returns for it is `null` — and a `""` sent against a `null`
read is a diff that never settles. On this kind that is worse than on any other: a permanent
diff on an `UpdateStrategy: Recreate` kind is a cable deleted and re-created on every resync.
So `""` is deliberately outside the enum. Set a profile or never set one; to remove one,
delete the cable and re-apply it without.

### `spec.tenantRef`

| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxTenant` |
|---|---|
| Required | no |
| NetBox column | `tenant`, `ForeignKey -> tenancy.Tenant on_delete=PROTECT` |

Who the cable belongs to. Not part of the identity and **not** the containment reference:
`PROTECT` means deleting the tenant is refused while a cable names it rather than cascading,
so an owner reference here would garbage-collect the CR while NetBox still held the row
([ownership](../concepts/ownership.md)).

### `spec.bundleRef`

| Type | [`ObjectRef`](../concepts/references.md) → [`NetBoxCableBundle`](netboxcablebundle.md) |
|---|---|
| Required | no |
| NetBox column | `bundle`, `ForeignKey -> dcim.CableBundle on_delete=SET_NULL` |

The bundle this cable is pulled with. `SET_NULL`, so deleting the bundle clears the column and
the next reconcile PATCHes it back — which is why this is an ordinary reference and not a
containment one.

### `spec.label`

| Type | `string`, up to 100 characters |
|---|---|
| Required | no |
| NetBox column | `label`, `CharField len=100` |

The cable's printed label. **Deliberately not part of the identity**: two cables may carry one
label, NetBox has no constraint saying otherwise, and changing a label is a PATCH rather than a
recreate — you do not unplug a cable to relabel it.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.color`

| Type | `string`, `^$|^[0-9a-f]{6}$` |
|---|---|
| Required | no |
| NetBox column | `color`, `ColorField` |

Six hexadecimal digits, no leading `#`.

Not defaulted, unlike a tag's: `dcim.Cable.color` carries no `def=` at all, so an uncoloured
cable is a real state and defaulting one would make the operator paint every cable it adopts.

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.length`

| Type | `string`, `^$|^[0-9]{1,6}(\.[0-9]{1,2})?$` |
|---|---|
| Required | no |
| NetBox column | `length`, `DecimalField decimal(8,2)`, nullable |

How long the cable is, in [`lengthUnit`](#speclengthunit).

**A string and not a number.** NetBox stores it as a `DecimalField` and returns it padded —
`"3.50"` for a spec that said `"3.5"` — and an OpenAPI `number` round-trips through IEEE-754
on its way in and out of the API server. The engine compares two numeric strings numerically
([drift rule 4](../concepts/drift.md)), so `"3.5"` and NetBox's `"3.50"` produce no PATCH and
`length: "3.5"` with `lengthUnit: m` round-trips without precision loss.

The pattern is the numeric-format rule read straight off `decimal(8,2)`: eight digits, two of
them after the point. The `^$` alternative is the clear.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. A cleared length is
written as `null` rather than as an empty string, which is what NetBox's nullable
`DecimalField` takes — DRF parses `""` as a number and rejects it
(`registry.Field.EmptyIsNull`).

`_abs_length` is NetBox's own normalisation of this value into one unit, and it is a
**read-only cache**: there is no spec field for it and the operator never writes it.

### `spec.lengthUnit`

| Type | `string`, enum: `""`, `km`, `m`, `cm`, `mi`, `ft`, `in` |
|---|---|
| Required | no |
| NetBox column | `length_unit`, `CharField len=50 choices=CableLengthUnitChoices`, nullable |

The unit `length` is expressed in. `""` clears it, and travels as `null`: NetBox declares this
`ChoiceField` `allow_null=True`, which is the difference from `profile`.

**NetBox's own `Cable.clean()` requires a unit whenever a length is set**, and clears the
length when the unit is removed. The operator does not duplicate that rule, so a length with
no unit is a `400` surfaced as `Ready=False, Reason=Invalid` carrying NetBox's own message —
which is easier to act on than an admission rejection would be, and cannot go stale against a
NetBox that changes the rule.

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description` (`PrimaryModel`), `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.comments`

| Type | `string`, unbounded |
|---|---|
| Required | no |
| NetBox column | `comments` (`PrimaryModel`), `TextField` |

Unbounded on purpose: the column is a `TextField`, so NetBox declares no length and a
`MaxLength` here would be a limit the operator invented.

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### What is deliberately absent

- **`connector` and `positions`.** Real columns of `dcim.CableTermination`, and
  **unreachable from the 4.6.8 REST API by any route**: the termination endpoint is read-only
  (below) and `GenericObjectSerializer` carries only `{object_type, object_id}`
  (`netbox/netbox/api/serializers/generic.py:15`). NBO-049 asked for them on each termination;
  there is no request that would set them.
- **`_abs_length`.** A denormalised cache NetBox maintains from `length` and `length_unit`
  (`docs/netbox-schema.md` preamble: every `_`-prefixed column is). Writing one is dropped
  silently, so a spec field for "length in metres" would be PATCHed forever.
- **`dcim.CablePath`, and any connectivity report in `status`.** NetBox recomputes every path
  traversing a cable whenever the cable changes, and every `PathEndpoint` caches a `_path` FK
  into it. The operator writes none of it and reports none of it; `dcim/connected-device` is a
  read-only view and is [excluded](../coverage.md).
- **`mark_connected`, `cable`, `cable_end`, `cable_connector`, `cable_positions` on a
  component.** `dcim.CabledObjectModel` columns NetBox sets itself when the cable is created,
  and `CabledObjectSerializer` declares `cable` and `cable_end` `read_only=True`
  (`netbox/dcim/api/serializers_/cables.py:110`).
- **`owner`.** `users.Owner` is an excluded endpoint, so nothing will ever write it
  ([coverage](../coverage.md)).
- **Inline `cables:` sugar on `NetBoxDevice`.** Out of scope for NBO-049 and untouched here.

### There is no `NetBoxCableTermination`

NBO-049 asked for one, "first-class so it can be a ref target and an owned child". There is
nothing for it to write.

`CableTerminationSerializer.Meta` sets `read_only_fields = fields`
(`netbox/dcim/api/serializers_/cables.py:71`), so **all twelve fields** of
`dcim/cable-terminations` — `cable`, `cable_end`, `termination_type`, `termination_id`,
`termination`, `connector`, `positions`, `created`, `last_updated`, `id`, `url`, `display` —
are refused on write. The endpoint is read-only, and it is
[excluded](../coverage.md) with that citation rather than implemented as a Kind that would
report itself synced while NetBox ignored every request.

Terminations are created and destroyed by writing the cable's own `a_terminations` /
`b_terminations`, which is what this Kind does. NetBox creates and deletes the rows to match.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | the representative termination of each end | `?termination_a_type=<type>&termination_a_id=<id>&termination_b_type=<type>&termination_b_id=<id>` |

One candidate, four filters, no null pin and **no fallback**. Every one of the four is
declared on `CableFilterSet` (`netbox/dcim/filtersets.py:2637`):

```
termination_a_type  MultiValueContentTypeFilter over terminations__termination_type
termination_a_id    MultiValueNumberFilter, method filter_by_cable_end_a
termination_b_type  MultiValueContentTypeFilter over terminations__termination_type
termination_b_id    MultiValueNumberFilter, method filter_by_cable_end_b
```

They are checked against NetBox rather than assumed, because django-filter drops a parameter
it does not recognise and answers with the **unfiltered** set — so a guessed filter name here
would be a lookup that matches the first cable in NetBox, on a kind that adopts what it finds.
The `_type` halves take the `app_label.model` string and not a `ContentType` id:
`MultiValueContentTypeFilter` splits on `.` and resolves through
`ContentType.objects.get_by_natural_key` (`netbox/utilities/filters.py:186-207`).

**A list has no single value a filter could take**, so the key is built from a *representative
element*: the first of each end's terminations after sorting by `(object type, id)`. Sorted, so
the query is the same one on every pass regardless of the order the manifest listed them in —
which is what makes "reordering produces zero API writes" true of the lookup as well as of the
diff. See [a natural key over a to-many
pair](../concepts/generic-refs.md#a-natural-key-over-a-to-many-pair).

**One element is enough**, and the reason is a constraint on the *other* model.
`unique(termination_type, termination_id)` on `dcim.CableTermination`
(`docs/netbox-schema.md` → `dcim.CableTermination`, `meta.constraints`) means an object is
terminated by **at most one cable, globally** — so an A-end termination names one cable or
none, and adoption by termination cannot pick the wrong cable. Both ends are filtered on
anyway: `termination_a_type` carries no `filter_by_cable_end_a` method of its own, so it is not
pinned to the A end on its own, and the four together are as narrow as the API gets.

**There is no weaker candidate to fall back to, and that is not a gap.** `label` is not unique,
and a candidate on it would adopt somebody else's cable. A cable whose terminations have not
resolved yet matches no candidate at all, and the engine waits — `Ready=False,
Reason=WaitingForKey` — rather than creating a second cable.

### What a `Conflict` means here

On every other kind, `Conflict` means two objects claim one unique key. Here **no constraint
backs the key at all**, so it means something narrower and more useful:

> some other cable is already terminated on the endpoint you asked for.

That is the `unique(termination_type, termination_id)` refusal, seen through the lookup. The
usual causes are an endpoint cabled by hand in the NetBox UI, a cable `netbox-populator` left
behind, or two `NetBoxCable` CRs naming one interface. `status.naturalKey` records exactly what
was searched for, which is most of the answer.

[`NetBoxContact`](netboxcontact.md) documents the same class of thing for a key backed by an
index; this is the case where the key is backed by nothing on its own model and borrows its
strength from a constraint one model over.

### Why swapping the ends is a 400

The A and B ends are symmetric to a cable and asymmetric to the lookup. A manifest that puts
`sw1-eth0` in `bTerminations` and `sw2-eth0` in `aTerminations` describes the same physical
link and asks a different query — so the lookup misses, the engine tries to create, and NetBox
refuses on `unique(termination_type, termination_id)`. That surfaces as
`Ready=False, Reason=Invalid` carrying NetBox's own message, and **not** as a duplicate cable.

A loud 400 is the honest cost of stating identity as a query rather than as a hash. NBO-049
proposed `cableKey() = sha256(sorted("type:id" for both ends))`, which would be
end-symmetric — but a hash cannot be sent to NetBox as a filter, so it could only ever have
been a local cache key on top of a query that still has to pick an end. If a cable reports
this, swap the ends in the manifest to match NetBox, or delete the NetBox cable and let the
operator create it.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

| Field | Type | Populated by | When |
|---|---|---|---|
| `id` | `int` | the create or adopt response | on every successful create, including the create half of a **recreate** — where it becomes the *replacement's* id |
| `naturalKey` | `map[string]string` | the lookup | before the result is looked at, and even when nothing matched |
| `lastAppliedHash` | `string` | the payload that was sent | after each successful write |

Two things about `status.id` on this kind specifically:

- **A recreate changes it.** The old cable is gone and the new one is a different row. An
  external system that recorded the id has to re-read it.
- **It is not cleared when the create half of a recreate fails.** It still names the object
  that was just deleted, and that is deliberate: the next reconcile's `GET` by that id returns
  404, the engine logs `netbox object is gone; clearing status.id`, falls through to the
  natural key — which now finds nothing, because the endpoints are free — and creates. So a
  half-finished recreate converges on its own, with no manual step. See
  [when the create half fails](#when-the-create-half-fails).

`dcim.Cable` is a `PrimaryModel`, so it mixes in both `TagsMixin` and `CustomFieldsMixin` and
is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set ([provenance](../operations/provenance.md)).

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Ready` | the cable exists in NetBox and matches the spec | anything below is unresolved, refused or failed | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `RefsResolved` | every termination, `tenantRef` and `bundleRef` became an id | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable`, `RefTypeNotAllowed`, `RefTargetFailed` |
| `Synced` | the last write succeeded, or there was nothing to write | drift was found and not written | `Synced`, `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `DriftDetected` | NetBox differs from the spec | it does not | `DriftDetected`, `DriftCorrected`, `NoDrift` |
| `Conflict` | another writer's stamp is on this cable | it is not | `ForeignCluster`, `ForeignOwner` |
| `Deleting` | the CR is being deleted and the cable is gone | the delete is blocked | `Protected`, `PendingDependents`, `APIError` |

There is no `ParentOwned` condition on this kind: it has [no containment
parent](#no-containment-parent-and-none-possible).

### Reason glossary

| Reason | Means | Retried |
|---|---|---|
| `Synced` | the cable exists and matches | — |
| `DriftCorrected` | the engine just wrote. On a **recreate** this is the reason, and the `Recreated` Event is what says the write was destructive | — |
| `WaitingForKey` | no natural-key candidate is usable: the terminations have not resolved, so there is no query to ask | every resync |
| `WaitingForRef` | a reference is declared and unresolved; `RefsResolved` names which | on the target's event, and every resync |
| `RefKindUnavailable` | a termination names one of the eight Kinds that have not landed | every 10 minutes |
| `Conflict` | some other cable already terminates on an endpoint this one asks for | every resync |
| `Invalid` | NetBox refused the body — a length with no unit, ends that do not match what NetBox holds — **or** the spec contradicts itself: `deletionPolicy: Retain` with a termination change | every resync |
| `Protected` | the delete was refused by NetBox | every resync |

### Retry intervals

The endpoint's `resyncPeriod` for everything except `RefKindUnavailable`, which is 10 minutes,
and a rate-limited or unreachable NetBox, which backs off. See [errors and
retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### Changing a termination replaces the cable

`UpdateStrategy: Recreate`, with `RecreateOn: ["a_terminations", "b_terminations"]` and nothing
else ([the Descriptor](../concepts/descriptor.md#updatestrategy-and-recreateon)). So:

| Edit | What happens |
|---|---|
| `label`, `length`, `type`, `status`, `profile`, `color`, `lengthUnit`, `tenantRef`, `bundleRef`, `description`, `comments`, tags, custom fields | one `PATCH`. No delete, no new `status.id` |
| adding, removing or changing an entry in either termination list | `DELETE` then `POST`. New `status.id`, `Recreated` Event |
| **reordering** entries within one list | nothing at all — zero API writes |

The order is `DELETE` then `POST`, and it cannot be the other way round:
`unique(termination_type, termination_id)` means the endpoint the replacement wants is still
occupied by the old cable, so creating first is impossible.

**This window is not atomic, and that is stated rather than hidden.** Between the delete and
the create the link is absent from NetBox: every `dcim.CablePath` that traversed it is gone,
and every downstream `PathEndpoint._path` is incomplete. NetBox rebuilds them on the create.
Two consequences worth planning for:

- **A recreate churns the change log.** One delete and one create per edit, plus NetBox's own
  path rebuilds.
- **Any end-to-end connectivity assertion is briefly false.** A test that checks paths after
  changing a cable must poll rather than read once.

The metric is `netbox_reconcile_total{result="recreated"}` — its own bucket, not `updated`
([observability](../operations/observability.md)).

### `deletionPolicy: Retain` refuses a recreate

`Retain` means "never destroy this NetBox object". A recreate destroys it, along with every
path through it. The two instructions contradict each other, so **the operator refuses rather
than picking one**:

```
Ready         False   Invalid
  spec.deletionPolicy is Retain and this change can only be applied by deleting and
  re-creating the object: b_terminations
```

Zero writes: the cable is still there afterwards, unchanged. The message names the field that
changed, because reverting it is half the fix; the other half is
`spec.deletionPolicy: Delete`.

The refusal is narrow. `Retain` blocks only the destructive path — relabelling a retained
cable is an ordinary PATCH and goes through.

> **Divergence from the ticket.** NBO-049 specifies `Ready=False, Reason=RecreateBlocked`. The
> reason vocabulary is closed and shared by every kind, and inventing a member for one kind's
> one case buys a word at the cost of a reason nothing else can ever set. `Invalid` is what
> "two fields of this spec contradict each other" already means, and the message carries which
> two. Likewise NBO-049's `Synced=True, Reason=Recreated`: the reason is `DriftCorrected` and
> the `Recreated` **Event** is what distinguishes a destructive write from a PATCH.

### When the create half fails

If the `POST` fails after the `DELETE` succeeded — NetBox rejected the body, or the request
timed out — the cable is gone and `status.id` still names it. The object reports
`Ready=False` with whatever the failure classified as (`Invalid` for a 400, `APIError` for a
5xx or a timeout).

**The next reconcile converges with no manual step**, and by the ordinary path rather than a
special case: `GET dcim/cables/<old id>` returns 404, the engine clears `status.id` and logs
`netbox object is gone; clearing status.id`, falls through to the natural key, finds nothing —
the endpoints are free now — and creates. Recreate is therefore idempotent, at the cost of a
visible gap.

### No containment parent, and none possible

[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 makes the containment parent
whichever foreign key NetBox cascades. A cable has none:

| Reference | `on_delete` | Why it is not the parent |
|---|---|---|
| `tenantRef` | `PROTECT` | deleting the tenant is refused, not cascaded |
| `bundleRef` | `SET_NULL` | deleting the bundle clears the column and keeps the cable |
| `aTerminations` / `bTerminations` | — | **cascades the wrong way** |

The last one is the interesting one. `dcim.CabledObjectModel.cable` is `on_delete=SET_NULL`
(`docs/netbox-schema.md` → `dcim.CabledObjectModel`), so deleting an interface does **not**
delete the cable plugged into it: it clears that interface's own denormalised `cable` column
while the cable and its `dcim.CableTermination` rows survive. An owner reference on
`aTerminations` would therefore garbage-collect the CR while NetBox still held the cable —
exactly the mistake [ownership](../concepts/ownership.md) refuses to make.

`dcim.CableTermination.cable` *is* `CASCADE`, but that is the cable deleting *its own*
terminations, which is the opposite direction and not a fact about this union. Every member
declares `CascadeOnDelete: false` explicitly rather than leaving it unstated, so this is a
recorded answer rather than an accident (`ErrMemberCascadePartial` makes it all-or-none).

Deleting a `NetBoxCable` CR removes the cable and its termination rows. **The far-end
interfaces survive**, with their `cable` columns cleared by NetBox.

### `deletionPolicy` defaults to `Delete`

A cable is a statement about a physical connection that the manifest is the record of, and
re-creating one loses nothing that was not in Git. Decision #176 gives `Retain` to the IPAM
kinds that hold *allocated* state — `NetBoxPrefix`, `NetBoxIPAddress`, `NetBoxIPRange`,
`NetBoxVLAN`, `NetBoxVRF` — and a cable is not one of them.

### Adoption

`onConflict: Adopt` takes over a cable `netbox-populator` or a human created on the same two
endpoints, rather than duplicating it — the lookup is by termination, so it finds the object
whatever its `label` or `type`. The adopted cable then gains the provenance stamp and drifts
into shape.

`onConflict: Fail`, the default, reports [`Conflict`](#what-a-conflict-means-here) and writes
nothing.

## Printer columns

```
NAME                 LABEL      TYPE   STATUS      ID   READY   AGE
sw1-eth0-sw2-eth0    patch-14   cat6   connected   7    True    4m
sw1-eth1-panel-1     patch-15   cat6   planned     8    False   4m
```

| Column | JSONPath |
|---|---|
| `LABEL` | `.spec.label` |
| `TYPE` | `.spec.type` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply`, `aTerminations` | none — admission | the list is absent or empty | Both ends are required with at least one entry; a cable with no terminations has no identity |
| Rejected by `kubectl apply`, an element | none — admission | zero or two members set on one termination | Exactly one of the nine `*Ref` members per element |
| Rejected by `kubectl apply`, `maxItems` | none — admission | more than 16 entries on one end | 16 is twice the widest `CableProfileChoices` geometry; a wider cable is a modelling mistake |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved` | a termination names a CR that is not Ready yet | Nothing to do — it converges on the target's event. `RefsResolved` names the indexed path |
| `Ready=False`, `Reason=RefKindUnavailable` | `RefsResolved` | a termination names a Kind this build does not carry (anything but `interfaceRef`) | Wait for the Kind, or point at the object with `lookup`/`id` on a member whose Kind exists |
| `Ready=False`, `Reason=WaitingForKey` | `Ready` | no termination resolved, so there is no query to identify the cable with | Fix the references; the key is built from them and from nothing else |
| `Ready=False`, `Reason=Conflict` | `Ready` | another cable already terminates on an endpoint this one asks for | `status.naturalKey` shows what was searched. Delete the other cable, or adopt it with `onConflict: Adopt` |
| `Ready=False`, `Reason=Invalid`, message names `deletionPolicy` | `Ready` | a termination changed on a cable with `deletionPolicy: Retain` | Either `deletionPolicy: Delete`, or revert the termination. Nothing was written |
| `Ready=False`, `Reason=Invalid`, NetBox's message names `length_unit` | `Ready` | `length` set with no `lengthUnit` | NetBox's `Cable.clean()` requires the unit. Set one |
| `Ready=False`, `Reason=Invalid` right after an edit, and the cable is missing from NetBox | `Ready` | the create half of a recreate failed | Nothing to do — the next reconcile clears the stale `status.id` and re-creates. Fix the cause if the create keeps failing |
| The cable is re-created on every resync | `DriftDetected` stays `True` | a field the operator writes and NetBox does not store the same way | Report it. Every comparison rule has a no-hot-loop regression test ([drift](../concepts/drift.md)), so this is a bug rather than a configuration |
| `profile` cannot be cleared | — | the column is `blank` and not nullable | Delete the cable and re-apply it without a profile |
| A connectivity check fails just after an edit | — | the recreate window: paths are rebuilt on the create | Poll rather than reading once |
| `Deleting=False`, `Reason=Protected` | `Deleting` | NetBox refused the delete | Read the message; something else references the cable |

## Related

- [`NetBoxCableBundle`](netboxcablebundle.md) — the bundle a cable is pulled with
- [Generic references](genericref.md#cableterminationtarget) — the union each termination is,
  and the three ways this one is unlike the others
- [Generic references (concept)](../concepts/generic-refs.md#a-to-many-pair) — why the union
  survived `dcim.Cable` and the Descriptor did not
- [Drift detection](../concepts/drift.md) — rule 9, and why a set comparison is load-bearing on
  a `Recreate` kind
- [The Descriptor](../concepts/descriptor.md#updatestrategy-and-recreateon) — `UpdateStrategy`,
  `RecreateOn` and `GenericFKList`
- [`NetBoxInterface`](netboxinterface.md) — the one termination target with a Kind today
- [`NetBoxContact`](netboxcontact.md) — the other kind whose lookup key no constraint backs
- [Ownership](../concepts/ownership.md) — why a cable takes no owner reference from its
  terminations
- [ADR-0003](../decisions/0003-ownership-and-references.md) — the containment rule this kind
  has nothing to satisfy
