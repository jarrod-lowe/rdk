# Alternatives & Prior Art

This document surveys existing tools that solve some or all of what RDK
("Repository Development Kit") sets out to solve. Its main job is **not** to
flatter RDK's plan — it is to point at the places where the neighbours bled, so
we can predict where RDK will bleed too. Where RDK's design in `GOAL.md` looks
risky, contradictory, or hand-waves the hard part, this document says so.

## What RDK is trying to be — and why that's the first problem

RDK deliberately spans five jobs that existing tools each tackle *one* of:

1. **Repository scaffolding + managed-file regeneration** (cf. Projen,
   Cookiecutter, Copier, Backstage).
2. **Opinionated resource abstraction** — a compact schema expands to real infra
   (cf. AWS SAM, Serverless Framework, SST, Architect).
3. **Policy-driven, multi-environment resolution** (cf. Terragrunt, org platform
   frameworks).
4. **Codegen of handler code + connection-derived permissions** (cf. Encore,
   Nitric, Winglang, SST).
5. **Terraform/OpenTofu emission** rather than a bespoke provisioner (cf.
   Terragrunt, CDKTF, SST v3).

**The scope is the single biggest risk in the whole plan.** Projen does *only*
job 1 and is still widely called too complex. SST does jobs 2/4/5 and needed a
from-scratch engine rewrite (v2→v3) when its foundation cracked. Terragrunt
exists *only* because job 3 is hard enough to justify a whole tool. RDK proposes
to do all five, in Go, as a `go install` CLI, maintained presumably by a very
small team. Every tool below is evidence that *each individual slice* is a
multi-year effort with a large maintenance surface. "Do most of them together"
should be read as a warning, not a value proposition. The rest of this document
assumes the scope survives contact with reality only if RDK picks a sharp
initial wedge and treats the other four jobs as later phases.

---

## Category 1 — Managed-file / scaffolding tools

Closest analogues to RDK's `rdk/` definitions + regenerated managed directory.

### Projen

Describe a project in code (`.projenrc.ts`); Projen synthesizes config files and
treats them as read-only artifacts. The nearest match to RDK's "managed files
get regenerated" model.

- **Advantages**: kills config drift across many repos; strong composition
  model; deterministic, marked, regenerated output.
- **Disadvantages / pain points**: steep learning curve (you learn an API, not
  the config); non-standard dependency workflow (no `npm install X` — you edit
  `.projenrc` and re-synth, which developers repeatedly report as unnatural);
  **rigid escape hatches** — generated files are read-only and hand edits are
  clobbered on the next run.
- **What this predicts for RDK**: RDK's "the managed directory is deleted and
  rewritten on every `apply`" *is Projen's most-hated behaviour, made worse*.
  Projen clobbers config files; RDK clobbers files that **back stateful cloud
  infrastructure through Terraform**. If regeneration is not perfectly
  deterministic — stable resource addresses, stable ordering, stable naming —
  Terraform will read a changed address as *destroy-and-recreate* and take real
  resources (databases, queues) with it. Projen's clobbering is an annoyance;
  RDK's clobbering can be an outage. This mechanism needs a specification and a
  safety story that `GOAL.md` does not yet have.

### Cookiecutter / Yeoman / Copier

Project *generators* that stamp out an initial repo from a template.

- **Advantages**: simple, ubiquitous, language-agnostic; Copier uniquely
  supports *updating* an already-generated project.
- **Disadvantages / pain points**: Cookiecutter/Yeoman are one-shot — template
  and project drift apart immediately; no notion of resources, environments,
  policies, or deploy.
- **What this predicts for RDK**: the "re-apply and reconcile forever" loop is
  the entire reason RDK exists, and it is *also* the part nobody has made
  pleasant. Copier's template-update flow is the only real prior art for doing
  it well — and even Copier's update produces merge conflicts. RDK should study
  Copier hard and be honest that reconcile-without-clobber is unsolved, not
  assumed.

