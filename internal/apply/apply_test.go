package apply

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// repofs cannot import apply (apply already imports repofs) to see
// ManagedDir, so repofs.AtRepoRoot's guard against colliding with the managed
// dir duplicates the name as an unexported constant. This is the one place
// that can see both, so it is the one place that can catch drift between them.
func TestRepofsManagedDirNameMatchesApplyManagedDir(t *testing.T) {
	set := repofs.NewFileSet()
	if err := set.Bytes(repofs.AtRepoRoot(ManagedDir+"/x.txt"), []byte("x")); err == nil {
		t.Errorf("repofs.AtRepoRoot did not reject a path inside %q; its managedDirName constant has drifted from apply.ManagedDir", ManagedDir)
	}
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

// This failure happens after the tree is already correct, so the message has
// to say so — otherwise the reader goes looking for damage that is not there,
// or starts deleting rdk-managed/ to fix a problem that does not exist.
func TestSweepFailureSaysTheApplySucceeded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	store, root := setupRepo(t)
	if _, err := Run(store, "v"); err != nil {
		t.Fatal(err)
	}
	// After the displacing rename this becomes .rdk/old/terraform, whose
	// contents cannot be unlinked, so only the final sweep fails.
	stuck := filepath.Join(root, ManagedDir, "terraform")
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(stuck, 0o700)
		os.Chmod(filepath.Join(root, repofs.ScratchDir, "old", "terraform"), 0o700)
	})

	_, err := Run(store, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeScratchNotRemoved {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeScratchNotRemoved)
	}
	if !strings.Contains(d.Summary, "wrote") {
		t.Errorf("summary %q does not lead with the apply having succeeded", d.Summary)
	}
	if !strings.Contains(d.Hint, "correct") {
		t.Errorf("hint %q does not say the tree is correct", d.Hint)
	}
	// Exit 1: a locked file is environmental, not an rdk bug.
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
	// The published tree really is correct — that is what the message claims.
	if _, err := os.Stat(filepath.Join(root, ManagedDir, "terraform", "main.tf.json")); err != nil {
		t.Errorf("published tree is not intact: %v", err)
	}
}

// A .rdk symlinked to the repo root would otherwise send the scratch
// .gitignore write and the scratch deletes onto the user's own files (the bug
// this diagnostic exists to report), so the message has to name .rdk and say
// to remove it — a bare "cannot write rdk-managed/" leaves no way to act on it.
func TestScratchTargetNamesRdkAndSaysToRemoveIt(t *testing.T) {
	store, root := setupRepo(t)
	if err := os.Symlink(".", filepath.Join(root, repofs.ScratchDir)); err != nil {
		t.Fatal(err)
	}
	_, err := Run(store, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeScratchTarget {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeScratchTarget)
	}
	if !strings.Contains(d.Line(), repofs.ScratchDir) {
		t.Errorf("rendered message %q does not name %s", d.Line(), repofs.ScratchDir)
	}
	if !strings.Contains(d.Hint, "remove "+repofs.ScratchDir) {
		t.Errorf("hint %q does not tell the user to remove %s", d.Hint, repofs.ScratchDir)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
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
