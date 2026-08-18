package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
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

// assertLockTarget checks that err is a diagnostic coded lock-target: a lock
// path is occupied by something other than a regular file, so nothing holds
// the repository and there is no id to name — the case lock-mismatch's hint
// (check the id) and apply-locked's hint (wait, or --break-lock) are both
// wrong for.
func assertLockTarget(t *testing.T, err error, what string) {
	t.Helper()
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != diag.CodeLockTarget {
		t.Errorf("%s: code = %v, want %q", what, err, diag.CodeLockTarget)
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

// --log-level=warn used to be a quiet mode that swallowed the summary along
// with the warnings it was meant to filter; a result is not a diagnostic, so
// it must survive any configured level.
func TestLogLevelWarnStillPrintsTheSummary(t *testing.T) {
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
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("stdout = %q, want the summary", out)
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

// The whole point of --log-format=jsonl is that a caller parsing stdout/stderr
// gets structured lines even when something goes wrong, so a rejected sibling
// flag (here, a bad --log-level) must not silently fall back to plain text.
// Before this was fixed, logger.Resolve discarded everything it had already
// resolved — including a perfectly valid --log-format=jsonl — the moment it
// rejected --log-level, so PersistentPreRunE built a.log from a zero
// logger.Options and `rdk version --log-format=jsonl --log-level=bogus`
// printed the plain-text line `error: unknown log level "bogus"` on stderr:
// unparseable by exactly the caller --log-format=jsonl exists for.
func TestBadLogLevelStillRendersAsJSONLWhenFormatIsValid(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	_, errOut, err := runSplit(t, dir, "version", "--log-format=jsonl", "--log-level=bogus")
	if err == nil {
		t.Fatal("want an error for an unknown log level")
	}
	var rec map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(errOut)), &rec); jsonErr != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", jsonErr, errOut)
	}
	if rec["code"] != diag.CodeInvalidFlag {
		t.Errorf("code = %v, want %q", rec["code"], diag.CodeInvalidFlag)
	}
}

