# Design Decisions

Concrete decisions that refine `GOAL.md` and mitigate faults raised in
`alternatives.md`. ADR-style: each records the decision, why, and the **residual
risk we're still on the hook for** — so mitigating a fault doesn't quietly become
"fault solved, stop thinking about it."

## DD-1 — Generation is strictly deterministic, verified by golden tests

**Decision.** `rdk apply` generation is a pure function of the definition inputs
(plus the pinned rdk version): identical inputs produce byte-identical output. A
test harness runs rdk over a library of definition sets and asserts the produced
tree matches committed golden output exactly. When output legitimately changes,
the workflow is: review the diff, then update the golden files.

**Why.** Directly targets the "delete-and-rewrite the managed dir" hazard
(alternatives.md fault #3). If regeneration is a guaranteed no-op when nothing
changed, a stray `apply` cannot perturb live infrastructure.

**Residual risk / still open.**

- **Determinism is not the same property as address stability**, and address
  stability is the one that actually protects infra. Golden tests over *fixed*
  inputs prove "same input → same output." They do **not** prove "a small input
  change → a small, safe output change." The dangerous case is a Terraform
  *resource address* shifting (a rename, a reorder, a `count` change, or an
  rdk-version change to naming) → Terraform plans a destroy/recreate of a live
  resource. To close this we also need:
  1. resource addresses derived deterministically from stable, user-chosen names
     — never from ordering or positional index;
  2. **evolution/delta golden tests** — take a definition set, apply a delta
     (rename / add / remove), and assert the resulting *diff*, specifically that
     no unintended destroy/recreate appears;
  3. a surfaced `terraform plan` for review before `apply` mutates anything.
- **Go map iteration is randomized.** The generator must sort keys / use ordered
  structures everywhere, or the golden tests themselves will flake. This is the
  concrete first determinism bug to guard against.

## DD-2 — Thin generated `.tf.json`; Terraform modules do the heavy lifting

**Decision.** rdk emits *thin* Terraform in native JSON syntax — essentially
`module` blocks wiring user-facing inputs to rdk-maintained Terraform modules
written in HCL. The generator never builds an HCL emitter and never encodes rich
resource logic in JSON; all substantive resource logic lives in versioned HCL
modules rdk ships.

**Why.**

- JSON serializes trivially and deterministically from Go structs (supports
  DD-1), and module calls are the *one* thing JSON expresses cleanly —
  scalar/list/map inputs, no complex expressions. The awkward-in-JSON constructs
  (expressions, meta-arguments) stay in HCL where they're ergonomic.
- Heavy logic lives in real, human-readable, independently testable HCL
  (`terraform test` / terratest) rather than machine-generated config.
- **Module version pinning is the natural vehicle for "Improved Defaults"
  (fault #5)** and rdk-version upgrades: pin a module version per resource; new
  resources get the newer module; existing ones stay pinned until deliberately
  migrated.
- Modules give a first-class tool for the DD-1 address-stability residual:
  internal refactors ship `moved {}` blocks, so a module upgrade need not
  destroy/recreate.
- Provides a natural **extension seam** (eases fault #7): power users can be
  handed raw passthrough inputs, or drop their own module alongside, without rdk
  having to model everything.

**Residual risk / still open.**

- **Module distribution + versioning is the key open decision.** Two shapes:
  (a) rdk **vendors pinned module source** into a managed modules dir — fully
  reproducible from the rdk binary version, offline, no registry, but bloats the
  repo; (b) reference a **remote registry/git source by version (+ hash)** —
  small repos, but adds a `terraform init` network dependency and supply-chain
  surface. DD-1's determinism goal and the AI-friendly/offline goal lean toward
  (a); repo size leans toward (b). Pick deliberately.
  **→ Ratified (PR-1):** option (a), vendored — modules are embedded in the rdk
  binary (`go:embed`) and written into the managed dir by apply. Fully hermetic
  and offline; module version ≡ rdk version.
- **Address stability moved *inside* the module.** The DD-1 residual doesn't
  vanish — a module upgrade that renames internal resources still
  destroys/recreates unless the module authors `moved {}` blocks. Convention:
  every module refactor ships `moved` blocks, and evolution/delta golden tests
  cover module version bumps.
- The resource-definition → module-input mapping is another schema surface; keep
  it mechanical and deterministic, and treat a module's changed variable
  interface as a version-migration event (ties back to Improved Defaults).
- Comments still can't live in `.tf.json` (use a Terraform-ignored `//` key if
  annotations are needed), and the encoder must emit deterministic key ordering.

## DD-3 — User code lives behind a seeded, never-regenerated boundary

**Decision.** The managed dir contains the resource entrypoint (e.g. a Lambda's
`main`), which imports a *user-owned* directory. On first creation, rdk seeds
that directory with a stub implementing the required call-in signature (returning
a base/placeholder or error value). After seeding, rdk never rewrites that file —
it belongs to the developer. The seam between generated and hand-written code is
a **directory/import boundary**, not marker-regions inside a shared file.

**Why.** Targets the "managed-vs-editable code boundary is hand-waved" fault
(#6). Managed code is fully owned and overwritten freely; user code is created
once and then untouched. No in-file merging, ever.

**Residual risk / still open.**

- **Express the call-in contract as a Go interface** the user code implements,
  rather than a bare function the entrypoint calls. The compiler then enforces
  the signature; when a future rdk version must change the required signature,
  the failure is a localized, clear compile error *in the user's file* (good, and
  AI-friendly) instead of a confusing error buried in managed code.
- Seeding rule is "file absent → seed; file present → never touch." Decide what
  happens to user files when their resource is *deleted*: leaving them (orphaned)
  is safer than rdk deleting developer code — prefer **warn-and-leave** over
  silent removal.
- Signature evolution across rdk versions still needs a migration/messaging story
  (ties into Improved Defaults, fault #5 — a later pass).

## DD-4 — Reconcile happens before the commit, in the working tree

**Decision.** `rdk apply` regenerates managed files (and any rdk-owned files
outside the managed dir) in the working tree; the developer reviews the ordinary
`git diff` and commits. There is no external "template update" re-applied over
already-committed, diverged user code — so there is no three-way merge.

**Why.** Targets the Copier-style merge-conflict risk. Combined with DD-3 (rdk
never rewrites user files), the only files rdk overwrites are ones it fully owns,
so there is nothing to conflict *with*.

**Residual risk / still open.**

- **rdk-owned files that can't live in the managed dir** (GOAL.md's own open
  question) are the weak spot. If rdk overwrites one the user also edited, that's
  silent edit-loss — visible in `git diff`, but only if the developer looks. The
  robust version: maintain a **manifest of rdk-owned paths + the hash rdk last
  wrote**; on apply, if an owned file's current content ≠ last-written hash,
  *warn* ("you edited rdk-managed file X") instead of clobbering. The same
  manifest answers GOAL.md's "how do we know when to remove outside files":
  remove manifest entries that are no longer produced.
- The flow leans on the developer actually reading the diff. The manifest/hash
  check is what keeps it safe when they don't.
- **→ Resolved by DD-14** (manifest designed; edits are hard errors, not
  warnings; clean stale files are deleted).

## DD-5 — A raw-Terraform definition type is the first-class escape hatch

**Decision.** One definition type lets a developer supply Terraform directly. rdk
creates a user-owned Terraform module directory for it (seeded once with the
skeleton — `variables.tf` / `main.tf` / `outputs.tf`) and generates the thin JSON
`module` call that invokes it with the values specified in the definition. The
module *body* is the developer's; the *call* is rdk-managed.

**Why.**

- Gives the SAM-style "drop to raw IaC" escape valve the plan was missing
  (fault #7) — but structured as a module call, so it reuses the *same* machinery
  as every other resource (DD-2). Nothing bespoke.
- Unifies the model: **every resource is ultimately a definition → module call.**
  Built-in types are rdk-shipped modules with rich definitions; the raw type is
  the same mechanism with a user-supplied module. The escape hatch validates the
  architecture rather than bolting onto it.
- The user module dir is owned exactly like DD-3 user code (seed-once, never
  rewritten), consistent with the managed/owned boundary already set.

**Residual risk / still open.**

- **The raw module is opaque to rdk — this is where connections (fault #9) and
  the auto-diagram get hard.** rdk can't see inside to know what it creates or
  what IAM it needs. To participate in connections it must **declare its inputs
  and outputs** in the definition (e.g. "exposes output `bucket_arn`", "consumes
  input `queue_url`") so rdk can wire edges. Absent that contract, a raw resource
  is an opaque node in the graph carrying only its description.
- **The escape hatch is where policy guarantees end.** Naming/sizing/OTel
  policies can't be enforced inside an opaque module. The best rdk can do is
  *pass policy-derived values as inputs* (computed name prefix, memory size, otel
  flag) and trust the module to honor them — it cannot guarantee compliance. Name
  this limit explicitly.
- **Provider/version reconciliation.** A raw module may pull its own providers or
  version constraints that conflict with rdk-shipped modules at `terraform init`.
  Decide how providers are passed to / constrained for user modules.
- Improved-Defaults version pinning (DD-2) does **not** apply — it's user code,
  not an rdk-shipped module, so upgrade and `moved`-block discipline fall to the
  developer.

## DD-6 — Connections are typed, declared once, and normalized into a graph

**Decision.** Keep GOAL.md's rule (no connection files; a connection is mentioned
in a resource), but pin down the three things it left open:

- **Connections are typed**, from a versioned rdk catalog (`reads`, `writes`,
  `triggered-by`, `publishes-to`, `subscribes-to`, `invokes`, …). Each type
  defines: the two endpoint *roles* it links, the direction, and the **bilateral
  effects** — the IAM statements, resource policy, event-source config, and
  injected identifiers (ARNs / names / URLs as module inputs or env vars) that
  the link produces on *each* side. A connection type is a shipped, versioned
  artifact like a module.
- **Declared exactly once**, inline in one resource, on the **canonical side
  named by the connection type** (not chosen ad hoc per repo). One source of
  truth per edge — no dual declaration to drift.
- rdk parses every definition into a **normalized, directed connection graph**.
  That graph — not the scattered source mentions — is the single source of truth
  for generating permissions/config on *both* endpoints, injecting identifiers,
  and drawing the auto-diagram.
- **Wiring runs over the DD-5 input/output contract.** A connection consumes
  named *outputs* of one endpoint and feeds them as *inputs*/env vars to the
  other. Because built-in modules and raw-Terraform modules (DD-5) both declare
  I/O, they connect through the identical mechanism — this is the payoff that
  makes #9 and DD-5 one design, not two.
- **The graph is validated**: both endpoints exist; the connection type permits
  those two resource types in its roles; duplicate/contradictory edges and
  dependency cycles are hard errors.

**Why.**

- A single canonical declaration side kills the "which side owns it / what if
  both mention it" inconsistency at the root.
- Bilateral effects from one declaration mean "lambda reads bucket" (declared on
  the lambda) can still emit the IAM on the lambda role, the env var into the
  lambda, *and* a bucket-policy statement on the bucket — without the developer
  touching the bucket definition.
- Normalizing to a graph makes the auto-diagram and permission generation read
  from one validated structure, so "scattered in source" stops mattering.
- Unmodeled connections have an escape hatch already: a raw-Terraform resource
  (DD-5) writes the wiring by hand — connections inherit DD-5's escape valve.

**Residual risk / still open.**

- **Effects on a resource now originate elsewhere.** To know everything shaping
  my lambda's IAM/env, I must find every connection *pointing at* it, not just
  the ones in its file. This is the same non-local readability problem as policy
  precedence (#4/#12); the answer is the same — a traceability command
  (`rdk explain <resource>`) and generated per-resource "connections in/out"
  docs. Ties #9 to the #4/#5/#12 work.
- **Choosing the canonical declaring side per type is a real taste call.** If it
  mismatches developer intuition ("I looked in the lambda, but the trigger is
  declared on the bucket"), it frustrates. Mitigation: document it in the type
  catalog and have diagnostics say exactly where to declare. Considered
  alternative — allow declaration on either end but forbid both — was rejected
  because it makes "where do I look" non-deterministic for readers.
- **Interaction with fault #2.** If env-specific resources are ever allowed, a
  connection may reference a resource absent in some environment — needs a rule
  (error, or conditional edge). Revisit when #2 is designed.
- **Provider constraints leak through.** Some links have provider limits (e.g.
  S3 event-notification overlap rules, one-config-per-event constraints). The
  connection engine must validate against these, not just against rdk's own type
  rules — extra per-type work.
- **External/unmanaged endpoints** (connect to a pre-existing bucket/ARN not
  managed by rdk) are out of scope initially; the I/O-contract model could later
  admit a "data source" resource type to cover them.

## DD-7 — Value resolution is one ordered, provenance-carrying pipeline

Addresses the interlocking cluster #4 (precedence matrix), #5 (Improved Defaults),
and the traceability half of #12 (AI-friendly). The core move: make "every value
is traceable to its source" the *implementation strategy*, not a constraint fought
against — the same pipeline that resolves values emits their provenance.

**Decision — a single, totally-ordered precedence stack.** For each setting, on
each (resource, environment), the value resolves through a fixed layer order,
lowest → highest precedence:

1. **Module default** — what the resource's current handler module ships.
2. **Improved-Defaults pin** (rdk-managed) — a legacy value preserved for a
   pre-existing resource so an rdk upgrade doesn't silently change it.
3. **Default-providing policy** — an applied policy's value, resolved for the
   environment's *type* (e.g. sizing `large` → 384 MB stage / 512 MB prod).
4. **Explicit resource-definition setting** — what the developer wrote.
5. **Mandating policy** — "must" policies (e.g. OTel required), resolved by
   env-type; wins over everything. A conflicting explicit value is a **surfaced
   error**, never a silent override.

**Decision — provenance is a first-class output.** The resolver is a function
`resolve(setting, resource, env) → {value, winning_layer, source, losers[]}`.
`source` is concrete (definition file+path, or policy name + env-type branch, or
"module default", or "pin introduced in rdk vX"). A `--trace` mode records the
considered-but-lost layers too. `rdk explain <resource> [--env E]` prints, per
setting, the value and its provenance — and, reusing DD-6's graph, the
connection-injected env vars / IAM with their originating connection. This is the
**one traceability tool demanded by both this cluster and #9**; it also emits JSON
for the AI-friendly goal, and warnings/errors cite provenance.

**Decision — Improved-Defaults pins are tightly bounded (tames #5).** A pin is
minted **only** for a setting whose *winning layer was the module default* at the
moment an upgrade changed that default. Consequences:

- A pin can never conflict with a policy or an explicit value — those would have
  won, so no pin is minted. This collapses most of #5's feared complexity.
- A pin is `(resource, setting, value, rdk-version-introduced)`.
- rdk reconciles pins every apply and **retracts** one when it is redundant
  (current default now equals the pin), superseded (setting became explicit or
  newly policy-governed), or orphaned (resource removed) — per GOAL.md.
- Every apply **warns** about live pins (legacy value, new default, how to
  migrate), keeping old usage visible. Requires recording the last-apply rdk
  version (GOAL.md already calls for this).

**Why.** One total order kills the "which of four layers won?" ambiguity. Binding
pins to "default was winning" bounds the time-varying layer instead of letting it
multiply. And carrying provenance through resolution means #12's traceability is a
byproduct, not an afterthought — so adding layers no longer *reduces*
explainability, because each layer announces itself.

**Residual risk / still open.**

- **Two overlapping "keep the old behavior" mechanisms.** DD-2 pins a whole
  *module version* per resource; this DD pins individual *setting values*. If a
  resource stays on its old module version, its defaults never change, so no
  setting-pin is needed — the mechanisms overlap. **Decide the division
  deliberately** (proposal: module-version pin is the coarse default-preservation
  mechanism; setting-level pins are the exception for when rdk *does* move
  everyone to a new module version but must preserve one specific legacy value).
  Do not build both without settling this.
- **Intra-layer conflicts, not just inter-layer.** Two applied policies both
  setting `memory` is a conflict *within* layer 3. Need a rule: explicit policy
  priority, or error-on-conflict (leaning error, unless one is a mandate).
  **→ Partly settled by DD-16** (external loses defaults / beats mandates vs
  local); two *local* policies colliding still needs the error-on-conflict call.
- **Mandate vs explicit is a hard error — is that too rigid?** Blocking an
  explicit value that violates a mandate is visible but may frustrate; some
  mandates are boolean (OTel on) while others are ranges (min memory) where
  *clamping* might be friendlier than erroring. Decide per mandate kind.
- **Provenance cost.** Storing full loser-lists for every setting×resource×env is
  verbose; cheaper to recompute with `--trace` on demand rather than persist.
  `rdk explain` re-runs resolution with tracing rather than reading stored data.
- **#12 is only half-addressed.** This nails *value traceability*; the broader
  "helpful output on every error/warning" remains a cross-cutting principle to
  uphold everywhere, not a solved item.
- Keep **type-resolved settings** (policies resolve by env-type) distinct from
  **per-env identity** (account, names, ARNs resolve per specific environment).

## DD-8 — `rdk apply` generates the repo; a generated pipeline deploys

**Decision.** `rdk apply` is **pure, local, offline generation** — it writes
content into the repo (managed dir, module calls, handler stubs) and never
touches cloud or Terraform state. Deployment is the job of a **CI/CD pipeline
that `rdk apply` itself generates**. That pipeline is what runs `terraform
init/plan/apply` per environment, and it owns every stateful concern:

- **state backends** (one isolated backend/state per environment/account);
- **locking** (lock table / native lock);
- **per-environment isolation and fan-out** (the pipeline iterates environments);
- **ordering and gating** driven by environment *type* (e.g. stage auto-applies,
  production requires manual approval);
- **credential assumption** per environment (assume a per-account deploy role /
  OIDC);
- the **plan → review → apply** flow (plan on PR, apply on merge).

**Why.**

- Keeps `rdk apply` deterministic, offline, and testable by golden files (DD-1)
  — no credentials, no state, no network in the generation step.
- It's self-hosting and consistent with the whole philosophy: rdk generates the
  very pipeline that deploys rdk's output, the same way it generates everything
  else.
- **It resolves DD-1's "plan gate before mutation" residual for free** — the gate
  is literally the pipeline's plan-on-PR / apply-on-merge flow, not something
  bolted onto `apply`.
- Env-type already drives policy resolution (DD-7); here it also drives pipeline
  topology (which environments, in what order, with which gates), so the two
  reuse one concept.

**Residual risk / still open.**

- **Bootstrap chicken-and-egg — the genuinely hard part.** The state backend
  (bucket + lock) and the pipeline's deploy role / OIDC trust must exist *before*
  the pipeline can run Terraform, so they can't be created by that same pipeline.
  Needs a one-time bootstrap path (an `rdk init`-adjacent step, run per account
  with elevated credentials) and a decision on how the bootstrap's *own* state is
  held (minimal, then migrate local→remote, or a dedicated management account).
  **→ Resolved by DD-15** (generated bootstrap config + script, run by a human;
  state self-hosted via init-migrate).
- **Credential/trust provisioning is out-of-band.** rdk can generate the role/
  OIDC Terraform and the pipeline's references to it, but *running* that
  provisioning needs elevated access a normal pipeline run won't have — same
  bootstrap problem.
- **The pipeline files live outside the managed dir** (`.github/workflows/…`), so
  they are exactly the "rdk-owned files outside the managed dir" case flagged in
  DD-4 — this makes the DD-4 manifest/hash guard necessary, not optional.
- **CI-platform coupling.** Pipeline generation is a per-platform handler; pick
  one first (GitHub Actions, matching fault #11's wedge) and keep it pluggable.
  The generated pipeline is a large surface and is itself under DD-1 golden tests.
- **Module distribution (DD-2) resurfaces at pipeline runtime.** If modules are
  remote, the pipeline's `terraform init` needs network + registry access; if
  vendored, runs are hermetic. #8 strengthens the lean toward **vendoring**.
- **Naming least-surprise.** `rdk apply` does *not* mean "apply to cloud" (unlike
  `terraform apply`); it applies definitions to the repo. Worth a deliberate
  naming/clarity call so Terraform users aren't surprised.
- Interacts with fault #2: a per-environment fan-out assumes the same resource
  set everywhere; env-specific resources would make the pipeline matrix
  non-uniform. Revisit with #2.

## DD-9 — Strict uniformity: identical resource graph everywhere; variation is values-only

**Decision.** GOAL.md's rule stands *by choice*: every environment gets the
**identical resource graph** — same resources, same connections, everywhere.
There is **no presence divergence**. Similarity is treated as a high-value
property (staging genuinely tests production, zero structural drift, uniform
DD-8 pipeline fan-out, identical DD-6 connection graph per env), and is worth
paying for.

All white-label / per-environment variation flows through **three value-only
channels**, none of which change what resources exist:

1. **Policy** (DD-7) — env-classified settings (sizing, etc.).
2. **Feature flags** — per-environment flag values that gate *behavior*, not
   presence. A flag being off may leave some infrastructure deployed-but-unused
   in that environment; **this cost is accepted on purpose** because uniform-
   but-idle is, in practice, cheaper than managing structurally different envs.
3. **Selectors** — per-environment choice of an asset/config file (strings file,
   CSS-variables file, …). The resource is identical; the *content it's fed*
   differs by environment.

**Why.**

- Preserves the core value proposition (test fidelity, no drift) that presence
  divergence would erode.
- Keeps everything upstream simpler: DD-6's connection graph and DD-8's pipeline
  matrix are uniform across environments — this decision **retires the
  "interacts with #2" residuals** noted in both DD-6 and DD-8.
- All three channels reduce to per-environment *value resolution*, so they reuse
  DD-7's ordered/provenance pipeline conceptually rather than adding a structural
  axis.

**Residual risk / still open.**

- **"Unused infra is cheap" has a tail.** Idle-but-deployed is fine for most
  resources, but some cost real money or carry risk while idle (RDS instances,
  provisioned concurrency, NAT). The pattern for those: feature-flag-off **plus**
  a sizing policy (DD-7) that scales the idle case toward zero per environment.
  Flags gate behavior; policy gates cost. Also note idle resources still present
  a security surface and still have their connections/IAM wired even when unused
  (a minor least-privilege wrinkle).
- **Feature-flag mechanism is a new concept needing its own spec.** Where flags
  are declared (a per-environment values source), whether they're deploy-time
  (baked/injected as config/env vars — simplest, fits the generate-everything
  model) or runtime (a flag store, likely out of scope / itself a resource), and
  how handlers read them.
- **Selector mechanism needs specifying.** How variant asset files are stored and
  chosen, and where the selected content lands — almost certainly the DD-8
  pipeline injects per-env values and builds per-env artifacts. Consequence: the
  *infra* is identical across envs but deployment **artifacts are not byte-
  identical** (branding assets differ) — expected, but worth stating so it isn't
  mistaken for drift.
- **Environment model still needs enough structure to resolve the above.** Each
  environment needs its type (for policy) plus per-environment / per-variant
  key-values so flags and selectors can resolve, and so `wl1-stage` and
  `wl1-prod` can share variant branding without repetition. Minimal proposal:
  **type + labels/values**; the exact shape is the open sub-decision (was Q2).

## DD-10 — Pipeline escape hatch: generated pipeline calls user-owned hooks

**Decision.** The DD-8 pipeline is opinionated and generated, so — exactly as
DD-5 does for resources — it needs an escape valve. The generated pipeline
defines named **extension points** (e.g. pre-build, post-build, pre-plan,
pre-apply, post-apply, custom validation) and at each one **invokes a user-owned
hook** rather than embedding user logic in the generated file. Concretely: the
pipeline calls out to Make targets / hook scripts (the "make escape") that are
**seeded once and never regenerated** (the DD-3 boundary pattern). Users extend
the pipeline by filling in hooks, never by editing the clobbered generated
pipeline.

**Why.**

- Same ceiling-avoidance as DD-5: without it, anything the pipeline doesn't model
  forces editing generated (and overwritten) files. With it, custom steps live in
  user-owned files.
- Reuses patterns already decided: seed-once/never-touch (DD-3) for the hook
  files, and "generated code calls a stable user interface" (the entrypoint→user
  boundary) for the invocation.

**Residual risk / still open.**

- **Hooks run with deploy credentials** — a hook can do anything in the deploy
  context (blast radius / security). Define the contract: what env/vars/context a
  hook receives, and document that hooks are trusted code.
- **Hooks are opaque to rdk** (like DD-5 raw modules) — rdk can't reason about
  what a hook does, so they sit outside determinism/traceability guarantees
  (they're runtime, like DD-3 handler code — acceptable, but state it).
- The set of extension points is an API surface to get right early; too few and
  users are stuck, too many and the generated pipeline is a maze.

## DD-11 — v1 = the "compelling minimum," delivered as a sequenced set of PRs

**Decision.** Resolves the scope cluster (#1, #10, #11). v1 — the first point rdk
is *properly usable* — is deliberately larger than the minimum buildable thing,
because the value only appears when the pieces combine. v1 target:

- **Resources:** Lambda, DynamoDB, S3, CloudFront.
- **Connections** (DD-6) with auto-derived IAM.
- **Environments** (DD-9, strict uniformity) and **pipelines** (DD-8).
- **Policy support** (DD-7).
- **Both escape hatches:** raw Terraform (DD-5) and pipeline hooks (DD-10).
- **Documentation output:** `rdk apply` also writes docs (resource purposes) and
  diagrams (from the DD-6 graph).

**Scope discipline (the #10/#11 half).** v1 is **one definition format** (YAML
`kind:`; OpenAPI/GraphQL deferred), **Go + AWS + GitHub Actions only**, and
**excludes** Improved Defaults (nothing legacy to preserve yet), feature
flags/selectors, extra clouds/CI platforms, and additional resource types. Narrow
is embraced *as the wedge*, not apologized for.

**Sequencing (the #1 half — "too much for one PR").** Build the target across
PRs, each independently valuable and de-risking the next. Proposed order:

1. **Spine:** `rdk init`, YAML schema/parser, deterministic generation to
   `.tf.json` module calls (DD-1/DD-2), managed-dir lifecycle + user-code
   boundary + outside-files manifest (DD-3/DD-4). Module distribution decided
   (lean: vendored).
2. **First resource end-to-end:** Lambda (module + seeded Go handler behind an
   interface) + local single-env `terraform apply` by hand. Proves definition →
   deterministic repo → deployable.
3. **Value spine:** add S3 + one connection type (Lambda reads S3 → graph +
   bilateral IAM/env injection), + a naming-policy sliver (DD-7), + multi-env
   model (type + labels).
4. **Deployment:** generated GitHub Actions pipeline (DD-8) + bootstrap path +
   pipeline hooks (DD-10). Now stage→prod with gating works hands-off.
5. **Breadth:** DynamoDB, then CloudFront (last — most complex connections:
   origins, behaviors, OAC to S3), each with its connection types.
6. **Hardening/UX:** full policy precedence + `rdk explain` (DD-7), raw-Terraform
   escape hatch (DD-5), then docs + diagrams output.

**Why.** Each PR ships something demoable; the risky, foundational bets
(determinism, the module-call model, the managed/user boundary) land first and
cheapest; the adoption-compelling magic (auto-IAM + one-command multi-env deploy)
arrives by step 4; the widest-surface items (more resources, docs) come once the
model is proven.

**Residual risk / still open.**

- **v1 is still big** — #1's execution risk is managed by sequencing, not
  eliminated. Hold the line that every PR is independently valuable.
- **Narrowness (#11) remains a real adoption bet** — Go/AWS-only enters below
  Encore/Nitric on reach. Accepted as the wedge; revisit breadth post-v1.
- **CloudFront is the scope-creep risk in the resource set** — distributions,
  cache behaviors, and OAC/Lambda@Edge connections are markedly more complex than
  Lambda/S3/DDB. It is correctly sequenced last; be willing to slip it past v1 if
  it balloons.
- **Docs/diagrams need a deterministic, diff-able renderer** — prefer a text
  format (e.g. Mermaid) so output stays golden-testable (DD-1) and readable in
  review; watch legibility as graphs grow, and note diagrams are only as good as
  the developer-written purpose fields.

## DD-12 — Cross-resource values: structured references, never expressions

**Decision.** Definitions may contain **references** — structured YAML fields
naming another resource's declared output (`{ ref: resource-name.output-name }`)
— anywhere a value is accepted. rdk compiles refs into Terraform interpolation
(`${module.x.output}`) in the generated `.tf.json`; the `${…}` syntax never
appears in a definition file. This is how values cross the escape-hatch boundary
in both directions: an rdk-managed output feeding a raw module's declared input,
and a raw module's declared output feeding a built-in resource's field (e.g. a
hand-built security group attached to a lambda).

Definitions may **not** contain expressions: no interpolation inside strings, no
conditionals, loops, arithmetic, or string composition. Composition (e.g.
building an ARN pattern from a ref) belongs inside a module, which receives the
ref as a plain input. **Computation lives in modules; data flow lives in
definitions.**

**Why.**

- Resolves the tension the escape hatches created with philosophy rule 9 without
  admitting a DSL: a ref *names* a value, an expression *computes* one; only the
  latter breeds Helm-style template hell.
- Already latent in DD-5/DD-6 — raw modules declare I/O contracts precisely so
  values can be wired across the boundary; refs are the value-only sibling of
  connections (a connection additionally carries bilateral effects like IAM).
- Structured (a mapping, not string syntax) means: no interpolation grammar to
  parse; generate-time validation against the DD-6 graph with errors naming the
  offending file/field (rule 11); provenance for `rdk explain` (rule 5); and
  every ref is a graph edge, so diagrams and dependency ordering capture *all*
  coupling — nothing hides inside strings.

**Union handling (resolved).** Reffable fields are a **bounded union**: scalar
`|` single-key mapping `{ ref: resource.output }` — accepted messiness,
deliberately contained:

- **One shape, forever.** Never a third alternative, never nested unions, never
  `{ ref: …, default: … }` growing options. In Go this is a single generic
  `ValueOrRef[T]` with one custom `UnmarshalYAML` dispatching on YAML node kind
  (scalar vs mapping) — written once, reused everywhere; no per-field code.
- **Only schema-marked reffable fields** (ARNs, IDs, names, URLs) get the union
  type; structural fields (`kind:`, `name:`) stay plain. The reffable surface is
  a deliberate, enumerable list.
- Rejected alternatives: **whole-string sentinels** (`"ref://x"`) only avoid
  unions for string fields — a reffable numeric field degenerates to
  `integer|string` anyway — plus they need an escaping rule and hide refs from
  the schema; **YAML tags** (`!ref`) are invisible to JSON Schema, mangled by
  formatters, lost in JSON round-trips, and cost the same node-level Go code.
- The custom unmarshaler hand-writes its errors ("`memory` expects a number or
  `{ref: resource.output}`") — rule 11 done properly. Editor-side JSON-Schema
  `oneOf` errors remain mediocre; rdk's own validation is authoritative, the
  editor schema is a convenience layer.
- **Parser choice pinned:** `goccy/go-yaml` (maintained, node API, source
  positions in errors — rule 11 needs file:line:col), not the effectively
  unmaintained `gopkg.in/yaml.v3`.
- Lists mix literals and refs naturally (`[sg-123, {ref: weird-sg.sg_id}]` is a
  list of the union type), resolving the former list-semantics residual.

**Residual risk / still open.**

- **Refs vs connections need a crisp user-facing rule.** Proposal: use a
  connection when the link should *do* something (IAM, triggers, injected env);
  use a ref when a field just needs a value. Risk: users reach for a ref, get an
  access-denied at runtime because no IAM came with it — diagnostics should
  notice "ref to a resource with no connection" and hint when the target type
  usually needs one.
- Refs to **policy-resolved or environment values** (not just resource outputs)
  will be wanted (e.g. a raw module needing the env's name prefix). Extend the
  ref namespace deliberately (`ref: env.name`, `ref: policy.x`) rather than ad
  hoc.
- Cycle detection must include ref edges (DD-6 validation already requires
  acyclicity; refs join that check).

## DD-13 — The blank page is rdk's problem: authoring aids, all projected from one schema source

**New requirement** (not from the fault list): a repo full of definition files
fails if the developer doesn't know what to put in them. rdk actively assists
authoring, for humans and AIs.

**Decision — one source, many projections.** Each resource kind carries a
**rich schema metadata** representation: per-field type, required/optional,
default, description, example(s), reffable flag (DD-12), plus kind-level
purpose, supported connection types (DD-6), and policy-affected settings
(DD-7). Every authoring surface is a **deterministic projection** of that one
source — never hand-written per kind, so they cannot drift:

- **JSON Schema** per kind, generated into `<managed-dir>/schemas/<kind>.json`;
  definition files reference it via a
  `# yaml-language-server: $schema=…` header line for live editor validation.
- **Commented starter** (working name `rdk spec <kind>`): outputs a valid
  skeleton definition — required fields present with TODO placeholders, optional
  fields present but commented out, each with its doc comment and default noted.
  Possibly also `rdk new <kind> <name>`: same content, written to
  `rdk/<name>.yaml` with the name and schema header pre-filled — the strongest
  blank-page killer.
- **AI skill** (`rdk skill <kind>`): SKILL.md-style output so an AI agent can
  write definitions against the real schema instead of hallucinating fields.
- **Human reference** (`rdk readme <kind>`): prose + field table.
- **Discovery** (`rdk kinds` / `rdk describe`): list available kinds, purposes,
  and their connection types — answers "what can I even make?"
- **Check without generating** (`rdk validate` or `apply --check`): full
  validation (schema, refs, graph) with rule-11 errors; cheap because
  generation is already pure (DD-1).
- **Error messages cite the same field docs** — a failed validation quotes the
  field's description and example, closing the loop with rule 11.

All projections are pure functions of the rdk version → golden-testable
(DD-1). Command names are **provisional** throughout.

**Clarification to philosophy rule 3.** `rdk fmt` (and any future explicit
fix-style command) may rewrite user-owned definition files **only when
explicitly invoked on them** — gofmt semantics: the user editing their own file
with a tool. Rule 3's ban is on rewriting user files *as a side effect of
`apply`/regeneration*; `fmt` must never run implicitly as part of apply.

**Why.**

- Single-source projection is the only way N authoring surfaces stay correct —
  and it's rule 7 (one mechanism) applied to documentation.
- This is the most concrete realization of GOAL.md's "AI friendly": schema +
  skill + starter + doc-citing errors means an AI can author a correct
  definition without ever seeing rdk's source.
- Costs land early but small: the *requirement* is that PR-1's schema
  representation carries docs/examples/defaults per field from day one. The
  projection commands are then cheap and can arrive incrementally
  (schema + starter first — highest value; skill/readme/describe later).

**Residual risk / still open.**

- **Where do skill files live?** Printed to stdout, or installed into the repo
  (e.g. `.claude/skills/…` / agent-discovery paths)? In-repo placement makes
  them discoverable to AI tools but creates more rdk-owned-files-outside-the-
  managed-dir (the DD-4 manifest case, again load-bearing). Lean: generate into
  the managed dir + a thin installed pointer, but undecided.
- **The actual cost is editorial, not mechanical**: field descriptions and
  examples must be *good*, per kind, and maintained as schemas evolve. Budget
  for it in every resource-type PR; a kind without complete field docs is an
  unfinished kind (rule 11's "a feature whose failures can't be explained isn't
  finished" extends to: a schema whose fields aren't documented isn't finished).
- **Starter-file policy** needs one call: all optional fields commented out
  (verbose but discoverable) vs a curated "common" subset (cleaner but hides
  options). Lean: all fields, grouped, common ones first.
- **Schema header line in hand-written files**: seeded via starter/`new`; for
  files missing it, `rdk fmt` or a validate-time hint can offer it — never
  auto-inserted by apply (rule 3).
- Command naming to settle once, before v1 ships (spec/new/describe/kinds
  overlap; pick a coherent verb/noun scheme).

## DD-14 — The manifest: ownership accounting for files outside the managed dir

Resolves DD-4's residual (made load-bearing by DD-8 pipelines, DD-10 hooks, and
DD-13 skills).

**Decision — scope.** The managed dir itself needs no accounting: it is deleted
and wholly regenerated every apply, unconditionally. The **manifest** exists
only for rdk-owned files that *cannot* live there (CI workflows, agent-discovery
files). That set is deliberately tiny — under rule 12, an outside file is an
exception requiring justification; everything that can live in the managed dir
must.

**Decision — the manifest file.** rdk's only cross-apply state: a machine-
written file in the managed dir recording (a) the rdk version of last apply
(GOAL.md already requires this for Improved Defaults) and (b) `{path, sha256}`
for each outside file rdk wrote. Apply reads the previous manifest before wiping
the managed dir, then writes the new one. Room is reserved for DD-7 pins to live
here too — **decision deferred** until pins are built (readdress then).
Generation is thereby a pure function of *(definitions, rdk version, previous
manifest)* — the manifest is the single stateful input, in-repo, so everything
stays hermetic and golden-testable (DD-1 evolution tests cover it). Git-history
awareness was rejected: hashes are self-contained and survive rebases/squashes.

**Decision — ownership is binary; no shared files, ever.** A file is wholly
rdk's or wholly the user's. Where both want a say (`.gitignore`), split by
mechanism: rdk's ignore rules live in per-directory `.gitignore` files inside
rdk-owned dirs; the root `.gitignore` is seeded-once user property.

**Decision — the apply algorithm per outside file.**

| Manifest entry | On disk | Still generated? | Action |
| --- | --- | --- | --- |
| none | absent | yes | write |
| none | **present** | yes | **hard error** — pre-existing file collision; never adopt-and-clobber |
| hash matches disk | — | yes | write unconditionally (rule 13) |
| hash ≠ disk | — | yes | **hard error** — user edited an rdk-owned file; message names the sanctioned door (hook, raw module, definition) |
| hash matches disk | — | no | **delete** — stale, wholly rdk's (answers GOAL.md's removal question) |
| hash ≠ disk | — | no | **hard error** — edited *and* stale; user reverts or deletes, then re-applies |

Edit-detection always wins over removal: silently destroying user edits is
worse than failing (rule 6). Note the contrast with *user-owned* seeded files
(DD-3 handler code): those are never deleted — orphaned with a warning. The
split is pure ownership: ours → freely overwritten/removed; theirs → never
touched.

**Decision — apply is loud about outside files.** Every apply summarizes
outside-file writes and removals (removing a CI workflow changes deploy
behavior; it must be visible in output, not just in `git diff`).

**Why.** Three-way comparison (manifest hash / disk / newly generated) cleanly
distinguishes "ours-untouched", "ours-edited", "foreign", and "stale" with no
git dependency and no content inspection. Hard errors are safe *because* every
legitimate customization now has a sanctioned door (DD-5, DD-10, DD-13) — the
error message can always point somewhere better.

**Residual risk / still open.**

- **Merge story for the manifest.** Two branches applying different definitions
  both rewrite it → conflict in a machine-written file. Recipe: resolve either
  way, re-run `rdk apply`, commit — the manifest regenerates deterministically.
  A lost entry degrades safely: the orphaned file triggers the collision error
  rather than silent adoption. Document the recipe; consider a
  conflict-friendly format (sorted, one entry per line).
- **Error-recovery UX carries the weight.** Each outside-file type must name its
  customization door in the error text (rule 11) — this is per-file-type
  editorial work, same bar as DD-13 field docs.
- Pins-in-manifest decision deferred (revisit when DD-7 pins are built,
  post-v1).

## DD-15 — Bootstrap is generated, never executed

Resolves DD-8's chicken-and-egg residual: the state backend and deploy
credentials must exist before the pipeline can run Terraform, and can't be
created by that pipeline.

**Decision — rdk generates bootstrap; humans run it.** For each environment,
`rdk apply` generates a bootstrap Terraform config + README + runner script.
A human with elevated credentials runs it once per environment. rdk itself
never calls a cloud API — philosophy rule 2 holds *absolutely*, no scoping
carve-out needed (the fact that no exception was required is evidence the
DD-8 split was right).

**Decision — bootstrap stack contents (per environment/account):**

- State bucket — versioned, encrypted, public-access-blocked, native S3
  lockfile locking (modern Terraform/OpenTofu; no DynamoDB lock table).
- GitHub OIDC identity provider (one per account).
- **Two roles, not one**: a read-only **plan role** (assumed by PRs for
  `terraform plan`) and a write **apply role** (assumed on merge), trust
  policies scoped to repo + branch/environment OIDC claims. Matches DD-8's
  plan-on-PR / apply-on-merge split and keeps PR-triggered code away from
  write credentials.

**Decision — self-hosting state.** The bootstrap stack's state lives in the
bucket it creates: generated script runs the standard dance
(`init -backend=false` → `apply` → `init -migrate-state`). After migration
nothing persists locally; re-runs are plain `init && apply`, idempotent.

**Decision — location.** Bootstrap dirs live **outside the managed dir**
(inside it, an intervening `rdk apply` would wipe transient local state
mid-dance and orphan the bucket). They are DD-14 manifest-tracked; Terraform
artifacts (`.terraform/`, state backups) are excluded via DD-14's
per-directory `.gitignore` mechanism and are neither rdk-owned nor committed.

**Decision — failure UX.** The generated pipeline detects the
un-bootstrapped case (assume-role failure) and emits a rule-11 error naming
the environment and pointing at that environment's bootstrap README — not a
raw AWS error.

**Residual risk / still open.**

- **Deploy-role breadth.** v1: a broad deploy policy (documented as such).
  Future: rdk *knows* the resource kinds in use and could generate a
  narrower per-repo policy — but kind additions would then require
  re-bootstrap; defer, note in generated README.
- **GitHub repo settings are not file-expressible.** Environment protection
  rules / required reviewers (DD-8's prod gate) live in repo settings, not
  files. Same pattern applies — generate a `gh` CLI script or manual
  checklist in the bootstrap README; rdk still never calls the API itself.
  Needs a decision on script vs checklist.
- **Bootstrap module evolution.** rdk upgrades that change the bootstrap
  stack hit existing environments; `moved`-block discipline (DD-2) and the
  idempotent re-run path must cover it; evolution golden tests apply.
- Multi-account provisioning tooling (Control Tower, org factories) is out
  of scope: rdk assumes the account exists and credentials reach it.

## DD-16 — External policy sets: vendored, semver-tagged, never fetched at apply

**Status: deferred past v1 (DD-11 discipline), shaped now so v1 doesn't
foreclose it.**

**Decision — the model is "policy dependencies," Go-modules style.**
Corporate/shared policy sets live in external git repos and are consumed by
**vendoring, never by fetch-at-apply** (rule 1 kills any live-fetch variant
permanently — same repo must generate the same output on any day, offline).

- `rdk policy update` — explicit invocation, the only moment network exists —
  fetches, vendors into `rdk/vendor/<set>/`, and pins in `rdk/policy.lock`.
- **Versions are explicit semver git tags** (`v2.3.1`) on the policy-set repo.
  The lock records `{tag, sha256}`: **the hash is the authority, the tag is the
  label**. A re-pointed tag → hash mismatch on update → hard error (go-sum-style
  tamper/moved-tag detection).
- `apply` reads the vendored copy and *verifies* it against the lock (hand-edited
  vendor tree → hard error: "run `rdk policy update` or revert"). Vendored files
  are a bounded third ownership category (rule 12): machine-written by an
  explicit command, verified-read-only to apply, never touched by regeneration.
- External policies are **ordinary DD-7 policies with a longer source name** —
  no new precedence layer; provenance cites `set@tag` in `rdk explain`, mandate
  errors, and warnings.
- Semver enables graded update flows: bot auto-PRs patch/minor, humans gate
  major; staleness reported in semver distance by the *pipeline* (which has
  network) — **not by apply, which cannot know a newer tag exists**.
- **Semver honesty checking (cheap, valuable):** policies are pure data, so
  `rdk policy update` can mechanically diff old→new and flag a bump whose
  content exceeds its label (a "patch" that changes an existing value, a
  "minor" that adds a mandate) — warn or refuse.
- Because of rule 9, policy sets carry values, never code — a shared policy set
  is not a code-execution vector into consuming repos; worst case is bad
  values, surfaced by plan/CI on the update PR.

**Decision — intra-layer conflict rule: "corporate sets floors and ceilings;
local fills the middle."** External sets *lose* to local policies for defaults,
*beat* local for mandates. This also settles DD-7's intra-layer residual.

**Decision — enforcement stance.** rdk is never the enforcement arm.
Freshness/compliance teeth live in CI (a generated pipeline step running
`rdk policy update --check`) and org tooling reading the greppable lock files —
not in apply.

**v1 hooks (cheap, do now):** policy resolution carries a source/provenance
field from day one; the DD-7 intra-layer rule is chosen per the above so
retrofitting external sets reshuffles nothing.

**Residual risk / still open.**

- **Semver semantics for policy content need a documented convention**
  (proposal: major = new/tightened mandates or changed existing values; minor =
  additive defaults/new optional policies; patch = metadata only) — the honesty
  checker enforces whatever is chosen.
- Multiple sets: ordering between two external sets (union? explicit priority?
  error on overlap?) — decide when multi-set support lands.
- Policy-set authoring aids (DD-13 projections for `kind: policy-set`) and a
  platform-team preview story ("what would v15 do to repo X?") — the update-PR
  plan output covers consumers; producer-side preview is open.

## DD-17 — rdk targets recent Go (itself and managed projects)

**Decision.** rdk requires a recent Go toolchain and does not support trailing/EOL
versions. `go.mod`'s `go` directive tracks the current toolchain rather than a low
floor. This is a *policy*, not just a build detail: keeping the Go version current
applies both to rdk's own build and — as something rdk actively manages — to the
Go projects it generates.

**Why.** Recent Go brings security fixes, performance, and language features;
letting managed repos drift onto EOL toolchains is exactly the invisible, latent
debt rdk exists to prevent (cf. Improved Defaults, DD-7). Supporting old
toolchains would constrain rdk's own code and dilute the "keep repos current"
value proposition. (Prompted by a reviewer suggesting we lower the floor to match
a stale doc; the fix was to correct the doc, not lower the floor.)

**Residual risk / still open.**

- The advance cadence is unspecified — likely track Go's release cycle (support
  the current and previous minor, drop older on each release). Decide deliberately.
- Enforcing recent Go in *managed* projects (a generated toolchain policy/check)
  is future feature work, not built yet.
- Contributors/CI on trailing Go fail the build by design; state the minimum in
  the repo README when one exists.

## DD-18 — All file handling goes through an injected `internal/repofs`

**Decision.** Components never touch the filesystem directly. A single injected
library, `internal/repofs`, performs the file *actions* rdk needs (materialize a
managed tree atomically, seed a user file once, read within the repo), rooted at
the repo via `os.Root` so confinement and symlink-safety are structural — not
per-call-site guards. Components build an in-memory `FileSet` (`Bytes`, `JSON`)
describing desired output; `repofs` owns directory creation, permissions,
deterministic serialization (DD-1), the atomic staging/swap, and security. An
in-memory fake makes component tests filesystem-free.

**Why.** The escape/symlink/atomicity fixes had accreted at scattered call sites
(securejoin, `O_EXCL`, lexical path checks, hand-rolled swap). Centralizing makes
insecure file access unrepresentable in component code, deletes the
`filepath-securejoin` dependency, and unifies the tf.json/manifest serialization
that DD-1 depends on. Enabled by `os.Root` (DD-17's recent-Go policy paying off).

**Scope.** Folded into PR-1 before merge. Only the actions PR-1 uses are built
(`Materialize`, `Seed`, `ReadFile`, `ReadDir`; `FileSet.Bytes`/`JSON`);
`WriteOutside`/`RemoveOutside`, `YAML`, and template entries are deferred to when
a component needs them (YAGNI).

**Full design:** `docs/superpowers/specs/2026-07-29-repofs-design.md`.

**Residual risk / still open.** Golden churn if `repofs.JSON` doesn't byte-match
the current encoders (manifest `MarshalIndent` vs tf.json `Encoder` trailing
newline) — verify, don't blind-update. Error quality drops slightly where lexical
path validation is removed (acceptable; rdk-generated paths).

---

### Fault scorecard (see `alternatives.md`)

| Fault | Status |
| --- | --- |
| #3 Delete-and-rewrite over stateful infra | Mitigated by DD-1/DD-2; residual: address stability (evolution tests + `plan` gate) |
| #6 Managed-vs-editable code boundary | Addressed by DD-3; residual: interface-based contract, orphan handling |
| Copier-style merge conflicts | Closed by DD-3 + DD-4; residual: manifest/hash guard for outside-managed files |
| #7 Extension / escape hatch (the 20%) | Addressed by DD-5; residual: connection contract + policy boundary for opaque modules |
| #9 Connections model / consistency / diagram | Addressed by DD-6; residual: non-local readability (`rdk explain`), provider-constraint validation |
| #4 Policy precedence matrix | Addressed by DD-7 (single ordered stack); residual: intra-layer policy conflicts |
| #5 Improved Defaults layer | Addressed by DD-7 (pins bounded to "default was winning"); residual: overlap with DD-2 module-version pinning |
| #12 AI-friendly (traceability) | Half-addressed by DD-7 (`rdk explain`, provenance); residual: broader "helpful output everywhere" is cross-cutting |
| #8 State management | Reframed by DD-8 (apply generates; a generated pipeline deploys + owns state); residual: bootstrap chicken-and-egg |
| #2 Uniformity vs env divergence | Resolved by DD-9 — constraint kept *by design*; variation is values-only (policy/flags/selectors); residual: flag & selector mechanisms, idle-cost tail |
| #1 Scope (five tools at once) | Addressed by DD-11 — v1 = sequenced multi-PR "compelling minimum"; residual: still large, execution risk managed by phasing |
| #10 Multiple definition formats | Addressed by DD-11 — v1 is YAML `kind:` only; OpenAPI/GraphQL deferred |
| #11 Go/AWS-only narrowness | Accepted by DD-11 as the deliberate wedge; residual: adoption reach vs Encore/Nitric |
| — Pipeline escape hatch (new) | Added as DD-10 (generated pipeline calls seeded user hooks) |
| — Cross-resource references (new) | Added as DD-12 (structured refs compile to `${…}`; expressions stay banned) — amends philosophy rule 9 |
| — Authoring assistance (new requirement) | Added as DD-13 (schema/starter/skill/readme all projected from one rich kind schema) — clarifies philosophy rule 3 |
| — Outside-files manifest (DD-4 residual) | Resolved by DD-14 (three-way hash comparison; hard errors; clean stale files deleted) — adds philosophy rule 13 |
| — Bootstrap chicken-and-egg (DD-8 residual) | Resolved by DD-15 (generated per-env bootstrap TF + script; plan/apply role split; self-hosted state) |
| — External policy sets (new, deferred) | Shaped as DD-16 (vendored, semver-tagged, hash-locked; never fetched at apply); v1 hooks only |
| — Module distribution (DD-2 residual) | Ratified in PR-1: vendored via `go:embed`; module version ≡ rdk version |
| — Recent-Go policy (new) | DD-17: rdk targets recent Go for itself and managed projects; no old-toolchain support |
| — Injected file handling (new) | DD-18: all FS access via `internal/repofs` (os.Root-rooted, injected, faked); centralizes security/determinism/atomicity |
| All faults #1–#12 now have a decision | #12 traceability solved; broader AI-friendly output remains cross-cutting |
