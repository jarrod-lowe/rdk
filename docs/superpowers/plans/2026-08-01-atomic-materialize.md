# Atomic Materialize Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every step of `repofs.Materialize` all-or-nothing, so no failure can leave `rdk-managed/` neither the old tree nor the new one.

**Architecture:** The scratch moves into a single rdk-owned `.rdk/` directory carrying its own `.gitignore` of `*`, so git never sees it and the user's root `.gitignore` is never touched. The destructive step becomes a rename rather than a `RemoveAll`. The recovery branch is deleted — purity of generation makes it redundant. The one failure that happens after the tree is correct is reported as a diagnostic whose message leads with the success.

**Tech Stack:** Go 1.26, `os.Root`. No new dependencies.

**Design spec:** `docs/superpowers/specs/2026-08-01-atomic-materialize-design.md`

---

## File Structure

**Modified:**
- `internal/repofs/store.go` — the scratch constants, `ErrSweep`, and the rewritten `Materialize`
- `internal/repofs/store_test.go` — the recovery test deleted; failure-state tests added
- `internal/diag/codes.go` — two new codes
- `docs/errors.md` — their rows
- `internal/apply/apply.go` — wraps materialize failures into diagnostics
- `internal/apply/apply_test.go` — covers the sweep-failure wording

No new files.

---

## Task 1: The atomic swap

**Files:**
- Modify: `internal/repofs/store.go`
- Test: `internal/repofs/store_test.go`

- [ ] **Step 1: Delete the test that asserts removed behaviour**

`TestMaterializeRecoversInterruptedSwap` asserts the recovery branch this design removes deliberately. Delete it whole. Do not adapt it — the replacement in step 2 covers the same crash from the other side.

- [ ] **Step 2: Write the failing tests**

Add to `internal/repofs/store_test.go`:

```go
// Scratch lives inside an rdk-owned directory that ignores itself, so git
// never sees it and the user's root .gitignore is never touched (DD-14).
func TestMaterializeWritesTheScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("f.txt", []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ScratchDir, ".gitignore"))
	if err != nil {
		t.Fatalf("scratch .gitignore missing: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf("scratch .gitignore = %q, want %q", got, "*\n")
	}
}

// It is rdk's file, so a deleted one comes back (rule 13: always write).
func TestMaterializeRestoresADeletedScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("f.txt", []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ScratchDir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, ".gitignore")); err != nil {
		t.Errorf("not restored: %v", err)
	}
}

// The state a crash between displacing and publishing leaves behind. There is
// no recovery step any more: the next run simply regenerates, which is
// byte-identical because generation is a pure function (rule 1).
func TestMaterializePublishesAfterAnInterruptedSwap(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("f.txt", []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	// Leave the crash state directly: managed absent, old holding the tree.
	if err := os.Rename(filepath.Join(root, "managed"), filepath.Join(root, ScratchDir, "old")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("materialize after interrupted swap: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil {
		t.Fatalf("managed not published: %v", err)
	}
	if string(got) != "x" {
		t.Errorf("managed/f.txt = %q, want %q", got, "x")
	}
}

// Nothing may survive in the published tree that the FileSet did not describe,
// and the displaced copy must not linger.
func TestMaterializeLeavesNoScratchBehind(t *testing.T) {
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
	for _, rel := range []string{
		filepath.Join(ScratchDir, "old"),
		filepath.Join(ScratchDir, "new"),
		filepath.Join("managed", "stale.txt"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s still present", rel)
		}
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/repofs/`
Expected: FAIL — `undefined: ScratchDir`.

- [ ] **Step 4: Rewrite `Materialize`**

In `internal/repofs/store.go`, add the constants near the top of the file:

