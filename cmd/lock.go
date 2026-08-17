package cmd

import (
	"errors"
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
				return lockHoldError(err, info)
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

// lockHoldError maps a HoldLock failure to the diagnostic rdk lock reports.
// Pulled out of lockCmd's RunE so the ErrLockNotReleased and ErrLockNotDurable
// branches can be exercised directly from cli_test.go with a crafted error:
// making HoldLock's own release genuinely fail needs .rdk to go unwritable in
// the narrow window between HoldLock creating .rdk/lock and its deferred
// ReleaseLock running, and making its directory-sync genuinely fail needs a
// similarly narrow window right after that — neither is reachable from
// outside internal/repofs. The equivalent failure in Materialize is only
// reachable in repofs's own test suite via its unexported afterPublish seam
// (internal/repofs/store_test.go), and the directory-sync failure only via
// its own afterHeldLockLinked seam; HoldLock exposes neither to this package.
func lockHoldError(err error, info repofs.LockInfo) error {
	if errors.Is(err, repofs.ErrScratchTarget) {
		// HoldLock's very first call is ensureScratchDir, before any lock is
		// touched, so this can never co-occur with the branches below — info
		// is still the zero value here, exactly as apply.Run's identical
		// check finds ManagedDir untouched. Reproduced: `.rdk` present as a
		// regular file made rdk apply report scratch-target at exit 1
		// ("remove .rdk, then re-run") but made rdk lock fall through to this
		// function's bare `return err` at the bottom — exit 2, "an rdk bug.
		// Report it" — for the identical, user-fixable condition, reached
		// through the one call site that had no branch for it.
		//
		// This duplicates apply.Run's construction (internal/apply/apply.go)
		// rather than calling a shared helper the way the LockTargetDiagnostic
		// and LockedDiagnostic branches below do: apply.Run builds this
		// diagnostic inline, not as an exported function, and extracting one
		// would mean editing internal/apply/apply.go, which is out of scope
		// for this change. A shared helper is the better shape; this is the
		// same message kept in sync by hand until one exists.
		return diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeScratchTarget,
			File:    repofs.ScratchDir,
			Summary: "cannot use it as rdk's scratch space",
			Hint:    "remove " + repofs.ScratchDir + ", then re-run",
		})
	}
	if errors.Is(err, repofs.ErrLockNotReleased) {
		// Checked before LockTargetDiagnostic, deliberately, mirroring
		// apply.Run's identical ordering (internal/apply/apply.go): a release
		// that fails because .rdk/apply.lock itself has become unusable — a
		// directory where the lock file was, say — wraps both
		// ErrLockNotReleased and ErrLockTarget, and checking
		// LockTargetDiagnostic first would report "cannot use rdk's lock
		// files" and never say the held lock was already created, which is
		// the one obligation this diagnostic exists to meet.
		//
		// info is trustworthy here specifically because of how HoldLock's
		// named return works: its last statement is
		// `return s.acquireLock(scratchLock, message)`, which assigns both
		// named returns before the deferred ReleaseLock runs — and that
		// defer only ever overwrites err, never info (see HoldLock's own doc
		// comment). So err wrapping ErrLockNotReleased here can only mean
		// HoldLock's own final acquireLock succeeded first: the held lock
		// info describes is genuinely sitting in .rdk/lock right now, and
		// its id is real — it is not invented, and it must reach the user,
		// because a held lock whose id nobody saw still blocks every apply
		// until someone finds it.
		return diag.Wrap(err, diag.Diagnostic{
			Code: diag.CodeLockNotReleased,
			Summary: fmt.Sprintf("rdk lock: held %s — %s, but could not release .rdk/apply.lock",
				info.ID, info.Message),
			// The held lock is already in effect, so the usual --with-lock /
			// unlock guidance still belongs here — but neither works yet:
			// Materialize acquires .rdk/apply.lock on every apply, including
			// one run with --with-lock, so both stay blocked until whatever
			// the cause above names is cleared. rdk lock itself must not be
			// re-run to "fix" this: .rdk/lock already exists, so a second
			// call would only report this repository as locked.
			Hint: fmt.Sprintf("the lock above is real and already in effect; read the cause above for what's blocking %s and clear it — until then every apply blocks on it, including one run with --with-lock. Once it's clear: apply while you hold this lock: rdk apply --with-lock=%s\nrelease when you are done: rdk unlock %s",
				repofs.ScratchDir+"/apply.lock", info.ID, info.ID),
			Attrs: []diag.Attr{diag.Str("lock_id", info.ID)},
		})
	}
	if d, ok := apply.LockTargetDiagnostic(err); ok {
		return diag.Wrap(err, d)
	}
	// Something already holds the repository — a held lock or a running
	// apply, it makes no difference. That is the exact condition a blocked
	// apply reports, so this reuses its diagnostic rather than a second,
	// differently-worded one.
	if d, ok := apply.LockedDiagnostic(err); ok {
		return diag.New(d)
	}
	if errors.Is(err, repofs.ErrLockNotDurable) {
		// Placement here (after ErrLockNotReleased, before the bare
		// fallback) is not load-bearing the way ErrLockNotReleased's is:
		// unlike that one, this sentinel cannot co-occur with any other
		// branch above. HoldLock only ever produces it from the assignment
		// `err = lockNotDurableErrorFor(info, syncErr)` immediately before
		// it returns (internal/repofs/store.go's HoldLock) — and the
		// deferred release that runs after that assignment only overwrites
		// err when err == nil, so a release failure can never also stack
		// ErrLockNotReleased onto this err the way it stacks onto a nil
		// one. Nor can syncErr itself surface as ErrLockTarget or
		// ErrLocked: it comes from opening/syncing .rdk's directory
		// (syncHeldLockDir), a different code path from the lock-file
		// target check those sentinels come from. So this could equally
		// sit first; it sits last among the special cases only to keep the
		// diff next to the fallback it replaces.
		//
		// info is trustworthy here for the same reason it is in the
		// ErrLockNotReleased branch above: HoldLock's named return is
		// assigned once, by its own final acquireLock(scratchLock, ...),
		// and nothing after that point — not the directory sync, not its
		// deferred release — ever reassigns it, only err (see HoldLock's
		// doc comment). So err wrapping ErrLockNotDurable here can only
		// mean that acquireLock already succeeded: the held lock info
		// describes is genuinely sitting in .rdk/lock right now.
		return diag.Wrap(err, diag.Diagnostic{
			Code: diag.CodeLockNotDurable,
			Summary: fmt.Sprintf("rdk lock: held %s — %s, in force now, but its directory entry could not be confirmed durable",
				info.ID, info.Message),
			// No retry belongs here: there is nothing to retry. The lock
			// already exists and works exactly like any other, so the
			// usual --with-lock / unlock guidance is exactly right, not
			// something to hedge on. What's actually at risk is narrow and
			// stated plainly: an ordinary crash, SIGKILL, or process exit
			// does not touch this lock at all; only a power loss or kernel
			// panic landing before the filesystem flushes .rdk's directory
			// entry on its own could make it vanish, and if that happens
			// nothing notifies anyone — the repository would simply read as
			// unlocked while someone still believes it is held.
			Hint: fmt.Sprintf("the lock above is real and already in effect: apply while you hold it: rdk apply --with-lock=%s\nrelease when you are done: rdk unlock %s\nonly a power loss or kernel panic before .rdk's directory entry is flushed could lose it — an ordinary crash, SIGKILL, or process exit will not; if you need to be sure it survived, check whether .rdk/lock still exists",
				info.ID, info.ID),
			Attrs: []diag.Attr{diag.Str("lock_id", info.ID)},
		})
	}
	return err
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
				return unlockError(err, id)
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

