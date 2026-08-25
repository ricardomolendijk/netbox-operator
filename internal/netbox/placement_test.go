package netbox

import (
	"net/netip"
	"testing"
)

// interval is the table's shorthand: two addresses, parsed and fatal on a typo.
func interval(t *testing.T, from, to string) Interval {
	t.Helper()

	lo, err := netip.ParseAddr(from)
	if err != nil {
		t.Fatalf("parsing %q: %v", from, err)
	}

	hi, err := netip.ParseAddr(to)
	if err != nil {
		t.Fatalf("parsing %q: %v", to, err)
	}

	return Interval{Lo: lo, Hi: hi}
}

// TestFirstGap is the whole of the placement contract.
//
// Table-driven and without a NetBox, which is the point of FirstGap being a pure function:
// the failure it prevents -- two claims handed the same block -- is invisible in production
// until two DHCP servers answer for one subnet, so the arithmetic has to be provable here.
func TestFirstGap(t *testing.T) {
	cases := []struct {
		name     string
		parent   string
		occupied [][2]string
		size     uint64
		align    Alignment
		want     string
		wantEnd  string
		found    bool
	}{
		{
			name: "an empty parent starts at its first address",
			// The network address included, deliberately: NetBox permits an ip-range to
			// contain it, and excluding it would be this operator inventing a rule for IPv4
			// that makes no sense for an IPv6 /64 or a /31.
			parent: "10.0.30.0/24", size: 64, align: AlignAny,
			want: "10.0.30.0", wantEnd: "10.0.30.63", found: true,
		},
		{
			name:   "a fully occupied parent has nowhere to go",
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.255"}},
			size: 1, align: AlignAny, found: false,
		},
		{
			name:   "a gap of exactly size fits",
			parent: "10.0.30.0/24",
			occupied: [][2]string{
				{"10.0.30.0", "10.0.30.63"}, {"10.0.30.128", "10.0.30.255"},
			},
			size: 64, align: AlignAny,
			want: "10.0.30.64", wantEnd: "10.0.30.127", found: true,
		},
		{
			name:   "a gap one short of size does not",
			parent: "10.0.30.0/24",
			occupied: [][2]string{
				{"10.0.30.0", "10.0.30.63"}, {"10.0.30.127", "10.0.30.255"},
			},
			size: 64, align: AlignAny, found: false,
		},
		{
			name:   "two adjacent occupied intervals are one obstacle",
			parent: "10.0.30.0/24",
			occupied: [][2]string{
				{"10.0.30.0", "10.0.30.31"}, {"10.0.30.32", "10.0.30.63"},
			},
			size: 32, align: AlignAny,
			want: "10.0.30.64", wantEnd: "10.0.30.95", found: true,
		},
		{
			name:   "overlapping occupied intervals do not walk the cursor backwards",
			parent: "10.0.30.0/24",
			occupied: [][2]string{
				{"10.0.30.0", "10.0.30.99"}, {"10.0.30.10", "10.0.30.20"},
			},
			size: 4, align: AlignAny,
			want: "10.0.30.100", wantEnd: "10.0.30.103", found: true,
		},
		{
			name: "an interval straddling the parent boundary occupies the part inside it",
			// The case a `?parent=` filter would miss: NetBox's IPRangeFilterSet.parent
			// requires *both* endpoints inside the prefix, so this range is invisible to it
			// and a placement computed from that filter would overlap it forever.
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.29.200", "10.0.30.63"}},
			size: 64, align: AlignAny,
			want: "10.0.30.64", wantEnd: "10.0.30.127", found: true,
		},
		{
			name:   "an interval entirely outside the parent is not an obstacle",
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.31.0", "10.0.31.255"}},
			size: 256, align: AlignAny,
			want: "10.0.30.0", wantEnd: "10.0.30.255", found: true,
		},
		{
			name:   "an IPv6 interval cannot occupy an IPv4 parent",
			parent: "10.0.30.0/24", occupied: [][2]string{{"fd00:10::", "fd00:10::ffff"}},
			size: 8, align: AlignAny,
			want: "10.0.30.0", wantEnd: "10.0.30.7", found: true,
		},
		{
			name:   "an IPv6 /64 parent is arithmetic like any other",
			parent: "fd00:10::/64", occupied: [][2]string{{"fd00:10::", "fd00:10::ff"}},
			size: 4096, align: AlignAny,
			want: "fd00:10::100", wantEnd: "fd00:10::10ff", found: true,
		},
		{
			name:   "size exceeding the parent finds nothing",
			parent: "10.0.30.0/24", size: 257, align: AlignAny, found: false,
		},
		{
			name:   "size zero is not a request",
			parent: "10.0.30.0/24", size: 0, align: AlignAny, found: false,
		},
		{
			name:   "a size of one is a single-address range",
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.0"}},
			size: 1, align: AlignAny,
			want: "10.0.30.1", wantEnd: "10.0.30.1", found: true,
		},
		{
			name:   "PowerOfTwo rounds the start up to the block",
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.12"}},
			size: 64, align: AlignPowerOfTwo,
			want: "10.0.30.64", wantEnd: "10.0.30.127", found: true,
		},
		{
			name: "Any takes the first free address on the same input",
			// The pair that makes the option worth having: same parent, same occupancy, and
			// the two answers differ by 51 addresses of packing against a start a DHCP config
			// generator can express as 10.0.30.64/26.
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.12"}},
			size: 64, align: AlignAny,
			want: "10.0.30.13", wantEnd: "10.0.30.76", found: true,
		},
		{
			name:   "PowerOfTwo skips a gap it cannot align into",
			parent: "10.0.30.0/24",
			occupied: [][2]string{
				{"10.0.30.0", "10.0.30.20"}, {"10.0.30.100", "10.0.30.120"},
			},
			size: 64, align: AlignPowerOfTwo,
			want: "10.0.30.128", wantEnd: "10.0.30.191", found: true,
		},
		{
			name: "a non-power-of-two size aligns to the next power of two",
			// 65 addresses align to 128, not to 65: "the next power of two greater than or
			// equal to size".
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.0"}},
			size: 65, align: AlignPowerOfTwo,
			want: "10.0.30.128", wantEnd: "10.0.30.192", found: true,
		},
		{
			name:   "the last block of the parent is usable",
			parent: "10.0.30.0/24", occupied: [][2]string{{"10.0.30.0", "10.0.30.191"}},
			size: 64, align: AlignPowerOfTwo,
			want: "10.0.30.192", wantEnd: "10.0.30.255", found: true,
		},
		{
			name:   "a /32 parent holds exactly one address",
			parent: "10.0.30.7/32", size: 1, align: AlignAny,
			want: "10.0.30.7", wantEnd: "10.0.30.7", found: true,
		},
		{
			name:   "a parent at the top of the address space does not overflow",
			parent: "255.255.255.0/24", occupied: [][2]string{{"255.255.255.0", "255.255.255.254"}},
			size: 2, align: AlignAny, found: false,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parent, err := netip.ParsePrefix(test.parent)
			if err != nil {
				t.Fatalf("parsing %q: %v", test.parent, err)
			}

			occupied := make([]Interval, 0, len(test.occupied))
			for _, pair := range test.occupied {
				occupied = append(occupied, interval(t, pair[0], pair[1]))
			}

			got, ok := FirstGap(parent, occupied, test.size, test.align)

			if ok != test.found {
				t.Fatalf("FirstGap() found = %v (%v), want %v", ok, got, test.found)
			}

			if !test.found {
				return
			}

			if got.Lo.String() != test.want || got.Hi.String() != test.wantEnd {
				t.Errorf("FirstGap() = %s-%s, want %s-%s",
					got.Lo, got.Hi, test.want, test.wantEnd)
			}

			if got.Size() != test.size {
				t.Errorf("placement spans %d addresses, want %d", got.Size(), test.size)
			}

			if !parent.Contains(got.Lo) || !parent.Contains(got.Hi) {
				t.Errorf("placement %s-%s is not inside %s", got.Lo, got.Hi, parent)
			}
		})
	}
}

// TestFirstGapNeverOverlaps is the property the table cannot state case by case: whatever it
// returns, no occupied interval touches it.
func TestFirstGapNeverOverlaps(t *testing.T) {
	parent := netip.MustParsePrefix("10.0.30.0/24")
	occupied := []Interval{
		interval(t, "10.0.30.4", "10.0.30.9"),
		interval(t, "10.0.30.40", "10.0.30.41"),
		interval(t, "10.0.29.0", "10.0.30.1"),
	}

	for size := uint64(1); size <= 32; size++ {
		for _, align := range []Alignment{AlignAny, AlignPowerOfTwo} {
			placed, ok := FirstGap(parent, occupied, size, align)
			if !ok {
				continue
			}

			for _, taken := range occupied {
				if placed.Lo.Compare(taken.Hi) <= 0 && taken.Lo.Compare(placed.Hi) <= 0 {
					t.Fatalf("size %d/%s placed %s-%s, which overlaps %s-%s",
						size, align, placed.Lo, placed.Hi, taken.Lo, taken.Hi)
				}
			}
		}
	}
}
