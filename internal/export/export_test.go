package export_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/ricardomolendijk/netbox-operator/internal/export"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// fakeNetBox answers a list per endpoint, and nothing for an endpoint it has no fixture
// for. It is the whole of the client surface export can reach.
type fakeNetBox struct {
	byEndpoint map[string][]netbox.Object
	err        error
}

func (f fakeNetBox) List(_ context.Context, endpoint string, _ netbox.Params) ([]netbox.Object, error) {
	if f.err != nil {
		return nil, f.err
	}

	return f.byEndpoint[endpoint], nil
}

// fixture is one small NetBox: two sites, a location scoped under one of them, a prefix
// scoped to the *location* rather than to the site, a VRF with route targets, a tag with
// object types, and two VLANs whose names collide.
func fixture() fakeNetBox {
	return fakeNetBox{byEndpoint: map[string][]netbox.Object{
		"dcim/sites": {
			{
				"id": 1.0, "name": "Home Lab / Rack 3", "slug": "home-lab",
				"status": map[string]any{"value": "active", "label": "Active"},
				"url":    "https://nb/api/dcim/sites/1/", "display": "Home Lab",
				"created": "2024-01-01", "last_updated": "2024-01-02",
				"description": "", "comments": "", "facility": "",
				"latitude": "52.100000", "physical_address": "", "shipping_address": "",
				"custom_fields": map[string]any{"owner": "net", "k8s_uid": nil, "empty": nil},
			},
			{
				"id": 2.0, "name": "Managed Site", "slug": "managed",
				"status": map[string]any{"value": "active", "label": "Active"},
				"tags":   []any{map[string]any{"id": 9.0, "slug": "k8s-managed", "name": "k8s-managed"}},
			},
		},
		"dcim/locations": {
			{
				"id": 5.0, "name": "Row A", "slug": "row-a",
				"site":   map[string]any{"id": 1.0, "name": "Home Lab", "slug": "home-lab"},
				"parent": nil, "_depth": 0.0, "_children": 0.0,
				"status": map[string]any{"value": "active", "label": "Active"},
			},
		},
		"ipam/prefixes": {
			{
				"id": 7.0, "prefix": "10.0.20.0/24",
				"status":     map[string]any{"value": "active", "label": "Active"},
				"scope_type": "dcim.location", "scope_id": 5.0,
				// The cached columns NetBox denormalises from the pair. A Location-scoped
				// prefix carries _site too, which is exactly why they are never read.
				"_site": map[string]any{"id": 1.0}, "_location": map[string]any{"id": 5.0},
				"_depth": 0.0, "_children": 0.0,
				"is_pool": false, "mark_utilized": false,
				"role": map[string]any{"id": 42.0, "name": "management"},
				"vrf":  map[string]any{"id": 3.0, "name": "home"},
			},
		},
		// virtualization.Cluster landed after this exporter did, and needed no code for
		// it: the same scope union, scoped to a Site rather than a Location.
		"virtualization/clusters": {
			{
				"id": 30.0, "name": "Rack 3 hosts",
				"scope_type": "dcim.site", "scope_id": 1.0,
				"_site": map[string]any{"id": 1.0},
				"type":  map[string]any{"id": 31.0, "name": "Proxmox"},
			},
		},
		"virtualization/cluster-types": {
			{"id": 31.0, "name": "Proxmox", "slug": "proxmox"},
		},
		"ipam/vrfs": {
			{
				"id": 3.0, "name": "home", "rd": "65000:1", "enforce_unique": true,
				"export_targets": []any{map[string]any{"id": 12.0}, map[string]any{"id": 11.0}},
			},
		},
		"ipam/route-targets": {
			{"id": 11.0, "name": "65000:11"},
			{"id": 12.0, "name": "65000:12"},
		},
		"ipam/vlans": {
			{"id": 20.0, "vid": 10.0, "name": "mgmt", "site": map[string]any{"id": 1.0}},
			{"id": 21.0, "vid": 10.0, "name": "mgmt", "site": map[string]any{"id": 2.0}},
		},
		"extras/tags": {
			{
				"id": 8.0, "name": "Edge", "slug": "edge", "color": "ff0000",
				"object_types": []any{"dcim.site", "dcim.location"},
			},
			// The operator's own provenance tag, which the bootstrap owns.
			{"id": 9.0, "name": "k8s-managed", "slug": "k8s-managed", "color": "9e9e9e"},
		},
	}}
}

func options(dir string) export.Options {
	return export.Options{
		OutputDir:   dir,
		Namespace:   "homelab",
		EndpointRef: "homelab",
		ManagedTag:  "k8s-managed",
	}
}

