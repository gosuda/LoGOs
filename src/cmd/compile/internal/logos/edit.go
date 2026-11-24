// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/internal/logos/editmap"
)

type Edit struct {
	Start, End syntax.Pos
	Text       string
	Chunks     []chunk
}

type splicePlacement uint8

const (
	placementOrdinary splicePlacement = iota
	placementAfterExpression
)

type splice struct {
	start, end int
	text       string
	chunks     []chunk
	owner      int
	placement  splicePlacement
	host       *cfHost
}

func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func offsetOf(starts []int, size int, pos syntax.Pos) (int, error) {
	return lineColOffset(starts, size, int(pos.Line()), int(pos.Col()))
}

func lineColOffset(starts []int, size, line, col int) (int, error) {
	if line < 1 || line > len(starts) || col < 1 {
		return 0, fmt.Errorf("position %d:%d is outside the source", line, col)
	}
	end := size
	if line < len(starts) {
		end = starts[line] - 1
	}
	off := starts[line-1] + col - 1
	if off > end {
		return 0, fmt.Errorf("position %d:%d is outside the source", line, col)
	}
	return off, nil
}

func resolve(src []byte, starts []int, base *syntax.PosBase, e Edit) (splice, error) {
	for _, pos := range []syntax.Pos{e.Start, e.End} {
		if pos.FileBase() != base {
			return splice{}, fmt.Errorf("edit position %v belongs to a different file than %s", pos, base.Filename())
		}
	}
	start, err := offsetOf(starts, len(src), e.Start)
	if err != nil {
		return splice{}, err
	}
	end, err := offsetOf(starts, len(src), e.End)
	if err != nil {
		return splice{}, err
	}
	if end < start {
		return splice{}, fmt.Errorf("edit at %v ends before it starts", e.Start)
	}
	return splice{start: start, end: end, text: e.Text, chunks: e.Chunks}, nil
}

func ApplyEdits(src []byte, base *syntax.PosBase, edits []Edit) ([]byte, error) {
	out, _, _, err := ApplyEditsSegments(src, base, edits)
	return out, err
}

func ApplyEditsSegments(src []byte, base *syntax.PosBase, edits []Edit) ([]byte, []editmap.EditSpan, []editmap.Segment, error) {
	want := base.Pos().FileBase()
	starts := lineStarts(src)
	splices := make([]splice, len(edits))
	for i, e := range edits {
		s, err := resolve(src, starts, want, e)
		if err != nil {
			return nil, nil, nil, err
		}
		splices[i] = s
	}
	text, err := applyChunks(otext{s: string(src)}, splices)
	if err != nil {
		return nil, nil, nil, err
	}
	text.checkChunks(src)
	out := []byte(text.s)
	return out, spansOf(splices), segmentsOf(splices, len(out)), nil
}

func spansOf(sorted []splice) []editmap.EditSpan {
	spans := make([]editmap.EditSpan, len(sorted))
	for i, s := range sorted {
		spans[i] = editmap.EditSpan{Start: s.start, End: s.end, Len: len(s.text)}
	}
	return spans
}

func segmentsOf(sorted []splice, outLen int) []editmap.Segment {
	var segs []editmap.Segment
	seg := func(start, end, orig int) {
		if end <= start {
			return
		}
		if n := len(segs); n > 0 {
			last := &segs[n-1]
			if (last.Orig < 0 && orig < 0) ||
				(last.Orig >= 0 && orig >= 0 && last.Orig+last.Len == orig && last.Out+last.Len == start) {
				last.Len += end - start
				return
			}
		}
		segs = append(segs, editmap.Segment{Out: start, Len: end - start, Orig: orig})
	}
	out := 0
	prev := 0
	for _, s := range sorted {
		seg(out, out+(s.start-prev), prev)
		out += s.start - prev
		cov := 0
		for _, ch := range s.chunks {
			if ch.n == 0 {
				continue
			}
			if ch.out > cov {
				seg(out+cov, out+ch.out, -1)
			}
			seg(out+ch.out, out+ch.out+ch.n, ch.orig)
			cov = ch.out + ch.n
		}
		if cov < len(s.text) {
			seg(out+cov, out+len(s.text), -1)
		}
		out += len(s.text)
		prev = s.end
	}
	seg(out, outLen, prev)
	return segs
}

