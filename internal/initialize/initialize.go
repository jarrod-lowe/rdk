// Package initialize implements rdk init: ensure a git repo exists, seed the
// definitions directory through repofs (Seed is create-once and symlink-safe).
package initialize

import (
	"bytes"
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

// repoToplevel runs `git rev-parse --show-toplevel` and hands back the
// parsed path from stdout plus git's stderr, kept separate. isRepoRoot only
// ever needs the bool, but the caller that must explain a directory git
// still can't use after `git init` needs git's own words: git names the
// actionable fix (e.g. the safe.directory command to run) on stderr, and a
// bare bool would throw that away. The streams cannot be merged before
// parsing: git writes to stderr even on success — GIT_TRACE=1,
// GIT_CURL_VERBOSE, GIT_TRACE_SETUP, and advice/hint lines all land there —
// so folding it into the same string as stdout corrupts the path with
// trace/advice text that happens to precede it. top is therefore parsed from
// stdout alone; stderr is returned only for a human-facing message.
func repoToplevel(dir string) (top string, stderr []byte, err error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	if err != nil {
		return "", stderrBuf.Bytes(), err
	}
	// TrimSpace would eat a real trailing/leading space in the directory
	// name — git preserves it faithfully, on Unix a path may legitimately
	// start or end with one, and isRepoRoot's comparison against dir would
	// then fail for a healthy repository. git's plumbing output always
	// terminates the line with a single "\n" (never "\r\n", even on
	// Windows: that CRLF conversion applies to checked-out file content,
	// not to command output), so only that trailing "\n" is git's framing;
	// everything else is the path.
	return strings.TrimSuffix(stdoutBuf.String(), "\n"), stderrBuf.Bytes(), nil
}

// isRepoRoot asks git whether dir is itself the root of a working
// repository, rather than inspecting the .git path directly: a worktree's
// .git is a file, not a directory, and one that is malformed or unreadable
// is indistinguishable from a healthy one by stat alone. git is the
// authority on what git can use. Comparing the toplevel keeps the existing
// behaviour that a subdirectory of a repo still gets its own.
func isRepoRoot(dir string) bool {
	top, _, err := repoToplevel(dir)
	if err != nil {
		return false
	}
	topReal, err := filepath.EvalSymlinks(top)
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return topReal == want
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
		// git documents that running `git init` in an existing repository is
		// safe — it just reinitializes it — so exit 0 here proves only that the
		// git binary ran, not that git can actually use the directory. The
		// common way that gap opens up is safe.directory: it rejects a repo
		// owned by someone else with "detected dubious ownership" even though
		// `git init` itself still exits 0. Prove usability instead of assuming
		// it, or rdk seeds the config and reports the repo ready while every
		// later git command keeps failing.
		if !isRepoRoot(dir) {
			_, out, err := repoToplevel(dir)
			if err == nil {
				// isRepoRoot can only be false here for a toplevel mismatch, not
				// a rev-parse failure — repoToplevel just said as much.
				err = fmt.Errorf("git toplevel for %s does not match it", dir)
			}
			// Same one-line folding as the git-init failure above, for the same
			// reason: git's explanation is the actionable part — it names the
			// exact `git config --global --add safe.directory …` to run — but it
			// spans several lines, some starting "hint:", which would otherwise
			// land flush-left and read as rdk's own output.
			if detail := strings.Join(strings.Fields(string(out)), " "); detail != "" {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			return diag.Wrap(err, diag.Diagnostic{
				Code:    diag.CodeGitUnusable,
				Summary: "git init reported success, but the directory is still not a usable git repository",
				Hint:    "read the cause; git names the command to run, then re-run 'rdk init'",
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
