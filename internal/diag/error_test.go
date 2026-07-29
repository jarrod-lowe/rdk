package diag

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestErrorTextIsTheComposedLine(t *testing.T) {
	err := New(Diagnostic{Code: "unknown-kind", File: "rdk/x.yaml", Summary: `unknown kind "volcano"`})
	if got, want := err.Error(), `rdk/x.yaml: unknown kind "volcano"`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A consumer that only prints the error must still see what the library said,
// or the underlying reason is lost.
func TestWrapAppendsTheCause(t *testing.T) {
	cause := errors.New(`"description" already defined at line 3`)
	err := Wrap(cause, Diagnostic{Code: "invalid-yaml", File: "rdk/x.yaml", Summary: "invalid YAML"})
	want := `rdk/x.yaml: invalid YAML: "description" already defined at line 3`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// Wrapping must not sever errors.Is, or callers lose the ability to test for
// sentinel filesystem errors.
func TestWrapPreservesErrorsIs(t *testing.T) {
	err := Wrap(fs.ErrNotExist, Diagnostic{Code: "read-file", File: "rdk/x.yaml", Summary: "cannot read"})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Error("errors.Is lost the cause")
	}
}

// Diagnostics bubble up through fmt.Errorf wrapping in intermediate packages.
func TestErrorsAsFindsADeeplyWrappedDiagnostic(t *testing.T) {
	inner := New(Diagnostic{Code: "unknown-kind", File: "rdk/x.yaml", Summary: "unknown kind"})
	outer := fmt.Errorf("apply: %w", fmt.Errorf("parse: %w", inner))
	var got *Error
	if !errors.As(outer, &got) {
		t.Fatal("errors.As did not find the diagnostic")
	}
	if got.Code != "unknown-kind" {
		t.Errorf("Code = %q, want %q", got.Code, "unknown-kind")
	}
}

// The split is what lets a script tell "fix your YAML" from "rdk is broken".
func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"diagnostic", New(Diagnostic{Code: "unknown-kind", Summary: "x"}), 1},
		{"wrapped diagnostic", fmt.Errorf("apply: %w", New(Diagnostic{Code: "unknown-kind", Summary: "x"})), 1},
		{"plain error", errors.New("boom"), 2},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}
