// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"context"
	"encoding/json"
)

var nullResult = json.RawMessage("null")

const codeRequestFailed = -32803

func (p *Proxy) clientRequest(ctx context.Context, m *message) {
	switch m.Method {
	case "initialize":
		p.initialize(m)
	case "shutdown":
		p.mu.Lock()
		p.shutdown = true
		p.mu.Unlock()
		p.send(p.server, m)
	case "logos/snapshotCounts":
		p.snapshotCounts(ctx, m)
	default:
		p.request(ctx, m)
	}
}

func (p *Proxy) initialize(m *message) {
	if params, err := decode(m.Params); err == nil {
		p.rememberRoots(params)
		dropPositionEncodings(params)
		if raw, err := encode(params); err == nil {
			m.Params = raw
		}
	}
	p.addPending(m.ID, pending{method: m.Method})
	p.send(p.server, m)
}

func dropPositionEncodings(params any) {
	root, _ := params.(map[string]any)
	caps, _ := root["capabilities"].(map[string]any)
	general, _ := caps["general"].(map[string]any)
	delete(general, "positionEncodings")
}

func dropDiagnosticProvider(result any) {
	root, _ := result.(map[string]any)
	caps, _ := root["capabilities"].(map[string]any)
	delete(caps, "diagnosticProvider")
}

func requestPath(params any) string {
	root, _ := params.(map[string]any)
	td, _ := root["textDocument"].(map[string]any)
	uri, _ := td["uri"].(string)
	path, err := uriPath(uri)
	if err != nil {
		return ""
	}
	return path
}

func (p *Proxy) target(path string) (*lowered, bool) {
	if !isLogosPath(path) {
		return nil, true
	}
	lw := p.tables.Load().bySrc[path]
	return lw, lw != nil && lw.fresh
}

func (p *Proxy) request(ctx context.Context, m *message) {
	params, err := decode(m.Params)
	if err != nil {
		p.send(p.server, m)
		return
	}
	path := requestPath(params)
	switch m.Method {
	case "textDocument/formatting", "textDocument/rangeFormatting", "textDocument/onTypeFormatting":
		if isLogosPath(path) {
			p.declineFormatting(m)
			return
		}
	}
	p.ensure(ctx)
	p.work.Lock()
	defer p.work.Unlock()
	lw, ok := p.target(path)
	if !ok {
		if m.Method == "textDocument/codeAction" && isLogosPath(path) {
			p.respond(m.ID, p.finishResult(pending{method: m.Method, path: path, params: m.Params}, []any{}))
			return
		}
		p.send(p.client, &message{ID: m.ID, Result: nullResult})
		return
	}
	tb := p.tables.Load()
	out := &message{ID: m.ID, Method: m.Method, Params: m.Params}
	if params != nil {
		if out.Params, err = encode(tb.toLowered().rewrite(params, lw)); err != nil {
			p.send(p.client, &message{ID: m.ID, Result: nullResult})
			return
		}
	}
	pend := pending{method: m.Method, path: path, lw: lw, tb: tb, params: m.Params}
	if m.Method == "textDocument/codeAction" {
		pend.fixVersion, pend.fixHas = p.docVersion(path)
	}
	p.addPending(m.ID, pend)
	p.send(p.server, out)
}

func (p *Proxy) declineFormatting(m *message) {
	reason := m.Method + " is not supported for loGo/s sources: gopls would format the lowered Go text, not the source"
	p.fail(m.ID, codeRequestFailed, reason)
}

func (p *Proxy) fromServer(ctx context.Context, m *message) {
	switch {
	case m.isResponse():
		p.serverResponse(ctx, m)
	case !m.isRequest() && m.Method == "textDocument/publishDiagnostics":
		p.serverDiagnostics(m)
	default:
		p.forwardToClient(m)
	}
}

func (p *Proxy) forwardToClient(m *message) {
	if params, err := decode(m.Params); err == nil && params != nil {
		if raw, err := encode(p.tables.Load().toSource().rewrite(params, nil)); err == nil {
			m.Params = raw
		}
	}
	p.send(p.client, m)
}

func (p *Proxy) serverResponse(ctx context.Context, m *message) {
	p.mu.Lock()
	pend, ok := p.pending[string(m.ID)]
	p.mu.Unlock()
	result, err := decode(m.Result)
	if !ok || len(m.Error) > 0 || err != nil {
		p.send(p.client, m)
		p.finish(m.ID)
		return
	}
	result = p.finishResult(pend, pend.tb.toSource().rewrite(prepareResult(pend, result), pend.lw))
	if p.opts.Hints != nil && servesHints(pend.method) && p.spawnHints(ctx, m.ID, pend, result) {
		return
	}
	p.respond(m.ID, result)
	p.finish(m.ID)
}

func (p *Proxy) finishResult(pend pending, result any) any {
	switch pend.method {
	case "initialize":
		dropDiagnosticProvider(result)
	case "textDocument/codeAction":
		return p.mergeCodeActions(pend, result)
	}
	return result
}

func (p *Proxy) respond(id json.RawMessage, result any) {
	raw, err := encode(result)
	if err != nil {
		p.logf("encoding a response: %v", err)
		raw = nullResult
	}
	p.send(p.client, &message{ID: id, Result: raw})
}

func (p *Proxy) serverDiagnostics(m *message) {
	params, err := decode(m.Params)
	root, _ := params.(map[string]any)
	uri, _ := root["uri"].(string)
	if err != nil || uri == "" {
		p.send(p.client, m)
		return
	}
	list, _ := root["diagnostics"].([]any)
	items, _ := json.Marshal(root["diagnostics"])
	version, hasVer := number(root["version"])
	path := p.docPath(uri)
	p.st.mu.Lock()
	_, openDoc := p.st.docs[path]
	p.st.mu.Unlock()
	live := openDoc || p.tables.Load().byVirt[path] != nil
	p.mu.Lock()
	if len(list) == 0 && !live {
		delete(p.srvDiags, uri)
	} else {
		p.srvDiags[uri] = serverDiags{version: version, hasVer: hasVer, raw: items}
	}
	p.mu.Unlock()
	tb := p.tables.Load()
	if lw := tb.lookup(tb.byVirt, uri); lw != nil {
		uri = lw.srcURI()
	}
	p.publish(uri)
}
