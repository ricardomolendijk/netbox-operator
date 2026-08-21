# Fixture: a miniature NetBox source tree

Hand-written Django model source laid out the way `hack/extract-netbox-schema.py` expects
a real NetBox checkout to be (`<app>/models/*.py`, `<app>/constants.py`,
`<app>/api/urls.py`, and `netbox/models/` for the shared abstract bases). It is *not* a
copy of NetBox: each declaration exists to pin one
behaviour of the extraction pipeline, so `hack/test_digest.py` can prove the pipeline is
right without a NetBox checkout to hand.

There is a sibling tree, `../netbox-models-bad/`, for the two defects whose fix is to **stop
the run**: a script cannot both succeed over the good tree and fail over the same declaration,
so the deliberately-broken ones live apart.

What each piece is here for (all five NBO-067 defects, then NBO-070, then NBO-071):

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
| `dcim/models/racks.py` — `Rack.tagged_vlans` | a `ManyToManyField`: no `NOT NULL` column, so never `REQ`, and no `on_delete` either — its second positional argument is `related_name` |
| `dcim/models/racks.py` — `Rack.site`, `Rack.role`, `RackReservation.rack` | a bare class reference as an FK target, positional and as `to=`, qualified to `app.Model` with the declaring model's app |
| `dcim/models/racks.py` — `RackReservation.user` | a bare reference to a class from outside the ten scanned apps (`django.contrib.auth`'s `User`): flagged, never mislabelled `dcim.User` |
| `netbox/models/__init__.py` — `NestedGroupModel.parent` | `to='self'` resolved to the model the column ends up **on**, so an inherited `parent` reads `-> dcim.RackGroup`, not `-> netbox.self` |
| `dcim/models/racks.py` — `Rack.status` vs `Rack.airflow` | a symbolic default (`RackStatus.ACTIVE`) distinguishable from a real string literal (`'front-to-rear'`); `choices` was always printed unquoted |
| `dcim/models/racks.py` — `Rack.position`, `dcim/models/mixins.py` — `WeightMixin.weight` | `max_digits`/`decimal_places` emitted, declared and inherited: `decimal(8,6)` is not a free float |
| `dcim/api/urls.py` — `racks`, `rack-reservations` | a double-quoted `router.register`, and a viewset with no `views.` prefix: both used to be skipped in silence, and a missing endpoint row is a Kind with no CRD at all |
| `dcim/models/racks.py` — `Rack.role` (`blank=True`, no `null=`) vs `Site.facility` | `blank` is form-level, not SQL: it does not make a `NOT NULL` FK optional, but it does stand in for optional on a `CharField`, which takes `''` |
| `dcim/models/racks.py` — `Rack.Meta`, `RackReservation.Meta` | a nested `class Meta(PrimaryModel.Meta)`, whose base name contains "Model": `ast.walk` made a phantom `dcim.Meta` model entry out of each, and the second collapsed into the first |
| `dcim/models/racks.py` — `Rack.outer_unit` | a field class the extractor's whitelist does not know: still dropped, but named on stderr rather than lost in silence |
| `../netbox-models-bad/dcim/models/racks*.py` | **must fail the run**: two same-named classes in one app, where one field list used to silently replace the other |
| `../netbox-models-bad/dcim/api/urls.py` | **must fail the run**: a `router.register` whose prefix is a name, not a string literal |

Keeping the fixture small is deliberate. If you extend it, add the smallest declaration
that pins the behaviour and give it a row above.