func sortSplices(splices []splice) {
	slices.SortStableFunc(splices, func(a, b splice) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end), cmp.Compare(a.placement, b.placement))
	})
}

// strictlyCovers reports whether the recorded span [cs, ce) strictly
// contains the new span [s, e). A zero-width insert on the cover's
// boundary stays outside the cover: it places text immediately before or
// after it, matching the boundary rule textOf drains with.
func strictlyCovers(cs, ce, s, e int) bool {
	if s == e {
		return cs < s && s < ce
	}
	return cs <= s && e <= ce && (cs < s || e < ce)
}

// foldIntoCover merges sp into the narrowest splice of list that strictly
// covers it, rewriting the cover's text in place, and reports whether it
// did. A splice recorded after its cover cannot drain into the cover's
// text through textOf: the cover already snapshotted its bytes. Without
// this the two splices overlap and the lowering aborts; an interpolation
// or option lowering inside a MustNonNil-wrapped call reaches here,
// because the wrapper runs before those passes. An option lowering
// records owned boundary inserts and deletes the ?? operator, so owned
// splices and deletions fold too. The merge maps the inner span through
// the cover's origin chunks and substitutes the inner text there,
// keeping generated fragments the cover already holds. A span that maps
// nowhere is refused, leaving the caller to record the splice on its own.
func foldIntoCover(list *[]splice, sp splice) bool {
	if sp.host != nil || (sp.text == "" && sp.start == sp.end) {
		return false
	}
	best := -1
	for i := range *list {
		cover := &(*list)[i]
		if cover.host != nil || cover.text == "" {
			continue
		}
		if !strictlyCovers(cover.start, cover.end, sp.start, sp.end) {
			continue
		}
		if best == -1 || cover.end-cover.start < (*list)[best].end-(*list)[best].start {
			best = i
		}
	}
	if best == -1 {
		return false
	}
	merged, ok := nestSplice((*list)[best], sp)
	if !ok {
		return false
	}
	(*list)[best] = merged
	return true
}

// nestSplice returns cover with inner's text substituted over the inner
// span. The inner text is a complete lowering of its span, so it replaces
// the whole mapped output range at once; source the cover no longer spells
// verbatim (earlier folds) would need the inner text re-lowered over them,
// which the inner pass cannot see, so such spans are refused instead.
func nestSplice(cover, inner splice) (splice, bool) {
	r := otext{s: inner.text, chunks: inner.chunks}
	os, ok1 := mapStart(cover.chunks, inner.start)
	oe, ok2 := mapEnd(cover.chunks, inner.end)
	if !ok1 || !ok2 {
		return splice{}, false
	}
	if inner.start == inner.end {
		out := splice{start: cover.start, end: cover.end, owner: cover.owner, placement: cover.placement}
		out.text = cover.text[:os] + r.s + cover.text[os:]
		out.chunks = insertChunks(cover.chunks, r.chunks, os, len(r.s))
		return out, true
	}
	if oe <= os {
		return splice{}, false
	}
	if !fullyCovered(cover.chunks, os, oe) {
		return splice{}, false
	}
	out := splice{start: cover.start, end: cover.end, owner: cover.owner, placement: cover.placement}
	out.text = cover.text[:os] + r.s + cover.text[oe:]
	delta := len(r.s) - (oe - os)
	for _, c := range cover.chunks {
		if c.n <= 0 || c.out+c.n <= os {
			if c.n > 0 {
				out.chunks = append(out.chunks, c)
			}
			continue
		}
		if c.out < os {
			out.chunks = append(out.chunks, chunk{out: c.out, orig: c.orig, n: os - c.out})
		}
	}
	for _, c := range r.chunks {
		if c.n <= 0 {
			continue
		}
		out.chunks = append(out.chunks, chunk{out: c.out + os, orig: c.orig, n: c.n})
	}
	for _, c := range cover.chunks {
		if c.n <= 0 {
			continue
		}
		if c.out >= oe {
			out.chunks = append(out.chunks, chunk{out: c.out + delta, orig: c.orig, n: c.n})
			continue
		}
		if c.out+c.n > oe {
			out.chunks = append(out.chunks, chunk{out: oe + delta, orig: c.orig + (oe - c.out), n: c.out + c.n - oe})
		}
	}
	return out, true
}

