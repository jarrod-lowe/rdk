package generate

import (
	"bytes"
	"encoding/json"

	"github.com/jarrod-lowe/rdk/internal/parse"
)

// tfJSON renders the root Terraform document: one module block per resource
// definition, inputs mapped mechanically from validated attrs (DD-2).
// encoding/json sorts map keys, giving deterministic output.
func tfJSON(defs []parse.Definition) ([]byte, error) {
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
	doc := map[string]any{"module": modules}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