```go
// ScratchDir is rdk's own working directory inside the repo. It carries a
// .gitignore of "*", which hides its entire contents from git — and since git
// tracks files rather than directories, the directory itself disappears from
// `git status` too. That matters because a per-directory .gitignore governs
// only its own directory: nothing rdk owns could ignore a sibling of the
// managed dir, and the root .gitignore is seeded-once user property that apply
// may never rewrite (DD-14, rule 3).
const ScratchDir = ".rdk"

const (
	scratchNew = ScratchDir + "/new"
	scratchOld = ScratchDir + "/old"
)
```

Replace the whole `Materialize` method with:

```go
func (s *osStore) Materialize(managedDir string, set *FileSet) error {
	if err := s.root.MkdirAll(ScratchDir, 0o755); err != nil {
		return err
	}
	if err := s.root.WriteFile(ScratchDir+"/.gitignore", []byte("*\n"), 0o644); err != nil {
		return err
	}

	// The scratch is never trusted across runs, so both names are cleared
	// unconditionally. This is what covers a hard kill, where the sweep at the
	// end never ran at all — and unlike that sweep, it has to succeed, because
	// the names are needed.
	if err := s.root.RemoveAll(scratchNew); err != nil {
		return err
	}
	if err := s.root.RemoveAll(scratchOld); err != nil {
		return err
	}

	for _, p := range set.sortedPaths() {
		full := path.Join(scratchNew, p)
		if err := s.root.MkdirAll(path.Dir(full), 0o755); err != nil {
			return err
		}
		if err := s.root.WriteFile(full, set.content[p], 0o644); err != nil {
			return err
		}
	}

	// Displace by rename, not RemoveAll: a rename is all-or-nothing, so there
	// is no half-deleted tree to be mistaken for a healthy one on the next run.
	// It also works on Windows, where renaming onto an existing directory does
	// not.
	if _, err := s.root.Stat(managedDir); err == nil {
		if err := s.root.Rename(managedDir, scratchOld); err != nil {
			return err
		}
	}
	if err := s.root.Rename(scratchNew, managedDir); err != nil {
		return err
	}

	// Past this point the tree on disk is correct, so the caller must say so
	// even while reporting this failure.
	if err := s.root.RemoveAll(scratchOld); err != nil {
		// Both wrapped: the caller matches on ErrSweep, and errors.Is still
		// reaches the underlying filesystem error.
		return fmt.Errorf("%w: %w", ErrSweep, err)
	}
	return nil
}
```

Add `"errors"` and `"fmt"` to the imports, and declare the sentinel next to the constants:

```go
// ErrSweep reports that the tree was published but the displaced copy could
// not be removed. It is separated from every other Materialize failure because
// the caller has to lead with the fact that the apply succeeded — anything
// else sends the reader looking for damage that is not there (rule 11).
var ErrSweep = errors.New("displaced copy not removed")
```

Update the `Materialize` doc comment on the `Store` interface, which currently describes staging in a sibling and recovering a half-done swap. It should describe the rename-based swap and say that the scratch is rdk-owned and git-ignored.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/repofs/ -count=1`
Expected: PASS

- [ ] **Step 6: Run everything**

Run: `go test ./... -count=1`
Expected: PASS. `internal/apply` and the golden tests exercise `Materialize` heavily; if a golden test fails, stop and report rather than updating goldens — the published tree must be byte-identical to before.

- [ ] **Step 7: Commit**

```bash
git add internal/repofs/store.go internal/repofs/store_test.go
git commit -m "fix(repofs): displace the managed tree by rename, never by delete"
```

---

## Task 2: Prove the failure states

**Files:**
- Test: `internal/repofs/store_test.go`

The point of Task 1 is what happens when steps fail, and nothing yet exercises that. These tests force real filesystem failures.

- [ ] **Step 1: Write the tests**

```go
// chmodUnwritable makes dir's contents undeletable, and restores it so the
// test's own cleanup can succeed. Skips as root, which ignores the bits.
func chmodUnwritable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

