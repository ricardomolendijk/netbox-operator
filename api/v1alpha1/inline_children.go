// Inline child declarations: the capability a parent Kind implements, and the two pure
// functions that turn one inline entry into a child CR's name and its provenance path.
//
// They live here, in a leaf package, rather than next to the materialiser in
// internal/reconciler, because a per-kind InlineChildren() has to be able to *name a
// sibling* -- an inline address's assignedObject points at the interface child that the
// same parent materialised, and an inline lag points at another interface (NBO-034). That
// makes ChildName part of the API package's own vocabulary, and api/v1alpha1 cannot import
// internal/reconciler without a cycle.
package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InlineParent is implemented by the Kinds that carry inline child declarations -- a
// NetBoxVirtualMachine's interfaces, a NetBoxDevice's ports. Implemented in
// api/v1alpha1/<app>_<kind>.go, next to the spec struct it reads.
//
// A capability rather than a field on the Descriptor, and a capability rather than a switch
// on Kind: the materialiser asks every object it reconciles whether it is one of these, and
// a Kind that is not answers by not implementing the method. That is the whole of the
// engine's per-kind branching (CONTRIBUTING.md, "Extensibility").
//
// +kubebuilder:object:generate=false
type InlineParent interface {
	// InlineChildren returns the child CRs this object's spec declares, as a tree.
	//
	// Called on every reconcile, so it must be pure: build the objects, do not read the
	// API server, and do not cache. Every child is returned minus its name, its labels and
	// its owner references, which the materialiser owns.
	InlineChildren() []InlineChildSet
}

// InlineChildSet is one inline list on a spec: the field it was written under, and its
// entries.
//
// +kubebuilder:object:generate=false
type InlineChildSet struct {
	// Field is the spec field these entries came from, as the user spelled it --
	// "interfaces", "addresses". It is what the owned-by path records, so it is the
	// spelling a human sees in the annotation and in an error message.
	Field string

	// Discriminator is the short token that goes into the derived *name* to keep two child
	// kinds under one parent from colliding: "ip" for an interface's addresses, "disk" for
	// a VM's disks. Empty for the set that owns the parent's own namespace of keys -- a
	// VM's interfaces are named `<vm>-<interface>` with no token.
	//
	// Explicit data rather than derived from Field, because "addresses" -> "ip" is not a
	// transformation, it is a decision the per-kind code is making. Two sibling sets that
	// share a discriminator and a key derive the same name, which the materialiser reports
	// as a Conflict rather than resolving.
	Discriminator string

	// Entries are the inline declarations, in the order the spec listed them. The order is
	// not read: identity is the entry's Key, so reordering the list changes nothing.
	Entries []InlineChildEntry
}

// InlineChildEntry is one inline child declaration.
//
// +kubebuilder:object:generate=false
type InlineChildEntry struct {
	// Key is this entry's identity within its set: an interface name, an address in CIDR
	// form. It is what the derived name and the owned-by path are both built from, so the
	// two move together or not at all -- and changing it prunes the old child and
	// materialises a new one, which in NetBox is a delete and a create.
	Key string

	// Desired is the child CR to materialise, fully formed except for its name, its
	// namespace, its labels, its annotations and its owner references. Nil for an entry
	// that exists only to carry nested children.
	Desired client.Object

	// Children are the child sets nested under this entry -- an interface's addresses. The
	// materialiser recurses, so depth is not baked in; the path and the name each grow by
	// one segment per level.
	Children []InlineChildSet
}

// ChildSegment is one level of an inline child's provenance: which spec field, which entry
// within it, and the name token that field contributes.
//
// +kubebuilder:object:generate=false
type ChildSegment struct {
	// Field is InlineChildSet.Field.
	Field string

	// Discriminator is InlineChildSet.Discriminator.
	Discriminator string

	// Key is InlineChildEntry.Key.
	Key string
}

