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

// rdk lock takes a lock and exits leaving it. Nothing automatic may remove it:
// not a defer, not a signal. Only rdk unlock or --break-lock, both of which
// name it. Mirrors TestReleaseLockLeavesAHeldLockAlone.
func TestMemReleaseLockLeavesAHeldLockAlone(t *testing.T) {
	m := NewMem()
	info, err := m.HoldLock("agent refactoring the s3-bucket module")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "held" || info.Message == "" {
		t.Errorf("info = %+v, want a held lock carrying its message", info)
	}
	if err := m.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()[scratchLock]; !ok {
		t.Error("ReleaseLock removed a held lock")
	}
}

func TestMemHoldLockRefusesWhenAlreadyLocked(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(heldLock())
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b
	if _, err := m.HoldLock("second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

// Unlock is the routine end of your own lock, so it names the lock and refuses
// anything else. Mirrors TestUnlockOnlyReleasesTheNamedHeldLock.
func TestMemUnlockOnlyReleasesTheNamedHeldLock(t *testing.T) {
	m := NewMem()
	info, err := m.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Unlock("some-other-id"); err == nil {
		t.Error("unlocked a lock whose id did not match")
	}
	if _, ok := m.Files()[scratchLock]; !ok {
		t.Error("removed it anyway")
	}
	if err := m.Unlock(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()[scratchLock]; ok {
		t.Error("lock not removed")
	}
}

// An apply's lock is not yours to end routinely — that is what --break-lock is
// for, and it warns. Mirrors TestUnlockRefusesAnApplyLock.
func TestMemUnlockRefusesAnApplyLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(heldLock()) // kind: apply
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b
	err = m.Unlock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlocked an apply lock")
	}
	if !strings.Contains(err.Error(), "--break-lock") {
		t.Errorf("error %q does not point at the right door", err.Error())
	}
}

// Running under someone's lock must neither take nor release it. Mirrors
// TestUseLockRunsWithoutAcquiringOrReleasing.
func TestMemUseLockRunsWithoutAcquiringOrReleasing(t *testing.T) {
	m := NewMem()
	info, err := m.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UseLock(info.ID); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := m.Materialize("managed", set); err != nil {
		t.Fatalf("materialize under a held lock: %v", err)
	}
	if _, ok := m.Files()[scratchLock]; !ok {
		t.Error("the held lock did not survive the apply")
	}
	if string(m.Files()["managed/f.txt"]) != "x" {
		t.Error("apply did not publish")
	}
}

// Mirrors TestUseLockRefusesAnApplyLock.
func TestMemUseLockRefusesAnApplyLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(heldLock()) // heldLock() is kind "apply" despite the name
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b
	_, err = m.UseLock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("adopted a running apply's lock")
	}
	if !strings.Contains(err.Error(), "apply") {
		t.Errorf("error %q does not say why", err.Error())
	}
}

// Mirrors TestUseLockRejectsAMismatchedOrAbsentLock.
func TestMemUseLockRejectsAMismatchedOrAbsentLock(t *testing.T) {
	m := NewMem()
	// Nothing held: you asserted you hold a lock and you do not, which means
	// it was broken out from under you.
	if _, err := m.UseLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("adopted a lock that does not exist")
	}
	b, err := json.Marshal(heldLock())
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b
	if _, err := m.UseLock("some-other-id"); err == nil {
		t.Error("adopted someone else's lock under the wrong id")
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
