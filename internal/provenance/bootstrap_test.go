package provenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The real client satisfies the narrow interface this package defines. An interface with one
// implementation is a speculation (CONTRIBUTING.md); this is the second one.
var _ Client = (*netbox.Client)(nil)

// call is one request the fake received, in the order it received them.
type call struct {
	method   string
	endpoint string
	id       int
	params   netbox.Params
	payload  netbox.Object
}

// fakeNetBox is a NetBox that holds tags and custom fields in a map and records every
// request, which is how "re-running changes nothing" is asserted: by the absence of a
// second POST rather than by the state afterwards.
type fakeNetBox struct {
	calls []call

	// objects is endpoint -> stored objects. Seeded by a test to stand for a NetBox that
	// already has the definition.
	objects map[string][]netbox.Object

	nextID int

	// createErr, when set, is returned for every POST: a token without
	// extras.add_customfield is the case this stands for.
	createErr error

	// patchErr, when set, is returned for every PATCH.
	patchErr error

	// dryRun suppresses writes through a real DryRun client, so the shape the bootstrap has
	// to recognise comes from the code that produces it rather than from a copy of its
	// marker.
	dryRun *netbox.Client
}

func newFakeNetBox() *fakeNetBox {
	return &fakeNetBox{objects: map[string][]netbox.Object{}, nextID: 40}
}

func (f *fakeNetBox) List(_ context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error) {
	f.calls = append(f.calls, call{method: "LIST", endpoint: endpoint, params: params})

	out := []netbox.Object{}

	for _, obj := range f.objects[endpoint] {
		if matches(obj, params) {
			out = append(out, obj)
		}
	}

	return out, nil
}

func (f *fakeNetBox) Create(ctx context.Context, endpoint string, payload netbox.Object) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "POST", endpoint: endpoint, payload: payload})

	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.dryRun != nil {
		return f.dryRun.Create(ctx, endpoint, payload)
	}

	f.nextID++

	stored := netbox.Object{"id": float64(f.nextID)}
	for key, value := range payload {
		stored[key] = value
	}
	f.objects[endpoint] = append(f.objects[endpoint], stored)

	return stored, nil
}

func (f *fakeNetBox) Patch(ctx context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error) {
	f.calls = append(f.calls, call{method: "PATCH", endpoint: endpoint, id: id, payload: payload})

	if f.patchErr != nil {
		return nil, f.patchErr
	}
	if f.dryRun != nil {
		return f.dryRun.Patch(ctx, endpoint, id, payload)
	}

	for _, obj := range f.objects[endpoint] {
		if stored, ok := obj.ID(); ok && stored == id {
			for key, value := range payload {
				obj[key] = value
			}

			return obj, nil
		}
	}

	return nil, fmt.Errorf("fake netbox has no %s/%d", endpoint, id)
}

func (f *fakeNetBox) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}

	return out
}

func (f *fakeNetBox) posted(endpoint string) []netbox.Object {
	out := []netbox.Object{}

	for _, c := range f.calls {
		if c.method == "POST" && c.endpoint == endpoint {
			out = append(out, c.payload)
		}
	}

	return out
}

func matches(obj netbox.Object, params netbox.Params) bool {
	for name, want := range params {
		if fmt.Sprint(obj[name]) != want {
			return false
		}
	}

	return true
}

// seededTag stands for the tag a NetBox admin created by hand.
func seededTag(f *fakeNetBox) {
	f.nextID++
	f.objects[tagsEndpoint] = append(f.objects[tagsEndpoint],
		netbox.Object{"id": float64(f.nextID), "name": DefaultTag, "slug": DefaultTag})
}

// seededField stands for a definition a NetBox admin created by hand. Each gets a distinct
// id, because a widening PATCH addresses one by id and sharing them would make the test pass
// for the wrong reason.
func seededField(f *fakeNetBox, name string, objectTypes []any) {
	f.nextID++
	f.objects[customFieldsEndpoint] = append(f.objects[customFieldsEndpoint],
		netbox.Object{"id": float64(f.nextID), "name": name, "object_types": objectTypes})
}

