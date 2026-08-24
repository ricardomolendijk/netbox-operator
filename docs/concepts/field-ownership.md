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
