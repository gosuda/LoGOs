// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package log provides scoped leveled logging with compile-time pruning.
//
// A Logger logs under one scope with one threshold level. A call passes
// when its level is at or below the threshold, the rule of Zig's
// logEnabled (lib/std/log.zig at revision
// c7826aa7a096270e4de8a1c7b8b4343650950f30, MIT licensed). Policy lives
// with the caller: each scope's gate is a const boolean in the calling
// package, and .lgo callers wrap cold calls in const if so the call and
// its argument work never reach lowering. The runtime gate in Log and
// Logf covers Go callers and ejected output.
//
// .lgo code imports it as logos/log; eject copies it into the shared
// support module github.com/gosuda/loGos/rt/log.
package log

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Level orders severities from Error (least verbose) to Debug (most
// verbose), matching Zig's Level declaration order.
type Level int

const (
	// Error marks a failure that may be recoverable or fatal.
	Error Level = iota
	// Warn marks a suspicious circumstance worth investigating.
	Warn
	// Info marks general state messages.
	Info
	// Debug marks messages only useful for debugging.
	Debug
)

// DefaultScope names loggers built with an empty scope, mirroring
// Zig's .default scope.
const DefaultScope = "default"

// String returns the full-text spelling of l: error, warning, info,
// or debug, matching Zig's Level.asText. Other values read as unknown.
func (l Level) String() string {
	switch l {
	case Error:
		return "error"
	case Warn:
		return "warning"
	case Info:
		return "info"
	case Debug:
		return "debug"
	default:
		return "unknown"
	}
}

// Parse returns the Level named by s, the inverse of String. An
// unknown name is an error naming the input.
func Parse(s string) (Level, error) {
	switch s {
	case "error":
		return Error, nil
	case "warning":
		return Warn, nil
	case "info":
		return Info, nil
	case "debug":
		return Debug, nil
	default:
		return Error, fmt.Errorf("log: unknown level %q", s)
	}
}

// Logger logs under one scope with one threshold level. Build loggers
// with Scoped. A Logger is safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	scope string
	level Level
}

// Scoped returns a Logger for scope that writes to standard error and
// logs messages at or below level. An empty scope reads as DefaultScope.
func Scoped(scope string, level Level) *Logger {
	if scope == "" {
		scope = DefaultScope
	}
	return &Logger{out: os.Stderr, scope: scope, level: level}
}

// WithOutput returns a copy of l that writes to w. A nil w discards.
func (l *Logger) WithOutput(w io.Writer) *Logger {
	if w == nil {
		w = io.Discard
	}
	return &Logger{out: w, scope: l.scope, level: l.level}
}

// Scope returns the scope name given to Scoped.
func (l *Logger) Scope() string {
	return l.scope
}

// Level returns the threshold level given to Scoped.
func (l *Logger) Level() Level {
	return l.level
}

// Enabled reports whether messages at m pass l's threshold.
func (l *Logger) Enabled(m Level) bool {
	return m <= l.level
}

// Log writes msg as one line when m passes the threshold.
func (l *Logger) Log(m Level, msg string) {
	if !l.Enabled(m) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.out, "[%s] (%s): %s\n", m, l.scope, msg)
}

// Logf writes the formatted message as one line when m passes. The
// arguments format before the writer lock is taken, so a String
// method that logs through the same logger cannot deadlock on it.
func (l *Logger) Logf(m Level, format string, args ...any) {
	if !l.Enabled(m) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.out, "[%s] (%s): %s\n", m, l.scope, msg)
}

// Debug writes msg when Debug passes the threshold.
func (l *Logger) Debug(msg string) {
	l.Log(Debug, msg)
}

// Debugf writes the formatted message when Debug passes.
func (l *Logger) Debugf(format string, args ...any) {
	l.Logf(Debug, format, args...)
}

// Info writes msg when Info passes the threshold.
func (l *Logger) Info(msg string) {
	l.Log(Info, msg)
}

// Infof writes the formatted message when Info passes.
func (l *Logger) Infof(format string, args ...any) {
	l.Logf(Info, format, args...)
}

// Warn writes msg when Warn passes the threshold.
func (l *Logger) Warn(msg string) {
	l.Log(Warn, msg)
}

// Warnf writes the formatted message when Warn passes.
func (l *Logger) Warnf(format string, args ...any) {
	l.Logf(Warn, format, args...)
}

// Error writes msg; Error always passes the threshold.
func (l *Logger) Error(msg string) {
	l.Log(Error, msg)
}

// Errorf writes the formatted message; Error always passes.
func (l *Logger) Errorf(format string, args ...any) {
	l.Logf(Error, format, args...)
}
