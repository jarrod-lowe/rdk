package initialize

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

func newStore(t *testing.T, dir string) repofs.Store {
	t.Helper()
	s, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInitCreatesLayout(t *testing.T) {
	dir := t.TempDir()
	if err := Run(newStore(t, dir), dir); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("expected git repo: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "rdk", "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "kind: config") {
		t.Errorf("config.yaml missing kind: config")
	}
}

func TestInitIsSeedOnce(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)
	if err := Run(store, dir); err != nil {
		t.Fatal(err)
	}
	custom := "kind: config\nname: customized\n"
	cfgPath := filepath.Join(dir, "rdk", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(store, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != custom {
		t.Error("init overwrote a user-owned seeded file")
	}
}

func TestInitDoesNotCommit(t *testing.T) {
	dir := t.TempDir()
	if err := Run(newStore(t, dir), dir); err != nil {
		t.Fatal(err)
	}
	heads := filepath.Join(dir, ".git", "refs", "heads")
	if entries, err := os.ReadDir(heads); err == nil && len(entries) != 0 {
		t.Error("init created a commit")
	}
}

// The failure path had no test, and it now has rendering worth pinning: git's
// output is folded into the cause, and is absent entirely when git never ran.
func TestGitInitFailureCarriesGitsOutput(t *testing.T) {
	stub := t.TempDir()
	script := "#!/bin/sh\necho 'fatal: cannot mkdir' >&2\necho 'hint: check perms' >&2\nexit 128\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub)

	dir := t.TempDir()
	store, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(store, dir)
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeGitInit {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeGitInit)
	}
	// One line, so git's own "hint:" cannot masquerade as rdk's output.
	if got := d.Error(); strings.Contains(got, "\n") {
		t.Errorf("error spans several lines: %q", got)
	}
	if !strings.Contains(d.Error(), "cannot mkdir") || !strings.Contains(d.Error(), "check perms") {
		t.Errorf("error does not carry git's output: %q", d.Error())
	}
}

// The dubious-ownership shape: rev-parse always fails (safe.directory
// rejects the repo), but git documents that init on an existing repository
// is safe, so init succeeds regardless. Without a recheck, rdk would seed the
// config and report the repo ready while every later git command still
// fails.
func TestGitInitSucceedsButRepoStaysUnusable(t *testing.T) {
	stub := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = rev-parse ]; then\n" +
		"  echo 'fatal: detected dubious ownership in repository' >&2\n" +
		"  echo 'hint: git config --global --add safe.directory /repo' >&2\n" +
		"  exit 128\n" +
		"fi\n" +
		"if [ \"$1\" = init ]; then\n" +
		"  echo 'Reinitialized existing Git repository'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub)

	dir := t.TempDir()
	store, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(store, dir)
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeGitUnusable {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeGitUnusable)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
	if !strings.Contains(d.Error(), "dubious ownership") || !strings.Contains(d.Error(), "safe.directory") {
		t.Errorf("error does not carry git's explanation: %q", d.Error())
	}
}

// A .git that git cannot use was indistinguishable from a healthy one by
// stat, so rdk seeded the config and reported success on a broken repo.
func TestMalformedGitIsNotTreatedAsARepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := repofs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(store, dir)
	if err == nil {
		t.Fatal("want an error for a .git git cannot use, got success")
	}
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeGitInit {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeGitInit)
	}
}

// `rdk -> docs` in the checkout used to let MkdirAll resolve straight through
// the symlink and seed config.yaml into docs/, reporting success while never
// touching the rdk/ the user meant. init must now refuse instead, name the
// symlink, and leave docs/ without a config.yaml.
func TestInitRefusesASymlinkedDefsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(dir, "rdk")); err != nil {
		t.Fatal(err)
	}
	err := Run(newStore(t, dir), dir)
	if err == nil {
		t.Fatal("want an error when rdk/ is a symlink, got success")
	}
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeUnsafePath {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeUnsafePath)
	}
	if !strings.Contains(d.Error(), "rdk") {
		t.Errorf("rendered message %q does not name rdk", d.Error())
	}
	if !strings.Contains(d.Hint, "symlink") {
		t.Errorf("hint %q does not say to remove the symlink", d.Hint)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "docs", "config.yaml")); statErr == nil {
		t.Error("init wrote through the symlink into docs/config.yaml")
	}
}