### Backstage (software templates + scaffolder)

Developer portal; golden-path templates scaffold services with CI/CD, docs, and
monitoring pre-wired, backed by a software catalog.

- **Advantages**: strong golden-path story; org-wide catalog visibility;
  extensible.
- **Disadvantages / pain points**: heavy ops (realistically a dedicated
  engineer; instances fall months behind upstream's ~monthly releases);
  UI-bound; the scaffolder is largely one-shot.
- **What this predicts for RDK**: a CLI beats a portal on ops cost, but Backstage
  gets *discoverability* from its UI — a form with labelled fields. RDK throws
  that away and bets on "AI-friendly output" to replace it. That bet is
  unproven and, as noted below, is in direct tension with RDK's own policy/
  override complexity: the more precedence layers RDK adds, the harder it is for
  *either* a human or an AI to explain why a resource got a given value.

---

## Category 2 — Serverless deployment frameworks

Closest to RDK's "define a lambda/API Gateway with a small schema."

### AWS SAM

CloudFormation superset with serverless shorthand.

- **Advantages**: first-party, stable, integrates with CloudFormation drift
  detection and the AWS ecosystem; local invoke/test.
- **Disadvantages / pain points**: CloudFormation underneath — slow deploys,
  opaque rollbacks, the hard 500-resource stack limit, AWS-only; shorthand
  covers only the common case, then you fall back to raw CloudFormation.
- **What this predicts for RDK**: the fall-back-to-raw problem is the one RDK
  hasn't answered. SAM at least *lets* you drop to CloudFormation. `GOAL.md`
  never says how a developer handles the 20% that RDK's schemas don't model. If
  the answer is "edit the generated Terraform," that collides head-on with
  "the managed directory is deleted and rewritten every apply." RDK needs a
  first-class, non-clobbered extension mechanism, or it inherits SAM's ceiling
  with none of SAM's escape hatch.

### Serverless Framework

Long-time default multi-cloud serverless tool (`serverless.yml`).

- **Advantages**: huge plugin ecosystem; multi-provider; `serverless.yml` is
  close in spirit to RDK's per-resource YAML.
- **Disadvantages / pain points**: **v4 (2024) now requires authentication and a
  paid subscription for organizations over ~$2M revenue** (honor-system
  threshold); v3 is open source but **unmaintained since 31 Dec 2024**;
  historically CloudFormation-bound on AWS.
- **What this predicts for RDK**: mostly a licensing/trust cautionary tale (see
  the faults section). Technically, note that Serverless's plugin ecosystem is
  what made a thin YAML tool survive for a decade. RDK has no extension story at
  all yet — a closed, opinionated tool with no plugin seam ossifies fast.

### Architect (arc.codes)

Terse `app.arc` manifest expanding to SAM/CloudFormation.

- **Advantages**: minimal boilerplate; opinionated defaults; fast start.
- **Disadvantages / pain points**: small community, AWS-only, CFN-bound, and the
  terseness caps how far you can push before dropping to raw CFN.
- **What this predicts for RDK**: Architect is what "compact + opinionated"
  looks like when it hits its ceiling and stalls. Terseness is cheap; the
  extension mechanism is what determines whether the tool has a future.

### SST (v3 / "Ion")

Full-stack AWS+ framework that **rewrote its engine in v3**: dropped AWS
CDK/CloudFormation for the **Pulumi engine with Terraform providers** underneath.

- **Advantages**: strong DX (fast parallel deploys, live lambda dev, good
  errors); v3 shed CloudFormation's 500-resource limit and gained multi-cloud.
- **Disadvantages / pain points**: the v2→v3 rewrite was **disruptive and
  migration-heavy**; TypeScript-centric; infra defined in code, not files.
- **What this predicts for RDK**: two warnings, not a validation. (1) Even a
  well-funded, popular team got its foundational engine choice wrong the first
  time and had to rebuild — RDK is choosing its engine (Terraform/OpenTofu) at
  the *start*, with less information than SST had after three years. Abstract
  the engine ruthlessly or plan for the same rewrite. (2) SST reaches for a
  *general-purpose language* (TS) precisely because declarative files can't
  express real logic. RDK bets the opposite way (YAML/OpenAPI/GraphQL). The
  moment a policy needs a conditional or a loop, RDK will feel the pull toward
  either an ugly templating DSL or a plugin API — the same wall Helm hit.

---

## Category 3 — IaC cores & Terraform abstractions

RDK builds *on* this layer.

### Terraform / OpenTofu

The engine RDK targets.

- **Advantages**: enormous provider ecosystem; mature state model; de-facto
  standard.
- **Disadvantages / pain points**: HashiCorp relicensed Terraform to the
  **BSL** (Aug 2023), restricting "competitive" use; the community forked
  **OpenTofu** (Linux Foundation, production-ready, initially drop-in). Raw HCL
  has no native DRY story for multi-environment (hence Terragrunt).
- **What this predicts for RDK**: `GOAL.md` says "Terraform (or OpenTofu)" and
  says **nothing about state** — backends, locking, per-environment state
  isolation, or how `apply` maps to `terraform apply` across N environments.
  That silence is the tell. State management is where multi-env IaC actually
  gets hard; Terragrunt is an entire product built around this one gap. RDK
  cannot treat it as an implementation detail.

### Terragrunt

Thin wrapper for DRY, multi-environment Terraform/OpenTofu.

- **Advantages**: solves the multi-env repetition + backend wiring RDK also
  targets; works over OpenTofu unchanged.
- **Disadvantages / pain points**: still raw HCL/modules; another config
  dialect to learn.
- **What this predicts for RDK**: RDK's environments/policies are a
  higher-altitude take on exactly Terragrunt's problem — which means RDK is
  signing up to re-solve everything Terragrunt has accumulated (dependency
  ordering, per-env inputs, remote state, `run-all` semantics). The existence of
  Terragrunt's long feature list is a measure of how much RDK is really
  promising here.

### CDK for Terraform (CDKTF)

Terraform infra defined in TS/Python/Go.

- **Advantages**: real language over Terraform for teams who dislike HCL.
- **Disadvantages / pain points**: **deprecated 10 Dec 2025** — HashiCorp
  stopped publishing prebuilt provider bindings and archived the repo.
- **What this predicts for RDK**: if RDK ever leans on a vendor's synthesis SDK
  to emit Terraform, this is how that dependency ends. Emit plain HCL/JSON that
  OpenTofu consumes directly. (This is one of the few places the neighbour's
  scar points at a clean choice rather than a hard problem.)

### Pulumi

General-purpose IaC in real languages; multi-cloud.

- **Advantages**: powerful, mature; the engine SST chose for v3.
- **Disadvantages / pain points**: defaults nudge toward Pulumi's hosted state
  service; a distinct execution model (not HCL/state-compatible with Terraform);
  infra-as-general-code, not opinionated definitions.
- **What this predicts for RDK**: a reminder that "which engine" also decides
  "whose state model, whose backend, whose lock-in." RDK's Terraform/OpenTofu
  choice keeps the larger provider ecosystem but commits RDK to owning the
  Terraform state lifecycle it hasn't yet designed.

### AWS CDK / CloudFormation / Crossplane (brief)

- **AWS CDK**: real-language constructs over CloudFormation; inherits CFN's
  speed/limits; **v1 in maintenance mode**; AWS-only.
- **CloudFormation**: first-party, reliable, but slow, verbose, AWS-only, 500-
  resource stacks.
- **Crossplane**: K8s-native control-plane IaC; powerful for platform teams
  already on Kubernetes, but a heavy, very different operating model.

---

## Category 4 — Infrastructure-from-Code (IfC) frameworks

The most ambitious neighbours: infra is inferred from / co-located with code and
permissions are derived automatically — close to RDK's "connections drive IAM"
and "a resource definition generates handler code."

### Encore

Go/TS backend framework that infers infra from typed primitives (APIs, DBs,
pub/sub) in code.

- **Advantages**: excellent DX; infra as a byproduct of typed code; Go-first,
  like RDK; by far the largest, most active IfC community.
- **Disadvantages / pain points**: historically coupled to Encore's own
  runtime/platform; infra inferred from code is *less explicit* — harder to see
  the whole deployment at a glance.
- **What this predicts for RDK**: Encore already occupies "Go-first, infra
  derived from code, connections→permissions" with real traction and funding.
  RDK's counter is "explicit, reviewable definition files" — but that is a
  *taste* argument, not a moat, and it comes at the cost of the DX Encore is
  praised for. RDK should be able to say clearly why a team picks its extra
  ceremony over Encore's inference; "we also scaffold the repo and emit
  Terraform" is the honest answer, and it circles back to the scope problem.

### Nitric

Cloud-agnostic: declare resources (APIs, queues, storage, schedules), write
handlers in any language, deploy to AWS/GCP/Azure; pluggable providers can emit
Terraform/Pulumi.

- **Advantages**: no lock-in; declared-resources model very close to RDK;
  multi-language; Terraform-capable providers.
- **Disadvantages / pain points**: **much smaller community and slower cadence**
  than Encore — a live adoption risk; agnostic abstraction lags
  provider-specific features.
- **What this predicts for RDK**: Nitric is the closest architectural cousin *and*
  a warning — it is the better-resourced version of RDK's own idea and is still
  struggling for adoption against Encore. RDK is Go-handler-only initially and
  AWS-only, i.e. *narrower than Nitric on both axes it competes on*. That narrows
  the addressable audience further than the plan acknowledges.

### Winglang (Wing)

A purpose-built cloud language separating "preflight" (infra) from "inflight"
(runtime), compiling to Terraform and others.

- **Advantages**: elegant unified infra+runtime model; good local simulation.
- **Disadvantages / pain points**: **a whole new language to adopt** — the
  steepest possible curve; has since added a TS SDK, tacitly conceding the
  language was too big an ask; small ecosystem.
- **What this predicts for RDK**: the one clear win — RDK's use of familiar YAML
  and standard OpenAPI/GraphQL avoids Wing's adoption cliff. But Wing also shows
  the flip side: a language exists because declarative config eventually can't
  express the logic real infra needs. RDK will meet that ceiling from the other
  direction.

---

## Faults, tensions, and open questions in RDK's own design

Drawn directly from `GOAL.md`. These are the places to push before writing code.

1. **Scope is a multi-tool effort for (apparently) a small team.** Five jobs,
   each of which justifies a standalone product. Highest-order risk. Mitigation:
   pick one wedge (e.g. Go Lambda services on AWS) and ship it end-to-end before
   generalizing.
   **Being addressed** (`design-decisions.md` DD-11): v1 is a defined "compelling
   minimum" (Lambda/DDB/S3/CloudFront + connections + envs + pipelines + policy +
   both escape hatches + docs) delivered as a *sequenced set of PRs*, each
   independently valuable, foundational bets first. **Residual:** v1 is still
   large — execution risk is managed by phasing, not removed.

