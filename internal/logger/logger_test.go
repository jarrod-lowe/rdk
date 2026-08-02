package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

func newTestLogger(f Format) (*Logger, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	l := New(Options{Format: f, Level: slog.LevelDebug, Color: ColorNever,
		Stdout: &out, Stderr: &errBuf, Env: noEnv})
	return l, &out, &errBuf
}

// stdout is the command's answer, so `rdk apply | tail -1` keeps working;
// anything diagnostic stays out of that stream.
func TestResultsGoToStdoutAndDiagnosticsToStderr(t *testing.T) {
	l, out, errBuf := newTestLogger(FormatText)
	l.Result(diag.Diagnostic{Code: diag.CodeVersion, Summary: "rdk version 0.1.0"})
	l.Warn(diag.Diagnostic{Code: diag.CodeSetAside, File: "rdk/a.yaml.disabled", Summary: "ignored"})
	l.Fail(diag.New(diag.Diagnostic{Code: diag.CodeUnknownKind, File: "rdk/x.yaml", Summary: "unknown kind"}))

	if got := out.String(); got != "rdk version 0.1.0\n" {
		t.Errorf("stdout = %q, want just the result", got)
	}
	got := errBuf.String()
	if !strings.Contains(got, "warning: rdk/a.yaml.disabled: ignored") {
		t.Errorf("stderr missing the warning: %q", got)
	}
	if !strings.Contains(got, "error: rdk/x.yaml: unknown kind") {
		t.Errorf("stderr missing the error: %q", got)
	}
}

func TestJSONLResultCarriesAttrs(t *testing.T) {
	l, out, _ := newTestLogger(FormatJSONL)
	l.Result(diag.Diagnostic{
		Code:    diag.CodeApplyComplete,
		Summary: "rdk apply: wrote 4 files to rdk-managed/",
		Attrs:   []diag.Attr{diag.Int("files", 4), diag.Str("dir", "rdk-managed")},
	})

	var rec map[string]any
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, out.String())
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["code"] != diag.CodeApplyComplete {
		t.Errorf("code = %v, want %v", rec["code"], diag.CodeApplyComplete)
	}
	if rec["files"] != float64(4) {
		t.Errorf("files = %v, want 4", rec["files"])
	}
	if rec["dir"] != "rdk-managed" {
		t.Errorf("dir = %v, want rdk-managed", rec["dir"])
	}
}

// A timestamp would make identical runs produce different output.
func TestJSONLHasNoTimestamp(t *testing.T) {
	l, out, _ := newTestLogger(FormatJSONL)
	l.Result(diag.Diagnostic{Code: diag.CodeVersion, Summary: "rdk version 0.1.0"})
	var rec map[string]any
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if _, present := rec["time"]; present {
		t.Errorf("record carries a timestamp: %v", rec)
	}
}

// Our sentence stays machine-stable; the library's wording lives in its own
// field rather than being spliced into the message.
func TestJSONLKeepsCauseSeparateFromMessage(t *testing.T) {
	l, _, errBuf := newTestLogger(FormatJSONL)
	l.Fail(diag.Wrap(errors.New("line 3: bad mapping"), diag.Diagnostic{
		Code: diag.CodeInvalidYAML, File: "rdk/x.yaml", Summary: "invalid YAML",
	}))
	var rec map[string]any
	if err := json.Unmarshal(errBuf.Bytes(), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, errBuf.String())
	}
	if rec["msg"] != "invalid YAML" {
		t.Errorf("msg = %v, want the summary alone", rec["msg"])
	}
	if rec["cause"] != "line 3: bad mapping" {
		t.Errorf("cause = %v", rec["cause"])
	}
	if rec["file"] != "rdk/x.yaml" {
		t.Errorf("file = %v", rec["file"])
	}
}

// A JSON line with escape codes in it is broken for every consumer.
func TestJSONLIsNeverColoured(t *testing.T) {
	var out, errBuf bytes.Buffer
	l := New(Options{Format: FormatJSONL, Level: slog.LevelDebug, Color: ColorAlways,
		Stdout: &out, Stderr: &errBuf, Env: noEnv})
	l.Warn(diag.Diagnostic{Code: diag.CodeEditorArtifact, File: "a.yaml.bak", Summary: "leftover"})
	if strings.Contains(errBuf.String(), "\x1b[") {
		t.Errorf("JSONL output carries ANSI codes: %q", errBuf.String())
	}
}

// An unwrapped error means rdk failed in a way it did not anticipate, and the
// output should say so rather than implying the user's YAML is at fault.
func TestFailOnAPlainErrorIsInternal(t *testing.T) {
	l, _, errBuf := newTestLogger(FormatJSONL)
	l.Fail(errors.New("no such file or directory"))
	var rec map[string]any
	if err := json.Unmarshal(errBuf.Bytes(), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v", err)
	}
	if rec["code"] != diag.CodeInternal {
		t.Errorf("code = %v, want %v", rec["code"], diag.CodeInternal)
	}
	if _, present := rec["file"]; present {
		t.Error("an internal error invented provenance")
	}
}

