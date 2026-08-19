# FileSet Roots Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a `FileSet` entry state what its path resolves against, so nothing can escape the directory being materialized without saying so.

**Architecture:** An opaque `repofs.Path` carries a repo-relative string plus its root, built only by `repofs.Managed` or `repofs.AtRepoRoot`. `Bytes` and `JSON` validate on add; `Materialize` re-checks before writing. Writing an `AtRepoRoot` entry is rejected until DD-14's reconcile lands.

**Tech Stack:** Go 1.26. No new dependencies.

**Design spec:** `docs/superpowers/specs/2026-08-02-fileset-roots-design.md`

---

## File Structure

**Modified:**
- `internal/repofs/fileset.go` — `Path`, the two constructors, validation, `Bytes`/`JSON`
- `internal/repofs/fileset_test.go` — validation cases
- `internal/repofs/store.go` — `Materialize` re-checks, rejects outside entries
- `internal/repofs/mem.go` — identical enforcement in the fake
- `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`
- `internal/generate/generate.go` — three call sites
- `internal/apply/apply.go` — one call site

No new files.

---

## Task 1: `Path` and validation on add

**Files:**
- Modify: `internal/repofs/fileset.go`
- Test: `internal/repofs/fileset_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/repofs/fileset_test.go`:

```go
// A key that climbs would resolve outside the directory being materialized.
// Nothing can supply one today; the point is that it stops being possible.
func TestFileSetRejectsEscapingPaths(t *testing.T) {
	for _, p := range []string{"", ".", "/abs.txt", "../up.txt", "a/../../up.txt", "./a.txt", "a//b.txt", "a/"} {
		set := NewFileSet()
		if err := set.Bytes(Managed(p), []byte("x")); err == nil {
			t.Errorf("Managed(%q) was accepted", p)
		}
		if err := set.Bytes(AtRepoRoot(p), []byte("x")); err == nil {
			t.Errorf("AtRepoRoot(%q) was accepted", p)
		}
	}
}

// Requiring already-clean paths means what a reader sees in the source is what
// lands on disk, with no normalisation step in between to reason about.
func TestFileSetRejectsUncleanPaths(t *testing.T) {
	set := NewFileSet()
	if err := set.Bytes(Managed("terraform/./main.tf.json"), []byte("x")); err == nil {
		t.Error("an unclean path was accepted")
	}
}

func TestFileSetAcceptsOrdinaryPaths(t *testing.T) {
	set := NewFileSet()
	for _, p := range []string{"README.md", "terraform/main.tf.json", "a/b/c.txt"} {
		if err := set.Bytes(Managed(p), []byte("x")); err != nil {
			t.Errorf("Managed(%q): %v", p, err)
		}
	}
	if err := set.Bytes(AtRepoRoot(".github/workflows/ci.yml"), []byte("x")); err != nil {
		t.Errorf("AtRepoRoot: %v", err)
	}
}

// Two names for one location: the manifest would end up tracking a file that
// gets wiped every apply (DD-14's scope decision).
func TestAtRepoRootRejectsPathsInsideRdkOwnedDirs(t *testing.T) {
	set := NewFileSet()
	for _, p := range []string{"rdk-managed/x.txt", ScratchDir + "/x.txt", ScratchDir} {
		if err := set.Bytes(AtRepoRoot(p), []byte("x")); err == nil {
			t.Errorf("AtRepoRoot(%q) was accepted", p)
		}
	}
}

func TestJSONValidatesItsPathToo(t *testing.T) {
	set := NewFileSet()
	if err := set.JSON(Managed("../escape.json"), map[string]any{}); err == nil {
		t.Error("JSON accepted an escaping path")
	}
}
```

Note `AtRepoRoot` must reject the managed dir by name. `FileSet` does not know
which directory it will be materialized into, so use the same constant the rest
of the code uses — check what `apply.ManagedDir` is and whether `repofs` can see
it without an import cycle. If it cannot, add an unexported constant in `repofs`
with a comment saying it must match `apply.ManagedDir`, and a test asserting
they are equal from a package that can see both (`internal/apply`).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repofs/`
Expected: FAIL — `undefined: Managed`.

- [ ] **Step 3: Implement**

In `internal/repofs/fileset.go`:

```go
// Root is what a Path resolves against.
type Root int

const (
	rootManaged Root = iota
	rootRepo
)

// Path is a FileSet entry's location: a repo-relative path plus what it
// resolves against. It is opaque so that a bare string cannot become one —
// escaping the managed directory has to be a word someone typed, not a path
// that happened to climb.
type Path struct {
	p    string
	root Root
}

// Managed resolves under the directory being materialized. This is the
// ordinary case: everything rdk generates lives there and is replaced wholesale
// every apply.
func Managed(p string) Path { return Path{p: p, root: rootManaged} }