// docs indexes the export by "Kind/name" so an assertion names the object it is about.
func docs(t *testing.T, files map[string]string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, content := range files {
		for _, raw := range strings.Split(content, "---\n") {
			if strings.TrimSpace(stripComments(raw)) == "" {
				continue
			}
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatalf("unmarshalling %q: %v", raw, err)
			}
			meta, _ := doc["metadata"].(map[string]any)
			out[doc["kind"].(string)+"/"+meta["name"].(string)] = doc
		}
	}

	return out
}

func stripComments(in string) string {
	var kept []string
	for _, line := range strings.Split(in, "\n") {
		if !strings.HasPrefix(line, "#") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

func build(t *testing.T, opts export.Options) (map[string]string, export.Result) {
	t.Helper()

	files, result, err := export.Build(context.Background(), fixture(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	return files, result
}

func specOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()

	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("document has no spec: %v", doc)
	}

	return spec
}

func TestReferencesResolveToExportedNames(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))
	all := docs(t, files)

	location := specOf(t, all["NetBoxLocation/row-a"])
	if got := location["siteRef"]; !equalRef(got, map[string]any{"name": "home-lab"}) {
		t.Errorf("siteRef = %v, want the exported site's CR name", got)
	}

	vrf := specOf(t, all["NetBoxVRF/home"])
	// Sorted by id, so the output does not depend on the order NetBox returned them in.
	// The CR names, not the NetBox names: `65000:11` is not a legal object name, and the
	// colon survives in the spec's own `name` field where NetBox sees it.
	want := []any{map[string]any{"name": "65000-11"}, map[string]any{"name": "65000-12"}}
	if got, _ := json.Marshal(vrf["exportTargets"]); string(got) != mustJSON(t, want) {
		t.Errorf("exportTargets = %s, want %s", got, mustJSON(t, want))
	}
}

func TestReferenceOutsideExportSetFallsBackToID(t *testing.T) {
	files, result := build(t, options(t.TempDir()))
	spec := specOf(t, docs(t, files)["NetBoxPrefix/10.0.20.0-24"])

	// ipam.Role has no Kind yet, so there is no CR to name and the id is the only honest
	// answer.
	if got := spec["roleRef"]; !equalRef(got, map[string]any{"id": 42.0}) {
		t.Errorf("roleRef = %v, want the raw NetBox id", got)
	}

	if !hasNote(result.Notes, "outside the export set") {
		t.Errorf("an id-mode reference was not reported: %v", result.Notes)
	}
}

func TestScopeComesFromThePairAndNotTheCache(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))
	spec := specOf(t, docs(t, files)["NetBoxPrefix/10.0.20.0-24"])

	scope, ok := spec["scope"].(map[string]any)
	if !ok {
		t.Fatalf("prefix has no scope: %v", spec)
	}
	if _, ok := scope["locationRef"]; !ok {
		t.Errorf("scope = %v, want locationRef derived from scope_type dcim.location", scope)
	}
	if _, ok := scope["siteRef"]; ok {
		t.Errorf("scope = %v, must not read the _site cache", scope)
	}

	// The other member of the same union, on a Kind added after this exporter: scoped to a
	// Site, and by name because the site is in the export set.
	cluster := specOf(t, docs(t, files)["NetBoxCluster/rack-3-hosts"])
	clusterScope, ok := cluster["scope"].(map[string]any)
	if !ok {
		t.Fatalf("cluster has no scope: %v", cluster)
	}
	if !equalRef(clusterScope["siteRef"], map[string]any{"name": "home-lab"}) {
		t.Errorf("cluster scope = %v, want siteRef by name", clusterScope)
	}
	if _, ok := spec["siteRef"]; ok {
		t.Errorf("prefix spec = %v, must not carry a siteRef at all", spec)
	}
}

func TestVLANUsesTheRealSiteForeignKey(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	for name, doc := range docs(t, files) {
		if !strings.HasPrefix(name, "NetBoxVLAN/") {
			continue
		}
		if _, ok := specOf(t, doc)["siteRef"]; !ok {
			t.Errorf("%s has no siteRef; ipam.VLAN.site is a real foreign key", name)
		}
	}
}

// forbidden is every field NetBox maintains for itself. None can appear in a spec, and the
// test walks the output rather than trusting a filter.
var forbidden = []string{"id", "url", "display", "created", "last_updated", "total_vlan_ids"}

func TestServerMaintainedFieldsAreNeverExported(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	for name, doc := range docs(t, files) {
		walk(t, name, specOf(t, doc))
	}
}

