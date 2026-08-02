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

var _ slog.Handler = (*textHandler)(nil)

// newTextHandler builds a handler for one stream. The mutex is shared with the
// sibling handler rather than owned per-handler: stdout and stderr are usually
// the same terminal, so serialising each stream alone would still let a result
// line land between an error and its hint.
func newTextHandler(w io.Writer, level slog.Level, color bool, mu *sync.Mutex) *textHandler {
	return &textHandler{w: w, level: level, color: color, mu: mu}
}

func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

// The logger's API is closed: nothing constructs a derived handler, so these
// are unreachable by design. Returning the receiver made that claim silently
// false — WithAttrs dropped attrs, and WithGroup left the handler matching
// "file" against what had become "g.file". A panic makes the claim enforceable
// on a path no user input can reach.
func (h *textHandler) WithAttrs([]slog.Attr) slog.Handler {
	panic("logger: WithAttrs is unsupported; the logger API is closed")
}

func (h *textHandler) WithGroup(string) slog.Handler {
	panic("logger: WithGroup is unsupported; the logger API is closed")
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var file, hint, cause string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "file":
			file = a.Value.Resolve().String()
		case "hint":
			hint = a.Value.Resolve().String()
		case "cause":
			cause = a.Value.Resolve().String()
		}
		return true
	})

	parts := make([]string, 0, 3)
	if file != "" {
		parts = append(parts, file)
	}
	if r.Message != "" {
		parts = append(parts, r.Message)
	}
	if cause != "" {
		parts = append(parts, cause)
	}

	var b strings.Builder
	b.WriteString(h.prefix(r.Level))
	b.WriteString(strings.Join(parts, ": "))
	b.WriteByte('\n')
	if hint != "" {
		// Every line gets its own indent, not just the first: a hint that
		// spans lines (rdk lock's follow-up commands, commit 4) must read as
		// one indented block, not a first line of hint followed by flush-left
		// lines that look like separate top-level output. A single trailing
		// newline is trimmed first so it doesn't produce a stray indented
		// blank line — the caller almost certainly meant "this hint ends
		// here", not "print an empty indented line after it".
		hint = strings.TrimSuffix(hint, "\n")
		for _, line := range strings.Split(hint, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
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
