// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosejected

package arena

import "internal/abi"

func fixedElementHasPointers[T any]() bool {
	return abi.TypeFor[T]().Pointers()
}
