# Field ownership: absent, empty, set

An optional field in a NetBox object's spec has three states, not two:

| State | You write | The operator does |
|---|---|---|
| **absent** | nothing | leaves the NetBox value exactly as it is |
| **empty** | `description: ""` | writes the empty value, clearing NetBox's |
| **set** | `description: "core switch"` | writes that value |

The middle row is the one that needs explaining, because a Go struct cannot tell it apart
from the first. `description: ""` and no `description` at all decode to the same empty
string, so the value carries no evidence of which one you meant. The operator does not read
the value to find out. It reads `metadata.managedFields`, which is where the API server
records, per field, which client last set it.

## Expressing each state

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTag
metadata:
  name: spine
spec:
  endpointRef: homelab
  name: Spine
  slug: spine

  # SET: the operator writes this and corrects it if somebody edits it in NetBox.
  description: core switches

  # ABSENT: `objectTypes` is not here at all, so whatever restriction NetBox holds
  # stays. Delete the key to hand the field back.

  # EMPTY: write the empty value to clear NetBox's. For a string that is "", for a
  # list [], for a map {}.
  # objectTypes: []
```

Both of the last two are ordinary YAML. There is nothing to annotate and no list of
managed fields to maintain.

A field's own `kubectl explain` says which of the three it supports:

```console
$ kubectl explain netboxtag.spec.description
DESCRIPTION:
    Description is free text shown next to the tag.

    Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
    NetBox. The two are different intents and the operator can tell them apart
    (docs/concepts/field-ownership.md).
```

A field with no such sentence is one where the distinction does not arise — because it is
required, because it carries a default so it is never absent, or because its validation
rejects the empty value.

## The exception that keeps its own rules: `customFields`

`spec.customFields` has three states like everything above, and it reaches them by a
different mechanism: **per key, inside the map**, rather than per field through
`managedFields`.

| State | You write | The operator does |
|---|---|---|
| **container absent** | no `customFields` at all | manages no custom field |
| **container empty** | `customFields: {}` | manages no custom field |
| **key set** | `audit_ticket: NET-42` | writes that value and corrects it |
| **key emptied** | `audit_ticket: ""` | writes the empty string |
| **key removed** | `audit_ticket: null` | removes that custom field's value |

`null` and `""` are different requests and both are expressible, which is the whole point of
the map's values being nullable in the CRD schema:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSite
metadata:
  name: home
spec:
  endpointRef: homelab
  name: Home
  slug: home
  customFields:
    owner_team: ""       # set this custom field to the empty string
    audit_ticket: null   # remove this custom field's value
    # rack_position is not here at all, so whatever NetBox holds for it stays
```

### Why the container is not exhaustive

`customFields` names the keys to manage rather than the container's whole contents, and the
two rows that look like they should clear everything deliberately do not.

NetBox returns *every* custom field defined for an object type, including ones this operator
knows nothing about, and it **merges a partial `custom_fields` PATCH** rather than replacing
the container. So the operator compares only the keys it sets (`customFieldsEqual`,
`internal/netbox/drift.go`). Treating the map as exhaustive would make it null out every
custom field some other writer on that NetBox owns, on every reconcile — which is the same
fight [ADR-0005](../decisions/0005-gitops-coexistence.md) exists to avoid, in a container
where the other writer is often a human.

