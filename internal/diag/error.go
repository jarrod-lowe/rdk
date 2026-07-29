package diag

import "errors"

// Error is a failure that knows which user file and field caused it. There is
// exactly one such type: the variation between failures lives in the fields,
// not in a family of types.
type Error struct {
	Diagnostic
	Cause error // the plain error being upgraded, if any
}

// Error returns the composed line rather than the bare summary, so a consumer
// that merely prints the error still sees the file and the cause.
func (e *Error) Error() string {
	s := e.Diagnostic.Line()
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap keeps errors.Is working on the cause.
func (e *Error) Unwrap() error { return e.Cause }

// New reports a failure rdk detected itself.
func New(d Diagnostic) *Error { return &Error{Diagnostic: d} }

// Wrap upgrades a plain error at the boundary that knows the user-facing
// context. There is deliberately no automatic conversion: an upgrade that
// guessed a code would fabricate provenance, which is rule 11 backwards. An
// error that reaches the top unwrapped is genuinely unanticipated, and says so
// by exiting 2.
func Wrap(err error, d Diagnostic) *Error {
	return &Error{Diagnostic: d, Cause: err}
}

// ExitCode classifies a failure for the process exit status: 1 when the user's
// definitions are at fault, 2 when rdk is.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var d *Error
	if errors.As(err, &d) {
		return 1
	}
	return 2
}
