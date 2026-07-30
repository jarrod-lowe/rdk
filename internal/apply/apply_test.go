package apply

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

func setupRepo(t *testing.T) (repofs.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rdk"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.yaml": "kind: config\nname: demo\n",
		"assets.yaml": "kind: s3-bucket\nname: assets\ndescription: Static assets\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, "rdk", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := repofs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestRunMaterializesManagedTree(t *testing.T) {
	store, root := setupRepo(t)
	res, err := Run(store, "test-version")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FilesWritten == 0 {
		t.Error("FilesWritten = 0")
	}
	for _, p := range []string{
		"rdk-managed/README.md",
		"rdk-managed/manifest.json",
		"rdk-managed/terraform/main.tf.json",
		"rdk-managed/terraform/modules/s3-bucket/main.tf",
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

func TestRunReplacesStrayManagedFiles(t *testing.T) {
	store, root := setupRepo(t)
	if _, err := Run(store, "v"); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "rdk-managed", "stray.txt")
	if err := os.WriteFile(stray, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(store, "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Error("stray file survived apply")
	}
}

func TestRunFailsWithoutDefinitions(t *testing.T) {
	root := t.TempDir()
	store, err := repofs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(store, "v"); err == nil {
		t.Error("want error when rdk/ is missing")
	}
}

// The summary is a diagnostic like any other, so JSONL consumers get the
// counts as attrs rather than having to parse the sentence.
func TestResultDiagnosticCarriesCounts(t *testing.T) {
	d := Result{FilesWritten: 4}.Diagnostic()
	if d.Code != diag.CodeApplyComplete {
		t.Errorf("Code = %q, want %q", d.Code, diag.CodeApplyComplete)
	}
	if want := "rdk apply: wrote 4 files to rdk-managed/"; d.Summary != want {
		t.Errorf("Summary = %q, want %q", d.Summary, want)
	}
	// A run-wide result has no file to name; a File here would prefix the
	// stdout line with a path that means nothing to the reader.
	if d.File != "" {
		t.Errorf("File = %q, want empty", d.File)
	}
	attrs := map[string]any{}
	for _, a := range d.Attrs {
		attrs[a.Key] = a.Value()
	}
	if attrs["files"] != 4 {
		t.Errorf("files attr = %v, want 4", attrs["files"])
	}
	if attrs["dir"] != ManagedDir {
		t.Errorf("dir attr = %v, want %q", attrs["dir"], ManagedDir)
	}
}
