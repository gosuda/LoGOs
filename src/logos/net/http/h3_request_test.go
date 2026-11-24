// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"errors"
	"io"
	stdhttp "net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func encodeH3RequestFixture(t *testing.T, fields []h3Field) []byte {
	t.Helper()
	block, err := appendH3Fields(nil, fields)
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func h3RequestFixtureFields() []h3Field {
	return []h3Field{
		{name: ":method", value: "GET"},
		{name: ":scheme", value: "https"},
		{name: ":authority", value: "example.com"},
		{name: ":path", value: "/"},
	}
}

func requireH3RequestMessageError(t *testing.T, err error, detail string) {
	t.Helper()
	message, ok := errors.AsType[h3MessageError](err)
	if !errors.Is(err, errH3MalformedMessage) || !ok || !strings.Contains(string(message), detail) {
		t.Fatalf("want malformed message containing %q; got %v", detail, err)
	}
}

func TestH3RequestMatchesNetHTTP(t *testing.T) {
	const target = "/items/a%2Fb?view=full&tag=%23"
	fields := []h3Field{
		{name: ":method", value: "POST"},
		{name: ":scheme", value: "https"},
		{name: ":authority", value: "example.com"},
		{name: ":path", value: target},
		{name: "host", value: "example.com"},
		{name: "content-length", value: "4"},
		{name: "content-length", value: "4, 04"},
		{name: "cookie", value: "a=1"},
		{name: "cookie", value: "b=2"},
		{name: "x-multi", value: "first"},
		{name: "x-multi", value: "second"},
	}
	got, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	const wire = "POST " + target + " HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\nCookie: a=1; b=2\r\nX-Multi: first\r\nX-Multi: second\r\n\r\nbody"
	want, err := stdhttp.ReadRequest(bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatalf("read HTTP/1 oracle: %v", err)
	}
	defer want.Body.Close()
	if got.Method != want.Method || got.Host != want.Host || got.RequestURI != want.RequestURI || got.ContentLength != want.ContentLength {
		t.Errorf("got method %s, host %q, URI %q, length %d; want %s, %q, %q, %d", got.Method, got.Host, got.RequestURI, got.ContentLength, want.Method, want.Host, want.RequestURI, want.ContentLength)
	}
	if got.URL == nil || *got.URL != *want.URL {
		t.Errorf("got URL %#v; want %#v", got.URL, want.URL)
	}
	for _, key := range []string{"Cookie", "Content-Length", "X-Multi", "Host"} {
		if !slices.Equal(got.Header.Values(key), want.Header.Values(key)) {
			t.Errorf("%s = %q; want %q", key, got.Header.Values(key), want.Header.Values(key))
		}
	}
	if got.Proto != "HTTP/3.0" || got.ProtoMajor != 3 || got.ProtoMinor != 0 {
		t.Errorf("unexpected protocol %s (%d.%d)", got.Proto, got.ProtoMajor, got.ProtoMinor)
	}
	if got.Body != nil || got.TLS != nil || got.RemoteAddr != "" {
		t.Error("header-only decoding set transport state")
	}
}

