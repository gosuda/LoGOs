// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeEnv = "LOGOS_LSP_FAKE_GOPLS"

	fakeLogEnv = "LOGOS_LSP_FAKE_LOG"

	waitLimit = 30 * time.Second
)

type fakeScript struct {
	Responses map[string]json.RawMessage `json:"responses"`

	Diagnostics map[string]json.RawMessage `json:"diagnostics"`

	Echo bool `json:"echo"`

	DelayMillis int `json:"delayMillis"`

	CrashOn string `json:"crashOn"`

	HoldMethod string `json:"holdMethod"`

	HoldAddr string `json:"holdAddr"`

	held bool

	docs map[string]string
}

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) != "" {
		os.Exit(runFakeGopls())
	}
	os.Exit(m.Run())
}

func runFakeGopls() int {
	var script fakeScript
	data, err := os.ReadFile(os.Getenv(fakeEnv))
	if err == nil {
		err = json.Unmarshal(data, &script)
	}
	logFile, err2 := os.OpenFile(os.Getenv(fakeLogEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "fake gopls:", err, err2)
		return 2
	}
	defer func() {
		if err := logFile.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "fake gopls closing log file:", err)
		}
	}()
	rd, wr := newReader(os.Stdin), &writer{w: os.Stdout}
	for {
		m, err := rd.read()
		if err != nil || m.Method == "exit" {
			return 0
		}
		line, err := json.Marshal(m)
		if err == nil {
			_, err = logFile.Write(append(line, '\n'))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake gopls: logging a message:", err)
			return 2
		}
		if m.Method == script.CrashOn && m.isRequest() {
			return 3
		}
		if err := fakeHandle(wr, &script, m); err != nil {
			fmt.Fprintln(os.Stderr, "fake gopls: answering a message:", err)
			return 2
		}
	}
}
func echoRespond(wr *writer, id json.RawMessage, v any) error {
	raw, err := encode(v)
	if err != nil {
		return err
	}
	return wr.respond(id, raw)
}

func echoWordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func echoWord(doc string, p Position) Range {
	t := newText([]byte(doc))
	off := t.offset(p)
	start, end := off, off
	for start > 0 && echoWordByte(doc[start-1]) {
		start--
	}
	for end < len(doc) && echoWordByte(doc[end]) {
		end++
	}
	return Range{Start: t.position(start), End: t.position(end)}
}

func echoDiags(doc string) []any {
	i := strings.Index(doc, "ReadFile")
	if i < 0 {
		return []any{}
	}
	t := newText([]byte(doc))
	return []any{map[string]any{
		"range":    Range{Start: t.position(i), End: t.position(i + len("ReadFile"))},
		"severity": 1,
		"message":  stockDiagMessage,
		"source":   "stock",
	}}
}

func echoText(script *fakeScript, m *message) (string, int, string) {
	v, err := decode(m.Params)
	root, _ := v.(map[string]any)
	if err != nil || root == nil {
		return "", 0, ""
	}
	td, _ := root["textDocument"].(map[string]any)
	uri, _ := td["uri"].(string)
	version, _ := number(td["version"])
	if m.Method == "textDocument/didOpen" {
		text, _ := td["text"].(string)
		script.docs[uri] = text
		return uri, version, text
	}
	text := script.docs[uri]
	changes, _ := root["contentChanges"].([]any)
	for _, c := range changes {
		if full, ok := c.(map[string]any)["text"].(string); ok {
			text = full
		}
	}
	script.docs[uri] = text
	return uri, version, text
}

func echoAnswer(wr *writer, script *fakeScript, m *message) error {
	v, err := decode(m.Params)
	root, _ := v.(map[string]any)
	if err != nil || root == nil {
		return wr.respond(m.ID, nullResult)
	}
	td, _ := root["textDocument"].(map[string]any)
	uri, _ := td["uri"].(string)
	pos, ok := asPosition(root["position"].(map[string]any))
	if !ok {
		return wr.respond(m.ID, nullResult)
	}
	span := echoWord(script.docs[uri], pos)
	switch m.Method {
	case "textDocument/hover":
		return echoRespond(wr, m.ID, map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "stock hover"}, "range": span})
	case "textDocument/definition":
		return echoRespond(wr, m.ID, []any{map[string]any{"uri": uri, "range": span}})
	default:
		return echoRespond(wr, m.ID, map[string]any{"isIncomplete": false, "items": []any{map[string]any{"label": "ReadFile", "textEdit": map[string]any{"range": span, "newText": "ReadFile"}}}})
	}
}

