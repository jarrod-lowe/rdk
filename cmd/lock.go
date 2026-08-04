package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/jarrod-lowe/rdk/internal/apply"
	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/spf13/cobra"
)

// lockCmd takes a held lock and exits leaving it, deliberately. It must not
// call a.setStore: that would give the SIGINT/SIGTERM handler a route to
// release the very lock this command exists to leave behind. repofs already
// makes that structural — ReleaseLock only ever touches the transaction
// lock's file, and a held lock is never written under that name — but not
// registering this Store at all is the belt to that braces: the handler has
// no route to any Store here, so there is nothing for a future repofs change
// to have to remember not to reach.
func (a *app) lockCmd() *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Hold this repository against applies until rdk unlock",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A lock nobody can explain is worse than no lock: the person it
			// blocks has nothing to act on, and the blocked-apply error
			// prints this message — that is the whole reason it exists.
			if strings.TrimSpace(message) == "" {
				return diag.New(diag.Diagnostic{
					Code:    diag.CodeInvalidFlag,
					Summary: "rdk lock: -m/--message is required",
					Hint:    `say why, e.g. rdk lock -m "agent refactoring the s3-bucket module"`,
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
			info, err := store.HoldLock(message)
			if err != nil {
				// Something already holds the repository — a held lock or a
				// running apply, it makes no difference. That is the exact
				// condition a blocked apply reports, so this reuses its
				// diagnostic rather than a second, differently-worded one.
				if d, ok := apply.LockedDiagnostic(err); ok {
					return diag.New(d)
				}
				return err
			}
			// The id has to reach stdout as part of the result: the caller is
			// very often a script or an agent that needs to capture it to
			// pass to rdk unlock or --with-lock later. This is also the one
			// place --with-lock guidance belongs at all — only the process
			// that took the lock sees this output, unlike a blocked apply's
			// error, which is read by whoever lost the race and is very often
			// not the holder. The release reminder matters for the same
			// reason a stranded lock is worth guarding against elsewhere: a
			// lock nobody releases blocks every apply after it, and the
			// holder is the only one who can prevent that.
			a.log.Result(diag.Diagnostic{
				Code:    diag.CodeLockHeld,
				Summary: fmt.Sprintf("rdk lock: held %s — %s", info.ID, info.Message),
				Hint: fmt.Sprintf("apply while you hold it: rdk apply --with-lock=%s\nrelease when you are done: rdk unlock %s",
					info.ID, info.ID),
				Attrs: []diag.Attr{diag.Str("lock_id", info.ID)},
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "why the repository is being locked (required)")
	return cmd
}

// unlockCmd releases a held lock. Like lockCmd, it must not call a.setStore:
// unlock's own store never acquires anything the signal handler would need to
// release.
func (a *app) unlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock <id>",
		Short: "Release a lock taken by rdk lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			store, err := repofs.New(wd)
			if err != nil {
				return err
			}
			if err := store.Unlock(id); err != nil {
				// Unlock's own refusals (no lock, a mismatched id, or an
				// apply lock — use --break-lock for that) are the user's
				// mistake, not rdk's: exit 1, not 2. This is lock-mismatch,
				// not apply-locked: nothing is necessarily holding the
				// repository, you named a lock and the repository disagreed
				// about it. Wrap (not New) so the underlying message — which
				// already distinguishes all three cases, including naming
				// --break-lock for the apply-lock one — reaches the user as
				// the cause; the hint just points at it rather than repeating
				// (or worse, guessing wrong at) which case applied. "re-run
				// rdk apply to see the current lock" used to be the hint, but
				// that describes a diagnostic step that will not happen when
				// no lock exists at all — the next apply just succeeds.
				return diag.Wrap(err, diag.Diagnostic{
					Code:    diag.CodeLockMismatch,
					Summary: fmt.Sprintf("cannot unlock %s", id),
					Hint:    "read the cause above: it says whether nothing is locked, the id is wrong, or it's an apply lock (use --break-lock for that)",
				})
			}
			a.log.Result(diag.Diagnostic{
				Code:    diag.CodeUnlocked,
				Summary: fmt.Sprintf("rdk unlock: released %s", id),
				Attrs:   []diag.Attr{diag.Str("lock_id", id)},
			})
			return nil
		},
	}
}
