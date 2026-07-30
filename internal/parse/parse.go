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
	"github.com/jarrod-lowe/rdk/internal/diag"
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
func Dir(store repofs.Store, dir string) ([]Definition, []diag.Diagnostic, error) {
	entries, err := store.ReadDir(dir)
	if err != nil {
		return nil, nil, diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeReadDefsDir,
			File:    dir,
			Summary: "cannot read the definitions directory",
			Hint:    "run 'rdk init' if this repository has not been initialised",
		})
	}

	var defs []Definition
	var warnings []diag.Diagnostic
	seen := map[string]string{} // resource name -> file
	configs := 0
	for _, e := range entries {
		name := e.Name
		if e.IsDir {
			return nil, nil, diag.New(diag.Diagnostic{
				Code:    diag.CodeDirInDefs,
				File:    name,
				Summary: "rdk/ holds definition files, not directories",
				Hint:    fmt.Sprintf("move the definitions in %s/ up into %s/", name, dir),
			})
		}
		if !strings.HasSuffix(name, ".yaml") {
			w, ok, err := classify(name)
			if err != nil {
				return nil, nil, err
			}
			if ok {
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
				return nil, nil, diag.New(diag.Diagnostic{
					Code:    diag.CodeDuplicateName,
					File:    name,
					Field:   "name",
					Summary: fmt.Sprintf("duplicate resource name %q (also defined in %s)", def.Name, prev),
					Hint:    "resource names must be unique; rename one of them",
				})
			}
			seen[def.Name] = name
		}
		defs = append(defs, def)
	}
	if configs != 1 {
		return nil, nil, diag.New(diag.Diagnostic{
			Code:    diag.CodeConfigCardinality,
			File:    dir,
			Summary: fmt.Sprintf("expected exactly one 'kind: config' definition in %s, found %d", dir, configs),
			Hint:    "every repository has exactly one config definition",
		})
	}
	return defs, warnings, nil
}

func parseFile(store repofs.Store, dir, name string) (Definition, error) {
	raw, err := store.ReadFile(path.Join(dir, name))
	if err != nil {
		return Definition{}, diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeReadFile,
			File:    name,
			Summary: "cannot read this definition file",
		})
	}
	// Decode document-by-document rather than with yaml.Unmarshal, which reads
	// only the first document and drops the rest without complaint. A file that
	// bundles definitions the Kubernetes way would otherwise apply partially and
	// silently, so a second document is an error rather than a lost resource.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return Definition{}, diag.Wrap(err, diag.Diagnostic{
			Code:    diag.CodeInvalidYAML,
			File:    name,
			Summary: "invalid YAML",
		})
	}
	var next map[string]any
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeMultiDocument,
			File:    name,
			Summary: "contains more than one YAML document (separated by '---')",
			Hint:    "rdk takes one definition per file — split them into separate files",
		})
	}

	if len(doc) == 0 {
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeEmptyFile,
			File:    name,
			Summary: "file is empty",
			Hint:    "every definition starts with a 'kind' field (e.g. kind: s3-bucket)",
		})
	}
	rawKind, present := doc["kind"]
	if !present {
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeMissingKind,
			File:    name,
			Field:   "kind",
			Summary: "missing 'kind' field",
			Hint:    "every definition starts with one (e.g. kind: s3-bucket)",
		})
	}
	kindVal, isStr := rawKind.(string)
	if !isStr {
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeKindNotString,
			File:    name,
			Field:   "kind",
			Summary: fmt.Sprintf("'kind' must be a string, got %s", yamlType(rawKind)),
			Hint:    "e.g. kind: s3-bucket",
		})
	}
	if kindVal == "" {
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeEmptyKind,
			File:    name,
			Field:   "kind",
			Summary: "'kind' must not be empty",
			Hint:    "e.g. kind: s3-bucket",
		})
	}
	ki, ok := kind.Lookup(kindVal)
	if !ok {
		known := knownKinds()
		return Definition{}, diag.New(diag.Diagnostic{
			Code:    diag.CodeUnknownKind,
			File:    name,
			Field:   "kind",
			Summary: fmt.Sprintf("unknown kind %q%s", kindVal, didYouMean(kindVal, known)),
			Hint:    "known kinds: " + strings.Join(known, ", "),
		})
	}
	k := ki.Schema()

	attrs := map[string]any{}
	for key, val := range doc {
		if key == "kind" {
			continue
		}
		if _, ok := k.Field(key); !ok {
			valid := fieldNames(k)
			return Definition{}, diag.New(diag.Diagnostic{
				Code:    diag.CodeUnknownField,
				File:    name,
				Field:   key,
				Summary: fmt.Sprintf("unknown field %q for kind %q%s", key, kindVal, didYouMean(key, valid)),
				Hint:    "valid fields: " + strings.Join(valid, ", "),
			})
		}
		attrs[key] = val
	}
	for _, f := range k.Fields {
		if f.Required {
			if _, present := attrs[f.Name]; !present {
				return Definition{}, diag.New(diag.Diagnostic{
					Code:    diag.CodeMissingField,
					File:    name,
					Field:   f.Name,
					Summary: fmt.Sprintf("kind %q has required field %q — %s", kindVal, f.Name, f.Description),
					Hint:    "example: " + f.Example,
				})
			}
		}
		if v, present := attrs[f.Name]; present && f.Type == schema.StringType {
			s, isStr := v.(string)
			if !isStr {
				return Definition{}, diag.New(diag.Diagnostic{
					Code:    diag.CodeFieldNotString,
					File:    name,
					Field:   f.Name,
					Summary: fmt.Sprintf("field %q must be a string, got %s", f.Name, yamlType(v)),
					Hint:    "example: " + f.Example,
				})
			}
			if f.Required && strings.TrimSpace(s) == "" {
				return Definition{}, diag.New(diag.Diagnostic{
					Code:    diag.CodeEmptyField,
					File:    name,
					Field:   f.Name,
					Summary: fmt.Sprintf("required field %q must not be empty", f.Name),
				})
			}
		}
	}

	defName, _ := attrs["name"].(string)
	return Definition{Kind: kindVal, Name: defName, File: name, Attrs: attrs}, nil
}
