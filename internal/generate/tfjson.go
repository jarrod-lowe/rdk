package generate

import "github.com/jarrod-lowe/rdk/internal/parse"

// tfDoc builds the root Terraform document as a data structure: one module
// block per resource definition, plus the stack-level provider pin. Serialized
// to deterministic JSON by repofs (DD-1); this function no longer encodes.
func tfDoc(defs []parse.Definition) map[string]any {
	modules := map[string]any{}
	for _, d := range defs {
		if d.Kind == "config" {
			continue
		}
		call := map[string]any{"source": "./modules/" + d.Kind}
		for k, v := range d.Attrs {
			call[k] = v
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
	}
}
