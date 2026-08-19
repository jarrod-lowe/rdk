# `internal/repofs` — secure, injected file materialization

**Status:** approved design, pre-implementation. Folded into PR-1 (the spine)
before merge.

## Problem

File handling has accreted ad-hoc security fixes across components: `securejoin`
in `apply`, `Lstat` + `O_CREATE|O_EXCL` in `initialize`, lexical `..`/absolute
path validation in `manifest`, and a hand-rolled staging/swap/recovery in
`apply`. Each patch is individually correct, but the security lives at the call
sites — easy to forget at the next one — and it forces tests to write real
files. This is a symptom-by-symptom pattern where a single abstraction belongs.

## Goals

- One injected library, `internal/repofs`, is the *only* way components touch the
  filesystem. Insecure or escaping file access is not expressible in component
  code.
- Express **intent** ("materialize this managed tree", "seed this file once"),
  not primitives — the library owns directory creation, permissions,
  deterministic serialization, atomic replacement, and confinement.
- Make component unit tests filesystem-free via an in-memory fake.
- Simplify: delete the scattered guards and the `filepath-securejoin`
  dependency.

## Non-goals (YAGNI — add when a component needs them)

- `WriteOutside` / `RemoveOutside`: PR-1 generates zero files outside the managed
  dir, so nothing exercises outside-file mutation yet. Added when the pipeline
  PR (DD-8) first emits outside files.
- `FileSet.YAML`, template entries: no component serializes a struct to YAML or
  renders a template to disk in PR-1 (definitions are YAML *input*, read then
  parsed; the seed is a static string).
- Multi-root / cross-repo access.

## The abstraction

Package `internal/repofs`.

### FileSet — a described set of files (a plain value, not injected)

Components build a `FileSet` describing what they want written; each entry knows
how to produce deterministic bytes on demand. Entry paths are **relative to the
managed directory the set is materialized into** (forward-slash) — e.g.
`terraform/main.tf.json`, `manifest.json`. The `FileSet` is extendable by the
caller (`apply` takes `generate`'s set and adds the manifest entry). By contrast,
`Store`'s discrete ops (`Seed`, `ReadFile`, `ReadDir`) take **repo-relative**
paths.

```go
type FileSet struct { /* ordered entries: path -> content producer */ }

// Bytes adds a file from raw bytes (static text, embedded module source,
// rendered template output).
func (s *FileSet) Bytes(path string, data []byte)

// JSON adds a file whose content is v serialized as deterministic JSON by
// repofs (see Determinism). Callers pass the data structure, not bytes.
func (s *FileSet) JSON(path string, v any)
```

Only `Bytes` and `JSON` are implemented now (the two content kinds PR-1
produces). `YAML` and template entries are added later without breaking callers.

### Store — the injected interface (the actions we perform)

This is what gets faked in tests. Only the actions PR-1 uses:

```go
type Store interface {
	// Materialize atomically replaces the managed directory with the FileSet:
	// it stages the full tree in a sibling temp dir, then swaps it into place,
	// recovering a swap interrupted by an earlier crash. Parent directories are
	// created; files use 0o644, dirs 0o755. Rooted and symlink-safe.
	Materialize(managedDir string, set FileSet) error

	// Seed creates a user-owned file once. It never overwrites an existing
	// file and never follows a symlink at the target; a pre-existing path is a
	// no-op (the file is the developer's from creation — DD-3).
	Seed(path string, data []byte) error

	// ReadFile / ReadDir read within the repo root, symlink-safe. ReadDir
	// returns entry names (sorted) for a directory.
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
}
```

### Real implementation — one `os.Root` at the repo root

`repofs.New(repoRoot string) (Store, error)` opens `os.OpenRoot(repoRoot)` (Go
1.24+, present on our recent-Go toolchain — DD-17) and holds the `*os.Root`.
Every method delegates to the rooted handle (`ReadFile`, `WriteFile`, `MkdirAll`,
`Remove`, `RemoveAll`, `Rename`, `OpenFile`, `Lstat`, …), all of which are
confined to the root and refuse paths that escape via `..`, an absolute path, or
a symlink whose target leaves the root. **Security is a property of the handle,
not of any call site.**

- `Materialize` stages into `<managedDir>.staging` (a rooted sibling), writes
  every entry, then `RemoveAll(managedDir)` + `Rename(staging, managedDir)`, with
  the same interrupted-swap recovery previously in `apply` (managed absent +
  staging present → finish the rename). This logic now lives here, once.
- `Seed` uses `OpenFile(path, O_CREATE|O_EXCL|O_WRONLY, 0o644)`: exclusive create
  (no overwrite), and `os.Root` will not traverse an escaping symlink at the
  target.

### Fake implementation — in-memory, for component tests

`repofs.NewMem() *Mem` holds an in-memory map of path → bytes. `Materialize`
records the resolved file set (replacing any prior managed content under
`managedDir`); `Seed` writes only if absent; `ReadFile`/`ReadDir` serve the map.
Component tests assert *what would be written* — content and paths — with no
disk. The fake does not reproduce `os.Root`'s confinement semantics: security is
verified against the real implementation, component logic against the fake.

## Determinism / serialization ownership

`repofs.JSON` is the single owner of deterministic JSON output (DD-1). It must
reproduce the bytes the current `tfjson.go` and `manifest.Encode` produce so
committed goldens do not churn: `encoding/json` with two-space indent, sorted map
keys, and the stdlib's default HTML escaping (the `>` form chosen
deliberately). One serializer, one place — the tf.json escaping decision and the
manifest formatting stop being per-component.