func echoHandle(wr *writer, script *fakeScript, m *message) error {
	if script.docs == nil {
		script.docs = map[string]string{}
	}
	if m.isRequest() {
		switch m.Method {
		case "initialize":
			return echoRespond(wr, m.ID, map[string]any{"capabilities": map[string]any{"hoverProvider": true, "definitionProvider": true, "completionProvider": map[string]any{}}})
		case "shutdown":
			return wr.respond(m.ID, nullResult)
		case "textDocument/hover", "textDocument/definition", "textDocument/completion":
			return echoAnswer(wr, script, m)
		default:
			return wr.respond(m.ID, nullResult)
		}
	}
	switch m.Method {
	case "textDocument/didOpen", "textDocument/didChange":
		uri, version, text := echoText(script, m)
		return wr.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "version": version, "diagnostics": echoDiags(text)})
	}
	return nil

}
func fakeHandle(wr *writer, script *fakeScript, m *message) error {
	if script.Echo {
		return echoHandle(wr, script, m)
	}
	switch {
	case m.isRequest():
		if m.Method == script.HoldMethod && script.HoldAddr != "" && !script.held {
			script.held = true
			if err := holdForRelease(script.HoldAddr); err != nil {
				return err
			}
		}
		time.Sleep(time.Duration(script.DelayMillis) * time.Millisecond)
		return wr.respond(m.ID, fakeResult(script, m.Method))
	case m.Method == "textDocument/didOpen" || m.Method == "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version int    `json:"version"`
			} `json:"textDocument"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return nil
		}
		if diags, ok := script.Diagnostics[p.TextDocument.URI]; ok {
			return wr.notify("textDocument/publishDiagnostics", map[string]any{
				"uri": p.TextDocument.URI, "version": p.TextDocument.Version, "diagnostics": diags,
			})
		}
	}
	return nil
}

func holdForRelease(addr string) (err error) {
	d := &net.Dialer{Timeout: waitLimit}
	c, dialErr := d.DialContext(context.Background(), "tcp", addr)
	if dialErr != nil {
		return dialErr
	}
	defer func() {
		if closeErr := c.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if err = c.SetDeadline(time.Now().Add(waitLimit)); err != nil {
		return err
	}
	var b [1]byte
	_, err = io.ReadFull(c, b[:])
	return err
}

func fakeResult(script *fakeScript, method string) json.RawMessage {
	switch method {
	case "initialize":
		return json.RawMessage(`{"capabilities":{"hoverProvider":true,"diagnosticProvider":{"interFileDependencies":true,"workspaceDiagnostics":false}}}`)
	case "shutdown":
		return nullResult
	}
	if r, ok := script.Responses[method]; ok {
		return r
	}
	return nullResult
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testClient struct {
	t      *testing.T
	w      *writer
	closeW func()

	done chan struct{}
	err  error

	mu      sync.Mutex
	cond    *sync.Cond
	msgs    []*message
	nextID  int
	expired bool
}

func startProxy(t *testing.T, opts Options) *testClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &testClient{t: t, w: &writer{w: inW}, closeW: func() {
		if err := inW.Close(); err != nil {
			t.Errorf("closing client input pipe: %v", err)
		}
	}, done: make(chan struct{})}
	c.cond = sync.NewCond(&c.mu)
	stderr := new(syncBuffer)
	opts.Stderr = stderr
	ctx := t.Context()
	go func() {
		c.err = Serve(ctx, inR, outW, opts)
		if err := outW.Close(); err != nil {
			t.Errorf("closing output pipe: %v", err)
		}
		close(c.done)
	}()
	go c.readLoop(newReader(outR))
	t.Cleanup(func() {
		if err := inW.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("closing client input pipe: %v", err)
		}
		if err := outR.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("closing proxy output pipe: %v", err)
		}
		select {
		case <-c.done:
		case <-time.After(waitLimit):
			t.Error("Serve did not return after the test")
		}
		if t.Failed() {
			t.Logf("proxy stderr:\n%s", stderr.String())
		}
	})
	return c
}

func (c *testClient) wait() error {
	c.t.Helper()
	select {
	case <-c.done:
		return c.err
	case <-time.After(waitLimit):
		c.t.Fatal("Serve did not return")
		return nil
	}
}

func (c *testClient) readLoop(rd *reader) {
	for {
		m, err := rd.read()
		c.mu.Lock()
		if err != nil {
			c.expired = true
		} else {
			c.msgs = append(c.msgs, m)
		}
		c.cond.Broadcast()
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *testClient) waitFor(what string, match func(*message) bool) *message {
	c.t.Helper()
	timer := time.AfterFunc(waitLimit, func() {
		c.mu.Lock()
		c.expired = true
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer timer.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for _, m := range c.msgs {
			if match(m) {
				return m
			}
		}
		if c.expired {
			c.t.Fatalf("no %s from the proxy", what)
		}
		c.cond.Wait()
	}
}

func (c *testClient) call(method string, params any) json.RawMessage {
	c.t.Helper()
	m := c.callMessage(method, params)
	if len(m.Error) > 0 {
		c.t.Fatalf("%s failed: %s", method, m.Error)
	}
	return m.Result
}

func (c *testClient) callMessage(method string, params any) *message {
	c.t.Helper()
	return c.await(method, c.request(method, params))
}

func (c *testClient) request(method string, params any) json.RawMessage {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := json.RawMessage(fmt.Sprint(c.nextID))
	c.mu.Unlock()
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.w.write(&message{ID: id, Method: method, Params: raw}); err != nil {
		c.t.Fatalf("sending %s: %v", method, err)
	}
	return id
}

func (c *testClient) await(method string, id json.RawMessage) *message {
	c.t.Helper()
	return c.waitFor("response to "+method, func(m *message) bool {
		return m.isResponse() && bytes.Equal(m.ID, id)
	})
}

func (c *testClient) notify(method string, params any) {
	c.t.Helper()
	if err := c.w.notify(method, params); err != nil {
		c.t.Fatalf("sending %s: %v", method, err)
	}
}

func (c *testClient) notification(accept func(map[string]any) bool) map[string]any {
	c.t.Helper()
	var found map[string]any
	c.waitFor("textDocument/publishDiagnostics", func(m *message) bool {
		if m.Method != "textDocument/publishDiagnostics" {
			return false
		}
		v, err := decode(m.Params)
		root, _ := v.(map[string]any)
		if err != nil || root == nil || !accept(root) {
			return false
		}
		found = root
		return true
	})
	return found
}

func (c *testClient) shutdown() error {
	c.t.Helper()
	c.call("shutdown", nil)
	c.notify("exit", nil)
	return c.wait()
}

type fixture struct {
	t      *testing.T
	root   string
	comp   *fakeCompiler
	log    string
	client *testClient
}

type fixtureOptions struct {
	Script func(root string) fakeScript

	Tune func(f *fixture, o *Options)
}

func newFixture(t *testing.T, files map[string]string, fo fixtureOptions) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	var script fakeScript
	if fo.Script != nil {
		script = fo.Script(root)
	}
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "script.json")
	if err := os.WriteFile(scriptPath, data, 0o666); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, comp: newFakeCompiler(), log: filepath.Join(scratch, "gopls.log")}
	opts := Options{
		Gopls:    []string{os.Args[0], "-mode=stdio"},
		Env:      append(os.Environ(), fakeEnv+"="+scriptPath, fakeLogEnv+"="+f.log),
		Compiler: f.comp,
	}
	if fo.Tune != nil {
		fo.Tune(f, &opts)
	}
	f.client = startProxy(t, opts)
	f.initialize()
	return f
}

func (f *fixture) initialize() {
	f.t.Helper()
	f.client.call("initialize", map[string]any{
		"processId": nil,
		"rootUri":   pathURI(f.root),
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
		},
	})
	f.client.notify("initialized", map[string]any{})
}

func (f *fixture) path(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func (f *fixture) uri(rel string) string { return pathURI(f.path(rel)) }

func (f *fixture) virtURI(rel string) string { return pathURI(virtualPath(f.path(rel))) }

func (f *fixture) open(rel string, version int) {
	f.t.Helper()
	data, err := os.ReadFile(f.path(rel))
	if err != nil {
		f.t.Fatal(err)
	}
	f.openText(rel, version, string(data))
}

func (f *fixture) openText(rel string, version int, text string) {
	f.t.Helper()
	f.client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": f.uri(rel), "languageId": "logos", "version": version, "text": text},
	})
}

func (f *fixture) sync() {
	f.t.Helper()
	f.client.call("test/sync", map[string]any{})
}

func (f *fixture) received() []*message {
	f.t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	var msgs []*message
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		m := new(message)
		if err := json.Unmarshal(line, m); err != nil {
			f.t.Fatalf("bad log line %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func (f *fixture) receivedMethod(method string) []map[string]any {
	f.t.Helper()
	var out []map[string]any
	for _, m := range f.received() {
		if m.Method != method {
			continue
		}
		v, err := decode(m.Params)
		root, _ := v.(map[string]any)
		if err != nil || root == nil {
			f.t.Fatalf("params of %s: %v", method, err)
		}
		out = append(out, root)
	}
	return out
}

func (f *fixture) workspaceFiles() []string {
	f.t.Helper()
	var files []string
	err := filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(f.root, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func toJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sameJSON(t *testing.T, what string, got json.RawMessage, want any) {
	t.Helper()
	g, err := decode(got)
	if err != nil {
		t.Fatalf("%s: %v (%s)", what, err, got)
	}
	w, err := decode(toJSON(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, toJSON(t, want))
	}
}

func positionParams(uri string, p Position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}
}

func forwardedPosition(t *testing.T, f *fixture, method string, nth int) (string, Position) {
	t.Helper()
	reqs := f.receivedMethod(method)
	if len(reqs) <= nth {
		t.Fatalf("gopls got %d %s requests, want more than %d", len(reqs), method, nth)
	}
	td, _ := reqs[nth]["textDocument"].(map[string]any)
	uri, _ := td["uri"].(string)
	pos, ok := asPosition(reqs[nth]["position"].(map[string]any))
	if !ok {
		t.Fatalf("%s forwarded without a position: %v", method, reqs[nth])
	}
	return uri, pos
}

func TestProxyMapsRequestsAndResponses(t *testing.T) {
	low, _, _ := lowerQuestion([]byte(aLgo))
	lowText := string(low)

	origOperand := shift(findPos(t, aLgo, "ReadFile(path)?", 0), len("ReadFile("))
	lowOperand := shift(findPos(t, lowText, "ReadFile(path)", 0), len("ReadFile("))
	origQ := findPos(t, aLgo, "?", 0)
	origCall := span(findPos(t, aLgo, "os.ReadFile", 0), len("os.ReadFile"))
	lowCall := span(findPos(t, lowText, "os.ReadFile", 0), len("os.ReadFile"))
	origRead := span(findPos(t, aLgo, "Read(", 0), len("Read"))
	lowRead := span(findPos(t, lowText, "Read(", 0), len("Read"))
	origData := findPos(t, aLgo, "data", 0)
	lowDataStart := findPos(t, lowText, "data", 0)
	lowData := shift(lowDataStart, len("data"))
	genRange := Range{Start: Position{Line: 6, Character: 0}, End: Position{Line: 7, Character: len(lineOf(lowText, 7))}}
	goFile := Range{Start: Position{Line: 3, Character: 5}, End: Position{Line: 3, Character: 9}}

	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(root string) fakeScript {
			virt := pathURI(filepath.Join(root, "a", "a.go"))
			other := pathURI(filepath.Join(root, "b", "b.go"))
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/hover": toJSON(t, map[string]any{
					"contents": map[string]any{"kind": "markdown", "value": "func ReadFile"},
					"range":    lowCall,
				}),
				"textDocument/definition": toJSON(t, []any{
					map[string]any{"uri": virt, "range": lowRead},
					map[string]any{"uri": other, "range": goFile},
				}),
				"textDocument/references": toJSON(t, []any{
					map[string]any{"uri": virt, "range": lowRead},
					map[string]any{"uri": virt, "range": span(lowDataStart, len("data"))},
					map[string]any{"uri": other, "range": goFile},
				}),
				"textDocument/completion": toJSON(t, map[string]any{
					"isIncomplete": false,
					"items": []any{map[string]any{"label": "ReadFile", "textEdit": map[string]any{
						"range": span(lowOperand, len("path")), "newText": "ReadFile",
					}}},
				}),
				"textDocument/codeAction": toJSON(t, []any{map[string]any{
					"title": "fix", "kind": "quickfix",
					"edit": map[string]any{
						"changes": map[string]any{virt: []any{map[string]any{"range": span(lowOperand, len("path")), "newText": "p"}}},
						"documentChanges": []any{map[string]any{
							"textDocument": map[string]any{"uri": virt, "version": 4},
							"edits":        []any{map[string]any{"range": lowCall, "newText": "io.ReadFile"}},
						}},
					},
				}}),
				"textDocument/inlayHint": toJSON(t, []any{
					map[string]any{"position": lowData, "label": ": []byte"},
				}),
				"textDocument/documentHighlight": toJSON(t, []any{
					map[string]any{"range": genRange, "kind": 1},
				}),
			}}
		},
	})
	f.open("a/a.lgo", 3)
	f.sync()
	aURI := f.uri("a/a.lgo")
	opened := f.receivedMethod("textDocument/didOpen")
	if len(opened) != 1 {
		t.Fatalf("gopls got %d didOpen messages, want one for the lowered document: %v", len(opened), opened)
	}
	doc, _ := opened[0]["textDocument"].(map[string]any)
	if doc["uri"] != f.virtURI("a/a.lgo") || doc["languageId"] != "go" {
		t.Errorf("lowered document opened as %v, want %s with lowered Go text", doc, f.virtURI("a/a.lgo"))
	}
	fwdText, _ := doc["text"].(string)
	if !strings.Contains(fwdText, "os.ReadFile(path)") || strings.Contains(fwdText, "ReadFile(path)?") {
		t.Errorf("gopls got the lowered text %q, want the lowered call without the ? marker", fwdText)
	}

	t.Run("hover on an operand inside the ? statement", func(t *testing.T) {
		res := f.client.call("textDocument/hover", positionParams(aURI, origOperand))
		uri, pos := forwardedPosition(t, f, "textDocument/hover", 0)
		if uri != f.virtURI("a/a.lgo") {
			t.Errorf("forwarded hover = %s, want %s", uri, f.virtURI("a/a.lgo"))
		}
		if got := lineOf(fwdText, pos.Line)[pos.Character:]; !strings.HasPrefix(got, "path)") {
			t.Errorf("forwarded hover at %v reaches %q, want the path operand", pos, got)
		}
		sameJSON(t, "hover result", res, map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": "func ReadFile"},
			"range":    origCall,
		})
	})
	t.Run("hover on the ?", func(t *testing.T) {
		f.client.call("textDocument/hover", positionParams(aURI, origQ))
		_, pos := forwardedPosition(t, f, "textDocument/hover", 1)
		if pos == origQ {
			t.Errorf("hover on the ? stayed at %v, want a position inside the lowered expansion", pos)
		}
	})
	t.Run("definition crosses files", func(t *testing.T) {
		res := f.client.call("textDocument/definition", positionParams(aURI, origOperand))
		sameJSON(t, "definition result", res, []any{
			map[string]any{"uri": aURI, "range": origRead},
			map[string]any{"uri": f.uri("b/b.go"), "range": goFile},
		})
	})
	t.Run("references", func(t *testing.T) {
		res := f.client.call("textDocument/references", map[string]any{
			"textDocument": map[string]any{"uri": aURI}, "position": origOperand,
			"context": map[string]any{"includeDeclaration": true},
		})
		sameJSON(t, "references result", res, []any{
			map[string]any{"uri": aURI, "range": origRead},
			map[string]any{"uri": aURI, "range": span(origData, len("data"))},
			map[string]any{"uri": f.uri("b/b.go"), "range": goFile},
		})
		reqs := f.receivedMethod("textDocument/references")
		if ctx, _ := reqs[0]["context"].(map[string]any); ctx["includeDeclaration"] != true {
			t.Errorf("references context was not passed on: %v", reqs[0])
		}
	})
	t.Run("completion", func(t *testing.T) {
		res := f.client.call("textDocument/completion", positionParams(aURI, origOperand))
		sameJSON(t, "completion result", res, map[string]any{
			"isIncomplete": false,
			"items": []any{map[string]any{"label": "ReadFile", "textEdit": map[string]any{
				"range": span(origOperand, len("path")), "newText": "ReadFile",
			}}},
		})
	})
	t.Run("codeAction maps the range, the diagnostics, and the edit", func(t *testing.T) {
		res := f.client.call("textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": aURI},
			"range":        span(origOperand, len("path")),
			"context": map[string]any{"diagnostics": []any{map[string]any{
				"range": span(origOperand, len("path")), "message": "bad",
			}}},
		})
		req := f.receivedMethod("textDocument/codeAction")[0]
		r, _ := asRange(req["range"].(map[string]any))
		if got := lineOf(fwdText, r.Start.Line)[r.Start.Character:]; !strings.HasPrefix(got, "path)") {
			t.Errorf("code action range starts at %q, want the path operand", got)
		}
		ctx, _ := req["context"].(map[string]any)
		diags, _ := ctx["diagnostics"].([]any)
		dr, _ := asRange(diags[0].(map[string]any)["range"].(map[string]any))
		if got := lineOf(fwdText, dr.Start.Line)[dr.Start.Character:]; !strings.HasPrefix(got, "path)") {
			t.Errorf("diagnostic range starts at %q, want the path operand", got)
		}
		sameJSON(t, "codeAction result", res, []any{map[string]any{
			"title": "fix", "kind": "quickfix",
			"edit": map[string]any{
				"changes": map[string]any{aURI: []any{map[string]any{"range": span(origOperand, len("path")), "newText": "p"}}},
				"documentChanges": []any{map[string]any{
					"textDocument": map[string]any{"uri": aURI, "version": nil},
					"edits":        []any{map[string]any{"range": origCall, "newText": "io.ReadFile"}},
				}},
			},
		}})
	})
	t.Run("inlayHint", func(t *testing.T) {
		res := f.client.call("textDocument/inlayHint", map[string]any{
			"textDocument": map[string]any{"uri": aURI},
			"range":        Range{Start: Position{Line: 0}, End: Position{Line: 8}},
		})
		sameJSON(t, "inlayHint result", res, []any{
			map[string]any{"position": shift(origData, len("data")), "label": ": []byte"},
		})
	})
	t.Run("a range over the generated lines maps to the ? token", func(t *testing.T) {
		res := f.client.call("textDocument/documentHighlight", positionParams(aURI, origQ))
		sameJSON(t, "documentHighlight result", res, []any{
			map[string]any{"range": span(origQ, 1), "kind": 1},
		})
	})
	t.Run("nothing is written to the workspace", func(t *testing.T) {
		if got := f.workspaceFiles(); !slices.Equal(got, []string{"a/a.lgo"}) {
			t.Errorf("workspace files = %v, want only a/a.lgo", got)
		}
	})
	if err := f.client.shutdown(); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
}

func TestProxyHandshakeShapesCapabilities(t *testing.T) {
	f := newFixture(t, nil, fixtureOptions{})
	inits := f.receivedMethod("initialize")
	if len(inits) != 1 {
		t.Fatalf("gopls got %d initialize requests, want 1", len(inits))
	}
	caps, _ := inits[0]["capabilities"].(map[string]any)
	general, _ := caps["general"].(map[string]any)
	if _, ok := general["positionEncodings"]; ok {
		t.Errorf("positionEncodings reached gopls: %v", general)
	}
	res := f.client.call("initialize", map[string]any{"capabilities": map[string]any{}})
	var got struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Capabilities["diagnosticProvider"]; ok {
		t.Errorf("diagnosticProvider reached the client: %v", got.Capabilities)
	}
	if _, ok := got.Capabilities["hoverProvider"]; !ok {
		t.Errorf("other capabilities were dropped: %v", got.Capabilities)
	}
}

func TestProxyEditsUpdateOverlay(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{})
	f.open("a/a.lgo", 1)
	aPath, aURI := f.path("a/a.lgo"), f.uri("a/a.lgo")

	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": aURI, "version": 2},
		"contentChanges": []any{map[string]any{
			"range": Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 0}},
			"text":  "// edited é\n",
		}},
	})
	edited := strings.Replace(aLgo, "import \"os\"\n", "// edited é\nimport \"os\"\n", 1)

	operand := shift(findPos(t, edited, "ReadFile(path)?", 0), len("ReadFile("))
	f.client.call("textDocument/hover", positionParams(aURI, operand))

	opened := f.receivedMethod("textDocument/didOpen")
	if len(opened) != 1 {
		t.Fatalf("gopls got %d lowered didOpen messages, want the one of the edited text", len(opened))
	}
	doc, _ := opened[0]["textDocument"].(map[string]any)
	if doc["uri"] != f.virtURI("a/a.lgo") {
		t.Errorf("didOpen sent for %v, want the lowered document", doc["uri"])
	}
	fwdText, _ := doc["text"].(string)
	if !strings.Contains(fwdText, "// edited é") {
		t.Errorf("gopls got the lowered text %q, want the edit in it", fwdText)
	}
	var order []string
	for _, m := range f.received() {
		switch m.Method {
		case "textDocument/didOpen", "textDocument/hover":
			order = append(order, m.Method)
		}
	}
	if want := []string{"textDocument/didOpen", "textDocument/hover"}; !slices.Equal(order, want) {
		t.Fatalf("gopls got %v, want the lowered document before the request: %v", order, want)
	}
	_, pos := forwardedPosition(t, f, "textDocument/hover", 0)
	lines := strings.Split(fwdText, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 || pos.Character > len(lines[pos.Line]) {
		t.Fatalf("hover forwarded at %v, outside the lowered text", pos)
	}
	if got := lines[pos.Line][pos.Character:]; !strings.HasPrefix(got, "path)") {
		t.Errorf("hover forwarded at %v reaches %q, want the path operand of the new lowering", pos, got)
	}
	calls := f.comp.bufferCalls()
	if last := calls[len(calls)-1]; string(last[aPath]) != edited {
		t.Errorf("the compiler read %q for the document, want the unsaved text", last[aPath])
	}
	onDisk, _ := os.ReadFile(aPath)
	if string(onDisk) != aLgo {
		t.Errorf("the file on disk changed to %q", onDisk)
	}
}

func TestProxyStaleLoweringAnswersNull(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(root string) fakeScript {
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/hover": toJSON(t, map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "x"}}),
			}}
		},
	})
	f.open("a/a.lgo", 1)
	aURI := f.uri("a/a.lgo")
	p := Position{Line: 5, Character: 3}

	if res := f.client.call("textDocument/hover", positionParams(aURI, p)); string(res) == "null" {
		t.Fatal("hover on a lowered document answered null")
	}
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": aURI, "version": 2},
		"contentChanges": []any{map[string]any{"text": aLgo + "// SYNTAX ERROR\n"}},
	})
	if res := f.client.call("textDocument/hover", positionParams(aURI, p)); string(res) != "null" {
		t.Errorf("hover on a stale lowering answered %s, want null", res)
	}
	if got := len(f.receivedMethod("textDocument/hover")); got != 1 {
		t.Errorf("gopls got %d hover requests, want the stale one withheld", got)
	}
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": aURI, "version": 3},
		"contentChanges": []any{map[string]any{"text": aLgo}},
	})
	if res := f.client.call("textDocument/hover", positionParams(aURI, p)); string(res) == "null" {
		t.Error("hover stayed null after the text compiled again")
	}
}

func TestProxySnapshotCountsStayAtOne(t *testing.T) {
	const otherLgo = "package a\n\nfunc Other() int { return 1 }\n"
	const secondLgo = "package b\n\nfunc Second() int { return 2 }\n"
	f := newFixture(t, map[string]string{
		"a/a.lgo": aLgo,
		"b/c.lgo": secondLgo,
	}, fixtureOptions{})
	f.open("a/a.lgo", 1)
	f.open("b/c.lgo", 1)
	want := map[string]int{f.path("a"): 1, f.path("b"): 1}
	counts := func() map[string]int {
		t.Helper()
		var got map[string]int
		if err := json.Unmarshal(f.client.call("logos/snapshotCounts", map[string]any{}), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := counts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot counts = %v, want %v", got, want)
	}
	change := func(rel, text string, version int) {
		t.Helper()
		f.client.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": f.uri(rel), "version": version},
			"contentChanges": []any{map[string]any{"text": text}},
		})
		if got := counts(); !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot counts after %s version %d = %v, want %v", rel, version, got, want)
		}
	}
	change("a/a.lgo", aLgo+"// edit one\n", 2)
	change("a/a.lgo", aLgo+"// edit two\n", 3)
	if err := os.WriteFile(f.path("a/b.lgo"), []byte(otherLgo), 0o666); err != nil {
		t.Fatal(err)
	}
	f.open("a/b.lgo", 1)
	if got := counts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot counts after adding a/b.lgo = %v, want %v", got, want)
	}
	change("a/b.lgo", otherLgo+"// edit three\n", 2)
	change("b/c.lgo", secondLgo+"// edit four\n", 2)
}

func TestProxyMergesDiagnostics(t *testing.T) {
	var lowCall Range
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(root string) fakeScript {
			low, _, _ := lowerQuestion([]byte(aLgo))
			lowCall = span(findPos(t, string(low), "os.ReadFile", 0), len("os.ReadFile"))
			virt := pathURI(filepath.Join(root, "a", "a.go"))
			return fakeScript{Diagnostics: map[string]json.RawMessage{virt: toJSON(t, []any{
				map[string]any{"range": lowCall, "severity": 1, "message": "gopls: bad call"},
				map[string]any{
					"range":    Range{Start: Position{Line: 6, Character: 1}, End: Position{Line: 6, Character: 5}},
					"severity": 2, "message": "gopls: inside the expansion",
				},
				map[string]any{
					"range":    Range{Start: Position{Line: 6, Character: 0}, End: Position{Line: 7, Character: len(lineOf(string(low), 7))}},
					"severity": 2, "message": "gopls: across the expansion",
				},
			})}}
		},
	})
	aPath := f.path("a/a.lgo")
	callLine, callCol := 6, strings.Index(lineOf(aLgo, 5), "os.ReadFile")+1
	f.comp.setDiagnostics(filepath.Dir(aPath),
		compilerDiag(t, diagSpec{Path: aPath, Severity: "deny", Rule: "correctness::example", Message: "compiler: unproven",
			Start: [2]int{callLine, callCol}, End: [2]int{callLine, callCol + len("os.ReadFile")}}),
		compilerDiag(t, diagSpec{Path: aPath, Severity: "deny", Message: "gopls: bad call",
			Start: [2]int{callLine, callCol}, End: [2]int{callLine, callCol + len("os.ReadFile")}}),
		compilerDiag(t, diagSpec{Path: aPath, Severity: "warn", Rule: "style::note", Message: "compiler: warning",
			Start: [2]int{3, 1}, End: [2]int{3, 7}}),
	)
	f.open("a/a.lgo", 7)
	f.client.call("textDocument/hover", positionParams(f.uri("a/a.lgo"), Position{}))

	params := f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		return m["uri"] == f.uri("a/a.lgo") && len(items) == 5
	})
	if v, _ := number(params["version"]); v != 7 {
		t.Errorf("diagnostics published for version %v, want the client's version 7", params["version"])
	}
	type diag struct {
		severity int
		start    Position
		end      Position
		code     string
	}
	got := map[string]diag{}
	for _, item := range params["diagnostics"].([]any) {
		m := item.(map[string]any)
		r, _ := asRange(m["range"].(map[string]any))
		sev, _ := number(m["severity"])
		code, _ := m["code"].(string)
		got[m["message"].(string)] = diag{sev, r.Start, r.End, code}
	}
	origCall := span(findPos(t, aLgo, "os.ReadFile", 0), len("os.ReadFile"))
	origQ := findPos(t, aLgo, "?", 0)
	want := map[string]diag{
		"gopls: bad call":             {1, origCall.Start, origCall.End, ""},
		"gopls: inside the expansion": {2, origQ, shift(origQ, 1), ""},
		"gopls: across the expansion": {2, origQ, shift(origQ, 1), ""},
		"compiler: unproven":          {1, origCall.Start, origCall.End, "correctness::example"},
		"compiler: warning":           {2, Position{Line: 2, Character: 0}, Position{Line: 2, Character: 6}, "style::note"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics:\n got %v\nwant %v", got, want)
	}
}

func TestProxyDiagnosticsFollowEdits(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{})
	aPath := f.path("a/a.lgo")
	f.comp.setDiagnostics(filepath.Dir(aPath),
		compilerDiag(t, diagSpec{Path: aPath, Severity: "deny", Message: "first", Start: [2]int{1, 1}, End: [2]int{1, 8}}))
	f.open("a/a.lgo", 1)
	f.client.call("textDocument/hover", positionParams(f.uri("a/a.lgo"), Position{}))
	f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		return len(items) == 1
	})
	f.comp.setDiagnostics(filepath.Dir(aPath))
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": f.uri("a/a.lgo"), "version": 2},
		"contentChanges": []any{map[string]any{"text": aLgo + "\n"}},
	})
	f.client.call("textDocument/hover", positionParams(f.uri("a/a.lgo"), Position{}))
	f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		v, _ := number(m["version"])
		return len(items) == 0 && v == 2
	})
}

func TestProxyMixedWorkspaceDefinition(t *testing.T) {
	const libLgo = "package lib\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n"
	const appGo = "package app\n\nimport \"example.com/m/lib\"\n\nvar Greeting = lib.Hello()\n"
	files := map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.27\n",
		"lib/lib.lgo": libLgo,
		"app/app.go":  appGo,
	}
	helloOrig := span(findPos(t, libLgo, "Hello", 0), len("Hello"))

	f := newFixture(t, files, fixtureOptions{
		Script: func(root string) fakeScript {
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/definition": toJSON(t, []any{
					map[string]any{"uri": pathURI(filepath.Join(root, "lib", "lib.go")), "range": helloOrig},
				}),
			}}
		},
		Tune: func(f *fixture, o *Options) {
			f.comp.setClosure(f.path("app"), f.path("lib"))
		},
	})
	before := f.workspaceFiles()

	f.open("app/app.go", 1)
	f.sync()
	opened := f.receivedMethod("textDocument/didOpen")
	var uris []string
	for _, o := range opened {
		td, _ := o["textDocument"].(map[string]any)
		uris = append(uris, td["uri"].(string))
	}
	slices.Sort(uris)
	if want := []string{f.uri("app/app.go"), f.virtURI("lib/lib.lgo")}; !slices.Equal(uris, want) {
		t.Fatalf("gopls opened %v, want the Go document and the lowered loGo/s dependency %v", uris, want)
	}
	for _, o := range opened {
		td, _ := o["textDocument"].(map[string]any)
		switch td["uri"] {
		case f.uri("app/app.go"):
			if td["text"] != appGo {
				t.Errorf("gopls opened the Go document with %q, want the disk bytes", td["text"])
			}
		case f.virtURI("lib/lib.lgo"):
			if text, _ := td["text"].(string); !strings.Contains(text, "func Hello() string") {
				t.Errorf("gopls opened the lowered dependency with %q, want the lowered lib", text)
			}
		default:
			t.Errorf("gopls opened unexpected %v: no virtual document may exist for a Go file", td["uri"])
		}
	}

	appCall := shift(findPos(t, appGo, "lib.Hello", 0), len("lib."))
	res := f.client.call("textDocument/definition", positionParams(f.uri("app/app.go"), appCall))
	if uri, pos := forwardedPosition(t, f, "textDocument/definition", 0); uri != f.uri("app/app.go") || pos != appCall {
		t.Errorf("a Go document request was forwarded as %s %v, want it unchanged", uri, pos)
	}
	sameJSON(t, "definition of a loGo/s symbol from a Go file", res, []any{
		map[string]any{"uri": f.uri("lib/lib.lgo"), "range": helloOrig},
	})

	if after := f.workspaceFiles(); !slices.Equal(before, after) {
		t.Errorf("workspace files changed from %v to %v", before, after)
	}
}

func TestProxyGoDefinitionFromLogosFile(t *testing.T) {
	const app2Lgo = "package app2\n\nimport \"example.com/m/golib\"\n\nvar N = golib.Thing()\n"
	const goLib = "package golib\n\nfunc Thing() int { return 1 }\n"
	thing := Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 10}}
	f := newFixture(t, map[string]string{
		"go.mod":        "module example.com/m\n\ngo 1.27\n",
		"golib/g.go":    goLib,
		"app2/app2.lgo": app2Lgo,
	}, fixtureOptions{
		Script: func(root string) fakeScript {
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/definition": toJSON(t, []any{
					map[string]any{"uri": pathURI(filepath.Join(root, "golib", "g.go")), "range": thing},
				}),
			}}
		},
		Tune: func(f *fixture, o *Options) {
			f.comp.setClosure(f.path("app2"), f.path("golib"))
		},
	})
	before := f.workspaceFiles()
	f.open("app2/app2.lgo", 1)
	call := shift(findPos(t, app2Lgo, "golib.Thing", 0), len("golib."))
	res := f.client.call("textDocument/definition", positionParams(f.uri("app2/app2.lgo"), call))
	sameJSON(t, "definition of a Go symbol from a loGo/s file", res, []any{
		map[string]any{"uri": f.uri("golib/g.go"), "range": thing},
	})
	if uri, _ := forwardedPosition(t, f, "textDocument/definition", 0); uri != f.virtURI("app2/app2.lgo") {
		t.Errorf("the loGo/s request went to %s, want the lowered document", uri)
	}
	if after := f.workspaceFiles(); !slices.Equal(before, after) {
		t.Errorf("workspace files changed from %v to %v", before, after)
	}
}

func TestProxyClosesLoweredDocuments(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{})
	aPath := f.path("a/a.lgo")
	f.comp.setDiagnostics(filepath.Dir(aPath),
		compilerDiag(t, diagSpec{Path: aPath, Severity: "deny", Message: "m", Start: [2]int{1, 1}, End: [2]int{1, 2}}))
	f.open("a/a.lgo", 1)
	f.client.call("textDocument/hover", positionParams(f.uri("a/a.lgo"), Position{}))
	f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		return len(items) == 1
	})
	f.client.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": f.uri("a/a.lgo")}})
	f.client.call("textDocument/hover", positionParams(f.uri("b/b.go"), Position{}))
	closed := f.receivedMethod("textDocument/didClose")
	if len(closed) != 1 {
		t.Fatalf("gopls got %d didClose messages, want 1", len(closed))
	}
	if td, _ := closed[0]["textDocument"].(map[string]any); td["uri"] != f.virtURI("a/a.lgo") {
		t.Errorf("gopls closed %v, want the lowered document", td["uri"])
	}
	f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		return m["uri"] == f.uri("a/a.lgo") && len(items) == 0
	})
}

func TestProxyAnswersRequestsInFlightWhenClientCloses(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("closing gate listener: %v", err)
		}
	}()
	if err := ln.SetDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatalf("arming the gate accept: %v", err)
	}
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(string) fakeScript {
			return fakeScript{
				HoldMethod: "textDocument/hover",
				HoldAddr:   ln.Addr().String(),
				Responses:  map[string]json.RawMessage{"textDocument/hover": toJSON(t, map[string]any{"contents": "late"})},
			}
		},
	})
	f.open("a/a.lgo", 1)
	raw := toJSON(t, positionParams(f.uri("a/a.lgo"), Position{Line: 5, Character: 3}))
	if err := f.client.w.write(&message{ID: json.RawMessage("99"), Method: "textDocument/hover", Params: raw}); err != nil {
		t.Fatal(err)
	}
	gate, err := ln.Accept()
	if err != nil {
		t.Fatalf("the held backend never took the hover: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("closing gate conn: %v", err)
		}
	}()
	if err := gate.SetWriteDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatalf("arming the gate write: %v", err)
	}
	f.client.closeW()
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatalf("releasing the held backend: %v", err)
	}
	m := f.client.waitFor("the late hover response", func(m *message) bool { return m.isResponse() && string(m.ID) == "99" })
	if !strings.Contains(string(m.Result), "late") {
		t.Errorf("hover result = %s, want the scripted answer", m.Result)
	}
	if err := f.client.wait(); err != nil {
		t.Errorf("Serve = %v, want nil after the client closed its output", err)
	}
	if !f.comp.isClosed() {
		t.Error("the compiler was not released")
	}
}

func TestServeExitWithoutShutdown(t *testing.T) {
	f := newFixture(t, nil, fixtureOptions{})
	f.client.notify("exit", nil)
	if err := f.client.wait(); err == nil || !strings.Contains(err.Error(), "exit without shutdown") {
		t.Errorf("Serve = %v, want the exit without shutdown error", err)
	}
}

func TestServeReportsGoplsDeath(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(string) fakeScript { return fakeScript{CrashOn: "textDocument/hover"} },
	})
	f.open("a/a.lgo", 1)
	raw := toJSON(t, positionParams(f.uri("a/a.lgo"), Position{Line: 5, Character: 3}))
	if err := f.client.w.write(&message{ID: json.RawMessage("5"), Method: "textDocument/hover", Params: raw}); err != nil {
		t.Fatal(err)
	}
	if err := f.client.wait(); err == nil || !strings.Contains(err.Error(), "gopls exited") {
		t.Errorf("Serve = %v, want an error naming the gopls exit", err)
	}
}
func TestServePrefersClientCloseOverGoplsExit(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		for i := range 50 {
			f := newFixture(t, nil, fixtureOptions{})
			if err := f.client.shutdown(); err != nil {
				t.Fatalf("iteration %d: Serve = %v, want nil", i, err)
			}
		}
	})
	t.Run("death", func(t *testing.T) {
		f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
			Script: func(string) fakeScript { return fakeScript{CrashOn: "textDocument/hover"} },
		})
		f.open("a/a.lgo", 1)
		raw := toJSON(t, positionParams(f.uri("a/a.lgo"), Position{Line: 5, Character: 3}))
		if err := f.client.w.write(&message{ID: json.RawMessage("5"), Method: "textDocument/hover", Params: raw}); err != nil {
			t.Fatal(err)
		}
		if err := f.client.wait(); err == nil || !strings.Contains(err.Error(), "gopls exited") {
			t.Errorf("Serve = %v, want an error naming the gopls exit", err)
		}
	})
}

func TestServeRejectsMissingParts(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Options{})
	if err == nil {
		t.Error("Serve accepted options with no gopls and no compiler")
	}
	err = Serve(context.Background(), strings.NewReader(""), io.Discard, Options{
		Gopls: []string{filepath.Join(t.TempDir(), "no-such-gopls")}, Compiler: newFakeCompiler(),
	})
	if err == nil || !strings.Contains(err.Error(), "starting gopls") {
		t.Errorf("Serve with an unstartable gopls = %v, want a starting gopls error", err)
	}
}

func TestGoplsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := goplsCommand()
	if err == nil || err.Error() != "logos lsp: gopls not found on PATH" {
		t.Errorf("goplsCommand() error = %v, want %q", err, "logos lsp: gopls not found on PATH")
	}
	dir := t.TempDir()
	name := "gopls"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	cmd, err := goplsCommand()
	if err != nil {
		t.Skipf("a script named gopls is not executable here: %v", err)
	}
	if cmd[0] != filepath.Join(dir, name) || !slices.Contains(cmd, "-mode=stdio") {
		t.Errorf("goplsCommand() = %v, want the gopls of PATH with -mode=stdio", cmd)
	}
}

func TestGoplsEnvPutsToolchainFirst(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := goplsEnv([]string{"HOME=/h", "PATH=/usr/bin" + sep + "/bin", "X=1"}, "/goroot/bin")
	want := []string{"HOME=/h", "X=1", "PATH=/goroot/bin" + sep + "/usr/bin" + sep + "/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("goplsEnv = %v, want %v", got, want)
	}
	if got := goplsEnv(nil, "/goroot/bin"); !slices.Equal(got, []string{"PATH=/goroot/bin"}) {
		t.Errorf("goplsEnv with no PATH = %v, want a PATH of the toolchain alone", got)
	}
}

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	w := &writer{w: &buf}
	if err := w.notify("a/b", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.fail(json.RawMessage("7"), -32601, "nope"); err != nil {
		t.Fatal(err)
	}
	rd := newReader(&buf)
	m, err := rd.read()
	if err != nil || m.Method != "a/b" || m.isRequest() || m.isResponse() {
		t.Fatalf("first message = %+v, %v", m, err)
	}
	m, err = rd.read()
	if err != nil || !m.isResponse() || string(m.ID) != "7" || !strings.Contains(string(m.Error), "nope") {
		t.Fatalf("second message = %+v, %v", m, err)
	}
	if _, err := rd.read(); !errors.Is(err, io.EOF) {
		t.Errorf("read at the end = %v, want io.EOF", err)
	}

	body := `{"jsonrpc":"2.0","method":"x"}`
	ok := fmt.Sprintf("content-type: application/vscode-jsonrpc\r\nCONTENT-LENGTH: %d\r\n\r\n%s", len(body), body)
	if m, err := newReader(strings.NewReader(ok)).read(); err != nil || m.Method != "x" {
		t.Errorf("framed with odd headers = %+v, %v", m, err)
	}
	for name, in := range map[string]string{
		"missing length": "X-Other: 1\r\n\r\n{}",
		"corrupt length": "Content-Length: abc\r\n\r\n{}",
		"negative":       "Content-Length: -4\r\n\r\n{}",
		"truncated":      "Content-Length: 99\r\n\r\n{}",
		"not json":       "Content-Length: 3\r\n\r\nabc",
		"cut header":     "Content-Length: 3",
	} {
		if _, err := newReader(strings.NewReader(in)).read(); err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%s: read = %v, want a framing error", name, err)
		}
	}
}

func TestProxyLeavesForeignJSONAlone(t *testing.T) {
	low, _, _ := lowerQuestion([]byte(aLgo))
	lowRead := span(findPos(t, string(low), "Read(", 0), len("Read"))
	foreign := map[string]any{"uri": "file:///foreign/x.go", "range": span(Position{Line: 5, Character: 5}, 3), "note": "keep"}
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(root string) fakeScript {
			virt := pathURI(filepath.Join(root, "a", "a.go"))
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/definition": toJSON(t, []any{
					map[string]any{"uri": virt, "range": lowRead},
					foreign,
				}),
				"textDocument/hover": toJSON(t, map[string]any{
					"contents": map[string]any{"kind": "plaintext", "value": "known"},
					"range":    lowRead,
					"extra":    map[string]any{"note": "keep"},
				}),
			}}
		},
	})
	f.open("a/a.lgo", 1)
	f.sync()
	aURI := f.uri("a/a.lgo")
	origOperand := shift(findPos(t, aLgo, "ReadFile(path)?", 0), len("ReadFile("))
	origRead := span(findPos(t, aLgo, "Read(", 0), len("Read"))
	res := f.client.call("textDocument/definition", positionParams(aURI, origOperand))
	sameJSON(t, "definition keeps the foreign location", res, []any{
		map[string]any{"uri": aURI, "range": origRead},
		foreign,
	})
	hres := f.client.call("textDocument/hover", positionParams(aURI, origOperand))
	sameJSON(t, "hover keeps the foreign field", hres, map[string]any{
		"contents": map[string]any{"kind": "plaintext", "value": "known"},
		"range":    origRead,
		"extra":    map[string]any{"note": "keep"},
	})
	if err := f.client.shutdown(); err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
}

func TestProxyMergesDiagnosticsOfGoDocuments(t *testing.T) {
	const src = "package p\n\nvar x int = \"s\"\n"
	f := newFixture(t, map[string]string{"p/p.go": src}, fixtureOptions{
		Script: func(root string) fakeScript {
			return fakeScript{Diagnostics: map[string]json.RawMessage{
				pathURI(filepath.Join(root, "p", "p.go")): toJSON(t, []any{map[string]any{
					"range":    span(Position{Line: 2, Character: 12}, 3),
					"severity": 1, "message": "cannot use \"s\" as int value",
				}}),
			}}
		},
	})
	pPath := f.path("p/p.go")
	f.comp.setDiagnostics(filepath.Dir(pPath),
		compilerDiag(t, diagSpec{Path: pPath, Severity: "deny", Message: "cannot use \"s\" as int value",
			Start: [2]int{3, 13}, End: [2]int{3, 16}}),
		compilerDiag(t, diagSpec{Path: pPath, Severity: "warn", Rule: "style::x", Message: "compiler: style",
			Start: [2]int{3, 5}, End: [2]int{3, 6}}),
	)
	f.open("p/p.go", 4)
	f.client.call("textDocument/hover", positionParams(f.uri("p/p.go"), Position{}))
	params := f.client.notification(func(m map[string]any) bool {
		items, _ := m["diagnostics"].([]any)
		return m["uri"] == f.uri("p/p.go") && len(items) == 2
	})
	if v, _ := number(params["version"]); v != 4 {
		t.Errorf("diagnostics published for version %v, want 4", params["version"])
	}
	var msgs []string
	for _, item := range params["diagnostics"].([]any) {
		msgs = append(msgs, item.(map[string]any)["message"].(string))
	}
	slices.Sort(msgs)
	if want := []string{"cannot use \"s\" as int value", "compiler: style"}; !slices.Equal(msgs, want) {
		t.Errorf("messages = %q, want %q", msgs, want)
	}
}

func TestWriterRespondsWithNullForNoResult(t *testing.T) {
	var buf bytes.Buffer
	if err := (&writer{w: &buf}).respond(json.RawMessage("3"), nil); err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":3,"result":null}`
	if want := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body); buf.String() != want {
		t.Errorf("response = %q, want %q", buf.String(), want)
	}
}

