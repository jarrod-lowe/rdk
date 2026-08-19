// Package config is the global-configuration kind. It carries a schema (so it
// is validated and projectable, DD-13) but produces no module: config is not a
// deployable resource, so it implements kind.Kind, not kind.Resource.
package config

import "github.com/jarrod-lowe/rdk/internal/schema"

// Kind is the config kind.
type Kind struct{}

// New returns the config kind.
func New() Kind { return Kind{} }

// Name is the kind's name as written in a definition file's `kind:` field.
func (Kind) Name() string { return "config" }

// Schema returns the config kind's field metadata.
func (Kind) Schema() schema.Kind {
	return schema.Kind{
		Name:        "config",
		Description: "Global repository configuration. Exactly one config definition is required.",
		Fields: []schema.Field{
			// Plain StringType, not IdentifierType: config produces no module
			// block, so this name is not a Terraform label today. A future
			// naming policy may constrain it, but that policy doesn't exist yet
			// — imposing identifier rules now would reject legitimate project
			// names ("Acme, Inc.") against a requirement nothing enforces.
			{Name: "name", Type: schema.StringType, Required: true,
				Description: "Project name; used in generated documentation and, later, naming policy.",
				Example:     "my-service"},
		},
	}
}
