package parse

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
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
	defs, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket}), "rdk")
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
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: volcano\nname: x\n"}), "rdk")
	errContains(t, err, "x.yaml", "volcano", "unknown kind")
}

// Naming the alternatives turns a dead end into a next step; the registry
// already knows them, so there is no reason to make the author go looking.
func TestUnknownKindListsKnownKinds(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: volcano\nname: x\n"}), "rdk")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	for _, want := range []string{"known kinds", "config", "s3-bucket"} {
		if !strings.Contains(d.Hint, want) {
			t.Errorf("hint %q does not mention %q", d.Hint, want)
		}
	}
}

// A near-miss is the common case for an unknown kind, so say the likely fix.
func TestUnknownKindSuggestsNearMatch(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-buckets\nname: x\ndescription: d\n"}), "rdk")
	errContains(t, err, "did you mean", "s3-bucket")
}

func TestUnknownField(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\ndescription: d\ncolour: red\n"}), "rdk")
	errContains(t, err, "x.yaml", "colour", "unknown field")
}

func TestUnknownFieldListsValidFields(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\ndescription: d\ncolour: red\n"}), "rdk")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	for _, want := range []string{"valid fields", "name", "description"} {
		if !strings.Contains(d.Hint, want) {
			t.Errorf("hint %q does not mention %q", d.Hint, want)
		}
	}
}

func TestUnknownFieldSuggestsNearMatch(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\ndescriptoin: d\n"}), "rdk")
	errContains(t, err, "descriptoin", "did you mean", "description")
}

// The library rejects duplicate keys by default; pin it, because the default is
// one AllowDuplicateMapKey() away from flipping on a dependency bump.
func TestDuplicateFieldRejected(t *testing.T) {
	dup := "kind: s3-bucket\nname: assets\ndescription: First\ndescription: Second\n"
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": dup}), "rdk")
	errContains(t, err, "x.yaml", "description", "already defined")
}

// "missing 'kind'" is the wrong diagnosis when kind is present but not a string.
func TestKindMustBeAString(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: 3\nname: x\n"}), "rdk")
	errContains(t, err, "x.yaml", "kind", "must be a string", "number")
}

func TestEmptyFileRejected(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": ""}), "rdk")
	errContains(t, err, "x.yaml", "empty")
}

// Go's type names are an implementation detail; an author writing YAML should be
// told the YAML type they wrote.
func TestWrongTypeNamesYAMLType(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\ndescription: 123\n"}), "rdk")
	errContains(t, err, "x.yaml", "description", "must be a string", "number")
	if strings.Contains(err.Error(), "uint64") {
		t.Errorf("error leaks a Go type name: %q", err.Error())
	}
}

// A definition in a .yml file was silently ignored: a valid resource, dropped,
// exit 0. Extension typos fail loudly like any other malformed definition.
func TestYmlExtensionRejected(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yml": goodBucket}), "rdk")
	errContains(t, err, "assets.yml", ".yaml")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if !strings.Contains(d.Hint, "assets.yaml") {
		t.Errorf("hint %q does not name the corrected filename", d.Hint)
	}
}

// rdk/ holds definitions and nothing else: a file rdk cannot process is a
// mistake to surface, not clutter to step around.
func TestUnprocessableFileRejected(t *testing.T) {
	for _, name := range []string{"README.md", "notes.txt", "deploy.sh"} {
		_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, name: "whatever\n"}), "rdk")
		errContains(t, err, name, "cannot process")
	}
}

// Nesting is not part of the definitions model, and a directory full of
// definitions would otherwise be skipped in full.
func TestDirectoryRejected(t *testing.T) {
	m := repofs.NewMem()
	if err := m.Seed("rdk/config.yaml", []byte(goodConfig)); err != nil {
		t.Fatal(err)
	}
	if err := m.Seed("rdk/buckets/assets.yaml", []byte(goodBucket)); err != nil {
		t.Fatal(err)
	}
	_, _, err := Dir(m, "rdk")
	errContains(t, err, "buckets", "directories")
}

// Setting a definition aside is deliberate, so it is allowed — but it changes
// what rdk generates, so it is never silent.
func TestSetAsideFilesWarn(t *testing.T) {
	for _, name := range []string{"logs.yaml.disabled", "bucket.yaml.example"} {
		defs, warnings, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket, name: goodBucket}), "rdk")
		if err != nil {
			t.Fatalf("%s: Dir: %v", name, err)
		}
		if len(defs) != 2 {
			t.Errorf("%s: got %d definitions, want 2", name, len(defs))
		}
		if len(warnings) != 1 {
			t.Fatalf("%s: got %d warnings, want 1", name, len(warnings))
		}
		if warnings[0].File != name {
			t.Errorf("%s: warning names file %q", name, warnings[0].File)
		}
		if warnings[0].Code != diag.CodeSetAside {
			t.Errorf("%s: code = %q, want %q", name, warnings[0].Code, diag.CodeSetAside)
		}
		if !strings.Contains(warnings[0].Summary, filepath.Ext(name)) {
			t.Errorf("%s: summary %q does not name the suffix that matched", name, warnings[0].Summary)
		}
	}
}

