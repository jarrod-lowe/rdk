# Held Locks Implementation Plan (step 2 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A developer or agent can hold a repository against applies while they work, say why, and run their own applies under that lock.

**Architecture:** The `.rdk/lock` file from step 1 already carries `kind` and `message`. `rdk lock -m "…"` takes a `kind: held` lock and exits leaving it; `rdk unlock <id>` releases it; `rdk apply --with-lock=<id>` runs under it without acquiring or releasing, announcing whose lock it is.

**Design spec:** `docs/superpowers/specs/2026-08-02-apply-lock-design.md`

**Step 1 (done):** commits `96f1113`, `1565d4e`, `f0b4b76`, `9bf6068` — the lock file, `Materialize` acquiring and releasing it, `--break-lock=<id>`, and signal handling.

---

## The decision this plan makes that the spec left implicit

The spec says the signal handler "must release only an `apply` lock this process took", and step 1 satisfied that by tracking what the store acquired. That is no longer sufficient once `rdk lock` exists, because `rdk lock` *does* acquire a lock in-process and then exits deliberately leaving it. If it registers its store the way `apply` does, a `SIGTERM` arriving in that window releases the lock the user just asked for.

Relying on "`rdk lock` must remember not to call `setStore`" is exactly the convention-fragility flagged at the end of step 1. So make it structural: **the store records the kind it acquired, and `ReleaseLock` releases only `kind: apply`.** A held lock is then unreleasable by any automatic path — defer, signal, or otherwise — and can only go through `rdk unlock` or `--break-lock`, both of which name it explicitly.

---

## Task 1: `repofs` learns held locks

**Files:**
- Modify: `internal/repofs/store.go`, `internal/repofs/mem.go`
- Test: `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// rdk lock takes a lock and exits leaving it. Nothing automatic may remove it:
// not a defer, not a signal. Only rdk unlock or --break-lock, both of which
// name it.
func TestReleaseLockLeavesAHeldLockAlone(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("agent refactoring the s3-bucket module")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "held" || info.Message == "" {
		t.Errorf("info = %+v, want a held lock carrying its message", info)
	}
	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("ReleaseLock removed a held lock: %v", err)
	}
}

func TestHoldLockRefusesWhenAlreadyLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())
	if _, err := s.HoldLock("second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

// Unlock is the routine end of your own lock, so it names the lock and refuses
// anything else.
func TestUnlockOnlyReleasesTheNamedHeldLock(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock("some-other-id"); err == nil {
		t.Error("unlocked a lock whose id did not match")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("removed it anyway: %v", err)
	}
	if err := s.Unlock(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock not removed")
	}
}

// An apply's lock is not yours to end routinely — that is what --break-lock is
// for, and it warns.
func TestUnlockRefusesAnApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock()) // kind: apply
	err := s.Unlock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlocked an apply lock")
	}
	if !strings.Contains(err.Error(), "--break-lock") {
		t.Errorf("error %q does not point at the right door", err.Error())
	}
}

// Running under someone's lock must neither take nor release it.
func TestUseLockRunsWithoutAcquiringOrReleasing(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UseLock(info.ID); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("materialize under a held lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("the held lock did not survive the apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "f.txt")); err != nil {
		t.Errorf("apply did not publish: %v", err)
	}
}

func TestUseLockRejectsAMismatchedOrAbsentLock(t *testing.T) {
	s, root := newTestStore(t)
	// Nothing held: you asserted you hold a lock and you do not, which means
	// it was broken out from under you.
	if err := s.UseLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("adopted a lock that does not exist")
	}
	writeLock(t, root, heldLock())
	if err := s.UseLock("some-other-id"); err == nil {
		t.Error("adopted someone else's lock under the wrong id")
	}
}
```

Mirror the coverage in `mem_test.go`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repofs/`
Expected: FAIL — `undefined: HoldLock`.

- [ ] **Step 3: Implement**

Three methods on `Store`, implemented on `osStore` and `Mem`:

```go
// HoldLock takes a lock that outlives this process, so a person or agent can
// work on the tree without an apply running underneath them. Nothing automatic
// releases it — see ReleaseLock.
HoldLock(message string) (LockInfo, error)

// Unlock ends a held lock. It names the lock because between reading an id and
// typing it the lock may have been replaced, and it refuses an apply lock:
// ending someone's running apply is breaking, not unlocking.
Unlock(id string) error

// UseLock runs under an existing held lock without taking or releasing it.
UseLock(id string) error
```

The kind-aware release is the load-bearing change: record the kind alongside `lockHeld`/`lockID`, and have `ReleaseLock` return without removing anything unless the kind it took was `apply`. Comment *why* — a held lock exists precisely to outlive its process, so an automatic release would silently defeat the feature.

`UseLock` records that this store is running under a foreign lock. `Materialize` then skips both acquire and release. Make sure a store that adopted a lock cannot also acquire one.

- [ ] **Step 4: Run, then prove the tests bite**

Run: `go test ./internal/repofs/ -count=1` → PASS.

Then two deliberate regressions, restoring after each, pasting all four outputs:
1. Make `ReleaseLock` ignore the kind; confirm `TestReleaseLockLeavesAHeldLockAlone` fails.
2. Make `UseLock` skip its id comparison; confirm `TestUseLockRejectsAMismatchedOrAbsentLock` fails.

- [ ] **Step 5: Commit**

```bash
git add internal/repofs
git commit -m "feat(repofs): locks that outlive the process that took them"
```

---

## Task 2: `rdk lock` and `rdk unlock`

**Files:**
- Create: `cmd/lock.go`
- Modify: `cmd/root.go` (register the commands), `internal/diag/codes.go`, `docs/errors.md`
- Test: `cmd/cli_test.go`

- [ ] **Step 1: Codes and docs**

```go
	CodeLockHeld          = "lock-held"
	CodeUnlocked          = "unlocked"
