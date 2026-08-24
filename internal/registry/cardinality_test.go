package registry

import (
	"reflect"
	"testing"
)

// TestComparisonSetsAreDerivedFromFieldClasses is the structural half of NBO-088.
//
// The three comparison sets used to be lists declared beside the field map, which is how a
// to-many reference came to be described twice -- once for resolution as `Ref: true`, once for
// comparison as an entry in `M2M` -- with nothing joining them. They are now read off
// Field.Class, so there is no second declaration left to disagree with the first.
//
// ipam.VRF is the fixture that made the problem concrete: `import_targets` and
// `export_targets` are ManyToManyFields onto ipam.RouteTarget (docs/netbox-schema.md ->
// ipam.VRF), and they were declared as references with no cardinality and as M2M columns, so
// the resolver skipped them and the engine reported them NotImplemented.
func TestComparisonSetsAreDerivedFromFieldClasses(t *testing.T) {
	vrf := vrfDescriptor()

	if got, want := vrf.M2MFields(), []string{"import_targets", "export_targets"}; !reflect.DeepEqual(got, want) {
		t.Errorf("M2MFields() = %v, want %v", got, want)
	}

	if got := vrf.ObjectTypeListFields(); len(got) != 0 {
		t.Errorf("ObjectTypeListFields() = %v, want none", got)
	}

	tag := tagDescriptor()

	if got, want := tag.ObjectTypeListFields(), []string{"object_types"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ObjectTypeListFields() = %v, want %v", got, want)
	}

	if got := tag.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none: object_types holds content types, not object ids", got)
	}
}

// TestToManyReferencesAreResolvable is the acceptance criterion, stated as the fixture the
// ticket named: `importTargets` and `exportTargets` are declared to-many references, so the
// resolver dispatches on them rather than skipping them.
//
// One class carries both facts, which is what makes the two answers below impossible to
// disagree. A test that read `Ref` and `M2M` separately could pass while they contradicted
// each other; this one cannot.
func TestToManyReferencesAreResolvable(t *testing.T) {
	vrf := vrfDescriptor()

	for _, spec := range []string{"importTargets", "exportTargets"} {
		field, ok := vrf.FieldFor(spec)
		if !ok {
			t.Fatalf("FieldFor(%q) = false, want the fixture to declare it", spec)
		}

		if !field.Class.Ref() {
			t.Errorf("%s class = %q, want a reference the resolver dispatches on", spec, field.Class)
		}

		if !field.Class.ToMany() {
			t.Errorf("%s class = %q, want to-many: it is a list of route targets", spec, field.Class)
		}
	}

	// And the to-one reference on the same kind is still to-one, so the class is per field
	// rather than per descriptor.
	tenant, _ := vrf.FieldFor("tenantRef")
	if !tenant.Class.Ref() || tenant.Class.ToMany() {
		t.Errorf("tenantRef class = %q, want a to-one reference", tenant.Class)
	}
}

// TestValidateAcceptsEveryClass is the boot check's positive case: each class the engine
// implements is accepted, so the closed set in fieldClasses and the constants cannot drift
// apart without this failing.
func TestValidateAcceptsEveryClass(t *testing.T) {
	for _, class := range fieldClasses {
		d := tagDescriptor()
		d.Fields = append(d.Fields, Field{Spec: "extra", API: "extra", Class: class})

		if class.Ref() {
			d.Fields[len(d.Fields)-1].Target = testGVK("NetBoxTag")
		}

		if err := d.Validate(); err != nil {
			t.Errorf("Validate() with class %q = %v, want nil", class, err)
		}
	}
}