// Precedence is flag > environment > default, and a partially-resolved
// Options has to keep respecting it: RDK_LOG_FORMAT=jsonl from the
// environment must be honoured on the same rejection path as an explicit
// --log-format flag above.
func TestBadLogLevelStillRendersAsJSONLWhenFormatComesFromEnv(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Setenv("RDK_LOG_FORMAT", "jsonl")

	_, errOut, err := runSplit(t, dir, "version", "--log-level=bogus")
	if err == nil {
		t.Fatal("want an error for an unknown log level")
	}
	var rec map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(errOut)), &rec); jsonErr != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", jsonErr, errOut)
	}
	if rec["code"] != diag.CodeInvalidFlag {
		t.Errorf("code = %v, want %q", rec["code"], diag.CodeInvalidFlag)
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

// --help short-circuits inside cobra before PersistentPreRunE ever builds
// a.log, so execute's post-Execute check for a delivery failure has to cope
// with a nil logger rather than assume RunE always ran. This is a regression
// test for exactly that: it panicked on a nil a.log until the check was
// guarded.
func TestHelpDoesNotPanic(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := execute([]string{"--help"}, &out, &errOut); got != 0 {
		t.Errorf("--help exit = %d, want 0 (stderr: %s)", got, errOut.String())
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("stdout = %q, want cobra's help text", out.String())
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
	// The transaction lock, not the held lock: since the storage split, a
	// running apply's lock lives at .rdk/apply.lock.
	os.WriteFile(filepath.Join(dir, ".rdk", "apply.lock"),
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
	// The notice rides with the result now, not a separate warning: it has to
	// be exactly as visible as the success it explains.
	if !strings.Contains(out, "broke lock") || !strings.Contains(out, "4127") {
		t.Errorf("breaking a lock was not announced in the result: %q", out)
	}
	if strings.Contains(errOut2, "broke lock") {
		t.Errorf("breaking a lock was also announced separately on stderr: %q", errOut2)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("apply did not proceed: %q", out)
	}
}

// Breaking happens before apply.Run, so a subsequent apply failure must not
// make that fact disappear — the lock is destroyed either way, and the user
// needs to know that regardless of whether what followed then succeeded.
func TestApplyBreakLockNoticeSurvivesAFailedApply(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	// A stray file makes apply.Run fail after the lock is already broken.
	os.WriteFile(filepath.Join(dir, "rdk", "notes.txt"), []byte("scratch\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	// A stranded transaction lock — the state a hard kill leaves — lives at
	// .rdk/apply.lock since the storage split.
	os.WriteFile(filepath.Join(dir, ".rdk", "apply.lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "apply", "--break-lock=9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("apply succeeded despite the stray file")
	}
	for _, want := range []string{"notes.txt", "broke lock", "9f3a1c4e7b2d8a05", "4127"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("failure does not mention %q: %q", want, errOut)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".rdk", "apply.lock")); !os.IsNotExist(statErr) {
		t.Errorf(".rdk/apply.lock still exists after --break-lock: %v", statErr)
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
// This is the case that prompted levels to stop filtering results: a script
// doing id=$(rdk lock -m work --log-level=warn) must not come back empty and
// hold a lock nobody can see the id of.
func TestLockIDSurvivesWarnLevel(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	out, _, err := runSplit(t, dir, "lock", "-m", "agent working", "--log-level=warn")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)
	if !strings.Contains(out, id) {
		t.Errorf("stdout = %q, want it to contain the lock id %q", out, id)
	}
}

// failingWriter always fails, standing in for stdout redirected to a full
// disk or a broken pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write: no space left on device")
}

// The same failure TestLockIDSurvivesWarnLevel guards against from the
// level-filtering angle, reached here by a broken stream instead: rdk lock's
// entire job is printing the id, so a run that takes the lock but can't print
// it must not exit 0 — that would be indistinguishable from success to a
// script holding a lock whose id it never received.
func TestLockDoesNotReportSuccessWhenStdoutFails(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	gotExit := execute([]string{"lock", "-m", "agent working"}, failingWriter{}, &errOut)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}

	if gotExit == 0 {
		t.Fatalf("rdk lock exited 0 despite failing to print its id (stderr: %s)", errOut.String())
	}
	if !strings.Contains(errOut.String(), "output") {
		t.Errorf("stderr does not explain the delivery failure: %q", errOut.String())
	}
}

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

// Reproduction from review: with .rdk present as a regular file, rdk apply
// already reports scratch-target at exit 1 ("remove .rdk, then re-run" — see
// internal/apply/apply_test.go's TestScratchTargetNamesRdkAndSaysToRemoveIt),
// but rdk lock's HoldLock reaches the identical ensureScratchDir check first
// (HoldLock's very first call) and, before this fix, had no branch for
// ErrScratchTarget in lockHoldError — it fell through to that function's
// bare `return err`, exit 2, "an rdk bug. Report it", for a condition the
// user can fix by removing one file. Unlike the other lock diagnostics this
// file tests via a crafted fake error, ErrScratchTarget needs no internal
// fault injection to reach for real: a plain regular file at .rdk is enough,
// so this runs the whole command.
func TestLockReportsAnUnusableScratchDirAsScratchTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, repofs.ScratchDir), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, dir, "lock", "-m", "agent working")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v (output: %s)", err, out)
	}
	if d.Code != diag.CodeScratchTarget {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeScratchTarget)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 — this is user-fixable, not an rdk bug", got)
	}
	if !strings.Contains(d.Hint, "remove "+repofs.ScratchDir) {
		t.Errorf("hint %q does not tell the user to remove %s", d.Hint, repofs.ScratchDir)
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

// fakeLockNotReleased and fakeLockTarget reproduce the shape repofs actually
// produces when HoldLock's own release fails because .rdk/apply.lock itself
// has become unusable: an error that Is(ErrLockNotReleased) and unwraps to
// one that separately Is(ErrLockTarget) (internal/repofs/store.go's
// ReleaseLock wraps whatever readLockFile's Lstat guard returns). Copied
// locally rather than shared with internal/apply/apply_test.go's identical
// pair: both are three-line stand-ins for a repofs state that cannot be
// reached from outside internal/repofs at all — HoldLock's release runs
// inside a single synchronous call with no seam this package can reach (see
// lockHoldError's doc comment in lock.go) — so there is no production entry
// point either copy is standing in for that the other package could expose
// instead.
type fakeLockNotReleased struct{ cause error }

func (e fakeLockNotReleased) Error() string        { return e.cause.Error() }
func (e fakeLockNotReleased) Unwrap() error        { return e.cause }
func (e fakeLockNotReleased) Is(target error) bool { return target == repofs.ErrLockNotReleased }

type fakeLockTarget struct{}

func (fakeLockTarget) Error() string {
	return ".rdk/apply.lock is a directory, not a lock file; remove it"
}
func (fakeLockTarget) Is(target error) bool { return target == repofs.ErrLockTarget }

// TestLockHoldErrorReportsLockNotReleasedWithTheHeldLockID exercises
// lockHoldError directly rather than through rdk lock end to end: reaching
// this branch for real needs .rdk to go unwritable in the exact window
// between HoldLock creating .rdk/lock and its own deferred ReleaseLock
// running, and nothing outside internal/repofs can land there (see
// lockHoldError's doc comment) — repofs's own suite only reaches the
// equivalent failure in Materialize via its unexported afterPublish seam.
// This test only asserts what a unit test of the mapping can honestly
// assert: given the sentinel HoldLock is documented to return alongside a
// LockInfo it already created, the diagnostic built from it carries the
// right code, exit status, and — the whole reason this task exists — the
// held lock's own id, without which the lock it just created would block
// every apply until someone found it by other means.
func TestLockHoldErrorReportsLockNotReleasedWithTheHeldLockID(t *testing.T) {
	info := repofs.LockInfo{ID: "deadbeefcafefeed", Message: "agent working"}
	err := lockHoldError(fakeLockNotReleased{cause: errors.New("remove .rdk/apply.lock: permission denied")}, info)

	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeLockNotReleased {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeLockNotReleased)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 — this is user-fixable state, not an rdk bug", got)
	}
	if !strings.Contains(d.Summary, info.ID) {
		t.Errorf("summary %q does not carry the held lock's id", d.Summary)
	}
	if !strings.Contains(d.Summary, info.Message) {
		t.Errorf("summary %q does not carry the lock's message", d.Summary)
	}
	for _, want := range []string{"rdk apply --with-lock=" + info.ID, "rdk unlock " + info.ID} {
		if !strings.Contains(d.Hint, want) {
			t.Errorf("hint %q does not mention %q — the id is only useful if it's handed over", d.Hint, want)
		}
	}
	found := false
	for _, a := range d.Attrs {
		if a.Key == "lock_id" && a.Value() == info.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("attrs %+v do not carry lock_id = %q", d.Attrs, info.ID)
	}
	if !errors.Is(err, repofs.ErrLockNotReleased) {
		t.Errorf("err does not unwrap to ErrLockNotReleased: %v", err)
	}
}

