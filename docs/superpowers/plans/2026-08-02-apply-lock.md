# Apply Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two `rdk apply` processes in one checkout can no longer produce a managed tree that neither of them generated.

**Architecture:** A `.rdk/lock` file created with `O_CREAT|O_EXCL` — atomic on NFS, unlike `flock` — held for the whole of `Materialize` and released by defer. `cmd` installs a `SIGINT`/`SIGTERM` handler that releases it, so Ctrl-C does not strand one. `rdk apply --break-lock` removes a stranded lock and says whose it was.

**Tech Stack:** Go 1.26, `os/signal`. No new dependencies.

**Design spec:** `docs/superpowers/specs/2026-08-02-apply-lock-design.md`

---

## File Structure

**Modified:**
- `internal/repofs/store.go` — `LockInfo`, `ErrLocked`, acquire/release/break, wired into `Materialize`
- `internal/repofs/mem.go` — the same behaviour in the fake
- `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`
- `internal/diag/codes.go`, `docs/errors.md` — two codes
- `internal/apply/apply.go` — map `ErrLocked` to a diagnostic
- `cmd/apply.go` — the `--break-lock` flag
- `cmd/root.go` — signal handling
- `cmd/cli_test.go`

---

## Task 1: The lock itself

**Files:**
- Modify: `internal/repofs/store.go`, `internal/repofs/mem.go`
- Test: `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// Two applies in one checkout used to interleave on the fixed scratch names
// and publish a mixture of both runs' files.
func TestMaterializeRefusesWhileLocked(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"),
		[]byte("host=builder-3\npid=4127\nsince=2026-08-02T10:04:11Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
	// The holder's details have to reach the message, or the reader cannot
	// tell a live apply from a stranded lock.
	for _, want := range []string{"4127", "builder-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the lock")
	}
}

func TestMaterializeReleasesTheLockOnSuccess(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock survived a successful apply")
	}
	// And a second run must work, which is the point.
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
}

// A stranded lock is the cost of not using flock; it must not also be the cost
// of an ordinary failure.
func TestMaterializeReleasesTheLockOnFailure(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	chmodUnwritable(t, root) // blocks the displacing rename

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	if err := s.Materialize("managed", second); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock survived a failed apply")
	}
}

func TestBreakLockReportsWhatItRemoved(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"),
		[]byte("host=builder-3\npid=4127\nsince=2026-08-02T10:04:11Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := s.BreakLock()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Held || info.PID != 4127 || info.Host != "builder-3" {
		t.Errorf("info = %+v, want the holder's details", info)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock not removed")
	}
}

// Nothing held: not an error, and the caller must be able to tell, so it does
// not report breaking a lock that never existed.
func TestBreakLockWithNoLockIsNotAnError(t *testing.T) {
	s, _ := newTestStore(t)
	info, err := s.BreakLock()
	if err != nil {
		t.Fatal(err)
	}
	if info.Held {
		t.Errorf("info.Held = true with no lock present")
	}
}
```

Add the `ErrLocked` and `BreakLock` equivalents to `mem_test.go` — the fake must behave identically or every test using it misrepresents production.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repofs/`
Expected: FAIL — `undefined: ErrLocked`.

- [ ] **Step 3: Implement**

In `internal/repofs/store.go`:

```go
// LockInfo is who holds (or held) the apply lock. It is written for a human to
// read in an error message and is never acted on: rdk must not decide a lock
// is stale because a pid looks dead, since on shared storage the pid is not
// even meaningful. Deciding that is the user's job (--break-lock).
type LockInfo struct {
	Held  bool
	Host  string
	PID   int
	Since string // RFC3339, as written
}

// ErrLocked reports that another apply holds the repository's lock.
var ErrLocked = errors.New("another rdk apply is running in this repository")
```

Acquire with `s.root.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)` — exclusive creation is atomic on NFSv3 and later, which is why this is not `flock`; put that reason in a comment, since the next reader will wonder. On `os.IsExist`, read the existing file best-effort (an unreadable or malformed lock must still produce `ErrLocked`, just with less detail) and return a wrapped `ErrLocked` carrying the `LockInfo`.

Write `host`, `pid` and an RFC3339 `since` into the file. Keep the format trivially parseable — `key=value` lines — and tolerate anything you cannot parse rather than failing.

`ReleaseLock` removes the file and must be idempotent, because both the deferred release and the signal handler will call it. Guard the held-state with a mutex.

`BreakLock` reads the info (if any), removes the file, and returns what it found.

Wire into `Materialize` at the position the spec gives: **after** the `.rdk` check and `MkdirAll` (the directory must exist first), **before** the `.gitignore` write (which is remove-then-create and would otherwise race two runs into a spurious `O_EXCL` failure). `defer s.ReleaseLock()`.

Add all three to the `Store` interface and implement them on `Mem`.

- [ ] **Step 4: Run**

Run: `go test ./internal/repofs/ -count=1`
Expected: PASS.

- [ ] **Step 5: Prove the test bites**

Temporarily remove the `defer s.ReleaseLock()` and confirm `TestMaterializeReleasesTheLockOnSuccess` fails, then restore. Paste both outputs — a lock test that passes without the release is worthless.

- [ ] **Step 6: Commit**

```bash
git add internal/repofs
git commit -m "feat(repofs): one apply at a time, per repository"
```

---

## Task 2: The diagnostic and the flag

**Files:**
- Modify: `internal/diag/codes.go`, `docs/errors.md`, `internal/apply/apply.go`, `cmd/apply.go`
- Test: `cmd/cli_test.go`

- [ ] **Step 1: Codes and docs**

`internal/diag/codes.go` (const block and `all`):

```go
	CodeApplyLocked       = "apply-locked"
	CodeLockBroken        = "lock-broken"
