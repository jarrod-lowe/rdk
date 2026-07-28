// Package apply orchestrates one rdk apply: parse definitions, build the file
// set, materialize the managed dir. All filesystem access is via repofs.
package apply

import (
	"fmt"
	"path/filepath"

	"github.com/jarrod-lowe/rdk/internal/generate"
	"github.com/jarrod-lowe/rdk/internal/manifest"
	"github.com/jarrod-lowe/rdk/internal/parse"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

const (
	// DefsDir is the user-owned definitions directory (repo-relative).
	DefsDir = "rdk"
	// ManagedDir is wholly rdk-owned: replaced atomically every apply.
	ManagedDir = "rdk-managed"
)

// Result summarises an apply for the CLI (rule 6: loud).
type Result struct {
	FilesWritten int
}

// Run performs apply against the repo the store is rooted at. Pure generation:
// definitions in, repo content out — no network, no cloud (rules 1-2).
func Run(store repofs.Store, root, version string) (Result, error) {
	// parse still uses plain os here (full path); Task 6 switches it to
	// parse.Dir(store, DefsDir) and drops root from this signature.
	defs, err := parse.Dir(filepath.Join(root, DefsDir))
	if err != nil {
		return Result{}, err
	}

	tree, err := generate.Build(defs)
	if err != nil {
		return Result{}, err
	}

	// Adapter (removed in Task 5 when generate returns a FileSet directly).
	set := repofs.NewFileSet()
	for p, data := range tree {
		set.Bytes(p, data)
	}

	// Fresh manifest: PR-1 has no outside files, so outside_files is always the
	// empty (non-nil) map, serializing as {}. No prev read / reconcile needed
	// until outside files land (DD-14 machinery kept in manifest, unwired).
	m := manifest.Manifest{RdkVersion: version, OutsideFiles: map[string]string{}}
	if err := set.JSON("manifest.json", m); err != nil {
		return Result{}, err
	}

	if err := store.Materialize(ManagedDir, set); err != nil {
		return Result{}, err
	}
	return Result{FilesWritten: set.Len()}, nil
}

// Summary renders the loud one-line apply report.
func (r Result) Summary() string {
	return fmt.Sprintf("rdk apply: wrote %d files to %s/", r.FilesWritten, ManagedDir)
}