func TestContentLengthLimit(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{strconv.Itoa(maxMessage - 1), true},
		{strconv.Itoa(maxMessage), true},
		{strconv.Itoa(maxMessage + 1), false},
		{"0", true},
		{"-1", false},
		{"", false},
		{"12x", false},
	} {
		n, err := parseLength(c.in)
		if (err == nil) != c.ok {
			t.Errorf("parseLength(%q) = %d, %v, want ok = %v", c.in, n, err, c.ok)
		}
	}
	header := "Content-Length: " + strconv.Itoa(maxMessage+1) + "\r\n\r\n"
	if _, err := newReader(strings.NewReader(header)).read(); err == nil || !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Errorf("a body over the cap = %v, want the invalid Content-Length error before any allocation", err)
	}
}

func TestProxyCodeActionWithoutLoweringUsesCompiler(t *testing.T) {
	root, tc := realWorkspace(t)
	aPath := filepath.Join(root, filepath.FromSlash("lmod/a/a.lgo"))
	const denied = "package a\n\nfunc get() (int, error) { return 1, nil }\n\nfunc run() error {\n\tget()\n\treturn nil\n}\n"
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "script.json")
	scriptData, err := json.Marshal(fakeScript{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, scriptData, 0o666); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(scratch, "gopls.log")
	client := startProxy(t, Options{
		Gopls:       []string{os.Args[0], "-mode=stdio"},
		Env:         append(os.Environ(), fakeEnv+"="+scriptPath, fakeLogEnv+"="+logPath),
		Compiler:    tc,
		CodeActions: codeActionsAdapter,
	})
	client.call("initialize", map[string]any{
		"processId": nil,
		"rootUri":   pathURI(root),
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}},
		},
	})
	client.notify("initialized", map[string]any{})
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(aPath), "languageId": "logos", "version": 1, "text": denied},
	})
	call := findPos(t, denied, "get()", 1)
	rng := span(call, len("get()"))
	res := client.call("textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(aPath)},
		"range":        rng,
		"context":      map[string]any{"diagnostics": []any{}},
	})
	if string(res) == "null" {
		t.Fatal("codeAction on a denied source answered null, want the compiler autofix")
	}
	if !strings.Contains(string(res), "add ? to propagate the error") {
		t.Errorf("codeAction lacks the compiler autofix: %s", res)
	}
	if !strings.Contains(string(res), `get()?`) {
		t.Errorf("codeAction lacks the ? replacement: %s", res)
	}
	if data, err := os.ReadFile(logPath); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), `"method":"textDocument/codeAction"`) {
		t.Errorf("gopls got a codeAction request for a document with no lowering; the proxy must answer compiler actions directly")
	}
	fixed := strings.Replace(denied, "\tget()\n", "", 1)
	client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": pathURI(aPath), "version": 2},
		"contentChanges": []any{map[string]any{"text": fixed}},
	})
	res2 := client.call("textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(aPath)},
		"range":        rng,
		"context":      map[string]any{"diagnostics": []any{}},
	})
	if strings.Contains(string(res2), "add ? to propagate the error") {
		t.Errorf("a stale autofix crossed the edit: %s", res2)
	}
}

