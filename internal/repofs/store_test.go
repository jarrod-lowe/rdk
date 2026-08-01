package repofs

import (
	"errors"
	"os"
	"path/filepath"
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

func TestMaterializeWritesTreeWithDirs(t *testing.T) {
	s, root := newTestStore(t)
	set := NewFileSet()
	set.Bytes("a.txt", []byte("a"))
	set.Bytes("sub/b.txt", []byte("b"))
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
	first.Bytes("stale.txt", []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
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
	set.Bytes("f.txt", []byte("x"))
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
	set.Bytes("f.txt", []byte("x"))
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
	set.Bytes("f.txt", []byte("x"))
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
	first.Bytes("stale.txt", []byte("old"))
	if err := s.Materialize("managed", first); err != nil {
		t.Fatal(err)
	}
	second := NewFileSet()
	second.Bytes("fresh.txt", []byte("new"))
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
	set.Bytes("f.txt", []byte("x"))
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
	first.Bytes("sub/f.txt", []byte("old"))
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
	second.Bytes("fresh.txt", []byte("new"))
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
	first.Bytes("f.txt", []byte("old"))
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
	second.Bytes("fresh.txt", []byte("new"))
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
