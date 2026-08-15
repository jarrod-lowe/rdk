# One apply at a time, per repository

**Status:** implemented. Landed in four steps — see "Sequencing" — the third
of which split one lock file into two (see "Two kinds, two files" and
"Migration"), and the fourth of which closed a P1 in the split's own
`--with-lock` path: adding step 5 to "Where the lock sits in the sequence".

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

### The lock files

Two files, same JSON shape, created exclusively:

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

`message` is set only for `kind: held`. `kind` itself is written by whichever
call created the file and is never consulted to decide what a file *is* —
which file it's in decides that now (see "Two kinds, two files" below). It
survives in the JSON anyway: a human or agent reading either file's raw
content sees a self-describing record without knowing the filename
convention, and it is already the shape JSONL attrs (`lock_kind`) are keyed
on.

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

### Two kinds, two files

| Kind | File | Taken by | Released by |
|---|---|---|---|
| `apply` | `.rdk/apply.lock` | `rdk apply`, for the duration of `Materialize` | defer, and the signal handler |
| `held` | `.rdk/lock` | `rdk lock -m "…"`, which exits leaving it | `rdk unlock <id>`, or `--break-lock` |

They started as one file, and that was a bug. `--with-lock` asserts "I hold
the repository" and is meant to leave the held lock untouched while still
letting Materialize run — but with one file, "leave the held lock untouched"
and "don't take a transaction lock" were the same instruction, because
acquiring anything would have collided with the very lock `--with-lock` was
adopting. So Materialize, running under `--with-lock`, skipped lock
acquisition entirely. Two `rdk apply --with-lock=<same id>` runs then both
observed "I hold this" and both proceeded straight to staging, unserialised —
the original two-runs-collide corruption, reached through the flag meant to
be safe from it. Splitting the storage lets `--with-lock` keep its promise
(the held lock survives, untouched) while still contending for the
transaction lock like every other apply, including another `--with-lock` run
under the same id: one wins, the other is told another apply is running.

ids are unique across both files — `acquireLock` draws its 8 random bytes the
same way regardless of which file it's writing — so `--break-lock=<id>` and
`rdk unlock <id>`'s diagnosis (below) can treat "which file" as an
implementation detail the id resolves on its own, never something the caller
has to know or say.

The consequence to get right, unchanged by the split: **the signal handler
must release only an `apply` lock this process took.** `rdk lock`'s entire
purpose is to leave one behind, so "release on Ctrl-C" must not mean "remove
whatever is there" — an agent's lock evaporating because someone pressed
Ctrl-C in another terminal would be a silent failure of the whole feature.
Before the split this was a check (`ReleaseLock` looked at `kind` before
touching the file); after it, `ReleaseLock` is hardcoded to
`.rdk/apply.lock` and a held lock is never written under that name, so
nothing automatic can reach one — the exclusion is structural, not a rule
every future caller has to remember. The same reasoning drops the equivalent
checks in `UseLock` (a running apply's lock can't appear in `.rdk/lock` to
adopt by accident) and, for the routine case, `Unlock` (anything found in
`.rdk/lock` already is a held lock). `Unlock` still reads `.rdk/apply.lock`,
though — not to act on it, but to diagnose: naming a running apply's id gets
the "use --break-lock" message instead of a bare "no lock is held". Read both
to explain; write to one.

### Every way past a lock names the lock

The user-visible surface is exactly the pre-split one; only the storage
underneath it changed:

```
rdk apply                        .rdk/lock must be absent; takes .rdk/apply.lock, releases on exit, defer and signal
rdk apply --with-lock=<id>       .rdk/lock must match <id> now and again once .rdk/apply.lock is held, left untouched; takes .rdk/apply.lock, releases on exit
rdk apply --break-lock=<id>      removes a stranded lock (either file, by id), warns whose it was, then applies
rdk lock -m "…"                  .rdk/apply.lock must be absent; takes .rdk/lock, prints the id, exits leaving it
rdk unlock <id>                  releases a held lock (.rdk/lock only) — the routine end of your own
```