// A release failure whose cause is also an unusable lock path must still
// report lock-not-released, not lock-target: the held lock was already
// created by the time this fires, and reporting "cannot use rdk's lock
// files" would never say so. Mirrors
// internal/apply/apply_test.go's TestRunReportsLockNotReleasedEvenWhenTheCauseIsALockTargetFailure,
// which guards the identical ordering in apply.Run.
func TestLockHoldErrorPrefersLockNotReleasedOverLockTarget(t *testing.T) {
	info := repofs.LockInfo{ID: "deadbeefcafefeed", Message: "agent working"}
	err := lockHoldError(fakeLockNotReleased{cause: fakeLockTarget{}}, info)

	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeLockNotReleased {
		t.Errorf("code = %q, want %q — the held lock was created and must say so, not report lock-target", d.Code, diag.CodeLockNotReleased)
	}
	if !strings.Contains(d.Summary, "held "+info.ID) {
		t.Errorf("summary %q does not lead with the held lock having been created", d.Summary)
	}
}

// fakeLockNotDurable reproduces the shape repofs actually produces when
// HoldLock's held lock is created but its directory-entry sync then fails
// (repofs.lockNotDurableErrorFor, internal/repofs/store.go): an error that
// Is(ErrLockNotDurable). Copied locally in the same spirit as
// fakeLockNotReleased above rather than shared: reaching this branch for real
// needs .rdk's directory sync to fail in the exact window between HoldLock
// linking .rdk/lock and reporting success, and nothing outside internal/repofs
// can land there — that package's own suite reaches it via its unexported
// afterHeldLockLinked seam (store_test.go), and there is no production entry
// point this copy stands in for that the other package could expose instead.
type fakeLockNotDurable struct{ cause error }

