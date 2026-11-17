// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types_test

import "bytes"

// goTypesPanicSource drops the loGo/s void-panic hook when generating
// go/types. Compiler types2 calls logosRecordVoidPanic after the upstream
// guard so option narrowing sees panic calls in functions without results;
// public go/types has neither that mode nor its flag. The exact source shape
// must occur once so a future checker edit fails closed.
func goTypesPanicSource(src []byte) []byte {
	const logos = `			p[call] = true
		}
		check.logosRecordVoidPanic(call)
`
	const upstream = `			p[call] = true
		}
`
	if bytes.Count(src, []byte(logos)) != 1 {
		panic("go/types generation: expected exactly one loGo/s panic registration guard")
	}
	return bytes.Replace(src, []byte(logos), []byte(upstream), 1)
}