func TestProxyCodeActionInflightEditDropsWholeAnswer(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("closing gate listener: %v", err)
		}
	}()
	if err := ln.SetDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatalf("arming the gate accept: %v", err)
	}
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo, "b/b.lgo": "package b\n\nSYNTAX ERROR\n"}, fixtureOptions{
		Script: func(string) fakeScript {
			return fakeScript{
				HoldMethod: "textDocument/codeAction",
				HoldAddr:   ln.Addr().String(),
				Responses: map[string]json.RawMessage{
					"textDocument/codeAction": toJSON(t, []any{map[string]any{"title": "from gopls"}}),
				},
			}
		},
		Tune: func(f *fixture, o *Options) {
			o.CodeActions = codeActionsAdapter
		},
	})
	aPath := f.path("a/a.lgo")
	line := autofixLine(t, fixDiag{Schema: "1.0", File: aPath, Start: Position{Line: 5, Character: 1}, Len: 4, Description: "stale-proof fix", Replacement: "p"})
	d, ok := parseDiagnostic(line)
	if !ok {
		t.Fatalf("parseDiagnostic rejected %s", line)
	}
	f.comp.setDiagnostics(filepath.Dir(aPath), d)
	f.open("a/a.lgo", 1)
	f.open("b/b.lgo", 1)
	params := map[string]any{
		"textDocument": map[string]any{"uri": f.uri("a/a.lgo")},
		"range":        span(Position{Line: 5, Character: 1}, 4),
		"context":      map[string]any{"diagnostics": []any{}},
	}
	id := f.client.request("textDocument/codeAction", params)
	gate, err := ln.Accept()
	if err != nil {
		t.Fatalf("the held backend never dialed the gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("closing gate conn: %v", err)
		}
	}()
	if err := gate.SetWriteDeadline(time.Now().Add(waitLimit)); err != nil {
		t.Fatalf("arming the gate write: %v", err)
	}
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": f.uri("a/a.lgo"), "version": 2},
		"contentChanges": []any{map[string]any{"text": aLgo + "// crossed\n"}},
	})
	if res := f.client.call("textDocument/hover", positionParams(f.uri("b/b.lgo"), Position{Line: 0})); string(res) != "null" {
		t.Fatalf("hover on a source with no lowering = %s, want null", res)
	}
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatalf("releasing the held backend: %v", err)
	}
	stale := f.client.await("textDocument/codeAction", id)
	sameJSON(t, "stale codeAction crossed by an edit", stale.Result, []any{})
	fresh := f.client.call("textDocument/codeAction", params)
	if !strings.Contains(string(fresh), "from gopls") {
		t.Errorf("fresh codeAction lost the backend action: %s", fresh)
	}
	if !strings.Contains(string(fresh), "stale-proof fix") {
		t.Errorf("fresh codeAction lost the current autofix: %s", fresh)
	}
}

