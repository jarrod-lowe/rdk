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
	if err := os.MkdirAll(defs, 0o755); err != nil {
		return err
	}

	cfg := filepath.Join(defs, "config.yaml")
	if _, err := os.Stat(cfg); os.IsNotExist(err) {
		// Seed once; the file is the user's from now on.
		if err := os.WriteFile(cfg, []byte(seedConfig), 0o644); err != nil {
			return err
		}
	}
	return nil
}
