// Package apply orchestrates one rdk apply: parse definitions, build the file
// set, materialize the managed dir. All filesystem access is via repofs.
package apply

import (
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/diag"
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
	// Warnings names entries in the definitions dir that were ignored. An
	// ignored file is a resource that does not get generated, so the CLI
	// reports these even though the apply succeeded.
	Warnings []diag.Diagnostic
}

// Run performs apply against the repo the store is rooted at. Pure generation:
// definitions in, repo content out — no network, no cloud (rules 1-2).
func Run(store repofs.Store, version string) (Result, error) {
	defs, warnings, err := parse.Dir(store, DefsDir)
	if err != nil {
		return Result{}, err
	}

	set, err := generate.Build(defs)
	if err != nil {
		return Result{}, err
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
	return Result{FilesWritten: set.Len(), Warnings: warnings}, nil
}

// Summary renders the loud one-line apply report.
func (r Result) Summary() string {
	return fmt.Sprintf("rdk apply: wrote %d files to %s/", r.FilesWritten, ManagedDir)
}
