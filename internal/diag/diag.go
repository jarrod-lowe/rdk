// Package diag carries structured, user-facing diagnostics from the packages
// that detect problems to the logger that renders them. It is types only, with
// no dependencies: everything that can go wrong is describable here without
// knowing how it will be printed.
package diag

import "strings"

// Severity is how loud a diagnostic is. The constants are prefixed because a
// bare Error would collide with the Error type in error.go.
type Severity int

const (
	SeverityDebug Severity = iota
	SeverityInfo
	SeverityWarn
	SeverityError
)

// Field is an extra key/value carried into the JSONL output only. It is built
// solely by the typed constructors below, so no call site can smuggle an
// arbitrary value into the log.
type Field struct {
	Key string
	val any
}

// Value is how the logger reads a field it cannot construct.
func (f Field) Value() any { return f.val }

// Str, Int and Bool are the complete set of field types. Anything else wants
// to be prose in the summary.
func Str(key, val string) Field       { return Field{Key: key, val: val} }
func Int(key string, val int) Field   { return Field{Key: key, val: val} }
func Bool(key string, val bool) Field { return Field{Key: key, val: val} }

// Diagnostic is one thing rdk has to say. Summary must stand alone — Fields is
// a machine-readable duplicate of what it says, never the only place a fact
// appears, or text mode would silently lose information (rule 11).
type Diagnostic struct {
	Severity Severity
	Code     string // stable slug from codes.go
	File     string // repo-relative user file, when there is one
	Field    string // yaml field, when known; machine-only
	Summary  string // what is wrong and where
	Hint     string // what to do next
	Fields   []Field
}

// Line is the one-line human form. Field is deliberately absent: the summary
// already names it.
func (d Diagnostic) Line() string {
	if d.File == "" {
		return d.Summary
	}
	return strings.Join([]string{d.File, d.Summary}, ": ")
}
