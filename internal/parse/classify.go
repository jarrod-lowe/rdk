package parse

import (
	"fmt"
	"strings"
)

// rdk/ is a definitions directory, not a general-purpose folder: a file rdk
// cannot process is a mistake worth surfacing, since the alternative is
// resources that silently never get generated. Three narrow exceptions below
// keep that rule from being hostile in practice.

// setAside suffixes park a definition without deleting it. Deliberate, so
// allowed — but they change what rdk generates, so never silent.
var setAside = []string{".disabled", ".example"}

// artifacts are leftovers from editors and merges. Nobody chooses to create
// them, so they do not block an apply; a stale .orig may still hold work
// someone wants, so they are not silent either.
var artifacts = []string{".orig", ".rej", ".bak", "~"}

// classify decides what a non-.yaml entry means. It returns a warning to
// report, "" to skip silently, or an error if the entry cannot be processed.
func classify(name string) (string, error) {
	// Hidden files are machine-made, never an attempt at a definition:
	// .DS_Store appears without anyone asking, vim writes .foo.yaml.swp while
	// editing, and .gitignore/.gitkeep are how the directory is kept in git.
	// Erroring would break apply for no reason; warning every time would train
	// people to ignore warnings.
	if strings.HasPrefix(name, ".") {
		return "", nil
	}
	// A .yml file is an attempt to write a definition that would otherwise be
	// dropped in full, so it names its own fix rather than warning.
	if strings.HasSuffix(name, ".yml") {
		return "", fmt.Errorf("%s: rdk definitions must use the .yaml extension — rename it to %s",
			name, strings.TrimSuffix(name, ".yml")+".yaml")
	}
	if suffix, ok := matchSuffix(name, setAside); ok {
		return fmt.Sprintf("warning: %s: ignored (%s); rdk generates nothing for it", name, suffix), nil
	}
	if suffix, ok := matchSuffix(name, artifacts); ok {
		return fmt.Sprintf("warning: %s: ignored (%s leftover); delete it or move it out of the definitions dir", name, suffix), nil
	}
	return "", fmt.Errorf("%s: rdk cannot process this file; the definitions dir takes .yaml definitions only "+
		"(to park one, suffix it %s; anything else belongs outside the definitions dir)",
		name, strings.Join(setAside, " or "))
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