// A stale .orig may hold work someone still wants; say it is there.
func TestEditorArtifactsWarn(t *testing.T) {
	cases := []struct{ name, suffix string }{
		{"assets.yaml.orig", ".orig"},
		{"assets.yaml.rej", ".rej"},
		{"assets.yaml.bak", ".bak"},
		{"assets.yaml~", "~"},
	}
	for _, c := range cases {
		_, warnings, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, c.name: goodBucket}), "rdk")
		if err != nil {
			t.Fatalf("%s: Dir: %v", c.name, err)
		}
		if len(warnings) != 1 {
			t.Fatalf("%s: got %d warnings, want 1", c.name, len(warnings))
		}
		if warnings[0].File != c.name {
			t.Errorf("%s: warning names file %q", c.name, warnings[0].File)
		}
		if warnings[0].Code != diag.CodeEditorArtifact {
			t.Errorf("%s: code = %q, want %q", c.name, warnings[0].Code, diag.CodeEditorArtifact)
		}
		if !strings.Contains(warnings[0].Summary, c.suffix) {
			t.Errorf("%s: summary %q does not name the suffix that matched", c.name, warnings[0].Summary)
		}
	}
}

// Finder writes .DS_Store on its own; warning about it every apply would train
// people to ignore warnings, and erroring would break apply outright.
func TestHiddenFilesSilentlyIgnored(t *testing.T) {
	files := map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket,
		".DS_Store": "\x00", ".gitignore": "*.tfstate\n", ".gitkeep": "", ".assets.yaml.swp": "vim"}
	defs, warnings, err := Dir(memWith(t, files), "rdk")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

// Warnings are output; DD-1 applies to them as much as to generated files.
func TestWarningsAreSorted(t *testing.T) {
	files := map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket,
		"c.yaml.disabled": goodBucket, "a.yaml.disabled": goodBucket, "b.yaml.example": goodBucket}
	_, warnings, err := Dir(memWith(t, files), "rdk")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	names := make([]string, len(warnings))
	for i, w := range warnings {
		names[i] = w.File
	}
	want := []string{"a.yaml.disabled", "b.yaml.example", "c.yaml.disabled"}
	if len(names) != len(want) {
		t.Fatalf("got %d warnings, want %d: %v", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("warnings[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestMissingRequiredField(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: x\n"}), "rdk")
	errContains(t, err, "x.yaml", "description", "required", "What this bucket is for")
}

func TestDuplicateName(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "a.yaml": goodBucket, "b.yaml": goodBucket}), "rdk")
	errContains(t, err, "duplicate", "assets")
}

func TestExactlyOneConfig(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"a.yaml": goodBucket}), "rdk")
	errContains(t, err, "config")
}

// A multi-document file is a natural thing to write coming from Kubernetes, and
// decoding silently kept only the first document — a partial apply with no error.
func TestMultiDocumentRejected(t *testing.T) {
	multi := goodBucket + "---\nkind: s3-bucket\nname: logs\ndescription: Log storage\n"
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "multi.yaml": multi}), "rdk")
	errContains(t, err, "multi.yaml", "---")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if !strings.Contains(d.Hint, "one definition per file") {
		t.Errorf("hint %q does not explain the fix", d.Hint)
	}
}

// A leading separator is ordinary YAML style for a single document, not a second one.
func TestLeadingDocumentSeparatorAccepted(t *testing.T) {
	defs, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": "---\n" + goodBucket}), "rdk")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	if defs[0].Name != "assets" {
		t.Errorf("defs[0].Name = %q, want assets", defs[0].Name)
	}
}

// A trailing separator ends the document; there is no second definition to lose.
func TestTrailingDocumentSeparatorAccepted(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "assets.yaml": goodBucket + "---\n"}), "rdk")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
}

func TestEmptyRequiredFieldRejected(t *testing.T) {
	_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, "x.yaml": "kind: s3-bucket\nname: \"\"\ndescription: d\n"}), "rdk")
	errContains(t, err, "x.yaml", "name", "empty")
}

// The code is the part a machine can match on, so it has to be set at the
// point of failure, not reconstructed later.
func TestErrorsCarryTheirCode(t *testing.T) {
	cases := []struct {
		file, body, code string
	}{
		{"x.yaml", "kind: volcano\nname: x\n", diag.CodeUnknownKind},
		{"x.yaml", "kind: s3-bucket\nname: x\ndescription: d\ncolour: red\n", diag.CodeUnknownField},
		{"x.yaml", "", diag.CodeEmptyFile},
		{"notes.txt", "scratch\n", diag.CodeUnprocessableFile},
		{"assets.yml", goodBucket, diag.CodeWrongExtension},
	}
	for _, c := range cases {
		_, _, err := Dir(memWith(t, map[string]string{"config.yaml": goodConfig, c.file: c.body}), "rdk")
		var d *diag.Error
		if !errors.As(err, &d) {
			t.Fatalf("%s: error is not a diagnostic: %v", c.file, err)
		}
		if d.Code != c.code {
			t.Errorf("%s: code = %q, want %q", c.file, d.Code, c.code)
		}
		if d.File != c.file {
			t.Errorf("%s: file = %q", c.file, d.File)
		}
	}
}
