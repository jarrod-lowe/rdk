// Package kind is the registry of definition kinds. Each kind owns its schema,
// its Terraform module, and its definition->module-input mapping in a
// sub-package; the spine (parse, generate) dispatches through this registry
// rather than special-casing kinds. Philosophy rule 7: one mechanism, reused.
package kind

import (
	"fmt"
	"io/fs"
	"sort"

	"github.com/jarrod-lowe/rdk/internal/schema"
)

// Kind is any kind writable in a definition file: it carries a schema, so it
// can be validated and projected into authoring aids (DD-13).
type Kind interface {
	Name() string
	Schema() schema.Kind
}

// Resource is a kind that becomes a Terraform module and joins the connection
// graph. Only Resource kinds are vendored and emitted as module calls. Future
// connection support (DD-6) and the DD-12 output contract attach here.
type Resource interface {
	Kind
	ModuleFS() fs.FS                                         // embedded HCL subtree, rooted at the module dir
	ModuleCall(attrs map[string]any) (map[string]any, error) // attrs -> module inputs; home for kind-specific mapping
}

// buildRegistry indexes kinds by name, rejecting a duplicate name as a
// build-time programming error. Returning an error (surfaced by Validate)
// rather than panicking lets the CLI exit with a controlled message and
// non-zero status instead of a stack trace (philosophy rule 11).
func buildRegistry(kinds ...Kind) (map[string]Kind, error) {
	reg := make(map[string]Kind, len(kinds))
	for _, k := range kinds {
		if _, dup := reg[k.Name()]; dup {
			return nil, fmt.Errorf("duplicate kind registration: %q", k.Name())
		}
		reg[k.Name()] = k
	}
	return reg, nil
}

// Validate reports any error from registering kinds (currently a duplicate
// kind name). The CLI calls this at startup so the failure surfaces as a clean
// error and non-zero exit rather than a panic.
func Validate() error { return regErr }

// Lookup returns the kind with the given name.
func Lookup(name string) (Kind, bool) {
	k, ok := registry[name]
	return k, ok
}

// All returns every registered kind, sorted by name for determinism (DD-1).
func All() []Kind {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
