package parse

import (
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/repofs"
)

// memWith seeds an in-memory store with definition files under rdk/.
func memWith(t *testing.T, files map[string]string) repofs.Store {
	t.Helper()
	m := repofs.NewMem()
	for name, body := range files {
		if err := m.Seed("rdk/"+name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

const goodConfig = "kind: config\nname: demo\n"
const goodBucket = "kind: s3-bucket\nname: assets\ndescription: Static assets\n"

func errContains(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err.Error(), w)
		}
	}
}

func TestParseValid(t *testing.T) {
	defs, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket}), "rdk")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	// ReadDir returns sorted names: assets.yaml before config.yaml.
	if defs[0].Kind != "s3-bucket" || defs[0].Name != "assets" {
		t.Errorf("defs[0] = %s/%s, want s3-bucket/assets", defs[0].Kind, defs[0].Name)
	}
	if defs[0].Attrs["description"] != "Static assets" {
		t.Errorf("description attr = %v", defs[0].Attrs["description"])
	}
}

func TestUnknownKind(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: volcano\nname: x\n"}), "rdk")
	errContains(t, err, "x.yaml", "volcano", "unknown kind")
}

func TestUnknownField(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\ndescription: d\ncolour: red\n"}), "rdk")
	errContains(t, err, "x.yaml", "colour", "unknown field")
}

func TestMissingRequiredField(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\n"}), "rdk")
	errContains(t, err, "x.yaml", "description", "required", "What this bucket is for")
}

func TestDuplicateName(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "a.yaml": goodBucket, "b.yaml": goodBucket}), "rdk")
	errContains(t, err, "duplicate", "assets")
}

func TestExactlyOneConfig(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"a.yaml": goodBucket}), "rdk")
	errContains(t, err, "config")
}

func TestEmptyRequiredFieldRejected(t *testing.T) {
	_, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: \"\"\ndescription: d\n"}), "rdk")
	errContains(t, err, "x.yaml", "name", "empty")
}
