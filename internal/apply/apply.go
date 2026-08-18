// Package apply orchestrates one rdk apply: parse definitions, build the file
// set, materialize the managed dir. All filesystem access is via repofs.
package apply

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/generate"
	"github.com/jarrod-lowe/rdk/internal/manifest"
	"github.com/jarrod-lowe/rdk/internal/parse"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

const (
	// DefsDir is the user-owned definitions directory (repo-relative).
	DefsDir = "rdk"
	// ManagedDir is wholly rdk-owned: replaced atomically every apply.
	ManagedDir = "rdk-managed"
)

// Result summarises an apply for the CLI (rule 6: loud).
type Result struct {
	FilesWritten int
	// Warnings names entries in the definitions dir that were ignored. An
	// ignored file is a resource that does not get generated, so the CLI
	// reports these even though the apply succeeded.
	Warnings []diag.Diagnostic
}

// Run performs apply against the repo the store is rooted at. Pure generation:
// definitions in, repo content out — no network, no cloud (rules 1-2).
func Run(store repofs.Store, version string) (res Result, err error) {
	// Checked before parsing, not only once something fails: a hard kill or
	// power loss between Materialize's two renames (see Store.Materialize's
	// doc comment — displace the current tree to .rdk/old, then publish the
	// new one in its place) can leave ManagedDir absent with the last good
	// tree sitting at .rdk/old. If the definitions are also broken — mid-edit
	// when the machine died — the very next step, parse.Dir, fails with an
	// error that only ever talks about a YAML field, and the reader has no
	// way to learn their managed tree is gone and safe rather than gone for
	// good. Detecting it now and carrying the fact through the defer below is
	// what lets whatever failure Run ultimately returns say that too.
	//
	// Detected, not failed on: a run that goes on to succeed republishes
	// ManagedDir and sweeps .rdk/old unconditionally (Materialize's final
	// step, on every path including this one), healing the state completely.
	// Failing here outright would block the very re-run that fixes it.
	hadInterruptedSwap := interruptedSwapPending(store)
	defer func() {
		if err == nil || !hadInterruptedSwap {
			return
		}
		// Re-checked, not trusted to still be true: if this run's own
		// Materialize got as far as publishing before failing on something
		// after that — ErrSweep, ErrLockNotReleased, or the after-publish
		// half of ErrLockLost (see their own doc comments in repofs) —
		// ManagedDir exists again and those diagnostics already say the tree
		// is correct. Claiming it's still absent here would be false.
		if _, statErr := store.ReadDir(ManagedDir); !errors.Is(statErr, fs.ErrNotExist) {
			return
		}
		err = withInterruptedSwapNotice(err)
	}()

	defs, warnings, err := parse.Dir(store, DefsDir)
	if err != nil {
		return Result{}, err
	}

	set, err := generate.Build(defs)
	if err != nil {
		return Result{}, err
	}

	// Fresh manifest: PR-1 has no outside files, so outside_files is always the
	// empty (non-nil) map, serializing as {}. No prev read / reconcile needed
	// until outside files land (DD-14 machinery kept in manifest, unwired).
	m := manifest.Manifest{RdkVersion: version, OutsideFiles: map[string]string{}}
	if err := set.JSON(repofs.Managed("manifest.json"), m); err != nil {
		return Result{}, err
	}

	if err := store.Materialize(ManagedDir, set); err != nil {
		if errors.Is(err, repofs.ErrUnsafePath) {
			// Named for completeness with Store's contract: ManagedDir is a
			// constant single path component today, so this cannot actually
			// fire, but Materialize's signature takes an arbitrary
			// repo-relative managedDir, and a caller that ever nests it hits
			// this instead of a silent write through a symlinked parent.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeUnsafePath,
				File:    ManagedDir,
				Summary: "cannot write it",
				Hint:    "remove the symlink named above, then re-run",
			})
		}
		if errors.Is(err, repofs.ErrScratchTarget) {
			// The generic write-managed-dir hint ("check permissions and free
			// space") would send the reader nowhere useful here: the fix is
			// not a permission or disk problem, it's a specific path that has
			// to be removed. Naming it is the whole point of a separate code.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeScratchTarget,
				File:    repofs.ScratchDir,
				Summary: "cannot use it as rdk's scratch space",
				Hint:    "remove " + repofs.ScratchDir + ", then re-run",
			})
		}
		if errors.Is(err, repofs.ErrSweep) {
			// The tree is already correct here, so the summary leads with that.
			// Swallowing this would only move the problem: the same locked file
			// blocks the next apply's mandatory scratch clear, far from its cause.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code: diag.CodeScratchNotRemoved,
				Summary: fmt.Sprintf("rdk apply: wrote %d files to %s/, but could not remove the displaced copy",
					set.Len(), ManagedDir),
				Hint: "the generated tree is correct; clear " + repofs.ScratchDir + "/old, then re-run",
			})
		}
		if errors.Is(err, repofs.ErrLockNotReleased) {
			// Checked before LockTargetDiagnostic, deliberately: a release
			// that fails because the lock path itself is now unusable (a
			// directory where .rdk/apply.lock was, say) wraps both
			// ErrLockNotReleased and ErrLockTarget — readLockFile's guard is
			// what ReleaseLock's own read failure runs into. Checking
			// LockTargetDiagnostic first would report "cannot use rdk's
			// lock files" and never say the apply itself succeeded, which is
			// the one obligation this whole diagnostic exists to meet (rule
			// 11). Same shape as ErrSweep otherwise, and reaches here for
			// the same reason: Materialize's own body only ever returns this
			// from its deferred release, which can only override a nil
			// result (see Materialize's named return), so the tree really
			// was written — and swept, if there was a previous one — before
			// this fired.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code: diag.CodeLockNotReleased,
				Summary: fmt.Sprintf("rdk apply: wrote %d files to %s/, but could not release its lock",
					set.Len(), ManagedDir),
				Hint: "the generated tree is correct; this apply has already finished — read the cause above for what's blocking " + repofs.ScratchDir + "/apply.lock, clear it, then re-run",
			})
		}
		if d, ok := LockTargetDiagnostic(err); ok {
			return Result{}, diag.Wrap(err, d)
		}
		if errors.Is(err, repofs.ErrLockLost) {
			// Not apply-locked: this run was not refused a lock, it held one
			// and had it taken away mid-flight. Saying "another apply is
			// running" would send the reader looking for a queue to wait in,
			// when what actually happened is that something destroyed this
			// run's claim. The hint does not say "nothing was written": the
			// revalidation this wraps fires at two different points, and at
			// the later one the tree has already been published — the cause
			// below says which, so the hint only needs to point there.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeLockLost,
				Summary: "this apply's lock was broken while it was running",
				Hint:    "read the cause above for what, if anything, was published, then re-run — and check who is using --break-lock on a live lock",
			})
		}
		if errors.Is(err, repofs.ErrLocked) {
			// No Cause here (diag.New, not diag.Wrap): the wrapped error's own
			// text is the same holder details in a different shape, and setting
			// it as Cause would print them twice.
			d, _ := LockedDiagnostic(err)
			return Result{}, diag.New(d)
		}
		if errors.Is(err, repofs.ErrPublish) {
			// rdk-managed/ is absent right now, which is alarming to look at.
			// Both trees are still on disk under the scratch, so the honest
			// reassurance is that nothing is lost — without sending the reader
			// into rdk's own working directory to verify it.
			return Result{}, diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodePublishFailed,
				Summary: fmt.Sprintf("built the tree but could not move it into %s/", ManagedDir),
				Hint:    "nothing is lost; clear the cause above, then re-run to publish it",
			})
		}
		return Result{}, diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeWriteManagedDir,
			Summary: fmt.Sprintf("cannot write %s/", ManagedDir),
			Hint:    "check permissions and free space",
		})
	}
	return Result{FilesWritten: set.Len(), Warnings: warnings}, nil
}