var siteAndRegion = []string{"dcim.region", "dcim.site"}

func enabledConfig() Config {
	return FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})
}

func TestBootstrapCreatesEverythingOnce(t *testing.T) {
	fake := newFakeNetBox()

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	if result.Stamp.TagID == 0 {
		t.Error("the tag id was not resolved")
	}
	if want := enabledConfig().CustomFieldNames(); !slices.Equal(result.Stamp.Fields, want) {
		t.Errorf("fields = %v, want %v", result.Stamp.Fields, want)
	}
	if len(result.Created) != 5 {
		t.Errorf("created %v, want the tag and four custom fields", result.Created)
	}
	if len(result.Missing) != 0 {
		t.Errorf("missing = %v, want none", result.Missing)
	}

	// object_types is the field most likely to be got wrong, and getting it wrong makes
	// every write to a type outside the list a 400.
	for _, payload := range fake.posted(customFieldsEndpoint) {
		types, ok := payload["object_types"].([]string)
		if !ok || !slices.Equal(types, siteAndRegion) {
			t.Errorf("custom field %v declared object_types %v, want %v",
				payload["name"], payload["object_types"], siteAndRegion)
		}
		if payload["type"] != customFieldType {
			t.Errorf("custom field %v has type %v, want %q", payload["name"], payload["type"], customFieldType)
		}
	}

	// The tag's own object_types is deliberately unset: restricting it to the kinds
	// registered today makes the first object of a kind added tomorrow unstampable.
	for _, payload := range fake.posted(tagsEndpoint) {
		if _, restricted := payload["object_types"]; restricted {
			t.Errorf("the tag was created with object_types %v; it must be unrestricted",
				payload["object_types"])
		}
		if payload["name"] != payload["slug"] {
			t.Errorf("tag name %v and slug %v differ", payload["name"], payload["slug"])
		}
	}
}

// TestBootstrapIsIdempotent is the acceptance criterion "re-running changes nothing", and it
// is asserted on the requests rather than on the resulting state: a second POST against a
// unique column is a 400, so the absence of one is the whole property.
func TestBootstrapIsIdempotent(t *testing.T) {
	fake := newFakeNetBox()
	cfg := enabledConfig()

	first, err := Bootstrap(context.Background(), fake, cfg, siteAndRegion)
	if err != nil {
		t.Fatalf("first Bootstrap() = %v", err)
	}

	fake.calls = nil

	second, err := Bootstrap(context.Background(), fake, cfg, siteAndRegion)
	if err != nil {
		t.Fatalf("second Bootstrap() = %v", err)
	}

	if slices.Contains(fake.methods(), "POST") || slices.Contains(fake.methods(), "PATCH") {
		t.Errorf("the second pass wrote: %v", fake.methods())
	}
	if len(second.Created) != 0 || len(second.Widened) != 0 {
		t.Errorf("second pass created %v and widened %v, want neither", second.Created, second.Widened)
	}
	if second.Stamp.TagID != first.Stamp.TagID {
		t.Errorf("tag id moved between passes: %d then %d", first.Stamp.TagID, second.Stamp.TagID)
	}
}

// TestBootstrapAdoptsWhatIsAlreadyThere is the case where a NetBox admin created the tag and
// the fields by hand, and the operator must resolve rather than duplicate them.
func TestBootstrapAdoptsWhatIsAlreadyThere(t *testing.T) {
	fake := newFakeNetBox()
	seededTag(fake)

	for _, name := range enabledConfig().CustomFieldNames() {
		seededField(fake, name, []any{"dcim.region", "dcim.site"})
	}

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	if result.Stamp.TagID != 41 {
		t.Errorf("tag id = %d, want the existing 41", result.Stamp.TagID)
	}
	if slices.Contains(fake.methods(), "POST") {
		t.Errorf("bootstrap wrote against a netbox that already had everything: %v", fake.methods())
	}
}