func TestProxyOneRunForTwoEditsBeforeHover(t *testing.T) {
	edited1 := aLgo + "// v2\n"
	edited2 := aLgo + "// v2\n// v3\n"
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{})
	f.open("a/a.lgo", 1)
	aURI, aPath := f.uri("a/a.lgo"), f.path("a/a.lgo")
	for i, text := range []string{edited1, edited2} {
		f.client.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": aURI, "version": i + 2},
			"contentChanges": []any{map[string]any{"text": text}},
		})
	}
	operand := shift(findPos(t, edited2, "ReadFile(path)?", 0), len("ReadFile("))
	f.client.call("textDocument/hover", positionParams(aURI, operand))

	calls := f.comp.lowerCalls()
	if len(calls) != 1 {
		t.Fatalf("the proxy compiled %d times for one open, two edits, and one hover, want the one run the hover asked for: %v", len(calls), calls)
	}
	buffers := f.comp.bufferCalls()
	if last := buffers[len(buffers)-1]; string(last[aPath]) != edited2 {
		t.Errorf("the one run lowered %q, want the text of version 3", last[aPath])
	}
	opened := f.receivedMethod("textDocument/didOpen")
	if len(opened) != 1 {
		t.Fatalf("gopls got %d lowered didOpen messages, want the one of the version 3 text", len(opened))
	}
	fwdText, _ := opened[0]["textDocument"].(map[string]any)["text"].(string)
	_, pos := forwardedPosition(t, f, "textDocument/hover", 0)
	lines := strings.Split(fwdText, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 || pos.Character > len(lines[pos.Line]) {
		t.Fatalf("hover forwarded at %v, outside the lowered text", pos)
	} else if got := lines[pos.Line][pos.Character:]; !strings.HasPrefix(got, "path)") {
		t.Errorf("the hover was mapped at %v reaching %q, want the path operand", pos, got)
	}
}

