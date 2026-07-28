// Package apply orchestrates one rdk apply: parse definitions, build the
// tree, reconcile outside files, wipe and rewrite the managed dir.
package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	securejoin "github.com/cyphar/filepath-securejoin"
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
// (definitions, version, previous manifest) in, repo content out — no network,
// no cloud (rules 1-2). The new managed tree is staged and swapped in
// atomically, so a failure never destroys the previous managed dir or its
// ownership manifest (crash-safety).
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
	staging := filepath.Join(root, ManagedDir+".staging")

	// Complete a swap interrupted by an earlier crash before doing anything:
	// if the managed dir is gone but a fully-staged replacement remains, the
	// crash landed between the old-dir removal and the rename — finish it.
	if err := recoverInterruptedSwap(managed, staging); err != nil {
		return Result{}, err
	}

	prev, _, err := manifest.Load(filepath.Join(managed, "manifest.json"))
	if err != nil {
		return Result{}, err
	}

	// PR-1 plans no outside files; reconcile still runs so stale outside files
	// from prior applies are handled per DD-14.
	plannedOutside := map[string][]byte{}
	var diskErr error
	plan, err := manifest.Reconcile(prev, plannedOutside, func(p string) ([]byte, bool) {
		sp, joinErr := securejoin.SecureJoin(root, p)
		if joinErr != nil {
			if diskErr == nil {
				diskErr = fmt.Errorf("resolving outside file %s: %w", p, joinErr)
			}
			return nil, false
		}
		b, readErr := os.ReadFile(sp)
		if readErr != nil {
			if !os.IsNotExist(readErr) && diskErr == nil {
				diskErr = fmt.Errorf("checking outside file %s: %w", p, readErr)
			}
			return nil, false
		}
		return b, true
	})
	if diskErr != nil {
		return Result{}, diskErr
	}
	if err != nil {
		return Result{}, err
	}

	// Compute the new manifest's outside-file ownership up front so the staged
	// manifest is complete before any destructive step.
	outsideHashes := map[string]string{}
	for _, p := range plan.Writes {
		outsideHashes[p] = manifest.Hash(plannedOutside[p])
	}

	// Stage the entire new managed tree — including the manifest — in a temp
	// dir. A failure here leaves the previous managed dir and its ownership
	// manifest fully intact: the old state is never destroyed until a complete
	// replacement is ready (rule 13 + crash-safety).
	if err := os.RemoveAll(staging); err != nil {
		return Result{}, err
	}
	var paths []string
	for p := range tree {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		dst := filepath.Join(staging, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(dst, tree[p], 0o644); err != nil {
			return Result{}, err
		}
	}
	m := manifest.Manifest{RdkVersion: version, OutsideFiles: outsideHashes}
	encoded, err := m.Encode()
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), encoded, 0o644); err != nil {
		return Result{}, err
	}

	// Outside-file mutations (external to the managed dir). Done before the swap
	// so a failure leaves the old managed dir intact and the apply retryable.
	// Paths resolved symlink-safely (securejoin) so they cannot escape the repo.
	for _, p := range plan.Writes {
		dst, joinErr := securejoin.SecureJoin(root, p)
		if joinErr != nil {
			return Result{}, fmt.Errorf("resolving outside file %s: %w", p, joinErr)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(dst, plannedOutside[p], 0o644); err != nil {
			return Result{}, err
		}
	}
	for _, p := range plan.Deletes {
		dst, joinErr := securejoin.SecureJoin(root, p)
		if joinErr != nil {
			return Result{}, fmt.Errorf("resolving outside file %s: %w", p, joinErr)
		}
		if err := os.Remove(dst); err != nil {
			return Result{}, err
		}
	}

	// Atomically swap staging into place. The RemoveAll+Rename window is closed
	// on the next run by recoverInterruptedSwap.
	if err := os.RemoveAll(managed); err != nil {
		return Result{}, err
	}
	if err := os.Rename(staging, managed); err != nil {
		return Result{}, err
	}

	return Result{
		FilesWritten:   len(tree) + 1, // +1 for manifest.json
		OutsideWrites:  plan.Writes,
		OutsideDeletes: plan.Deletes,
	}, nil
}

// recoverInterruptedSwap completes a swap left half-done by a crash: if the
// managed dir is absent but a fully-staged replacement exists, the crash landed
// between removing the old dir and renaming the new one, so finish the rename.
// (Staging is only fully populated just before the swap, so this is safe.)
func recoverInterruptedSwap(managed, staging string) error {
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		return nil // managed present (or a stat error) — nothing to recover
	}
	if _, err := os.Stat(staging); err != nil {
		return nil // no staged replacement — nothing to recover
	}
	if err := os.Rename(staging, managed); err != nil {
		return fmt.Errorf("completing interrupted apply swap: %w", err)
	}
	return nil
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
