package repofs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// add puts an entry in the set and fails the test if it is rejected. These are
// fixtures: a rejection means the test is wrong, not the code — and a bare
// set.Bytes(...) would discard that signal silently.
func add(t *testing.T, set *FileSet, p Path, data []byte) {
	t.Helper()
	if err := set.Bytes(p, data); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeWritesTreeWithDirs(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	add(t, set, Managed("sub/b.txt"), []byte("b"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"managed/a.txt": "a", "managed/sub/b.txt": "b"} {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

func TestMaterializeReplacesPriorContent(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("stale.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	if err := s.Materialize("managed", second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale file survived re-materialize")
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "fresh.txt")); err != nil {
		t.Errorf("fresh file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed.staging")); !os.IsNotExist(err) {
		t.Error("staging dir should be gone after success")
	}
}

// Scratch lives inside an rdk-owned directory that ignores itself, so git
// never sees it and the user's root .gitignore is never touched (DD-14).
func TestMaterializeWritesTheScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ScratchDir, ".gitignore"))
	if err != nil {
		t.Fatalf("scratch .gitignore missing: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf("scratch .gitignore = %q, want %q", got, "*\n")
	}
}

// It is rdk's file, so a deleted one comes back (rule 13: always write).
func TestMaterializeRestoresADeletedScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ScratchDir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, ".gitignore")); err != nil {
		t.Errorf("not restored: %v", err)
	}
}

// The state a crash between displacing and publishing leaves behind. There is
// no recovery step any more: the next run simply regenerates, which is
// byte-identical because generation is a pure function (rule 1).
func TestMaterializePublishesAfterAnInterruptedSwap(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	// Leave the crash state directly: managed absent, old holding the tree.
	if err := os.Rename(filepath.Join(root, "managed"), filepath.Join(root, ScratchDir, "old")); err != nil {
		t.Fatal(err)
	}
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("materialize after interrupted swap: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil {
		t.Fatalf("managed not published: %v", err)
	}
	if string(got) != "x" {
		t.Errorf("managed/f.txt = %q, want %q", got, "x")
	}
}

// Nothing may survive in the published tree that the FileSet did not describe,
// and the displaced copy must not linger.
func TestMaterializeLeavesNoScratchBehind(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("stale.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	if err := s.Materialize("managed", second); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join(ScratchDir, "old"),
		filepath.Join(ScratchDir, "new"),
		filepath.Join("managed", "stale.txt"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Errorf("%s still present", rel)
		}
	}
}

// Not being able to tell whether there is a tree to displace is a failure in
// its own right; reporting it as a rename error would name the wrong cause.
func TestMaterializeReportsAnUnreadableManagedDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	// Remove search permission on the parent so Stat("managed") fails with
	// EACCES rather than ENOENT.
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(sub, "managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o700) })

	err := s.Materialize("nested/managed", set)
	if err == nil {
		t.Fatal("want an error when the managed dir cannot be stat-ed")
	}
	if os.IsNotExist(err) {
		t.Errorf("reported as not-exist, want the underlying permission error: %v", err)
	}
}

// chmodUnwritable makes dir's contents undeletable, and restores it so the
// test's own cleanup can succeed. Skips as root, which ignores the bits.
func chmodUnwritable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

