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

// The blocked-apply error is exactly where an agent looks for a way past a
// lock, so it has to hand over the id, while carrying the holder's details as
// attrs a JSONL consumer can match on directly rather than parsing the
// sentence. An apply lock is nothing an agent could ever legitimately hold
// (--with-lock is only for a held lock), so its hint says nothing about
// --with-lock at all rather than warning off a door that was never relevant.
func TestRunReportsAnExistingApplyLockWithAttrs(t *testing.T) {
	store, root := setupRepo(t)
	if err := os.MkdirAll(filepath.Join(root, repofs.ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// The transaction lock, not the held lock: since the storage split, a
	// running apply's lock lives at .rdk/apply.lock — see
	// docs/superpowers/specs/2026-08-02-apply-lock-design.md.
	lock := `{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`
	if err := os.WriteFile(filepath.Join(root, repofs.ScratchDir, "apply.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(store, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeApplyLocked {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeApplyLocked)
	}
	if !strings.Contains(d.Summary, "another rdk apply is running") {
		t.Errorf("summary %q does not name it as an apply, not a held lock", d.Summary)
	}
	if !strings.Contains(d.Summary, "9f3a1c4e7b2d8a05") {
		t.Errorf("summary %q does not name the lock", d.Summary)
	}
	if !strings.Contains(d.Hint, "--break-lock=9f3a1c4e7b2d8a05") {
		t.Errorf("hint %q does not name --break-lock", d.Hint)
	}
	// --with-lock is only ever for a held lock; an apply lock's hint must not
	// mention it at all, not even as a warning — there is nothing to warn
	// against here.
	if strings.Contains(d.Hint, "--with-lock") {
		t.Errorf("hint %q mentions --with-lock for an apply lock", d.Hint)
	}
	attrs := map[string]any{}
	for _, a := range d.Attrs {
		attrs[a.Key] = a.Value()
	}
	if attrs["lock_id"] != "9f3a1c4e7b2d8a05" {
		t.Errorf("lock_id attr = %v", attrs["lock_id"])
	}
	if attrs["pid"] != 4127 {
		t.Errorf("pid attr = %v, want 4127", attrs["pid"])
	}
	if attrs["host"] != "builder-3" {
		t.Errorf("host attr = %v, want builder-3", attrs["host"])
	}
	// Exit 1: someone else holding the lock is environmental, not an rdk bug.
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
}

// A held lock blocking an apply is not "another rdk apply" — nothing is
// applying — so it must render as "locked", carry the holder's message, and
// warn off --with-lock (the id is right there, and someone will try it) while
// never explaining how to use it: that instruction belongs only in rdk lock's
// own success output, seen only by the person who took the lock.
func TestRunReportsAnExistingHeldLockWithAttrs(t *testing.T) {
	store, root := setupRepo(t)
	if err := os.MkdirAll(filepath.Join(root, repofs.ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := `{"id":"9f3a1c4e7b2d8a05","kind":"held","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z","message":"agent refactoring the s3-bucket module"}`
	if err := os.WriteFile(filepath.Join(root, repofs.ScratchDir, "lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(store, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeApplyLocked {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeApplyLocked)
	}
	if strings.Contains(d.Summary, "apply") {
		t.Errorf("summary %q calls a held lock an apply", d.Summary)
	}
	if !strings.Contains(d.Summary, "is locked") {
		t.Errorf("summary %q does not say the repository is locked", d.Summary)
	}
	if !strings.Contains(d.Summary, "agent refactoring the s3-bucket module") {
		t.Errorf("summary %q does not carry the holder's message", d.Summary)
	}
	if !strings.Contains(d.Hint, "--break-lock=9f3a1c4e7b2d8a05") {
		t.Errorf("hint %q does not name --break-lock", d.Hint)
	}
	// The warning form ("do not use") is allowed and expected; the
	// instruction form ("--with-lock=<id>", showing how to use it) is not —
	// that belongs only to rdk lock's own success output. Checking for the
	// bare flag name would pass vacuously since it's expected to appear as
	// part of the warning, so this checks specifically for the "=" that would
	// make it an instruction.
	if !strings.Contains(d.Hint, "--with-lock") {
		t.Errorf("hint %q does not warn off --with-lock", d.Hint)
	}
	if strings.Contains(d.Hint, "--with-lock=") {
		t.Errorf("hint %q explains how to use --with-lock, which is not this reader's to use", d.Hint)
	}
	attrs := map[string]any{}
	for _, a := range d.Attrs {
		attrs[a.Key] = a.Value()
	}
	if attrs["lock_id"] != "9f3a1c4e7b2d8a05" {
		t.Errorf("lock_id attr = %v", attrs["lock_id"])
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
