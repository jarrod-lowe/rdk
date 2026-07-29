package generate

import (
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/parse"
)

// tfDoc builds the root Terraform document as a data structure: one module
// block per resource definition, plus the stack-level provider pin. Non-resource
// kinds (e.g. config) contribute no module block. Serialized to deterministic
// JSON by repofs (DD-1); this function does not encode.
func tfDoc(defs []parse.Definition) (map[string]any, error) {
	modules := map[string]any{}
	for _, d := range defs {
		r, ok, err := resource(d)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // config and other non-resource kinds produce no module block
		}
		inputs, err := r.ModuleCall(d.Attrs)
		if err != nil {
			return nil, fmt.Errorf("%s: mapping module inputs: %w", d.File, err)
		}
		call := map[string]any{"source": "./modules/" + d.Kind}
		for name, v := range inputs {
			call[name] = v
		}
		modules[d.Name] = call
	}
	return map[string]any{
		"terraform": map[string]any{
			"required_providers": map[string]any{
				"aws": map[string]any{
					"source":  "hashicorp/aws",
					"version": "~> 6.0",
				},
			},
		},
		"module": modules,
	}, nil
}
