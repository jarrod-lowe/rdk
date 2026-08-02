package repofs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// writeLock plants a lock file the way another process would have.
func writeLock(t *testing.T, root string, info LockInfo) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ScratchDir), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ScratchDir, "lock"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func heldLock() LockInfo {
	return LockInfo{
		ID: "9f3a1c4e7b2d8a05", Kind: "apply", Host: "builder-3",
		PID: 4127, Since: "2026-08-02T10:04:11Z",
	}
}

// Two applies in one checkout used to interleave on the fixed scratch names
// and publish a mixture of both runs' files.
func TestMaterializeRefusesWhileLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())

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
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock survived a successful apply")
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
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); !os.IsNotExist(err) {
		t.Error("lock survived a failed apply")
	}
}

// The id is the whole safety property: between reading an error and typing the
// recovery, the stranded lock may have been replaced by a live one.
func TestBreakLockOnlyRemovesTheNamedLock(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())

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

func TestHoldLockRefusesWhenAlreadyLocked(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock())
	if _, err := s.HoldLock("second"); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
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
// for, and it warns.
func TestUnlockRefusesAnApplyLock(t *testing.T) {
	s, root := newTestStore(t)
	writeLock(t, root, heldLock()) // kind: apply
	err := s.Unlock("9f3a1c4e7b2d8a05")
	if err == nil {
		t.Fatal("unlocked an apply lock")
	}
	if !strings.Contains(err.Error(), "--break-lock") {
		t.Errorf("error %q does not point at the right door", err.Error())
	}
}

// Running under someone's lock must neither take nor release it.
func TestUseLockRunsWithoutAcquiringOrReleasing(t *testing.T) {
	s, root := newTestStore(t)
	info, err := s.HoldLock("working")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UseLock(info.ID); err != nil {
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
	if _, err := os.Stat(filepath.Join(root, "managed", "f.txt")); err != nil {
		t.Errorf("apply did not publish: %v", err)
	}
}

func TestUseLockRejectsAMismatchedOrAbsentLock(t *testing.T) {
	s, root := newTestStore(t)
	// Nothing held: you asserted you hold a lock and you do not, which means
	// it was broken out from under you.
	if err := s.UseLock("9f3a1c4e7b2d8a05"); err == nil {
		t.Error("adopted a lock that does not exist")
	}
	writeLock(t, root, heldLock())
	if err := s.UseLock("some-other-id"); err == nil {
		t.Error("adopted someone else's lock under the wrong id")
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

	// Someone else broke that lock and a third run acquired a fresh one.
	writeLock(t, root, heldLock())

	if err := s.ReleaseLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ScratchDir, "lock")); err != nil {
		t.Errorf("released a lock it did not take: %v", err)
	}
}
