// errorcheck

// correctness::layout_constant_mismatch (M9-S9): a constant marked
// //logos:sizeof, //logos:offsetof, or //logos:alignof must equal the
// compiler's value for the named type or field. The diagnostic states the
// constant and the actual value. Values below hold for 64-bit targets.

package p

type payload struct {
	a int32
	b int64
	c string
}

type head struct {
	first int8
	b     int8
}

//logos:sizeof int64
const sizeInt64 = 8

//logos:alignof int64
const alignInt64 = 8

//logos:sizeof head
const sizeHead = 2

//logos:offsetof head.b
const offHeadB = 1

//logos:sizeof int8
const sizeInt8 = 1

//logos:sizeof string
const sizeString = 16

//logos:offsetof payload.b
const offPayloadB = 8

//logos:sizeof payload
const sizePayload = 32

//logos:alignof payload
const alignPayload = 8

//logos:sizeof int64
const wrongSize = 4 // ERROR "\[correctness::layout_constant_mismatch\]"

//logos:offsetof payload.b
const wrongOff = 4 // ERROR "\[correctness::layout_constant_mismatch\]"

//logos:alignof payload
const wrongAlign = 4 // ERROR "\[correctness::layout_constant_mismatch\]"

//logos:sizeof nosuchtype
const missing = 0 // ERROR "\[correctness::layout_constant_mismatch\]"

//logos:sizeof payload.nofield
const missingField = 0 // ERROR "\[correctness::layout_constant_mismatch\]"

//logos:sizeof int64
var notConst = 8 // ERROR "\[correctness::layout_constant_mismatch\]"
