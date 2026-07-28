// Package repofs is the only path through which rdk touches the filesystem.
// Components describe desired files in a FileSet and hand it to a Store; the
// Store owns security (rooted at the repo), directory creation, permissions,
// deterministic serialization, and atomic replacement.
package repofs

import (
	"bytes"
	"encoding/json"
	"sort"
)

// FileSet is an in-memory description of files to write. Entry paths are
// relative to the directory the set is materialized into (forward-slash).
// Content is resolved to bytes at add time.
type FileSet struct {
	content map[string][]byte
}

// NewFileSet returns an empty FileSet.
func NewFileSet() *FileSet {
	return &FileSet{content: map[string][]byte{}}
}

// Bytes adds a file from raw bytes.
func (s *FileSet) Bytes(path string, data []byte) {
	s.content[path] = data
}

// JSON adds a file whose content is v serialized as deterministic JSON:
// two-space indent, sorted map keys (encoding/json), stdlib default escaping
// (so < > & appear as \uXXXX), trailing newline. This is the single owner of
// rdk's JSON output format (DD-1).
func (s *FileSet) JSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	s.content[path] = buf.Bytes()
	return nil
}

// Len reports the number of entries.
func (s *FileSet) Len() int { return len(s.content) }

// sortedPaths returns entry paths in deterministic order.
func (s *FileSet) sortedPaths() []string {
	paths := make([]string, 0, len(s.content))
	for p := range s.content {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