// The failure that motivated this design: it must leave the published tree
// untouched rather than half-deleted, and it must say so.
func TestMaterializeReportsASweepFailureAndKeepsTheTree(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	first.Bytes("sub/f.txt", []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// After the displacing rename this becomes .rdk/old/sub, whose contents
	// cannot be unlinked — so the sweep fails while everything before it works.
	chmodUnwritable(t, filepath.Join(root, "managed", "sub"))

	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
	err := s.Materialize("managed", second)
	if !errors.Is(err, ErrSweep) {
		t.Fatalf("err = %v, want it to wrap ErrSweep", err)
	}
	// The tree is correct: that is the whole point of reporting this
	// separately from every other failure.
	got, readErr := os.ReadFile(filepath.Join(root, "managed", "fresh.txt"))
	if readErr != nil {
		t.Fatalf("published tree is not correct: %v", readErr)
	}
	if string(got) != "new" {
		t.Errorf("managed/fresh.txt = %q, want %q", got, "new")
	}
}

// A blocked scratch must stop the run before anything is displaced — the tree
// on disk has to survive intact.
func TestMaterializeLeavesTheTreeIntactWhenScratchCannotBeCleared(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	first.Bytes("f.txt", []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// Leave an old scratch behind that cannot be removed.
	oldDir := filepath.Join(root, ScratchDir, "old", "sub")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "stuck.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	chmodUnwritable(t, oldDir)

	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
	if err := s.Materialize("managed", second); err == nil {
		t.Fatal("want an error when the scratch cannot be cleared")
	} else if errors.Is(err, ErrSweep) {
		t.Errorf("reported as a sweep failure, but nothing was published: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil {
		t.Fatalf("previous tree did not survive: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("managed/f.txt = %q, want the previous %q", got, "old")
	}
}
```

Add `"errors"` to the test imports.

- [ ] **Step 2: Run them**

Run: `go test ./internal/repofs/ -count=1 -v -run 'TestMaterialize'`
Expected: PASS. If either fails, read carefully before changing the test — these are the design's central claims, so a failure here means Task 1 is wrong, not that the assertion is.

- [ ] **Step 3: Commit**

```bash
git add internal/repofs/store_test.go
git commit -m "test(repofs): pin what each materialize failure leaves on disk"
```

---

## Task 3: Report the failures as diagnostics

**Files:**
- Modify: `internal/diag/codes.go`, `docs/errors.md`
- Modify: `internal/apply/apply.go`
- Test: `internal/apply/apply_test.go`

`apply` currently returns `Materialize`'s error bare, so any filesystem failure reaches the top unwrapped and exits 2 as `code: internal` — "this is an rdk bug, report it". These are environmental failures, like `read-defs-dir` and `git-init`, and belong as diagnostics with exit 1.

- [ ] **Step 1: Add the codes**

In `internal/diag/codes.go`, keeping the existing alignment, and adding both to `all`:

```go
	CodeWriteManagedDir   = "write-managed-dir"
	CodeScratchNotRemoved = "scratch-not-removed"
```

- [ ] **Step 2: Document them**

Add to the **Failures** table in `docs/errors.md`:

```markdown
| `write-managed-dir` | The generated tree could not be written or published. | Read the cause; check permissions and free space, then re-run. |
| `scratch-not-removed` | The tree was written correctly, but rdk could not remove its displaced copy under `.rdk/`. | The generated tree is correct. Clear `.rdk/old` — something is holding a file open — then re-run. |
```

The `internal/diag` suite enforces that every code is documented and every documented code exists, so this is not optional.

- [ ] **Step 3: Write the failing test**

Add to `internal/apply/apply_test.go`:

```go
// This failure happens after the tree is already correct, so the message has
// to say so — otherwise the reader goes looking for damage that is not there,
// or starts deleting rdk-managed/ to fix a problem that does not exist.
func TestSweepFailureSaysTheApplySucceeded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	store, root := setupRepo(t)
	if _, err := Run(store, "v"); err != nil {
		t.Fatal(err)
	}
	// After the displacing rename this becomes .rdk/old/terraform, whose
	// contents cannot be unlinked, so only the final sweep fails.
	stuck := filepath.Join(root, ManagedDir, "terraform")
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, repofs.ScratchDir, "old", "terraform"), 0o700) })

	_, err := Run(store, "v")
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeScratchNotRemoved {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeScratchNotRemoved)
	}
	if !strings.Contains(d.Summary, "wrote") {
		t.Errorf("summary %q does not lead with the apply having succeeded", d.Summary)
	}
	if !strings.Contains(d.Hint, "correct") {
		t.Errorf("hint %q does not say the tree is correct", d.Hint)
	}
	// Exit 1: a locked file is environmental, not an rdk bug.
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
}
```

Add `"errors"`, `"strings"`, `"github.com/jarrod-lowe/rdk/internal/diag"` and `"github.com/jarrod-lowe/rdk/internal/repofs"` to the test imports as needed. The `t.Cleanup` path differs from the chmod path because the directory is renamed in between; if that proves awkward, chmod both candidates in cleanup and ignore the errors.

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/apply/ -run TestSweepFailure -v`
Expected: FAIL — the error is not a `*diag.Error`.