// interruptedSwapPending reports whether ManagedDir is absent while
// repofs.ScratchDir+"/old" holds a tree — the state Store.Materialize's own
// doc comment describes as reachable only by a hard kill or power loss
// between its two renames (the current tree displaced to .rdk/old, then the
// new one published in its place).
//
// A read error other than "not found" on either path is treated as "not this
// state" rather than guessed at: this function only ever adds a fact to a
// failure that already has its own cause (see withInterruptedSwapNotice), so
// understating is the safe direction. In particular a fresh repository —
// nothing at ManagedDir, nothing at .rdk/old — must not trip it: the first
// ReadDir there returns not-exist, which is also what this function itself
// returns for, so both checks report "not this state" and it correctly stays
// silent on the very first apply anyone ever runs.
func interruptedSwapPending(store repofs.Store) bool {
	if _, err := store.ReadDir(ManagedDir); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, err := store.ReadDir(repofs.ScratchDir + "/old")
	return err == nil
}

// interruptedSwapHintSuffix and interruptedSwapAttrs carry the interrupted-
// swap fact appended by withInterruptedSwapNotice. Appended to Hint, not
// Summary: this is what to do about a fact separate from whatever the
// diagnostic's own cause names, not a restatement of what went wrong — the
// existing Summary and Code still say why the run actually failed (rule 11:
// that is the one obligation a report of this fact must not crowd out).
//
// Deliberately does not say "restore .rdk/old" or offer any way to do so:
// that tree was generated from the definitions as they stood before this
// failure, not whatever is in rdk/ now, and rule 1 (the managed tree is a
// pure function of the definitions) makes publishing it exactly the hazard
// the locking work exists to prevent, reached from the recovery side instead
// of the concurrency side. The only correct recovery is fixing the cause
// above and re-running, which republishes it under today's definitions and
// clears .rdk/old on its own.
const interruptedSwapHintSuffix = ". Separately: " + ManagedDir + "/ is currently absent — an earlier apply was interrupted after displacing it to " + repofs.ScratchDir + "/old but before publishing a replacement. That copy is untouched and safe; fix the cause above and re-run, which republishes it under the current definitions and clears " + repofs.ScratchDir + "/old automatically. Do not copy " + repofs.ScratchDir + "/old into " + ManagedDir + "/ by hand — it reflects the definitions as they stood before this failure, not what's in " + DefsDir + "/ now."

