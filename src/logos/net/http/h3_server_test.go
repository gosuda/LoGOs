// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	stdhttp "net/http"
	"slices"
	"testing"
)

// Embedded operations panic if validation ever touches the caller's socket.
type h3UntouchedPacketConn struct{ net.PacketConn }

func TestH3ServerSetupFailuresDoNotTouchPacketConn(t *testing.T) {
	pc := h3UntouchedPacketConn{}
	for _, tc := range []struct {
		name string
		tls  *tls.Config
		pc   net.PacketConn
		want error
	}{
		{name: "no TLS", pc: pc, want: errH3ServerTLS},
		{name: "incompatible maximum", tls: &tls.Config{MaxVersion: tls.VersionTLS12}, pc: pc, want: errH3ServerTLS},
		{name: "unsupported minimum", tls: &tls.Config{MinVersion: tls.VersionTLS13 + 1}, pc: pc, want: errH3ServerTLS},
		{name: "no certificate", tls: &tls.Config{}, pc: pc, want: errH3ServerCert},
		{name: "no socket", tls: &tls.Config{Certificates: []tls.Certificate{{}}}, want: errH3ServerSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &H3Server{TLSConfig: tc.tls}
			if err := s.Serve(tc.pc); !errors.Is(err, tc.want) {
				t.Fatalf("Serve = %v, want %v", err, tc.want)
			}
			if s.run != nil || s.closed {
				t.Fatal("setup failure changed the server lifecycle")
			}
		})
	}
}

func TestH3ServerCannotServeAfterStop(t *testing.T) {
	for _, method := range []string{"Shutdown", "Close"} {
		t.Run(method, func(t *testing.T) {
			var s H3Server
			var err error
			switch method {
			case "Shutdown":
				err = s.Shutdown(t.Context())
			case "Close":
				err = s.Close()
			}
			if err != nil {
				t.Fatalf("%s before Serve: %v", method, err)
			}
			if err := s.Serve(h3UntouchedPacketConn{}); !errors.Is(err, stdhttp.ErrServerClosed) {
				t.Fatalf("Serve after %s = %v, want ErrServerClosed", method, err)
			}
		})
	}
}

func TestH3ServerReservationAndDefaults(t *testing.T) {
	s := &H3Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{{}}}}
	pc := h3UntouchedPacketConn{}
	run, _, _, _, err := s.beginServe(pc)
	if err != nil {
		t.Fatal(err)
	}
	defer s.endServe(run)
	if run.requests.handler != stdhttp.DefaultServeMux ||
		run.requests.maxHeaderBytes != stdhttp.DefaultMaxHeaderBytes ||
		run.requests.maxFieldSectionSize != stdhttp.DefaultMaxHeaderBytes {
		t.Fatalf("request defaults = %+v", run.requests)
	}
	if err := s.Serve(pc); !errors.Is(err, errH3ServerServing) {
		t.Fatalf("second Serve = %v, want %v", err, errH3ServerServing)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown = %v, want context.Canceled", err)
	}
	select {
	case <-run.draining:
	default:
		t.Fatal("Shutdown did not close admission")
	}
	select {
	case <-run.forced:
	default:
		t.Fatal("canceled Shutdown did not force closure")
	}
}

func TestH3ServerTLSClonesSelectedConfig(t *testing.T) {
	selected := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, Certificates: []tls.Certificate{{}}}
	config := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	config.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return selected, nil
	}
	owned, err := h3ServerTLS(config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := owned.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got == selected || owned == config {
		t.Fatal("server did not clone both TLS configurations")
	}
	for _, c := range []*tls.Config{owned, got} {
		if c.MinVersion != tls.VersionTLS13 || !slices.Equal(c.NextProtos, []string{"h3"}) {
			t.Fatalf("configured minimum version/ALPN = %x/%v, want TLS 1.3/[h3]", c.MinVersion, c.NextProtos)
		}
	}
	if config.MinVersion != tls.VersionTLS12 || !slices.Equal(config.NextProtos, []string{"h2", "http/1.1"}) ||
		selected.MinVersion != tls.VersionTLS12 || !slices.Equal(selected.NextProtos, []string{"h2"}) {
		t.Fatal("server mutated a caller TLS configuration")
	}
	selected = &tls.Config{MaxVersion: tls.VersionTLS12}
	if got, err := owned.GetConfigForClient(&tls.ClientHelloInfo{}); got != nil || !errors.Is(err, errH3ServerTLS) {
		t.Fatalf("callback selected incompatible config: got %v, err %v", got, err)
	}
	selected = nil
	if got, err := owned.GetConfigForClient(&tls.ClientHelloInfo{}); got != nil || err != nil {
		t.Fatalf("callback fallback: got %v, err %v, want default config", got, err)
	}
}

func TestH3ServerAdmissionCutoffUsesHighestRequestID(t *testing.T) {
	s := &H3Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{{}}}}
	run, _, _, _, err := s.beginServe(h3UntouchedPacketConn{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.endServe(run)
	conn := &h3ServerConn{run: run}
	for _, id := range []uint64{12, 0, 8} {
		if !conn.admitRequest(t.Context(), id) {
			t.Fatalf("rejected stream %d before drain", id)
		}
		conn.active.Done()
	}
	if conn.admitRequest(t.Context(), h3ServerLastRequestID) {
		t.Fatal("admitted a stream whose GOAWAY successor cannot be encoded")
	}
	run.startDrain()
	if conn.admitRequest(t.Context(), 4) {
		t.Fatal("admitted a new stream below the high-water mark after drain")
	}
	if conn.nextRequestID != 16 {
		t.Fatalf("GOAWAY cutoff = %d, want 16", conn.nextRequestID)
	}
}
