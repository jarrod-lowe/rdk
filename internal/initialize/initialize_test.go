package initialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
