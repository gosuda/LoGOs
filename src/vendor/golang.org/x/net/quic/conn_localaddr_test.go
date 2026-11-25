// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestConnLocalAddrPacketConn(t *testing.T) {
	const serverPort = 1234
	loopback := netip.MustParseAddrPort("127.0.0.1:1234")
	other := netip.MustParseAddrPort("127.0.0.2:1234")
	unknown := netip.AddrPortFrom(netip.Addr{}, serverPort)
	for _, tc := range []struct {
		name      string
		bind      netip.AddrPort
		initial   netip.AddrPort
		published netip.AddrPort
	}{
		{"unicast", loopback, loopback, loopback},
		{"wildcard4", netip.MustParseAddrPort("0.0.0.0:1234"), unknown, other},
		{"wildcard6", netip.MustParseAddrPort("[::]:1234"), unknown, other},
		{"wildcard4Mapped", netip.MustParseAddrPort("[::ffff:0.0.0.0]:1234"), unknown, other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := endpointLifecycleContext(t)
			serverConfig, clientConfig := endpointLifecycleTLS(t)
			serverPC := newEndpointLifecycleSocket(ctx, serverPort)
			clientPC := newEndpointLifecycleSocket(ctx, 1235)
			serverPC.peer = clientPC
			clientPC.peer = serverPC
			server, err := NewEndpoint(localAddrTestSocket{serverPC, net.UDPAddrFromAddrPort(tc.bind)}, serverConfig)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				server.Close(ctx)
			})
			if got := server.LocalAddr(); got != tc.bind {
				t.Fatalf("endpoint LocalAddr = %v, want bind address %v", got, tc.bind)
			}
			client := openEndpointLifecycle(t, clientPC, nil)
			if _, err := client.Dial(ctx, "udp", loopback.String(), clientConfig); err != nil {
				t.Fatal(err)
			}
			c, err := server.Accept(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.LocalAddr(); got != tc.initial {
				t.Fatalf("accepted Conn.LocalAddr = %v, want %v", got, tc.initial)
			}

			// Supply destination metadata that a wildcard PacketConn cannot report.
			before := tc.initial
			for _, destination := range []netip.AddrPort{other, loopback} {
				select {
				case c.msgc <- &datagram{localAddr: destination, peerAddr: c.RemoteAddr()}:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if got := c.LocalAddr(); got != before && got != tc.published {
					t.Fatalf("Conn.LocalAddr during packet handling = %v, want %v or %v", got, before, tc.published)
				}
				processed := make(chan struct{})
				select {
				case c.msgc <- func(time.Time, *Conn) { close(processed) }:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				endpointLifecycleReceive(t, ctx, processed)
				if got := c.LocalAddr(); got != tc.published {
					t.Fatalf("Conn.LocalAddr after packet to %v = %v, want %v", destination, got, tc.published)
				}
				before = tc.published
			}
		})
	}
}

type localAddrTestSocket struct {
	*endpointLifecycleSocket
	bindAddr *net.UDPAddr
}

func (s localAddrTestSocket) LocalAddr() net.Addr { return s.bindAddr }
