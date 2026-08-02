package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// The loser of a race has to be told what to do — and warned off the door that
// is not theirs, since the error itself hands over the id both flags need.
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
	for _, want := range []string{"9f3a1c4e7b2d8a05", "--break-lock", "never --with-lock"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not mention %q: %q", want, errOut)
		}
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

	// A wrong id must not get past it.
	if _, _, err := runSplit(t, dir, "apply", "--break-lock=wrong-id"); err == nil {
		t.Error("apply proceeded with a mismatched --break-lock id")
	}

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
