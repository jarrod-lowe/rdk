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

// lockCmd takes a held lock and exits leaving it, deliberately — and it does
// register its Store with the signal handler, which an earlier version of
// this comment argued against.
//
// HoldLock transiently acquires .rdk/apply.lock, the transaction lock, for
// the span in which it creates .rdk/lock, the held lock (see HoldLock's own
// doc comment). SIGINT/SIGTERM don't run deferred functions (see
// handleSignals), so a Ctrl-C landing in that span would leave HoldLock's own
// `defer s.ReleaseLock()` never called — and without registration, nothing
// else calls it either, stranding the transaction lock. That is exactly the
// goal-5 violation ("nothing is stranded by an ordinary exit, including
// Ctrl-C") the rest of this series exists to close, reopened by the one
// command that used to hold no transaction lock at all. Registering gives the
// handler a route to release it, which is exactly the span that needs
// releasing on a Ctrl-C.
//
// Registering is safe now for a reason that did not hold when this comment
// argued the opposite: the handler can only ever reach .rdk/apply.lock.
// ReleaseLock is hardcoded to that one file, and acquireLock records
// ownership (lockHeld/lockID) only when it is the file being written — never
// for the held lock (see acquireLock's own doc comment) — so lockHeld/lockID
// can never name .rdk/lock. There is no route from this registration to the
// held lock this command exists to leave behind, by construction, not by a
// check that has to remember to exclude it.
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
			// Registered before HoldLock runs, not after: HoldLock is what
			// acquires the transaction lock this exists to let the handler
			// release, so registering any later would leave exactly the span
			// that needs covering unregistered. See this function's own doc
			// comment for why registering is safe.
			a.setStore(store)
			info, err := store.HoldLock(message)
			if err != nil {
				if d, ok := apply.LockTargetDiagnostic(err); ok {
					return diag.Wrap(err, d)
				}
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

// unlockCmd releases a held lock. Unlike lockCmd, it must not call a.setStore
// — not because registering would be unsafe (see lockCmd's doc comment on
// why it never was), but because there is nothing here to register for:
// Unlock reads both scratchLock and scratchApplyLock (the second only to
// diagnose an id that belongs to a running apply) but removes only
// scratchLock, and never calls acquireLock — so it never touches
// lockHeld/lockID and never acquires the transaction lock ReleaseLock is
// hardcoded to release. A Ctrl-C during this command strands nothing for the
// same reason it always would have had nothing to strand.
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
				if d, ok := apply.LockTargetDiagnostic(err); ok {
					return diag.Wrap(err, d)
				}
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
