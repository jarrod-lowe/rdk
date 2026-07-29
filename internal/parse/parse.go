// Package parse loads and validates the rdk/ definitions directory.
package parse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
// one config). Entries rdk cannot process are errors, not clutter to step
// around; the few that are ignorable come back as warnings for the caller to
// report. Warnings inherit ReadDir's sorted order (DD-1).
func Dir(store repofs.Store, dir string) ([]Definition, []string, error) {
	entries, err := store.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading definitions dir %s: %w", dir, err)
	}

	var defs []Definition
	var warnings []string
	seen := map[string]string{} // resource name -> file
	configs := 0
	for _, e := range entries {
		name := e.Name
		if e.IsDir {
			return nil, nil, fmt.Errorf("%s: rdk/ holds definition files, not directories — move the definitions in %s/ up into %s/", name, name, dir)
		}
		if !strings.HasSuffix(name, ".yaml") {
			w, err := classify(name)
			if err != nil {
				return nil, nil, err
			}
			if w != "" {
				warnings = append(warnings, w)
			}
			continue
		}
		def, err := parseFile(store, dir, name)
		if err != nil {
			return nil, nil, err
		}
		// The "exactly one config" cardinality rule is config-specific and not
		// yet modeled by the kind registry, so it names the kind directly here.
		if def.Kind == "config" {
			configs++
		} else {
			if prev, dup := seen[def.Name]; dup {
				return nil, nil, fmt.Errorf("%s: duplicate resource name %q (also defined in %s)", name, def.Name, prev)
			}
			seen[def.Name] = name
		}
		defs = append(defs, def)
	}
	if configs != 1 {
		return nil, nil, fmt.Errorf("expected exactly one 'kind: config' definition in %s, found %d", dir, configs)
	}
	return defs, warnings, nil
}

func parseFile(store repofs.Store, dir, name string) (Definition, error) {
	raw, err := store.ReadFile(path.Join(dir, name))
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %w", name, err)
	}
	// Decode document-by-document rather than with yaml.Unmarshal, which reads
	// only the first document and drops the rest without complaint. A file that
	// bundles definitions the Kubernetes way would otherwise apply partially and
	// silently, so a second document is an error rather than a lost resource.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return Definition{}, fmt.Errorf("%s: invalid YAML: %w", name, err)
	}
	var next map[string]any
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return Definition{}, fmt.Errorf("%s: contains more than one YAML document (separated by '---'); rdk takes one definition per file — split them into separate files", name)
	}

	if len(doc) == 0 {
		return Definition{}, fmt.Errorf("%s: file is empty; every definition starts with a 'kind' field (e.g. kind: s3-bucket)", name)
	}
	rawKind, present := doc["kind"]
	if !present {
		return Definition{}, fmt.Errorf("%s: missing 'kind' field; every definition starts with one (e.g. kind: s3-bucket)", name)
	}
	kindVal, isStr := rawKind.(string)
	if !isStr {
		return Definition{}, fmt.Errorf("%s: 'kind' must be a string, got %s (e.g. kind: s3-bucket)", name, yamlType(rawKind))
	}
	if kindVal == "" {
		return Definition{}, fmt.Errorf("%s: 'kind' must not be empty (e.g. kind: s3-bucket)", name)
	}
	ki, ok := kind.Lookup(kindVal)
	if !ok {
		known := knownKinds()
		return Definition{}, fmt.Errorf("%s: unknown kind %q%s (known kinds: %s)",
			name, kindVal, didYouMean(kindVal, known), strings.Join(known, ", "))
	}
	k := ki.Schema()

	attrs := map[string]any{}
	for key, val := range doc {
		if key == "kind" {
			continue
		}
		if _, ok := k.Field(key); !ok {
			valid := fieldNames(k)
			return Definition{}, fmt.Errorf("%s: unknown field %q for kind %q%s (valid fields: %s)",
				name, key, kindVal, didYouMean(key, valid), strings.Join(valid, ", "))
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
				return Definition{}, fmt.Errorf("%s: field %q must be a string, got %s (example: %s)", name, f.Name, yamlType(v), f.Example)
			}
			if f.Required && strings.TrimSpace(s) == "" {
				return Definition{}, fmt.Errorf("%s: required field %q must not be empty", name, f.Name)
			}
		}
	}

	defName, _ := attrs["name"].(string)
	return Definition{Kind: kindVal, Name: defName, File: name, Attrs: attrs}, nil
}
