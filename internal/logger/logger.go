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

	// outErr latches the first failure writing to out. Result sits on
	// slog.Logger.LogAttrs, whose signature is void, so a handler's write
	// error has no way back to a caller through the normal return path — this
	// is the side channel that lets Delivered report it after the fact. There
	// is no equivalent field for err: see Delivered's doc for why a stderr
	// failure does not need the same treatment.
	outErr *streamError
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
	outErr := &streamError{}
	return &Logger{
		// --log-level is a filter over diagnostics — "don't tell me about
		// warnings" — not over a command's answer. Building the stdout handler
		// at the lowest level, unconditionally, is what makes a result
		// unfilterable: only the stderr handler honours the configured level.
		// (This asymmetry is deliberate, not a bug to "fix" — see the PR
		// history for what happened when a result could be silenced: `rdk
		// lock -m work` at --log-level=warn created a lock and printed
		// nothing, so `id=$(rdk lock ...)` came back empty.)
		out: slog.New(newHandler(o, slog.LevelDebug, o.Stdout, mu, outErr)),
		// nil: the err handler's write failures are never read back out, so
		// there is nothing for it to record into. See Delivered's doc.
		err:    slog.New(newHandler(o, o.Level, o.Stderr, mu, nil)),
		outErr: outErr,
	}
}

// Delivered reports whether every result this Logger has emitted has actually
// reached stdout: nil if so, or the first write failure otherwise (a full
// disk, or a broken pipe on a redirected stdout). Result's own signature is
// void — it has to be, to stay a thin wrapper over slog.Logger.LogAttrs,
// which is itself void — so this is the only way such a failure ever reaches
// a caller. It exists to be checked once, by Execute, rather than by every
// Result call site: that makes the protection structural, not a convention
// every future command has to remember, and it is what stops `rdk lock` (or
// any command) exiting 0 having taken an action but never shown the caller
// its result.
//
// Only out is tracked, not err: a lost warning, or a lost copy of the very
// error message that is already the reason for a non-zero exit, does not
// change whether the run's actual deliverable got through. Every command's
// deliverable is what Result carries, and stdout is the only stream that
// matters for judging whether it did.
func (l *Logger) Delivered() error {
	return l.outErr.load()
}

func newHandler(o Options, level slog.Level, w io.Writer, mu *sync.Mutex, errs *streamError) slog.Handler {
	if o.Format == FormatJSONL {
		return slog.NewJSONHandler(&lockedWriter{mu: mu, w: w, errs: errs}, &slog.HandlerOptions{Level: level, ReplaceAttr: dropTime})
	}
	return newTextHandler(w, level, useColor(o.Color, w, o.Env), mu, errs)
}

// lockedWriter lets the JSON handler share the text handlers' mutex. slog's
// own handler locks per handler, which would leave the two streams
// unsynchronised in JSONL mode alone.
type lockedWriter struct {
	mu   *sync.Mutex
	w    io.Writer
	errs *streamError // nil is valid; see streamError
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.w.Write(p)
	l.errs.record(err)
	return n, err
}

// streamError latches the first write failure a handler observes, on behalf
// of Logger.Delivered — see its doc for why this exists at all. A nil
// *streamError is valid and simply records nothing: only the stdout handler
// is given a real one, so the stderr handler's writes (which Delivered never
// reports) don't need a mutex and a field they'd never use.
type streamError struct {
	mu  sync.Mutex
	err error
}

func (s *streamError) record(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *streamError) load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
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