func interruptedSwapAttrs() []diag.Attr {
	// prev_tree_path alone is the detection signal for a JSONL consumer —
	// mirroring how apply-complete's own doc comment says a consumer detects
	// an under-lock apply by lock_id's presence rather than a dedicated code
	// (see withInterruptedSwapNotice's doc comment for why this stays a
	// suffix/attrs addition rather than a new diag code).
	return []diag.Attr{diag.Str("prev_tree_path", repofs.ScratchDir+"/old")}
}

// withInterruptedSwapNotice folds the interrupted-swap fact detected at the
// top of Run into whatever diagnostic its failure produced — the same shape
// cmd/apply.go's withLockErr uses for the under-lock and broke-lock notices:
// a fact discovered outside the failure that ultimately fires still has to
// ride on it, since the failure is the only thing the CLI prints, and Run has
// no access to cmd/apply.go's helper (this fact has to be settled before
// apply.Run returns, not after).
//
// No new diag code for this: the code the caller already picked
// (invalid-yaml, missing-field, publish-failed, whatever actually broke this
// run) is what tells the reader why the run failed, and replacing it here
// with one meaning "the previous tree wants reporting" would answer a
// different question than the one they are asking. A JSONL consumer that
// wants this fact specifically can match on prev_tree_path's presence, the
// same way apply-complete's lock_id already works.
func withInterruptedSwapNotice(err error) error {
	var d *diag.Error
	if !errors.As(err, &d) {
		// Not every path through Run upgrades its error to *diag.Error: the
		// manifest write (FileSet.JSON) and a few of generate.Build's own
		// internal-consistency checks (an unknown kind reaching it despite
		// parse.Dir's own rejection, an embedded module's fs.WalkDir/ReadFile
		// failing) return a plain error instead — reachable in principle, not
		// in practice, but not provably impossible either. Mirrors
		// cmd/apply.go's withLockErr, which keeps the same fallback for the
		// same reason rather than assuming it away.
		return fmt.Errorf("%w%s", err, interruptedSwapHintSuffix)
	}
	upgraded := *d
	upgraded.Hint += interruptedSwapHintSuffix
	upgraded.Attrs = append(append([]diag.Attr{}, d.Attrs...), interruptedSwapAttrs()...)
	return &upgraded
}

