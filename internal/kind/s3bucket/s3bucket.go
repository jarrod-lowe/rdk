// Package s3bucket is the s3-bucket resource kind: an S3 bucket with safe
// defaults (public access blocked). It owns its schema, its Terraform module
// (embedded below), and its definition->module-input mapping.
package s3bucket

import (
	"embed"
	"io/fs"

	"github.com/jarrod-lowe/rdk/internal/schema"
)

//go:embed module
var moduleFS embed.FS

// Kind is the s3-bucket resource kind.
type Kind struct {
	module fs.FS
}

// New returns the s3-bucket kind with its module subtree rooted for vendoring.
func New() Kind {
	sub, err := fs.Sub(moduleFS, "module")
	if err != nil {
		panic(err) // unreachable: "module" is a valid path and //go:embed guarantees the subtree at compile time
	}
	return Kind{module: sub}
}

// Name is the kind's name as written in a definition file's `kind:` field.
func (Kind) Name() string { return "s3-bucket" }

// Schema returns the s3-bucket kind's field metadata.
func (Kind) Schema() schema.Kind {
	return schema.Kind{
		Name:        "s3-bucket",
		Description: "An S3 bucket with safe defaults (public access blocked).",
		Fields: []schema.Field{
			{Name: "name", Type: schema.StringType, Required: true,
				Description: "Resource name; becomes the bucket name until naming policy lands.",
				Example:     "assets"},
			{Name: "description", Type: schema.StringType, Required: true,
				Description: "What this bucket is for; feeds generated documentation.",
				Example:     "Static assets for the public site"},
		},
	}
}

// ModuleFS returns the embedded HCL module, rooted at its top-level files.
func (k Kind) ModuleFS() fs.FS { return k.module }

// ModuleCall maps definition attrs to module inputs. The mapping is identity
// today (every attr is a module input); it is the home for future structured
// transforms (e.g. S3 lifecycle rules). It returns a fresh map so callers never
// alias the definition's attrs. The error return is always nil today but is
// kept deliberately and must be checked by callers.
func (Kind) ModuleCall(attrs map[string]any) (map[string]any, error) {
	inputs := make(map[string]any, len(attrs))
	for k, v := range attrs {
		inputs[k] = v
	}
	return inputs, nil
}
