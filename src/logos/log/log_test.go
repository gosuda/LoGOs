// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	logoslog "logos/log"
)

func TestLevelString(t *testing.T) {
	cases := []struct {
		level logoslog.Level
		want  string
	}{
		{logoslog.Error, "error"},
		{logoslog.Warn, "warning"},
		{logoslog.Info, "info"},
		{logoslog.Debug, "debug"},
		{logoslog.Level(7), "unknown"},
	}
	for _, c := range cases {
		if got := c.level.String(); got != c.want {
			t.Errorf("Level(%d).String() = %q, want %q", int(c.level), got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		want  logoslog.Level
		valid bool
	}{
		{"error", logoslog.Error, true},
		{"warning", logoslog.Warn, true},
		{"info", logoslog.Info, true},
		{"debug", logoslog.Debug, true},
		{"", logoslog.Error, false},
		{"warn", logoslog.Error, false},
		{"WARN", logoslog.Error, false},
		{"verbose", logoslog.Error, false},
	}
	for _, c := range cases {
		got, err := logoslog.Parse(c.in)
		if c.valid {
			if err != nil {
				t.Errorf("Parse(%q) error = %v, want nil", c.in, err)
				continue
			}
			if got != c.want {
				t.Errorf("Parse(%q) = %v, want %v", c.in, got, c.want)
			}
			back, backErr := logoslog.Parse(got.String())
			if backErr != nil || back != got {
				t.Errorf("Parse(%q.String()) = %v, %v; want %v, nil", c.in, back, backErr, got)
			}
			continue
		}
		if err == nil {
			t.Errorf("Parse(%q) = %v, want error", c.in, got)
			continue
		}
		if !strings.Contains(err.Error(), "log: ") || !strings.Contains(err.Error(), c.in) {
			t.Errorf("Parse(%q) error %q names neither package nor input", c.in, err)
		}
	}
}

func TestEnabledMatrix(t *testing.T) {
	levels := []logoslog.Level{logoslog.Error, logoslog.Warn, logoslog.Info, logoslog.Debug}
	for _, threshold := range levels {
		l := logoslog.Scoped("s", threshold)
		for _, msg := range levels {
			want := int(msg) <= int(threshold)
			if got := l.Enabled(msg); got != want {
				t.Errorf("threshold %v Enabled(%v) = %v, want %v", threshold, msg, got, want)
			}
		}
	}
}

func TestScopedDefaults(t *testing.T) {
	l := logoslog.Scoped("", logoslog.Info)
	if got := l.Scope(); got != logoslog.DefaultScope {
		t.Errorf("empty scope Scope() = %q, want %q", got, logoslog.DefaultScope)
	}
	if got := l.Level(); got != logoslog.Info {
		t.Errorf("Level() = %v, want %v", got, logoslog.Info)
	}
	named := logoslog.Scoped("catalog", logoslog.Debug)
	if got := named.Scope(); got != "catalog" {
		t.Errorf("Scope() = %q, want catalog", got)
	}
}

func TestOutputLines(t *testing.T) {
	cases := []struct {
		name string
		emit func(l *logoslog.Logger)
		want string
	}{
		{"error", func(l *logoslog.Logger) { l.Error("down") }, "[error] (s): down\n"},
		{"warn", func(l *logoslog.Logger) { l.Warn("slow") }, "[warning] (s): slow\n"},
		{"info", func(l *logoslog.Logger) { l.Info("up") }, "[info] (s): up\n"},
		{"debug-dropped", func(l *logoslog.Logger) { l.Debug("trace") }, ""},
		{"errorf", func(l *logoslog.Logger) { l.Errorf("code %d", 500) }, "[error] (s): code 500\n"},
		{"infof", func(l *logoslog.Logger) { l.Infof("bound %d items", 5) }, "[info] (s): bound 5 items\n"},
		{"log-info", func(l *logoslog.Logger) { l.Log(logoslog.Info, "plain") }, "[info] (s): plain\n"},
		{"logf-warn", func(l *logoslog.Logger) { l.Logf(logoslog.Warn, "n=%d", 3) }, "[warning] (s): n=3\n"},
		{"log-debug-dropped", func(l *logoslog.Logger) { l.Log(logoslog.Debug, "x") }, ""},
		{"logf-debug-dropped", func(l *logoslog.Logger) { l.Logf(logoslog.Debug, "x=%d", 1) }, ""},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		l := logoslog.Scoped("s", logoslog.Info).WithOutput(&buf)
		c.emit(l)
		if got := buf.String(); got != c.want {
			t.Errorf("%s output = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDefaultScopeRenders(t *testing.T) {
	var buf bytes.Buffer
	l := logoslog.Scoped("", logoslog.Debug).WithOutput(&buf)
	l.Debug("hello")
	if got, want := buf.String(), "[debug] (default): hello\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestWithOutputIsolates(t *testing.T) {
	var a, b bytes.Buffer
	base := logoslog.Scoped("s", logoslog.Debug)
	la := base.WithOutput(&a)
	lb := base.WithOutput(&b)
	la.Info("to-a")
	if a.String() != "[info] (s): to-a\n" {
		t.Errorf("a = %q, want one line", a.String())
	}
	if b.String() != "" {
		t.Errorf("b = %q, want empty", b.String())
	}
	lb.Info("to-b")
	if b.String() != "[info] (s): to-b\n" {
		t.Errorf("b = %q, want one line", b.String())
	}
}

func TestConcurrentWritesStayWhole(t *testing.T) {
	var buf bytes.Buffer
	l := logoslog.Scoped("s", logoslog.Debug).WithOutput(&buf)
	const writers = 8
	const per = 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range per {
				l.Infof("w%d i%d", w, i)
			}
		}(w)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != writers*per {
		t.Fatalf("got %d lines, want %d", len(lines), writers*per)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[info] (s): w") {
			t.Fatalf("torn line %q", line)
		}
	}
}

func TestDisabledPathAllocatesNothing(t *testing.T) {
	l := logoslog.Scoped("s", logoslog.Info)
	if n := testing.AllocsPerRun(100, func() { _ = l.Enabled(logoslog.Debug) }); n != 0 {
		t.Errorf("disabled Enabled allocates %v times per call", n)
	}
	if n := testing.AllocsPerRun(100, func() { l.Log(logoslog.Debug, "static") }); n != 0 {
		t.Errorf("disabled Log allocates %v times per call", n)
	}
	if n := testing.AllocsPerRun(100, func() { l.Logf(logoslog.Debug, "static") }); n != 0 {
		t.Errorf("disabled Logf without args allocates %v times per call", n)
	}
}

type reentrantString struct{ l *logoslog.Logger }

func (r reentrantString) String() string {
	r.l.Info("from-string")
	return "v"
}

func TestLogfReentrantString(t *testing.T) {
	var buf bytes.Buffer
	l := logoslog.Scoped("s", logoslog.Debug).WithOutput(&buf)
	done := make(chan string, 1)
	go func() {
		l.Infof("arg %s", reentrantString{l})
		done <- buf.String()
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, "from-string") || !strings.Contains(out, "arg v") {
			t.Errorf("reentrant Logf wrote %q, want both lines", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Logf deadlocked on a String method that logs")
	}
}
