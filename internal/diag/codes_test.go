package diag

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// An error code is a contract the moment anything matches on it, so two codes
// must never collide.
func TestCodesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
}

func TestCodesAreNonEmpty(t *testing.T) {
	for i, c := range all {
		if strings.TrimSpace(c) == "" {
			t.Errorf("all[%d] is empty", i)
		}
	}
}

// An undocumented error code is worse than no code: it looks like a contract
// and isn't one.
func TestEveryCodeIsDocumented(t *testing.T) {
	body, err := os.ReadFile("../../docs/errors.md")
	if err != nil {
		t.Fatalf("reading docs/errors.md: %v", err)
	}
	doc := string(body)
	for _, c := range all {
		if !strings.Contains(doc, "`"+c+"`") {
			t.Errorf("code %q is not documented in docs/errors.md", c)
		}
	}
}

// The forward check catches a code missing from the page; this catches a row
// left behind by a rename, which is the same lie in the other direction.
func TestEveryDocumentedCodeExists(t *testing.T) {
	body, err := os.ReadFile("../../docs/errors.md")
	if err != nil {
		t.Fatalf("reading docs/errors.md: %v", err)
	}
	known := map[string]bool{}
	for _, c := range all {
		known[c] = true
	}
	// Anchor on the table's first cell so prose backticks are not mistaken
	// for code rows.
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|")
	for _, m := range row.FindAllStringSubmatch(string(body), -1) {
		if !known[m[1]] {
			t.Errorf("docs/errors.md documents %q, which is not a code", m[1])
		}
	}
}
