# Lock Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the five defects the lock audit found — a live transaction lock that `--break-lock` can destroy silently, a window where an interrupt strands a lock, an unbreakable empty id, `rdk lock` succeeding under a running apply, and lock paths that a symlink can neutralise — so that goals 2 and 5 of the lock design hold as written.

**Architecture:** All five fixes live in `internal/repofs/store.go`, mirrored in `internal/repofs/mem.go`, and surface through `internal/apply/apply.go` and `cmd/apply.go`. The shape of every fix is the same one the design already commits to: no staleness heuristics, no new primitive, compare-and-swap on an id that was actually observed. Two of the five (lock creation, lock revalidation) turn a silent window into a loud abort rather than trying to make the window not exist; the other three remove a state the code could not express a recovery for.

**Tech Stack:** Go 1.26, `os.Root`, `crypto/rand`, `encoding/json`, `log/slog` via `internal/logger`, cobra.

---

## Background you need

Read these before starting. They are short and every task depends on them.

- `docs/superpowers/specs/2026-08-02-apply-lock-design.md` — the design being repaired. Sections "Two kinds, two files", "Where the lock sits in the sequence", "Stated limits".
- `docs/philosophy.md` rules 3 (one owner per file), 6 (loud, never silent), 7 (one mechanism, reused), 11 (errors are for the reader who has to fix them), 12 (messiness is budgeted), 13 (always write, never diff).
- `docs/errors.md` — every `diag` code is documented here, and `internal/diag` has a test that fails if a code exists without a row **or** a row exists without a code. Adding a code means adding a row in the same commit.

### The seven goals of locking, for reference

1. Two concurrent applies cannot produce a tree that no single run generated.
2. A person or agent can hold the repository against applies, and say why.
3. Every way past a lock requires having observed that specific lock (compare-and-swap).
4. No dependence on filesystem lock propagation (this is why it is `O_CREAT|O_EXCL`, never `flock`).
5. Nothing is stranded by an ordinary exit, including Ctrl-C — only a hard kill, and its recovery must be stated.
6. Misuse is visible, not prevented.
7. The transaction claim (`.rdk/apply.lock`) and the repository hold (`.rdk/lock`) stay separable.

Tasks 1–5 restore goals 1, 2 and 5. Nothing in this plan may weaken goals 3, 4 or 7.

### The two facts that make these bugs possible

- `readLockFile` uses `ReadFile`, which **follows** symlinks; `OpenFile(O_EXCL)` and `Link` **do not**. A dangling symlink at a lock path is therefore "absent" to a reader and "present" to a writer. That asymmetry is Task 1.
- `acquireLock` creates the visible lock file first and writes its contents second, and records ownership third. Every window between those three is a way to strand a lock nobody can name. That is Task 2.

---

## File Structure

| File | Change |
|---|---|
| `internal/repofs/store.go` | All five fixes. Gains `ErrLockTarget`, `ErrLockLost`, `ErrLockNotReleased` and their error types; `readLockFile` gains an Lstat guard and becomes authoritative for `Kind`/`Path`; `acquireLock` becomes write-then-link; `Materialize` revalidates ownership before the destructive renames; `HoldLock` runs under the transaction lock. |
| `internal/repofs/seams.go` | **New.** Two package-level test seams, nil in production, that let the destructive-window tests be deterministic instead of timing-dependent. |
| `internal/repofs/mem.go` | Mirrors every semantic change so a component test cannot pass against the fake while misrepresenting production. |
| `internal/repofs/store_test.go` | Tests for Tasks 1–6. |
| `internal/repofs/mem_test.go` | Mirror tests where `Mem` can express the behaviour. |
| `internal/apply/apply.go` | Maps the three new sentinels to diagnostics; `LockedDiagnostic` keys on which file the lock came from, not on the JSON's `kind`, and handles a lock whose id is unreadable. |
| `cmd/apply.go` | `Flags().Changed` instead of `!= ""`; an explicitly empty id is rejected. |
| `internal/diag/codes.go` | `CodeLockTarget`, `CodeLockLost`, `CodeLockNotReleased`. |
| `docs/errors.md` | One row per new code. |
| `docs/superpowers/specs/2026-08-02-apply-lock-design.md` | Corrects the two false claims and records the new sequence and limits. |

Task order is dependency order. Task 2 depends on Task 1's `lockPathUsable` helper; Task 4 depends on Task 2's scoping of ownership to the transaction lock; Task 5 depends on Task 1's authoritative `Path`. Do not reorder.

---

### Task 1: A lock path is a regular file, or nothing

**Why:** A dangling symlink at `.rdk/lock` makes the held lock silently stop excluding — `readLockFile` follows it, gets ENOENT, and reports "no lock". That is exactly the failure mode `flock` was rejected for (goal 4's whole argument), reached through a different door. At `.rdk/apply.lock` the same symlink makes the lock *unbreakable and invisible*: the exclusive create sees it (EEXIST, so applies block) but every reader says it is not there, so no id can ever be printed. A directory at either path is worse: today it exits 2, "an rdk bug. Report it", for a state the user can fix in one command.

**Files:**
- Modify: `internal/repofs/store.go`
- Modify: `internal/repofs/mem.go`
- Modify: `internal/apply/apply.go`
- Modify: `internal/diag/codes.go`
- Modify: `docs/errors.md`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// A dangling symlink at the held lock's path used to read as "no lock is
// held", so the exclusion silently stopped excluding — the exact failure
// flock was rejected for, reached through a different door.
func TestMaterializeRefusesADanglingSymlinkAtTheHeldLock(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, scratchLock)); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLockTarget) {
		t.Fatalf("Materialize err = %v, want ErrLockTarget", err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %q, want it to name what is in the way", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "managed")); !os.IsNotExist(statErr) {
		t.Error("published a tree while the held lock path was unusable")
	}
}

// The transaction lock's path has the mirror problem: the exclusive create
// sees the symlink (so applies block forever) but every reader says it is
// absent, so no id is ever printed and nothing can name it to break it.
func TestMaterializeRefusesADanglingSymlinkAtTheApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, scratchApplyLock)); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrLockTarget) {
		t.Fatalf("Materialize err = %v, want ErrLockTarget", err)
	}
}

// A directory at a lock path is user-fixable state, so it must not surface as
// "an rdk bug. Report it".
func TestHoldLockRefusesADirectoryAtTheLockPath(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, scratchLock), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.HoldLock("work")
	if !errors.Is(err, ErrLockTarget) {
		t.Fatalf("HoldLock err = %v, want ErrLockTarget", err)
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("err = %q, want it to name what is in the way", err)
	}
}

// Every entry point that reads a lock has to refuse the same way, or one of
// them becomes the door the others are guarding.
func TestEveryLockEntryPointRefusesAnUnusableLockPath(t *testing.T) {
	for _, file := range []string{scratchLock, scratchApplyLock} {
		t.Run(file, func(t *testing.T) {
			s, root := newTestStore(t)
			if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("nowhere", filepath.Join(root, file)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BreakLock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("BreakLock err = %v, want ErrLockTarget", err)
			}
			if err := s.Unlock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("Unlock err = %v, want ErrLockTarget", err)
			}
			if _, err := s.UseLock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("UseLock err = %v, want ErrLockTarget", err)
			}
		})
	}
}

