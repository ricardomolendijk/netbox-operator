package netbox

import (
	"fmt"
	"strconv"
	"strings"
)

// The supported NetBox range. The lower bound is not arbitrary: NetBox 4.2 replaced the
// `site` foreign key on Prefix, Cluster, WirelessLAN and VLANGroup with a polymorphic
// (scope_type, scope_id) pair, and on 4.2+ writing `site` silently no-ops. An operator
// pointed at 4.1 would need the opposite payload for those kinds, so rather than carry
// two field mappings it refuses to run.
//
// The upper bound is a guess about a major version that does not exist yet, which is the
// point: refusing to touch NetBox 5.0 until someone has checked is cheaper than
// discovering the difference by writing to it.
const (
	MinVersion = "4.2.0"
	MaxVersion = "5.0.0"
)

// Version is a parsed semantic-ish version. NetBox uses major.minor.patch, occasionally
// with a suffix such as "-dev" or "4.6.8-Docker-3.2.0".
type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// ParseVersion reads a NetBox version string. Anything after the third component is
// ignored: distributions append their own suffixes and none of it affects the API.
func ParseVersion(s string) (Version, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Version{}, fmt.Errorf("empty version string")
	}
	// Cut any suffix at the first character that cannot start a component.
	for i, r := range trimmed {
		if (r < '0' || r > '9') && r != '.' {
			trimmed = trimmed[:i]
			break
		}
	}
	parts := strings.Split(strings.Trim(trimmed, "."), ".")
	if len(parts) < 2 {
		return Version{}, fmt.Errorf("version %q has fewer than two components", s)
	}

	var out Version
	targets := []*int{&out.Major, &out.Minor, &out.Patch}
	for i := 0; i < len(parts) && i < len(targets); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return Version{}, fmt.Errorf("version %q has a non-numeric component %q", s, parts[i])
		}
		*targets[i] = n
	}
	return out, nil
}

// Compare returns -1, 0 or 1.
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]int{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// SupportedVersion reports whether s is in [MinVersion, MaxVersion).
func SupportedVersion(s string) (Version, bool, error) {
	version, err := ParseVersion(s)
	if err != nil {
		return Version{}, false, err
	}
	minimum, err := ParseVersion(MinVersion)
	if err != nil {
		return Version{}, false, fmt.Errorf("parsing MinVersion: %w", err)
	}
	maximum, err := ParseVersion(MaxVersion)
	if err != nil {
		return Version{}, false, fmt.Errorf("parsing MaxVersion: %w", err)
	}
	return version, version.Compare(minimum) >= 0 && version.Compare(maximum) < 0, nil
}