That is why removal is said *inside* the map. A per-key null keeps one field describing one
NetBox column and leaves every other key alone, where a second `removeCustomFields` list
would be two declarations of one thing that can disagree
([issue #196](https://github.com/ricardomolendijk/netbox-operator/issues/196)).

### What `null` does to NetBox

NetBox merges the container key by key on write and stores the null as the custom field's
absent value: `CustomFieldsDataField.to_internal_value` overlays the submitted map on the
stored one, and `CustomField.validate` returns immediately for `None`. It is not defaulted
back either — `save()` re-applies a custom field's default only for a key that is *missing*
from the container, and this one is present holding null. On read,
`CustomFieldsDataField.to_representation` answers every defined custom field as
`cf.deserialize(...)`, which passes `None` straight through.

So a removed value reads back as an explicit `null`, indistinguishable from a custom field
that was never set — which is what "removed" means at this API. The comparison then finds the
null it asked for, so the removal drifts exactly once and settles rather than PATCHing
forever (`TestCustomFieldRemovalSettles`, `internal/netbox/drift_test.go`;
`TestServerSideApplyCanRemoveACustomField`, `internal/controller/fieldownership_test.go`).

`""` is a different stored value that reads back as `""`, so asking for one when NetBox holds
the other is drift. Collapsing the two would make one of them unsayable.

### Emptying the container is not clearing it

`customFields: {}` manages nothing. It has to: field ownership restores a claimed-and-emptied
map as `{}`, and reading that as "clear everything" would be exactly the fight above. An
empty container sends no `custom_fields` at all.

One wrinkle, and it is Kubernetes' rather than this operator's: **server-side apply cannot
turn an owned non-empty map into `{}`.** The merge yields `null` for the emptied map and the
API server rejects it — `spec.customFields in body must be of type object: "null"`. It
applies to any `map[string]…` spec field, not only this one, so the table near the top of
this page is optimistic about `{}` in that one transition. To stop managing custom fields you
have already set, delete the `customFields` key from the manifest: that is the **absent**
state, it is what the three-state rule is for, and NetBox keeps whatever it holds.

### It is on every Kind at once

`customFields` lives on the shared envelope (`NetBoxObjectSpec`), like `endpointRef`, not in
any kind's field map — NetBox's `custom_fields` is not a per-kind column but the same
container under the same name on every model that has it. So `kubectl explain` shows
`spec.customFields` on every object kind the moment it exists, including the ones whose NetBox
model has no such column. `NetBoxTag` is the case: `extras.Tag` mixes in no
`CustomFieldsMixin`, so setting `customFields` on one is refused with `Ready=False`,
`Reason=Invalid` rather than silently dropped.

Nothing else writes this container. The provenance stamp's own keys are overlaid on top of
whatever the spec set, and are reported in `status.provenance.customFields`.

## How the operator knows

`metadata.managedFields` is Kubernetes' own record of who set what. It looks like this on a
tag applied by Flux:

```yaml
metadata:
  managedFields:
  - manager: kustomize-controller
    operation: Apply
    fieldsV1:
      f:spec:
        f:endpointRef: {}
        f:name: {}
        f:slug: {}
        f:description: {}     # claimed, so the operator manages it -- even when empty
  - manager: netbox-operator
    operation: Update
    subresource: status       # the operator's own writes, never under f:spec
    fieldsV1:
      f:status: {}
```

Every entry that is not the operator's own is somebody stating an intent about a spec
field. The operator takes the union of them, and manages exactly those fields plus any
field that holds a non-empty value. That is why the operator writes with one fixed field
manager name, `netbox-operator`: "not the operator" has to stay decidable.

Nothing about this is per-kind. The engine reads the field names out of the metadata and
matches them against whatever JSON names the spec has, so a kind added tomorrow gets the
three states without a line of code.

### Why not pointer types

The other way to express three states in Go is `*string`, `*int32`, `*bool` on every
optional field. It works, and it was the recommendation until it was costed: it makes every
optional field on ~120 kinds a pointer, every reader of one a nil check, and every manifest
example harder to read — to re-derive information the API server is already tracking for
free. Field ownership makes the Go type irrelevant, which is why it won
([issue #121](https://github.com/ricardomolendijk/netbox-operator/issues/121)).

### Why `omitempty` stays on the structs

Taking `omitempty` off would look like a smaller fix: the empty value would then be in the
marshalled spec by itself. It inverts the bug. A typed Go client — including the operator
itself, materialising an inline child ([ADR-0005](../decisions/0005-gitops-coexistence.md)
§2) — would marshal every unset string as `""` and thereby *claim* it, so adopting a
pre-existing NetBox object would wipe every value the user had not restated. So the structs
keep `omitempty`, and the engine puts the claimed-but-empty fields back from the metadata.

## What happens per client

| How the object is written | What lands in `managedFields` | Result |
|---|---|---|
| **Server-side apply** — Flux, `kubectl apply --server-side`, Argo CD with `ServerSideApply=true` | exactly the fields the manifest carries; a defaulted field the manifest does not mention is owned by nobody | full three-state behaviour |
| **Client-side `kubectl apply`** | an `Update` entry naming every field the request stored, defaults included; a field dropped from the manifest loses its claim on the next apply | full three-state behaviour, plus every defaulted field counted as claimed — which changes nothing, because a default is non-empty and managed anyway |
| **`kubectl edit`** | an `Update` entry naming only the fields that edit changed; the previous manager keeps the rest | full three-state behaviour. An empty value typed into an editor is a claim like any other |
| **An object created before the operator was installed** | whatever its writers left, unchanged — the operator installing does not touch it | full three-state behaviour. Field management has been on by default since Kubernetes 1.18, so an object written by any supported API server already has it |

The API server tracks ownership for `Update` requests as well as for `Apply` ones, which is
why the second and third rows work at all. A rule that only trusted server-side apply would
manage nothing for everybody using `kubectl apply` without `--server-side`.

## When there is nothing to read

An object can reach the operator with no spec ownership metadata at all: a controller-runtime
cache configured to strip `managedFields` to save memory, a restore that dropped them, an
API server with the feature turned off. The operator does **not** treat that as "the user set
no fields" — that would stop it managing anything, silently, for a whole class of users.

It falls back to the behaviour it had before it read ownership at all: **every field holding a
non-empty value is managed, and an empty one is invisible.** The fallback can only ever
manage fewer fields than the metadata would, never more, so it cannot overwrite something
nobody asked for.

The ceiling is worth stating plainly, because it is the original bug surviving in one corner:
under the fallback an empty string, an empty list, an empty map, a `false` and a `0` are
indistinguishable from absent, so **clearing such a field does nothing and no condition
disagrees.** That is not silent:

```promql
netbox_operator_spec_ownership_untracked_total
```

counts reconciles that had no ownership to read, by kind. It should be flat at zero. A
non-zero rate means something in the path is erasing field ownership — start with any
`cache.Options.DefaultTransform` in the manager's wiring. The debug log carries the same
event with `action=fallback`.

## This does not weaken never-writing-`spec`

Reading `managedFields` is a read. The operator's output still goes to `status` and
`metadata.finalizers` and nowhere else
([ADR-0005](../decisions/0005-gitops-coexistence.md) §1).

If anything the invariant got easier to check. The operator's field manager name is fixed,
and its own managed-fields entries are the API server's record of what it wrote — so the
invariant is now observable from outside the process:

```console
$ kubectl get netboxtag spine -o jsonpath='{.metadata.managedFields}' | jq '.[] | select(.manager=="netbox-operator")'
{
  "manager": "netbox-operator",
  "operation": "Update",
  "subresource": "status",
  "fieldsV1": { "f:status": { ... } }
}
```

`f:spec` appearing there would mean the operator wrote a spec. `TestOperatorFieldManagerNeverOwnsSpec`
asserts it against a real API server.

## Related

- [Drift detection](drift.md) — how the empty value you send is compared with what NetBox
  returns, which is not always the same shape
- [Reconciliation](reconciliation.md) — where in a pass the payload is built
- [ADR-0005: coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) —
  why the operator never writes a spec
