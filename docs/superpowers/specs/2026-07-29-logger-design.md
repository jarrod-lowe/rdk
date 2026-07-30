# `internal/diag` + `internal/logger` — one way to talk to the user

**Status:** approved design, pre-implementation.

## Problem

Output has no owner. Four `fmt.Fprint*` calls sit in `cmd/`, warnings travel as
pre-formatted strings out of `parse.Dir`, and errors are printed by cobra itself
(`SilenceErrors: false`). Nothing decides which stream a message belongs on,
nothing renders a warning and an error consistently, and there is no
machine-readable form of any of it.

That collides with two rules. Rule 11 says output is written for the person or
AI who must act on it, naming the user's file and field — but a
`fmt.Errorf("%s: ...")` string is prose, so a consumer can only regex it. Rule 6
says surprises must announce themselves, which presumes a channel that reliably
carries them. Both want one output path with structure behind it.

## Goals

- Exactly one way to write to stdout or stderr, enforced by a test rather than
  by discipline.
- Three modes: coloured text, plain text, JSONL. Default text; colour resolved
  per stream from TTY, `TERM` and `NO_COLOR`.
- Diagnostics are typed values carrying code, file, field, summary and hint —
  not strings — so the JSONL form is genuinely machine-readable.
- Generation stays a pure function (rule 1): non-`cmd` packages return
  diagnostics, they never print.
- Existing `cmd` tests keep working, because the logger writes through cobra's
  `OutOrStdout`/`ErrOrStderr`.

## Non-goals (YAGNI — add when something needs them)

- Accumulating multiple errors per apply. `parse` keeps failing on the first
  error; only warnings come back as a slice. Multi-error collection is its own
  design with its own cost (rule 12: an admitted flexibility ships with its
  limit).
- Log destinations other than the two standard streams — no files, no syslog,
  no rotation.
- Provenance chains (rule 5). `Diagnostic` has room to grow a provenance field
  when resolution lands; it does not have one now.

## Design

### `internal/diag` — types only

No dependencies, mirroring how `internal/schema` is types-only.

```go
// Attr is an extra key/value for the JSONL form, built only by the typed
// constructors diag.Str, diag.Int and diag.Bool — a closed set, so no call
// site can smuggle an arbitrary value in.
type Attr struct {
    Key string
    val any // unexported: only the constructors can set it
}

func (f Attr) Value() any { return f.val } // how the logger reads it

type Diagnostic struct {
    Code     string   // stable slug from codes.go, e.g. "unknown-kind"
    File     string   // repo-relative user file, when there is one
    Field    string   // yaml field, when known
    Summary  string   // self-contained: what is wrong and where
    Hint     string   // what to do next
    Attrs    []Attr   // typed extras, emitted in JSONL only
}

type Error struct {
    Diagnostic
    Cause error       // the plain error being upgraded, if any
}

func (d Diagnostic) Line() string { ... }  // "file: summary"
func (e *Error) Error() string           // Line(), plus ": cause" when present
func (e *Error) Unwrap() error { return e.Cause }

// Wrap upgrades a plain error at the boundary that knows the user-facing
// context. Code and File are required.
func Wrap(err error, d Diagnostic) *Error
```

`Summary` must stand alone. `Attrs` is a machine-readable duplicate of what the
summary already says, never the only place a fact appears — otherwise text mode
silently loses information. `Field` is machine-only for the same reason: the
summary already names the offending field, so `Line()` composes `File` and
`Summary` and nothing else.

`Error()` returns the composed line rather than the bare summary, so that any
consumer which merely prints the error still sees the file and the underlying
cause. This is what keeps the existing `parse` tests — which assert on
`err.Error()` containing both the filename and the YAML library's wording —
passing through the migration.

**There is deliberately no `FromError(err)`.** An automatic upgrade could only
invent a code and fabricate provenance, which is rule 11 backwards. Errors are
wrapped at the boundary that knows the file, and nowhere else. An error that
reaches the top unwrapped is genuinely unanticipated, renders as internal, and
exits 2 — so forgetting to wrap surfaces as rdk reporting its own bug, which is
the loud failure mode we want.

### `internal/logger` — rendering and streams

Built on `log/slog`. The logger holds **two `*slog.Logger`s**, one bound to
stdout and one to stderr; routing is choosing which to call, since each handler
already owns its writer. No routing handler is needed.

The API is closed — no method takes a variadic `any`, so an unstructured record
is not expressible:

```go
func (l *Logger) Debug(d diag.Diagnostic)   // -> stderr
func (l *Logger) Result(d diag.Diagnostic)  // Info  -> stdout
func (l *Logger) Warn(d diag.Diagnostic)    // Warn  -> stderr
func (l *Logger) Fail(err error)            // Error -> stderr
```

`Fail` does `errors.As` for `*diag.Error`. Anything else renders as a bare
`error: <err>` with `code:"internal"` in JSONL — the honest signal that rdk
failed rather than the input being wrong.

`Debug` has no callers yet. It exists so `--log-level=debug` means something.

### Text handler

