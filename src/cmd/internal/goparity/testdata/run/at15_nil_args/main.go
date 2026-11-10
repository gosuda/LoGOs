// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"example.com/parity/at15_nil_args/dep"
)

type T struct {
	V int
}

func asErr() error {
	return errors.New("x")
}

func main() {
	bad := false
	emit := func(name string, got string, want string) {
		fmt.Printf("%s %s\n", name, got)
		if got != want {
			fmt.Printf("LAW %s got %s want %s\n", name, got, want)
			bad = true
		}
	}
	var v any
	err := json.Unmarshal([]byte(`{"a":1}`), nil)
	emit("json", fmt.Sprintf("%T|%v", err, err), "*json.InvalidUnmarshalError|json: Unmarshal(nil)")
	func() {
		defer func() {
			r := recover()
			emit("errors", fmt.Sprintf("%v", r), "errors: target cannot be nil")
		}()
		_ = errors.As(asErr(), nil)
		emit("errors", "no panic", "errors: target cannot be nil")
	}()
	var t *T
	emit("fmt", fmt.Sprint(t), "<nil>")
	emit("bytes", fmt.Sprint(bytes.NewBuffer(nil).Len()), "0")
	var m map[string]int
	emit("mapread", fmt.Sprint(m["k"]), "0")
	emit("depmap", fmt.Sprint(dep.FirstNil(nil)), "-1")
	emit("depptr", fmt.Sprint(dep.DerefNil(nil)), "-1")
	emit("depslice", fmt.Sprint(dep.LenNil(nil)), "-1")
	var b *dep.Box
	emit("depbox", fmt.Sprint(b.Get()), "-1")
	_ = v
	if bad {
		os.Exit(1)
	}
}
