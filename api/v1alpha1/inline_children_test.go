package v1alpha1

import (
	"strconv"
	"strings"
	"testing"
)

// interfaces is the one-level path a VM's inline interface produces.
func interfaces(key string) []ChildSegment {
	return []ChildSegment{{Field: "interfaces", Key: key}}
}

// addresses is the two-level path an address under `eth0` produces: the interface set carries
// no discriminator, the address set carries "ip".
func addresses(address string) []ChildSegment {
	return append(interfaces("eth0"),
		ChildSegment{Field: "addresses", Discriminator: "ip", Key: address})
}

func TestChildName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		parent string
		path   []ChildSegment
		want   string
	}{{
		name:   "an interface takes the parent's name and the key",
		parent: "dns",
		path:   interfaces("eth0"),
		want:   "dns-eth0",
	}, {
		name:   "an address takes the set's discriminator between the two keys",
		parent: "dns",
		path:   addresses("10.20.0.10/24"),
		want:   "dns-eth0-ip-10-20-0-10-24",
	}, {
		// The prefix length is part of the key, so it is part of the name. Two addresses
		// that differ only in their mask are two NetBox objects and must be two CRs.
		name:   "the prefix length is part of the name",
		parent: "dns",
		path:   addresses("10.20.0.10/25"),
		want:   "dns-eth0-ip-10-20-0-10-25",
	}, {
		name:   "uppercase is folded",
		parent: "DNS",
		path:   interfaces("Eth0"),
		want:   "dns-eth0",
	}, {
		name:   "runs of separators collapse to one dash",
		parent: "dns",
		path:   interfaces("eth0//../ .0"),
		want:   "dns-eth0-0",
	}, {
		name:   "a key that is nothing but separators leaves the parent's name",
		parent: "dns",
		path:   interfaces("///"),
		want:   "dns",
	}, {
		// Not a case fold: a Unicode letter is out of range and becomes a separator, so the
		// name never depends on a locale table.
		name:   "unicode becomes a separator rather than being transliterated",
		parent: "dns",
		path:   interfaces("ethérnet0"),
		want:   "dns-eth-rnet0",
	}, {
		name:   "an empty path is the parent's own name, slugified",
		parent: "DNS.Home",
		path:   nil,
		want:   "dns-home",
	}, {
		name:   "a nested key with a colon still slugifies",
		parent: "web-01",
		path:   addresses("2001:db8::10/64"),
		want:   "web-01-eth0-ip-2001-db8-10-64",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ChildName(tc.parent, tc.path); got != tc.want {
				t.Errorf("ChildName(%q) = %q, want %q", tc.parent, got, tc.want)
			}
		})
	}
}

// TestChildNameLength walks the boundary rather than one value on either side of it,
// because the truncation is the case where a bug is a name collision and a collision is
// data loss: two long siblings resolving to one name means one of them silently wins.
func TestChildNameLength(t *testing.T) {
	t.Parallel()

	for _, length := range []int{maxChildName - 1, maxChildName, maxChildName + 1, 900} {
		t.Run("slug of "+strconv.Itoa(length)+" characters", func(t *testing.T) {
			t.Parallel()

			// One key of exactly `length` characters, so the slug is that long: the parent
			// name contributes nothing because the path is what grows in practice.
			got := ChildName("p", interfaces(strings.Repeat("a", length-2)))

			if len(got) > maxChildName {
				t.Fatalf("a slug of %d characters produced a name of %d, over the limit of %d",
					length, len(got), maxChildName)
			}

			if length <= maxChildName {
				if strings.Count(got, "-") != 1 {
					t.Errorf("a slug within the limit was hashed: %q", got)
				}

				return
			}

			// Truncated: 246 characters of slug, a dash, and six hex characters.
			if len(got) != childNamePrefix+1+childNameDigest {
				t.Errorf("a truncated name is %d characters, want %d",
					len(got), childNamePrefix+1+childNameDigest)
			}
		})
	}
}

// TestChildNameSharedPrefix is the collision case the digest exists for: two siblings whose
// slugs agree for far longer than the truncation point, so a digest of the *truncated* form
// would give them one name and one of the two would silently win.
func TestChildNameSharedPrefix(t *testing.T) {
	t.Parallel()

	shared := strings.Repeat("a", 300)

	first := ChildName("p", interfaces(shared+"one"))
	second := ChildName("p", interfaces(shared+"two"))

	if first == second {
		t.Fatalf("two siblings sharing a %d-character prefix got one name: %q", len(shared), first)
	}

	if first[:childNamePrefix] != second[:childNamePrefix] {
		t.Errorf("the readable half should still match:\n%q\n%q", first, second)
	}
}

// TestChildNameIsDeterministic is the property NBO-036 depends on: the same manifest
// applied to a rebuilt cluster derives the same child names, whose claims compute the same
// allocation identity, and reclaim the same addresses (ADR-0005 §3).
func TestChildNameIsDeterministic(t *testing.T) {
	t.Parallel()

	path := addresses("10.20.0.10/24")

	for range 5 {
		if got, want := ChildName("dns", path), "dns-eth0-ip-10-20-0-10-24"; got != want {
			t.Fatalf("ChildName is not stable: %q, want %q", got, want)
		}
	}
}

func TestChildPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path []ChildSegment
		want string
	}{{
		name: "no segments is the spec itself",
		path: nil,
		want: "spec",
	}, {
		name: "one level names the field and the key",
		path: interfaces("eth0"),
		want: "spec.interfaces[eth0]",
	}, {
		name: "nesting appends a segment",
		path: addresses("10.20.0.10/24"),
		want: "spec.interfaces[eth0].addresses[10.20.0.10/24]",
	}, {
		// The key verbatim, not the slug. The path is provenance a human reads, and the
		// spelling they wrote is the one they will look for.
		name: "the key is not slugified",
		path: interfaces("Eth0/1"),
		want: "spec.interfaces[Eth0/1]",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ChildPath(tc.path); got != tc.want {
				t.Errorf("ChildPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChildPathSurvivesReorder is the whole reason the path is key-based rather than
// index-based. plan.md §7 spelled it `spec.interfaces[0]` while stating the purpose as "so
// re-ordering the inline list doesn't churn objects", which an index cannot deliver: an
// insertion at the front changes every index below it, so every child would be pruned and
// recreated -- and in NetBox that is a delete and a create with a new id.
func TestChildPathSurvivesReorder(t *testing.T) {
	t.Parallel()

	declared := []string{"eth0", "eth1", "eth2"}
	reordered := []string{"eth2", "eth0", "eth1"}

	paths := func(keys []string) map[string]string {
		out := make(map[string]string, len(keys))
		for _, key := range keys {
			out[key] = ChildPath(interfaces(key)) + " -> " + ChildName("dns", interfaces(key))
		}

		return out
	}

	before, after := paths(declared), paths(reordered)

	for key, want := range before {
		if after[key] != want {
			t.Errorf("reordering changed %s: %q became %q", key, want, after[key])
		}
	}
}
