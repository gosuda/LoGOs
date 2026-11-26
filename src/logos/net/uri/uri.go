// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Translated from lib/std/Uri.zig at revision c7826aa7a096270e4de8a1c7b8b4343650950f30 (MIT).

// Package uri provides percent-aware URI components in the shape of
// Zig std.Uri: each component records whether its text is already
// percent-encoded, and printing a parsed URI never double-encodes it.
package uri

import (
	"errors"
	"fmt"
	"strings"
)

const maxHostLen = 254

// ErrInvalidFormat reports text with no usable scheme split or authority shape.
var ErrInvalidFormat = errors.New("uri: invalid format")

// ErrUnexpectedCharacter reports a scheme with a leading or interior byte outside ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
var ErrUnexpectedCharacter = errors.New("uri: unexpected character")

// ErrInvalidPort reports a port that is empty, non-decimal, or above 65535.
var ErrInvalidPort = errors.New("uri: invalid port")

// ErrInvalidHostName reports a host longer than 254 bytes.
var ErrInvalidHostName = errors.New("uri: invalid host name")

// Component is one URI component. Encoded marks text that is already
// percent-encoded and must print unchanged. Raw text prints percent-encoded.
type Component struct {
	Text    string
	Encoded bool
}

// Raw returns a component that prints percent-encoded.
func Raw(s string) Component {
	return Component{Text: s}
}

// Encoded returns a component that prints unchanged.
func Encoded(s string) Component {
	return Component{Text: s, Encoded: true}
}

// IsEmpty reports whether the component holds no text.
func (c Component) IsEmpty() bool {
	return c.Text == ""
}

// Decoded returns the percent-decoded text. Invalid % sequences stay
// literal and never fail. Text without '%' returns without allocation.
func (c Component) Decoded() string {
	if c.Text == "" {
		return ""
	}
	if !c.Encoded {
		return c.Text
	}
	if strings.IndexByte(c.Text, '%') < 0 {
		return c.Text
	}
	return string(appendDecoded(nil, c.Text))
}

// AppendDecoded appends the percent-decoded text to dst.
func (c Component) AppendDecoded(dst []byte) []byte {
	return appendDecoded(dst, c.Text)
}

// String renders the component with the generic unreserved table.
func (c Component) String() string {
	if c.Encoded || c.Text == "" {
		return c.Text
	}
	return string(appendEncoded(nil, c.Text, isUnreserved))
}

// A URI is a parsed RFC 3986 reference. Components from Parse borrow the
// input string and stay valid as long as it does. Optional parts use the
// matching Has flag. Path is always present and may be empty.
type URI struct {
	Scheme      string
	User        Component
	Password    Component
	Host        Component
	Port        uint16
	Path        Component
	Query       Component
	Fragment    Component
	HasUser     bool
	HasPassword bool
	HasHost     bool
	HasPort     bool
	HasQuery    bool
	HasFragment bool
}

// Parse splits s into scheme and the remaining components. Every component
// borrows s. The success path allocates nothing.
func Parse(s string) (URI, error) {
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return URI{}, fmt.Errorf("uri: parse %q: %w", s, ErrInvalidFormat)
	}
	scheme := s[:colon]
	if !isValidScheme(scheme) {
		return URI{}, fmt.Errorf("uri: parse %q: %w", s, ErrUnexpectedCharacter)
	}
	return parseAfterScheme(scheme, s[colon+1:])
}

// String renders the URI. Encoded components print unchanged, so a parsed
// URI never double-encodes. Empty optional parts stay absent.
func (u URI) String() string {
	return string(u.AppendString(nil))
}

// AppendString appends the rendered URI to dst.
func (u URI) AppendString(dst []byte) []byte {
	dst = append(dst, u.Scheme...)
	dst = append(dst, ':')
	if u.HasHost || u.HasUser || u.HasPassword || u.HasPort {
		dst = append(dst, '/', '/')
		if u.HasUser {
			dst = appendComponent(dst, u.User, isUserChar)
			if u.HasPassword {
				dst = append(dst, ':')
				dst = appendComponent(dst, u.Password, isPasswordChar)
			}
			dst = append(dst, '@')
		}
		if u.HasHost {
			dst = appendComponent(dst, u.Host, isHostChar)
		}
		if u.HasPort {
			dst = append(dst, ':')
			dst = appendPort(dst, u.Port)
		}
	}
	dst = appendComponent(dst, u.Path, isPathChar)
	if u.HasQuery {
		dst = append(dst, '?')
		dst = appendComponent(dst, u.Query, isQueryChar)
	}
	if u.HasFragment {
		dst = append(dst, '#')
		dst = appendComponent(dst, u.Fragment, isQueryChar)
	}
	return dst
}

