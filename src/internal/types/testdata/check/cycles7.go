// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package p

// A forwards to an instance of itself, so resolving the underlying of
// A[int] before the delayed recursive-type check runs must terminate
// and report the cycle instead of hanging. The initializer forces the
// early resolution: checking A[int]{} needs its underlying during
// packageObjects, while validType for A runs later in processDelayed.
type A[P any] A /* ERROR "invalid recursive type" */ [int]

var _ = A[int]{}