// TestBootstrapWidensObjectTypes is the answer to "what happens when a kind is registered
// after bootstrap ran": the definition in NetBox does not cover the new type, and the first
// write to it would be a 400 naming a custom field the user can see in the UI.
func TestBootstrapWidensObjectTypes(t *testing.T) {
	fake := newFakeNetBox()
	seededTag(fake)

	for _, name := range enabledConfig().CustomFieldNames() {
		// Created when only dcim.site existed.
		seededField(fake, name, []any{"dcim.site"})
	}

	result, err := Bootstrap(context.Background(), fake, enabledConfig(),
		[]string{"dcim.region", "dcim.site", "ipam.prefix"})
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	if len(result.Widened) != 4 {
		t.Errorf("widened %v, want all four custom fields", result.Widened)
	}

	want := []string{"dcim.region", "dcim.site", "ipam.prefix"}

	for _, c := range fake.calls {
		if c.method != "PATCH" {
			continue
		}

		types, ok := c.payload["object_types"].([]string)
		if !ok || !slices.Equal(types, want) {
			t.Errorf("patched object_types = %v, want %v", c.payload["object_types"], want)
		}
	}

	// A second pass now finds every type covered and must send nothing.
	fake.calls = nil
	if _, err := Bootstrap(context.Background(), fake, enabledConfig(),
		[]string{"dcim.region", "dcim.site", "ipam.prefix"}); err != nil {
		t.Fatalf("second Bootstrap() = %v", err)
	}
	if slices.Contains(fake.methods(), "PATCH") {
		t.Error("a widened definition was widened again")
	}
}

// TestBootstrapNeverNarrows: the definition may be shared with a NetBox admin's own use of
// it, and narrowing somebody else's schema on a resync is not a thing an operator should do.
func TestBootstrapNeverNarrows(t *testing.T) {
	fake := newFakeNetBox()
	seededTag(fake)

	for _, name := range enabledConfig().CustomFieldNames() {
		seededField(fake, name, []any{"dcim.region", "dcim.site", "tenancy.tenant"})
	}

	if _, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion); err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	if slices.Contains(fake.methods(), "PATCH") {
		t.Errorf("bootstrap narrowed a definition covering a type it does not manage: %v", fake.methods())
	}
}

func TestBootstrapDisabled(t *testing.T) {
	off := false
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu", Bootstrap: &off})

	fake := newFakeNetBox()

	result, err := Bootstrap(context.Background(), fake, cfg, siteAndRegion)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Bootstrap() = %v, want ErrIncomplete", err)
	}

	if slices.Contains(fake.methods(), "POST") {
		t.Errorf("bootstrap wrote with bootstrap disabled: %v", fake.methods())
	}
	if result.Stamp.Applicable() {
		t.Error("an incomplete bootstrap handed back a usable stamp")
	}

	// The condition has to name what a human must create, or "disabled" is not actionable.
	if len(result.Missing) != 5 {
		t.Errorf("missing = %v, want the tag and four custom fields", result.Missing)
	}
	for _, want := range []string{"tag k8s-managed", "custom field k8s_uid"} {
		if !slices.Contains(result.Missing, want) {
			t.Errorf("missing %v does not name %q", result.Missing, want)
		}
	}
}

// TestBootstrapDisabledButProvisioned: bootstrap off is not the same as provenance off. With
// the definitions already in NetBox the stamp resolves and the endpoint is fine.
func TestBootstrapDisabledButProvisioned(t *testing.T) {
	off := false
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu", Bootstrap: &off})

	fake := newFakeNetBox()
	seededTag(fake)

	for _, name := range cfg.CustomFieldNames() {
		seededField(fake, name, []any{"dcim.region", "dcim.site"})
	}

	result, err := Bootstrap(context.Background(), fake, cfg, siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	if !result.Stamp.Applicable() {
		t.Error("the stamp is not applicable against a netbox that has every definition")
	}
}

func TestBootstrapRefused(t *testing.T) {
	// What a token without extras.add_customfield actually returns.
	refused := &netbox.AuthError{}

	fake := newFakeNetBox()
	fake.createErr = refused

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion)
	if !errors.As(err, &refused) {
		t.Fatalf("Bootstrap() = %v, want the client's own error type", err)
	}
	if errors.Is(err, ErrIncomplete) {
		t.Error("a refused bootstrap classified as a disabled one; the two have different fixes")
	}
	if result.Stamp.Applicable() {
		t.Error("a failed bootstrap handed back a usable stamp")
	}
}

