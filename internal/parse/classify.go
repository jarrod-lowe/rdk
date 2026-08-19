package parse

import (
	"fmt"
	"strings"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

// rdk/ is a definitions directory, not a general-purpose folder: a file rdk
// cannot process is a mistake worth surfacing, since the alternative is
// resources that silently never get generated. Three named exemptions below
// keep that rule from being hostile in practice — each one warns rather than
// erroring, but none of them is silent.

// setAside suffixes park a definition without deleting it. Deliberate, so
// allowed — but they change what rdk generates, so never silent.
var setAside = []string{".disabled", ".example"}

// artifacts are leftovers from editors and merges. Nobody chooses to create
// them, so they do not block an apply; a stale .orig may still hold work
// someone wants, so they are not silent either.
var artifacts = []string{".orig", ".rej", ".bak", "~"}

// machineMade names the files that appear in a directory without anyone
// choosing to put them there: the OS writes .DS_Store and Thumbs.db, vim
// writes .foo.yaml.swp while editing, and .gitignore/.gitkeep are how a
// directory is kept in git. They are exempt from being errors, but not from
// being mentioned — rdk/ holds definitions, and anything else in it is worth
// one line of output (rule 6).
var machineMade = []string{".gitignore", ".gitkeep", ".DS_Store", "Thumbs.db"}

// classify decides what a non-.yaml entry means: a warning to report, or an
// error if the entry cannot be processed. Every path through it returns one
// or the other — rdk/ holds definitions, and nothing in it is silently
// skipped.
func classify(name string) (diag.Diagnostic, error) {
	// A .yml file is an attempt to write a definition that would otherwise be
	// dropped in full, so it names its own fix rather than warning.
	if strings.HasSuffix(name, ".yml") {
		return diag.Diagnostic{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeWrongExtension,
			File:    name,
			Summary: "rdk definitions must use the .yaml extension",
			Hint:    "rename it to " + strings.TrimSuffix(name, ".yml") + ".yaml",
		})
	}
	if suffix, matched := matchSuffix(name, setAside); matched {
		return diag.Diagnostic{
			Code:    diag.CodeSetAside,
			File:    name,
			Summary: fmt.Sprintf("ignored (%s); rdk generates nothing for it", suffix),
			Hint:    "rename it to .yaml to enable it",
		}, nil
	}
	if suffix, matched := matchSuffix(name, artifacts); matched {
		return diag.Diagnostic{
			Code:    diag.CodeEditorArtifact,
			File:    name,
			Summary: fmt.Sprintf("ignored (%s leftover)", suffix),
			Hint:    "delete it or move it out of the definitions dir",
		}, nil
	}
	if isMachineMade(name) {
		return diag.Diagnostic{
			Code:    diag.CodeMachineFile,
			File:    name,
			Summary: "ignored; rdk generates nothing for it",
			Hint:    "delete it or move it out of the definitions dir",
		}, nil
	}
	return diag.Diagnostic{}, diag.New(diag.Diagnostic{
		Code:    diag.CodeUnprocessableFile,
		File:    name,
		Summary: "rdk cannot process this file; the definitions dir takes .yaml definitions only",
		Hint: fmt.Sprintf("to park one, suffix it %s; anything else belongs outside the definitions dir",
			strings.Join(setAside, " or ")),
	})
}

// isMachineMade reports whether name is one of the exact machineMade names,
// or looks like a vim swap file (a leading dot, plus a .sw + one letter
// suffix: .swp, .swo, .swn). Everything else hidden — a leading dot on an
// otherwise ordinary name — is not special-cased here, so it falls through to
// the same rules as a visible file of the same name.
func isMachineMade(name string) bool {
	for _, m := range machineMade {
		if name == m {
			return true
		}
	}
	return isVimSwapFile(name)
}

func isVimSwapFile(name string) bool {
	if !strings.HasPrefix(name, ".") || len(name) < 4 {
		return false
	}
	stem, last := name[:len(name)-1], name[len(name)-1]
	return strings.HasSuffix(stem, ".sw") && last >= 'a' && last <= 'z'
}

// matchSuffix reports the first matching suffix, so the warning can name the
// one that applied.
func matchSuffix(name string, suffixes []string) (string, bool) {
	for _, s := range suffixes {
		if strings.HasSuffix(name, s) {
			return s, true
		}
	}
	return "", false
}