2. **"All resources are deployed into all environments" is too rigid to be
   true.** Real systems have env-specific resources: a data-migration lambda that
   runs once, a debug endpoint only in staging, a resource being canaried in one
   white-label. This constraint will break on first contact and force an
   exceptions mechanism — better to design for per-environment divergence now
   than to bolt it on.
   **Resolved — as a deliberate design stance** (`design-decisions.md` DD-9):
   the constraint is *kept on purpose*. Strict uniformity (identical resource
   graph everywhere) is treated as a high-value property; all white-label
   variation is values-only via three channels — policy (DD-7), feature flags
   (gate behavior, not presence), and per-env asset/config selectors. The
   critique is accepted but overridden: idle-but-uniform infra is judged cheaper
   than structural divergence. **Residual:** the accepted idle-cost tail (pair
   flags with sizing policy for expensive resources), plus specs for the flag and
   selector mechanisms.

3. **"Delete and rewrite the managed directory every apply" is dangerous over
   stateful infra.** This is Projen's clobbering problem plus Terraform state.
   Non-deterministic regeneration → changed resource addresses → destroy/recreate
   of real resources. Needs: guaranteed-stable addressing/naming, a plan/diff
   preview before mutation, and a story for state that is decoupled from the
   regenerated files.
   **Being mitigated** (`design-decisions.md` DD-1/DD-2): deterministic
   generation + golden tests + Terraform JSON output. **Residual:** determinism
   ≠ address stability — still need name-derived stable addresses,
   evolution/delta golden tests, and a `terraform plan` gate.

