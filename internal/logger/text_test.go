package logger

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
)

func newTextLogger(w *bytes.Buffer, colour bool) *slog.Logger {
	return slog.New(&textHandler{w: w, level: slog.LevelDebug, color: colour, mu: &sync.Mutex{}})
}

func logOne(t *testing.T, colour bool, level slog.Level, msg string, attrs ...slog.Attr) string {
	t.Helper()
	var buf bytes.Buffer
	newTextLogger(&buf, colour).LogAttrs(context.Background(), level, msg, attrs...)
	return buf.String()
}

// A result is the command answering the question it was asked. "INFO rdk
// version 0.1.0" is logger furniture the reader did not ask for.
func TestResultsPrintBare(t *testing.T) {
	got := logOne(t, false, slog.LevelInfo, "rdk version 0.1.0")
	if want := "rdk version 0.1.0\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWarningIsPrefixedAndNamesTheFile(t *testing.T) {
	got := logOne(t, false, slog.LevelWarn, "ignored (.disabled); rdk generates nothing for it",
		slog.String("file", "rdk/logs.yaml.disabled"))
	want := "warning: rdk/logs.yaml.disabled: ignored (.disabled); rdk generates nothing for it\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestErrorShowsCauseAndHint(t *testing.T) {
	got := logOne(t, false, slog.LevelError, "rdk cannot process this file",
		slog.String("file", "rdk/notes.txt"),
		slog.String("hint", "to park one, suffix it .disabled or .example"))
	want := "error: rdk/notes.txt: rdk cannot process this file\n" +
		"  to park one, suffix it .disabled or .example\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCauseIsAppendedToTheLine(t *testing.T) {
	got := logOne(t, false, slog.LevelError, "invalid YAML",
		slog.String("file", "rdk/x.yaml"),
		slog.String("cause", "line 3: mapping values are not allowed"))
	want := "error: rdk/x.yaml: invalid YAML: line 3: mapping values are not allowed\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Attrs exist for JSONL. In text they are already inside the summary, so
// printing them would say everything twice.
func TestAttrsAreNotPrintedInText(t *testing.T) {
	got := logOne(t, false, slog.LevelInfo, "rdk apply: wrote 4 files to rdk-managed/",
		slog.String("code", "apply-complete"), slog.Int("files", 4), slog.String("dir", "rdk-managed"))
	if want := "rdk apply: wrote 4 files to rdk-managed/\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Timestamps would make every line differ from the last, for no reader's
// benefit (rule 1).
func TestNoTimestamps(t *testing.T) {
	got := logOne(t, false, slog.LevelWarn, "something", slog.String("file", "a.yaml"))
	if bytes.Contains([]byte(got), []byte("time")) || bytes.Contains([]byte(got), []byte("20")) {
		t.Errorf("output looks like it carries a timestamp: %q", got)
	}
}

func TestColourWrapsOnlyThePrefix(t *testing.T) {
	got := logOne(t, true, slog.LevelWarn, "ignored", slog.String("file", "a.yaml"))
	want := "\x1b[33mwarning: \x1b[0ma.yaml: ignored\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestErrorColourIsRed(t *testing.T) {
	got := logOne(t, true, slog.LevelError, "boom")
	if want := "\x1b[31merror: \x1b[0mboom\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(&textHandler{w: &buf, level: slog.LevelWarn, mu: &sync.Mutex{}})
	l.LogAttrs(context.Background(), slog.LevelInfo, "a result")
	if buf.Len() != 0 {
		t.Errorf("info survived a warn threshold: %q", buf.String())
	}
	l.LogAttrs(context.Background(), slog.LevelWarn, "a warning")
	if buf.Len() == 0 {
		t.Error("warn was filtered out at a warn threshold")
	}
}
