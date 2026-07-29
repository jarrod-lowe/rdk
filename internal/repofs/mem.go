package repofs

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Mem is an in-memory Store for filesystem-free component tests. It does not
// reproduce os.Root's confinement semantics — security is verified against the
// real Store; Mem is for logic.
type Mem struct {
	files map[string][]byte // repo-relative path -> content
}

// Compile-time assertion that *Mem satisfies Store.
var _ Store = (*Mem)(nil)

// NewMem returns an empty in-memory Store.
func NewMem() *Mem { return &Mem{files: map[string][]byte{}} }

// Files exposes the backing map for test assertions.
func (m *Mem) Files() map[string][]byte { return m.files }

func (m *Mem) Materialize(managedDir string, set *FileSet) error {
	prefix := managedDir + "/"
	for p := range m.files {
		if strings.HasPrefix(p, prefix) {
			delete(m.files, p)
		}
	}
	for _, p := range set.sortedPaths() {
		m.files[path.Join(managedDir, p)] = append([]byte(nil), set.content[p]...)
	}
	return nil
}

func (m *Mem) Seed(name string, data []byte) error {
	if _, ok := m.files[name]; ok {
		return nil
	}
	m.files[name] = append([]byte(nil), data...)
	return nil
}

func (m *Mem) ReadFile(name string) ([]byte, error) {
	d, ok := m.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), d...), nil
}

func (m *Mem) ReadDir(dir string) ([]Entry, error) {
	prefix := dir + "/"
	seen := map[string]bool{} // entry name -> is a directory
	for p := range m.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		// A remaining separator means the entry is a directory: this model has
		// no directory objects, only the paths that imply them.
		isDir := false
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest, isDir = rest[:i], true
		}
		seen[rest] = seen[rest] || isDir
	}
	// A directory with no entries doesn't exist in the in-memory model (dirs
	// are implied by file paths). Match the real Store, which errors on a
	// missing directory rather than returning an empty listing — otherwise a
	// component test could pass on Mem but fail against os.Root.
	if len(seen) == 0 {
		return nil, fs.ErrNotExist
	}
	out := make([]Entry, 0, len(seen))
	for n, isDir := range seen {
		out = append(out, Entry{Name: n, IsDir: isDir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
