// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// document is an open document of the client.
type document struct {
	uri     string
	version int
	text    *text
}

// unit is the lowered snapshot of one package directory: the dependency
// closure it was last compiled with, and what that compile produced. A
// package holds at most one snapshot at a time.
type unit struct {
	dir   string
	dirs  map[string]bool
	paths map[string]bool
	diags []Diagnostic
	dirty bool

	// diagVersions holds the version of each open document at the time diags
	// was computed, so diagnostics are never shown against a newer text.
	diagVersions map[string]int

	// gen counts the changes of the unit's inputs and results, so work
	// computed from an older state is never stored as current.
	gen int

	hints   json.RawMessage
	hintsOK bool
}

// flight is one snapshot computation in progress. Concurrent callers wait
// for done instead of computing again.
type flight struct {
	done chan struct{}
}

// store holds the documents keyed by source URI and the lowered snapshot
// of each package. Its mutex guards the documents and the units; the
// flights turn concurrent requests on one version set into one snapshot
// computation.
type store struct {
	mu      sync.Mutex
	docs    map[string]*document
	units   map[string]*unit
	flights map[string]*flight
}

func (s *store) init() {
	s.docs = make(map[string]*document)
	s.units = make(map[string]*unit)
	s.flights = make(map[string]*flight)
}

// open reports whether the document at path is open.
func (s *store) open(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docs[path] != nil
}

// setDoc records an opened document and marks the packages that its
// directory feeds for a snapshot.
func (s *store) setDoc(uri, path string, version int, t *text) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[path] = &document{uri: uri, version: version, text: t}
	s.touchLocked(path)
}

// change applies the changes of one didChange to the recorded text and
// bumps its version. It reports whether the document is open.
func (s *store) change(path string, version int, changes []contentChange) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[path]
	if doc == nil {
		return false
	}
	doc.text = applyChanges(doc.text, changes)
	doc.version = version
	s.touchLocked(path)
	return true
}

// forget removes the open document at path and its unsaved text. Closing
// discards the client's text, so every package compiled with it is marked
// dirty. The package of the document keeps its snapshot for a refresh
// while the discarded text differs from the file on disk; when it does
// not, the snapshot is already the disk result, and it is dropped when no
// open document is left in the package. forget reports whether the
// snapshot must be refreshed.
func (s *store) forget(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.docs[path]
	if doc == nil {
		return false
	}
	unsaved := !diskEqual(path, doc.text.data)
	delete(s.docs, path)
	s.touchLocked(path)
	if unsaved {
		return true
	}
	s.dropUnitLocked(filepath.Dir(path))
	return false
}

// diskEqual reports whether the file at path on disk holds data.
func diskEqual(path string, data []byte) bool {
	disk, err := os.ReadFile(path)
	return err == nil && bytes.Equal(disk, data)
}

// touchLocked records that the document at path changed: its package gets
// a unit, and every unit whose closure holds that package needs a
// snapshot. The caller holds s.mu.
func (s *store) touchLocked(path string) {
	dir := filepath.Dir(path)
	if s.units[dir] == nil {
		s.units[dir] = &unit{dir: dir}
	}
	for _, u := range s.units {
		if u.dir == dir || u.dirs[dir] {
			u.dirty = true
			u.gen++
		}
	}
}

// dropUnitLocked removes the unit of dir when no open document is left in
// it. The caller holds s.mu.
func (s *store) dropUnitLocked(dir string) {
	for path := range s.docs {
		if filepath.Dir(path) == dir {
			return
		}
	}
	delete(s.units, dir)
}

// unitFor returns the unit of the package that holds path. The caller
// holds s.mu.
func (s *store) unitFor(path string) *unit {
	return s.units[filepath.Dir(path)]
}

// unitNeed returns the set of lowerings the current units need. The caller
// holds s.mu.
func (s *store) unitNeed() map[string]bool {
	need := make(map[string]bool)
	for _, u := range s.units {
		maps.Copy(need, u.paths)
	}
	return need
}

