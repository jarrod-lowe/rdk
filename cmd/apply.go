package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/jarrod-lowe/rdk/internal/apply"
	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/jarrod-lowe/rdk/internal/version"
	"github.com/spf13/cobra"
)

func (a *app) applyCmd() *cobra.Command {
	// Local rather than an app field: --break-lock and --with-lock are
	// specific to this one command, unlike the persistent log/color flags
	// every command shares, so they don't belong on app.
	var breakLock string
	var withLock string
	// Captured from UseLock/BreakLock below so each notice can ride with
	// whatever apply.Run produces — a result on success, an error on failure
	// — rather than being announced separately before rdk knows either.
	var lockInfo repofs.LockInfo
	var underLock bool
	var brokenLock repofs.LockInfo
	var brokeLock bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Regenerate all rdk-managed files from the definitions in rdk/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --with-lock says "this lock is mine, run under it"; --break-lock
			// says "this lock is stale, destroy it". Together they contradict
			// each other, so this has to fail before either flag does anything
			// rather than let one silently win.
			if breakLock != "" && withLock != "" {
				return diag.New(diag.Diagnostic{
					Code:    diag.CodeInvalidFlag,
					Summary: "rdk apply: --with-lock and --break-lock are mutually exclusive",
					Hint:    "pick one: --with-lock to run under a lock you hold, or --break-lock to remove one that's stranded",
				})
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			store, err := repofs.New(wd)
			if err != nil {
				return err
			}
			// Gives the signal handler installed in execute a route to this
			// store, so Ctrl-C during Materialize below releases the lock
			// instead of stranding it. Harmless when running under an adopted
			// lock too: ReleaseLock is a no-op unless this process itself
			// acquired an apply lock, which UseLock below deliberately never
			// does.
			a.setStore(store)
			if withLock != "" {
				info, err := store.UseLock(withLock)
				if err != nil {
					// UseLock's own refusals (no lock at all — yours was broken
					// out from under you — or an id that doesn't match) are the
					// user's mistake, not rdk's: exit 1, not 2. This is
					// lock-mismatch, the same code --break-lock and rdk unlock
					// use for "you named a lock and the repository disagreed":
					// nothing is necessarily holding the repository, the id you
					// gave just doesn't check out. Wrap, not New, so the cause
					// above — which already distinguishes the two cases —
					// reaches the user rather than being summarised (or
					// guessed at) a second time.
					return diag.Wrap(err, diag.Diagnostic{
						Code:    diag.CodeLockMismatch,
						Summary: fmt.Sprintf("cannot run under lock %s", withLock),
						Hint:    "read the cause above: if your lock was broken out from under you, it's gone; if the id is wrong, it does not match the lock actually held",
					})
				}
				// This is the safety property, not decoration: rdk cannot tell
				// the legitimate holder from someone who copied the id out of a
				// blocked apply's error, since the token is identical either
				// way. Prevention is impossible, so every run under a lock
				// names whose it is and what they said they were doing — but
				// that notice now rides with the result below instead of
				// firing here as its own warning, so it cannot be silenced by
				// --log-level independently of the outcome it qualifies.
				lockInfo = info
				underLock = true
			}
			if breakLock != "" {
				info, err := store.BreakLock(breakLock)
				if err != nil {
					// BreakLock's own error (no lock, or an id that doesn't
					// match) is the user's mistake, not rdk's — it must exit 1,
					// not 2. This is lock-mismatch, not apply-locked: nothing is
					// necessarily holding the repository, you named a lock and
					// the repository disagreed about it. "re-run rdk apply to
					// see the current lock" used to be the hint here, but that
					// describes a diagnostic step that will not happen when no
					// lock exists at all — the next apply just succeeds. The
					// cause above already says which of the two it was.
					return diag.Wrap(err, diag.Diagnostic{
						Code:    diag.CodeLockMismatch,
						Summary: fmt.Sprintf("cannot break lock %s", breakLock),
						Hint:    "read the cause above: if nothing is locked, drop --break-lock and re-run; if the id is wrong, it does not match the lock actually held",
					})
				}
				// Taking someone else's lock is surprising state and rule 6
				// says surprising state announces itself — but destroying it is
				// also destructive, and the apply that follows is the thing
				// that acts on it, so the announcement rides with whatever
				// apply.Run produces (below) instead of firing here as its own
				// warning that --log-level could silence independently of the
				// outcome it explains.
				brokenLock = info
				brokeLock = true
			}
			res, err := apply.Run(store, version.Version)
			if err != nil {
				if brokeLock {
					// The lock is already gone by the time apply.Run can fail —
					// breaking it was a prerequisite step, not part of Run — so
					// that fact does not get to vanish just because what
					// followed then failed. It rides on the error the same way
					// it would have ridden on the result.
					err = withBrokeLockErr(err, brokenLock)
				}
				return err
			}
			// Warnings print before the summary so the summary lands last, and
			// on stderr so stdout stays the answer a script reads.
			for _, w := range res.Warnings {
				a.log.Warn(w)
			}
			d := res.Diagnostic()
			if brokeLock {
				d = withBrokeLockNotice(d, brokenLock)
			}
			if underLock {
				d = withUnderLockNotice(d, lockInfo)
			}
			a.log.Result(d)
			return nil
		},
	}
	cmd.Flags().StringVar(&breakLock, "break-lock", "", "remove a stranded lock with this id before applying")
	cmd.Flags().StringVar(&withLock, "with-lock", "", "run this apply under an existing held lock with this id, without acquiring or releasing it")
	return cmd
}

