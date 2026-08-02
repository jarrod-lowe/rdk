package cmd

import (
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
	// Captured from UseLock below so the under-lock notice can ride with the
	// result once apply.Run returns, rather than being announced separately
	// before rdk even knows whether the apply will succeed.
	var lockInfo repofs.LockInfo
	var underLock bool
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
				// Taking someone else's lock is surprising state, and
				// surprising state announces itself (rule 6).
				a.log.Warn(diag.Diagnostic{
					Code:    diag.CodeLockBroken,
					Summary: fmt.Sprintf("broke lock %s held since %s by pid %d on host %s", info.ID, info.Since, info.PID, info.Host),
				})
			}
			res, err := apply.Run(store, version.Version)
			if err != nil {
				return err
			}
			// Warnings print before the summary so the summary lands last, and
			// on stderr so stdout stays the answer a script reads.
			for _, w := range res.Warnings {
				a.log.Warn(w)
			}
			d := res.Diagnostic()
			if underLock {
				d = withLockNotice(d, lockInfo)
			}
			a.log.Result(d)
			return nil
		},
	}
	cmd.Flags().StringVar(&breakLock, "break-lock", "", "remove a stranded lock with this id before applying")
	cmd.Flags().StringVar(&withLock, "with-lock", "", "run this apply under an existing held lock with this id, without acquiring or releasing it")
	return cmd
}

// withLockNotice folds the under-lock announcement into the apply result
// rather than emitting it as a separate warning: a warning can be silenced by
// --log-level independently of the result it qualifies, which is exactly the
// gap that let an apply run under someone else's lock with no output at all.
// Riding with the result means the notice is exactly as visible as the
// success it describes.
func withLockNotice(d diag.Diagnostic, info repofs.LockInfo) diag.Diagnostic {
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
