package logger

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
)

func newTextLogger(w *bytes.Buffer, colour bool) *slog.Logger {
	return slog.New(newTextHandler(w, slog.LevelDebug, colour, &sync.Mutex{}, nil))
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
// benefit (rule 1). An exact match is what proves their absence: any format
// one might take would break it.
func TestNoTimestamps(t *testing.T) {
	got := logOne(t, false, slog.LevelWarn, "something", slog.String("file", "a.yaml"))
	if want := "warning: a.yaml: something\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
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
	l := slog.New(newTextHandler(&buf, slog.LevelWarn, false, &sync.Mutex{}, nil))
	l.LogAttrs(context.Background(), slog.LevelInfo, "a result")
	if buf.Len() != 0 {
		t.Errorf("info survived a warn threshold: %q", buf.String())
	}
	l.LogAttrs(context.Background(), slog.LevelWarn, "a warning")
	if buf.Len() == 0 {
		t.Error("warn was filtered out at a warn threshold")
	}
}

// An empty message must not leave the separators dangling.
func TestEmptyMessageDoesNotStrandSeparators(t *testing.T) {
	got := logOne(t, false, slog.LevelError, "",
		slog.String("file", "a.yaml"), slog.String("cause", "boom"))
	if want := "error: a.yaml: boom\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The handler must resolve lazy values, as the slog.Handler contract requires.
func TestLogValuerIsResolved(t *testing.T) {
	got := logOne(t, false, slog.LevelError, "invalid YAML", slog.Any("cause", lazyValue{}))
	if want := "error: invalid YAML: resolved\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

type lazyValue struct{}

func (lazyValue) LogValue() slog.Value { return slog.StringValue("resolved") }

// Only the first line used to be indented, so a second line landed flush-left
// and read as separate top-level output — which is why every hint until now
// had to be a single line.
func TestMultiLineHintIsIndentedThroughout(t *testing.T) {
	got := logOne(t, false, slog.LevelInfo, "held 9f3a1c4e",
		slog.String("hint", "apply with: rdk apply --with-lock=9f3a1c4e\nwhen done: rdk unlock 9f3a1c4e"))
	want := "held 9f3a1c4e\n" +
		"  apply with: rdk apply --with-lock=9f3a1c4e\n" +
		"  when done: rdk unlock 9f3a1c4e\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A hint built with fmt.Sprintf("...\n") is an easy mistake — the trailing
// newline must not survive as an indented blank line of its own.
func TestHintTrailingNewlineDoesNotAddBlankLine(t *testing.T) {
	got := logOne(t, false, slog.LevelInfo, "held", slog.String("hint", "do the thing\n"))
	want := "held\n  do the thing\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The closed API means these are unreachable; the panic is what keeps that true.
func TestDerivedHandlersPanic(t *testing.T) {
	h := newTextHandler(&bytes.Buffer{}, slog.LevelDebug, false, &sync.Mutex{}, nil)
	for name, call := range map[string]func(){
		"WithAttrs": func() { h.WithAttrs(nil) },
		"WithGroup": func() { h.WithGroup("g") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			call()
		}()
	}
}