// TestWideningRefused: a token with extras.add_customfield but not extras.change_customfield
// can create a definition and not widen one, and the endpoint has to fail rather than let
// objects of the uncovered type discover it one 400 at a time.
func TestWideningRefused(t *testing.T) {
	refused := &netbox.AuthError{}

	fake := newFakeNetBox()
	fake.patchErr = refused
	seededTag(fake)

	for _, name := range enabledConfig().CustomFieldNames() {
		seededField(fake, name, []any{"dcim.site"})
	}

	if _, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion); !errors.As(err, &refused) {
		t.Fatalf("Bootstrap() = %v, want the client's own error type", err)
	}
}

// TestBootstrapSuppressed: a DryRun or driftMode: Report endpoint cannot create anything, and
// that must not fail the endpoint -- an endpoint that sends nothing cannot produce the 400
// this gate exists to prevent.
func TestBootstrapSuppressed(t *testing.T) {
	client, err := netbox.New(netbox.Config{URL: "https://netbox.invalid", Mode: netbox.ModeDryRun})
	if err != nil {
		t.Fatalf("building a dry-run client: %v", err)
	}

	fake := newFakeNetBox()
	fake.dryRun = client

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v, want a reported result rather than a failure", err)
	}
	if !result.Suppressed {
		t.Error("result.Suppressed is false on an endpoint that cannot write")
	}
	if result.Stamp.Applicable() {
		t.Error("a suppressed bootstrap handed back a usable stamp")
	}
	if len(result.Missing) == 0 {
		t.Error("a suppressed bootstrap reported nothing missing")
	}
}

// TestWideningSuppressed: a non-writing endpoint cannot widen a definition either, and must
// not claim it did.
func TestWideningSuppressed(t *testing.T) {
	client, err := netbox.New(netbox.Config{URL: "https://netbox.invalid", Mode: netbox.ModeDryRun})
	if err != nil {
		t.Fatalf("building a dry-run client: %v", err)
	}

	fake := newFakeNetBox()
	fake.dryRun = client
	seededTag(fake)

	for _, name := range enabledConfig().CustomFieldNames() {
		seededField(fake, name, []any{"dcim.site"})
	}

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v, want a reported result rather than a failure", err)
	}
	if len(result.Widened) != 0 {
		t.Errorf("widened = %v on an endpoint that cannot write", result.Widened)
	}
	if !result.Suppressed {
		t.Error("result.Suppressed is false after a suppressed widening")
	}
}

// TestBootstrapWithNoObjectTypes is the state a registry with no CustomFieldable kind leaves:
// extras.CustomField.object_types is required, so there is nothing to create. The tag still
// works, which is a coherent outcome rather than a failure.
func TestBootstrapWithNoObjectTypes(t *testing.T) {
	fake := newFakeNetBox()

	result, err := Bootstrap(context.Background(), fake, enabledConfig(), nil)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}

	if !result.Stamp.Applicable() {
		t.Error("the tag half of the stamp did not resolve")
	}
	if len(result.Stamp.Fields) != 0 {
		t.Errorf("fields = %v, want none", result.Stamp.Fields)
	}
	if posted := fake.posted(customFieldsEndpoint); len(posted) != 0 {
		t.Errorf("created custom fields with no object type to declare: %v", posted)
	}
}

func TestBootstrapDisabledEntirely(t *testing.T) {
	fake := newFakeNetBox()

	result, err := Bootstrap(context.Background(), fake, Config{}, siteAndRegion)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("an endpoint with no spec.managedBy issued %v", fake.methods())
	}
	if result.Stamp.Applicable() {
		t.Error("a disabled config produced a usable stamp")
	}
}

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"k8s_uid":                 "K8s uid",
		"k8s_allocation_identity": "K8s allocation identity",
		"":                        "",
	}

	for name, want := range cases {
		if got := label(name); got != want {
			t.Errorf("label(%q) = %q, want %q", name, got, want)
		}
	}
}
