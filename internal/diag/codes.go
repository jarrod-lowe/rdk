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
	CodeEditorArtifact    = "editor-artifact"
	CodeMachineFile       = "machine-file"
	CodeReadDefsDir       = "read-defs-dir"
	CodeReadFile          = "read-file"
	CodeGitInit           = "git-init"
	CodeGitUnusable       = "git-unusable"
	CodeWriteManagedDir   = "write-managed-dir"
	CodePublishFailed     = "publish-failed"
	CodeScratchNotRemoved = "scratch-not-removed"
	CodeScratchTarget     = "scratch-target"
	CodeUnsafePath        = "unsafe-path"
	CodeSeedNotAFile      = "seed-not-a-file"
	CodeSeedFailed        = "seed-failed"
	CodeApplyComplete     = "apply-complete"
	CodeInitComplete      = "init-complete"
	CodeVersion           = "version"
	CodeInternal          = "internal"
	CodeInvalidFlag       = "invalid-flag"
	CodeApplyLocked       = "apply-locked"
	CodeLockMismatch      = "lock-mismatch"
	CodeLockBroken        = "lock-broken"
	CodeLockHeld          = "lock-held"
	CodeUnlocked          = "unlocked"
)

// all lists every code so the documentation coverage tests can check them.
// Unexported: nothing outside this package's own tests reads it, and an
// exported mutable slice would be shared state anyone could reorder.
var all = []string{
	CodeInvalidYAML, CodeEmptyFile, CodeMissingKind, CodeKindNotString,
	CodeEmptyKind, CodeUnknownKind, CodeUnknownField, CodeMissingField,
	CodeFieldNotString, CodeEmptyField, CodeMultiDocument, CodeDuplicateName,
	CodeConfigCardinality, CodeDirInDefs, CodeWrongExtension,
	CodeUnprocessableFile, CodeSetAside, CodeEditorArtifact, CodeMachineFile,
	CodeReadDefsDir,
	CodeReadFile, CodeGitInit, CodeGitUnusable, CodeWriteManagedDir, CodePublishFailed,
	CodeScratchNotRemoved, CodeScratchTarget, CodeUnsafePath, CodeSeedNotAFile, CodeSeedFailed,
	CodeApplyComplete, CodeInitComplete,
	CodeVersion, CodeInternal, CodeInvalidFlag,
	CodeApplyLocked, CodeLockMismatch, CodeLockBroken, CodeLockHeld, CodeUnlocked,
}