// The failure that motivated this design: it must leave the published tree
// untouched rather than half-deleted, and it must say so.
func TestMaterializeReportsASweepFailureAndKeepsTheTree(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("sub/f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// After the displacing rename this becomes .rdk/old/sub, whose contents
	// cannot be unlinked — so the sweep fails while everything before it works.
	chmodUnwritable(t, filepath.Join(root, "managed", "sub"))
	// The pre-rename path is restored above, but by the time cleanup runs the
	// directory has moved to .rdk/old/sub — restore that path too, ignoring
	// whichever of the two no longer exists.
	t.Cleanup(func() { os.Chmod(filepath.Join(root, ScratchDir, "old", "sub"), 0o700) })

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	err := s.Materialize("managed", second)
	if !errors.Is(err, ErrSweep) {
		t.Fatalf("err = %v, want it to wrap ErrSweep", err)
	}
	// The tree is correct: that is the whole point of reporting this
	// separately from every other failure.
	got, readErr := os.ReadFile(filepath.Join(root, "managed", "fresh.txt"))
	if readErr != nil {
		t.Fatalf("published tree is not correct: %v", readErr)
	}
	if string(got) != "new" {
		t.Errorf("managed/fresh.txt = %q, want %q", got, "new")
	}
}

// A blocked scratch must stop the run before anything is displaced — the tree
// on disk has to survive intact.
func TestMaterializeLeavesTheTreeIntactWhenScratchCannotBeCleared(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// Leave an old scratch behind that cannot be removed.
	oldDir := filepath.Join(root, ScratchDir, "old", "sub")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "stuck.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	chmodUnwritable(t, oldDir)

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	if err := s.Materialize("managed", second); err == nil {
		t.Fatal("want an error when the scratch cannot be cleared")
	} else if errors.Is(err, ErrSweep) {
		t.Errorf("reported as a sweep failure, but nothing was published: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil {
		t.Fatalf("previous tree did not survive: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("managed/f.txt = %q, want the previous %q", got, "old")
	}
}

// Displacing is a rename precisely so that failing it leaves the previous tree
// whole — a RemoveAll here could delete half of it and return.
func TestMaterializeLeavesTheTreeIntactWhenItCannotBeDisplaced(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// Read-only root: the scratch already exists and stays writable, so this
	// blocks the displacing rename and nothing before it.
	chmodUnwritable(t, root)

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	err := s.Materialize("managed", second)
	if err == nil {
		t.Fatal("want an error when the tree cannot be displaced")
	}
	if errors.Is(err, ErrSweep) {
		t.Errorf("reported as a sweep failure, but nothing was published: %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if readErr != nil {
		t.Fatalf("previous tree did not survive: %v", readErr)
	}
	if string(got) != "old" {
		t.Errorf("managed/f.txt = %q, want the previous %q", got, "old")
	}
}

// managedDir absent is the alarming case: the reader looks and their tree
// seems to be gone. The message has to say the previous tree is safe and
// where.
func TestMaterializeReportsAPublishFailureAndSaysWhereThePreviousTreeIs(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// Remove managedDir outright (not via rename) so the next Materialize finds
	// nothing to displace and step 4 is skipped — step 5, the publish rename,
	// is then the only remaining step that can fail.
	if err := os.RemoveAll(filepath.Join(root, "managed")); err != nil {
		t.Fatal(err)
	}
	// Read-only root: creating the "managed" entry for the publish rename needs
	// a writable root, same as the displace rename in the test above.
	chmodUnwritable(t, root)

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	err := s.Materialize("managed", second)
	if err == nil {
		t.Fatal("want an error when the tree cannot be published")
	}
	if !errors.Is(err, ErrPublish) {
		t.Fatalf("err = %v, want it to wrap ErrPublish", err)
	}
	if errors.Is(err, ErrSweep) {
		t.Errorf("reported as a sweep failure, but nothing was published: %v", err)
	}
}

// The sequence that motivated keeping .rdk/old alive: a first apply
// publishes, a second run displaces it and then fails at the publish rename
// — ErrPublish's own scenario, left directly on disk here rather than driven
// through a live failure — leaving managed absent and .rdk/old holding the
// only copy. That failure's message promises nothing is lost: clear the
// cause, then re-run to publish it. A third run (the retry) that fails at
// the very same step must not have broken that promise. managedDir is still
// absent, so nothing is going to be renamed onto .rdk/old's name this run;
// the old unconditional clear would have destroyed the only recovery copy
// for no gain, moments before failing again with nothing left to recover.
func TestMaterializeKeepsDisplacedTreeAcrossARepeatedPublishFailure(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	// Leave the state a failed publish leaves: managed absent, .rdk/old
	// holding the previous tree.
	if err := os.Rename(filepath.Join(root, "managed"), filepath.Join(root, ScratchDir, "old")); err != nil {
		t.Fatal(err)
	}

	// Read-only root: everything inside .rdk (clearing, staging) only needs
	// .rdk itself to be writable, so it all still succeeds — only creating
	// the "managed" entry for the publish rename needs a writable root,
	// which makes the retry fail at that same step again.
	chmodUnwritable(t, root)

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	err := s.Materialize("managed", second)
	if !errors.Is(err, ErrPublish) {
		t.Fatalf("err = %v, want it to wrap ErrPublish", err)
	}

	got, err := os.ReadFile(filepath.Join(root, ScratchDir, "old", "f.txt"))
	if err != nil {
		t.Fatalf(".rdk/old lost the previous tree: %v", err)
	}
	if string(got) != "old" {
		t.Errorf(".rdk/old/f.txt = %q, want the previous %q", got, "old")
	}
	if _, statErr := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(statErr) {
		t.Error("managed should still be absent; nothing was published")
	}
}

// os.Root confines symlinks to the repository but follows them inside it, so
// a symlinked scratch would aim the .gitignore write and the scratch deletes
// at whatever it points to — the user's own files.
func TestMaterializeRefusesASymlinkedScratch(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.Symlink(".", filepath.Join(root, ScratchDir)); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(guard, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrScratchTarget) {
		t.Fatalf("err = %v, want it to wrap ErrScratchTarget", err)
	}
	got, err := os.ReadFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "theirs\n" {
		t.Errorf("the user's .gitignore was overwritten: %q", got)
	}
}

// A file where the scratch belongs is the same problem in a plainer form.
func TestMaterializeRefusesAFileWhereScratchBelongs(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.WriteFile(filepath.Join(root, ScratchDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrScratchTarget) {
		t.Fatalf("err = %v, want it to wrap ErrScratchTarget", err)
	}
}

// The scratch .gitignore is rdk's own file, but it lives inside a directory
// the FileSet writer walks straight through. A symlink planted there in place
// of the .gitignore must not turn Materialize into a write against whatever
// it points to — that write would land on a file the user owns.
func TestMaterializeDoesNotFollowASymlinkedScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	victim := filepath.Join(root, "README.md")
	if err := os.WriteFile(victim, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "README.md"), filepath.Join(root, ScratchDir, ".gitignore")); err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# mine\n" {
		t.Errorf("README.md = %q, want it untouched", got)
	}
	// The scratch .gitignore itself must still end up correct: rdk's own file
	// comes back even though a symlink used to occupy its name.
	gi, err := os.ReadFile(filepath.Join(root, ScratchDir, ".gitignore"))
	if err != nil {
		t.Fatalf("scratch .gitignore missing: %v", err)
	}
	if string(gi) != "*\n" {
		t.Errorf("scratch .gitignore = %q, want %q", gi, "*\n")
	}
	if info, err := os.Lstat(filepath.Join(root, ScratchDir, ".gitignore")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("scratch .gitignore is still a symlink")
	}
}

// A dangling symlink at managedDir's name used to be judged via Stat, which
// follows it: Stat reports ENOENT, Materialize concludes there is nothing to
// displace, skips the displacing rename, and the publish rename then fails
// against the symlink that is still occupying the name. Lstat sees the
// symlink itself, so the displace step runs and clears it first.
func TestMaterializePublishesOverADanglingManagedSymlink(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.Symlink("nowhere", filepath.Join(root, "managed")); err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("Materialize over a dangling symlink: %v", err)
	}

	info, err := os.Lstat(filepath.Join(root, "managed"))
	if err != nil {
		t.Fatalf("managed missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("managed is still a symlink after Materialize")
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil || string(got) != "x" {
		t.Errorf("managed/f.txt = %q, %v, want %q", got, err, "x")
	}
}

// A symlink at managedDir's name that resolves to a real directory is not a
// tree rdk made, so displacing it must move the link entry rather than
// follow it: the directory it points to is the user's, wherever it lives,
// and must survive untouched.
func TestMaterializeDisplacesASymlinkToARealDirectoryWithoutTouchingIt(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "elsewhere", "theirs.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(root, "managed")); err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("Materialize over a symlink to a real directory: %v", err)
	}

	info, err := os.Lstat(filepath.Join(root, "managed"))
	if err != nil {
		t.Fatalf("managed missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("managed is still a symlink after Materialize")
	}
	got, err := os.ReadFile(filepath.Join(root, "managed", "f.txt"))
	if err != nil || string(got) != "x" {
		t.Errorf("managed/f.txt = %q, %v, want %q", got, err, "x")
	}
	// The directory the symlink pointed to is not rdk's; only the pointer
	// moved, not what it pointed to.
	got, err = os.ReadFile(victim)
	if err != nil || string(got) != "precious" {
		t.Errorf("elsewhere/theirs.txt = %q, %v, want it untouched", got, err)
	}
}

func TestSeedCreatesOnceAndDoesNotOverwrite(t *testing.T) {
	s, root := newTestStore(t)
	if err := s.Seed("rdk/config.yaml", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed("rdk/config.yaml", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "rdk", "config.yaml"))
	if string(got) != "first" {
		t.Errorf("seed overwrote: got %q", got)
	}
}

// Seeding is "create once, then it is the user's file". A directory is not a
// file, and accepting it silently would defer the failure to a later apply.
func TestSeedRejectsADirectoryAtTheTarget(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, "rdk", "config.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := s.Seed("rdk/config.yaml", []byte("x"))
	if !errors.Is(err, ErrSeedTarget) {
		t.Fatalf("err = %v, want it to wrap ErrSeedTarget", err)
	}
}

// os.Root follows a symlinked parent directory, so `rdk -> docs` would
// otherwise let MkdirAll resolve straight through it and land config.yaml in
// a directory the user owns instead of the one they named.
func TestSeedRefusesASymlinkedParentDirectory(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(root, "rdk")); err != nil {
		t.Fatal(err)
	}

	err := s.Seed("rdk/config.yaml", []byte("seeded"))
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want it to wrap ErrUnsafePath", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "docs", "config.yaml")); statErr == nil {
		t.Error("Seed wrote through the symlinked parent into docs/config.yaml")
	}
	if _, statErr := os.Lstat(filepath.Join(root, "rdk")); statErr != nil {
		t.Errorf("the rdk symlink itself was disturbed: %v", statErr)
	} else if target, err := os.Readlink(filepath.Join(root, "rdk")); err != nil || target != "docs" {
		t.Errorf("rdk symlink target = %q, %v, want it untouched", target, err)
	}
}

// The guard must not reject a legitimate multi-level path where every
// component really is a directory rdk (or the user) made — only a symlinked
// one.
func TestSeedStillWritesALegitimateDeepPath(t *testing.T) {
	s, root := newTestStore(t)
	err := s.Seed("a/b/c/config.yaml", []byte("deep"))
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "a", "b", "c", "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml missing: %v", err)
	}
	if string(got) != "deep" {
		t.Errorf("config.yaml = %q, want %q", got, "deep")
	}
}

