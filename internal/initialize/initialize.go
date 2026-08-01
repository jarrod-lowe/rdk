// Package initialize implements rdk init: ensure a git repo exists, seed the
// definitions directory through repofs (Seed is create-once and symlink-safe).
package initialize

import (
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

// seedConfig is the user-owned config seeded on first init. It lives on disk
// under seed/ so it reads as the YAML it is: editable, lintable, diffable.

//go:embed seed/config.yaml
var seedConfig string

// isRepoRoot asks git whether dir is itself the root of a working
// repository, rather than inspecting the .git path directly: a worktree's
// .git is a file, not a directory, and one that is malformed or unreadable
// is indistinguishable from a healthy one by stat alone. git is the
// authority on what git can use. Comparing the toplevel keeps the existing
// behaviour that a subdirectory of a repo still gets its own.
func isRepoRoot(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return top == want
}

// Run initialises dir as an rdk repository. The store must be rooted at dir.
func Run(store repofs.Store, dir string) error {
	if !isRepoRoot(dir) {
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			// Git's own output is the cause, not a next step, and docs/errors.md
			// points the reader at the cause for this code. Fold it to one line:
			// git often emits several, including its own "hint:" lines, which
			// would otherwise land flush-left and read as rdk's output. It is
			// empty exactly when git never ran, so only append it when there is
			// something to say.
			if detail := strings.Join(strings.Fields(string(out)), " "); detail != "" {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			return diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeGitInit,
				Summary: "git init failed",
				Hint:    "check that git is installed and the directory is writable",
				Attrs:   []diag.Attr{diag.Str("dir", dir)},
			})
		}
	}
	if err := store.Seed("rdk/config.yaml", []byte(seedConfig)); err != nil {
		if errors.Is(err, repofs.ErrUnsafePath) {
			// The generic seed-failed hint ("check permissions") would send the
			// reader nowhere useful: the fix is not permissions, it's a specific
			// symlink somewhere above rdk/config.yaml that has to go. The cause
			// names it.
			return diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeUnsafePath,
				File:    "rdk/config.yaml",
				Summary: "cannot seed the config",
				Hint:    "remove the symlink named above, then re-run 'rdk init'",
			})
		}
		if errors.Is(err, repofs.ErrSeedTarget) {
			return diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeSeedNotAFile,
				File:    "rdk/config.yaml",
				Summary: "cannot seed the config",
				Hint:    "remove or rename it, then re-run 'rdk init'",
			})
		}
		return diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeSeedFailed,
			File:    "rdk/config.yaml",
			Summary: "cannot seed the config",
			Hint:    "check permissions and free space",
		})
	}
	return nil
}
