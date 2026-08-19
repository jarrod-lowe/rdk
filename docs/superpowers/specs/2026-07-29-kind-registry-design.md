# `internal/kind` — each resource kind gets a cohesive home

**Status:** approved design, pre-implementation. Folded into PR-1 (the spine)
before it grows a second and third kind.

## Problem

A "kind" is not a thing in the code — it is scattered across three packages that
do not know about each other:

- `internal/schema` — `s3-bucket` is a `Kind` *data* entry (fields, docs) in a
  flat `registry` map.
- `internal/generate/modules/s3-bucket/` — the embedded HCL, found by string
  convention (`"modules/" + kind`).
- `internal/generate` — `tfDoc` and `vendorModule` treat *every* kind
  identically: dump all `Attrs` as module inputs, copy the module dir.

That uniformity is exactly why s3 "disappears": it works only because s3 is
trivially 1:1. The moment a kind needs non-trivial definition→input mapping (S3
lifecycle rules), declared outputs as an I/O contract (DD-5/DD-12), or connection
participation (DD-6 bilateral effects), there is no home for that logic — it
lands as `if kind == "..."` special-casing inside the generic spine, or as
another scattered fourth touch point. Each future kind (Lambda, DynamoDB,
CloudFront) multiplies the scatter.

## Goal

Give each kind **one cohesive home** that the spine dispatches into, and name
(without building) the place where DD-6 connections will attach. Adding a kind
becomes adding a package, never editing a central `switch`. This is philosophy
rule 7 (one mechanism, reused) applied to resource kinds: the spine is generic
dispatch; the kind is the reused unit.

## Scope (deliberately narrow)

**Structural home only.** Build the per-kind cohesion unit and make the spine
dispatch into it. Do **not** build the connection engine, and do **not** yet
formalize the I/O-contract (`Outputs()`) — those are PR-3 (DD-11 sequencing).
The deliverable is that connections and the I/O contract have an obvious,
documented place to land, with no dead code standing in for them (YAGNI).

This is a pure refactor: `testdata/golden/basic` must stay **byte-identical**.

## The abstraction

A new registry package, `internal/kind`, defines two interfaces and holds the
registry of kinds. Each kind is a self-contained sub-package owning its schema,
its HCL module, and its input mapping.

```text
internal/
  schema/            types only: Field, FieldType, Kind, helpers  (data moves out)
  kind/              registry + interfaces  → imports schema, config, s3bucket
    config/          → imports schema
    s3bucket/        → imports schema;  //go:embed module
      module/        the HCL, moved from internal/generate/modules/s3-bucket/
  parse/             validates via kind.Lookup  → imports schema, kind
  generate/          → imports parse, kind
```

### Interfaces

```go
// internal/kind

// Kind is every kind writable in a definition file: it has a schema, so it can
// be validated and projected into authoring aids (DD-13).
type Kind interface {
    Name() string
    Schema() schema.Kind
}

// Resource is a kind that becomes a Terraform module and joins the connection
// graph. Only Resource kinds are vendored and emitted as module calls.
type Resource interface {
    Kind
    ModuleFS() fs.FS                                            // embedded HCL subtree, rooted at the module dir
    ModuleCall(attrs map[string]any) (map[string]any, error)   // attrs → module inputs; the home for kind-specific mapping
}

func Lookup(name string) (Kind, bool)   // parse: validation
func All() []Kind                        // sorted by name; DD-13 projections + generate
```

- `ModuleCall` takes the **attrs map**, not `parse.Definition`, so kind packages
  never import `parse` — which is what lets `parse` import the registry without a
  cycle. For s3 today it returns a copy of the attrs (identity). S3 lifecycle
  rules later become a structured-attrs → module-input transform *here*.
- `ModuleCall` keeps an `error` return even though the identity mapping cannot
  fail yet: it is the natural shape once transforms arrive, and avoids
  re-touching every kind's signature later. Callers **must** check it and
  propagate — never `_`-discard — so the day a mapping *can* fail there is no
  pre-existing silent path to fix. `tfDoc` returns the error up through
  `generate.Build`.
- `ModuleFS()` returns the module subtree (e.g. `fs.Sub(embed, "module")`), built
  once in the kind's constructor. `generate` walks it; the spine still owns the
  `terraform/modules/<kind>/` output path and the `./modules/<kind>` source
  string, both keyed off the kind **name**.

### Dependency direction (cycle-free)

`schema → kind/* → kind → parse → generate`. The one-way flow is guaranteed by
`ModuleCall` taking attrs (kind packages depend only on `schema`), so `parse` and
`generate` can both import `kind`.

### Registration

Explicit, not `init()` magic: a central constructor list in `internal/kind`
(`config.New()`, `s3bucket.New()`) registered into a name-keyed map. `All()`
returns them **sorted by name** for determinism (DD-1). Explicit construction is
testable and has no import-ordering surprises.

