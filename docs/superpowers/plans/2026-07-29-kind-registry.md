# `internal/kind` Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give each resource kind one cohesive home (its schema + Terraform module + input mapping in a single package) behind a `kind.Kind`/`kind.Resource` registry, and make the spine dispatch through it instead of special-casing kinds.

**Architecture:** A new `internal/kind` package defines two interfaces and a name-keyed registry. Each kind is a sub-package (`internal/kind/config`, `internal/kind/s3bucket`) owning its own `schema.Kind` data, its embedded HCL, and its `ModuleCall` mapping. `parse` validates via `kind.Lookup`; `generate` selects deployable resources by type-asserting `kind.Resource` (so `config` is skipped structurally, not by a magic string). The `schema` package is slimmed to types only. This is a **pure refactor** — `testdata/golden/basic` must stay byte-identical.

**Tech Stack:** Go, `io/fs` + `embed`, `goccy/go-yaml` (unchanged), standard `testing`.

**Spec:** `docs/superpowers/specs/2026-07-29-kind-registry-design.md`

**Ordering rationale:** Every commit compiles and stays green. The s3-bucket HCL is *copied* into its new home in Task 2 and the original *deleted* in Task 4 — a two-step move so `internal/generate`'s existing `//go:embed modules` never points at a missing directory mid-refactor.

---

### Task 1: Create the `kind` registry, interfaces, and the `config` kind

**Files:**
- Create: `internal/kind/kind.go`
- Create: `internal/kind/register.go`
- Create: `internal/kind/config/config.go`
- Test: `internal/kind/kind_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/kind/kind_test.go`:

```go
package kind

import (
	"sort"
	"testing"
)

func TestLookupConfig(t *testing.T) {
	k, ok := Lookup("config")
	if !ok {
		t.Fatal(`Lookup("config"): not found`)
	}
	if k.Name() != "config" {
		t.Errorf("Name() = %q, want config", k.Name())
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("volcano"); ok {
		t.Error("Lookup(volcano): want not found")
	}
}

func TestConfigIsNotAResource(t *testing.T) {
	k, _ := Lookup("config")
	if _, ok := k.(Resource); ok {
		t.Error("config must implement Kind but not Resource (it produces no module)")
	}
}

func TestAllSortedAndDocumented(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() is empty")
	}
	names := make([]string, len(all))
	for i, k := range all {
		names[i] = k.Name()
		// DD-13: a kind without complete field docs is unfinished.
		s := k.Schema()
		if s.Description == "" {
			t.Errorf("%s: empty kind description", k.Name())
		}
		for _, f := range s.Fields {
			if f.Description == "" {
				t.Errorf("%s.%s: empty field description (DD-13)", k.Name(), f.Name)
			}
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("All() not sorted by name: %v", names)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kind/`
Expected: FAIL — build error, `undefined: Lookup` / package has no `kind.go` yet.

- [ ] **Step 3: Write the interfaces and registry**

Create `internal/kind/kind.go`:

```go
// Package kind is the registry of definition kinds. Each kind owns its schema,
// its Terraform module, and its definition->module-input mapping in a
// sub-package; the spine (parse, generate) dispatches through this registry
// rather than special-casing kinds. Philosophy rule 7: one mechanism, reused.
package kind

import (
	"io/fs"
	"sort"

	"github.com/jarrod-lowe/rdk/internal/schema"
)

// Kind is any kind writable in a definition file: it carries a schema, so it
// can be validated and projected into authoring aids (DD-13).
type Kind interface {
	Name() string
	Schema() schema.Kind
}

// Resource is a kind that becomes a Terraform module and joins the connection
// graph. Only Resource kinds are vendored and emitted as module calls. Future
// connection support (DD-6) and the DD-12 output contract attach here.
type Resource interface {
	Kind
	ModuleFS() fs.FS                                          // embedded HCL subtree, rooted at the module dir
	ModuleCall(attrs map[string]any) (map[string]any, error) // attrs -> module inputs; home for kind-specific mapping
}

var registry = map[string]Kind{}

func register(k Kind) { registry[k.Name()] = k }

// Lookup returns the kind with the given name.
func Lookup(name string) (Kind, bool) {
	k, ok := registry[name]
	return k, ok
}

// All returns every registered kind, sorted by name for determinism (DD-1).
func All() []Kind {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
```

