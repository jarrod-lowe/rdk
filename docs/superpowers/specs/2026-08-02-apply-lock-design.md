# One apply at a time, per repository

**Status:** approved design, pre-implementation.

## Problem

`Materialize` uses fixed scratch names, `.rdk/new` and `.rdk/old`, and nothing
serialises two runs. Two `rdk apply` processes in one checkout interleave:

- A clears `.rdk/new` and starts staging.
- B clears `.rdk/new` mid-write, destroying A's partial staging, and stages its
  own files into the same directory.
- Whichever publishes first renames a **mixture** into place — some of A's
  files, some of B's — and can exit 0.

The result is a managed tree that no single run generated. That breaks rule 13
outright (the state after apply is defined by generation alone) and it is
silent, which rule 6 refuses. Likelihood is low — two terminals, a file watcher,
parallel CI jobs on a shared workspace — but the failure mode is wrong output
reported as success.

## Goals

- Two concurrent applies in one checkout cannot produce a tree that no run
  generated.
- The loser is told what happened and what to do, rather than failing obscurely.
- No reliance on filesystem lock propagation, which varies by mount option.
- Nothing is left behind under any ordinary exit, including Ctrl-C.

## Non-goals

- Serialising two checkouts of the same repository on different storage. Nothing
  could, and it is not the reported failure.
- Making the loser *wait*. Failing fast is louder and avoids an apply that
  appears to hang. A waiting mode stays available later.

## Design

### Why not `flock`

`flock` was the obvious choice because the kernel releases it when the process
dies, including on `SIGKILL`. It was rejected because it degrades **silently**
on shared storage:

- On Linux, `flock` over NFS is emulated as a whole-file POSIX lock and does
  reach the server — unless the mount uses `local_lock=flock` or `local_lock=all`,
  in which case it is purely local and two hosts exclude nothing. No error.
- NFSv3 needs `lockd`/`statd` running at all.
- macOS's NFS client behaves differently again, and this project is developed on
  darwin.
- Release stops being immediate: a killed process's lock persists until the
  NFSv4 lease expires.

A lock that silently does not lock is worse than no lock, because it
manufactures confidence.

### Exclusive creation instead

`O_CREAT|O_EXCL` is atomic on NFSv3 and later and does not depend on mount
options or a lock daemon. It is the classic NFS-safe lock for exactly this
reason.

```
.rdk/lock    created exclusively; held for the whole materialization
```

Content is `host`, `pid`, and an RFC3339 start time. It is **displayed, never
acted on**: rdk must not decide a lock is stale because a pid looks dead, since
on shared storage the pid is not even meaningful. Any staleness heuristic is the
cleverness rule 12 refuses; the human decides.

Unique per-run scratch names were also considered — they remove the mixture
without any lock, since each run stages in its own directory and the publishes
serialise into last-writer-wins. Rejected because a hard kill then leaves
arbitrarily-named directories behind, and sweeping them safely needs the same
staleness heuristic. One known path left behind is recoverable; a growing pile
of unknown ones is not.

### Where the lock sits in the sequence

```
1. reject outside entries
2. Stat the managed dir (drives two later decisions)
3. check and create .rdk/          ← must precede the lock; the dir must exist
4. ACQUIRE .rdk/lock               ← defer release
5. write .rdk/.gitignore
6. clear new (and old, when the managed dir exists)
7. stage into .rdk/new
8. check the managed dir's parent components
9. displace by rename, publish by rename
10. sweep .rdk/old
11. RELEASE .rdk/lock
```

Step 3 sits outside the lock and is safe there: `MkdirAll` is idempotent and the
symlink check is a read. Step 5 is deliberately *inside* it — the `.gitignore`
write is remove-then-create, so two concurrent runs would otherwise race and one
would fail `O_EXCL` spuriously.

### Release on every exit

Deferred release covers every `return err` path and a panic. Errors that happen
before materialising — a parse failure, a bad definition — never reach the lock.

`SIGINT` and `SIGTERM` do **not** run defers, and Ctrl-C during a slow apply is
not exotic: it is the ordinary way a developer changes their mind. Without
handling it, breaking the lock becomes a routine step rather than a rare
recovery. So the CLI installs a handler that releases the lock and exits.

That is safe precisely because of the atomic-materialize work: every
intermediate state either self-heals on the next run or fails loudly, so the
handler only removes the lock — it unwinds nothing. A signal arriving mid-rename
leaves a state the next run already handles.

Exit status follows the shell convention of 128 + signal: 130 for `SIGINT`, 143
for `SIGTERM`. A script can then tell an interruption from a definition error
(1) or an rdk bug (2).

Only `SIGKILL`, a power loss, or an OOM kill now leave a lock behind.

### Breaking a lock is a deliberate act

`rdk apply --break-lock` removes an existing lock, then takes its own and
applies normally.

Named `--break-lock` rather than `--force`: "force" is generic enough to be
added reflexively to get past an error, and forcing past a lock while an apply
genuinely is running reinstates the corruption the lock prevents. "Break lock"
names the thing being done.

It **announces what it broke**, as a warning carrying the host, pid and time
from the lock file. Taking someone else's lock is surprising state, and rule 6
says surprising state announces itself; silent success would be the worst
outcome here.

In the code it is a separate call rather than a mode of materialising: `Store`
gains `BreakLock`, and `cmd` invokes it before `apply.Run` when the flag is set.
`Materialize`'s signature does not change, breaking a lock is a distinct act at
a distinct moment even internally, and `Mem` has to implement it too — so tests
cannot drift from production.

### Rendered

```
$ rdk apply
error: another rdk apply is running in this repository
  held since 2026-08-02T10:04:11Z by pid 4127 on host builder-3
  if no apply is running, re-run with --break-lock

$ rdk apply --break-lock
warning: broke a lock held since 2026-08-02T10:04:11Z by pid 4127 on host builder-3
rdk apply: wrote 6 files to rdk-managed/
```

## Consequences

`Store` grows two methods, `BreakLock` and an idempotent `ReleaseLock`, the
latter so the signal handler and the deferred release can both call it. The
store owns the lock state; `cmd` owns the signal.

`cmd` gains signal handling, which it has not had before. It is confined to
releasing the lock and exiting — it must not grow into general cleanup.

A hard kill still leaves `.rdk/lock`, and the recovery is `--break-lock`. That
is the accepted price of not depending on lock propagation, and the error
message states it.
