# `NetBoxContactAssignment`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxContactAssignment` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcontactassignment` |
| Status subresource | yes |

A `NetBoxContactAssignment` is one `tenancy.ContactAssignment` in NetBox: the row that says
*this contact, in this role, for this object*. It has no name and no slug — it is a **join
object**, and its whole content is the three things it joins.

It is four firsts in one Kind:

| First | What it is |
|---|---|
| the widest polymorphic reference | 11 union members over a column NetBox permits 25 types in |
| the first **`REQ`** pair to ship | both columns non-nullable, so the union is required and the CEL rule is `== 1` |
| the first identity mixing a pair with ordinary refs | `(object_type, object_id, contact_id, role_id)` |
| the first containment parent that is a union on a Kind made only of references | the object the contact is assigned to |

And it is still three files and no engine change. The union is `GenericFKSpec.Members` on its
Descriptor, the identity is `NaturalKeys`, the cascade is a `bool` per member.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxContactAssignment
metadata:
  name: noc-on-rtm1
  namespace: default
spec:
  endpointRef: homelab
  objectRef:
    siteRef:
      name: rtm1
  contactRef:
    name: noc
  roleRef:
    name: technical
  priority: primary
```

Nothing in that manifest spells `dcim.site`. The **member name** is what selects the target
Kind, and that Kind's own `Descriptor.ObjectType` is where the `app_label.model` string is
written down — once, in the whole codebase
([generic references](../concepts/generic-refs.md)).

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `objectRef` | [union](#objectref) | **yes** | `object_type` + `object_id`, both `REQ` |
| `contactRef` | [ref](../concepts/references.md) → `NetBoxContact` | **yes** | `contact`, `ForeignKey REQ -> tenancy.Contact on_delete=PROTECT` |
| `roleRef` | [ref](../concepts/references.md) → `NetBoxContactRole` | **yes** | `role`, `ForeignKey REQ -> tenancy.ContactRole on_delete=PROTECT` |
| `priority` | enum: `""`, `primary`, `secondary`, `tertiary`, `inactive` | no | `priority`, `CharField len=50 choices=ContactPriorityChoices` |

There is no `description` and no `comments`: `tenancy.ContactAssignment` is not a
`PrimaryModel`. Its bases are `CustomFieldsMixin, ExportTemplatesMixin, TagsMixin,
ChangeLoggedModel` (`docs/netbox-schema.md` → `tenancy.ContactAssignment`) — so it *is* tagged
and *does* carry custom fields, and it is stamped in full.

`object` is absent from the spec: it is the `GenericForeignKey` itself, which is not a column.
NetBox's serializer returns it as a read-only nested view of the pair
(`netbox/tenancy/api/serializers_/contacts.py:71`), and it is never written.

### `roleRef` is required, whatever the serializer says

NetBox's serializer declares `role = ContactRoleSerializer(nested=True, required=False,
allow_null=True)` (`netbox/tenancy/api/serializers_/contacts.py:72`). The Django column has no
`null=True` (`netbox/tenancy/models/contacts.py:138-143`), so an assignment without a role is an
integrity error rather than a null — and the role is part of the uniqueness constraint, so an
assignment without one has no identity either. It is required here.

### `priority`

Values from `ContactPriorityChoices` (`netbox/tenancy/choices.py:10-21`). `""` is a member of
the enum because NetBox's column is blank-able and its serializer declares
`ChoiceField(..., allow_blank=True, default=lambda: '')` (`serializers_/contacts.py:73`) — so
`""` is how NetBox spells "no priority" and the only way to clear one that was set.

The field carries no *omit-versus-empty* note in its description, and that is deliberate rather
than an oversight: an `enum` is validation that ordinarily rejects the empty value, so the note
and the schema would contradict each other and
`TestClearableFieldsDocumentBothStatesInTheSchema` refuses the combination. The three states are
still real — absent leaves NetBox's own priority alone, `""` clears it, a value sets it — and
they are documented on the `ContactPriority` type instead.

`priority` is **not** part of the identity. Two CRs differing only in priority are one row, and
changing it is a `PATCH`.

## `objectRef`

A named union with one typed member per target Kind. At most one may be set, and **at least
one must be**:

```
[has(self.regionRef), has(self.siteGroupRef), has(self.siteRef), has(self.locationRef),
 has(self.deviceRef), has(self.prefixRef), has(self.ipAddressRef), has(self.tenantRef),
 has(self.clusterRef), has(self.clusterGroupRef), has(self.virtualMachineRef)]
  .filter(x, x).size() == 1
