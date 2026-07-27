# Goal

RDK — the **Repository Development Kit** — manages the common machinery of a
service repository so developers only write application logic. It is a Go CLI
(installable with `go install`) aimed at services deployed to the cloud.

One YAML definition file per resource in, a complete working repository out:
infrastructure, handler scaffolding, least-privilege IAM, CI/CD pipeline,
documentation, and diagrams — for every environment, identically.

This document is the current statement of intent. It reflects the decisions in
[design-decisions.md](design-decisions.md) (DD-1…16); the rules that bind all
development are in [philosophy.md](philosophy.md); the survey of neighbouring
tools and the faults this design answers is in
[alternatives.md](alternatives.md). Where this document and a DD disagree, the
DD wins and this document is stale.

## How it works

Three strictly separated stages (philosophy rules 1–2):

1. **Definitions** (user-owned, `rdk/` directory): one YAML file per resource,
   plus global config and policies. Declarative only — structured references
   (`{ ref: resource.output }`) yes, expressions never (DD-12).
2. **Generation** (`rdk apply`): a pure, offline, deterministic function of
   (definitions, rdk version, previous manifest). It writes the repo: thin
   Terraform JSON calling versioned modules (DD-2), handler scaffolding
   (DD-3), the CI/CD pipeline (DD-8), bootstrap configs (DD-15), docs and
   diagrams. It never touches the cloud, reads no state, and is verified by
   golden tests (DD-1). Identical inputs give byte-identical output; managed
   files are always rewritten, never diffed (rule 13).
3. **Deployment** (the generated pipeline): GitHub Actions runs
   `terraform plan` on PRs and `apply` on merge, per environment, with
   env-type gating (stage auto-applies; production requires approval). The
   pipeline — not rdk — owns state backends, locking, and credentials (DD-8).

## Concepts

### Environments

An environment is a place to deploy — an AWS account plus configuration.
**Every environment receives the identical resource graph** (DD-9): no
per-environment resources, no presence conditions. Variation is values-only,
through three channels: policy (env-type-resolved settings), feature flags
(gating behavior, not presence — idle-but-uniform infrastructure is accepted
as cheaper than divergence), and selectors (per-environment asset/config
choice, e.g. branding files for white-labels). Environments carry a type
(`stage`, `production`) that drives policy resolution and pipeline gating;
the exact shape (type + labels) is still open.

### Resources and kinds

A resource is a deployable thing — lambda, S3 bucket, DynamoDB table,
CloudFront distribution. Each definition names its kind, its purpose
(feeding generated docs), and its settings. **Every resource is ultimately a
definition → Terraform module call** (DD-2/DD-5): built-in kinds invoke
rdk-shipped, versioned HCL modules; the escape hatch (`kind: terraform`) is
the same mechanism with a user-supplied module seeded into a user-owned
directory (DD-5).

Kinds that need code (lambdas) get scaffolding: a managed entrypoint imports
a user-owned package, seeded once with a stub implementing the required
interface, then never touched again (DD-3). Generated and hand-written code
never share a file (rule 3).

### Connections and references

Connections are typed links between resources (`reads`, `writes`,
`triggered-by`, …) declared once, on the side the connection type names
canonically (DD-6). rdk normalizes all definitions into a validated directed
graph — the single source for generating IAM on *both* endpoints, injecting
ARNs/URLs/names as inputs and env vars, and drawing the architecture diagram.
Where only a value (not behavior) must cross between resources, structured
refs carry it (DD-12); rdk compiles them to Terraform interpolation, which
never appears in definitions.

### Policies

Policies resolve settings by environment type: a sizing policy's `large`
might mean 384 MB in stage and 512 MB in production; a mandate might require
OpenTelemetry everywhere. Resolution is one totally-ordered stack — module
default → improved-defaults pin → default policy → explicit setting →
mandate — and every resolved value carries provenance: `rdk explain` answers
"why is this lambda 512 MB here?" exactly (DD-7).

Shared **policy sets** can live outside the repo (corporate standards),
consumed Go-modules style: explicit semver git tags, vendored by
`rdk policy update`, hash-locked, verified by apply, never fetched at
generate time. Corporate sets the floors and ceilings (mandates beat local);
local fills the middle (defaults lose to local) (DD-16).

### Escape hatches

Paved road, first-class doors (rule 8): `kind: terraform` for resources rdk
doesn't model (DD-5); named pipeline extension points invoking user-owned
Make/script hooks for CI steps rdk doesn't model (DD-10). Beyond a door,
rdk's guarantees stop — and say so.

### Ownership and the manifest

Every file is wholly rdk's or wholly the user's — never shared (DD-14).
rdk-owned files live in one managed directory, deleted and fully regenerated
each apply. The few rdk-owned files that must live elsewhere (CI workflows)
are tracked in a manifest — rdk's only cross-apply state — recording the
last-apply rdk version and a hash per file. Edits to rdk-owned files are
hard errors pointing at the sanctioned alternative; stale unedited files are
deleted; user-owned files are never deleted (orphans get warnings).

### Authoring assistance

The blank page is rdk's problem (DD-13). Each kind's schema carries rich
per-field metadata — docs, defaults, examples — from which every aid is
projected: JSON Schema for live editor validation, commented starter files
(`rdk new <kind> <name>`), AI skill files, human READMEs, and error messages
that cite field documentation. One source; the projections cannot drift.

### Bootstrap

The state backend, OIDC provider, and plan/apply deploy roles must pre-exist
the pipeline. rdk *generates* per-environment bootstrap Terraform plus a
runner script; a human with elevated credentials runs it once (DD-15). rdk
itself never calls a cloud API.

### Improved defaults

When an rdk upgrade improves a kind's implementation, existing resources are
pinned to their old behavior rather than silently changed; every apply warns
about live pins until they're deliberately migrated or become redundant
(DD-7). Module version pinning (DD-2) is the coarse mechanism; the exact
division with setting-level pins is deferred until built.

### Documentation output

`rdk apply` also writes docs: per-resource purpose descriptions and
architecture diagrams derived from the connection graph, in deterministic
text formats (Mermaid) so they diff and golden-test like everything else.

## AI friendly

Designed for, not asserted (rule 11): every resolved value is traceable to
its source (`rdk explain`, JSON output); errors name the user's file and
field and what to do next; schemas, skills, and starters let an AI author
correct definitions without seeing rdk's source.

## v1 — the wedge

Deliberately narrow, end-to-end (DD-11): **Go handlers, AWS, GitHub
Actions**, one definition format (YAML kinds), resources Lambda + DynamoDB +
S3 + CloudFront, connections, environments, pipelines, policy support, both
escape hatches, docs output. Built as a sequence of independently valuable
PRs — spine first (deterministic generation, managed-dir lifecycle), then
first resource, connections, pipeline, breadth, hardening. Deferred past v1:
OpenAPI/GraphQL definition formats, other languages/clouds/CI, feature
flags/selectors, improved-defaults pins, external policy sets.

## Hard lines

Things rdk will not do, by design:

- Deploy, or read anything from the cloud (rule 2).
- Fetch anything at generate time (rule 1).
- Vary the resource graph per environment (rule 4, DD-9).
- Allow expressions in definitions (rule 9, DD-12).
- Share a file with the user, or rewrite user files as an apply side effect
  (rules 3, 13).
- Silently override, silently delete, or silently drift (rule 6).
