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

// A lock whose id cannot be read has no --break-lock recovery, so the
// diagnostic has to name the path instead of printing a command that cannot
// be typed. Reachable from a lock file truncated by a power loss, or written
// by a binary older than repofs' complete-or-absent lock creation.
//
// Reproduced through repofs's own exported surface rather than a
// package-internal constructor: a garbage .rdk/apply.lock planted directly,
// then a real Materialize call losing the exclusive-create race against it,
// is exactly the state a truncated write or a pre-split binary would leave —
// readLockFile's best-effort unmarshal has to tolerate it either way.
func TestLockedDiagnosticNamesThePathWhenTheIDIsUnreadable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repofs.ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, repofs.ScratchDir, "apply.lock"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := repofs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	set := repofs.NewFileSet()
	if err := set.Bytes(repofs.Managed("a.txt"), []byte("a")); err != nil {
		t.Fatal(err)
	}

	merr := store.Materialize(ManagedDir, set)
	d, ok := LockedDiagnostic(merr)
	if !ok {
		t.Fatal("LockedDiagnostic returned false")
	}
	lockPath := repofs.ScratchDir + "/apply.lock"
	if !strings.Contains(d.Hint, lockPath) {
		t.Errorf("hint = %q, want it to name the path to remove", d.Hint)
	}
	if strings.Contains(d.Hint, "--break-lock=") {
		t.Errorf("hint = %q, still offers a --break-lock that cannot name anything", d.Hint)
	}
	// pid/host/since/lock_id were never actually read — the record didn't
	// parse — so they must be absent, not present as zero values rdk never
	// observed. lock_kind and lock_path are still known (see readLockFile:
	// both come from the file, never the record), so those stay.
	attrs := map[string]any{}
	for _, a := range d.Attrs {
		attrs[a.Key] = a.Value()
	}
	for _, unknown := range []string{"pid", "host", "since", "lock_id"} {
		if _, present := attrs[unknown]; present {
			t.Errorf("attrs carries %q = %v, but nothing was ever read for it", unknown, attrs[unknown])
		}
	}
	if attrs["lock_kind"] != "apply" {
		t.Errorf("lock_kind attr = %v, want %q — known from the file regardless of parse failure", attrs["lock_kind"], "apply")
	}
	if attrs["lock_path"] != lockPath {
		t.Errorf("lock_path attr = %v, want %q", attrs["lock_path"], lockPath)
	}
}

// The ordinary case must keep printing the id, since that is the only
// argument --break-lock accepts. Same reproduction shape as the unreadable-id
// case above, but with a valid record this time.
func TestLockedDiagnosticStillPrintsTheIDWhenItIsReadable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, repofs.ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := `{"id":"9f3a1c4e","kind":"apply","host":"h","pid":1,"since":"2026-08-16T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(root, repofs.ScratchDir, "apply.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := repofs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	set := repofs.NewFileSet()
	if err := set.Bytes(repofs.Managed("a.txt"), []byte("a")); err != nil {
		t.Fatal(err)
	}

	merr := store.Materialize(ManagedDir, set)
	d, _ := LockedDiagnostic(merr)
	if !strings.Contains(d.Hint, "--break-lock=9f3a1c4e") {
		t.Errorf("hint = %q, want it to hand over the id", d.Hint)
	}
}

// materializeErrStore wraps a real Store and makes Materialize return a
// canned error, so Run can be driven into a branch that repofs's own state
// cannot produce on demand (readLockFile's Lstat guard requires a directory
// to appear at .rdk/apply.lock in the narrow window between the publish
// rename and the deferred ReleaseLock — reachable in production, but only
// reachable in a test via repofs's own unexported afterPublish seam, which a
// different package cannot set). Every other Store method is promoted
// unchanged, so parse.Dir and generate.Build still run for real against
// setupRepo's fixture.
type materializeErrStore struct {
	repofs.Store
	err error
}

func (s *materializeErrStore) Materialize(managedDir string, set *repofs.FileSet) error {
	return s.err
}

// fakeLockNotReleased and fakeLockTarget reproduce the shape repofs actually
// produces when a release fails because the lock path itself has become
// unusable: an error that Is(ErrLockNotReleased) and unwraps to one that
// separately Is(ErrLockTarget) — readLockFile's Lstat guard is what
// ReleaseLock's own read runs into. Built locally rather than via an exported
// repofs constructor: repofs deliberately keeps ErrLocked's own error type
// unexported (see the LockedDiagnostic tests below, which reach that state
// through repofs.New and a planted lock file instead), and this pairing has
// no equivalent production entry point to test through — Materialize cannot
// be made to lose its own lock path mid-run without repofs's unexported
// afterPublish seam (see materializeErrStore's own comment above).
type fakeLockNotReleased struct{ cause error }

func (e fakeLockNotReleased) Error() string        { return e.cause.Error() }
func (e fakeLockNotReleased) Unwrap() error        { return e.cause }
func (e fakeLockNotReleased) Is(target error) bool { return target == repofs.ErrLockNotReleased }

type fakeLockTarget struct{}

func (fakeLockTarget) Error() string {
	return ".rdk/apply.lock is a directory, not a lock file; remove it"
}
func (fakeLockTarget) Is(target error) bool { return target == repofs.ErrLockTarget }

// A release that fails because the lock path is now unusable must still lead
// with the fact that the apply succeeded: that is Task 6's entire reason to
// exist, and checking LockTargetDiagnostic before ErrLockNotReleased threw it
// away, reporting "cannot use rdk's lock files" and never mentioning the tree
// that had just been published. The repofs-level test for this
// (TestReleaseLockDoesNotTreatAReadFailureAsSuccess) only ever asserted that
// ReleaseLock returned an error — it never went through Run, so it could not
// have caught which diagnostic Run built from it.
func TestRunReportsLockNotReleasedEvenWhenTheCauseIsALockTargetFailure(t *testing.T) {
	store, _ := setupRepo(t)
	wrapped := &materializeErrStore{
		Store: store,
		err:   fakeLockNotReleased{cause: fakeLockTarget{}},
	}

	_, err := Run(wrapped, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeLockNotReleased {
		t.Errorf("code = %q, want %q — the apply succeeded and must say so, not report lock-target", d.Code, diag.CodeLockNotReleased)
	}
	if !strings.Contains(d.Summary, "wrote") {
		t.Errorf("summary %q does not lead with the apply having succeeded", d.Summary)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
}
