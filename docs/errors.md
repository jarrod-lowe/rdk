# Error and warning codes

Every diagnostic rdk emits carries a stable `code`. In JSONL output
(`--log-format=jsonl`) it is the `code` field; matching on it is reliable in a
way that matching on message text is not. The codes are defined in
`internal/diag/codes.go`, and a test fails if one is missing from this page.

Codes never change meaning. A code may be retired, but it is never reused for
something else.

Exit status is a property of the run, not of a code: a failure exits 1, an
unanticipated error exits 2, and warnings and results do not affect the exit
status on their own — a run that warns and then fails still exits 1.

## Failures

| Code | Means | Fix |
|---|---|---|
| `invalid-yaml` | The file is not a YAML mapping — either malformed, or a list or scalar where a single definition was expected. | Read the cause; it names the line. |
| `empty-file` | A `.yaml` file in `rdk/` has no content, or no fields. | Add a definition, or delete the file. |
| `missing-kind` | The definition has no `kind` field. | Add one, e.g. `kind: s3-bucket`. |
| `kind-not-string` | `kind` is present but is not a string. | Quote it or remove the stray type, e.g. `kind: s3-bucket`. |
| `empty-kind` | `kind` is an empty string. | Name a kind, e.g. `kind: s3-bucket`. |
| `unknown-kind` | The named kind is not registered. | Use one of the kinds the message lists. |
| `unknown-field` | A field is not valid for the definition's kind. | Remove it, or use one of the fields the message lists. |
| `missing-field` | A required field is absent. | Add the field the message names. |
| `field-not-string` | A string field holds another YAML type. | Quote the value. |
| `empty-field` | A required string field is blank. | Give it a value. |
| `multi-document` | One file holds several `---`-separated documents. | Split them into one definition per file. |
| `duplicate-name` | Two definitions share a resource name. | Rename one; the message names the other file. |
| `config-cardinality` | The definitions dir does not hold exactly one `kind: config`. | Add the missing one, or remove the extras. |
| `dir-in-defs` | `rdk/` contains a subdirectory. | Move the definitions up into `rdk/`, or move the directory out of `rdk/` if it holds none. |
| `wrong-extension` | A definition uses `.yml`. | Rename it to `.yaml`. |
| `unprocessable-file` | A file in `rdk/` is not a definition. | Move it out, or park it with `.disabled`. |
| `read-defs-dir` | The definitions directory could not be read. | Check it exists and is readable; run `rdk init` if not. |
| `read-file` | A definition file could not be read. | Check its permissions. |
| `git-init` | `git init` failed while initialising the repository. | Read the cause; check git is installed. |
| `write-managed-dir` | The generated tree could not be written or published. | Read the cause; check permissions and free space, then re-run. |
| `publish-failed` | The generated tree was built but could not be moved into place, so `rdk-managed/` is currently absent. | Nothing is lost. Clear the cause, then re-run — a successful apply publishes it. |
| `scratch-not-removed` | The tree was written correctly, but rdk could not remove its displaced copy under `.rdk/`. | The generated tree is correct. Clear `.rdk/old` — something is holding a file open — then re-run. |
| `invalid-flag` | A flag, command, or flag value on the command line was not recognised. | Check the message; run `rdk --help` for the accepted commands and flags. |

## Warnings

| Code | Means | Fix |
|---|---|---|
| `set-aside` | A file is parked with `.disabled` or `.example`, so nothing is generated for it. | Intentional — rename to `.yaml` to enable it. |
| `editor-artifact` | An editor or merge leftover (`.orig`, `.rej`, `.bak`, `~`) sits in `rdk/`. | Delete it, or move it out of the definitions dir. |
| `machine-file` | A file nobody chose to create (`.DS_Store`, `Thumbs.db`, a vim swap file) or a git housekeeping file (`.gitignore`, `.gitkeep`) sits in `rdk/`. | Delete it, or move it out of the definitions dir. |

## Results

| Code | Means |
|---|---|
| `apply-complete` | `rdk apply` finished; carries `files` and `dir`. |
| `init-complete` | `rdk init` finished. |
| `version` | `rdk version` output; carries `version`. |

## Internal

| Code | Means | Fix |
|---|---|---|
| `internal` | A failure rdk did not anticipate; it reached the top without being upgraded to a diagnostic. Exits 2. | This is an rdk bug. Report it with the message. |