// The file a lock was read from is what decides how it is described — not the
// kind field inside it, which a pre-split binary (or a hand-edited file) can
// contradict.
func TestReadLockFileTrustsThePathOverTheRecordedKind(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	writeLock(t, root, LockInfo{ID: "aaaa", Kind: lockKindApply, PID: 1, Host: "h", Since: "s"})
	info, err := st.readLockFile(scratchLock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != lockKindHeld {
		t.Errorf("Kind = %q, want %q — the file it came from is authoritative", info.Kind, lockKindHeld)
	}
	if info.Path != scratchLock {
		t.Errorf("Path = %q, want %q", info.Path, scratchLock)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/repofs/ -run 'LockTarget|DanglingSymlinkAtThe|UnusableLockPath|TrustsThePath' -v`

Expected: FAIL — `undefined: ErrLockTarget`, and `info.Path undefined`.

- [ ] **Step 3: Add the sentinel and its error type**

In `internal/repofs/store.go`, after the `ErrLocked` block (around line 150):

```go
// ErrLockTarget reports that a lock path exists as something other than a
// regular file. It is separate from ErrLocked because nothing is actually
// holding the repository: a dangling symlink at .rdk/lock reads as absent to
// ReadFile but is refused by the exclusive create, so the held lock silently
// stops excluding while still being impossible to replace — the same
// "manufactures confidence" failure flock was rejected for. At
// .rdk/apply.lock the pair is worse: applies block on a lock no reader can
// see, so no id is ever printed and --break-lock has nothing to name. Both
// are user-fixable by removing one path, which is why this exits 1 with that
// instruction rather than 2 as an rdk bug.
var ErrLockTarget = errors.New("lock path is not a regular file")

// lockTargetError carries what is actually occupying the path, since the
// caller's summary cannot know that — the same shape as scratchTargetError.
type lockTargetError struct{ err error }

func (e *lockTargetError) Error() string        { return e.err.Error() }
func (e *lockTargetError) Unwrap() error        { return e.err }
func (e *lockTargetError) Is(target error) bool { return target == ErrLockTarget }
```

- [ ] **Step 4: Add `Path` to `LockInfo` and make the file authoritative**

In `internal/repofs/store.go`, add to the `LockInfo` struct (after `Held`):

```go
	// Path is the lock file this record was read from, repo-relative. Not
	// serialised: it is a property of where the record was found, not of the
	// record, and writing it would let a copied file lie about its own
	// location. It is what a diagnostic names when a lock's id cannot be read
	// and so cannot be handed to --break-lock.
	Path string `json:"-"`
```

Replace `readLockFile` (around line 629) with:

```go
// readLockFile reads whatever is at file, held lock or transaction lock.
//
// The Lstat comes first because ReadFile follows symlinks and the exclusive
// create does not: without it, a dangling symlink at a lock path reads as
// "no lock" while still blocking every acquisition — an exclusion that has
// silently stopped excluding, which is worse than no lock at all (see
// ErrLockTarget). Anything that is not a regular file is refused for the same
// reason, and named, because the fix is to remove one specific path.
//
// A parse failure is still not reported as an error: the file existing is
// itself the fact that matters (something is blocking), so the caller gets a
// zero-valued LockInfo rather than losing that fact to a JSON error. Held is
// set whenever a regular file was found at all, parseable or not.
//
// Kind and Path are set from the file this read actually came from, after
// unmarshalling, so they override whatever the JSON claimed. Which file a
// lock lives in is what governs behaviour (see the constants at the top of
// this file), so a record whose kind field disagrees — a pre-split binary's
// file, or a hand-edited one — must not be able to make a caller describe it
// as the other thing.
func (s *osStore) readLockFile(file string) (LockInfo, error) {
	// The not-exist error is returned unwrapped: every caller distinguishes
	// "absent" from "unusable" with os.IsNotExist, and wrapping it would make
	// an absent lock read as a failure.
	if st, err := s.root.Lstat(file); err != nil {
		return LockInfo{}, err
	} else if !st.Mode().IsRegular() {
		return LockInfo{}, &lockTargetError{err: fmt.Errorf("%s is a %s, not a lock file; remove it", file, modeKind(st.Mode()))}
	}
	b, err := s.root.ReadFile(file)
	if err != nil {
		return LockInfo{}, err
	}
	var info LockInfo
	_ = json.Unmarshal(b, &info) // best effort; see doc comment
	info.Held = true
	info.Path = file
	info.Kind = lockKindFor(file)
	return info, nil
}

// lockKindFor names the kind a lock found in file is, regardless of what its
// own JSON says. The file is the fact; the field is a display copy.
func lockKindFor(file string) string {
	if file == scratchLock {
		return lockKindHeld
	}
	return lockKindApply
}
```

- [ ] **Step 5: Guard the create path too**

`readLockFile`'s guard covers every reader, but `acquireLock` creates without reading first, so a symlink there would surface as a bare EEXIST with no id. In `internal/repofs/store.go`, add before the `OpenFile` in `acquireLock`:

```go
	// Checked before creating, not just when reading: the exclusive create
	// refuses a symlink with EEXIST, which is indistinguishable from a real
	// lock being present — so without this the caller would report "another
	// apply is running" about a lock that does not exist and cannot be named.
	if _, err := s.readLockFile(file); err != nil && !os.IsNotExist(err) {
		return LockInfo{}, err
	}
```

- [ ] **Step 6: Mirror the kind authority in `Mem`**

`Mem` models no filesystem, so it cannot produce a symlink or a directory at a lock path — there is nothing to mirror for the Lstat guard, and inventing one would model a state the fake cannot reach. It *can* disagree about `Kind`, so mirror that. In `internal/repofs/mem.go`, in `readLockFile`, after `info.Held = true`:

```go
	info.Path = file
	info.Kind = lockKindFor(file)
```

and extend that function's doc comment with:

```go
// Kind and Path come from the file, not the record, exactly as osStore does —
// otherwise a component test could plant a record whose kind disagrees with
// its file and see Mem describe it differently from production.
```

- [ ] **Step 7: Add the diagnostic code**

In `internal/diag/codes.go`, add `CodeLockTarget = "lock-target"` next to `CodeLockHeld`, and add it to the `All` slice on the `CodeApplyLocked, CodeLockMismatch, ...` line.

- [ ] **Step 8: Map the sentinel in `apply.Run`**

In `internal/apply/apply.go`, inside the `store.Materialize` error handling, **before** the `ErrLocked` branch (order matters: a lock-target error is not a locked error, and putting it after would still work but reads as an afterthought):

```go
		if errors.Is(err, repofs.ErrLockTarget) {
			// Not ErrLocked: nothing is holding the repository. Something is
			// occupying a path rdk needs, and while it sits there the lock
			// neither excludes reliably nor can be named to be broken — so the
			// fix is a path to remove, which the cause already names.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeLockTarget,
				Summary: "cannot use rdk's lock files",
				Hint:    "remove the path named above, then re-run",
			})
		}
```

`rdk lock` and `rdk unlock` reach `ErrLockTarget` through their own paths; both already `diag.Wrap` an unrecognised error into `lock-mismatch`, which would be wrong here. In `cmd/lock.go`, in `lockCmd`'s error branch, before the `LockedDiagnostic` check:

```go
				if errors.Is(err, repofs.ErrLockTarget) {
					return diag.Wrap(err, diag.Diagnostic{
						Code:    diag.CodeLockTarget,
						Summary: "cannot use rdk's lock files",
						Hint:    "remove the path named above, then re-run",
					})
				}
```

and the identical block in `unlockCmd`'s error branch, before its `diag.Wrap(err, ... CodeLockMismatch ...)`. Add `"errors"` to `cmd/lock.go`'s imports.

- [ ] **Step 9: Document the code**

In `docs/errors.md`, in the exit-1 table, after the `lock-mismatch` row:

```markdown
| `lock-target` | One of rdk's lock paths (`.rdk/lock` or `.rdk/apply.lock`) exists as something other than a regular file — most often a symlink, which reads as absent but still blocks every acquisition, so the lock stops excluding while becoming impossible to name or break. | Remove the path the message names, then re-run. Nothing is holding the repository; there is no lock to break. |
```

- [ ] **Step 10: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 11: Commit**

```bash
git add internal/repofs/store.go internal/repofs/mem.go internal/repofs/store_test.go internal/apply/apply.go cmd/lock.go internal/diag/codes.go docs/errors.md
git commit -m "fix(repofs): a lock path is a regular file, or nothing

A dangling symlink at .rdk/lock read as absent to ReadFile while still
being refused by the exclusive create: the held lock silently stopped
excluding but could not be replaced — the same manufactured confidence
flock was rejected for. At .rdk/apply.lock the pair was worse: applies
blocked on a lock no reader could see, so no id was ever printed and
--break-lock had nothing to name. A directory at either path exited 2
as an rdk bug for state the user can fix in one command.

readLockFile now Lstats before reading and refuses anything that is not
a regular file via ErrLockTarget, naming what is in the way; acquireLock
runs the same check before creating, so an EEXIST from a symlink is
never reported as another apply running. LockInfo gains Path, and Kind
is now set from the file the record was read from rather than trusted
from its own JSON — which file a lock lives in is what governs
behaviour, so a record that disagrees must not be able to make a caller
describe it as the other thing."
```

---

### Task 2: A lock file is complete the instant it exists

**Why:** `acquireLock` creates the visible file, then writes it, then records ownership. Two windows follow from that. A signal in the first leaves a **zero-length** lock file — present enough to block every apply, empty enough that no id can be read, so `--break-lock` cannot name it and the printed recovery is unusable. A signal in the second leaves a complete file the handler will not remove, because `lockHeld` is still false. Measured: 3000 SIGTERM'd applies, 568 reached the handler, 14 stranded a lock; a focused band gave 34 of 1261. About three quarters of the strands were zero-length. The same two windows are reachable without a signal at all — a full disk (proven with `RLIMIT_FSIZE`) leaves exactly the zero-length file.

The fix uses the pattern `seedNew` already uses (rule 7): write the whole record to a scratch name, `Sync`, `Close`, then `Link` it into place. `link()` fails with `EEXIST` if anything is there, so it is exactly as exclusive as `O_CREAT|O_EXCL` — and on NFS it is the more reliable of the two, which strengthens goal 4 rather than trading it away. Ownership is recorded *before* the link and cleared if the link fails, so there is no ordering in which the handler can see a lock it does not know is its own.

**Files:**
- Modify: `internal/repofs/store.go`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// The lock file must never be observable in a half-written state: a
// zero-length lock blocks every apply while carrying no id, so --break-lock
// has nothing to name and the printed recovery cannot be typed.
func TestLockFileIsCompleteTheInstantItIsVisible(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	bad := make(chan string, 1)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(root, ScratchDir, "apply.lock"))
			if err != nil {
				continue // absent is fine; partial is not
			}
			var info LockInfo
			if err := json.Unmarshal(b, &info); err != nil || info.ID == "" {
				select {
				case bad <- string(b):
				default:
				}
				return
			}
		}
	}()

	for i := 0; i < 200; i++ {
		if _, err := st.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.ReleaseLock(); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	select {
	case b := <-bad:
		t.Fatalf("observed a lock file that was not a complete record: %q", b)
	default:
	}
}

// Ownership has to be recorded before the lock becomes visible, or a signal
// arriving in between leaves a complete lock the handler declines to remove
// because it does not know it is its own.
func TestAcquireLockRecordsOwnershipBeforeTheLockIsVisible(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	seen := make(chan bool, 1)
	afterLockOwnershipRecorded = func() {
		_, err := os.Lstat(filepath.Join(root, ScratchDir, "apply.lock"))
		seen <- os.IsNotExist(err)
	}
	t.Cleanup(func() { afterLockOwnershipRecorded = nil })

	if _, err := st.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
		t.Fatal(err)
	}
	if !<-seen {
		t.Error("the lock file was already visible when ownership was recorded")
	}
	st.lockMu.Lock()
	held, id := st.lockHeld, st.lockID
	st.lockMu.Unlock()
	if !held || id == "" {
		t.Errorf("lockHeld/lockID = %v/%q, want ownership recorded", held, id)
	}
}

// Losing the exclusive-create race must leave no ownership behind: the lock
// on disk is someone else's, and a release that believed otherwise would
// remove a live lock.
func TestAcquireLockClearsOwnershipWhenItLosesTheRace(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	writeApplyLock(t, root, sampleLock(lockKindApply))
	if _, err := st.acquireLock(scratchApplyLock, lockKindApply, ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("acquireLock err = %v, want ErrLocked", err)
	}
	st.lockMu.Lock()
	held := st.lockHeld
	st.lockMu.Unlock()
	if held {
		t.Error("ownership survived a lost race; a release would remove someone else's live lock")
	}
	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Error("ReleaseLock removed a lock this store never acquired")
	}
}

// Only the transaction lock is ever this store's to release. Recording a held
// lock's id in the same fields would make the deferred release in Task 4's
// HoldLock compare the wrong id and strand the transaction lock.
func TestAcquireLockDoesNotClaimOwnershipOfAHeldLock(t *testing.T) {
	s, _ := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchLock, lockKindHeld, "work"); err != nil {
		t.Fatal(err)
	}
	st.lockMu.Lock()
	held := st.lockHeld
	st.lockMu.Unlock()
	if held {
		t.Error("a held lock was recorded as this store's transaction lock")
	}
}

// The scratch dir must not accumulate the temporary records lock creation
// writes through.
func TestAcquireLockLeavesNoTemporaryFiles(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ScratchDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".gitignore" {
			t.Errorf("scratch dir holds %q, want only .gitignore", e.Name())
		}
	}
}
```

Add `"encoding/json"` to the test file's imports if it is not already there (it is — `writeLockFile` uses it).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/repofs/ -run 'AcquireLock|LockFileIsComplete' -v`

Expected: FAIL — `undefined: afterLockOwnershipRecorded`, and `TestAcquireLockDoesNotClaimOwnershipOfAHeldLock` fails because today's `acquireLock` records ownership for both files.

- [ ] **Step 3: Create the test seams file**

Create `internal/repofs/seams.go`:

```go
package repofs

// Test seams. Both are nil in production and are called only if set, so they
// cost one nil check on paths that already do filesystem work.
//
// They exist because the windows this package's hardest bugs live in are, by
// construction, between two syscalls: a test that tried to hit them by racing
// goroutines would be timing-dependent, and a timing-dependent test for a
// timing bug is one that passes on the machine where the bug is worst. These
// make the same assertions deterministic. They are package-level vars rather
// than fields on osStore so that no production caller can reach them: nothing
// outside this package can set them, and nothing inside sets them except a
// test.
var (
	// afterLockOwnershipRecorded fires inside acquireLock once this store has
	// recorded the lock as its own and before the lock becomes visible on
	// disk. A test uses it to assert that ordering, which is what keeps a
	// signal in the gap from stranding a lock the handler will not release.
	afterLockOwnershipRecorded func()

	// afterStaging fires inside Materialize once the new tree is fully staged
	// in .rdk/new and before anything destructive runs. A test uses it to
	// break this run's lock at the exact point where doing so used to produce
	// a mixed tree in silence.
	afterStaging func()

	// afterApplyLockHeldByHoldLock fires inside HoldLock once it holds the
	// transaction lock and before it creates the held lock. A test uses it to
	// assert that an apply is genuinely excluded for that whole span, which
	// is the property that stops rdk lock returning success underneath a
	// running apply.
	afterApplyLockHeldByHoldLock func()

	// afterPublish fires inside Materialize once the tree is published and
	// before the sweep and the release. A test uses it to make the release
	// itself fail, which is the only way to reach the path where the apply
	// succeeded but its lock is still on disk.
	afterPublish func()
)
```

- [ ] **Step 4: Rewrite `acquireLock`**

Replace the body of `acquireLock` in `internal/repofs/store.go` (keep the existing doc comment's first paragraph, and replace the `usingLock` paragraph as shown):

```go
// acquireLock takes a lock by making it appear atomically and complete —
// scratchLock for a held lock, scratchApplyLock for a transaction lock; kind
// is recorded in the JSON as which one this was (see the Kind field's doc
// comment), but file is what actually governs.
//
// The record is written to a scratch name, flushed, closed, and only then
// linked into place. link() fails with EEXIST if anything is already at the
// destination, so it is exactly as exclusive as O_CREAT|O_EXCL — and over NFS
// it is the more dependable of the two, which is the same reason both were
// chosen over flock: flock over NFS is emulated as a whole-file POSIX lock
// that degrades to purely local (excluding nothing) under the
// local_lock=flock/all mount options, with no error to say so. A lock that
// silently does not lock is worse than no lock, because it manufactures
// confidence — see docs/superpowers/specs/2026-08-02-apply-lock-design.md.
//
// Creating the visible name first and writing into it second — what this used
// to do — left the lock observable while empty. That is not a cosmetic
// window: a zero-length lock still blocks every apply, but carries no id, so
// --break-lock has nothing to name and the recovery the blocked-apply error
// prints cannot be typed. A signal, a full disk, or a power loss all landed
// there. Now the visible name only ever comes into existence already
// complete.
//
// Ownership is recorded before the link, not after, and cleared if the link
// fails. The old order left a second window: a signal between a successful
// create and the assignment left a perfectly good lock that the handler
// declined to remove, because lockHeld still said this store held nothing.
// Recording first inverts which way the race can go wrong, and the wrong way
// is now harmless — a release that runs before the link finds the file absent
// or someone else's, compares ids, and removes nothing.
//
// Only the transaction lock is recorded as this store's. ReleaseLock only
// ever targets scratchApplyLock, so a held lock's id in those fields was
// always meaningless; it becomes actively wrong once HoldLock acquires both
// (see HoldLock), because the second acquisition would overwrite the first's
// id and the deferred release would then fail its own id compare and strand
// the transaction lock.
func (s *osStore) acquireLock(file, kind, message string) (LockInfo, error) {
	// Checked before creating, not just when reading: the exclusive create
	// refuses a symlink with EEXIST, which is indistinguishable from a real
	// lock being present — so without this the caller would report "another
	// apply is running" about a lock that does not exist and cannot be named.
	if _, err := s.readLockFile(file); err != nil && !os.IsNotExist(err) {
		return LockInfo{}, err
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return LockInfo{}, err
	}
	info := LockInfo{
		ID:      hex.EncodeToString(idBytes),
		Kind:    kind,
		Host:    hostname(),
		PID:     os.Getpid(),
		Since:   time.Now().UTC().Format(time.RFC3339),
		Message: message,
	}
	b, err := json.Marshal(info)
	if err != nil {
		return LockInfo{}, err
	}

	// Random, not sequential or fixed, for the same reason as
	// publishScratchGitignore's suffix: two concurrent acquisitions must not
	// choose the same scratch name and stomp each other's in-flight write.
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return LockInfo{}, err
	}
	tmp := ScratchDir + "/lock." + hex.EncodeToString(suffix) + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return LockInfo{}, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = s.root.Remove(tmp)
		return LockInfo{}, err
	}
	// Flushed before the link, so "visible implies complete" survives a power
	// loss and not just a signal: without this the directory entry the link
	// creates can outlive the bytes it points at, which is the zero-length
	// lock again by a slower route. It does not make the lock durable — the
	// scratch directory's own entry is not synced — and it does not need to:
	// a lock that vanishes entirely is the harmless failure.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = s.root.Remove(tmp)
		return LockInfo{}, err
	}
	if err := f.Close(); err != nil {
		_ = s.root.Remove(tmp)
		return LockInfo{}, err
	}

	owned := file == scratchApplyLock
	if owned {
		s.lockMu.Lock()
		s.lockHeld = true
		s.lockID = info.ID
		s.lockMu.Unlock()
		if afterLockOwnershipRecorded != nil {
			afterLockOwnershipRecorded()
		}
	}
	if err := s.root.Link(tmp, file); err != nil {
		if owned {
			s.lockMu.Lock()
			// Only this call's own claim is withdrawn. Comparing the id first
			// matters because a concurrent ReleaseLock may already have
			// settled and cleared it, and blindly writing false would then be
			// clearing a state this call no longer owns.
			if s.lockID == info.ID {
				s.lockHeld = false
				s.lockID = ""
			}
			s.lockMu.Unlock()
		}
		_ = s.root.Remove(tmp)
		if os.IsExist(err) {
			// Read whatever is there for the message; a malformed or
			// unreadable lock must still block (rule 6) rather than let a
			// corrupt file silently disable the exclusion, so parse failures
			// here are swallowed and describeLock is handed whatever did come
			// through, even if that's nothing.
			existing, _ := s.readLockFile(file)
			return LockInfo{}, lockedErrorFor(existing)
		}
		return LockInfo{}, err
	}
	// Best-effort: file is already complete by the time Link returns — Link
	// only adds a second directory entry for the same bytes — so a temp left
	// here by a failed Remove is clutter in the gitignored scratch dir, not a
	// reason to fail an acquisition that succeeded.
	_ = s.root.Remove(tmp)
	return info, nil
}
```

- [ ] **Step 5: Fix the two comments that describe the old ownership rule**

In `internal/repofs/store.go`, the `lockMu` comment on `osStore` currently ends with a paragraph saying `HoldLock also uses acquireLock and sets these the same way`. Replace that sentence with:

```go
	// only ever set for scratchApplyLock: a held lock is never this store's to
	// release, so recording one here would be at best meaningless and at worst
	// (once HoldLock acquires both) the thing that strands a transaction lock
	// by making its release compare the wrong id.
```

In `Unlock`, the block that clears `lockHeld` when `s.lockID == id` is now unreachable — `lockID` only ever holds a transaction lock's id, and `Unlock` only ever matches against the held lock's. Replace:

```go
			s.lockMu.Lock()
			if s.lockID == id {
				s.lockHeld = false
			}
			s.lockMu.Unlock()
```

with:

```go
			// No ownership to clear: lockHeld/lockID track only the
			// transaction lock (see acquireLock), and a held lock's id can
			// never appear there, so there is nothing here this call could be
			// the owner of.
```

Make the same edit in `internal/repofs/mem.go`'s `Unlock`, replacing:

```go
		if m.lockID == id {
			m.lockHeld = false
		}
```

with the same comment, and in `Mem.acquireLock` replace:

```go
	m.lockHeld = true
	m.lockID = info.ID
```

with:

```go
	// Only the transaction lock is this Mem's to release — mirrors
	// osStore.acquireLock, where recording a held lock's id would make
	// HoldLock's deferred release compare the wrong id.
	if file == scratchApplyLock {
		m.lockHeld = true
		m.lockID = info.ID
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 7: Commit**

```bash
git add internal/repofs/store.go internal/repofs/mem.go internal/repofs/seams.go internal/repofs/store_test.go
git commit -m "fix(repofs): a lock file is complete the instant it exists

acquireLock created the visible lock, then wrote it, then recorded
ownership. A signal in the first gap left a zero-length lock: present
enough to block every apply, empty enough that no id could be read, so
--break-lock had nothing to name and the recovery the blocked-apply
error prints could not be typed. A signal in the second gap left a
complete lock the handler declined to remove, because lockHeld was
still false. Measured on 3000 SIGTERM'd applies: 568 reached the
handler, 14 stranded a lock, about three quarters of them zero-length.
A full disk and a power loss reach the same state without a signal.

The record is now written to a scratch name, flushed, closed, and
linked into place, reusing the pattern seedNew already uses: link()
fails with EEXIST just as the exclusive create did, and over NFS is the
more dependable of the two, so the exclusion is unchanged and goal 4 is
strengthened rather than traded. Ownership is recorded before the link
and withdrawn if it fails, which inverts the second race into its
harmless direction. Only the transaction lock is recorded as this
store's, which a held lock never was in any meaningful sense and which
HoldLock is about to depend on."
```

---

### Task 3: An apply that loses its lock aborts loudly

**Why:** This is the one that produces a wrong tree. `--break-lock` removes a transaction lock without asking whether it is live, and the blocked-apply error *instructs the user to do exactly that*: "wait for it; if it is stranded use `--break-lock=L`". Follow that while A is genuinely running and B wipes A's claim, both stage into `.rdk/new`, both publish, both exit 0, and the tree is a mixture no single run generated. Goal 3 does not help: B did observe L. Goal 1 is violated by construction, because the design forbids staleness heuristics yet requires the user to make precisely that live-versus-stranded judgement with less information than rdk has.

rdk cannot make `--break-lock` safe without a heuristic it has ruled out. What it can do is stop the victim from participating in the corruption: A re-reads `.rdk/apply.lock` immediately before the destructive renames and immediately before the sweep, and refuses to continue if it is no longer the lock A took. Silent corruption becomes a loud abort with nothing published, which is what rule 6 asks for and what makes `--break-lock` survivable rather than catastrophic.

The re-read is not a fix for the race — it narrows it to the width of one rename — and the plan says so rather than claiming otherwise (see Task 6's doc correction, which exists because the last such claim was false).

**Files:**
- Modify: `internal/repofs/store.go`
- Modify: `internal/repofs/mem.go`
- Modify: `internal/apply/apply.go`
- Modify: `internal/diag/codes.go`
- Modify: `docs/errors.md`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// The corruption this exists to stop: --break-lock on a live transaction
// lock, which the blocked-apply error actively instructs the user to do.
// Nothing may be published after this run's claim is gone.
func TestMaterializeAbortsWhenItsLockIsBrokenBeforePublishing(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("keep.txt"), []byte("original"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}

	afterStaging = func() {
		if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterStaging = nil })

	second := NewFileSet()
	add(t, second, Managed("new.txt"), []byte("replacement"))
	err := s.Materialize("managed", second)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("Materialize err = %v, want ErrLockLost", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "managed", "keep.txt"))
	if readErr != nil {
		t.Fatalf("the published tree was disturbed after the lock was lost: %v", readErr)
	}
	if string(got) != "original" {
		t.Errorf("keep.txt = %q, want the tree left exactly as it was", got)
	}
}

// A lock that was broken and replaced is the same abort, and the message has
// to name the lock that holds the repository now — that is the id the reader
// needs, not the dead one this run was carrying.
func TestMaterializeAbortsWhenItsLockWasBrokenAndReplaced(t *testing.T) {
	s, root := newTestStore(t)
	afterStaging = func() {
		if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
			t.Error(err)
		}
		writeApplyLock(t, root, LockInfo{ID: "beefbeefbeefbeef", Kind: lockKindApply, PID: 99, Host: "other", Since: "2026-08-16T00:00:00Z"})
	}
	t.Cleanup(func() { afterStaging = nil })

	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("Materialize err = %v, want ErrLockLost", err)
	}
	if !strings.Contains(err.Error(), "beefbeefbeefbeef") {
		t.Errorf("err = %q, want it to name the lock that holds the repository now", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "managed")); !os.IsNotExist(statErr) {
		t.Error("published a tree after this run's lock was replaced")
	}
}

// The revalidation must not fire on the ordinary path, where nothing has
// touched the lock.
func TestMaterializeStillSucceedsWhenItsLockIsUntouched(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "a.txt")); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/repofs/ -run 'AbortsWhenItsLock|StillSucceedsWhenItsLock' -v`

Expected: FAIL — `undefined: ErrLockLost`, `undefined: afterStaging` is already defined by Task 2 so the failure is the sentinel and, once stubbed, a mixed tree.

- [ ] **Step 3: Add the sentinel**

In `internal/repofs/store.go`, after the `ErrLockTarget` block:

```go
// ErrLockLost reports that this run's transaction lock was removed or
// replaced while the run was still going. It is not ErrLocked: this is not a
// run that failed to start, it is a run that had already started under a
// claim someone then destroyed — most often --break-lock used on a live lock,
// which the blocked-apply error itself invites, since rdk refuses to guess
// whether a lock is stranded and the reader has less information than rdk
// does. The only thing rdk can do about that from here is refuse to be the
// second half of the corruption: abort with nothing published, loudly, rather
// than complete a tree that will be interleaved with another run's.
var ErrLockLost = errors.New("this apply's lock was broken while it was running")

// lockLostError names the lock that holds the repository now, when there is
// one: that is the id the reader has to act on, not the dead id this run was
// carrying.
type lockLostError struct{ err error }

func (e *lockLostError) Error() string        { return e.err.Error() }
func (e *lockLostError) Unwrap() error        { return e.err }
func (e *lockLostError) Is(target error) bool { return target == ErrLockLost }
```

- [ ] **Step 4: Add the revalidation helper**

In `internal/repofs/store.go`, after `ReleaseLock`:

```go
// checkStillLocked verifies this run still holds the transaction lock it
// acquired. Called immediately before each irreversible step, because between
// acquiring and here someone may have run --break-lock on a live lock —
// which the blocked-apply error tells them to do when they judge it stranded,
// a judgement rdk deliberately declines to make for them.
//
// This narrows the corruption window to the width of one rename; it does not
// close it. Closing it would need an atomic compare-and-rename, which does
// not exist, or a staleness heuristic, which the design rules out (rule 12:
// the limit ships with the flexibility). What it does buy is that the common
// case — a person breaking a lock they believed dead, seconds or minutes
// before the victim's renames — stops being silent, and a mixed tree becomes
// an error with nothing published.
func (s *osStore) checkStillLocked() error {
	s.lockMu.Lock()
	held, id := s.lockHeld, s.lockID
	s.lockMu.Unlock()
	if !held {
		// Not reachable from Materialize, which acquires before staging, but
		// returned rather than ignored: a future caller that reaches here
		// without a lock is asking the wrong question, and answering "fine"
		// would be the dangerous way to be wrong.
		return &lockLostError{err: errors.New("this apply is not holding a lock")}
	}
	switch info, err := s.readLockFile(scratchApplyLock); {
	case err == nil && info.ID == id:
		return nil
	case err == nil:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running, and %s holds the repository now: nothing was published, re-run when it is free", id, info.ID)}
	case os.IsNotExist(err):
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running: nothing was published, re-run", id)}
	default:
		return err
	}
}
```

- [ ] **Step 5: Call it before every irreversible step**

In `internal/repofs/store.go`'s `Materialize`, after the staging loop and before the `checkPathComponents` call, add the seam and the first check:

```go
	if afterStaging != nil {
		afterStaging()
	}

	// Revalidated here, at the last moment before anything irreversible: the
	// staging above is all inside .rdk and is discarded by the next run
	// regardless, so up to this point losing the lock costs nothing. From the
	// displacing rename onward every step is visible in the user's tree, so a
	// run whose claim is gone has to stop rather than interleave its output
	// with whoever holds the repository now.
	if err := s.checkStillLocked(); err != nil {
		return err
	}
```

and before the sweep, replacing the existing comment block above `RemoveAll(scratchOld)`:

```go
	// Checked again before the sweep, which is irreversible in a different
	// direction: .rdk/old is the previous tree, and if this run's lock was
	// broken after publishing, that copy may now be the only thing standing
	// between another run's failed publish and a lost tree. Past this point
	// the tree on disk is correct, so the caller must say so even while
	// reporting a failure.
	if err := s.checkStillLocked(); err != nil {
		return err
	}
	if err := s.root.RemoveAll(scratchOld); err != nil {
		return &sweepError{err: err}
	}
```

- [ ] **Step 6: Mirror in `Mem`**

`Mem` has no partial-write or interleaving model, but it must reject the same states so a component test cannot pass against the fake while production aborts. In `internal/repofs/mem.go`, add after `ReleaseLock`:

```go
// checkStillLocked mirrors osStore's: Mem cannot be raced (it is
// single-goroutine test scaffolding), but a test can still plant a broken or
// replaced lock in the map between calls, and the two Store implementations
// must agree about what that means.
func (m *Mem) checkStillLocked() error {
	if !m.lockHeld {
		return &lockLostError{err: errors.New("this apply is not holding a lock")}
	}
	switch info, err := m.readLockFile(scratchApplyLock); {
	case err == nil && info.ID == m.lockID:
		return nil
	case err == nil:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running, and %s holds the repository now: nothing was published, re-run when it is free", m.lockID, info.ID)}
	case err == fs.ErrNotExist:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running: nothing was published, re-run", m.lockID)}
	default:
		return err
	}
}
```

and call it in `Mem.Materialize` immediately before the loop that deletes the managed-dir prefix:

```go
	// Mirrors osStore.Materialize: the last point before anything the caller
	// can observe changes.
	if err := m.checkStillLocked(); err != nil {
		return err
	}
```

- [ ] **Step 7: Add the diagnostic code and map it**

In `internal/diag/codes.go`, add `CodeLockLost = "lock-lost"` next to `CodeLockTarget` and to the `All` slice.

In `internal/apply/apply.go`, before the `ErrLocked` branch:

```go
		if errors.Is(err, repofs.ErrLockLost) {
			// Not apply-locked: this run was not refused a lock, it held one
			// and had it taken away mid-flight. Saying "another apply is
			// running" would send the reader looking for a queue to wait in,
			// when what actually happened is that something destroyed this
			// run's claim — and the tree is untouched, which is the first
			// thing they need to know.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeLockLost,
				Summary: "this apply's lock was broken while it was running",
				Hint:    "nothing was written; re-run, and check who is using --break-lock on a live lock",
			})
		}
```

- [ ] **Step 8: Document the code**

In `docs/errors.md`, in the exit-1 table, after the `lock-target` row:

```markdown
| `lock-lost` | This apply held `.rdk/apply.lock` and something removed or replaced it before the apply finished — almost always `--break-lock` used on a lock that was live rather than stranded. rdk refuses to guess whether a lock is stale, so that judgement is the user's, and this is what it looks like when it goes the wrong way. | Nothing was written; the managed tree is exactly as it was. Re-run. If this recurs, the cause is a `--break-lock` habit rather than a stranded lock: wait for the running apply instead. |
```

- [ ] **Step 9: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 10: Commit**

```bash
git add internal/repofs/store.go internal/repofs/mem.go internal/repofs/store_test.go internal/apply/apply.go internal/diag/codes.go docs/errors.md
git commit -m "fix(repofs): an apply that loses its lock aborts before publishing

--break-lock removes a transaction lock without asking whether it is
live, and the blocked-apply error instructs the user to do exactly
that. Followed while the named apply is genuinely running, both runs
staged, both published, both exited 0, and the tree was a mixture no
single run generated. Compare-and-swap does not help: the breaker did
observe that lock. The design forbids staleness heuristics while
requiring the user to make precisely that judgement, with less
information than rdk has.

rdk cannot make breaking a live lock safe without the heuristic it has
ruled out, so instead the victim stops participating: Materialize
re-reads .rdk/apply.lock immediately before the displacing rename and
again before the sweep, and refuses to continue unless it still carries
the id this run took. Silent corruption becomes ErrLockLost with
nothing published and the managed tree untouched.

This narrows the window to one rename rather than closing it, and the
comment says so — the previous claim of structural closure in this area
turned out to be false, and one unearned 'structurally cannot' per
design is enough."
```

---

### Task 4: `rdk lock` runs under the transaction lock

**Why:** `HoldLock` reads `.rdk/apply.lock`, finds it absent, and creates `.rdk/lock`; `Materialize` reads `.rdk/lock`, finds it absent, and creates `.rdk/apply.lock`. Each checks the other's file before creating its own, so interleaved they both succeed. Reproduced: 3 of 3000 trials returned nil from both, published a tree, *and* left `.rdk/lock` standing — `rdk lock` reporting the repository held while an apply ran underneath it. That is goal 2 failing at exactly the moment it matters, and the person it lies to is the one who asked for exclusivity.

The fix uses the mechanism already there rather than a second one (rule 7): `HoldLock` acquires the transaction lock, creates the held lock under it, and releases. Two mutually-checking creates become one create inside a lock, and the interleaving has nowhere to happen. This depends on Task 2 having scoped ownership to the transaction lock — without that, the second acquisition overwrites `lockID` and the deferred release strands the transaction lock.

**Files:**
- Modify: `internal/repofs/store.go`
- Modify: `internal/repofs/mem.go`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// The property, stated deterministically: for the whole span in which the
// held lock is being created, an apply is genuinely excluded. Without it,
// both creates check the other's file first and interleave.
func TestHoldLockExcludesAnApplyWhileItCreatesTheHeldLock(t *testing.T) {
	s, root := newTestStore(t)
	other, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	var applyErr error
	afterApplyLockHeldByHoldLock = func() {
		set := NewFileSet()
		add(t, set, Managed("a.txt"), []byte("a"))
		applyErr = other.Materialize("managed", set)
	}
	t.Cleanup(func() { afterApplyLockHeldByHoldLock = nil })

	if _, err := s.HoldLock("work"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(applyErr, ErrLocked) {
		t.Fatalf("concurrent apply err = %v, want ErrLocked — rdk lock did not exclude it", applyErr)
	}
	if _, err := os.Lstat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("an apply published a tree while rdk lock was taking the repository")
	}
}

// The transaction lock HoldLock takes is transient: it must be gone by the
// time rdk lock returns, or every subsequent apply blocks on a lock nobody
// holds.
func TestHoldLockReleasesTheTransactionLock(t *testing.T) {
	s, root := newTestStore(t)
	if _, err := s.HoldLock("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Fatal("rdk lock left its transaction lock behind")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Fatalf("the held lock was not created: %v", err)
	}
}

// Two rdk locks must still read as "this repository is locked", not as
// "another apply is running": the transaction lock is now an implementation
// detail of rdk lock, and describing it to the user would be describing
// rdk's own plumbing back at them.
func TestHoldLockTwiceReportsTheHeldLock(t *testing.T) {
	s, _ := newTestStore(t)
	first, err := s.HoldLock("first holder")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.HoldLock("second")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second HoldLock err = %v, want ErrLocked", err)
	}
	info, ok := LockInfoFromError(err)
	if !ok {
		t.Fatal("no LockInfo on the error")
	}
	if info.Kind != lockKindHeld || info.ID != first.ID {
		t.Errorf("blocked by %s/%s, want the held lock %s", info.Kind, info.ID, first.ID)
	}
	if info.Message != "first holder" {
		t.Errorf("message = %q, want the first holder's", info.Message)
	}
}

// A genuinely running apply still reports as one.
func TestHoldLockStillReportsARunningApply(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	writeApplyLock(t, root, sampleLock(lockKindApply))
	_, err := s.HoldLock("work")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("HoldLock err = %v, want ErrLocked", err)
	}
	info, _ := LockInfoFromError(err)
	if info.Kind != lockKindApply {
		t.Errorf("Kind = %q, want %q", info.Kind, lockKindApply)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/repofs/ -run 'HoldLock' -v`

Expected: FAIL — `TestHoldLockExcludesAnApplyWhileItCreatesTheHeldLock` fails because the seam is never called and the apply succeeds; `TestHoldLockTwiceReportsTheHeldLock` passes already (keep it as a regression guard for the new failure wording).

- [ ] **Step 3: Rewrite `HoldLock`**

Replace `HoldLock` and its doc comment in `internal/repofs/store.go`:

```go
// HoldLock takes a lock that outlives this process, so a person or agent can
// work on the tree without an apply running underneath them.
//
// It runs under the transaction lock. The old shape — read .rdk/apply.lock,
// find it absent, create .rdk/lock — was one half of a symmetric race:
// Materialize reads .rdk/lock, finds it absent, and creates .rdk/apply.lock,
// so each checked the other's file before creating its own and interleaved
// runs both succeeded. Measured at 3 in 3000: rdk lock returned success, the
// apply published, and .rdk/lock stood — the repository reported as held
// while an apply was running underneath it, which is the exact lie this
// feature exists to prevent, told to the one person who asked for
// exclusivity.
//
// Acquiring the transaction lock first turns two mutually-checking creates
// into one create inside a lock, using the mechanism already here rather than
// a second one (rule 7). The transaction lock is transient — held only for
// the span of the create, released before returning — so it never leaks into
// what the user sees, except in the one case below where an apply genuinely
// beat it to the lock.
//
// The held lock is deliberately not recorded as this store's (see
// acquireLock): if it were, the second acquisition would overwrite the
// transaction lock's id and the deferred release below would fail its own id
// compare and strand it.
func (s *osStore) HoldLock(message string) (LockInfo, error) {
	if err := s.ensureScratchDir(); err != nil {
		return LockInfo{}, err
	}
	if _, err := s.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
		// A held lock takes precedence in the message when there is one: two
		// rdk locks racing means the loser briefly collides with the winner's
		// transaction lock, and reporting "another rdk apply is running"
		// would be describing rdk's own plumbing back at a user whose actual
		// situation is that someone else holds the repository. When there is
		// no held lock, the collision was a genuine apply and the original
		// error is already right.
		if errors.Is(err, ErrLocked) {
			if held, readErr := s.readLockFile(scratchLock); readErr == nil {
				return LockInfo{}, lockedErrorFor(held)
			}
		}
		return LockInfo{}, err
	}
	defer s.ReleaseLock()
	if afterApplyLockHeldByHoldLock != nil {
		afterApplyLockHeldByHoldLock()
	}
	return s.acquireLock(scratchLock, lockKindHeld, message)
}
```

- [ ] **Step 4: Mirror in `Mem`**

Replace `Mem.HoldLock` in `internal/repofs/mem.go`:

```go
// HoldLock mirrors osStore.HoldLock: it creates the held lock while holding
// the transaction lock, so rdk lock and an apply can never both succeed. Mem
// is single-goroutine and cannot race, but the two implementations must agree
// on the sequence and not merely on the end state — otherwise a component
// test could describe an ordering production does not have.
func (m *Mem) HoldLock(message string) (LockInfo, error) {
	if _, err := m.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
		if errors.Is(err, ErrLocked) {
			if held, readErr := m.readLockFile(scratchLock); readErr == nil {
				return LockInfo{}, lockedErrorFor(held)
			}
		}
		return LockInfo{}, err
	}
	defer m.ReleaseLock()
	return m.acquireLock(scratchLock, lockKindHeld, message)
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 6: Commit**

```bash
git add internal/repofs/store.go internal/repofs/mem.go internal/repofs/store_test.go
git commit -m "fix(repofs): rdk lock takes the repository under the transaction lock

HoldLock read .rdk/apply.lock and then created .rdk/lock; Materialize
read .rdk/lock and then created .rdk/apply.lock. Each checked the
other's file before creating its own, so interleaved they both
succeeded: 3 of 3000 trials returned nil from both, published a tree,
and left .rdk/lock standing — the repository reported held while an
apply ran underneath it. Goal 2 failing at the moment it matters, to
the one person who asked for exclusivity.

HoldLock now acquires the transaction lock, creates the held lock under
it, and releases — one create inside a lock instead of two
mutually-checking creates, reusing the mechanism already here rather
than adding a second (rule 7). A blocked rdk lock still reports the
held lock rather than the winner's transient transaction lock, which
would be describing rdk's plumbing back at the user."
```

---

### Task 5: A lock id that cannot be named cannot be broken by guessing

**Why:** `cmd/apply.go` treats `breakLock != ""` as "the flag was given", so `--break-lock=` runs a plain apply — while the error it is answering literally prints `use --break-lock=`. A lock with no readable id therefore has a recovery that is inexpressible: the printed command is a no-op and only `rm .rdk/apply.lock` works. `repofs.BreakLock("")` would have worked, which makes the gap purely a CLI accident.

The fix is two-sided. `Flags().Changed` makes "given but empty" distinguishable, and an empty id is then rejected as a flag error — because compare-and-swap on an empty id is not compare-and-swap at all, and `BreakLock` must never become "remove whatever is there" (goal 3). That leaves the state the empty id was standing in for, and it needs a recovery that can actually be typed: a lock file whose id is unreadable is named by *path* in the diagnostic, so the instruction is "remove `.rdk/apply.lock`" rather than a command that cannot work. Task 1's `LockInfo.Path` is what makes that possible, and Task 2 makes the state itself vanishingly rare going forward — but a lock file written by an older binary, or truncated by a power loss, still has to have a way out.

**Files:**
- Modify: `cmd/apply.go`
- Modify: `internal/repofs/store.go`
- Modify: `internal/repofs/mem.go`
- Modify: `internal/apply/apply.go`
- Test: `cmd/cli_test.go`, `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// An empty id is not a compare-and-swap. Accepting it would make BreakLock
// "remove whatever is there", which is the one thing goal 3 forbids — and it
// would match a truncated lock file's empty id by accident.
func TestBreakLockRejectsAnEmptyID(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, scratchApplyLock), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BreakLock(""); err == nil {
		t.Fatal("BreakLock(\"\") succeeded; an empty id must never match")
	}
	if _, err := os.Stat(filepath.Join(root, scratchApplyLock)); err != nil {
		t.Error("BreakLock(\"\") removed a lock it could not name")
	}
}

func TestUnlockAndUseLockRejectAnEmptyID(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, scratchLock), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(""); err == nil {
		t.Error("Unlock(\"\") succeeded")
	}
	if _, err := s.UseLock(""); err == nil {
		t.Error("UseLock(\"\") succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, scratchLock)); err != nil {
		t.Error("a lock with no readable id was removed by an unnamed call")
	}
}
```

`apply`'s test needs to construct an `ErrLocked` from a `LockInfo`, which it cannot today: `lockedError` is unexported and `apply` is a different package. Export a thin wrapper in `internal/repofs/store.go`, next to `lockedErrorFor`:

```go
// LockedErrorFor builds the same blocked-by-a-lock error every path inside
// this package produces, from a LockInfo the caller already has. It exists
// for tests in other packages — apply's diagnostic rendering, principally,
// which has to be able to render a lock whose id is unreadable without
// arranging one on a real filesystem. lockedError stays unexported so that
// the only production source of ErrLocked remains inside this package.
func LockedErrorFor(info LockInfo) error { return lockedErrorFor(info) }
```

Then add to `internal/apply/apply_test.go`:

```go
// A lock whose id cannot be read has no --break-lock recovery, so the
// diagnostic has to name the path instead of printing a command that cannot
// be typed. Reachable from a lock file truncated by a power loss, or written
// by a binary older than repofs' complete-or-absent lock creation.
func TestLockedDiagnosticNamesThePathWhenTheIDIsUnreadable(t *testing.T) {
	err := repofs.LockedErrorFor(repofs.LockInfo{Held: true, Kind: "apply", Path: ".rdk/apply.lock"})
	d, ok := LockedDiagnostic(err)
	if !ok {
		t.Fatal("LockedDiagnostic returned false")
	}
	if !strings.Contains(d.Hint, ".rdk/apply.lock") {
		t.Errorf("hint = %q, want it to name the path to remove", d.Hint)
	}
	if strings.Contains(d.Hint, "--break-lock=") {
		t.Errorf("hint = %q, still offers a --break-lock that cannot name anything", d.Hint)
	}
}

// The ordinary case must keep printing the id, since that is the only
// argument --break-lock accepts.
func TestLockedDiagnosticStillPrintsTheIDWhenItIsReadable(t *testing.T) {
	err := repofs.LockedErrorFor(repofs.LockInfo{Held: true, ID: "9f3a1c4e", Kind: "apply", Path: ".rdk/apply.lock"})
	d, _ := LockedDiagnostic(err)
	if !strings.Contains(d.Hint, "--break-lock=9f3a1c4e") {
		t.Errorf("hint = %q, want it to hand over the id", d.Hint)
	}
}
```

Add to `cmd/cli_test.go`:

```go
// The blocked-apply error prints "--break-lock=<id>"; --break-lock= with no
// id used to run a plain apply instead, so a lock with no readable id had a
// recovery that could not be typed.
func TestApplyRejectsAnEmptyBreakLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, dir, "apply", "--break-lock=")
	if err == nil {
		t.Fatalf("apply --break-lock= succeeded, want a flag error; output:\n%s", out)
	}
	if !strings.Contains(out, "--break-lock") {
		t.Errorf("output = %q, want it to name the flag", out)
	}
}

func TestApplyRejectsAnEmptyWithLock(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, dir, "apply", "--with-lock="); err == nil {
		t.Fatalf("apply --with-lock= succeeded, want a flag error; output:\n%s", out)
	}
}

// Giving both flags is an error however they are spelled, including when one
// of them is empty — otherwise "empty means absent" comes back through the
// mutual-exclusion check.
func TestApplyRejectsBothLockFlagsEvenWhenOneIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, dir, "apply", "--break-lock=", "--with-lock=abc")
	if err == nil {
		t.Fatalf("both flags accepted; output:\n%s", out)
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("output = %q, want the mutual-exclusion error", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/ ./internal/repofs/ ./internal/apply/ -run 'EmptyBreakLock|EmptyWithLock|BothLockFlags|EmptyID|NamesThePath' -v`

Expected: FAIL — `apply --break-lock=` currently exits 0, `BreakLock("")` currently matches a truncated file, `LockedErrorFor` undefined.

- [ ] **Step 3: Reject empty ids in `repofs`**

In `internal/repofs/store.go`, add at the top of `BreakLock`:

```go
	// An empty id is not a compare-and-swap, it is "remove whatever is
	// there" — the one thing this function exists not to be. It would also
	// match a truncated lock file, whose id unmarshals to "", turning the
	// safety property inside out precisely in the case where the caller can
	// see least.
	if id == "" {
		return LockInfo{}, errors.New("a lock id is required: --break-lock names the one lock it may remove")
	}
```

The same guard at the top of `Unlock`:

```go
	if id == "" {
		return errors.New("a lock id is required: rdk unlock names the one lock it may release")
	}
```

and `UseLock`:

```go
	if id == "" {
		return LockInfo{}, errors.New("a lock id is required: --with-lock names the one lock it may run under")
	}
```

Add the identical three guards to `internal/repofs/mem.go`'s `BreakLock`, `Unlock` and `UseLock`, so the fake cannot accept what production refuses.

Add the exported `LockedErrorFor` helper from Step 1 next to `lockedErrorFor`.

- [ ] **Step 4: Make "given" mean given in `cmd/apply.go`**

Replace the mutual-exclusion block and the two `!= ""` guards in `cmd/apply.go`'s `RunE`:

```go
			// Changed, not != "": --break-lock= is a flag that was given with
			// no id, and treating that as "absent" ran a plain apply — while
			// the error it was answering printed "use --break-lock=<id>". A
			// lock whose id could not be read therefore had a recovery that
			// could not be typed. Empty is now its own, named mistake.
			gaveBreak := cmd.Flags().Changed("break-lock")
			gaveWith := cmd.Flags().Changed("with-lock")
			if gaveBreak && gaveWith {
				return diag.New(diag.Diagnostic{
					Code:    diag.CodeInvalidFlag,
					Summary: "rdk apply: --with-lock and --break-lock are mutually exclusive",
					Hint:    "pick one: --with-lock to run under a lock you hold, or --break-lock to remove one that's stranded",
				})
			}
			if gaveBreak && breakLock == "" {
				return diag.New(diag.Diagnostic{
					Code:    diag.CodeInvalidFlag,
					Summary: "rdk apply: --break-lock needs the id of the lock to remove",
					Hint:    "the blocked-apply error prints the id; if it prints none, the lock file is unreadable — remove " + repofs.ScratchDir + "/apply.lock",
				})
			}
			if gaveWith && withLock == "" {
				return diag.New(diag.Diagnostic{
					Code:    diag.CodeInvalidFlag,
					Summary: "rdk apply: --with-lock needs the id of the lock to run under",
					Hint:    `rdk lock -m "..." prints the id it takes`,
				})
			}
```

Then replace `if withLock != "" {` with `if gaveWith {` and `if breakLock != "" {` with `if gaveBreak {`.

- [ ] **Step 5: Give an unreadable lock a recovery that can be typed**

In `internal/apply/apply.go`'s `LockedDiagnostic`, replace the summary/hint construction:

```go
	// The holder's details go in the summary, not the hint, so the hint stays
	// short and single-purpose regardless of how much there is to say about
	// the holder. The Message tail is only present for a kind "held" lock, so
	// it's appended rather than baked into a fixed format — leaving a
	// dangling colon for kind "apply" (no message) would be its own small
	// lie.
	//
	// info.Kind is now the file the lock was read from rather than the field
	// inside it (repofs.readLockFile), so this can no longer describe a held
	// lock as an apply because someone hand-edited a JSON field.
	var summary, hint string
	switch {
	case info.ID == "":
		// No id means no --break-lock: naming the lock is the whole
		// mechanism, and printing "use --break-lock=" would be printing a
		// command that cannot work — which is exactly what it used to do. The
		// path is the only handle left, so the recovery names that instead.
		// Reachable from a lock file truncated by a power loss or written by
		// a binary older than the complete-or-absent creation in repofs.
		summary = fmt.Sprintf("this repository is locked by %s, and the lock file carries no readable id", info.Path)
		hint = fmt.Sprintf("nothing can name that lock, so --break-lock cannot remove it: if no rdk is running, delete %s", info.Path)
	case info.Kind == "held":
		summary = fmt.Sprintf("this repository is locked (lock %s, pid %d on %s since %s)",
			info.ID, info.PID, info.Host, info.Since)
		hint = fmt.Sprintf("wait for it to be unlocked; if it is stranded use --break-lock=%s — do not use --with-lock unless this lock is yours",
			info.ID)
	default:
		summary = fmt.Sprintf("another rdk apply is running (lock %s, pid %d on %s since %s)",
			info.ID, info.PID, info.Host, info.Since)
		hint = fmt.Sprintf("wait for it; if it is stranded use --break-lock=%s", info.ID)
	}
```

Add `diag.Str("lock_path", info.Path)` to the `Attrs` slice at the end of `LockedDiagnostic`, so a JSONL consumer gets the same handle.

- [ ] **Step 6: Update the docs**

In `docs/errors.md`, extend the `apply-locked` row's Fix column with:

```
If the message says the lock file carries no readable id, no `--break-lock` can name it: check nothing is running and delete the path the message gives.
```

and extend the `lock-mismatch` row's Meaning column with:

```
An empty id (`--break-lock=`, `--with-lock=`) is rejected as `invalid-flag` rather than treated as absent: it can name no lock, and a break that names nothing would remove whatever it found.
```

- [ ] **Step 7: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 8: Commit**

```bash
git add cmd/apply.go internal/repofs/store.go internal/repofs/mem.go internal/repofs/store_test.go internal/apply/apply.go internal/apply/apply_test.go cmd/cli_test.go docs/errors.md
git commit -m "fix(cmd): an empty lock id is a mistake, not an absent flag

--break-lock= ran a plain apply, because the flag was tested with != \"\"
rather than Changed. The error it answers prints 'use --break-lock=<id>',
so a lock whose id could not be read had a recovery that was literally
inexpressible: the printed command was a no-op and only rm worked.
repofs.BreakLock(\"\") would have removed it, which made the gap a pure
CLI accident.

Both flags now test Changed, and an empty value is its own named flag
error — accepting it would make BreakLock 'remove whatever is there',
which is the one thing compare-and-swap exists not to be, and it would
match a truncated lock file's empty id by accident. BreakLock, Unlock
and UseLock refuse an empty id directly for the same reason.

That leaves the state the empty id was standing in for, so it gets a
recovery that can be typed: a lock with no readable id is named by path
in the diagnostic — delete .rdk/apply.lock — instead of by a command
that cannot work."
```

---

### Task 6: A release that fails is not a success

**Beyond the five, and cheap.** `ReleaseLock` treats a *read* failure as success: it returns nil, clears ownership, and leaves the file. Its `Remove`-error branch keeps ownership "so a retry can see it", but there is no retry — both callers discard the error, so an apply exits 0 with its lock still on disk and the next apply blocks on a lock nobody holds. That is goal 5 failing on a path with no signal involved at all, and it is a few lines to fix, so it goes in with the rest rather than into a list.

**Files:**
- Modify: `internal/repofs/store.go`
- Modify: `internal/apply/apply.go`
- Modify: `internal/diag/codes.go`
- Modify: `docs/errors.md`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/repofs/store_test.go`:

```go
// A lock that could not be released must not be reported as a clean apply:
// the tree is correct, but the next apply will block on a lock nobody holds,
// far from the cause (rule 6).
func TestMaterializeReportsALockItCouldNotRelease(t *testing.T) {
	s, root := newTestStore(t)
	// afterPublish, not afterStaging: everything between staging and
	// publishing needs .rdk writable — the publish rename moves .rdk/new out
	// of it — so making the directory unwritable any earlier would fail the
	// publish instead of the release. There is no prior tree, so .rdk/old
	// does not exist and the sweep's RemoveAll succeeds without needing write
	// permission; the release's Remove is the only thing left to fail.
	afterPublish = func() { chmodUnwritable(t, filepath.Join(root, ScratchDir)) }
	t.Cleanup(func() { afterPublish = nil })

	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLockNotReleased) {
		t.Fatalf("Materialize err = %v, want ErrLockNotReleased", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "managed", "a.txt")); statErr != nil {
		t.Errorf("the tree should still be published: %v", statErr)
	}
}