func (e fakeLockNotDurable) Error() string        { return e.cause.Error() }
func (e fakeLockNotDurable) Unwrap() error        { return e.cause }
func (e fakeLockNotDurable) Is(target error) bool { return target == repofs.ErrLockNotDurable }

// TestLockHoldErrorReportsLockNotDurableWithTheHeldLockID exercises
// lockHoldError directly rather than through rdk lock end to end, for the same
// reason as TestLockHoldErrorReportsLockNotReleasedWithTheHeldLockID above:
// reaching this branch for real needs a real OS failure in a window this
// package cannot reach (see fakeLockNotDurable's doc comment). This test only
// asserts what a unit test of the mapping can honestly assert: given the
// sentinel HoldLock is documented to return alongside a LockInfo it already
// created, the diagnostic built from it exits 1 — the lock is real and in
// force, not an rdk bug — and carries the held lock's own id, without which a
// lock that is genuinely blocking every apply right now would have nothing for
// the user to act on. It does not, and cannot, exercise syncHeldLockDir itself
// or any real fsync failure; that is internal/repofs's own suite's job.
func TestLockHoldErrorReportsLockNotDurableWithTheHeldLockID(t *testing.T) {
	info := repofs.LockInfo{ID: "deadbeefcafefeed", Message: "agent working"}
	err := lockHoldError(fakeLockNotDurable{cause: errors.New("sync .rdk: input/output error")}, info)

	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeLockNotDurable {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeLockNotDurable)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 — the lock is real and in force, not an rdk bug", got)
	}
	if !strings.Contains(d.Summary, info.ID) {
		t.Errorf("summary %q does not carry the held lock's id", d.Summary)
	}
	if !strings.Contains(d.Summary, info.Message) {
		t.Errorf("summary %q does not carry the lock's message", d.Summary)
	}
	for _, want := range []string{"rdk apply --with-lock=" + info.ID, "rdk unlock " + info.ID} {
		if !strings.Contains(d.Hint, want) {
			t.Errorf("hint %q does not mention %q — the id is only useful if it's handed over", d.Hint, want)
		}
	}
	found := false
	for _, a := range d.Attrs {
		if a.Key == "lock_id" && a.Value() == info.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("attrs %+v do not carry lock_id = %q", d.Attrs, info.ID)
	}
	if !errors.Is(err, repofs.ErrLockNotDurable) {
		t.Errorf("err does not unwrap to ErrLockNotDurable: %v", err)
	}
}

// TestLockHoldErrorReportsShuttingDownAsInterruptedNotAsABug exercises
// lockHoldError directly with repofs.ErrShuttingDown itself, not a fake
// stand-in: unlike ErrLockNotReleased and ErrLockNotDurable above,
// ErrShuttingDown is an exported sentinel (repofs.ErrShuttingDown) that
// errors.Is compares by identity, so no wrapper type is needed to reproduce
// its shape.
//
// Before lockHoldError grew a branch for it, this sentinel fell through to
// the bare `return err` at the bottom of lockHoldError, which diag.ExitCode
// treats as an unclassified error — exit 2, "an rdk bug. Report it" — for a
// user who had simply pressed Ctrl-C during rdk lock. This test proves the
// fix: the diagnostic carries diag.CodeInterrupted, exits 1, and its text
// does not claim rdk is broken.
//
// What this does NOT cover: the actual signal race (PrepareShutdown's
// shuttingDown flag winning acquireLock's owned-path check mid-writeScratchTemp,
// see acquireLock's own doc comment in internal/repofs/store.go) is not
// deterministically reachable from a test — it depends on a signal landing
// inside a real fsync's duration — so this only proves lockHoldError's
// mapping once HoldLock has already decided to return the sentinel, not that
// HoldLock decides correctly. That mechanism is internal/repofs's own
// suite's job (store_test.go).
func TestLockHoldErrorReportsShuttingDownAsInterruptedNotAsABug(t *testing.T) {
	err := lockHoldError(repofs.ErrShuttingDown, repofs.LockInfo{})

	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeInterrupted {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeInterrupted)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 — the user pressed Ctrl-C, this is not an rdk bug", got)
	}
	line := d.Line()
	for _, falseClaim := range []string{"bug", "report it", "panic"} {
		if strings.Contains(strings.ToLower(line), falseClaim) {
			t.Errorf("message %q falsely suggests an rdk bug (contains %q)", line, falseClaim)
		}
	}
	if !strings.Contains(d.Hint, "nothing is wrong") {
		t.Errorf("hint %q does not reassure the reader that nothing is wrong", d.Hint)
	}
	if !strings.Contains(d.Hint, "no lock was created") {
		t.Errorf("hint %q does not say that no lock was created", d.Hint)
	}
	if !errors.Is(err, repofs.ErrShuttingDown) {
		t.Errorf("err does not unwrap to ErrShuttingDown: %v", err)
	}
}

