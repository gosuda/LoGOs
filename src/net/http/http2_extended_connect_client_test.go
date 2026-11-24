// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !nethttpomithttp2

package http_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTP2ExtendedConnectClient(t *testing.T) {
	run(t, func(t *testing.T, mode testMode) {
		t.Run("enabled", func(t *testing.T) { testHTTP2ExtendedConnectClientEnabled(t, mode) })
		t.Run("default", func(t *testing.T) { testHTTP2ExtendedConnectClientDefault(t, mode) })
	}, []testMode{http2Mode, http2UnencryptedMode})
}

func testHTTP2ExtendedConnectClientEnabled(t *testing.T, mode testMode) {
	server := newClientServerTest(t, mode, http.HandlerFunc(echoHTTP2ExtendedConnect), (*http.Server).EnableHTTP2ExtendedConnect)
	withExtendedConnectClients(t, server.tr, server.ts.URL, func(t *testing.T, rt http.RoundTripper) {
		const payload = "hello"
		req := newExtendedConnectClientRequest(t, http.MethodConnect, server.ts.URL+"/ws", strings.NewReader(payload))
		req.Header.Set(":protocol", "websocket")
		res, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK || res.ProtoMajor != 2 {
			t.Fatalf("CONNECT response = %s %s, want HTTP/2 200", res.Proto, res.Status)
		}
		got, err := io.ReadAll(res.Body)
		if err != nil || string(got) != payload {
			t.Errorf("CONNECT response body = %q, %v; want %q", got, err, payload)
		}
	})
}

func testHTTP2ExtendedConnectClientDefault(t *testing.T, mode testMode) {
	server := newClientServerTest(t, mode, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/probe" {
			t.Errorf("HTTP/2 peer received unexpected request %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	withExtendedConnectClients(t, server.tr, server.ts.URL, func(t *testing.T, rt http.RoundTripper) {
		body := new(unreadExtendedConnectBody)
		req := newExtendedConnectClientRequest(t, http.MethodConnect, server.ts.URL+"/ws", body)
		req.Header.Set(":protocol", "websocket")
		cc := reserveExtendedConnectClient(t, rt)
		checkExtendedConnectRejection(t, rt, req, body, "extended connect not supported by peer")
		if cc != nil && cc.InFlight() != 0 {
			t.Errorf("InFlight after peer rejected CONNECT = %d, want 0", cc.InFlight())
		}
		checkExtendedConnectProbe(t, rt, newExtendedConnectClientRequest(t, http.MethodGet, server.ts.URL+"/probe", nil), 2)
	})
}

func TestHTTP2ExtendedConnectClientInvalidHeaders(t *testing.T) {
	run(t, func(t *testing.T, mode testMode) {
		server := newClientServerTest(t, mode, http.HandlerFunc(echoHTTP2ExtendedConnect), (*http.Server).EnableHTTP2ExtendedConnect)
		withExtendedConnectClients(t, server.tr, server.ts.URL, func(t *testing.T, rt http.RoundTripper) {
			for _, tt := range []struct {
				name, method    string
				header, trailer http.Header
				want            string
			}{
				{"non-CONNECT", http.MethodGet, http.Header{":protocol": {"websocket"}}, nil, `invalid header field name ":protocol"`},
				{"duplicate", http.MethodConnect, http.Header{":protocol": {"websocket", "websocket"}}, nil, `invalid header field value for ":protocol"`},
				{"no-values", http.MethodConnect, http.Header{":protocol": nil}, nil, `invalid header field value for ":protocol"`},
				{"empty", http.MethodConnect, http.Header{":protocol": {""}}, nil, `invalid header field value for ":protocol"`},
				{"not-a-token", http.MethodConnect, http.Header{":protocol": {"web socket"}}, nil, `invalid header field value for ":protocol"`},
				{"other-pseudo-header", http.MethodConnect, http.Header{":protocol": {"websocket"}, ":path": {"/ws"}}, nil, `invalid header field name ":path"`},
				{"pseudo-trailer", http.MethodConnect, http.Header{":protocol": {"websocket"}}, http.Header{":protocol": {"websocket"}}, `invalid trailer field name ":protocol"`},
			} {
				t.Run(tt.name, func(t *testing.T) {
					body := new(unreadExtendedConnectBody)
					req := newExtendedConnectClientRequest(t, tt.method, server.ts.URL+"/ws", body)
					req.Header, req.Trailer = tt.header, tt.trailer
					var attempted atomic.Bool
					req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
						GetConn: func(string) { attempted.Store(true) },
					}))
					cc := reserveExtendedConnectClient(t, rt)
					checkExtendedConnectRejection(t, rt, req, body, tt.want)
					if attempted.Load() {
						t.Error("invalid request reached connection acquisition")
					}
					if cc != nil && cc.InFlight() != 0 {
						t.Errorf("InFlight after invalid request = %d, want 0", cc.InFlight())
					}
				})
			}
		})
	}, []testMode{http2UnencryptedMode})
}

