# Fixture: a miniature NetBox source tree

Hand-written Django model source laid out the way `hack/extract-netbox-schema.py` expects
a real NetBox checkout to be (`<app>/models/*.py`, `<app>/constants.py`,
`<app>/api/urls.py`). It is *not* a copy of NetBox: each declaration exists to pin one
behaviour of the extraction pipeline, so `hack/test_digest.py` can prove the pipeline is
right without a NetBox checkout to hand.

What each piece is here for (all five NBO-067 defects):

| Fixture | Pins |
|---|---|
| `ipam/models/vlans.py` — `VLAN.Meta.constraints` | a long multi-constraint `Meta`, emitted in full and wrapped, never truncated |
| `ipam/models/vlans.py` — `VLAN.site` / `group` / `role`, `Prefix.vrf` | one FK per `on_delete` value (`PROTECT`, `CASCADE`, `SET_NULL`, `SET_DEFAULT`, and a positional one) |
| `ipam/constants.py` + `VRF.rd`, `RouteTarget.name`, `VLAN.description` | `max_length=SOME_CONSTANT` resolved to an integer; an unresolvable symbol flagged, not emitted raw |
| `ipam/models/vlans.py` — `Prefix.scope`, `VLAN.l2vpn_terminations` | `GenericForeignKey` / `GenericRelation` are not columns and must not be marked `REQ` |
| `dcim/models/sites.py` — `Manufacturer`, `RackGroup` | a column-less `OrganizationalModel` / `NestedGroupModel` subclass still gets a Models entry |

Keeping the fixture small is deliberate. If you extend it, add the smallest declaration
that pins the behaviour and give it a row above.