The id is **required**, not optional, on both flags. The safety property is
compare-and-swap, and it is stronger than "do not break the wrong lock": between
reading an error and typing the recovery, the stranded lock may have been
released and a *live* apply started. A blind `--break-lock` would kill that live
one and reinstate the corruption the lock exists to prevent. Requiring the id
means you cannot get past a lock you never observed.

`--with-lock` and `--break-lock` together are an error; they are contradictory.

`--with-lock` semantics:

- id matches → apply proceeds and **does not release the held lock
  afterwards** (it still takes and releases its own transaction lock, same as
  any other apply). The held lock has to survive; that is the point.
- id does not match → error. Someone else's lock, or yours was broken and
  replaced.
- no lock at all → **also an error**. You asserted you hold a lock and you do
  not, which means it was broken out from under you. Proceeding would hide that.

The match is checked twice, not once: `UseLock` checks it up front (that's
where the messages above are worded from the CLI's point of view), and
`Materialize` checks it again after acquiring `.rdk/apply.lock` — see "Where
the lock sits in the sequence" below. `UseLock` runs before any transaction
lock exists, so its check alone leaves a window the same width as the one
between `HoldLock`'s absence check and its own create (see "Stated limits"):
wide enough for the held lock to be released, or replaced by a different one,
before this run ever contends for `.rdk/apply.lock`. The second check is what
makes "id matches" a fact this run can act on rather than a fact it merely
observed once.

Everyone without the id stays blocked, which is what the holder wanted — and
two runs both holding the id are no longer everyone: they contend for the
transaction lock exactly like two ordinary applies would.

`rdk lock` twice is an error, which falls out of exclusive creation for free: the
second gets the same "already locked" error, showing the first's message.
`rdk lock` while an apply is genuinely running is also an error, for a
different reason: without the check, it would return success while claiming
the repository is held, which is false — a Materialize is mid-flight, not
waiting on `rdk unlock`. The two are told apart in the message: "another rdk
apply is running" (transient — wait) versus "this repository is locked"
(held — wait, or `--with-lock` if it's yours).

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
error: another rdk apply is running (lock 9f3a1c4e7b2d8a05, pid 4127 on builder-3 since 2026-08-02T10:04:11Z)
  wait for it; if it is stranded use --break-lock=9f3a1c4e7b2d8a05
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
2. check and create .rdk/, write .rdk/.gitignore   ← must precede the lock; the dir must exist
3. check .rdk/lock is absent (skipped if this run adopted it via UseLock)
4. ACQUIRE .rdk/apply.lock                         ← defer release
5. on the --with-lock path only: re-check .rdk/lock still carries the adopted id
6. Stat the managed dir (drives two later decisions)
7. clear new (and old, when the managed dir exists)
8. stage into .rdk/new
9. check the managed dir's parent components
10. displace by rename, publish by rename
11. sweep .rdk/old
12. RELEASE .rdk/apply.lock
```

Step 2 sits outside the lock and is safe there: `MkdirAll` is idempotent and the
symlink check is a read. Writing `.gitignore` is deliberately *inside* it,
though — it's remove-then-create, so two concurrent runs would otherwise race
and one would fail `O_EXCL` spuriously.

Step 5 exists because step 3 alone is not enough on the `--with-lock` path.
`UseLock` (step 3's check, when it runs at all) executes in `cmd`, before
`Materialize` is even called — there is no transaction lock yet at that
point, so nothing stops `.rdk/lock` from being released or replaced between
that read and step 4 acquiring `.rdk/apply.lock`. A stale adopter that
skipped straight to staging on the strength of `UseLock`'s answer could
publish while a *different* held lock's owner believed applies were
excluded — the same shape of corruption the rest of this design exists to
prevent, just reached through `--with-lock` instead of two ordinary applies.
Step 5 closes it: once `.rdk/apply.lock` is held, `HoldLock` structurally
cannot create a new `.rdk/lock` (see "Two kinds, two files"), so a re-read
taken right here is a fact that stays true for the rest of the run — the
same reasoning as step 6 below, just one lock earlier. Checking any earlier
(e.g. folding it into step 3) would leave the same window open, only
narrower. If the id no longer matches — the lock was replaced — the error is
`ErrLocked` naming the new holder, identical in shape to any other run
colliding with a held lock. If the lock is simply gone, it's a distinct
plain error: nothing currently holds the repository, so `ErrLocked`'s claim
would not be true.

Step 6 — Stat-ing the managed dir — sits *after* every lock step, not before,
and that ordering is itself load-bearing, not incidental: the answer drives
whether `.rdk/old` needs clearing and whether there is a tree to displace once
staging succeeds, and both are only safe to act on once nothing else can be
changing the managed dir underneath this run. Reading it any earlier is
exactly the bug this design fixes — stat sees the tree, block on the lock,
another run displaces it and dies before publishing, this run then acquires
the lock still believing the tree exists and clears `.rdk/old`, destroying the
only remaining copy. This still holds on the `--with-lock` path, where step 3
is skipped rather than step 4: an adopted lock, once step 5 has confirmed it
still stands, is just as much a lock as one acquired here, so the state it
protects is exactly as settled.

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

- A hard kill strands a lock. The recovery is manual and the message says so.
- **Neither `BreakLock` nor `ReleaseLock` is atomic between checking the id and
  removing the file.** If the observed holder releases in that window and a
  third run acquires a fresh lock, the removal deletes the newcomer's. There is
  no portable atomic compare-and-delete for a file, so closing this would mean
  a second lock guarding the first — turtles down. The window is two syscalls
  wide, against a `--break-lock` window that spans a human reading an error and
  typing a command, so the id check remains worth having even though it is not
  airtight. Accepted, not overlooked. `HoldLock`'s check that `.rdk/apply.lock`
  is absent (see "Two kinds, two files") has the same shape: it is a read of
  one file followed by an exclusive create of another, not one atomic
  operation, so an apply could start in the gap between them. Closing it would
  need the same nonexistent atomic compare-and-create, so it is accepted for
  the same reason — the window is narrow, and the check exists to catch the
  ordinary case, not to be airtight against an adversary.

## Migration

A `.rdk/lock` written by a pre-split binary always has `kind: "apply"` — that
binary never had a second file to put it in. Read by the post-split binary,
it is found at the held lock's path, so it is treated as a held lock
regardless of what its `kind` field says: `rdk apply` reports the repository
locked rather than another apply running, and `--break-lock=<id>` clears it
the same way it always could. The consequence is mild — a slightly misleading
message, not a wrong outcome — and `.rdk/` is gitignored, so a stale file
never travels between checkouts; it is local to whichever repository was
mid-apply when the binary changed underneath it. No migration code; the next
apply or `rdk lock`/`rdk unlock`/`--break-lock` in that repository simply
sees a held lock.

## Sequencing

Four steps. The first two share one file format designed for both, so the
second was a command and a warning rather than a format migration; the third
splits that one file into two without changing the format or the
user-visible surface at all; the fourth closes a gap the split itself opened:

1. **The apply lock** — the JSON file, acquire/release in `Materialize`,
   `--break-lock=<id>`, and the signal handler. This was the original P1.
2. **Held locks** — `rdk lock -m`, `rdk unlock <id>`, `--with-lock=<id>` and its
   announcement.
3. **Split the storage** — `.rdk/lock` (held) and `.rdk/apply.lock`
   (transaction), so `--with-lock` can leave the held lock untouched while
   still contending for the transaction lock like every other apply. This
   closed a second P1: two `--with-lock` runs under the same id used to both
   proceed, reintroducing the original corruption through the flag meant to
   be safe from it — see "Two kinds, two files".
4. **Revalidate the adopted lock under the transaction lock** — a third P1,
   found in review of the split itself: `UseLock` (step 3 of "Where the lock
   sits in the sequence") runs in `cmd`, before any transaction lock exists,
   so between that check and `Materialize` acquiring `.rdk/apply.lock` the
   held lock could be released and replaced by a different one, and the
   stale adopter would publish none the wiser. `Materialize` now re-reads
   `.rdk/lock` immediately after acquiring `.rdk/apply.lock` (step 5) and
   refuses unless it still carries the id `UseLock` verified — see that
   step's explanation above.

## Consequences

`Store` grows lock methods, including an idempotent release so the deferred call
and the signal handler can both use it. The store owns the lock state; `cmd`
owns the signal.

`cmd` gains signal handling, which it has not had before. It stays confined to
releasing the lock and exiting — it must not grow into general cleanup.
