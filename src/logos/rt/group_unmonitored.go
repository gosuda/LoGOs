// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosconc

package rt

import "context"

func monitorCreated(*Group) {}

func monitorCancelled(*Group) {}

func monitorJoined(*Group) {}

func monitored(_ *Group, f func(context.Context) error) func(context.Context) error {
	return f
}
