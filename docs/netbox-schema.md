# NetBox 4.6.8 data-model reference

Machine-extracted from the NetBox source (`netbox-community/netbox`, tag/release
`4.6.8`, `release.yaml: published 2026-08-11`) by walking the Django model AST.
This is the authoritative field list the operator's CRD schemas are derived from.

How to read an entry:

- `REQ`   — the column is `NOT NULL` with no default: it must be supplied on create.
- `-> x`  — `ForeignKey` / `OneToOneField` / `ManyToManyField` target, always spelled
  `app.Model`. `-> UNRESOLVED:Name` means the source names a class from outside the ten
  scanned apps (`django.contrib.auth`'s `User`, `Permission`), so there is no app to qualify
  it with: read the source rather than assume the app.
- `on_delete=X` — what NetBox does to this row when the target goes away: `PROTECT`
  (the delete is refused while this row exists), `CASCADE` (this row goes too),
  `SET_NULL` / `SET_DEFAULT` (the column is cleared).
- `UNIQUE` — column-level unique index.
- `len=n` — `max_length`. `len=UNRESOLVED:SYMBOL` means NetBox declares the length as a
  module constant the AST walk could not pin to a single value: read the source, do not
  assume.
- `decimal(d,p)` — a `DecimalField`'s `max_digits` and `decimal_places`. `decimal(8,6)` is
  not a free float, and a CRD field for it needs the same precision.
- `def=x` — the column default. A quoted value is a real literal; `def=UNRESOLVED:SYMBOL` is
  a symbol (`VLANStatusChoices.STATUS_ACTIVE`, `dict`) that the AST walk cannot evaluate, so
  it is *not* the string it happens to spell.
- A `ManyToManyField` is a through table, not a column on this model: it has no `NOT NULL` to
  violate, Django ignores `null=` on it, and it therefore never carries `REQ`.
- `M2M -> extras.Tag (via TaggedItem, not a column)` — a `TaggableManager`. It is not a field
  at all, so there is no column anywhere for it, but it *is* a writable REST field: see the
  next section.
- `blank=True` is a form-level flag, not SQL. It stands in for optional on a `CharField`,
  whose `NOT NULL` column takes `''` instead — but a `ForeignKey(null=False, blank=True)`
  column has no such empty value and really must be supplied, so it is marked `REQ`.
- `meta.constraints` — table-level `UniqueConstraint`s, wrapped across lines but never
  truncated. **These are the natural keys the operator uses to look an object up before
  deciding create-vs-update.**
- `GenericForeignKey` pairs (`*_type` / `*_id`) are polymorphic FKs; over the REST API
  the `_type` half is written as an `"app_label.model"` string. The accessor half
  (`scope`, `assigned_object`) is not a column, so it never carries `REQ` of its own —
  requiredness is that of the `*_type` column. Likewise a `GenericRelation`
  (`ip_addresses`, `l2vpn_terminations`) is a reverse relation and is never required.
- `(no own columns …)` — a real API kind that declares no fields of its own; everything
  comes from the named base classes (`OrganizationalModel` gives `name`, `slug`,
  `description`; `NestedGroupModel` adds `parent`).
- `name (OrganizationalModel)` — the column is inherited, not declared on this model, and
  the parenthesis names the class that declares it. It is a column of this model all the
  same: an inherited `name` is as required, and as writable, as a declared one.
- Fields prefixed `_` (e.g. `_site`, `_depth`, `_children`) and every `CounterCacheField`
  are denormalised caches maintained by NetBox itself — read-only, never write them.

## This is the database, and a CRD is derived from the API

The walk above reads Django models, so every entry below describes a **column**. Every CRD is
derived from the **REST API**. The two lists differ in both directions, and neither contains
the other: an entry below is evidence about a column, not proof that the field is writable —
and a field's absence below is not proof that the API does not take it.

**In the API, not a column** (so emitted only where the walk can see the declaration at all):

- `tags` — declared as `TaggableManager(through='extras.TaggedItem')`, a through-table manager
  rather than a model field, so it contributes no column. It is nonetheless writable on every
  `PrimaryModel`, `OrganizationalModel` and `NestedGroupModel`, as a list of `extras.Tag`
  **ids**. It is emitted from the mixin that declares it, marked as what it is:
  `tags (TagsMixin)   TaggableManager   M2M -> extras.Tag (via TaggedItem, not a column)`.
- `custom_fields` — the *column* is `custom_field_data`, a `JSONField` defaulting to `{}`.
  The *API field* is `custom_fields`, and the shapes differ: a map keyed by custom-field
  **name**, validated against `extras.CustomField`. Never write `custom_field_data`.
- `id` — Django's implicit `AutoField` primary key is declared nowhere, so no entry below
  lists it (the one `id` row that appears, `extras.CachedValue`, is an explicitly declared
  `UUIDField`). It is in every API response, and it is what the operator stores as
  `status.id`.
- `url`, `display` and the other serializer-computed fields — read-only, and no part of any
  model. (`url` is what the operator stores as `status.url`.)

**A column, but never writable:**

- `_`-prefixed columns — `_site`, `_location`, `_rack`, `_region`, `_site_group`, `_device`,
  `_path`, `_depth`, `_children`, `_abs_length`, and the `NaturalOrderingField` `_name`:
  denormalised caches NetBox maintains itself.
- every `CounterCacheField` (`interface_count`, `device_count`, the 35 `*_count` rows) — the
  API returns them and refuses to accept them. Their `default=0` and `editable=False` are set
  inside the field class rather than at the declaration, so the AST sees neither: they are
  *not* required, whatever an entry produced before NBO-073 says.
- `created` / `last_updated` (`auto_now_add` / `auto_now`) — returned, never accepted.

**A model attribute, and no API field at all:**

- `GenericRelation` rows (`prefixes`, `ip_addresses`, `l2vpn_terminations`,
  `cable_terminations`, `services`, `group_assignments`, …) are reverse relations: not a
  column on this model, and not writable. Most are not serialized at all.
- the accessor half of a `GenericForeignKey` (`scope`, `assigned_object`) is not a column
  either; the API writes the `*_type` / `*_id` pair.
- managers other than the `TaggableManager` (`objects = TreeManager()`,
  `RestrictedQuerySet.as_manager()`) are queryset accessors, deliberately not emitted.

**A column in Postgres, absent below:** mptt's `lft`, `rght`, `tree_id` and `level` on every
nested group model. `MPTTModel` lives outside the ten scanned apps, so the walk never sees
them; they are maintained by mptt and are not API fields.

Where the two disagree, the REST API wins — the operator talks to the API, not to Postgres
(`docs/regenerating.md`). This list is curated by hand. NBO-041's ingest now reads the
writable-vs-read-only half, the choice *values* and the generic-FK target spellings out of the
REST serializers and `<app>/choices.py` -- see `hack/build-netbox-ir.py` and
`hack/testdata/ir-4.6.8.json.gz`, whose `conflicts` list is the derived form of this section.

Regenerate with `hack/extract-netbox-schema.py` (see `docs/regenerating.md`).
> Generated from the `v4.6.8` tag of netbox-community/netbox (commit `3db98de`,
> `netbox/release.yaml`: version 4.6.8, Community, published 2026-08-11) by the
> post-NBO-067/070/071/073/093 scripts. Every entry below therefore lists inherited columns
> attributed to the class that declares them, full `meta.constraints`, `on_delete`, resolved
> `len=` symbols, `decimal(p,s)` precision, and a `tags` row on each of the 92 taggable models.
> A `def=UNRESOLVED:X` value is a symbol the extractor did not evaluate, not a literal.
>
> Two omissions NBO-041 fixed while building the code-generator IR, both of them silent:
> `circuits.CircuitType` and `circuits.VirtualCircuitType` -- two shipped API endpoints --
> had no entry at all, because the inclusion test asked whether a base class's *name*
> contained "Model", "Component" or "Template" and `BaseCircuitType` contains none of them.
> And `ComponentModel` is declared in both `dcim` and `virtualization`, which made the base
> unattributable and so dropped `name`, `label` and `description` from eleven shipped
> component Kinds; a base now resolves within the declaring model's own app first. The one
> remaining unattributable case is a base declared in two apps and in neither the subclass's
> own -- there are none at 4.6.8, and the extractor warns per affected model if one appears.

## API endpoint -> model map

```
circuits/providers                                   circuits.Provider
circuits/provider-accounts                           circuits.ProviderAccount
circuits/provider-networks                           circuits.ProviderNetwork
circuits/circuit-types                               circuits.CircuitType
circuits/circuits                                    circuits.Circuit
circuits/circuit-terminations                        circuits.CircuitTermination
circuits/circuit-groups                              circuits.CircuitGroup
circuits/circuit-group-assignments                   circuits.CircuitGroupAssignment
circuits/virtual-circuits                            circuits.VirtualCircuit
circuits/virtual-circuit-types                       circuits.VirtualCircuitType
circuits/virtual-circuit-terminations                circuits.VirtualCircuitTermination
core/data-sources                                    core.DataSource
core/data-files                                      core.DataFile
core/jobs                                            core.Job
core/object-changes                                  core.ObjectChange
core/object-types                                    core.ObjectType
core/background-queues                               core.BackgroundQueue
core/background-workers                              core.BackgroundWorker
core/background-tasks                                core.BackgroundTask
dcim/regions                                         dcim.Region
dcim/site-groups                                     dcim.SiteGroup
dcim/sites                                           dcim.Site
dcim/locations                                       dcim.Location
dcim/rack-groups                                     dcim.RackGroup
dcim/rack-types                                      dcim.RackType
dcim/rack-roles                                      dcim.RackRole
dcim/racks                                           dcim.Rack
dcim/rack-reservations                               dcim.RackReservation
dcim/manufacturers                                   dcim.Manufacturer
dcim/device-types                                    dcim.DeviceType
dcim/module-types                                    dcim.ModuleType
dcim/module-type-profiles                            dcim.ModuleTypeProfile
dcim/console-port-templates                          dcim.ConsolePortTemplate
dcim/console-server-port-templates                   dcim.ConsoleServerPortTemplate
dcim/power-port-templates                            dcim.PowerPortTemplate
dcim/power-outlet-templates                          dcim.PowerOutletTemplate
dcim/interface-templates                             dcim.InterfaceTemplate
dcim/front-port-templates                            dcim.FrontPortTemplate
dcim/rear-port-templates                             dcim.RearPortTemplate
dcim/module-bay-templates                            dcim.ModuleBayTemplate
dcim/device-bay-templates                            dcim.DeviceBayTemplate
dcim/inventory-item-templates                        dcim.InventoryItemTemplate
dcim/device-roles                                    dcim.DeviceRole
dcim/platforms                                       dcim.Platform
dcim/devices                                         dcim.Device
dcim/virtual-device-contexts                         dcim.VirtualDeviceContext
dcim/modules                                         dcim.Module
dcim/console-ports                                   dcim.ConsolePort
dcim/console-server-ports                            dcim.ConsoleServerPort
dcim/power-ports                                     dcim.PowerPort
dcim/power-outlets                                   dcim.PowerOutlet
dcim/interfaces                                      dcim.Interface
dcim/front-ports                                     dcim.FrontPort
dcim/rear-ports                                      dcim.RearPort
dcim/module-bays                                     dcim.ModuleBay
dcim/device-bays                                     dcim.DeviceBay
dcim/inventory-items                                 dcim.InventoryItem
dcim/inventory-item-roles                            dcim.InventoryItemRole
dcim/mac-addresses                                   dcim.MACAddress
dcim/cables                                          dcim.Cable
dcim/cable-terminations                              dcim.CableTermination
dcim/cable-bundles                                   dcim.CableBundle
dcim/virtual-chassis                                 dcim.VirtualChassis
dcim/power-panels                                    dcim.PowerPanel
dcim/power-feeds                                     dcim.PowerFeed
dcim/connected-device                                dcim.ConnectedDevice
extras/event-rules                                   extras.EventRule
extras/webhooks                                      extras.Webhook
extras/custom-fields                                 extras.CustomField
extras/custom-field-choice-sets                      extras.CustomFieldChoiceSet
extras/custom-links                                  extras.CustomLink
extras/export-templates                              extras.ExportTemplate
extras/saved-filters                                 extras.SavedFilter
extras/table-configs                                 extras.TableConfig
extras/bookmarks                                     extras.Bookmark
extras/notifications                                 extras.Notification
extras/notification-groups                           extras.NotificationGroup
extras/subscriptions                                 extras.Subscription
extras/tags                                          extras.Tag
extras/tagged-objects                                extras.TaggedItem
extras/image-attachments                             extras.ImageAttachment
extras/journal-entries                               extras.JournalEntry
extras/config-contexts                               extras.ConfigContext
extras/config-context-profiles                       extras.ConfigContextProfile
extras/config-templates                              extras.ConfigTemplate
extras/scripts/upload                                extras.ScriptModule
extras/scripts                                       extras.Script
ipam/asns                                            ipam.ASN
ipam/asn-ranges                                      ipam.ASNRange
ipam/vrfs                                            ipam.VRF
ipam/route-targets                                   ipam.RouteTarget
ipam/rirs                                            ipam.RIR
ipam/aggregates                                      ipam.Aggregate
ipam/roles                                           ipam.Role
ipam/prefixes                                        ipam.Prefix
ipam/ip-ranges                                       ipam.IPRange
ipam/ip-addresses                                    ipam.IPAddress
ipam/fhrp-groups                                     ipam.FHRPGroup
ipam/fhrp-group-assignments                          ipam.FHRPGroupAssignment
ipam/vlan-groups                                     ipam.VLANGroup
ipam/vlans                                           ipam.VLAN
ipam/vlan-translation-policies                       ipam.VLANTranslationPolicy
ipam/vlan-translation-rules                          ipam.VLANTranslationRule
ipam/service-templates                               ipam.ServiceTemplate
ipam/services                                        ipam.Service
tenancy/tenant-groups                                tenancy.TenantGroup
tenancy/tenants                                      tenancy.Tenant
tenancy/contact-groups                               tenancy.ContactGroup
tenancy/contact-roles                                tenancy.ContactRole
tenancy/contacts                                     tenancy.Contact
tenancy/contact-assignments                          tenancy.ContactAssignment
users/users                                          users.User
users/groups                                         users.Group
users/tokens                                         users.Token
users/permissions                                    users.ObjectPermission
users/owner-groups                                   users.OwnerGroup
users/owners                                         users.Owner
users/config                                         users.UserConfig
virtualization/cluster-types                         virtualization.ClusterType
virtualization/cluster-groups                        virtualization.ClusterGroup
virtualization/clusters                              virtualization.Cluster
virtualization/virtual-machine-types                 virtualization.VirtualMachineType
virtualization/virtual-machines                      virtualization.VirtualMachine
virtualization/interfaces                            virtualization.VMInterface
virtualization/virtual-disks                         virtualization.VirtualDisk
vpn/ike-policies                                     vpn.IKEPolicy
vpn/ike-proposals                                    vpn.IKEProposal
vpn/ipsec-policies                                   vpn.IPSecPolicy
vpn/ipsec-proposals                                  vpn.IPSecProposal
vpn/ipsec-profiles                                   vpn.IPSecProfile
vpn/tunnel-groups                                    vpn.TunnelGroup
vpn/tunnels                                          vpn.Tunnel
vpn/tunnel-terminations                              vpn.TunnelTermination
vpn/l2vpns                                           vpn.L2VPN
vpn/l2vpn-terminations                               vpn.L2VPNTermination
wireless/wireless-lan-groups                         wireless.WirelessLANGroup
wireless/wireless-lans                               wireless.WirelessLAN
wireless/wireless-links                              wireless.WirelessLink
```

## Models

```
## circuits.BaseCircuitType   (circuits/models/base.py)
   bases: OrganizationalModel
     color                                  ColorField
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## circuits.Circuit   (circuits/models/circuits.py)
   bases: ContactsMixin, ImageAttachmentsMixin, DistanceMixin, PrimaryModel
     cid                                    CharField                   REQ len=100
     provider                               ForeignKey                  REQ -> circuits.Provider on_delete=PROTECT
     provider_account                       ForeignKey                      -> circuits.ProviderAccount on_delete=PROTECT
     type                                   ForeignKey                  REQ -> circuits.CircuitType on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:CircuitStatusChoices.STATUS_ACTIVE choices=CircuitStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     install_date                           DateField
     termination_date                       DateField
     commit_rate                            PositiveIntegerField
     termination_a                          ForeignKey                      -> circuits.CircuitTermination on_delete=SET_NULL
     termination_z                          ForeignKey                      -> circuits.CircuitTermination on_delete=SET_NULL
     group_assignments                      GenericRelation
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     distance (DistanceMixin)               DecimalField                    decimal(8,2)
     distance_unit (DistanceMixin)          CharField                       len=50 choices=DistanceUnitChoices
     _abs_distance (DistanceMixin)          DecimalField                    decimal(13,4)
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'cid'),
      name='%(app_label)s_%(class)s_unique_provider_cid'), models.UniqueConstraint(fields=('provider_account',
      'cid'), name='%(app_label)s_%(class)s_unique_provideraccount_cid'))
   meta.ordering: ['provider', 'provider_account', 'cid']
   meta.indexes: (models.Index(fields=('provider', 'provider_account', 'cid')),)

## circuits.CircuitGroup   (circuits/models/circuits.py)
   bases: OrganizationalModel
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## circuits.CircuitGroupAssignment   (circuits/models/circuits.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     member_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     member_id                              PositiveBigIntegerField     REQ
     member                                 GenericForeignKey           REQ
     group                                  ForeignKey                  REQ -> circuits.CircuitGroup on_delete=CASCADE
     priority                               CharField                       len=50 choices=CircuitPriorityChoices
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('member_type', 'member_id', 'group'),
      name='%(app_label)s_%(class)s_unique_member_group'),)
   meta.ordering: ('group', 'member_type', 'member_id', 'priority', 'pk')
   meta.indexes: (models.Index(fields=('group', 'member_type', 'member_id', 'priority', 'id')),)

## circuits.CircuitTermination   (circuits/models/circuits.py)
   bases: CustomFieldsMixin, CustomLinksMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel, CabledObjectModel
     circuit                                ForeignKey                  REQ -> circuits.Circuit on_delete=CASCADE
     term_side                              CharField                   REQ len=1 choices=CircuitTerminationSideChoices
     termination_type                       ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     termination_id                         PositiveBigIntegerField
     termination                            GenericForeignKey
     port_speed                             PositiveIntegerField
     upstream_speed                         PositiveIntegerField
     xconnect_id                            CharField                       len=50
     pp_info                                CharField                       len=100
     description                            CharField                       len=200
     _provider_network                      ForeignKey                      -> circuits.ProviderNetwork on_delete=PROTECT
     _location                              ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site                                  ForeignKey                      -> dcim.Site on_delete=CASCADE
     _region                                ForeignKey                      -> dcim.Region on_delete=CASCADE
     _site_group                            ForeignKey                      -> dcim.SiteGroup on_delete=CASCADE
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
   meta.constraints: (models.UniqueConstraint(fields=('circuit', 'term_side'),
      name='%(app_label)s_%(class)s_unique_circuit_term_side'),)
   meta.ordering: ['circuit', 'term_side']
   meta.indexes: (models.Index(fields=('termination_type', 'termination_id')),)

## circuits.CircuitType   (circuits/models/circuits.py)
   bases: BaseCircuitType
   (no own columns — every field is inherited from BaseCircuitType)
     color (BaseCircuitType)                ColorField
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## circuits.Provider   (circuits/models/providers.py)
   bases: ContactsMixin, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     asns                                   ManyToManyField                 -> ipam.ASN
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['name']

## circuits.ProviderAccount   (circuits/models/providers.py)
   bases: ContactsMixin, PrimaryModel
     provider                               ForeignKey                  REQ -> circuits.Provider on_delete=PROTECT
     account                                CharField                   REQ len=100
     name                                   CharField                       len=100
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'account'),
      name='%(app_label)s_%(class)s_unique_provider_account'), models.UniqueConstraint(fields=('provider',
      'name'), name='%(app_label)s_%(class)s_unique_provider_name', condition=~Q(name='')))
   meta.ordering: ('provider', 'account')

## circuits.ProviderNetwork   (circuits/models/providers.py)
   bases: PrimaryModel
     name                                   CharField                   REQ len=100
     provider                               ForeignKey                  REQ -> circuits.Provider on_delete=PROTECT
     service_id                             CharField                       len=100
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'name'),
      name='%(app_label)s_%(class)s_unique_provider_name'),)
   meta.ordering: ('provider', 'name')

## circuits.VirtualCircuit   (circuits/models/virtual_circuits.py)
   bases: ContactsMixin, PrimaryModel
     cid                                    CharField                   REQ len=100
     provider_network                       ForeignKey                  REQ -> circuits.ProviderNetwork on_delete=PROTECT
     provider_account                       ForeignKey                      -> circuits.ProviderAccount on_delete=PROTECT
     type                                   ForeignKey                  REQ -> circuits.VirtualCircuitType on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:CircuitStatusChoices.STATUS_ACTIVE choices=CircuitStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     group_assignments                      GenericRelation
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('provider_network', 'cid'),
      name='%(app_label)s_%(class)s_unique_provider_network_cid'),
      models.UniqueConstraint(fields=('provider_account', 'cid'),
      name='%(app_label)s_%(class)s_unique_provideraccount_cid'))
   meta.ordering: ['provider_network', 'provider_account', 'cid']
   meta.indexes: (models.Index(fields=('provider_network', 'provider_account', 'cid')),)

## circuits.VirtualCircuitTermination   (circuits/models/virtual_circuits.py)
   bases: CustomFieldsMixin, CustomLinksMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     virtual_circuit                        ForeignKey                  REQ -> circuits.VirtualCircuit on_delete=CASCADE
     role                                   CharField                       len=50 def=UNRESOLVED:VirtualCircuitTerminationRoleChoices.ROLE_PEER choices=VirtualCircuitTerminationRoleChoices
     interface                              OneToOneField               REQ -> dcim.Interface on_delete=CASCADE
     description                            CharField                       len=200
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ['virtual_circuit', 'role', 'pk']
   meta.indexes: (models.Index(fields=('virtual_circuit', 'role', 'id')),)

## circuits.VirtualCircuitType   (circuits/models/virtual_circuits.py)
   bases: BaseCircuitType
   (no own columns — every field is inherited from BaseCircuitType)
     color (BaseCircuitType)                ColorField
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## core.AutoSyncRecord   (core/models/data.py)
   bases: models.Model
     datafile                               ForeignKey                  REQ -> core.DataFile on_delete=CASCADE
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     object_id                              PositiveBigIntegerField     REQ
     object                                 GenericForeignKey           REQ
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id'),
      name='%(app_label)s_%(class)s_object'),)

## core.ConfigRevision   (core/models/config.py)
   bases: models.Model
     active                                 BooleanField                    def=False
     created                                DateTimeField               REQ
     comment                                CharField                       len=200
     data                                   JSONField
   meta.constraints: [models.UniqueConstraint(fields=('active',), condition=models.Q(active=True),
      name='unique_active_config_revision')]
   meta.ordering: ['-created']
   meta.indexes: (models.Index(fields=('-created',)),)

## core.DataFile   (core/models/data.py)
   bases: models.Model
     created                                DateTimeField               REQ
     last_updated                           DateTimeField               REQ
     source                                 ForeignKey                  REQ -> core.DataSource on_delete=CASCADE
     path                                   CharField                   REQ len=1000
     size                                   PositiveIntegerField        REQ
     hash                                   CharField                   REQ len=64
     data                                   BinaryField                 REQ
   meta.constraints: (models.UniqueConstraint(fields=('source', 'path'),
      name='%(app_label)s_%(class)s_unique_source_path'),)
   meta.ordering: ('source', 'path')

## core.DataSource   (core/models/data.py)
   bases: JobsMixin, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     type                                   CharField                   REQ len=50
     source_url                             CharField                   REQ len=200
     status                                 CharField                       len=50 def=UNRESOLVED:DataSourceStatusChoices.NEW choices=DataSourceStatusChoices
     enabled                                BooleanField                    def=True
     sync_interval                          PositiveSmallIntegerField       choices=JobIntervalChoices
     ignore_rules                           TextField
     parameters                             JSONField
     last_synced                            DateTimeField
     jobs (JobsMixin)                       GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## core.Job   (core/models/jobs.py)
   bases: models.Model
     object_type                            ForeignKey                      -> contenttypes.ContentType on_delete=CASCADE
     object_id                              PositiveBigIntegerField
     object                                 GenericForeignKey
     name                                   CharField                   REQ len=200
     created                                DateTimeField               REQ
     scheduled                              DateTimeField
     interval                               PositiveIntegerField
     started                                DateTimeField
     completed                              DateTimeField
     user                                   ForeignKey                      -> settings.AUTH_USER_MODEL on_delete=SET_NULL
     status                                 CharField                       len=30 def=UNRESOLVED:JobStatusChoices.STATUS_PENDING choices=JobStatusChoices
     data                                   JSONField
     error                                  TextField
     job_id                                 UUIDField                   REQ UNIQUE
     queue_name                             CharField                       len=100
     notifications                          CharField                       len=30 def=UNRESOLVED:JobNotificationChoices.NOTIFICATION_ALWAYS choices=JobNotificationChoices
     log_entries                            ArrayField                      def=UNRESOLVED:list
   meta.ordering: ['-created']
   meta.indexes: (models.Index(fields=('-created',)), models.Index(fields=('object_type', 'object_id')))

## core.ManagedFile   (core/models/files.py)
   bases: SyncedDataMixin, models.Model
     created                                DateTimeField               REQ
     last_updated                           DateTimeField
     file_root                              CharField                   REQ len=1000 choices=ManagedFileRootPathChoices
     file_path                              FilePathField               REQ
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('file_root', 'file_path'),
      name='%(app_label)s_%(class)s_unique_root_path'),)
   meta.ordering: ('file_root', 'file_path')

## core.ObjectChange   (core/models/change_logging.py)
   bases: models.Model
     time                                   DateTimeField               REQ
     user                                   ForeignKey                      -> settings.AUTH_USER_MODEL on_delete=SET_NULL
     user_name                              CharField                   REQ len=150
     request_id                             UUIDField                   REQ
     action                                 CharField                   REQ len=50 choices=ObjectChangeActionChoices
     changed_object_type                    ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     changed_object_id                      PositiveBigIntegerField     REQ
     changed_object                         GenericForeignKey           REQ
     related_object_type                    ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     related_object_id                      PositiveBigIntegerField
     related_object                         GenericForeignKey
     object_repr                            CharField                   REQ len=200
     message                                CharField                       len=200
     prechange_data                         JSONField
     postchange_data                        JSONField
   meta.ordering: ['-time']
   meta.indexes: (models.Index(fields=('changed_object_type', 'changed_object_id')),
      models.Index(fields=('related_object_type', 'related_object_id')))

## core.ObjectType   (core/models/object_types.py)
   bases: ContentType
     contenttype_ptr                        OneToOneField               REQ -> contenttypes.ContentType on_delete=CASCADE
     public                                 BooleanField                    def=False
     features                               ArrayField                      def=UNRESOLVED:list
   meta.ordering: ('app_label', 'model')
   meta.indexes: [GinIndex(fields=['features'])]

## dcim.BaseInterface   (dcim/models/device_components.py)
   bases: models.Model
     enabled                                BooleanField                    def=True
     mtu                                    PositiveIntegerField
     mode                                   CharField                       len=50 choices=InterfaceModeChoices
     parent                                 ForeignKey                      -> dcim.BaseInterface on_delete=RESTRICT
     bridge                                 ForeignKey                      -> dcim.BaseInterface on_delete=SET_NULL
     untagged_vlan                          ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     tagged_vlans                           ManyToManyField                 -> ipam.VLAN
     qinq_svlan                             ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     vlan_translation_policy                ForeignKey                      -> ipam.VLANTranslationPolicy on_delete=PROTECT
     primary_mac_address                    OneToOneField                   -> dcim.MACAddress on_delete=SET_NULL

## dcim.Cable   (dcim/models/cables.py)
   bases: PrimaryModel
     type                                   CharField                       len=50 choices=CableTypeChoices
     status                                 CharField                       len=50 def=UNRESOLVED:LinkStatusChoices.STATUS_CONNECTED choices=LinkStatusChoices
     profile                                CharField                       len=50 choices=CableProfileChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     label                                  CharField                       len=100
     color                                  ColorField
     length                                 DecimalField                    decimal(8,2)
     length_unit                            CharField                       len=50 choices=CableLengthUnitChoices
     _abs_length                            DecimalField                    decimal(14,4)
     bundle                                 ForeignKey                      -> dcim.CableBundle on_delete=SET_NULL
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('pk',)

## dcim.CableBundle   (dcim/models/cables.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.CablePath   (dcim/models/cables.py)
   bases: models.Model
     path                                   JSONField                       def=UNRESOLVED:list
     is_active                              BooleanField                    def=False
     is_complete                            BooleanField                    def=False
     is_split                               BooleanField                    def=False
     _nodes                                 PathField                   REQ
   meta.indexes: (GinIndex(fields=('_nodes',)),)

## dcim.CableTermination   (dcim/models/cables.py)
   bases: ChangeLoggedModel
     cable                                  ForeignKey                  REQ -> dcim.Cable on_delete=CASCADE
     cable_end                              CharField                   REQ len=1 choices=CableEndChoices
     termination_type                       ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     termination_id                         PositiveBigIntegerField     REQ
     termination                            GenericForeignKey           REQ
     connector                              PositiveSmallIntegerField
     positions                              ArrayField
     _device                                ForeignKey                      -> dcim.Device on_delete=CASCADE
     _rack                                  ForeignKey                      -> dcim.Rack on_delete=CASCADE
     _location                              ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site                                  ForeignKey                      -> dcim.Site on_delete=CASCADE
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('termination_type', 'termination_id'),
      name='%(app_label)s_%(class)s_unique_termination'), models.UniqueConstraint(fields=('cable',
      'cable_end', 'connector'), name='%(app_label)s_%(class)s_unique_connector'))
   meta.ordering: ('cable', 'cable_end', 'connector', 'pk')

## dcim.CabledObjectModel   (dcim/models/device_components.py)
   bases: models.Model
     cable                                  ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end                              CharField                       len=1 choices=CableEndChoices
     cable_connector                        PositiveSmallIntegerField
     cable_positions                        ArrayField
     mark_connected                         BooleanField                    def=False
     cable_terminations                     GenericRelation

## dcim.CachedScopeMixin   (dcim/models/mixins.py)
   bases: models.Model
     scope_type                             ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     scope_id                               PositiveBigIntegerField
     scope                                  GenericForeignKey
     _location                              ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site                                  ForeignKey                      -> dcim.Site on_delete=CASCADE
     _region                                ForeignKey                      -> dcim.Region on_delete=SET_NULL
     _site_group                            ForeignKey                      -> dcim.SiteGroup on_delete=SET_NULL

## dcim.ComponentModel   (dcim/models/device_components.py)
   bases: OwnerMixin, NetBoxModel
     device                                 ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name                                   CharField                   REQ len=64
     label                                  CharField                       len=64
     description                            CharField                       len=200
     _site                                  ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location                              ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack                                  ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('device', 'name'),
      name='%(app_label)s_%(class)s_unique_device_name'),)
   meta.ordering: ('device', 'name')

## dcim.ComponentTemplateModel   (dcim/models/device_component_templates.py)
   bases: ChangeLoggedModel, TrackingModelMixin
     device_type                            ForeignKey                  REQ -> dcim.DeviceType on_delete=CASCADE
     name                                   CharField                   REQ len=64
     label                                  CharField                       len=64
     description                            CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'),
      name='%(app_label)s_%(class)s_unique_device_type_name'),)
   meta.ordering: ('device_type', 'name')

## dcim.ConsolePort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                                   CharField                       len=50 choices=ConsolePortTypeChoices
     speed                                  PositiveIntegerField            choices=ConsolePortSpeedChoices
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL

## dcim.ConsolePortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                       len=50 choices=ConsolePortTypeChoices
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.ConsoleServerPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                                   CharField                       len=50 choices=ConsolePortTypeChoices
     speed                                  PositiveIntegerField            choices=ConsolePortSpeedChoices
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL

## dcim.ConsoleServerPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                       len=50 choices=ConsolePortTypeChoices
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.Device   (dcim/models/devices.py)
   bases: ContactsMixin, ImageAttachmentsMixin, RenderConfigMixin, ConfigContextModel, TrackingModelMixin, PrimaryModel
     device_type                            ForeignKey                  REQ -> dcim.DeviceType on_delete=PROTECT
     role                                   ForeignKey                  REQ -> dcim.DeviceRole on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     platform                               ForeignKey                      -> dcim.Platform on_delete=SET_NULL
     name                                   CharField                       len=64
     serial                                 CharField                       len=50
     asset_tag                              CharField                       UNIQUE len=50
     site                                   ForeignKey                  REQ -> dcim.Site on_delete=PROTECT
     location                               ForeignKey                      -> dcim.Location on_delete=PROTECT
     rack                                   ForeignKey                      -> dcim.Rack on_delete=PROTECT
     position                               DecimalField                    decimal(4,1)
     face                                   CharField                       len=50 choices=DeviceFaceChoices
     status                                 CharField                       len=50 def=UNRESOLVED:DeviceStatusChoices.STATUS_ACTIVE choices=DeviceStatusChoices
     airflow                                CharField                       len=50 choices=DeviceAirflowChoices
     primary_ip4                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     primary_ip6                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     oob_ip                                 OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     cluster                                ForeignKey                      -> virtualization.Cluster on_delete=SET_NULL
     virtual_chassis                        ForeignKey                      -> dcim.VirtualChassis on_delete=SET_NULL
     vc_position                            PositiveIntegerField
     vc_priority                            PositiveSmallIntegerField
     latitude                               DecimalField                    decimal(8,6)
     longitude                              DecimalField                    decimal(9,6)
     services                               GenericRelation
     console_port_count                     CounterCacheField
     console_server_port_count              CounterCacheField
     power_port_count                       CounterCacheField
     power_outlet_count                     CounterCacheField
     interface_count                        CounterCacheField
     front_port_count                       CounterCacheField
     rear_port_count                        CounterCacheField
     device_bay_count                       CounterCacheField
     module_bay_count                       CounterCacheField
     inventory_item_count                   CounterCacheField
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     config_template (RenderConfigMixin)    ForeignKey                      -> extras.ConfigTemplate on_delete=PROTECT
     local_context_data (ConfigContextModel) JSONField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(Lower('name'), 'site', 'tenant',
      name='%(app_label)s_%(class)s_unique_name_site_tenant'), models.UniqueConstraint(Lower('name'), 'site',
      name='%(app_label)s_%(class)s_unique_name_site', condition=Q(tenant__isnull=True),
      violation_error_message=_('Device name must be unique per site.')),
      models.UniqueConstraint(fields=('rack', 'position', 'face'),
      name='%(app_label)s_%(class)s_unique_rack_position_face'),
      models.UniqueConstraint(fields=('virtual_chassis', 'vc_position'),
      name='%(app_label)s_%(class)s_unique_virtual_chassis_vc_position'))
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')),)

## dcim.DeviceBay   (dcim/models/device_components.py)
   bases: ComponentModel, TrackingModelMixin
     installed_device                       OneToOneField                   -> dcim.Device on_delete=SET_NULL
     enabled                                BooleanField                    def=True
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## dcim.DeviceBayTemplate   (dcim/models/device_component_templates.py)
   bases: ComponentTemplateModel
     enabled                                BooleanField                    def=True
     device_type (ComponentTemplateModel)   ForeignKey                  REQ -> dcim.DeviceType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.DeviceRole   (dcim/models/devices.py)
   bases: NestedGroupModel
     color                                  ColorField                      def=UNRESOLVED:ColorChoices.COLOR_GREY
     vm_role                                BooleanField                    def=True
     config_template                        ForeignKey                      -> extras.ConfigTemplate on_delete=PROTECT
     parent (NestedGroupModel)              TreeForeignKey                  -> dcim.DeviceRole on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'),
      name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',),
      name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level device role with this name already exists.')), models.UniqueConstraint(fields=('parent',
      'slug'), name='%(app_label)s_%(class)s_parent_slug'), models.UniqueConstraint(fields=('slug',),
      name='%(app_label)s_%(class)s_slug', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level device role with this slug already exists.')))
   meta.ordering: ('name',)
   meta.indexes: ()

## dcim.DeviceType   (dcim/models/devices.py)
   bases: ImageAttachmentsMixin, PrimaryModel, WeightMixin
     manufacturer                           ForeignKey                  REQ -> dcim.Manufacturer on_delete=PROTECT
     model                                  CharField                   REQ len=100
     slug                                   SlugField                   REQ len=100
     default_platform                       ForeignKey                      -> dcim.Platform on_delete=SET_NULL
     part_number                            CharField                       len=50
     u_height                               DecimalField                    decimal(4,1) def=1.0
     exclude_from_utilization               BooleanField                    def=False
     is_full_depth                          BooleanField                    def=True
     subdevice_role                         CharField                       len=50 choices=SubdeviceRoleChoices
     airflow                                CharField                       len=50 choices=DeviceAirflowChoices
     front_image                            ImageField
     rear_image                             ImageField
     console_port_template_count            CounterCacheField
     console_server_port_template_count     CounterCacheField
     power_port_template_count              CounterCacheField
     power_outlet_template_count            CounterCacheField
     interface_template_count               CounterCacheField
     front_port_template_count              CounterCacheField
     rear_port_template_count               CounterCacheField
     device_bay_template_count              CounterCacheField
     module_bay_template_count              CounterCacheField
     inventory_item_template_count          CounterCacheField
     device_count                           CounterCacheField
     images (ImageAttachmentsMixin)         GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     weight (WeightMixin)                   DecimalField                    decimal(8,2)
     weight_unit (WeightMixin)              CharField                       len=50 choices=WeightUnitChoices
     _abs_weight (WeightMixin)              PositiveBigIntegerField
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'),
      name='%(app_label)s_%(class)s_unique_manufacturer_model'),
      models.UniqueConstraint(fields=('manufacturer', 'slug'),
      name='%(app_label)s_%(class)s_unique_manufacturer_slug'))
   meta.ordering: ['manufacturer', 'model']

## dcim.FrontPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, TrackingModelMixin
     type                                   CharField                   REQ len=50 choices=PortTypeChoices
     color                                  ColorField
     positions                              PositiveSmallIntegerField       def=1
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
   meta.constraints: (models.UniqueConstraint(fields=('device', 'name'),
      name='%(app_label)s_%(class)s_unique_device_name'),)

## dcim.FrontPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                   REQ len=50 choices=PortTypeChoices
     color                                  ColorField
     positions                              PositiveSmallIntegerField       def=1
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'),
      name='%(app_label)s_%(class)s_unique_device_type_name'), models.UniqueConstraint(fields=('module_type',
      'name'), name='%(app_label)s_%(class)s_unique_module_type_name'))

## dcim.Interface   (dcim/models/device_components.py)
   bases: InterfaceValidationMixin, ModularComponentModel, BaseInterface, CabledObjectModel, PathEndpoint, TrackingModelMixin
     _name                                  NaturalOrderingField            len=100
     vdcs                                   ManyToManyField                 -> dcim.VirtualDeviceContext
     lag                                    ForeignKey                      -> dcim.Interface on_delete=SET_NULL
     type                                   CharField                   REQ len=50 choices=InterfaceTypeChoices
     mgmt_only                              BooleanField                    def=False
     speed                                  PositiveBigIntegerField
     duplex                                 CharField                       len=50 choices=InterfaceDuplexChoices
     wwn                                    WWNField
     rf_role                                CharField                       len=30 choices=WirelessRoleChoices
     rf_channel                             CharField                       len=50 choices=WirelessChannelChoices
     rf_channel_frequency                   DecimalField                    decimal(8,3)
     rf_channel_width                       DecimalField                    decimal(7,3)
     tx_power                               SmallIntegerField
     poe_mode                               CharField                       len=50 choices=InterfacePoEModeChoices
     poe_type                               CharField                       len=50 choices=InterfacePoETypeChoices
     wireless_link                          ForeignKey                      -> wireless.WirelessLink on_delete=SET_NULL
     wireless_lans                          ManyToManyField                 -> wireless.WirelessLAN
     vrf                                    ForeignKey                      -> ipam.VRF on_delete=SET_NULL
     ip_addresses                           GenericRelation
     mac_addresses                          GenericRelation
     fhrp_group_assignments                 GenericRelation
     tunnel_terminations                    GenericRelation
     l2vpn_terminations                     GenericRelation
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     enabled (BaseInterface)                BooleanField                    def=True
     mtu (BaseInterface)                    PositiveIntegerField
     mode (BaseInterface)                   CharField                       len=50 choices=InterfaceModeChoices
     parent (BaseInterface)                 ForeignKey                      -> dcim.Interface on_delete=RESTRICT
     bridge (BaseInterface)                 ForeignKey                      -> dcim.Interface on_delete=SET_NULL
     untagged_vlan (BaseInterface)          ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     tagged_vlans (BaseInterface)           ManyToManyField                 -> ipam.VLAN
     qinq_svlan (BaseInterface)             ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     vlan_translation_policy (BaseInterface) ForeignKey                      -> ipam.VLANTranslationPolicy on_delete=PROTECT
     primary_mac_address (BaseInterface)    OneToOneField                   -> dcim.MACAddress on_delete=SET_NULL
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL
   meta.ordering: ('device', CollateAsChar('_name'))

## dcim.InterfaceTemplate   (dcim/models/device_component_templates.py)
   bases: InterfaceValidationMixin, ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     _name                                  NaturalOrderingField            len=100
     type                                   CharField                   REQ len=50 choices=InterfaceTypeChoices
     enabled                                BooleanField                    def=True
     mgmt_only                              BooleanField                    def=False
     bridge                                 ForeignKey                      -> dcim.InterfaceTemplate on_delete=SET_NULL
     poe_mode                               CharField                       len=50 choices=InterfacePoEModeChoices
     poe_type                               CharField                       len=50 choices=InterfacePoETypeChoices
     rf_role                                CharField                       len=30 choices=WirelessRoleChoices
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.InventoryItem   (dcim/models/device_components.py)
   bases: MPTTModel, ComponentModel, TrackingModelMixin
     parent                                 TreeForeignKey                  -> dcim.InventoryItem on_delete=CASCADE
     component_type                         ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     component_id                           PositiveBigIntegerField
     component                              GenericForeignKey
     status                                 CharField                       len=50 def=UNRESOLVED:InventoryItemStatusChoices.STATUS_ACTIVE choices=InventoryItemStatusChoices
     role                                   ForeignKey                      -> dcim.InventoryItemRole on_delete=PROTECT
     manufacturer                           ForeignKey                      -> dcim.Manufacturer on_delete=PROTECT
     part_id                                CharField                       len=50
     serial                                 CharField                       len=50
     asset_tag                              CharField                       UNIQUE len=50
     discovered                             BooleanField                    def=False
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('device', 'parent', 'name'),
      name='%(app_label)s_%(class)s_unique_device_parent_name'),)
   meta.ordering: ('device__id', 'parent__id', 'name')
   meta.indexes: (models.Index(fields=('component_type', 'component_id')),)

## dcim.InventoryItemRole   (dcim/models/device_components.py)
   bases: OrganizationalModel
     color                                  ColorField                      def=UNRESOLVED:ColorChoices.COLOR_GREY
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.InventoryItemTemplate   (dcim/models/device_component_templates.py)
   bases: MPTTModel, ComponentTemplateModel
     parent                                 TreeForeignKey                  -> dcim.InventoryItemTemplate on_delete=CASCADE
     component_type                         ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     component_id                           PositiveBigIntegerField
     component                              GenericForeignKey
     role                                   ForeignKey                      -> dcim.InventoryItemRole on_delete=PROTECT
     manufacturer                           ForeignKey                      -> dcim.Manufacturer on_delete=PROTECT
     part_id                                CharField                       len=50
     device_type (ComponentTemplateModel)   ForeignKey                  REQ -> dcim.DeviceType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'parent', 'name'),
      name='%(app_label)s_%(class)s_unique_device_type_parent_name'),)
   meta.ordering: ('device_type__id', 'parent__id', 'name')
   meta.indexes: (models.Index(fields=('component_type', 'component_id')),)

## dcim.Location   (dcim/models/sites.py)
   bases: ContactsMixin, ImageAttachmentsMixin, NestedGroupModel
     site                                   ForeignKey                  REQ -> dcim.Site on_delete=CASCADE
     status                                 CharField                       len=50 def=UNRESOLVED:LocationStatusChoices.STATUS_ACTIVE choices=LocationStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     facility                               CharField                       len=50
     prefixes                               GenericRelation
     vlan_groups                            GenericRelation
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     parent (NestedGroupModel)              TreeForeignKey                  -> dcim.Location on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('site', 'parent', 'name'),
      name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('site', 'name'),
      name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A
      location with this name already exists within the specified site.')),
      models.UniqueConstraint(fields=('site', 'parent', 'slug'), name='%(app_label)s_%(class)s_parent_slug'),
      models.UniqueConstraint(fields=('site', 'slug'), name='%(app_label)s_%(class)s_slug',
      condition=Q(parent__isnull=True), violation_error_message=_('A location with this slug already exists
      within the specified site.')))
   meta.ordering: ['site', 'name']
   meta.indexes: ()

## dcim.MACAddress   (dcim/models/devices.py)
   bases: PrimaryModel
     mac_address                            MACAddressField             REQ
     assigned_object_type                   ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     assigned_object_id                     PositiveBigIntegerField
     assigned_object                        GenericForeignKey
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('mac_address', 'pk')
   meta.indexes: (models.Index(fields=('mac_address', 'id')), models.Index(fields=('assigned_object_type',
      'assigned_object_id')))

## dcim.Manufacturer   (dcim/models/devices.py)
   bases: ContactsMixin, OrganizationalModel
   (no own columns — every field is inherited from ContactsMixin, OrganizationalModel)
     contacts (ContactsMixin)               GenericRelation
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.ModularComponentModel   (dcim/models/device_components.py)
   bases: ComponentModel
     module                                 ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items                        GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## dcim.ModularComponentTemplateModel   (dcim/models/device_component_templates.py)
   bases: ComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     device_type                            ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type                            ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'),
      name='%(app_label)s_%(class)s_unique_device_type_name'), models.UniqueConstraint(fields=('module_type',
      'name'), name='%(app_label)s_%(class)s_unique_module_type_name'))
   meta.ordering: ('device_type', 'module_type', 'name')
   meta.indexes: (models.Index(fields=('device_type', 'module_type', 'name')),)

## dcim.Module   (dcim/models/modules.py)
   bases: TrackingModelMixin, PrimaryModel
     device                                 ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     module_bay                             OneToOneField               REQ -> dcim.ModuleBay on_delete=CASCADE
     module_type                            ForeignKey                  REQ -> dcim.ModuleType on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:ModuleStatusChoices.STATUS_ACTIVE choices=ModuleStatusChoices
     serial                                 CharField                       len=50
     asset_tag                              CharField                       UNIQUE len=50
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('module_bay',)

## dcim.ModuleBay   (dcim/models/device_components.py)
   bases: ModularComponentModel, TrackingModelMixin, MPTTModel
     parent                                 TreeForeignKey                  -> dcim.ModuleBay on_delete=CASCADE
     position                               CharField                       len=30
     enabled                                BooleanField                    def=True
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('device', 'module', 'name'),
      name='%(app_label)s_%(class)s_unique_device_module_name'),)
   meta.indexes: ()

## dcim.ModuleBayTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     position                               CharField                       len=30
     enabled                                BooleanField                    def=True
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.ModuleType   (dcim/models/modules.py)
   bases: ImageAttachmentsMixin, PrimaryModel, WeightMixin
     profile                                ForeignKey                      -> dcim.ModuleTypeProfile on_delete=PROTECT
     manufacturer                           ForeignKey                  REQ -> dcim.Manufacturer on_delete=PROTECT
     model                                  CharField                   REQ len=100
     part_number                            CharField                       len=50
     airflow                                CharField                       len=50 choices=ModuleAirflowChoices
     attribute_data                         JSONField
     module_count                           CounterCacheField
     console_port_template_count            CounterCacheField
     console_server_port_template_count     CounterCacheField
     power_port_template_count              CounterCacheField
     power_outlet_template_count            CounterCacheField
     interface_template_count               CounterCacheField
     front_port_template_count              CounterCacheField
     rear_port_template_count               CounterCacheField
     module_bay_template_count              CounterCacheField
     images (ImageAttachmentsMixin)         GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     weight (WeightMixin)                   DecimalField                    decimal(8,2)
     weight_unit (WeightMixin)              CharField                       len=50 choices=WeightUnitChoices
     _abs_weight (WeightMixin)              PositiveBigIntegerField
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'),
      name='%(app_label)s_%(class)s_unique_manufacturer_model'),)
   meta.ordering: ('profile', 'manufacturer', 'model')
   meta.indexes: (models.Index(fields=('profile', 'manufacturer', 'model')),)

## dcim.ModuleTypeProfile   (dcim/models/modules.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     schema                                 JSONField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.PathEndpoint   (dcim/models/device_components.py)
   bases: models.Model
     _path                                  ForeignKey                      -> dcim.CablePath on_delete=SET_NULL

## dcim.Platform   (dcim/models/devices.py)
   bases: NestedGroupModel
     manufacturer                           ForeignKey                      -> dcim.Manufacturer on_delete=PROTECT
     config_template                        ForeignKey                      -> extras.ConfigTemplate on_delete=PROTECT
     parent (NestedGroupModel)              TreeForeignKey                  -> dcim.Platform on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'name'),
      name='%(app_label)s_%(class)s_manufacturer_name'), models.UniqueConstraint(fields=('name',),
      name='%(app_label)s_%(class)s_name', condition=Q(manufacturer__isnull=True),
      violation_error_message=_('Platform name must be unique.')),
      models.UniqueConstraint(fields=('manufacturer', 'slug'),
      name='%(app_label)s_%(class)s_manufacturer_slug'), models.UniqueConstraint(fields=('slug',),
      name='%(app_label)s_%(class)s_slug', condition=Q(manufacturer__isnull=True),
      violation_error_message=_('Platform slug must be unique.')))
   meta.ordering: ('name',)
   meta.indexes: ()

## dcim.PortMapping   (dcim/models/device_components.py)
   bases: ChangeLoggingMixin, PortMappingBase
     device                                 ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     front_port                             ForeignKey                  REQ -> dcim.FrontPort on_delete=CASCADE
     rear_port                              ForeignKey                  REQ -> dcim.RearPort on_delete=CASCADE
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     front_port_position (PortMappingBase)  PositiveSmallIntegerField       def=1
     rear_port_position (PortMappingBase)   PositiveSmallIntegerField       def=1

## dcim.PortMappingBase   (dcim/models/base.py)
   bases: models.Model
     front_port_position                    PositiveSmallIntegerField       def=1
     rear_port_position                     PositiveSmallIntegerField       def=1
   meta.constraints: (models.UniqueConstraint(fields=('front_port', 'front_port_position'),
      name='%(app_label)s_%(class)s_unique_front_port_position'), models.UniqueConstraint(fields=('rear_port',
      'rear_port_position'), name='%(app_label)s_%(class)s_unique_rear_port_position'))

## dcim.PortTemplateMapping   (dcim/models/device_component_templates.py)
   bases: ChangeLoggingMixin, PortMappingBase
     device_type                            ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type                            ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     front_port                             ForeignKey                  REQ -> dcim.FrontPortTemplate on_delete=CASCADE
     rear_port                              ForeignKey                  REQ -> dcim.RearPortTemplate on_delete=CASCADE
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     front_port_position (PortMappingBase)  PositiveSmallIntegerField       def=1
     rear_port_position (PortMappingBase)   PositiveSmallIntegerField       def=1

## dcim.PowerFeed   (dcim/models/power.py)
   bases: PrimaryModel, PathEndpoint, CabledObjectModel
     power_panel                            ForeignKey                  REQ -> dcim.PowerPanel on_delete=PROTECT
     rack                                   ForeignKey                      -> dcim.Rack on_delete=PROTECT
     name                                   CharField                   REQ len=100
     status                                 CharField                       len=50 def=UNRESOLVED:PowerFeedStatusChoices.STATUS_ACTIVE choices=PowerFeedStatusChoices
     type                                   CharField                       len=50 def=UNRESOLVED:PowerFeedTypeChoices.TYPE_PRIMARY choices=PowerFeedTypeChoices
     supply                                 CharField                       len=50 def=UNRESOLVED:PowerFeedSupplyChoices.SUPPLY_AC choices=PowerFeedSupplyChoices
     phase                                  CharField                       len=50 def=UNRESOLVED:PowerFeedPhaseChoices.PHASE_SINGLE choices=PowerFeedPhaseChoices
     voltage                                SmallIntegerField               def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_VOLTAGE')
     amperage                               PositiveSmallIntegerField       def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_AMPERAGE')
     max_utilization                        PositiveSmallIntegerField       def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')
     available_power                        PositiveIntegerField            def=0
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
   meta.constraints: (models.UniqueConstraint(fields=('power_panel', 'name'),
      name='%(app_label)s_%(class)s_unique_power_panel_name'),)
   meta.ordering: ['power_panel', 'name']

## dcim.PowerOutlet   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     status                                 CharField                       len=50 def=UNRESOLVED:PowerOutletStatusChoices.STATUS_ENABLED choices=PowerOutletStatusChoices
     type                                   CharField                       len=50 choices=PowerOutletTypeChoices
     power_port                             ForeignKey                      -> dcim.PowerPort on_delete=SET_NULL
     feed_leg                               CharField                       len=50 choices=PowerOutletFeedLegChoices
     color                                  ColorField
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL

## dcim.PowerOutletTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                       len=50 choices=PowerOutletTypeChoices
     color                                  ColorField
     power_port                             ForeignKey                      -> dcim.PowerPortTemplate on_delete=SET_NULL
     feed_leg                               CharField                       len=50 choices=PowerOutletFeedLegChoices
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.PowerPanel   (dcim/models/power.py)
   bases: ContactsMixin, ImageAttachmentsMixin, PrimaryModel
     site                                   ForeignKey                  REQ -> dcim.Site on_delete=PROTECT
     location                               ForeignKey                      -> dcim.Location on_delete=PROTECT
     name                                   CharField                   REQ len=100
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('site', 'name'),
      name='%(app_label)s_%(class)s_unique_site_name'),)
   meta.ordering: ['site', 'name']

## dcim.PowerPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                                   CharField                       len=50 choices=PowerPortTypeChoices
     maximum_draw                           PositiveIntegerField
     allocated_draw                         PositiveIntegerField
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation
     _path (PathEndpoint)                   ForeignKey                      -> dcim.CablePath on_delete=SET_NULL

## dcim.PowerPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                       len=50 choices=PowerPortTypeChoices
     maximum_draw                           PositiveIntegerField
     allocated_draw                         PositiveIntegerField
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.Rack   (dcim/models/racks.py)
   bases: ContactsMixin, ImageAttachmentsMixin, TrackingModelMixin, RackBase
     form_factor                            CharField                       len=50 choices=RackFormFactorChoices
     rack_type                              ForeignKey                      -> dcim.RackType on_delete=PROTECT
     name                                   CharField                   REQ len=100
     facility_id                            CharField                       len=50
     site                                   ForeignKey                  REQ -> dcim.Site on_delete=PROTECT
     location                               ForeignKey                      -> dcim.Location on_delete=SET_NULL
     group                                  ForeignKey                      -> dcim.RackGroup on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:RackStatusChoices.STATUS_ACTIVE choices=RackStatusChoices
     role                                   ForeignKey                      -> dcim.RackRole on_delete=PROTECT
     serial                                 CharField                       len=50
     asset_tag                              CharField                       UNIQUE len=50
     airflow                                CharField                       len=50 choices=RackAirflowChoices
     vlan_groups                            GenericRelation
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     width (RackBase)                       PositiveSmallIntegerField       def=UNRESOLVED:RackWidthChoices.WIDTH_19IN choices=RackWidthChoices
     u_height (RackBase)                    PositiveSmallIntegerField       def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT
     starting_unit (RackBase)               PositiveSmallIntegerField       def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT
     desc_units (RackBase)                  BooleanField                    def=False
     outer_width (RackBase)                 PositiveSmallIntegerField
     outer_height (RackBase)                PositiveSmallIntegerField
     outer_depth (RackBase)                 PositiveSmallIntegerField
     outer_unit (RackBase)                  CharField                       len=50 choices=RackDimensionUnitChoices
     mounting_depth (RackBase)              PositiveSmallIntegerField
     max_weight (RackBase)                  PositiveIntegerField
     _abs_max_weight (RackBase)             PositiveBigIntegerField
     weight (WeightMixin)                   DecimalField                    decimal(8,2)
     weight_unit (WeightMixin)              CharField                       len=50 choices=WeightUnitChoices
     _abs_weight (WeightMixin)              PositiveBigIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('location', 'name'),
      name='%(app_label)s_%(class)s_unique_location_name'), models.UniqueConstraint(fields=('location',
      'facility_id'), name='%(app_label)s_%(class)s_unique_location_facility_id'))
   meta.ordering: ('site', 'location', 'name', 'pk')
   meta.indexes: (models.Index(fields=('site', 'location', 'name', 'id')),)

## dcim.RackBase   (dcim/models/racks.py)
   bases: WeightMixin, PrimaryModel
     width                                  PositiveSmallIntegerField       def=UNRESOLVED:RackWidthChoices.WIDTH_19IN choices=RackWidthChoices
     u_height                               PositiveSmallIntegerField       def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT
     starting_unit                          PositiveSmallIntegerField       def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT
     desc_units                             BooleanField                    def=False
     outer_width                            PositiveSmallIntegerField
     outer_height                           PositiveSmallIntegerField
     outer_depth                            PositiveSmallIntegerField
     outer_unit                             CharField                       len=50 choices=RackDimensionUnitChoices
     mounting_depth                         PositiveSmallIntegerField
     max_weight                             PositiveIntegerField
     _abs_max_weight                        PositiveBigIntegerField
     weight (WeightMixin)                   DecimalField                    decimal(8,2)
     weight_unit (WeightMixin)              CharField                       len=50 choices=WeightUnitChoices
     _abs_weight (WeightMixin)              PositiveBigIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## dcim.RackGroup   (dcim/models/racks.py)
   bases: OrganizationalModel
   (no own columns — every field is inherited from OrganizationalModel)
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.RackReservation   (dcim/models/racks.py)
   bases: PrimaryModel
   shadows inherited: description (PrimaryModel)
     rack                                   ForeignKey                  REQ -> dcim.Rack on_delete=CASCADE
     units                                  ArrayField                  REQ
     status                                 CharField                       len=50 def=UNRESOLVED:RackReservationStatusChoices.STATUS_ACTIVE choices=RackReservationStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     user                                   ForeignKey                  REQ -> settings.AUTH_USER_MODEL on_delete=PROTECT
     description                            CharField                   REQ len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['created', 'pk']
   meta.indexes: (models.Index(fields=('created', 'id')),)

## dcim.RackRole   (dcim/models/racks.py)
   bases: OrganizationalModel
     color                                  ColorField                      def=UNRESOLVED:ColorChoices.COLOR_GREY
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.RackType   (dcim/models/racks.py)
   bases: ImageAttachmentsMixin, RackBase
     form_factor                            CharField                   REQ len=50 choices=RackFormFactorChoices
     manufacturer                           ForeignKey                  REQ -> dcim.Manufacturer on_delete=PROTECT
     model                                  CharField                   REQ len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     rack_count                             CounterCacheField
     images (ImageAttachmentsMixin)         GenericRelation
     width (RackBase)                       PositiveSmallIntegerField       def=UNRESOLVED:RackWidthChoices.WIDTH_19IN choices=RackWidthChoices
     u_height (RackBase)                    PositiveSmallIntegerField       def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT
     starting_unit (RackBase)               PositiveSmallIntegerField       def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT
     desc_units (RackBase)                  BooleanField                    def=False
     outer_width (RackBase)                 PositiveSmallIntegerField
     outer_height (RackBase)                PositiveSmallIntegerField
     outer_depth (RackBase)                 PositiveSmallIntegerField
     outer_unit (RackBase)                  CharField                       len=50 choices=RackDimensionUnitChoices
     mounting_depth (RackBase)              PositiveSmallIntegerField
     max_weight (RackBase)                  PositiveIntegerField
     _abs_max_weight (RackBase)             PositiveBigIntegerField
     weight (WeightMixin)                   DecimalField                    decimal(8,2)
     weight_unit (WeightMixin)              CharField                       len=50 choices=WeightUnitChoices
     _abs_weight (WeightMixin)              PositiveBigIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'),
      name='%(app_label)s_%(class)s_unique_manufacturer_model'),
      models.UniqueConstraint(fields=('manufacturer', 'slug'),
      name='%(app_label)s_%(class)s_unique_manufacturer_slug'))
   meta.ordering: ('manufacturer', 'model')

## dcim.RearPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, TrackingModelMixin
     type                                   CharField                   REQ len=50 choices=PortTypeChoices
     color                                  ColorField
     positions                              PositiveSmallIntegerField       def=1
     module (ModularComponentModel)         ForeignKey                      -> dcim.Module on_delete=CASCADE
     inventory_items (ModularComponentModel) GenericRelation
     device (ComponentModel)                ForeignKey                  REQ -> dcim.Device on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     label (ComponentModel)                 CharField                       len=64
     description (ComponentModel)           CharField                       len=200
     _site (ComponentModel)                 ForeignKey                      -> dcim.Site on_delete=SET_NULL
     _location (ComponentModel)             ForeignKey                      -> dcim.Location on_delete=SET_NULL
     _rack (ComponentModel)                 ForeignKey                      -> dcim.Rack on_delete=SET_NULL
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     cable (CabledObjectModel)              ForeignKey                      -> dcim.Cable on_delete=SET_NULL
     cable_end (CabledObjectModel)          CharField                       len=1 choices=CableEndChoices
     cable_connector (CabledObjectModel)    PositiveSmallIntegerField
     cable_positions (CabledObjectModel)    ArrayField
     mark_connected (CabledObjectModel)     BooleanField                    def=False
     cable_terminations (CabledObjectModel) GenericRelation

## dcim.RearPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
   shadows inherited: device_type (ComponentTemplateModel)
     type                                   CharField                   REQ len=50 choices=PortTypeChoices
     color                                  ColorField
     positions                              PositiveSmallIntegerField       def=1
     device_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.DeviceType on_delete=CASCADE
     module_type (ModularComponentTemplateModel) ForeignKey                      -> dcim.ModuleType on_delete=CASCADE
     name (ComponentTemplateModel)          CharField                   REQ len=64
     label (ComponentTemplateModel)         CharField                       len=64
     description (ComponentTemplateModel)   CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## dcim.Region   (dcim/models/sites.py)
   bases: ContactsMixin, NestedGroupModel
     prefixes                               GenericRelation
     vlan_groups                            GenericRelation
     clusters                               GenericRelation
     wireless_lans                          GenericRelation
     contacts (ContactsMixin)               GenericRelation
     parent (NestedGroupModel)              TreeForeignKey                  -> dcim.Region on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'),
      name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',),
      name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level region with this name already exists.')), models.UniqueConstraint(fields=('parent', 'slug'),
      name='%(app_label)s_%(class)s_parent_slug'), models.UniqueConstraint(fields=('slug',),
      name='%(app_label)s_%(class)s_slug', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level region with this slug already exists.')))
   meta.indexes: ()

## dcim.RenderConfigMixin   (dcim/models/mixins.py)
   bases: models.Model
     config_template                        ForeignKey                      -> extras.ConfigTemplate on_delete=PROTECT

## dcim.Site   (dcim/models/sites.py)
   bases: ContactsMixin, ImageAttachmentsMixin, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     status                                 CharField                       len=50 def=UNRESOLVED:SiteStatusChoices.STATUS_ACTIVE choices=SiteStatusChoices
     region                                 ForeignKey                      -> dcim.Region on_delete=SET_NULL
     group                                  ForeignKey                      -> dcim.SiteGroup on_delete=SET_NULL
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     facility                               CharField                       len=50
     asns                                   ManyToManyField                 -> ipam.ASN
     time_zone                              TimeZoneField
     physical_address                       CharField                       len=200
     shipping_address                       CharField                       len=200
     latitude                               DecimalField                    decimal(8,6)
     longitude                              DecimalField                    decimal(9,6)
     prefixes                               GenericRelation
     vlan_groups                            GenericRelation
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## dcim.SiteGroup   (dcim/models/sites.py)
   bases: ContactsMixin, NestedGroupModel
     prefixes                               GenericRelation
     vlan_groups                            GenericRelation
     clusters                               GenericRelation
     wireless_lans                          GenericRelation
     contacts (ContactsMixin)               GenericRelation
     parent (NestedGroupModel)              TreeForeignKey                  -> dcim.SiteGroup on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'),
      name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',),
      name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level site group with this name already exists.')), models.UniqueConstraint(fields=('parent',
      'slug'), name='%(app_label)s_%(class)s_parent_slug'), models.UniqueConstraint(fields=('slug',),
      name='%(app_label)s_%(class)s_slug', condition=Q(parent__isnull=True), violation_error_message=_('A
      top-level site group with this slug already exists.')))
   meta.indexes: ()

## dcim.VirtualChassis   (dcim/models/devices.py)
   bases: PrimaryModel
     master                                 OneToOneField                   -> dcim.Device on_delete=PROTECT
     name                                   CharField                   REQ len=64
     domain                                 CharField                       len=30
     member_count                           CounterCacheField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## dcim.VirtualDeviceContext   (dcim/models/devices.py)
   bases: PrimaryModel
   shadows inherited: comments (PrimaryModel)
     device                                 ForeignKey                      -> dcim.Device on_delete=PROTECT
     name                                   CharField                   REQ len=64
     status                                 CharField                   REQ len=50 choices=VirtualDeviceContextStatusChoices
     identifier                             PositiveSmallIntegerField
     primary_ip4                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     primary_ip6                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     comments                               TextField
     description (PrimaryModel)             CharField                       len=200
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('device', 'identifier'),
      name='%(app_label)s_%(class)s_device_identifier'), models.UniqueConstraint(fields=('device', 'name'),
      name='%(app_label)s_%(class)s_device_name'))
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## extras.Bookmark   (extras/models/models.py)
   bases: models.Model
     created                                DateTimeField               REQ
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     object_id                              PositiveBigIntegerField     REQ
     object                                 GenericForeignKey           REQ
     user                                   ForeignKey                  REQ -> settings.AUTH_USER_MODEL on_delete=CASCADE
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'),
      name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('created', 'pk')
   meta.indexes: (models.Index(fields=('created', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.CachedValue   (extras/models/search.py)
   bases: models.Model
     id                                     UUIDField                       def=UNRESOLVED:uuid.uuid4
     timestamp                              DateTimeField               REQ
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     object_id                              PositiveBigIntegerField     REQ
     object                                 RestrictedGenericForeignKey REQ
     field                                  CharField                   REQ len=200
     type                                   CharField                   REQ len=30
     value                                  CachedValueField            REQ
     weight                                 PositiveSmallIntegerField       def=1000
   meta.ordering: ('weight', 'object_type', 'value', 'object_id')
   meta.indexes: (models.Index(fields=('object_type', 'object_id'), name='extras_cachedvalue_object'),)

## extras.ConfigContext   (extras/models/configs.py)
   bases: SyncedDataMixin, CloningMixin, CustomLinksMixin, OwnerMixin, ChangeLoggedModel
     name                                   CharField                   REQ UNIQUE len=100
     profile                                ForeignKey                      -> extras.ConfigContextProfile on_delete=PROTECT
     weight                                 PositiveSmallIntegerField       def=1000
     description                            CharField                       len=200
     is_active                              BooleanField                    def=True
     regions                                ManyToManyField                 -> dcim.Region
     site_groups                            ManyToManyField                 -> dcim.SiteGroup
     sites                                  ManyToManyField                 -> dcim.Site
     locations                              ManyToManyField                 -> dcim.Location
     device_types                           ManyToManyField                 -> dcim.DeviceType
     roles                                  ManyToManyField                 -> dcim.DeviceRole
     platforms                              ManyToManyField                 -> dcim.Platform
     cluster_types                          ManyToManyField                 -> virtualization.ClusterType
     cluster_groups                         ManyToManyField                 -> virtualization.ClusterGroup
     clusters                               ManyToManyField                 -> virtualization.Cluster
     tenant_groups                          ManyToManyField                 -> tenancy.TenantGroup
     tenants                                ManyToManyField                 -> tenancy.Tenant
     tags                                   ManyToManyField                 -> extras.Tag
     data                                   JSONField                   REQ
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ['weight', 'name']
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.ConfigContextModel   (extras/models/configs.py)
   bases: models.Model
     local_context_data                     JSONField

## extras.ConfigContextProfile   (extras/models/configs.py)
   bases: SyncedDataMixin, PrimaryModel
   shadows inherited: description (PrimaryModel)
     name                                   CharField                   REQ UNIQUE len=100
     description                            CharField                       len=200
     schema                                 JSONField
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## extras.ConfigTemplate   (extras/models/configs.py)
   bases: RenderTemplateMixin, SyncedDataMixin, CustomLinksMixin, ExportTemplatesMixin, OwnerMixin, TagsMixin, ChangeLoggedModel
     name                                   CharField                   REQ len=100
     description                            CharField                       len=200
     debug                                  BooleanField                    def=False
     template_code (RenderTemplateMixin)    TextField                   REQ
     environment_params (RenderTemplateMixin) JSONField                       def=UNRESOLVED:dict
     mime_type (RenderTemplateMixin)        CharField                       len=50
     file_name (RenderTemplateMixin)        CharField                       len=200
     file_extension (RenderTemplateMixin)   CharField                       len=15
     as_attachment (RenderTemplateMixin)    BooleanField                    def=True
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## extras.CustomField   (extras/models/customfields.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     type                                   CharField                       len=50 def=UNRESOLVED:CustomFieldTypeChoices.TYPE_TEXT choices=CustomFieldTypeChoices
     related_object_type                    ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     name                                   CharField                   REQ UNIQUE len=50
     label                                  CharField                       len=50
     group_name                             CharField                       len=50
     description                            CharField                       len=200
     required                               BooleanField                    def=False
     unique                                 BooleanField                    def=False
     search_weight                          PositiveSmallIntegerField       def=1000
     filter_logic                           CharField                       len=50 def=UNRESOLVED:CustomFieldFilterLogicChoices.FILTER_LOOSE choices=CustomFieldFilterLogicChoices
     default                                JSONField
     related_object_filter                  JSONField
     weight                                 PositiveSmallIntegerField       def=100
     validation_minimum                     DecimalField                    decimal(16,4)
     validation_maximum                     DecimalField                    decimal(16,4)
     validation_regex                       CharField                       len=500
     validation_schema                      JSONField
     choice_set                             ForeignKey                      -> extras.CustomFieldChoiceSet on_delete=PROTECT
     ui_visible                             CharField                       len=50 def=UNRESOLVED:CustomFieldUIVisibleChoices.ALWAYS choices=CustomFieldUIVisibleChoices
     ui_editable                            CharField                       len=50 def=UNRESOLVED:CustomFieldUIEditableChoices.YES choices=CustomFieldUIEditableChoices
     is_cloneable                           BooleanField                    def=False
     comments                               TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ['group_name', 'weight', 'name']
   meta.indexes: (models.Index(fields=('group_name', 'weight', 'name')),)

## extras.CustomFieldChoiceSet   (extras/models/customfields.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     name                                   CharField                   REQ UNIQUE len=100
     description                            CharField                       len=200
     base_choices                           CharField                       len=50 choices=CustomFieldChoiceSetBaseChoices
     extra_choices                          ChoiceSetField
     choice_colors                          JSONField                       def=UNRESOLVED:dict
     order_alphabetically                   BooleanField                    def=False
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name',)

## extras.CustomLink   (extras/models/models.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     name                                   CharField                   REQ UNIQUE len=100
     enabled                                BooleanField                    def=True
     link_text                              TextField                   REQ
     link_url                               TextField                   REQ
     weight                                 PositiveSmallIntegerField       def=100
     group_name                             CharField                       len=50
     button_class                           CharField                       len=30 def=UNRESOLVED:CustomLinkButtonClassChoices.DEFAULT choices=CustomLinkButtonClassChoices
     new_window                             BooleanField                    def=False
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ['group_name', 'weight', 'name']
   meta.indexes: (models.Index(fields=('group_name', 'weight', 'name')),)

## extras.Dashboard   (extras/models/dashboard.py)
   bases: models.Model
     user                                   OneToOneField               REQ -> users.User on_delete=CASCADE
     layout                                 JSONField                       def=UNRESOLVED:list
     config                                 JSONField                       def=UNRESOLVED:dict

## extras.EventRule   (extras/models/models.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, OwnerMixin, TagsMixin, ChangeLoggedModel
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     name                                   CharField                   REQ UNIQUE len=150
     description                            CharField                       len=200
     event_types                            ArrayField                  REQ
     enabled                                BooleanField                    def=True
     conditions                             JSONField
     action_type                            CharField                       len=30 def=UNRESOLVED:EventRuleActionChoices.WEBHOOK choices=EventRuleActionChoices
     action_object_type                     ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     action_object_id                       PositiveBigIntegerField
     action_object                          GenericForeignKey           REQ
     action_data                            JSONField
     comments                               TextField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('action_object_type', 'action_object_id')),)

## extras.ExportTemplate   (extras/models/models.py)
   bases: SyncedDataMixin, CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, RenderTemplateMixin
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     name                                   CharField                   REQ len=100
     description                            CharField                       len=200
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     template_code (RenderTemplateMixin)    TextField                   REQ
     environment_params (RenderTemplateMixin) JSONField                       def=UNRESOLVED:dict
     mime_type (RenderTemplateMixin)        CharField                       len=50
     file_name (RenderTemplateMixin)        CharField                       len=200
     file_extension (RenderTemplateMixin)   CharField                       len=15
     as_attachment (RenderTemplateMixin)    BooleanField                    def=True
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## extras.ImageAttachment   (extras/models/models.py)
   bases: ChangeLoggedModel
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     object_id                              PositiveBigIntegerField     REQ
     parent                                 GenericForeignKey           REQ
     image                                  ImageField                  REQ
     image_height                           PositiveSmallIntegerField   REQ
     image_width                            PositiveSmallIntegerField   REQ
     image_size                             PositiveBigIntegerField
     name                                   CharField                       len=50
     description                            CharField                       len=200
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.JournalEntry   (extras/models/models.py)
   bases: CustomFieldsMixin, CustomLinksMixin, TagsMixin, ExportTemplatesMixin, ChangeLoggedModel
     assigned_object_type                   ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     assigned_object_id                     PositiveBigIntegerField     REQ
     assigned_object                        GenericForeignKey           REQ
     created_by                             ForeignKey                      -> settings.AUTH_USER_MODEL on_delete=SET_NULL
     kind                                   CharField                       len=30 def=UNRESOLVED:JournalEntryKindChoices.KIND_INFO choices=JournalEntryKindChoices
     comments                               TextField                   REQ
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('-created',)
   meta.indexes: (models.Index(fields=('-created',)), models.Index(fields=('assigned_object_type',
      'assigned_object_id')))

## extras.Notification   (extras/models/notifications.py)
   bases: models.Model
     created                                DateTimeField               REQ
     read                                   DateTimeField
     user                                   ForeignKey                  REQ -> settings.AUTH_USER_MODEL on_delete=CASCADE
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     object_id                              PositiveBigIntegerField     REQ
     object                                 GenericForeignKey           REQ
     object_repr                            CharField                   REQ len=200
     event_type                             CharField                   REQ len=50 choices=get_event_type_choices
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'),
      name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('-created', 'pk')
   meta.indexes: (models.Index(fields=('-created', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.NotificationGroup   (extras/models/notifications.py)
   bases: ChangeLoggedModel
     name                                   CharField                   REQ UNIQUE len=100
     description                            CharField                       len=200
     groups                                 ManyToManyField                 -> users.Group
     users                                  ManyToManyField                 -> users.User
     event_rules                            GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name',)

## extras.RenderTemplateMixin   (extras/models/mixins.py)
   bases: models.Model
     template_code                          TextField                   REQ
     environment_params                     JSONField                       def=UNRESOLVED:dict
     mime_type                              CharField                       len=50
     file_name                              CharField                       len=200
     file_extension                         CharField                       len=15
     as_attachment                          BooleanField                    def=True

## extras.SavedFilter   (extras/models/models.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     description                            CharField                       len=200
     user                                   ForeignKey                      -> settings.AUTH_USER_MODEL on_delete=SET_NULL
     weight                                 PositiveSmallIntegerField       def=100
     enabled                                BooleanField                    def=True
     shared                                 BooleanField                    def=True
     parameters                             JSONField                   REQ
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.Script   (extras/models/scripts.py)
   bases: EventRulesMixin, JobsMixin
     name                                   CharField                   REQ len=79
     module                                 ForeignKey                  REQ -> extras.ScriptModule on_delete=CASCADE
     is_executable                          BooleanField                    def=True
     events                                 GenericRelation
     jobs (JobsMixin)                       GenericRelation
   meta.constraints: (models.UniqueConstraint(fields=('name', 'module'),
      name='extras_script_unique_name_module'),)
   meta.ordering: ('module', 'name')
   meta.indexes: (models.Index(fields=('module', 'name')),)

## extras.ScriptModule   (extras/models/scripts.py)
   bases: PythonModuleMixin, JobsMixin, ManagedFile
     event_rules                            GenericRelation
     jobs (JobsMixin)                       GenericRelation
     created (ManagedFile)                  DateTimeField               REQ
     last_updated (ManagedFile)             DateTimeField
     file_root (ManagedFile)                CharField                   REQ len=1000 choices=ManagedFileRootPathChoices
     file_path (ManagedFile)                FilePathField               REQ
     data_source (SyncedDataMixin)          ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file (SyncedDataMixin)            ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path (SyncedDataMixin)            CharField                       len=1000
     auto_sync_enabled (SyncedDataMixin)    BooleanField                    def=False
     data_synced (SyncedDataMixin)          DateTimeField
   meta.ordering: ('file_root', 'file_path')

## extras.Subscription   (extras/models/notifications.py)
   bases: models.Model
     created                                DateTimeField               REQ
     user                                   ForeignKey                  REQ -> settings.AUTH_USER_MODEL on_delete=CASCADE
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     object_id                              PositiveBigIntegerField     REQ
     object                                 GenericForeignKey           REQ
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'),
      name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('-created', 'user')
   meta.indexes: (models.Index(fields=('-created', 'user')), models.Index(fields=('object_type',
      'object_id')))

## extras.TableConfig   (extras/models/models.py)
   bases: CloningMixin, ChangeLoggedModel
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     table                                  CharField                   REQ len=100
     name                                   CharField                   REQ len=100
     description                            CharField                       len=200
     user                                   ForeignKey                      -> settings.AUTH_USER_MODEL on_delete=SET_NULL
     weight                                 PositiveSmallIntegerField       def=1000
     enabled                                BooleanField                    def=True
     shared                                 BooleanField                    def=True
     columns                                ArrayField                  REQ
     ordering                               ArrayField
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.Tag   (extras/models/tags.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, TagBase
     id                                     BigAutoField                REQ
     color                                  ColorField                      def=UNRESOLVED:ColorChoices.COLOR_GREY
     description                            CharField                       len=200
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     weight                                 PositiveSmallIntegerField       def=1000
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.TaggedItem   (extras/models/tags.py)
   bases: GenericTaggedItemBase
     tag                                    ForeignKey                  REQ -> extras.Tag on_delete=CASCADE
   meta.indexes: [models.Index(fields=['content_type', 'object_id'])]

## extras.Webhook   (extras/models/models.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, OwnerMixin, ChangeLoggedModel
     name                                   CharField                   REQ UNIQUE len=150
     description                            CharField                       len=200
     payload_url                            CharField                   REQ len=500
     http_method                            CharField                       len=30 def=UNRESOLVED:WebhookHttpMethodChoices.METHOD_POST choices=WebhookHttpMethodChoices
     http_content_type                      CharField                       len=100 def=UNRESOLVED:HTTP_CONTENT_TYPE_JSON
     additional_headers                     TextField
     body_template                          TextField
     secret                                 CharField                       len=255
     ssl_verification                       BooleanField                    def=True
     ca_file_path                           CharField                       len=4096
     events                                 GenericRelation
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.ordering: ('name',)

## ipam.ASN   (ipam/models/asns.py)
   bases: ContactsMixin, PrimaryModel
     rir                                    ForeignKey                  REQ -> ipam.RIR on_delete=PROTECT
     asn                                    ASNField                    REQ UNIQUE
     role                                   ForeignKey                      -> ipam.Role on_delete=SET_NULL
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['asn']

## ipam.ASNRange   (ipam/models/asns.py)
   bases: OrganizationalModel
   shadows inherited: name (OrganizationalModel), slug (OrganizationalModel)
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     rir                                    ForeignKey                  REQ -> ipam.RIR on_delete=PROTECT
     start                                  ASNField                    REQ
     end                                    ASNField                    REQ
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## ipam.Aggregate   (ipam/models/ip.py)
   bases: ContactsMixin, GetAvailablePrefixesMixin, PrimaryModel
     prefix                                 IPNetworkField              REQ
     rir                                    ForeignKey                  REQ -> ipam.RIR on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     date_added                             DateField
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('prefix', 'pk')
   meta.indexes: (models.Index(fields=('prefix', 'id')),)

## ipam.FHRPGroup   (ipam/models/fhrp.py)
   bases: PrimaryModel
     group_id                               PositiveSmallIntegerField   REQ
     name                                   CharField                       len=100
     protocol                               CharField                   REQ len=50 choices=FHRPGroupProtocolChoices
     auth_type                              CharField                       len=50 choices=FHRPGroupAuthTypeChoices
     auth_key                               CharField                       len=255
     ip_addresses                           GenericRelation
     services                               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['protocol', 'group_id', 'pk']
   meta.indexes: (models.Index(fields=('protocol', 'group_id', 'id')),)

## ipam.FHRPGroupAssignment   (ipam/models/fhrp.py)
   bases: ChangeLoggedModel
     interface_type                         ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     interface_id                           PositiveBigIntegerField     REQ
     interface                              GenericForeignKey           REQ
     group                                  ForeignKey                  REQ -> ipam.FHRPGroup on_delete=CASCADE
     priority                               PositiveSmallIntegerField   REQ
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('interface_type', 'interface_id', 'group'),
      name='%(app_label)s_%(class)s_unique_interface_group'),)
   meta.ordering: ('-priority', 'pk')
   meta.indexes: (models.Index(fields=('-priority', 'id')), models.Index(fields=('interface_type',
      'interface_id')))

## ipam.IPAddress   (ipam/models/ip.py)
   bases: ContactsMixin, PrimaryModel
     address                                IPAddressField              REQ
     vrf                                    ForeignKey                      -> ipam.VRF on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:IPAddressStatusChoices.STATUS_ACTIVE choices=IPAddressStatusChoices
     role                                   CharField                       len=50 choices=IPAddressRoleChoices
     assigned_object_type                   ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     assigned_object_id                     PositiveBigIntegerField
     assigned_object                        GenericForeignKey
     nat_inside                             ForeignKey                      -> ipam.IPAddress on_delete=SET_NULL
     dns_name                               CharField                       len=255
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('address', 'pk')
   meta.indexes: (models.Index(fields=('address', 'id')), models.Index(Cast(Host('address'),
      output_field=IPAddressField()), F('id'), name='ipam_ipaddress_host'),
      models.Index(fields=('assigned_object_type', 'assigned_object_id')))

## ipam.IPRange   (ipam/models/ip.py)
   bases: ContactsMixin, PrimaryModel
     start_address                          IPAddressField              REQ
     end_address                            IPAddressField              REQ
     size                                   PositiveIntegerField        REQ
     vrf                                    ForeignKey                      -> ipam.VRF on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:IPRangeStatusChoices.STATUS_ACTIVE choices=IPRangeStatusChoices
     role                                   ForeignKey                      -> ipam.Role on_delete=SET_NULL
     mark_populated                         BooleanField                    def=False
     mark_utilized                          BooleanField                    def=False
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: (F('vrf').asc(nulls_first=True), 'start_address', 'pk')
   meta.indexes: (models.Index(Cast(Host('start_address'), output_field=IPAddressField()),
      name='ipam_iprange_start_host'), models.Index(Cast(Host('end_address'), output_field=IPAddressField()),
      name='ipam_iprange_end_host'))

## ipam.Prefix   (ipam/models/ip.py)
   bases: ContactsMixin, GetAvailablePrefixesMixin, CachedScopeMixin, PrimaryModel
     prefix                                 IPNetworkField              REQ
     vrf                                    ForeignKey                      -> ipam.VRF on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     vlan                                   ForeignKey                      -> ipam.VLAN on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:PrefixStatusChoices.STATUS_ACTIVE choices=PrefixStatusChoices
     role                                   ForeignKey                      -> ipam.Role on_delete=SET_NULL
     is_pool                                BooleanField                    def=False
     mark_utilized                          BooleanField                    def=False
     _depth                                 PositiveSmallIntegerField       def=0
     _children                              PositiveBigIntegerField         def=0
     contacts (ContactsMixin)               GenericRelation
     scope_type (CachedScopeMixin)          ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     scope_id (CachedScopeMixin)            PositiveBigIntegerField
     scope (CachedScopeMixin)               GenericForeignKey
     _location (CachedScopeMixin)           ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site (CachedScopeMixin)               ForeignKey                      -> dcim.Site on_delete=CASCADE
     _region (CachedScopeMixin)             ForeignKey                      -> dcim.Region on_delete=SET_NULL
     _site_group (CachedScopeMixin)         ForeignKey                      -> dcim.SiteGroup on_delete=SET_NULL
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: (F('vrf').asc(nulls_first=True), 'prefix', 'pk')
   meta.indexes: (models.Index(fields=('scope_type', 'scope_id')), GistIndex(fields=['prefix'],
      name='ipam_prefix_gist_idx', opclasses=['inet_ops']))

## ipam.RIR   (ipam/models/ip.py)
   bases: OrganizationalModel
     is_private                             BooleanField                    def=False
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## ipam.Role   (ipam/models/ip.py)
   bases: OrganizationalModel
     weight                                 PositiveSmallIntegerField       def=1000
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## ipam.RouteTarget   (ipam/models/vrfs.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=21
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['name']

## ipam.Service   (ipam/models/services.py)
   bases: ContactsMixin, ServiceBase, PrimaryModel
     parent_object_type                     ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     parent_object_id                       PositiveBigIntegerField     REQ
     parent                                 GenericForeignKey           REQ
     name                                   CharField                   REQ len=100
     ipaddresses                            ManyToManyField                 -> ipam.IPAddress
     contacts (ContactsMixin)               GenericRelation
     protocol (ServiceBase)                 CharField                   REQ len=50 choices=ServiceProtocolChoices
     ports (ServiceBase)                    ArrayField                  REQ
     _ports_lowest (ServiceBase)            PositiveIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('protocol', '_ports_lowest', 'id')
   meta.indexes: (models.Index(fields=('protocol', '_ports_lowest', 'id')),
      models.Index(fields=('parent_object_type', 'parent_object_id')))

## ipam.ServiceBase   (ipam/models/services.py)
   bases: models.Model
     protocol                               CharField                   REQ len=50 choices=ServiceProtocolChoices
     ports                                  ArrayField                  REQ
     _ports_lowest                          PositiveIntegerField

## ipam.ServiceTemplate   (ipam/models/services.py)
   bases: ServiceBase, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     protocol (ServiceBase)                 CharField                   REQ len=50 choices=ServiceProtocolChoices
     ports (ServiceBase)                    ArrayField                  REQ
     _ports_lowest (ServiceBase)            PositiveIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## ipam.VLAN   (ipam/models/vlans.py)
   bases: PrimaryModel
     site                                   ForeignKey                      -> dcim.Site on_delete=PROTECT
     group                                  ForeignKey                      -> ipam.VLANGroup on_delete=PROTECT
     vid                                    PositiveSmallIntegerField   REQ
     name                                   CharField                   REQ len=64
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:VLANStatusChoices.STATUS_ACTIVE choices=VLANStatusChoices
     role                                   ForeignKey                      -> ipam.Role on_delete=SET_NULL
     qinq_svlan                             ForeignKey                      -> ipam.VLAN on_delete=PROTECT
     qinq_role                              CharField                       len=50 choices=VLANQinQRoleChoices
     l2vpn_terminations                     GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('group', 'vid'),
      name='%(app_label)s_%(class)s_unique_group_vid'), models.UniqueConstraint(fields=('group', 'name'),
      name='%(app_label)s_%(class)s_unique_group_name'), models.UniqueConstraint(fields=('qinq_svlan', 'vid'),
      name='%(app_label)s_%(class)s_unique_qinq_svlan_vid'), models.UniqueConstraint(fields=('qinq_svlan',
      'name'), name='%(app_label)s_%(class)s_unique_qinq_svlan_name'))
   meta.ordering: ('site', 'group', 'vid', 'pk')
   meta.indexes: (models.Index(fields=('site', 'group', 'vid', 'id')),)

## ipam.VLANGroup   (ipam/models/vlans.py)
   bases: OrganizationalModel
   shadows inherited: name (OrganizationalModel), slug (OrganizationalModel)
     name                                   CharField                   REQ len=100
     slug                                   SlugField                   REQ len=100
     scope_type                             ForeignKey                      -> contenttypes.ContentType on_delete=CASCADE
     scope_id                               PositiveBigIntegerField
     scope                                  GenericForeignKey
     vid_ranges                             ArrayField                      def=UNRESOLVED:default_vid_ranges
     total_vlan_ids                         PositiveBigIntegerField         def=UNRESOLVED:VLAN_VID_MAX - VLAN_VID_MIN + 1
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('scope_type', 'scope_id', 'name'),
      name='%(app_label)s_%(class)s_unique_scope_name'), models.UniqueConstraint(fields=('scope_type',
      'scope_id', 'slug'), name='%(app_label)s_%(class)s_unique_scope_slug'))
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')), models.Index(fields=('scope_type', 'scope_id')))

## ipam.VLANTranslationPolicy   (ipam/models/vlans.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## ipam.VLANTranslationRule   (ipam/models/vlans.py)
   bases: NetBoxModel
     policy                                 ForeignKey                  REQ -> ipam.VLANTranslationPolicy on_delete=CASCADE
     description                            CharField                       len=200
     local_vid                              PositiveSmallIntegerField   REQ
     remote_vid                             PositiveSmallIntegerField   REQ
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('policy', 'local_vid'),
      name='%(app_label)s_%(class)s_unique_policy_local_vid'), models.UniqueConstraint(fields=('policy',
      'remote_vid'), name='%(app_label)s_%(class)s_unique_policy_remote_vid'))
   meta.ordering: ('policy', 'local_vid')

## ipam.VRF   (ipam/models/vrfs.py)
   bases: PrimaryModel
     name                                   CharField                   REQ len=100
     rd                                     CharField                       UNIQUE len=21
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     enforce_unique                         BooleanField                    def=True
     import_targets                         ManyToManyField                 -> ipam.RouteTarget
     export_targets                         ManyToManyField                 -> ipam.RouteTarget
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name', 'rd', 'pk')
   meta.indexes: (models.Index(fields=('name', 'rd', 'id')),)

## netbox.AdminModel   (netbox/models/__init__.py)
   bases: BookmarksMixin, CloningMixin, CustomLinksMixin, CustomValidationMixin, EventRulesMixin, ExportTemplatesMixin, NotificationsMixin, BaseModel
     description                            CharField                       len=200
     bookmarks (BookmarksMixin)             GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation

## netbox.BookmarksMixin   (netbox/models/features.py)
   bases: models.Model
     bookmarks                              GenericRelation

## netbox.ChangeLoggedModel   (netbox/models/__init__.py)
   bases: ChangeLoggingMixin, CustomValidationMixin, EventRulesMixin, BaseModel
   (no own columns — every field is inherited from ChangeLoggingMixin, CustomValidationMixin, EventRulesMixin, BaseModel)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField

## netbox.ChangeLoggingMixin   (netbox/models/features.py)
   bases: DeleteMixin, models.Model
     created                                DateTimeField
     last_updated                           DateTimeField

## netbox.ContactsMixin   (netbox/models/features.py)
   bases: models.Model
     contacts                               GenericRelation

## netbox.CustomFieldsMixin   (netbox/models/features.py)
   bases: models.Model
     custom_field_data                      JSONField                       def=UNRESOLVED:dict

## netbox.DistanceMixin   (netbox/models/mixins.py)
   bases: models.Model
     distance                               DecimalField                    decimal(8,2)
     distance_unit                          CharField                       len=50 choices=DistanceUnitChoices
     _abs_distance                          DecimalField                    decimal(13,4)

## netbox.ImageAttachmentsMixin   (netbox/models/features.py)
   bases: models.Model
     images                                 GenericRelation

## netbox.JobsMixin   (netbox/models/features.py)
   bases: models.Model
     jobs                                   GenericRelation

## netbox.JournalingMixin   (netbox/models/features.py)
   bases: models.Model
     journal_entries                        GenericRelation

## netbox.NestedGroupModel   (netbox/models/__init__.py)
   bases: OwnerMixin, NetBoxModel, MPTTModel
     parent                                 TreeForeignKey                  -> netbox.NestedGroupModel on_delete=CASCADE
     name                                   CharField                   REQ len=100
     slug                                   SlugField                   REQ len=100
     description                            CharField                       len=200
     comments                               TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## netbox.NetBoxFeatureSet   (netbox/models/__init__.py)
   bases: BookmarksMixin, ChangeLoggingMixin, CloningMixin, CustomFieldsMixin, CustomLinksMixin, CustomValidationMixin, ExportTemplatesMixin, JournalingMixin, NotificationsMixin, TagsMixin, EventRulesMixin
   (no own columns — every field is inherited from BookmarksMixin, ChangeLoggingMixin, CloningMixin, CustomFieldsMixin, CustomLinksMixin, CustomValidationMixin, ExportTemplatesMixin, JournalingMixin, NotificationsMixin, TagsMixin, EventRulesMixin)
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## netbox.NetBoxModel   (netbox/models/__init__.py)
   bases: NetBoxFeatureSet, BaseModel
   (no own columns — every field is inherited from NetBoxFeatureSet, BaseModel)
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## netbox.NotificationsMixin   (netbox/models/features.py)
   bases: models.Model
     subscriptions                          GenericRelation

## netbox.OrganizationalModel   (netbox/models/__init__.py)
   bases: OwnerMixin, NetBoxModel
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     description                            CharField                       len=200
     comments                               TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## netbox.OwnerMixin   (netbox/models/mixins.py)
   bases: models.Model
     owner                                  ForeignKey                      -> users.Owner on_delete=PROTECT

## netbox.PrimaryModel   (netbox/models/__init__.py)
   bases: OwnerMixin, NetBoxModel
     description                            CharField                       len=200
     comments                               TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## netbox.SyncedDataMixin   (netbox/models/features.py)
   bases: models.Model
     data_source                            ForeignKey                      -> core.DataSource on_delete=PROTECT
     data_file                              ForeignKey                      -> core.DataFile on_delete=SET_NULL
     data_path                              CharField                       len=1000
     auto_sync_enabled                      BooleanField                    def=False
     data_synced                            DateTimeField

## netbox.TagsMixin   (netbox/models/features.py)
   bases: models.Model
     tags                                   NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)

## netbox.WeightMixin   (netbox/models/mixins.py)
   bases: models.Model
     weight                                 DecimalField                    decimal(8,2)
     weight_unit                            CharField                       len=50 choices=WeightUnitChoices
     _abs_weight                            PositiveBigIntegerField

## tenancy.Contact   (tenancy/models/contacts.py)
   bases: PrimaryModel
     groups                                 ManyToManyField                 -> tenancy.ContactGroup
     name                                   CharField                   REQ len=100
     title                                  CharField                       len=100
     phone                                  CharField                       len=50
     email                                  EmailField
     address                                CharField                       len=200
     link                                   URLField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## tenancy.ContactAssignment   (tenancy/models/contacts.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     object_type                            ForeignKey                  REQ -> contenttypes.ContentType on_delete=CASCADE
     object_id                              PositiveBigIntegerField     REQ
     object                                 GenericForeignKey           REQ
     contact                                ForeignKey                  REQ -> tenancy.Contact on_delete=PROTECT
     role                                   ForeignKey                  REQ -> tenancy.ContactRole on_delete=PROTECT
     priority                               CharField                       len=50 choices=ContactPriorityChoices
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'contact', 'role'),
      name='%(app_label)s_%(class)s_unique_object_contact_role'),)
   meta.ordering: ('contact', 'priority', 'role', 'pk')
   meta.indexes: (models.Index(fields=('contact', 'priority', 'role', 'id')),
      models.Index(fields=('object_type', 'object_id')))

## tenancy.ContactGroup   (tenancy/models/contacts.py)
   bases: NestedGroupModel
   (no own columns — every field is inherited from NestedGroupModel)
     parent (NestedGroupModel)              TreeForeignKey                  -> tenancy.ContactGroup on_delete=CASCADE
     name (NestedGroupModel)                CharField                   REQ len=100
     slug (NestedGroupModel)                SlugField                   REQ len=100
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'),
      name='%(app_label)s_%(class)s_unique_parent_name'),)
   meta.ordering: ['name']
   meta.indexes: ()

## tenancy.ContactRole   (tenancy/models/contacts.py)
   bases: OrganizationalModel
   (no own columns — every field is inherited from OrganizationalModel)
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## tenancy.Tenant   (tenancy/models/tenants.py)
   bases: ContactsMixin, PrimaryModel
     name                                   CharField                   REQ len=100
     slug                                   SlugField                   REQ len=100
     group                                  ForeignKey                      -> tenancy.TenantGroup on_delete=SET_NULL
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'),
      name='%(app_label)s_%(class)s_unique_group_name', violation_error_message=_('Tenant name must be unique
      per group.')), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_unique_name',
      condition=Q(group__isnull=True)), models.UniqueConstraint(fields=('group', 'slug'),
      name='%(app_label)s_%(class)s_unique_group_slug', violation_error_message=_('Tenant slug must be unique
      per group.')), models.UniqueConstraint(fields=('slug',), name='%(app_label)s_%(class)s_unique_slug',
      condition=Q(group__isnull=True)))
   meta.ordering: ['name']

## tenancy.TenantGroup   (tenancy/models/tenants.py)
   bases: NestedGroupModel
   shadows inherited: name (NestedGroupModel), slug (NestedGroupModel)
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     parent (NestedGroupModel)              TreeForeignKey                  -> tenancy.TenantGroup on_delete=CASCADE
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ['name']
   meta.indexes: ()

## users.Group   (users/models/users.py)
   bases: models.Model
     name                                   CharField                   REQ UNIQUE len=150
     description                            CharField                       len=200
     object_permissions                     ManyToManyField                 -> users.ObjectPermission
     permissions                            ManyToManyField                 -> UNRESOLVED:Permission
   meta.ordering: ('name',)

## users.ObjectPermission   (users/models/permissions.py)
   bases: CloningMixin, models.Model
     name                                   CharField                   REQ len=100
     description                            CharField                       len=200
     enabled                                BooleanField                    def=True
     object_types                           ManyToManyField                 -> contenttypes.ContentType
     actions                                ArrayField                  REQ
     constraints                            JSONField
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## users.Owner   (users/models/owners.py)
   bases: AdminModel
     name                                   CharField                   REQ UNIQUE len=100
     group                                  ForeignKey                      -> users.OwnerGroup on_delete=PROTECT
     user_groups                            ManyToManyField                 -> users.Group
     users                                  ManyToManyField                 -> users.User
     description (AdminModel)               CharField                       len=200
     bookmarks (BookmarksMixin)             GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
   meta.ordering: ('name',)

## users.OwnerGroup   (users/models/owners.py)
   bases: AdminModel
     name                                   CharField                   REQ UNIQUE len=100
     description (AdminModel)               CharField                       len=200
     bookmarks (BookmarksMixin)             GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
   meta.ordering: ['name']

## users.Token   (users/models/tokens.py)
   bases: models.Model
     version                                PositiveSmallIntegerField       def=UNRESOLVED:TokenVersionChoices.V2 choices=TokenVersionChoices
     user                                   ForeignKey                  REQ -> users.User on_delete=CASCADE
     description                            CharField                       len=200
     created                                DateTimeField               REQ
     expires                                DateTimeField
     last_used                              DateTimeField
     enabled                                BooleanField                    def=True
     write_enabled                          BooleanField                    def=True
     plaintext                              CharField                       UNIQUE len=40
     key                                    CharField                       UNIQUE len=12
     pepper_id                              PositiveSmallIntegerField
     hmac_digest                            CharField                       len=64
     allowed_ips                            ArrayField
   meta.constraints: [models.CheckConstraint(name='enforce_version_dependent_fields', condition=Q(version=1,
      key__isnull=True, pepper_id__isnull=True, hmac_digest__isnull=True, plaintext__isnull=False) |
      Q(version=2, key__isnull=False, pepper_id__isnull=False, hmac_digest__isnull=False,
      plaintext__isnull=True))]
   meta.ordering: ('-created',)
   meta.indexes: (models.Index(fields=('-created',)),)

## users.User   (users/models/users.py)
   bases: AbstractBaseUser, PermissionsMixin
     username                               CharField                   REQ UNIQUE len=150
     first_name                             CharField                       len=150
     last_name                              CharField                       len=150
     email                                  EmailField
     is_active                              BooleanField                    def=True
     date_joined                            DateTimeField                   def=UNRESOLVED:timezone.now
     groups                                 ManyToManyField                 -> users.Group
     object_permissions                     ManyToManyField                 -> users.ObjectPermission
   meta.ordering: ('username',)

## users.UserConfig   (users/models/preferences.py)
   bases: models.Model
     user                                   OneToOneField               REQ -> users.User on_delete=CASCADE
     data                                   JSONField                       def=UNRESOLVED:dict
   meta.ordering: ['user']

## virtualization.Cluster   (virtualization/models/clusters.py)
   bases: ContactsMixin, CachedScopeMixin, PrimaryModel
     name                                   CharField                   REQ len=100
     type                                   ForeignKey                  REQ -> virtualization.ClusterType on_delete=PROTECT
     group                                  ForeignKey                      -> virtualization.ClusterGroup on_delete=PROTECT
     status                                 CharField                       len=50 def=UNRESOLVED:ClusterStatusChoices.STATUS_ACTIVE choices=ClusterStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     vlan_groups                            GenericRelation
     contacts (ContactsMixin)               GenericRelation
     scope_type (CachedScopeMixin)          ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     scope_id (CachedScopeMixin)            PositiveBigIntegerField
     scope (CachedScopeMixin)               GenericForeignKey
     _location (CachedScopeMixin)           ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site (CachedScopeMixin)               ForeignKey                      -> dcim.Site on_delete=CASCADE
     _region (CachedScopeMixin)             ForeignKey                      -> dcim.Region on_delete=SET_NULL
     _site_group (CachedScopeMixin)         ForeignKey                      -> dcim.SiteGroup on_delete=SET_NULL
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'),
      name='%(app_label)s_%(class)s_unique_group_name'), models.UniqueConstraint(fields=('_site', 'name'),
      name='%(app_label)s_%(class)s_unique__site_name'))
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('scope_type', 'scope_id')),)

## virtualization.ClusterGroup   (virtualization/models/clusters.py)
   bases: ContactsMixin, OrganizationalModel
     vlan_groups                            GenericRelation
     contacts (ContactsMixin)               GenericRelation
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## virtualization.ClusterType   (virtualization/models/clusters.py)
   bases: OrganizationalModel
   (no own columns — every field is inherited from OrganizationalModel)
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## virtualization.ComponentModel   (virtualization/models/virtualmachines.py)
   bases: OwnerMixin, NetBoxModel
     virtual_machine                        ForeignKey                  REQ -> virtualization.VirtualMachine on_delete=CASCADE
     name                                   CharField                   REQ len=64
     description                            CharField                       len=200
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('virtual_machine', 'name'),
      name='%(app_label)s_%(class)s_unique_virtual_machine_name'),)

## virtualization.VMInterface   (virtualization/models/virtualmachines.py)
   bases: ComponentModel, BaseInterface, TrackingModelMixin
   shadows inherited: virtual_machine (ComponentModel), name (ComponentModel)
     name                                   CharField                   REQ len=64
     _name                                  NaturalOrderingField            len=100
     virtual_machine                        ForeignKey                  REQ -> virtualization.VirtualMachine on_delete=CASCADE
     ip_addresses                           GenericRelation
     vrf                                    ForeignKey                      -> ipam.VRF on_delete=SET_NULL
     fhrp_group_assignments                 GenericRelation
     tunnel_terminations                    GenericRelation
     l2vpn_terminations                     GenericRelation
     mac_addresses                          GenericRelation
     description (ComponentModel)           CharField                       len=200
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     enabled (BaseInterface)                BooleanField                    def=True
     mtu (BaseInterface)                    PositiveIntegerField
     mode (BaseInterface)                   CharField                       len=50 choices=InterfaceModeChoices
     parent (BaseInterface)                 ForeignKey                      -> virtualization.VMInterface on_delete=RESTRICT
     bridge (BaseInterface)                 ForeignKey                      -> virtualization.VMInterface on_delete=SET_NULL
     untagged_vlan (BaseInterface)          ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     tagged_vlans (BaseInterface)           ManyToManyField                 -> ipam.VLAN
     qinq_svlan (BaseInterface)             ForeignKey                      -> ipam.VLAN on_delete=SET_NULL
     vlan_translation_policy (BaseInterface) ForeignKey                      -> ipam.VLANTranslationPolicy on_delete=PROTECT
     primary_mac_address (BaseInterface)    OneToOneField                   -> dcim.MACAddress on_delete=SET_NULL
   meta.ordering: ('virtual_machine', CollateAsChar('_name'))

## virtualization.VirtualDisk   (virtualization/models/virtualmachines.py)
   bases: ComponentModel, TrackingModelMixin
     size                                   PositiveIntegerField        REQ
     virtual_machine (ComponentModel)       ForeignKey                  REQ -> virtualization.VirtualMachine on_delete=CASCADE
     name (ComponentModel)                  CharField                   REQ len=64
     description (ComponentModel)           CharField                       len=200
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('virtual_machine', 'name')

## virtualization.VirtualMachine   (virtualization/models/virtualmachines.py)
   bases: ContactsMixin, ImageAttachmentsMixin, RenderConfigMixin, ConfigContextModel, TrackingModelMixin, PrimaryModel
     virtual_machine_type                   ForeignKey                      -> virtualization.VirtualMachineType on_delete=PROTECT
     site                                   ForeignKey                      -> dcim.Site on_delete=PROTECT
     cluster                                ForeignKey                      -> virtualization.Cluster on_delete=PROTECT
     device                                 ForeignKey                      -> dcim.Device on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     platform                               ForeignKey                      -> dcim.Platform on_delete=SET_NULL
     name                                   CharField                   REQ len=64
     status                                 CharField                       len=50 def=UNRESOLVED:VirtualMachineStatusChoices.STATUS_ACTIVE choices=VirtualMachineStatusChoices
     start_on_boot                          CharField                       len=32 def=UNRESOLVED:VirtualMachineStartOnBootChoices.STATUS_OFF choices=VirtualMachineStartOnBootChoices
     role                                   ForeignKey                      -> dcim.DeviceRole on_delete=PROTECT
     primary_ip4                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     primary_ip6                            OneToOneField                   -> ipam.IPAddress on_delete=SET_NULL
     vcpus                                  DecimalField                    decimal(6,2)
     memory                                 PositiveIntegerField
     disk                                   PositiveIntegerField
     serial                                 CharField                       len=50
     services                               GenericRelation
     interface_count                        CounterCacheField
     virtual_disk_count                     CounterCacheField
     contacts (ContactsMixin)               GenericRelation
     images (ImageAttachmentsMixin)         GenericRelation
     config_template (RenderConfigMixin)    ForeignKey                      -> extras.ConfigTemplate on_delete=PROTECT
     local_context_data (ConfigContextModel) JSONField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(Lower('name'), 'cluster', 'tenant',
      name='%(app_label)s_%(class)s_unique_name_cluster_tenant', violation_error_message=_('Virtual machine
      name must be unique per cluster and tenant.')), models.UniqueConstraint(Lower('name'), 'cluster',
      name='%(app_label)s_%(class)s_unique_name_cluster', condition=Q(tenant__isnull=True),
      violation_error_message=_('Virtual machine name must be unique per cluster.')),
      models.UniqueConstraint(Lower('name'), 'device', 'tenant',
      name='%(app_label)s_%(class)s_unique_name_device_tenant', condition=Q(cluster__isnull=True,
      device__isnull=False), violation_error_message=_('Virtual machine name must be unique per device and
      tenant.')), models.UniqueConstraint(Lower('name'), 'device',
      name='%(app_label)s_%(class)s_unique_name_device', condition=Q(cluster__isnull=True,
      device__isnull=False, tenant__isnull=True), violation_error_message=_('Virtual machine name must be
      unique per device.')))
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')),)

## virtualization.VirtualMachineType   (virtualization/models/virtualmachines.py)
   bases: ImageAttachmentsMixin, PrimaryModel
     name                                   CharField                   REQ len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     default_platform                       ForeignKey                      -> dcim.Platform on_delete=SET_NULL
     default_vcpus                          DecimalField                    decimal(6,2)
     default_memory                         PositiveIntegerField
     virtual_machine_count                  CounterCacheField
     images (ImageAttachmentsMixin)         GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(Lower('name'), name='%(app_label)s_%(class)s_unique_name',
      violation_error_message=_('Virtual machine type name must be unique.')),)
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## vpn.IKEPolicy   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     version                                PositiveSmallIntegerField       def=UNRESOLVED:IKEVersionChoices.VERSION_2 choices=IKEVersionChoices
     mode                                   CharField                       choices=IKEModeChoices
     proposals                              ManyToManyField                 -> vpn.IKEProposal
     preshared_key                          TextField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.IKEProposal   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     authentication_method                  CharField                   REQ choices=AuthenticationMethodChoices
     encryption_algorithm                   CharField                   REQ choices=EncryptionAlgorithmChoices
     authentication_algorithm               CharField                       choices=AuthenticationAlgorithmChoices
     group                                  PositiveSmallIntegerField   REQ choices=DHGroupChoices
     sa_lifetime                            PositiveIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.IPSecPolicy   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     proposals                              ManyToManyField                 -> vpn.IPSecProposal
     pfs_group                              PositiveSmallIntegerField       choices=DHGroupChoices
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.IPSecProfile   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     mode                                   CharField                   REQ choices=IPSecModeChoices
     ike_policy                             ForeignKey                  REQ -> vpn.IKEPolicy on_delete=PROTECT
     ipsec_policy                           ForeignKey                  REQ -> vpn.IPSecPolicy on_delete=PROTECT
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.IPSecProposal   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     encryption_algorithm                   CharField                       choices=EncryptionAlgorithmChoices
     authentication_algorithm               CharField                       choices=AuthenticationAlgorithmChoices
     sa_lifetime_seconds                    PositiveIntegerField
     sa_lifetime_data                       PositiveIntegerField
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.L2VPN   (vpn/models/l2vpn.py)
   bases: ContactsMixin, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     type                                   CharField                   REQ len=50 choices=L2VPNTypeChoices
     status                                 CharField                       len=50 def=UNRESOLVED:L2VPNStatusChoices.STATUS_ACTIVE choices=L2VPNStatusChoices
     identifier                             BigIntegerField
     import_targets                         ManyToManyField                 -> ipam.RouteTarget
     export_targets                         ManyToManyField                 -> ipam.RouteTarget
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name', 'identifier')

## vpn.L2VPNTermination   (vpn/models/l2vpn.py)
   bases: NetBoxModel
     l2vpn                                  ForeignKey                  REQ -> vpn.L2VPN on_delete=CASCADE
     assigned_object_type                   ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     assigned_object_id                     PositiveBigIntegerField     REQ
     assigned_object                        GenericForeignKey           REQ
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('assigned_object_type', 'assigned_object_id'),
      name='vpn_l2vpntermination_assigned_object'),)
   meta.ordering: ('l2vpn',)

## vpn.Tunnel   (vpn/models/tunnels.py)
   bases: ContactsMixin, PrimaryModel
     name                                   CharField                   REQ UNIQUE len=100
     status                                 CharField                       len=50 def=UNRESOLVED:TunnelStatusChoices.STATUS_ACTIVE choices=TunnelStatusChoices
     group                                  ForeignKey                      -> vpn.TunnelGroup on_delete=PROTECT
     encapsulation                          CharField                   REQ len=50 choices=TunnelEncapsulationChoices
     ipsec_profile                          ForeignKey                      -> vpn.IPSecProfile on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     tunnel_id                              PositiveBigIntegerField
     contacts (ContactsMixin)               GenericRelation
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'),
      name='%(app_label)s_%(class)s_group_name'), models.UniqueConstraint(fields=('name',),
      name='%(app_label)s_%(class)s_name', condition=Q(group__isnull=True)))
   meta.ordering: ('name',)

## vpn.TunnelGroup   (vpn/models/tunnels.py)
   bases: ContactsMixin, OrganizationalModel
   (no own columns — every field is inherited from ContactsMixin, OrganizationalModel)
     contacts (ContactsMixin)               GenericRelation
     name (OrganizationalModel)             CharField                   REQ UNIQUE len=100
     slug (OrganizationalModel)             SlugField                   REQ UNIQUE len=100
     description (OrganizationalModel)      CharField                       len=200
     comments (OrganizationalModel)         TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('name',)

## vpn.TunnelTermination   (vpn/models/tunnels.py)
   bases: CustomFieldsMixin, CustomLinksMixin, TagsMixin, ChangeLoggedModel
     tunnel                                 ForeignKey                  REQ -> vpn.Tunnel on_delete=CASCADE
     role                                   CharField                       len=50 def=UNRESOLVED:TunnelTerminationRoleChoices.ROLE_PEER choices=TunnelTerminationRoleChoices
     termination_type                       ForeignKey                  REQ -> contenttypes.ContentType on_delete=PROTECT
     termination_id                         PositiveBigIntegerField
     termination                            GenericForeignKey           REQ
     outside_ip                             ForeignKey                      -> ipam.IPAddress on_delete=PROTECT
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
   meta.constraints: (models.UniqueConstraint(fields=('termination_type', 'termination_id'),
      name='%(app_label)s_%(class)s_termination', violation_error_message=_('An object may be terminated to
      only one tunnel at a time.')),)
   meta.ordering: ('tunnel', 'role', 'pk')
   meta.indexes: (models.Index(fields=('tunnel', 'role', 'id')),)

## wireless.WirelessAuthenticationBase   (wireless/models.py)
   bases: models.Model
     auth_type                              CharField                       len=50 choices=WirelessAuthTypeChoices
     auth_cipher                            CharField                       len=50 choices=WirelessAuthCipherChoices
     auth_psk                               CharField                       len=64

## wireless.WirelessLAN   (wireless/models.py)
   bases: WirelessAuthenticationBase, CachedScopeMixin, PrimaryModel
     ssid                                   CharField                   REQ len=32
     group                                  ForeignKey                      -> wireless.WirelessLANGroup on_delete=SET_NULL
     status                                 CharField                       len=50 def=UNRESOLVED:WirelessLANStatusChoices.STATUS_ACTIVE choices=WirelessLANStatusChoices
     vlan                                   ForeignKey                      -> ipam.VLAN on_delete=PROTECT
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     auth_type (WirelessAuthenticationBase) CharField                       len=50 choices=WirelessAuthTypeChoices
     auth_cipher (WirelessAuthenticationBase) CharField                       len=50 choices=WirelessAuthCipherChoices
     auth_psk (WirelessAuthenticationBase)  CharField                       len=64
     scope_type (CachedScopeMixin)          ForeignKey                      -> contenttypes.ContentType on_delete=PROTECT
     scope_id (CachedScopeMixin)            PositiveBigIntegerField
     scope (CachedScopeMixin)               GenericForeignKey
     _location (CachedScopeMixin)           ForeignKey                      -> dcim.Location on_delete=CASCADE
     _site (CachedScopeMixin)               ForeignKey                      -> dcim.Site on_delete=CASCADE
     _region (CachedScopeMixin)             ForeignKey                      -> dcim.Region on_delete=SET_NULL
     _site_group (CachedScopeMixin)         ForeignKey                      -> dcim.SiteGroup on_delete=SET_NULL
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.ordering: ('ssid', 'pk')
   meta.indexes: (models.Index(fields=('ssid', 'id')), models.Index(fields=('scope_type', 'scope_id')))

## wireless.WirelessLANGroup   (wireless/models.py)
   bases: NestedGroupModel
   shadows inherited: name (NestedGroupModel), slug (NestedGroupModel)
     name                                   CharField                   REQ UNIQUE len=100
     slug                                   SlugField                   REQ UNIQUE len=100
     parent (NestedGroupModel)              TreeForeignKey                  -> wireless.WirelessLANGroup on_delete=CASCADE
     description (NestedGroupModel)         CharField                       len=200
     comments (NestedGroupModel)            TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'),
      name='%(app_label)s_%(class)s_unique_parent_name'),)
   meta.ordering: ('name', 'pk')
   meta.indexes: ()

## wireless.WirelessLink   (wireless/models.py)
   bases: WirelessAuthenticationBase, DistanceMixin, PrimaryModel
     interface_a                            ForeignKey                  REQ -> dcim.Interface on_delete=PROTECT
     interface_b                            ForeignKey                  REQ -> dcim.Interface on_delete=PROTECT
     ssid                                   CharField                       len=32
     status                                 CharField                       len=50 def=UNRESOLVED:LinkStatusChoices.STATUS_CONNECTED choices=LinkStatusChoices
     tenant                                 ForeignKey                      -> tenancy.Tenant on_delete=PROTECT
     _interface_a_device                    ForeignKey                      -> dcim.Device on_delete=CASCADE
     _interface_b_device                    ForeignKey                      -> dcim.Device on_delete=CASCADE
     auth_type (WirelessAuthenticationBase) CharField                       len=50 choices=WirelessAuthTypeChoices
     auth_cipher (WirelessAuthenticationBase) CharField                       len=50 choices=WirelessAuthCipherChoices
     auth_psk (WirelessAuthenticationBase)  CharField                       len=64
     distance (DistanceMixin)               DecimalField                    decimal(8,2)
     distance_unit (DistanceMixin)          CharField                       len=50 choices=DistanceUnitChoices
     _abs_distance (DistanceMixin)          DecimalField                    decimal(13,4)
     description (PrimaryModel)             CharField                       len=200
     comments (PrimaryModel)                TextField
     owner (OwnerMixin)                     ForeignKey                      -> users.Owner on_delete=PROTECT
     bookmarks (BookmarksMixin)             GenericRelation
     created (ChangeLoggingMixin)           DateTimeField
     last_updated (ChangeLoggingMixin)      DateTimeField
     custom_field_data (CustomFieldsMixin)  JSONField                       def=UNRESOLVED:dict
     journal_entries (JournalingMixin)      GenericRelation
     subscriptions (NotificationsMixin)     GenericRelation
     tags (TagsMixin)                       NetBoxTaggableManagerField      M2M -> extras.Tag (via TaggedItem, not a column)
   meta.constraints: (models.UniqueConstraint(fields=('interface_a', 'interface_b'),
      name='%(app_label)s_%(class)s_unique_interfaces'),)
   meta.ordering: ['pk']
```
