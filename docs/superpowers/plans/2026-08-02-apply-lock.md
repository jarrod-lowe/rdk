# Apply Lock Implementation Plan (step 1 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two `rdk apply` processes in one checkout can no longer produce a managed tree that neither of them generated.

**Architecture:** A `.rdk/lock` JSON file created with `O_CREAT|O_EXCL` — atomic on NFS, unlike `flock` — held for the whole of `Materialize` and released by defer. `cmd` installs a `SIGINT`/`SIGTERM` handler that releases it, so Ctrl-C does not strand one. `rdk apply --break-lock=<id>` removes a stranded lock, naming whose it was.

**Scope:** This is step 1. Held locks (`rdk lock -m`, `rdk unlock <id>`, `--with-lock=<id>`) are step 2 and get their own plan — but the file format here is designed for both, so step 2 is a command and a warning rather than a format migration. Write the `kind` and `message` fields now even though only `kind: "apply"` is produced.

**Tech Stack:** Go 1.26, `encoding/json`, `crypto/rand`, `os/signal`. No new dependencies.

**Design spec:** `docs/superpowers/specs/2026-08-02-apply-lock-design.md`

---

## File Structure

**Modified:**
- `internal/repofs/store.go` — `LockInfo`, `ErrLocked`, acquire/release/break, wired into `Materialize`
- `internal/repofs/mem.go` — the same behaviour in the fake
- `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`
- `internal/diag/codes.go`, `docs/errors.md` — two codes, and the exit-status paragraph
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
// writeLock plants a lock file the way another process would have.
func writeLock(t *testing.T, root string, info LockInfo) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func heldLock() LockInfo {
	return LockInfo{
		ID: "9f3a1c4e7b2d8a05", Kind: "apply", Host: "builder-3",
		PID: 4127, Since: "2026-08-02T10:04:11Z",
	}
}

// Two applies in one checkout used to interleave on the fixed scratch names
// and publish a mixture of both runs' files.
func TestMaterializeRefusesWhileLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
	// The holder's details have to reach the message, or the reader cannot
	// tell a live apply from a stranded lock — and the id is what any
	// recovery has to name.
	for _, want := range []string{"9f3a1c4e7b2d8a05", "4127", "builder-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the lock")
	}
}

// A malformed lock must still block. Failing open here would mean a corrupt
// file silently disables the exclusion.
func TestMaterializeRefusesWhileLockedEvenIfUnreadable(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
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

// The id is the whole safety property: between reading an error and typing the
// recovery, the stranded lock may have been replaced by a live one.
func TestBreakLockOnlyRemovesTheNamedLock(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())

	if _, err := s.BreakLock("some-other-id"); err == nil {
		t.Error("broke a lock whose id did not match")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("removed the lock anyway: %v", err)
	}

	info, err := s.BreakLock("9f3a1c4e7b2d8a05")
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

func TestBreakLockWithNoLockPresentIsAnError(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.BreakLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("want an error: the named lock does not exist")
	}
}
```

Mirror `ErrLocked` and `BreakLock` coverage in `mem_test.go` — the fake must behave identically or every test using it misrepresents production.

Note `TestBreakLockWithNoLockPresentIsAnError`: with a required id, "nothing there" means the lock you named is gone, which the caller should hear rather than have silently treated as success.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repofs/`
Expected: FAIL — `undefined: ErrLocked`.

- [ ] **Step 3: Implement**

In `internal/repofs/store.go`:

```go
// LockInfo is who holds (or held) the repository lock. It is written for a
// human to read in an error message and is never acted on: rdk must not decide
// a lock is stale because a pid looks dead, since on shared storage the pid is
// not even meaningful. That decision is the user's, and the id is how they say
// which lock they decided about.
type LockInfo struct {
	Held    bool   `json:"-"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`    // "apply" now; "held" arrives with rdk lock
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`   // RFC3339
	Message string `json:"message,omitempty"` // only for kind "held"
}

