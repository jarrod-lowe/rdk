package repofs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScratchDir is rdk's own working directory inside the repo. It carries a
// .gitignore of "*", which hides its entire contents from git — and since git
// tracks files rather than directories, the directory itself disappears from
// `git status` too. That matters because a per-directory .gitignore governs
// only its own directory: nothing rdk owns could ignore a sibling of the
// managed dir, and the root .gitignore is seeded-once user property that apply
// may never rewrite (DD-14, rule 3).
const ScratchDir = ".rdk"

const (
	scratchNew = ScratchDir + "/new"
	scratchOld = ScratchDir + "/old"

	// scratchLock is the held lock — rdk lock / unlock / --break-lock, long-
	// lived. scratchApplyLock is the transaction lock — taken by every
	// Materialize and released by defer or the signal handler. They used to
	// be the same file, and the two kinds shared it by tagging themselves
	// with Kind. That was the bug: --with-lock adopts the held lock without
	// acquiring anything, and because acquisition and holding were the same
	// file, adopting it meant Materialize skipped acquisition entirely — so
	// two `rdk apply --with-lock=<same id>` runs both proceeded unserialised
	// and collided on .rdk/new and .rdk/old, the exact corruption a lock
	// exists to prevent, reached through the flag meant to be safe. Splitting
	// the storage means --with-lock can leave the held lock untouched while
	// still contending with every other applier — including another
	// --with-lock run under the same id — for the transaction lock.
	scratchLock      = ScratchDir + "/lock"
	scratchApplyLock = ScratchDir + "/apply.lock"

	// The two kinds of lock, still recorded in LockInfo.Kind even though
	// which file a lock is found in is now what actually governs behaviour
	// (see the constants above). Kept for display: a human or agent reading
	// either file's raw JSON sees a self-describing record without having to
	// know the filename convention, and JSONL consumers already match on
	// lock_kind. A lock file written by a pre-split binary can disagree with
	// its file now — see the migration note in
	// docs/superpowers/specs/2026-08-02-apply-lock-design.md.
	lockKindApply = "apply"
	lockKindHeld  = "held"
)

// ErrScratchTarget reports that the scratch path exists as something other
// than a real directory. os.Root confines symlinks to the repository but still
// follows them inside it, so a .rdk symlinked to the repo root would send the
// .gitignore write onto the user's own file and the scratch deletes onto their
// directories — apply clobbering user-owned files, which rule 3 forbids.
var ErrScratchTarget = errors.New("scratch path is not a directory")

// scratchTargetError marks a failure to use the scratch dir because something
// other than a directory occupies its path. It carries what's actually there,
// since the caller's summary can't know that.
type scratchTargetError struct{ err error }

func (e *scratchTargetError) Error() string        { return e.err.Error() }
func (e *scratchTargetError) Unwrap() error        { return e.err }
func (e *scratchTargetError) Is(target error) bool { return target == ErrScratchTarget }

// ErrSweep reports that the tree was published but the displaced copy could
// not be removed. It is separated from every other Materialize failure because
// the caller has to lead with the fact that the apply succeeded — anything
// else sends the reader looking for damage that is not there (rule 11).
var ErrSweep = errors.New("displaced copy not removed")

// sweepError marks a failure of the final sweep without contributing to the
// message: the caller's summary already says what could not be removed, so
// repeating it here would render the same complaint twice.
type sweepError struct{ err error }

func (e *sweepError) Error() string        { return e.err.Error() }
func (e *sweepError) Unwrap() error        { return e.err }
func (e *sweepError) Is(target error) bool { return target == ErrSweep }

// ErrPublish reports that the tree could not be moved into place. The managed
// dir is absent when this happens, which is alarming to look at, so the caller
// has to say where the previous tree went and that re-running fixes it.
var ErrPublish = errors.New("tree not published")

// publishError marks a failure of the publishing rename without contributing
// to the message, for the same reason as sweepError.
type publishError struct{ err error }

func (e *publishError) Error() string        { return e.err.Error() }
func (e *publishError) Unwrap() error        { return e.err }
func (e *publishError) Is(target error) bool { return target == ErrPublish }

// ErrSeedTarget reports that the path a seed would occupy exists but is not a
// file rdk can leave alone. Seeding is "create once, then it is the user's" —
// a directory there is neither, and accepting it silently defers the failure
// to a later apply, far from its cause.
var ErrSeedTarget = errors.New("seed target is not a file")

// seedTargetError marks a failure to seed because the target already exists
// as something other than a file or symlink. It carries what's actually
// there, since the caller's summary can't know that.
type seedTargetError struct{ err error }

func (e *seedTargetError) Error() string        { return e.err.Error() }
func (e *seedTargetError) Unwrap() error        { return e.err }
func (e *seedTargetError) Is(target error) bool { return target == ErrSeedTarget }

// ErrUnsafePath reports that a path rdk was about to write through passes
// something that is not a real directory.
var ErrUnsafePath = errors.New("path passes through a symlink")

// unsafePathError marks a failure of checkPathComponents. The offending
// component is folded into the message rather than carried as a separate
// field: it is often a parent of the path the caller knows (Seed's directory
// for a seed target, a managed dir's parent for a rename target), so the
// caller cannot supply it the way it supplies File for ErrSeedTarget or
// ErrScratchTarget.
type unsafePathError struct{ err error }

func (e *unsafePathError) Error() string        { return e.err.Error() }
func (e *unsafePathError) Unwrap() error        { return e.err }
func (e *unsafePathError) Is(target error) bool { return target == ErrUnsafePath }

// ErrLocked reports that something else holds the repository's lock.
var ErrLocked = errors.New("another rdk apply holds this repository")

// lockedError marks a failure to acquire the repository lock. It carries the
// holder's details (whatever could be read), since the caller's summary can't
// know that — and the id it carries is the only way past this error, so it
// has to be in the message rather than dropped. info is carried structurally,
// not just baked into err's text, so a caller (apply.Run) can emit the
// holder's fields as typed attrs instead of parsing the sentence back apart.
type lockedError struct {
	err  error
	info LockInfo
}

func (e *lockedError) Error() string        { return e.err.Error() }
func (e *lockedError) Unwrap() error        { return e.err }
func (e *lockedError) Is(target error) bool { return target == ErrLocked }

// LockInfoFromError extracts the held lock's details from an error wrapping
// ErrLocked. False if err doesn't carry one — a defensive caller-side check,
// since Materialize is the only source of ErrLocked and always attaches it.
func LockInfoFromError(err error) (LockInfo, bool) {
	var le *lockedError
	if errors.As(err, &le) {
		return le.info, true
	}
	return LockInfo{}, false
}

