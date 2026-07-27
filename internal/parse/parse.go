// Package parse loads and validates the rdk/ definitions directory.
package parse

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/jarrod-lowe/rdk/internal/schema"
)

// Definition is one parsed, schema-validated definition file.
type Definition struct {
	Kind  string
	Name  string
	File  string         // path relative to the definitions dir, for error messages
	Attrs map[string]any // all fields except kind, validated against the schema
}

// Dir loads every *.yaml in dir (sorted by filename), validates each against
// its kind schema, and enforces cross-file rules (unique names, exactly one
// config).
func Dir(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading definitions dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var defs []Definition
	seen := map[string]string{} // resource name -> file
	configs := 0
	for _, name := range names {
		def, err := parseFile(dir, name)
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

func parseFile(dir, name string) (Definition, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
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
	k, ok := schema.Lookup(kindVal)
	if !ok {
		return Definition{}, fmt.Errorf("%s: unknown kind %q", name, kindVal)
	}

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