func walk(t *testing.T, where string, spec map[string]any) {
	t.Helper()

	for key, value := range spec {
		if strings.HasPrefix(key, "_") {
			t.Errorf("%s exports the cached column %q", where, key)
		}
		for _, bad := range forbidden {
			// `id` is legal inside a reference and nowhere else, so a nested map is only
			// walked when it is not one.
			if key == bad && len(spec) > 1 {
				t.Errorf("%s exports the server-maintained field %q", where, key)
			}
		}
		if nested, ok := value.(map[string]any); ok && !isRef(nested) {
			walk(t, where+"."+key, nested)
		}
	}
}

func isRef(in map[string]any) bool {
	if len(in) != 1 {
		return false
	}
	_, byName := in["name"]
	_, byID := in["id"]

	return byName || byID
}

func TestOperatorManagedObjectsAreSkipped(t *testing.T) {
	files, result := build(t, options(t.TempDir()))

	if _, ok := docs(t, files)["NetBoxSite/managed"]; ok {
		t.Error("a site carrying the provenance tag was exported; a CR in Git already describes it")
	}
	// The tagged site, and the provenance tag itself.
	if result.SkippedManaged != 2 {
		t.Errorf("SkippedManaged = %d, want 2", result.SkippedManaged)
	}

	opts := options(t.TempDir())
	opts.IncludeManaged = true
	included, _ := build(t, opts)
	if _, ok := docs(t, included)["NetBoxSite/managed"]; !ok {
		t.Error("--include-managed did not export the managed site")
	}
}

func TestProvenanceNeverReachesTheOutput(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	for name, content := range files {
		for _, secret := range []string{"k8s_uid", "k8s_allocation_identity", "k8s-managed"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s contains %q, which is provenance rather than desired state", name, secret)
			}
		}
	}
}

func TestChoiceFieldsExportTheirValue(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	if got := specOf(t, docs(t, files)["NetBoxSite/home-lab"])["status"]; got != "active" {
		t.Errorf("status = %v, want the choice value", got)
	}
}

func TestObjectTypeListsAreSorted(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))
	spec := specOf(t, docs(t, files)["NetBoxTag/edge"])

	if got := mustJSON(t, spec["objectTypes"]); got != `["dcim.location","dcim.site"]` {
		t.Errorf("objectTypes = %s, want them sorted", got)
	}
}

func TestMinimalDropsEmptyValuesAndFullKeepsThem(t *testing.T) {
	minimal, _ := build(t, options(t.TempDir()))
	if _, ok := specOf(t, docs(t, minimal)["NetBoxSite/home-lab"])["comments"]; ok {
		t.Error("the default export kept an empty comments field")
	}

	opts := options(t.TempDir())
	opts.Full = true
	full, _ := build(t, opts)
	if _, ok := specOf(t, docs(t, full)["NetBoxSite/home-lab"])["comments"]; !ok {
		t.Error("--full dropped an empty comments field")
	}
}

func TestBooleanFalseSurvivesMinimal(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))
	spec := specOf(t, docs(t, files)["NetBoxPrefix/10.0.20.0-24"])

	if got, ok := spec["isPool"]; !ok || got != false {
		t.Errorf("isPool = %v (present %v), want false: NetBox defaults are not in the registry", got, ok)
	}
}

func TestCollidingNamesTakeAHashSuffix(t *testing.T) {
	files, result := build(t, options(t.TempDir()))
	all := docs(t, files)

	var vlans []string
	for name := range all {
		if strings.HasPrefix(name, "NetBoxVLAN/") {
			vlans = append(vlans, name)
		}
	}
	if len(vlans) != 2 {
		t.Fatalf("expected two VLANs, got %v", vlans)
	}
	if vlans[0] == vlans[1] {
		t.Fatal("two NetBox objects were exported under one CR name")
	}
	if !hasNote(result.Notes, "collides on name") {
		t.Errorf("the collision was not reported: %v", result.Notes)
	}
}

func TestNamesAreDNSSubdomains(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	for name := range docs(t, files) {
		crName := strings.SplitN(name, "/", 2)[1]
		if !legalName(crName) {
			t.Errorf("%q is not a legal object name", crName)
		}
	}
}

func TestExportIsByteIdentical(t *testing.T) {
	first, _ := build(t, options(t.TempDir()))
	second, _ := build(t, options(t.TempDir()))

	if mustJSON(t, first) != mustJSON(t, second) {
		t.Error("two exports of an unchanged NetBox differ; the diff a human reads would be noise")
	}
}

