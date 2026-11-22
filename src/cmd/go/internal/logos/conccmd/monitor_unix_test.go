// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package conccmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

func scanStderr(t *testing.T, input string) int {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer func() { _ = devNull.Close() }()
	old := os.Stderr
	os.Stderr = devNull
	defer func() { os.Stderr = old }()
	s := &exitScanWriter{}
	if _, err := io.Copy(s, strings.NewReader(input)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	s.finish()
	return s.status()
}

func TestExitScanBareLine(t *testing.T) {
	if got := scanStderr(t, "some noise\nexit status 3\n"); got != 3 {
		t.Fatalf("status = %d, want 3", got)
	}
}

func TestExitScanGluedToProgramOutput(t *testing.T) {
	// The program's last stderr byte sits right before the wrapper's
	// report, with no newline between them.
	if got := scanStderr(t, "operation failed:exit status 3\n"); got != 3 {
		t.Fatalf("status = %d, want 3", got)
	}
	if got := scanStderr(t, "operation failed:exit status 3"); got != 3 {
		t.Fatalf("unterminated glued status = %d, want 3", got)
	}
}

func TestExitScanRejectsDiagnostics(t *testing.T) {
	for _, input := range []string{
		"error obtaining VCS status: exit status 128\nexit status 3\n",
		"error obtaining VCS status: exit status 128\n",
	} {
		want := 3
		if !strings.Contains(input, "exit status 3") {
			want = 0
		}
		if got := scanStderr(t, input); got != want {
			t.Fatalf("%q: status = %d, want %d", input, got, want)
		}
	}
}

func TestExitScanFirstMatchWins(t *testing.T) {
	// A helper that inherited stderr reports its own status after the
	// program's; the first report is the program's.
	if got := scanStderr(t, "exit status 1\nexit status 9\n"); got != 1 {
		t.Fatalf("status = %d, want 1", got)
	}
	if got := scanStderr(t, "exit status 1\nhelper output\nexit status 9\n"); got != 1 {
		t.Fatalf("status = %d, want 1", got)
	}
}