- [ ] **Step 5: Wrap the failures in `apply.Run`**

Replace the bare materialize call in `internal/apply/apply.go`:

```go
	if err := store.Materialize(ManagedDir, set); err != nil {
		if errors.Is(err, repofs.ErrSweep) {
			// The tree is already correct here, so the summary leads with that.
			// Swallowing this would only move the problem: the same locked file
			// blocks the next apply's mandatory scratch clear, far from its cause.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code: diag.CodeScratchNotRemoved,
				Summary: fmt.Sprintf("rdk apply: wrote %d files to %s/, but could not remove the displaced copy",
					set.Len(), ManagedDir),
				Hint: "the generated tree is correct; clear " + repofs.ScratchDir + "/old, then re-run",
			})
		}
		return Result{}, diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeWriteManagedDir,
			Summary: fmt.Sprintf("cannot write %s/", ManagedDir),
			Hint:    "check permissions and free space",
		})
	}
```

Add `"errors"` to `apply.go`'s imports.

- [ ] **Step 6: Run everything**

Run: `go test ./... -count=1`, then `go vet ./...` and `gofmt -l .`
Expected: PASS, both silent.

- [ ] **Step 7: Check the rendered message by hand**

Build the binary, apply once in a temp repo, make `rdk-managed/terraform` unwritable, apply again, and read what a user would see. Paste it into the report. Expected shape:

```
error: rdk apply: wrote 3 files to rdk-managed/, but could not remove the displaced copy: unlinkat ...: permission denied
  the generated tree is correct; clear .rdk/old, then re-run
```

with exit 1, and `rdk-managed/` containing the new tree.

- [ ] **Step 8: Commit**

```bash
git add internal/diag/codes.go docs/errors.md internal/apply
git commit -m "feat(apply): report materialize failures as diagnostics"
```

---

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1 && gofmt -l .
```

Then confirm the git claim the design rests on, which no unit test can make:

```bash
go build -o /tmp/rdk . && cd "$(mktemp -d)" && /tmp/rdk init >/dev/null && \
  printf 'kind: config\nname: demo\n' > rdk/config.yaml && \
  /tmp/rdk apply >/dev/null && mkdir -p .rdk/old && touch .rdk/old/leftover && \
  git status --porcelain
```

Expected: `.rdk/` appears nowhere in the output — only `rdk/` and `rdk-managed/` do. If `.rdk/` shows up, the `*` ignore is not doing what the design assumes and the whole scratch-location decision needs revisiting.

## Follow-up, not in this plan

`.rdk/` is a new ownership claim on the user's repository that DD-14's ownership decision does not name. That wants either an amendment to DD-14 or a short DD of its own — by convention, amending a design decision is its own deliberate commit, so it is deliberately outside this plan.
