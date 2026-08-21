# Fixture: a miniature NetBox source tree

Hand-written Django model source laid out the way `hack/extract-netbox-schema.py` expects
a real NetBox checkout to be (`<app>/models/*.py`, `<app>/constants.py`,
`<app>/api/urls.py`, and `netbox/models/` for the shared abstract bases). It is *not* a
copy of NetBox: each declaration exists to pin one
behaviour of the extraction pipeline, so `hack/test_digest.py` can prove the pipeline is
right without a NetBox checkout to hand.

What each piece is here for (all five NBO-067 defects, then NBO-070):

| Fixture | Pins |
|---|---|
| `ipam/models/vlans.py` — `VLAN.Meta.constraints` | a long multi-constraint `Meta`, emitted in full and wrapped, never truncated |
| `ipam/models/vlans.py` — `VLAN.site` / `group` / `role`, `Prefix.vrf` | one FK per `on_delete` value (`PROTECT`, `CASCADE`, `SET_NULL`, `SET_DEFAULT`, and a positional one) |
| `ipam/constants.py` + `VRF.rd`, `RouteTarget.name`, `VLAN.description` | `max_length=SOME_CONSTANT` resolved to an integer; an unresolvable symbol flagged, not emitted raw |
| `ipam/models/vlans.py` — `Prefix.scope`, `VLAN.l2vpn_terminations` | `GenericForeignKey` / `GenericRelation` are not columns and must not be marked `REQ` |
| `dcim/models/sites.py` — `Manufacturer`, `RackGroup` | a column-less `OrganizationalModel` / `NestedGroupModel` subclass still gets a Models entry |
| `netbox/models/__init__.py` — `OrganizationalModel`, `NestedGroupModel`, `PrimaryModel` | the shared abstract bases the extractor never scanned: `name`, `slug`, `parent`, `description`, `comments` are inherited, and must appear on the subclass attributed to the class declaring them |
| `netbox/models/__init__.py` — `BASE_NAME_MAX_LENGTH`, `NetBoxModel` | an inherited column arrives with its `max_length` already resolved, and attribution survives two hops (`Manufacturer` -> `OrganizationalModel` -> `NetBoxModel` -> `CustomFieldsMixin`) |
| `netbox/models/features.py` — `CustomFieldsMixin`, `TagsMixin` | a mixin column (`custom_field_data`) is merged; a `TaggableManager` is not a column and must not be invented as one |
| `netbox/models/mixins.py` + `ipam/models/vlans.py` — `VLANGroup` | `CachedScopeMixin`'s `scope_type`/`scope_id`/`scope` merged, with the generic FK's requiredness still derived from a `scope_type` that also arrives by inheritance |
| `dcim/models/mixins.py` + `dcim/models/racks.py` — `DeviceType` | `WeightMixin`'s `weight`/`weight_unit`: an app-local mixin that was always extracted but never merged |
| `dcim/models/racks.py` — `RackRole` | the entry that listed only `color`, with `name`/`slug`/`description` inherited |
| `dcim/models/sites.py` — `Region.Meta.constraints` | **the regression test**: a `Meta` constraint naming a column (`parent`, `name`) the entry itself does not list. Asserted for every fixture model, not just this one |
| `ipam/models/vlans.py` — `VLAN.description`, `VLAN.qinq_svlan` | a declared column shadowing an inherited one of the same name (declared wins, the loser is reported); a constraint-cited column that must exist |

Keeping the fixture small is deliberate. If you extend it, add the smallest declaration
that pins the behaviour and give it a row above.
