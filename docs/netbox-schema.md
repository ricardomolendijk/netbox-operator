# NetBox 4.6.8 data-model reference

Machine-extracted from the NetBox source (`netbox-community/netbox`, tag/release
`4.6.8`, `release.yaml: published 2026-08-11`) by walking the Django model AST.
This is the authoritative field list the operator's CRD schemas are derived from.

How to read an entry:

- `REQ`   — the column is `NOT NULL` with no default: it must be supplied on create.
- `-> x`  — `ForeignKey` / `OneToOneField` / `ManyToManyField` target (the SQL FK).
- `UNIQUE` — column-level unique index.
- `meta.constraints` — table-level `UniqueConstraint`s. **These are the natural keys
  the operator uses to look an object up before deciding create-vs-update.**
- `GenericForeignKey` pairs (`*_type` / `*_id`) are polymorphic FKs; over the REST API
  the `_type` half is written as an `"app_label.model"` string.
- Fields prefixed `_` (e.g. `_site`, `_depth`, `_children`) and every `CounterCacheField`
  are denormalised caches maintained by NetBox itself — read-only, never write them.

Regenerate with `hack/extract-netbox-schema.py` (see `docs/regenerating.md`).

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
     color                        ColorField                 

## circuits.Circuit   (circuits/models/circuits.py)
   bases: ContactsMixin, ImageAttachmentsMixin, DistanceMixin, PrimaryModel
     cid                          CharField              REQ len=100
     provider                     ForeignKey             REQ -> circuits.Provider
     provider_account             ForeignKey                 -> circuits.ProviderAccount
     type                         ForeignKey             REQ -> circuits.CircuitType
     status                       CharField                  len=50 def='CircuitStatusChoices.STATUS_ACTIVE' choices=CircuitStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     install_date                 DateField                  
     termination_date             DateField                  
     commit_rate                  PositiveIntegerField       
     termination_a                ForeignKey                 -> circuits.CircuitTermination
     termination_z                ForeignKey                 -> circuits.CircuitTermination
     group_assignments            GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'cid'), name='%(app_label)s_%(class)s_unique_provider_cid'), models.UniqueConstraint(fields=('provider_account', 'cid'), name='%(app_label)s_%(class)s_unique_provideraccount_cid'))
   meta.ordering: ['provider', 'provider_account', 'cid']
   meta.indexes: (models.Index(fields=('provider', 'provider_account', 'cid')),)

## circuits.CircuitGroup   (circuits/models/circuits.py)
   bases: OrganizationalModel
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ('name',)

## circuits.CircuitGroupAssignment   (circuits/models/circuits.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     member_type                  ForeignKey             REQ -> contenttypes.ContentType
     member_id                    PositiveBigIntegerField REQ 
     member                       GenericForeignKey      REQ 
     group                        ForeignKey             REQ -> circuits.CircuitGroup
     priority                     CharField                  len=50 choices=CircuitPriorityChoices
   meta.constraints: (models.UniqueConstraint(fields=('member_type', 'member_id', 'group'), name='%(app_label)s_%(class)s_unique_member_group'),)
   meta.ordering: ('group', 'member_type', 'member_id', 'priority', 'pk')
   meta.indexes: (models.Index(fields=('group', 'member_type', 'member_id', 'priority', 'id')),)

## circuits.CircuitTermination   (circuits/models/circuits.py)
   bases: CustomFieldsMixin, CustomLinksMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel, CabledObjectModel
     circuit                      ForeignKey             REQ -> circuits.Circuit
     term_side                    CharField              REQ len=1 choices=CircuitTerminationSideChoices
     termination_type             ForeignKey                 -> contenttypes.ContentType
     termination_id               PositiveBigIntegerField     
     termination                  GenericForeignKey      REQ 
     port_speed                   PositiveIntegerField       
     upstream_speed               PositiveIntegerField       
     xconnect_id                  CharField                  len=50
     pp_info                      CharField                  len=100
     description                  CharField                  len=200
     _provider_network            ForeignKey                 -> circuits.ProviderNetwork
     _location                    ForeignKey                 -> dcim.Location
     _site                        ForeignKey                 -> dcim.Site
     _region                      ForeignKey                 -> dcim.Region
     _site_group                  ForeignKey                 -> dcim.SiteGroup
   meta.constraints: (models.UniqueConstraint(fields=('circuit', 'term_side'), name='%(app_label)s_%(class)s_unique_circuit_term_side'),)
   meta.ordering: ['circuit', 'term_side']
   meta.indexes: (models.Index(fields=('termination_type', 'termination_id')),)

## circuits.Provider   (circuits/models/providers.py)
   bases: ContactsMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
     asns                         ManyToManyField            -> ipam.ASN
   meta.ordering: ['name']

## circuits.ProviderAccount   (circuits/models/providers.py)
   bases: ContactsMixin, PrimaryModel
     provider                     ForeignKey             REQ -> circuits.Provider
     account                      CharField              REQ len=100
     name                         CharField                  len=100
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'account'), name='%(app_label)s_%(class)s_unique_provider_account'), models.UniqueConstraint(fields=('provider', 'name'), name='%(app_label)s_%(class)s_unique_provider_name', condition=~Q(name='')))
   meta.ordering: ('provider', 'account')

## circuits.ProviderNetwork   (circuits/models/providers.py)
   bases: PrimaryModel
     name                         CharField              REQ len=100
     provider                     ForeignKey             REQ -> circuits.Provider
     service_id                   CharField                  len=100
   meta.constraints: (models.UniqueConstraint(fields=('provider', 'name'), name='%(app_label)s_%(class)s_unique_provider_name'),)
   meta.ordering: ('provider', 'name')

## circuits.VirtualCircuit   (circuits/models/virtual_circuits.py)
   bases: ContactsMixin, PrimaryModel
     cid                          CharField              REQ len=100
     provider_network             ForeignKey             REQ -> circuits.ProviderNetwork
     provider_account             ForeignKey                 -> circuits.ProviderAccount
     type                         ForeignKey             REQ -> circuits.VirtualCircuitType
     status                       CharField                  len=50 def='CircuitStatusChoices.STATUS_ACTIVE' choices=CircuitStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     group_assignments            GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('provider_network', 'cid'), name='%(app_label)s_%(class)s_unique_provider_network_cid'), models.UniqueConstraint(fields=('provider_account', 'cid'), name='%(app_label)s_%(class)s_unique_provideraccount_cid'))
   meta.ordering: ['provider_network', 'provider_account', 'cid']
   meta.indexes: (models.Index(fields=('provider_network', 'provider_account', 'cid')),)

## circuits.VirtualCircuitTermination   (circuits/models/virtual_circuits.py)
   bases: CustomFieldsMixin, CustomLinksMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     virtual_circuit              ForeignKey             REQ -> circuits.VirtualCircuit
     role                         CharField                  len=50 def='VirtualCircuitTerminationRoleChoices.ROLE_PEER' choices=VirtualCircuitTerminationRoleChoices
     interface                    OneToOneField          REQ -> dcim.Interface
     description                  CharField                  len=200
   meta.ordering: ['virtual_circuit', 'role', 'pk']
   meta.indexes: (models.Index(fields=('virtual_circuit', 'role', 'id')),)

## core.AutoSyncRecord   (core/models/data.py)
   bases: models.Model
     datafile                     ForeignKey             REQ -> DataFile
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       GenericForeignKey      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id'), name='%(app_label)s_%(class)s_object'),)

## core.ConfigRevision   (core/models/config.py)
   bases: models.Model
     active                       BooleanField               def=False
     created                      DateTimeField          REQ 
     comment                      CharField                  len=200
     data                         JSONField                  
   meta.constraints: [models.UniqueConstraint(fields=('active',), condition=models.Q(active=True), name='unique_active_config_revision')]
   meta.ordering: ['-created']
   meta.indexes: (models.Index(fields=('-created',)),)

## core.DataFile   (core/models/data.py)
   bases: models.Model
     created                      DateTimeField          REQ 
     last_updated                 DateTimeField          REQ 
     source                       ForeignKey             REQ -> core.DataSource
     path                         CharField              REQ len=1000
     size                         PositiveIntegerField   REQ 
     hash                         CharField              REQ len=64
   meta.constraints: (models.UniqueConstraint(fields=('source', 'path'), name='%(app_label)s_%(class)s_unique_source_path'),)
   meta.ordering: ('source', 'path')

## core.DataSource   (core/models/data.py)
   bases: JobsMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     type                         CharField              REQ len=50
     source_url                   CharField              REQ len=200
     status                       CharField                  len=50 def='DataSourceStatusChoices.NEW' choices=DataSourceStatusChoices
     enabled                      BooleanField               def=True
     sync_interval                PositiveSmallIntegerField     choices=JobIntervalChoices
     ignore_rules                 TextField                  
     parameters                   JSONField                  
     last_synced                  DateTimeField              
   meta.ordering: ('name',)

## core.Job   (core/models/jobs.py)
   bases: models.Model
     object_type                  ForeignKey                 -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField     
     object                       GenericForeignKey      REQ 
     name                         CharField              REQ len=200
     created                      DateTimeField          REQ 
     scheduled                    DateTimeField              
     interval                     PositiveIntegerField       
     started                      DateTimeField              
     completed                    DateTimeField              
     user                         ForeignKey                 -> settings.AUTH_USER_MODEL
     status                       CharField                  len=30 def='JobStatusChoices.STATUS_PENDING' choices=JobStatusChoices
     data                         JSONField                  
     error                        TextField                  
     job_id                       UUIDField              REQ UNIQUE
     queue_name                   CharField                  len=100
     notifications                CharField                  len=30 def='JobNotificationChoices.NOTIFICATION_ALWAYS' choices=JobNotificationChoices
     log_entries                  ArrayField                 def='list'
   meta.ordering: ['-created']
   meta.indexes: (models.Index(fields=('-created',)), models.Index(fields=('object_type', 'object_id')))

## core.ManagedFile   (core/models/files.py)
   bases: SyncedDataMixin, models.Model
     created                      DateTimeField          REQ 
     last_updated                 DateTimeField              
     file_root                    CharField              REQ len=1000 choices=ManagedFileRootPathChoices
   meta.constraints: (models.UniqueConstraint(fields=('file_root', 'file_path'), name='%(app_label)s_%(class)s_unique_root_path'),)
   meta.ordering: ('file_root', 'file_path')