// A directory at rdk/config.yaml is indistinguishable from an already-seeded
// file by EEXIST alone, so without a mode check init would report success and
// leave the failure for a later apply, far from its cause.
func TestConfigPathIsADirectoryIsNotTreatedAsSeeded(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rdk", "config.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Run(newStore(t, dir), dir)
	if err == nil {
		t.Fatal("want an error when rdk/config.yaml is a directory, got success")
	}
	var d *diag.Error
	if !errors.As(err, &d) {
		t.Fatalf("error is not a diagnostic: %v", err)
	}
	if d.Code != diag.CodeSeedNotAFile {
		t.Errorf("code = %q, want %q", d.Code, diag.CodeSeedNotAFile)
	}
	if got := diag.ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d, want 1", got)
	}
}

// repoToplevel used to run git with CombinedOutput and parse the merged
// stream as the path. Git writes to stderr even when it succeeds — GIT_TRACE
// is a real-world example, and advice/hint lines are another — so any such
// output landing before the path text corrupted it. Confirm the parsed path
// is stdout alone by making git as noisy as possible on stderr.
func TestRepoToplevelParsesStdoutOnly(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Setenv("GIT_TRACE", "1")

	top, _, err := repoToplevel(dir)
	if err != nil {
		t.Fatalf("repoToplevel: %v", err)
	}
	if strings.Contains(top, "trace:") {
		t.Fatalf("parsed path contains git trace output: %q", top)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", top, err)
	}
	if realTop != realDir {
		t.Errorf("top = %q, want %q", realTop, realDir)
	}
}

// repoToplevel used to TrimSpace the whole of stdout, which eats a real
// trailing/leading space in the directory name as readily as it eats git's
// line terminator — git preserves such a name faithfully. isRepoRoot then
// compared the mangled path against dir, they never matched, and a healthy
// repository whose name ends in a space was reported as not a repository at
// all. Confirm only git's "\n" is stripped, not a trailing space that is
// genuinely part of the path.
func TestRepoToplevelPreservesTrailingSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo ")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("filesystem rejected a directory name ending in a space: %v", err)
	}
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	top, _, err := repoToplevel(dir)
	if err != nil {
		t.Fatalf("repoToplevel: %v", err)
	}
	if !strings.HasSuffix(top, " ") {
		t.Fatalf("top = %q, lost the trailing space that is genuinely part of the path", top)
	}
}

// The end-to-end version of the same bug: rdk init on a perfectly healthy
// repository whose directory name ends in a space used to run `git init`
// again (isRepoRoot wrongly said "not a repo") and then report the
// directory git-unusable, because the same mangled comparison failed a
// second time. Neither should happen for a repository git itself is happy
// with.
func TestInitAcceptsDirectoryWithTrailingSpaceInName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo ")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("filesystem rejected a directory name ending in a space: %v", err)
	}
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	if err := Run(newStore(t, dir), dir); err != nil {
		t.Fatalf("Run reported a healthy repository unusable: %v", err)
	}
}

// The end-to-end version of the bug: with GIT_TRACE=1 in the environment
// (exec.Command inherits the parent's env unless cmd.Env is set, and
// repoToplevel never sets it), rev-parse's trace lines used to corrupt the
// parsed toplevel enough that it no longer matched dir, so isRepoRoot
// reported a healthy repository as not a repository at all.
func TestIsRepoRootIgnoresGitTraceOnStderr(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Setenv("GIT_TRACE", "1")

	if !isRepoRoot(dir) {
		t.Fatal("isRepoRoot reported a healthy repository as not a repository while GIT_TRACE was set")
	}
}
