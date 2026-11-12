// errorcheck

package p

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

var errSentinel = errors.New("boom")

var fail = false

func f() error {
	if fail {
		return errSentinel
	}
	return nil
}

func g() (int, error) {
	if fail {
		return 0, errSentinel
	}
	return 0, nil
}

func alwaysNil() error { return nil }

func alwaysNilPair() (int, error) { return 0, nil }

type nilT struct{}

func (nilT) M() error { return nil }

func innerNil() error { return nil }

func outerNil() error { return innerNil() }

func outerNilPair() (int, error) { return 0, innerNil() }

func namedNilOK() (err error) {
	err = nil
	return
}

func mixedNil(b bool) error {
	if b {
		return errSentinel
	}
	return nil
}

func namedNilBad() (err error) {
	err = errSentinel
	return
}

type nilIface interface {
	M() error
}

func dropped() {
	f()         // ERROR "discarded_error"
	g()         // ERROR "discarded_error"
	_ = f()     // ERROR "discarded_error"
	_, _ = g()  // ERROR "discarded_error"
	n, _ := g() // ERROR "discarded_error"
	_ = n
	h := func() error { return f() }
	h()       // ERROR "discarded_error"
	_ = (f()) // ERROR "discarded_error"
}

func droppedAllowed() {
	alwaysNil()
	alwaysNilPair()
	nilT{}.M()
	innerNil()
	outerNil()
	outerNilPair()
	namedNilOK()
	_ = alwaysNil()
	_, _ = alwaysNilPair()
	n, _ := alwaysNilPair()
	_ = n
}

func droppedReported(b bool, e nilIface) {
	mixedNil(b)   // ERROR "discarded_error"
	namedNilBad() // ERROR "discarded_error"
	e.M()         // ERROR "discarded_error"
}

func printed() {
	fmt.Print("x")
	fmt.Printf("%d", 1)
	fmt.Println("x")
	fmt.Fprintf(os.Stderr, "x")
	fmt.Fprintln(os.Stdout, "x")
}

func written() {
	var b bytes.Buffer
	b.WriteString("x")
	b.WriteByte('x')
	var sb strings.Builder
	sb.WriteString("x")
	sb.WriteByte('x')
}

func printedToBuffer() {
	var b bytes.Buffer
	fmt.Fprintf(&b, "x") // ERROR "discarded_error"
	var sb strings.Builder
	fmt.Fprintf(&sb, "x") // ERROR "discarded_error"
}

func handled() {
	err := f()
	_ = err
	defer f() // ERROR "discarded_error"
	m, err := g()
	_, _ = m, err
}

func spawned() {
	go f()    // ERROR "discarded_error"
	defer g() // ERROR "discarded_error"
}

func inErrorFunc() error {
	f() // ERROR "discarded_error"
	return nil
}

//logos:allow(slop::discarded_error)
func allowed() {
	f()
}