## core.ObjectChange   (core/models/change_logging.py)
   bases: models.Model
     time                         DateTimeField          REQ 
     user                         ForeignKey                 -> settings.AUTH_USER_MODEL
     user_name                    CharField              REQ len=150
     request_id                   UUIDField              REQ 
     action                       CharField              REQ len=50 choices=ObjectChangeActionChoices
     changed_object_type          ForeignKey             REQ -> contenttypes.ContentType
     changed_object_id            PositiveBigIntegerField REQ 
     changed_object               GenericForeignKey      REQ 
     related_object_type          ForeignKey                 -> contenttypes.ContentType
     related_object_id            PositiveBigIntegerField     
     related_object               GenericForeignKey      REQ 
     object_repr                  CharField              REQ len=200
     message                      CharField                  len=200
     prechange_data               JSONField                  
     postchange_data              JSONField                  
   meta.ordering: ['-time']
   meta.indexes: (models.Index(fields=('changed_object_type', 'changed_object_id')), models.Index(fields=('related_object_type', 'related_object_id')))

## core.ObjectType   (core/models/object_types.py)
   bases: ContentType
     contenttype_ptr              OneToOneField          REQ -> contenttypes.ContentType
     public                       BooleanField               def=False
     features                     ArrayField                 def='list'
   meta.ordering: ('app_label', 'model')
   meta.indexes: [GinIndex(fields=['features'])]

## dcim.BaseInterface   (dcim/models/device_components.py)
   bases: models.Model
     enabled                      BooleanField               def=True
     mtu                          PositiveIntegerField       
     mode                         CharField                  len=50 choices=InterfaceModeChoices
     parent                       ForeignKey                 -> self
     bridge                       ForeignKey                 -> self
     untagged_vlan                ForeignKey                 -> ipam.VLAN
     tagged_vlans                 ManyToManyField            -> ipam.VLAN
     qinq_svlan                   ForeignKey                 -> ipam.VLAN
     vlan_translation_policy      ForeignKey                 -> ipam.VLANTranslationPolicy
     primary_mac_address          OneToOneField              -> dcim.MACAddress

## dcim.Cable   (dcim/models/cables.py)
   bases: PrimaryModel
     type                         CharField                  len=50 choices=CableTypeChoices
     status                       CharField                  len=50 def='LinkStatusChoices.STATUS_CONNECTED' choices=LinkStatusChoices
     profile                      CharField                  len=50 choices=CableProfileChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     label                        CharField                  len=100
     color                        ColorField                 
     length                       DecimalField               
     length_unit                  CharField                  len=50 choices=CableLengthUnitChoices
     _abs_length                  DecimalField               
     bundle                       ForeignKey                 -> dcim.CableBundle
   meta.ordering: ('pk',)

## dcim.CableBundle   (dcim/models/cables.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
   meta.ordering: ('name',)

## dcim.CablePath   (dcim/models/cables.py)
   bases: models.Model
     path                         JSONField                  def='list'
     is_active                    BooleanField               def=False
     is_complete                  BooleanField               def=False
     is_split                     BooleanField               def=False
   meta.indexes: (GinIndex(fields=('_nodes',)),)

## dcim.CableTermination   (dcim/models/cables.py)
   bases: ChangeLoggedModel
     cable                        ForeignKey             REQ -> dcim.Cable
     cable_end                    CharField              REQ len=1 choices=CableEndChoices
     termination_type             ForeignKey             REQ -> contenttypes.ContentType
     termination_id               PositiveBigIntegerField REQ 
     termination                  GenericForeignKey      REQ 
     connector                    PositiveSmallIntegerField     
     positions                    ArrayField                 
     _device                      ForeignKey                 -> dcim.Device
     _rack                        ForeignKey                 -> dcim.Rack
     _location                    ForeignKey                 -> dcim.Location
     _site                        ForeignKey                 -> dcim.Site
   meta.constraints: (models.UniqueConstraint(fields=('termination_type', 'termination_id'), name='%(app_label)s_%(class)s_unique_termination'), models.UniqueConstraint(fields=('cable', 'cable_end', 'connector'), name='%(app_label)s_%(class)s_unique_connector'))
   meta.ordering: ('cable', 'cable_end', 'connector', 'pk')

## dcim.CabledObjectModel   (dcim/models/device_components.py)
   bases: models.Model
     cable                        ForeignKey                 -> dcim.Cable
     cable_end                    CharField                  len=1 choices=CableEndChoices
     cable_connector              PositiveSmallIntegerField     
     cable_positions              ArrayField                 
     mark_connected               BooleanField               def=False
     cable_terminations           GenericRelation        REQ 

## dcim.CachedScopeMixin   (dcim/models/mixins.py)
   bases: models.Model
     scope_type                   ForeignKey                 -> contenttypes.ContentType
     scope_id                     PositiveBigIntegerField     
     scope                        GenericForeignKey      REQ 
     _location                    ForeignKey                 -> dcim.Location
     _site                        ForeignKey                 -> dcim.Site
     _region                      ForeignKey                 -> dcim.Region
     _site_group                  ForeignKey                 -> dcim.SiteGroup

## dcim.ComponentModel   (dcim/models/device_components.py)
   bases: OwnerMixin, NetBoxModel
     device                       ForeignKey             REQ -> dcim.Device
     name                         CharField              REQ len=64
     label                        CharField                  len=64
     description                  CharField                  len=200
     _site                        ForeignKey                 -> dcim.Site
     _location                    ForeignKey                 -> dcim.Location
     _rack                        ForeignKey                 -> dcim.Rack
   meta.constraints: (models.UniqueConstraint(fields=('device', 'name'), name='%(app_label)s_%(class)s_unique_device_name'),)
   meta.ordering: ('device', 'name')

## dcim.ComponentTemplateModel   (dcim/models/device_component_templates.py)
   bases: ChangeLoggedModel, TrackingModelMixin
     device_type                  ForeignKey             REQ -> dcim.DeviceType
     name                         CharField              REQ len=64
     label                        CharField                  len=64
     description                  CharField                  len=200
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'), name='%(app_label)s_%(class)s_unique_device_type_name'),)
   meta.ordering: ('device_type', 'name')

## dcim.ConsolePort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                         CharField                  len=50 choices=ConsolePortTypeChoices
     speed                        PositiveIntegerField       choices=ConsolePortSpeedChoices

## dcim.ConsolePortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField                  len=50 choices=ConsolePortTypeChoices

## dcim.ConsoleServerPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                         CharField                  len=50 choices=ConsolePortTypeChoices
     speed                        PositiveIntegerField       choices=ConsolePortSpeedChoices

## dcim.ConsoleServerPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField                  len=50 choices=ConsolePortTypeChoices

## dcim.Device   (dcim/models/devices.py)
   bases: ContactsMixin, ImageAttachmentsMixin, RenderConfigMixin, ConfigContextModel, TrackingModelMixin, PrimaryModel
     device_type                  ForeignKey             REQ -> dcim.DeviceType
     role                         ForeignKey             REQ -> dcim.DeviceRole
     tenant                       ForeignKey                 -> tenancy.Tenant
     platform                     ForeignKey                 -> dcim.Platform
     name                         CharField                  len=64
     serial                       CharField                  len=50
     asset_tag                    CharField                  UNIQUE len=50
     site                         ForeignKey             REQ -> dcim.Site
     location                     ForeignKey                 -> dcim.Location
     rack                         ForeignKey                 -> dcim.Rack
     position                     DecimalField               
     face                         CharField                  len=50 choices=DeviceFaceChoices
     status                       CharField                  len=50 def='DeviceStatusChoices.STATUS_ACTIVE' choices=DeviceStatusChoices
     airflow                      CharField                  len=50 choices=DeviceAirflowChoices
     primary_ip4                  OneToOneField              -> ipam.IPAddress
     primary_ip6                  OneToOneField              -> ipam.IPAddress
     oob_ip                       OneToOneField              -> ipam.IPAddress
     cluster                      ForeignKey                 -> virtualization.Cluster
     virtual_chassis              ForeignKey                 -> VirtualChassis
     vc_position                  PositiveIntegerField       
     vc_priority                  PositiveSmallIntegerField     
     latitude                     DecimalField               
     longitude                    DecimalField               
     services                     GenericRelation        REQ 
     console_port_count           CounterCacheField      REQ 
     console_server_port_count    CounterCacheField      REQ 
     power_port_count             CounterCacheField      REQ 
     power_outlet_count           CounterCacheField      REQ 
     interface_count              CounterCacheField      REQ 
     front_port_count             CounterCacheField      REQ 
     rear_port_count              CounterCacheField      REQ 
     device_bay_count             CounterCacheField      REQ 
     module_bay_count             CounterCacheField      REQ 
     inventory_item_count         CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(Lower('name'), 'site', 'tenant', name='%(app_label)s_%(class)s_unique_name_site_tenant'), models.UniqueConstraint(Lower('name'), 'site', name='%(app_label)s_%(class)s_unique_name_site', condition=Q(tenant__isnull=True), violation_error_message=_('Device name must be unique per site.')), models.UniqueConstraint(fields=('rack', 'position', 'face'), name='%(app_label)s_%(clas
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')),)

## dcim.DeviceBay   (dcim/models/device_components.py)
   bases: ComponentModel, TrackingModelMixin
     installed_device             OneToOneField              -> dcim.Device
     enabled                      BooleanField               def=True

## dcim.DeviceBayTemplate   (dcim/models/device_component_templates.py)
   bases: ComponentTemplateModel
     enabled                      BooleanField               def=True

## dcim.DeviceRole   (dcim/models/devices.py)
   bases: NestedGroupModel
     color                        ColorField                 def='ColorChoices.COLOR_GREY'
     vm_role                      BooleanField               def=True
     config_template              ForeignKey                 -> extras.ConfigTemplate
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'), name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A top-level device role with this name already exists.')), models.UniqueConstraint(fields=('parent', 'slug'), name='%(app_label)s_%(class)s_parent_slug'), models.U
   meta.ordering: ('name',)
   meta.indexes: ()

