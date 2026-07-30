# Philosophy

The rules that guide all rdk development. Each one exists to *decide arguments*:
when a proposed change conflicts with a rule, either the change is wrong or the
rule must be deliberately amended here — never silently eroded. A rule that
can't reject a plausible proposal is a platitude and doesn't belong on this
list.

(`DD-n` references are to `design-decisions.md`.)

## 1. Generation is a pure function

`rdk apply` maps definitions → repo content. Same input, same rdk version →
byte-identical output. No network, no credentials, no clock, no randomness.
*Therefore:* no map-iteration ordering, no timestamps in output, no "fetch
latest" at generate time; everything is golden-testable. (DD-1)

## 2. rdk never touches the cloud

rdk writes the repo; the generated pipeline deploys it. The repo is the only
interface between the two.
*Therefore:* no `rdk deploy`, no cloud reads to "help" generation, no state
files in rdk's hands. If deployment needs something, rdk generates it into the
repo. (DD-8)

## 3. Every file has exactly one owner

A file is rdk's (regenerated freely, never hand-edited) or the developer's
(seeded once, never rewritten). The boundary is a file/directory line, never a
region inside a file.
*Therefore:* no marker-comment regions, no three-way merges, no "rdk updates
this bit of your file." Generated code reaches user code only through a stable
interface. The ban is on rewriting user files *as a side effect of apply*;
an explicitly invoked, gofmt-style tool (`rdk fmt`) run by the user on their
own file is the user editing with a tool — allowed, but never implicit.
(DD-3, DD-4, DD-13)

## 4. Same graph everywhere

Every environment gets the identical resource graph. Variation is values-only:
policy, flags, selectors. Idle-but-uniform is cheaper than different.
*Therefore:* no per-environment resources, no presence conditions, no
"just this env" exceptions — a flag turning behavior off is the mechanism, even
when that leaves infrastructure unused. (DD-9)

## 5. Every value answers "why?"

Anything rdk resolves — a memory size, an IAM statement, a name — must be
traceable to its source (which file, which policy, which connection, which
layer won). Provenance is carried through resolution, not reconstructed after.
*Therefore:* no resolution logic that can't explain itself in `rdk explain`;
if a clever feature can't produce provenance, it's too clever. (DD-6, DD-7)

## 6. Loud, never silent

rdk may hold surprising state (pins, overrides, orphaned files) only if it says
so on every apply. Conflicts are errors, not quiet winners.
*Therefore:* no silent overriding of an explicit setting, no silently deleting
developer-owned files (warn-and-leave), no divergence that doesn't announce
itself. (DD-5, DD-7)

## 7. One mechanism, reused

Everything is a definition → module call. Escape hatches use the same machinery
as built-ins: a raw-Terraform resource is just a user-supplied module (DD-5); a
pipeline hook is just a user-owned target the generated pipeline calls (DD-10);
user handlers sit behind the same seeded boundary (DD-3).
*Therefore:* before adding a new mechanism, prove the existing one can't
express it. Special cases are a smell.

## 8. Escape hatches are doors, not holes

Users must always be able to leave the paved road — but through a first-class,
declared exit whose limits are explicit: raw modules declare their I/O contract;
hooks are named extension points. Beyond the door, rdk's guarantees (policy
enforcement, traceability, least privilege) stop, and rdk says so.
*Therefore:* never force editing of generated files as the workaround, and
never pretend guarantees extend into user-owned code. (DD-5, DD-10)

## 9. References yes, expressions no

YAML definitions, `.tf.json` output, plain HCL modules, Mermaid diagrams,
Make hooks. No invented language, no vendor synthesis SDK, no format a
five-year-old tool can't parse. Definitions may *name* a value that lives
elsewhere — a structured `{ ref: resource.output }` — but may never *compute*
one: no interpolation in strings, no conditionals, loops, or composition.
Terraform's `${…}` appears only in generated output.
*Therefore:* when tempted by a DSL, expression language, or template syntax
inside definitions — stop; computation belongs in a module or a policy, which
receives refs as plain inputs. (DD-2, DD-12, and Winglang/CDKTF's scars in
`alternatives.md`)

## 10. Narrow and finished beats broad and partial

Go + AWS + GitHub Actions, a small resource catalog, done end-to-end — before
any widening. Every shipped slice is independently usable.
*Therefore:* no speculative abstraction for clouds/languages we don't support
yet (but no design that forecloses them either); breadth proposals queue behind
completeness. (DD-11)

## 11. Errors are for the reader who has to fix them

Output — errors, warnings, explanations — is written for the person (or AI)
who must act on it: what's wrong, where, and what to do next, citing
provenance. This is a design constraint, not a polish pass.
*Therefore:* no error message that names an internal concept without naming
the user's file/field that triggered it; a feature whose failures can't be
explained clearly isn't finished. Mechanically: everything rdk says is a
`diag.Diagnostic` carrying a documented code, the user's file and field, and a
hint; `internal/logger` is the only path to stdout or stderr, and a test
enforces it. (GOAL.md "AI Friendly", DD-7)

## 12. Messiness is budgeted

Complexity may be admitted, but only inside an explicit boundary that stops it
spreading: the definition union is one shape forever (DD-12), escape hatches
are named doors (DD-5, DD-10), pins exist only where the default was winning
(DD-7), `fmt` mutates only on explicit invocation (DD-13).
*Therefore:* no open-ended mechanisms — every admitted flexibility ships with
its stated limit, and a proposal that *widens* an existing boundary must argue
against this rule, not just add an option.

## 13. Always write; never diff

When writing a managed file, rdk never asks "does this need to change?" — it
writes, even if that overwrites identical content. Prior file contents never
influence generated content; prior state is consulted only as a safety gate
(manifest hash check, seed-once presence check), never to decide output.
*Therefore:* no skip-if-unchanged optimizations, no read-modify-write, no
merging — the state after apply is defined by generation alone, idempotence is
by construction, and the entire cache-invalidation class of staleness bugs
cannot exist. (DD-14)

---

**Using this list:** in review, cite rules by number. If a change needs an
exemption, that's a signal to either redesign the change or amend the rule —
in this file, deliberately, as its own commit.