```

Rows in the **Results** table of `docs/errors.md` — both are successful outcomes, not failures.

- [ ] **Step 2: The commands**

```
rdk lock -m "<message>"     takes a held lock, prints the id, exits 0
rdk unlock <id>             releases a held lock
```

`-m`/`--message` is required on `lock`: a lock nobody can explain is worse than no lock, because the person it blocks has nothing to act on. Reject an empty message with a diagnostic rather than accepting it.

`lock` reports through the logger like everything else — the id has to be on **stdout** as part of the result, since the caller (very often a script or an agent) needs to capture it:

```
rdk lock: held 9f3a1c4e7b2d8a05 — agent refactoring the s3-bucket module
```

In JSONL that result carries `lock_id` as an attr, which is how an agent should read it rather than parsing the sentence.

`unlock` takes the id as a positional argument, so `Args: cobra.ExactArgs(1)`.

Neither command should register its store for signal-based release. With the kind-aware `ReleaseLock` from task 1 that is now belt-and-braces rather than the only protection, which is the point — but do not add the registration.

- [ ] **Step 3: Tests**

```go
func TestLockThenUnlock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)

	out, _, err := runSplit(t, dir, "lock", "-m", "agent working")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !strings.Contains(out, "agent working") {
		t.Errorf("lock did not report its message: %q", out)
	}
	id := extractLockID(t, dir) // read .rdk/lock and pull out the id

	// An apply is now blocked, and told why.
	_, errOut, err := runSplit(t, dir, "apply")
	if err == nil {
		t.Fatal("apply ran under someone else's lock")
	}
	if !strings.Contains(errOut, "agent working") {
		t.Errorf("the blocked apply does not say why: %q", errOut)
	}

	if _, _, err := runSplit(t, dir, "unlock", id); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, _, err := runSplit(t, dir, "apply"); err != nil {
		t.Fatalf("apply after unlock: %v", err)
	}
}

// A lock nobody can explain is worse than no lock.
func TestLockRequiresAMessage(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, _, err := runSplit(t, dir, "lock"); err == nil {
		t.Error("lock succeeded with no message")
	}
}
```

- [ ] **Step 4: Commit**

```bash
go test ./... -count=1 && go vet ./... && gofmt -l .
git add cmd internal/diag/codes.go docs/errors.md
git commit -m "feat(cmd): rdk lock and rdk unlock"
```

---

## Task 3: `rdk apply --with-lock`

**Files:**
- Modify: `cmd/apply.go`, `internal/diag/codes.go`, `docs/errors.md`
- Test: `cmd/cli_test.go`

- [ ] **Step 1: Implement**

`--with-lock=<id>` calls `store.UseLock(id)` before `apply.Run`. Its errors — no lock, or a mismatched id — surface as diagnostics, not bare errors; "your lock was broken out from under you" is exactly what the user needs to hear.

`--with-lock` and `--break-lock` together are an error: they are contradictory.

**It announces what it is running under.** This is the point of the whole flag from a safety perspective, not decoration. rdk cannot tell a legitimate holder from someone who copied the id out of a blocked apply's error message — the token is the same either way — so the guarantee is visibility:

```
warning: running under lock 9f3a1c4e7b2d8a05, held by pid 4127 on builder-3: agent refactoring the s3-bucket module
rdk apply: wrote 6 files to rdk-managed/
```

Add `CodeRunningUnderLock = "running-under-lock"` and a **Warnings** row.

- [ ] **Step 2: Tests**

Cover: applying under a held lock succeeds and the lock survives; a mismatched id fails; no lock at all fails; the announcement names the holder and the message; `--with-lock` with `--break-lock` is rejected.

- [ ] **Step 3: Commit**

```bash
go test ./... -count=1 && go vet ./... && gofmt -l .
git add cmd internal/diag/codes.go docs/errors.md
git commit -m "feat(cmd): run an apply under a held lock"
```

---

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1 && gofmt -l .
```

Then the workflow the feature exists for, by hand:

```bash
go build -o /tmp/rdk . && cd "$(mktemp -d)" && /tmp/rdk init >/dev/null && \
  printf 'kind: config\nname: demo\n' > rdk/config.yaml && \
  /tmp/rdk lock -m "agent working" && \
  ID=$(python3 -c "import json;print(json.load(open('.rdk/lock'))['id'])") && \
  /tmp/rdk apply; echo "blocked exit=$?"; \
  /tmp/rdk apply --with-lock=$ID; echo "under-lock exit=$?"; \
  /tmp/rdk unlock $ID && /tmp/rdk apply; echo "after unlock exit=$?"
```

Expected: the bare apply is blocked and says why; the `--with-lock` apply warns what it is running under, succeeds, and **leaves the lock in place**; after `unlock` a bare apply works. Paste the transcript.

Also confirm the held lock survives a `SIGINT` during an apply run under it — that is the case the kind-aware release exists for, and nothing else covers it.