// A pre-existing symlink is still the user's, and still left alone.
func TestSeedLeavesASymlinkAlone(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, "rdk"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "rdk", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed("rdk/config.yaml", []byte("ours")); err != nil {
		t.Fatalf("Seed over a symlink: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "theirs" {
		t.Errorf("symlink target = %q, want it untouched", got)
	}
}

func TestReadFileAndReadDir(t *testing.T) {
	s, root := newTestStore(t)
	os.MkdirAll(filepath.Join(root, "rdk"), 0o755)
	os.WriteFile(filepath.Join(root, "rdk", "b.yaml"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(root, "rdk", "a.yaml"), []byte("a"), 0o644)
	entries, err := s.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a.yaml" || entries[1].Name != "b.yaml" {
		t.Errorf("ReadDir = %v, want sorted [a.yaml b.yaml]", entries)
	}
	data, err := s.ReadFile("rdk/a.yaml")
	if err != nil || string(data) != "a" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
}

// Callers must be able to tell a nested directory from a file without a second
// syscall, so rdk can reject one instead of trying to parse it.
func TestReadDirReportsDirectories(t *testing.T) {
	s, root := newTestStore(t)
	os.MkdirAll(filepath.Join(root, "rdk", "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "rdk", "a.yaml"), []byte("a"), 0o644)
	entries, err := s.ReadDir("rdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "a.yaml" || entries[0].IsDir {
		t.Errorf("entries[0] = %+v, want a.yaml file", entries[0])
	}
	if entries[1].Name != "nested" || !entries[1].IsDir {
		t.Errorf("entries[1] = %+v, want nested dir", entries[1])
	}
}

// managedDir is a Store.Materialize parameter, not a hardcoded value: nothing
// stops a future caller from nesting it, and if they do, a symlinked parent
// component must be refused the same way Seed refuses one, rather than
// renaming the managed tree into whatever the symlink points to.
func TestMaterializeRefusesASymlinkedManagedDirParent(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("sub/managed", set)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want it to wrap ErrUnsafePath", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "docs", "managed")); statErr == nil {
		t.Error("Materialize wrote through the symlinked parent into docs/managed")
	}
}

// The store owns security: a set that somehow carries a bad entry must not be
// written, whatever the FileSet already checked.
func TestMaterializeRejectsAnOutsideEntry(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	add(t, set, AtRepoRoot(".github/workflows/ci.yml"), []byte("x"))
	if err := s.Materialize("managed", set); err == nil {
		t.Fatal("want an error for an outside entry")
	}
	// Nothing partial: the managed tree must not appear either.
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("wrote a partial tree before rejecting")
	}
}

func TestSecurityRejectsEscapes(t *testing.T) {
	s, root := newTestStore(t)
	if err := s.Seed("/etc/evil", []byte("x")); err == nil {
		t.Error("absolute path seed should fail")
	}
	if _, err := s.ReadFile("../escape"); err == nil {
		t.Error("dotdot read should fail")
	}
	external := t.TempDir()
	victim := filepath.Join(external, "victim")
	os.WriteFile(victim, []byte("precious"), 0o644)
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := s.ReadFile("link/victim"); err == nil {
		t.Error("read through escaping symlink should fail")
	}
	// Seeding through the escaping symlink must not create a file outside the
	// repo (the scenario the removed initialize symlink tests covered).
	if err := s.Seed("link/evil.yaml", []byte("x")); err == nil {
		t.Error("seed through escaping symlink should fail")
	}
	if _, err := os.Stat(filepath.Join(external, "evil.yaml")); err == nil {
		t.Error("seed wrote through an escaping symlink into an external dir")
	}
}

// writeLockFile plants a lock file the way another process would have, at
// name (relative to ScratchDir) — "lock" for the held lock, "apply.lock" for
// the transaction lock.
func writeLockFile(t *testing.T, root, name string, info LockInfo) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeLock plants a held lock at .rdk/lock.
func writeLock(t *testing.T, root string, info LockInfo) {
	t.Helper()
	writeLockFile(t, root, "lock", info)
}

// writeApplyLock plants a transaction lock at .rdk/apply.lock — the state a
// hard kill leaves behind, since an ordinary exit always releases it via
// defer or the signal handler.
func writeApplyLock(t *testing.T, root string, info LockInfo) {
	t.Helper()
	writeLockFile(t, root, "apply.lock", info)
}

// sampleLock is a fixture LockInfo with fields a test can recognise in an
// error message (id, pid, host) once written directly to disk. kind should
// match whichever file the caller is about to plant it in via writeLock or
// writeApplyLock — acquireLock always keeps the two in agreement; a test that
// deliberately mismatches them is exercising the migration case, and says so.
func sampleLock(kind string) LockInfo {
	return LockInfo{
		ID: "9f3a1c4e7b2d8a05", Kind: kind, Host: "builder-3",
		PID: 4127, Since: "2026-08-02T10:04:11Z",
	}
}

// A held lock blocks an ordinary apply — the table's "must be absent" row.
func TestMaterializeRefusesWhileLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindHeld))

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
	// The holder's details have to reach the message, or the reader cannot
	// tell a live apply from a stranded lock — and the id is what any
	// recovery has to name.
	for _, want := range []string{"9f3a1c4e7b2d8a05", "4127", "builder-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the lock")
	}
}

// The transient case: a second apply started while a first is genuinely
// mid-flight collides on .rdk/apply.lock's O_CREAT|O_EXCL rather than the
// held-lock check above. Planting the transaction lock directly is the same
// state a real concurrent Materialize would leave while it still holds it.
func TestMaterializeRefusesWhileAnotherApplyIsRunning(t *testing.T) {
	s, root := newTestStore(t)
	writeApplyLock(t, root, sampleLock(lockKindApply))

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
	for _, want := range []string{"9f3a1c4e7b2d8a05", "4127", "builder-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the lock")
	}
}

// Testing the actual race directly needs two processes racing between a Stat
// and a lock acquisition — not worth the harness. This instead asserts the
// invariant that makes the race impossible: Materialize resolves the held
// lock (checked, or adopted via UseLock) before it ever reads managedDir's
// state. It plants a held lock so the check fails immediately, and —
// separately — makes managedDir itself unreadable (EACCES, not ENOENT, same
// trick as TestMaterializeReportsAnUnreadableManagedDir) so that a Stat which
// ran before the lock check would surface *that* error instead of ErrLocked.
// Getting ErrLocked back is only possible if the lock was checked first.
//
// What this does not cover: two real processes actually racing between the
// Stat and a concurrent Materialize's displace-then-die. That scenario needs
// two processes to reproduce honestly, and isn't exercised here — this test
// only shows that a single Materialize call never reads managedDir state
// ahead of settling the lock, which is the property the fix relies on.
func TestMaterializeChecksTheLockBeforeReadingManagedDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindHeld))

	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(sub, "managed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o700) })

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err := s.Materialize("nested/managed", set)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked — a different error means Stat read managedDir before the lock was checked", err)
	}
}

