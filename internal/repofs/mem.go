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
	lockKind string

	// usingLock mirrors osStore's: this Mem adopted a lock it did not take
	// (UseLock), so Materialize neither acquires nor releases.
	usingLock bool
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
	if !m.usingLock {
		if _, err := m.acquireLock(lockKindApply, ""); err != nil {
			return err
		}
		defer m.ReleaseLock()
	}
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
func (m *Mem) acquireLock(kind, message string) (LockInfo, error) {
	if m.usingLock {
		return LockInfo{}, errors.New("this store is already running under an adopted lock and cannot also acquire one")
	}
	if _, ok := m.files[scratchLock]; ok {
		existing, _ := m.readLock()
		return LockInfo{}, &lockedError{err: fmt.Errorf("%w (%s)", ErrLocked, describeLock(existing)), info: existing}
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
	m.files[scratchLock] = b
	m.lockHeld = true
	m.lockID = info.ID
	m.lockKind = kind
	return info, nil
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
//
// lockHeld is cleared only once the removal (or the discovery that there is
// nothing of this call's left to remove) is settled, mirroring osStore: Mem
// has no concurrent callers of its own (see the doc comment on the struct),
// but the two Store implementations must agree on when ownership is
// surrendered, not just on the end state, so a test written against one
// cannot describe a sequencing osStore does not actually have.
func (m *Mem) ReleaseLock() error {
	// A held lock outlives the process that took it, so no automatic path may
	// remove one — see osStore.ReleaseLock.
	if !m.lockHeld || m.lockKind != lockKindApply {
		return nil
	}
	info, err := m.readLock()
	if err != nil || !info.Held || info.ID != m.lockID {
		m.lockHeld = false
		return nil
	}
	delete(m.files, scratchLock)
	m.lockHeld = false
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

// Seed mirrors osStore.Seed's create-once contract. It needs none of
// osStore's write-to-a-scratch-name-then-Link machinery: that exists to keep
// a write that fails partway from leaving a half-written file at name, and a
// map assignment has no partway — it either happens or the earlier error
// return means it never runs, so there is no truncated state for Mem to
// produce or guard against.
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

// HoldLock mirrors osStore.HoldLock.
func (m *Mem) HoldLock(message string) (LockInfo, error) {
	return m.acquireLock(lockKindHeld, message)
}

// Unlock mirrors osStore.Unlock: it names the lock, and refuses an apply lock
// because ending a running apply is breaking, not unlocking.
func (m *Mem) Unlock(id string) error {
	info, err := m.readLock()
	if err != nil {
		if err == fs.ErrNotExist {
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
	delete(m.files, scratchLock)
	if m.lockID == id {
		m.lockHeld = false
	}
	return nil
}

// UseLock mirrors osStore.UseLock.
func (m *Mem) UseLock(id string) (LockInfo, error) {
	info, err := m.readLock()
	if err != nil {
		if err == fs.ErrNotExist {
			return LockInfo{}, fmt.Errorf("no lock is held: %s was broken out from under you", id)
		}
		return LockInfo{}, err
	}
	if info.ID != id {
		return LockInfo{}, fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	if info.Kind != lockKindHeld {
		// An apply lock exists only for the duration of one Materialize, so
		// adopting it would mean running concurrently with the apply that holds
		// it — the corruption the lock exists to prevent, reached through the
		// flag meant to be safe.
		return LockInfo{}, fmt.Errorf("lock %s belongs to a running apply, not to a person or agent — wait for it, or use --break-lock=%s if it is stranded", info.ID, info.ID)
	}
	if m.lockHeld && m.lockID != id {
		return LockInfo{}, errors.New("this store already holds a different lock and cannot also run under one")
	}
	m.usingLock = true
	return info, nil
}