// LockedDiagnostic builds the apply-locked diagnostic from an error wrapping
// repofs.ErrLocked, naming the holder the way a blocked apply does. `rdk lock`
// failing to acquire because something already holds the repository is the
// identical condition — not a second, differently-worded message — so it
// calls this too rather than growing its own copy. Since the storage split
// (docs/superpowers/specs/2026-08-02-apply-lock-design.md), that call site
// covers a case it could not before: HoldLock now also fails this way when a
// transaction lock is in flight, so `rdk lock` never claims the repository
// held while an apply is genuinely running.
//
// The wording is keyed off info.Kind: a "held" lock is nothing applying at
// all, and calling it an apply would be false; an "apply" lock is
// transient — someone else's Materialize is mid-flight right now — so its
// summary says "is running", not "holds", to avoid the word "held" reading
// as if it were the same durable thing rdk lock leaves behind. Neither
// branch explains how to use --with-lock, even though a held lock's id is
// exactly what it needs — that instruction belongs only in rdk lock's own
// success output (cmd/lock.go), the one place only the holder sees it.
// Whoever is blocked here is very often not the holder, and warning them off
// the wrong door (held case only — an apply lock is never a door
// --with-lock could open) costs a clause; explaining the right one to a
// reader who may not be entitled to it would not be reversible once it ships.
func LockedDiagnostic(err error) (diag.Diagnostic, bool) {
	info, ok := repofs.LockInfoFromError(err)
	if !ok {
		return diag.Diagnostic{}, false
	}

	// The holder's details go in the summary, not the hint, so the hint stays
	// short and single-purpose regardless of how much there is to say about
	// the holder. The Message tail is only present for a kind "held" lock, so
	// it's appended rather than baked into a fixed format — leaving a
	// dangling colon for kind "apply" (no message) would be its own small
	// lie.
	//
	// info.Kind is now the file the lock was read from rather than the field
	// inside it (repofs.readLockFile), so this can no longer describe a held
	// lock as an apply because someone hand-edited a JSON field.
	var summary, hint string
	switch {
	case info.ID == "":
		// No id means no --break-lock: naming the lock is the whole
		// mechanism, and printing "use --break-lock=" would be printing a
		// command that cannot work — which is exactly what it used to do. The
		// path is the only handle left, so the recovery names that instead.
		// Reachable from a lock file truncated by a power loss or written by
		// a binary older than the complete-or-absent creation in repofs.
		summary = fmt.Sprintf("this repository is locked by %s, and the lock file carries no readable id", info.Path)
		hint = fmt.Sprintf("nothing can name that lock, so --break-lock cannot remove it: if no rdk is running, delete %s", info.Path)
	case info.Kind == "held":
		summary = fmt.Sprintf("this repository is locked (lock %s, pid %d on %s since %s)",
			info.ID, info.PID, info.Host, info.Since)
		hint = fmt.Sprintf("wait for it to be unlocked; if it is stranded use --break-lock=%s — do not use --with-lock unless this lock is yours",
			info.ID)
	default:
		summary = fmt.Sprintf("another rdk apply is running (lock %s, pid %d on %s since %s)",
			info.ID, info.PID, info.Host, info.Since)
		hint = fmt.Sprintf("wait for it; if it is stranded use --break-lock=%s", info.ID)
	}
	if info.Message != "" {
		summary += ": " + info.Message
	}

	// lock_kind and lock_path are always known: Kind comes from the file the
	// lock was read from, never from the record's own JSON (see
	// repofs.readLockFile), and Path is a property of where the read
	// happened — neither depends on the record parsing at all. The rest
	// (lock_id, host, pid, since) come from the record itself, so they are
	// only included when info.ID != "": in the no-id branch above nothing
	// was actually read successfully, and emitting them as zero values
	// (`"pid":0`, `"host":""`) would have rdk asserting facts it does not
	// have — worse, an empty lock_id invites exactly the broken
	// --break-lock= a JSONL consumer might build from it.
	attrs := []diag.Attr{diag.Str("lock_kind", info.Kind), diag.Str("lock_path", info.Path)}
	if info.ID != "" {
		attrs = append(attrs,
			diag.Str("lock_id", info.ID),
			diag.Str("host", info.Host),
			diag.Int("pid", info.PID),
			diag.Str("since", info.Since),
		)
	}

	return diag.Diagnostic{
		Code:    diag.CodeApplyLocked,
		Summary: summary,
		Hint:    hint,
		Attrs:   attrs,
	}, true
}