## dcim.DeviceType   (dcim/models/devices.py)
   bases: ImageAttachmentsMixin, PrimaryModel, WeightMixin
     manufacturer                 ForeignKey             REQ -> dcim.Manufacturer
     model                        CharField              REQ len=100
     slug                         SlugField              REQ len=100
     default_platform             ForeignKey                 -> dcim.Platform
     part_number                  CharField                  len=50
     u_height                     DecimalField               def=1.0
     exclude_from_utilization     BooleanField               def=False
     is_full_depth                BooleanField               def=True
     subdevice_role               CharField                  len=50 choices=SubdeviceRoleChoices
     airflow                      CharField                  len=50 choices=DeviceAirflowChoices
     console_port_template_count  CounterCacheField      REQ 
     console_server_port_template_count CounterCacheField      REQ 
     power_port_template_count    CounterCacheField      REQ 
     power_outlet_template_count  CounterCacheField      REQ 
     interface_template_count     CounterCacheField      REQ 
     front_port_template_count    CounterCacheField      REQ 
     rear_port_template_count     CounterCacheField      REQ 
     device_bay_template_count    CounterCacheField      REQ 
     module_bay_template_count    CounterCacheField      REQ 
     inventory_item_template_count CounterCacheField      REQ 
     device_count                 CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'), name='%(app_label)s_%(class)s_unique_manufacturer_model'), models.UniqueConstraint(fields=('manufacturer', 'slug'), name='%(app_label)s_%(class)s_unique_manufacturer_slug'))
   meta.ordering: ['manufacturer', 'model']

## dcim.FrontPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, TrackingModelMixin
     type                         CharField              REQ len=50 choices=PortTypeChoices
     color                        ColorField                 
     positions                    PositiveSmallIntegerField     def=1
   meta.constraints: (models.UniqueConstraint(fields=('device', 'name'), name='%(app_label)s_%(class)s_unique_device_name'),)

## dcim.FrontPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField              REQ len=50 choices=PortTypeChoices
     color                        ColorField                 
     positions                    PositiveSmallIntegerField     def=1
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'), name='%(app_label)s_%(class)s_unique_device_type_name'), models.UniqueConstraint(fields=('module_type', 'name'), name='%(app_label)s_%(class)s_unique_module_type_name'))

## dcim.Interface   (dcim/models/device_components.py)
   bases: InterfaceValidationMixin, ModularComponentModel, BaseInterface, CabledObjectModel, PathEndpoint, TrackingModelMixin
     _name                        NaturalOrderingField       len=100
     vdcs                         ManyToManyField        REQ -> dcim.VirtualDeviceContext
     lag                          ForeignKey                 -> self
     type                         CharField              REQ len=50 choices=InterfaceTypeChoices
     mgmt_only                    BooleanField               def=False
     speed                        PositiveBigIntegerField     
     duplex                       CharField                  len=50 choices=InterfaceDuplexChoices
     wwn                          WWNField                   
     rf_role                      CharField                  len=30 choices=WirelessRoleChoices
     rf_channel                   CharField                  len=50 choices=WirelessChannelChoices
     rf_channel_frequency         DecimalField               
     rf_channel_width             DecimalField               
     tx_power                     SmallIntegerField          
     poe_mode                     CharField                  len=50 choices=InterfacePoEModeChoices
     poe_type                     CharField                  len=50 choices=InterfacePoETypeChoices
     wireless_link                ForeignKey                 -> wireless.WirelessLink
     wireless_lans                ManyToManyField            -> wireless.WirelessLAN
     vrf                          ForeignKey                 -> ipam.VRF
     ip_addresses                 GenericRelation        REQ 
     mac_addresses                GenericRelation        REQ 
     fhrp_group_assignments       GenericRelation        REQ 
     tunnel_terminations          GenericRelation        REQ 
     l2vpn_terminations           GenericRelation        REQ 
   meta.ordering: ('device', CollateAsChar('_name'))

## dcim.InterfaceTemplate   (dcim/models/device_component_templates.py)
   bases: InterfaceValidationMixin, ModularComponentTemplateModel
     _name                        NaturalOrderingField       len=100
     type                         CharField              REQ len=50 choices=InterfaceTypeChoices
     enabled                      BooleanField               def=True
     mgmt_only                    BooleanField               def=False
     bridge                       ForeignKey                 -> self
     poe_mode                     CharField                  len=50 choices=InterfacePoEModeChoices
     poe_type                     CharField                  len=50 choices=InterfacePoETypeChoices
     rf_role                      CharField                  len=30 choices=WirelessRoleChoices

## dcim.InventoryItem   (dcim/models/device_components.py)
   bases: MPTTModel, ComponentModel, TrackingModelMixin
     component_type               ForeignKey                 -> contenttypes.ContentType
     component_id                 PositiveBigIntegerField     
     component                    GenericForeignKey      REQ 
     status                       CharField                  len=50 def='InventoryItemStatusChoices.STATUS_ACTIVE' choices=InventoryItemStatusChoices
     role                         ForeignKey                 -> dcim.InventoryItemRole
     manufacturer                 ForeignKey                 -> dcim.Manufacturer
     part_id                      CharField                  len=50
     serial                       CharField                  len=50
     asset_tag                    CharField                  UNIQUE len=50
     discovered                   BooleanField               def=False
   meta.constraints: (models.UniqueConstraint(fields=('device', 'parent', 'name'), name='%(app_label)s_%(class)s_unique_device_parent_name'),)
   meta.ordering: ('device__id', 'parent__id', 'name')
   meta.indexes: (models.Index(fields=('component_type', 'component_id')),)

## dcim.InventoryItemRole   (dcim/models/device_components.py)
   bases: OrganizationalModel
     color                        ColorField                 def='ColorChoices.COLOR_GREY'
   meta.ordering: ('name',)

## dcim.InventoryItemTemplate   (dcim/models/device_component_templates.py)
   bases: MPTTModel, ComponentTemplateModel
     component_type               ForeignKey                 -> contenttypes.ContentType
     component_id                 PositiveBigIntegerField     
     component                    GenericForeignKey      REQ 
     role                         ForeignKey                 -> dcim.InventoryItemRole
     manufacturer                 ForeignKey                 -> dcim.Manufacturer
     part_id                      CharField                  len=50
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'parent', 'name'), name='%(app_label)s_%(class)s_unique_device_type_parent_name'),)
   meta.ordering: ('device_type__id', 'parent__id', 'name')
   meta.indexes: (models.Index(fields=('component_type', 'component_id')),)

## dcim.Location   (dcim/models/sites.py)
   bases: ContactsMixin, ImageAttachmentsMixin, NestedGroupModel
     site                         ForeignKey             REQ -> dcim.Site
     status                       CharField                  len=50 def='LocationStatusChoices.STATUS_ACTIVE' choices=LocationStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     facility                     CharField                  len=50
     prefixes                     GenericRelation        REQ 
     vlan_groups                  GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('site', 'parent', 'name'), name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('site', 'name'), name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A location with this name already exists within the specified site.')), models.UniqueConstraint(fields=('site', 'parent', 'slug'), name='%(app_label
   meta.ordering: ['site', 'name']
   meta.indexes: ()

## dcim.MACAddress   (dcim/models/devices.py)
   bases: PrimaryModel
     mac_address                  MACAddressField        REQ 
     assigned_object_type         ForeignKey                 -> contenttypes.ContentType
     assigned_object_id           PositiveBigIntegerField     
     assigned_object              GenericForeignKey      REQ 
   meta.ordering: ('mac_address', 'pk')
   meta.indexes: (models.Index(fields=('mac_address', 'id')), models.Index(fields=('assigned_object_type', 'assigned_object_id')))

## dcim.ModularComponentModel   (dcim/models/device_components.py)
   bases: ComponentModel
     module                       ForeignKey                 -> dcim.Module
     inventory_items              GenericRelation        REQ 

## dcim.ModularComponentTemplateModel   (dcim/models/device_component_templates.py)
   bases: ComponentTemplateModel
     device_type                  ForeignKey                 -> dcim.DeviceType
     module_type                  ForeignKey                 -> dcim.ModuleType
   meta.constraints: (models.UniqueConstraint(fields=('device_type', 'name'), name='%(app_label)s_%(class)s_unique_device_type_name'), models.UniqueConstraint(fields=('module_type', 'name'), name='%(app_label)s_%(class)s_unique_module_type_name'))
   meta.ordering: ('device_type', 'module_type', 'name')
   meta.indexes: (models.Index(fields=('device_type', 'module_type', 'name')),)

## dcim.Module   (dcim/models/modules.py)
   bases: TrackingModelMixin, PrimaryModel
     device                       ForeignKey             REQ -> dcim.Device
     module_bay                   OneToOneField          REQ -> dcim.ModuleBay
     module_type                  ForeignKey             REQ -> dcim.ModuleType
     status                       CharField                  len=50 def='ModuleStatusChoices.STATUS_ACTIVE' choices=ModuleStatusChoices
     serial                       CharField                  len=50
     asset_tag                    CharField                  UNIQUE len=50
   meta.ordering: ('module_bay',)

## dcim.ModuleBay   (dcim/models/device_components.py)
   bases: ModularComponentModel, TrackingModelMixin, MPTTModel
     position                     CharField                  len=30
     enabled                      BooleanField               def=True
   meta.constraints: (models.UniqueConstraint(fields=('device', 'module', 'name'), name='%(app_label)s_%(class)s_unique_device_module_name'),)
   meta.indexes: ()

## dcim.ModuleBayTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     position                     CharField                  len=30
     enabled                      BooleanField               def=True

## dcim.ModuleType   (dcim/models/modules.py)
   bases: ImageAttachmentsMixin, PrimaryModel, WeightMixin
     profile                      ForeignKey                 -> dcim.ModuleTypeProfile
     manufacturer                 ForeignKey             REQ -> dcim.Manufacturer
     model                        CharField              REQ len=100
     part_number                  CharField                  len=50
     airflow                      CharField                  len=50 choices=ModuleAirflowChoices
     attribute_data               JSONField                  
     module_count                 CounterCacheField      REQ 
     console_port_template_count  CounterCacheField      REQ 
     console_server_port_template_count CounterCacheField      REQ 
     power_port_template_count    CounterCacheField      REQ 
     power_outlet_template_count  CounterCacheField      REQ 
     interface_template_count     CounterCacheField      REQ 
     front_port_template_count    CounterCacheField      REQ 
     rear_port_template_count     CounterCacheField      REQ 
     module_bay_template_count    CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'), name='%(app_label)s_%(class)s_unique_manufacturer_model'),)
   meta.ordering: ('profile', 'manufacturer', 'model')
   meta.indexes: (models.Index(fields=('profile', 'manufacturer', 'model')),)

## dcim.ModuleTypeProfile   (dcim/models/modules.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     schema                       JSONField                  
   meta.ordering: ('name',)

## dcim.PathEndpoint   (dcim/models/device_components.py)
   bases: models.Model
     _path                        ForeignKey                 -> dcim.CablePath

## dcim.Platform   (dcim/models/devices.py)
   bases: NestedGroupModel
     manufacturer                 ForeignKey                 -> dcim.Manufacturer
     config_template              ForeignKey                 -> extras.ConfigTemplate
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'name'), name='%(app_label)s_%(class)s_manufacturer_name'), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_name', condition=Q(manufacturer__isnull=True), violation_error_message=_('Platform name must be unique.')), models.UniqueConstraint(fields=('manufacturer', 'slug'), name='%(app_label)s_%(class)s_manufacturer_slug'), mod
   meta.ordering: ('name',)
   meta.indexes: ()

