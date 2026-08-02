# One apply at a time, per repository

**Status:** approved design, pre-implementation. Implemented in two steps — see
"Sequencing".

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
silent, which rule 6 refuses.

A second, related need: a developer or agent working on the tree wants to stop
anything else applying while they work. That is the same exclusion, held for
longer and released deliberately, so it is the same mechanism (rule 7).

## Goals

- Two concurrent applies in one checkout cannot produce a tree that no run
  generated.
- A human or agent can hold the repository against applies, saying why.
- Every way past a lock requires having *observed* that specific lock.
- No reliance on filesystem lock propagation, which varies by mount option.
- Nothing is left behind under any ordinary exit, including Ctrl-C.

## Non-goals

- Serialising two checkouts of the same repository on different storage. Nothing
  could, and it is not the reported failure.
- Making the loser wait. Failing fast is louder, and an apply that appears to
  hang is its own support burden. A waiting mode stays available later.
- Any check on whether the working tree is dirty. See "No tree-state policy".

## Design

### Why not `flock`

`flock` was the obvious choice, because the kernel releases it when the process
dies. It was rejected because it degrades **silently** on shared storage:

- On Linux, `flock` over NFS is emulated as a whole-file POSIX lock and does
  reach the server — unless the mount uses `local_lock=flock` or
  `local_lock=all`, in which case it is purely local and two hosts exclude
  nothing. No error.
- NFSv3 needs `lockd`/`statd` running at all.
- macOS's NFS client behaves differently again, and this project is developed on
  darwin.
- Release stops being immediate: a killed process's lock persists until the
  NFSv4 lease expires.

A lock that silently does not lock is worse than no lock, because it
manufactures confidence.

`O_CREAT|O_EXCL` is atomic on NFSv3 and later and depends on no mount option or
lock daemon. It is the classic NFS-safe lock for exactly this reason.

Unique per-run scratch names were also considered — they remove the mixture
without any lock, since each run stages in its own directory. Rejected because a
hard kill then leaves arbitrarily-named directories behind, and sweeping them
safely needs a staleness heuristic. One known path left behind is recoverable; a
growing pile of unknown ones is not.

### The lock file

`.rdk/lock`, created exclusively, JSON:

```json
{
  "id": "9f3a1c4e7b2d8a05",
  "kind": "apply",
  "host": "builder-3",
  "pid": 4127,
  "since": "2026-08-02T10:04:11Z",
  "message": "agent refactoring the s3-bucket module"
}
```

`message` is set only for `kind: held`.

**This must not go through `FileSet.JSON`**, whose comment claims it is the
single owner of rdk's JSON output format (DD-1). It is — for *generated output*,
where determinism is load-bearing. The lock is coordination state: gitignored,
never read by generation, never part of the tree. Plain `encoding/json`.

The same distinction answers an objection this would otherwise attract: rule 1
bans clocks and randomness. It bans them **in generation**. A timestamp and a
random id in a gitignored coordination file never touch the pure function.

The contents are **displayed, never acted on**. rdk must not decide a lock is
stale because a pid looks dead — on shared storage the pid is not even
meaningful. Any staleness heuristic is the cleverness rule 12 refuses; the human
decides, and the id is how they say which lock they decided about.

### Two kinds, one file

| Kind | Taken by | Released by |
|---|---|---|
| `apply` | `rdk apply`, for the duration of `Materialize` | defer, and the signal handler |
| `held` | `rdk lock -m "…"`, which exits leaving it | `rdk unlock <id>`, or `--break-lock` |

They share one file because otherwise they would not exclude each other.

The consequence to get right: **the signal handler must release only an `apply`
lock this process took.** `rdk lock`'s entire purpose is to leave one behind, so
"release on Ctrl-C" must not mean "remove whatever is there" — an agent's lock
evaporating because someone pressed Ctrl-C in another terminal would be a silent
failure of the whole feature.

### Every way past a lock names the lock

```
rdk apply                        takes kind=apply, releases on exit, defer and signal
rdk apply --with-lock=<id>       runs under an existing held lock, leaves it in place
rdk apply --break-lock=<id>      removes a stranded lock, warns whose it was, then applies
rdk lock -m "…"                  takes kind=held, prints the id, exits leaving it
rdk unlock <id>                  releases a held lock — the routine end of your own
```

The id is **required**, not optional, on both flags. The safety property is
compare-and-swap, and it is stronger than "do not break the wrong lock": between
reading an error and typing the recovery, the stranded lock may have been
released and a *live* apply started. A blind `--break-lock` would kill that live
one and reinstate the corruption the lock exists to prevent. Requiring the id
means you cannot get past a lock you never observed.

`--with-lock` and `--break-lock` together are an error; they are contradictory.

`--with-lock` semantics:

- id matches → apply proceeds and **does not release the lock afterwards**. The
  held lock has to survive; that is the point.
- id does not match → error. Someone else's lock, or yours was broken and
  replaced.
- no lock at all → **also an error**. You asserted you hold a lock and you do
  not, which means it was broken out from under you. Proceeding would hide that.

Everyone without the id stays blocked, which is what the holder wanted.

`rdk lock` twice is an error, which falls out of exclusive creation for free: the
second gets the same "already locked" error, showing the first's message.

### Visibility, not prevention, is the guarantee against misuse

The blocked-apply error **hands over the key**: it must print the id so
`--break-lock` is usable, and that same id is what `--with-lock` needs. An agent
that reads "another apply is running, lock 9f3a…" has everything required to
bypass.

rdk fundamentally cannot tell a legitimate holder from a hijacker — the id is
the same token either way. So the guarantee is visibility.

**The blocked-apply error warns off the wrong door.** The hint stays one line,
because the text handler indents only a hint's first line, so the holder's
details go in the summary:

```
error: another rdk apply holds this repository (lock 9f3a1c4e7b2d8a05, pid 4127 on builder-3 since 2026-08-02T10:04:11Z): agent refactoring the s3-bucket module
  wait for it; if it is stranded use --break-lock=9f3a1c4e7b2d8a05 — never --with-lock, which is only for the process that took the lock
```

**Running under a lock announces itself**, which is what wording alone cannot
achieve. Without it the two bypasses are backwards in loudness: `--break-lock`
warns, while `--with-lock` would proceed in silence — the more dangerous misuse
being the quieter one.

```
warning: running under lock 9f3a1c4e7b2d8a05, held by pid 4127 on builder-3: agent refactoring the s3-bucket module
rdk apply: wrote 6 files to rdk-managed/
```

A hijacker now leaves a trace naming whose lock it was and what they said they
were doing, and the legitimate holder sees their own message echoed back, which
confirms it is theirs.

rdk deliberately does **not** warn when the lock's host differs from the current
one. A legitimate holder may well apply from another host, so that is a
judgement rdk should not make. Printing the facts and letting the reader see a
mismatch is better than rdk guessing — and it keeps the no-heuristics line
intact.

In JSONL every field is separate, so an agent matching on `code: apply-locked`
never parses prose. That is the case that matters most, since the misuse being
guarded against is an agent's.

### No tree-state policy

`rdk lock` does not check whether the working tree is dirty, because **apply
does not either**. `git` appears only in `internal/initialize`; apply never
shells out to it and reads no prior state, overwriting the managed dir wholesale
by rule 13, which DD-14 exempts explicitly.

Gating a coordination primitive on tree cleanliness would give it a policy apply
itself lacks (rule 7). Edit detection belongs where DD-14 already designs it —
the per-file manifest hash check for outside files — when that lands.

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

Deferred release covers every `return err` path and a panic. Errors before
materialising — a parse failure, a bad definition — never reach the lock.

`SIGINT` and `SIGTERM` do not run defers, and Ctrl-C during a slow apply is not
exotic: it is the ordinary way a developer changes their mind. Without handling
it, breaking a lock becomes routine rather than rare. So the CLI installs a
handler that releases the lock and exits.

That is safe precisely because of the atomic-materialize work: every intermediate
state either self-heals on the next run or fails loudly, so the handler removes
the lock and unwinds nothing.

Exit status follows the shell convention of 128 + signal: 130 for `SIGINT`, 143
for `SIGTERM`, so a script can tell an interruption from a definition error (1)
or an rdk bug (2).

Only `SIGKILL`, a power loss, or an OOM kill now leave a lock behind, and
`--break-lock=<id>` is the stated recovery.

## Stated limits (rule 12)

- Two applies both passing `--with-lock=<same id>` concurrently reintroduce the
  original race. There is one lock file, so rdk cannot serialise the holder
  against itself without nesting state that does not earn its complexity.
  `--with-lock` asserts "I am the coordinator here": rdk serialises everyone
  else, not you against yourself.
- A hard kill strands a lock. The recovery is manual and the message says so.

## Sequencing

Two steps, one file format designed for both, so the second is a command and a
warning rather than a format migration:

1. **The apply lock** — the JSON file, acquire/release in `Materialize`,
   `--break-lock=<id>`, and the signal handler. This is the P1.
2. **Held locks** — `rdk lock -m`, `rdk unlock <id>`, `--with-lock=<id>` and its
   announcement.

## Consequences

`Store` grows lock methods, including an idempotent release so the deferred call
and the signal handler can both use it. The store owns the lock state; `cmd`
owns the signal.

`cmd` gains signal handling, which it has not had before. It stays confined to
releasing the lock and exiting — it must not grow into general cleanup.