```

`== 1` and not `<= 1`, because both columns are `REQ`: `object_type ForeignKey REQ` and
`object_id PositiveBigIntegerField REQ`, neither carrying `null=True`
(`netbox/tenancy/models/contacts.py:124-132`). The `REQ` printed against the `object` row above
them is the extractor artefact every generic FK has — a `GenericForeignKey` takes no `null=`
kwarg — and here it happens to agree with the two real columns.

That is why the field itself is **required** on the spec rather than optional. A CEL rule on an
absent field is never evaluated, so an optional `== 1` union would be satisfied by leaving it
out entirely. It also means `objectRef: {}` is not an instruction here: on a nullable pair an
empty union clears both columns, and NetBox's `NOT NULL` would refuse that.

### The members

| Member | Object type | Kind |
|---|---|---|
| `regionRef` | `dcim.region` | [`NetBoxRegion`](netboxregion.md) |
| `siteGroupRef` | `dcim.sitegroup` | [`NetBoxSiteGroup`](netboxsitegroup.md) |
| `siteRef` | `dcim.site` | [`NetBoxSite`](netboxsite.md) |
| `locationRef` | `dcim.location` | [`NetBoxLocation`](netboxlocation.md) |
| `deviceRef` | `dcim.device` | `NetBoxDevice` — **not in this build** |
| `prefixRef` | `ipam.prefix` | [`NetBoxPrefix`](netboxprefix.md) |
| `ipAddressRef` | `ipam.ipaddress` | [`NetBoxIPAddress`](netboxipaddress.md) |
| `tenantRef` | `tenancy.tenant` | [`NetBoxTenant`](netboxtenant.md) |
| `clusterRef` | `virtualization.cluster` | [`NetBoxCluster`](netboxcluster.md) |
| `clusterGroupRef` | `virtualization.clustergroup` | [`NetBoxClusterGroup`](netboxclustergroup.md) |
| `virtualMachineRef` | `virtualization.virtualmachine` | [`NetBoxVirtualMachine`](netboxvirtualmachine.md) |

`deviceRef` is declared and does not resolve. That is the correct behaviour, not a gap: a member
whose target Kind has no Descriptor reports
`RefsResolved=False, Reason=RefKindUnavailable` in **all four** ref modes — `name`, `slug`,
`lookup` and `id` — because all four need the target's NetBox endpoint, which only a Descriptor
holds. Accepting the member and dropping it would report success while NetBox held nothing
([generic references](../concepts/generic-refs.md#kinds-that-do-not-exist-yet)).

### What NetBox permits, and why the list here is shorter

NetBox accepts **25** object types in this column: every model that mixes in
`netbox.models.features.ContactsMixin` in 4.6.8. The gate is the model's own `clean()`:

```python
if not has_feature(self.object_type, 'contacts'):
    raise ValidationError(_("Contacts cannot be assigned to this object type ({type})."))
```

(`netbox/tenancy/models/contacts.py:173-179`.) The serializer restricts nothing on its own —
`object_type = ContentTypeField(queryset=ContentType.objects.all())`
(`serializers_/contacts.py:68-70`) — which is the difference from
[`NetBoxPrefix`](netboxprefix.md)'s `scope_type`, where a queryset filter is what narrows the
pair to four.

The full 25 are recorded as the pair's `AllowedTypes`, with the source line each mixin appears
on, and are **not** derived from the member list. They are two independent statements and the
operator cross-checks them at boot: a member whose object type is outside `AllowedTypes` fails
the boot (`ErrMemberTypeNotAllowed`), and a list computed from the members would make that check
tautological.

The fourteen with no member — `circuits.circuit`, `circuits.provider`,
`circuits.provideraccount`, `circuits.virtualcircuit`, `dcim.manufacturer`, `dcim.powerpanel`,
`dcim.rack`, `ipam.aggregate`, `ipam.asn`, `ipam.iprange`, `ipam.service`, `vpn.l2vpn`,
`vpn.tunnel`, `vpn.tunnelgroup` — are absent rather than stubbed. A member needs a typed alias
to write its target Kind down on, and inventing eleven aliases for Kinds nobody has designed
would put eleven guesses about their endpoints into the API. Each arrives with its Kind, as one
`Members` entry.

> NBO-056's spec says 23 models. It is 25 in the 4.6.8 tree; `dcim.Manufacturer`
> (`netbox/dcim/models/devices.py:54`) and `vpn.TunnelGroup`
> (`netbox/vpn/models/tunnels.py:19`) are the two the spec's list omits — they carry the mixin
> and nothing else about contacts, which is what makes them easy to miss.

### An object the operator does not manage

`id` is the escape hatch, and it is the one place in this API where a raw NetBox primary key is
accepted ([ADR-0003](../decisions/0003-ownership-and-references.md)):

```yaml
  objectRef:
    prefixRef:
      id: 41
