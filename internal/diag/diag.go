// Package diag carries structured, user-facing diagnostics from the packages
// that detect problems to the logger that renders them. It is types only, with
// no dependencies: everything that can go wrong is describable here without
// knowing how it will be printed.
package diag

// Attr is an extra key/value carried into the JSONL output only. It is built
// solely by the typed constructors below, so no call site can smuggle an
// arbitrary value into the log.
type Attr struct {
	Key string
	val any
}

// Value is how the logger reads an attr it cannot construct.
func (a Attr) Value() any { return a.val }

// Str, Int and Bool are the complete set of attr types. Anything else wants
// to be prose in the summary.
func Str(key, val string) Attr       { return Attr{Key: key, val: val} }
func Int(key string, val int) Attr   { return Attr{Key: key, val: val} }
func Bool(key string, val bool) Attr { return Attr{Key: key, val: val} }

// Diagnostic is one thing rdk has to say. Summary must stand alone — Attrs is
// a machine-readable duplicate of what it says, never the only place a fact
// appears, or text mode would silently lose information (rule 11).
type Diagnostic struct {
	Code    string // stable slug from codes.go
	File    string // repo-relative user file, when there is one
	Field   string // yaml field, when known; machine-only
	Summary string // what is wrong and where
	Hint    string // what to do next
	Attrs   []Attr
}

// Line is the one-line human form. Field is deliberately absent: the summary
// already names it.
func (d Diagnostic) Line() string {
	if d.File == "" {
		return d.Summary
	}
	return d.File + ": " + d.Summary
}
