// asmcheck

package main

func scrub(key []byte) int {
	n := len(key)
	// amd64:"CALL.*memclrNoHeapPointers"
	// arm64:"CALL.*memclrNoHeapPointers"
	clear(key)
	return n
}

func main() {}
