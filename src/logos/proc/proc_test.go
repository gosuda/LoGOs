// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proc_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	logosproc "logos/proc"
)

func TestMain(m *testing.M) {
	behavior := os.Getenv("LOGOS_PROC_CHILD")
	if behavior != "" {
		depth := 0
		if v := os.Getenv("LOGOS_PROC_DEPTH"); v != "" {
			n, err := strconv.Atoi(v)
			if err == nil {
				depth = n
			} else {
				depth = 2
			}
		}
		if depth > 1 {
			os.Exit(2)
		}
		runChild(behavior)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func runChild(behavior string) {
	switch behavior {
	case "exit0":
		os.Exit(0)
	case "exit3":
		os.Exit(3)
	case "block":
		for {
			time.Sleep(time.Hour)
		}
	case "stdout":
		_, _ = fmt.Fprint(os.Stdout, "hello-stdout")
		os.Exit(0)
	case "stderr":
		_, _ = fmt.Fprint(os.Stderr, "hello-stderr")
		os.Exit(0)
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprint(os.Stdout, wd)
		os.Exit(0)
	case "env":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv("LOGOS_PROC_WANT"))
		os.Exit(0)
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func currentDepth() int {
	v := os.Getenv("LOGOS_PROC_DEPTH")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 2
	}
	return n
}

func childEnv(behavior string, base []string, inherit bool) []string {
	var env []string
	if inherit && base == nil {
		env = os.Environ()
	} else if base != nil {
		env = append([]string{}, base...)
	}
	env = append(env, "LOGOS_PROC_CHILD="+behavior)
	env = append(env, "LOGOS_PROC_DEPTH=1")
	return env
}

func spawnSelf(t *testing.T, behavior string, opts logosproc.Options) *logosproc.Child {
	t.Helper()
	if currentDepth() >= 1 {
		t.Fatal("depth limit reached")
	}
	inherit := opts.Env == nil
	opts.Env = childEnv(behavior, opts.Env, inherit)
	c, err := logosproc.Spawn(os.Args[0], nil, opts)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return c
}

func waitDeadline(t *testing.T, c *logosproc.Child) (logosproc.Term, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Wait(ctx)
}

func defaultOpts() logosproc.Options {
	return logosproc.Options{
		Stdin:  logosproc.InheritStdIO(),
		Stdout: logosproc.InheritStdIO(),
		Stderr: logosproc.InheritStdIO(),
	}
}

func TestSpawnExitCode(t *testing.T) {
	cases := []struct {
		behavior string
		code     int
		success  bool
	}{
		{"exit0", 0, true},
		{"exit3", 3, false},
	}
	for _, tc := range cases {
		c := spawnSelf(t, tc.behavior, defaultOpts())
		term, err := waitDeadline(t, c)
		if err != nil {
			t.Fatalf("%s wait: %v", tc.behavior, err)
		}
		if term.Kind != logosproc.TermExited {
			t.Fatalf("%s kind = %v, want exited", tc.behavior, term.Kind)
		}
		if term.Code != tc.code {
			t.Fatalf("%s code = %d, want %d", tc.behavior, term.Code, tc.code)
		}
		if term.IsSuccess() != tc.success {
			t.Fatalf("%s success = %v, want %v", tc.behavior, term.IsSuccess(), tc.success)
		}
		oracle := exec.Command(os.Args[0])
		oracle.Env = childEnv(tc.behavior, nil, true)
		oracleErr := oracle.Run()
		oracleCode := 0
		if oracleErr != nil {
			if exitErr, ok := oracleErr.(*exec.ExitError); ok {
				oracleCode = exitErr.ExitCode()
			} else {
				t.Fatalf("%s oracle: %v", tc.behavior, oracleErr)
			}
		}
		if oracleCode != tc.code {
			t.Fatalf("%s oracle code = %d, want %d", tc.behavior, oracleCode, tc.code)
		}
	}
}

func TestSpawnMissingPath(t *testing.T) {
	_, err := logosproc.Spawn("logos-proc-no-such-binary-xyz", nil, defaultOpts())
	if err == nil {
		t.Fatal("spawn missing path succeeded, want error")
	}
}

func TestWaitIdempotent(t *testing.T) {
	c := spawnSelf(t, "exit0", defaultOpts())
	first, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("first wait: %v", err)
	}
	second, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if first != second {
		t.Fatalf("wait not idempotent: %+v vs %+v", first, second)
	}
}

func TestKillIdempotent(t *testing.T) {
	c := spawnSelf(t, "block", defaultOpts())
	if err := c.Kill(); err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if err := c.Kill(); err != nil {
		t.Fatalf("second kill: %v", err)
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if term.Kind != logosproc.TermSignaled {
		t.Fatalf("kind = %v, want signaled", term.Kind)
	}
}

func TestKillAfterWait(t *testing.T) {
	c := spawnSelf(t, "exit0", defaultOpts())
	if _, err := waitDeadline(t, c); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := c.Kill(); err != nil {
		t.Fatalf("kill after wait: %v", err)
	}
	if err := c.KillGroup(); err != nil {
		t.Fatalf("killgroup after wait: %v", err)
	}
}

func TestKillGroup(t *testing.T) {
	opts := defaultOpts()
	opts.NewProcessGroup = true
	c := spawnSelf(t, "block", opts)
	if err := c.KillGroup(); err != nil {
		t.Fatalf("killgroup: %v", err)
	}
	if err := c.KillGroup(); err != nil {
		t.Fatalf("second killgroup: %v", err)
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if term.Kind != logosproc.TermSignaled {
		t.Fatalf("kind = %v, want signaled", term.Kind)
	}
}

func TestTermMatch(t *testing.T) {
	cases := []logosproc.Term{
		{Kind: logosproc.TermExited, Code: 3},
		{Kind: logosproc.TermSignaled, Signal: syscall.SIGKILL},
		{Kind: logosproc.TermStopped, Signal: syscall.SIGSTOP},
		{Kind: logosproc.TermUnknown, Code: 7},
	}
	for _, term := range cases {
		called := ""
		term.Match(
			func(code int) {
				called = fmt.Sprintf("exited:%d", code)
			},
			func(sig os.Signal) {
				called = fmt.Sprintf("signaled:%s", sig)
			},
			func(sig os.Signal) {
				called = fmt.Sprintf("stopped:%s", sig)
			},
			func(code int) {
				called = fmt.Sprintf("unknown:%d", code)
			},
		)
		want := ""
		switch term.Kind {
		case logosproc.TermExited:
			want = fmt.Sprintf("exited:%d", term.Code)
		case logosproc.TermSignaled:
			want = fmt.Sprintf("signaled:%s", term.Signal)
		case logosproc.TermStopped:
			want = fmt.Sprintf("stopped:%s", term.Signal)
		default:
			want = fmt.Sprintf("unknown:%d", term.Code)
		}
		if called != want {
			t.Fatalf("match = %q, want %q", called, want)
		}
	}
}

func TestTermMatchPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("match with nil handler did not panic")
		}
	}()
	term := logosproc.Term{Kind: logosproc.TermExited, Code: 0}
	term.Match(nil, func(os.Signal) {}, func(os.Signal) {}, func(int) {})
}

