// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosconc && !logosejected

package rt

import (
	"context"
	"unsafe"

	"logos/internal/conc"
)

// The monitor of logos/internal/conc knows g by its address from
// monitorCreated to monitorJoined.

func monitorCreated(g *Group) {
	conc.GroupCreated(unsafe.Pointer(g))
}

func monitorCancelled(g *Group) {
	conc.GroupCancelled(unsafe.Pointer(g))
}

func monitorJoined(g *Group) {
	conc.GroupJoined(unsafe.Pointer(g))
}

// monitored returns f with the reports that tell the monitor which
// goroutine runs f, and from which statement Go or GoNested started it.
func monitored(g *Group, f func(context.Context) error) func(context.Context) error {
	site := conc.SpawnSite()
	return func(ctx context.Context) error {
		conc.ChildStarted(unsafe.Pointer(g), site)
		defer conc.ChildDone(unsafe.Pointer(g))
		return f(ctx)
	}
}
