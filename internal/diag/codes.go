package diag

// Every error code rdk can emit, in one flat vocabulary. Centralised rather
// than scattered per package so that uniqueness and documentation coverage are
// both testable — see codes_test.go and docs/errors.md.
const (
	CodeInvalidYAML       = "invalid-yaml"
	CodeEmptyFile         = "empty-file"
	CodeMissingKind       = "missing-kind"
	CodeKindNotString     = "kind-not-string"
	CodeEmptyKind         = "empty-kind"
	CodeUnknownKind       = "unknown-kind"
	CodeUnknownField      = "unknown-field"
	CodeMissingField      = "missing-field"
	CodeFieldNotString    = "field-not-string"
	CodeEmptyField        = "empty-field"
	CodeMultiDocument     = "multi-document"
	CodeDuplicateName     = "duplicate-name"
	CodeConfigCardinality = "config-cardinality"
	CodeDirInDefs         = "dir-in-defs"
	CodeWrongExtension    = "wrong-extension"
	CodeUnprocessableFile = "unprocessable-file"
	CodeSetAside          = "set-aside"
	CodeArtifact          = "artifact"
	CodeReadDefsDir       = "read-defs-dir"
	CodeReadFile          = "read-file"
	CodeGitInit           = "git-init"
	CodeApplyComplete     = "apply-complete"
	CodeInitComplete      = "init-complete"
	CodeVersion           = "version"
	CodeInternal          = "internal"
)

// All lists every code so the documentation coverage test can check them.
var All = []string{
	CodeInvalidYAML, CodeEmptyFile, CodeMissingKind, CodeKindNotString,
	CodeEmptyKind, CodeUnknownKind, CodeUnknownField, CodeMissingField,
	CodeFieldNotString, CodeEmptyField, CodeMultiDocument, CodeDuplicateName,
	CodeConfigCardinality, CodeDirInDefs, CodeWrongExtension,
	CodeUnprocessableFile, CodeSetAside, CodeArtifact, CodeReadDefsDir,
	CodeReadFile, CodeGitInit, CodeApplyComplete, CodeInitComplete,
	CodeVersion, CodeInternal,
}
