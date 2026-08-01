package repofs

import (
	"errors"
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

// Store is the injected set of filesystem actions rdk performs. The real
// implementation is rooted at the repo, so no operation can escape it.
type Store interface {
	// Materialize atomically replaces managedDir with the FileSet: the new
	// tree is built in ScratchDir, the current managedDir (if any) is
	// displaced by rename rather than deleted, and the new tree is renamed
	// into place. A rename is all-or-nothing, so no step can leave managedDir
	// half-written. ScratchDir is rdk-owned and git-ignores its own contents,
	// so it never touches the user's root .gitignore. Parent dirs are
	// created; files use 0o644, dirs 0o755. managedDir is repo-relative.
	Materialize(managedDir string, set *FileSet) error
	// Seed creates a user-owned file once: it never overwrites and never
	// follows a symlink at the target. A pre-existing path is a no-op.
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
		return err
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
			return nil // already present (file or symlink) — leave it (DD-3)
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
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