// Resolve parses ref against b following RFC 3986 section 5.2 and returns
// the merged URI. Absolute refs replace b. Relative refs merge paths with
// dot-segment removal. The result allocates.
func (b URI) Resolve(ref string) (URI, error) {
	r, err := parseReference(ref)
	if err != nil {
		return URI{}, err
	}
	return b.resolveParsed(r), nil
}

func parseAfterScheme(scheme, text string) (URI, error) {
	var u URI
	u.Scheme = scheme
	u.Path = Component{Encoded: true}
	i := 0
	if len(text) >= 2 && text[0] == '/' && text[1] == '/' {
		end := indexAuthorityEnd(text)
		auth := text[2:end]
		i = end
		if auth == "" {
			if len(text) == 2 || text[2] != '/' {
				return URI{}, fmt.Errorf("uri: parse %q: %w", text, ErrInvalidFormat)
			}
		} else if err := parseAuthorityField(auth, &u); err != nil {
			return URI{}, err
		}
	}
	parsePathQueryFragment(text, i, &u)
	return u, nil
}

func indexAuthorityEnd(text string) int {
	for i := 2; i < len(text); i++ {
		c := text[i]
		if c == '/' || c == '?' || c == '#' {
			return i
		}
	}
	return len(text)
}

func parseAuthorityField(auth string, u *URI) error {
	userinfo, hostport, hasUser := splitAuthority(auth)
	if hasUser {
		parseUserinfo(userinfo, u)
		if hostport == "" {
			return nil
		}
	}
	return parseHostPort(hostport, u)
}

func splitAuthority(auth string) (string, string, bool) {
	idx := strings.LastIndexByte(auth, '@')
	if idx < 0 {
		return "", auth, false
	}
	return auth[:idx], auth[idx+1:], true
}

func parseUserinfo(info string, u *URI) {
	u.HasUser = true
	idx := strings.IndexByte(info, ':')
	if idx < 0 {
		u.User = Component{Text: info, Encoded: true}
		return
	}
	u.User = Component{Text: info[:idx], Encoded: true}
	if idx+1 < len(info) {
		u.Password = Component{Text: info[idx+1:], Encoded: true}
		u.HasPassword = true
	}
}

func parseHostPort(hp string, u *URI) error {
	if hp == "" {
		return fmt.Errorf("uri: authority %q: %w", hp, ErrInvalidFormat)
	}
	if hp[0] == ']' {
		return fmt.Errorf("uri: authority %q: %w", hp, ErrInvalidFormat)
	}
	if hp[0] == '[' {
		return parseBracketHost(hp, u)
	}
	if idx := strings.LastIndexByte(hp, ':'); idx >= 0 {
		host := hp[:idx]
		if host == "" {
			return fmt.Errorf("uri: authority %q: %w", hp, ErrInvalidFormat)
		}
		port, err := parsePortNum(hp[idx+1:])
		if err != nil {
			return err
		}
		u.Host = Component{Text: host, Encoded: true}
		u.HasHost = true
		u.Port = port
		u.HasPort = true
		return checkHostLen(host)
	}
	u.Host = Component{Text: hp, Encoded: true}
	u.HasHost = true
	return checkHostLen(hp)
}

func parseBracketHost(hp string, u *URI) error {
	close := strings.LastIndexByte(hp, ']')
	if close < 0 {
		return fmt.Errorf("uri: authority %q: %w", hp, ErrInvalidFormat)
	}
	host := hp[:close+1]
	rest := hp[close+1:]
	if rest == "" {
		u.Host = Component{Text: host, Encoded: true}
		u.HasHost = true
		return checkHostLen(host)
	}
	if len(rest) < 2 || rest[0] != ':' {
		return fmt.Errorf("uri: authority %q: %w", hp, ErrInvalidFormat)
	}
	port, err := parsePortNum(rest[1:])
	if err != nil {
		return err
	}
	u.Host = Component{Text: host, Encoded: true}
	u.HasHost = true
	u.Port = port
	u.HasPort = true
	return checkHostLen(host)
}

func parsePortNum(s string) (uint16, error) {
	if s == "" {
		return 0, fmt.Errorf("uri: port %q: %w", s, ErrInvalidPort)
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("uri: port %q: %w", s, ErrInvalidPort)
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0, fmt.Errorf("uri: port %q: %w", s, ErrInvalidPort)
		}
	}
	return uint16(n), nil
}