## dcim.PortMapping   (dcim/models/device_components.py)
   bases: ChangeLoggingMixin, PortMappingBase
     device                       ForeignKey             REQ -> dcim.Device
     front_port                   ForeignKey             REQ -> dcim.FrontPort
     rear_port                    ForeignKey             REQ -> dcim.RearPort

## dcim.PortMappingBase   (dcim/models/base.py)
   bases: models.Model
     front_port_position          PositiveSmallIntegerField     def=1
     rear_port_position           PositiveSmallIntegerField     def=1
   meta.constraints: (models.UniqueConstraint(fields=('front_port', 'front_port_position'), name='%(app_label)s_%(class)s_unique_front_port_position'), models.UniqueConstraint(fields=('rear_port', 'rear_port_position'), name='%(app_label)s_%(class)s_unique_rear_port_position'))

## dcim.PortTemplateMapping   (dcim/models/device_component_templates.py)
   bases: ChangeLoggingMixin, PortMappingBase
     device_type                  ForeignKey                 -> dcim.DeviceType
     module_type                  ForeignKey                 -> dcim.ModuleType
     front_port                   ForeignKey             REQ -> dcim.FrontPortTemplate
     rear_port                    ForeignKey             REQ -> dcim.RearPortTemplate

## dcim.PowerFeed   (dcim/models/power.py)
   bases: PrimaryModel, PathEndpoint, CabledObjectModel
     power_panel                  ForeignKey             REQ -> PowerPanel
     rack                         ForeignKey                 -> Rack
     name                         CharField              REQ len=100
     status                       CharField                  len=50 def='PowerFeedStatusChoices.STATUS_ACTIVE' choices=PowerFeedStatusChoices
     type                         CharField                  len=50 def='PowerFeedTypeChoices.TYPE_PRIMARY' choices=PowerFeedTypeChoices
     supply                       CharField                  len=50 def='PowerFeedSupplyChoices.SUPPLY_AC' choices=PowerFeedSupplyChoices
     phase                        CharField                  len=50 def='PowerFeedPhaseChoices.PHASE_SINGLE' choices=PowerFeedPhaseChoices
     voltage                      SmallIntegerField          def="ConfigItem('POWERFEED_DEFAULT_VOLTAGE')"
     amperage                     PositiveSmallIntegerField     def="ConfigItem('POWERFEED_DEFAULT_AMPERAGE')"
     max_utilization              PositiveSmallIntegerField     def="ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')"
     available_power              PositiveIntegerField       def=0
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.constraints: (models.UniqueConstraint(fields=('power_panel', 'name'), name='%(app_label)s_%(class)s_unique_power_panel_name'),)
   meta.ordering: ['power_panel', 'name']

## dcim.PowerOutlet   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     status                       CharField                  len=50 def='PowerOutletStatusChoices.STATUS_ENABLED' choices=PowerOutletStatusChoices
     type                         CharField                  len=50 choices=PowerOutletTypeChoices
     power_port                   ForeignKey                 -> dcim.PowerPort
     feed_leg                     CharField                  len=50 choices=PowerOutletFeedLegChoices
     color                        ColorField                 

## dcim.PowerOutletTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField                  len=50 choices=PowerOutletTypeChoices
     color                        ColorField                 
     power_port                   ForeignKey                 -> dcim.PowerPortTemplate
     feed_leg                     CharField                  len=50 choices=PowerOutletFeedLegChoices

## dcim.PowerPanel   (dcim/models/power.py)
   bases: ContactsMixin, ImageAttachmentsMixin, PrimaryModel
     site                         ForeignKey             REQ -> Site
     location                     ForeignKey                 -> dcim.Location
     name                         CharField              REQ len=100
   meta.constraints: (models.UniqueConstraint(fields=('site', 'name'), name='%(app_label)s_%(class)s_unique_site_name'),)
   meta.ordering: ['site', 'name']

## dcim.PowerPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, PathEndpoint, TrackingModelMixin
     type                         CharField                  len=50 choices=PowerPortTypeChoices
     maximum_draw                 PositiveIntegerField       
     allocated_draw               PositiveIntegerField       

## dcim.PowerPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField                  len=50 choices=PowerPortTypeChoices
     maximum_draw                 PositiveIntegerField       
     allocated_draw               PositiveIntegerField       

## dcim.Rack   (dcim/models/racks.py)
   bases: ContactsMixin, ImageAttachmentsMixin, TrackingModelMixin, RackBase
     form_factor                  CharField                  len=50 choices=RackFormFactorChoices
     rack_type                    ForeignKey                 -> dcim.RackType
     name                         CharField              REQ len=100
     facility_id                  CharField                  len=50
     site                         ForeignKey             REQ -> dcim.Site
     location                     ForeignKey                 -> dcim.Location
     group                        ForeignKey                 -> dcim.RackGroup
     tenant                       ForeignKey                 -> tenancy.Tenant
     status                       CharField                  len=50 def='RackStatusChoices.STATUS_ACTIVE' choices=RackStatusChoices
     role                         ForeignKey                 -> dcim.RackRole
     serial                       CharField                  len=50
     asset_tag                    CharField                  UNIQUE len=50
     airflow                      CharField                  len=50 choices=RackAirflowChoices
     vlan_groups                  GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('location', 'name'), name='%(app_label)s_%(class)s_unique_location_name'), models.UniqueConstraint(fields=('location', 'facility_id'), name='%(app_label)s_%(class)s_unique_location_facility_id'))
   meta.ordering: ('site', 'location', 'name', 'pk')
   meta.indexes: (models.Index(fields=('site', 'location', 'name', 'id')),)

## dcim.RackBase   (dcim/models/racks.py)
   bases: WeightMixin, PrimaryModel
     width                        PositiveSmallIntegerField     def='RackWidthChoices.WIDTH_19IN' choices=RackWidthChoices
     u_height                     PositiveSmallIntegerField     def='RACK_U_HEIGHT_DEFAULT'
     starting_unit                PositiveSmallIntegerField     def='RACK_STARTING_UNIT_DEFAULT'
     desc_units                   BooleanField               def=False
     outer_width                  PositiveSmallIntegerField     
     outer_height                 PositiveSmallIntegerField     
     outer_depth                  PositiveSmallIntegerField     
     outer_unit                   CharField                  len=50 choices=RackDimensionUnitChoices
     mounting_depth               PositiveSmallIntegerField     
     max_weight                   PositiveIntegerField       
     _abs_max_weight              PositiveBigIntegerField     

## dcim.RackReservation   (dcim/models/racks.py)
   bases: PrimaryModel
     rack                         ForeignKey             REQ -> dcim.Rack
     units                        ArrayField             REQ 
     status                       CharField                  len=50 def='RackReservationStatusChoices.STATUS_ACTIVE' choices=RackReservationStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     user                         ForeignKey             REQ -> settings.AUTH_USER_MODEL
     description                  CharField              REQ len=200
   meta.ordering: ['created', 'pk']
   meta.indexes: (models.Index(fields=('created', 'id')),)

## dcim.RackRole   (dcim/models/racks.py)
   bases: OrganizationalModel
     color                        ColorField                 def='ColorChoices.COLOR_GREY'
   meta.ordering: ('name',)

## dcim.RackType   (dcim/models/racks.py)
   bases: ImageAttachmentsMixin, RackBase
     form_factor                  CharField              REQ len=50 choices=RackFormFactorChoices
     manufacturer                 ForeignKey             REQ -> dcim.Manufacturer
     model                        CharField              REQ len=100
     slug                         SlugField              REQ UNIQUE len=100
     rack_count                   CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('manufacturer', 'model'), name='%(app_label)s_%(class)s_unique_manufacturer_model'), models.UniqueConstraint(fields=('manufacturer', 'slug'), name='%(app_label)s_%(class)s_unique_manufacturer_slug'))
   meta.ordering: ('manufacturer', 'model')

## dcim.RearPort   (dcim/models/device_components.py)
   bases: ModularComponentModel, CabledObjectModel, TrackingModelMixin
     type                         CharField              REQ len=50 choices=PortTypeChoices
     color                        ColorField                 
     positions                    PositiveSmallIntegerField     def=1

## dcim.RearPortTemplate   (dcim/models/device_component_templates.py)
   bases: ModularComponentTemplateModel
     type                         CharField              REQ len=50 choices=PortTypeChoices
     color                        ColorField                 
     positions                    PositiveSmallIntegerField     def=1

## dcim.Region   (dcim/models/sites.py)
   bases: ContactsMixin, NestedGroupModel
     prefixes                     GenericRelation        REQ 
     vlan_groups                  GenericRelation        REQ 
     clusters                     GenericRelation        REQ 
     wireless_lans                GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'), name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A top-level region with this name already exists.')), models.UniqueConstraint(fields=('parent', 'slug'), name='%(app_label)s_%(class)s_parent_slug'), models.Unique
   meta.indexes: ()

## dcim.RenderConfigMixin   (dcim/models/mixins.py)
   bases: models.Model
     config_template              ForeignKey                 -> extras.ConfigTemplate

## dcim.Site   (dcim/models/sites.py)
   bases: ContactsMixin, ImageAttachmentsMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
     status                       CharField                  len=50 def='SiteStatusChoices.STATUS_ACTIVE' choices=SiteStatusChoices
     region                       ForeignKey                 -> dcim.Region
     group                        ForeignKey                 -> dcim.SiteGroup
     tenant                       ForeignKey                 -> tenancy.Tenant
     facility                     CharField                  len=50
     asns                         ManyToManyField            -> ipam.ASN
     physical_address             CharField                  len=200
     shipping_address             CharField                  len=200
     latitude                     DecimalField               
     longitude                    DecimalField               
     prefixes                     GenericRelation        REQ 
     vlan_groups                  GenericRelation        REQ 
   meta.ordering: ('name',)