func TestProxyConcurrentHoversShareOneRun(t *testing.T) {
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(string) fakeScript {
			return fakeScript{DelayMillis: 50}
		},
	})
	f.open("a/a.lgo", 1)
	aURI := f.uri("a/a.lgo")
	p := Position{Line: 5, Character: 3}
	ids := []json.RawMessage{f.client.request("textDocument/hover", positionParams(aURI, p)), f.client.request("textDocument/hover", positionParams(aURI, p))}
	for _, id := range ids {
		f.client.await("textDocument/hover", id)
	}
	if got := len(f.comp.lowerCalls()); got != 1 {
		t.Errorf("two concurrent hovers caused %d lowerings, want the shared one", got)
	}
}

func TestProxyHoverAfterCloseSeesDiskText(t *testing.T) {
	unsaved := "// unsaved\n" + aLgo
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo}, fixtureOptions{
		Script: func(string) fakeScript {
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/hover": toJSON(t, map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "disk"}}),
			}}
		},
	})
	aPath, aURI := f.path("a/a.lgo"), f.uri("a/a.lgo")
	f.openText("a/a.lgo", 1, unsaved)
	f.client.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": aURI}})

	origOperand := shift(findPos(t, aLgo, "ReadFile(path)?", 0), len("ReadFile("))
	res := f.client.call("textDocument/hover", positionParams(aURI, origOperand))
	if string(res) == "null" {
		t.Fatal("hover on a closed document answered null, want a lowering of the disk text")
	}
	for _, b := range f.comp.bufferCalls() {
		if string(b[aPath]) == unsaved {
			t.Errorf("a lowering used the discarded unsaved text")
		}
	}
	opened := f.receivedMethod("textDocument/didOpen")
	if len(opened) != 1 {
		t.Fatalf("gopls got %d lowered didOpen messages, want the one of the disk text", len(opened))
	}
	doc, _ := opened[0]["textDocument"].(map[string]any)
	fwdText, _ := doc["text"].(string)
	if strings.Contains(fwdText, "// unsaved") {
		t.Errorf("gopls holds a lowering of the discarded unsaved text")
	}
	_, pos := forwardedPosition(t, f, "textDocument/hover", 0)
	lines := strings.Split(fwdText, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 || pos.Character > len(lines[pos.Line]) {
		t.Fatalf("hover forwarded at %v, outside the lowered text", pos)
	} else if got := lines[pos.Line][pos.Character:]; !strings.HasPrefix(got, "path)") {
		t.Errorf("the hover was mapped at %v reaching %q, want the path operand of the disk lowering", pos, got)
	}
}