// fullyCovered reports whether every output offset in [os, oe) is spelled
// by a verbatim chunk. Generated bytes in between mean an earlier fold
// the inner text cannot see, so the span is not a clean replacement.
func fullyCovered(chunks []chunk, os, oe int) bool {
	pos := os
	for _, c := range chunks {
		if c.n <= 0 {
			continue
		}
		if c.out+c.n <= pos {
			continue
		}
		if c.out > pos {
			return false
		}
		pos = c.out + c.n
		if pos >= oe {
			return true
		}
	}
	return pos >= oe
}

// mapStart maps p to the output offset of the chunk that holds p on its
// inside for a range start: an interior position maps inside its chunk;
// an edge maps to the chunk that starts at p, so generated text inserted
// at the edge stays before the replacement. When several chunks start at
// p, the latest output wins, preserving the recorded order of inserts at
// the same source position.
func mapStart(chunks []chunk, p int) (int, bool) {
	for _, c := range chunks {
		if c.n <= 0 {
			continue
		}
		if c.orig < p && p < c.orig+c.n {
			return c.out + (p - c.orig), true
		}
	}
	best := -1
	found := false
	for _, c := range chunks {
		if c.n <= 0 || p != c.orig {
			continue
		}
		if !found || c.out > best {
			best = c.out
			found = true
		}
	}
	if found {
		return best, true
	}
	for _, c := range chunks {
		if c.n <= 0 || p != c.orig+c.n {
			continue
		}
		if !found || c.out+c.n > best {
			best = c.out + c.n
			found = true
		}
	}
	if !found {
		return -1, false
	}
	return best, true
}

// mapEnd maps p to the output offset of the chunk that holds p on its
// inside for a range end: an interior position maps inside its chunk;
// an edge maps to the chunk that ends at p, so generated text inserted at
// the edge stays after the replacement. When several chunks end at p, the
// earliest output wins, keeping the replacement before every adjacent
// generated fragment.
func mapEnd(chunks []chunk, p int) (int, bool) {
	for _, c := range chunks {
		if c.n <= 0 {
			continue
		}
		if c.orig < p && p < c.orig+c.n {
			return c.out + (p - c.orig), true
		}
	}
	best := -1
	found := false
	for _, c := range chunks {
		if c.n <= 0 || p != c.orig+c.n {
			continue
		}
		if !found || c.out+c.n < best {
			best = c.out + c.n
			found = true
		}
	}
	if found {
		return best, true
	}
	for _, c := range chunks {
		if c.n <= 0 || p != c.orig {
			continue
		}
		if !found || c.out < best {
			best = c.out
			found = true
		}
	}
	if !found {
		return -1, false
	}
	return best, true
}

// insertChunks returns the chunks of cover with the n output bytes that
// r maps inserted at output offset k, shifting later cover chunks by n.
func insertChunks(cover, r []chunk, k, n int) []chunk {
	var out []chunk
	for _, c := range cover {
		if c.n <= 0 || c.out+c.n <= k {
			if c.n > 0 {
				out = append(out, c)
			}
			continue
		}
		if c.out < k {
			out = append(out, chunk{out: c.out, orig: c.orig, n: k - c.out})
		}
	}
	for _, c := range r {
		if c.n <= 0 {
			continue
		}
		out = append(out, chunk{out: c.out + k, orig: c.orig, n: c.n})
	}
	for _, c := range cover {
		if c.n <= 0 {
			continue
		}
		if c.out >= k {
			out = append(out, chunk{out: c.out + n, orig: c.orig, n: c.n})
			continue
		}
		if c.out+c.n > k {
			out = append(out, chunk{out: c.out + (k - c.out) + n, orig: c.orig + (k - c.out), n: c.out + c.n - k})
		}
	}
	return out
}