// LockInfo is who holds (or held) the repository lock. It is written for a
// human to read in an error message and is never acted on: rdk must not
// decide a lock is stale because a pid looks dead, since on shared storage
// the pid is not even meaningful. That decision is the user's, and the id is
// how they say which lock they decided about.
//
// Encoded with plain encoding/json rather than FileSet.JSON: FileSet.JSON is
// the single owner of rdk's *generated output* format, where determinism is
// load-bearing (DD-1). The lock is coordination state — gitignored, never
// read by generation, never part of the managed tree — so it doesn't belong
// to that format at all. The same distinction is why the Since timestamp and
// the random ID below don't violate rule 1's ban on clocks and randomness:
// that rule governs generation, and this file never touches it.
type LockInfo struct {
	Held    bool   `json:"-"`
	ID      string `json:"id"`
	Kind    string `json:"kind"` // "apply" or "held" — see the constants above
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`             // RFC3339
	Message string `json:"message,omitempty"` // only for kind "held"

	// Path is the lock file this record was read from, repo-relative. Not
	// serialised: it is a property of where the record was found, not of the
	// record, and writing it would let a copied file lie about its own
	// location. Set by every readLockFile call. It is what
	// apply.LockedDiagnostic names when a lock's id cannot be read: a lock
	// with no id has no --break-lock recovery, and "remove the path" is the
	// one instruction that still works. Carried on the record here, rather
	// than left for that call site to recompute from the file argument it
	// happens to have in scope, because readLockFile is the one place that
	// already knows which file it read; a second computation elsewhere would
	// be rule 12's kind of unbudgeted complexity for a fact this call
	// already has.
	Path string `json:"-"`
}

// describeLock formats a held lock's details for the blocked-apply error.
// Printing the id here is what makes the error actionable at all: it is the
// only argument --break-lock accepts, so the error has to hand it over.
func describeLock(info LockInfo) string {
	msg := fmt.Sprintf("lock %s, pid %d on %s since %s", info.ID, info.PID, info.Host, info.Since)
	if info.Message != "" {
		msg += ": " + info.Message
	}
	return msg
}

// lockedErrorFor wraps a lock this call found blocking it as the same
// *lockedError shape acquireLock's EEXIST branch produces, so every blocked
// path — acquiring a lock that already exists, or rdk lock finding an apply
// in flight — carries the holder's details identically and
// apply.LockedDiagnostic renders them the same regardless of which check
// caught it.
func lockedErrorFor(info LockInfo) error {
	return &lockedError{err: fmt.Errorf("%w (%s)", ErrLocked, describeLock(info)), info: info}
}

// ErrLockTarget reports that a lock path exists as something other than a
// regular file. It is separate from ErrLocked because nothing is actually
// holding the repository: a dangling symlink at .rdk/lock reads as absent to
// ReadFile but is refused by the exclusive create, so the held lock silently
// stops excluding while still being impossible to replace — the same
// "manufactures confidence" failure flock was rejected for. At
// .rdk/apply.lock the pair is worse: applies block on a lock no reader can
// see, so no id is ever printed and --break-lock has nothing to name. Both
// are user-fixable by removing one path, which is why this exits 1 with that
// instruction rather than 2 as an rdk bug.
var ErrLockTarget = errors.New("lock path is not a regular file")

// lockTargetError carries what is actually occupying the path, since the
// caller's summary cannot know that — the same shape as scratchTargetError.
type lockTargetError struct{ err error }

func (e *lockTargetError) Error() string        { return e.err.Error() }
func (e *lockTargetError) Unwrap() error        { return e.err }
func (e *lockTargetError) Is(target error) bool { return target == ErrLockTarget }

// ErrLockLost reports that this run's transaction lock was removed or
// replaced while the run was still going. It is not ErrLocked: this is not a
// run that failed to start, it is a run that had already started under a
// claim someone then destroyed — most often --break-lock used on a live lock,
// which the blocked-apply error itself invites, since rdk refuses to guess
// whether a lock is stranded and the reader has less information than rdk
// does. The only thing rdk can do about that from here is refuse to be the
// second half of the corruption: abort loudly rather than complete a tree
// that will be interleaved with another run's. Almost always that means
// nothing was published — but checkStillLocked's second call runs after this
// run's own publish has already succeeded, so on that path the tree itself is
// fine and what the abort actually skips is the sweep that would otherwise
// clear .rdk/old; see checkStillLocked's doc comment for which is which.
var ErrLockLost = errors.New("this apply's lock was broken while it was running")

// lockLostError names the lock that holds the repository now, when there is
// one: that is the id the reader has to act on, not the dead id this run was
// carrying.
type lockLostError struct{ err error }

func (e *lockLostError) Error() string        { return e.err.Error() }
func (e *lockLostError) Unwrap() error        { return e.err }
func (e *lockLostError) Is(target error) bool { return target == ErrLockLost }

// ErrLockNotReleased reports that the apply finished but its transaction lock
// is still on disk. The tree is correct; the repository is not usable until
// the lock goes, and saying nothing would move the failure to the next
// apply — the same reasoning as ErrSweep, and the same obligation on the
// caller to lead with the fact that the apply itself succeeded.
var ErrLockNotReleased = errors.New("lock not released")

// lockNotReleasedError marks a failed release without contributing to the
// message, for the same reason as sweepError: the caller's summary already
// says what is still there.
type lockNotReleasedError struct{ err error }

func (e *lockNotReleasedError) Error() string        { return e.err.Error() }
func (e *lockNotReleasedError) Unwrap() error        { return e.err }
func (e *lockNotReleasedError) Is(target error) bool { return target == ErrLockNotReleased }

// Store is the injected set of filesystem actions rdk performs. The real
// implementation is rooted at the repo, so no operation can escape it.
type Store interface {
	// Materialize atomically replaces managedDir with the FileSet: the new
	// tree is built in ScratchDir, the current managedDir (if any) is
	// displaced by rename rather than deleted, and the new tree is renamed
	// into place. A rename is all-or-nothing, so no step can leave managedDir
	// half-written. ScratchDir is rdk-owned and git-ignores its own contents,
	// so it never touches the user's root .gitignore — but only when it is
	// actually a directory rdk made; if something else occupies that path
	// (most dangerously a symlink) this refuses via ErrScratchTarget rather
	// than writing and deleting through it, and the scratch .gitignore itself
	// is removed and recreated rather than truncated so a symlink planted
	// there cannot redirect the write either. A symlinked parent directory
	// component of managedDir is refused via ErrUnsafePath for the same
	// reason. Parent dirs are created; files use 0o644, dirs 0o755. managedDir
	// is repo-relative. Materialize checks the held lock is absent before
	// acquiring the transaction lock, unless this Store adopted it via
	// UseLock — in which case that pre-acquire read is skipped (UseLock
	// already made it) but Materialize re-reads the held lock once the
	// transaction lock is acquired and refuses to proceed unless it still
	// carries the id UseLock verified: the gap between UseLock running and
	// the transaction lock existing is otherwise wide enough for the held
	// lock to be released or replaced without this run ever noticing. The
	// transaction lock is acquired for Materialize's duration (after the
	// ScratchDir check, MkdirAll, and .gitignore write) and released before
	// returning, on every path including failure — a stranded lock is not
	// the price of an ordinary error. A lock still found in the way (the
	// pre-acquire check, or a replacement found by the adopted path's
	// re-check) is reported as an error wrapping ErrLocked; an adopted lock
	// found simply gone is reported as a plain error, since nothing is
	// actually holding the repository for ErrLocked to describe.
	Materialize(managedDir string, set *FileSet) error
	// Seed creates a user-owned file once: it never overwrites and never
	// follows a symlink at the target, and it refuses via ErrUnsafePath if any
	// parent directory component of path is a symlink rather than creating
	// through it. A pre-existing file or symlink at the target is a no-op;
	// anything else there (a directory, a device, a socket) is reported via
	// ErrSeedTarget rather than treated as already seeded. A write that fails
	// partway leaves nothing at the target: the content is written and closed
	// at a scratch name first and only linked into place once complete, so a
	// reader of the target never sees a truncated file, only nothing or the
	// whole thing.
	Seed(path string, data []byte) error
	// ReadFile / ReadDir read within the repo root. ReadDir returns entries
	// sorted by name. Paths are repo-relative.
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]Entry, error)
	// ReleaseLock removes the transaction lock, but only if this Store value
	// acquired it — never one left by another process or another call. It
	// only ever touches the transaction lock file: a held lock lives
	// elsewhere now, so nothing automatic (this defer, the signal handler)
	// can reach it by construction, not by a check that has to remember to
	// exclude it. Idempotent, so both a deferred call and a signal handler
	// can call it unconditionally.
	ReleaseLock() error
	// BreakLock removes a lock only if its id matches, checking the held
	// lock and the transaction lock and removing whichever carries the id —
	// ids are unique across both, so the id alone is an unambiguous handle
	// and the caller never has to say which file they mean. The id is
	// required, not optional: the safety property is compare-and-swap, not
	// "remove whatever is there" — between reading a blocked-apply error and
	// typing the recovery, the lock it named may have been released and a
	// live one taken. Erroring when id doesn't match either file, or when
	// neither exists, means a caller can never break a lock it hasn't
	// observed.
	BreakLock(id string) (LockInfo, error)
	// HoldLock takes a lock that outlives this process, so a person or agent
	// can work on the tree without an apply running underneath them. Nothing
	// automatic releases it — see ReleaseLock. Refuses while the transaction
	// lock is held: an apply is then genuinely in flight, and rdk lock
	// returning success while that is true would be exactly the lie the
	// feature exists to prevent. It also briefly takes the transaction lock
	// itself while it creates the held one, and releases it before
	// returning; if that release fails, the returned LockInfo is still the
	// held lock this call genuinely created — the error wraps
	// ErrLockNotReleased, the same sentinel Materialize uses, so the caller
	// is not told the whole call failed when only the cleanup did.
	HoldLock(message string) (LockInfo, error)
	// Unlock ends a held lock, and only ever writes to the held lock file.
	// It names the lock because between reading an id and typing it the lock
	// may have been replaced. It reads the transaction lock too, purely to
	// diagnose: an id that names a running apply gets a message redirecting
	// to --break-lock instead of a generic "no lock" — ending someone's
	// running apply is breaking, not unlocking.
	Unlock(id string) error
	// UseLock runs under an existing held lock without taking or releasing it.
	// It returns the lock's details from the very read that verified id, rather
	// than making the caller re-read the file afterwards — a second read could
	// in principle disagree with the one that just verified the id, and the
	// caller (rdk apply --with-lock) needs those details to announce whose lock
	// it is running under.
	UseLock(id string) (LockInfo, error)
}

// Entry is one directory entry. It carries IsDir rather than the full
// fs.DirEntry because that is the only distinction rdk acts on, and keeping the
// Store's surface small keeps Mem an honest stand-in for the real thing.
type Entry struct {
	Name  string
	IsDir bool
}

type osStore struct {
	root *os.Root

	// lockMu guards lockHeld/lockID and usingLock/usingLockID alike: all four
	// are UseLock/acquireLock/Materialize state read and written from more
	// than one call, not just the transaction-lock bookkeeping the older half
	// of this comment used to describe alone.
	//
	// lockHeld and lockID record whether *this* Store value itself acquired
	// the transaction lock and, if so, its id. ReleaseLock consults both:
	// lockHeld to know whether there is anything of this call's to release,
	// and lockID to avoid removing a lock left by another process — one it
	// did not grant — rather than unconditionally removing scratchApplyLock.
	// UseLock consults lockHeld alone, to refuse adopting a held lock while
	// this store already holds a transaction lock of its own; it has no
	// reason to compare lockID, which never holds a held lock's id (see
	// acquireLock). acquireLock only ever sets them for scratchApplyLock: a
	// held lock is never this store's to release, so recording one here
	// would be at best meaningless and at worst (once HoldLock acquires
	// both) the thing that strands a transaction lock by making its release
	// compare the wrong id.
	lockMu   sync.Mutex
	lockHeld bool
	lockID   string

	// usingLock records that this Store adopted a lock it did not take itself
	// (UseLock), so Materialize runs without acquiring or releasing one.
	// usingLockID is the id UseLock verified, kept so Materialize can
	// re-verify the same fact once the transaction lock is held — see the
	// re-read in Materialize for why the id has to travel this far rather
	// than UseLock's own check being trusted to still hold by then.
	usingLock   bool
	usingLockID string
}

// New opens a Store rooted at repoRoot. All operations are confined to it and
// refuse paths that escape via "..", an absolute path, or an escaping symlink.
func New(repoRoot string) (Store, error) {
	r, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	return &osStore{root: r}, nil
}

// checkPathComponents verifies that every directory component of p is a real
// directory. os.Root confines symlinks to the repository but still follows
// them inside it, so a checkout containing `rdk -> docs` would otherwise send
// a write into a directory the user owns — apply clobbering user content,
// which rule 3 forbids. Absent components are fine: they are about to be
// created.
func (s *osStore) checkPathComponents(p string) error {
	p = path.Clean(p)
	if p == "." {
		return nil
	}
	prefix := ""
	for _, part := range strings.Split(p, "/") {
		if prefix == "" {
			prefix = part
		} else {
			prefix = prefix + "/" + part
		}
		switch info, err := s.root.Lstat(prefix); {
		case err == nil && !info.IsDir():
			return &unsafePathError{err: fmt.Errorf("%s is a %s, not a directory", prefix, modeKind(info.Mode()))}
		case err != nil && !os.IsNotExist(err):
			return err
		}
	}
	return nil
}

func (s *osStore) Materialize(managedDir string, set *FileSet) (err error) {
	// Checked before any work: the store owns security, so it re-checks what
	// FileSet already checked on add rather than trusting the caller passed a
	// set that was never tampered with in between.
	if err := set.checkNoOutsideEntries(); err != nil {
		return err
	}

	// Guards the scratch directory itself, and writes its .gitignore; see
	// ensureScratchDir for why Lstat rather than Stat, why this is not folded
	// into checkPathComponents, and why the .gitignore write lives there
	// rather than here.
	//
	// scratchNew and scratchOld get no matching check: both are RemoveAll'd a
	// few lines down, and RemoveAll unlinks a symlink as itself rather than
	// recursing through it (it only recurses on EISDIR, which a symlink never
	// returns), so a symlink planted at either name is inert — removed, not
	// followed. ScratchDir is different because nothing removes it first;
	// MkdirAll walks straight through it.
	if err := s.ensureScratchDir(); err != nil {
		return err
	}

	// The held lock is checked, then the transaction lock is acquired, both
	// after ScratchDir (and its .gitignore) exist and before anything else is
	// written into it. The transaction lock is released on every path out of
	// this function, including a panic, which is what makes a stranded lock
	// the cost of only a hard kill rather than of an ordinary error.
	//
	// The pre-acquire check is skipped when this Store adopted the held lock
	// via UseLock: that call already verified it against the id given, so
	// repeating the same read here, still before the transaction lock
	// exists, would only repeat a check made under the exact same lack of
	// guarantee — see the re-verification right after acquireLock below for
	// where that gets settled instead. The transaction lock is still
	// acquired on the usingLock path, though: --with-lock asserts "I hold
	// the repository", not "serialise nobody against me", so every apply —
	// including two --with-lock runs under the same id — still contends for
	// it. That is the fix: the two locks used to be one file, so adopting
	// the held lock meant skipping acquisition entirely, and two
	// --with-lock runs under the same id both proceeded unserialised and
	// collided on .rdk/new and .rdk/old.
	s.lockMu.Lock()
	usingLock := s.usingLock
	usingLockID := s.usingLockID
	s.lockMu.Unlock()
	if !usingLock {
		switch held, err := s.readLockFile(scratchLock); {
		case err == nil:
			return lockedErrorFor(held)
		case !os.IsNotExist(err):
			return err
		}
	}
	if _, err := s.acquireLock(scratchApplyLock, ""); err != nil {
		return err
	}
	// The release's error is no longer discarded here. It used to be — a
	// bare `defer s.ReleaseLock()` — which is what let the branch in
	// ReleaseLock that keeps ownership on an unexpected failure write to
	// nobody: an apply that failed only to release its lock still exited 0,
	// and the next apply then blocked on a lock no process held. A failure
	// the run is already returning takes priority — that error is the
	// cause, and a failed release is at most its consequence — but a
	// release that fails on an otherwise-successful run is now the run's
	// result, via the named return.
	//
	// The accepted limit (rule 12): when err is already set — ErrSweep, say
	// — a release failure alongside it is not reported here at all; err ==
	// nil guards the assignment, so the sweep failure wins and the release
	// failure is silently dropped for this run. That is still the right
	// precedence — the reader needs to act on the sweep first, and both
	// messages would not change what to do about either — but the stranded
	// lock it leaves behind is not silent forever, only later than a
	// dedicated message would be: the next acquireLock on this path finds
	// the file still there, still complete, and reports ErrLocked exactly as
	// "another rdk apply is running" even though none is. The existing
	// blocked-apply hint ("if it is stranded use --break-lock=<id>") is what
	// actually resolves it from there, one apply after this one rather than
	// on this one.
	defer func() {
		if relErr := s.ReleaseLock(); relErr != nil && err == nil {
			err = relErr
		}
	}()

	// Same kinship as the managedDir Lstat just below, whose comment explains
	// why that read is deliberately not taken any earlier: UseLock's
	// verification ran before the transaction lock existed, so it could
	// already be stale by the time this runs — the held lock released or
	// replaced in the gap. From here on nothing can replace it: HoldLock
	// refuses to create a new held lock while the transaction lock exists,
	// so this read, taken now, is the one that gets to stay true for the
	// rest of the run. Checking any earlier (e.g. where UseLock itself
	// checked) would leave the same window open, just narrower.
	if usingLock {
		switch held, err := s.readLockFile(scratchLock); {
		case err == nil && held.ID == usingLockID:
			// still the lock this run adopted
		case err == nil:
			return lockedErrorFor(held)
		case os.IsNotExist(err):
			return fmt.Errorf("lock %s is gone: it was released or broken after this apply had already begun running under it", usingLockID)
		default:
			return err
		}
	}

	// Deliberately not read any earlier: the answer drives two decisions below
	// (whether .rdk/old needs clearing, and — the very same result, not a
	// second Stat — whether there is a tree to displace once staging
	// succeeds), and both are only safe to act on once nothing else can be
	// changing managedDir underneath this run. Reading it before the lock was
	// exactly the bug this fixes: stat sees the tree, block on the lock,
	// another run displaces it and dies before publishing, this run then
	// acquires the lock still believing the tree exists and clears .rdk/old —
	// destroying the only remaining copy. Still runs on the usingLock path,
	// where nothing above acquires anything: an adopted lock is just as much a
	// lock as one taken here, so the state it protects is exactly as settled.
	// Its non-ENOENT error is returned rather than treated as "absent": not
	// knowing whether there is a tree to displace is its own failure, and
	// falling through would surface a confusing rename error instead of the
	// real cause.
	//
	// Lstat, not Stat, for the same reason ensureScratchDir judges .rdk by
	// Lstat: Stat follows a symlink, so a dangling rdk-managed would read as
	// absent, skip the displacing rename below, and then the publish rename
	// would fail against the symlink that is still sitting there — the
	// displace step exists precisely to clear that name first. Lstat sees the
	// symlink itself and reports it as present regardless of where (or
	// whether) it resolves.
	//
	// Whether the symlink is dangling or points at a real directory, the
	// answer is the same: treat it as present and displace it. Displacing is
	// a rename, and a rename of a symlink moves the link entry, not whatever
	// it points to — so a live target is never read, written, or deleted
	// through, only the name that pointed at it. That is the correct
	// behaviour, not just the convenient one: a symlink at managedDir's name
	// was never a tree rdk made, so treating "displace" as "move the pointer
	// out of the way" rather than "absorb whatever it points to" is what
	// keeps this from ever acting on a directory that isn't rdk's.
	_, managedStatErr := s.root.Lstat(managedDir)
	managedExists := managedStatErr == nil
	if managedStatErr != nil && !os.IsNotExist(managedStatErr) {
		return managedStatErr
	}

	// .rdk/new is never trusted across runs, so it is cleared unconditionally.
	// This is what covers a hard kill, where the sweep at the end never ran at
	// all — and unlike that sweep, it has to succeed, because the name is
	// needed a few lines down.
	//
	// .rdk/old gets the same treatment only when managedDir exists, i.e. only
	// when the displacing rename below will actually run and needs the name
	// free. When managedDir is absent that rename is skipped, so the name is
	// not needed this run — and clearing it anyway would destroy whatever
	// tree it holds for no gain. That tree is often the only copy left after
	// an earlier run failed at the publish rename (ErrPublish): the message
	// for that failure says the previous tree is safe at .rdk/old and re-
	// running will fix it, and an unconditional clear here would make that
	// promise false the moment the retry also fails.
	if err := s.root.RemoveAll(scratchNew); err != nil {
		return err
	}
	if managedExists {
		if err := s.root.RemoveAll(scratchOld); err != nil {
			return err
		}
	}

	for _, p := range set.sortedPaths() {
		full := path.Join(scratchNew, p)
		if err := s.root.MkdirAll(path.Dir(full), 0o755); err != nil {
			return err
		}
		if err := s.root.WriteFile(full, set.managedBytes(p), 0o644); err != nil {
			return err
		}
	}

	if afterStaging != nil {
		afterStaging()
	}

	// Revalidated here, at the last moment before anything irreversible: the
	// staging above writes only into .rdk/new, which every run discards and
	// rewrites unconditionally, so up to this point losing the lock costs the
	// user's visible tree nothing — that claim is scoped to managedDir on
	// purpose, not to this run's own bookkeeping: a run that has genuinely
	// taken over the lock can still race this run's .rdk/new with its own
	// pre-staging clear, in which case the renames below fail loudly as
	// ErrPublish rather than silently corrupting managedDir. From the
	// displacing rename onward every step is visible in the user's tree, so a
	// run whose claim is gone has to stop rather than interleave its output
	// with whoever holds the repository now. This narrows the corruption
	// window --break-lock opens on a live lock to the displace-and-publish
	// rename pair; it does not close it — see checkStillLocked's doc comment.
	if err := s.checkStillLockedBeforePublish(); err != nil {
		return err
	}

	// Both renames below walk managedDir's parent chain the same way MkdirAll
	// does, so an intermediate symlink would land the tree in whatever
	// directory it points to, not managedDir. Every caller today passes a
	// single top-level name (no parent to walk), so this never fires in
	// practice — but managedDir is a parameter, not a constant, and the point
	// of a shared guard is that the next caller who nests it doesn't have to
	// remember to add this back.
	if err := s.checkPathComponents(path.Dir(managedDir)); err != nil {
		return err
	}

	// Displace by rename, not RemoveAll: a rename is all-or-nothing, so there
	// is no half-deleted tree to be mistaken for a healthy one on the next run.
	// It also works on Windows, where renaming onto an existing directory does
	// not. managedExists is the Stat taken up front, not a fresh one: nothing
	// between there and here can have changed it.
	if managedExists {
		if err := s.root.Rename(managedDir, scratchOld); err != nil {
			return err
		}
	}
	if err := s.root.Rename(scratchNew, managedDir); err != nil {
		// managedDir is absent right now; the caller has to say the previous
		// tree is safe at scratchOld rather than let the reader assume it is
		// gone.
		return &publishError{err: err}
	}

	if afterPublish != nil {
		afterPublish()
	}

	// Checked again before the sweep, which is irreversible in a different
	// direction: .rdk/old is the previous tree, and if this run's lock was
	// broken after publishing, that copy may now be the only thing standing
	// between another run's failed publish and a lost tree. Past this point
	// the tree on disk is correct, so the caller must say so even while
	// reporting a failure.
	if err := s.checkStillLockedAfterPublish(); err != nil {
		return err
	}
	if err := s.root.RemoveAll(scratchOld); err != nil {
		return &sweepError{err: err}
	}
	return nil
}

// writeScratchTemp writes b to a randomly-named temporary file in ScratchDir
// — prefix + "." + 16 hex digits + ".tmp" — and returns its path once the
// write (and, if sync, an fsync) and the close have all succeeded. It is the
// one piece acquireLock, seedNew and publishScratchGitignore share (rule 7):
// each still does its own commit — Link for acquireLock and seedNew, which
// must never overwrite; Rename for publishScratchGitignore, which may — and
// its own handling of a failed commit, because those differ enough (whether
// losing the race is an error at all, what the caller does with what was
// already there) that folding them in here would just move the special-casing
// rather than remove it. On any failure the temp file is removed and the
// error returned, so no caller has to clean one up itself.
//
// Random, not sequential or fixed: concurrent writers of the same kind must
// not choose the same name and stomp each other's in-flight write.
//
// sync is true only for a lock. Flushing before the commit is what makes
// "visible implies complete" survive a power loss and not just a signal:
// without it, the directory entry a Link or Rename creates can outlive the
// bytes it points at, which is the zero-length-lock failure again by a
// slower route. Seed and the scratch .gitignore don't carry that guarantee
// today — a partial seed or a rewritten-from-nothing gitignore surviving a
// crash is not the failure this task addresses — so they pass false and skip
// the fsync's cost.
func (s *osStore) writeScratchTemp(prefix string, b []byte, sync bool) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	tmp := ScratchDir + "/" + prefix + "." + hex.EncodeToString(suffix) + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = s.root.Remove(tmp)
		return "", err
	}
	if sync {
		if err := f.Sync(); err != nil {
			f.Close()
			_ = s.root.Remove(tmp)
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		_ = s.root.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// acquireLock takes a lock by making it appear atomically and complete —
// scratchLock for a held lock, scratchApplyLock for a transaction lock. The
// file is the only input: the kind recorded in the JSON is derived from it
// (see the Kind field's doc comment), so there is no way for a caller to
// write a record that disagrees with where it lives.
//
// The record is written to a scratch name, flushed, closed, and only then
// linked into place (writeScratchTemp, then Link). link() fails with EEXIST
// if anything is already at the destination, so it is exactly as exclusive as
// O_CREAT|O_EXCL — and over NFS it is the more dependable of the two, which
// is the same reason both were chosen over flock: flock over NFS is emulated
// as a whole-file POSIX lock that degrades to purely local (excluding
// nothing) under the local_lock=flock/all mount options, with no error to say
// so. A lock that silently does not lock is worse than no lock, because it
// manufactures confidence — see
// docs/superpowers/specs/2026-08-02-apply-lock-design.md.
//
// Creating the visible name first and writing into it second — what this used
// to do — left the lock observable while empty. That is not a cosmetic
// window: a zero-length lock still blocks every apply, but carries no id, so
// --break-lock has nothing to name and the recovery the blocked-apply error
// prints cannot be typed. A signal, a full disk, or a power loss all landed
// there. Now the visible name only ever comes into existence already
// complete.
//
// Ownership is recorded before the link, not after — and lockMu stays held
// from that recording through the Link call itself, not just across the two
// assignments that record it. Releasing in between (what an earlier version
// of this fix still did) reopened a narrower version of the same window: a
// concurrent ReleaseLock could see lockHeld true, read scratchApplyLock, find
// nothing there yet because the Link had not run, and conclude there was
// nothing of this call's left to remove — withdrawing ownership of a lock
// that was about to exist. This call would then complete the Link and
// return, and a signal handler whose ReleaseLock lands in exactly that gap
// (its read-then-no-op before the Link, its os.Exit after) is the SIGTERM
// scenario this task exists for. Holding lockMu across the Link closes it:
// ReleaseLock cannot run at all until this call has released, and by then
// the file is either linked (present, matching the id ReleaseLock will read)
// or the attempt failed (present as someone else's, or absent only because
// this call never recorded anything for it to find).
//
// What is not closed, stated plainly rather than implied away (rule 12): a
// signal whose handler's ReleaseLock call runs and returns — correctly
// finding nothing yet to release — before this call ever takes lockMu, can
// still be followed by this call completing and the process exiting with no
// second release ever running. Synchronising signal delivery itself against
// an in-flight acquisition would close it, and is out of scope here. What
// this fix buys instead is narrower but real: a strand left this way is now
// always a complete record --break-lock can name, never the zero-length file
// that could not be.
//
// Only the transaction lock is recorded as this store's. ReleaseLock only
// ever targets scratchApplyLock, so a held lock's id in those fields was
// always meaningless; it becomes actively wrong once HoldLock acquires both
// (see HoldLock), because the second acquisition would overwrite the first's
// id and the deferred release would then fail its own id compare and strand
// the transaction lock.
func (s *osStore) acquireLock(file, message string) (LockInfo, error) {
	// No usingLock guard here any more: before the split, adopting the held
	// lock (UseLock) meant Materialize skipped acquiring anything at all, so
	// this function running at all while usingLock was true could only mean
	// a bug. Now every Materialize acquires the transaction lock regardless
	// of whether it also adopted the held lock — that is the fix, so the two
	// --with-lock runs under the same id still contend for it — so this is
	// the ordinary path, not a bug to guard against.
	//
	// Checked before creating, not just when reading: the exclusive create
	// refuses a symlink with EEXIST, which is indistinguishable from a real
	// lock being present — so without this the caller would report "another
	// apply is running" about a lock that does not exist and cannot be named.
	if err := s.lockPathUsable(file); err != nil {
		return LockInfo{}, err
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return LockInfo{}, err
	}
	info := LockInfo{
		ID:      hex.EncodeToString(idBytes),
		Kind:    lockKindFor(file),
		Host:    hostname(),
		PID:     os.Getpid(),
		Since:   time.Now().UTC().Format(time.RFC3339),
		Message: message,
	}
	b, err := json.Marshal(info)
	if err != nil {
		return LockInfo{}, err
	}
	tmp, err := s.writeScratchTemp("lock", b, true)
	if err != nil {
		return LockInfo{}, err
	}

	owned := file == scratchApplyLock
	if owned {
		s.lockMu.Lock()
		s.lockHeld = true
		s.lockID = info.ID
		if afterLockOwnershipRecorded != nil {
			// Fires with lockMu still held — see the seam's own doc comment
			// for why a future callback here has to keep that in mind.
			afterLockOwnershipRecorded()
		}
	}
	linkErr := s.root.Link(tmp, file)
	if owned {
		s.lockMu.Unlock()
	}
	if linkErr != nil {
		if owned {
			s.lockMu.Lock()
			// Only this call's own claim is withdrawn, and only if it is
			// still there to withdraw. ReleaseLock clears lockHeld alone and
			// never touches lockID, so it cannot be what makes this compare
			// fail — lockMu was released just above, though, so a *different*
			// acquireLock call on this same store can have started and
			// already recorded its own claim in that gap. Clearing
			// unconditionally would strand that claim: its own ReleaseLock
			// would then find lockHeld false and believe there was nothing
			// to release.
			if s.lockID == info.ID {
				s.lockHeld = false
				s.lockID = ""
			}
			s.lockMu.Unlock()
		}
		_ = s.root.Remove(tmp)
		if os.IsExist(linkErr) {
			// Read whatever is there for the message; a malformed or
			// unreadable lock must still block (rule 6) rather than let a
			// corrupt file silently disable the exclusion, so parse failures
			// here are swallowed and describeLock is handed whatever did come
			// through, even if that's nothing.
			existing, _ := s.readLockFile(file)
			return LockInfo{}, lockedErrorFor(existing)
		}
		return LockInfo{}, linkErr
	}
	// Best-effort: file is already complete by the time Link returns — Link
	// only adds a second directory entry for the same bytes — so a temp left
	// here by a failed Remove is clutter in the gitignored scratch dir, not a
	// reason to fail an acquisition that succeeded.
	_ = s.root.Remove(tmp)
	return info, nil
}

// readLockFile reads whatever is at file, held lock or transaction lock.
//
// lockPathUsable is checked first because ReadFile follows symlinks and the
// exclusive create does not: without it, a dangling symlink at a lock path
// reads as "no lock" while still blocking every acquisition — an exclusion
// that has silently stopped excluding, which is worse than no lock at all
// (see ErrLockTarget). Anything that is not a regular file is refused for the
// same reason, and named, because the fix is to remove one specific path.
//
// A parse failure is still not reported as an error: the file existing is
// itself the fact that matters (something is blocking), so the caller gets a
// zero-valued LockInfo rather than losing that fact to a JSON error. Held is
// set whenever a regular file was found at all, parseable or not.
//
// Kind and Path are set from the file this read actually came from, after
// unmarshalling, so they override whatever the JSON claimed. Which file a
// lock lives in is what governs behaviour (see the constants at the top of
// this file), so a record whose kind field disagrees — a pre-split binary's
// file, or a hand-edited one — must not be able to make a caller describe it
// as the other thing.
func (s *osStore) readLockFile(file string) (LockInfo, error) {
	// lockPathUsable returns nil for a path that simply doesn't exist, so the
	// not-exist error below still comes from ReadFile, unwrapped: every caller
	// distinguishes "absent" from "unusable" with os.IsNotExist, and wrapping
	// it would make an absent lock read as a failure.
	if err := s.lockPathUsable(file); err != nil {
		return LockInfo{}, err
	}
	b, err := s.root.ReadFile(file)
	if err != nil {
		return LockInfo{}, err
	}
	var info LockInfo
	_ = json.Unmarshal(b, &info) // best effort; see doc comment
	info.Held = true
	info.Path = file
	info.Kind = lockKindFor(file)
	return info, nil
}

// lockPathUsable Lstats file and says only whether the path can be used as a
// lock, without opening or unmarshalling whatever is there: a not-exist is
// fine (nil — there is simply no lock), a real file is fine (nil), and
// anything else — most often a symlink — is refused via lockTargetError,
// naming what is in the way. Kept separate from readLockFile so a caller that
// only needs "is this path usable" (acquireLock's pre-create guard, UseLock's
// check of scratchApplyLock) never has to read or parse a file whose contents
// it has no use for — and, for UseLock specifically, so it structurally
// cannot come away with an id: an Lstat has none to give.
func (s *osStore) lockPathUsable(file string) error {
	st, err := s.root.Lstat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !st.Mode().IsRegular() {
		return &lockTargetError{err: fmt.Errorf("%s is a %s, not a lock file; remove it", file, modeKind(st.Mode()))}
	}
	return nil
}

// lockKindFor names the kind a lock found in file is, regardless of what its
// own JSON says. The file is the fact; the field is a display copy.
func lockKindFor(file string) string {
	if file == scratchLock {
		return lockKindHeld
	}
	return lockKindApply
}

// ReleaseLock removes the transaction lock, but only the one this Store
// value itself acquired, and only ever from scratchApplyLock. A held lock
// exists precisely to outlive the process that took it, so no automatic path
// may remove one — not this defer, not the signal handler that also calls
// here. That used to be enforced by a kind check, back when both locks lived
// in the same file: acquiring or releasing had to ask "is what I'm looking at
// actually mine to touch" because the answer wasn't implied by the name
// alone. Now it is: this function is hardcoded to scratchApplyLock, a name a
// held lock is never written under, so there is no held lock this code could
// reach even if lockHeld/lockID were wrong — the exclusion is structural, not
// a check that has to remember to exclude it. (`rdk lock`'s own Store is
// registered with the signal handler — see cmd/lock.go — because HoldLock
// transiently acquires the transaction lock while it creates the held one,
// and a SIGTERM landing in that span needs this function reachable to avoid
// stranding it. Registering is what the hardcoded file name above makes
// safe: the handler can call this freely because it structurally has no
// route from here to the held lock, not because it never reaches this
// function at all.)
//
// lockHeld/lockID record what acquireLock actually created, but that alone
// isn't enough: between this process taking the lock and this call running,
// someone could have broken it (--break-lock) and a third apply could have
// acquired a fresh one. Deleting on the strength of the boolean alone would
// remove that third run's live lock — the same mistake as a blind
// --break-lock, just from the release side, and it reopens exactly the
// two-applies-at-once corruption this feature exists to prevent. So this
// re-reads the file and removes it only if it still carries the id this call
// took. Gone already, or replaced by a live lock, both mean there is nothing
// of this call's left to remove — releasing nothing is right, and ownership
// is surrendered. A read that fails for any other reason is different, and
// used to be conflated with those two: it is not evidence the lock is gone,
// so ownership is kept and the failure is returned as ErrLockNotReleased —
// clearing it, which this used to do, left the file standing with nothing
// tracking it, so no later call would ever remove it. A Remove that fails is
// the same story from the other side: the file may still be there, and
// surrendering the only record of who is responsible for it would make it
// unreleasable by anything short of --break-lock. There is a TOCTOU window
// between the read and the remove, but it is only the interval of one Remove
// syscall, far narrower than the read-a-stale-error-then-type-a-command
// window --break-lock guards against, and not worth adding machinery for.
//
// lockMu is held across that read-and-remove rather than only across the
// flag check: Materialize's defer and the SIGINT/SIGTERM handler both call
// this on the same Store value, and clearing lockHeld before the file is
// actually gone lets the second caller observe "nothing held", no-op, and
// (in the signal handler's case) os.Exit before the first caller's Remove
// ever runs — stranding the lock via the very handler that exists to
// prevent that. Ownership is surrendered only once the removal is settled:
// either this call actually removed the file, or it found the id already
// gone/replaced, in which case there is nothing of this call's left to
// remove. Holding the mutex across a filesystem call is normally suspect,
// but the only two callers of ReleaseLock are this defer and the signal
// handler, neither of which needs lockMu for anything else while a release
// is in flight, so the cost is at most one goroutine blocking for the
// duration of one Remove — far cheaper than the alternative of a lock that
// can be stranded by its own release path.
//
// Idempotent: safe to call when nothing is held, which covers both the
// deferred call after a failed acquireLock and a second call from a signal
// handler racing the deferred one.
func (s *osStore) ReleaseLock() error {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if !s.lockHeld {
		return nil
	}
	info, err := s.readLockFile(scratchApplyLock)
	switch {
	case err != nil && os.IsNotExist(err):
		// Already gone — nothing of this call's is left to remove, so there
		// is nothing left to track either.
		s.lockHeld = false
		return nil
	case err != nil:
		// A read that failed for any other reason is not evidence the lock is
		// gone. Clearing ownership here — which this used to do — left the
		// file standing with nothing tracking it, so no later call would
		// remove it either.
		return &lockNotReleasedError{err: err}
	case info.ID != s.lockID:
		// Not this call's lock any more: most often replaced by a live one
		// after a --break-lock, but the same mismatch also covers a record
		// that failed to parse (readLockFile's unmarshal is best-effort, so
		// a garbled file — this call's own, corrupted by something other
		// than acquireLock's write-then-link — reads back with ID ""). This
		// call cannot tell those apart from here, and removing either would
		// risk deleting a lock that is not its own, so both are treated the
		// same way: nothing of this call's is safely removable, and
		// ownership is surrendered.
		s.lockHeld = false
		return nil
	}
	if err := s.root.Remove(scratchApplyLock); err != nil && !os.IsNotExist(err) {
		// Ownership is kept: the file is still there, and surrendering the
		// only record of who is responsible for it would make it unreleasable
		// by anything short of --break-lock.
		return &lockNotReleasedError{err: err}
	}
	s.lockHeld = false
	return nil
}

// checkStillLocked verifies this run still holds the transaction lock it
// acquired. Called immediately before each irreversible step, because between
// acquiring and here someone may have run --break-lock on a live lock —
// which the blocked-apply error tells them to do when they judge it stranded,
// a judgement rdk deliberately declines to make for them.
//
// This narrows the corruption window to the displace-and-publish rename
// pair; it does not close it. Closing it would need an atomic
// compare-and-rename, which does not exist, or a staleness heuristic, which
// the design rules out (rule 12: the limit ships with the flexibility). What
// it does buy is that the common case — a person breaking a lock they
// believed dead, seconds or minutes before the victim's renames — stops
// being silent, and a mixed tree becomes a loud error instead.
//
// published tells the message which of the two call sites this is, because
// "nothing was published" stops being true at the second one: it runs after
// the publishing rename has already succeeded. Even then the message must not
// name .rdk/old as if a previous tree were sitting there: the displacing
// rename that would have put one there only runs when managedDir already
// existed (see managedExists in Materialize), so on a first apply into a
// fresh repository .rdk/old was never populated by this run at all. What is
// true in every published case, regardless of managedExists, is only that the
// sweep which would otherwise clear .rdk/old did not run — so that is the
// only claim the message makes.
func (s *osStore) checkStillLocked(published bool) error {
	s.lockMu.Lock()
	held, id := s.lockHeld, s.lockID
	s.lockMu.Unlock()
	if !held {
		// Defensive, not a dead branch: ordinarily Materialize acquires the
		// transaction lock before staging, so this run's own goroutine cannot
		// reach checkStillLocked without holding one. But ReleaseLock also
		// runs from the SIGINT/SIGTERM handler's own goroutine (handleSignals
		// in cmd/root.go), which clears lockHeld before it calls os.Exit —
		// not atomically with the exit itself — so a signal landing at the
		// right instant can flip held to false while this goroutine is still
		// executing Materialize. Narrow, and harmless here since the process
		// is already on its way out, but a real way to arrive, not a
		// cannot-happen one; answering "fine" over it would be the dangerous
		// way to be wrong.
		return &lockLostError{err: errors.New("this apply is not holding a lock")}
	}
	switch info, err := s.readLockFile(scratchApplyLock); {
	case err == nil && info.ID == id:
		return nil
	case err == nil && published:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running, and %s holds the repository now: this apply's tree was already published and is correct, but the sweep of %s was skipped — re-run to clean up anything left there", id, info.ID, scratchOld)}
	case err == nil:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running, and %s holds the repository now: nothing was published, re-run when it is free", id, info.ID)}
	case os.IsNotExist(err) && published:
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running, after its tree was already published (which is correct): the sweep of %s was skipped — re-run to clean up anything left there", id, scratchOld)}
	case os.IsNotExist(err):
		return &lockLostError{err: fmt.Errorf("lock %s was broken while this apply was running: nothing was published, re-run", id)}
	case published:
		// Unexpected and not classified as ErrLockTarget (readLockFile
		// already refuses a non-regular file that way, above this switch's
		// reach) or as one of the two cases handled above, so this is a
		// filesystem failure this code did not anticipate. Reported as
		// ErrSweep — not left bare — because the consequence and the fix are
		// identical to a sweep that genuinely failed: the publish already
		// succeeded, .rdk/old is left exactly where a failed RemoveAll would
		// leave it, and "the tree is correct; clear .rdk/old, then re-run" is
		// the right instruction regardless of which of the two stopped the
		// clearing from happening. Leaving it bare would fall through to
		// apply.Run's generic write-managed-dir fallback, which claims the
		// write itself failed — false once the publish has already
		// succeeded.
		return &sweepError{err: err}
	default:
		// Same unexpected failure, but before publishing: nothing has been
		// written to the user's tree yet, so the generic write-managed-dir
		// fallback this bare error reaches in apply.Run is not making a false
		// claim the way the published case above would be.
		return err
	}
}

// checkStillLockedBeforePublish and checkStillLockedAfterPublish name
// checkStillLocked's two call sites so a reader at either one sees which
// question is being asked without following the bool to its doc comment.
// Both share the one implementation (rule 7) rather than duplicating its
// branching under two names.
func (s *osStore) checkStillLockedBeforePublish() error { return s.checkStillLocked(false) }
func (s *osStore) checkStillLockedAfterPublish() error  { return s.checkStillLocked(true) }

// BreakLock removes a lock only if its id matches, and returns what it
// found. The id is required so this can never be "remove whatever is
// there": between reading a blocked-apply error and typing the recovery, the
// stranded lock it named may have been released and a live one taken, and a
// blind removal would destroy that live one.
//
// It checks the held lock and the transaction lock, in that order, and
// removes whichever carries id: ids are unique across both files, so the id
// alone is an unambiguous handle and --break-lock never has to say (or the
// caller ask) which file it means. This is what keeps --break-lock's
// existing job — clearing a transaction lock stranded by a SIGKILL or a
// power loss, the only way one is ever left behind, since an ordinary exit
// releases it via defer or the signal handler — working unchanged now that
// there are two files to look in, rather than regressing to "only clears a
// held lock" the moment the split landed.
func (s *osStore) BreakLock(id string) (LockInfo, error) {
	// An empty id is not a compare-and-swap, it is "remove whatever is
	// there" — the one thing this function exists not to be. It would also
	// match a truncated lock file, whose id unmarshals to "", turning the
	// safety property inside out precisely in the case where the caller can
	// see least.
	if id == "" {
		return LockInfo{}, errors.New("a lock id is required: --break-lock names the one lock it may remove")
	}
	var found []LockInfo
	for _, file := range []string{scratchLock, scratchApplyLock} {
		info, err := s.readLockFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return LockInfo{}, err
		}
		if info.ID != id {
			found = append(found, info)
			continue
		}
		if err := s.root.Remove(file); err != nil {
			return LockInfo{}, err
		}
		return info, nil
	}
	if len(found) == 0 {
		return LockInfo{}, fmt.Errorf("no lock %q is held", id)
	}
	ids := make([]string, len(found))
	for i, f := range found {
		ids[i] = f.ID
	}
	return LockInfo{}, fmt.Errorf("lock %s does not match the current lock(s): %s", id, strings.Join(ids, ", "))
}

// ensureScratchDir makes .rdk usable, refusing anything there that is not a
// real directory. Judged by Lstat rather than Stat, and before MkdirAll:
// MkdirAll succeeds whenever the path already resolves to a directory,
// including through a symlink, and by then a write into the scratch would be
// aimed wherever that symlink points. Lstat sees the symlink itself, so it
// catches what MkdirAll cannot refuse.
//
// Not folded into checkPathComponents even though the condition is the same
// (Lstat, non-directory): ScratchDir is a single top-level component, so that
// check would Lstat exactly this one entry and nothing more. Kept separate
// because modeKind here can say "device"/"socket"/"named pipe" as well as
// "symlink" or "file", where ErrUnsafePath is worded for the symlink case —
// a bare file at .rdk deserves to be named as a file.
//
// Shared by Materialize and HoldLock: `rdk lock` may well run in a repository
// that has never been applied, so it cannot assume the directory exists.
//
// Also writes the scratch .gitignore, rather than leaving that to Materialize
// alone: `rdk lock` in a repository that has never been applied used to leave
// `.rdk/lock` with nothing ignoring it, so it showed up in `git status` and a
// routine `git add .` could commit it — after which every checkout carries a
// lock nobody can release, since the id belongs to a process that is long
// gone. Whatever creates the scratch directory has to make it invisible to
// git in the same step, not on the first apply that happens to follow.
func (s *osStore) ensureScratchDir() error {
	switch info, err := s.root.Lstat(ScratchDir); {
	case err == nil && !info.IsDir():
		// The path itself isn't repeated here: the caller already has it
		// (ScratchDir, or diag's File field), so this only needs to say what's
		// actually occupying it — same reasoning as seedTargetError.
		return &scratchTargetError{err: fmt.Errorf("it is a %s, not a directory", modeKind(info.Mode()))}
	case err != nil && !os.IsNotExist(err):
		return err
	}
	if err := s.root.MkdirAll(ScratchDir, 0o755); err != nil {
		return err
	}
	return s.publishScratchGitignore()
}

// publishScratchGitignore (re)writes the scratch .gitignore, unconditionally
// (rule 13: always write) — this protects the leaf itself, which the Lstat in
// ensureScratchDir cannot: that only verifies ScratchDir, and .gitignore is
// the name inside it.
//
// It used to remove the file and recreate it with O_EXCL, on the reasoning
// that EEXIST from a racing run's O_EXCL meant the desired state already
// held. That reasoning was wrong: EEXIST only proves another run *created*
// the file, not that it finished writing "*\n" — so a run that lost the
// O_EXCL race could return success while the winner's Write had not
// happened yet, and a reader (or `git status`) landing in that window saw a
// zero-length .gitignore, briefly unignoring the scratch dir's contents.
//
// Writing the full content to an unpredictable temporary name and renaming
// it into place closes that window: rename is atomic, so any reader sees
// either the previous .gitignore or the complete new one, never a partial
// write. Two concurrent calls each write their own temp name — no O_EXCL
// collision between them — and whichever rename lands second simply wins;
// the content is the same constant either way, so it doesn't matter which.
//
// Renaming onto an existing path also preserves the no-follow property the
// old remove-then-O_EXCL existed for: rename() replaces the directory entry
// at the destination without dereferencing a symlink that might be sitting
// there, the same way unlink() does not follow one, unlike open(). Verified
// directly, not assumed — TestMaterializeDoesNotFollowASymlinkedScratchGitignore
// plants a symlink at the .gitignore path pointing at a file the user owns
// and asserts that file is untouched after this runs.
func (s *osStore) publishScratchGitignore() error {
	// writeScratchTemp gets the temp name (random, so two concurrent runs
	// never collide on it — the very failure mode this replaces), the write,
	// and the close; sync is false because a gitignore that reverts to
	// nothing across a crash just gets rewritten by the next call (rule 13),
	// which is cheaper than the fsync on every apply.
	tmp, err := s.writeScratchTemp(".gitignore", []byte("*\n"), false)
	if err != nil {
		return err
	}
	if err := s.root.Rename(tmp, ScratchDir+"/.gitignore"); err != nil {
		_ = s.root.Remove(tmp)
		return err
	}
	return nil
}

// HoldLock takes a lock that outlives this process, so a person or agent can
// work on the tree without an apply running underneath them.
//
// It runs under the transaction lock. The old shape — read .rdk/apply.lock,
// find it absent, create .rdk/lock — was one half of a symmetric race:
// Materialize reads .rdk/lock, finds it absent, and creates .rdk/apply.lock,
// so each checked the other's file before creating its own and interleaved
// runs both succeeded. Reproduced at 3 in 3000 trials: rdk lock returned
// success, the apply published, and .rdk/lock stood — the repository
// reported as held while an apply was running underneath it, which is the
// exact lie this feature exists to prevent, told to the one person who asked
// for exclusivity.
//
// Acquiring the transaction lock first turns two mutually-checking creates
// into one create inside a lock, using the mechanism already here rather than
// a second one (rule 7). The transaction lock is transient — held only for
// the span of the create, released before returning — and the message-
// selection branch below hides it from a second, losing rdk lock call
// whenever the held lock already exists by the time the loser checks. It is
// not hidden unconditionally: if the loser's read of scratchLock lands before
// the winner has created it — a real, verified window, not a hypothetical one
// — the loser sees the winner's transaction lock instead, worded exactly like
// a running apply ("another rdk apply is running ... --break-lock=<id>"), and
// the id that message hands over is the transaction lock's, not the held
// lock's. That message is not false — the winner's transaction lock really is
// there, and following it really would remove it — but it describes rdk's own
// momentary plumbing, not the held lock the winner is about to create.
//
// The substitution itself has a stated limit, separate from the window
// above: when a held lock genuinely exists (readErr == nil), it is reported
// instead of the transaction-lock collision even though both can be real
// blockers at once — an apply can be running alongside a held lock via
// --with-lock, which contends for the transaction lock like any other apply
// (see Materialize) but only verifies the held lock it adopted once, before
// staging, and never again. The caller here hears only about the held lock,
// whose hint says to --break-lock it if stranded; following that while the
// --with-lock apply is genuinely running removes the held lock out from
// under it without that apply ever noticing. Verified directly: it is not
// corruption — the transaction lock the --with-lock apply holds is untouched
// by breaking a different file, so nothing can run concurrently with it —
// but it is not an abort either. Task 3's re-verification (checkStillLocked)
// watches only the transaction lock, not the held lock, so the running apply
// completes and publishes normally, and "the repository is held" quietly
// stops being true while it still is running. A misleading recovery
// instruction that costs a false promise, not a tree, and a real gap this
// task does not close.
//
// The held lock is deliberately not recorded as this store's (see
// acquireLock): if it were, the second acquisition below would overwrite the
// transaction lock's id and the deferred release would then fail its own id
// compare and strand the transaction lock.
//
// This makes "HoldLock cannot create a held lock while an apply genuinely
// holds the transaction lock" true against ordinary interleaving — the case
// the 3-in-3000 measurement above came from, where nothing disturbs either
// side's lock. It is not true unconditionally: --break-lock can remove the
// transaction lock this call is holding (it is an ordinary transaction lock,
// discoverable and breakable exactly like any Materialize's, once someone
// has observed its id — goal 3), and this call does not re-verify it between
// acquiring and creating the held lock the way Materialize re-verifies
// before its own destructive renames (see checkStillLocked). A break
// landing in that window lets a fresh apply's Materialize run genuinely
// concurrently with this call's acquireLock(scratchLock, ...) — the same
// shape of race Task 3 closes for Materialize's own irreversible steps, left
// open here because this task does not add the equivalent revalidation to
// HoldLock. What this task closes is the symmetric race described above,
// not every way to interrupt this call's hold on the transaction lock.
func (s *osStore) HoldLock(message string) (info LockInfo, err error) {
	if err := s.ensureScratchDir(); err != nil {
		return LockInfo{}, err
	}
	if _, err := s.acquireLock(scratchApplyLock, ""); err != nil {
		// A held lock takes precedence in the message when there is one: two
		// rdk locks racing means the loser briefly collides with the winner's
		// transaction lock, and reporting "another rdk apply is running"
		// would be describing rdk's own plumbing back at a user whose actual
		// situation is that someone else holds the repository. When there is
		// no held lock, the collision was a genuine apply and the original
		// error is already right.
		if errors.Is(err, ErrLocked) {
			switch held, readErr := s.readLockFile(scratchLock); {
			case readErr == nil:
				return LockInfo{}, lockedErrorFor(held)
			case !os.IsNotExist(readErr):
				// Not swallowed into the transaction-lock's message: a
				// symlink at .rdk/lock is exactly the state Task 1's
				// ErrLockTarget exists to name, and it is no less reachable
				// here than in the ordinary held-lock case above — falling
				// through to "another rdk apply is running" would hide a
				// user-fixable path behind a message about a lock that, for
				// all this call knows, may not even be real.
				return LockInfo{}, readErr
			}
		}
		return LockInfo{}, err
	}
	// Mirrors Materialize's named-return defer (see its doc comment for the
	// full reasoning): a bare `defer s.ReleaseLock()` here had the identical
	// shape of bug — a release that failed after the held lock was already
	// created left rdk lock reporting success with .rdk/apply.lock still on
	// disk, and every apply or rdk lock after it would then block on a
	// transaction lock nobody holds. cmd/lock.go's lockHoldError special-cases
	// that outcome — checking ErrLockNotReleased before its ErrLockTarget
	// branch, deliberately, since a release that fails because .rdk/apply.lock
	// itself is unusable wraps both sentinels, and reporting the target
	// problem first would bury the fact that the held lock already exists —
	// so this surfaces as a lock-not-released diagnostic at exit 1, not an
	// unclassified error. That ordering depends on info surviving this
	// function's own failure path: the named return is assigned by the final
	// acquireLock above, and this defer only ever overwrites err, never info,
	// so a caller seeing ErrLockNotReleased still has the real id to report.
	defer func() {
		if relErr := s.ReleaseLock(); relErr != nil && err == nil {
			err = relErr
		}
	}()
	if afterApplyLockHeldByHoldLock != nil {
		afterApplyLockHeldByHoldLock()
	}
	return s.acquireLock(scratchLock, message)
}

// Unlock ends a held lock, and only ever writes to scratchLock — never
// scratchApplyLock, which is not this call's to touch. It names the lock for
// the same compare-and-swap reason BreakLock does.
//
// Used to refuse via a kind check once acquireLock's target could be either
// lock: reading a single shared file, "is this actually a held lock" was a
// real question. Now scratchLock is written only by HoldLock, so anything
// found there already is one (barring a stale file from before this split —
// see the migration note in the design doc, where the accepted consequence
// is that Unlock treats it as an ordinary held lock and clears it, same as
// it always could). What still needs checking is the id itself: if it
// doesn't match what's in scratchLock (or nothing is there at all), this
// reads scratchApplyLock too, purely to diagnose — an id naming a running
// apply gets the redirecting message, rather than degrading to "no lock is
// held" just because Unlock looked in the wrong file for it.
func (s *osStore) Unlock(id string) error {
	if id == "" {
		return errors.New("a lock id is required: rdk unlock names the one lock it may release")
	}
	held, heldErr := s.readLockFile(scratchLock)
	if heldErr != nil && !os.IsNotExist(heldErr) {
		return heldErr
	}
	if heldErr == nil && held.ID == id {
		if err := s.root.Remove(scratchLock); err != nil && !os.IsNotExist(err) {
			return err
		}
		// No ownership to clear: lockHeld/lockID track only the
		// transaction lock (see acquireLock), and a held lock's id can
		// never appear there, so there is nothing here this call could be
		// the owner of.
		return nil
	}
	switch applying, err := s.readLockFile(scratchApplyLock); {
	case err == nil && applying.ID == id:
		return fmt.Errorf("lock %s belongs to a running apply, not to you — use --break-lock=%s if it is stranded", id, id)
	case err != nil && !os.IsNotExist(err):
		// Not swallowed with the rest of this diagnostic-only read: an unusable
		// apply-lock path (see ErrLockTarget) is not "no apply is running", it
		// is "rdk cannot tell" — reporting "no lock is held" over that would
		// send the reader looking for a lock to type, when what's actually
		// blocking them is a path to remove.
		return err
	}
	if heldErr != nil {
		return fmt.Errorf("no lock %q is held", id)
	}
	return fmt.Errorf("lock %s does not match %s", held.ID, id)
}

// UseLock runs under an existing held lock without taking or releasing it.
// It only ever reads scratchLock's contents: a mismatched id means someone
// else's lock, and no lock at all means yours was broken out from under
// you — which the caller needs to hear rather than have silently treated as
// permission to proceed. It returns the LockInfo this same read verified, so
// the caller can announce the holder's details without a second read that
// could disagree with this one. It also checks that scratchApplyLock's path
// is usable before any of that — see the lockPathUsable call below — so a
// caller adopting a lock over an unusable transaction-lock path fails here,
// before doing any of the work (parsing definitions, building the tree) that
// Materialize's own guard on the same path would otherwise let run to
// completion only to refuse at the very last step. The diagnostic that
// reaches the top is the same lock-target report either way (both routes
// wrap ErrLockTarget through apply.LockTargetDiagnostic) — what this buys is
// not a different answer, only a cheaper way to arrive at it.
//
// Used to also refuse a matching id whose Kind wasn't "held" — adopting a
// running apply's lock would mean running concurrently with the apply that
// holds it, the corruption this feature exists to prevent, reached through
// the flag meant to be safe. That refusal is now structural rather than a
// check: a running apply's lock lives in scratchApplyLock, whose *contents*
// this function never reads — the lockPathUsable check below is an Lstat,
// which has no id to give — so a running apply's id cannot appear in what
// UseLock finds here to begin with (the same migration edge case as Unlock's
// aside applies, and is equally accepted: a pre-split file's stale id is
// adoptable, but nothing is actually running under it).
func (s *osStore) UseLock(id string) (LockInfo, error) {
	if id == "" {
		return LockInfo{}, errors.New("a lock id is required: --with-lock names the one lock it may run under")
	}
	// Checked even though UseLock's own logic never reads scratchApplyLock's
	// contents: without this, adopting the held lock would succeed over an
	// unusable transaction-lock path, and the caller would only discover it
	// after doing the rest of an apply's work, when Materialize's own guard
	// (see acquireLock) reaches the same path and refuses. Both routes report
	// the identical lock-target diagnostic (see apply.LockTargetDiagnostic,
	// called from every site that can reach either one) — checking here is
	// about failing before that work runs, not about saying something
	// different.
	if err := s.lockPathUsable(scratchApplyLock); err != nil {
		return LockInfo{}, err
	}
	info, err := s.readLockFile(scratchLock)
	if err != nil {
		if os.IsNotExist(err) {
			return LockInfo{}, fmt.Errorf("no lock is held: %s was broken out from under you", id)
		}
		return LockInfo{}, err
	}
	if info.ID != id {
		return LockInfo{}, fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	// lockHeld alone, not also compared against id: lockID, when set, is
	// always a transaction lock's id (see acquireLock), never a held lock's,
	// so it and id — always a held lock's id here — can never legitimately
	// be equal. The comparison this used to make could only ever be true,
	// which made it a check on the wrong question; the real one is simply
	// whether this store already holds a transaction lock of its own.
	if s.lockHeld {
		return LockInfo{}, errors.New("this store already holds a lock and cannot also run under one")
	}
	s.usingLock = true
	s.usingLockID = id
	return info, nil
}

// hostname reports the current host for a lock's Host field, falling back to
// "unknown" rather than failing acquireLock over what is only a display
// value.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func (s *osStore) Seed(name string, data []byte) error {
	if dir := path.Dir(name); dir != "." {
		// Checked before MkdirAll for the same reason as Materialize's scratch
		// check: MkdirAll succeeds through a symlinked parent (e.g. `rdk ->
		// docs`), and by then the create below would already be aimed at
		// whatever the symlink points to. Link's no-follow-at-the-leaf
		// behaviour (see seedNew) protects the leaf; this protects everything
		// above it.
		if err := s.checkPathComponents(dir); err != nil {
			return err
		}
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Common case first, and cheap: on a repo that's already seeded — by far
	// the most frequent way Seed is called, since every `rdk init` re-run
	// takes this path — there is nothing to write, so this returns without
	// ever touching the scratch dir. Lstat, not Stat: a symlink must be
	// judged as itself, not as whatever it points to, so that leaving it
	// alone (DD-3) doesn't depend on where it happens to resolve.
	if info, err := s.root.Lstat(name); err == nil {
		return seedExisting(info)
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.seedNew(name, data)
}

// seedExisting turns what's already occupying a seed target into Seed's
// result: a file or symlink is a no-op (DD-3), anything else is
// ErrSeedTarget. The path itself isn't repeated in the error: the caller
// already has it (Store.Seed's argument, or diag's File field), so this only
// needs to say what's actually occupying it.
func seedExisting(info fs.FileInfo) error {
	if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil // already present (file or symlink) — leave it (DD-3)
	}
	return &seedTargetError{err: fmt.Errorf("it is a %s", modeKind(info.Mode()))}
}

