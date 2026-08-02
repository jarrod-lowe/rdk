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
// release the very lock this command exists to leave behind, which is
// exactly the failure the kind-aware ReleaseLock in repofs makes structural
// rather than a convention every future command has to remember.
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
			// pass to rdk unlock or --with-lock later.
			a.log.Result(diag.Diagnostic{
				Code:    diag.CodeLockHeld,
				Summary: fmt.Sprintf("rdk lock: held %s — %s", info.ID, info.Message),
				Attrs:   []diag.Attr{diag.Str("lock_id", info.ID)},
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
				// Unlock's own refusals (a mismatched id, or an apply lock —
				// use --break-lock for that) are the user's mistake, not
				// rdk's: exit 1, not 2. apply-locked fits the same way it
				// fits --break-lock's own failure in apply.go: either way the
				// repository's lock state isn't what the caller assumed, and
				// the fix is the same, re-observe it. Wrap (not New) so the
				// underlying message — which already names --break-lock for
				// the apply-lock case — reaches the user as the cause.
				return diag.Wrap(err, diag.Diagnostic{
					Code:    diag.CodeApplyLocked,
					Summary: fmt.Sprintf("cannot unlock %s", id),
					Hint:    "re-run rdk apply to see the current lock, if any, and its id",
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