// ensure brings every package whose inputs changed to one snapshot per
// version set, computed on demand: a caller that finds a computation in
// flight for a package waits for it instead of computing again. It returns
// once nothing is dirty.
func (p *Proxy) ensure(ctx context.Context) {
	for {
		p.st.mu.Lock()
		var dir string
		for _, d := range slices.Sorted(maps.Keys(p.st.units)) {
			if u := p.st.units[d]; u.dirty {
				dir = d
				break
			}
		}
		if dir == "" {
			p.st.mu.Unlock()
			return
		}
		if fl, ok := p.st.flights[dir]; ok {
			p.st.mu.Unlock()
			select {
			case <-fl.done:
			case <-ctx.Done():
				return
			}
			continue
		}
		fl := &flight{done: make(chan struct{})}
		p.st.flights[dir] = fl
		p.st.mu.Unlock()

		p.compileUnit(ctx, dir)

		p.st.mu.Lock()
		delete(p.st.flights, dir)
		p.st.mu.Unlock()
		close(fl.done)
	}
}

// compileUnit computes the snapshot of the package in dir and brings gopls
// and the client up to date with the result. It does nothing once the
// proxy is stopping, so a late computation cannot use a compiler that stop
// is about to close.
func (p *Proxy) compileUnit(ctx context.Context, dir string) {
	buffers, versions := p.takeDirty(dir)
	if buffers == nil {
		return
	}
	low, diags := p.compile(ctx, dir, buffers)
	p.apply(dir, low, diags, versions)
	p.syncGopls()
	p.publishAll()
}

// takeDirty marks the package in dir clean and returns the texts of the
// open documents by path, and the version of each. A nil map means a
// stopped proxy has nothing to compute.
func (p *Proxy) takeDirty(dir string) (map[string][]byte, map[string]int) {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	if p.stopped {
		return nil, nil
	}
	u := p.st.units[dir]
	if u == nil || !u.dirty {
		return nil, nil
	}
	u.dirty = false
	buffers := make(map[string][]byte, len(p.st.docs))
	versions := make(map[string]int, len(p.st.docs))
	for path, doc := range p.st.docs {
		buffers[path] = doc.text.data
		versions[path] = doc.version
	}
	return buffers, versions
}

// compile lowers the package in dir and collects its diagnostics. A
// failed run yields a nil lowering and no diagnostics; the log has the
// reason.
func (p *Proxy) compile(ctx context.Context, dir string, buffers map[string][]byte) (*Lowering, []Diagnostic) {
	low, err := p.opts.Compiler.Lower(ctx, dir, buffers)
	if err != nil {
		p.logf("lowering %s: %v", dir, err)
		return nil, nil
	}
	diags, err := p.opts.Compiler.Diagnose(ctx, dir, buffers)
	if err != nil {
		p.logf("diagnosing %s: %v", dir, err)
	}
	return low, diags
}

// apply installs the snapshot of the package in dir. A nil lowering means
// the run failed: the package keeps its closure, and every file of it
// turns stale.
func (p *Proxy) apply(dir string, low *Lowering, diags []Diagnostic, versions map[string]int) {
	p.st.mu.Lock()
	u := p.st.units[dir]
	if u == nil {
		p.st.mu.Unlock()
		return
	}
	if low == nil {
		low = &Lowering{Dirs: slices.Collect(maps.Keys(u.dirs)), Missing: slices.Collect(maps.Keys(u.paths))}
	}
	u.dirs = setOf(low.Dirs)
	u.diags = diags
	u.diagVersions = versions
	u.hintsOK = false
	u.gen++
	u.paths = make(map[string]bool, len(low.Files)+len(low.Missing))
	fresh := make(map[string]*lowered, len(low.Files))
	for _, f := range low.Files {
		u.paths[f.Path] = true
		pm, err := newPosMap(f.Src, f.Out, f.Map)
		if err != nil {
			p.logf("%v", err)
			fresh[f.Path] = nil
			continue
		}
		doc := p.st.docs[f.Path]
		fresh[f.Path] = &lowered{
			src:   f.Path,
			virt:  virtualPath(f.Path),
			pm:    pm,
			fresh: doc == nil || bytes.Equal(doc.text.data, f.Src),
		}
	}
	for _, path := range low.Missing {
		u.paths[path] = true
	}
	need := p.st.unitNeed()
	p.st.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	for path, lw := range fresh {
		if lw == nil {
			p.staleLocked(path)
			continue
		}
		p.files[path] = lw
	}
	for _, path := range low.Missing {
		p.staleLocked(path)
	}
	p.pruneLocked(need)
}