// TestUnlockErrorReportsLockNotDurableWithoutClaimingTheReleaseFailed
// exercises unlockError directly, the same way
// TestLockHoldErrorReportsLockNotDurableWithTheHeldLockID exercises
// lockHoldError, and for the same reason: reaching this branch for real
// needs .rdk to go unreadable in the exact window between Unlock's Remove
// succeeding and its own syncHeldLockDir call running, which internal/repofs's
// own suite reaches via its unexported afterHeldLockRemoved seam
// (store_test.go) — not from this package. Reusing fakeLockNotDurable rather
// than defining a second copy: the shape it stands in for (an error that
// Is(ErrLockNotDurable)) is identical regardless of whether HoldLock or
// Unlock produced it.
//
// What matters here specifically, beyond the code and exit status already
// covered by the HoldLock-side test: the summary must not read as though the
// unlock failed. It already succeeded by the time Unlock can return this
// sentinel (see store.go's Unlock — the sync runs after Remove, not
// instead of it), so a summary built from the generic lock-mismatch
// fallback ("cannot unlock %s") would be false here, not just imprecise.
func TestUnlockErrorReportsLockNotDurableWithoutClaimingTheReleaseFailed(t *testing.T) {
	err := unlockError(fakeLockNotDurable{cause: errors.New("sync .rdk: input/output error")}, "deadbeefcafefeed")

	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeLockNotDurable {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeLockNotDurable)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1 — the lock really was removed, not an rdk bug", got)
	}
	if strings.Contains(d.Summary, "cannot unlock") {
		t.Errorf("summary %q reads like the unlock failed — it already succeeded", d.Summary)
	}
	if !strings.Contains(d.Summary, "removed") {
		t.Errorf("summary %q does not say the lock was removed", d.Summary)
	}
	if !strings.Contains(d.Summary, "deadbeefcafefeed") {
		t.Errorf("summary %q does not carry the lock id", d.Summary)
	}
	found := false
	for _, a := range d.Attrs {
		if a.Key == "lock_id" && a.Value() == "deadbeefcafefeed" {
			found = true
		}
	}
	if !found {
		t.Errorf("attrs %+v do not carry lock_id", d.Attrs)
	}
	if !errors.Is(err, repofs.ErrLockNotDurable) {
		t.Errorf("err does not unwrap to ErrLockNotDurable: %v", err)
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
// The required scenario: rdk unlock naming a running apply's transaction
// lock still gets the redirecting message, even though unlock only ever
// touches .rdk/lock — it reads .rdk/apply.lock too, purely to diagnose.
func TestUnlockRefusesAnApplyLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "apply.lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "unlock", "9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlock removed an apply lock")
	}
	if !strings.Contains(errOut, "--break-lock") {
		t.Errorf("unlock's refusal does not point at --break-lock: %q", errOut)
	}
	assertLockMismatch(t, err, "unlock naming an apply lock")
	if _, statErr := os.Stat(filepath.Join(dir, ".rdk", "apply.lock")); statErr != nil {
		t.Errorf("unlock touched the apply lock: %v", statErr)
	}
}

