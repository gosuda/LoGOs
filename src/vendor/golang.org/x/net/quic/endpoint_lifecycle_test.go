// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpointAcceptReadFailure(t *testing.T) {
	ctx := endpointLifecycleContext(t)
	pc := newEndpointLifecycleSocket(ctx, 1234)
	e := openEndpointLifecycle(t, pc, nil)
	waiting := &endpointLifecycleWaitContext{Context: ctx, waiting: make(chan struct{})}
	accepted := make(chan error, 1)
	go func() {
		_, err := e.Accept(waiting)
		accepted <- err
	}()
	endpointLifecycleReceive(t, ctx, waiting.waiting)

	fault := errors.New("receive failed")
	pc.in <- endpointLifecyclePacket{err: &net.OpError{Op: "read", Net: "udp", Err: fault}}
	if err := endpointLifecycleReceive(t, ctx, accepted); !errors.Is(err, fault) {
		t.Fatalf("blocked Accept = %v, want %v", err, fault)
	}
	endpointLifecycleReceive(t, ctx, pc.closed)
	if err := e.Close(ctx); err != nil {
		t.Fatalf("Close after receive failure = %v, want nil", err)
	}
	if _, err := e.Accept(ctx); !errors.Is(err, fault) {
		t.Fatalf("future Accept after Close = %v, want %v", err, fault)
	}
}

func TestEndpointClosePacketResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"normal", nil},
		{"close error", errors.New("socket close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := endpointLifecycleContext(t)
			pc := newEndpointLifecycleSocket(ctx, 1234)
			pc.closeErr = tc.err
			e := openEndpointLifecycle(t, pc, nil)
			start := make(chan struct{})
			result := make(chan error, 2)
			for range 2 {
				go func() {
					<-start
					result <- e.Close(ctx)
				}()
			}
			close(start)
			for range 2 {
				if err := endpointLifecycleReceive(t, ctx, result); !errors.Is(err, tc.err) {
					t.Fatalf("concurrent Close = %v, want %v", err, tc.err)
				}
			}
			if err := e.Close(ctx); !errors.Is(err, tc.err) {
				t.Fatalf("repeated Close = %v, want %v", err, tc.err)
			}
			if n := pc.closeCalls.Load(); n != 1 {
				t.Fatalf("packet socket Close called %d times, want 1", n)
			}
			if _, err := e.Accept(ctx); err == nil || err.Error() != "endpoint closed" {
				t.Fatalf("Accept after deliberate close = %v, want endpoint closed", err)
			}
		})
	}
}

func TestEndpointCloseDeadlineReleasesSocket(t *testing.T) {
	for _, name := range []string{"first caller", "concurrent caller"} {
		t.Run(name, func(t *testing.T) {
			ctx := endpointLifecycleContext(t)
			pc := newEndpointLifecycleSocket(ctx, 1234)
			e := openEndpointLifecycle(t, pc, nil)
			cfg := &Config{TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				NextProtos: []string{"quic-lifecycle"},
				ServerName: "localhost",
			}}
			peer := netip.MustParseAddrPort("127.0.0.1:1235")
			c, err := e.newConn(time.Now(), cfg, clientSide, newServerConnIDs{}, "localhost", peer)
			if err != nil {
				t.Fatal(err)
			}
			endpointLifecycleReceive(t, ctx, pc.firstWrite)
			c.wake()

			if name == "concurrent caller" {
				waiting := &endpointLifecycleWaitContext{Context: ctx, waiting: make(chan struct{})}
				graceful := make(chan error, 1)
				go func() { graceful <- e.Close(waiting) }()
				endpointLifecycleReceive(t, ctx, waiting.waiting)
				defer func() {
					if err := endpointLifecycleReceive(t, ctx, graceful); err != nil {
						t.Errorf("concurrent close with live context: %v", err)
					}
				}()
			}

			expired, cancel := context.WithDeadline(t.Context(), time.Time{})
			defer cancel()
			forced := make(chan error, 1)
			go func() { forced <- e.Close(expired) }()
			if err := endpointLifecycleReceive(t, ctx, forced); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close with expired context = %v, want deadline exceeded", err)
			}
			select {
			case <-pc.closed:
			default:
				t.Fatal("Close returned without releasing the packet socket")
			}
			if err := c.Wait(ctx); err == nil || ctx.Err() != nil {
				t.Fatalf("connection Wait after forced close = %v, want connection error", err)
			}
			if err := e.Close(ctx); err != nil {
				t.Fatalf("joining forced close: %v", err)
			}
		})
	}
}

