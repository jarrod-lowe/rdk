// Package generate builds the rdk-managed file set from parsed definitions.
// Build is pure: same definitions, same rdk binary -> identical FileSet.
package generate

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"

	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/parse"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

// readme is the banner dropped at the root of the managed directory. It lives
// on disk under static/ so it reads as the Markdown it is, rather than as a
// string literal spliced around its own backticks.

//go:embed static/README.md
var readme string

// Build produces the managed file set for the given definitions.
func Build(defs []parse.Definition) (*repofs.FileSet, error) {
	set := repofs.NewFileSet()
	if err := set.Bytes(repofs.Managed("README.md"), []byte(readme)); err != nil {
		return nil, err
	}

	doc, err := tfDoc(defs)
	if err != nil {
		return nil, err
	}
	if err := set.JSON(repofs.Managed("terraform/main.tf.json"), doc); err != nil {
		return nil, err
	}

	// Vendor each distinct resource kind's module once. Non-resource kinds
	// (config) have no module and are skipped structurally.
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Kind] {
			continue
		}
		seen[d.Kind] = true
		r, ok, err := resource(d)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := vendorModule(d.Kind, r.ModuleFS(), set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// resource resolves d to its Resource kind. ok is false for a non-resource kind
// (e.g. config, which produces no module); err is non-nil only for a kind that
// is not registered at all.
func resource(d parse.Definition) (r kind.Resource, ok bool, err error) {
	k, found := kind.Lookup(d.Kind)
	if !found {
		return nil, false, fmt.Errorf("%s: unknown kind %q", d.File, d.Kind)
	}
	r, ok = k.(kind.Resource)
	return r, ok, nil
}

// vendorModule copies a resource kind's embedded module (fsys, rooted at the
// module dir) into the set under terraform/modules/<name>/, preserving
// subdirectories. Subdir preservation is currently only exercised by flat
// modules (s3-bucket); when a module first ships nested files, add a golden
// fixture covering them.
func vendorModule(name string, fsys fs.FS, set *repofs.FileSet) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("embedded module %q: %w", name, err)
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return set.Bytes(repofs.Managed(path.Join("terraform/modules", name, p)), content)
	})
}