func TestH3RequestTargetForms(t *testing.T) {
	cases := []struct {
		name     string
		fields   []h3Field
		host     string
		uri      string
		url      url.URL
		protocol string
		te       string
	}{
		{name: "OPTIONS asterisk with Host fallback", host: "example.com", uri: "*", url: url.URL{Path: "*"}, te: "Trailers, trailers", fields: []h3Field{
			{name: ":method", value: "OPTIONS"}, {name: ":scheme", value: "HTTPS"}, {name: ":path", value: "*"},
			{name: "host", value: "example.com"}, {name: "te", value: "Trailers, trailers"},
		}},
		{name: "CONNECT authority form", host: "[2001:db8::1]:443", uri: "[2001:db8::1]:443", url: url.URL{Host: "[2001:db8::1]:443"}, fields: []h3Field{
			{name: ":method", value: "CONNECT"}, {name: ":authority", value: "[2001:db8::1]:443"},
		}},
		{name: "extended CONNECT origin form", host: "example.com", uri: "/socket?room=blue", url: url.URL{Path: "/socket", RawQuery: "room=blue"}, protocol: "websocket", fields: []h3Field{
			{name: ":method", value: "CONNECT"}, {name: ":protocol", value: "websocket"}, {name: ":scheme", value: "https"},
			{name: ":authority", value: "example.com"}, {name: ":path", value: "/socket?room=blue"},
		}},
		{name: "IPvFuture origin with double slash and empty query", host: "[vF.a:b]:65535", uri: "//a%2Fb?", url: url.URL{Path: "//a/b", RawPath: "//a%2Fb", ForceQuery: true}, fields: []h3Field{
			{name: ":method", value: "GET"}, {name: ":scheme", value: "a1+foo.-"},
			{name: ":authority", value: "[vF.a:b]:65535"}, {name: ":path", value: "//a%2Fb?"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeH3Request(encodeH3RequestFixture(t, tc.fields), 1024)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req.URL == nil || *req.URL != tc.url || req.Host != tc.host || req.RequestURI != tc.uri {
				t.Errorf("target = %#v, Host %q, URI %q; want %#v, %q, %q", req.URL, req.Host, req.RequestURI, tc.url, tc.host, tc.uri)
			}
			if req.ContentLength != -1 {
				t.Errorf("ContentLength = %d; want -1", req.ContentLength)
			}
			if got := req.Header.Values(":protocol"); tc.protocol == "" && len(got) != 0 || tc.protocol != "" && !slices.Equal(got, []string{tc.protocol}) {
				t.Errorf(":protocol = %q; want %q", got, tc.protocol)
			}
			if got := req.Header.Get("Te"); got != tc.te {
				t.Errorf("TE = %q; want %q", got, tc.te)
			}
			if _, ok := req.Header["Host"]; ok {
				t.Error("request retained Host in the header map")
			}
		})
	}
}

func TestH3RequestRejectsMalformedFields(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		value  string
		detail string
	}{
		{"uppercase name", "X-Foo", "a", "invalid field name"},
		{"empty name", "", "a", "invalid field name"},
		{"invalid token", "x foo", "a", "invalid field name"},
		{"carriage return", "x-foo", "a\rb", "invalid field value"},
		{"line feed", "x-foo", "a\nb", "invalid field value"},
		{"NUL", "x-foo", "a\x00b", "invalid field value"},
		{"leading space", "x-foo", " a", "invalid field value"},
		{"trailing tab", "x-foo", "a\t", "invalid field value"},
		{"response pseudo-header", ":status", "200", "unknown request pseudo-header"},
		{"duplicate pseudo-header", ":path", "/", "duplicate request pseudo-header"},
		{"empty protocol on GET", ":protocol", "", "invalid :protocol"},
		{"protocol on GET", ":protocol", "websocket", "invalid :protocol"},
		{"conflicting host", "host", "other.example", "mismatch with :authority"},
		{"empty host", "host", "", "invalid Host"},
		{"connection", "connection", "close", "connection-specific"},
		{"keep-alive", "keep-alive", "timeout=1", "connection-specific"},
		{"proxy-connection", "proxy-connection", "close", "connection-specific"},
		{"transfer-encoding", "transfer-encoding", "chunked", "connection-specific"},
		{"upgrade", "upgrade", "websocket", "connection-specific"},
		{"TE with transfer coding", "te", "trailers, gzip", "TE must contain only trailers"},
		{"empty TE", "te", "", "TE must contain only trailers"},
		{"empty length", "content-length", "", "invalid Content-Length"},
		{"signed length", "content-length", "+1", "invalid Content-Length"},
		{"negative length", "content-length", "-1", "invalid Content-Length"},
		{"overflowing length", "content-length", "9223372036854775808", "invalid Content-Length"},
		{"empty length list member", "content-length", "2,", "invalid Content-Length"},
		{"conflicting length list", "content-length", "2, 3", "conflicting Content-Length"},
		{"prohibited announced trailer", "trailer", "X-Foo, Content-Length", "prohibited trailer field"},
		{"invalid announced trailer", "trailer", "X Foo", "invalid trailer name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := append(h3RequestFixtureFields(), h3Field{name: tc.field, value: tc.value})
			_, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
			requireH3RequestMessageError(t, err, tc.detail)
		})
	}
}

func TestH3RequestRejectsInvalidFieldSequences(t *testing.T) {
	cases := []struct {
		name   string
		extra  []h3Field
		detail string
	}{
		{"pseudo after regular field", []h3Field{{name: "x", value: "y"}, {name: ":path", value: "/"}}, "pseudo-header after regular field"},
		{"multiple host fields", []h3Field{{name: "host", value: "example.com"}, {name: "host", value: "example.com"}}, "multiple Host fields"},
		{"conflicting length fields", []h3Field{{name: "content-length", value: "2"}, {name: "content-length", value: "3"}}, "conflicting Content-Length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := append(h3RequestFixtureFields(), tc.extra...)
			_, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
			requireH3RequestMessageError(t, err, tc.detail)
		})
	}
}