## dcim.SiteGroup   (dcim/models/sites.py)
   bases: ContactsMixin, NestedGroupModel
     prefixes                     GenericRelation        REQ 
     vlan_groups                  GenericRelation        REQ 
     clusters                     GenericRelation        REQ 
     wireless_lans                GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'), name='%(app_label)s_%(class)s_parent_name'), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_name', condition=Q(parent__isnull=True), violation_error_message=_('A top-level site group with this name already exists.')), models.UniqueConstraint(fields=('parent', 'slug'), name='%(app_label)s_%(class)s_parent_slug'), models.Un
   meta.indexes: ()

## dcim.VirtualChassis   (dcim/models/devices.py)
   bases: PrimaryModel
     master                       OneToOneField              -> Device
     name                         CharField              REQ len=64
     domain                       CharField                  len=30
     member_count                 CounterCacheField      REQ 
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## dcim.VirtualDeviceContext   (dcim/models/devices.py)
   bases: PrimaryModel
     device                       ForeignKey                 -> Device
     name                         CharField              REQ len=64
     status                       CharField              REQ len=50 choices=VirtualDeviceContextStatusChoices
     identifier                   PositiveSmallIntegerField     
     primary_ip4                  OneToOneField              -> ipam.IPAddress
     primary_ip6                  OneToOneField              -> ipam.IPAddress
     tenant                       ForeignKey                 -> tenancy.Tenant
     comments                     TextField                  
   meta.constraints: (models.UniqueConstraint(fields=('device', 'identifier'), name='%(app_label)s_%(class)s_device_identifier'), models.UniqueConstraint(fields=('device', 'name'), name='%(app_label)s_%(class)s_device_name'))
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## extras.Bookmark   (extras/models/models.py)
   bases: models.Model
     created                      DateTimeField          REQ 
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       GenericForeignKey      REQ 
     user                         ForeignKey             REQ -> settings.AUTH_USER_MODEL
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'), name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('created', 'pk')
   meta.indexes: (models.Index(fields=('created', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.CachedValue   (extras/models/search.py)
   bases: models.Model
     id                           UUIDField                  def='uuid.uuid4'
     timestamp                    DateTimeField          REQ 
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       RestrictedGenericForeignKey REQ 
     field                        CharField              REQ len=200
     type                         CharField              REQ len=30
     value                        CachedValueField       REQ 
     weight                       PositiveSmallIntegerField     def=1000
   meta.ordering: ('weight', 'object_type', 'value', 'object_id')
   meta.indexes: (models.Index(fields=('object_type', 'object_id'), name='extras_cachedvalue_object'),)

## extras.ConfigContext   (extras/models/configs.py)
   bases: SyncedDataMixin, CloningMixin, CustomLinksMixin, OwnerMixin, ChangeLoggedModel
     name                         CharField              REQ UNIQUE len=100
     profile                      ForeignKey                 -> extras.ConfigContextProfile
     weight                       PositiveSmallIntegerField     def=1000
     description                  CharField                  len=200
     is_active                    BooleanField               def=True
     regions                      ManyToManyField            -> dcim.Region
     site_groups                  ManyToManyField            -> dcim.SiteGroup
     sites                        ManyToManyField            -> dcim.Site
     locations                    ManyToManyField            -> dcim.Location
     device_types                 ManyToManyField            -> dcim.DeviceType
     roles                        ManyToManyField            -> dcim.DeviceRole
     platforms                    ManyToManyField            -> dcim.Platform
     cluster_types                ManyToManyField            -> virtualization.ClusterType
     cluster_groups               ManyToManyField            -> virtualization.ClusterGroup
     clusters                     ManyToManyField            -> virtualization.Cluster
     tenant_groups                ManyToManyField            -> tenancy.TenantGroup
     tenants                      ManyToManyField            -> tenancy.Tenant
     tags                         ManyToManyField            -> extras.Tag
     data                         JSONField              REQ 
   meta.ordering: ['weight', 'name']
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.ConfigContextModel   (extras/models/configs.py)
   bases: models.Model
     local_context_data           JSONField                  

## extras.ConfigContextProfile   (extras/models/configs.py)
   bases: SyncedDataMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     description                  CharField                  len=200
     schema                       JSONField                  
   meta.ordering: ('name',)

## extras.ConfigTemplate   (extras/models/configs.py)
   bases: RenderTemplateMixin, SyncedDataMixin, CustomLinksMixin, ExportTemplatesMixin, OwnerMixin, TagsMixin, ChangeLoggedModel
     name                         CharField              REQ len=100
     description                  CharField                  len=200
     debug                        BooleanField               def=False
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## extras.CustomField   (extras/models/customfields.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     type                         CharField                  len=50 def='CustomFieldTypeChoices.TYPE_TEXT' choices=CustomFieldTypeChoices
     related_object_type          ForeignKey                 -> contenttypes.ContentType
     name                         CharField              REQ UNIQUE len=50
     label                        CharField                  len=50
     group_name                   CharField                  len=50
     description                  CharField                  len=200
     required                     BooleanField               def=False
     unique                       BooleanField               def=False
     search_weight                PositiveSmallIntegerField     def=1000
     filter_logic                 CharField                  len=50 def='CustomFieldFilterLogicChoices.FILTER_LOOSE' choices=CustomFieldFilterLogicChoices
     default                      JSONField                  
     related_object_filter        JSONField                  
     weight                       PositiveSmallIntegerField     def=100
     validation_minimum           DecimalField               
     validation_maximum           DecimalField               
     validation_regex             CharField                  len=500
     validation_schema            JSONField                  
     choice_set                   ForeignKey                 -> CustomFieldChoiceSet
     ui_visible                   CharField                  len=50 def='CustomFieldUIVisibleChoices.ALWAYS' choices=CustomFieldUIVisibleChoices
     ui_editable                  CharField                  len=50 def='CustomFieldUIEditableChoices.YES' choices=CustomFieldUIEditableChoices
     is_cloneable                 BooleanField               def=False
     comments                     TextField                  
   meta.ordering: ['group_name', 'weight', 'name']
   meta.indexes: (models.Index(fields=('group_name', 'weight', 'name')),)

## extras.CustomFieldChoiceSet   (extras/models/customfields.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     name                         CharField              REQ UNIQUE len=100
     description                  CharField                  len=200
     base_choices                 CharField                  len=50 choices=CustomFieldChoiceSetBaseChoices
     choice_colors                JSONField                  def='dict'
     order_alphabetically         BooleanField               def=False
   meta.ordering: ('name',)

## extras.CustomLink   (extras/models/models.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     name                         CharField              REQ UNIQUE len=100
     enabled                      BooleanField               def=True
     link_text                    TextField              REQ 
     link_url                     TextField              REQ 
     weight                       PositiveSmallIntegerField     def=100
     group_name                   CharField                  len=50
     button_class                 CharField                  len=30 def='CustomLinkButtonClassChoices.DEFAULT' choices=CustomLinkButtonClassChoices
     new_window                   BooleanField               def=False
   meta.ordering: ['group_name', 'weight', 'name']
   meta.indexes: (models.Index(fields=('group_name', 'weight', 'name')),)

## extras.Dashboard   (extras/models/dashboard.py)
   bases: models.Model
     user                         OneToOneField          REQ -> users.User
     layout                       JSONField                  def='list'
     config                       JSONField                  def='dict'

## extras.EventRule   (extras/models/models.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, OwnerMixin, TagsMixin, ChangeLoggedModel
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     name                         CharField              REQ UNIQUE len=150
     description                  CharField                  len=200
     event_types                  ArrayField             REQ 
     enabled                      BooleanField               def=True
     conditions                   JSONField                  
     action_type                  CharField                  len=30 def='EventRuleActionChoices.WEBHOOK' choices=EventRuleActionChoices
     action_object_type           ForeignKey             REQ -> contenttypes.ContentType
     action_object_id             PositiveBigIntegerField     
     action_object                GenericForeignKey      REQ 
     action_data                  JSONField                  
     comments                     TextField                  
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('action_object_type', 'action_object_id')),)

## extras.ExportTemplate   (extras/models/models.py)
   bases: SyncedDataMixin, CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, RenderTemplateMixin
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     name                         CharField              REQ len=100
     description                  CharField                  len=200
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## extras.ImageAttachment   (extras/models/models.py)
   bases: ChangeLoggedModel
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     parent                       GenericForeignKey      REQ 
     image_height                 PositiveSmallIntegerField REQ 
     image_width                  PositiveSmallIntegerField REQ 
     image_size                   PositiveBigIntegerField     
     name                         CharField                  len=50
     description                  CharField                  len=200
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.JournalEntry   (extras/models/models.py)
   bases: CustomFieldsMixin, CustomLinksMixin, TagsMixin, ExportTemplatesMixin, ChangeLoggedModel
     assigned_object_type         ForeignKey             REQ -> contenttypes.ContentType
     assigned_object_id           PositiveBigIntegerField REQ 
     assigned_object              GenericForeignKey      REQ 
     created_by                   ForeignKey                 -> settings.AUTH_USER_MODEL
     kind                         CharField                  len=30 def='JournalEntryKindChoices.KIND_INFO' choices=JournalEntryKindChoices
     comments                     TextField              REQ 
   meta.ordering: ('-created',)
   meta.indexes: (models.Index(fields=('-created',)), models.Index(fields=('assigned_object_type', 'assigned_object_id')))

## extras.Notification   (extras/models/notifications.py)
   bases: models.Model
     created                      DateTimeField          REQ 
     read                         DateTimeField              
     user                         ForeignKey             REQ -> settings.AUTH_USER_MODEL
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       GenericForeignKey      REQ 
     object_repr                  CharField              REQ len=200
     event_type                   CharField              REQ len=50 choices=get_event_type_choices
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'), name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('-created', 'pk')
   meta.indexes: (models.Index(fields=('-created', 'id')), models.Index(fields=('object_type', 'object_id')))

## extras.NotificationGroup   (extras/models/notifications.py)
   bases: ChangeLoggedModel
     name                         CharField              REQ UNIQUE len=100
     description                  CharField                  len=200
     groups                       ManyToManyField            -> users.Group
     users                        ManyToManyField            -> users.User
     event_rules                  GenericRelation        REQ 
   meta.ordering: ('name',)

## extras.RenderTemplateMixin   (extras/models/mixins.py)
   bases: models.Model
     template_code                TextField              REQ 
     environment_params           JSONField                  def='dict'
     mime_type                    CharField                  len=50
     file_name                    CharField                  len=200
     file_extension               CharField                  len=15
     as_attachment                BooleanField               def=True

## extras.SavedFilter   (extras/models/models.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
     description                  CharField                  len=200
     user                         ForeignKey                 -> settings.AUTH_USER_MODEL
     weight                       PositiveSmallIntegerField     def=100
     enabled                      BooleanField               def=True
     shared                       BooleanField               def=True
     parameters                   JSONField              REQ 
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.Script   (extras/models/scripts.py)
   bases: EventRulesMixin, JobsMixin
     name                         CharField              REQ len=79
     module                       ForeignKey             REQ -> extras.ScriptModule
     is_executable                BooleanField               def=True
     events                       GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('name', 'module'), name='extras_script_unique_name_module'),)
   meta.ordering: ('module', 'name')
   meta.indexes: (models.Index(fields=('module', 'name')),)

