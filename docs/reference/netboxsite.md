# `NetBoxSite`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxSite` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsite` |
| Status subresource | yes |
| Lands with | NBO-009 (M1) |

A `NetBoxSite` is one `dcim.Site` in NetBox: a physical location that devices, racks,
prefixes and VLANs are scoped to.

It is the second kind driven by the generic engine, and it exists partly to prove a claim.
`dcim.Site` has a **choice** column (`status`) and two **decimal** columns
(`latitude`, `longitude`) — the two value shapes that look like they need per-kind handling
and do not. Its descriptor declares no field class at all: NetBox returns a choice as
`{"value","label"}` and a decimal as a string padded to its `decimal_places`, and the
engine's existing comparison covers both. If either were wrong, the operator would find a
difference on every reconcile and PATCH forever.

**Its foreign keys are deliberately absent.** `dcim.Site` has optional references to a
region, a site group, a tenant and a list of ASNs. They are not in this CRD, because the
reference system does not exist yet ([NBO-011](https://github.com/ricardomolendijk/netbox-operator/issues/23),
[NBO-012](https://github.com/ricardomolendijk/netbox-operator/issues/24)). They are left out
rather than accepted and ignored — a field that does nothing is worse than a missing one,
because it looks like it works.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSite
metadata:
  name: home
  namespace: netbox-demo
spec:
  endpointRef: homelab
  name: Home
  slug: home
```

Everything else is optional and, if omitted, is left exactly as NetBox has it.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSite
metadata:
  name: home
  namespace: netbox-demo
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Home
  slug: home
  status: active              # planned | staging | active | decommissioning | retired
  description: Home lab
  facility: Spare room
  physicalAddress: |
    Example Street 1
    3011 Rotterdam
  shippingAddress: |
    Example Street 1
    3011 Rotterdam
  latitude: "51.9244"
  longitude: "4.4777"
  timeZone: Europe/Amsterdam
  comments: Managed by netbox-operator.
```

A runnable version, with its Secret and endpoint, is [`../examples/site.yaml`](../examples/site.yaml).

## `spec`

### `endpointRef`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Default | none |

The `NetBoxEndpoint` in **this namespace** to write through. There is no cluster-wide
default endpoint, deliberately — see [`NetBoxEndpoint`](netboxendpoint.md).

If it names an endpoint that does not exist or is not `Ready`, the site reports
`Ready=False, Reason=WaitingForEndpoint` and **no NetBox request is made**.

### `name`

| | |
|---|---|
| Type | `string`, max 100 |
| Required | **yes** |
| Default | none |

The site's display name. Column-unique in NetBox, but **not** the natural key — see
[Kind-specific behaviour](#kind-specific-behaviour).

### `slug`

| | |
|---|---|
| Type | `string`, max 100, pattern `^[-a-zA-Z0-9_]+$` |
| Required | **yes** |
| Default | none |

**The natural key.** This is how the operator finds the site in NetBox, and NetBox enforces
slug uniqueness **globally** — across every namespace in your cluster and across anything
else writing to the same NetBox. Two `NetBoxSite`s with the same slug in different
namespaces is a real and routine case; see [Kind-specific behaviour](#kind-specific-behaviour).

Rejected at admission if it does not match the pattern, so a bad slug never reaches NetBox.

### `status`

| | |
|---|---|
| Type | `string`, one of `planned` `staging` `active` `decommissioning` `retired` |
| Required | no |
| Default | `active` (`+kubebuilder:default=active`), which is also NetBox's own default |

A choice column. The API server rejects any other value at admission, from the CRD's enum,
so a typo fails your `kubectl apply` rather than surfacing as a NetBox 400 one reconcile
later.

Omitting it does **not** mean "do not manage the status": the field carries a default, so the
API server fills it in and the operator manages it from the first reconcile. A status set in
the NetBox UI is drift and gets corrected back to `active`. Set it explicitly to whatever the
site should be. (A defaulted field that never reached a payload would be a field the operator
could never correct, which is why it has one.)

### `description`, `facility`, `comments`

| | |
|---|---|
| Type | `string` — `description` max 200 |
| Required | no |
| Default | none |

Free text. `facility` is the building or room; `comments` is NetBox's long-form field.

Setting one of these to `""` clears it in NetBox; omitting the key leaves whatever NetBox
holds. Absent, empty and set are three states and the operator tells them apart from
`metadata.managedFields` — see [field ownership](../concepts/field-ownership.md), which also
covers the one case where an empty value is still invisible.

> `latitude` and `longitude` are clearable too, and are the one pair that reaches NetBox as
> `null` rather than as `""` — NetBox's nullable `DecimalField` rejects an empty string
> outright ([issue #170](https://github.com/ricardomolendijk/netbox-operator/issues/170)).

### `physicalAddress`, `shippingAddress`

| | |
|---|---|
| Type | `string`, multi-line |
| Required | no |
| Default | none |

Postal addresses. These are the two fields that earn the descriptor's explicit field table
rather than a camelCase-to-snake_case convention: they map to `physical_address` and
`shipping_address`, and **NetBox ignores a field name it does not recognise rather than
rejecting it** — so a wrong name produces a write that reports success and changes nothing,
forever.

### `latitude`, `longitude`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | none |

Decimal degrees. **Strings, not numbers**, because a JSON number cannot round-trip a
decimal without risking precision loss, and NetBox stores these as `DecimalField`s.

NetBox returns them padded to their `decimal_places` — write `"51.9244"` and read back
`"51.924400"`. The operator compares them numerically, so that padding does not read as a
change. If it did, this kind would PATCH on every reconcile.

`latitude: ""` clears the coordinate, and is sent to NetBox as `null`: the column is
nullable, and DRF parses `""` as a number and rejects it — so the empty string is the
spelling in YAML and `null` is what goes over the wire (`registry.Field.EmptyIsNull`).

### `timeZone`

| | |
|---|---|
| Type | `string`, an IANA zone such as `Europe/Amsterdam` |
| Required | no |
| Default | none |

Validated by NetBox, not by the CRD, so an invalid zone surfaces as
`Ready=False, Reason=Invalid` with NetBox's own message naming the field.

### `onConflict`

| | |
|---|---|
| Type | `string`, one of `Fail` `Adopt` `AdoptOnly` |
| Required | no |
| Default | `Fail` |

What to do when NetBox already holds a site with this slug. `Fail` reports `Conflict` and
writes nothing — silently taking over an object somebody else created is not a default worth
having. See [the engine](../concepts/engine.md).

### `deletionPolicy`

| | |
|---|---|
| Type | `string`, one of `Delete` `Retain` |
| Required | no |
| Default | `Delete` |

Whether deleting the CR deletes the NetBox site. See [deletion](../concepts/deletion.md).

Deleting a site that devices or prefixes still reference will be refused by NetBox with a
protected foreign key, reported as `Deleting=False, Reason=Protected` with the blockers
named. That is expected, not a bug: delete what references it first.

## `status`

| Field | Type | Meaning |
|---|---|---|
| `id` | `integer` | The NetBox object id. **Set only once the object provably exists server-side.** `0` means nothing was created. |
| `url` | `string` | The object's NetBox API URL, for jumping straight to it. |
| `naturalKey` | `object` | The query the operator used to look the site up. Recorded even when nothing matched, because the first question about an object that was not adopted is what was actually searched for. |
| `adopted` | `boolean` | Whether this site was taken over rather than created. |
| `lastAppliedHash` | `string` | Digest of the last payload written. Diagnostic only — it deliberately gates nothing, because skipping a PATCH on it would suppress exactly the drift correction the operator exists to perform. |
| `provenance` | `object` | The stamp this site carries in NetBox — the tag and the custom fields the engine wrote, as it wrote them. Unset when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is unset. `dcim.Site` is a `PrimaryModel`, so it carries both columns and is stamped in full. See [provenance](../operations/provenance.md). |
| `deletionAttempts` | `integer` | How many times a blocked delete has been retried, so backoff survives a restart. |
| `observedGeneration` | `integer` | The spec generation this status describes. |
| `conditions` | `[]Condition` | Below. |

## Conditions

| Type | `True` | `False` | `Unknown` |
|---|---|---|---|
| `Ready` | The site exists in NetBox and matches the spec | any failure — the `Reason` says which | — |
| `Synced` | The last comparison found no difference | drift was found and, in `DryRun`, not corrected | — |
| `RefsResolved` | All references resolved. Always `True` here, since this kind has none in scope | — | — |
| `Deleting` | — | the delete is blocked; `Protected` or `WaitingForEndpoint` | — |

Reasons you can see on `Ready`:

| `Reason` | Meaning | What to do |
|---|---|---|
| `Ready` | Created or updated successfully | nothing |
| `WaitingForEndpoint` | `endpointRef` names an endpoint that is missing or not `Ready` | fix the endpoint; `kubectl describe netboxendpoint` |
| `Conflict` | NetBox already holds a site with this slug, and `onConflict: Fail` | pick a different slug, or set `onConflict: Adopt` if it is yours |
| `Invalid` | NetBox rejected the payload (400) | read the message — it names the field. Retrying unchanged cannot help |
| `Ambiguous` | The lookup matched more than one NetBox object | the message names the ids; resolve it in NetBox |
| `Truncated` | The lookup paginated past the page cap | the filter did not apply, or the endpoint is enormous. Nothing was written |
| `NetBoxError` | A transient failure or a 5xx | it retries with backoff; check NetBox |

## Kind-specific behaviour

### `slug` is the identity, and it is global

The descriptor declares one natural-key candidate: `slug`. `name` is column-unique in
NetBox too and is deliberately **not** a candidate — a kind gets one identity, and `slug` is
the one the API calls the site's identifier.

The consequence is the accepted footgun of namespaced CRDs over globally-unique NetBox
columns: **two namespaces can both claim `slug: home`, and the first one to reconcile wins.**
The loser reports `Conflict` and writes nothing; the winner is unaffected. Neither corrupts
the other, and this is a tested behaviour rather than a surprise.

The `Conflict` message names the winning **NetBox id**, not the winning CR — the engine
reconciles one object at a time and has no cross-namespace view. To find the holder:

```sh
kubectl get netboxsite -A -o jsonpath='{range .items[?(@.status.id==7)]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
```

### The two value shapes this kind exercises

`status` is a choice and `latitude`/`longitude` are decimals, and **neither needs a field
class on the descriptor.** That is the substantive claim: a field class exists for a
difference the comparison cannot infer from the value — an order-independent id set, an
order-sensitive array — and a choice or a decimal is not one of those. See
[drift detection](../concepts/drift.md).

### What is not here yet

`region`, `group`, `tenant` and `asns` need the reference system (NBO-011, NBO-012). `tags`
needs that too — the field itself is described in
[the schema reference](../netbox-schema.md) now that NBO-073 emits it, but the CR still has
no way to name a `NetBoxTag`.
Custom fields need NBO-059.

## Printer columns

```
$ kubectl get netboxsite -n netbox-demo
NAME     SLUG     STATUS   ID   READY   AGE
home     home     active   7    True    4m
office   office   planned  8    True    4m
```

`nbsite` is the short name: `kubectl get nbsite`.

## Troubleshooting

| Symptom | Command | Cause and fix |
|---|---|---|
| `READY` empty, no conditions | `kubectl logs -n netbox-system deploy/netbox-operator-controller-manager` | No reconcile has run. Check the manager is alive and the CRD is installed |
| `Reason=WaitingForEndpoint` | `kubectl get netboxendpoint -n <ns>` | The endpoint is missing or not `Ready`. If its own reason is `SecretMissing`, the Secret may exist but lack the credential label — see [RBAC](../operations/rbac.md) |
| `Reason=Conflict` | the `jsonpath` above | Another namespace holds this slug. Pick another, or `onConflict: Adopt` |
| `Reason=Invalid`, message names `time_zone` | `kubectl describe netboxsite <name> -n <ns>` | Not a valid IANA zone. The CRD does not validate this; NetBox does |
| `status.id` is `0` and `READY` is `True` | — | Should be impossible. `status.id` is only set once the object exists; file a bug |
| Setting `description: ""` changes nothing | `kubectl get netboxsite <name> -n <ns> -o jsonpath='{.metadata.managedFields}'` | Nothing claims `description`, so the operator read the empty string as "not set". Either the key was deleted rather than emptied, or this object has no field-ownership metadata — see [field ownership](../concepts/field-ownership.md) |
| A delete hangs, `Reason=Protected` | `kubectl describe netboxsite <name> -n <ns>` | Devices or prefixes still reference the site. The message names them; delete those first |
| A delete hangs, `Reason=WaitingForEndpoint` | `kubectl get netboxendpoint -n <ns>` | The operator will not orphan a real NetBox object. Fix the endpoint, or annotate `netbox.kubeforge.org/skip-finalizer=true` to force it through and accept the orphan |

## Related

- [`NetBoxEndpoint`](netboxendpoint.md) — the connection this kind writes through
- [`NetBoxTag`](netboxtag.md) — the other kind in M1
- [The reconcile engine](../concepts/engine.md) — create, adopt and update
- [Drift detection](../concepts/drift.md) — why a choice and a decimal need no field class
- [Deletion](../concepts/deletion.md) — finalizers, `deletionPolicy`, `PROTECT`
- [The Descriptor](../concepts/descriptor.md) — how this kind is expressed as data