func TestH3RequestRejectsInvalidTargetFields(t *testing.T) {
	t.Run("missing authority", func(t *testing.T) {
		fields := []h3Field{{name: ":method", value: "GET"}, {name: ":scheme", value: "https"}, {name: ":path", value: "/"}}
		_, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
		requireH3RequestMessageError(t, err, "missing authority")
	})
	cases := []struct {
		name   string
		index  int
		value  string
		detail string
	}{
		{"empty method", 0, "", "invalid :method"},
		{"invalid method", 0, "GET/POST", "invalid :method"},
		{"empty scheme", 1, "", "invalid :scheme"},
		{"invalid scheme", 1, "https://", "invalid :scheme"},
		{"empty authority", 2, "", "missing authority"},
		{"userinfo", 2, "user@example.com", "invalid authority host"},
		{"authority with path", 2, "example.com/api", "invalid authority host"},
		{"authority with nonnumeric port", 2, "example.com:http", "invalid authority port"},
		{"authority with overflowing port", 2, "example.com:65536", "invalid authority port"},
		{"invalid IP literal", 2, "[example.com]", "invalid authority IP literal"},
		{"unbracketed IPv6", 2, "2001:db8::1", "invalid authority port"},
		{"empty path", 3, "", "invalid :path"},
		{"absolute URI path", 3, "https://example.com/", "invalid :path"},
		{"relative path", 3, "items/1", "invalid :path"},
		{"asterisk GET", 3, "*", "invalid :path"},
		{"fragment", 3, "/items#one", "invalid :path"},
		{"unescaped space", 3, "/a b", "invalid :path"},
		{"bad path escape", 3, "/a%ZZ", "invalid :path"},
		{"bad query escape", 3, "/?a=%Z", "invalid :path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := h3RequestFixtureFields()
			fields[tc.index].value = tc.value
			_, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
			requireH3RequestMessageError(t, err, tc.detail)
		})
	}
}

func TestH3RequestRejectsMalformedConnect(t *testing.T) {
	cases := []struct {
		name   string
		fields []h3Field
		detail string
	}{
		{"Host cannot replace CONNECT authority", []h3Field{{name: "host", value: "example.com:443"}}, "missing authority"},
		{"classic CONNECT requires port", []h3Field{{name: ":authority", value: "example.com"}}, "lacks port"},
		{"classic CONNECT omits scheme even if empty", []h3Field{{name: ":authority", value: "example.com:443"}, {name: ":scheme", value: ""}}, "must omit :scheme and :path"},
		{"classic CONNECT omits path even if empty", []h3Field{{name: ":authority", value: "example.com:443"}, {name: ":path", value: ""}}, "must omit :scheme and :path"},
		{"empty protocol is not absent", []h3Field{{name: ":authority", value: "example.com:443"}, {name: ":protocol", value: ""}}, "invalid :protocol"},
		{"protocol must be token", []h3Field{{name: ":authority", value: "example.com"}, {name: ":protocol", value: "websocket/13"}, {name: ":scheme", value: "https"}, {name: ":path", value: "/"}}, "invalid :protocol"},
		{"extended CONNECT requires authority", []h3Field{{name: ":protocol", value: "websocket"}, {name: ":scheme", value: "https"}, {name: ":path", value: "/"}, {name: "host", value: "example.com"}}, "missing authority"},
		{"extended CONNECT requires scheme", []h3Field{{name: ":protocol", value: "websocket"}, {name: ":authority", value: "example.com"}, {name: ":path", value: "/"}}, "invalid :scheme"},
		{"extended CONNECT requires path", []h3Field{{name: ":protocol", value: "websocket"}, {name: ":authority", value: "example.com"}, {name: ":scheme", value: "https"}}, "invalid :path"},
		{"extended CONNECT rejects asterisk", []h3Field{{name: ":protocol", value: "websocket"}, {name: ":authority", value: "example.com"}, {name: ":scheme", value: "https"}, {name: ":path", value: "*"}}, "invalid :path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := append([]h3Field{{name: ":method", value: "CONNECT"}}, tc.fields...)
			_, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
			requireH3RequestMessageError(t, err, tc.detail)
		})
	}
}

func TestH3RequestAnnouncedTrailers(t *testing.T) {
	fields := append(h3RequestFixtureFields(),
		h3Field{name: "trailer", value: "X-Checksum, X-Warning"},
		h3Field{name: "trailer", value: "x-checksum,x-extra"},
	)
	req, err := decodeH3Request(encodeH3RequestFixture(t, fields), 1024)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, present := req.Header["Trailer"]; present || len(req.Trailer) != 3 {
		t.Fatalf("unexpected trailer announcement: Header %v, Trailer %v", req.Header, req.Trailer)
	}
	for _, key := range []string{"X-Checksum", "X-Warning", "X-Extra"} {
		if values, present := req.Trailer[key]; !present || values != nil {
			t.Errorf("trailer %s = %q (present %v); want announced without a value", key, values, present)
		}
	}
}

