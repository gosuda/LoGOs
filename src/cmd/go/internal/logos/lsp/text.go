// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"sort"
	"unicode/utf8"

	"cmd/internal/logos/editmap"
)

// Position is an LSP position: a zero-based line and a character offset
// counted in UTF-16 code units. The proxy forces both of its peers onto
// this encoding, so it is the only one it converts.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range: Start is inclusive and End is exclusive.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// text is an immutable document text with a line index. Lines end at "\n";
// a "\r" before it belongs to the line.
type text struct {
	data   []byte
	starts []int
}

// newText indexes data. The text keeps data without copying it.
func newText(data []byte) *text {
	starts := []int{0}
	for i, c := range data {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &text{data: data, starts: starts}
}

// lineEnd returns the offset where the content of line ends.
func (t *text) lineEnd(line int) int {
	if line+1 < len(t.starts) {
		return t.starts[line+1] - 1
	}
	return len(t.data)
}

// offset converts an LSP position to a byte offset. A line past the end
// maps to the end of the text, and a character past the end of its line
// maps to the end of the line, as the protocol requires.
func (t *text) offset(p Position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(t.starts) {
		return len(t.data)
	}
	off, end := t.starts[p.Line], t.lineEnd(p.Line)
	for units := 0; off < end && units < p.Character; {
		r, size := utf8.DecodeRune(t.data[off:end])
		units += utf16Len(r)
		off += size
	}
	return off
}

// position converts a byte offset to an LSP position. An offset outside
// the text is clamped to it.
func (t *text) position(off int) Position {
	off = min(max(off, 0), len(t.data))
	line := sort.SearchInts(t.starts, off+1) - 1
	return Position{Line: line, Character: utf16Units(t.data[t.starts[line]:off])}
}

// lineCol converts a 1-based line and 1-based byte column, the position
// form of compiler diagnostics, to a byte offset. It reports false for a
// line or column outside the text.
func (t *text) lineCol(line, col int) (int, bool) {
	if line < 1 || line > len(t.starts) || col < 1 {
		return 0, false
	}
	off := t.starts[line-1] + col - 1
	if off > t.lineEnd(line-1) {
		return 0, false
	}
	return off, true
}

// replace returns the text with the bytes from start up to end replaced
// by repl.
func (t *text) replace(start, end int, repl string) *text {
	data := make([]byte, 0, len(t.data)-(end-start)+len(repl))
	data = append(data, t.data[:start]...)
	data = append(data, repl...)
	data = append(data, t.data[end:]...)
	return newText(data)
}

// utf16Len returns the number of UTF-16 code units that encode r.
func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// utf16Units returns the number of UTF-16 code units that encode b. An
// invalid byte counts as one unit, as the replacement character does.
func utf16Units(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		n += utf16Len(r)
		b = b[size:]
	}
	return n
}

// posMap translates LSP positions between an original loGo/s text and its
// lowered Go text through the edit map the compiler wrote beside the
// lowered file.
type posMap struct {
	*editmap.Map
	orig, low *text
}

// newPosMap checks m against the two texts and builds the map.
func newPosMap(src, out []byte, m editmap.EditMap) (*posMap, error) {
	em, err := editmap.New(src, out, m)
	if err != nil {
		return nil, err
	}
	return &posMap{Map: em, orig: newText(src), low: newText(out)}, nil
}

// toLowered maps a position of the original text to the lowered text. A
// position inside deleted text maps to the start of what replaced it.
func (m *posMap) toLowered(p Position) Position {
	o := m.orig.offset(p)
	if v, ok := m.LoweredOffset(o, editmap.Leading); ok {
		return m.low.position(v)
	}
	return m.low.position(m.LoweredOffsetNear(o, editmap.Leading))
}

// toOriginal maps a position of the lowered text to the original text. A
// position inside generated text maps to the original text that produced
// it.
func (m *posMap) toOriginal(p Position) Position {
	o, _ := m.OriginalOffset(m.low.offset(p), editmap.Leading)
	return m.orig.position(o)
}

// rangeToLowered maps a range of the original text to the lowered text. It
// stays tight: the start sits after text inserted at its offset and the end
// before text inserted at its offset.
func (m *posMap) rangeToLowered(r Range) Range {
	start := m.LoweredOffsetNear(m.orig.offset(r.Start), editmap.Leading)
	end := max(start, m.LoweredOffsetNear(m.orig.offset(r.End), editmap.Trailing))
	return Range{Start: m.low.position(start), End: m.low.position(end)}
}

// rangeToOriginal maps a range of the lowered text to the original text. A
// range that touches generated text widens to the original text that
// produced it: every generated run the range overlaps contributes its
// anchor, so a highlight over the ? expansion maps to the ? token even
// when its end sits exactly on the generated-to-copy seam.
func (m *posMap) rangeToOriginal(r Range) Range {
	loStart := m.low.offset(r.Start)
	loEnd := m.low.offset(r.End)
	if loEnd < loStart {
		loEnd = loStart
	}
	start, _ := m.OriginalOffset(loStart, editmap.Leading)
	end, _ := m.OriginalOffset(loEnd, editmap.Trailing)
	if loStart < loEnd {
		start, end = m.WidenOverGenerated(loStart, loEnd, start, end)
	}
	end = max(start, end)
	return Range{Start: m.orig.position(start), End: m.orig.position(end)}
}
