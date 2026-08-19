// Package logger is the only way rdk writes to stdout or stderr. It renders
// diag.Diagnostic values in one of three modes — coloured text, plain text, or
// JSONL — over log/slog.
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jarrod-lowe/rdk/internal/diag"
)

// Format is how records are rendered.
type Format int

const (
	FormatText Format = iota
	FormatJSONL
)

// ColorMode is the requested colour behaviour for text output.
type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorAlways
	ColorNever
)

// Env looks up an environment variable. Injected rather than read directly so
// configuration is testable without mutating the process environment.
type Env func(string) (string, bool)

// Options fully describes a logger. Writers default to the process streams.
type Options struct {
	Format Format
	Level  slog.Level
	Color  ColorMode
	Stdout io.Writer
	Stderr io.Writer
	Env    Env
}

// Resolve applies the precedence flag > environment > default. An empty flag
// string means the flag was not given.
//
// On rejection, Resolve returns whatever it had already resolved, not a zero
// Options — a caller (cmd/root.go's PersistentPreRunE) uses that partial
// result to render the rejection itself, rather than falling back to
// FormatText and silently overriding a valid --log-format=jsonl sitting next
// to the flag that was actually wrong. That is also why format is resolved
// before level and colour: whichever of the three is bad, the fields checked
// earlier are already settled on the returned Options. When format itself is
// the rejected value, there is no honest way to know what the caller wanted —
// the field that would say so is exactly the one that failed to parse — so
// the returned Options simply keeps FormatText, its zero value, rather than
// guess.
func Resolve(flagFormat, flagLevel, flagColor string, env Env) (Options, error) {
	o := Options{Format: FormatText, Level: slog.LevelInfo, Color: ColorAuto, Env: env}

	if s, origin := pick(flagFormat, "RDK_LOG_FORMAT", env); s != "" {
		switch s {
		case "text":
			o.Format = FormatText
		case "jsonl":
			o.Format = FormatJSONL
		default:
			return o, diag.New(diag.Diagnostic{
				Code:    diag.CodeInvalidFlag,
				Summary: fmt.Sprintf("unknown log format %q%s", s, origin),
				Hint:    "valid formats: text, jsonl",
			})
		}
	}

	if s, origin := pick(flagLevel, "RDK_LOG_LEVEL", env); s != "" {
		switch s {
		case "debug":
			o.Level = slog.LevelDebug
		case "info":
			o.Level = slog.LevelInfo
		case "warn":
			o.Level = slog.LevelWarn
		case "error":
			o.Level = slog.LevelError
		default:
			return o, diag.New(diag.Diagnostic{
				Code:    diag.CodeInvalidFlag,
				Summary: fmt.Sprintf("unknown log level %q%s", s, origin),
				Hint:    "valid levels: debug, info, warn, error",
			})
		}
	}

	switch flagColor {
	case "":
		// leave the default
	case "auto":
		o.Color = ColorAuto
	case "always":
		o.Color = ColorAlways
	case "never":
		o.Color = ColorNever
	default:
		return o, diag.New(diag.Diagnostic{
			Code:    diag.CodeInvalidFlag,
			Summary: fmt.Sprintf("unknown colour mode %q", flagColor),
			Hint:    "valid colour modes: auto, always, never",
		})
	}

	return o, nil
}

// pick returns the flag value, else the environment value, else "" — and where
// it came from, because a bad value from a stale environment variable is
// otherwise indistinguishable from a typo on the command line the user is
// looking at.
func pick(flag, envKey string, env Env) (value, origin string) {
	if flag != "" {
		return flag, ""
	}
	if env != nil {
		if v, ok := env(envKey); ok {
			return v, " from " + envKey
		}
	}
	return "", ""
}

// useColor decides colour for one stream. NO_COLOR and TERM=dumb win over an
// explicit --color=always: both are the environment reporting that escape
// codes will not render, which is a fact rather than a preference.
func useColor(mode ColorMode, w io.Writer, env Env) bool {
	if mode == ColorNever {
		return false
	}
	if env != nil {
		if v, ok := env("NO_COLOR"); ok && v != "" {
			return false
		}
		if v, _ := env("TERM"); v == "dumb" {
			return false
		}
	}
	if mode == ColorAlways {
		return true
	}
	f, isFile := w.(*os.File)
	if !isFile {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