func TestH3RequestTrailersPreserveValueOrder(t *testing.T) {
	fields := []h3Field{{name: "x-proof", value: "first"}, {name: "x-proof", value: "second"}}
	got, err := decodeH3Trailers(encodeH3RequestFixture(t, fields), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !slices.Equal(got.Values("X-Proof"), []string{"first", "second"}) {
		t.Errorf("trailers = %v; want both X-Proof values in order", got)
	}
}

func TestH3RequestRejectsInvalidTrailers(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		detail string
	}{
		{":path", "/", "invalid trailer name"},
		{"X-Foo", "value", "invalid field name"},
		{"x-foo", "a\r\nb", "invalid field value"},
		{"content-length", "0", "prohibited trailer field"},
		{"host", "example.com", "prohibited trailer field"},
		{"trailer", "x-foo", "prohibited trailer field"},
		{"te", "trailers", "prohibited trailer field"},
		{"upgrade", "websocket", "prohibited trailer field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := encodeH3RequestFixture(t, []h3Field{{name: tc.name, value: tc.value}})
			_, err := decodeH3Trailers(block, 1024)
			requireH3RequestMessageError(t, err, tc.detail)
		})
	}
}

func TestH3RequestClassifiesQPACKFailures(t *testing.T) {
	cases := []struct {
		name  string
		block []byte
	}{
		{"missing prefix", nil},
		{"missing delta base", []byte{0}},
		{"nonzero insert count", []byte{1, 0}},
		{"negative base", []byte{0, 0x80}},
		{"post-base name reference", []byte{0, 0, 0}},
		{"dynamic table index", []byte{0, 0, 0x80}},
		{"out of range static index", []byte{0, 0, 0xff, 0x25}},
		{"missing literal value", []byte{0, 0, 0x50}},
		{"truncated static index", []byte{0, 0, 0xff}},
		{"bad Huffman value", []byte{0, 0, 0x50, 0x81, 0xff}},
		{"bad field after malformed HTTP field", append(encodeH3RequestFixture(t, []h3Field{{name: "X-Upper", value: "bad"}}), 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, requestErr := decodeH3Request(tc.block, 1024)
			_, trailerErr := decodeH3Trailers(tc.block, 1024)
			if !errors.Is(requestErr, errH3QPACK) || !errors.Is(trailerErr, errH3QPACK) {
				t.Errorf("request error %v, trailer error %v; want QPACK errors", requestErr, trailerErr)
			}
		})
	}
	if _, err := decodeH3Request([]byte{0, 0, 0x50}, 1024); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("QPACK error did not preserve unexpected EOF: %v", err)
	}
}

func TestH3RequestAcceptsStaticQPACKWithPositiveBase(t *testing.T) {
	// Required Insert Count 0 and Delta Base 1 are valid without dynamic references.
	block := append([]byte{0x00, 0x01, 0xd1, 0xd7, 0xc1, 0x50, 0x0b}, "example.com"...)
	req, err := decodeH3Request(block, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.Host != "example.com" || req.RequestURI != "/" {
		t.Errorf("decoded %s %s%s; want GET example.com/", req.Method, req.Host, req.RequestURI)
	}
}

func TestH3RequestFieldSectionLimit(t *testing.T) {
	fields := append(h3RequestFixtureFields(), h3Field{name: "x", value: "a"}, h3Field{name: "x", value: "a"})
	request := encodeH3RequestFixture(t, fields)
	trailer := encodeH3RequestFixture(t, []h3Field{{name: "x-proof", value: "\xc3\xa9"}})
	const requestSize = 42 + 44 + 53 + 38 + 34 + 34
	const trailerSize = 32 + 7 + 2
	for _, limit := range []uint64{requestSize, requestSize + 1, ^uint64(0)} {
		if _, err := decodeH3Request(request, limit); err != nil {
			t.Errorf("request under limit %d: %v", limit, err)
		}
	}
	if _, err := decodeH3Request(request, requestSize-1); !errors.Is(err, errH3FieldSectionTooLarge) {
		t.Errorf("request exceeds size by one: %v", err)
	}
	if _, err := decodeH3Trailers(trailer, trailerSize); err != nil {
		t.Errorf("trailer at exact limit: %v", err)
	}
	for _, limit := range []uint64{trailerSize - 1, 0} {
		if _, err := decodeH3Trailers(trailer, limit); !errors.Is(err, errH3FieldSectionTooLarge) {
			t.Errorf("trailer with limit %d: %v; want size error", limit, err)
		}
	}
	empty := []byte{0, 0}
	got, err := decodeH3Trailers(empty, 0)
	if err != nil || len(got) != 0 {
		t.Errorf("empty QPACK trailer section decoded as %v, %v", got, err)
	}
	_, err = decodeH3Request(empty, 0)
	requireH3RequestMessageError(t, err, "invalid :method")
}
