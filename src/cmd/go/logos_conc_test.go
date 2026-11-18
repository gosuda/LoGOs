// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package main_test

import (
	"bufio"
	"bytes"
	"errors"
	"internal/testenv"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const logosConcWaitingProgram = `package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"logos/sync"
)

type account struct{ balance int }

var (
	checking sync.Locked[account]
	savings  sync.Locked[account]
)

func save() error {
	checking.WithLock(from => {
		savings.WithLock(to => {
			from.balance--
			to.balance++
		})
	})
	return nil
}

func spend() error {
	savings.WithLock(from => {
		checking.WithLock(to => {
			from.balance--
			to.balance++
		})
	})
	return nil
}

func transfers(ctx context.Context) error {
	go group (ctx) {
		go save()
	}
	go group (ctx) {
		go spend()
	}
	return nil
}

func main() {
	if err := transfers(context.Background()); err != nil {
		os.Exit(2)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(2)
	}
	fmt.Println("released")
}
`

// TestLogosConcPrintsAFindingWhileTheProgramRuns holds the standard
// input of the program open. The program blocks on it after the lock
// order inversion, so a finding line that the test reads before it
// closes that input came from a program that had not exited.
func TestLogosConcPrintsAFindingWhileTheProgramRuns(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir := logosJSONFixture(t, map[string]string{
		"go.mod":   "module example.com/waiting\n\ngo 1.27\n",
		"main.lgo": logosConcWaitingProgram,
	})
	cmd := exec.Command(testLogos, "conc", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var gaveUp atomic.Bool
	watchdog := time.AfterFunc(2*time.Minute, func() {
		gaveUp.Store(true)
		input.Close()
	})
	defer watchdog.Stop()

	lines := bufio.NewReader(diagnostics)
	var seen strings.Builder
	found := false
	for !found {
		line, err := lines.ReadString('\n')
		seen.WriteString(line)
		found = strings.Contains(line, "[conc::lock_order_inversion]")
		if err != nil {
			break
		}
	}
	if !found || gaveUp.Load() {
		input.Close()
		cmd.Wait()
		t.Fatalf("no finding arrived while the program waited for its input (gave up: %v):\n%s", gaveUp.Load(), seen.String())
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	rest, _ := lines.ReadString(0)
	err = cmd.Wait()
	exit, isExit := errors.AsType[*exec.ExitError](err)
	if !isExit || exit.ExitCode() != 1 {
		t.Fatalf("logos conc run: %v, want exit status 1\nstderr after the finding:\n%s", err, rest)
	}
	if got := output.String(); got != "released\n" {
		t.Fatalf("standard output = %q, want the line the program prints after its input closed", got)
	}
}