func TestEndpointCloseWithUnconfirmedServerHandshake(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"normal", nil},
		{"socket close error", errors.New("half-open endpoint socket close failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := endpointLifecycleContext(t)
			serverConfig, clientConfig := endpointLifecycleTLS(t)
			serverConfig.TLSConfig.CurvePreferences = []tls.CurveID{tls.X25519}
			clientConfig.TLSConfig.CurvePreferences = []tls.CurveID{tls.X25519}
			serverPC := newEndpointLifecycleSocket(ctx, 1234)
			clientPC := newEndpointLifecycleSocket(ctx, 1235)
			serverPC.peer = clientPC
			serverPC.closeErr = tc.err
			clientPC.peer = serverPC
			clientPC.initialOnly = true
			server := openEndpointLifecycle(t, serverPC, serverConfig)
			client := openEndpointLifecycle(t, clientPC, nil)

			clientConn, err := client.Dial(ctx, "udp", server.LocalAddr().String(), clientConfig)
			if err != nil {
				t.Fatalf("receive server handshake flight: %v", err)
			}
			t.Cleanup(func() {
				joinCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				forceCtx, force := context.WithCancel(joinCtx)
				force()
				if err := client.Close(forceCtx); err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("force close test client: %v", err)
				}
				endpointLifecycleReceive(t, joinCtx, clientConn.donec)
			})

			server.connsMu.Lock()
			pending := server.snapshotConnsLocked()
			server.connsMu.Unlock()
			if len(pending) != 1 {
				t.Fatalf("server has %d connections, want one unconfirmed handshake", len(pending))
			}
			unconfirmed := make(chan bool, 1)
			select {
			case pending[0].msgc <- func(_ time.Time, c *Conn) {
				unconfirmed <- c.isAlive() && !c.handshakeConfirmed.isSet()
			}:
			case <-pending[0].donec:
				t.Fatal("server connection exited before Endpoint.Close")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !endpointLifecycleReceive(t, ctx, unconfirmed) {
				t.Fatal("client handshake flight completed the server handshake")
			}

			closeCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := server.Close(closeCtx); !errors.Is(err, tc.err) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close with an unconfirmed server handshake = %v, want %v", err, tc.err)
			}
			select {
			case <-serverPC.closed:
			default:
				t.Fatal("Close returned without releasing the packet socket")
			}
			if n := serverPC.closeCalls.Load(); n != 1 {
				t.Fatalf("packet socket Close called %d times, want 1", n)
			}
			if err := clientConn.Wait(ctx); err != nil {
				t.Fatalf("Wait after peer transport NO_ERROR = %v, want nil", err)
			}
			if err := clientConn.Close(); err != nil {
				t.Fatalf("Close after peer transport NO_ERROR = %v, want nil", err)
			}
			endpointLifecycleReceive(t, ctx, pending[0].donec)
		})
	}
}