// ErrLocked reports that something else holds the repository's lock.
var ErrLocked = errors.New("another rdk apply holds this repository")
```

Encode with plain `encoding/json`. **Do not route this through `FileSet.JSON`** — that function is the single owner of rdk's *generated output* format, where determinism is load-bearing. The lock is coordination state: gitignored, never read by generation. Put that distinction in a comment; it is exactly the kind of thing a later reader will "tidy up".

The same reasoning covers rule 1's ban on clocks and randomness: it governs generation, and this file never touches it. Say so, or someone will file it as a violation.

The id: 8 bytes from `crypto/rand`, hex-encoded.

Acquire with `s.root.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)` — exclusive creation, atomic on NFSv3 and later, which is *why* this is not `flock`. Comment the reason; the next reader will wonder.

On `os.IsExist`, read the existing file best-effort and return an error wrapping `ErrLocked` that carries whatever detail was readable. **A malformed or unreadable lock must still block** — failing open would let a corrupt file silently disable the exclusion.

`ReleaseLock` removes the file and must be idempotent, because the deferred call and the signal handler both use it. Guard the held-state with a mutex. It must release only a lock **this store acquired** — step 2 adds held locks that outlive the process, and a release that removes whatever is present would destroy them.

`BreakLock(id string)` reads the lock, errors if absent or if the id does not match, otherwise removes it and returns what it found.

Wire into `Materialize` at the position the spec gives: **after** the `.rdk` check and `MkdirAll` (the directory must exist first), **before** the `.gitignore` write (remove-then-create, which would otherwise race two runs into a spurious `O_EXCL` failure). `defer s.ReleaseLock()`.

Add the methods to the `Store` interface and implement them on `Mem`.

- [ ] **Step 4: Run**

Run: `go test ./internal/repofs/ -count=1`
Expected: PASS.

- [ ] **Step 5: Prove the tests bite**

Temporarily remove `defer s.ReleaseLock()`; confirm `TestMaterializeReleasesTheLockOnSuccess` fails. Restore. Then temporarily make `BreakLock` ignore its id argument; confirm `TestBreakLockOnlyRemovesTheNamedLock` fails. Restore. Paste all four outputs — a lock test that passes without the release, or an id check that passes without checking, is worthless.

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
| `apply-locked` | Something else holds this repository's lock. | Wait for it. If it is stranded — the process is gone — re-run with `--break-lock=<id>`, naming the id from the message. |
```

```markdown
| `lock-broken` | `--break-lock` removed a lock another run had left behind. | Intentional. If an apply really was running, both runs are now unsafe — check `rdk-managed/` and re-run. |
```

- [ ] **Step 2: Map it in `apply.Run`**

A branch before the generic one, in the same shape as the existing
`ErrSweep`/`ErrPublish`/`ErrUnsafePath` branches. The holder's details go in the
**summary**, not the hint, because the text handler indents only a hint's first
line — so a multi-line hint renders badly. The hint stays one line and must warn
off the wrong door:

```
error: another rdk apply holds this repository (lock 9f3a1c4e7b2d8a05, pid 4127 on builder-3 since 2026-08-02T10:04:11Z): agent refactoring the s3-bucket module
  wait for it; if it is stranded use --break-lock=9f3a1c4e7b2d8a05 — never --with-lock, which is only for the process that took the lock
```

`--with-lock` does not exist until step 2, and the hint naming it anyway is
deliberate: the blocked-apply error is where an agent will look for a way past,
and the error hands over the id that both flags need. Warning off the wrong door
before that door exists is cheaper than retrofitting the warning later.

Add the lock's fields as typed attrs (`diag.Str("lock_id", …)`, `diag.Int("pid", …)`
and so on) so JSONL consumers match on fields rather than parsing the sentence.
Check the reserved-key guard in `logger.emit` does not drop the names you pick.

- [ ] **Step 3: The flag**

In `cmd/apply.go`, add `--break-lock` as a **string** (the id), on the apply
command only. Empty means not given. Before `apply.Run`:

```go
			if breakLock != "" {
				info, err := store.BreakLock(breakLock)
				if err != nil {
					return err
				}
				// Taking someone else's lock is surprising state, and
				// surprising state announces itself (rule 6).
				a.log.Warn(diag.Diagnostic{
					Code:    diag.CodeLockBroken,
					Summary: fmt.Sprintf("broke lock %s held since %s by pid %d on host %s", info.ID, info.Since, info.PID, info.Host),
				})
			}
```