// rdk lock must refuse while an apply is genuinely in flight — otherwise it
// would return claiming the repository is held while a Materialize is
// halfway through, the exact lie the feature exists to prevent.
func TestLockRefusedWhileAnApplyIsRunning(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "apply.lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "lock", "-m", "agent working")
	if err == nil {
		t.Fatal("lock succeeded while an apply was running")
	}
	if !strings.Contains(errOut, "another rdk apply is running") {
		t.Errorf("lock's refusal does not say an apply is running: %q", errOut)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".rdk", "lock")); !os.IsNotExist(statErr) {
		t.Errorf("rdk lock created a held lock despite the running apply: %v", statErr)
	}
}

// A stranded transaction lock — the state a hard kill leaves, since an
// ordinary exit always releases via defer or the signal handler —
// --break-lock clears exactly as it always could when there was one file.
func TestApplyBreakLockClearsAStrandedApplyLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "apply.lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	out, _, err := runSplit(t, dir, "apply", "--break-lock=9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatalf("apply --break-lock: %v", err)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("apply did not proceed: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".rdk", "apply.lock")); !os.IsNotExist(statErr) {
		t.Errorf(".rdk/apply.lock still exists after --break-lock: %v", statErr)
	}
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

// This is the whole point of --with-lock: the holder can keep applying while
// they hold the lock, so it has to survive — twice, to show it is adopted
// rather than consumed.
func TestApplyWithLockSucceedsAndLockSurvives(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	if _, _, err := runSplit(t, dir, "lock", "-m", "agent refactoring the s3-bucket module"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)

	out, _, err := runSplit(t, dir, "apply", "--with-lock="+id)
	if err != nil {
		t.Fatalf("apply --with-lock: %v", err)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("apply did not run: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".rdk", "lock")); err != nil {
		t.Errorf(".rdk/lock did not survive: %v", err)
	}
	if got := lockIDFromDisk(t, dir); got != id {
		t.Errorf("lock id changed: got %q, want %q", got, id)
	}

	// Not consumed: a second apply under the same id works exactly like the
	// first, and the lock is still there afterwards.
	out2, _, err := runSplit(t, dir, "apply", "--with-lock="+id)
	if err != nil {
		t.Fatalf("second apply --with-lock: %v", err)
	}
	if !strings.Contains(out2, "rdk apply: wrote") {
		t.Errorf("second apply did not run: %q", out2)
	}
	if _, err := os.Stat(filepath.Join(dir, ".rdk", "lock")); err != nil {
		t.Errorf(".rdk/lock did not survive a second apply: %v", err)
	}
	if got := lockIDFromDisk(t, dir); got != id {
		t.Errorf("lock id changed after second apply: got %q, want %q", got, id)
	}
}

// Adoption succeeding and the apply then failing is exactly the case a
// result-only notice loses: the run did proceed under someone's lock
// regardless of whether what it then tried to do worked, so the notice has
// to survive on the error the same way withBrokeLockErr's does for
// --break-lock.
func TestApplyWithLockNoticeSurvivesAFailedApply(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	if _, _, err := runSplit(t, dir, "lock", "-m", "agent refactoring the s3-bucket module"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)
	// A stray file makes apply.Run fail after the lock has already been
	// adopted.
	os.WriteFile(filepath.Join(dir, "rdk", "notes.txt"), []byte("scratch\n"), 0o644)

	_, errOut, err := runSplit(t, dir, "apply", "--with-lock="+id)
	if err == nil {
		t.Fatal("apply succeeded despite the stray file")
	}
	for _, want := range []string{"notes.txt", "under lock", id, strconv.Itoa(os.Getpid()), "agent refactoring the s3-bucket module"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("failure does not mention %q: %q", want, errOut)
		}
	}
	// --with-lock never releases what it adopted, failure or not.
	if _, statErr := os.Stat(filepath.Join(dir, ".rdk", "lock")); statErr != nil {
		t.Errorf(".rdk/lock did not survive a failed --with-lock apply: %v", statErr)
	}
}