// A malformed lock must still block. Failing open here would mean a corrupt
// file silently disables the exclusion.
func TestMaterializeRefusesWhileLockedEvenIfUnreadable(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked", err)
	}
}

func TestMaterializeReleasesTheLockOnSuccess(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply lock survived a successful apply")
	}
	// And a second run must work, which is the point.
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
}

// A stranded lock is the cost of not using flock; it must not also be the cost
// of an ordinary failure.
func TestMaterializeReleasesTheLockOnFailure(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("f.txt"), []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	chmodUnwritable(t, root) // blocks the displacing rename

	second := NewFileSet()
	add(t, second, Managed("fresh.txt"), []byte("new"))
	if err := s.Materialize("managed", second); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply lock survived a failed apply")
	}
}

// The id is the whole safety property: between reading an error and typing the
// recovery, the stranded lock may have been replaced by a live one.
func TestBreakLockOnlyRemovesTheNamedLock(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindHeld))

	if _, err := s.BreakLock("some-other-id"); err == nil {
		t.Error("broke a lock whose id did not match")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("removed the lock anyway: %v", err)
	}

	info, err := s.BreakLock("9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Held || info.PID != 4127 || info.Host != "builder-3" {
		t.Errorf("info = %+v, want the holder's details", info)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock not removed")
	}
}

// With a required id, "nothing there" means the lock you named is gone, which
// the caller should hear rather than have silently treated as success.
func TestBreakLockWithNoLockPresentIsAnError(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.BreakLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("want an error: the named lock does not exist")
	}
}

// A hard kill is the only way a transaction lock is ever found stranded — an
// ordinary exit always releases it via defer or the signal handler — and
// --break-lock has to clear it exactly as it always could when there was
// only one file to look in.
func TestBreakLockRemovesAStrandedApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	writeApplyLock(t, root, sampleLock(lockKindApply))

	info, err := s.BreakLock("9f3a1c4e7b2d8a05")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Held || info.PID != 4127 || info.Host != "builder-3" {
		t.Errorf("info = %+v, want the holder's details", info)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply lock not removed")
	}
	// The repository must be usable again, not just the file gone.
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("materialize after --break-lock: %v", err)
	}
}

// ids are unique across both files, so --break-lock never has to be told (or
// figure out) which one to look in — it resolves the id regardless of which
// file it is actually sitting in, and leaves the other alone.
func TestBreakLockFindsTheIDInEitherFile(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindHeld)) // id 9f3a1c4e7b2d8a05
	other := sampleLock(lockKindApply)
	other.ID = "aaaa1111bbbb2222"
	writeApplyLock(t, root, other)

	if _, err := s.BreakLock("9f3a1c4e7b2d8a05"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("held lock not removed")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Errorf("breaking the held lock also touched the apply lock: %v", err)
	}

	if _, err := s.BreakLock("aaaa1111bbbb2222"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply lock not removed")
	}
}

// rdk lock takes a lock and exits leaving it. Nothing automatic may remove it:
// not a defer, not a signal. Only rdk unlock or --break-lock, both of which
// name it.
func TestReleaseLockLeavesAHeldLockAlone(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("agent refactoring the s3-bucket module")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "held" || info.Message == "" {
		t.Errorf("info = %+v, want a held lock carrying its message", info)
	}
	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("ReleaseLock removed a held lock: %v", err)
	}
}

// A lock committed into the repository is unreleasable: its id belongs to a
// process that is long gone. The scratch has to be invisible to git from the
// moment it exists, not from the first apply.
func TestHoldLockWritesTheScratchGitignore(t *testing.T) {
	s, root := newTestStore(t)
	if _, err := s.HoldLock("working"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ScratchDir, ".gitignore"))
	if err != nil {
		t.Fatalf("scratch .gitignore missing after HoldLock: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf("scratch .gitignore = %q, want %q", got, "*\n")
	}
}

// ensureScratchDir runs before the lock, in both HoldLock and Materialize, so
// two of either racing is normal, not exceptional — a repository with two
// people or two applies starting close together hits this on every fresh
// scratch. Exactly when the goroutines interleave is not something a test can
// pin to a precise instant, so this cannot force any particular interleaving
// the way a unit test normally would; it only drives enough concurrent calls
// that Go's scheduler interleaves some of them across
// publishScratchGitignore's write-then-rename gap. What is deterministic is
// the assertion: however they interleave, none may ever return an error —
// that's what "idempotent" means here — and the .gitignore they leave behind
// must be the complete "*\n", never empty or partial. That second assertion
// is the one this test could not make before write-then-rename replaced
// remove-then-O_EXCL: a losing goroutine used to return success (EEXIST
// treated as "already done") without ever checking what was actually on
// disk. Run with -race and this also confirms there is no data race
// underneath, on top of the logical one this targets.
func TestEnsureScratchDirIsIdempotentUnderConcurrency(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = st.ensureScratchDir()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: ensureScratchDir returned %v, want nil", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(root, ScratchDir, ".gitignore"))
	if err != nil {
		t.Fatalf("scratch .gitignore missing after concurrent calls: %v", err)
	}
	if string(got) != "*\n" {
		t.Errorf("scratch .gitignore = %q, want the complete %q — a reader saw a partial write", got, "*\n")
	}

	// Renaming a temp file consumes its name whether or not that rename is
	// later overwritten by someone else's rename onto the same destination,
	// so none of the n temp names should survive as litter next to the
	// .gitignore they were building.
	entries, err := os.ReadDir(filepath.Join(root, ScratchDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".gitignore" {
			t.Errorf("unexpected entry left in scratch dir: %s", e.Name())
		}
	}
}

func TestHoldLockRefusesWhenAlreadyLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindHeld))
	if _, err := s.HoldLock("second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

// rdk lock must not return success while an apply is genuinely mid-flight —
// that would be exactly the lie the feature exists to prevent, reporting the
// repository held when what's actually true is that a Materialize is
// running.
func TestHoldLockRefusesWhileAnApplyIsRunning(t *testing.T) {
	s, root := newTestStore(t)
	writeApplyLock(t, root, sampleLock(lockKindApply))

	_, err := s.HoldLock("agent working")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), "9f3a1c4e7b2d8a05") {
		t.Errorf("error %q does not name the running apply", err.Error())
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("HoldLock created a held lock despite the running apply")
	}
}

// Unlock is the routine end of your own lock, so it names the lock and refuses
// anything else.
func TestUnlockOnlyReleasesTheNamedHeldLock(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock("some-other-id"); err == nil {
		t.Error("unlocked a lock whose id did not match")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("removed it anyway: %v", err)
	}
	if err := s.Unlock(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock not removed")
	}
}

// An apply's lock is not yours to end routinely — that is what --break-lock is
// for, and it warns. This is the required scenario: rdk unlock naming a
// running apply's lock still gets the redirecting message, even though
// Unlock only ever reads and writes the held-lock file — it reads the
// transaction lock too, purely to diagnose.
func TestUnlockRefusesAnApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	writeApplyLock(t, root, sampleLock(lockKindApply))

	err := s.Unlock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlocked an apply lock")
	}
	if !strings.Contains(err.Error(), "--break-lock") {
		t.Errorf("error %q does not point at the right door", err.Error())
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Errorf("unlock touched the apply lock: %v", err)
	}
}

