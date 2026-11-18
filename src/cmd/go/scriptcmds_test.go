// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"cmd/go/internal/work"
	"cmd/internal/script"
	"cmd/internal/script/scripttest"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func scriptCommands(interrupt os.Signal, waitDelay time.Duration) map[string]script.Cmd {
	cmds := scripttest.DefaultCmds()

	// Customize the "exec" interrupt signal and grace period.
	var cancel func(cmd *exec.Cmd) error
	if interrupt != nil {
		cancel = func(cmd *exec.Cmd) error {
			return cmd.Process.Signal(interrupt)
		}
	}

	cmdExec := script.Exec(cancel, waitDelay)
	cmds["exec"] = cmdExec

	add := func(name string, cmd script.Cmd) {
		if _, ok := cmds[name]; ok {
			panic(fmt.Sprintf("command %q is already registered", name))
		}
		cmds[name] = cmd
	}

	add("cc", scriptCC(cmdExec))
	add("find", scriptFind())
	cmdGo := scriptGo(cancel, waitDelay)
	add("go", cmdGo)
	add("logos", scriptGoNamed(testLogos, cancel, waitDelay))
	add("stale", scriptStale(cmdGo))

	return cmds
}

// scriptFind implements a portable subset of find(1) for script tests:
//
//	find dir [ ( ] -name glob [ -o -name glob ... ] [ ) ]
//
// It prints each matching path, relative to the working directory, on
// its own stdout line. Native Windows has no find(1) on PATH (its find
// searches file contents), so script tests cannot exec it portably.
func scriptFind() script.Cmd {
	return script.Command(
		script.CmdUsage{
			Summary: "list files matching -name globs under a directory",
			Args:    "dir [ ( ] -name 'glob' [ -o -name 'glob' ... ] [ ) ]",
		},
		func(s *script.State, args ...string) (script.WaitFunc, error) {
			if len(args) < 3 {
				return nil, script.ErrUsage
			}
			root := s.Path(args[0])
			var pats []string
			for i := 1; i < len(args); i++ {
				if args[i] == "-name" && i+1 < len(args) {
					pats = append(pats, args[i+1])
					i++
				}
			}
			if len(pats) == 0 {
				return nil, script.ErrUsage
			}
			var matches []string
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				for _, pat := range pats {
					if ok, _ := path.Match(pat, d.Name()); ok {
						rel, err := filepath.Rel(s.Getwd(), p)
						if err != nil {
							return err
						}
						matches = append(matches, filepath.ToSlash(rel))
						break
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			return func(*script.State) (stdout, stderr string, err error) {
				stdout = strings.Join(matches, "\n")
				if stdout != "" {
					stdout += "\n"
				}
				return stdout, "", nil
			}, nil
		})
}

// scriptCC runs the C compiler along with platform specific options.
func scriptCC(cmdExec script.Cmd) script.Cmd {
	return script.Command(
		script.CmdUsage{
			Summary: "run the platform C compiler",
			Args:    "args...",
		},
		func(s *script.State, args ...string) (script.WaitFunc, error) {
			fakeVendorDirProvider := func() string { return "" }
			b := work.NewBuilder(s.Getwd(), fakeVendorDirProvider)
			wait, err := cmdExec.Run(s, append(b.GccCmd(".", ""), args...)...)
			if err != nil {
				return wait, err
			}
			waitAndClean := func(s *script.State) (stdout, stderr string, err error) {
				stdout, stderr, err = wait(s)
				if closeErr := b.Close(); err == nil {
					err = closeErr
				}
				return stdout, stderr, err
			}
			return waitAndClean, nil
		})
}

var scriptGoInvoked sync.Map // testing.TB → go command was invoked

// scriptGo runs the go command.
func scriptGo(cancel func(*exec.Cmd) error, waitDelay time.Duration) script.Cmd {
	return scriptGoNamed(testGo, cancel, waitDelay)
}

// scriptStale checks that the named build targets are stale.
func scriptStale(cmdGo script.Cmd) script.Cmd {
	return script.Command(
		script.CmdUsage{
			Summary: "check that build targets are stale",
			Args:    "target...",
		},
		func(s *script.State, args ...string) (script.WaitFunc, error) {
			if len(args) == 0 {
				return nil, script.ErrUsage
			}
			tmpl := "{{if .Error}}{{.ImportPath}}: {{.Error.Err}}" +
				"{{else}}{{if not .Stale}}{{.ImportPath}} ({{.Target}}) is not stale{{end}}" +
				"{{end}}"

			wait, err := cmdGo.Run(s, append([]string{"list", "-e", "-f=" + tmpl}, args...)...)
			if err != nil {
				return nil, err
			}

			stdout, stderr, err := wait(s)
			if len(stderr) != 0 {
				s.Logf("%s", stderr)
			}
			if err != nil {
				return nil, err
			}
			if out := strings.TrimSpace(stdout); out != "" {
				return nil, errors.New(out)
			}
			return nil, nil
		})
}