## extras.ScriptModule   (extras/models/scripts.py)
   bases: PythonModuleMixin, JobsMixin, ManagedFile
     event_rules                  GenericRelation        REQ 
   meta.ordering: ('file_root', 'file_path')

## extras.Subscription   (extras/models/notifications.py)
   bases: models.Model
     created                      DateTimeField          REQ 
     user                         ForeignKey             REQ -> settings.AUTH_USER_MODEL
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       GenericForeignKey      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'user'), name='%(app_label)s_%(class)s_unique_per_object_and_user'),)
   meta.ordering: ('-created', 'user')
   meta.indexes: (models.Index(fields=('-created', 'user')), models.Index(fields=('object_type', 'object_id')))

## extras.TableConfig   (extras/models/models.py)
   bases: CloningMixin, ChangeLoggedModel
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     table                        CharField              REQ len=100
     name                         CharField              REQ len=100
     description                  CharField                  len=200
     user                         ForeignKey                 -> settings.AUTH_USER_MODEL
     weight                       PositiveSmallIntegerField     def=1000
     enabled                      BooleanField               def=True
     shared                       BooleanField               def=True
     columns                      ArrayField             REQ 
     ordering                     ArrayField                 
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.Tag   (extras/models/tags.py)
   bases: CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, TagBase
     color                        ColorField                 def='ColorChoices.COLOR_GREY'
     description                  CharField                  len=200
     object_types                 ManyToManyField            -> contenttypes.ContentType
     weight                       PositiveSmallIntegerField     def=1000
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## extras.TaggedItem   (extras/models/tags.py)
   bases: GenericTaggedItemBase
     tag                          ForeignKey             REQ -> Tag
   meta.indexes: [models.Index(fields=['content_type', 'object_id'])]

## extras.Webhook   (extras/models/models.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, OwnerMixin, ChangeLoggedModel
     name                         CharField              REQ UNIQUE len=150
     description                  CharField                  len=200
     payload_url                  CharField              REQ len=500
     http_method                  CharField                  len=30 def='WebhookHttpMethodChoices.METHOD_POST' choices=WebhookHttpMethodChoices
     http_content_type            CharField                  len=100 def='HTTP_CONTENT_TYPE_JSON'
     additional_headers           TextField                  
     body_template                TextField                  
     secret                       CharField                  len=255
     ssl_verification             BooleanField               def=True
     ca_file_path                 CharField                  len=4096
     events                       GenericRelation        REQ 
   meta.ordering: ('name',)

## ipam.ASN   (ipam/models/asns.py)
   bases: ContactsMixin, PrimaryModel
     rir                          ForeignKey             REQ -> ipam.RIR
     asn                          ASNField               REQ UNIQUE
     role                         ForeignKey                 -> ipam.Role
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ['asn']

## ipam.ASNRange   (ipam/models/asns.py)
   bases: OrganizationalModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
     rir                          ForeignKey             REQ -> ipam.RIR
     start                        ASNField               REQ 
     end                          ASNField               REQ 
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ('name',)

## ipam.Aggregate   (ipam/models/ip.py)
   bases: ContactsMixin, GetAvailablePrefixesMixin, PrimaryModel
     prefix                       IPNetworkField         REQ 
     rir                          ForeignKey             REQ -> ipam.RIR
     tenant                       ForeignKey                 -> tenancy.Tenant
     date_added                   DateField                  
   meta.ordering: ('prefix', 'pk')
   meta.indexes: (models.Index(fields=('prefix', 'id')),)

## ipam.FHRPGroup   (ipam/models/fhrp.py)
   bases: PrimaryModel
     group_id                     PositiveSmallIntegerField REQ 
     name                         CharField                  len=100
     protocol                     CharField              REQ len=50 choices=FHRPGroupProtocolChoices
     auth_type                    CharField                  len=50 choices=FHRPGroupAuthTypeChoices
     auth_key                     CharField                  len=255
     ip_addresses                 GenericRelation        REQ 
     services                     GenericRelation        REQ 
   meta.ordering: ['protocol', 'group_id', 'pk']
   meta.indexes: (models.Index(fields=('protocol', 'group_id', 'id')),)

## ipam.FHRPGroupAssignment   (ipam/models/fhrp.py)
   bases: ChangeLoggedModel
     interface_type               ForeignKey             REQ -> contenttypes.ContentType
     interface_id                 PositiveBigIntegerField REQ 
     interface                    GenericForeignKey      REQ 
     group                        ForeignKey             REQ -> ipam.FHRPGroup
     priority                     PositiveSmallIntegerField REQ 
   meta.constraints: (models.UniqueConstraint(fields=('interface_type', 'interface_id', 'group'), name='%(app_label)s_%(class)s_unique_interface_group'),)
   meta.ordering: ('-priority', 'pk')
   meta.indexes: (models.Index(fields=('-priority', 'id')), models.Index(fields=('interface_type', 'interface_id')))

## ipam.IPAddress   (ipam/models/ip.py)
   bases: ContactsMixin, PrimaryModel
     address                      IPAddressField         REQ 
     vrf                          ForeignKey                 -> ipam.VRF
     tenant                       ForeignKey                 -> tenancy.Tenant
     status                       CharField                  len=50 def='IPAddressStatusChoices.STATUS_ACTIVE' choices=IPAddressStatusChoices
     role                         CharField                  len=50 choices=IPAddressRoleChoices
     assigned_object_type         ForeignKey                 -> contenttypes.ContentType
     assigned_object_id           PositiveBigIntegerField     
     assigned_object              GenericForeignKey      REQ 
     nat_inside                   ForeignKey                 -> self
     dns_name                     CharField                  len=255
   meta.ordering: ('address', 'pk')
   meta.indexes: (models.Index(fields=('address', 'id')), models.Index(Cast(Host('address'), output_field=IPAddressField()), F('id'), name='ipam_ipaddress_host'), models.Index(fields=('assigned_object_type', 'assigned_object_id')))

## ipam.IPRange   (ipam/models/ip.py)
   bases: ContactsMixin, PrimaryModel
     start_address                IPAddressField         REQ 
     end_address                  IPAddressField         REQ 
     size                         PositiveIntegerField   REQ 
     vrf                          ForeignKey                 -> ipam.VRF
     tenant                       ForeignKey                 -> tenancy.Tenant
     status                       CharField                  len=50 def='IPRangeStatusChoices.STATUS_ACTIVE' choices=IPRangeStatusChoices
     role                         ForeignKey                 -> ipam.Role
     mark_populated               BooleanField               def=False
     mark_utilized                BooleanField               def=False
   meta.ordering: (F('vrf').asc(nulls_first=True), 'start_address', 'pk')
   meta.indexes: (models.Index(Cast(Host('start_address'), output_field=IPAddressField()), name='ipam_iprange_start_host'), models.Index(Cast(Host('end_address'), output_field=IPAddressField()), name='ipam_iprange_end_host'))

## ipam.Prefix   (ipam/models/ip.py)
   bases: ContactsMixin, GetAvailablePrefixesMixin, CachedScopeMixin, PrimaryModel
     prefix                       IPNetworkField         REQ 
     vrf                          ForeignKey                 -> ipam.VRF
     tenant                       ForeignKey                 -> tenancy.Tenant
     vlan                         ForeignKey                 -> ipam.VLAN
     status                       CharField                  len=50 def='PrefixStatusChoices.STATUS_ACTIVE' choices=PrefixStatusChoices
     role                         ForeignKey                 -> ipam.Role
     is_pool                      BooleanField               def=False
     mark_utilized                BooleanField               def=False
     _depth                       PositiveSmallIntegerField     def=0
     _children                    PositiveBigIntegerField     def=0
   meta.ordering: (F('vrf').asc(nulls_first=True), 'prefix', 'pk')
   meta.indexes: (models.Index(fields=('scope_type', 'scope_id')), GistIndex(fields=['prefix'], name='ipam_prefix_gist_idx', opclasses=['inet_ops']))

## ipam.RIR   (ipam/models/ip.py)
   bases: OrganizationalModel
     is_private                   BooleanField               def=False
   meta.ordering: ('name',)

## ipam.Role   (ipam/models/ip.py)
   bases: OrganizationalModel
     weight                       PositiveSmallIntegerField     def=1000
   meta.ordering: ('weight', 'name')
   meta.indexes: (models.Index(fields=('weight', 'name')),)

## ipam.RouteTarget   (ipam/models/vrfs.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=VRF_RD_MAX_LENGTH
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ['name']

## ipam.Service   (ipam/models/services.py)
   bases: ContactsMixin, ServiceBase, PrimaryModel
     parent_object_type           ForeignKey             REQ -> contenttypes.ContentType
     parent_object_id             PositiveBigIntegerField REQ 
     parent                       GenericForeignKey      REQ 
     name                         CharField              REQ len=100
     ipaddresses                  ManyToManyField            -> ipam.IPAddress
   meta.ordering: ('protocol', '_ports_lowest', 'id')
   meta.indexes: (models.Index(fields=('protocol', '_ports_lowest', 'id')), models.Index(fields=('parent_object_type', 'parent_object_id')))

## ipam.ServiceBase   (ipam/models/services.py)
   bases: models.Model
     protocol                     CharField              REQ len=50 choices=ServiceProtocolChoices
     ports                        ArrayField             REQ 
     _ports_lowest                PositiveIntegerField       

