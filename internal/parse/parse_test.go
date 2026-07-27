package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDefs(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const goodConfig = "kind: config\nname: demo\n"
const goodBucket = "kind: s3-bucket\nname: assets\ndescription: Static assets\n"

func TestParseValid(t *testing.T) {
	dir := writeDefs(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket})
	defs, err := Dir(dir)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	// files are read in sorted order: assets.yaml first
	if defs[0].Kind != "s3-bucket" || defs[0].Name != "assets" {
		t.Errorf("defs[0] = %s/%s, want s3-bucket/assets", defs[0].Kind, defs[0].Name)
	}
	if defs[0].Attrs["description"] != "Static assets" {
		t.Errorf("description attr = %v", defs[0].Attrs["description"])
	}
}

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

func TestUnknownKind(t *testing.T) {
	dir := writeDefs(t, map[string]string{"config.yaml": goodConfig,
		"x.yaml": "kind: volcano\nname: x\n"})
	_, err := Dir(dir)
	errContains(t, err, "x.yaml", "volcano", "unknown kind")
}

func TestUnknownField(t *testing.T) {
	dir := writeDefs(t, map[string]string{"config.yaml": goodConfig,
		"x.yaml": "kind: s3-bucket\nname: x\ndescription: d\ncolour: red\n"})
	_, err := Dir(dir)
	errContains(t, err, "x.yaml", "colour", "unknown field")
}

func TestMissingRequiredField(t *testing.T) {
	dir := writeDefs(t, map[string]string{"config.yaml": goodConfig,
		"x.yaml": "kind: s3-bucket\nname: x\n"})
	_, err := Dir(dir)
	errContains(t, err, "x.yaml", "description", "required", "What this bucket is for")
}

func TestDuplicateName(t *testing.T) {
	dir := writeDefs(t, map[string]string{"config.yaml": goodConfig,
		"a.yaml": goodBucket, "b.yaml": goodBucket})
	_, err := Dir(dir)
	errContains(t, err, "duplicate", "assets")
}

func TestExactlyOneConfig(t *testing.T) {
	dir := writeDefs(t, map[string]string{"a.yaml": goodBucket})
	_, err := Dir(dir)
	errContains(t, err, "config")
}
