package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

// noEnv is an environment with nothing set.
func noEnv(string) (string, bool) { return "", false }

func envWith(vars map[string]string) Env {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func TestResolveDefaults(t *testing.T) {
	o, err := Resolve("", "", "", noEnv)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if o.Format != FormatText {
		t.Errorf("Format = %v, want FormatText", o.Format)
	}
	if o.Level != slog.LevelInfo {
		t.Errorf("Level = %v, want Info", o.Level)
	}
	if o.Color != ColorAuto {
		t.Errorf("Color = %v, want ColorAuto", o.Color)
	}
}

func TestResolveReadsEnv(t *testing.T) {
	o, err := Resolve("", "", "", envWith(map[string]string{
		"RDK_LOG_FORMAT": "jsonl",
		"RDK_LOG_LEVEL":  "warn",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if o.Format != FormatJSONL {
		t.Errorf("Format = %v, want FormatJSONL", o.Format)
	}
	if o.Level != slog.LevelWarn {
		t.Errorf("Level = %v, want Warn", o.Level)
	}
}

// A pipeline sets the env once; a single command still has to be able to
// override it.
func TestFlagBeatsEnv(t *testing.T) {
	o, err := Resolve("text", "debug", "", envWith(map[string]string{
		"RDK_LOG_FORMAT": "jsonl",
		"RDK_LOG_LEVEL":  "error",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if o.Format != FormatText {
		t.Errorf("Format = %v, want FormatText", o.Format)
	}
	if o.Level != slog.LevelDebug {
		t.Errorf("Level = %v, want Debug", o.Level)
	}
}

func TestResolveRejectsBadValues(t *testing.T) {
	cases := []struct{ format, level, colour string }{
		{"yaml", "", ""},
		{"", "chatty", ""},
		{"", "", "rainbow"},
	}
	for _, c := range cases {
		if _, err := Resolve(c.format, c.level, c.colour, noEnv); err == nil {
			t.Errorf("Resolve(%q, %q, %q) succeeded, want error", c.format, c.level, c.colour)
		}
	}
}

// A bad value has to name the valid ones, or the user is left guessing. The
// valid-value list lives in Hint, which diag.Error.Error() deliberately
// omits, so it has to be read off the diagnostic itself.
func TestResolveErrorListsValidValues(t *testing.T) {
	cases := []struct {
		format, level, colour string
		wantSummary           string
		wantHint              string
	}{
		{"yaml", "", "", `unknown log format "yaml"`, "valid formats: text, jsonl"},
		{"", "chatty", "", `unknown log level "chatty"`, "valid levels: debug, info, warn, error"},
		{"", "", "rainbow", `unknown colour mode "rainbow"`, "valid colour modes: auto, always, never"},
	}
	for _, c := range cases {
		_, err := Resolve(c.format, c.level, c.colour, noEnv)
		if err == nil {
			t.Fatalf("Resolve(%q, %q, %q): want error", c.format, c.level, c.colour)
		}
		var d *diag.Error
		if !errors.As(err, &d) {
			t.Fatalf("Resolve(%q, %q, %q): error is not a *diag.Error: %v", c.format, c.level, c.colour, err)
		}
		if !strings.Contains(d.Summary, c.wantSummary) {
			t.Errorf("Summary = %q, want to contain %q", d.Summary, c.wantSummary)
		}
		if d.Hint != c.wantHint {
			t.Errorf("Hint = %q, want %q", d.Hint, c.wantHint)
		}
	}
}

func TestUseColorModes(t *testing.T) {
	var buf bytes.Buffer
	if useColor(ColorNever, &buf, noEnv) {
		t.Error("ColorNever produced colour")
	}
	if !useColor(ColorAlways, &buf, noEnv) {
		t.Error("ColorAlways produced no colour")
	}
}

// A bytes.Buffer is not a terminal, which is what makes tests colourless
// without any special-casing.
func TestUseColorAutoIsOffForNonFiles(t *testing.T) {
	var buf bytes.Buffer
	if useColor(ColorAuto, &buf, noEnv) {
		t.Error("auto produced colour for a non-file writer")
	}
}

func TestUseColorHonoursNoColorAndDumbTerm(t *testing.T) {
	var buf bytes.Buffer
	if useColor(ColorAlways, &buf, envWith(map[string]string{"NO_COLOR": "1"})) {
		t.Error("NO_COLOR did not suppress an explicit --color=always")
	}
	if useColor(ColorAuto, &buf, envWith(map[string]string{"TERM": "dumb"})) {
		t.Error("TERM=dumb produced colour")
	}
}

// Mutating the colour mapping passed every existing test, so pin it.
func TestResolveMapsColourModes(t *testing.T) {
	for flag, want := range map[string]ColorMode{"auto": ColorAuto, "always": ColorAlways, "never": ColorNever} {
		o, err := Resolve("", "", flag, noEnv)
		if err != nil {
			t.Fatalf("--color=%s: %v", flag, err)
		}
		if o.Color != want {
			t.Errorf("--color=%s gave %v, want %v", flag, o.Color, want)
		}
	}
}

// Every level has to map, not just the two the other tests happen to use.
func TestResolveMapsLevels(t *testing.T) {
	for flag, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "error": slog.LevelError,
	} {
		o, err := Resolve("", flag, "", noEnv)
		if err != nil {
			t.Fatalf("--log-level=%s: %v", flag, err)
		}
		if o.Level != want {
			t.Errorf("--log-level=%s gave %v, want %v", flag, o.Level, want)
		}
	}
}

// A redirected stream is a file but not a terminal, which is the case that
// stops `rdk apply > out.txt` filling the file with escape codes.
func TestUseColorAutoIsOffForARedirectedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if useColor(ColorAuto, f, noEnv) {
		t.Error("auto produced colour for a regular file")
	}
}

// Clearing an inherited NO_COLOR is how a user asks for colour back.
func TestEmptyNoColorDoesNotDisableColour(t *testing.T) {
	var buf bytes.Buffer
	if !useColor(ColorAlways, &buf, envWith(map[string]string{"NO_COLOR": ""})) {
		t.Error("an empty NO_COLOR suppressed colour")
	}
}

// An exported-but-empty variable means "unset", not "invalid".
func TestEmptyEnvValueFallsBackToTheDefault(t *testing.T) {
	o, err := Resolve("", "", "", envWith(map[string]string{"RDK_LOG_FORMAT": ""}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if o.Format != FormatText {
		t.Errorf("Format = %v, want the default FormatText", o.Format)
	}
}

// A stale environment variable and a command-line typo produce the same bad
// value; only the message can tell them apart.
func TestErrorNamesTheEnvironmentAsTheSource(t *testing.T) {
	_, err := Resolve("", "", "", envWith(map[string]string{"RDK_LOG_LEVEL": "chatty"}))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "RDK_LOG_LEVEL") {
		t.Errorf("error %q does not name the environment variable", err.Error())
	}
}
