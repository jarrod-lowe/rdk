package logger

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

// Logger is the only path from rdk to stdout or stderr. Its API is closed —
// no method takes a variadic any — so an unstructured record is not
// expressible, and every line has a code behind it.
//
// Routing is two loggers rather than one handler that dispatches: each slog
// handler already owns its writer, so choosing the stream is choosing which
// logger to call.
type Logger struct {
	out *slog.Logger // results
	err *slog.Logger // diagnostics
}

// New builds a logger from resolved options.
func New(o Options) *Logger {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Env == nil {
		o.Env = os.LookupEnv
	}
	// One mutex across both streams. They are usually the same terminal, so a
	// mutex per stream would still let a result line land between an error and
	// its hint.
	mu := &sync.Mutex{}
	return &Logger{
		// --log-level is a filter over diagnostics — "don't tell me about
		// warnings" — not over a command's answer. Building the stdout handler
		// at the lowest level, unconditionally, is what makes a result
		// unfilterable: only the stderr handler honours the configured level.
		// (This asymmetry is deliberate, not a bug to "fix" — see the PR
		// history for what happened when a result could be silenced: `rdk
		// lock -m work` at --log-level=warn created a lock and printed
		// nothing, so `id=$(rdk lock ...)` came back empty.)
		out: slog.New(newHandler(o, slog.LevelDebug, o.Stdout, mu)),
		err: slog.New(newHandler(o, o.Level, o.Stderr, mu)),
	}
}

func newHandler(o Options, level slog.Level, w io.Writer, mu *sync.Mutex) slog.Handler {
	if o.Format == FormatJSONL {
		return slog.NewJSONHandler(&lockedWriter{mu: mu, w: w}, &slog.HandlerOptions{Level: level, ReplaceAttr: dropTime})
	}
	return newTextHandler(w, level, useColor(o.Color, w, o.Env), mu)
}

// lockedWriter lets the JSON handler share the text handlers' mutex. slog's
// own handler locks per handler, which would leave the two streams
// unsynchronised in JSONL mode alone.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// dropTime removes slog's timestamp: nothing consumes it, and its absence keeps
// output diffable and golden-testable (rule 1).
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// Result reports what a command did. Results go to stdout so a script can read
// them.
func (l *Logger) Result(d diag.Diagnostic) { emit(l.out, slog.LevelInfo, d, nil) }

// Warn reports something surprising that did not stop the run (rule 6).
func (l *Logger) Warn(d diag.Diagnostic) { emit(l.err, slog.LevelWarn, d, nil) }

// Debug reports rdk's own workings. It has no callers yet; it exists so that
// --log-level=debug means something.
func (l *Logger) Debug(d diag.Diagnostic) { emit(l.err, slog.LevelDebug, d, nil) }

// Fail reports the error that ended a command. An error carrying a diagnostic
// is rendered with its provenance; anything else is rdk's own fault and says so
// rather than inventing a file to blame.
func (l *Logger) Fail(err error) {
	if err == nil {
		return
	}
	var d *diag.Error
	if errors.As(err, &d) {
		emit(l.err, slog.LevelError, d.Diagnostic, d.Cause)
		return
	}
	emit(l.err, slog.LevelError, diag.Diagnostic{Code: diag.CodeInternal, Summary: err.Error()}, nil)
}

func emit(l *slog.Logger, level slog.Level, d diag.Diagnostic, cause error) {
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}
	attrs := make([]slog.Attr, 0, 5+len(d.Attrs))
	if d.Code != "" {
		attrs = append(attrs, slog.String("code", d.Code))
	}
	if d.File != "" {
		attrs = append(attrs, slog.String("file", d.File))
	}
	if d.Field != "" {
		attrs = append(attrs, slog.String("field", d.Field))
	}
	if d.Hint != "" {
		attrs = append(attrs, slog.String("hint", d.Hint))
	}
	if cause != nil {
		attrs = append(attrs, slog.String("cause", cause.Error()))
	}
	// The promoted keys are the diagnostic's own. An attr reusing one would
	// emit a duplicate key in JSONL and, worse, silently replace the file or
	// inject a hint in text mode — so the diagnostic wins and the attr is
	// dropped rather than quietly rewriting the record.
	for _, a := range d.Attrs {
		if reserved[a.Key] {
			continue
		}
		attrs = append(attrs, slog.Any(a.Key, a.Value()))
	}
	l.LogAttrs(ctx, level, d.Summary, attrs...)
}

var reserved = map[string]bool{"code": true, "file": true, "field": true, "hint": true, "cause": true}
