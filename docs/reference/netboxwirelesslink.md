# `NetBoxWirelessLink`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxWirelessLink` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbwlink` |
| Status subresource | yes |
| Lands with | NBO-050 (M9) |

A `NetBoxWirelessLink` is one `wireless.WirelessLink` in NetBox: a point-to-point connection
between two wireless interfaces — a mesh AP with no wired uplink, a building-to-building
bridge — which NetBox models as an object of its own rather than as a field on either
interface.

> ### The first kind whose identity is a pair of references to one Kind, and it is **ordered**
>
> NetBox's single constraint is `unique(interface_a, interface_b)` with no expression and no
> conditional form, so Postgres stores `(a,b)` and `(b,a)` as two distinct rows: a link from A
> to B and a link from B to A are two objects to NetBox and one physical link to everybody else.
>
> The operator closes that with **two natural-key candidates — the declared orientation and its
> reverse** — and no engine code at all. See [natural keys](#natural-keys).
>
> ### And it has **no containment parent**, as a consequence rather than a gap
>
> All three of its writable foreign keys are `on_delete=PROTECT`. See
> [no containment parent](#no-containment-parent-and-nothing-resurrects).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLink
metadata:
  name: rtmap0003-backhaul
  namespace: default
spec:
  endpointRef: homelab
  interfaceARef:
    name: rtmap0003-wlan0
  interfaceBRef:
    name: rtmap0001-wlan1
```

Both references are **required**, and together they are the whole identity. There is no
minimal-but-unidentifiable shape here: a link with one endpoint is not a link.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLink
metadata:
  # A DNS-1123 label, and unrelated to spec.ssid below.
  name: rtmap0003-backhaul
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail
  deletionPolicy: Delete

  # The ordered pair, and the whole natural key.
  interfaceARef:
    name: rtmap0003-wlan0
  interfaceBRef:
    name: rtmap0001-wlan1

  # Optional here, unlike on a NetBoxWirelessLAN: the column is blank=True and a backhaul
  # link commonly has none.
  ssid: Donkersloot-Backhaul

  # NetBox's own default, written out.
  status: connected

  # on_delete=PROTECT. Holding this blocks deletion of that tenant in NetBox.
  tenantRef:
    name: donkerslootstraat

  # A string and not a number: NetBox stores decimal(8,2) and returns a string.
  distance: "12"
  distanceUnit: m

  authType: wpa-personal
  authCipher: aes

  # There is deliberately no authPSK; see NetBoxWirelessLAN.

  description: Mesh backhaul to the shed AP
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy`, the `driftMode` override, `tags` and
`customFields` come from the shared envelope and behave identically on every kind — see
[`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `interfaceARef` | [ref](../concepts/references.md) → `NetBoxInterface` | **yes** | — | `interface_a`, `ForeignKey REQ on_delete=PROTECT` |
| `interfaceBRef` | [ref](../concepts/references.md) → `NetBoxInterface` | **yes** | — | `interface_b`, `ForeignKey REQ on_delete=PROTECT` |
| `ssid` | `string` | no | — | `ssid`, `CharField len=32 blank=True` |
| `status` | enum | no | `connected` | `status`, `CharField len=50 choices=LinkStatusChoices` |
| `tenantRef` | [ref](../concepts/references.md) → `NetBoxTenant` | no | — | `tenant`, `ForeignKey on_delete=PROTECT` |
| `distance` | `string` | no | — | `distance`, `DecimalField(8,2)` from `DistanceMixin` |
| `distanceUnit` | enum | no | — | `distance_unit`, `CharField len=50` |
| `authType` | enum | no | — | `auth_type`, from `WirelessAuthenticationBase` |
| `authCipher` | enum | no | — | `auth_cipher`, from `WirelessAuthenticationBase` |
| `description` | `string` | no | — | `description`, `CharField len=200` |
| `comments` | `string` | no | — | `comments`, `TextField` |

### `spec.interfaceARef`, `spec.interfaceBRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxInterface`](netboxinterface.md) |
| Required | **yes**, both |
| Deferred | **no**, and none is possible |

The two endpoints (`netbox/wireless/models.py:138-149`), both
`ForeignKey -> dcim.Interface on_delete=PROTECT`.

Which interface is A and which is B is **NetBox's ordering rather than a physical fact**, which
is why the lookup also tries the reverse pair — see [natural keys](#natural-keys).

**Neither can be deferred, and that is enforced.** `validateDeferred` refuses to defer a field a
natural key matches on, and both references are matched on by both candidates — correctly: a
deferred reference is by construction unresolved when the lookup runs, and this kind's whole
identity is those two references.

`PROTECT`, so each reference blocks deletion of its interface in NetBox — and therefore of the
interface's device, except that `_interface_a_device` is `on_delete=CASCADE`
(`netbox/wireless/models.py:171-184`), which is exactly what makes a device deletion collect the
link first instead of hitting the `PROTECT`. That cascade is on a read-only cache column with no
spec field behind it, so it can contribute no owner reference; see
[no containment parent](#no-containment-parent-and-nothing-resurrects).

**If it is wrong:**

- Missing at `kubectl apply` → rejected at admission. Both are required.
- Unresolved (interface CR absent, not `Ready`, ambiguous, or a cross-namespace reference with
  no grant) → `RefsResolved=False` naming the field, **no lookup and no write at all**. Both
  halves of the key are these references, so no candidate is applicable and the engine waits
  rather than creating a link with an endpoint it could not name.
- Resolved to an interface that is not of a wireless *type* → NetBox's own `clean()` refuses it
  (`netbox/wireless/models.py:205-220`) and the object reports `Ready=False, Reason=Invalid` with
  NetBox's message verbatim. The operator does not pre-check interface types; the type list is
  NetBox's and would be a second copy of it.

### `spec.ssid`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=32` |

The network name carried over the link.

**Optional here where it is required on a [`NetBoxWirelessLAN`](netboxwirelesslan.md)**: the
column is `blank=True` (`netbox/wireless/models.py:150-154`), and a backhaul link commonly has
no SSID at all. It is also not part of the identity here — the interface pair is.

Three states: omit to leave NetBox's own value alone, set to `""` to clear it, set to a string
to write it ([field ownership](../concepts/field-ownership.md)).

### `spec.status`

| | |
|---|---|
| Type | `string` (`LinkStatus`) |
| Required | no |
| Default | `connected` |
| Validation | `Enum=connected;planned;decommissioning` |

Values read from `LinkStatusChoices` (`netbox/dcim/choices.py:1965-1975`). **A `dcim` enum on a
`wireless` kind**, because `wireless.WirelessLink.status` points straight at it
(`netbox/wireless/models.py:155-160`) — the same set `dcim.Cable` uses, which is why the Go type
is named for the concept (`LinkStatus`) and not for either kind.

Defaulted to NetBox's own default so the operator manages the column from the first reconcile: a
defaulted field that never reaches a payload is a field the operator can never correct. It
carries no omit-versus-empty note for the reason the default makes it never absent.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |

`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`
(`netbox/wireless/models.py:161-167`).

**Not part of the natural key**, unlike on a [`NetBoxWirelessLAN`](netboxwirelesslan.md): this
kind *has* a real uniqueness constraint and the interface pair is the whole of it, so there is
nothing for a tenant term to disambiguate.

### `spec.distance`, `spec.distanceUnit`

| | `distance` | `distanceUnit` |
|---|---|---|
| Type | `string` | `DistanceUnit` |
| Required | no | no |
| Default | — | — |
| Validation | `Pattern=^$\|^[0-9]{1,6}(\.[0-9]{1,2})?$` | `Enum=km;m;mi;ft` |

The span between the endpoints, from `DistanceMixin` (`netbox/netbox/models/mixins.py:77-91`).

**A string and not a `float64`**, for the reason [`NetBoxSite`](netboxsite.md)'s latitude is
one: NetBox stores it as `DecimalField(max_digits=8, decimal_places=2)` and returns it as a
string, and an OpenAPI `number` round-trips through IEEE-754 on its way in and out of the API
server. The engine compares it **numerically** (`scalarEqual`), so `"12"` and NetBox's `"12.00"`
are the same value and produce no `PATCH` ([drift detection](../concepts/drift.md)).

The pattern is `decimal(8,2)` written out: at most six integer digits and two fractional. It
admits **no sign**, and that bound is the operator's rather than NetBox's — a negative distance
is not a value NetBox rejects, it is a value NetBox would happily normalise into a negative
`_abs_distance` and sort by. Stated here rather than discovered.

`distance` is cleared as `null` and not as an empty string (`registry.Field.EmptyIsNull`),
because DRF parses `""` as a number and rejects it: `distance: ""` would be admission-legal and
fail on every write ([#170](https://github.com/ricardomolendijk/netbox-operator/issues/170)).
`distance_unit` is a char column and takes the empty string, so it needs nothing.

`distanceUnit` is meaningless without `distance`, and NetBox enforces that from its side by
nulling the unit on save whenever the distance is null (`netbox/netbox/models/mixins.py:115-117`).
Undefaulted: the column is nullable with no Django default, and there is no unit that is right
by default. Four values — two metric, two imperial (`netbox/netbox/choices.py:166-181`).

### `spec.authType`, `spec.authCipher`

The same two enums as [`NetBoxWirelessLAN`](netboxwirelesslan.md#specauthtype-specauthcipher),
from `WirelessAuthenticationBase` and declared once in `api/v1alpha1/wireless_auth.go`.

**There is no `authPSK` here either**, and for the same reason — see
[`NetBoxWirelessLAN`](netboxwirelesslan.md#there-is-no-authpsk-and-no-authpsksecretref-yet). No
spec field maps onto the column, so it cannot reach a payload, and it is already masked in every
log line.

### `spec.description`, `spec.comments`

Both have all three states ([field ownership](../concepts/field-ownership.md)).

## Natural keys

Two candidates: the pair, then the pair **reversed**.

| # | Candidate | Query |
|---|---|---|
| 1 | `(interface_a, interface_b)` as declared | `?interface_a_id=<A>&interface_b_id=<B>` |
| 2 | the same pair crossed | `?interface_a_id=<B>&interface_b_id=<A>` |

Candidate 2 filters `interface_a_id` from **`interfaceBRef`** and `interface_b_id` from
**`interfaceARef`**. Nothing about a `KeyField` requires the filter and the spec field to
correspond — `Filter` is the query parameter and `Spec` is where the value comes from — and
`declaresSpecField` still checks both names at boot, so a misspelling fails there rather than on
the wire.

Both filters are `ModelMultipleChoiceFilter`s on `WirelessLinkFilterSet`
(`netbox/wireless/filtersets.py:102-109`). No null pins and no fallback candidate: both fields
are required, so there is no state in which one is missing and a narrower identity applies.

### Why the pair is ordered, and what the reverse candidate buys

NetBox's single constraint is

```
UniqueConstraint(fields=('interface_a', 'interface_b'),
                 name='%(app_label)s_%(class)s_unique_interfaces')
```

(`netbox/wireless/models.py:190-195`; `docs/netbox-schema.md` → `wireless.WirelessLink`) — no
expression, no second conditional form. And `WirelessLink.clean` does not close the gap either:
it validates that both interfaces are of a wireless type and nothing else (`:205-220`).

So, four cases:

| What is in NetBox | Candidate | Outcome |
|---|---|---|
| this CR's own row, by `status.id` | none needed | reconciled as usual, whichever orientation it was created in |
| nothing | 1 finds nothing, 2 finds nothing | created as declared |
| the row, as declared, made by somebody else | 1 matches | the ordinary adoption rule: `Conflict` under the default `onConflict: Fail`, adopted under `Adopt` |
| **the row, reversed** | 1 finds nothing, 2 matches | the second CR sees the first CR's row instead of concluding no link exists. `Conflict` under the default, one orientation-normalising `PATCH` under `Adopt` |

**Without candidate 2 that last case is a silent duplicate**: the reverse-declared CR would look
up `(b,a)`, find nothing, and `POST` a second row for the same radio path — which NetBox's
ordered constraint is perfectly happy to store. One physical link, two NetBox rows, and two CRs
each reporting `Ready=True`.

### Why not canonicalise to ascending id

Always filtering and writing `min(a,b)` first was the other option, and it is worse two ways.
It would silently rewrite which endpoint the user called A. And two CRs declaring opposite
orientations would then match the **same** candidate, adopt each other's row, and `PATCH` the
pair back and forth on every resync. Two candidates keep the user's orientation and make the
collision loud.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status).

**Nothing is cleared on failure**, `status.id` included — which matters here more than on most
kinds: once the id is set the link is reconciled by it, and neither candidate is consulted
again, so an interface that later disappears cannot make the operator look for a different row.

`status.provenance` is stamped in full: `wireless.WirelessLink` is a `PrimaryModel`
([provenance](../operations/provenance.md)).

`status.naturalKey` records **which orientation** located the row. A
`{"interface_a_id": "10", "interface_b_id": "9"}` on a CR whose spec says A=9, B=10 is candidate
2 having matched — that is, somebody else recorded this link backwards.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the link exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | both interfaces and the tenant resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

**`ParentOwned` never appears**: this kind has no containment reference, so there is no owner
reference to report on.

Reason glossary and retry intervals are shared; see
[errors and retries](../concepts/errors-and-retries.md). The three that mean something
particular here:

- **`WaitingForRef`** on `Ready` naming `interfaceARef` or `interfaceBRef`: **zero writes and
  zero lookups**. Both halves of the identity are references, so the engine has nothing to look
  the object up by. Designed, not a stall.
- **`Conflict`** on `Ready`: a link between these two interfaces already exists — possibly in the
  other orientation. `status.naturalKey` says which. This is the acceptance criterion, not a
  malfunction.
- **`Invalid`** on `Ready`: NetBox's `clean()` refused it, almost always because an interface is
  not of a wireless type. The message is NetBox's own.

## Kind-specific behaviour

### Moving an endpoint is a `PATCH`, not a recreate

Both endpoints are plain foreign keys, unlike [`dcim.Cable`](netboxinterface.md)'s terminations,
so re-pointing one is an ordinary `PATCH`: `UpdateStrategy` stays `Patch` and there is no
`RecreateOn`. The row — and every reference anybody else holds to its id — survives.

NetBox still recomputes the wireless path and the two device caches on save, which is invisible
from here because those columns are never written.

### No containment parent, and nothing resurrects

All three of this kind's writable foreign keys are `on_delete=PROTECT`, and
`validateContainment` refuses a containment ref whose `Field.CascadeOnDelete` is false — naming
one here would fail the manager's boot.

There *is* a server-side cascade in the model, and it is worth writing down why it cannot be
used: `_interface_a_device` and `_interface_b_device` are `on_delete=CASCADE` to `dcim.Device`
(`netbox/wireless/models.py:171-184`), which is precisely how deleting a Device collects the link
instead of hitting the `PROTECT` on its interfaces. But they are caches NetBox recomputes in
`save()` (`:222-227`), they are in `ReadOnly`, and **no spec field maps onto either** —
and `ContainmentRef` names a *spec field*, because the owner reference is built from a resolved
reference's target CR. There is nothing to own.

**Nothing resurrects as a result**, which is the failure a missing containment parent usually
causes ([#203](https://github.com/ricardomolendijk/netbox-operator/issues/203)). Both candidates
match on both interface references, so once the device and its interfaces are gone neither
candidate is applicable, the engine has no identity to look up, and create-if-absent never runs.
The CR sits at `RefsResolved=False` naming the interface that disappeared — which is the correct
report.

### Three read-only columns that would `PATCH` forever

`_abs_distance` is derived from `distance` and `distance_unit` on every save
(`netbox/netbox/models/mixins.py:108-117`); `_interface_a_device` and `_interface_b_device` from
the two interfaces (`netbox/wireless/models.py:222-227`). Writing any of the three does not fail
— it silently no-ops, so the next reconcile finds the same difference and `PATCH`es again,
forever. All three are in the descriptor's `ReadOnly` list, and a test asserts none of them (nor
`auth_psk`) reaches a request body.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete`
([deletion](../concepts/deletion.md#the-two-policies)). Nothing points *at* a
wireless link, so the delete is never refused for that reason.

## Printer columns

```
NAME                 SSID                    A                 B                 STATUS      ID   READY   AGE
rtmap0003-backhaul   Donkersloot-Backhaul    rtmap0003-wlan0   rtmap0001-wlan1   connected   71   True    5m
```

| Column | JSONPath |
|---|---|
| `SSID` | `.spec.ssid` |
| `A` | `.spec.interfaceARef.name` |
| `B` | `.spec.interfaceBRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`A` and `B` read the spec's `name`, so both are empty for an interface referenced by `id` or
`lookup`. `status.naturalKey` is where the resolved ids are.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `interfaceARef` required | admission, nothing stored | one endpoint omitted | Both are required. A link with one endpoint is not a link |
| `kubectl apply` rejected, `distance` pattern | admission | more than two decimal places, a sign, or a unit in the string | `decimal(8,2)`, unsigned. The unit goes in `distanceUnit` |
| `Ready=False`, `Reason=WaitingForRef` naming an interface | reconcile, **zero writes and zero lookups** | the interface CR does not exist or is not `Ready` | Apply it; the link re-enqueues on the ref watch. Both halves of the identity are references, so nothing can happen first |
| `Ready=False`, `Reason=Conflict` | reconcile, zero writes | a link between these interfaces exists, possibly reversed | Read `status.naturalKey`: if the ids are crossed, somebody recorded it backwards. Delete the duplicate CR, or `onConflict: Adopt` to take the row over and normalise the orientation |
| `Ready=False`, `Reason=Invalid`, message about interface type | reconcile, long backoff | NetBox's `clean()` — an endpoint is not a wireless interface | Fix the interface's `type` in NetBox or on its CR. The operator does not carry NetBox's type list |
| Two rows in NetBox for one radio path | none — both CRs `Ready=True` | the rows predate this operator, or `onConflict: Adopt` on both orientations | Delete one row in NetBox. Then one CR adopts and the other reports `Conflict`, which is the state you want |
| `distance` keeps getting `PATCH`ed | `Synced` flapping | should not happen — the compare is numeric | Check that `distance` really is a string in the manifest. A YAML bare `12.5` is a number and the API server will reject it |
| `distanceUnit` cleared itself | none | NetBox nulls the unit whenever the distance is null | Expected. Set `distance` too |
| `_abs_distance` never matches the spec | none | it is server-maintained and `ReadOnly` | NetBox recomputes it in metres on every save |
| `ParentOwned` is absent from `status.conditions` | none | this kind has no containment reference | Correct — all three foreign keys are `PROTECT`. See [no containment parent](#no-containment-parent-and-nothing-resurrects) |
| Deleting a device left this CR behind, not `Ready` | `RefsResolved=False` | NetBox's `_interface_*_device` cascade removed the row; the CR has no owner reference to remove it | Delete the CR. Neither candidate is applicable, so nothing was re-created |
| The PSK is not being set | none | no spec field maps `auth_psk` | By design; see [`NetBoxWirelessLAN`](netboxwirelesslan.md#there-is-no-authpsk-and-no-authpsksecretref-yet) |

## Related

- [`NetBoxWirelessLAN`](netboxwirelesslan.md) — the SSID rather than the backhaul, the same two
  auth enums, and the PSK deferral in full
- [`NetBoxWirelessLANGroup`](netboxwirelesslangroup.md) — the SSID's group
- [`NetBoxInterface`](netboxinterface.md) — the endpoints, and where `wireless_link` will live on
  the interface side (NBO-053)
- [`NetBoxSite`](netboxsite.md) — the other decimal-as-string, and why
- [Lookups](../concepts/lookups.md) — how a natural key becomes a query string
- [Errors and retries](../concepts/errors-and-retries.md) — the reason vocabulary and the retry
  intervals
- [Drift detection](../concepts/drift.md) — the numeric compare `distance` goes through
- [Ownership](../concepts/ownership.md) — containment references, and a kind that has none
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — rule 4,
  and why `PROTECT` contributes no owner reference
