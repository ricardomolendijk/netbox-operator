# `NetBoxContact`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxContact` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcontact` |
| Status subresource | yes |

A `NetBoxContact` is one `tenancy.Contact` in NetBox: a person or a rota, with a phone number
and an email address, that can be attached to anything NetBox lets you attach a contact to.

Two things make it unusual, and both are stated here before the field table because both are
places a reasonable assumption is wrong.

## Its lookup key is a convention, not a constraint

`tenancy.Contact` declares **no `meta.constraints`, no column `UNIQUE`, and only an index**:

```
meta.indexes: (models.Index(fields=('name',)),)
```

(`docs/netbox-schema.md` → `tenancy.Contact`; `netbox/tenancy/models/contacts.py:114-120`.)

So two contacts called `NOC` are legal NetBox state. `name` is the natural key because a contact
has nothing else to be identified by — there is no `slug` on this model — and the consequence is
explicit rather than papered over: if the lookup matches more than one row, the CR reports
`Ready=False, Reason=Conflict` and writes nothing. It does not take the first.

This is the same position [`NetBoxPrefix`](netboxprefix.md) and
[`NetBoxIPAddress`](netboxipaddress.md) are in. Unlike `NetBoxIPAddress` there is no
`allowDuplicate` here, because a duplicate contact is never something NetBox's data model
*requires* — it is somebody declaring the same contact twice, and the honest answer to that is a
`Conflict`.

## Its group relationship is a many-to-many

Everywhere else in NetBox a "group" is one foreign key: `Tenant.group`, `Cluster.group`,
`VLAN.group`. `Contact.groups` is
`ManyToManyField -> tenancy.ContactGroup` (`netbox/tenancy/models/contacts.py:71-76`), so a
contact may sit in several groups at once — and it cannot be part of the identity, because there
is no single value a lookup filter could take.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxContact
metadata:
  name: noc
  namespace: default
spec:
  endpointRef: homelab
  name: NOC
  email: noc@example.net
  groups:
    - name: operations
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `name` | `string`, 1–100 | yes | `name`, `CharField REQ len=100` |
| `groups` | list of [refs](../concepts/references.md) → `NetBoxContactGroup`, ≤256 | no | `groups`, `ManyToManyField -> tenancy.ContactGroup` |
| `title` | `string`, ≤100 | no | `title`, `CharField len=100` |
| `phone` | `string`, ≤50 | no | `phone`, `CharField len=50` |
| `email` | `string`, ≤254 | no | `email`, `EmailField` |
| `address` | `string`, ≤200 | no | `address`, `CharField len=200` |
| `link` | `string`, ≤200 | no | `link`, `URLField` |
| `description` | `string`, ≤200 | no | `description` (`PrimaryModel`), `CharField len=200` |
| `comments` | `string` | no | `comments` (`PrimaryModel`), `TextField` |

Every optional field here is clearable: omit one to leave NetBox's own value alone, set it to
`""` to clear it ([field ownership](../concepts/field-ownership.md)). All of them are text
columns, so `""` *is* how NetBox spells empty and there is no null to send.

`email` and `link` carry no pattern. NetBox validates both server-side, with Django's
`EmailValidator` and `URLValidator`, and a second-guessing regex in the CRD would reject
addresses NetBox accepts while still having to admit `""` — which is the value that clears the
column. Their `MaxLength`s are Django's own field defaults (`EmailField` 254, `URLField` 200),
which are the column widths: the schema digest prints no `len=` for either because the model
passes no `max_length`.

`owner` is absent: `ForeignKey -> users.Owner`, and the `users` app is deferred whole
([coverage](../coverage.md#endpoints)). `assignments` is absent: it is the reverse of
`ContactAssignment.contact`, a read-only view, and the way to create one is a
[`NetBoxContactAssignment`](netboxcontactassignment.md).

### `groups`

A to-many reference, so **the listed set is the set**: NetBox replaces a many-to-many wholesale
on `PATCH` and has no add or remove verb. Three states, and all three are instructions:

| `groups` | Means |
|---|---|
| absent | do not manage the groups; leave NetBox's own alone |
| `[]` | this contact is in no groups; clear them |
| a list | exactly these |

Order is not data — NetBox does not preserve it, so the ids are sorted and deduplicated and the
comparison is order-independent. Reordering the list produces no write
([drift](../concepts/drift.md)).

**All or nothing.** If any element cannot be resolved the whole field is left out of the payload
and the object reports `RefsResolved=False` naming the element that failed. Writing only the ones
that resolved would be a full-list replacement with a shorter list — a deletion, reported as a
success.

`MaxItems: 256` is not a NetBox limit. `ObjectRef` carries five CEL rules and the API server
costs a rule on a list item at the list's **maximum** length, so an unbounded list of refs is
refused outright at install
([references](../concepts/references.md), "A list needs a bound").

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, no null pin, and nothing conditional. The filter is registered:
`Meta.fields = ('id', 'name', 'title', 'phone', 'email', 'address', 'link', 'description')` on
`ContactFilterSet` (NetBox 4.6.8, `netbox/tenancy/filtersets.py:94-96`).

`groups` is deliberately outside it. `?group_id=` *is* a registered filter on this endpoint
(`netbox/tenancy/filtersets.py:80-85`), and it is a `TreeNodeMultipleChoiceFilter` over
`groups`: it matches **membership**, which is a different question from identity and would make
one contact in three groups match three different lookups.

## No containment parent

`groups` is the only reference this Kind has, and a many-to-many has no `on_delete` in either
direction: deleting a `NetBoxContactGroup` drops the join-table rows and leaves the contact
standing. There is no server-side deletion for an owner reference to mirror, so this Kind has
none ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

## Deleting a contact does not remove its assignments

`ContactAssignment.contact` is `on_delete=PROTECT` (`docs/netbox-schema.md` →
`tenancy.ContactAssignment`), so NetBox **refuses** to delete a contact while any assignment
names it. The CR reports `Deleting=False, Reason=Protected`.

That is worth stating plainly because the obvious expectation is the other one: NBO-056's own
acceptance list asks for the assignments to be garbage-collected from the contact. They are not,
and cannot be — an owner reference on a `PROTECT` foreign key would delete the assignment CR
while NetBox still held the row, and the row could not have been deleted anyway (#217). Delete
the [`NetBoxContactAssignment`](netboxcontactassignment.md) CRs first; *they* are owned by what
they are assigned to, which does cascade.

## `deletionPolicy` defaults to `Delete`

`Delete`, like every kind since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304).

## Printer columns

```
NAME   CONTACT   EMAIL              ID   READY   AGE
noc    NOC       noc@example.net    22   True    4m
```

| Column | JSONPath |
|---|---|
| `CONTACT` | `.spec.name` |
| `EMAIL` | `.spec.email` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Related

- [`NetBoxContactAssignment`](netboxcontactassignment.md) — attaching this contact to something
- [`NetBoxContactGroup`](netboxcontactgroup.md) — the other end of `groups`
- [`NetBoxVRF`](netboxvrf.md) — the first to-many reference, and the same three states
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
