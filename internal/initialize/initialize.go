// Package initialize implements rdk init: ensure a git repo exists, seed the
// definitions directory. Seeded files are user-owned from the moment they are
// written (DD-3): init never overwrites and never commits.
package initialize

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const seedConfig = `# rdk global configuration.
# Docs: run 'rdk apply' after editing anything in this directory.
kind: config
name: my-project # TODO: set your project name
`

// Run initialises dir as an rdk repository.
func Run(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %v\n%s", err, out)
		}
	}

	defs := filepath.Join(dir, "rdk")
	// Refuse to seed through a symlinked definitions directory: writing into it
	// could create files outside the repository.
	if fi, err := os.Lstat(defs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; rdk will not seed through a symlinked definitions directory", defs)
	}
	if err := os.MkdirAll(defs, 0o755); err != nil {
		return err
	}

	// Seed once, without following symlinks. O_CREATE|O_EXCL creates the file
	// only if the path does not already exist — as a regular file OR a symlink —
	// so a pre-existing or dangling symlink at config.yaml can never redirect
	// the write outside the repo. An existing path means "already seeded": the
	// file is the user's from then on (DD-3), so leave it untouched.
	cfg := filepath.Join(defs, "config.yaml")
	f, err := os.OpenFile(cfg, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil // already present (file or symlink) — seed-once, leave it
		}
		return err
	}
	defer f.Close()
	if _, err := f.Write([]byte(seedConfig)); err != nil {
		return err
	}
	return nil
}
