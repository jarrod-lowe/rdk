package kind

import (
	"sort"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/schema"
)

func TestLookupConfig(t *testing.T) {
	k, ok := Lookup("config")
	if !ok {
		t.Fatal(`Lookup("config"): not found`)
	}
	if k.Name() != "config" {
		t.Errorf("Name() = %q, want config", k.Name())
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("volcano"); ok {
		t.Error("Lookup(volcano): want not found")
	}
}

func TestConfigIsNotAResource(t *testing.T) {
	k, _ := Lookup("config")
	if _, ok := k.(Resource); ok {
		t.Error("config must implement Kind but not Resource (it produces no module)")
	}
}

func TestAllSortedAndDocumented(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() is empty")
	}
	names := make([]string, len(all))
	for i, k := range all {
		names[i] = k.Name()
		// DD-13: a kind without complete field docs is unfinished.
		s := k.Schema()
		if s.Description == "" {
			t.Errorf("%s: empty kind description", k.Name())
		}
		for _, f := range s.Fields {
			if f.Description == "" {
				t.Errorf("%s.%s: empty field description (DD-13)", k.Name(), f.Name)
			}
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("All() not sorted by name: %v", names)
	}
}

func TestBuildRegistryRejectsDuplicates(t *testing.T) {
	if _, err := buildRegistry(named("x"), named("x")); err == nil {
		t.Error("buildRegistry: want error on duplicate name")
	}
}

func TestBuildRegistryIndexesByName(t *testing.T) {
	reg, err := buildRegistry(named("a"), named("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg["a"]; !ok {
		t.Error(`buildRegistry: missing "a"`)
	}
	if len(reg) != 2 {
		t.Errorf("len = %d, want 2", len(reg))
	}
}

func TestValidateNilForRealRegistry(t *testing.T) {
	if err := Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for the real registry", err)
	}
}

func TestS3BucketIsAResource(t *testing.T) {
	k, ok := Lookup("s3-bucket")
	if !ok {
		t.Fatal(`Lookup("s3-bucket"): not found`)
	}
	if _, ok := k.(Resource); !ok {
		t.Error("s3-bucket must implement Resource")
	}
}

// named is a minimal Kind stub for registry tests.
type named string

func (n named) Name() string      { return string(n) }
func (named) Schema() schema.Kind { return schema.Kind{} }