func checkHostLen(host string) error {
	if len(host) > maxHostLen {
		return fmt.Errorf("uri: host %q: %w", host, ErrInvalidHostName)
	}
	return nil
}

func parsePathQueryFragment(text string, start int, u *URI) {
	end := len(text)
	for i := start; i < len(text); i++ {
		if text[i] == '?' || text[i] == '#' {
			end = i
			break
		}
	}
	u.Path = Component{Text: text[start:end], Encoded: true}
	i := end
	if i < len(text) && text[i] == '?' {
		q := i + 1
		qend := len(text)
		for j := q; j < len(text); j++ {
			if text[j] == '#' {
				qend = j
				break
			}
		}
		u.Query = Component{Text: text[q:qend], Encoded: true}
		u.HasQuery = true
		i = qend
	}
	if i < len(text) && text[i] == '#' {
		u.Fragment = Component{Text: text[i+1:], Encoded: true}
		u.HasFragment = true
	}
}

func parseReference(s string) (URI, error) {
	if schemeEnd(s) >= 0 {
		return Parse(s)
	}
	return parseAfterScheme("", s)
}

func schemeEnd(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' {
			if isValidScheme(s[:i]) {
				return i
			}
			return -1
		}
		if c == '/' || c == '?' || c == '#' {
			return -1
		}
	}
	return -1
}

func (b URI) resolveParsed(r URI) URI {
	if r.Scheme != "" {
		return slashForAuthority(URI{
			Scheme:      r.Scheme,
			User:        r.User,
			Password:    r.Password,
			Host:        r.Host,
			Port:        r.Port,
			Path:        Encoded(removeDotSegments(renderEncoded(r.Path, isPathChar))),
			Query:       r.Query,
			Fragment:    r.Fragment,
			HasUser:     r.HasUser,
			HasPassword: r.HasPassword,
			HasHost:     r.HasHost,
			HasPort:     r.HasPort,
			HasQuery:    r.HasQuery,
			HasFragment: r.HasFragment,
		})
	}
	if r.HasHost || r.HasUser || r.HasPassword || r.HasPort {
		return slashForAuthority(URI{
			Scheme:      b.Scheme,
			User:        r.User,
			Password:    r.Password,
			Host:        r.Host,
			Port:        r.Port,
			Path:        Encoded(removeDotSegments(renderEncoded(r.Path, isPathChar))),
			Query:       r.Query,
			Fragment:    r.Fragment,
			HasUser:     r.HasUser,
			HasPassword: r.HasPassword,
			HasHost:     r.HasHost,
			HasPort:     r.HasPort,
			HasQuery:    r.HasQuery,
			HasFragment: r.HasFragment,
		})
	}
	return slashForAuthority(b.resolveMerge(r))
}

func (b URI) resolveMerge(r URI) URI {
	var out URI
	out.Scheme = b.Scheme
	out.User = b.User
	out.Password = b.Password
	out.Host = b.Host
	out.Port = b.Port
	out.HasUser = b.HasUser
	out.HasPassword = b.HasPassword
	out.HasHost = b.HasHost
	out.HasPort = b.HasPort
	if r.Path.Text == "" {
		out.Path = b.Path
		if r.HasQuery {
			out.Query = r.Query
			out.HasQuery = true
		} else {
			out.Query = b.Query
			out.HasQuery = b.HasQuery
		}
	} else if r.Path.Text[0] == '/' {
		out.Path = Encoded(removeDotSegments(renderEncoded(r.Path, isPathChar)))
		out.Query = r.Query
		out.HasQuery = r.HasQuery
	} else {
		out.Path = Encoded(removeDotSegments(mergePaths(renderEncoded(b.Path, isPathChar), renderEncoded(r.Path, isPathChar))))
		out.Query = r.Query
		out.HasQuery = r.HasQuery
	}
	out.Fragment = r.Fragment
	out.HasFragment = r.HasFragment
	return out
}

func slashForAuthority(u URI) URI {
	if u.Path.Text == "" && (u.HasHost || u.HasUser || u.HasPassword || u.HasPort) {
		u.Path = Encoded("/")
	}
	return u
}

func renderEncoded(c Component, valid func(byte) bool) string {
	if c.Encoded {
		return c.Text
	}
	if c.Text == "" {
		return ""
	}
	return string(appendEncoded(nil, c.Text, valid))
}