// The announcement is the safety property, not decoration: rdk cannot tell a
// legitimate holder from someone who copied the id out of a blocked apply's
// error, so it has to say whose lock this is and why every time — riding with
// the result rather than as a separate warning, so it is exactly as visible
// as the success it qualifies and cannot be silenced independently of it.
func TestApplyWithLockAnnouncesTheHolder(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	if _, _, err := runSplit(t, dir, "lock", "-m", "agent refactoring the s3-bucket module"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)

	out, errOut, err := runSplit(t, dir, "apply", "--with-lock="+id)
	if err != nil {
		t.Fatalf("apply --with-lock: %v (%s)", err, errOut)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("stdout missing the result: %q", out)
	}
	// runSplit executes in-process, so the pid the lock recorded is this test
	// process's own.
	for _, want := range []string{id, strconv.Itoa(os.Getpid()), "agent refactoring the s3-bucket module"} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %q: %q", want, out)
		}
	}
	if strings.Contains(errOut, id) {
		t.Errorf("the notice was also emitted separately on stderr: %q", errOut)
	}
}

// The JSONL form must carry the holder's details as typed fields on the
// apply-complete record itself, not just prose or a second record — a
// consumer detects an under-lock apply by lock_id's presence.
func TestApplyWithLockJSONLCarriesTypedAttrs(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	if _, _, err := runSplit(t, dir, "lock", "-m", "agent refactoring the s3-bucket module"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	id := lockIDFromDisk(t, dir)

	out, errOut, err := runSplit(t, dir, "apply", "--with-lock="+id, "--log-format=jsonl")
	if err != nil {
		t.Fatalf("apply --with-lock: %v (%s)", err, errOut)
	}
	var rec map[string]any
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	if jsonErr := json.Unmarshal([]byte(line), &rec); jsonErr != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", jsonErr, out)
	}
	if rec["code"] != "apply-complete" {
		t.Errorf("code = %v, want apply-complete", rec["code"])
	}
	if rec["lock_id"] != id {
		t.Errorf("lock_id = %v, want %q", rec["lock_id"], id)
	}
	host, _ := os.Hostname()
	if rec["host"] != host {
		t.Errorf("host = %v, want %q", rec["host"], host)
	}
	if rec["message"] != "agent refactoring the s3-bucket module" {
		t.Errorf("message = %v, want the holder's message", rec["message"])
	}
}

// A mismatched id must not be treated as permission to proceed — the id is
// the only thing distinguishing "this is mine" from a guess, so a wrong one
// has to fail.
func TestApplyWithLockRejectsAMismatchedID(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	if _, _, err := runSplit(t, dir, "lock", "-m", "working"); err != nil {
		t.Fatalf("lock: %v", err)
	}

	_, _, err := runSplit(t, dir, "apply", "--with-lock=not-the-right-id")
	if err == nil {
		t.Fatal("apply ran under a mismatched --with-lock id")
	}
	assertLockMismatch(t, err, "--with-lock with a wrong id")

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	gotExit := execute([]string{"apply", "--with-lock=not-the-right-id"}, &out, &errOut)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("mismatched --with-lock exit = %d, want 1 (stderr: %s)", gotExit, errOut.String())
	}
}

// No lock at all is not "nothing to worry about" — it means the lock the
// caller believed they held was broken out from under them, which they need
// to hear rather than have silently treated as permission to proceed.
func TestApplyWithLockRejectsWhenNoLockExists(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	_, errOut, err := runSplit(t, dir, "apply", "--with-lock=9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("apply ran with --with-lock but no lock exists")
	}
	assertLockMismatch(t, err, "--with-lock with no lock held")
	if !strings.Contains(errOut, "broken out from under you") {
		t.Errorf("does not say the lock was broken out from under them: %q", errOut)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut2 bytes.Buffer
	gotExit := execute([]string{"apply", "--with-lock=9f3a1c4e7b2d8a05"}, &out, &errOut2)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("--with-lock with no lock exit = %d, want 1 (stderr: %s)", gotExit, errOut2.String())
	}
}