// Migration note (docs/superpowers/specs/2026-08-02-apply-lock-design.md): a
// lock file left by a pre-split binary can carry kind:"apply" while
// physically sitting in .rdk/lock, since that binary only ever had one file.
// Unlock no longer consults Kind to decide whether a lock is "actually"
// held — the file it's found in is what governs now — so this is treated as
// an ordinary held lock and clears normally. Accepted as mild: the file is
// genuinely stale (nothing is running), so unlocking it directly has the
// same effect --break-lock would have had.
func TestUnlockClearsAStaleApplyKindLockFoundInTheHeldFile(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindApply)) // pre-split binary's stale content

	if err := s.Unlock("9f3a1c4e7b2d8a05"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("stale lock not removed")
	}
}

// Running under someone's lock must neither take nor release it.
func TestUseLockRunsWithoutAcquiringOrReleasing(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseLock(info.ID); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatalf("materialize under a held lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("the held lock did not survive the apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("the transaction lock survived a successful apply run under an adopted lock")
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "f.txt")); err != nil {
		t.Errorf("apply did not publish: %v", err)
	}
}

// UseLock only ever reads scratchLock, so a running apply's lock — which
// lives in scratchApplyLock — can never be what it finds: the old
// Kind-based refusal in UseLock is structural now, not a check. This plants
// the apply lock (with no held lock at all) and confirms adopting its id
// fails the same way any other nonexistent held lock would — the safety
// property (never adopt a live apply's lock) survives, just through a
// different mechanism than before the split.
func TestUseLockCannotAdoptARunningApplysLock(t *testing.T) {
	s, root := newTestStore(t)
	writeApplyLock(t, root, sampleLock(lockKindApply))

	_, err := s.UseLock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("adopted a running apply's lock")
	}
	if !strings.Contains(err.Error(), "broken out from under you") {
		t.Errorf("error %q is not the generic no-held-lock message", err.Error())
	}
}

// Migration note, UseLock's side of TestUnlockClearsAStaleApplyKindLockFoundInTheHeldFile:
// a stale kind:"apply" lock physically sitting in .rdk/lock (left by a
// pre-split binary) is adoptable via --with-lock now — Kind no longer gates
// UseLock, the file does. Accepted as mild: nothing is actually running
// under it, so adopting it is no more dangerous than adopting any other held
// lock.
func TestUseLockAdoptsAStaleApplyKindLockFoundInTheHeldFile(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, sampleLock(lockKindApply))

	if _, err := s.UseLock("9f3a1c4e7b2d8a05"); err != nil {
		t.Fatalf("UseLock: %v", err)
	}
}

func TestUseLockRejectsAMismatchedOrAbsentLock(t *testing.T) {
	s, root := newTestStore(t)
	// Nothing held: you asserted you hold a lock and you do not, which means
	// it was broken out from under you.
	if _, err := s.UseLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("adopted a lock that does not exist")
	}
	writeLock(t, root, sampleLock(lockKindHeld))
	if _, err := s.UseLock("some-other-id"); err == nil {
		t.Error("adopted someone else's lock under the wrong id")
	}
}

// The whole point of --with-lock is that the holder can apply repeatedly
// while they work: the lock is adopted, never consumed. Each iteration opens
// a fresh Store, the way each separate `rdk apply --with-lock` process would,
// so this exercises the lock file surviving across processes, not just calls
// on one Go value.
func TestUseLockSupportsRepeatedApplies(t *testing.T) {
	root := t.TempDir()
	holder, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := holder.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		s, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UseLock(info.ID); err != nil {
			t.Fatalf("UseLock #%d: %v", i, err)
		}
		set := NewFileSet()
		add(t, set, Managed("f.txt"), []byte("x"))
		if err := s.Materialize("managed", set); err != nil {
			t.Fatalf("materialize #%d under a held lock: %v", i, err)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("the held lock did not survive two applies: %v", err)
	}
	if err := holder.Unlock(info.ID); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// The decisive case for the P1 this fixes: UseLock verifies the held lock
// before the transaction lock exists at all, so between that read and
// Materialize acquiring .rdk/apply.lock, the held lock can be released and
// replaced by a different one — and the stale adopter must not publish
// believing it still excludes everyone, while the new holder believes the
// same thing. Three independent Store values opened on the same root, the
// way three separate `rdk` invocations would be: holder1 takes lock A,
// adopter reads and adopts it, then A is released and holder2 takes a fresh
// lock B before adopter ever calls Materialize. Using the real HoldLock/
// Unlock/UseLock surface rather than writing lock files directly keeps this
// honest about what a real sequence of commands produces.
func TestMaterializeRefusesAnAdoptedLockThatWasReplaced(t *testing.T) {
	root := t.TempDir()
	holder1, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	heldA, err := holder1.HoldLock("first holder")
	if err != nil {
		t.Fatal(err)
	}

	adopter, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adopter.UseLock(heldA.ID); err != nil {
		t.Fatal(err)
	}

	// The window: A goes away and B takes its place before adopter's
	// Materialize ever runs.
	if err := holder1.Unlock(heldA.ID); err != nil {
		t.Fatal(err)
	}
	holder2, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	heldB, err := holder2.HoldLock("second holder")
	if err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err = adopter.Materialize("managed", set)
	if err == nil {
		t.Fatal("materialize published under a stale adopted lock")
	}
	// B is genuinely blocking this run now, so this is exactly the shape any
	// other apply colliding with a held lock produces — same ErrLocked, same
	// holder details, just discovered by the re-check instead of the
	// pre-acquire path.
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want it to wrap ErrLocked (lock B, the one actually blocking this run)", err)
	}
	if !strings.Contains(err.Error(), heldB.ID) {
		t.Errorf("error %q does not name lock B (%s)", err.Error(), heldB.ID)
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the replaced lock")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("lock B did not survive the refused adopted apply: %v", err)
	}
}

// The residual half of the same bug: the adopted lock can simply vanish
// (rdk unlock, with nothing replacing it) instead of being replaced. Also
// must refuse: Materialize was told "run under lock A" and A no longer
// exists, so there is nothing left for --with-lock's promise to mean.
func TestMaterializeRefusesAnAdoptedLockThatWasUnlocked(t *testing.T) {
	root := t.TempDir()
	holder, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	held, err := holder.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}

	adopter, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adopter.UseLock(held.ID); err != nil {
		t.Fatal(err)
	}

	if err := holder.Unlock(held.ID); err != nil {
		t.Fatal(err)
	}

	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	err = adopter.Materialize("managed", set)
	if err == nil {
		t.Fatal("materialize published under an adopted lock that no longer exists")
	}
	// Distinct from the replaced case: nothing holds the repository right
	// now, so wrapping ErrLocked ("another rdk apply holds this repository")
	// would be a claim this state doesn't support.
	if errors.Is(err, ErrLocked) {
		t.Errorf("err = %v wraps ErrLocked, but nothing holds the repository to describe", err)
	}
	if !strings.Contains(err.Error(), held.ID) {
		t.Errorf("error %q does not name the lock that vanished", err.Error())
	}
	if _, err := os.Stat(filepath.Join(root, "managed")); !os.IsNotExist(err) {
		t.Error("published despite the vanished lock")
	}
}

