package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Writing to a stream directly is how "one way to output" decays: the first
// hurried commit adds a fmt.Println, and the JSONL mode quietly stops being
// complete. internal/logger is the one place allowed to touch a stream.
var streamWriters = []string{
	"fmt.Print",
	"fmt.Fprint",
	"os.Stdout",
	"os.Stderr",
	"println(",
}

func TestOnlyTheLoggerWritesToTheStreams(t *testing.T) {
	skipDirs := map[string]bool{".git": true, "testdata": true, "docs": true}

	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// The logger is the exception: it is what everything else routes through.
		if strings.HasPrefix(filepath.ToSlash(p), "internal/logger/") {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, w := range streamWriters {
				if strings.Contains(line, w) {
					t.Errorf("%s:%d writes to a stream directly (%s); use internal/logger", p, i+1, w)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
