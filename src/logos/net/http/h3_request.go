// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"errors"
	stdhttp "net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

var errH3MalformedMessage = errors.New("http3: malformed message")

type h3MessageError string

func (e h3MessageError) Error() string {
	return errH3MalformedMessage.Error() + ": " + string(e)
}

func (e h3MessageError) Unwrap() error {
	return errH3MalformedMessage
}

const (
	h3Method = iota
	h3Scheme
	h3Authority
	h3Path
	h3Protocol
	h3PseudoCount
)

type h3RequestFields struct {
	pseudo [h3PseudoCount]string
	seen   uint8
	header stdhttp.Header
}

// decodeH3Request borrows strings from block. The returned strings keep its
// storage live; callers must not modify or reuse block while any are in use.
func decodeH3Request(block []byte, maxFieldSectionSize uint64) (*stdhttp.Request, error) {
	var fieldBuffer [8]h3Field
	fields, err := decodeH3Fields(fieldBuffer[:0], block, maxFieldSectionSize)
	if err != nil {
		return nil, err
	}
	request := h3RequestFields{header: make(stdhttp.Header)}
	for _, field := range fields {
		if err := validateH3RequestField(field); err != nil {
			return nil, err
		}
		if err := request.add(field); err != nil {
			return nil, err
		}
	}
	return request.request()
}

// decodeH3Trailers borrows strings under the same ownership rule as decodeH3Request.
func decodeH3Trailers(block []byte, maxFieldSectionSize uint64) (stdhttp.Header, error) {
	var fieldBuffer [8]h3Field
	fields, err := decodeH3Fields(fieldBuffer[:0], block, maxFieldSectionSize)
	if err != nil {
		return nil, err
	}
	trailer := make(stdhttp.Header)
	for _, field := range fields {
		if err := validateH3RequestField(field); err != nil {
			return nil, err
		}
		name, err := h3RequestTrailerName(field.name)
		if err != nil {
			return nil, err
		}
		trailer[name] = append(trailer[name], field.value)
	}
	return trailer, nil
}

func validateH3RequestField(field h3Field) error {
	name := strings.TrimPrefix(field.name, ":")
	if name == "" {
		return h3MessageError("invalid field name")
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'A' && c <= 'Z' || !httpguts.IsTokenRune(rune(c)) {
			return h3MessageError("invalid field name")
		}
	}
	if strings.Trim(field.value, " \t") != field.value || !httpguts.ValidHeaderFieldValue(field.value) {
		return h3MessageError("invalid field value")
	}
	return nil
}

func (f *h3RequestFields) has(index int) bool {
	return f.seen&(1<<index) != 0
}

func (f *h3RequestFields) addPseudo(field h3Field) error {
	if len(f.header) != 0 {
		return h3MessageError("pseudo-header after regular field")
	}
	var index int
	switch field.name {
	case ":method":
		index = h3Method
	case ":scheme":
		index = h3Scheme
	case ":authority":
		index = h3Authority
	case ":path":
		index = h3Path
	case ":protocol":
		index = h3Protocol
	default:
		return h3MessageError("unknown request pseudo-header")
	}
	if f.has(index) {
		return h3MessageError("duplicate request pseudo-header")
	}
	f.seen |= 1 << index
	f.pseudo[index] = field.value
	return nil
}

func (f *h3RequestFields) add(field h3Field) error {
	if strings.HasPrefix(field.name, ":") {
		return f.addPseudo(field)
	}
	name := stdhttp.CanonicalHeaderKey(field.name)
	if h3RequestConnectionField(name) {
		return h3MessageError("connection-specific field")
	}
	if name == "Te" && !h3TETrailers(field.value) {
		return h3MessageError("TE must contain only trailers")
	}
	f.header[name] = append(f.header[name], field.value)
	return nil
}

