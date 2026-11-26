// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package uri_test

import (
	"errors"
	"logos/net/uri"
	"net/url"
	"strings"
	"testing"
)

func equalURI(a, b uri.URI) bool {
	if a.Scheme != b.Scheme {
		return false
	}
	if a.User != b.User || a.Password != b.Password {
		return false
	}
	if a.Host != b.Host || a.Port != b.Port {
		return false
	}
	if a.Path != b.Path || a.Query != b.Query {
		return false
	}
	if a.Fragment != b.Fragment {
		return false
	}
	if a.HasUser != b.HasUser || a.HasPassword != b.HasPassword {
		return false
	}
	if a.HasHost != b.HasHost || a.HasPort != b.HasPort {
		return false
	}
	if a.HasQuery != b.HasQuery || a.HasFragment != b.HasFragment {
		return false
	}
	return true
}

func mustParse(t *testing.T, s string) uri.URI {
	t.Helper()
	u, err := uri.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", s, err)
	}
	return u
}

func TestParseBasic(t *testing.T) {
	u := mustParse(t, "https://ziglang.org/download")
	if u.Scheme != "https" {
		t.Errorf("Scheme = %q", u.Scheme)
	}
	if !u.HasHost || u.Host.Text != "ziglang.org" || !u.Host.Encoded {
		t.Errorf("Host = %+v", u.Host)
	}
	if u.Path.Text != "/download" || !u.Path.Encoded {
		t.Errorf("Path = %+v", u.Path)
	}
	if u.HasPort {
		t.Errorf("HasPort = true")
	}
	std, err := url.Parse("https://ziglang.org/download")
	if err != nil {
		t.Fatal(err)
	}
	if std.Scheme != u.Scheme || std.Host != u.Host.Text {
		t.Errorf("net/url = %q %q", std.Scheme, std.Host)
	}
	if got := u.String(); got != "https://ziglang.org/download" {
		t.Errorf("String = %q", got)
	}
}

func TestParseAuthorityPassword(t *testing.T) {
	cases := []struct {
		in          string
		user        string
		password    string
		hasPassword bool
	}{
		{"scheme://username@a", "username", "", false},
		{"scheme://username:@a", "username", "", false},
		{"scheme://username:password@a", "username", "password", true},
		{"scheme://username::@a", "username", ":", true},
		{"scheme://user:p@ssword@hostname:1234", "user", "p@ssword", true},
	}
	for _, c := range cases {
		u := mustParse(t, c.in)
		if !u.HasUser || u.User.Text != c.user {
			t.Errorf("%q user = %+v", c.in, u.User)
		}
		if u.HasPassword != c.hasPassword || u.Password.Text != c.password {
			t.Errorf("%q password = %+v has=%v", c.in, u.Password, u.HasPassword)
		}
	}
}

func TestParseScheme(t *testing.T) {
	valid := []string{"http", "scheme-mee", "a.b.c", "ab+", "X+++", "Y+-."}
	for _, s := range valid {
		u := mustParse(t, s+":_")
		if u.Scheme != s {
			t.Errorf("Scheme = %q want %q", u.Scheme, s)
		}
	}
	invalid := []string{"", ":", "-:", "+hTTp:", "$:", "h$:"}
	for _, s := range invalid {
		if _, err := uri.Parse(s); err == nil {
			t.Errorf("Parse(%q) ok, want error", s)
		}
	}
	if _, err := uri.Parse(":"); !errors.Is(err, uri.ErrUnexpectedCharacter) {
		t.Errorf("Parse(\":\") = %v", err)
	}
	if _, err := uri.Parse(""); !errors.Is(err, uri.ErrInvalidFormat) {
		t.Errorf("Parse(\"\") = %v", err)
	}
}

func TestParseFile(t *testing.T) {
	u := mustParse(t, "file:///")
	if u.HasHost {
		t.Errorf("file:/// host = %+v", u.Host)
	}
	if u.Path.Text != "/" {
		t.Errorf("file:/// path = %q", u.Path.Text)
	}
	u = mustParse(t, "file:///an/absolute/path/to/something")
	if u.Path.Text != "/an/absolute/path/to/something" {
		t.Errorf("path = %q", u.Path.Text)
	}
	u = mustParse(t, "file://localhost/an/absolute/path/to/another/thing/")
	if !u.HasHost || u.Host.Text != "localhost" {
		t.Errorf("host = %+v", u.Host)
	}
	u = mustParse(t, "file:/an/absolute/path")
	if u.HasHost {
		t.Errorf("file:/ host = %+v", u.Host)
	}
	if u.Path.Text != "/an/absolute/path" {
		t.Errorf("path = %q", u.Path.Text)
	}
}

