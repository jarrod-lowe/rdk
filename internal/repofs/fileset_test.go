package repofs

import (
	"strings"
	"testing"
)

func TestFileSetBytesAndPaths(t *testing.T) {
	s := NewFileSet()
	s.Bytes("b/z.txt", []byte("z"))
	s.Bytes("a.txt", []byte("a"))
	got := s.sortedPaths()
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "b/z.txt" {
		t.Fatalf("sortedPaths = %v, want [a.txt b/z.txt]", got)
	}
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
	if string(s.content["a.txt"]) != "a" {
		t.Errorf("content mismatch")
	}
}

func TestFileSetJSONDeterministic(t *testing.T) {
	s := NewFileSet()
	if err := s.JSON("m.json", map[string]any{"b": "~> 6.0", "a": 1}); err != nil {
		t.Fatal(err)
	}
	out := string(s.content["m.json"])
	want := "{\n  \"a\": 1,\n  \"b\": \"~\\u003e 6.0\"\n}\n"
	if out != want {
		t.Errorf("JSON =\n%q\nwant\n%q", out, want)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("JSON must end with newline")
	}
}