// Name-length arithmetic. A CR's metadata.name is an RFC 1123 *subdomain*, so the limit is
// 253 characters and not the 63 of a DNS label -- nothing turns a child CR's name into a
// label, and truncating to 63 would throw away readability the API server does not ask for.
const (
	// maxChildName is the RFC 1123 subdomain limit the API server enforces on a CR name.
	maxChildName = 253

	// childNamePrefix is how much of the slug survives truncation. 246 + 1 for the
	// separator + 6 for the digest is exactly 253.
	childNamePrefix = 246

	// childNameDigest is how many hex characters of the digest are appended. Six is 24 bits
	// -- enough that two siblings sharing a 246-character prefix do not collide in any
	// realistic parent, and short enough that the readable part stays readable.
	childNameDigest = 6
)

// ChildName is the deterministic name of the child CR at path under a parent named
// parentName.
//
//	ChildName("dns", interfaces[eth0])                  -> "dns-eth0"
//	ChildName("dns", interfaces[eth0].addresses[10...])  -> "dns-eth0-ip-10-20-0-10-24"
//
// **parentName is metadata.name, never spec.name.** A Kubernetes object's name is
// immutable, so a child name derived from metadata.name never changes under a live object;
// deriving it from spec.name -- the NetBox name -- would mean renaming an object in NetBox
// churned every child CR in Kubernetes, deleting and recreating the NetBox rows underneath
// them. Callers get this right by construction: the materialiser passes obj.GetName().
//
// Determinism is load-bearing beyond avoiding churn. A claim's allocation identity is
// derived from its name (ADR-0005 §3), so a parent deleted and re-applied from the same
// manifest materialises children with the same names, whose claims compute the same
// identity, and reclaim the same addresses. A random suffix would hand out new addresses on
// every cluster rebuild.
func ChildName(parentName string, path []ChildSegment) string {
	parts := make([]string, 0, 2*len(path)+1)
	parts = append(parts, parentName)

	for _, segment := range path {
		if segment.Discriminator != "" {
			parts = append(parts, segment.Discriminator)
		}

		parts = append(parts, segment.Key)
	}

	slug := slugify(strings.Join(parts, "-"))
	if len(slug) <= maxChildName {
		return slug
	}

	// The digest is of the *untruncated* slug, which is the whole point: two long siblings
	// that share a 246-character prefix differ only past the cut, so hashing the truncated
	// form would give them one name and one of them would silently win.
	sum := sha256.Sum256([]byte(slug))

	return strings.TrimRight(slug[:childNamePrefix], "-") +
		"-" + hex.EncodeToString(sum[:])[:childNameDigest]
}

// ChildPath renders the spec path that produced a child, for the owned-by-path annotation:
//
//	spec.interfaces[eth0].addresses[10.20.0.10/24]
//
// **Key-based, not index-based**, and that is what makes it useful. The annotation exists so
// that pruning can tell two inline entries of one parent apart (ADR-0005 §2), and it is read
// on every reconcile; an index-based path changes for every entry below an insertion, so
// reordering a list would prune and recreate every child after the edit. A key survives the
// reorder, and it is the same string ChildName consumed, so the name and the path move
// together or not at all.
func ChildPath(path []ChildSegment) string {
	var b strings.Builder

	b.WriteString("spec")

	for _, segment := range path {
		b.WriteString("." + segment.Field + "[" + segment.Key + "]")
	}

	return b.String()
}

// slugify lowercases, replaces every character outside [a-z0-9-] with a dash, collapses
// runs of dashes and trims them from both ends -- the shape RFC 1123 requires of a name.
//
// Byte-wise rather than rune-wise, and ASCII-only lowercasing. A multi-byte rune becomes
// two to four out-of-range bytes, which collapse to the one dash a rune-wise pass would have
// produced, so the two agree on every input; and a Unicode case fold would map some runes
// into ASCII letters, making the slug depend on a locale table rather than on the string.
func slugify(s string) string {
	out := make([]byte, 0, len(s))

	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}

		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, c)

			continue
		}

		// One dash per run, and never a leading one: a name may not begin or end with a
		// dash, and the trailing case is trimmed below because the run may be the last
		// thing in the string.
		if len(out) > 0 && out[len(out)-1] != '-' {
			out = append(out, '-')
		}
	}

	return strings.TrimRight(string(out), "-")
}
