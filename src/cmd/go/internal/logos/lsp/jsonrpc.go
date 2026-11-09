// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const maxMessage = 1 << 30

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (m *message) isResponse() bool { return m.Method == "" }

func (m *message) isRequest() bool { return m.Method != "" && len(m.ID) > 0 }

type reader struct{ r *bufio.Reader }

func newReader(r io.Reader) *reader { return &reader{bufio.NewReaderSize(r, 1<<16)} }

func (rd *reader) read() (*message, error) {
	n, err := rd.contentLength()
	if err != nil {
		return nil, err
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(rd.r, body); err != nil {
		return nil, fmt.Errorf("reading message body: %w", midMessage(err))
	}
	m := new(message)
	if err := json.Unmarshal(body, m); err != nil {
		return nil, fmt.Errorf("decoding message: %w", err)
	}
	return m, nil
}

func (rd *reader) contentLength() (int, error) {
	line, err := rd.r.ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return 0, io.EOF
	}
	n := -1
	for {
		if err != nil {
			return 0, fmt.Errorf("reading message header: %w", midMessage(err))
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := headerValue(line, "Content-Length"); ok {
			if n, err = parseLength(v); err != nil {
				return 0, err
			}
		}
		line, err = rd.r.ReadString('\n')
	}
	if n < 0 {
		return 0, errors.New("message header has no Content-Length")
	}
	return n, nil
}

func midMessage(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func headerValue(line, name string) (string, bool) {
	key, value, ok := strings.Cut(line, ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(key), name) {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func parseLength(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxMessage {
		return 0, fmt.Errorf("invalid Content-Length %q", v)
	}
	return n, nil
}

type writer struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *writer) write(m *message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	fmt.Fprintf(&frame, "Content-Length: %d\r\n\r\n", len(body))
	frame.Write(body)
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(frame.Bytes())
	return err
}

func (w *writer) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return w.write(&message{Method: method, Params: raw})
}

func (w *writer) respond(id, result json.RawMessage) error {
	if len(result) == 0 {
		result = nullResult
	}
	return w.write(&message{ID: id, Result: result})
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (w *writer) fail(id json.RawMessage, code int, text string) error {
	raw, err := json.Marshal(rpcError{Code: code, Message: text})
	if err != nil {
		return err
	}
	return w.write(&message{ID: id, Error: raw})
}
