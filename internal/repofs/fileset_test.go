package repofs

import (
	"strings"
	"testing"
)

func TestFileSetBytesAndPaths(t *testing.T) {
	s := NewFileSet()
	if err := s.Bytes(Managed("b/z.txt"), []byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := s.Bytes(Managed("a.txt"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	got := s.sortedPaths()
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b/z.txt" {
		t.Fatalf("sortedPaths = %v, want [a.txt b/z.txt]", got)
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
	if string(s.managedBytes("a.txt")) != "a" {
		t.Errorf("content mismatch")
	}
}

func TestFileSetJSONDeterministic(t *testing.T) {
	s := NewFileSet()
	if err := s.JSON(Managed("m.json"), map[string]any{"b": "~> 6.0", "a": 1}); err != nil {
		t.Fatal(err)
	}
	out := string(s.managedBytes("m.json"))
	want := "{\n  \"a\": 1,\n  \"b\": \"~\\u003e 6.0\"\n}\n"
	if out != want {
		t.Errorf("JSON =\n%q\nwant\n%q", out, want)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("JSON must end with newline")
	}
}

// A key that climbs would resolve outside the directory being materialized.
// Nothing can supply one today; the point is that it stops being possible.
func TestFileSetRejectsEscapingPaths(t *testing.T) {
	for _, p := range []string{"", ".", "/abs.txt", "../up.txt", "a/../../up.txt", "./a.txt", "a//b.txt", "a/"} {
		set := NewFileSet()
		if err := set.Bytes(Managed(p), []byte("x")); err == nil {
			t.Errorf("Managed(%q) was accepted", p)
		}
		if err := set.Bytes(AtRepoRoot(p), []byte("x")); err == nil {
			t.Errorf("AtRepoRoot(%q) was accepted", p)
		}
	}
}

// Requiring already-clean paths means what a reader sees in the source is what
// lands on disk, with no normalisation step in between to reason about.
func TestFileSetRejectsUncleanPaths(t *testing.T) {
	set := NewFileSet()
	if err := set.Bytes(Managed("terraform/./main.tf.json"), []byte("x")); err == nil {
		t.Error("an unclean path was accepted")
	}
}

func TestFileSetAcceptsOrdinaryPaths(t *testing.T) {
	set := NewFileSet()
	for _, p := range []string{"README.md", "terraform/main.tf.json", "a/b/c.txt"} {
		if err := set.Bytes(Managed(p), []byte("x")); err != nil {
			t.Errorf("Managed(%q): %v", p, err)
		}
	}
	if err := set.Bytes(AtRepoRoot(".github/workflows/ci.yml"), []byte("x")); err != nil {
		t.Errorf("AtRepoRoot: %v", err)
	}
}

// Two names for one location: the manifest would end up tracking a file that
// gets wiped every apply (DD-14's scope decision).
func TestAtRepoRootRejectsPathsInsideRdkOwnedDirs(t *testing.T) {
	set := NewFileSet()
	for _, p := range []string{"rdk-managed/x.txt", ScratchDir + "/x.txt", ScratchDir} {
		if err := set.Bytes(AtRepoRoot(p), []byte("x")); err == nil {
			t.Errorf("AtRepoRoot(%q) was accepted", p)
		}
	}
}

func TestJSONValidatesItsPathToo(t *testing.T) {
	set := NewFileSet()
	if err := set.JSON(Managed("../escape.json"), map[string]any{}); err == nil {
		t.Error("JSON accepted an escaping path")
	}
}
