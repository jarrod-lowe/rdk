package repofs

import (
	"io/fs"
	"os"
	"path"
	"sort"
)

// Store is the injected set of filesystem actions rdk performs. The real
// implementation is rooted at the repo, so no operation can escape it.
type Store interface {
	// Materialize atomically replaces managedDir with the FileSet: it stages
	// the full tree in a sibling, then swaps it in, recovering a swap that an
	// earlier crash left half-done. Parent dirs are created; files use 0o644,
	// dirs 0o755. managedDir is repo-relative.
	Materialize(managedDir string, set *FileSet) error
	// Seed creates a user-owned file once: it never overwrites and never
	// follows a symlink at the target. A pre-existing path is a no-op.
	Seed(path string, data []byte) error
	// ReadFile / ReadDir read within the repo root. ReadDir returns sorted
	// entry names. Paths are repo-relative.
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]string, error)
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
	staging := managedDir + ".staging"

	// Complete a swap interrupted by an earlier crash: managed absent but a
	// fully-staged replacement present -> finish the rename.
	if _, err := s.root.Stat(managedDir); os.IsNotExist(err) {
		if _, serr := s.root.Stat(staging); serr == nil {
			if err := s.root.Rename(staging, managedDir); err != nil {
				return err
			}
		}
	}

	if err := s.root.RemoveAll(staging); err != nil {
		return err
	}
	for _, p := range set.sortedPaths() {
		full := path.Join(staging, p)
		if err := s.root.MkdirAll(path.Dir(full), 0o755); err != nil {
			return err
		}
		if err := s.root.WriteFile(full, set.content[p], 0o644); err != nil {
			return err
		}
	}
	if err := s.root.RemoveAll(managedDir); err != nil {
		return err
	}
	return s.root.Rename(staging, managedDir)
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

func (s *osStore) ReadDir(dir string) ([]string, error) {
	entries, err := fs.ReadDir(s.root.FS(), dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