4. **The policy engine is an under-specified precedence matrix.** Value
   resolution runs resource-default → policy → env-type override → *Improved
   Defaults* override, per resource, per environment type. That is four layers
   and a combinatorial space. Helm/Terragrunt/Kustomize all show these merge
   systems accrete complexity and become the hardest thing to debug. "Why is
   this lambda 512MB here?" must be answerable trivially, or the "AI-friendly"
   goal dies here first.
   **Being addressed** (`design-decisions.md` DD-7): one totally-ordered
   precedence stack (module default → Improved-Defaults pin → default policy →
   explicit → mandating policy) with provenance carried through resolution and
   surfaced by `rdk explain`. **Residual:** conflicts *between* policies within a
   layer still need a rule.

5. **"Improved Defaults" adds a fifth, time-varying precedence layer.** An
   auto-populated override file that upgrades mutate, warn about, and later
   retract. This is a small migration engine with its own state (the recorded
   last-apply version). Powerful, but it deepens the very precedence problem in
   (4) and needs its own lifecycle spec.
   **Being addressed** (`design-decisions.md` DD-7): pins are minted only where
   the module default was the winning layer (so they can't conflict with policy
   or explicit values), and are auto-retracted when redundant/superseded/orphaned.
   **Residual:** this overlaps DD-2's module-version pinning — the coarse vs fine
   "keep old behavior" mechanisms must be reconciled, not both built blindly.

