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
// production's locking. sampleLock and the scratchLock/scratchApplyLock
// constants are shared with store_test.go - same package, same fixtures.
func TestMemMaterializeRefusesWhileLocked(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindHeld))
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

// The transient case: a lock found in scratchApplyLock blocks Materialize via
// the O_CREAT|O_EXCL collision, not the held-lock check above.
func TestMemMaterializeRefusesWhileAnotherApplyIsRunning(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err = m.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
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
	if _, ok := m.Files()[scratchApplyLock]; ok {
		t.Error("apply lock survived a successful apply")
	}
	// And a second run must work, which is the point.
	if err := m.Materialize("managed", set); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
}

// The id is the whole safety property, same as the real Store.
func TestMemBreakLockOnlyRemovesTheNamedLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindHeld))
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

// A hard kill is the only way a transaction lock is ever found stranded, and
// --break-lock has to clear it, same as the real Store.
func TestMemBreakLockRemovesAStrandedApplyLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b

	info, err := m.BreakLock("9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Held || info.PID != 4127 || info.Host != "builder-3" {
		t.Errorf("info = %+v, want the holder's details", info)
	}
	if _, ok := m.Files()[scratchApplyLock]; ok {
		t.Error("apply lock not removed")
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
	b, err := json.Marshal(sampleLock(lockKindHeld))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b
	if _, err := m.HoldLock("second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

// rdk lock must not return success while an apply is genuinely mid-flight.
// Mirrors TestHoldLockRefusesWhileAnApplyIsRunning.
func TestMemHoldLockRefusesWhileAnApplyIsRunning(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b

	if _, err := m.HoldLock("agent working"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if _, ok := m.Files()[scratchLock]; ok {
		t.Error("HoldLock created a held lock despite the running apply")
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
// for, and it warns. Mirrors TestUnlockRefusesAnApplyLock: this is the
// required scenario, naming an apply lock via the redirecting message even
// though Unlock only ever touches the held-lock file.
func TestMemUnlockRefusesAnApplyLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b
	err = m.Unlock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlocked an apply lock")
	}
	if !strings.Contains(err.Error(), "--break-lock") {
		t.Errorf("error %q does not point at the right door", err.Error())
	}
	if _, ok := m.Files()[scratchApplyLock]; !ok {
		t.Error("unlock touched the apply lock")
	}
}

// Migration note: mirrors TestUnlockClearsAStaleApplyKindLockFoundInTheHeldFile.
func TestMemUnlockClearsAStaleApplyKindLockFoundInTheHeldFile(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b // pre-split binary's stale content

	if err := m.Unlock("9f3a1c4e7b2d8a05"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, ok := m.Files()[scratchLock]; ok {
		t.Error("stale lock not removed")
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
	if _, ok := m.Files()[scratchApplyLock]; ok {
		t.Error("the transaction lock survived a successful apply run under an adopted lock")
	}
	if string(m.Files()["managed/f.txt"]) != "x" {
		t.Error("apply did not publish")
	}
}

// UseLock only ever reads scratchLock, so a running apply's lock can never be
// what it finds. Mirrors TestUseLockCannotAdoptARunningApplysLock.
func TestMemUseLockCannotAdoptARunningApplysLock(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b
	_, err = m.UseLock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("adopted a running apply's lock")
	}
	if !strings.Contains(err.Error(), "broken out from under you") {
		t.Errorf("error %q is not the generic no-held-lock message", err.Error())
	}
}

// Migration note: mirrors TestUseLockAdoptsAStaleApplyKindLockFoundInTheHeldFile.
func TestMemUseLockAdoptsAStaleApplyKindLockFoundInTheHeldFile(t *testing.T) {
	m := NewMem()
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchLock] = b

	if _, err := m.UseLock("9f3a1c4e7b2d8a05"); err != nil {
		t.Fatalf("UseLock: %v", err)
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
	b, err := json.Marshal(sampleLock(lockKindHeld))
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

	// Someone else broke that lock and a third run acquired a fresh one. Has
	// to be scratchApplyLock: ReleaseLock only ever reads that file now.
	b, err := json.Marshal(sampleLock(lockKindApply))
	if err != nil {
		t.Fatal(err)
	}
	m.Files()[scratchApplyLock] = b

	if err := m.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files()[scratchApplyLock]; !ok {
		t.Error("released a lock it did not take")
	}
}
