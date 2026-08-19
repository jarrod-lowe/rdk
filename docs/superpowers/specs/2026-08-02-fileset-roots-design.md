# A `FileSet` entry says what its path resolves against

**Status:** approved design, pre-implementation.

## Problem

`Materialize` writes every `FileSet` key with `path.Join(scratchNew, p)` and never checks that the result stays under the staging directory. A key of `../../x` would resolve to a write at the repo root.

Nothing can supply such a key today — keys are compile-time constants, or
`path.Join("terraform/modules", d.Kind, p)` where `d.Kind` must resolve through
`kind.Lookup` and `p` comes from `fs.WalkDir` over an `embed.FS`. So this is not
an exploitable bug. It is a guarantee that rests on every caller behaving rather
than on the store enforcing it, in the one package whose stated premise is that
"insecure or escaping file access is not expressible in component code".

That guarantee has to hold up *before* rdk starts writing outside the managed
dir, which DD-14 already anticipates: CI workflows and agent-discovery files
cannot live in a directory that is deleted and regenerated every apply.

## Goals

- A `FileSet` entry cannot escape the directory it is materialized into.
- Writing outside the managed dir is possible, but only by saying so explicitly
  at the call site — never by omission, and never by a path that happens to
  climb.
- Escapes are greppable: one search finds every place rdk writes outside the
  managed dir.
- `Mem` enforces identically, or every test using the fake misrepresents
  production.

## Non-goals

- **Actually writing outside files.** That needs DD-14's reconcile (manifest
  `{path, sha256}`, hard error on a pre-existing unknown file, hard error when
  the on-disk hash differs, delete when stale). None of it is wired. This design
  makes the declaration expressible and enforced; `Materialize` rejects a set
  containing one until that lands. See "Why not write them now".
- Changing what rdk generates. The managed tree is byte-identical after this.

## Design

### The root travels on the path, not in the verb

```go
// Path is a FileSet entry's location: a repo-relative path plus what it
// resolves against. It is opaque, so a bare string cannot become one.
type Path struct{ ... }

func Managed(p string) Path     // resolves under the directory being materialized
func AtRepoRoot(p string) Path  // resolves from the repository root

func (s *FileSet) Bytes(p Path, data []byte) error
func (s *FileSet) JSON(p Path, v any) error
```

```go
set.Bytes(repofs.Managed("README.md"), readme)
set.Bytes(repofs.AtRepoRoot(".github/workflows/ci.yml"), workflow)
```

The alternative considered was a second verb — `Bytes` plus `Outside`. Rejected
on naming: the file *is* in the set, so "outside" describes membership, which is
the thing that does not vary. What varies is only what the path resolves
against, and that belongs on the path.

Rejected more firmly was an options parameter (`Bytes(p, data, repofs.AtRoot)`).
It makes the dangerous call the same shape as the safe one — one token added,
and in review it reads almost exactly like the line above it. A distinct
constructor makes the escape a deliberate word.

There is no default. With `Bytes`/`Outside`, managed-relative is what you get by
not thinking; here every call site names its root.

### Constructors do not validate; adding does

`Managed` and `AtRepoRoot` are infallible and just tag a string. Validation
happens in `Bytes`/`JSON`, which already return an error and are the only way an
entry enters the set.

This is deliberate: fallible constructors would put two error checks on every
call site (`Managed` then `Bytes`) for one guarantee, and `Bytes` needs an error
return regardless. Storing a deferred error inside `Path` was rejected — errors
hiding in values are worse than an error return one step later.

### What is rejected

Both roots: an empty path, `.`, an absolute path, any `..` surviving
`path.Clean`, and — deliberately — any path that `path.Clean` *changes*.
Requiring already-clean paths means what a reader sees in the source is what
lands on disk, with no normalization step in between to reason about.

`AtRepoRoot` additionally rejects anything under the managed dir or under
`.rdk/`. A path under the managed dir would be two names for one location, and
the manifest would end up tracking a file that gets wiped every apply — the
confusion DD-14's scope decision exists to prevent.

### Enforced twice, on purpose

`FileSet` validates on add, so the error names the call site that knows what it
meant. `Materialize` re-checks before writing, because the store owns security —
and because `Mem` must reject exactly what `osStore` rejects, or the fake tells
tests a comfortable lie.

### Why not write outside files now

Three options were weighed:

**Write if absent, hard error if present.** Looks safe and is a trap: with no
manifest, rdk cannot tell its own file from a stranger's, so the second apply
fails on the file the first one wrote.

**Build DD-14's reconcile.** A real piece of work — hash comparison, the
stale-file delete, and per-file-type error wording that DD-14 itself flags as
editorial. It deserves its own spec.

**Reject the write, keep the declaration.** Chosen. The security property lands
now; the unsafe half stays impossible rather than half-built. Rule 12 agrees: an
outside file is an exception requiring justification, so friction is correct.

`Materialize` returns a diagnostic-worthy error naming the path when a set
contains an `AtRepoRoot` entry.

## Consequences

`Result.FilesWritten` feeds "wrote N files to `rdk-managed/`". Once outside
files are written, that sentence is wrong and the summary needs splitting. Not
an issue while they are rejected, but it lands with DD-14 rather than being
discovered then.

Outside files also cannot ride the atomic swap: they are scattered individual
files, not one tree. They need their own write discipline, which is again
DD-14's reconcile.
