# Atomic materialize — no step may leave the managed tree torn

**Status:** approved design, pre-implementation.

## Problem

`repofs.Materialize` already stages and swaps: it writes the whole new tree into
`rdk-managed.staging`, deletes `rdk-managed`, then renames staging into place.
The delete is the flaw. `RemoveAll` walks a tree deleting as it goes, so a
locked file, a permission error, or an unremovable directory partway through
leaves `rdk-managed` **half-deleted** and returns an error.

That state is not self-healing, because it is indistinguishable from a healthy
one. On the next apply `rdk-managed` still exists, so the recovery branch does
not fire; the first thing that runs is `RemoveAll(staging)`, which destroys the
complete replacement sitting right there; the tree is rebuilt and the same
`RemoveAll` fails again. If the cause is persistent, that repeats forever with a
corrupt tree on disk — violating rule 13, which says the state after apply is
defined by generation alone.

Every other interruption already recovers:

| Interrupted at | On disk | Next apply |
|---|---|---|
| clear staging fails | managed intact | retries, no harm |
| a write fails | managed intact, staging partial | staging cleared, rebuilt |
| **delete fails partway** | **managed torn, staging complete** | **replacement destroyed, same failure, stuck** |
| crash between delete and publish | managed absent, staging complete | recovered |
| publish rename fails | managed absent, staging complete | recovered |

One non-atomic destructive step, whose partial failure reads as success.

## Goals

- No step may leave the managed tree in a state that is neither the old tree nor
  the new one.
- Every failure returns an error naming what failed, at the moment it fails.
- rdk's scratch directories are invisible to git without rdk ever writing to the
  user's root `.gitignore` (rule 3, DD-14).
- The failure that happens *after* the tree is correct must say so, or the user
  will go looking for damage that isn't there (rule 11).

## Non-goals

- **Recovering an interrupted swap.** Dropped deliberately — see below.
- **Cleaning up legacy `rdk-managed.staging` directories.** The current
  implementation renames staging away on success, so one exists only where a
  past apply failed. Users delete it; rdk does not go looking.
- Cross-filesystem materialization. Everything stays inside the repo root, so
  every rename is same-filesystem by construction.

## Design

### Scratch lives in one rdk-owned, self-ignoring directory

```
.rdk/               # rdk-owned scratch
  .gitignore        # contains "*"
  new/              # the tree being built
  old/              # the tree just displaced
rdk-managed/        # published, committed
rdk/                # user definitions
```

DD-14 fixes the mechanism: *"rdk's ignore rules live in per-directory
`.gitignore` files inside rdk-owned dirs; the root `.gitignore` is seeded-once
user property."* A per-directory `.gitignore` governs only its own directory and
below, so **nothing rdk owns can ignore a sibling of `rdk-managed/`** — only the
root `.gitignore` reaches `rdk-managed.staging`, and rule 3 forbids apply from
rewriting it. Seeding it at init does not rescue this either: `Seed` is a no-op
when the file already exists, which is the common case.

So the scratch moves inside a single rdk-owned directory that carries its own
ignore file. A `.gitignore` of `*` ignores the directory's entire contents,
itself included; since git tracks files rather than directories, `.rdk/` then
produces nothing in `git status`. This is the same mechanism DD-14 already
applies to bootstrap dirs, which it describes as using "DD-14's per-directory
`.gitignore` mechanism and are neither rdk-owned nor committed."

`Materialize` creates `.rdk/` and writes its `.gitignore` on every run — always
write, never diff (rule 13) — so a deleted or edited one is restored.

### The sequence

```
0. MkdirAll(.rdk) + write .rdk/.gitignore ("*")
1. RemoveAll(.rdk/new)                mandatory: the name must be free
2. RemoveAll(.rdk/old)                only when the managed dir exists — see below
3. write the FileSet into .rdk/new
4. rename(rdk-managed → .rdk/old)     skipped when rdk-managed does not exist
5. rename(.rdk/new → rdk-managed)     the publish
6. RemoveAll(.rdk/old)                the sweep
```

The destructive step is now a rename. A rename is all-or-nothing, so there is no
partial state to be misread — the failure mode that motivated this work cannot
occur. It also fixes Windows, where renaming *onto* an existing directory fails
outright, which the current code does.

Steps 1 and 2 are not redundant with step 6. They are what covers a hard kill,
where step 6 never runs at all, and a step 6 that failed on the previous run.
Their job is different: 1 and 2 **must** succeed because the names are needed;
6 is tidying.

Step 2 is conditional for that same reason. `.rdk/old` is only needed as a name
when step 4 is going to rename onto it, which happens only when the managed dir
exists. When it does not — the state a failed publish leaves — clearing `old/`
achieves nothing except destroying the only local copy of the previous tree,
right before a retry that may fail again. So it is cleared when the name is
needed and left alone when it is not. This does not weaken the invariant below:
the scratch is still never *read*, only deleted. Not reading it and not
needlessly deleting it are different properties, and only the first is what lets
the recovery step go.