func TestConnWaitAndClosePreservePeerApplicationError(t *testing.T) {
	ctx := endpointLifecycleContext(t)
	serverConfig, clientConfig := endpointLifecycleTLS(t)
	serverPC := newEndpointLifecycleSocket(ctx, 1234)
	clientPC := newEndpointLifecycleSocket(ctx, 1235)
	serverPC.peer = clientPC
	clientPC.peer = serverPC
	server := openEndpointLifecycle(t, serverPC, serverConfig)
	client := openEndpointLifecycle(t, clientPC, nil)

	clientConn, err := client.Dial(ctx, "udp", server.LocalAddr().String(), clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	serverConn, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := ApplicationError{Code: 0, Reason: "application closed"}
	serverConn.Abort(&want)

	for _, result := range []struct {
		name string
		call func() error
	}{
		{"Wait", func() error { return clientConn.Wait(ctx) }},
		{"Close", clientConn.Close},
	} {
		err := result.call()
		got, ok := errors.AsType[*ApplicationError](err)
		if !ok || *got != want {
			t.Fatalf("%s after peer application close = %v, want %v", result.name, err, &want)
		}
	}
}

func TestEndpointStopAccepting(t *testing.T) {
	ctx := endpointLifecycleContext(t)
	serverConfig, clientConfig := endpointLifecycleTLS(t)
	serverPC := newEndpointLifecycleSocket(ctx, 1234)
	clientPC := newEndpointLifecycleSocket(ctx, 1235)
	serverPC.peer = clientPC
	clientPC.peer = serverPC
	server := openEndpointLifecycle(t, serverPC, serverConfig)
	client := openEndpointLifecycle(t, clientPC, nil)
	client.StopAccepting()
	if _, err := client.Dial(ctx, "udp", server.LocalAddr().String(), clientConfig); err != nil {
		t.Fatalf("outbound Dial after StopAccepting: %v", err)
	}

	server.StopAccepting()
	server.StopAccepting()
	c, err := server.Accept(ctx)
	if err != nil {
		t.Fatalf("Accept of connection admitted before StopAccepting: %v", err)
	}

	probePC := newEndpointLifecycleSocket(ctx, 1236)
	probePC.peer = serverPC
	probe := openEndpointLifecycle(t, probePC, nil)
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	dialed := make(chan error, 1)
	go func() {
		_, err := probe.Dial(probeCtx, "udp", server.LocalAddr().String(), clientConfig)
		dialed <- err
	}()
	endpointLifecycleReceive(t, ctx, probePC.firstWrite)
	processed := make(chan struct{})
	select {
	case serverPC.in <- endpointLifecyclePacket{barrier: processed}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	endpointLifecycleReceive(t, ctx, processed)
	server.connsMu.Lock()
	connections := len(server.conns)
	server.connsMu.Unlock()
	if connections != 1 {
		t.Fatalf("server has %d connections after a new Initial, want the original connection only", connections)
	}
	alive := make(chan bool, 1)
	select {
	case c.msgc <- func(_ time.Time, c *Conn) { alive <- c.isAlive() }:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !endpointLifecycleReceive(t, ctx, alive) {
		t.Fatal("StopAccepting terminated the preexisting connection")
	}
	cancel()
	if err := endpointLifecycleReceive(t, ctx, dialed); !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial after stopping admission = %v, want canceled while waiting for handshake", err)
	}
}

func endpointLifecycleContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func endpointLifecycleReceive[T any](t *testing.T, ctx context.Context, c <-chan T) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}

type endpointLifecycleWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *endpointLifecycleWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type endpointLifecyclePacket struct {
	bytes   []byte
	from    net.Addr
	err     error
	barrier chan struct{}
}

type endpointLifecycleSocket struct {
	ctx        context.Context
	addr       *net.UDPAddr
	peer       *endpointLifecycleSocket
	in         chan endpointLifecyclePacket
	closed     chan struct{}
	firstWrite chan struct{}
	writeOnce  sync.Once
	closeCalls atomic.Int32
	closeErr   error

	initialOnly bool
	initialSent atomic.Bool
}

func newEndpointLifecycleSocket(ctx context.Context, port uint16) *endpointLifecycleSocket {
	return &endpointLifecycleSocket{
		ctx:        ctx,
		addr:       net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port)),
		in:         make(chan endpointLifecyclePacket, 16),
		closed:     make(chan struct{}),
		firstWrite: make(chan struct{}),
	}
}

func openEndpointLifecycle(t *testing.T, pc *endpointLifecycleSocket, cfg *Config) *Endpoint {
	t.Helper()
	e, err := NewEndpoint(pc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		e.Close(ctx)
	})
	return e
}

func (c *endpointLifecycleSocket) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		select {
		case p := <-c.in:
			if p.barrier != nil {
				close(p.barrier)
				continue
			}
			return copy(b, p.bytes), p.from, p.err
		case <-c.closed:
			return 0, nil, net.ErrClosed
		case <-c.ctx.Done():
			return 0, nil, c.ctx.Err()
		}
	}
}

func (c *endpointLifecycleSocket) WriteTo(b []byte, _ net.Addr) (int, error) {
	if c.peer == nil {
		c.writeOnce.Do(func() { close(c.firstWrite) })
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-c.ctx.Done():
			return 0, c.ctx.Err()
		}
	}
	if c.initialOnly {
		if !c.initialSent.CompareAndSwap(false, true) {
			return len(b), nil
		}
		if getPacketType(b) != packetTypeInitial {
			return 0, errors.New("initial-only test socket first datagram is not Initial")
		}
	}
	select {
	case c.peer.in <- endpointLifecyclePacket{bytes: bytes.Clone(b), from: c.addr}:
		c.writeOnce.Do(func() { close(c.firstWrite) })
		return len(b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	case <-c.peer.closed:
		return 0, net.ErrClosed
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	}
}

func (c *endpointLifecycleSocket) Close() error {
	if c.closeCalls.Add(1) == 1 {
		close(c.closed)
	}
	return c.closeErr
}

func (c *endpointLifecycleSocket) LocalAddr() net.Addr              { return c.addr }
func (c *endpointLifecycleSocket) SetDeadline(time.Time) error      { return errors.ErrUnsupported }
func (c *endpointLifecycleSocket) SetReadDeadline(time.Time) error  { return errors.ErrUnsupported }
func (c *endpointLifecycleSocket) SetWriteDeadline(time.Time) error { return errors.ErrUnsupported }

func endpointLifecycleTLS(t *testing.T) (server, client *Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<32, 0),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	const protocol = "quic-lifecycle"
	return &Config{TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{protocol},
	}}, &Config{TLSConfig: &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		NextProtos: []string{protocol},
		ServerName: "127.0.0.1",
	}}
}