// --with-lock's own UseLock call checks the transaction-lock path is usable
// before it ever reads scratchLock (see repofs.osStore.UseLock) — so a
// symlink at .rdk/apply.lock fails here, before parsing or generation ever
// runs, not lock-mismatch (there is no id to check) and not apply-locked
// (nothing is actually holding the repository).
func TestApplyWithLockReportsASymlinkedApplyLockAsLockTarget(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	if err := os.Symlink("nowhere", filepath.Join(dir, ".rdk", "apply.lock")); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := runSplit(t, dir, "apply", "--with-lock=9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("apply ran --with-lock over a symlinked apply-lock path")
	}
	assertLockTarget(t, err, "--with-lock over a symlinked .rdk/apply.lock")
	if !strings.Contains(errOut, ".rdk/apply.lock") {
		t.Errorf("stderr does not name the offending path: %q", errOut)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut2 bytes.Buffer
	gotExit := execute([]string{"apply", "--with-lock=9f3a1c4e7b2d8a05"}, &out, &errOut2)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("lock-target exit = %d, want 1 (stderr: %s)", gotExit, errOut2.String())
	}
}

// --break-lock's BreakLock call reads both lock files directly (see
// repofs.osStore.BreakLock) and refuses the same way: a symlinked lock path
// is not "no lock is held" and not "the id doesn't match" — it's a path that
// has to be removed before rdk can tell either way.
func TestApplyBreakLockReportsASymlinkedApplyLockAsLockTarget(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	if err := os.Symlink("nowhere", filepath.Join(dir, ".rdk", "apply.lock")); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := runSplit(t, dir, "apply", "--break-lock=9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("apply ran --break-lock over a symlinked apply-lock path")
	}
	assertLockTarget(t, err, "--break-lock over a symlinked .rdk/apply.lock")
	if !strings.Contains(errOut, ".rdk/apply.lock") {
		t.Errorf("stderr does not name the offending path: %q", errOut)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, ".rdk", "apply.lock")); statErr != nil {
		t.Errorf("the symlink itself was disturbed: %v", statErr)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut2 bytes.Buffer
	gotExit := execute([]string{"apply", "--break-lock=9f3a1c4e7b2d8a05"}, &out, &errOut2)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("lock-target exit = %d, want 1 (stderr: %s)", gotExit, errOut2.String())
	}
}

// --with-lock and --break-lock contradict each other: one says the lock is
// yours to run under, the other says it is stale and should be destroyed.
func TestApplyRejectsWithLockAndBreakLockTogether(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	_, _, err := runSplit(t, dir, "apply", "--with-lock=abc", "--break-lock=abc")
	if err == nil {
		t.Fatal("apply accepted --with-lock and --break-lock together")
	}
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != diag.CodeInvalidFlag {
		t.Errorf("code = %v, want %q", err, diag.CodeInvalidFlag)
	}

	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	gotExit := execute([]string{"apply", "--with-lock=abc", "--break-lock=abc"}, &out, &errOut)
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	if gotExit != 1 {
		t.Errorf("--with-lock + --break-lock exit = %d, want 1 (stderr: %s)", gotExit, errOut.String())
	}
}

// The blocked-apply error prints "--break-lock=<id>"; --break-lock= with no
// id used to run a plain apply instead, so a lock with no readable id had a
// recovery that could not be typed.
func TestApplyRejectsAnEmptyBreakLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, dir, "apply", "--break-lock=")
	if err == nil {
		t.Fatalf("apply --break-lock= succeeded, want a flag error; output:\n%s", out)
	}
	if !strings.Contains(out, "--break-lock") {
		t.Errorf("output = %q, want it to name the flag", out)
	}
}

func TestApplyRejectsAnEmptyWithLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, dir, "apply", "--with-lock="); err == nil {
		t.Fatalf("apply --with-lock= succeeded, want a flag error; output:\n%s", out)
	}
}

// Giving both flags is an error however they are spelled, including when one
// of them is empty — otherwise "empty means absent" comes back through the
// mutual-exclusion check.
func TestApplyRejectsBothLockFlagsEvenWhenOneIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, dir, "apply", "--break-lock=", "--with-lock=abc")
	if err == nil {
		t.Fatalf("both flags accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("output = %q, want the mutual-exclusion error", out)
	}
}