func TestServeDuplicateContentLengthFails(t *testing.T) {
	scratch := t.TempDir()
	scriptPath := filepath.Join(scratch, "script.json")
	if err := os.WriteFile(scriptPath, []byte(`{}`), 0o666); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Gopls:    []string{os.Args[0], "-mode=stdio"},
		Env:      append(os.Environ(), fakeEnv+"="+scriptPath, fakeLogEnv+"="+filepath.Join(scratch, "gopls.log")),
		Compiler: newFakeCompiler(),
		Stderr:   new(syncBuffer),
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx := t.Context()
	go func() { done <- Serve(ctx, inR, outW, opts) }()
	body := `{"jsonrpc":"2.0","method":"initialized","params":{}}`
	bye := `{"jsonrpc":"2.0","method":"exit","params":{}}`
	if _, err := fmt.Fprintf(inW, "Content-Length: %d\r\nContent-Length: %d\r\n\r\n%s", len(body), len(body), body); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(inW, "Content-Length: %d\r\n\r\n%s", len(bye), bye); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "duplicate Content-Length") {
			t.Fatalf("Serve over a duplicate Content-Length frame = %v, want a duplicate Content-Length error", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve did not return after the duplicate Content-Length frame")
	}
	outW.Close()
	outR.Close()
}

