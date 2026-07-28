package repofs

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestMaterializeWritesTreeWithDirs(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("a.txt", []byte("a"))
	set.Bytes("sub/b.txt", []byte("b"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"managed/a.txt": "a", "managed/sub/b.txt": "b"} {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestMaterializeReplacesPriorContent(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	first.Bytes("stale.txt", []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
	if err := s.Materialize("managed", second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale file survived re-materialize")
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "fresh.txt")); err != nil {
		t.Errorf("fresh file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed.staging")); !os.IsNotExist(err) {
		t.Error("staging dir should be gone after success")
	}
}

func TestMaterializeRecoversInterruptedSwap(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("f.txt", []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "managed"), filepath.Join(root, "managed.staging")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("recovery materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "f.txt")); err != nil {
		t.Errorf("managed not restored: %v", err)
	}
}

func TestSeedCreatesOnceAndDoesNotOverwrite(t *testing.T) {
	s, root := newTestStore(t)
	if err := s.Seed("rdk/config.yaml", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed("rdk/config.yaml", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "rdk", "config.yaml"))
	if string(got) != "first" {
		t.Errorf("seed overwrote: got %q", got)
	}
}

func TestReadFileAndReadDir(t *testing.T) {
	s, root := newTestStore(t)
	os.MkdirAll(filepath.Join(root, "rdk"), 0o755)
	os.WriteFile(filepath.Join(root, "rdk", "b.yaml"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(root, "rdk", "a.yaml"), []byte("a"), 0o644)
	names, err := s.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a.yaml" || names[1] != "b.yaml" {
		t.Errorf("ReadDir = %v, want sorted [a.yaml b.yaml]", names)
	}
	data, err := s.ReadFile("rdk/a.yaml")
	if err != nil || string(data) != "a" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
}

func TestSecurityRejectsEscapes(t *testing.T) {
	s, root := newTestStore(t)
	if err := s.Seed("/etc/evil", []byte("x")); err == nil {
		t.Error("absolute path seed should fail")
	}
	if _, err := s.ReadFile("../escape"); err == nil {
		t.Error("dotdot read should fail")
	}
	external := t.TempDir()
	victim := filepath.Join(external, "victim")
	os.WriteFile(victim, []byte("precious"), 0o644)
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := s.ReadFile("link/victim"); err == nil {
		t.Error("read through escaping symlink should fail")
	}
	// Seeding through the escaping symlink must not create a file outside the
	// repo (the scenario the removed initialize symlink tests covered).
	if err := s.Seed("link/evil.yaml", []byte("x")); err == nil {
		t.Error("seed through escaping symlink should fail")
	}
	if _, err := os.Stat(filepath.Join(external, "evil.yaml")); err == nil {
		t.Error("seed wrote through an escaping symlink into an external dir")
	}
}
