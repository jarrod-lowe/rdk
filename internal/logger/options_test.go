package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
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

// A bad value has to name the valid ones, or the user is left guessing.
func TestResolveErrorListsValidValues(t *testing.T) {
	_, err := Resolve("yaml", "", "", noEnv)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"yaml", "text", "jsonl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
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
