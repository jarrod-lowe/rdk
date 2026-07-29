// Package initialize implements rdk init: ensure a git repo exists, seed the
// definitions directory through repofs (Seed is create-once and symlink-safe).
package initialize

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jarrod-lowe/rdk/internal/repofs"
)

// seedConfig is the user-owned config seeded on first init. It lives on disk
// under seed/ so it reads as the YAML it is: editable, lintable, diffable.

//go:embed seed/config.yaml
var seedConfig string

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
