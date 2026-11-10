// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build amd64 || arm64 || 386 || ppc64le || s390x

package rt

import (
	"runtime"
	"testing"
	"weak"
)

func pinnedViewas() (*viewasHeader, weak.Pointer[[16]byte]) {
	backing := new([16]byte)
	for i := range backing {
		backing[i] = byte(i + 17)
	}
	return ViewAs[viewasHeader](backing[:]), weak.Make(backing)
}

func TestViewAsKeepsBackingAlive(t *testing.T) {
	v, w := pinnedViewas()
	for range 20 {
		runtime.GC()
		if w.Value() == nil {
			t.Fatalf("backing collected while view live")
		}
	}
	if v.Magic != 336794129 || v.Version != 404166165 || v.Size != 2314601843866147353 {
		t.Fatalf("view after GC = %+v", *v)
	}
	runtime.KeepAlive(v)
	v = nil
	for range 200 {
		runtime.GC()
		if w.Value() == nil {
			return
		}
	}
	t.Fatalf("backing never collected after view dropped")
}
