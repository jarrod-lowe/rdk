package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

// assertLockMismatch checks that err is a diagnostic coded lock-mismatch: the
// caller named a lock and the repository disagreed about it, as opposed to
// apply-locked (something holds the repository and you didn't name it).
func assertLockMismatch(t *testing.T, err error, what string) {
	t.Helper()
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != diag.CodeLockMismatch {
		t.Errorf("%s: code = %v, want %q", what, err, diag.CodeLockMismatch)
	}
}

func runSplit(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	// Built directly rather than via NewRootCmd, which discards the *app: a
	// failing RunE only returns an error, it never renders one (that's
	// execute's job in production), so a test that wants to see the rendered
	// diagnostic — not just err.Error() — has to run the same rendering here.
	a := &app{}
	root := a.rootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		err = a.renderFailure(err, &out, &errOut)
	}
	return out.String(), errOut.String(), err
}

func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	out, errOut, err := runSplit(t, dir, args...)
	return out + errOut, err
}

func TestInitThenApply(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// complete the seeded config, add a bucket
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rdk", "assets.yaml"),
		[]byte("kind: s3-bucket\nname: assets\ndescription: Static assets\n"), 0o644)

	out, err := run(t, dir, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("missing summary, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "rdk-managed", "terraform", "main.tf.json")); err != nil {
		t.Errorf("apply produced no terraform: %v", err)
	}
}

// A parked definition still succeeds, but the user has to hear about it, or the
// missing resource is a mystery at the far end.
func TestApplyReportsIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rdk", "logs.yaml.disabled"),
		[]byte("kind: s3-bucket\nname: logs\ndescription: Logs\n"), 0o644)

	out, err := run(t, dir, "apply")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "logs.yaml.disabled") || !strings.Contains(out, "warning") {
		t.Errorf("apply did not report the ignored file, got: %s", out)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("missing summary, got: %s", out)
	}
}

// An unprocessable file fails the apply outright.
func TestApplyRejectsStrayFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rdk", "notes.txt"), []byte("scratch\n"), 0o644)

	out, err := run(t, dir, "apply")
	if err == nil {
		t.Fatalf("apply succeeded despite a stray file, got: %s", out)
	}
	if !strings.Contains(err.Error(), "notes.txt") {
		t.Errorf("error does not name the file: %v", err)
	}
}

// The summary is the command's answer and belongs on stdout; the warning is
// commentary and belongs on stderr, so `rdk apply | tail -1` still works.
func TestApplySplitsStreams(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rdk", "logs.yaml.disabled"),
		[]byte("kind: s3-bucket\nname: logs\ndescription: Logs\n"), 0o644)

	out, errOut, err := runSplit(t, dir, "apply")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("stdout missing the summary: %q", out)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("stdout carries a warning: %q", out)
	}
	if !strings.Contains(errOut, "warning: logs.yaml.disabled") {
		t.Errorf("stderr missing the warning: %q", errOut)
	}
}

func TestJSONLModeEmitsOneObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	out, _, err := runSplit(t, dir, "apply", "--log-format=jsonl")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, out)
	}
	if rec["code"] != "apply-complete" {
		t.Errorf("code = %v, want apply-complete", rec["code"])
	}
}

func TestQuietModeSuppressesTheSummary(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	out, _, err := runSplit(t, dir, "apply", "--log-level=warn")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}

func TestBadFlagValueIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, _, err := runSplit(t, dir, "version", "--log-format=yaml")
	if err == nil {
		t.Fatal("want an error for an unknown log format")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error does not name the bad value: %v", err)
	}
}

// The exit code is a contract: a script has to be able to tell "fix your
// input" from "rdk is broken".
func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rdk", "notes.txt"), []byte("scratch\n"), 0o644)

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"version"}, 0},
		{"bad definition", []string{"apply"}, 1},
		{"bad flag value", []string{"version", "--log-format=yaml"}, 1},
		{"unknown flag", []string{"--bogus"}, 1},
		{"unknown command", []string{"bogus"}, 1},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := execute(c.args, &out, &errOut); got != c.want {
			t.Errorf("%s: exit = %d, want %d (stderr: %s)", c.name, got, c.want, errOut.String())
		}
	}
}

