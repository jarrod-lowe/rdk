// Package parse loads and validates the rdk/ definitions directory.
package parse

import (
	"fmt"
	"path"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/jarrod-lowe/rdk/internal/schema"
)

// Definition is one parsed, schema-validated definition file.
type Definition struct {
	Kind  string
	Name  string
	File  string         // path relative to the definitions dir, for error messages
	Attrs map[string]any // all fields except kind, validated against the schema
}

// Dir loads every *.yaml in dir (read through the store, sorted), validates each
// against its kind schema, and enforces cross-file rules (unique names, exactly
// one config).
func Dir(store repofs.Store, dir string) ([]Definition, error) {
	names, err := store.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading definitions dir %s: %w", dir, err)
	}

	var defs []Definition
	seen := map[string]string{} // resource name -> file
	configs := 0
	for _, name := range names {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		def, err := parseFile(store, dir, name)
		if err != nil {
			return nil, err
		}
		if def.Kind == "config" {
			configs++
		} else {
			if prev, dup := seen[def.Name]; dup {
				return nil, fmt.Errorf("%s: duplicate resource name %q (also defined in %s)", name, def.Name, prev)
			}
			seen[def.Name] = name
		}
		defs = append(defs, def)
	}
	if configs != 1 {
		return nil, fmt.Errorf("expected exactly one 'kind: config' definition in %s, found %d", dir, configs)
	}
	return defs, nil
}

func parseFile(store repofs.Store, dir, name string) (Definition, error) {
	raw, err := store.ReadFile(path.Join(dir, name))
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", name, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Definition{}, fmt.Errorf("%s: invalid YAML: %w", name, err)
	}

	kindVal, ok := doc["kind"].(string)
	if !ok || kindVal == "" {
		return Definition{}, fmt.Errorf("%s: missing 'kind' field; every definition starts with one (e.g. kind: s3-bucket)", name)
	}
	ki, ok := kind.Lookup(kindVal)
	if !ok {
		return Definition{}, fmt.Errorf("%s: unknown kind %q", name, kindVal)
	}
	k := ki.Schema()

	attrs := map[string]any{}
	for key, val := range doc {
		if key == "kind" {
			continue
		}
		if _, ok := k.Field(key); !ok {
			return Definition{}, fmt.Errorf("%s: unknown field %q for kind %q", name, key, kindVal)
		}
		attrs[key] = val
	}
	for _, f := range k.Fields {
		if f.Required {
			if _, present := attrs[f.Name]; !present {
				return Definition{}, fmt.Errorf("%s: kind %q has required field %q — %s (example: %s)",
					name, kindVal, f.Name, f.Description, f.Example)
			}
		}
		if v, present := attrs[f.Name]; present && f.Type == schema.StringType {
			s, isStr := v.(string)
			if !isStr {
				return Definition{}, fmt.Errorf("%s: field %q must be a string, got %T", name, f.Name, v)
			}
			if f.Required && strings.TrimSpace(s) == "" {
				return Definition{}, fmt.Errorf("%s: required field %q must not be empty", name, f.Name)
			}
		}
	}

	defName, _ := attrs["name"].(string)
	return Definition{Kind: kindVal, Name: defName, File: name, Attrs: attrs}, nil
}
