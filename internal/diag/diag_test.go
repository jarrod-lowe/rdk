package diag

import "testing"

func TestLineComposesFileAndSummary(t *testing.T) {
	d := Diagnostic{File: "rdk/x.yaml", Summary: `unknown kind "volcano"`}
	if got, want := d.Line(), `rdk/x.yaml: unknown kind "volcano"`; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

// A diagnostic about the run as a whole has no file to name.
func TestLineWithoutFileIsJustTheSummary(t *testing.T) {
	d := Diagnostic{Summary: "rdk apply: wrote 4 files to rdk-managed/"}
	if got, want := d.Line(), "rdk apply: wrote 4 files to rdk-managed/"; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

// Field is machine-only: the summary already names the offending field, so
// repeating it in the human line would read as a stutter.
func TestLineOmitsField(t *testing.T) {
	d := Diagnostic{File: "rdk/x.yaml", Field: "colour", Summary: `unknown field "colour"`}
	if got, want := d.Line(), `rdk/x.yaml: unknown field "colour"`; got != want {
		t.Errorf("Line() = %q, want %q", got, want)
	}
}

func TestTypedFieldConstructors(t *testing.T) {
	fields := []Field{Str("dir", "rdk-managed"), Int("files", 4), Bool("dry", true)}
	want := []any{"rdk-managed", 4, true}
	for i, f := range fields {
		if f.Value() != want[i] {
			t.Errorf("fields[%d].Value() = %v, want %v", i, f.Value(), want[i])
		}
	}
	if fields[0].Key != "dir" {
		t.Errorf("Key = %q, want %q", fields[0].Key, "dir")
	}
}