func removeDotSegments(s string) string {
	if s == "" {
		return ""
	}
	buf := []byte(s)
	in := 0
	out := 0
	for in < len(buf) {
		if startsWithAt(buf, in, "./") {
			in += 2
			continue
		}
		if startsWithAt(buf, in, "../") {
			in += 3
			continue
		}
		if startsWithAt(buf, in, "/./") {
			in += 2
			continue
		}
		if equalsAt(buf, in, "/.") {
			in++
			buf[in] = '/'
			continue
		}
		if startsWithAt(buf, in, "/../") {
			in += 3
			out = popSegment(buf, out)
			continue
		}
		if equalsAt(buf, in, "/..") {
			in += 2
			buf[in] = '/'
			out = popSegment(buf, out)
			continue
		}
		if equalsAt(buf, in, ".") {
			in++
			continue
		}
		if equalsAt(buf, in, "..") {
			in += 2
			continue
		}
		for {
			buf[out] = buf[in]
			out++
			in++
			if in >= len(buf) || buf[in] == '/' {
				break
			}
		}
	}
	return string(buf[:out])
}

func mergePaths(base, rel string) string {
	if base == "" {
		return removeDotSegments(rel)
	}
	slash := -1
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			slash = i
			break
		}
	}
	if slash < 0 {
		return removeDotSegments(rel)
	}
	merged := make([]byte, 0, slash+2+len(rel))
	merged = append(merged, base[:slash+1]...)
	merged = append(merged, rel...)
	return removeDotSegments(string(merged))
}

func startsWithAt(buf []byte, at int, s string) bool {
	if len(buf)-at < len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if buf[at+i] != s[i] {
			return false
		}
	}
	return true
}

func equalsAt(buf []byte, at int, s string) bool {
	if len(buf)-at != len(s) {
		return false
	}
	return startsWithAt(buf, at, s)
}

func popSegment(buf []byte, out int) int {
	for out > 0 {
		out--
		if buf[out] == '/' {
			break
		}
	}
	return out
}

func appendComponent(dst []byte, c Component, valid func(byte) bool) []byte {
	if c.Encoded || c.Text == "" {
		return append(dst, c.Text...)
	}
	return appendEncoded(dst, c.Text, valid)
}

func appendPort(dst []byte, p uint16) []byte {
	if p == 0 {
		return append(dst, '0')
	}
	var buf [5]byte
	n := 0
	v := p
	for v > 0 {
		buf[n] = byte(v%10) + '0'
		n++
		v /= 10
	}
	for i := n - 1; i >= 0; i-- {
		dst = append(dst, buf[i])
	}
	return dst
}

const upperHex = "0123456789ABCDEF"

func appendEncoded(dst []byte, s string, valid func(byte) bool) []byte {
	start := 0
	for i := 0; i < len(s); i++ {
		if valid(s[i]) {
			continue
		}
		dst = append(dst, s[start:i]...)
		dst = append(dst, '%', upperHex[s[i]>>4], upperHex[s[i]&15])
		start = i + 1
	}
	return append(dst, s[start:]...)
}

func appendDecoded(dst []byte, s string) []byte {
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '%' {
			dst = append(dst, c)
			i++
			continue
		}
		if i+2 >= len(s) {
			dst = append(dst, '%')
			i++
			continue
		}
		hi, ok1 := hexVal(s[i+1])
		lo, ok2 := hexVal(s[i+2])
		if !ok1 || !ok2 {
			dst = append(dst, '%')
			i++
			continue
		}
		dst = append(dst, hi<<4|lo)
		i += 3
	}
	return dst
}

func hexVal(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func isValidScheme(s string) bool {
	if s == "" {
		return false
	}
	c0 := s[0]
	if !(('A' <= c0 && c0 <= 'Z') || ('a' <= c0 && c0 <= 'z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') {
			continue
		}
		if c == '+' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z':
		return true
	case 'a' <= c && c <= 'z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	switch c {
	case '-', '.', '_', '~':
		return true
	}
	return false
}

func isSubDelim(c byte) bool {
	switch c {
	case '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=':
		return true
	}
	return false
}

func isUserChar(c byte) bool {
	return isUnreserved(c) || isSubDelim(c)
}

func isPasswordChar(c byte) bool {
	return isUserChar(c) || c == ':'
}

func isHostChar(c byte) bool {
	return isPasswordChar(c) || c == '[' || c == ']'
}

func isPathChar(c byte) bool {
	return isUserChar(c) || c == '/' || c == ':' || c == '@'
}

func isQueryChar(c byte) bool {
	return isPathChar(c) || c == '?'
}
