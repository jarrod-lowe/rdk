package s3bucket

import (
	"io/fs"
	"sort"
	"testing"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "s3-bucket" {
		t.Errorf("Name() = %q, want s3-bucket", got)
	}
}

func TestSchemaFieldsRequiredAndDocumented(t *testing.T) {
	s := New().Schema()
	for _, want := range []string{"name", "description"} {
		f, ok := s.Field(want)
		if !ok {
			t.Fatalf("Schema missing field %q", want)
		}
		if !f.Required {
			t.Errorf("field %q: want required", want)
		}
		if f.Description == "" {
			t.Errorf("field %q: empty description (DD-13)", want)
		}
	}
}

func TestModuleCallIsIdentityAndCopies(t *testing.T) {
	attrs := map[string]any{"name": "assets", "description": "Static assets"}
	inputs, err := New().ModuleCall(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["name"] != "assets" || inputs["description"] != "Static assets" {
		t.Errorf("inputs = %v, want identity of attrs", inputs)
	}
	// Must be a fresh map, never an alias of the caller's attrs.
	inputs["name"] = "mutated"
	if attrs["name"] != "assets" {
		t.Error("ModuleCall aliased the caller's attrs map")
	}
}

func TestModuleFSHasExpectedFiles(t *testing.T) {
	var got []string
	err := fs.WalkDir(New().ModuleFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"main.tf", "outputs.tf", "variables.tf"}
	if len(got) != len(want) {
		t.Fatalf("module files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("module files = %v, want %v", got, want)
			break
		}
	}
}
