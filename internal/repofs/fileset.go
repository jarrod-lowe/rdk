// Package repofs is the only path through which rdk touches the filesystem.
// Components describe desired files in a FileSet and hand it to a Store; the
// Store owns security (rooted at the repo), directory creation, permissions,
// deterministic serialization, and atomic replacement.
package repofs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// managedDirName mirrors apply.ManagedDir. repofs cannot import apply — apply
// already imports repofs, and the reverse would cycle — so the name is
// duplicated here rather than shared. A test in internal/apply asserts the two
// stay equal; if they ever drift, AtRepoRoot would silently accept a path that
// collides with the managed dir.
const managedDirName = "rdk-managed"

// Root is what a Path resolves against.
type Root int

const (
	rootManaged Root = iota
	rootRepo
)

// Path is a FileSet entry's location: a repo-relative path plus what it
// resolves against. It is opaque so that a bare string cannot become one —
// escaping the managed directory has to be a word someone typed, not a path
// that happened to climb.
type Path struct {
	p    string
	root Root
}

// Managed resolves under the directory being materialized. This is the
// ordinary case: everything rdk generates lives there and is replaced wholesale
// every apply.
func Managed(p string) Path { return Path{p: p, root: rootManaged} }

// AtRepoRoot resolves from the repository root, for the few files that cannot
// live in a directory rdk deletes and regenerates — CI workflows,
// agent-discovery files. DD-14 calls these outside files and requires manifest
// accounting for them, so they are an exception requiring justification
// (rule 12), not a convenience.
func AtRepoRoot(p string) Path { return Path{p: p, root: rootRepo} }

// check rejects anything that could resolve somewhere the caller did not name.
// Unclean paths are rejected rather than cleaned so that what a reader sees in
// the source is what lands on disk, with no normalization step in between to
// reason about.
func (p Path) check() error {
	if p.p == "" || p.p == "." {
		return fmt.Errorf("repofs: empty path")
	}
	if path.IsAbs(p.p) {
		return fmt.Errorf("repofs: %q is absolute", p.p)
	}
	if clean := path.Clean(p.p); clean != p.p {
		return fmt.Errorf("repofs: %q is not clean (want %q)", p.p, clean)
	}
	if p.p == ".." || strings.HasPrefix(p.p, "../") {
		return fmt.Errorf("repofs: %q escapes the directory it resolves against", p.p)
	}
	if p.root == rootRepo {
		// Two names for one location: the manifest would end up tracking a
		// file that gets wiped every apply (DD-14's scope decision).
		if p.p == managedDirName || strings.HasPrefix(p.p, managedDirName+"/") {
			return fmt.Errorf("repofs: %q is inside the managed dir %s", p.p, managedDirName)
		}
		if p.p == ScratchDir || strings.HasPrefix(p.p, ScratchDir+"/") {
			return fmt.Errorf("repofs: %q is inside rdk's scratch dir %s", p.p, ScratchDir)
		}
	}
	return nil
}

// FileSet is an in-memory description of files to write. Content is resolved
// to bytes at add time.
type FileSet struct {
	content map[Path][]byte
}

// NewFileSet returns an empty FileSet.
func NewFileSet() *FileSet {
	return &FileSet{content: map[Path][]byte{}}
}

// Bytes adds a file from raw bytes. The constructors that build p (Managed,
// AtRepoRoot) do not validate; this is the first point that can return an
// error, so this is where validation happens.
func (s *FileSet) Bytes(p Path, data []byte) error {
	if err := p.check(); err != nil {
		return err
	}
	s.content[p] = data
	return nil
}

// JSON adds a file whose content is v serialized as deterministic JSON:
// two-space indent, sorted map keys (encoding/json), stdlib default escaping
// (so < > & appear as \uXXXX), trailing newline. This is the single owner of
// rdk's JSON output format (DD-1).
func (s *FileSet) JSON(p Path, v any) error {
	if err := p.check(); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	s.content[p] = buf.Bytes()
	return nil
}

// Len reports the number of entries.
func (s *FileSet) Len() int { return len(s.content) }

// sortedPaths returns the managed entries' paths, in deterministic order.
// Repo-root entries are surfaced separately via outsideEntries: a Store
// materializing the managed tree must not see them mixed in with what it's
// about to write.
func (s *FileSet) sortedPaths() []string {
	paths := make([]string, 0, len(s.content))
	for p := range s.content {
		if p.root == rootManaged {
			paths = append(paths, p.p)
		}
	}
	sort.Strings(paths)
	return paths
}

// managedBytes returns the content for a managed-root path added via Bytes or
// JSON.
func (s *FileSet) managedBytes(p string) []byte {
	return s.content[Path{p: p, root: rootManaged}]
}

// outsideEntries returns the repo-relative paths of every repo-root entry, in
// deterministic order. A non-empty result means the set cannot be
// materialized yet: writing these needs DD-14's manifest reconcile, which
// isn't wired.
func (s *FileSet) outsideEntries() []string {
	var paths []string
	for p := range s.content {
		if p.root == rootRepo {
			paths = append(paths, p.p)
		}
	}
	sort.Strings(paths)
	return paths
}

// checkNoOutsideEntries rejects a set containing any repo-root entry. Outside
// files need DD-14's manifest reconcile — hard error on a pre-existing unknown
// file, hash comparison against the manifest, the stale-file delete. None of
// that is wired, and writing them blindly is how apply clobbers files the user
// owns.
func (s *FileSet) checkNoOutsideEntries() error {
	outside := s.outsideEntries()
	if len(outside) == 0 {
		return nil
	}
	return fmt.Errorf("repofs: %s: outside files are not yet supported (needs DD-14's manifest reconcile)", outside[0])
}
