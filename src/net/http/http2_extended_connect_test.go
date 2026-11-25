// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !nethttpomithttp2

package http_test

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/internal/http2"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/http2/hpack"
)

func TestServerHTTP2ExtendedConnect(t *testing.T) {
	run(t, func(t *testing.T, mode testMode) {
		t.Run("enabled", func(t *testing.T) {
			testServerHTTP2ExtendedConnect(t, mode, true)
		})
		t.Run("default", func(t *testing.T) {
			testServerHTTP2ExtendedConnect(t, mode, false)
		})
	}, []testMode{http2Mode, http2UnencryptedMode})
}

func testServerHTTP2ExtendedConnect(t *testing.T, mode testMode, enabled bool) {
	t.Helper()
	server := newClientServerTest(t, mode, http.HandlerFunc(echoHTTP2ExtendedConnect), func(s *http.Server) {
		if enabled {
			s.EnableHTTP2ExtendedConnect()
		}
	})

	addr := server.ts.Listener.Addr().String()
	dialer := &net.Dialer{Deadline: time.Now().Add(10 * time.Second)}
	var conn net.Conn
	var err error
	if mode == http2Mode {
		conf := server.tr.TLSClientConfig.Clone()
		conf.NextProtos = []string{"h2"}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, conf)
	} else {
		conn, err = dialer.DialContext(t.Context(), "tcp", addr)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(dialer.Deadline); err != nil {
		t.Fatal(err)
	}

	fr := http2.NewFramer(conn, conn)
	fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	readFrame := func() http2.Frame {
		t.Helper()
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	readResponseFrame := func() http2.Frame {
		t.Helper()
		for {
			f := readFrame()
			switch f.(type) {
			case *http2.SettingsFrame, *http2.WindowUpdateFrame:
				continue
			default:
				return f
			}
		}
	}

	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	if err := fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	f := readFrame()
	settings, ok := f.(*http2.SettingsFrame)
	if !ok || settings.IsAck() {
		t.Fatalf("first frame = %v, want initial SETTINGS", f)
	}
	value, present := settings.Value(http2.SettingEnableConnectProtocol)
	if present != enabled || (present && value != 1) {
		t.Errorf("SETTINGS_ENABLE_CONNECT_PROTOCOL = (%d, present=%t), want (1, present=true) if enabled, absent otherwise; enabled=%t", value, present, enabled)
	}
	if err := fr.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}

	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	writeHeaders := func(p http2.HeadersFrameParam, fields []hpack.HeaderField) {
		t.Helper()
		block.Reset()
		for _, field := range fields {
			if err := enc.WriteField(field); err != nil {
				t.Fatal(err)
			}
		}
		p.BlockFragment = block.Bytes()
		p.EndHeaders = true
		if err := fr.WriteHeaders(p); err != nil {
			t.Fatal(err)
		}
	}
	writeHeaders(http2.HeadersFrameParam{StreamID: 1, EndStream: !enabled}, []hpack.HeaderField{
		{Name: ":method", Value: http.MethodConnect},
		{Name: ":scheme", Value: mode.Scheme()},
		{Name: ":authority", Value: addr},
		{Name: ":path", Value: "/ws"},
		{Name: ":protocol", Value: "websocket"},
		{Name: "sec-websocket-version", Value: "13"},
	})

	f = readResponseFrame()
	if !enabled {
		rst, ok := f.(*http2.RSTStreamFrame)
		if !ok || rst.StreamID != 1 || rst.ErrCode != http2.ErrCodeProtocol {
			t.Fatalf("response = %v, want RST_STREAM with PROTOCOL_ERROR on stream 1", f)
		}
		return
	}
	headers, ok := f.(*http2.MetaHeadersFrame)
	if !ok || headers.StreamID != 1 || headers.PseudoValue("status") != "200" || headers.StreamEnded() {
		t.Fatalf("response = %v, want :status 200 on open stream 1", f)
	}

	for _, want := range []byte("ok") {
		if err := fr.WriteData(1, false, []byte{want}); err != nil {
			t.Fatal(err)
		}
		f = readResponseFrame()
		data, ok := f.(*http2.DataFrame)
		if !ok || data.StreamID != 1 || data.StreamEnded() {
			t.Fatalf("response = %v, want DATA on open stream 1", f)
		}
		if got := data.Data(); len(got) != 1 || got[0] != want {
			t.Fatalf("echo = %q, want %q", got, want)
		}
	}
	if err := fr.WriteData(1, true, nil); err != nil {
		t.Fatal(err)
	}
	f = readResponseFrame()
	if data, ok := f.(*http2.DataFrame); !ok || data.StreamID != 1 || !data.StreamEnded() || len(data.Data()) != 0 {
		t.Fatalf("response = %v, want empty DATA with END_STREAM on stream 1", f)
	}

	baseFields := [...]hpack.HeaderField{
		{Name: ":method", Value: http.MethodConnect},
		{Name: ":scheme", Value: mode.Scheme()},
		{Name: ":authority", Value: addr},
		{Name: ":path", Value: "/other"},
		{Name: ":protocol", Value: "websocket"},
		{Name: "host", Value: addr},
	}
	for i, tt := range []struct {
		name    string
		omit    []string
		replace hpack.HeaderField
	}{
		{name: "empty-protocol-on-connect", omit: []string{":scheme", ":path"}, replace: hpack.HeaderField{Name: ":protocol"}},
		{name: "protocol-on-get", replace: hpack.HeaderField{Name: ":method", Value: http.MethodGet}},
		{name: "invalid-protocol", replace: hpack.HeaderField{Name: ":protocol", Value: "not a token"}},
		{name: "missing-authority-with-host", omit: []string{":authority"}},
		{name: "empty-authority-with-host", replace: hpack.HeaderField{Name: ":authority"}},
		{name: "missing-scheme", omit: []string{":scheme"}},
		{name: "empty-scheme", replace: hpack.HeaderField{Name: ":scheme"}},
		{name: "invalid-scheme", replace: hpack.HeaderField{Name: ":scheme", Value: "not a scheme"}},
		{name: "asterisk-path", replace: hpack.HeaderField{Name: ":path", Value: "*"}},
	} {
		fields := make([]hpack.HeaderField, 0, len(baseFields))
		for _, field := range baseFields {
			if slices.Contains(tt.omit, field.Name) {
				continue
			}
			if field.Name == tt.replace.Name {
				field = tt.replace
			}
			fields = append(fields, field)
		}
		streamID := uint32(3 + 2*i)
		writeHeaders(http2.HeadersFrameParam{StreamID: streamID, EndStream: true}, fields)
		f := readResponseFrame()
		rst, ok := f.(*http2.RSTStreamFrame)
		if !ok || rst.StreamID != streamID || rst.ErrCode != http2.ErrCodeProtocol {
			t.Errorf("%s: response = %v, want RST_STREAM with PROTOCOL_ERROR on stream %d", tt.name, f, streamID)
		}
	}
}

func echoHTTP2ExtendedConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || r.URL.Path != "/ws" || r.Header.Get(":protocol") != "websocket" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	var b [1]byte
	for {
		if _, err := io.ReadFull(r.Body, b[:]); err != nil {
			return
		}
		if _, err := w.Write(b[:]); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}
