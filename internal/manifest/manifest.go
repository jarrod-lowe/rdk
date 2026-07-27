// Package manifest implements DD-14: rdk's only cross-apply state, recording
// the last-apply version and a content hash for every rdk-owned file that
// lives outside the managed directory.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Manifest is persisted as manifest.json inside the managed dir.
type Manifest struct {
	RdkVersion   string            `json:"rdk_version"`
	OutsideFiles map[string]string `json:"outside_files"` // repo-relative path -> sha256
}

// Hash returns the hex sha256 of content.
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Load reads a manifest; found=false (no error) when the file doesn't exist.
func Load(path string) (m Manifest, found bool, err error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("corrupt manifest %s: %w", path, err)
	}
	return m, true, nil
}

// Encode renders the manifest deterministically (struct field order fixed;
// encoding/json sorts the map keys).
func (m Manifest) Encode() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Plan is the outcome of Reconcile: what to write and what to delete.
type Plan struct {
	Writes  []string
	Deletes []string
}

// Reconcile applies the DD-14 decision table. planned maps repo-relative
// paths to the content rdk wants to write this apply; readDisk reports the
// current on-disk content of a repo-relative path. Any detected user edit of
// an rdk-owned file is a hard error — never a silent overwrite or delete.
func Reconcile(prev Manifest, planned map[string][]byte, readDisk func(string) ([]byte, bool)) (Plan, error) {
	var plan Plan

	var paths []string
	for p := range planned {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		prevHash, tracked := prev.OutsideFiles[p]
		current, exists := readDisk(p)
		switch {
		case !tracked && exists:
			return Plan{}, fmt.Errorf(
				"%s already exists but is not rdk-managed; rdk never adopts existing files — move it aside and re-run apply", p)
		case tracked && exists && Hash(current) != prevHash:
			return Plan{}, fmt.Errorf(
				"%s was edited since rdk last wrote it; rdk-owned files must not be hand-edited — revert it (or move your changes to the sanctioned customization point) and re-run apply", p)
		default:
			// New, or ours-untouched, or ours-but-vanished: write
			// unconditionally (philosophy rule 13 — never diff).
			plan.Writes = append(plan.Writes, p)
		}
	}

	var stale []string
	for p := range prev.OutsideFiles {
		if _, still := planned[p]; !still {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	for _, p := range stale {
		current, exists := readDisk(p)
		switch {
		case !exists:
			// already gone; nothing to do
		case Hash(current) == prev.OutsideFiles[p]:
			plan.Deletes = append(plan.Deletes, p)
		default:
			return Plan{}, fmt.Errorf(
				"%s is no longer generated but was edited since rdk last wrote it; revert or delete it yourself, then re-run apply", p)
		}
	}
	return plan, nil
}