// A read failure is not a release. Clearing ownership on one leaves the file
// standing with nothing tracking it.
func TestReleaseLockDoesNotTreatAReadFailureAsSuccess(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, lockKindApply, ""); err != nil {
		t.Fatal(err)
	}
	// A directory where the lock file was: readLockFile refuses it (Task 1),
	// which is a read failure that is not "absent".
	if err := os.Remove(filepath.Join(root, scratchApplyLock)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, scratchApplyLock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseLock(); err == nil {
		t.Fatal("ReleaseLock reported success over a lock path it could not read")
	}
	st.lockMu.Lock()
	held := st.lockHeld
	st.lockMu.Unlock()
	if !held {
		t.Error("ownership was surrendered without the lock being removed")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/repofs/ -run 'CouldNotRelease|ReadFailureAsSuccess' -v`

Expected: FAIL — `undefined: ErrLockNotReleased`, and `ReleaseLock` returns nil on a read failure.

- [ ] **Step 3: Add the sentinel**

In `internal/repofs/store.go`, after `ErrLockLost`:

```go
// ErrLockNotReleased reports that the apply finished but its transaction lock
// is still on disk. The tree is correct; the repository is not usable until
// the lock goes, and saying nothing would move the failure to the next
// apply — the same reasoning as ErrSweep, and the same obligation on the
// caller to lead with the fact that the apply itself succeeded.
var ErrLockNotReleased = errors.New("lock not released")

// lockNotReleasedError marks a failed release without contributing to the
// message, for the same reason as sweepError: the caller's summary already
// says what is still there.
type lockNotReleasedError struct{ err error }

func (e *lockNotReleasedError) Error() string        { return e.err.Error() }
func (e *lockNotReleasedError) Unwrap() error        { return e.err }
func (e *lockNotReleasedError) Is(target error) bool { return target == ErrLockNotReleased }
```

- [ ] **Step 4: Make `ReleaseLock` honest**

In `internal/repofs/store.go`, replace `ReleaseLock`'s body from the `readLockFile` call:

```go
	info, err := s.readLockFile(scratchApplyLock)
	switch {
	case err != nil && os.IsNotExist(err):
		// Already gone — nothing of this call's is left to remove, so there
		// is nothing left to track either.
		s.lockHeld = false
		return nil
	case err != nil:
		// A read that failed for any other reason is not evidence the lock is
		// gone. Clearing ownership here — which this used to do — left the
		// file standing with nothing tracking it, so no later call would
		// remove it either.
		return &lockNotReleasedError{err: err}
	case info.ID != s.lockID:
		// Replaced by a live lock this call must not touch.
		s.lockHeld = false
		return nil
	}
	if err := s.root.Remove(scratchApplyLock); err != nil && !os.IsNotExist(err) {
		// Ownership is kept: the file is still there, and surrendering the
		// only record of who is responsible for it would make it unreleasable
		// by anything short of --break-lock.
		return &lockNotReleasedError{err: err}
	}
	s.lockHeld = false
	return nil
```

Replace the paragraph in its doc comment beginning "So this re-reads the file and removes it only if it still carries the id this call took; if it doesn't (or the read fails, or it's gone already), releasing nothing is the safe outcome" with:

```go
// So this re-reads the file and removes it only if it still carries the id
// this call took. Gone already, or replaced by a live lock, both mean there
// is nothing of this call's left to remove — releasing nothing is right, and
// ownership is surrendered. A read that fails for any other reason is
// different and used to be conflated with those two: it is not evidence the
// lock is gone, so ownership is kept and the failure is returned, because
// clearing the only record of who is responsible for a file that is still
// there makes it unreleasable by anything short of --break-lock.
```

- [ ] **Step 5: Surface it from `Materialize`**

In `internal/repofs/store.go`, change `Materialize`'s signature to a named return and replace the bare defer:

```go
func (s *osStore) Materialize(managedDir string, set *FileSet) (err error) {
```

and replace `defer s.ReleaseLock()` with:

```go
	// The release's error is not discarded. It used to be, on both callers,
	// which meant the branch above that deliberately keeps ownership "so a
	// retry can see it" was writing to nobody: an apply exited 0 with its
	// lock still on disk, and the next apply blocked on a lock no process
	// held. A failure that already has an error to report keeps it — that
	// error is the cause, and this is its consequence.
	defer func() {
		if relErr := s.ReleaseLock(); relErr != nil && err == nil {
			err = relErr
		}
	}()
```

Fire the seam immediately after the publishing rename succeeds, before the sweep's `checkStillLocked`:

```go
	if afterPublish != nil {
		afterPublish()
	}
```

Mirror the same named-return and deferred-release change in `internal/repofs/mem.go`'s `Materialize` (`func (m *Mem) Materialize(managedDir string, set *FileSet) (err error)` and the equivalent deferred closure), so the two agree. `Mem` gets no seam: it has no filesystem to make unwritable, so there is nothing there that can fail a release.

- [ ] **Step 6: Add the diagnostic code and map it**

In `internal/diag/codes.go`, add `CodeLockNotReleased = "lock-not-released"` next to `CodeLockLost` and to the `All` slice.

In `internal/apply/apply.go`, next to the `ErrSweep` branch (they are the same shape — the apply succeeded, something rdk owns is still on disk):

```go
		if errors.Is(err, repofs.ErrLockNotReleased) {
			// The tree is already correct here, so the summary leads with
			// that — the same obligation ErrSweep carries. Staying quiet
			// would move the failure to the next apply, which would block on
			// a lock no process holds, far from the cause.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code: diag.CodeLockNotReleased,
				Summary: fmt.Sprintf("rdk apply: wrote %d files to %s/, but could not release its lock",
					set.Len(), ManagedDir),
				Hint: "the generated tree is correct; nothing is running — remove " + repofs.ScratchDir + "/apply.lock, then re-run",
			})
		}
```

- [ ] **Step 7: Document the code**

In `docs/errors.md`, after the `scratch-not-removed` row:

```markdown
| `lock-not-released` | The apply finished and the tree is correct, but `.rdk/apply.lock` could not be removed — a permission change under `.rdk/`, or the directory becoming unreadable mid-run. Left unsaid, the next apply would block on a lock no process holds. | The generated tree is correct and nothing is running. Remove `.rdk/apply.lock` (and fix whatever made `.rdk/` unwritable), then re-run. |
```

- [ ] **Step 8: Run the tests**

Run: `go test ./... && gofmt -l . && go vet ./...`

Expected: PASS, no gofmt output.

- [ ] **Step 9: Commit**

```bash
git add internal/repofs/store.go internal/repofs/mem.go internal/repofs/store_test.go internal/apply/apply.go internal/diag/codes.go docs/errors.md
git commit -m "fix(repofs): a release that fails is not a success

ReleaseLock returned nil on a read failure, clearing ownership and
leaving the file — so nothing would ever remove it. Its Remove-error
branch kept ownership 'so a retry can see it', but no retry existed:
both callers discarded the error, so an apply exited 0 with its lock
still on disk and the next apply blocked on a lock no process held.
Goal 5 failing with no signal involved at all.

A read failure that is not not-exist now keeps ownership and returns
ErrLockNotReleased; Materialize takes a named return so its deferred
release can surface that when the apply itself succeeded, leading with
the fact that the tree is correct — the same shape as ErrSweep."
```

---

### Task 7: Correct the design document

**Why:** Two claims in the spec are false, and one of them was the entire argument for the sufficiency of the previous P1 fix. Leaving them would mean the next person to reason about this starts from the same wrong premise. This task changes no code.

**Files:**
- Modify: `docs/superpowers/specs/2026-08-02-apply-lock-design.md`

- [ ] **Step 1: Correct the false "structurally cannot" claim**

In "Where the lock sits in the sequence", the paragraph about step 5 says `HoldLock` "structurally cannot create a new `.rdk/lock`" once the transaction lock is held. Before Task 4 that was false — the check and the create were two operations. After Task 4 it is true, but for a different reason than the sentence gives, and the sentence should say which. Replace the sentence beginning "Step 5 closes it: once `.rdk/apply.lock` is held" through "only narrower." with:

```markdown
Step 5 narrows it, and since `HoldLock` began running under the transaction
lock it also closes it: `HoldLock` acquires `.rdk/apply.lock` before creating
`.rdk/lock`, so while this run holds the transaction lock no new held lock can
appear. That property is worth stating carefully, because the first version of
this paragraph asserted it while it was false — `HoldLock` then checked
`.rdk/apply.lock` and created `.rdk/lock` as two separate operations, so the
re-read at step 5 was a narrowing and nothing more, and the argument for its
sufficiency rested on a guarantee that did not exist. It exists now because
`HoldLock` was changed to make it exist, not because the file layout implied
it.
```

- [ ] **Step 2: Correct the `.gitignore` claim**

The same section says: "Writing `.gitignore` is deliberately *inside* it, though — it's remove-then-create, so two concurrent runs would otherwise race and one would fail `O_EXCL` spuriously." Both halves are stale: the write is in `ensureScratchDir`, at step 2, outside the lock, and it is temp-and-rename rather than remove-then-create. Replace that sentence with:

```markdown
Writing `.gitignore` is part of step 2 and also sits outside the lock. It was
briefly moved inside, on the reasoning that its remove-then-create shape would
otherwise let two concurrent runs collide on `O_EXCL`; that shape is gone — it
writes a temp name and renames it into place, so concurrent runs cannot
collide and the loser cannot observe a half-written file. The write has to
happen wherever the scratch directory is created, which includes `rdk lock` in
a repository that has never been applied, so it belongs to `ensureScratchDir`
rather than to the locked span.
```

- [ ] **Step 3: Update the sequence listing**

Replace the numbered sequence block with:

```
1. reject outside entries
2. check and create .rdk/, write .rdk/.gitignore   ← must precede the lock; the dir must exist
3. check .rdk/lock is absent (skipped if this run adopted it via UseLock)
4. ACQUIRE .rdk/apply.lock                         ← defer release
5. on the --with-lock path only: re-check .rdk/lock still carries the adopted id
6. Stat the managed dir (drives two later decisions)
7. clear new (and old, when the managed dir exists)
8. stage into .rdk/new
9. re-check this run still holds .rdk/apply.lock   ← last point before anything visible
10. check the managed dir's parent components
11. displace by rename, publish by rename
12. re-check this run still holds .rdk/apply.lock
13. sweep .rdk/old
14. RELEASE .rdk/apply.lock, and report a release that fails
```

Add after it:

```markdown
Steps 9 and 12 exist because `--break-lock` does not ask whether the lock it
removes is live, and the blocked-apply error tells the reader to use it when
they judge the lock stranded — a judgement rdk declines to make for them, and
which they make with less information than rdk has. Nothing here makes
breaking a live lock safe. What these two re-reads do is stop the victim from
completing a tree that will be interleaved with the breaker's: everything
before step 9 lives in `.rdk` and is discarded by the next run regardless, so
a run that has lost its claim aborts with nothing published (`ErrLockLost`).
Step 12 guards the sweep for the mirror reason: `.rdk/old` may be the only
remaining copy of a tree if another run has since failed to publish.
```

- [ ] **Step 4: Restate the limits honestly**

In "Stated limits (rule 12)", replace the second bullet's final sentences about `HoldLock` (from "`HoldLock`'s check that `.rdk/apply.lock` is absent" to the end of the bullet) with:

```markdown
  `HoldLock` no longer has a window of this shape: it acquires
  `.rdk/apply.lock` and creates `.rdk/lock` under it, so the two creates that
  used to check each other's file cannot interleave. It used to, and did — 3
  of 3000 trials had `rdk lock` and an apply both return success, the tree
  published and `.rdk/lock` standing.
```

and add a new bullet:

```markdown
- **Breaking a live transaction lock still corrupts, in a window one rename
  wide.** `--break-lock` cannot tell live from stranded, because rdk refuses
  to guess and the design has no third source of truth. The re-reads at steps
  9 and 12 shrink the exposure from "the whole apply" to "between the re-read
  and the rename that follows it", and turn every wider case into a loud
  abort. Closing it entirely would need an atomic compare-and-rename, which
  does not exist. Accepted, and the reason `--break-lock`'s own output is
  louder than `--with-lock`'s: it is the more dangerous of the two.
- **A lock file with no readable id cannot be broken by `--break-lock`**, and
  is not meant to be: naming the lock is the mechanism. Since lock files are
  written complete-or-not-at-all, this can only come from a pre-existing file
  or a filesystem that lost the contents, and the diagnostic names the path to
  delete instead of a command that could not work.
```

- [ ] **Step 5: Extend the sequencing section**

At the end of the "Sequencing" section's numbered list, add:

```markdown
5. **Audit hardening** — five defects found by a full review of the mechanism
   against its own goals: lock paths that a symlink could neutralise
   (`ErrLockTarget`), a lock file observable while empty (write-then-link),
   an apply that could publish after its lock was broken (`ErrLockLost`),
   `rdk lock` succeeding underneath a running apply (`HoldLock` under the
   transaction lock), and `--break-lock=` with no id running a plain apply.
   Goals 2 and 5 did not hold as written before this; goals 1, 3, 4 and 7 did.
```

Also update the status line at the top of the file from "Landed in four steps" to "Landed in five steps".

- [ ] **Step 6: Verify the docs still pass their checks**

Run: `go test ./... && npx cspell lint docs/superpowers/specs/2026-08-02-apply-lock-design.md docs/errors.md`

Expected: PASS. If cspell flags a word that is genuinely new and correct, add it to `cspell.json`.

- [ ] **Step 7: Commit**

```bash
git add docs/superpowers/specs/2026-08-02-apply-lock-design.md
git commit -m "docs(lock): correct two false claims and record the audit's findings

The spec asserted that HoldLock 'structurally cannot' create a held
lock while the transaction lock exists. It could: the check and the
create were two operations. That sentence was the whole argument for
the sufficiency of the revalidation added alongside it, so it is now
stated as what it was — a narrowing — and as what it has since become,
with the reason being a change to HoldLock rather than anything the
file layout implied.

The .gitignore paragraph was stale twice over: the write lives in
ensureScratchDir, outside the lock, and is temp-and-rename rather than
remove-then-create.

The sequence, the stated limits and the sequencing list now match the
code, including the two limits that remain: breaking a live transaction
lock still corrupts in a window one rename wide, and a lock file with no
readable id is recovered by path rather than by --break-lock."
```

---

## Final verification

- [ ] **Step 1: Full suite, formatting, vet, and the golden tests**

```bash
go test ./... && gofmt -l . && go vet ./... && go build ./...
```

- [ ] **Step 2: Re-run the audit's own reproductions**

The two that were measurable before the fixes. Write these as throwaway scripts under the scratchpad, not in the repo, and delete them afterwards.

1. **`rdk lock` under a running apply.** 3000 trials, each starting `rdk apply` and `rdk lock -m x` concurrently in a fresh temp repo. Assert: never both exit 0. Before Task 4 this failed roughly 1 in 1000.
2. **Ctrl-C stranding.** 3000 `rdk apply` runs each sent SIGTERM at a random delay in the band where the lock is being taken. Assert: no run leaves `.rdk/apply.lock` behind, and no lock file is ever zero-length. Before Task 2 this stranded roughly 2.5% of the runs that reached the handler.

- [ ] **Step 3: Confirm the tree is clean**

```bash
git status --porcelain --untracked-files=all
```

Expected: only `TODO.md`, which was already untracked before this work.

- [ ] **Step 4: Push**

```bash
git push
```

Then report on the PR which findings are closed and which two limits are now stated rather than fixed.