func TestIDRefsForcesEveryReferenceToAnID(t *testing.T) {
	opts := options(t.TempDir())
	opts.IDRefs = true
	files, _ := build(t, opts)

	spec := specOf(t, docs(t, files)["NetBoxLocation/row-a"])
	if got := spec["siteRef"]; !equalRef(got, map[string]any{"id": 1.0}) {
		t.Errorf("siteRef = %v, want the raw id under --id-refs", got)
	}
}

func TestEveryObjectCarriesTheEndpointRef(t *testing.T) {
	files, _ := build(t, options(t.TempDir()))

	for name, doc := range docs(t, files) {
		if got := specOf(t, doc)["endpointRef"]; got != "homelab" {
			t.Errorf("%s endpointRef = %v, want the --endpoint value", name, got)
		}
	}
}

func TestKindsFilterRejectsAnUnknownKind(t *testing.T) {
	opts := options(t.TempDir())
	opts.Kinds = []string{"NetBoxSite", "NetBoxNope"}

	if _, _, err := export.Build(context.Background(), fixture(), opts); err == nil {
		t.Error("an unknown --kinds value was accepted")
	}
}

func TestTruncatedListAbortsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	client := fakeNetBox{err: &netbox.TruncatedError{Endpoint: "ipam/prefixes", MaxPages: 2, Collected: 500}}

	_, err := export.Run(context.Background(), client, options(dir))

	var truncated *netbox.TruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("Run error = %v, want a *netbox.TruncatedError", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a truncated list still wrote %d files; a partial export is unreviewable", len(entries))
	}
}

func TestExistingOutputIsNotOverwrittenWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "netboxsite.yaml")
	if err := os.WriteFile(path, []byte("reviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := export.Run(context.Background(), fixture(), options(dir)); !errors.Is(err, export.ErrOutputExists) {
		t.Fatalf("Run error = %v, want ErrOutputExists", err)
	}
	if content, _ := os.ReadFile(path); string(content) != "reviewed\n" {
		t.Error("the existing manifest was overwritten")
	}

	opts := options(dir)
	opts.Force = true
	if _, err := export.Run(context.Background(), fixture(), opts); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if content, _ := os.ReadFile(path); string(content) == "reviewed\n" {
		t.Error("--force did not overwrite")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	opts := options(dir)
	opts.DryRun = true

	result, err := export.Run(context.Background(), fixture(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) == 0 {
		t.Error("--dry-run listed no files")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("--dry-run wrote %d files", len(entries))
	}
}

func TestSingleFileSplit(t *testing.T) {
	opts := options(t.TempDir())
	opts.Single = true
	files, _ := build(t, opts)

	if len(files) != 1 || files["export.yaml"] == "" {
		t.Errorf("--split single produced %d files", len(files))
	}
}

// TestExportOnlyReads is the proof that a coding mistake in this package cannot mutate
// NetBox: the server fails the test on anything but a GET, and export is handed the real
// client rather than a fake.
func TestExportOnlyReads(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"results": [], "next": null}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := netbox.New(netbox.Config{URL: server.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := export.Run(context.Background(), client, options(t.TempDir())); err != nil {
		t.Fatal(err)
	}

	for _, method := range methods {
		if method != http.MethodGet {
			t.Errorf("export issued a %s", method)
		}
	}
	if len(methods) == 0 {
		t.Error("export made no requests at all")
	}
}

func TestPaginationFollowsEveryPage(t *testing.T) {
	const total = 2500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/ipam/prefixes/") {
			if _, err := w.Write([]byte(`{"results": [], "next": null}`)); err != nil {
				t.Error(err)
			}

			return
		}
		if _, err := w.Write([]byte(page(r, total, "http://"+r.Host+r.URL.Path))); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := netbox.New(netbox.Config{URL: server.URL, Token: "t", PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	result, err := export.Run(context.Background(), client, options(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Objects != total {
		t.Errorf("exported %d prefixes, want %d", result.Objects, total)
	}
}

// page renders one page of synthetic prefixes, and a next link until they run out.
func page(r *http.Request, total int, base string) string {
	offset := atoi(r.URL.Query().Get("offset"))

	results := make([]map[string]any, 0, 50)
	for i := offset; i < offset+50 && i < total; i++ {
		results = append(results, map[string]any{"id": i + 1, "prefix": cidr(i)})
	}

	body := map[string]any{"results": results}
	if offset+50 < total {
		body["next"] = base + "?limit=50&offset=" + strconv.Itoa(offset+50)
	}
	encoded, _ := json.Marshal(body)

	return string(encoded)
}
