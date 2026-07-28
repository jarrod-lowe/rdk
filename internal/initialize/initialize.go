// Package initialize implements rdk init: ensure a git repo exists, seed the
// definitions directory through repofs (Seed is create-once and symlink-safe).
package initialize

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jarrod-lowe/rdk/internal/repofs"
)

const seedConfig = `# rdk global configuration.
# Docs: run 'rdk apply' after editing anything in this directory.
kind: config
name: my-project # TODO: set your project name
`

// Run initialises dir as an rdk repository. The store must be rooted at dir.
func Run(store repofs.Store, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %v\n%s", err, out)
		}
	}
	return store.Seed("rdk/config.yaml", []byte(seedConfig))
}
