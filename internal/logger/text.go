package logger

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// ANSI codes, written by hand rather than pulled in as a dependency: three
// colours is not a library's worth of problem.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m"
)

// textHandler renders records for a person at a terminal: no timestamps, no
// level= keys, and results printed bare so `rdk version` reads as itself.
// slog's own TextHandler emits logfmt, which is the wrong shape for output
// whose job is to be read and acted on (rule 11).
type textHandler struct {
	w     io.Writer
	level slog.Level
	color bool
	mu    *sync.Mutex
}

func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

// The logger's API is closed and never calls these, so they return the handler
// unchanged rather than carrying attrs no caller can set.
func (h *textHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *textHandler) WithGroup(string) slog.Handler      { return h }

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var file, hint, cause string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "file":
			file = a.Value.String()
		case "hint":
			hint = a.Value.String()
		case "cause":
			cause = a.Value.String()
		}
		return true
	})

	var b strings.Builder
	b.WriteString(h.prefix(r.Level))
	if file != "" {
		b.WriteString(file)
		b.WriteString(": ")
	}
	b.WriteString(r.Message)
	if cause != "" {
		b.WriteString(": ")
		b.WriteString(cause)
	}
	b.WriteByte('\n')
	if hint != "" {
		b.WriteString("  ")
		b.WriteString(hint)
		b.WriteByte('\n')
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// prefix labels the severity. Results get none: the answer to a command is not
// an event to be logged.
func (h *textHandler) prefix(l slog.Level) string {
	var word, colour string
	switch l {
	case slog.LevelError:
		word, colour = "error: ", ansiRed
	case slog.LevelWarn:
		word, colour = "warning: ", ansiYellow
	case slog.LevelDebug:
		word, colour = "debug: ", ansiDim
	default:
		return ""
	}
	if !h.color {
		return word
	}
	return colour + word + ansiReset
}
