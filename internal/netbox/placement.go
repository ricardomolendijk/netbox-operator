// This file is address arithmetic and nothing else: no HTTP, no NetBox, no context.
//
// It exists because one of the three claim kinds has no server-side allocation endpoint to
// lean on. `available-ips` and `available-prefixes` are advisory-locked views NetBox
// implements itself (see allocate.go); there is no `available-ranges` at 4.6.8, and the
// closest thing -- `POST ipam/ip-ranges/{id}/available-ips/` -- allocates an *address out of
// a range*, which is the opposite operation. So placing an ip-range inside a prefix has to be
// computed here and committed with an ordinary POST.
//
// Two properties are deliberate and load-bearing:
//
//   - **Arithmetic, never enumeration.** The occupied set is the other *ranges* in the same
//     VRF, of which there are a handful; it is never the addresses inside them. An IPv6 /64
//     parent has 2^64 addresses and 0 or 1 ranges, and only one of those two numbers can be
//     asked about.
//   - **Pure.** FirstGap takes the parent, the occupied intervals and the request, and
//     returns a placement. It is table-testable without a NetBox, which matters because the
//     failure it prevents -- handing the same block to two claims -- is invisible in
//     production until somebody's DHCP server starts answering for somebody else's subnet.

package netbox

import (
	"math/big"
	"math/bits"
	"net/netip"
	"slices"
)

// Alignment is how a placement's start address may be chosen.
//
// Spelled as strings because it arrives from a CR field
// (`NetBoxIPRangeClaim.spec.alignment`) and is compared against the value a user wrote.
type Alignment string

const (
	// AlignAny takes the first address that leaves room, wherever it falls. The zero value:
	// a caller that expresses no preference gets the densest packing.
	AlignAny Alignment = "Any"

	// AlignPowerOfTwo starts the range on a multiple of the next power of two greater than
	// or equal to its size, counted from the start of the address space rather than from the
	// parent.
	//
	// Absolute rather than parent-relative on purpose. A DHCP pool of 64 addresses that
	// starts at 10.0.30.64 is expressible as `10.0.30.64/26` in every downstream config
	// generator; one that starts at 10.0.30.13 is expressible in none of them, and the bug
	// report arrives months later as "our leases are wrong". Since a prefix is itself
	// aligned to its own mask, an absolute alignment inside a parent is also aligned
	// relative to the parent, so the stronger guarantee costs nothing.
	AlignPowerOfTwo Alignment = "PowerOfTwo"
)

// Interval is an inclusive run of addresses: an ip-range as arithmetic.
//
// Inclusive at both ends because that is what NetBox stores -- `start_address` and
// `end_address` are both real addresses in the range, and `size` is `end - start + 1`
// (ipam.IPRange.save, NetBox 4.6.8). A half-open interval would be one off from the model
// this has to agree with, in the direction that silently overlaps by one address.
type Interval struct {
	Lo netip.Addr
	Hi netip.Addr
}

// Size is how many addresses the interval covers, as NetBox counts them.
func (i Interval) Size() uint64 {
	if !i.Lo.IsValid() || !i.Hi.IsValid() {
		return 0
	}

	span := new(big.Int).Sub(addrInt(i.Hi), addrInt(i.Lo))

	return span.Add(span, big.NewInt(1)).Uint64()
}

// FirstGap returns the lowest placement of size addresses inside parent that no interval in
// occupied touches, honouring align.
//
// The occupied set does not have to be sorted, clipped to the parent, or free of intervals
// from another address family: every caller of this reads its input from NetBox, and a
// function that required its caller to normalise first would be a function whose contract is
// the caller's bug. Intervals outside the parent are dropped, intervals straddling its
// boundary are clipped, and overlapping intervals are merged by the walk.
//
// Returns false when there is no such placement -- which is the pool being exhausted for this
// request, and is reported as such rather than as an error.
func FirstGap(parent netip.Prefix, occupied []Interval, size uint64, align Alignment) (Interval, bool) {
	if !parent.IsValid() || size == 0 {
		return Interval{}, false
	}

	parent = parent.Masked()
	lo, hi := parent.Addr().Unmap(), lastAddr(parent)

	busy := make([]Interval, 0, len(occupied))

	for _, taken := range occupied {
		if clipped, ok := clip(taken, lo, hi); ok {
			busy = append(busy, clipped)
		}
	}

	slices.SortFunc(busy, func(a, b Interval) int { return a.Lo.Compare(b.Lo) })

	cursor := lo

	for _, taken := range busy {
		// A gap before this interval, which the sort guarantees is the lowest one left.
		if taken.Lo.Compare(cursor) > 0 {
			if placed, ok := fit(cursor, taken.Lo.Prev(), size, align); ok {
				return placed, true
			}
		}

		// Merge rather than assume disjointness: two occupied intervals may overlap, and an
		// unconditional advance would walk the cursor backwards.
		if taken.Hi.Compare(cursor) >= 0 {
			cursor = taken.Hi.Next()
		}

		if !cursor.IsValid() || cursor.Compare(hi) > 0 {
			return Interval{}, false
		}
	}

	return fit(cursor, hi, size, align)
}