## ipam.ServiceTemplate   (ipam/models/services.py)
   bases: ServiceBase, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
   meta.ordering: ('name',)

## ipam.VLAN   (ipam/models/vlans.py)
   bases: PrimaryModel
     site                         ForeignKey                 -> dcim.Site
     group                        ForeignKey                 -> ipam.VLANGroup
     vid                          PositiveSmallIntegerField REQ 
     name                         CharField              REQ len=64
     tenant                       ForeignKey                 -> tenancy.Tenant
     status                       CharField                  len=50 def='VLANStatusChoices.STATUS_ACTIVE' choices=VLANStatusChoices
     role                         ForeignKey                 -> ipam.Role
     qinq_svlan                   ForeignKey                 -> self
     qinq_role                    CharField                  len=50 choices=VLANQinQRoleChoices
     l2vpn_terminations           GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('group', 'vid'), name='%(app_label)s_%(class)s_unique_group_vid'), models.UniqueConstraint(fields=('group', 'name'), name='%(app_label)s_%(class)s_unique_group_name'), models.UniqueConstraint(fields=('qinq_svlan', 'vid'), name='%(app_label)s_%(class)s_unique_qinq_svlan_vid'), models.UniqueConstraint(fields=('qinq_svlan', 'name'), name='%(app_label)s_%(class)s_uniqu
   meta.ordering: ('site', 'group', 'vid', 'pk')
   meta.indexes: (models.Index(fields=('site', 'group', 'vid', 'id')),)

## ipam.VLANGroup   (ipam/models/vlans.py)
   bases: OrganizationalModel
     name                         CharField              REQ len=100
     slug                         SlugField              REQ len=100
     scope_type                   ForeignKey                 -> contenttypes.ContentType
     scope_id                     PositiveBigIntegerField     
     scope                        GenericForeignKey      REQ 
     vid_ranges                   ArrayField                 def='default_vid_ranges'
     total_vlan_ids               PositiveBigIntegerField     def='VLAN_VID_MAX - VLAN_VID_MIN + 1'
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.constraints: (models.UniqueConstraint(fields=('scope_type', 'scope_id', 'name'), name='%(app_label)s_%(class)s_unique_scope_name'), models.UniqueConstraint(fields=('scope_type', 'scope_id', 'slug'), name='%(app_label)s_%(class)s_unique_scope_slug'))
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')), models.Index(fields=('scope_type', 'scope_id')))

## ipam.VLANTranslationPolicy   (ipam/models/vlans.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
   meta.ordering: ('name',)

## ipam.VLANTranslationRule   (ipam/models/vlans.py)
   bases: NetBoxModel
     policy                       ForeignKey             REQ -> VLANTranslationPolicy
     description                  CharField                  len=200
     local_vid                    PositiveSmallIntegerField REQ 
     remote_vid                   PositiveSmallIntegerField REQ 
   meta.constraints: (models.UniqueConstraint(fields=('policy', 'local_vid'), name='%(app_label)s_%(class)s_unique_policy_local_vid'), models.UniqueConstraint(fields=('policy', 'remote_vid'), name='%(app_label)s_%(class)s_unique_policy_remote_vid'))
   meta.ordering: ('policy', 'local_vid')

## ipam.VRF   (ipam/models/vrfs.py)
   bases: PrimaryModel
     name                         CharField              REQ len=100
     rd                           CharField                  UNIQUE len=VRF_RD_MAX_LENGTH
     tenant                       ForeignKey                 -> tenancy.Tenant
     enforce_unique               BooleanField               def=True
     import_targets               ManyToManyField            -> ipam.RouteTarget
     export_targets               ManyToManyField            -> ipam.RouteTarget
   meta.ordering: ('name', 'rd', 'pk')
   meta.indexes: (models.Index(fields=('name', 'rd', 'id')),)

## tenancy.Contact   (tenancy/models/contacts.py)
   bases: PrimaryModel
     groups                       ManyToManyField            -> tenancy.ContactGroup
     name                         CharField              REQ len=100
     title                        CharField                  len=100
     phone                        CharField                  len=50
     email                        EmailField                 
     address                      CharField                  len=200
     link                         URLField                   
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## tenancy.ContactAssignment   (tenancy/models/contacts.py)
   bases: CustomFieldsMixin, ExportTemplatesMixin, TagsMixin, ChangeLoggedModel
     object_type                  ForeignKey             REQ -> contenttypes.ContentType
     object_id                    PositiveBigIntegerField REQ 
     object                       GenericForeignKey      REQ 
     contact                      ForeignKey             REQ -> tenancy.Contact
     role                         ForeignKey             REQ -> tenancy.ContactRole
     priority                     CharField                  len=50 choices=ContactPriorityChoices
   meta.constraints: (models.UniqueConstraint(fields=('object_type', 'object_id', 'contact', 'role'), name='%(app_label)s_%(class)s_unique_object_contact_role'),)
   meta.ordering: ('contact', 'priority', 'role', 'pk')
   meta.indexes: (models.Index(fields=('contact', 'priority', 'role', 'id')), models.Index(fields=('object_type', 'object_id')))

## tenancy.Tenant   (tenancy/models/tenants.py)
   bases: ContactsMixin, PrimaryModel
     name                         CharField              REQ len=100
     slug                         SlugField              REQ len=100
     group                        ForeignKey                 -> tenancy.TenantGroup
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'), name='%(app_label)s_%(class)s_unique_group_name', violation_error_message=_('Tenant name must be unique per group.')), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_unique_name', condition=Q(group__isnull=True)), models.UniqueConstraint(fields=('group', 'slug'), name='%(app_label)s_%(class)s_unique_group_slug', violation_
   meta.ordering: ['name']

## tenancy.TenantGroup   (tenancy/models/tenants.py)
   bases: NestedGroupModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
   meta.ordering: ['name']
   meta.indexes: ()

## users.Group   (users/models/users.py)
   bases: models.Model
     name                         CharField              REQ UNIQUE len=150
     description                  CharField                  len=200
     object_permissions           ManyToManyField            -> users.ObjectPermission
     permissions                  ManyToManyField            -> Permission
   meta.ordering: ('name',)

## users.ObjectPermission   (users/models/permissions.py)
   bases: CloningMixin, models.Model
     name                         CharField              REQ len=100
     description                  CharField                  len=200
     enabled                      BooleanField               def=True
     object_types                 ManyToManyField        REQ -> contenttypes.ContentType
     actions                      ArrayField             REQ 
     constraints                  JSONField                  
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('name',)),)

## users.Owner   (users/models/owners.py)
   bases: AdminModel
     name                         CharField              REQ UNIQUE len=100
     group                        ForeignKey                 -> users.OwnerGroup
     user_groups                  ManyToManyField            -> users.Group
     users                        ManyToManyField            -> users.User
   meta.ordering: ('name',)

## users.OwnerGroup   (users/models/owners.py)
   bases: AdminModel
     name                         CharField              REQ UNIQUE len=100
   meta.ordering: ['name']

## users.Token   (users/models/tokens.py)
   bases: models.Model
     version                      PositiveSmallIntegerField     def='TokenVersionChoices.V2' choices=TokenVersionChoices
     user                         ForeignKey             REQ -> users.User
     description                  CharField                  len=200
     created                      DateTimeField          REQ 
     expires                      DateTimeField              
     last_used                    DateTimeField              
     enabled                      BooleanField               def=True
     write_enabled                BooleanField               def=True
     plaintext                    CharField                  UNIQUE len=40
     key                          CharField                  UNIQUE len=TOKEN_KEY_LENGTH
     pepper_id                    PositiveSmallIntegerField     
     hmac_digest                  CharField                  len=64
     allowed_ips                  ArrayField                 
   meta.constraints: [models.CheckConstraint(name='enforce_version_dependent_fields', condition=Q(version=1, key__isnull=True, pepper_id__isnull=True, hmac_digest__isnull=True, plaintext__isnull=False) | Q(version=2, key__isnull=False, pepper_id__isnull=False, hmac_digest__isnull=False, plaintext__isnull=True))]
   meta.ordering: ('-created',)
   meta.indexes: (models.Index(fields=('-created',)),)

## users.User   (users/models/users.py)
   bases: AbstractBaseUser, PermissionsMixin
     username                     CharField              REQ UNIQUE len=150
     first_name                   CharField                  len=150
     last_name                    CharField                  len=150
     email                        EmailField                 
     is_active                    BooleanField               def=True
     date_joined                  DateTimeField              def='timezone.now'
     groups                       ManyToManyField            -> users.Group
     object_permissions           ManyToManyField            -> users.ObjectPermission
   meta.ordering: ('username',)

## users.UserConfig   (users/models/preferences.py)
   bases: models.Model
     user                         OneToOneField          REQ -> users.User
     data                         JSONField                  def='dict'
   meta.ordering: ['user']

## virtualization.Cluster   (virtualization/models/clusters.py)
   bases: ContactsMixin, CachedScopeMixin, PrimaryModel
     name                         CharField              REQ len=100
     type                         ForeignKey             REQ -> ClusterType
     group                        ForeignKey                 -> ClusterGroup
     status                       CharField                  len=50 def='ClusterStatusChoices.STATUS_ACTIVE' choices=ClusterStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     vlan_groups                  GenericRelation        REQ 
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'), name='%(app_label)s_%(class)s_unique_group_name'), models.UniqueConstraint(fields=('_site', 'name'), name='%(app_label)s_%(class)s_unique__site_name'))
   meta.ordering: ['name']
   meta.indexes: (models.Index(fields=('scope_type', 'scope_id')),)

## virtualization.ClusterGroup   (virtualization/models/clusters.py)
   bases: ContactsMixin, OrganizationalModel
     vlan_groups                  GenericRelation        REQ 
   meta.ordering: ('name',)

## virtualization.ComponentModel   (virtualization/models/virtualmachines.py)
   bases: OwnerMixin, NetBoxModel
     virtual_machine              ForeignKey             REQ -> virtualization.VirtualMachine
     name                         CharField              REQ len=64
     description                  CharField                  len=200
   meta.constraints: (models.UniqueConstraint(fields=('virtual_machine', 'name'), name='%(app_label)s_%(class)s_unique_virtual_machine_name'),)