// staleLocked marks the lowering of path, if any, as no longer matching
// the source. The caller holds p.mu.
func (p *Proxy) staleLocked(path string) {
	if lw := p.files[path]; lw != nil && lw.fresh {
		stale := *lw
		stale.fresh = false
		p.files[path] = &stale
	}
}

// pruneLocked drops the lowerings need does not hold any more, and
// publishes the new tables. need is unitNeed, taken under store.mu before
// p.mu, so no caller holds the two locks in opposite order. The caller
// holds p.mu.
func (p *Proxy) pruneLocked(need map[string]bool) {
	maps.DeleteFunc(p.files, func(path string, _ *lowered) bool { return !need[path] })
	tb := &tables{bySrc: make(map[string]*lowered, len(p.files)), byVirt: make(map[string]*lowered, len(p.files))}
	for path, lw := range p.files {
		tb.bySrc[path] = lw
		tb.byVirt[lw.virt] = lw
	}
	p.tables.Store(tb)
}

// setOf returns the set of the elements of list.
func setOf(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set
}

// syncGopls makes the lowered documents open in gopls match the
// snapshots: it opens the new ones, updates the changed ones, and closes
// the ones no unit needs.
func (p *Proxy) syncGopls() {
	for _, m := range p.virtualMessages() {
		p.send(p.server, m)
	}
}

// virtualMessages computes the messages that bring gopls in line with the
// lowerings, and records what it will then hold.
func (p *Proxy) virtualMessages() []*message {
	p.st.mu.Lock()
	p.mu.Lock()
	defer p.st.mu.Unlock()
	defer p.mu.Unlock()
	var msgs []*message
	want := make(map[string]bool, len(p.files))
	for _, path := range slices.Sorted(maps.Keys(p.files)) {
		lw := p.files[path]
		want[lw.virt] = true
		msgs = append(msgs, p.openOrChangeLocked(lw)...)
	}
	for _, virt := range slices.Sorted(maps.Keys(p.virt)) {
		if !want[virt] {
			msgs = append(msgs, p.notification("textDocument/didClose", docParams{TextDocument: docIdentifier{URI: pathURI(virt)}})...)
			delete(p.virt, virt)
			delete(p.srvDiags, pathURI(virt))
		}
	}
	return msgs
}

// openOrChangeLocked returns the notification that gives gopls the lowered
// text of lw, or none when it holds that text already. The caller holds
// p.st.mu and p.mu.
func (p *Proxy) openOrChangeLocked(lw *lowered) []*message {
	out := lw.pm.low.data
	vd := p.virt[lw.virt]
	if vd == nil {
		p.virt[lw.virt] = &virtDoc{lw: lw, version: 1}
		return p.notification("textDocument/didOpen", didOpenParams{
			TextDocument: textDocumentItem{URI: lw.virtURI(), LanguageID: "go", Version: 1, Text: string(out)},
		})
	}
	same := bytes.Equal(vd.lw.pm.low.data, out)
	vd.lw = lw
	if same {
		return nil
	}
	vd.version++
	return p.notification("textDocument/didChange", didChangeParams{
		TextDocument:   versionedID{URI: lw.virtURI(), Version: vd.version},
		ContentChanges: []contentChange{{Text: string(out)}},
	})
}

// notification returns the notification message of method with params, as
// a list of one message, or of none when the params do not encode.
func (p *Proxy) notification(method string, params any) []*message {
	raw, err := json.Marshal(params)
	if err != nil {
		p.logf("encoding %s: %v", method, err)
		return nil
	}
	return []*message{{Method: method, Params: raw}}
}
