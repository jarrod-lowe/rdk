# Error and warning codes

Every diagnostic rdk emits carries a stable `code`. In JSONL output
(`--log-format=jsonl`) it is the `code` field; matching on it is reliable in a
way that matching on message text is not. The codes are defined in
`internal/diag/codes.go`, and a test fails if one is missing from this page.

Codes never change meaning. A code may be retired, but it is never reused for
something else.

Exit status is a property of the run, not of a code: a failure exits 1, an
unanticipated error exits 2, and warnings and results do not affect the exit
status on their own — a run that warns and then fails still exits 1.
`SIGINT` and `SIGTERM` follow the shell convention of 128 + signal instead —
130 and 143 — so a script can tell an interruption apart from either of those.
An interrupted `rdk apply` releases `.rdk/apply.lock` before exiting; only a
`SIGKILL`, a power loss, or an OOM kill leaves it stranded, and
`--break-lock=<id>` is the recovery. `.rdk/lock` is the separate, longer-lived
lock `rdk lock` leaves behind on purpose; see
docs/superpowers/specs/2026-08-02-apply-lock-design.md for why the two are
different files.

The same kind of interruption — a hard kill or power loss, this time between
`Materialize`'s two renames (`rdk-managed` to `.rdk/old`, then `.rdk/new` to
`rdk-managed`) — can leave `rdk-managed/` absent with the last good tree
sitting untouched at `.rdk/old`. rdk detects this before parsing and, if the
run then fails for any reason, appends a note to that failure's own `hint`
saying so — the failure's own `code` is unchanged, since that is still what
tells you why the run actually failed; a JSONL consumer looking for this
specific fact matches on the `prev_tree_path` attr instead. rdk never
restores `.rdk/old` on your behalf: it was built from the definitions as they
stood before, and publishing it would put a tree in place that no current run
would generate. A run whose definitions parse republishes `rdk-managed/` and
clears `.rdk/old` on its own, so nothing needs saying once that happens.

## Failures