// fit places size addresses at or after lo, without passing hi.
func fit(lo, hi netip.Addr, size uint64, align Alignment) (Interval, bool) {
	if !lo.IsValid() || !hi.IsValid() || lo.Compare(hi) > 0 {
		return Interval{}, false
	}

	start := addrInt(lo)

	if align == AlignPowerOfTwo {
		start = alignUp(start, blockSize(size))
	}

	end := new(big.Int).Add(start, new(big.Int).SetUint64(size-1))
	if end.Cmp(addrInt(hi)) > 0 {
		return Interval{}, false
	}

	return Interval{Lo: intAddr(start, lo.Is4()), Hi: intAddr(end, lo.Is4())}, true
}

// clip narrows taken to [lo, hi], reporting whether anything of it is left.
//
// An interval from another address family clips to nothing: netip.Addr.Compare orders IPv4
// before IPv6, so a v6 interval is entirely above a v4 parent and entirely below nothing.
// That is the correct answer rather than a lucky one -- an IPv6 range cannot occupy space in
// an IPv4 prefix.
func clip(taken Interval, lo, hi netip.Addr) (Interval, bool) {
	from, to := taken.Lo.Unmap(), taken.Hi.Unmap()

	if !from.IsValid() || !to.IsValid() || from.Compare(to) > 0 {
		return Interval{}, false
	}

	if to.Compare(lo) < 0 || from.Compare(hi) > 0 {
		return Interval{}, false
	}

	if from.Compare(lo) < 0 {
		from = lo
	}

	if to.Compare(hi) > 0 {
		to = hi
	}

	return Interval{Lo: from, Hi: to}, true
}

// blockSize is the next power of two greater than or equal to size, which is the granularity
// AlignPowerOfTwo aligns to.
func blockSize(size uint64) *big.Int {
	if size <= 1 {
		return big.NewInt(1)
	}

	return new(big.Int).Lsh(big.NewInt(1), uint(bits.Len64(size-1)))
}

// alignUp rounds value up to the next multiple of block.
func alignUp(value, block *big.Int) *big.Int {
	remainder := new(big.Int).Mod(value, block)
	if remainder.Sign() == 0 {
		return value
	}

	return new(big.Int).Add(value, new(big.Int).Sub(block, remainder))
}

// lastAddr is the highest address in prefix: the broadcast address of an IPv4 network, and
// the all-ones host of an IPv6 one.
func lastAddr(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr().Unmap()
	hostBits := uint(addr.BitLen() - prefix.Bits())

	mask := new(big.Int).Lsh(big.NewInt(1), hostBits)
	mask.Sub(mask, big.NewInt(1))

	return intAddr(mask.Or(mask, addrInt(addr)), addr.Is4())
}

// addrInt is addr as an integer, for the arithmetic net/netip does not offer: netip.Addr can
// step by one in either direction and compare, and nothing else.
func addrInt(addr netip.Addr) *big.Int {
	bytes := addr.Unmap().AsSlice()

	return new(big.Int).SetBytes(bytes)
}

// intAddr is the inverse of addrInt. The width is the caller's, because an integer carries no
// family and a v4 address and the v6 address of the same value are different addresses.
func intAddr(value *big.Int, is4 bool) netip.Addr {
	width := 16
	if is4 {
		width = 4
	}

	addr, _ := netip.AddrFromSlice(value.FillBytes(make([]byte, width)))

	return addr
}
