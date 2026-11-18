// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosconc && !logosejected

package rt

import (
	"context"

	"logos/internal/conc"
)

// monitorState is the record that logos/internal/conc keeps of a group.
type monitorState struct {
	record *conc.Group
}

func monitorCreated(g *Group) {
	g.monitor.record = conc.NewGroup()
}

func monitorCancelled(g *Group) {
	g.monitor.record.Cancelled()
}

func monitorJoined(g *Group) {
	g.monitor.record.Joined()
}

// monitored returns f with the reports that tell the monitor which
// goroutine runs f, and from which statement Go or GoNested started it.
func monitored(g *Group, f func(context.Context) error) func(context.Context) error {
	site := conc.SpawnSite()
	record := g.monitor.record
	return func(ctx context.Context) error {
		record.ChildStarted(site)
		defer record.ChildDone()
		return f(ctx)
	}
}