func TestParsePort(t *testing.T) {
	u := mustParse(t, "http://example:1337/")
	if !u.HasPort || u.Port != 1337 {
		t.Errorf("port = %v %v", u.Port, u.HasPort)
	}
	if got := u.String(); got != "http://example:1337/" {
		t.Errorf("String = %q", got)
	}
	u = mustParse(t, "scheme://hostname:1234")
	if !u.HasPort || u.Port != 1234 {
		t.Errorf("port = %v", u.Port)
	}
	bad := []string{"scheme://host:", "scheme://host:abc", "scheme://host:99999", "scheme://host:1x"}
	for _, s := range bad {
		if _, err := uri.Parse(s); !errors.Is(err, uri.ErrInvalidPort) {
			t.Errorf("Parse(%q) = %v, want invalid port", s, err)
		}
	}
}

func TestParseMalformed(t *testing.T) {
	bad := []string{"http://][", "http://]@[", "foobar://"}
	for _, s := range bad {
		if _, err := uri.Parse(s); err == nil {
			t.Errorf("Parse(%q) ok, want error", s)
		}
	}
	if _, err := uri.Parse("http://]["); !errors.Is(err, uri.ErrInvalidFormat) {
		t.Errorf("err = %v", err)
	}
}

func TestHostTooLong(t *testing.T) {
	s := "http://" + strings.Repeat("Z", 255)
	if _, err := uri.Parse(s); !errors.Is(err, uri.ErrInvalidHostName) {
		t.Errorf("err = %v", err)
	}
	ok := "http://" + strings.Repeat("Z", 254)
	if _, err := uri.Parse(ok); err != nil {
		t.Errorf("254-byte host = %v", err)
	}
}

func TestPercentEncode(t *testing.T) {
	got := uri.Raw("\\\u00f6/ \u00e4\u00f6\u00df ~~-test").String()
	want := "%5C%C3%B6%2F%20%C3%A4%C3%B6%C3%9F%20~~-test"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := uri.Raw("~~.adas").String(); got != "~~.adas" {
		t.Errorf("unreserved = %q", got)
	}
	if got := uri.Raw("a/b?c").String(); got != "a%2Fb%3Fc" {
		t.Errorf("generic = %q", got)
	}
}