Wire the flag through the `app` struct the way the log flags already are, or as
a local on the command — pick whichever matches the existing style and say which.

- [ ] **Step 4: Tests**

```go
// The loser of a race has to be told what to do — and warned off the door that
// is not theirs, since the error itself hands over the id both flags need.
func TestApplyReportsAnExistingLock(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runSplit(t, dir, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "rdk", "config.yaml"),
		[]byte("kind: config\nname: demo\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".rdk"), 0o755)
	os.WriteFile(filepath.Join(dir, ".rdk", "lock"),
		[]byte(`{"id":"9f3a1c4e7b2d8a05","kind":"apply","host":"builder-3","pid":4127,"since":"2026-08-02T10:04:11Z"}`), 0o644)

	_, errOut, err := runSplit(t, dir, "apply")
	if err == nil {
		t.Fatal("apply succeeded despite a lock")
	}
	for _, want := range []string{"9f3a1c4e7b2d8a05", "--break-lock", "never --with-lock"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not mention %q: %q", want, errOut)
		}
	}

	// A wrong id must not get past it.
	if _, _, err := runSplit(t, dir, "apply", "--break-lock=wrong-id"); err == nil {
		t.Error("apply proceeded with a mismatched --break-lock id")
	}

	out, errOut2, err := runSplit(t, dir, "apply", "--break-lock=9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatalf("apply --break-lock: %v (%s)", err, errOut2)
	}
	if !strings.Contains(errOut2, "broke lock") || !strings.Contains(errOut2, "4127") {
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
git commit -m "feat(cmd): report a held lock and allow breaking a named one"
```

---

## Task 3: Ctrl-C must not strand a lock

**Files:**
- Modify: `cmd/root.go`, `docs/errors.md`

- [ ] **Step 1: Implement**

`SIGINT` and `SIGTERM` do not run defers, and Ctrl-C during a slow apply is the
ordinary way a developer changes their mind — without this, `--break-lock`
becomes routine rather than rare.

Install a handler for the duration of the run that releases the lock and exits
128 + the signal number (130 for `SIGINT`, 143 for `SIGTERM`, the shell
convention — a script can then tell an interruption from a definition error or
an rdk bug).

The store is created inside each subcommand's `RunE`, so the handler needs a
route to it. Work out the cleanest wiring — a store registered on the `app` when
created, or `signal.NotifyContext` plumbed down — and **explain what you chose
and why** in your report. Keep the handler confined to releasing the lock and
exiting; it must not grow into general cleanup.

It must release only a lock this process acquired, not whatever is present.
Step 2 adds locks that deliberately outlive the process that took them, and a
handler that removed those would make the feature silently useless.

This is safe only because of the atomic-materialize work: every intermediate
state either self-heals on the next run or fails loudly, so the handler unwinds
nothing.

- [ ] **Step 2: Document the exit codes**

Extend the exit-status paragraph in `docs/errors.md` to cover 130 and 143.

- [ ] **Step 3: Verify by hand**

Automated signal testing needs a subprocess harness — more machinery than this
earns. Verify manually and paste the transcript: build the binary, start an
apply in a repo large enough to interrupt (or add a temporary sleep, removed
afterwards), press Ctrl-C, and confirm the exit status is 130 **and**
`.rdk/lock` is gone. Then confirm a normal apply works immediately afterwards
with no `--break-lock` needed.

If you cannot make the window wide enough to interrupt reliably, say so rather
than claiming a result you did not observe.

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

Then the race the whole thing exists to prevent — two applies at once, in one
checkout:

```bash
go build -o /tmp/rdk . && cd "$(mktemp -d)" && /tmp/rdk init >/dev/null && \
  printf 'kind: config\nname: demo\n' > rdk/config.yaml && \
  ( /tmp/rdk apply & /tmp/rdk apply & wait )
```

Expected: one succeeds, and the other either reports the lock or also succeeds
(the runs did not overlap). What must **never** happen is both succeeding with a
`rdk-managed/` missing files. Run it several times and say what you saw.

## Step 2, not in this plan

`rdk lock -m "…"`, `rdk unlock <id>`, and `rdk apply --with-lock=<id>` with its
"running under lock …" announcement. The file format here already carries `kind`
and `message` for them.