### Directory-name vs kind-name

Go package dirs cannot contain hyphens, so the package is `s3bucket` and its HCL
lives in `internal/kind/s3bucket/module/`. The kind's **name** is still
`s3-bucket`, and the vendored output path stays `terraform/modules/s3-bucket/`
and the module `source` stays `./modules/s3-bucket`. Source-dir name (Go-legal)
is decoupled from output-dir name (kind name); golden output is unchanged.

## What the existing packages lose

- **`schema`** keeps the *types* (`Field`, `FieldType`, `Kind`, the `Field()`
  helper) and, later, DD-13 projection logic operating on `schema.Kind` values.
  The `registry` data and `Lookup` move into the kind packages — each kind now
  owns its field metadata alongside its module and mapping.
- **`parse`** swaps `schema.Lookup` → `kind.Lookup`. Validation logic is otherwise
  unchanged (still schema-driven; `ModuleCall` assumes already-valid attrs).
- **`generate`** loses `//go:embed modules`, the `"modules/"+kind` string-globbing
  in `vendorModule`, and *both* `d.Kind == "config"` checks. `tfDoc` builds inputs
  via `r.ModuleCall(d.Attrs)`; vendoring walks `r.ModuleFS()`.

## config falls out of the magic-string check

`config` implements only `Kind` (it has a schema, produces no module, is not a
graph node). `generate` selects deployable resources by type-asserting
`k.(Resource)` — `config` does not satisfy it and is skipped **structurally**.
The `if d.Kind == "config"` special-cases disappear; "is this a deployable
resource?" becomes "does it implement `Resource`?" This cleanup is earned by the
refactor, not bolted on.

## The connections seam (named, not built)

Per the scope choice, no engine and no `Outputs()` method now. But the homes are
unambiguous and recorded here so PR-3 plugs in without re-architecting:

- A resource kind's **own side** of a connection (the IAM/env/policy it
  contributes as an endpoint, and the outputs it exposes) attaches as future
  methods on `Resource` (`Outputs()`, then connection-support). The `s3bucket`
  package is where "what a bucket exposes" and "what a `reads` edge does to a
  bucket" will live.
- The **connection type** itself — DD-6's bilateral, versioned artifact — becomes
  a sibling `internal/connections` catalog that references kinds by name.

Neither is added now: no dead interface methods, no empty package.

## Naming decisions

- Package `internal/kind`, base interface `kind.Kind`, resource interface
  `kind.Resource`. `kind.Kind` reads slightly awkwardly next to `schema.Kind`,
  but the base interface is rarely named directly — code mostly touches
  `kind.Resource`, `kind.Lookup`, and `kind.All`. Accepted as-is over renaming the
  package or the metadata struct.
- `ModuleCall` retains its `error` return (justified above).

## Testing

- **Golden is the acceptance test.** `testdata/golden/basic` must remain
  byte-identical; if the generated tree changes, the refactor is wrong. Verify —
  never blind-update.
- Per-kind unit tests: `s3bucket.ModuleCall` (identity today), and that
  `s3bucket.ModuleFS()` contains the expected `.tf` files.
- Registry test: `Lookup` hit/miss, `All()` sorted, and that `config` is a `Kind`
  but not a `Resource` while `s3-bucket` is both.
- Existing `parse`/`generate` tests updated to the registry; behavior unchanged.

## Migration steps

1. `internal/schema`: keep the types; remove the `registry` data and `Lookup`.
2. `internal/kind`: add `Kind`/`Resource` interfaces, the registry (`Lookup`,
   `All`), and explicit registration.
3. `internal/kind/config`: implement `Kind` with the config schema.
4. `internal/kind/s3bucket`: implement `Resource` (Schema, ModuleFS via
   `//go:embed module`, identity ModuleCall); move the HCL from
   `internal/generate/modules/s3-bucket/` → `internal/kind/s3bucket/module/`.
5. `internal/parse`: `schema.Lookup` → `kind.Lookup`.
6. `internal/generate`: drop the embed, the string-globbing, and the `config`
   checks; dispatch via `kind.Lookup` + `Resource` type-assertion.
7. Verify golden byte-identical; add the new unit tests.

## Residual risk / still open

- **The `Resource` interface will grow.** `Outputs()` and connection-support land
  in PR-3; a bad shape now (e.g. `ModuleCall`'s signature) costs a multi-package
  edit. Kept minimal deliberately, and it is a single-repo refactor when it moves.
- **`config` may not stay a lone non-`Resource`.** If a future non-deployable
  kind appears (a policy set, DD-16), the `Kind`-but-not-`Resource` split is
  already the right seam; revisit only if a third category emerges.
- **Schema-projection commands (DD-13) are not yet built.** They will enumerate via
  `kind.All()`; this design puts the registry in the right place for them but does
  not build them.