// AtRepoRoot resolves from the repository root, for the few files that cannot
// live in a directory rdk deletes and regenerates — CI workflows,
// agent-discovery files. DD-14 calls these outside files and requires manifest
// accounting for them, so they are an exception requiring justification
// (rule 12), not a convenience.
func AtRepoRoot(p string) Path { return Path{p: p, root: rootRepo} }
```

The constructors deliberately do not validate: `Bytes` and `JSON` already return
an error and are the only way an entry enters a set, so validating there gives
one error check per call site instead of two.

Add the validator and wire it into both `Bytes` and `JSON`:

```go
// check rejects anything that could resolve somewhere the caller did not name.
// Unclean paths are rejected rather than cleaned so that what a reader sees in
// the source is what lands on disk.
func (p Path) check() error {
	if p.p == "" || p.p == "." {
		return fmt.Errorf("empty path")
	}
	if path.IsAbs(p.p) {
		return fmt.Errorf("%q: path must be relative", p.p)
	}
	if path.Clean(p.p) != p.p {
		return fmt.Errorf("%q: path must already be clean (got %q)", p.p, path.Clean(p.p))
	}
	if p.p == ".." || strings.HasPrefix(p.p, "../") {
		return fmt.Errorf("%q: path must not climb out", p.p)
	}
	if p.root == rootRepo {
		for _, owned := range []string{managedDirName, ScratchDir} {
			if p.p == owned || strings.HasPrefix(p.p, owned+"/") {
				return fmt.Errorf("%q: is inside %s, which rdk replaces wholesale — use Managed", p.p, owned)
			}
		}
	}
	return nil
}
```

`Bytes` becomes `func (s *FileSet) Bytes(p Path, data []byte) error`, calling
`check` before storing. Keep the entries keyed so that root and path travel
together — two maps, or one map keyed by `Path`; pick whichever keeps
`sortedPaths` simple and say why in your report.

`sortedPaths` is used by both `Materialize` implementations. Whatever shape you
choose, managed and repo-root entries must be separable without either caller
having to filter.

- [ ] **Step 4: Run**

Run: `go test ./internal/repofs/ -count=1`
Expected: the new tests pass. Others will fail to compile until Task 2 — that is expected; do not fix them here.

- [ ] **Step 5: Do not commit yet**

Changing `Bytes`'s signature breaks every caller, so the tree does not build
until Task 2 updates them. The two tasks are split for reading, not for history:
Task 2 commits both together. Do not commit a broken tree.

---

## Task 2: Both stores enforce it

**Files:**
- Modify: `internal/repofs/store.go`, `internal/repofs/mem.go`
- Modify: `internal/generate/generate.go`, `internal/apply/apply.go`
- Test: `internal/repofs/store_test.go`, `internal/repofs/mem_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// The store owns security: a set that somehow carries a bad entry must not be
// written, whatever the FileSet already checked.
func TestMaterializeRejectsAnOutsideEntry(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	if err := set.Bytes(Managed("f.txt"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := set.Bytes(AtRepoRoot(".github/workflows/ci.yml"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err == nil {
		t.Fatal("want an error for an outside entry")
	}
	// Nothing partial: the managed tree must not appear either.
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("wrote a partial tree before rejecting")
	}
}
```

Add the same assertion for `Mem` in `mem_test.go` — the fake must reject what
the real store rejects, or every test using it misrepresents production.

- [ ] **Step 2: Implement**

In both `osStore.Materialize` and `Mem.Materialize`, reject a set containing any
repo-root entry **before** doing any work, with a message naming the path:

```go
	// Outside files need DD-14's manifest reconcile — hard error on a
	// pre-existing unknown file, hash comparison against the manifest, the
	// stale-file delete. None of that is wired, and writing them blindly is
	// how apply clobbers files the user owns.
```

Then iterate the managed entries as before.

- [ ] **Step 3: Update the call sites**

`internal/generate/generate.go` — three:

```go
	if err := set.Bytes(repofs.Managed("README.md"), []byte(readme)); err != nil {
		return nil, err
	}
	...
	if err := set.JSON(repofs.Managed("terraform/main.tf.json"), doc); err != nil {
		return nil, err
	}
```

and in `vendorModule`:

```go
		return set.Bytes(repofs.Managed(path.Join("terraform/modules", name, p)), content)
```

`internal/apply/apply.go` — one: `set.JSON(repofs.Managed("manifest.json"), m)`.

- [ ] **Step 4: Run everything**

Run: `go test ./... -count=1`, `go vet ./...`, `gofmt -l .`
Expected: PASS, both silent. **The golden tests must pass unchanged** — the
managed tree is byte-identical after this. If a golden diff appears, stop and
report; do not regenerate.

- [ ] **Step 5: Commit**

```bash
git add internal/repofs internal/generate/generate.go internal/apply/apply.go
git commit -m "feat(repofs): a FileSet entry says what its path resolves against"
```

---

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1 && gofmt -l .
```

Then confirm the escape is greppable, which is half the point of the design:

```bash
grep -rn "AtRepoRoot(" --include='*.go' internal cmd | grep -v _test.go
```

Expected: no matches outside tests. When a real outside file lands, that one
command is the audit of every place rdk writes beyond the managed dir.

## Follow-up, not in this plan

DD-14's reconcile — the manifest `{path, sha256}` accounting that makes writing
an outside file safe. Until it exists, `Materialize` rejects those entries. The
apply summary's "wrote N files to `rdk-managed/`" will also need splitting once
files land elsewhere.