// LockTargetDiagnostic builds the lock-target diagnostic from an error
// wrapping repofs.ErrLockTarget — a lock path occupied by something other
// than a regular file. Three call sites (rdk apply's own Materialize, rdk
// lock, rdk unlock) already special-cased it correctly, each carrying its own
// copy of the same three-line Diagnostic; two more (rdk apply --with-lock and
// --break-lock, via UseLock and BreakLock) had no case for it at all and fell
// through to lock-mismatch, sending the reader hunting for an id to fix when
// the actual fix is a path to remove and there is no lock to name. One shared
// constructor for all five, reused the way LockedDiagnostic already is by rdk
// lock, so the fix is one definition rather than three copies that happened
// to agree and two call sites that silently didn't.
//
// False if err doesn't wrap ErrLockTarget, so a caller can chain it before
// its own fallback handling the same way it already chains a LockedDiagnostic
// check. Unlike LockedDiagnostic, this never bakes the holder's details into
// the summary — lockTargetError carries only a formatted message, no
// structured LockInfo, since nothing is actually holding the repository for
// there to be a holder to describe (see ErrLockTarget's doc comment) — so the
// caller is expected to wrap err as Cause (diag.Wrap, not diag.New) so the
// path lockTargetError names still reaches the reader.
func LockTargetDiagnostic(err error) (diag.Diagnostic, bool) {
	if !errors.Is(err, repofs.ErrLockTarget) {
		return diag.Diagnostic{}, false
	}
	return diag.Diagnostic{
		Code:    diag.CodeLockTarget,
		Summary: "cannot use rdk's lock files",
		Hint:    "remove the path named above, then re-run",
	}, true
}

// Diagnostic renders the loud one-line apply report (rule 6). The counts are
// repeated as attrs so a JSONL consumer does not have to read the sentence.
func (r Result) Diagnostic() diag.Diagnostic {
	return diag.Diagnostic{
		Code:    diag.CodeApplyComplete,
		Summary: fmt.Sprintf("rdk apply: wrote %d files to %s/", r.FilesWritten, ManagedDir),
		Attrs:   []diag.Attr{diag.Int("files", r.FilesWritten), diag.Str("dir", ManagedDir)},
	}
}