// Silently acting on a different directory than the one named is the failure
// this guards: it used to succeed, exit 0, and initialise the wrong place.
func TestCommandsRejectPositionalArguments(t *testing.T) {
	for _, args := range [][]string{{"init", "/some/path"}, {"version", "extra"}, {"apply", "x"}} {
		var out, errOut bytes.Buffer
		if got := execute(args, &out, &errOut); got != 1 {
			t.Errorf("%v: exit = %d, want 1", args, got)
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout = %q, want empty", args, out.String())
		}
	}
}

// The fallback logger is built after cobra has already failed, so it is the
// one most likely to be constructed without the injected writers — and a
// diagnostic written to the real stderr is one no caller can act on.
func TestCommandLineErrorsReachTheInjectedStream(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"bogus"}} {
		var out, errOut bytes.Buffer
		if got := execute(args, &out, &errOut); got != 1 {
			t.Errorf("%v: exit = %d, want 1", args, got)
		}
		if !strings.Contains(errOut.String(), "invalid command line") {
			t.Errorf("%v: stderr = %q, want the diagnostic", args, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout = %q, want empty", args, out.String())
		}
	}
}

// The loser of a race has to be told what to do — an apply lock is never a
// door --with-lock could open, so the error hands over only the id and
// --break-lock, not a warning about a flag that was never relevant here.
func TestApplyReportsAnExistingLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "apply")
	if err == nil {
		t.Fatal("apply succeeded despite a lock")
	}
	for _, want := range []string{"9f3a1c4e7b2d8a05", "--break-lock"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not mention %q: %q", want, errOut)
		}
	}
	if strings.Contains(errOut, "--with-lock") {
		t.Errorf("stderr mentions --with-lock for an apply lock: %q", errOut)
	}

	// A blocked apply is the user's problem, not rdk's — the exit code is the
	// whole point of the diagnostic, so it has to be 1, not 2.
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var lockedOut, lockedErr bytes.Buffer
	gotExit := execute([]string{"apply"}, &lockedOut, &lockedErr)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("blocked apply exit = %d, want 1 (stderr: %s)", gotExit, lockedErr.String())
	}

	// A wrong id must not get past it, and codes as lock-mismatch: you named
	// a lock and the repository disagreed, not "something holds it".
	_, _, breakErr := runSplit(t, dir, "apply", "--break-lock=wrong-id")
	if breakErr == nil {
		t.Error("apply proceeded with a mismatched --break-lock id")
	}
	assertLockMismatch(t, breakErr, "--break-lock with a wrong id")

	out, errOut2, err := runSplit(t, dir, "apply", "--break-lock=9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatalf("apply --break-lock: %v (%s)", err, errOut2)
	}
	if !strings.Contains(errOut2, "broke lock") || !strings.Contains(errOut2, "4127") {
		t.Errorf("breaking a lock was not announced: %q", errOut2)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("apply did not proceed: %q", out)
	}
}

// lockIDFromDisk reads .rdk/lock and pulls out the id, via encoding/json
// rather than string surgery — the format is JSON, not a format worth
// re-parsing by hand in a test.
func lockIDFromDisk(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".rdk", "lock"))
	if err != nil {
		t.Fatalf("reading .rdk/lock: %v", err)
	}
	var lock struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatalf(".rdk/lock is not JSON: %v (%q)", err, b)
	}
	if lock.ID == "" {
		t.Fatalf(".rdk/lock has no id: %q", b)
	}
	return lock.ID
}

func TestLockThenUnlock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	out, _, err := runSplit(t, dir, "lock", "-m", "agent working")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !strings.Contains(out, "agent working") {
		t.Errorf("lock did not report its message: %q", out)
	}

	// An apply is now blocked, and told why. A held lock is not "another rdk
	// apply" — nothing is applying — so the message must say "locked", not
	// "apply", and it must not explain how to use --with-lock: that
	// instruction is for the holder alone, and belongs only in rdk lock's own
	// success output above.
	_, errOut, err := runSplit(t, dir, "apply")
	if err == nil {
		t.Fatal("apply ran under someone else's lock")
	}
	if !strings.Contains(errOut, "agent working") {
		t.Errorf("the blocked apply does not say why: %q", errOut)
	}
	if !strings.Contains(errOut, "is locked") || strings.Contains(errOut, "another rdk apply") {
		t.Errorf("the blocked apply does not name a held lock for what it is: %q", errOut)
	}
	if strings.Contains(errOut, "--with-lock=") {
		t.Errorf("the blocked apply explains how to use --with-lock: %q", errOut)
	}

	id := lockIDFromDisk(t, dir)
	if _, _, err := runSplit(t, dir, "unlock", id); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, _, err := runSplit(t, dir, "apply"); err != nil {
		t.Fatalf("apply after unlock: %v", err)
	}
}

