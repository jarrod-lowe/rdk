package initialize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

func newStore(t *testing.T, dir string) repofs.Store {
	t.Helper()
	s, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInitCreatesLayout(t *testing.T) {
	dir := t.TempDir()
	if err := Run(newStore(t, dir), dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("expected git repo: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "rdk", "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "kind: config") {
		t.Errorf("config.yaml missing kind: config")
	}
}

func TestInitIsSeedOnce(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)
	if err := Run(store, dir); err != nil {
		t.Fatal(err)
	}
	custom := "kind: config\nname: customized\n"
	cfgPath := filepath.Join(dir, "rdk", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(store, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != custom {
		t.Error("init overwrote a user-owned seeded file")
	}
}

func TestInitDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	if err := Run(newStore(t, dir), dir); err != nil {
		t.Fatal(err)
	}
	heads := filepath.Join(dir, ".git", "refs", "heads")
	if entries, err := os.ReadDir(heads); err == nil && len(entries) != 0 {
		t.Error("init created a commit")
	}
}

// The failure path had no test, and it now has rendering worth pinning: git's
// output is folded into the cause, and is absent entirely when git never ran.
func TestGitInitFailureCarriesGitsOutput(t *testing.T) {
	stub := t.TempDir()
	script := "#!/bin/sh\necho 'fatal: cannot mkdir' >&2\necho 'hint: check perms' >&2\nexit 128\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub)

	dir := t.TempDir()
	store, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(store, dir)
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeGitInit {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeGitInit)
	}
	// One line, so git's own "hint:" cannot masquerade as rdk's output.
	if got := d.Error(); strings.Contains(got, "\n") {
		t.Errorf("error spans several lines: %q", got)
	}
	if !strings.Contains(d.Error(), "cannot mkdir") || !strings.Contains(d.Error(), "check perms") {
		t.Errorf("error does not carry git's output: %q", d.Error())
	}
}

// A .git that git cannot use was indistinguishable from a healthy one by
// stat, so rdk seeded the config and reported success on a broken repo.
func TestMalformedGitIsNotTreatedAsARepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(store, dir)
	if err == nil {
		t.Fatal("want an error for a .git git cannot use, got success")
	}
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeGitInit {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeGitInit)
	}
}
