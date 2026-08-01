package repofs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
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
	// than writing and deleting through it. Parent dirs are created; files use
	// 0o644, dirs 0o755. managedDir is repo-relative.
	Materialize(managedDir string, set *FileSet) error
	// Seed creates a user-owned file once: it never overwrites and never
	// follows a symlink at the target. A pre-existing file or symlink is a
	// no-op; anything else there (a directory, a device, a socket) is
	// reported via ErrSeedTarget rather than treated as already seeded.
	Seed(path string, data []byte) error
	// ReadFile / ReadDir read within the repo root. ReadDir returns entries
	// sorted by name. Paths are repo-relative.
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]Entry, error)
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

func (s *osStore) Materialize(managedDir string, set *FileSet) error {
	// Judged before MkdirAll, and by Lstat rather than Stat: MkdirAll succeeds
	// whenever the path already resolves to a directory, including through a
	// symlink, and by then the .gitignore write below would already be aimed
	// wherever that symlink points. Lstat sees the symlink itself rather than
	// its target, so it catches what MkdirAll cannot refuse.
	//
	// scratchNew and scratchOld get no matching check: both are unconditionally
	// RemoveAll'd a few lines down, and RemoveAll unlinks a symlink as itself
	// rather than recursing through it (it only recurses on EISDIR, which a
	// symlink never returns), so a symlink planted at either name is inert —
	// removed, not followed. ScratchDir is different because nothing removes
	// it first; MkdirAll and WriteFile walk straight through it.
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
	if err := s.root.WriteFile(ScratchDir+"/.gitignore", []byte("*\n"), 0o644); err != nil {
		return err
	}

	// The scratch is never trusted across runs, so both names are cleared
	// unconditionally. This is what covers a hard kill, where the sweep at the
	// end never ran at all — and unlike that sweep, it has to succeed, because
	// the names are needed.
	if err := s.root.RemoveAll(scratchNew); err != nil {
		return err
	}
	if err := s.root.RemoveAll(scratchOld); err != nil {
		return err
	}

	for _, p := range set.sortedPaths() {
		full := path.Join(scratchNew, p)
		if err := s.root.MkdirAll(path.Dir(full), 0o755); err != nil {
			return err
		}
		if err := s.root.WriteFile(full, set.content[p], 0o644); err != nil {
			return err
		}
	}

	// Displace by rename, not RemoveAll: a rename is all-or-nothing, so there
	// is no half-deleted tree to be mistaken for a healthy one on the next run.
	// It also works on Windows, where renaming onto an existing directory does
	// not.
	switch _, err := s.root.Stat(managedDir); {
	case err == nil:
		if err := s.root.Rename(managedDir, scratchOld); err != nil {
			return err
		}
	case !os.IsNotExist(err):
		// Not knowing whether there is a tree to displace is its own failure.
		// Falling through would surface a confusing rename error instead of
		// the real cause.
		return err
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

func (s *osStore) Seed(name string, data []byte) error {
	if dir := path.Dir(name); dir != "." {
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
