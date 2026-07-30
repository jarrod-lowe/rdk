// Package logger is the only way rdk writes to stdout or stderr. It renders
// diag.Diagnostic values in one of three modes — coloured text, plain text, or
// JSONL — over log/slog.
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
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
func Resolve(flagFormat, flagLevel, flagColor string, env Env) (Options, error) {
	o := Options{Format: FormatText, Level: slog.LevelInfo, Color: ColorAuto, Env: env}

	if s := pick(flagFormat, "RDK_LOG_FORMAT", env); s != "" {
		switch s {
		case "text":
			o.Format = FormatText
		case "jsonl":
			o.Format = FormatJSONL
		default:
			return Options{}, fmt.Errorf("unknown log format %q (valid: text, jsonl)", s)
		}
	}

	if s := pick(flagLevel, "RDK_LOG_LEVEL", env); s != "" {
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
			return Options{}, fmt.Errorf("unknown log level %q (valid: debug, info, warn, error)", s)
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
		return Options{}, fmt.Errorf("unknown colour mode %q (valid: auto, always, never)", flagColor)
	}

	return o, nil
}

// pick returns the flag value, else the environment value, else "".
func pick(flag, envKey string, env Env) string {
	if flag != "" {
		return flag
	}
	if env != nil {
		if v, ok := env(envKey); ok {
			return v
		}
	}
	return ""
}

// useColor decides colour for one stream. NO_COLOR and TERM=dumb win over an
// explicit --color=always: both are the environment reporting that escape
// codes will not render, which is a fact rather than a preference.
func useColor(mode ColorMode, w io.Writer, env Env) bool {
	if mode == ColorNever {
		return false
	}
	if env != nil {
		if _, ok := env("NO_COLOR"); ok {
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
