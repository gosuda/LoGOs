// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
)

// textDocumentItem is the document of a didOpen notification.
type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// didOpenParams are the params of textDocument/didOpen.
type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

// contentChange is one change of a didChange notification: a whole new
// text when Range is nil, else a replacement of Range.
type contentChange struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

// docIdentifier names a document by its URI.
type docIdentifier struct {
	URI string `json:"uri"`
}

// versionedID names a document by its URI and version.
type versionedID struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// didChangeParams are the params of textDocument/didChange.
type didChangeParams struct {
	TextDocument   versionedID     `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

// docParams are the params of notifications and requests that carry only
// a text document identifier.
type docParams struct {
	TextDocument docIdentifier `json:"textDocument"`
}

// watchedParams are the params of workspace/didChangeWatchedFiles.
type watchedParams struct {
	Changes []struct {
		URI string `json:"uri"`
	} `json:"changes"`
}

// fromClient handles one message from the client.
func (p *Proxy) fromClient(ctx context.Context, m *message) {
	switch {
	case m.isResponse():
		p.send(p.server, m)
	case m.isRequest():
		p.clientRequest(ctx, m)
	default:
		p.clientNotification(ctx, m)
	}
}

// clientNotification handles one notification from the client. The
// document notifications of loGo/s sources stop here, since gopls knows
// those files only through the lowered documents; everything else goes on
// to gopls.
func (p *Proxy) clientNotification(ctx context.Context, m *message) {
	switch m.Method {
	case "textDocument/didOpen":
		p.didOpen(ctx, m)
	case "textDocument/didChange":
		p.didChange(ctx, m)
	case "textDocument/didSave":
		p.didSave(ctx, m)
	case "textDocument/didClose":
		p.didClose(ctx, m)
	case "workspace/didChangeWatchedFiles":
		p.watched(ctx, m)
	default:
		p.send(p.server, m)
	}
}

// trackedPath reports whether path is a source the proxy compiles: a Go or
// loGo/s file.
func trackedPath(path string) bool { return isLogosPath(path) || isGoPath(path) }

// docPath returns the file path of the document a notification names, or
// "" when it names none the proxy follows.
func docPath(uri string) string {
	path, err := uriPath(uri)
	if err != nil || !trackedPath(path) {
		return ""
	}
	return path
}

// forwardUnlessLogos sends m on to gopls unless it concerns the loGo/s
// source path.
func (p *Proxy) forwardUnlessLogos(m *message, path string) {
	if !isLogosPath(path) {
		p.send(p.server, m)
	}
}

// didOpen records the opened document. Nothing compiles: gopls learns the
// lowered documents of the package when the first request asks for them.
func (p *Proxy) didOpen(ctx context.Context, m *message) {
	var params didOpenParams
	if err := json.Unmarshal(m.Params, &params); err != nil {
		p.logf("didOpen: %v", err)
		p.send(p.server, m)
		return
	}
	item := params.TextDocument
	path := docPath(item.URI)
	if path != "" {
		p.st.setDoc(item.URI, path, item.Version, newText([]byte(item.Text)))
	}
	p.forwardUnlessLogos(m, path)
}

// didChange applies the changes to the recorded text. No snapshot is
// computed: the next request or didSave brings the lowering back in line.
func (p *Proxy) didChange(ctx context.Context, m *message) {
	var params didChangeParams
	if err := json.Unmarshal(m.Params, &params); err != nil {
		p.logf("didChange: %v", err)
		p.send(p.server, m)
		return
	}
	path := docPath(params.TextDocument.URI)
	p.st.change(path, params.TextDocument.Version, params.ContentChanges)
	p.forwardUnlessLogos(m, path)
}

// applyChanges applies the changes of one didChange notification, in order,
// each against the text the previous one left.
func applyChanges(t *text, changes []contentChange) *text {
	for _, c := range changes {
		if c.Range == nil {
			t = newText([]byte(c.Text))
			continue
		}
		t = t.replace(t.offset(c.Range.Start), t.offset(c.Range.End), c.Text)
	}
	return t
}

// didSave computes the snapshots the saved document's packages are
// waiting for: a save is one of the two events a recompile may follow.
func (p *Proxy) didSave(ctx context.Context, m *message) {
	var params docParams
	if err := json.Unmarshal(m.Params, &params); err != nil {
		p.logf("didSave: %v", err)
		p.send(p.server, m)
		return
	}
	path := docPath(params.TextDocument.URI)
	if p.st.open(path) {
		p.ensure(ctx)
	}
	p.forwardUnlessLogos(m, path)
}

// didClose forgets the document and what the compiles took from its
// buffer. A package whose snapshot was computed from the discarded text
// keeps its unit and refreshes it from what is on disk at once; a package
// whose snapshot already matches the disk drops its unit when no open
// document is left, and with it the lowered documents only it needed.
func (p *Proxy) didClose(ctx context.Context, m *message) {
	var params docParams
	if err := json.Unmarshal(m.Params, &params); err != nil {
		p.logf("didClose: %v", err)
		p.send(p.server, m)
		return
	}
	path := docPath(params.TextDocument.URI)
	retained := p.st.forget(path)
	p.forwardUnlessLogos(m, path)
	if retained {
		p.ensure(ctx)
		return
	}
	p.st.mu.Lock()
	need := p.st.unitNeed()
	p.st.mu.Unlock()
	p.work.Lock()
	defer p.work.Unlock()
	p.mu.Lock()
	p.pruneLocked(need)
	p.mu.Unlock()
	p.syncGopls()
	p.publishAll()
}

// watched marks every package for a snapshot when a watched file that can
// change a compile changed on disk, and passes the notification on. The
// snapshots are computed by the next request or didSave.
func (p *Proxy) watched(ctx context.Context, m *message) {
	var params watchedParams
	if err := json.Unmarshal(m.Params, &params); err == nil {
		p.markIfRelevant(params)
	}
	p.send(p.server, m)
}

// markIfRelevant marks every unit dirty when one of the changed files is a
// source or a module file.
func (p *Proxy) markIfRelevant(params watchedParams) {
	relevant := false
	for _, c := range params.Changes {
		path, err := uriPath(c.URI)
		if err == nil && (trackedPath(path) || isModuleFile(path)) {
			relevant = true
		}
	}
	if !relevant {
		return
	}
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	for _, u := range p.st.units {
		u.dirty = true
		u.gen++
	}
}

// isModuleFile reports whether path names a file that defines the module
// graph.
func isModuleFile(path string) bool {
	switch filepath.Base(path) {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "logos.toml":
		return true
	}
	return false
}
