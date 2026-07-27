// Package apply orchestrates one rdk apply: parse definitions, build the
// tree, reconcile outside files, wipe and rewrite the managed dir.
package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jarrod-lowe/rdk/internal/generate"
	"github.com/jarrod-lowe/rdk/internal/manifest"
	"github.com/jarrod-lowe/rdk/internal/parse"
)

const (
	// DefsDir is the user-owned definitions directory.
	DefsDir = "rdk"
	// ManagedDir is wholly rdk-owned: deleted and rewritten every apply.
	ManagedDir = "rdk-managed"
)

// Result summarises an apply for the CLI (rule 6: loud).
type Result struct {
	FilesWritten   int
	OutsideWrites  []string
	OutsideDeletes []string
}

// Run performs apply for the repo rooted at root. It is pure generation:
// (definitions, version, previous manifest) in, repo content out — no
// network, no cloud (rules 1-2).
func Run(root, version string) (Result, error) {
	defs, err := parse.Dir(filepath.Join(root, DefsDir))
	if err != nil {
		return Result{}, err
	}

	tree, err := generate.Build(defs)
	if err != nil {
		return Result{}, err
	}

	managed := filepath.Join(root, ManagedDir)
	prev, _, err := manifest.Load(filepath.Join(managed, "manifest.json"))
	if err != nil {
		return Result{}, err
	}

	// PR-1 plans no outside files; reconcile still runs so stale outside
	// files from prior applies are handled per DD-14.
	plannedOutside := map[string][]byte{}
	plan, err := manifest.Reconcile(prev, plannedOutside, func(p string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			return nil, false
		}
		return b, true
	})
	if err != nil {
		return Result{}, err
	}

	// Wipe and rewrite the managed dir (rule 13: always write, never diff).
	if err := os.RemoveAll(managed); err != nil {
		return Result{}, err
	}
	var paths []string
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		dst := filepath.Join(managed, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(dst, tree[p], 0o644); err != nil {
			return Result{}, err
		}
	}

	// Execute the outside-file plan.
	outsideHashes := map[string]string{}
	for _, p := range plan.Writes {
		dst := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(dst, plannedOutside[p], 0o644); err != nil {
			return Result{}, err
		}
		outsideHashes[p] = manifest.Hash(plannedOutside[p])
	}
	for _, p := range plan.Deletes {
		if err := os.Remove(filepath.Join(root, p)); err != nil {
			return Result{}, err
		}
	}

	m := manifest.Manifest{RdkVersion: version, OutsideFiles: outsideHashes}
	encoded, err := m.Encode()
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(managed, "manifest.json"), encoded, 0o644); err != nil {
		return Result{}, err
	}

	return Result{
		FilesWritten:   len(tree) + 1, // +1 for manifest.json
		OutsideWrites:  plan.Writes,
		OutsideDeletes: plan.Deletes,
	}, nil
}

// Summary renders the loud one-line apply report.
func (r Result) Summary() string {
	s := fmt.Sprintf("rdk apply: wrote %d files to %s/", r.FilesWritten, ManagedDir)
	for _, p := range r.OutsideWrites {
		s += fmt.Sprintf("\n  wrote   %s", p)
	}
	for _, p := range r.OutsideDeletes {
		s += fmt.Sprintf("\n  deleted %s", p)
	}
	return s
}