// The shape of the race between Materialize's defer and the SIGINT/SIGTERM
// handler in cmd: both call ReleaseLock on the same Store value, and in
// production one of them (the handler) calls os.Exit right after. That
// os.Exit is what turns a merely-late removal into a stranded lock, and it
// cannot be reproduced honestly inside a test process without actually
// exiting it — so this instead drives many concurrent ReleaseLock calls
// against one acquired lock and asserts the invariant the mutex-held-across-
// the-remove fix is supposed to guarantee: every call returns nil, and the
// lock file is gone once they have all returned. Run with -race, this also
// confirms lockHeld/lockID are never touched outside lockMu.
//
// What this does and does not prove: it proves ReleaseLock is safe to call
// concurrently from multiple goroutines and that the lock always ends up
// removed — which is the property that makes the fix correct regardless of
// which caller happens to do the removing. It does not prove the specific
// bad interleaving from the bug report (clear the flag, get descheduled,
// second caller no-ops, first caller's Remove never runs because the process
// already exited) used to occur on the old code, since forcing that exact
// schedule would need control over the Go scheduler this test does not have
// — that claim rests on reading the old code (the flag was cleared before
// the unlocked read-and-remove) rather than on reproducing it live.
func TestReleaseLockConcurrentCallsAlwaysRemoveTheLock(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.ReleaseLock()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: ReleaseLock returned %v, want nil", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply lock file survived concurrent ReleaseLock calls")
	}
}

// Releasing must not remove a lock this process did not take. Otherwise a
// broken-and-replaced lock gets deleted by the run whose lock was broken,
// letting a third apply start alongside the live one.
func TestReleaseLockLeavesAReplacedLockAlone(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("f.txt"), []byte("x"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	// Materialize already released on the way out, so put the store back into
	// "believes it holds lock X" the same way acquireLock would have, rather
	// than trying to catch it mid-flight. Reaching into unexported fields is
	// fine: this test is in-package and nothing here is exported for its sake.
	st := s.(*osStore)
	st.lockMu.Lock()
	st.lockHeld = true
	st.lockID = "stale-id-this-run-took"
	st.lockMu.Unlock()

	// Someone else broke that lock and a third run acquired a fresh one. Has
	// to be the transaction lock: ReleaseLock only ever reads scratchApplyLock
	// now, so planting the replacement in scratchLock would make this test
	// pass for the wrong reason (ReleaseLock never even looking there) rather
	// than for the id check actually working.
	writeApplyLock(t, root, sampleLock(lockKindApply))

	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Errorf("released a lock it did not take: %v", err)
	}
}

// The property that matters most: two `rdk apply --with-lock=<same id>`
// processes must not both proceed to Materialize at once. Each --with-lock
// apply opens its own Store (cmd/apply.go calls repofs.New per invocation),
// so this drives several independent *osStore values that have all already
// adopted the same held lock via UseLock — exactly what --with-lock does —
// and calls Materialize on all of them at the same instant, each generating
// a different, internally-tagged tree. The synchronisation under test,
// O_CREAT|O_EXCL on .rdk/apply.lock, is a real syscall against the real
// filesystem regardless of whether the racing callers are separate
// goroutines in one process or separate processes; nothing about the
// mechanism being exercised is faked or mocked.
//
// This does not assert "exactly one call succeeds": --with-lock's whole
// point (TestUseLockSupportsRepeatedApplies) is that the holder can apply
// repeatedly, and if one goroutine's acquire-write-release cycle finishes
// before another even attempts to acquire, that second one legitimately
// succeeds too — sequential, not concurrent, and not a bug. Asserting a
// fixed success count made an earlier version of this test flaky by
// construction. What must never happen, at any success count, is two
// Materialize calls being inside the acquire..release window at once — the
// original bug, where two runs staged into the same .rdk/new and published a
// mixture of both. This test proves that by giving each generation a lot of
// files sharing one tag, and checking that whatever is on disk once every
// goroutine has returned is exactly one generation's complete output — never
// a mixture of tags, and never a partial file count, which is what
// overlapping writers would leave.
//
// What it does not prove: the precise scheduling two real OS processes would
// hit — the kernel interleaves processes differently than the Go runtime
// interleaves goroutines — or anything about SIGINT/SIGTERM handling, which
// lives in cmd and is exercised by hand instead (see the task's verification
// transcripts). What is under test is the file-level exclusion itself, and
// that does not depend on which kind of caller is racing it: O_CREAT|O_EXCL
// is a single atomic kernel operation either way, and this test's tag check
// would catch a corrupted result regardless of how many callers actually won
// the race.
func TestMaterializeUnderAdoptedLockNeverLetsTwoApplyRunsBothProceed(t *testing.T) {
	root := t.TempDir()
	holder, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	held, err := holder.HoldLock("agent refactoring the s3-bucket module")
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	const filesPerGen = 300 // wide enough to give a broken lock a real chance to interleave
	stores := make([]Store, n)
	sets := make([]*FileSet, n)
	for i := range stores {
		s, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UseLock(held.ID); err != nil {
			t.Fatalf("UseLock #%d: %v", i, err)
		}
		stores[i] = s

		tag := []byte(fmt.Sprintf("gen-%d", i))
		set := NewFileSet()
		for f := 0; f < filesPerGen; f++ {
			add(t, set, Managed(fmt.Sprintf("f%03d.txt", f)), tag)
		}
		sets[i] = set
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = stores[i].Materialize("managed", sets[i])
		}(i)
	}
	close(start)
	wg.Wait()

	successes, lockedErrs := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrLocked):
			lockedErrs++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes == 0 {
		t.Fatal("no run ever acquired the transaction lock")
	}
	if successes+lockedErrs != n {
		t.Errorf("successes(%d) + lockedErrs(%d) != n(%d) — an error other than nil or ErrLocked came back", successes, lockedErrs, n)
	}

	// The property that matters: whatever ended up on disk is exactly one
	// generation's complete output. Two overlapping Materialize calls would
	// leave a mixture of tags (files from more than one generation) or a
	// short count (one generation's files partially overwritten by another
	// mid-write) — either is exactly the corruption the lock exists to
	// prevent.
	entries, err := os.ReadDir(filepath.Join(root, "managed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != filesPerGen {
		t.Errorf("managed dir has %d entries, want %d — a sign of a partial write from an overlapping run", len(entries), filesPerGen)
	}
	tags := map[string]int{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, "managed", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tags[string(b)]++
	}
	if len(tags) != 1 {
		t.Errorf("managed dir contains a mixture of generations: %v", tags)
	}

	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("apply.lock survived every Materialize call returning")
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("the held lock did not survive: %v", err)
	}
	if err := holder.Unlock(held.ID); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// A dangling symlink at the held lock's path used to read as "no lock is
// held", so the exclusion silently stopped excluding — the exact failure
// flock was rejected for, reached through a different door.
func TestMaterializeRefusesADanglingSymlinkAtTheHeldLock(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, scratchLock)); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLockTarget) {
		t.Fatalf("Materialize err = %v, want ErrLockTarget", err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %q, want it to name what is in the way", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "managed")); !os.IsNotExist(statErr) {
		t.Error("published a tree while the held lock path was unusable")
	}
}