6. **The managed-vs-developer-editable code boundary is the crux, and it's
   hand-waved.** `GOAL.md`: handler files where "some remain rdk-managed, others
   available for the developer to edit." Mixing generated and hand-written code
   in the same tree is one of the hardest problems in tooling (protobuf, ORMs,
   OpenAPI codegen all struggle). How are developer edits preserved across
   regeneration? Marker regions? Separate files with a stable interface? "Never
   regenerate once created"? Until this is answered, the codegen feature is a
   sketch.
   **Being addressed** (`design-decisions.md` DD-3/DD-4): a directory/import
   boundary — managed entrypoint imports a user dir seeded once with a stub, then
   never rewritten; reconcile happens pre-commit so there's no merge.
   **Residual:** express the call-in as a Go interface (loud, local compile
   errors on signature change) and decide orphan handling for deleted resources.

7. **No stated extension/escape hatch → SAM's and Architect's ceiling.** When a
   user needs something RDK doesn't model, what happens? If "edit the Terraform,"
   that contradicts fault (3). Serverless survived on plugins; RDK has no seam.
   Decide the extension model early — it shapes everything.
   **Being addressed** (`design-decisions.md` DD-5): a first-class
   raw-Terraform definition type that seeds a user-owned module directory and
   generates the module call — the escape hatch is itself a module call,
   consistent with DD-2. **Residual:** the raw module is opaque to rdk, so
   connections need a declared input/output contract and policies can't be
   guaranteed inside it.