```

It goes inside the member, not beside it: the member is what pins the Kind, so
`{objectType: "ipam.prefix", id: 41}` is not a shape this API has — there is no `objectType`
field to write. `slug` and `lookup` work in a member too, for a target with no CR.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `(object_type, object_id, contact, role)` | `?object_type=<app.model>&object_id=<id>&contact_id=<id>&role_id=<id>` |

Straight out of `meta.constraints`:

```
UniqueConstraint(fields=('object_type', 'object_id', 'contact', 'role'),
                 name='%(app_label)s_%(class)s_unique_object_contact_role')
```

(`docs/netbox-schema.md` → `tenancy.ContactAssignment`;
`netbox/tenancy/models/contacts.py:159-164`.)

**Every filter on it is registered**, which is checked rather than assumed. django-filter
*ignores* a parameter it does not recognise and NetBox 4.6.8 has no strict-filter validation, so
a guessed filter name is a lookup that returns the **unfiltered** set — the engine would adopt
the first contact assignment in NetBox and PATCH it into this CR's shape (#206):

| Filter | Declaration | Line |
|---|---|---|
| `object_type` | `MultiValueContentTypeFilter()` | `netbox/tenancy/filtersets.py:119` |
| `object_id` | `Meta.fields = ('id', 'object_type_id', 'object_id', 'priority')` | `netbox/tenancy/filtersets.py:153` |
| `contact_id` | `ModelMultipleChoiceFilter(queryset=Contact.objects.all())` | `netbox/tenancy/filtersets.py:120-124` |
| `role_id` | `ModelMultipleChoiceFilter(queryset=ContactRole.objects.all())` | `netbox/tenancy/filtersets.py:138-142` |

`object_type` takes the `app_label.model` **string**, not a ContentType id:
`MultiValueContentTypeFilter` splits the value on `.` and resolves it through
`ContentType.objects.get_by_natural_key` (`netbox/utilities/filters.py:186-207`). It is the same
filter class [`NetBoxVLANGroup`](netboxvlangroup.md)'s `scope_type` uses.

Three properties of this key are load-bearing:

- **Both halves of the pair or neither.** An id is only unique *within* its type, so
  `?object_id=7` alone matches the site with id 7 and the tenant with id 7 alike. The pair is
  written into the lookup by column name, which is what the engine puts into the decoded spec
  once the union resolves — the shape #180 built for `ipam.VLANGroup`.
- **`role_id` is in the key.** Drop it and the second assignment of one contact to one object
  looks like drift on the first, and the two CRs PATCH over each other forever. With it, they
  are two rows and both reconcile `Ready`.
- **No null pin and nothing to pin.** All four columns are `REQ`, so there is no state in which
  one is absent. The conditional-constraint shape every nested-group kind needs has nothing to
  express here, and a pin on a `REQ` column would be a candidate that can never match.

### The two cases that look the same and are not

| Two CRs | NetBox | Outcome |
|---|---|---|
| same object, same contact, **different** `roleRef` | two rows | both `Ready`; neither is drift on the other |
| same object, same contact, same `roleRef` | one row | first `Ready`, second `Conflict`, one row |

## `objectRef` is the containment parent

`ContactsMixin` declares

```python
contacts = GenericRelation(to='tenancy.ContactAssignment',
                           content_type_field='object_type', object_id_field='object_id')
