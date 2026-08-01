package repofs

import (
	"errors"
	"io/fs"
	"testing"
)

func TestMemReadDirMissingErrors(t *testing.T) {
	m := NewMem()
	// A directory with no entries must error like the real Store, not return
	// an empty listing (fake-vs-real fidelity).
	if _, err := m.ReadDir("rdk"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir(missing) err = %v, want fs.ErrNotExist", err)
	}
}

func TestMemMaterializeAndReplace(t *testing.T) {
	m := NewMem()
	first := NewFileSet()
	first.Bytes(Managed("stale.txt"), []byte("old"))
	if err := m.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	second.Bytes(Managed("fresh.txt"), []byte("new"))
	if err := m.Materialize("managed", second); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()["managed/stale.txt"]; ok {
		t.Error("stale entry survived replace")
	}
	if string(m.Files()["managed/fresh.txt"]) != "new" {
		t.Error("fresh entry missing")
	}
}

// The fake must reject what the real store rejects, or every test using it
// misrepresents production.
func TestMemMaterializeRejectsAnOutsideEntry(t *testing.T) {
	m := NewMem()
	set := NewFileSet()
	if err := set.Bytes(Managed("f.txt"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := set.Bytes(AtRepoRoot(".github/workflows/ci.yml"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := m.Materialize("managed", set); err == nil {
		t.Fatal("want an error for an outside entry")
	}
	if _, ok := m.Files()["managed/f.txt"]; ok {
		t.Error("wrote a partial tree before rejecting")
	}
}

func TestMemSeedAndReads(t *testing.T) {
	m := NewMem()
	m.Seed("rdk/config.yaml", []byte("cfg"))
	m.Seed("rdk/config.yaml", []byte("nope")) // no overwrite
	if b, err := m.ReadFile("rdk/config.yaml"); err != nil || string(b) != "cfg" {
		t.Errorf("ReadFile = %q, %v", b, err)
	}
	m.Seed("rdk/a.yaml", []byte("a"))
	entries, err := m.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a.yaml" || entries[1].Name != "config.yaml" {
		t.Errorf("ReadDir = %v", entries)
	}
}

// Mem models directories implicitly, via paths; it must still report them, or a
// component test would not see what the real Store sees.
func TestMemReadDirReportsDirectories(t *testing.T) {
	m := NewMem()
	m.Seed("rdk/a.yaml", []byte("a"))
	m.Seed("rdk/nested/b.yaml", []byte("b"))
	entries, err := m.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "a.yaml" || entries[0].IsDir {
		t.Errorf("entries[0] = %+v, want a.yaml file", entries[0])
	}
	if entries[1].Name != "nested" || !entries[1].IsDir {
		t.Errorf("entries[1] = %+v, want nested dir", entries[1])
	}
}
