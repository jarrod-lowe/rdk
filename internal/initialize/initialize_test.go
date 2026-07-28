package initialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesLayout(t *testing.T) {
	dir := t.TempDir()
	if err := Run(dir); err != nil {
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
		t.Errorf("config.yaml missing kind: config:\n%s", cfg)
	}
}

func TestInitIsSeedOnce(t *testing.T) {
	dir := t.TempDir()
	if err := Run(dir); err != nil {
		t.Fatal(err)
	}
	custom := "kind: config\nname: customized\n"
	cfgPath := filepath.Join(dir, "rdk", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(dir); err != nil { // re-running init must not clobber (DD-3)
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != custom {
		t.Error("init overwrote a user-owned seeded file")
	}
}

func TestInitDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	if err := Run(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(dir, ".git", "refs", "heads"))
		if len(entries) != 0 {
			t.Error("init created a commit; committing is the developer's act")
		}
	}
}

func TestInitRejectsSymlinkedDefsDir(t *testing.T) {
	dir := t.TempDir()
	external := t.TempDir()
	// rdk/ is a symlink to an external directory.
	if err := os.Symlink(external, filepath.Join(dir, "rdk")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := Run(dir); err == nil {
		t.Fatal("want error for a symlinked rdk/ dir, got nil")
	}
	if _, err := os.Stat(filepath.Join(external, "config.yaml")); err == nil {
		t.Error("seed was written through a symlinked defs dir into an external location")
	}
}

func TestInitDoesNotWriteThroughSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	external := t.TempDir()
	extTarget := filepath.Join(external, "target") // dangling: does not exist yet
	if err := os.MkdirAll(filepath.Join(dir, "rdk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extTarget, filepath.Join(dir, "rdk", "config.yaml")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Seeding must not follow the symlink and create the external target.
	if err := Run(dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(extTarget); err == nil {
		t.Error("seed followed a symlinked config.yaml and wrote outside the repo")
	}
}
