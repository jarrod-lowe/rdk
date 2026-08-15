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
	// value itself acquired the transaction lock, so ReleaseLock never
	// removes one it didn't grant. No mutex — Mem is single-goroutine test
	// scaffolding, unlike the real Store.
	lockHeld bool
	lockID   string

	// usingLock and usingLockID mirror osStore's: this Mem adopted a lock it
	// did not take (UseLock), so Materialize neither acquires nor releases
	// it, and usingLockID is the id UseLock verified, kept so Materialize can
	// re-verify it later — see store.go's Materialize for why.
	usingLock   bool
	usingLockID string
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
	// Mirrors osStore.Materialize: check the held lock (unless this Mem
	// adopted it via UseLock), then always acquire the transaction lock — see
	// store.go for why both still run on the usingLock path.
	if !m.usingLock {
		if held, err := m.readLockFile(scratchLock); err == nil {
			return lockedErrorFor(held)
		} else if err != fs.ErrNotExist {
			return err
		}
	}
	if _, err := m.acquireLock(scratchApplyLock, ""); err != nil {
		return err
	}
	defer m.ReleaseLock()
	// Mirrors osStore.Materialize's re-verification: see its comment for why
	// this has to happen now, under the transaction lock, rather than trust
	// UseLock's own read to still hold.
	if m.usingLock {
		switch held, err := m.readLockFile(scratchLock); {
		case err == nil && held.ID == m.usingLockID:
			// still the lock this run adopted
		case err == nil:
			return lockedErrorFor(held)
		case err == fs.ErrNotExist:
			return fmt.Errorf("lock %s is gone: it was released or broken after this apply had already begun running under it", m.usingLockID)
		default:
			return err
		}
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
// the same map that models the rest of the tree, keyed under file (scratchLock
// for a held lock, scratchApplyLock for a transaction lock) so a planted lock
// and a materialized file can never collide. No separate kind argument, for
// the same reason as osStore.acquireLock: lockKindFor(file) is the one
// authority on what a lock found at file is.
func (m *Mem) acquireLock(file, message string) (LockInfo, error) {
	// No usingLock guard here — see osStore.acquireLock: every Materialize
	// now acquires the transaction lock regardless of whether it also
	// adopted the held lock via UseLock, so this running with usingLock true
	// is the ordinary --with-lock path, not a bug to guard against.
	if _, ok := m.files[file]; ok {
		existing, _ := m.readLockFile(file)
		return LockInfo{}, lockedErrorFor(existing)
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
	m.files[file] = b
	m.lockHeld = true
	m.lockID = info.ID
	return info, nil
}

// readLockFile mirrors osStore.readLockFile: a lock file that exists but
// fails to parse still reports Held, since the malformed file is itself what
// has to keep blocking (rule 6).
//
// Kind and Path come from the file, not the record, exactly as osStore does —
// otherwise a component test could plant a record whose kind disagrees with
// its file and see Mem describe it differently from production.
//
// Where the mirror stops: osStore.readLockFile also Lstats file and refuses
// anything that is not a regular file (ErrLockTarget). Mem has no
// filesystem — m.files is a map from path to bytes, so every entry it holds
// is already exactly one "regular file's" worth of content — so there is no
// symlink or directory state for it to model, and no component test can
// exercise ErrLockTarget against Mem. That path is verified against the real
// Store only.
func (m *Mem) readLockFile(file string) (LockInfo, error) {
	b, ok := m.files[file]
	if !ok {
		return LockInfo{}, fs.ErrNotExist
	}
	var info LockInfo
	_ = json.Unmarshal(b, &info) // best effort; see doc comment
	info.Held = true
	info.Path = file
	info.Kind = lockKindFor(file)
	return info, nil
}

// ReleaseLock removes the transaction lock only if this Mem value acquired
// it and it still carries the id this call took — see osStore.ReleaseLock
// for why the id recheck matters (a broken-and-replaced lock must not be
// deleted by the run whose lock was broken) and why this only ever touches
// scratchApplyLock, never scratchLock: a held lock is not this function's to
// remove, and now that the two live in different files that is structural,
// not a check. Idempotent.
//
// lockHeld is cleared only once the removal (or the discovery that there is
// nothing of this call's left to remove) is settled, mirroring osStore: Mem
// has no concurrent callers of its own (see the doc comment on the struct),
// but the two Store implementations must agree on when ownership is
// surrendered, not just on the end state, so a test written against one
// cannot describe a sequencing osStore does not actually have.
func (m *Mem) ReleaseLock() error {
	if !m.lockHeld {
		return nil
	}
	info, err := m.readLockFile(scratchApplyLock)
	if err != nil || !info.Held || info.ID != m.lockID {
		m.lockHeld = false
		return nil
	}
	delete(m.files, scratchApplyLock)
	m.lockHeld = false
	return nil
}

// BreakLock mirrors osStore.BreakLock: the id is required, checked against
// both the held lock and the transaction lock, and only a match is removed.
func (m *Mem) BreakLock(id string) (LockInfo, error) {
	var found []LockInfo
	for _, file := range []string{scratchLock, scratchApplyLock} {
		info, err := m.readLockFile(file)
		if err != nil {
			if err == fs.ErrNotExist {
				continue
			}
			return LockInfo{}, err
		}
		if info.ID != id {
			found = append(found, info)
			continue
		}
		delete(m.files, file)
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

// HoldLock mirrors osStore.HoldLock: refuses while the transaction lock
// exists, so rdk lock never claims the repository is held while an apply is
// genuinely running.
func (m *Mem) HoldLock(message string) (LockInfo, error) {
	if applying, err := m.readLockFile(scratchApplyLock); err == nil {
		return LockInfo{}, lockedErrorFor(applying)
	} else if err != fs.ErrNotExist {
		return LockInfo{}, err
	}
	return m.acquireLock(scratchLock, message)
}

// Unlock mirrors osStore.Unlock: it only ever writes to scratchLock, and
// reads scratchApplyLock too purely to diagnose an id that names a running
// apply, redirecting to --break-lock instead of degrading to "no lock is
// held".
func (m *Mem) Unlock(id string) error {
	held, heldErr := m.readLockFile(scratchLock)
	if heldErr != nil && heldErr != fs.ErrNotExist {
		return heldErr
	}
	if heldErr == nil && held.ID == id {
		delete(m.files, scratchLock)
		if m.lockID == id {
			m.lockHeld = false
		}
		return nil
	}
	if applying, err := m.readLockFile(scratchApplyLock); err == nil && applying.ID == id {
		return fmt.Errorf("lock %s belongs to a running apply, not to you — use --break-lock=%s if it is stranded", id, id)
	}
	if heldErr != nil {
		return fmt.Errorf("no lock %q is held", id)
	}
	return fmt.Errorf("lock %s does not match %s", held.ID, id)
}

// UseLock mirrors osStore.UseLock: only ever reads scratchLock, so a running
// apply's lock (scratchApplyLock) can never be what this finds — see
// osStore.UseLock for why that makes the old Kind check structural now
// rather than something this still has to test for.
func (m *Mem) UseLock(id string) (LockInfo, error) {
	info, err := m.readLockFile(scratchLock)
	if err != nil {
		if err == fs.ErrNotExist {
			return LockInfo{}, fmt.Errorf("no lock is held: %s was broken out from under you", id)
		}
		return LockInfo{}, err
	}
	if info.ID != id {
		return LockInfo{}, fmt.Errorf("lock %s does not match %s", info.ID, id)
	}
	if m.lockHeld && m.lockID != id {
		return LockInfo{}, errors.New("this store already holds a different lock and cannot also run under one")
	}
	m.usingLock = true
	m.usingLockID = id
	return info, nil
}
