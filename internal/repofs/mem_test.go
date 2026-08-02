package repofs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
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
	add(t, first, Managed("stale.txt"), []byte("old"))
	if err := m.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
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
	add(t, set, Managed("f.txt"), []byte("x"))
	add(t, set, AtRepoRoot(".github/workflows/ci.yml"), []byte("x"))
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

// The fake must exclude a concurrent Materialize exactly like the real Store,
// or a component test could pass against Mem while misrepresenting
// production's locking.
func TestMemMaterializeRefusesWhileLocked(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(heldLock())
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err = m.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
	for _, want := range []string{"9f3a1c4e7b2d8a05", "4127", "builder-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if _, ok := m.Files()["managed/f.txt"]; ok {
		t.Error("published despite the lock")
	}
}

// A malformed lock must still block, same as the real Store.
func TestMemMaterializeRefusesWhileLockedEvenIfUnreadable(t *testing.T) {
	m := NewMem()
	m.Files()[scratchLock] = []byte("{not json")

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := m.Materialize("managed", set); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
}

func TestMemMaterializeReleasesTheLockOnSuccess(t *testing.T) {
	m := NewMem()
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := m.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()[scratchLock]; ok {
		t.Error("lock survived a successful apply")
	}
	// And a second run must work, which is the point.
	if err := m.Materialize("managed", set); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
}

// The id is the whole safety property, same as the real Store.
func TestMemBreakLockOnlyRemovesTheNamedLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(heldLock())
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b

	if _, err := m.BreakLock("some-other-id"); err == nil {
		t.Error("broke a lock whose id did not match")
	}
	if _, ok := m.Files()[scratchLock]; !ok {
		t.Error("removed the lock anyway")
	}

	info, err := m.BreakLock("9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Held || info.PID != 4127 || info.Host != "builder-3" {
		t.Errorf("info = %+v, want the holder's details", info)
	}
	if _, ok := m.Files()[scratchLock]; ok {
		t.Error("lock not removed")
	}
}

func TestMemBreakLockWithNoLockPresentIsAnError(t *testing.T) {
	m := NewMem()
	if _, err := m.BreakLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("want an error: the named lock does not exist")
	}
}

// Same property as osStore: releasing must not remove a lock this Mem value
// did not take, or a broken-and-replaced lock gets deleted by the run whose
// lock was broken.
func TestMemReleaseLockLeavesAReplacedLockAlone(t *testing.T) {
	m := NewMem()
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := m.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	// Put it back into "believes it holds lock X" the way acquireLock would
	// have; Materialize already released on the way out.
	m.lockHeld = true
	m.lockID = "stale-id-this-run-took"

	// Someone else broke that lock and a third run acquired a fresh one.
	b, err := json.Marshal(heldLock())
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b

	if err := m.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()[scratchLock]; !ok {
		t.Error("released a lock it did not take")
	}
}
