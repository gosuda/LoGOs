// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
)

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type contentChange struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type docIdentifier struct {
	URI string `json:"uri"`
}

type versionedID struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type didChangeParams struct {
	TextDocument   versionedID     `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

type docParams struct {
	TextDocument docIdentifier `json:"textDocument"`
}

type watchedParams struct {
	Changes []struct {
		URI string `json:"uri"`
	} `json:"changes"`
}

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

func trackedPath(path string) bool { return isLogosPath(path) || isGoPath(path) }

func docPath(uri string) string {
	path, err := uriPath(uri)
	if err != nil || !trackedPath(path) {
		return ""
	}
	return path
}

func (p *Proxy) forwardUnlessLogos(m *message, path string) {
	if !isLogosPath(path) {
		p.send(p.server, m)
	}
}

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

func (p *Proxy) watched(ctx context.Context, m *message) {
	var params watchedParams
	if err := json.Unmarshal(m.Params, &params); err == nil {
		p.markIfRelevant(params)
	}
	p.send(p.server, m)
}

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

func isModuleFile(path string) bool {
	switch filepath.Base(path) {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "logos.toml":
		return true
	}
	return false
}
