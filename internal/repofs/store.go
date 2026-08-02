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
	scratchNew  = ScratchDir + "/new"
	scratchOld  = ScratchDir + "/old"
	scratchLock = ScratchDir + "/lock"

	// The two kinds of lock. An apply lock lives only as long as the
	// Materialize that took it; a held lock is taken by `rdk lock` and
	// deliberately outlives its process, which is why ReleaseLock has to tell
	// them apart rather than removing whatever it finds.
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
	Kind    string `json:"kind"` // "apply" now; "held" arrives with rdk lock
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`             // RFC3339
	Message string `json:"message,omitempty"` // only for kind "held"
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
	// is repo-relative. Materialize acquires the repository lock for its
	// duration (after the ScratchDir check and MkdirAll, before the
	// .gitignore write) and releases it before returning, on every path
	// including failure — a stranded lock is not the price of an ordinary
	// error. If something else already holds it, Materialize returns an error
	// wrapping ErrLocked without touching the tree.
	Materialize(managedDir string, set *FileSet) error
	// Seed creates a user-owned file once: it never overwrites and never
	// follows a symlink at the target, and it refuses via ErrUnsafePath if any
	// parent directory component of path is a symlink rather than creating
	// through it. A pre-existing file or symlink at the target is a no-op;
	// anything else there (a directory, a device, a socket) is reported via
	// ErrSeedTarget rather than treated as already seeded.
	Seed(path string, data []byte) error
	// ReadFile / ReadDir read within the repo root. ReadDir returns entries
	// sorted by name. Paths are repo-relative.
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]Entry, error)
	// ReleaseLock removes the repository lock, but only if this Store value
	// acquired it — never a lock left by another process or another call.
	// Idempotent, so both a deferred call and (in a later step) a signal
	// handler can call it unconditionally.
	ReleaseLock() error
	// BreakLock removes the repository lock only if its id matches, and
	// returns what it found. The id is required, not optional: the safety
	// property is compare-and-swap, not "remove whatever is there" — between
	// reading a blocked-apply error and typing the recovery, the lock it named
	// may have been released and a live one taken. Erroring when id doesn't
	// match, or when no lock exists at all, means a caller can never break a
	// lock it hasn't observed.
	BreakLock(id string) (LockInfo, error)
	// HoldLock takes a lock that outlives this process, so a person or agent
	// can work on the tree without an apply running underneath them. Nothing
	// automatic releases it — see ReleaseLock.
	HoldLock(message string) (LockInfo, error)
	// Unlock ends a held lock. It names the lock because between reading an id
	// and typing it the lock may have been replaced, and it refuses an apply
	// lock: ending someone's running apply is breaking, not unlocking.
	Unlock(id string) error
	// UseLock runs under an existing held lock without taking or releasing it.
	UseLock(id string) error
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

	// lockMu guards lockHeld and lockID, which record whether *this* Store
	// value currently holds the repository lock and, if so, its id.
	// ReleaseLock consults them rather than unconditionally removing
	// scratchLock: a lock left by another process (or, once held locks land,
	// deliberately outliving this one) must survive a release it did not
	// grant.
	lockMu   sync.Mutex
	lockHeld bool
	lockID   string
	lockKind string

	// usingLock records that this Store adopted a lock it did not take itself
	// (UseLock), so Materialize runs without acquiring or releasing one.
	usingLock bool
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

func (s *osStore) Materialize(managedDir string, set *FileSet) error {
	// Checked before any work: the store owns security, so it re-checks what
	// FileSet already checked on add rather than trusting the caller passed a
	// set that was never tampered with in between.
	if err := set.checkNoOutsideEntries(); err != nil {
		return err
	}

	// Taken once, up front, because the answer drives two independent
	// decisions below: whether .rdk/old needs to be cleared before staging
	// even starts, and — using the very same result, not a second Stat —
	// whether there is a tree to displace once staging succeeds. Its
	// non-ENOENT error is returned rather than treated as "absent": not
	// knowing whether there is a tree to displace is its own failure, and
	// falling through would surface a confusing rename error instead of the
	// real cause.
	_, managedStatErr := s.root.Stat(managedDir)
	managedExists := managedStatErr == nil
	if managedStatErr != nil && !os.IsNotExist(managedStatErr) {
		return managedStatErr
	}

	// Guards the scratch directory itself; see ensureScratchDir for why Lstat
	// rather than Stat, and why this is not folded into checkPathComponents.
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

	// Acquired here — after ScratchDir exists, before anything is written into
	// it — because the .gitignore write just below is remove-then-create, and
	// two concurrent runs racing into that O_EXCL would otherwise fail
	// spuriously instead of one of them simply waiting on the lock. Released on
	// every path out of this function, including a panic, which is what makes
	// a stranded lock the cost of only a hard kill rather than of an ordinary
	// error.
	// Skipped entirely when this Store adopted someone else's lock via
	// UseLock: the file already belongs to that lock's holder, so acquiring
	// would fail against it, and releasing it on the way out is exactly what
	// --with-lock promises not to do.
	s.lockMu.Lock()
	usingLock := s.usingLock
	s.lockMu.Unlock()
	if !usingLock {
		if _, err := s.acquireLock(lockKindApply, ""); err != nil {
			return err
		}
		defer s.ReleaseLock()
	}

	// Remove-then-create rather than truncate: RemoveAll unlinks a symlink as
	// itself, and O_EXCL then cannot follow one, so a planted link cannot aim
	// this write at a file the user owns. The file is rdk's, so removing it is
	// ours to do (rule 13: always write). This protects the leaf itself, which
	// checkPathComponents cannot: ScratchDir is already known to be a real
	// directory by this point, but .gitignore is the name inside it, and a
	// component check only ever verifies directories, not the file being
	// written.
	gitignore := ScratchDir + "/.gitignore"
	if err := s.root.RemoveAll(gitignore); err != nil {
		return err
	}
	f, err := s.root.OpenFile(gitignore, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("*\n")); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
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

	// Past this point the tree on disk is correct, so the caller must say so
	// even while reporting this failure.
	if err := s.root.RemoveAll(scratchOld); err != nil {
		return &sweepError{err: err}
	}
	return nil
}

// acquireLock takes the repository lock by creating scratchLock exclusively.
// O_CREAT|O_EXCL is atomic on NFSv3 and later and depends on no mount option
// or lock daemon, unlike flock: flock over NFS is emulated as a whole-file
// POSIX lock that degrades to purely local (excluding nothing) under the
// local_lock=flock/all mount options, with no error to say so. A lock that
// silently does not lock is worse than no lock, because it manufactures
// confidence — see docs/superpowers/specs/2026-08-02-apply-lock-design.md.
func (s *osStore) acquireLock(kind, message string) (LockInfo, error) {
	s.lockMu.Lock()
	usingLock := s.usingLock
	s.lockMu.Unlock()
	if usingLock {
		// Already running under a lock adopted via UseLock; acquiring another
		// would mean holding two under one Store value, and ReleaseLock only
		// ever tracks one id.
		return LockInfo{}, errors.New("this store is already running under an adopted lock and cannot also acquire one")
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return LockInfo{}, err
	}
	info := LockInfo{
		ID:      hex.EncodeToString(idBytes),
		Kind:    kind,
		Host:    hostname(),
		PID:     os.Getpid(),
		Since:   time.Now().UTC().Format(time.RFC3339),
		Message: message,
	}
	b, err := json.Marshal(info)
	if err != nil {
		return LockInfo{}, err
	}
	f, err := s.root.OpenFile(scratchLock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Read whatever is there for the message; a malformed or
			// unreadable lock must still block (rule 6) rather than let a
			// corrupt file silently disable the exclusion, so parse failures
			// here are swallowed and describeLock is handed whatever did come
			// through, even if that's nothing.
			existing, _ := s.readLock()
			return LockInfo{}, &lockedError{err: fmt.Errorf("%w (%s)", ErrLocked, describeLock(existing)), info: existing}
		}
		return LockInfo{}, err
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		return LockInfo{}, err
	}
	if err := f.Close(); err != nil {
		return LockInfo{}, err
	}
	s.lockMu.Lock()
	s.lockHeld = true
	s.lockID = info.ID
	s.lockKind = kind
	s.lockMu.Unlock()
	return info, nil
}

// readLock reads whatever lock file is present. A parse failure is not
// reported as an error: the file existing is itself the fact that matters
// (something is blocking), so the caller gets a zero-valued LockInfo rather
// than losing that fact to a JSON error. Held is set whenever a file was
// found at all, parseable or not.
func (s *osStore) readLock() (LockInfo, error) {
	b, err := s.root.ReadFile(scratchLock)
	if err != nil {
		return LockInfo{}, err
	}
	var info LockInfo
	_ = json.Unmarshal(b, &info) // best effort; see doc comment
	info.Held = true
	return info, nil
}

// ReleaseLock removes the repository lock, but only the one this Store value
// itself acquired. lockHeld/lockID record what acquireLock actually created,
// but that alone isn't enough: between this process taking the lock and this
// call running, someone could have broken it (--break-lock, once that
// exists) and a third apply could have acquired a fresh one. Deleting on the
// strength of the boolean alone would remove that third run's live lock —
// the same mistake as a blind --break-lock, just from the release side, and
// it reopens exactly the two-applies-at-once corruption this feature exists
// to prevent. So this re-reads the file and removes it only if it still
// carries the id this call took; if it doesn't (or the read fails, or it's
// gone already), releasing nothing is the safe outcome — whoever holds the
// lock now will release their own. There is a TOCTOU window between the read
// and the remove, but it is only the interval of one Remove syscall, far
// narrower than the read-a-stale-error-then-type-a-command window
// --break-lock guards against, and not worth adding machinery for.
//
// Idempotent: safe to call when nothing is held, which covers both the
// deferred call after a failed acquireLock and a second call from a signal
// handler racing the deferred one.
func (s *osStore) ReleaseLock() error {
	s.lockMu.Lock()
	held, id, kind := s.lockHeld, s.lockID, s.lockKind
	if kind == lockKindApply {
		s.lockHeld = false
	}
	s.lockMu.Unlock()
	// A held lock exists precisely to outlive the process that took it, so no
	// automatic path may remove one — not this defer, not the signal handler
	// that also calls here. Otherwise a SIGTERM arriving while `rdk lock` was
	// still running would release the lock the user had just asked for, and
	// the only thing standing between that and silent failure would be every
	// future command remembering not to register its store.
	if !held || kind != lockKindApply {
		return nil
	}
	info, err := s.readLock()
	if err != nil || !info.Held || info.ID != id {
		return nil
	}
	if err := s.root.Remove(scratchLock); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// BreakLock removes the lock only if its id matches, and returns what it
// found. The id is required so this can never be "remove whatever is
// there": between reading a blocked-apply error and typing the recovery, the
// stranded lock it named may have been released and a live one taken, and a
// blind removal would destroy that live one.
func (s *osStore) BreakLock(id string) (LockInfo, error) {
	info, err := s.readLock()
	if err != nil {
		if os.IsNotExist(err) {
			return LockInfo{}, fmt.Errorf("no lock %q is held", id)
		}
		return LockInfo{}, err
	}
	if info.ID != id {
		return LockInfo{}, fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	if err := s.root.Remove(scratchLock); err != nil {
		return LockInfo{}, err
	}
	return info, nil
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
	return s.root.MkdirAll(ScratchDir, 0o755)
}

// HoldLock takes a lock that outlives this process, so a person or agent can
// work on the tree without an apply running underneath them.
func (s *osStore) HoldLock(message string) (LockInfo, error) {
	if err := s.ensureScratchDir(); err != nil {
		return LockInfo{}, err
	}
	return s.acquireLock(lockKindHeld, message)
}

// Unlock ends a held lock. It names the lock for the same compare-and-swap
// reason BreakLock does, and refuses an apply lock: ending a running apply is
// breaking, not unlocking, and the two differ in how loudly they report.
func (s *osStore) Unlock(id string) error {
	info, err := s.readLock()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no lock %q is held", id)
		}
		return err
	}
	if info.ID != id {
		return fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	if info.Kind != lockKindHeld {
		return fmt.Errorf("lock %s belongs to a running apply, not to you — use --break-lock=%s if it is stranded", info.ID, info.ID)
	}
	if err := s.root.Remove(scratchLock); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.lockMu.Lock()
	if s.lockID == id {
		s.lockHeld = false
	}
	s.lockMu.Unlock()
	return nil
}

// UseLock runs under an existing held lock without taking or releasing it.
// Both refusals matter: a mismatched id means someone else's lock, and no lock
// at all means yours was broken out from under you — which the caller needs to
// hear rather than have silently treated as permission to proceed.
func (s *osStore) UseLock(id string) error {
	info, err := s.readLock()
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no lock is held: %s was broken out from under you", id)
		}
		return err
	}
	if info.ID != id {
		return fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.lockHeld && s.lockID != id {
		return errors.New("this store already holds a different lock and cannot also run under one")
	}
	s.usingLock = true
	return nil
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
		// whatever the symlink points to. O_EXCL on the final component (below)
		// protects the leaf; this protects everything above it.
		if err := s.checkPathComponents(dir); err != nil {
			return err
		}
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Lstat, not Stat: a symlink must be judged as itself, not as
			// whatever it points to, so that leaving it alone (DD-3) doesn't
			// depend on where it happens to resolve.
			info, statErr := s.root.Lstat(name)
			if statErr != nil {
				return statErr
			}
			if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil // already present (file or symlink) — leave it (DD-3)
			}
			// The path itself isn't repeated here: the caller already has it
			// (Store.Seed's argument, or diag's File field), so this only
			// needs to say what's actually occupying it.
			return &seedTargetError{err: fmt.Errorf("it is a %s", modeKind(info.Mode()))}
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
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