The sweep cannot move earlier. `.rdk/old` exists only after step 4, so removing
it before step 5 would put a recursive delete inside the window where
`rdk-managed` does not exist — lengthening exactly the gap this design shortens.

### Failure states

| Fails at | Tree on disk | Reported as |
|---|---|---|
| 1, 2 | intact | cannot clear its own scratch |
| 3 | intact | cannot stage — disk full, permissions |
| 4 | intact | cannot displace the current tree |
| 5 | absent; `.rdk/old` holds the previous tree | cannot publish |
| 6 | **correct and complete** | apply succeeded, but the scratch copy remains |

Only step 5 leaves the repo without `rdk-managed`, and that is honest rather
than corrupt: the next apply regenerates and publishes. Nothing unique is lost,
because `rdk-managed/` is committed and the previous tree is in git history.

A hard kill returns no error at all and leaves `.rdk/new` or `.rdk/old` behind
silently. That is accepted: the next apply sweeps it, git never saw it, and the
user knows their machine died.

### No recovery step

The current recovery — managed absent plus staging present means finish the
rename — is removed rather than ported.

It is redundant. Without it, a crash between steps 4 and 5 leaves managed absent
and `.rdk/new` complete; the next apply clears the scratch, rebuilds, finds
nothing to displace at step 4, and publishes. The result is byte-identical to
the tree recovery would have rescued, because generation is a pure function
(rule 1).

It only ever mattered when the *next* run could not generate — and that is
nearly unreachable, since `apply.Run` parses before it materializes, so a broken
definition fails long before `Materialize` is called and never touches the tree.
Reaching it needs a crash in the window, then the definitions edited into a
broken state, then an apply.

Removing it also removes the need to trust the scratch across runs. Nothing ever
reads `.rdk/new`, so nothing has to prove it is complete or that it is rdk's —
which is what made the earlier "validate the staging directory" question
disappear.

### The sweep failure is a diagnostic, not an internal error

Step 6 fails after the deliverable is correct. Three things follow.

**It must not be swallowed.** Ignoring it does not avoid the problem, it
relocates it: the same locked file blocks step 2 of the *next* apply, which is
mandatory, so a run that would otherwise have worked now fails — and by then the
cause is separated in time from the run that created it. Failing immediately
puts the error next to the thing that caused it.

**It is exit 1, not 2.** A file that will not delete is environmental — a lock,
a permission, an antivirus — not an rdk bug. Exit 2 renders as `code: internal`,
which `docs/errors.md` documents as "this is an rdk bug. Report it with the
message." The precedent for environmental failures is `read-defs-dir`,
`read-file` and `git-init`: diagnostics with their own codes, exit 1. This gets
`scratch-not-removed`.

**The message leads with the success.** This is the only failure in the sequence
that happens after the tree is correct, so the wording carries real weight:

```
error: rdk apply: wrote 3 files to rdk-managed/, but could not remove .rdk/old: permission denied
  the generated tree is correct; clear .rdk/old, then re-run
```

Get that wrong and someone re-runs a healthy apply, or starts deleting
`rdk-managed/` to fix a problem that does not exist.

### Making the sweep failure distinguishable

`apply` must be able to tell a sweep failure from every other `Materialize`
failure, or all of them collapse into one code and one wording. `repofs` stays
free of any `diag` dependency — it is a filesystem layer — so it exports a
sentinel instead:

```go
// ErrSweep reports that the tree was published but the displaced copy could
// not be removed. The caller frames it: the apply succeeded.
var ErrSweep = errors.New("scratch not removed")
```

wrapped around the underlying error, and `apply` tests it with `errors.Is` to
choose between `CodeScratchNotRemoved` and the general materialize diagnostic.
The user-facing wording lives in `apply`, alongside every other diagnostic.

## Testing

- Each failure row above, driven against a real temp directory, since the swap
  semantics live only in `osStore` (`Mem` implements `Materialize` as a plain
  in-memory replacement and does not model the swap).
- Step 4 and 5 failures forced by making the target undeletable/unrenamable —
  on Unix, a read-only parent directory is the reliable lever.
- A crash between 4 and 5 simulated by leaving that state on disk directly, then
  asserting the next `Materialize` publishes correctly **without** a recovery
  step existing.
- `.rdk/.gitignore` is written on every run, and restored when deleted.
- `git status` is clean in a repo after an apply that left `.rdk/old` behind —
  the claim that a `*` ignore hides the directory is worth pinning rather than
  assuming.
- `TestMaterializeRecoversInterruptedSwap` is deleted; it asserts behaviour this
  design removes deliberately.

## Consequences to record elsewhere

`.rdk/` is a new ownership claim on the user's repository, and DD-14's ownership
decision does not currently name it. That warrants either an amendment to DD-14
or a short DD of its own — flagged here rather than decided, since amending a
design decision is its own commit by convention.