// The transaction lock's path has the mirror problem: the exclusive create
// sees the symlink (so applies block forever) but every reader says it is
// absent, so no id is ever printed and nothing can name it to break it.
func TestMaterializeRefusesADanglingSymlinkAtTheApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, scratchApplyLock)); err != nil {
		t.Fatal(err)
	}
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	if err := s.Materialize("managed", set); !errors.Is(err, ErrLockTarget) {
		t.Fatalf("Materialize err = %v, want ErrLockTarget", err)
	}
}

// A directory at a lock path is user-fixable state, so it must not surface as
// "an rdk bug. Report it".
func TestHoldLockRefusesADirectoryAtTheLockPath(t *testing.T) {
	s, root := newTestStore(t)
	if err := os.MkdirAll(filepath.Join(root, scratchLock), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.HoldLock("work")
	if !errors.Is(err, ErrLockTarget) {
		t.Fatalf("HoldLock err = %v, want ErrLockTarget", err)
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("err = %q, want it to name what is in the way", err)
	}
}

// Every entry point that reads a lock has to refuse the same way, or one of
// them becomes the door the others are guarding.
func TestEveryLockEntryPointRefusesAnUnusableLockPath(t *testing.T) {
	for _, file := range []string{scratchLock, scratchApplyLock} {
		t.Run(file, func(t *testing.T) {
			s, root := newTestStore(t)
			if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("nowhere", filepath.Join(root, file)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BreakLock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("BreakLock err = %v, want ErrLockTarget", err)
			}
			if err := s.Unlock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("Unlock err = %v, want ErrLockTarget", err)
			}
			if _, err := s.UseLock("deadbeef"); !errors.Is(err, ErrLockTarget) {
				t.Errorf("UseLock err = %v, want ErrLockTarget", err)
			}
		})
	}
}

// The file a lock was read from is what decides how it is described — not the
// kind field inside it, which a pre-split binary (or a hand-edited file) can
// contradict.
func TestReadLockFileTrustsThePathOverTheRecordedKind(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	writeLock(t, root, LockInfo{ID: "aaaa", Kind: lockKindApply, PID: 1, Host: "h", Since: "s"})
	info, err := st.readLockFile(scratchLock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != lockKindHeld {
		t.Errorf("Kind = %q, want %q — the file it came from is authoritative", info.Kind, lockKindHeld)
	}
	if info.Path != scratchLock {
		t.Errorf("Path = %q, want %q", info.Path, scratchLock)
	}
}

// The mirror of the test above: the override runs the same way regardless of
// which direction the JSON disagrees with the file, since lockKindFor(file)
// never consults the record at all.
func TestReadLockFileTrustsThePathOverTheRecordedKindTheOtherWay(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	writeApplyLock(t, root, LockInfo{ID: "bbbb", Kind: lockKindHeld, PID: 2, Host: "h", Since: "s"})
	info, err := st.readLockFile(scratchApplyLock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != lockKindApply {
		t.Errorf("Kind = %q, want %q — the file it came from is authoritative", info.Kind, lockKindApply)
	}
	if info.Path != scratchApplyLock {
		t.Errorf("Path = %q, want %q", info.Path, scratchApplyLock)
	}
}

// The lock file must never be observable in a half-written state: a
// zero-length lock blocks every apply while carrying no id, so --break-lock
// has nothing to name and the printed recovery cannot be typed.
func TestLockFileIsCompleteTheInstantItIsVisible(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	finished := make(chan struct{})
	bad := make(chan string, 1)
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(root, ScratchDir, "apply.lock"))
			if err != nil {
				continue // absent is fine; partial is not
			}
			var info LockInfo
			if err := json.Unmarshal(b, &info); err != nil || info.ID == "" {
				select {
				case bad <- string(b):
				default:
				}
				return
			}
		}
	}()

	for i := 0; i < 200; i++ {
		if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.ReleaseLock(); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	// Joined before checking bad: without this, the poller can still be
	// between detecting a partial file and sending on bad when the
	// non-blocking read below runs, which would only ever under-report a
	// real failure, never manufacture one — but under-reporting is still
	// worth closing.
	<-finished
	select {
	case b := <-bad:
		t.Fatalf("observed a lock file that was not a complete record: %q", b)
	default:
	}
}

// Ownership has to be recorded before the lock becomes visible, or a signal
// arriving in between leaves a complete lock the handler declines to remove
// because it does not know it is its own.
func TestAcquireLockRecordsOwnershipBeforeTheLockIsVisible(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	seen := make(chan bool, 1)
	afterLockOwnershipRecorded = func() {
		_, err := os.Lstat(filepath.Join(root, ScratchDir, "apply.lock"))
		seen <- os.IsNotExist(err)
	}
	t.Cleanup(func() { afterLockOwnershipRecorded = nil })

	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	if !<-seen {
		t.Error("the lock file was already visible when ownership was recorded")
	}
	st.lockMu.Lock()
	held, id := st.lockHeld, st.lockID
	st.lockMu.Unlock()
	if !held || id == "" {
		t.Errorf("lockHeld/lockID = %v/%q, want ownership recorded", held, id)
	}
}

// The property TestAcquireLockRecordsOwnershipBeforeTheLockIsVisible cannot
// show on its own: that lockMu stays held all the way to the Link, so a
// ReleaseLock racing the exact instant ownership is recorded is excluded
// until the lock actually exists to be released. The seam fires with lockMu
// held, so a ReleaseLock launched from inside it can only start running once
// this call has released — which, if the window were still open (lockMu
// dropped right after the two field assignments, as an earlier version of
// this fix still did), would not be true: that release would run immediately,
// find nothing on disk yet, conclude there was nothing of this call's to
// remove, and return — leaving the lock this call is about to create
// permanently unreleased once the Link does land.
func TestAcquireLockCannotBeStrandedByAConcurrentRelease(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}

	releaseDone := make(chan error, 1)
	afterLockOwnershipRecorded = func() {
		go func() { releaseDone <- st.ReleaseLock() }()
	}
	t.Cleanup(func() { afterLockOwnershipRecorded = nil })

	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	// The concurrent release could only have run after the Link — lockMu
	// excluded it until then — so it saw the real, complete file and
	// actually removed it. If the window were open, it would have run
	// early, removed nothing, and this file would still be here with
	// nothing left tracking it as this store's to release.
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); !os.IsNotExist(err) {
		t.Error("lock file survived the concurrent ReleaseLock: it ran before the lock was fully recorded and is now stranded")
	}
}

// Losing the exclusive-create race must leave no ownership behind: the lock
// on disk is someone else's, and a release that believed otherwise would
// remove a live lock.
func TestAcquireLockClearsOwnershipWhenItLosesTheRace(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	writeApplyLock(t, root, sampleLock(lockKindApply))
	if _, err := st.acquireLock(scratchApplyLock, ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("acquireLock err = %v, want ErrLocked", err)
	}
	st.lockMu.Lock()
	held := st.lockHeld
	st.lockMu.Unlock()
	if held {
		t.Error("ownership survived a lost race; a release would remove someone else's live lock")
	}
	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Error("ReleaseLock removed a lock this store never acquired")
	}
}

