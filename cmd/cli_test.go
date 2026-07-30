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
	root := NewRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
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