| Code | Means | Fix |
|---|---|---|
| `invalid-yaml` | The file is not a YAML mapping — either malformed, or a list or scalar where a single definition was expected. | Read the cause; it names the line. |
| `empty-file` | A `.yaml` file in `rdk/` has no content, or no fields. | Add a definition, or delete the file. |
| `missing-kind` | The definition has no `kind` field. | Add one, e.g. `kind: s3-bucket`. |
| `kind-not-string` | `kind` is present but is not a string. | Quote it or remove the stray type, e.g. `kind: s3-bucket`. |
| `empty-kind` | `kind` is an empty string. | Name a kind, e.g. `kind: s3-bucket`. |
| `unknown-kind` | The named kind is not registered. | Use one of the kinds the message lists. |
| `unknown-field` | A field is not valid for the definition's kind. | Remove it, or use one of the fields the message lists. |
| `missing-field` | A required field is absent. | Add the field the message names. |
| `field-not-string` | A string field holds another YAML type. | Quote the value. |
| `empty-field` | A required string field is blank. | Give it a value. |
| `field-not-identifier` | A field that becomes a Terraform label holds a value that is not a legal Terraform identifier. | Use only letters, digits, underscores and dashes, starting with a letter or underscore. |
| `multi-document` | One file holds several `---`-separated documents. | Split them into one definition per file. |
| `duplicate-name` | Two definitions share a resource name. | Rename one; the message names the other file. |
| `config-cardinality` | The definitions dir does not hold exactly one `kind: config`. | Add the missing one, or remove the extras. |
| `dir-in-defs` | `rdk/` contains a subdirectory. | Move the definitions up into `rdk/`, or move the directory out of `rdk/` if it holds none. |
| `wrong-extension` | A definition uses `.yml`. | Rename it to `.yaml`. |
| `unprocessable-file` | A file in `rdk/` is not a definition. | Move it out, or park it with `.disabled`. |
| `read-defs-dir` | The definitions directory could not be read. | Check it exists and is readable; run `rdk init` if not. |
| `read-file` | A definition file could not be read. | Check its permissions. |
| `git-init` | `git init` failed while initialising the repository. | Read the cause; check git is installed. |
| `git-unusable` | `git init` reported success but git still cannot use the directory — most often "detected dubious ownership". | Read the cause; git names the command to run, then re-run `rdk init`. |
| `write-managed-dir` | The generated tree could not be written or published. | Read the cause; check permissions and free space, then re-run. |
| `publish-failed` | The generated tree was built but could not be moved into place, so `rdk-managed/` is currently absent. | Nothing is lost. Clear the cause, then re-run — a successful apply publishes it. |
| `scratch-not-removed` | The tree was written correctly, but its displaced copy under `.rdk/old` was not cleared — either the removal itself failed, or an unexpected failure re-checking this apply's lock afterward stopped rdk from attempting it. | The generated tree is correct. Clear `.rdk/old` — a locked file is the common cause — then re-run. |
| `lock-not-released` | Two distinct causes share this code, worded differently: `rdk apply` finished — the tree was written and, if there was a previous tree, swept — but `.rdk/apply.lock` could not be removed afterward; or `rdk lock` created its held lock (`.rdk/lock`, id in the message above) but then could not remove the transaction lock it briefly took to do so. Either way the cause above names what's blocking `.rdk/apply.lock`, most often a permission change under `.rdk/` since the command started. | Read the cause and clear whatever is blocking `.rdk/apply.lock`. For `rdk apply`, the generated tree is already correct; re-run once it's clear. For `rdk lock`, the held lock above is already in effect and needs no re-run — use its id with `rdk apply --with-lock=<id>` or `rdk unlock <id>` once `.rdk/apply.lock` is clear. |
| `lock-not-durable` | `rdk lock` created its held lock (`.rdk/lock`, id in the message above) and it is in force right now — but on this platform rdk could not confirm the directory entry pointing at it had reached durable storage before returning (the cause above names why that confirmation itself failed; this code never fires on Windows, where rdk does not attempt the confirmation at all — see docs/superpowers/specs/2026-08-02-apply-lock-design.md). Only a power loss or kernel panic between the lock being linked and the filesystem flushing that directory entry on its own could then lose it, leaving the repository reading as unlocked while you still believe it is held; an ordinary crash, `SIGKILL`, or a normal process exit does not. | Nothing to fix and nothing to retry — the lock already exists and works exactly like any other: use its id with `rdk apply --with-lock=<id>` or `rdk unlock <id>` as usual. If you want to be sure it would survive a crash right now, check whether `.rdk/lock` still exists. |
| `scratch-target` | `.rdk` exists but is not a directory — a symlink, a file, or something else occupies its path, so rdk refuses to use it as scratch space rather than write or delete through it. | Remove `.rdk`, then re-run. |
| `unsafe-path` | A directory component of a path rdk needs to write through is a symlink (or something else that isn't a real directory), so rdk refuses to write through it rather than following it to wherever it points. | Read the cause; it names the offending path. Remove the symlink, then re-run. |
| `seed-not-a-file` | The path a seeded file would occupy exists but is not a file — most often a directory of the same name. | Remove or rename it, then re-run `rdk init`. |
| `seed-failed` | A seeded file could not be created. | Read the cause; check permissions and free space. |
| `interrupted` | `rdk apply` was asked to shut down (`SIGINT`/`SIGTERM`) while acquiring its transaction lock — the very first step `Materialize` takes, before anything in `rdk-managed/` is touched — and lost the race: the signal handler's foreclosure of new locks (see above) won before this apply's own acquisition finished. Reachable because that acquisition writes and fsyncs the lock's own record before it checks whether a shutdown was requested, a real span of I/O time, not only a pathological delay. Almost always invisible in practice — the far more common outcome is the process exiting via the signal handler (128 + signal) before this is ever constructed — but when it is seen, nothing is wrong and nothing needs fixing. | Nothing to fix. Re-run `rdk apply` if you still want it applied. |
| `invalid-flag` | A flag, command, or flag value on the command line was not recognised. | Check the message; run `rdk --help` for the accepted commands and flags. |
| `apply-locked` | Something else holds this repository, and you did not name any lock yourself. Two distinct causes share this code, worded differently: "another rdk apply is running" (`.rdk/apply.lock` — transient, a Materialize is genuinely in flight, from a blocked `rdk apply` or a blocked `rdk lock`) and "this repository is locked" (`.rdk/lock` — held, `rdk lock` is out until someone runs `rdk unlock`). | Wait for it. If it is stranded — the process is gone — `--break-lock=<id>` removes it, naming the id the message gives. If the holder is a held lock rather than a running apply, do not try `--with-lock` unless it's yours. If the message says the lock file carries no readable id, no `--break-lock` can name it: check nothing is running and delete the path the message gives. |
| `lock-mismatch` | You named a lock (`--break-lock=<id>`, or `rdk unlock <id>`), and the repository disagrees: no lock exists, the id doesn't match the one held, or (for `rdk unlock`) the named lock belongs to a running apply rather than to `rdk lock`. An empty id (`--break-lock=`, `--with-lock=`) is rejected as `invalid-flag` rather than treated as absent: it can name no lock, and a break that names nothing would remove whatever it found. | Read the cause; it says which of the three happened. If no lock exists, drop the flag and re-run — there is nothing to break or unlock. If the id is stale, re-run without it to see the current lock and its real id. If `rdk unlock` named an apply lock, use `--break-lock=<id>` instead. |
| `lock-target` | A lock path (`.rdk/lock` or `.rdk/apply.lock`) is occupied by something other than a regular file — the cause above names which path and what is actually there, almost always a symlink. Nothing is holding the repository: there is no id to read, so there is nothing `--break-lock` could name. | Remove the path the cause names, then re-run. |
| `lock-lost` | This apply held `.rdk/apply.lock` and something removed or replaced it before the apply finished — almost always `--break-lock` used on a lock that was live rather than stranded. rdk refuses to guess whether a lock is stale, so that judgement is the user's, and this is what it looks like when it goes the wrong way. | Read the cause: most often nothing was published and the tree is exactly as it was, but this can also fire just after this apply's own tree was published (that tree is correct; only the sweep of `.rdk/old` was skipped, which may have had nothing in it to begin with). Re-run either way. If this recurs, the cause is a `--break-lock` habit rather than a stranded lock: wait for the running apply instead. |
| `output-failed` | The command itself completed, but writing its result to stdout failed partway — most often a full disk or a broken pipe on a redirected stdout. | Check the destination (redirect target, disk space). The command already ran: for one with a side effect (`rdk lock`, for instance), re-running reports that it's already done, and still names the id you need. |

## Warnings

| Code | Means | Fix |
|---|---|---|
| `set-aside` | A file is parked with `.disabled` or `.example`, so nothing is generated for it. | Intentional — rename to `.yaml` to enable it. |
| `editor-artifact` | An editor or merge leftover (`.orig`, `.rej`, `.bak`, `~`) sits in `rdk/`. | Delete it, or move it out of the definitions dir. |
| `machine-file` | A file nobody chose to create (`.DS_Store`, `Thumbs.db`, a vim swap file) or a git housekeeping file (`.gitignore`, `.gitkeep`) sits in `rdk/`. | Delete it, or move it out of the definitions dir. |

## Results

| Code | Means |
|---|---|
| `apply-complete` | `rdk apply` finished; carries `files` and `dir`. Run with `--with-lock=<id>`, it also carries `lock_id`, `lock_kind`, `host`, `pid`, `since`, and `message` — a JSONL consumer detects an under-lock apply by `lock_id`'s presence, and the summary names the holder in the same line as the result: rdk cannot tell the legitimate holder from someone who copied the id out of a blocked apply's error, so this is the only guarantee, and it cannot be silenced by `--log-level` independently of the result it qualifies. Run with `--break-lock=<id>`, it instead carries `broke_lock_id`, `broke_lock_kind`, `broke_host`, `broke_pid`, `broke_since`, and `broke_message`, prefixed so the two facts — ran under a lock, broke one — are never ambiguous on the same record; the summary names what was broken and its provenance the same way. If the apply that follows a broken lock then fails, the same `broke_*` facts and summary suffix are folded into the failure's own diagnostic instead, since breaking already happened and is not undone by what came after. |
| `init-complete` | `rdk init` finished. |
| `version` | `rdk version` output; carries `version`. |
| `lock-held` | `rdk lock` took a held lock; carries `lock_id`. It outlives this process — only `rdk unlock` or `--break-lock` ends it. |
| `unlocked` | `rdk unlock` released a held lock; carries `lock_id`. |

## Internal

| Code | Means | Fix |
|---|---|---|
| `internal` | A failure rdk did not anticipate; it reached the top without being upgraded to a diagnostic. Exits 2. | This is an rdk bug. Report it with the message. |