func TestProxyPackageSnapshotHoldsEveryVersion(t *testing.T) {
	const bLgo = "package a\n\nfunc B() int { return 2 }\n"
	f := newFixture(t, map[string]string{"a/a.lgo": aLgo, "a/b.lgo": bLgo}, fixtureOptions{})
	aURI, aPath := f.uri("a/a.lgo"), f.path("a/a.lgo")
	bURI, bPath := f.uri("a/b.lgo"), f.path("a/b.lgo")
	f.open("a/a.lgo", 1)
	f.open("a/b.lgo", 1)
	editedA2 := aLgo + "// v2\n"
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": aURI, "version": 2},
		"contentChanges": []any{map[string]any{"text": editedA2}},
	})
	operand := shift(findPos(t, editedA2, "ReadFile(path)?", 0), len("ReadFile("))
	f.client.call("textDocument/hover", positionParams(aURI, operand))
	if got := len(f.comp.lowerCalls()); got != 1 {
		t.Fatalf("one edit and one hover caused %d lowerings, want the single run for the version set", got)
	}
	if last := f.comp.bufferCalls()[len(f.comp.bufferCalls())-1]; string(last[aPath]) != editedA2 || string(last[bPath]) != bLgo {
		t.Errorf("the run lowered a=%q b=%q, want the version 2 text of a and the version 1 text of b", last[aPath], last[bPath])
	}
	var fwdText string
	for _, o := range f.receivedMethod("textDocument/didOpen") {
		td, _ := o["textDocument"].(map[string]any)
		if td["uri"] == f.virtURI("a/a.lgo") {
			fwdText, _ = td["text"].(string)
		}
	}
	if fwdText == "" {
		t.Fatal("gopls got no lowered didOpen for a/a.lgo")
	}
	_, pos := forwardedPosition(t, f, "textDocument/hover", 0)
	lines := strings.Split(fwdText, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 || pos.Character > len(lines[pos.Line]) {
		t.Fatalf("hover forwarded at %v, outside the lowered text", pos)
	} else if got := lines[pos.Line][pos.Character:]; !strings.HasPrefix(got, "path)") {
		t.Errorf("hover forwarded at %v reaching %q, want the path operand", pos, got)
	}
	f.client.call("textDocument/hover", positionParams(aURI, operand))
	if got := len(f.comp.lowerCalls()); got != 1 {
		t.Fatalf("a repeat hover with no edits caused %d lowerings, want still the one run", got)
	}
	editedB2 := bLgo + "// v2\n"
	editedA3 := editedA2 + "// v3\n"
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": bURI, "version": 2},
		"contentChanges": []any{map[string]any{"text": editedB2}},
	})
	f.client.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": aURI, "version": 3},
		"contentChanges": []any{map[string]any{"text": editedA3}},
	})
	f.client.call("textDocument/hover", positionParams(bURI, Position{Line: 2}))
	if got := len(f.comp.lowerCalls()); got != 2 {
		t.Fatalf("edits to both documents caused %d lowerings, want only the second run", got)
	}
	if last := f.comp.bufferCalls()[len(f.comp.bufferCalls())-1]; string(last[aPath]) != editedA3 || string(last[bPath]) != editedB2 {
		t.Errorf("the rerun lowered a=%q b=%q, want the version 3 text of a and the version 2 text of b", last[aPath], last[bPath])
	}
}

func TestProxyLargeFileMapsExactly(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("package big\n\n")
	for i := range 300 {
		fmt.Fprintf(&sb, "var x%d = %d\n", i, i)
	}
	src := sb.String()
	if len(src) <= 2048 {
		t.Fatalf("fixture is %d bytes, want more than 2048 so a size cap would bite", len(src))
	}
	f := newFixture(t, map[string]string{"b/b.lgo": src}, fixtureOptions{})
	f.open("b/b.lgo", 1)
	at := Position{Line: 250, Character: 4}
	f.client.call("textDocument/hover", positionParams(f.uri("b/b.lgo"), at))
	if uri, pos := forwardedPosition(t, f, "textDocument/hover", 0); uri != f.virtURI("b/b.lgo") || pos != at {
		t.Errorf("hover at %v reached gopls as %s %v, want the exact position with no size cap", at, uri, pos)
	}
}

func TestProxyDuplicateTokenMapsToOwnSegment(t *testing.T) {
	const dupLgo = "package a\n\nimport \"os\"\n\nfunc first(path string) string {\n\tdata := os.ReadFile(path)?\n\treturn string(data)\n}\n\nfunc second(path string) string {\n\tdata := os.ReadFile(path)?\n\treturn string(data)\n}\n"
	f := newFixture(t, map[string]string{"a/a.lgo": dupLgo}, fixtureOptions{})
	f.open("a/a.lgo", 1)
	f.sync()
	aURI := f.uri("a/a.lgo")
	origFirst := shift(findPos(t, dupLgo, "ReadFile(path)?", 0), len("ReadFile("))
	origSecond := shift(findPos(t, dupLgo, "ReadFile(path)?", 1), len("ReadFile("))
	f.client.call("textDocument/hover", positionParams(aURI, origFirst))
	f.client.call("textDocument/hover", positionParams(aURI, origSecond))
	opened := f.receivedMethod("textDocument/didOpen")
	if len(opened) != 1 {
		t.Fatalf("gopls got %d lowered didOpen messages, want one", len(opened))
	}
	fwdText, _ := opened[0]["textDocument"].(map[string]any)["text"].(string)
	uri0, pos0 := forwardedPosition(t, f, "textDocument/hover", 0)
	uri1, pos1 := forwardedPosition(t, f, "textDocument/hover", 1)
	if uri0 != f.virtURI("a/a.lgo") || uri1 != f.virtURI("a/a.lgo") {
		t.Errorf("copies reached gopls as %s and %s, want %s", uri0, uri1, f.virtURI("a/a.lgo"))
	}
	if pos0 == pos1 {
		t.Fatalf("both copies forwarded at %v, want each copy to map to its own segment", pos0)
	}
	for i, pos := range []Position{pos0, pos1} {
		lines := strings.Split(fwdText, "\n")
		if pos.Line < 0 || pos.Line >= len(lines) || pos.Character < 0 || pos.Character > len(lines[pos.Line]) {
			t.Fatalf("copy %d forwarded at %v, outside the lowered text", i, pos)
		}
		if got := lines[pos.Line][pos.Character:]; !strings.HasPrefix(got, "path)") {
			t.Errorf("copy %d forwarded at %v reaches %q, want the path operand", i, pos, got)
		}
	}
}

func TestProxyDefinitionWritesNoBytes(t *testing.T) {
	const libLgo = "package lib\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n"
	const appGo = "package app\n\nimport \"example.com/m/lib\"\n\nvar Greeting = lib.Hello()\n"
	const app2Lgo = "package app2\n\nimport \"example.com/m/golib\"\n\nvar N = golib.Thing()\n"
	const goLib = "package golib\n\nfunc Thing() int { return 1 }\n"
	thing := Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 10}}
	hello := span(findPos(t, libLgo, "Hello", 0), len("Hello"))
	f := newFixture(t, map[string]string{
		"go.mod":        "module example.com/m\n\ngo 1.27\n",
		"lib/lib.lgo":   libLgo,
		"app/app.go":    appGo,
		"golib/g.go":    goLib,
		"app2/app2.lgo": app2Lgo,
	}, fixtureOptions{
		Script: func(root string) fakeScript {
			return fakeScript{Responses: map[string]json.RawMessage{
				"textDocument/definition": toJSON(t, []any{
					map[string]any{"uri": pathURI(filepath.Join(root, "lib", "lib.go")), "range": hello},
					map[string]any{"uri": pathURI(filepath.Join(root, "golib", "g.go")), "range": thing},
				}),
			}}
		},
		Tune: func(f *fixture, o *Options) {
			f.comp.setClosure(f.path("app"), f.path("lib"))
			f.comp.setClosure(f.path("app2"), f.path("golib"))
		},
	})
	f.open("app/app.go", 1)
	f.open("app2/app2.lgo", 1)
	f.sync()
	snap := func() (map[string][]byte, map[string]int64) {
		data := map[string][]byte{}
		mtime := map[string]int64{}
		for _, rel := range f.workspaceFiles() {
			path := filepath.Join(f.root, filepath.FromSlash(rel))
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			data[rel] = content
			mtime[rel] = st.ModTime().UnixNano()
		}
		return data, mtime
	}
	before, beforeTime := snap()
	appCall := shift(findPos(t, appGo, "lib.Hello", 0), len("lib."))
	f.client.call("textDocument/definition", positionParams(f.uri("app/app.go"), appCall))
	thingCall := shift(findPos(t, app2Lgo, "golib.Thing", 0), len("golib."))
	f.client.call("textDocument/definition", positionParams(f.uri("app2/app2.lgo"), thingCall))
	after, afterTime := snap()
	if !reflect.DeepEqual(after, before) {
		t.Errorf("definition changed workspace bytes")
	}
	if !reflect.DeepEqual(afterTime, beforeTime) {
		t.Errorf("definition touched workspace mtimes")
	}
}