func TestFailFindsAWrappedDiagnostic(t *testing.T) {
	l, _, errBuf := newTestLogger(FormatText)
	inner := diag.New(diag.Diagnostic{Code: diag.CodeUnknownKind, File: "rdk/x.yaml", Summary: "unknown kind"})
	l.Fail(fmt.Errorf("apply: %w", inner))
	if !strings.Contains(errBuf.String(), "error: rdk/x.yaml: unknown kind") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

// --log-level filters diagnostics, not a command's answer: a result survives
// any configured level, while a warning at a stricter level than it carries
// does not.
func TestLogLevelFiltersWarningsButNeverResults(t *testing.T) {
	var out, errBuf bytes.Buffer
	l := New(Options{Format: FormatText, Level: slog.LevelError, Color: ColorNever,
		Stdout: &out, Stderr: &errBuf, Env: noEnv})
	l.Result(diag.Diagnostic{Code: diag.CodeApplyComplete, Summary: "rdk apply: wrote 4 files"})
	l.Warn(diag.Diagnostic{Code: diag.CodeSetAside, File: "a.yaml.disabled", Summary: "ignored"})
	if out.Len() == 0 {
		t.Error("stdout is empty, want the result to survive --log-level=error")
	}
	if errBuf.Len() != 0 {
		t.Errorf("stderr = %q, want the warning suppressed at error level", errBuf.String())
	}
}

func TestDebugIsSilentAtTheDefaultLevel(t *testing.T) {
	var out, errBuf bytes.Buffer
	l := New(Options{Format: FormatText, Level: slog.LevelInfo, Color: ColorNever,
		Stdout: &out, Stderr: &errBuf, Env: noEnv})
	l.Debug(diag.Diagnostic{Code: diag.CodeInternal, Summary: "considering rdk/x.yaml"})
	if errBuf.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errBuf.String())
	}
}

func TestFailOnNilDoesNothing(t *testing.T) {
	l, out, errBuf := newTestLogger(FormatText)
	l.Fail(nil)
	if out.Len() != 0 || errBuf.Len() != 0 {
		t.Errorf("Fail(nil) wrote output: stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

// The shared mutex is the most deliberate decision in this file, and nothing
// exercised it. Run with -race.
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSONL} {
		l, out, errBuf := newTestLogger(f)
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l.Result(diag.Diagnostic{Code: diag.CodeVersion, Summary: "a result"})
				l.Warn(diag.Diagnostic{Code: diag.CodeSetAside, File: "a.yaml", Summary: "a warning"})
			}()
		}
		wg.Wait()
		for _, line := range strings.Split(strings.TrimSpace(out.String()+errBuf.String()), "\n") {
			if line == "" {
				t.Error("blank line in output")
			}
		}
	}
}

// Nothing pinned Field reaching the record, so deleting its promotion stayed green.
func TestJSONLCarriesTheField(t *testing.T) {
	l, _, errBuf := newTestLogger(FormatJSONL)
	l.Fail(diag.New(diag.Diagnostic{
		Code: diag.CodeUnknownField, File: "rdk/x.yaml", Field: "colour",
		Summary: `unknown field "colour"`, Hint: "valid fields: name, description",
	}))
	var rec map[string]any
	if err := json.Unmarshal(errBuf.Bytes(), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v", err)
	}
	if rec["field"] != "colour" {
		t.Errorf("field = %v, want colour", rec["field"])
	}
	if rec["hint"] != "valid fields: name, description" {
		t.Errorf("hint = %v", rec["hint"])
	}
}

// A warning in JSONL has to be a warning, on stderr, with stdout untouched.
func TestJSONLWarningGoesToStderrAtWarnLevel(t *testing.T) {
	l, out, errBuf := newTestLogger(FormatJSONL)
	l.Warn(diag.Diagnostic{Code: diag.CodeSetAside, File: "a.yaml.disabled", Summary: "ignored"})
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	var rec map[string]any
	if err := json.Unmarshal(errBuf.Bytes(), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v", err)
	}
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
}

// An attr may not quietly rewrite the diagnostic that carries it.
func TestReservedAttrKeysAreDropped(t *testing.T) {
	l, _, errBuf := newTestLogger(FormatText)
	l.Warn(diag.Diagnostic{
		Code: diag.CodeSetAside, File: "real.yaml", Summary: "ignored",
		Attrs: []diag.Attr{diag.Str("file", "attr.yaml"), diag.Str("hint", "invented")},
	})
	if got, want := errBuf.String(), "warning: real.yaml: ignored\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A caller-built zero Options must not panic; the zero values are the defaults.
func TestZeroOptionsIsUsable(t *testing.T) {
	if New(Options{}) == nil {
		t.Error("New(Options{}) returned nil")
	}
}
