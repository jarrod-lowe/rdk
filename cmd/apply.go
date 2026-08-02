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
	// Local rather than an app field: --break-lock is specific to this one
	// command, unlike the persistent log/color flags every command shares, so
	// it doesn't belong on app.
	var breakLock string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Regenerate all rdk-managed files from the definitions in rdk/",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			// instead of stranding it.
			a.setStore(store)
			if breakLock != "" {
				info, err := store.BreakLock(breakLock)
				if err != nil {
					// BreakLock's own error (no lock, or an id that doesn't
					// match) is the user's mistake, not rdk's — it must exit 1,
					// not 2. This is lock-mismatch, not apply-locked: nothing is
					// necessarily holding the repository, you named a lock and
					// the repository disagreed about it.
					return diag.Wrap(err, diag.Diagnostic{
						Code:    diag.CodeLockMismatch,
						Summary: fmt.Sprintf("cannot break lock %s", breakLock),
						Hint:    "re-run rdk apply to see the current lock, if any, and its id",
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
			a.log.Result(res.Diagnostic())
			return nil
		},
	}
	cmd.Flags().StringVar(&breakLock, "break-lock", "", "remove a stranded lock with this id before applying")
	return cmd
}
