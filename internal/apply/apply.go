// Package apply orchestrates one rdk apply: parse definitions, build the file
// set, materialize the managed dir. All filesystem access is via repofs.
package apply

import (
	"errors"
	"fmt"

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
func Run(store repofs.Store, version string) (Result, error) {
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

	return diag.Diagnostic{
		Code:    diag.CodeApplyLocked,
		Summary: summary,
		Hint:    hint,
		Attrs: []diag.Attr{
			diag.Str("lock_id", info.ID),
			diag.Str("lock_kind", info.Kind),
			diag.Str("host", info.Host),
			diag.Int("pid", info.PID),
			diag.Str("since", info.Since),
			diag.Str("lock_path", info.Path),
		},
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