func TestTermString(t *testing.T) {
	cases := []struct {
		term logosproc.Term
		want string
	}{
		{logosproc.Term{Kind: logosproc.TermExited, Code: 0}, "exited with code 0"},
		{logosproc.Term{Kind: logosproc.TermExited, Code: 3}, "exited with code 3"},
		{logosproc.Term{Kind: logosproc.TermSignaled, Signal: syscall.SIGKILL}, "terminated with signal killed"},
		{logosproc.Term{Kind: logosproc.TermUnknown}, "terminated unexpectedly"},
	}
	for _, tc := range cases {
		if got := tc.term.String(); got != tc.want {
			t.Fatalf("string = %q, want %q", got, tc.want)
		}
	}
}

func TestIsSuccess(t *testing.T) {
	if !((logosproc.Term{Kind: logosproc.TermExited, Code: 0}).IsSuccess()) {
		t.Fatal("exited 0 not success")
	}
	if (logosproc.Term{Kind: logosproc.TermExited, Code: 1}).IsSuccess() {
		t.Fatal("exited 1 success, want false")
	}
	if (logosproc.Term{Kind: logosproc.TermSignaled, Signal: syscall.SIGKILL}).IsSuccess() {
		t.Fatal("signaled success, want false")
	}
	if (logosproc.Term{Kind: logosproc.TermUnknown}).IsSuccess() {
		t.Fatal("unknown success, want false")
	}
}

func TestPipeStdout(t *testing.T) {
	opts := defaultOpts()
	opts.Stdout = logosproc.PipeStdIO()
	c := spawnSelf(t, "stdout", opts)
	pipe := c.StdoutPipe()
	if pipe == nil {
		t.Fatal("stdout pipe nil")
	}
	out, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
	if string(out) != "hello-stdout" {
		t.Fatalf("pipe output = %q, want %q", out, "hello-stdout")
	}
}