func TestHTTP2ExtendedConnectClientHTTP1Fallback(t *testing.T) {
	run(t, func(t *testing.T, mode testMode) {
		var written atomic.Int64
		server := newClientServerTest(t, mode, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/probe" {
				t.Errorf("HTTP/1 peer received unexpected request %s %s", r.Method, r.URL)
			}
			w.WriteHeader(http.StatusNoContent)
		}), func(tr *http.Transport) {
			tr.Protocols = new(http.Protocols)
			tr.Protocols.SetHTTP1(true)
			tr.Protocols.SetHTTP2(true)
			tr.MaxConnsPerHost = 1
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				var dialer net.Dialer
				c, err := dialer.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &extendedConnectCountingConn{Conn: c, written: &written}, nil
			}
		})
		origin := server.ts.URL
		if mode == http1Mode {
			proxy, err := url.Parse(server.ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			server.tr.Proxy = http.ProxyURL(proxy)
			origin = "http://origin.invalid:80"
		}
		withExtendedConnectClients(t, server.tr, origin, func(t *testing.T, rt http.RoundTripper) {
			cc := reserveExtendedConnectClient(t, rt)
			before := written.Load()
			var acquired net.Conn
			body := new(unreadExtendedConnectBody)
			req := newExtendedConnectClientRequest(t, http.MethodConnect, origin+"/ws", body)
			req.Header.Set(":protocol", "websocket")
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) {
					acquired = info.Conn
					before = written.Load()
				},
			}))
			checkExtendedConnectRejection(t, rt, req, body, "extended CONNECT requires HTTP/2")
			if cc == nil && acquired == nil {
				t.Fatal("Transport did not acquire the HTTP/1 connection")
			}
			if n := written.Load() - before; n != 0 {
				t.Errorf("wrote %d bytes after acquiring the HTTP/1 connection, want 0", n)
			}
			if cc != nil && (cc.InFlight() != 0 || cc.Available() != 1) {
				t.Errorf("InFlight, Available = %d, %d; want 0, 1", cc.InFlight(), cc.Available())
			}
			probe := newExtendedConnectClientRequest(t, http.MethodGet, origin+"/probe", nil)
			if cc == nil {
				probe = probe.WithContext(httptrace.WithClientTrace(probe.Context(), &httptrace.ClientTrace{
					GotConn: func(info httptrace.GotConnInfo) {
						if info.Conn != acquired {
							t.Error("ordinary request did not reuse the acquired HTTP/1 connection")
						}
					},
				}))
			}
			checkExtendedConnectProbe(t, rt, probe, 1)
		})
	}, []testMode{https1Mode, http1Mode})
}

func withExtendedConnectClients(t *testing.T, tr *http.Transport, origin string, f func(*testing.T, http.RoundTripper)) {
	t.Helper()
	t.Run("Transport", func(t *testing.T) { f(t, tr) })
	t.Run("ClientConn", func(t *testing.T) {
		u, err := url.Parse(origin)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if tr.MaxConnsPerHost != 0 {
			tr = tr.Clone()
			tr.MaxConnsPerHost = 0
		}
		cc, err := tr.NewClientConn(ctx, u.Scheme, u.Host)
		if err != nil {
			t.Fatal(err)
		}
		defer cc.Close()
		f(t, cc)
	})
}

func reserveExtendedConnectClient(t *testing.T, rt http.RoundTripper) *http.ClientConn {
	t.Helper()
	cc, _ := rt.(*http.ClientConn)
	if cc != nil {
		if err := cc.Reserve(); err != nil {
			t.Fatal(err)
		}
	}
	return cc
}

func newExtendedConnectClientRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func checkExtendedConnectRejection(t *testing.T, rt http.RoundTripper, req *http.Request, body *unreadExtendedConnectBody, want string) {
	t.Helper()
	var wroteHeaders atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteHeaders: func() { wroteHeaders.Store(true) },
	}))
	res, err := rt.RoundTrip(req)
	if res != nil {
		res.Body.Close()
		t.Fatalf("unexpected response to rejected request: %s", res.Status)
	}
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("RoundTrip error = %v, want suffix %q", err, want)
	}
	if wroteHeaders.Load() {
		t.Error("rejected request wrote headers")
	}
	if n := body.reads.Load(); n != 0 {
		t.Errorf("rejected request body was read %d times, want 0", n)
	}
	if n := body.closes.Load(); n != 1 {
		t.Errorf("rejected request body was closed %d times, want 1", n)
	}
}

func checkExtendedConnectProbe(t *testing.T, rt http.RoundTripper, req *http.Request, major int) {
	t.Helper()
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.ProtoMajor != major {
		t.Errorf("ordinary request response = %s %s, want HTTP/%d 204", res.Proto, res.Status, major)
	}
}

type unreadExtendedConnectBody struct {
	reads, closes atomic.Int32
}

func (b *unreadExtendedConnectBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return 0, io.EOF
}

func (b *unreadExtendedConnectBody) Close() error {
	b.closes.Add(1)
	return nil
}

type extendedConnectCountingConn struct {
	net.Conn
	written *atomic.Int64
}

func (c *extendedConnectCountingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}