## Component migrations

- **`generate.Build(defs) FileSet`** — returns a `FileSet` instead of the
  `Tree map[string][]byte` type (which is deleted): `JSON("terraform/main.tf.json",
  doc)`, `Bytes("README.md", …)`, `Bytes("terraform/modules/<kind>/<file>", …)`.
  No pre-serialization in `generate`.
- **`apply.Run(store repofs.Store, root, version string)`** — parse via the
  store, reconcile (pure, unchanged), assemble the `FileSet` (generate's set plus
  `JSON("manifest.json", m)`), then `store.Materialize(ManagedDir, set)`. The
  staging/swap/recovery and `securejoin` usage are removed from `apply`.
- **`manifest`** — `Reconcile` stays pure. The `Manifest` struct is written via
  `FileSet.JSON`; `Load` reads via `store.ReadFile`. The lexical path validation
  (`validOutsidePath`) is deleted — `os.Root` blocks escapes structurally.
- **`initialize.Run(store repofs.Store, dir string)`** — `store.Seed(
  "rdk/config.yaml", seedConfig)`; the `Lstat` guard and `OpenFile` dance are
  gone. `git init` remains (not a file op).
- **`parse.Dir`** — lists and reads definitions through `store.ReadDir` /
  `store.ReadFile`.
- **`cmd`** — constructs `repofs.New(cwd)` and injects it into `apply.Run` and
  `initialize.Run`.

## Deletions

- `github.com/cyphar/filepath-securejoin` (dependency + all call sites).
- `manifest.validOutsidePath` and its tests (superseded by `os.Root`).
- `apply`'s `recoverInterruptedSwap` and inline staging/swap (moved to
  `Materialize`).
- `initialize`'s `Lstat` symlink check and `O_EXCL` seed code (moved to `Seed`).
- `generate`'s `Tree` type.

## Testing strategy

- **Component logic** (generate, apply orchestration, init, manifest reconcile):
  the in-memory fake. No disk. Assert produced file sets / behavior.
- **`repofs` real implementation** (its own package tests, temp-dir `os.Root`):
  - Security: a FileSet/manifest path containing `..`, an absolute path, and a
    symlink whose target escapes the root all fail — confinement proven against
    the real handle.
  - `Materialize`: correct tree written, dirs/perms, prior managed content
    replaced, idempotent re-materialize, and interrupted-swap recovery
    (managed absent + staging present → completed).
  - `Seed`: creates once, no overwrite, refuses an escaping symlink target.
- **Golden** (`golden_test.go`): the real store in a temp dir; output must match
  committed goldens (see risk 1).

## Risks

1. **Golden churn.** `repofs.JSON` must reproduce current bytes exactly. The
   manifest presently uses `json.MarshalIndent` (no trailing newline) while
   `tfjson` uses a `json.Encoder` (trailing newline). Unifying on one serializer
   may change `manifest.json` by a trailing byte, requiring a one-time, reviewed
   `-update` of the golden. Verify during implementation and flag the diff
   explicitly; do not blind-update.
2. **Error quality.** Dropping the manifest's lexical validation means a bad
   path surfaces as an `os.Root` error rather than a curated rule-11 message.
   Acceptable: those paths are rdk-generated. If a specific message proves
   valuable later, add it in `repofs`, once.

## Relationship to existing decisions

- Rule 3 (one owner per file), rule 6 (loud/never silent), rule 13 (always
  write, never diff) — `Materialize` and `Seed` encode these in one place.
- DD-1 (determinism) — `repofs.JSON` centralizes it.
- DD-14 (manifest / outside files) — the manifest is materialized through the
  same path; outside-file ops arrive with `WriteOutside`/`RemoveOutside` later.
- DD-17 (recent Go) — this design depends on `os.Root`, a recent-Go feature; a
  concrete payoff of that policy.
- Recorded as **DD-18** in `design-decisions.md`.