A custom `slog.Handler` (~80 lines). Info prints the summary bare — `rdk version
0.1.0` should not read `INFO rdk version 0.1.0`. Warn and Error take a
`warning:`/`error:` prefix, coloured yellow and red when colour is on. `File`
and `Field` compose into the line, `Hint` follows it, `Cause` is appended once,
and `Attrs` is dropped because the summary already carries it.

```
rdk apply: wrote 4 files to rdk-managed/
warning: rdk/logs.yaml.disabled: ignored (.disabled); rdk generates nothing for it
error: rdk/notes.txt: rdk cannot process this file
  to park one, suffix it .disabled or .example
```

No timestamps, ever — in either mode. Nothing consumes them, and their absence
keeps output diffable and golden-testable.

### JSONL handler

`slog.NewJSONHandler` with a `ReplaceAttr` that drops `time`. Colour is forced
off unconditionally: a JSON line containing ANSI codes is broken for every
consumer.

```json
{"level":"WARN","msg":"ignored (.disabled); rdk generates nothing for it","code":"set-aside","file":"rdk/logs.yaml.disabled"}
{"level":"INFO","msg":"rdk apply: wrote 4 files to rdk-managed/","code":"apply-complete","files":4,"dir":"rdk-managed"}
```

`msg` is our sentence and stays machine-stable; an upgraded error keeps the
library's wording in a separate `cause` field.

### Configuration

Precedence is flag > environment > default.

| Flag | Env | Values | Default |
|---|---|---|---|
| `--log-format` | `RDK_LOG_FORMAT` | `text`, `jsonl` | `text` |
| `--log-level` | `RDK_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `--color` | — | `auto`, `always`, `never` | `auto` |

`auto` is resolved per stream: the writer is an `*os.File` on a character
device, `TERM` is not `dumb`, and `NO_COLOR` is unset. Test buffers are not
files, so tests are colourless without special-casing. `NO_COLOR` is honoured
because goal.md aims rdk at AI consumers as well as people, and that is the
conventional signal for "no escape codes".

Because results are Info, `--log-level=warn` is the quiet mode that suppresses
the summary while keeping warnings, and `--log-level=error` is near-silent.

### Who may call the logger

Only `cmd/`. `parse`, `generate`, `apply`, `repofs` and `initialize` return
diagnostics and errors and never print — which preserves rule 1's pure-function
property and makes "the only way to output" true by construction.

The logger is built in the root command's `PersistentPreRunE` from the resolved
flags, writing through `cmd.OutOrStdout()`/`ErrOrStderr()`. `SilenceErrors`
becomes `true`; `main.go` takes the error `Execute` returns and hands it to
`Fail`, falling back to a default text logger for flag-parse errors that occur
before the configured one exists.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | user or definition error — a `*diag.Error` reached the top |
| 2 | internal error — an unwrapped error reached the top |

A script can distinguish "fix your YAML" from "rdk is broken".

### Error codes and their documentation

Every code is a constant in `internal/diag/codes.go` — one flat vocabulary — and
`docs/errors.md` documents each as code → meaning → fix. A test asserts every
constant appears in the doc.

This means `diag` names parse-specific concepts like `unknown-kind`, which is
mildly odd for a types-only package. It is worth it: a vocabulary scattered
across packages cannot be checked for uniqueness or documented completeness, and
an undocumented error code is worse than no code at all.

Initial codes: `invalid-yaml`, `empty-file`, `missing-kind`, `kind-not-string`,
`empty-kind`, `unknown-kind`, `unknown-field`, `missing-field`,
`field-not-string`, `empty-field`, `multi-document`, `duplicate-name`,
`config-cardinality`, `dir-in-defs`, `wrong-extension`, `unprocessable-file`,
`set-aside`, `editor-artifact`, `read-defs-dir`, `read-file`, `git-init`,
`apply-complete`, `init-complete`, `version`, `internal`.

### Enforcement

A test walks the repository's `.go` files and fails on `fmt.Print*`, `println`,
`os.Stdout` or `os.Stderr` outside `internal/logger`. Without it, "the only way
to output" decays on the first hurried commit.

## Migration

- `parse.Dir` returns `[]diag.Diagnostic` instead of `[]string`.
- `classify` builds `Diagnostic` values rather than pre-formatted strings.
- Every `fmt.Errorf` in `parse.go` becomes a `*diag.Error` with a code. The
  `"%s: "` filename prefixing is removed — `File` carries it, and prefixing
  would print it twice.
- `apply.Result.Summary()` becomes `Diagnostic()`, carrying `files` and `dir`
  as typed fields.
- The three print sites in `cmd/` become `Result` calls.
- `cmd/root.go` gains the three persistent flags and `SilenceErrors: true`.

## Testing

- Table tests rendering a representative `Diagnostic` of each severity in all
  three modes, including one with a `Cause` and one with `Attrs`.
- Colour resolution: TTY, non-TTY, `NO_COLOR`, `TERM=dumb`, `--color=always`
  into a buffer, and JSONL forcing colour off.
- `Fail` with a `*diag.Error`, with a wrapped `*diag.Error` several layers deep,
  and with a plain error — asserting exit-code classification.
- Precedence tests for flag > env > default on format and level.
- The source-walking enforcement test.
- Existing `cmd` tests continue to assert on buffers and must pass with only the
  expected message-shape updates.
