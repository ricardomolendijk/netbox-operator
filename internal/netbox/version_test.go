package netbox

import "testing"

func TestParseVersion(t *testing.T) {
	tests := map[string]struct {
		want    Version
		wantErr bool
	}{
		"4.6.8":              {Version{4, 6, 8}, false},
		"4.2.0":              {Version{4, 2, 0}, false},
		"4.6":                {Version{4, 6, 0}, false},
		"4.6.8-Docker-3.2.0": {Version{4, 6, 8}, false}, // netbox-docker appends its own
		"4.6.8-dev":          {Version{4, 6, 8}, false},
		"v4.6.8":             {Version{}, true}, // NetBox does not prefix; a "v" means something else answered
		"":                   {Version{}, true},
		"4":                  {Version{}, true},
		"four.six.eight":     {Version{}, true},
	}
	for input, tc := range tests {
		t.Run(input, func(t *testing.T) {
			got, err := ParseVersion(input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want an error", input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q): %v", input, err)
			}
			if got != tc.want {
				t.Errorf("ParseVersion(%q) = %v, want %v", input, got, tc.want)
			}
		})
	}
}

// TestSupportedVersion pins the gate. The lower bound is not cosmetic: NetBox 4.2
// replaced the `site` FK on Prefix, Cluster, WirelessLAN and VLANGroup with a
// polymorphic (scope_type, scope_id) pair, and on 4.2+ writing `site` silently no-ops.
// An operator run against 4.1 would therefore appear to work and change nothing.
func TestSupportedVersion(t *testing.T) {
	tests := map[string]bool{
		"4.2.0":  true,
		"4.6.8":  true,
		"4.9.99": true,
		"4.1.11": false, // pre-CachedScopeMixin: needs the opposite payload
		"3.7.8":  false,
		"5.0.0":  false, // a major version nobody has checked
		"5.1.0":  false,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			_, got, err := SupportedVersion(input)
			if err != nil {
				t.Fatalf("SupportedVersion(%q): %v", input, err)
			}
			if got != want {
				t.Errorf("SupportedVersion(%q) = %v, want %v", input, got, want)
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b Version
		want int
	}{
		{Version{4, 6, 8}, Version{4, 6, 8}, 0},
		{Version{4, 6, 8}, Version{4, 6, 9}, -1},
		{Version{4, 6, 8}, Version{4, 7, 0}, -1},
		{Version{4, 6, 8}, Version{5, 0, 0}, -1},
		{Version{5, 0, 0}, Version{4, 9, 9}, 1},
		{Version{4, 10, 0}, Version{4, 9, 0}, 1}, // not a string compare
	}
	for _, tc := range tests {
		if got := tc.a.Compare(tc.b); got != tc.want {
			t.Errorf("%v.Compare(%v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStatusRequiresAVersion(t *testing.T) {
	// A status response without netbox-version means something other than NetBox
	// answered -- a proxy, a login page, the wrong URL. Better to fail the endpoint than
	// to treat an unknown server as supported.
	if _, err := ParseVersion(""); err == nil {
		t.Error("an empty version parsed without error")
	}
}