// seedNew writes data where Seed's own Lstat just found nothing. This is
// where the old implementation's bug lived: it created name itself with
// O_CREATE|O_EXCL and wrote straight into it, deferring Close and discarding
// its error. A write that failed partway — a full disk, or an error only
// Close reports — left a truncated regular file sitting at name. The next
// Seed call's O_EXCL then hit EEXIST, judged the truncated file "already
// there" (it looked exactly like a legitimately seeded one), and returned
// success — so `rdk init` reported the repository ready over a config it had
// only half written, and the eventual failure surfaced later, far from its
// cause.
//
// The fix writes the full content to a scratch name first — Write and Close
// both checked now, not deferred and dropped — and only once that has fully
// succeeded links it into place. Link, unlike the rename
// publishScratchGitignore uses for the same "write complete-or-nothing"
// shape, never overwrites an existing name: it fails with EEXIST if anything
// is already there. That's exactly the difference Seed needs: a bare rename
// onto the target would silently destroy a config that was legitimately the
// user's, which is the one thing "create once, then it is the user's" can
// never do. So the visible name comes into existence only once, and only
// complete — a half-written temp file never reaches it, and if something else
// wins the race to create name first, this defers to whatever it left (via
// the same seedExisting check the fast path above uses) rather than erroring.
//
// The scratch name lives in ScratchDir, not beside the target: the target's
// own directory is rdk/, which parse.classify scans and errors on anything it
// doesn't recognise, so debris left behind by a failed cleanup there would
// break the very next apply. ScratchDir is already rdk's own working space
// and is gitignored, so litter left there by, e.g., a Remove that itself
// fails is wasted space, not a correctness problem — see the comment on the
// final Remove below.
func (s *osStore) seedNew(name string, data []byte) error {
	if err := s.ensureScratchDir(); err != nil {
		return err
	}
	// writeScratchTemp gets the temp name, the write, and the close; sync is
	// false, same as publishScratchGitignore's: the guarantee this function
	// needs is "never publish a partial write" (see the doc comment above),
	// which write-then-link already gives without an fsync — durability
	// across a power loss is not a promise Seed makes.
	tmp, err := s.writeScratchTemp("seed", data, false)
	if err != nil {
		return err
	}
	if err := s.root.Link(tmp, name); err != nil {
		_ = s.root.Remove(tmp)
		if os.IsExist(err) {
			// Lost the create-once race: something else — a concurrent Seed,
			// almost certainly — put a name there between this call's Lstat
			// and this Link. Whatever it left is exactly as valid a seed as
			// this call's own data would have been, so this defers to it
			// rather than erroring.
			info, statErr := s.root.Lstat(name)
			if statErr != nil {
				return statErr
			}
			return seedExisting(info)
		}
		return err
	}
	// Best-effort: name is already complete and correct by the time Link
	// returns — Link only adds a second directory entry for the same bytes,
	// it doesn't move them — so a temp file stranded here by a failed Remove
	// is clutter in the gitignored scratch dir, not a reason to tell the
	// caller the seed itself failed.
	_ = s.root.Remove(tmp)
	return nil
}

// modeKind names what occupies a seed target, for a message that says what's
// actually in the way instead of just that it isn't a file.
func modeKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode&fs.ModeDevice != 0:
		return "device"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeIrregular != 0:
		return "irregular file"
	default:
		return "non-regular file"
	}
}

func (s *osStore) ReadFile(name string) ([]byte, error) {
	return s.root.ReadFile(name)
}

func (s *osStore) ReadDir(dir string) ([]Entry, error) {
	entries, err := fs.ReadDir(s.root.FS(), dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Name: e.Name(), IsDir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