func applySplices(src []byte, splices []splice) ([]byte, error) {
	t, err := applyChunks(otext{s: string(src)}, splices)
	return []byte(t.s), err
}

func applyChunks(src otext, splices []splice) (otext, error) {
	sortSplices(splices)
	var sb strings.Builder
	var chunks []chunk
	out := 0
	prev := 0
	gap := func(upto int) {
		if upto <= prev {
			return
		}
		sb.WriteString(src.s[prev:upto])
		for _, c := range src.slice(prev, upto).chunks {
			chunks = append(chunks, chunk{out: out + c.out, orig: c.orig, n: c.n})
		}
		out += upto - prev
	}
	var last splice
	for _, s := range splices {
		if s.start < prev {
			return otext{}, fmt.Errorf("overlapping edits at byte offset %d: [%d,%d)->%q, [%d,%d)->%q", s.start, last.start, last.end, last.text, s.start, s.end, s.text)
		}
		gap(s.start)
		sb.WriteString(s.text)
		for _, c := range s.chunks {
			if c.n == 0 {
				continue
			}
			chunks = append(chunks, chunk{out: out + c.out, orig: c.orig, n: c.n})
		}
		out += len(s.text)
		prev = s.end
		last = s
	}
	gap(len(src.s))
	res := otext{s: sb.String(), chunks: chunks}
	if res.s == "" {
		return otext{}, nil
	}
	return res, nil
}

func editsOf(src []byte, base *syntax.PosBase, splices []splice) []Edit {
	starts := lineStarts(src)
	posAt := func(off int) syntax.Pos {
		line := sort.SearchInts(starts, off+1) - 1
		return syntax.MakePos(base, uint(line+1), uint(off-starts[line]+1))
	}
	sorted := slices.Clone(splices)
	sortSplices(sorted)
	// A splice may cover earlier splices strictly inside it (a default
	// erasure over a respelled literal, a drained fold). Those inner
	// splices already resolved into any textOf copies made while they
	// were pending; their source span is gone, so drop them here —
	// after every composition — instead of reporting an overlap.
	// Zero-width inserts at a cover's boundary stay: they place text
	// immediately before or after the cover.
	kept := sorted[:0]
	maxEarlierEnd := 0
	for i, s := range sorted {
		orphan := false
		// An earlier cover needs end >= s.end; when no earlier splice
		// reaches that far the scan cannot find one.
		if maxEarlierEnd >= s.end {
			for j := 0; j < i && !orphan; j++ {
				k := sorted[j]
				inside := k.start <= s.start && s.end <= k.end &&
					(s.start > k.start || s.end < k.end)
				if inside && !(s.start == s.end && (s.start == k.start || s.end == k.end)) {
					orphan = true
				}
			}
		}
		// A cover sharing s's start sorts after it (by end), so look
		// ahead through the same-start run as well.
		for j := i + 1; !orphan && j < len(sorted) && sorted[j].start == s.start; j++ {
			if s.start != s.end && s.end < sorted[j].end {
				orphan = true
			}
		}
		if !orphan {
			kept = append(kept, s)
		}
		if s.end > maxEarlierEnd {
			maxEarlierEnd = s.end
		}
	}
	edits := make([]Edit, len(kept))
	for i, s := range kept {
		edits[i] = Edit{Start: posAt(s.start), End: posAt(s.end), Text: s.text, Chunks: s.chunks}
	}
	return edits
}
