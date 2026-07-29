package diag

import (
	"os"
	"strings"
	"testing"
)

// An error code is a contract the moment anything matches on it, so two codes
// must never collide.
func TestCodesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range All {
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
}

func TestCodesAreNonEmpty(t *testing.T) {
	for i, c := range All {
		if strings.TrimSpace(c) == "" {
			t.Errorf("All[%d] is empty", i)
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
	for _, c := range All {
		if !strings.Contains(doc, "`"+c+"`") {
			t.Errorf("code %q is not documented in docs/errors.md", c)
		}
	}
}