Create `internal/kind/register.go` (the single, explicit list of kinds — no per-package self-registration):

```go
package kind

import "github.com/jarrod-lowe/rdk/internal/kind/config"

// init registers every kind. This central list is the one place the set of
// kinds is declared; adding a kind means adding one line here plus its package.
func init() {
	register(config.New())
}
```

Create `internal/kind/config/config.go`:

```go
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
			{Name: "name", Type: schema.StringType, Required: true,
				Description: "Project name; used in generated documentation and, later, naming policy.",
				Example:     "my-service"},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kind/...`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/kind/kind.go internal/kind/register.go internal/kind/config/config.go internal/kind/kind_test.go
git commit -m "feat(kind): registry + interfaces + config kind"
```

---

### Task 2: Add the `s3-bucket` resource kind (move its HCL into the package)

**Files:**
- Create: `internal/kind/s3bucket/module/main.tf` (copy of `internal/generate/modules/s3-bucket/main.tf`)
- Create: `internal/kind/s3bucket/module/outputs.tf` (copy)
- Create: `internal/kind/s3bucket/module/variables.tf` (copy)
- Create: `internal/kind/s3bucket/s3bucket.go`
- Modify: `internal/kind/register.go`
- Test: `internal/kind/s3bucket/s3bucket_test.go`
- Test: `internal/kind/kind_test.go` (add one test)

- [ ] **Step 1: Copy the module HCL into the new package**

Run (copy, do not move — the original stays until Task 4):

```bash
mkdir -p internal/kind/s3bucket/module
cp internal/generate/modules/s3-bucket/main.tf internal/kind/s3bucket/module/main.tf
cp internal/generate/modules/s3-bucket/outputs.tf internal/kind/s3bucket/module/outputs.tf
cp internal/generate/modules/s3-bucket/variables.tf internal/kind/s3bucket/module/variables.tf
```

- [ ] **Step 2: Write the failing test**

Create `internal/kind/s3bucket/s3bucket_test.go`:

```go
package s3bucket

import (
	"io/fs"
	"sort"
	"testing"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "s3-bucket" {
		t.Errorf("Name() = %q, want s3-bucket", got)
	}
}

func TestSchemaFieldsRequiredAndDocumented(t *testing.T) {
	s := New().Schema()
	for _, want := range []string{"name", "description"} {
		f, ok := s.Field(want)
		if !ok {
			t.Fatalf("Schema missing field %q", want)
		}
		if !f.Required {
			t.Errorf("field %q: want required", want)
		}
		if f.Description == "" {
			t.Errorf("field %q: empty description (DD-13)", want)
		}
	}
}