8. **State management is absent from the design.** No mention of backends,
   locking, per-env state isolation, or how one `rdk apply` orchestrates N
   `terraform apply`s. This is where multi-env IaC is genuinely hard (see
   Terragrunt) and cannot be an afterthought.
   **Reframed** (`design-decisions.md` DD-8): the premise is corrected — `rdk
   apply` orchestrates *zero* applies. It is pure local generation; a generated
   CI/CD pipeline runs Terraform per environment and owns backends, locking,
   isolation, ordering/gating, and the plan→apply flow. **Residual:** the
   bootstrap chicken-and-egg (state backend + deploy role must pre-exist) is the
   hard part, and the pipeline files are a concrete case of DD-4's outside-the-
   managed-dir problem.

9. **Connections stored "in one of the two resources" invites inconsistency.**
   Which side owns it? What if both mention it? How is the full graph discovered
   for the auto-generated diagram if edges live scattered on one endpoint each?
   A resolved connection model (or a derived index) is needed before the docs/
   diagram feature is real.
   **Being addressed** (`design-decisions.md` DD-6): typed connections declared
   once on a canonical side, normalized into a validated directed graph that
   drives bilateral permission/config generation and the diagram; wiring runs
   over the DD-5 I/O contract so built-in and raw resources connect identically.
   **Residual:** effects on a resource now originate in other files (non-local
   readability — needs `rdk explain`), and provider-specific link constraints
   still need validating.

10. **Multiple definition formats multiply surface area.** YAML `kind:` +
    OpenAPI-with-`x-` + GraphQL-with-`@directives` each need their own parser,
    schema, validation, editor support, and — critically — their own
    *helpful error messages* to hit the AI-friendly goal. Three formats is three
    times the tooling.
    **Being addressed** (`design-decisions.md` DD-11): v1 ships **one** format
    (YAML `kind:`); OpenAPI- and GraphQL-driven definitions are deferred past v1.

11. **Go-only + AWS-only initially, competing with broader tools.** Encore is
    multi-language-trending and better-adopted; Nitric is multi-cloud. RDK enters
    narrower on both axes. Fine as a wedge — but the plan should state it *as* a
    wedge, not assume the general tool.
    **Accepted — as the deliberate wedge** (`design-decisions.md` DD-11): Go +
    AWS + GitHub Actions only for v1, embraced rather than apologized for.
    **Residual:** the adoption-reach bet vs Encore/Nitric is real; revisit
    breadth after v1.

12. **"AI-friendly" is asserted, not designed, and fights faults (4)/(5).** The
    more precedence layers and regeneration magic RDK adds, the less explainable
    any given output becomes — to a human or an LLM. If AI-friendliness is a real
    goal, "every value is traceable to its source" is a design constraint that
    limits how clever the policy/override system may be.
    **Being addressed** (`design-decisions.md` DD-7): "traceable to source" is
    made the *implementation* — resolution carries provenance, exposed via
    `rdk explain` (+ JSON), so extra layers no longer reduce explainability.
    **Residual:** only value-traceability is solved; "helpful output on every
    error/warning" stays a cross-cutting principle, not a done item.

## Where RDK might still be worth building

Stated honestly, and net of the above: no surveyed tool combines whole-repo
ownership, *explicit reviewable* definitions (vs. Encore/Wing inference), a
policy/env-type model, and Terraform/OpenTofu emission, without a new language
(vs. Wing), a portal to operate (vs. Backstage), or a paid-CLI surprise (vs.
Serverless v4). That gap is real. Whether it is *worth the combined complexity
of five hard tools* is the open question this document exists to keep in front of
you — and the answer depends entirely on ruthlessly narrowing the initial scope.

---

## Quick comparison