// withUnderLockNotice folds the under-lock announcement into the apply result
// rather than emitting it as a separate warning: a warning can be silenced by
// --log-level independently of the result it qualifies, which is exactly the
// gap that let an apply run under someone else's lock with no output at all.
// Riding with the result means the notice is exactly as visible as the
// success it describes. Named "under" (rather than left as the only lock
// notice) once withBrokeLockNotice below needed to be told apart from it —
// the two report different facts and must not collide in JSONL.
func withUnderLockNotice(d diag.Diagnostic, info repofs.LockInfo) diag.Diagnostic {
	d.Summary += fmt.Sprintf(" (under lock %s, held by pid %d", info.ID, info.PID)
	if info.Message != "" {
		d.Summary += ": " + info.Message
	}
	d.Summary += ")"
	d.Attrs = append(d.Attrs,
		diag.Str("lock_id", info.ID),
		diag.Str("lock_kind", info.Kind),
		diag.Str("host", info.Host),
		diag.Int("pid", info.PID),
		diag.Str("since", info.Since),
		diag.Str("message", info.Message),
	)
	return d
}

// withBrokeLockNotice is a sibling of withUnderLockNotice, not a shared
// branchy helper: the two notices carry different fields (broke names the
// host and since; under does not) and would need a mode flag to merge, which
// is worse than two small functions that each read as one fact.
//
// It carries the same information the removed CodeLockBroken warning did
// (host and since — a stale lock's provenance, useful for judging whether
// breaking it was reasonable), prefixed broke_* on the JSONL attrs so a
// consumer can tell "this apply broke a lock" apart from "this apply ran
// under one" on the same apply-complete record; the two are different facts
// even though at most one can be true of any single run (--with-lock and
// --break-lock are mutually exclusive).
func withBrokeLockNotice(d diag.Diagnostic, info repofs.LockInfo) diag.Diagnostic {
	d.Summary += brokeLockSuffix(info)
	d.Attrs = append(d.Attrs, brokeLockAttrs(info)...)
	return d
}

// withBrokeLockErr is withBrokeLockNotice's counterpart for the path where
// apply.Run fails after the lock was already broken: breaking happens before
// Run and is not undone by Run's failure, so the fact has to reach the user
// some way that survives --log-level — and unlike a result, a failure has no
// stdout line to ride on, only the *diag.Error rendered by Fail, which is
// always Error level and therefore never filtered by --log-level (there is
// no level stricter than error). Folding the notice into that diagnostic's
// Summary and Attrs is what makes it visible on the one output a failed
// apply is guaranteed to produce.
func withBrokeLockErr(err error, info repofs.LockInfo) error {
	var d *diag.Error
	if !errors.As(err, &d) {
		// apply.Run is documented to return diagnostics, not bare errors, so
		// this is a defensive fallback, not the expected path — mirroring how
		// Fail itself treats an error it cannot upgrade: render it rather than
		// dropping the one fact this function exists to preserve.
		return fmt.Errorf("%w%s", err, brokeLockSuffix(info))
	}
	upgraded := *d
	upgraded.Summary += brokeLockSuffix(info)
	upgraded.Attrs = append(append([]diag.Attr{}, d.Attrs...), brokeLockAttrs(info)...)
	return &upgraded
}

// brokeLockSuffix and brokeLockAttrs are shared by the result and error
// paths above so the two can never say the broke-lock fact in different
// words depending on whether apply.Run happened to succeed.
func brokeLockSuffix(info repofs.LockInfo) string {
	s := fmt.Sprintf(" (broke lock %s, held by pid %d on %s since %s", info.ID, info.PID, info.Host, info.Since)
	if info.Message != "" {
		s += ": " + info.Message
	}
	return s + ")"
}

func brokeLockAttrs(info repofs.LockInfo) []diag.Attr {
	return []diag.Attr{
		diag.Str("broke_lock_id", info.ID),
		diag.Str("broke_lock_kind", info.Kind),
		diag.Str("broke_host", info.Host),
		diag.Int("broke_pid", info.PID),
		diag.Str("broke_since", info.Since),
		diag.Str("broke_message", info.Message),
	}
}