```

`docs/errors.md` — `apply-locked` in **Failures**, `lock-broken` in **Warnings**:

```markdown
| `apply-locked` | Another `rdk apply` holds this repository's lock. | Wait for it to finish. If nothing is running — a previous apply was killed — re-run with `--break-lock`. |
```

```markdown
| `lock-broken` | `--break-lock` removed a lock another run had left behind. | Intentional. If an apply really was running, both runs are now unsafe — check `rdk-managed/` and re-run. |
```

- [ ] **Step 2: Map it in `apply.Run`**

Add a branch before the generic one, in the same shape as the existing `ErrSweep`/`ErrPublish`/`ErrUnsafePath` branches:

```go
		if errors.Is(err, repofs.ErrLocked) {
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeApplyLocked,
				Summary: "another rdk apply is running in this repository",
				Hint:    "if no apply is running, re-run with --break-lock",
			})
		}
```

The holder's details ride in on the wrapped error's own message, which `diag.Error()` appends after the summary — check that renders correctly rather than assuming.

- [ ] **Step 3: The flag**

In `cmd/apply.go`, add a `--break-lock` bool to the apply command only, and before `apply.Run`:

```go
			if breakLock {
				info, err := store.BreakLock()
				if err != nil {
					return err
				}
				if info.Held {
					// Taking someone else's lock is surprising state, and
					// surprising state announces itself (rule 6).
					a.log.Warn(diag.Diagnostic{
						Code:    diag.CodeLockBroken,
						Summary: fmt.Sprintf("broke a lock held since %s by pid %d on host %s", info.Since, info.PID, info.Host),
					})
				}
			}
```

Wire the flag through the `app` struct the way the log flags already are, or as a local on the command — pick whichever matches the existing style and say which.

- [ ] **Step 4: Tests**

```go
// The loser of a race has to be told what to do, not just that it failed.
func TestApplyReportsAnExistingLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "lock"),
		[]byte("host=builder-3\npid=4127\nsince=2026-08-02T10:04:11Z\n"), 0o644)

	_, errOut, err := runSplit(t, dir, "apply")
	if err == nil {
		t.Fatal("apply succeeded despite a lock")
	}
	if !strings.Contains(errOut, "--break-lock") {
		t.Errorf("stderr does not name the recovery: %q", errOut)
	}

	out, errOut2, err := runSplit(t, dir, "apply", "--break-lock")
	if err != nil {
		t.Fatalf("apply --break-lock: %v (%s)", err, errOut2)
	}
	if !strings.Contains(errOut2, "broke a lock") || !strings.Contains(errOut2, "4127") {
		t.Errorf("breaking a lock was not announced: %q", errOut2)
	}
	if !strings.Contains(out, "rdk apply: wrote") {
		t.Errorf("apply did not proceed: %q", out)
	}
}
```

- [ ] **Step 5: Run everything, then commit**

```bash
go test ./... -count=1 && go vet ./... && gofmt -l .
git add internal/diag/codes.go docs/errors.md internal/apply cmd
git commit -m "feat(cmd): report a held lock and allow breaking it"
```

---

## Task 3: Ctrl-C must not strand a lock

**Files:**
- Modify: `cmd/root.go`, `docs/errors.md`

- [ ] **Step 1: Implement**

`SIGINT` and `SIGTERM` do not run defers, and Ctrl-C during a slow apply is the ordinary way a developer changes their mind — without this, `--break-lock` becomes routine rather than rare.

In `cmd`, install a handler for the duration of the run that releases the lock and exits 128 + the signal number (130 for `SIGINT`, 143 for `SIGTERM`, the shell convention — a script can then tell an interruption from a definition error or an rdk bug).

The store is created inside each subcommand's `RunE`, so the handler needs a way to reach it. Work out the cleanest wiring — a store registered on the `app` when it is created, or `signal.NotifyContext` plumbed down — and **explain what you chose and why** in your report. Keep the handler confined to releasing the lock and exiting: it must not grow into general cleanup.

This is safe only because of the atomic-materialize work: every intermediate state either self-heals on the next run or fails loudly, so the handler unwinds nothing.

- [ ] **Step 2: Document the exit codes**

Extend the exit-status paragraph in `docs/errors.md` to cover 130 and 143.

- [ ] **Step 3: Verify by hand**

Automated signal testing needs a subprocess and is more machinery than it is worth here. Verify manually and paste the transcript: build the binary, start an apply in a repo large enough to interrupt (or add a temporary sleep — remove it after), press Ctrl-C, and confirm the exit status is 130 **and** `.rdk/lock` is gone. Then confirm a normal apply still works immediately afterwards, with no `--break-lock` needed.

If you cannot make the window wide enough to interrupt reliably, say so rather than claiming a result you did not observe.

- [ ] **Step 4: Commit**

```bash
git add cmd docs/errors.md
git commit -m "fix(cmd): release the lock when interrupted"
```

---

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1 && gofmt -l .
```

Then the race the whole thing exists to prevent. Two applies at once, in one checkout:

```bash
go build -o /tmp/rdk . && cd "$(mktemp -d)" && /tmp/rdk init >/dev/null && \
  printf 'kind: config\nname: demo\n' > rdk/config.yaml && \
  ( /tmp/rdk apply & /tmp/rdk apply & wait )
```

Expected: one succeeds, and either the other reports `another rdk apply is
running` or it also succeeds (the runs did not overlap). What must **never**
happen is both succeeding with a `rdk-managed/` that is missing files. Run it
several times and say what you saw.

## Follow-up, not in this plan

A waiting mode — the loser blocks rather than failing — is deliberately out of
scope. Failing fast is louder, and an apply that appears to hang is its own
support burden.
