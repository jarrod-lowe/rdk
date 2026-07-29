package generate

import (
	"encoding/json"
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/parse"
)

// The AWS provider pin applied to every generated stack. Held here so the
// version rdk generates against is one edit, not a literal buried in a map.
const (
	awsProviderSource  = "hashicorp/aws"
	awsProviderVersion = "~> 6.0"
)

// sourceKey is Terraform's reserved argument naming a module's source. A kind
// may not map a module input onto it (see newModuleCall).
const sourceKey = "source"

// tfRoot is the root Terraform document rdk emits (terraform/main.tf.json):
// the stack-level provider pin plus one module block per resource definition,
// keyed by definition name. Field names are fixed by Terraform's JSON syntax,
// so the struct tags are the schema, not decoration.
type tfRoot struct {
	Terraform tfSettings            `json:"terraform"`
	Module    map[string]moduleCall `json:"module"`
}

// tfSettings is the `terraform` block.
type tfSettings struct {
	RequiredProviders map[string]providerRequirement `json:"required_providers"`
}

// providerRequirement is one entry of `required_providers`.
type providerRequirement struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

// moduleCall is one `module "<name>"` block: the vendored module's source path
// plus that kind's module inputs. Inputs stays map[string]any deliberately —
// its shape is owned by the kind (kind.Resource.ModuleCall), not by the spine,
// so the spine can only promise it is a JSON object.
type moduleCall struct {
	Source string
	Inputs map[string]any
}

// newModuleCall builds a module call, rejecting inputs that would collide with
// the reserved source argument. Terraform JSON flattens inputs alongside
// source, so a kind mapping an input named "source" would otherwise silently
// redirect the module call at another module.
func newModuleCall(source string, inputs map[string]any) (moduleCall, error) {
	if _, clash := inputs[sourceKey]; clash {
		return moduleCall{}, fmt.Errorf("module input %q collides with Terraform's reserved module argument of the same name", sourceKey)
	}
	return moduleCall{Source: source, Inputs: inputs}, nil
}

// MarshalJSON flattens the inputs alongside source, as Terraform's JSON syntax
// requires: a module block is one object of arguments, with no nesting for the
// kind-specific ones. Key order is left to encoding/json, which sorts map keys,
// keeping output deterministic (DD-1). Callers build through newModuleCall, so
// Inputs never carries a source key of its own.
func (m moduleCall) MarshalJSON() ([]byte, error) {
	obj := make(map[string]any, len(m.Inputs)+1)
	obj[sourceKey] = m.Source
	for name, v := range m.Inputs {
		obj[name] = v
	}
	return json.Marshal(obj)
}

// tfDoc builds the root Terraform document as a data structure: one module
// block per resource definition, plus the stack-level provider pin. Non-resource
// kinds (e.g. config) contribute no module block. Serialized to deterministic
// JSON by repofs (DD-1); this function does not encode.
func tfDoc(defs []parse.Definition) (tfRoot, error) {
	modules := map[string]moduleCall{}
	for _, d := range defs {
		r, ok, err := resource(d)
		if err != nil {
			return tfRoot{}, err
		}
		if !ok {
			continue // config and other non-resource kinds produce no module block
		}
		inputs, err := r.ModuleCall(d.Attrs)
		if err != nil {
			return tfRoot{}, fmt.Errorf("%s: mapping module inputs: %w", d.File, err)
		}
		call, err := newModuleCall("./modules/"+d.Kind, inputs)
		if err != nil {
			return tfRoot{}, fmt.Errorf("%s: kind %q: %w", d.File, d.Kind, err)
		}
		modules[d.Name] = call
	}
	return tfRoot{
		Terraform: tfSettings{
			RequiredProviders: map[string]providerRequirement{
				"aws": {Source: awsProviderSource, Version: awsProviderVersion},
			},
		},
		Module: modules,
	}, nil
}
