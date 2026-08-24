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

What each piece is here for (all five NBO-067 defects, then NBO-070, NBO-071, NBO-073, NBO-041):

| Fixture | Pins |
|---|---|
| `ipam/models/vlans.py` — `VLAN.Meta.constraints` | a long multi-constraint `Meta`, emitted in full and wrapped, never truncated |
| `ipam/models/vlans.py` — `VLAN.site` / `group` / `role`, `Prefix.vrf` | one FK per `on_delete` value (`PROTECT`, `CASCADE`, `SET_NULL`, `SET_DEFAULT`, and a positional one) |
| `ipam/constants.py` + `VRF.rd`, `RouteTarget.name`, `VLAN.description` | `max_length=SOME_CONSTANT` resolved to an integer; an unresolvable symbol flagged, not emitted raw |
| `ipam/models/vlans.py` — `Prefix.scope`, `VLAN.l2vpn_terminations` | `GenericForeignKey` / `GenericRelation` are not columns and must not be marked `REQ` |
| `dcim/models/sites.py` — `Manufacturer`, `RackGroup` | a column-less `OrganizationalModel` / `NestedGroupModel` subclass still gets a Models entry |
| `netbox/models/__init__.py` — `OrganizationalModel`, `NestedGroupModel`, `PrimaryModel` | the shared abstract bases the extractor never scanned: `name`, `slug`, `parent`, `description`, `comments` are inherited, and must appear on the subclass attributed to the class declaring them |
| `netbox/models/__init__.py` — `BASE_NAME_MAX_LENGTH`, `NetBoxModel` | an inherited column arrives with its `max_length` already resolved, and attribution survives two hops (`Manufacturer` -> `OrganizationalModel` -> `NetBoxModel` -> `CustomFieldsMixin`) |
| `netbox/models/features.py` — `CustomFieldsMixin`, `TagsMixin`, `LegacyTagsMixin` | a mixin column (`custom_field_data`) is merged; a tag manager declares no column, and must not be invented as one. `TagsMixin` uses `NetBoxTaggableManagerField` (the subclass real NetBox declares), `LegacyTagsMixin` taggit’s base `TaggableManager` — both names must be recognised, and the fixture pinned only the base one until NBO-093 |
| `netbox/models/features.py` — `TagsMixin.tags` | **NBO-073**: `tags` is a writable REST field on every kind and no column at all, so it appeared in no entry. Emitted from the mixin that declares it, as an M2M onto `extras.Tag` through `extras.TaggedItem`, marked as not a column — and never `REQ` |
| `netbox/models/__init__.py` — `NestedGroupModel.objects` | a manager that is *not* an API field: mptt's `TreeManager()` is a call assigned in a class body exactly like the `TaggableManager`, and must stay out of the field list |
| `dcim/models/racks.py` — `DeviceType.interface_template_count` | a `CounterCacheField`, whose `default=0` and `editable=False` live inside the field class: the AST sees no `default=`, and all 35 real rows came out `REQ` — a counter the API returns read-only, demanded on create |
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
| `dcim/models/racks.py` — `SpecialRackKind(BaseRackKind)` | **NBO-041**: a subclass with a docstring for a body, whose base's *name* contains none of "Model", "Component", "Template" — the inclusion test the real `circuits.CircuitType` and `circuits.VirtualCircuitType` failed, so two shipped API endpoints had no schema entry at all. Whether a class is a model is reachability, not a substring |
| `dcim/models/mixins.py` + `ipam/models/mixins.py` — `ComponentModel` | **NBO-041**: one base class name declared in two apps, each declaring a *different* column. `dcim.RackPort` must inherit dcim's (`name`, `label`) and `ipam.PrefixPort` ipam's (`component_kind`). Resolving by bare name found the name twice and dropped both, which cost eleven shipped component Kinds (`dcim.Interface`, `dcim.ConsolePort`, …) their `name`, `label` and `description` |
| `ipam/models/mixins.py` + `netbox/models/features.py` — `TenancyMixin` | **NBO-041**: the case that is still not attributable and must still say so — a base in two apps and in neither the subclass's own. `dcim.RackPort` loses `ambiguous_tenant`, and the extractor warns once, naming the model that lost it |
| `utilities/constants.py` — the `FILTER_*_LOOKUP_MAP`s | **NBO-041**: the *parameter suffixes* NetBox registers, mapped to the ORM lookups they compile to. `empty` is the suffix; `isnull` is what it means on a numeric filter, and `empty` (string emptiness) is what it means on a char one. Emitting one where NetBox registers the other is #206 |
| `netbox/filtersets.py` — `FILTER_DEFAULTS`, `_get_filter_lookup_dict`, `STANDARD_LOOKUPS` | **NBO-041**: which filter class a bare `Meta.fields` entry gets, which lookup map that class then takes (an *ordered* if-chain, so numeric is tried before char), and the fact that a non-standard `lookup_expr` gets no suffixes. All three read from the source, because a hardcoded copy is the bug |
| `utilities/filters.py` — `MultiValueContentTypeFilter(MultiValueCharFilter)` | **NBO-041**: a subclass must be recognised as the base the isinstance chain names, or it gets no lookup map at all |
| `ipam/filtersets.py` — `PrefixFilterSet.vrf_id` | **NBO-041**: an FK filter is a `ModelMultipleChoiceFilter` and takes `FILTER_NEGATION_LOOKUP_MAP`, so it registers `n` and nothing else — neither `vrf_id__isnull` nor `vrf_id__empty` exists, and django-filter drops both in silence (#206) |
| `ipam/filtersets.py` — `PrefixFilterSet.prefix` / `.mask_length` / `Meta.fields` | **NBO-041**: a `method=` and a non-standard `lookup_expr` each yield no suffixes; a numeric `Meta.fields` entry (`scope_id`) does register `empty`, and there it really means SQL NULL |
| `ipam/filtersets.py` — `VRFFilterSet.Meta.fields` | **NBO-041**: on a char column (`rd`) the same `empty` suffix asks about string emptiness, not NULL — a different question with the same spelling |
| `ipam/filtersets.py` — `VLANGroupFilterSet.scope_type` | **NBO-041**: a generic-FK type filter takes `app_label.model` strings rather than IDs (#35) |
| `netbox/filtersets.py` — `NetBoxModelFilterSet.q` | **NBO-041**: declared filters are inherited down the filterset MRO; `Meta.fields` is not. The `cf_<name>` filters this base adds from the database cannot be enumerated statically, and are recorded as dynamic rather than implied absent |
| `ipam/choices.py` — `PrefixStatusChoices` vs `RoleKindChoices` | **NBO-041**: a `key` means a deployment's `FIELD_CHOICES` can replace or extend the set, so a closed CRD enum would reject a legitimate value; no `key` means it is safe to pin. Also a colour as the third tuple element, and `_()` unwrapped |
| `ipam/choices.py` — `VLANWidthChoices` | **NBO-041**: members built by arithmetic on class constants (`60 * 24`, `WIDTH_HOUR * 12`), which `ast.literal_eval` will not take; and a label built at import time, flagged rather than passed off as a literal |
| `ipam/choices.py` — `RoleKindChoices` / `ExtendedRoleKindChoices` | **NBO-041**: a grouped ChoiceSet is flattened (a CRD enum has no optgroup) and says so; a star-unpack splices another set in, resolved in a second pass so declaration order does not matter |
| `utilities/choices.py` — `ChoiceSet`, `ChoiceSetMeta` | **NBO-041**: `CHOICES = list()` on the abstract base is legitimately empty, not a parse failure, and a metaclass is not a choice set at all |
| `ipam/api/serializers.py` — `PrefixSerializer` | **NBO-041**: `Meta.fields` is the write path (`scope_id` is deliberately absent, which is a conflict to record); `read_only=True`; `ChoiceField(choices=…)` naming the ChoiceSet; and `ContentTypeField(queryset=ContentType.objects.filter(model__in=…))`, the only place the legal generic-FK targets are written — as bare model names, qualified to `app_label.model` against models.json |
| `ipam/api/serializers.py` — `VLANGroupSerializer.Meta.read_only_fields = fields` | **NBO-041**: a Meta key naming a sibling Meta key, not a literal list |
| `netbox/api/serializers/models.py` | **NBO-041**: DRF accumulates declared serializer fields down the MRO; `Meta.fields` it does not |
| `ipam/models/vlans.py` — `Prefix.status`, `Prefix.role_kind` | **NBO-041**: an enum the AST can only name, resolved to its members; and a column absent from the serializer's `Meta.fields`, which is a recorded conflict rather than a generated field |
| `release.yaml` | **NBO-041**: the version stamped on the IR. Deliberately `0.0.0-fixture` — an IR labelled with the wrong NetBox version is worse than an unlabelled one, because a version bump then produces a diff nobody can attribute |

Keeping the fixture small is deliberate. If you extend it, add the smallest declaration
that pins the behaviour and give it a row above.