func TestPipeStdin(t *testing.T) {
	opts := defaultOpts()
	opts.Stdin = logosproc.PipeStdIO()
	opts.Stdout = logosproc.PipeStdIO()
	c := spawnSelf(t, "stdin", opts)
	w := c.StdinPipe()
	if w == nil {
		t.Fatal("stdin pipe nil")
	}
	r := c.StdoutPipe()
	if r == nil {
		t.Fatal("stdout pipe nil")
	}
	if _, err := io.WriteString(w, "echo-me"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
	if string(out) != "echo-me" {
		t.Fatalf("echo = %q, want %q", out, "echo-me")
	}
}

func TestFileStdIO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	opts := defaultOpts()
	opts.Stdout = logosproc.FileStdIO(f)
	c := spawnSelf(t, "stdout", opts)
	term, err := waitDeadline(t, c)
	_ = f.Close()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello-stdout" {
		t.Fatalf("file = %q, want %q", data, "hello-stdout")
	}
}

func TestNullStdIO(t *testing.T) {
	opts := defaultOpts()
	opts.Stdout = logosproc.NullStdIO()
	opts.Stderr = logosproc.NullStdIO()
	c := spawnSelf(t, "stdout", opts)
	if c.StdoutPipe() != nil {
		t.Fatal("null stdout pipe non-nil")
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
}

func TestCloseStdIO(t *testing.T) {
	opts := defaultOpts()
	opts.Stdout = logosproc.CloseStdIO()
	c := spawnSelf(t, "exit0", opts)
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
}

func TestCwdPath(t *testing.T) {
	dir := t.TempDir()
	opts := defaultOpts()
	opts.Stdout = logosproc.PipeStdIO()
	opts.Cwd = logosproc.PathCwd(dir)
	c := spawnSelf(t, "pwd", opts)
	pipe := c.StdoutPipe()
	if pipe == nil {
		t.Fatal("stdout pipe nil")
	}
	out, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := waitDeadline(t, c); err != nil {
		t.Fatalf("wait: %v", err)
	}
	got, err := filepath.EvalSymlinks(string(out))
	if err != nil {
		t.Fatalf("eval child wd: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval dir: %v", err)
	}
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func TestEnvReplace(t *testing.T) {
	opts := defaultOpts()
	opts.Stdout = logosproc.PipeStdIO()
	opts.Env = []string{"LOGOS_PROC_WANT=replaced-value", "PATH=" + os.Getenv("PATH")}
	c := spawnSelf(t, "env", opts)
	pipe := c.StdoutPipe()
	if pipe == nil {
		t.Fatal("stdout pipe nil")
	}
	out, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := waitDeadline(t, c); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if string(out) != "replaced-value" {
		t.Fatalf("env = %q, want %q", out, "replaced-value")
	}
}

func TestEnvInherit(t *testing.T) {
	t.Setenv("LOGOS_PROC_WANT", "inherited-value")
	opts := defaultOpts()
	opts.Stdout = logosproc.PipeStdIO()
	c := spawnSelf(t, "env", opts)
	pipe := c.StdoutPipe()
	if pipe == nil {
		t.Fatal("stdout pipe nil")
	}
	out, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := waitDeadline(t, c); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if string(out) != "inherited-value" {
		t.Fatalf("env = %q, want %q", out, "inherited-value")
	}
}

func TestContextDeadline(t *testing.T) {
	c := spawnSelf(t, "block", defaultOpts())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	term, err := c.Wait(ctx)
	if err == nil {
		t.Fatalf("wait succeeded, want deadline: %+v", term)
	}
}

func TestZeroOptions(t *testing.T) {
	var opts logosproc.Options
	opts.Env = childEnv("exit0", nil, true)
	c, err := logosproc.Spawn(os.Args[0], nil, opts)
	if err != nil {
		t.Fatalf("spawn zero opts: %v", err)
	}
	term, err := waitDeadline(t, c)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !term.IsSuccess() {
		t.Fatalf("term = %+v, want success", term)
	}
}

func TestWaitNilContext(t *testing.T) {
	c := spawnSelf(t, "exit0", defaultOpts())
	if _, err := c.Wait(nil); err == nil {
		t.Fatal("wait nil context succeeded, want error")
	} else {
		_ = c.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.Wait(ctx)
	}
}

func TestFileStdIONil(t *testing.T) {
	opts := defaultOpts()
	opts.Stdout = logosproc.FileStdIO(nil)
	if _, err := logosproc.Spawn(os.Args[0], nil, opts); err == nil {
		t.Fatal("spawn nil file succeeded, want error")
	}
}