```

(`netbox/netbox/models/features.py:392-396`), and Django deletes the rows behind a
`GenericRelation` when the object owning it is deleted. So deleting a site in NetBox takes its
contact assignments with it — and that holds for **every** member of the union, because the
cascade is a property of `ContactsMixin`, which every allowed type has by definition. Here the
`GenericRelation` is the whole of the cascade: this model carries no denormalised `_`-prefixed
columns, unlike `ipam.Prefix`'s `_site`.

By [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 that makes `objectRef` the
containment reference: the assignment CR carries a non-controller owner reference to the CR of
whatever it is assigned to, so `kubectl delete netboxsite rtm1` takes the assignment CR with it.
Without it the CR outlives the row, finds nothing at `.status.id`, and the engine's
create-if-absent step recreates an assignment NetBox deliberately deleted.

An owner reference is only filed when it is legal — same namespace. An assignment onto a shared
catalogue object in another namespace gets none and reports `CascadeUnavailable` naming
`objectRef` ([ownership](../concepts/ownership.md)).

### Why not `contactRef`

Because nothing on the server side disappears. `ContactAssignment.contact` is
`on_delete=PROTECT`: deleting the contact is **refused** while an assignment names it. An owner
reference there would garbage-collect the assignment CR while NetBox still held the row, and the
row could not have been deleted anyway (#217).

NBO-056's acceptance list asks for exactly that owner reference — "deleting a `NetBoxContact`
removes its assignments". It is not implemented, and the `on_delete` is the reason. Delete the
assignment CRs first, or delete the object they are assigned to and let the cascade take them.

`roleRef` is `PROTECT` too, and there is only one containment slot in any case: Kubernetes
garbage collection waits for *every* owner, so a second one would silently turn "delete the site
and the assignment goes" into "delete both".

## The cycle check does not follow this pair

A `REQ` pair *blocks* creation — the object cannot exist until the reference resolves — which is
the condition under which the [cycle walk](../concepts/references.md#cycles) follows an edge.
It is not followed here, and does not need to be: **nothing in NetBox points at a
ContactAssignment**, so there is no `contactAssignmentRef` anywhere in this API and the object is
a leaf in the reference graph. A ring through it is unconstructible rather than unchecked.

## Conditions

`RefsResolved` carries the polymorphic reasons, and the field named in the message is the
**member's** path rather than the union's — `objectRef.deviceRef` is the string to grep the
manifest for.

| Reason | Means | Retried |
|---|---|---|
| `RefKindUnavailable` | the member is declared and its target Kind is not in this build (`deviceRef`) | every 10 minutes |
| `RefTypeNotAllowed` | the resolved object type is outside what the column accepts | **no** — terminal, only an edit clears it |
| `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied` | exactly as for a typed reference | see [references](../concepts/references.md) |

With `objectRef` unresolved **nothing is written at all** — not the contact, not the role, not
the priority. Both columns are `REQ`, so a payload without them is one NetBox would reject.

## `deletionPolicy` defaults to `Delete`

`Delete`, like every kind since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304). Deleting the CR deletes the row; both foreign
keys out of it are `PROTECT`, and nothing points *at* it, so the delete is never refused.

## Printer columns

```
NAME                   CONTACT   ROLE        PRIORITY   ID   READY   AGE
noc-on-rtm1            noc       technical   primary    31   True    5m
noc-on-acme            noc       technical   primary    32   True    5m
noc-on-acme-billing    noc       billing                33   True    5m
```

| Column | JSONPath |
|---|---|
| `CONTACT` | `.spec.contactRef.name` |
| `ROLE` | `.spec.roleRef.name` |
| `PRIORITY` | `.spec.priority` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Related

- [`NetBoxContact`](netboxcontact.md) — the contact, and why deleting it does not cascade here
- [`NetBoxContactRole`](netboxcontactrole.md) — why the role is part of this identity
- [Generic references](genericref.md) — the union shape, `<= 1` versus `== 1`, and the reasons
- [Generic references (concept)](../concepts/generic-refs.md) — the mechanism and the spelling
  rule
- [`NetBoxVLANGroup`](netboxvlangroup.md) — the other identity built on a polymorphic pair
- [Ownership](../concepts/ownership.md) — containment references and `CascadeUnavailable`