// rdk lock's success output is the one place --with-lock guidance belongs —
// only the holder sees it — and it reminds the holder to release when done,
// since a lock left behind blocks everyone else.
func TestLockTellsYouHowToApplyAndRelease(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	out, _, err := runSplit(t, dir, "lock", "-m", "agent working")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)
	for _, want := range []string{
		"rdk apply --with-lock=" + id,
		"rdk unlock " + id,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lock output does not mention %q: %q", want, out)
		}
	}
}

// lock_id has to be a typed attr, not just prose in the summary, so a JSONL
// consumer reads a field instead of parsing the sentence apart.
func TestLockJSONLCarriesTypedLockID(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	out, _, err := runSplit(t, dir, "lock", "-m", "agent working", "--log-format=jsonl")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	var rec map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); jsonErr != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", jsonErr, out)
	}
	if rec["code"] != "lock-held" {
		t.Errorf("code = %v, want lock-held", rec["code"])
	}
	id := lockIDFromDisk(t, dir)
	if rec["lock_id"] != id {
		t.Errorf("lock_id = %v, want %q", rec["lock_id"], id)
	}
}

// A lock nobody can explain is worse than no lock.
func TestLockRequiresAMessage(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, _, err := runSplit(t, dir, "lock"); err == nil {
		t.Error("lock succeeded with no message")
	}
}

// A second rdk lock while one is held gets the same treatment as a blocked
// apply, and exits 1 — this is the user's problem (someone else has it), not
// rdk's.
func TestLockRefusesWhenAlreadyLocked(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, _, err := runSplit(t, dir, "lock", "-m", "first"); err != nil {
		t.Fatalf("first lock: %v", err)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	gotExit := execute([]string{"lock", "-m", "second"}, &out, &errOut)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("blocked lock exit = %d, want 1 (stderr: %s)", gotExit, errOut.String())
	}
	if !strings.Contains(errOut.String(), "first") {
		t.Errorf("second lock's error does not name the first holder: %q", errOut.String())
	}
}

// rdk unlock with a wrong id must not remove the real lock.
func TestUnlockRejectsAWrongID(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, _, err := runSplit(t, dir, "lock", "-m", "agent working"); err != nil {
		t.Fatalf("lock: %v", err)
	}

	_, _, err := runSplit(t, dir, "unlock", "not-the-right-id")
	if err == nil {
		t.Error("unlock succeeded with a wrong id")
	}
	assertLockMismatch(t, err, "unlock with a wrong id")
	if _, _, err := runSplit(t, dir, "apply"); err == nil {
		t.Error("apply ran after a failed unlock — the lock was removed anyway")
	}
}

// rdk unlock refuses to end an apply lock, and points at --break-lock instead
// — ending someone's running apply is breaking, not unlocking.
func TestUnlockRefusesAnApplyLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "unlock", "9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlock removed an apply lock")
	}
	if !strings.Contains(errOut, "--break-lock") {
		t.Errorf("unlock's refusal does not point at --break-lock: %q", errOut)
	}
	assertLockMismatch(t, err, "unlock naming an apply lock")
}

// Naming a lock id when nothing is locked at all is still lock-mismatch, not
// apply-locked: nothing holds the repository, so there is no holder to
// report — the repository simply disagrees with the id you named.
func TestLockMismatchWhenNoLockExists(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	_, _, unlockErr := runSplit(t, dir, "unlock", "nonexistent")
	if unlockErr == nil {
		t.Error("unlock succeeded with no lock held")
	}
	assertLockMismatch(t, unlockErr, "unlock with no lock held")

	_, _, breakErr := runSplit(t, dir, "apply", "--break-lock=nonexistent")
	if breakErr == nil {
		t.Error("apply --break-lock succeeded with no lock held")
	}
	assertLockMismatch(t, breakErr, "--break-lock with no lock held")
}