func h3TETrailers(value string) bool {
	found := false
	for part := range strings.SplitSeq(value, ",") {
		token := strings.Trim(part, " \t")
		if token == "" {
			continue
		}
		if len(token) != len("trailers") || !strings.EqualFold(token, "trailers") {
			return false
		}
		found = true
	}
	return found
}

func h3RequestConnectionField(canonicalName string) bool {
	switch canonicalName {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

func (f *h3RequestFields) request() (*stdhttp.Request, error) {
	method := f.pseudo[h3Method]
	if !httpguts.ValidHeaderFieldName(method) {
		return nil, h3MessageError("invalid :method")
	}
	authority, err := f.authority()
	if err != nil {
		return nil, err
	}
	target, requestURI, err := f.target(authority)
	if err != nil {
		return nil, err
	}
	length, err := h3RequestContentLength(f.header)
	if err != nil {
		return nil, err
	}
	trailer, err := h3RequestAnnouncedTrailers(f.header)
	if err != nil {
		return nil, err
	}
	delete(f.header, "Host")
	if cookies := f.header["Cookie"]; len(cookies) > 1 {
		cookies[0] = strings.Join(cookies, "; ")
		clear(cookies[1:])
		f.header["Cookie"] = cookies[:1:1]
	}
	if f.has(h3Protocol) {
		f.header[":protocol"] = []string{f.pseudo[h3Protocol]}
	}
	return &stdhttp.Request{
		Method:        method,
		URL:           target,
		Proto:         "HTTP/3.0",
		ProtoMajor:    3,
		Header:        f.header,
		ContentLength: length,
		Host:          authority,
		RequestURI:    requestURI,
		Trailer:       trailer,
	}, nil
}

func (f *h3RequestFields) authority() (string, error) {
	authority := f.pseudo[h3Authority]
	hosts := f.header["Host"]
	if len(hosts) > 1 {
		return "", h3MessageError("multiple Host fields")
	}
	connect := f.pseudo[h3Method] == stdhttp.MethodConnect
	if len(hosts) == 1 {
		host := hosts[0]
		if host == "" || f.has(h3Authority) && host != authority {
			return "", h3MessageError("invalid Host or mismatch with :authority")
		}
		if !f.has(h3Authority) && !connect {
			authority = host
		}
	}
	if authority == "" {
		return "", h3MessageError("missing authority")
	}
	port, err := h3AuthorityPort(authority)
	if err != nil {
		return "", err
	}
	if connect && !f.has(h3Protocol) && port == "" {
		return "", h3MessageError("CONNECT authority lacks port")
	}
	return authority, nil
}

func h3AuthorityPort(authority string) (string, error) {
	if !strings.HasPrefix(authority, "[") {
		host, port, _ := strings.Cut(authority, ":")
		if host == "" || !validH3URIChars(host, "") {
			return "", h3MessageError("invalid authority host")
		}
		if port != "" && !validH3Port(port) {
			return "", h3MessageError("invalid authority port")
		}
		return port, nil
	}
	literal, suffix, closed := strings.Cut(authority[1:], "]")
	if !closed || !validH3IPLiteral(literal) {
		return "", h3MessageError("invalid authority IP literal")
	}
	if suffix == "" {
		return "", nil
	}
	port, hasPort := strings.CutPrefix(suffix, ":")
	if !hasPort || port != "" && !validH3Port(port) {
		return "", h3MessageError("invalid authority port")
	}
	return port, nil
}

func validH3Port(port string) bool {
	_, err := strconv.ParseUint(port, 10, 16)
	return err == nil
}

func validH3IPLiteral(literal string) bool {
	if strings.IndexByte(literal, '%') >= 0 {
		return false
	}
	if len(literal) != 0 && literal[0]|0x20 == 'v' {
		version, addr, found := strings.Cut(literal[1:], ".")
		if !found || version == "" || addr == "" || !validH3URIChars(addr, ":") {
			return false
		}
		for i := range len(version) {
			if !h3HexDigit(version[i]) {
				return false
			}
		}
		return true
	}
	ip, err := netip.ParseAddr(literal)
	return err == nil && ip.Is6()
}

func (f *h3RequestFields) target(authority string) (*url.URL, string, error) {
	method := f.pseudo[h3Method]
	if f.has(h3Protocol) {
		if method != stdhttp.MethodConnect || !httpguts.ValidHeaderFieldName(f.pseudo[h3Protocol]) {
			return nil, "", h3MessageError("invalid :protocol")
		}
	} else if method == stdhttp.MethodConnect {
		if f.has(h3Scheme) || f.has(h3Path) {
			return nil, "", h3MessageError("CONNECT must omit :scheme and :path")
		}
		return &url.URL{Host: authority}, authority, nil
	}
	if !validH3Scheme(f.pseudo[h3Scheme]) {
		return nil, "", h3MessageError("invalid :scheme")
	}
	path := f.pseudo[h3Path]
	if !(method == stdhttp.MethodOptions && path == "*") && (!strings.HasPrefix(path, "/") || !validH3URIChars(path, ":@/?")) {
		return nil, "", h3MessageError("invalid :path")
	}
	target, err := url.ParseRequestURI(path)
	if err != nil {
		return nil, "", h3MessageError("invalid :path")
	}
	return target, path, nil
}

func validH3Scheme(scheme string) bool {
	if scheme == "" || !h3ASCIIAlpha(scheme[0]) {
		return false
	}
	for i := 1; i < len(scheme); i++ {
		c := scheme[i]
		if !h3ASCIIAlpha(c) && !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func validH3URIChars(value, extra string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if h3ASCIIAlpha(c) || c >= '0' && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=", c) >= 0 || strings.IndexByte(extra, c) >= 0 {
			continue
		}
		if c != '%' || len(value)-i < 3 || !h3HexDigit(value[i+1]) || !h3HexDigit(value[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func h3ASCIIAlpha(c byte) bool {
	lower := c | 0x20
	return lower >= 'a' && lower <= 'z'
}

func h3HexDigit(c byte) bool {
	lower := c | 0x20
	return c >= '0' && c <= '9' || lower >= 'a' && lower <= 'f'
}

func h3RequestContentLength(header stdhttp.Header) (int64, error) {
	values := header["Content-Length"]
	if len(values) == 0 {
		return -1, nil
	}
	length := int64(-1)
	var first string
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			digits := strings.Trim(part, " \t")
			n, err := strconv.ParseUint(digits, 10, 63)
			if err != nil {
				return 0, h3MessageError("invalid Content-Length")
			}
			if length == -1 {
				first, length = digits, int64(n)
			} else if uint64(length) != n {
				return 0, h3MessageError("conflicting Content-Length fields")
			}
		}
	}
	values[0] = first
	clear(values[1:])
	header["Content-Length"] = values[:1:1]
	return length, nil
}

func h3RequestAnnouncedTrailers(header stdhttp.Header) (stdhttp.Header, error) {
	var trailer stdhttp.Header
	for _, value := range header["Trailer"] {
		for part := range strings.SplitSeq(value, ",") {
			name := strings.Trim(part, " \t")
			if name == "" {
				continue
			}
			canonical, err := h3RequestTrailerName(name)
			if err != nil {
				return nil, err
			}
			if trailer == nil {
				trailer = make(stdhttp.Header)
			}
			trailer[canonical] = nil
		}
	}
	delete(header, "Trailer")
	return trailer, nil
}

func h3RequestTrailerName(name string) (string, error) {
	if !httpguts.ValidHeaderFieldName(name) {
		return "", h3MessageError("invalid trailer name")
	}
	canonical := stdhttp.CanonicalHeaderKey(name)
	if h3RequestConnectionField(canonical) || !httpguts.ValidTrailerHeader(canonical) {
		return "", h3MessageError("prohibited trailer field")
	}
	return canonical, nil
}