// Only the transaction lock is ever this store's to release. Recording a held
// lock's id in the same fields would make the deferred release in Task 4's
// HoldLock compare the wrong id and strand the transaction lock.
func TestAcquireLockDoesNotClaimOwnershipOfAHeldLock(t *testing.T) {
	s, _ := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchLock, "work"); err != nil {
		t.Fatal(err)
	}
	st.lockMu.Lock()
	held := st.lockHeld
	st.lockMu.Unlock()
	if held {
		t.Error("a held lock was recorded as this store's transaction lock")
	}
}

// The scratch dir must not accumulate the temporary records lock creation
// writes through.
func TestAcquireLockLeavesNoTemporaryFiles(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ScratchDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".gitignore" {
			t.Errorf("scratch dir holds %q, want only .gitignore", e.Name())
		}
	}
}

// The corruption this exists to stop: --break-lock on a live transaction
// lock, which the blocked-apply error actively instructs the user to do.
// Nothing may be published after this run's claim is gone.
func TestMaterializeAbortsWhenItsLockIsBrokenBeforePublishing(t *testing.T) {
	s, root := newTestStore(t)
	first := NewFileSet()
	add(t, first, Managed("keep.txt"), []byte("original"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}

	afterStaging = func() {
		if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterStaging = nil })

	second := NewFileSet()
	add(t, second, Managed("new.txt"), []byte("replacement"))
	err := s.Materialize("managed", second)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("Materialize err = %v, want ErrLockLost", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "managed", "keep.txt"))
	if readErr != nil {
		t.Fatalf("the published tree was disturbed after the lock was lost: %v", readErr)
	}
	if string(got) != "original" {
		t.Errorf("keep.txt = %q, want the tree left exactly as it was", got)
	}
}

// A lock that was broken and replaced is the same abort, and the message has
// to name the lock that holds the repository now — that is the id the reader
// needs, not the dead one this run was carrying.
func TestMaterializeAbortsWhenItsLockWasBrokenAndReplaced(t *testing.T) {
	s, root := newTestStore(t)
	afterStaging = func() {
		if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
			t.Error(err)
		}
		writeApplyLock(t, root, LockInfo{ID: "beefbeefbeefbeef", Kind: lockKindApply, PID: 99, Host: "other", Since: "2026-08-16T00:00:00Z"})
	}
	t.Cleanup(func() { afterStaging = nil })

	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	err := s.Materialize("managed", set)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("Materialize err = %v, want ErrLockLost", err)
	}
	if !strings.Contains(err.Error(), "beefbeefbeefbeef") {
		t.Errorf("err = %q, want it to name the lock that holds the repository now", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "managed")); !os.IsNotExist(statErr) {
		t.Error("published a tree after this run's lock was replaced")
	}
}

// The revalidation must not fire on the ordinary path, where nothing has
// touched the lock.
func TestMaterializeStillSucceedsWhenItsLockIsUntouched(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	add(t, set, Managed("a.txt"), []byte("a"))
	if err := s.Materialize("managed", set); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed", "a.txt")); err != nil {
		t.Fatal(err)
	}
}

// checkStillLocked's message must not claim "nothing was published" once
// this run's own publishing rename has already succeeded — that claim would
// be false at the call site right before the sweep, which only ever runs
// after the publish. Exercised directly against checkStillLocked rather than
// through Materialize: reaching that call site with a broken lock needs a
// seam between publish and the sweep, and that seam (afterPublish) belongs to
// a later task in this series, not this one.
func TestCheckStillLockedDoesNotClaimNothingWasPublishedAfterItWas(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Fatal(err)
	}

	// Fresh-repo shape, deliberately: no prior Materialize ever ran here, so
	// .rdk/old was never populated — the displacing rename that would put a
	// previous tree there only runs when managedDir already existed. A
	// message that claimed a previous tree was "left in place" would be false
	// in exactly this shape, which is why the assertions below check for that
	// specifically rather than just checking for *some* mention of .rdk/old.
	if _, statErr := os.Stat(filepath.Join(root, ScratchDir, "old")); !os.IsNotExist(statErr) {
		t.Fatalf(".rdk/old already exists; this test needs it absent to prove the message doesn't assume it exists")
	}

	err := st.checkStillLocked(true)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("checkStillLocked(true) err = %v, want ErrLockLost", err)
	}
	if strings.Contains(err.Error(), "nothing was published") {
		t.Errorf("err = %q, claims nothing was published after the publish already succeeded", err)
	}
	if !strings.Contains(err.Error(), "already published") {
		t.Errorf("err = %q, want it to say this run's own tree was already published", err)
	}
	if strings.Contains(err.Error(), "previous tree") || strings.Contains(err.Error(), "was left in place") {
		t.Errorf("err = %q, asserts a previous tree exists at .rdk/old when this run never created one", err)
	}
	if !strings.Contains(err.Error(), "skipped") {
		t.Errorf("err = %q, want it to say the sweep was skipped rather than claim what state .rdk/old is in", err)
	}
}

// The same call, before publishing, must still say nothing was published —
// confirming the two messages actually differ rather than one silently
// subsuming the other.
func TestCheckStillLockedClaimsNothingWasPublishedBeforeItWas(t *testing.T) {
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ScratchDir, "apply.lock")); err != nil {
		t.Fatal(err)
	}

	err := st.checkStillLocked(false)
	if !errors.Is(err, ErrLockLost) {
		t.Fatalf("checkStillLocked(false) err = %v, want ErrLockLost", err)
	}
	if !strings.Contains(err.Error(), "nothing was published") {
		t.Errorf("err = %q, want it to say nothing was published", err)
	}
}

// After publishing, an unexpected failure while re-checking the lock (not
// "gone", not "replaced" — something readLockFile could not even classify)
// must not fall through to apply.Run's generic write-managed-dir fallback:
// the write already succeeded, so "cannot write" would be false. Reported as
// ErrSweep instead, because the consequence and the fix are identical to a
// sweep that genuinely failed to clear .rdk/old.
func TestCheckStillLockedAfterPublishReportsAnUnexpectedReadFailureAsASweepFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ScratchDir, "apply.lock")
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(lockPath, 0o644) })

	err := st.checkStillLocked(true)
	if !errors.Is(err, ErrSweep) {
		t.Fatalf("checkStillLocked(true) err = %v, want ErrSweep", err)
	}
	if errors.Is(err, ErrLockLost) {
		t.Errorf("err = %v, an unexplained read failure is not a confirmed lock loss and must not claim to be one", err)
	}
}

// The same failure before publishing is left bare and unclassified: the
// generic write-managed-dir fallback it reaches in apply.Run is not making a
// false claim there, since nothing has been written to the user's tree yet.
func TestCheckStillLockedBeforePublishLeavesAnUnexpectedReadFailureUnclassified(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s, root := newTestStore(t)
	st := s.(*osStore)
	if err := st.ensureScratchDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.acquireLock(scratchApplyLock, ""); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ScratchDir, "apply.lock")
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(lockPath, 0o644) })

	err := st.checkStillLocked(false)
	if err == nil {
		t.Fatal("want an error when the lock file cannot be read")
	}
	if errors.Is(err, ErrSweep) || errors.Is(err, ErrLockLost) {
		t.Errorf("checkStillLocked(false) err = %v, want a bare unclassified error, not ErrSweep or ErrLockLost", err)
	}
}