## virtualization.VMInterface   (virtualization/models/virtualmachines.py)
   bases: ComponentModel, BaseInterface, TrackingModelMixin
     name                         CharField              REQ len=64
     _name                        NaturalOrderingField       len=100
     virtual_machine              ForeignKey             REQ -> virtualization.VirtualMachine
     ip_addresses                 GenericRelation        REQ 
     vrf                          ForeignKey                 -> ipam.VRF
     fhrp_group_assignments       GenericRelation        REQ 
     tunnel_terminations          GenericRelation        REQ 
     l2vpn_terminations           GenericRelation        REQ 
     mac_addresses                GenericRelation        REQ 
   meta.ordering: ('virtual_machine', CollateAsChar('_name'))

## virtualization.VirtualDisk   (virtualization/models/virtualmachines.py)
   bases: ComponentModel, TrackingModelMixin
     size                         PositiveIntegerField   REQ 
   meta.ordering: ('virtual_machine', 'name')

## virtualization.VirtualMachine   (virtualization/models/virtualmachines.py)
   bases: ContactsMixin, ImageAttachmentsMixin, RenderConfigMixin, ConfigContextModel, TrackingModelMixin, PrimaryModel
     virtual_machine_type         ForeignKey                 -> virtualization.VirtualMachineType
     site                         ForeignKey                 -> dcim.Site
     cluster                      ForeignKey                 -> virtualization.Cluster
     device                       ForeignKey                 -> dcim.Device
     tenant                       ForeignKey                 -> tenancy.Tenant
     platform                     ForeignKey                 -> dcim.Platform
     name                         CharField              REQ len=64
     status                       CharField                  len=50 def='VirtualMachineStatusChoices.STATUS_ACTIVE' choices=VirtualMachineStatusChoices
     start_on_boot                CharField                  len=32 def='VirtualMachineStartOnBootChoices.STATUS_OFF' choices=VirtualMachineStartOnBootChoices
     role                         ForeignKey                 -> dcim.DeviceRole
     primary_ip4                  OneToOneField              -> ipam.IPAddress
     primary_ip6                  OneToOneField              -> ipam.IPAddress
     vcpus                        DecimalField               
     memory                       PositiveIntegerField       
     disk                         PositiveIntegerField       
     serial                       CharField                  len=50
     services                     GenericRelation        REQ 
     interface_count              CounterCacheField      REQ 
     virtual_disk_count           CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(Lower('name'), 'cluster', 'tenant', name='%(app_label)s_%(class)s_unique_name_cluster_tenant', violation_error_message=_('Virtual machine name must be unique per cluster and tenant.')), models.UniqueConstraint(Lower('name'), 'cluster', name='%(app_label)s_%(class)s_unique_name_cluster', condition=Q(tenant__isnull=True), violation_error_message=_('Virtual machine name must 
   meta.ordering: ('name', 'pk')
   meta.indexes: (models.Index(fields=('name', 'id')),)

## virtualization.VirtualMachineType   (virtualization/models/virtualmachines.py)
   bases: ImageAttachmentsMixin, PrimaryModel
     name                         CharField              REQ len=100
     slug                         SlugField              REQ UNIQUE len=100
     default_platform             ForeignKey                 -> dcim.Platform
     default_vcpus                DecimalField               
     default_memory               PositiveIntegerField       
     virtual_machine_count        CounterCacheField      REQ 
   meta.constraints: (models.UniqueConstraint(Lower('name'), name='%(app_label)s_%(class)s_unique_name', violation_error_message=_('Virtual machine type name must be unique.')),)
   meta.ordering: ('name',)
   meta.indexes: (models.Index(fields=('name',)),)

## vpn.IKEPolicy   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     version                      PositiveSmallIntegerField     def='IKEVersionChoices.VERSION_2' choices=IKEVersionChoices
     mode                         CharField                  choices=IKEModeChoices
     proposals                    ManyToManyField        REQ -> vpn.IKEProposal
     preshared_key                TextField                  
   meta.ordering: ('name',)

## vpn.IKEProposal   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     authentication_method        CharField              REQ choices=AuthenticationMethodChoices
     encryption_algorithm         CharField              REQ choices=EncryptionAlgorithmChoices
     authentication_algorithm     CharField                  choices=AuthenticationAlgorithmChoices
     group                        PositiveSmallIntegerField REQ choices=DHGroupChoices
     sa_lifetime                  PositiveIntegerField       
   meta.ordering: ('name',)

## vpn.IPSecPolicy   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     proposals                    ManyToManyField        REQ -> vpn.IPSecProposal
     pfs_group                    PositiveSmallIntegerField     choices=DHGroupChoices
   meta.ordering: ('name',)

## vpn.IPSecProfile   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     mode                         CharField              REQ choices=IPSecModeChoices
     ike_policy                   ForeignKey             REQ -> vpn.IKEPolicy
     ipsec_policy                 ForeignKey             REQ -> vpn.IPSecPolicy
   meta.ordering: ('name',)

## vpn.IPSecProposal   (vpn/models/crypto.py)
   bases: PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     encryption_algorithm         CharField                  choices=EncryptionAlgorithmChoices
     authentication_algorithm     CharField                  choices=AuthenticationAlgorithmChoices
     sa_lifetime_seconds          PositiveIntegerField       
     sa_lifetime_data             PositiveIntegerField       
   meta.ordering: ('name',)

## vpn.L2VPN   (vpn/models/l2vpn.py)
   bases: ContactsMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
     type                         CharField              REQ len=50 choices=L2VPNTypeChoices
     status                       CharField                  len=50 def='L2VPNStatusChoices.STATUS_ACTIVE' choices=L2VPNStatusChoices
     identifier                   BigIntegerField            
     import_targets               ManyToManyField            -> ipam.RouteTarget
     export_targets               ManyToManyField            -> ipam.RouteTarget
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ('name', 'identifier')

## vpn.L2VPNTermination   (vpn/models/l2vpn.py)
   bases: NetBoxModel
     l2vpn                        ForeignKey             REQ -> vpn.L2VPN
     assigned_object_type         ForeignKey             REQ -> contenttypes.ContentType
     assigned_object_id           PositiveBigIntegerField REQ 
     assigned_object              GenericForeignKey      REQ 
   meta.constraints: (models.UniqueConstraint(fields=('assigned_object_type', 'assigned_object_id'), name='vpn_l2vpntermination_assigned_object'),)
   meta.ordering: ('l2vpn',)

## vpn.Tunnel   (vpn/models/tunnels.py)
   bases: ContactsMixin, PrimaryModel
     name                         CharField              REQ UNIQUE len=100
     status                       CharField                  len=50 def='TunnelStatusChoices.STATUS_ACTIVE' choices=TunnelStatusChoices
     group                        ForeignKey                 -> vpn.TunnelGroup
     encapsulation                CharField              REQ len=50 choices=TunnelEncapsulationChoices
     ipsec_profile                ForeignKey                 -> vpn.IPSecProfile
     tenant                       ForeignKey                 -> tenancy.Tenant
     tunnel_id                    PositiveBigIntegerField     
   meta.constraints: (models.UniqueConstraint(fields=('group', 'name'), name='%(app_label)s_%(class)s_group_name'), models.UniqueConstraint(fields=('name',), name='%(app_label)s_%(class)s_name', condition=Q(group__isnull=True)))
   meta.ordering: ('name',)

## vpn.TunnelTermination   (vpn/models/tunnels.py)
   bases: CustomFieldsMixin, CustomLinksMixin, TagsMixin, ChangeLoggedModel
     tunnel                       ForeignKey             REQ -> vpn.Tunnel
     role                         CharField                  len=50 def='TunnelTerminationRoleChoices.ROLE_PEER' choices=TunnelTerminationRoleChoices
     termination_type             ForeignKey             REQ -> contenttypes.ContentType
     termination_id               PositiveBigIntegerField     
     termination                  GenericForeignKey      REQ 
     outside_ip                   ForeignKey                 -> ipam.IPAddress
   meta.constraints: (models.UniqueConstraint(fields=('termination_type', 'termination_id'), name='%(app_label)s_%(class)s_termination', violation_error_message=_('An object may be terminated to only one tunnel at a time.')),)
   meta.ordering: ('tunnel', 'role', 'pk')
   meta.indexes: (models.Index(fields=('tunnel', 'role', 'id')),)

## wireless.WirelessAuthenticationBase   (wireless/models.py)
   bases: models.Model
     auth_type                    CharField                  len=50 choices=WirelessAuthTypeChoices
     auth_cipher                  CharField                  len=50 choices=WirelessAuthCipherChoices
     auth_psk                     CharField                  len=PSK_MAX_LENGTH

## wireless.WirelessLAN   (wireless/models.py)
   bases: WirelessAuthenticationBase, CachedScopeMixin, PrimaryModel
     ssid                         CharField              REQ len=SSID_MAX_LENGTH
     group                        ForeignKey                 -> wireless.WirelessLANGroup
     status                       CharField                  len=50 def='WirelessLANStatusChoices.STATUS_ACTIVE' choices=WirelessLANStatusChoices
     vlan                         ForeignKey                 -> ipam.VLAN
     tenant                       ForeignKey                 -> tenancy.Tenant
   meta.ordering: ('ssid', 'pk')
   meta.indexes: (models.Index(fields=('ssid', 'id')), models.Index(fields=('scope_type', 'scope_id')))

## wireless.WirelessLANGroup   (wireless/models.py)
   bases: NestedGroupModel
     name                         CharField              REQ UNIQUE len=100
     slug                         SlugField              REQ UNIQUE len=100
   meta.constraints: (models.UniqueConstraint(fields=('parent', 'name'), name='%(app_label)s_%(class)s_unique_parent_name'),)
   meta.ordering: ('name', 'pk')
   meta.indexes: ()

## wireless.WirelessLink   (wireless/models.py)
   bases: WirelessAuthenticationBase, DistanceMixin, PrimaryModel
     interface_a                  ForeignKey             REQ -> dcim.Interface
     interface_b                  ForeignKey             REQ -> dcim.Interface
     ssid                         CharField                  len=SSID_MAX_LENGTH
     status                       CharField                  len=50 def='LinkStatusChoices.STATUS_CONNECTED' choices=LinkStatusChoices
     tenant                       ForeignKey                 -> tenancy.Tenant
     _interface_a_device          ForeignKey                 -> dcim.Device
     _interface_b_device          ForeignKey                 -> dcim.Device
   meta.constraints: (models.UniqueConstraint(fields=('interface_a', 'interface_b'), name='%(app_label)s_%(class)s_unique_interfaces'),)
   meta.ordering: ['pk']

```