func TestPercentDecode(t *testing.T) {
	c := uri.Encoded("%5C%C3%B6%2F%20%C3%A4%C3%B6%C3%9F%20~~.adas-https%3A%2F%2Fcanvas%3A123%2F%23ads%26%26sad")
	want := "\\\u00f6/ \u00e4\u00f6\u00df ~~.adas-https://canvas:123/#ads&&sad"
	if got := c.Decoded(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := uri.Encoded("/abc%").Decoded(); got != "/abc%" {
		t.Errorf("trailing %% = %q", got)
	}
	if got := uri.Encoded("%zz%2G").Decoded(); got != "%zz%2G" {
		t.Errorf("invalid = %q", got)
	}
	if got := uri.Encoded("plain").Decoded(); got != "plain" {
		t.Errorf("plain = %q", got)
	}
}

func TestNoDoubleEncode(t *testing.T) {
	const s = "https://example.com/a%2Fb?x=%2F"
	u := mustParse(t, s)
	if got := u.String(); got != s {
		t.Errorf("got %q want %q", got, s)
	}
	built := uri.URI{
		Scheme:  "https",
		Host:    uri.Raw("example.com"),
		HasHost: true,
		Path:    uri.Raw("/a b"),
	}
	if got, want := built.String(), "https://example.com/a%20b"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	re := mustParse(t, built.String())
	if re.Path.Decoded() != "/a b" {
		t.Errorf("decoded = %q", re.Path.Decoded())
	}
}

func TestRoundTripDifferential(t *testing.T) {
	corpus := []string{
		"http://a/b/c/d;p?q",
		"http://a/b/c/g",
		"http://a/b/c/",
		"http://a/g",
		"http://a/b/c/d;p?q#s",
		"https://john.doe@www.example.com:123/forum/questions/?tag=networking&order=newest#top",
		"ldap://[2001:db8::7]/c=GB?objectClass?one",
		"mailto:John.Doe@example.com",
		"news:comp.infosystems.www.servers.unix",
		"tel:+1-816-555-1212",
		"urn:oasis:names:specification:docbook:dtd:xml:4.1.2",
		"http://a/b/c/d;p?q",
		"https://objects.githubusercontent.com/?response-content-type=application%2Foctet-stream",
		"foo://example.com:8042/over/there?name=ferret#nose",
		"urn:example:animal:ferret:nose",
	}
	for _, s := range corpus {
		u := mustParse(t, s)
		if got := u.String(); got != s {
			t.Errorf("round trip %q = %q", s, got)
		}
		std, err := url.Parse(s)
		if err != nil {
			t.Errorf("net/url Parse(%q) = %v", s, err)
			continue
		}
		if got := u.String(); got != std.String() {
			t.Errorf("differential %q: uri %q vs net/url %q", s, got, std.String())
		}
	}
}

func TestRoundTripDifferences(t *testing.T) {
	u := mustParse(t, "scheme://username:@a")
	if u.HasPassword {
		t.Errorf("empty password must be absent")
	}
	if got := u.String(); got != "scheme://username@a" {
		t.Errorf("got %q", got)
	}
	std, _ := url.Parse("scheme://username:@a")
	if std.String() == u.String() {
		t.Errorf("net/url keeps the colon, uri drops it")
	}
	if _, err := uri.Parse("scheme://host:"); !errors.Is(err, uri.ErrInvalidPort) {
		t.Errorf("empty port = %v", err)
	}
	if _, err := url.Parse("scheme://host:"); err != nil {
		t.Errorf("net/url rejects empty port: %v", err)
	}
	if _, err := uri.Parse("foobar://"); err == nil {
		t.Errorf("empty authority without slash must fail")
	}
	upper := mustParse(t, "HTTP://EXAMPLE.COM/Path")
	if upper.Scheme != "HTTP" {
		t.Errorf("scheme case = %q", upper.Scheme)
	}
	low, _ := url.Parse("HTTP://EXAMPLE.COM/Path")
	if low.Scheme == upper.Scheme {
		t.Errorf("net/url lowercases the scheme")
	}
	if got := uri.Encoded("%zz").Decoded(); got != "%zz" {
		t.Errorf("decode = %q", got)
	}
	if _, err := url.PathUnescape("%zz"); err == nil {
		t.Errorf("net/url must reject %%zz")
	}
	opaque := mustParse(t, "urn:example:animal:ferret:nose")
	if opaque.Path.Text != "example:animal:ferret:nose" {
		t.Errorf("path = %q", opaque.Path.Text)
	}
	so, _ := url.Parse("urn:example:animal:ferret:nose")
	if so.Opaque == "" || so.Path != "" {
		t.Errorf("net/url uses Opaque %q", so.Opaque)
	}
}

func TestResolveRFC3986(t *testing.T) {
	base := mustParse(t, "http://a/b/c/d;p?q")
	cases := []struct{ ref, want string }{
		{"g:h", "g:h"},
		{"g", "http://a/b/c/g"},
		{"./g", "http://a/b/c/g"},
		{"g/", "http://a/b/c/g/"},
		{"/g", "http://a/g"},
		{"//g", "http://g/"},
		{"?y", "http://a/b/c/d;p?y"},
		{"g?y", "http://a/b/c/g?y"},
		{"#s", "http://a/b/c/d;p?q#s"},
		{"g#s", "http://a/b/c/g#s"},
		{"g?y#s", "http://a/b/c/g?y#s"},
		{";x", "http://a/b/c/;x"},
		{"g;x", "http://a/b/c/g;x"},
		{"g;x?y#s", "http://a/b/c/g;x?y#s"},
		{"", "http://a/b/c/d;p?q"},
		{".", "http://a/b/c/"},
		{"./", "http://a/b/c/"},
		{"..", "http://a/b/"},
		{"../", "http://a/b/"},
		{"../g", "http://a/b/g"},
		{"../..", "http://a/"},
		{"../../", "http://a/"},
		{"../../g", "http://a/g"},
		{"../../../g", "http://a/g"},
		{"/./g", "http://a/g"},
		{"/../g", "http://a/g"},
		{"/a/b/c/./../../g", "http://a/a/g"},
		{"http://a/b/c/g", "http://a/b/c/g"},
	}
	for _, c := range cases {
		got, err := base.Resolve(c.ref)
		if err != nil {
			t.Errorf("Resolve(%q) = %v", c.ref, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("Resolve(%q) = %q want %q", c.ref, got.String(), c.want)
		}
	}
}

func TestParseAllocFree(t *testing.T) {
	s := "https://example.com:8080/p?q#f"
	allocs := testing.AllocsPerRun(200, func() {
		u, err := uri.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "https" || u.Port != 8080 {
			t.Fatal("bad parse")
		}
	})
	if allocs != 0 {
		t.Fatalf("Parse allocated %v times", allocs)
	}
}

func TestDecodedAllocFree(t *testing.T) {
	c := uri.Encoded("/plain/path")
	allocs := testing.AllocsPerRun(200, func() {
		if c.Decoded() != "/plain/path" {
			t.Fatal("bad decode")
		}
	})
	if allocs != 0 {
		t.Fatalf("Decoded allocated %v times", allocs)
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"https://ziglang.org/download",
		"scheme://username:password@a",
		"scheme://username:@a",
		"file:///an/absolute/path",
		"http://a/b/c/d;p?q",
		"http://example:1337/",
		"https://example.com/a%2Fb?x=%2F",
		"/abc%",
		"%zz",
		"http://][",
		"foobar://",
		"g",
		"//g",
		"?y",
		"#s",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := uri.Parse(s)
		if err != nil {
			return
		}
		out := u.String()
		if strings.Contains(s, "%25") != strings.Contains(out, "%25") {
			t.Fatalf("double-encode: %q -> %q", s, out)
		}
		v, err := uri.Parse(out)
		if err != nil {
			t.Fatalf("reparse %q = %v", out, err)
		}
		if !equalURI(u, v) {
			t.Fatalf("unstable: %+v vs %+v", u, v)
		}
		_ = u.Path.Decoded()
		_ = u.Query.Decoded()
		_ = u.Fragment.Decoded()
		_ = u.Host.Decoded()
	})
}
