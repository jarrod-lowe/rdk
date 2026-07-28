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
	first.Bytes("stale.txt", []byte("old"))
	if err := m.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
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

func TestMemSeedAndReads(t *testing.T) {
	m := NewMem()
	m.Seed("rdk/config.yaml", []byte("cfg"))
	m.Seed("rdk/config.yaml", []byte("nope")) // no overwrite
	if b, err := m.ReadFile("rdk/config.yaml"); err != nil || string(b) != "cfg" {
		t.Errorf("ReadFile = %q, %v", b, err)
	}
	m.Seed("rdk/a.yaml", []byte("a"))
	names, err := m.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a.yaml" || names[1] != "config.yaml" {
		t.Errorf("ReadDir = %v", names)
	}
}
