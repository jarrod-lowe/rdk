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

// New builds a logger from resolved options. Writers default to the process
// streams; Env defaults to the real environment.
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
		out: slog.New(newHandler(o, o.Stdout, mu)),
		err: slog.New(newHandler(o, o.Stderr, mu)),
	}
}

func newHandler(o Options, w io.Writer, mu *sync.Mutex) slog.Handler {
	if o.Format == FormatJSONL {
		return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: o.Level, ReplaceAttr: dropTime})
	}
	return newTextHandler(w, o.Level, useColor(o.Color, w, o.Env), mu)
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
	for _, a := range d.Attrs {
		attrs = append(attrs, slog.Any(a.Key, a.Value()))
	}
	l.LogAttrs(ctx, level, d.Summary, attrs...)
}
