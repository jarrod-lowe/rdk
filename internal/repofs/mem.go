package repofs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Mem is an in-memory Store for filesystem-free component tests. It does not
// reproduce os.Root's confinement semantics — security is verified against the
// real Store; Mem is for logic.
type Mem struct {
	files map[string][]byte // repo-relative path -> content

	// lockHeld and lockID mirror osStore's: they record whether this Mem
	// value took the lock it is about to release, so ReleaseLock never
	// removes one it didn't grant. No mutex — Mem is single-goroutine test
	// scaffolding, unlike the real Store.
	lockHeld bool
	lockID   string
}

// Compile-time assertion that *Mem satisfies Store.
var _ Store = (*Mem)(nil)

// NewMem returns an empty in-memory Store.
func NewMem() *Mem { return &Mem{files: map[string][]byte{}} }

// Files exposes the backing map for test assertions.
func (m *Mem) Files() map[string][]byte { return m.files }

func (m *Mem) Materialize(managedDir string, set *FileSet) error {
	// Mem must reject exactly what osStore rejects, or a component test could
	// pass against the fake while misrepresenting what production does.
	if err := set.checkNoOutsideEntries(); err != nil {
		return err
	}
	if err := m.acquireLock(); err != nil {
		return err
	}
	defer m.ReleaseLock()
	prefix := managedDir + "/"
	for p := range m.files {
		if strings.HasPrefix(p, prefix) {
			delete(m.files, p)
		}
	}
	for _, p := range set.sortedPaths() {
		m.files[path.Join(managedDir, p)] = append([]byte(nil), set.managedBytes(p)...)
	}
	return nil
}

// acquireLock mirrors osStore.acquireLock's exclusive-create semantics using
// the same map that models the rest of the tree, keyed under scratchLock so a
// planted lock and a materialized file can never collide.
func (m *Mem) acquireLock() error {
	if _, ok := m.files[scratchLock]; ok {
		existing, _ := m.readLock()
		return &lockedError{err: fmt.Errorf("%w (%s)", ErrLocked, describeLock(existing))}
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	info := LockInfo{
		ID:    hex.EncodeToString(idBytes),
		Kind:  "apply",
		Host:  hostname(),
		PID:   os.Getpid(),
		Since: time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	m.files[scratchLock] = b
	m.lockHeld = true
	m.lockID = info.ID
	return nil
}

// readLock mirrors osStore.readLock: a lock file that exists but fails to
// parse still reports Held, since the malformed file is itself what has to
// keep blocking (rule 6).
func (m *Mem) readLock() (LockInfo, error) {
	b, ok := m.files[scratchLock]
	if !ok {
		return LockInfo{}, fs.ErrNotExist
	}
	var info LockInfo
	_ = json.Unmarshal(b, &info) // best effort; see doc comment
	info.Held = true
	return info, nil
}

// ReleaseLock removes the lock only if this Mem value acquired it and the
// lock still carries the id it took — see osStore.ReleaseLock for why the id
// recheck matters (a broken-and-replaced lock must not be deleted by the run
// whose lock was broken). Idempotent.
func (m *Mem) ReleaseLock() error {
	if !m.lockHeld {
		return nil
	}
	id := m.lockID
	m.lockHeld = false
	info, err := m.readLock()
	if err != nil || !info.Held || info.ID != id {
		return nil
	}
	delete(m.files, scratchLock)
	return nil
}

// BreakLock mirrors osStore.BreakLock: the id is required, and only a match
// is removed.
func (m *Mem) BreakLock(id string) (LockInfo, error) {
	info, err := m.readLock()
	if err != nil {
		if err == fs.ErrNotExist {
			return LockInfo{}, fmt.Errorf("no lock %q is held", id)
		}
		return LockInfo{}, err
	}
	if info.ID != id {
		return LockInfo{}, fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	delete(m.files, scratchLock)
	return info, nil
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