| Tool | Scaffolds repo | Managed/regenerated files | Resource abstraction | Multi-env + policy | Engine | Sharpest pain point |
| --- | :---: | :---: | :---: | :---: | --- | --- |
| **RDK** (goal) | ✅ | ✅ | ✅ | ✅ | Terraform/OpenTofu | Scope = 5 hard tools at once |
| Projen | ✅ | ✅ | ➖ | ➖ | n/a | Learning curve; edits clobbered |
| Cookiecutter/Yeoman | ✅ | ❌ | ❌ | ❌ | n/a | One-shot; instant drift |
| Copier | ✅ | ➖ | ❌ | ❌ | n/a | Update flow = merge conflicts |
| Backstage | ✅ | ➖ | ➖ | ➖ | pluggable | Heavy ops; UI-bound |
| AWS SAM | ❌ | ➖ | ✅ | ➖ | CloudFormation | CFN speed/limits; AWS-only |
| Serverless Fw | ❌ | ➖ | ✅ | ✅ | CFN/plugins | v4 paid licensing |
| Architect | ✅ | ➖ | ✅ | ➖ | CFN | Terseness ceiling; small community |
| SST v3 | ➖ | ➖ | ✅ | ✅ | Pulumi + TF providers | Disruptive engine rewrite |
| Terraform/OpenTofu | ❌ | ❌ | ❌ | ➖ | self | HCL DRY gap; TF BSL |
| Terragrunt | ❌ | ❌ | ❌ | ✅ | Terraform/OpenTofu | Still raw HCL |
| CDKTF | ❌ | ➖ | ➖ | ➖ | Terraform | **Deprecated Dec 2025** |
| Pulumi | ❌ | ➖ | ➖ | ➖ | self | Distinct state model/lock-in |
| Encore | ➖ | ➖ | ✅ | ✅ | own/cloud | Infra implicit; platform-coupled |
| Nitric | ➖ | ➖ | ✅ | ➖ | pluggable (TF/Pulumi) | Small community vs. Encore |
| Winglang | ➖ | ➖ | ✅ | ➖ | Terraform+ | New language to learn |

Legend: ✅ yes · ➖ partial/possible · ❌ no

---

## Sources

- [Serverless Framework V4: A New Model](https://www.serverless.com/blog/serverless-framework-v4-a-new-model) · [Upgrading to v4](https://www.serverless.com/framework/docs/guides/upgrading-v4) · [Pricing](https://www.serverless.com/pricing)
- [SST v3 announcement](https://sst.dev/blog/sst-v3/) · [Moving away from CDK](https://sst.dev/blog/moving-away-from-cdk/) · [AWS CDK vs Pulumi: Why SST switched (Pulumi blog)](https://www.pulumi.com/blog/aws-cdk-vs-pulumi-why-sst-switched/)
- [Projen (GitHub)](https://github.com/projen/projen) · [Project templating philosophies: Projen vs Copier](https://mhdez.com/notes/project-templating-philosophies-projen-vs-copier/) · [Projen: NodeJS project boilerplating (ITNEXT)](https://itnext.io/projen-nodejs-project-boilerplating-15c20665c6b5)
- [OpenTofu announces fork of Terraform](https://opentofu.org/blog/opentofu-announces-fork-of-terraform/) · [OpenTofu Manifesto](https://opentofu.org/manifesto/) · [Terraform license change (Spacelift)](https://spacelift.io/blog/terraform-license-change)
- [CDK for Terraform (HashiCorp Developer)](https://developer.hashicorp.com/terraform/cdktf) · [cdktf-provider-aws (archived)](https://github.com/cdktf/cdktf-provider-aws) · [AWS CDK v1 maintenance mode](https://aws.amazon.com/blogs/developer/version-1-of-the-aws-cloud-development-kit-aws-cdk-is-now-in-maintenance-mode/)
- [Encore vs Terraform/Pulumi (Encore docs)](https://encore.dev/docs/platform/other/vs-terraform) · [Encore vs Nitric](https://openalternative.co/compare/encore/vs/nitric) · [Nitric: State of Infrastructure from Code](https://nitric.io/blog/state-of-infrastructure-from-code)
- [Backstage Software Templates](https://backstage.io/docs/features/software-templates/) · [Building golden paths with Backstage](https://gokhan-gokalp.com/devex-series-01-creating-golden-paths-with-backstage-developer-self-service-without-losing-control/)