func TestModuleCallIsIdentityAndCopies(t *testing.T) {
	attrs := map[string]any{"name": "assets", "description": "Static assets"}
	inputs, err := New().ModuleCall(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["name"] != "assets" || inputs["description"] != "Static assets" {
		t.Errorf("inputs = %v, want identity of attrs", inputs)
	}
	// Must be a fresh map, never an alias of the caller's attrs.
	inputs["name"] = "mutated"
	if attrs["name"] != "assets" {
		t.Error("ModuleCall aliased the caller's attrs map")
	}
}

func TestModuleFSHasExpectedFiles(t *testing.T) {
	var got []string
	err := fs.WalkDir(New().ModuleFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"main.tf", "outputs.tf", "variables.tf"}
	if len(got) != len(want) {
		t.Fatalf("module files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("module files = %v, want %v", got, want)
			break
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/kind/s3bucket/`
Expected: FAIL — no `s3bucket.go`, `undefined: New`.

- [ ] **Step 4: Write the s3bucket kind**

Create `internal/kind/s3bucket/s3bucket.go`:

```go
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
		panic(err) // embedded at build time; can only fail if the embed is malformed
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
// kept deliberately and must be checked by callers (see the spec).
func (Kind) ModuleCall(attrs map[string]any) (map[string]any, error) {
	inputs := make(map[string]any, len(attrs))
	for k, v := range attrs {
		inputs[k] = v
	}
	return inputs, nil
}
```

- [ ] **Step 5: Register s3bucket**

Modify `internal/kind/register.go` to its final form:

```go
package kind

import (
	"github.com/jarrod-lowe/rdk/internal/kind/config"
	"github.com/jarrod-lowe/rdk/internal/kind/s3bucket"
)

// init registers every kind. This central list is the one place the set of
// kinds is declared; adding a kind means adding one line here plus its package.
func init() {
	register(config.New())
	register(s3bucket.New())
}
```

- [ ] **Step 6: Add the "s3-bucket is a Resource" test**

Append to `internal/kind/kind_test.go`:

```go
func TestS3BucketIsAResource(t *testing.T) {
	k, ok := Lookup("s3-bucket")
	if !ok {
		t.Fatal(`Lookup("s3-bucket"): not found`)
	}
	if _, ok := k.(Resource); !ok {
		t.Error("s3-bucket must implement Resource")
	}
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/kind/...`
Expected: PASS (s3bucket package tests + the kind registry tests, now including s3-bucket in `TestAllSortedAndDocumented` and `TestS3BucketIsAResource`).

- [ ] **Step 8: Commit**

```bash
git add internal/kind/s3bucket/ internal/kind/register.go internal/kind/kind_test.go
git commit -m "feat(kind): s3-bucket resource kind with embedded module"
```

---

### Task 3: Point `parse` at the kind registry

**Files:**
- Modify: `internal/parse/parse.go:72` (and the import block)

- [ ] **Step 1: Run the existing parse tests (baseline green)**

Run: `go test ./internal/parse/`
Expected: PASS — establishes the behavior we must preserve.

- [ ] **Step 2: Switch the lookup source**

In `internal/parse/parse.go`, change the import block from:

```go
import (
	"fmt"
	"path"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/jarrod-lowe/rdk/internal/schema"
)
```

to:

```go
import (
	"fmt"
	"path"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/repofs"
	"github.com/jarrod-lowe/rdk/internal/schema"
)
```

Then change the lookup (currently at line 72). Replace:

```go
	k, ok := schema.Lookup(kindVal)
	if !ok {
		return Definition{}, fmt.Errorf("%s: unknown kind %q", name, kindVal)
	}
```

with:

```go
	ki, ok := kind.Lookup(kindVal)
	if !ok {
		return Definition{}, fmt.Errorf("%s: unknown kind %q", name, kindVal)
	}
	k := ki.Schema()
```

`k` is now a `schema.Kind` exactly as before, so the rest of the function (`k.Field(...)`, `k.Fields`, `schema.StringType`) is unchanged. `schema` stays imported for `schema.StringType`.

- [ ] **Step 3: Run the parse tests to verify unchanged behavior**

Run: `go test ./internal/parse/`
Expected: PASS (identical results to Step 1).

- [ ] **Step 4: Commit**

```bash
git add internal/parse/parse.go
git commit -m "refactor(parse): validate via kind.Lookup instead of schema.Lookup"
```

---

### Task 4: Dispatch `generate` through the registry; delete the old module dir

**Files:**
- Modify: `internal/generate/generate.go`
- Modify: `internal/generate/tfjson.go`
- Modify: `internal/generate/generate_test.go`
- Delete: `internal/generate/modules/` (the whole tree)

- [ ] **Step 1: Update the `tfDoc` test for the new signature**

In `internal/generate/generate_test.go`, replace the body of `TestTFDocPinsProviderAndModules`'s first two lines. Change:

```go
func TestTFDocPinsProviderAndModules(t *testing.T) {
	doc := tfDoc(demoDefs())
	b, _ := json.Marshal(doc)
```

to:

```go
func TestTFDocPinsProviderAndModules(t *testing.T) {
	doc, err := tfDoc(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
```

- [ ] **Step 2: Run the generate tests to verify they fail to compile**

Run: `go test ./internal/generate/`
Expected: FAIL — `tfDoc` still returns a single value; test now expects two. (Confirms the test drives the signature change.)

- [ ] **Step 3: Rewrite `tfDoc` to map via the registry**

Replace the entire contents of `internal/generate/tfjson.go` with:

```go
package generate

import (
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/parse"
)

// tfDoc builds the root Terraform document as a data structure: one module
// block per resource definition, plus the stack-level provider pin. Non-resource
// kinds (e.g. config) contribute no module block. Serialized to deterministic
// JSON by repofs (DD-1); this function does not encode.
func tfDoc(defs []parse.Definition) (map[string]any, error) {
	modules := map[string]any{}
	for _, d := range defs {
		k, ok := kind.Lookup(d.Kind)
		if !ok {
			return nil, fmt.Errorf("%s: unknown kind %q", d.File, d.Kind)
		}
		r, isResource := k.(kind.Resource)
		if !isResource {
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
```

- [ ] **Step 4: Rewrite `generate.go` to vendor via `Resource.ModuleFS`**

Replace the entire contents of `internal/generate/generate.go` with:

```go
// Package generate builds the rdk-managed file set from parsed definitions.
// Build is pure: same definitions, same rdk binary -> identical FileSet.
package generate

import (
	"fmt"
	"io/fs"
	"path"

	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/parse"
	"github.com/jarrod-lowe/rdk/internal/repofs"
)

const readme = `# Generated by rdk — DO NOT EDIT

Everything in this directory is deleted and rewritten by ` + "`rdk apply`" + `.
Edit the definitions in ` + "`rdk/`" + ` instead.
`

// Build produces the managed file set for the given definitions.
func Build(defs []parse.Definition) (*repofs.FileSet, error) {
	set := repofs.NewFileSet()
	set.Bytes("README.md", []byte(readme))

	doc, err := tfDoc(defs)
	if err != nil {
		return nil, err
	}
	if err := set.JSON("terraform/main.tf.json", doc); err != nil {
		return nil, err
	}

	// Vendor each distinct resource kind's module once. Non-resource kinds
	// (config) have no module and are skipped structurally.
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Kind] {
			continue
		}
		seen[d.Kind] = true
		k, ok := kind.Lookup(d.Kind)
		if !ok {
			return nil, fmt.Errorf("%s: unknown kind %q", d.File, d.Kind)
		}
		r, isResource := k.(kind.Resource)
		if !isResource {
			continue
		}
		if err := vendorModule(d.Kind, r.ModuleFS(), set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// vendorModule copies a resource kind's embedded module (fsys, rooted at the
// module dir) into the set under terraform/modules/<name>/, preserving
// subdirectories.
func vendorModule(name string, fsys fs.FS, set *repofs.FileSet) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("embedded module %q: %w", name, err)
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		set.Bytes(path.Join("terraform/modules", name, p), content)
		return nil
	})
}
```

- [ ] **Step 5: Delete the old embedded module tree**

Run:

```bash
git rm -r internal/generate/modules
```

- [ ] **Step 6: Run the generate + golden tests**

Run: `go test ./internal/generate/ ./...`
Expected: PASS. In particular the root golden test must be **byte-identical** — the generated `terraform/modules/s3-bucket/*.tf`, `main.tf.json`, and `README.md` are unchanged.

If the golden test FAILS: the refactor changed output. Fix the code — do **not** run any `-update` golden flag. The most likely culprits: a differing module output path (must remain `terraform/modules/s3-bucket/…`) or an accidental change to `main.tf.json` key/values.

- [ ] **Step 7: Commit**

```bash
git add internal/generate/generate.go internal/generate/tfjson.go internal/generate/generate_test.go
git commit -m "refactor(generate): dispatch via kind.Resource; drop modules embed"
```

---

### Task 5: Slim `schema` to types-only

**Files:**
- Modify: `internal/schema/schema.go` (remove `registry` + `Lookup`)
- Modify: `internal/schema/schema_test.go` (drop registry tests; keep a `Field` helper test)

- [ ] **Step 1: Replace the schema test with a types-only test**

The registry-content tests now live in `internal/kind` (Task 1–2). Replace the entire contents of `internal/schema/schema_test.go` with a test for the remaining surface, the `Field` helper:

```go
package schema

import "testing"

func TestKindField(t *testing.T) {
	k := Kind{
		Name:        "example",
		Description: "An example kind.",
		Fields: []Field{
			{Name: "name", Type: StringType, Required: true,
				Description: "The name.", Example: "demo"},
		},
	}
	f, ok := k.Field("name")
	if !ok {
		t.Fatal(`Field("name"): not found`)
	}
	if !f.Required || f.Type != StringType {
		t.Errorf("Field(name) = %+v, want required string", f)
	}
	if _, ok := k.Field("nope"); ok {
		t.Error("Field(nope): want not found")
	}
}
```

- [ ] **Step 2: Run to establish baseline green**

This task removes code rather than adding behavior, so there is no red phase — the new `TestKindField` must pass against the still-intact `schema.go` (which still has `Lookup`/`registry`, now unused).

Run: `go test ./internal/schema/`
Expected: PASS — confirms the new test compiles and holds before the removal.

- [ ] **Step 3: Remove `registry` and `Lookup` from `schema.go`**

In `internal/schema/schema.go`, delete the `registry` variable and the `Lookup` function (currently lines 39–67). The file should end after the `Field` method. Its final contents:

```go
// Package schema defines the types describing a kind and its rich field
// metadata. Every authoring aid (JSON Schema, starters, skills, error text) is
// a projection of this data (DD-13); a field without documentation is a bug.
// The kinds themselves live in internal/kind and its sub-packages.
package schema

// FieldType is the YAML type a field accepts.
type FieldType string

const (
	StringType FieldType = "string"
)

// Field describes one settable field of a kind.
type Field struct {
	Name        string
	Type        FieldType
	Required    bool
	Description string // shown in starters, docs, and error messages
	Example     string
}

// Kind describes one definition kind.
type Kind struct {
	Name        string
	Description string
	Fields      []Field
}

// Field returns the named field, if the kind has it.
func (k Kind) Field(name string) (Field, bool) {
	for _, f := range k.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}
```

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages. `schema` no longer exports `Lookup`; nothing references it (parse switched in Task 3, generate in Task 4).

- [ ] **Step 5: Commit**

```bash
git add internal/schema/schema.go internal/schema/schema_test.go
git commit -m "refactor(schema): slim to types-only; registry now lives in kind"
```

---

### Task 6: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full build, vet, and test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all succeed; golden test passes (byte-identical output).

- [ ] **Step 2: Confirm the old module directory is gone and the new home exists**

Run: `test ! -e internal/generate/modules && ls internal/kind/s3bucket/module`
Expected: lists `main.tf  outputs.tf  variables.tf`; no error from the `test !` guard.

- [ ] **Step 3: Confirm no lingering `schema.Lookup` references**

Run: `! grep -rn "schema.Lookup" --include="*.go" .`
Expected: exit 0 (no matches).

- [ ] **Step 4: Confirm the working tree is clean and committed**

Run: `git status --porcelain`
Expected: empty output.