// unlockError maps an Unlock failure to the diagnostic rdk unlock reports.
// Pulled out of unlockCmd's RunE, mirroring lockHoldError, so the
// ErrLockNotDurable branch can be exercised directly from cli_test.go with a
// crafted error: making Unlock's own directory sync genuinely fail needs
// .rdk to go unreadable in the narrow window between Remove succeeding and
// syncHeldLockDir running, which internal/repofs's own suite reaches via its
// unexported afterHeldLockRemoved seam (store_test.go) — not reachable from
// this package.
func unlockError(err error, id string) error {
	if d, ok := apply.LockTargetDiagnostic(err); ok {
		return diag.Wrap(err, d)
	}
	if errors.Is(err, repofs.ErrLockNotDurable) {
		// The removal already happened — Unlock only ever reaches this
		// sentinel after its own Remove has already succeeded (see
		// store.go's Unlock) — so the summary must not read like the
		// release failed; it read exactly that way until this branch
		// existed, since unclassified Unlock errors all fell through to the
		// generic lock-mismatch case below, whose summary ("cannot unlock
		// %s") is false here: the unlock happened, only its durability is
		// unconfirmed.
		return diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeLockNotDurable,
			Summary: fmt.Sprintf("rdk unlock: removed %s, but its directory entry's removal could not be confirmed durable", id),
			Hint:    "only a power loss or kernel panic before .rdk's directory entry is flushed could bring it back — an ordinary crash, SIGKILL, or process exit will not; if you need to be sure, check whether " + repofs.ScratchDir + "/lock still exists and remove it again if so",
			Attrs:   []diag.Attr{diag.Str("lock_id", id)},
		})
	}
	// Unlock's own refusals (no lock, a mismatched id, or an apply lock —
	// use --break-lock for that) are the user's mistake, not rdk's: exit 1,
	// not 2. This is lock-mismatch, not apply-locked: nothing is
	// necessarily holding the repository, you named a lock and the
	// repository disagreed about it. Wrap (not New) so the underlying
	// message — which already distinguishes all three cases, including
	// naming --break-lock for the apply-lock one — reaches the user as the
	// cause; the hint just points at it rather than repeating (or worse,
	// guessing wrong at) which case applied. "re-run rdk apply to see the
	// current lock" used to be the hint, but that describes a diagnostic
	// step that will not happen when no lock exists at all — the next apply
	// just succeeds.
	return diag.Wrap(err, diag.Diagnostic{
		Code:    diag.CodeLockMismatch,
		Summary: fmt.Sprintf("cannot unlock %s", id),
		Hint:    "read the cause above: it says whether nothing is locked, the id is wrong, or it's an apply lock (use --break-lock for that)",
	})
}
