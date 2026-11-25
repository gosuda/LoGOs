// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"sync"
	"time"

	"golang.org/x/net/quic"
)

const (
	h3ServerHandshakeTimeout = 5 * time.Second
	h3ServerIdleTimeout      = 30 * time.Second
	h3ServerSettingsTimeout  = 5 * time.Second
	h3ServerCloseTimeout     = 5 * time.Second
	h3ServerBidiStreams      = 100
	h3ServerUniStreams       = 100
	h3ServerLastRequestID    = (1 << 62) - 4

	h3ServerNoError              = 0x100
	h3ServerStreamCreationError  = 0x103
	h3ServerClosedCriticalStream = 0x104
	h3ServerMissingSettings      = 0x10a
	h3ServerRequestRejected      = 0x10b
)

// H3Server serves HTTP/3 over a packet connection without 0-RTT or datagrams.
// An H3Server must not be copied after first use. Its configuration must not
// change during Serve.
type H3Server struct {
	// Handler handles requests. If nil, Serve uses [net/http.DefaultServeMux].
	Handler stdhttp.Handler

	// TLSConfig provides the server credentials. Serve clones the configuration
	// and uses TLS 1.3 with the "h3" application protocol.
	TLSConfig *tls.Config

	// MaxHeaderBytes bounds both the encoded request header block and its
	// decoded field section. Nonpositive values use [net/http.DefaultMaxHeaderBytes].
	MaxHeaderBytes int

	mu     sync.Mutex
	closed bool
	run    *h3ServerRun
}

var (
	errH3ServerTLS     = errors.New("http3: server needs a TLS configuration that allows TLS 1.3")
	errH3ServerCert    = errors.New("http3: server TLS configuration has no certificate")
	errH3ServerSocket  = errors.New("http3: server needs a packet connection")
	errH3ServerServing = errors.New("http3: packet connection already being served")
)

type h3ServerRun struct {
	requests h3RequestStreamConfig

	admission     sync.Mutex
	conns         map[*h3ServerConn]struct{}
	draining      <-chan struct{}
	stopAccept    context.CancelFunc
	acceptStopped chan struct{}

	forced        <-chan struct{}
	forceClose    context.CancelFunc
	transportDone chan struct{}
	done          chan struct{}
	closeErr      error
}

// Serve serves HTTP/3 on pc. It takes ownership of pc only after successful
// setup. Serve returns [net/http.ErrServerClosed] after a clean shutdown.
func (s *H3Server) Serve(pc net.PacketConn) error {
	run, acceptCtx, forceCtx, cfg, err := s.beginServe(pc)
	if err != nil {
		return err
	}
	defer s.endServe(run)
	if acceptCtx.Err() != nil {
		close(run.transportDone)
		return stdhttp.ErrServerClosed
	}
	endpoint, err := quic.NewEndpoint(pc, cfg)
	if err != nil {
		close(run.transportDone)
		return fmt.Errorf("http3: create QUIC endpoint: %w", err)
	}
	return run.serve(acceptCtx, forceCtx, endpoint)
}

func (s *H3Server) beginServe(pc net.PacketConn) (*h3ServerRun, context.Context, context.Context, *quic.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, nil, nil, stdhttp.ErrServerClosed
	}
	if s.run != nil {
		return nil, nil, nil, nil, errH3ServerServing
	}
	if pc == nil {
		return nil, nil, nil, nil, errH3ServerSocket
	}
	tlsConfig, err := h3ServerTLS(s.TLSConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	handler := s.Handler
	if handler == nil {
		handler = stdhttp.DefaultServeMux
	}
	maxHeaderBytes := s.MaxHeaderBytes
	if maxHeaderBytes <= 0 {
		maxHeaderBytes = stdhttp.DefaultMaxHeaderBytes
	}
	acceptCtx, stopAccept := context.WithCancel(context.Background())
	forceCtx, forceClose := context.WithCancel(context.Background())
	run := &h3ServerRun{
		requests: h3RequestStreamConfig{
			handler:             handler,
			maxHeaderBytes:      uint64(maxHeaderBytes),
			maxFieldSectionSize: min(uint64(maxHeaderBytes), 1<<62-1),
		},
		conns:         make(map[*h3ServerConn]struct{}),
		draining:      acceptCtx.Done(),
		stopAccept:    stopAccept,
		acceptStopped: make(chan struct{}),
		forced:        forceCtx.Done(),
		forceClose:    forceClose,
		transportDone: make(chan struct{}),
		done:          make(chan struct{}),
	}
	s.run = run
	return run, acceptCtx, forceCtx, &quic.Config{
		TLSConfig:            tlsConfig,
		MaxBidiRemoteStreams: h3ServerBidiStreams,
		MaxUniRemoteStreams:  h3ServerUniStreams,
		HandshakeTimeout:     h3ServerHandshakeTimeout,
		MaxIdleTimeout:       h3ServerIdleTimeout,
	}, nil
}

func h3ServerTLS(config *tls.Config) (*tls.Config, error) {
	if config == nil || config.MinVersion > tls.VersionTLS13 ||
		(config.MaxVersion != 0 && config.MaxVersion < tls.VersionTLS13) {
		return nil, errH3ServerTLS
	}
	if len(config.Certificates) == 0 && config.GetCertificate == nil && config.GetConfigForClient == nil {
		return nil, errH3ServerCert
	}
	owned := config.Clone()
	owned.MinVersion = tls.VersionTLS13
	owned.NextProtos = []string{"h3"}
	if choose := config.GetConfigForClient; choose != nil {
		owned.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			selected, err := choose(hello)
			if selected == nil || err != nil {
				return nil, err
			}
			return h3ServerTLS(selected)
		}
	}
	return owned, nil
}

func (s *H3Server) endServe(run *h3ServerRun) {
	run.startDrain()
	run.forceClose()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = nil
	close(run.done)
}

func (s *H3Server) stopServing() *h3ServerRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.run != nil {
		s.run.startDrain()
	}
	return s.run
}

// Shutdown stops new requests, sends GOAWAY, and waits for admitted requests
// and their responses to finish. If ctx ends first, Shutdown initiates a
// forced close and returns ctx.Err(). A server cannot be reused after Shutdown.
func (s *H3Server) Shutdown(ctx context.Context) error {
	run := s.stopServing()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		return run.closeErr
	case <-ctx.Done():
		select {
		case <-run.done:
			return run.closeErr
		default:
		}
		run.forceClose()
		return ctx.Err()
	}
}

// Close force-closes the transport and cancels active requests. It waits up
// to five seconds for the transport to close. Serve still owns any handlers
// that have not yet returned. A server cannot be reused after Close.
func (s *H3Server) Close() error {
	run := s.stopServing()
	if run == nil {
		return nil
	}
	run.forceClose()
	ctx, cancel := context.WithTimeout(context.Background(), h3ServerCloseTimeout)
	defer cancel()
	select {
	case <-run.transportDone:
		return run.closeErr
	case <-ctx.Done():
		return fmt.Errorf("http3: close transport: %w", ctx.Err())
	}
}

func (r *h3ServerRun) startDrain() {
	r.admission.Lock()
	defer r.admission.Unlock()
	r.stopAccept()
}

func (r *h3ServerRun) serve(ctx context.Context, forceCtx context.Context, endpoint *quic.Endpoint) error {
	var connections sync.WaitGroup
	acceptErr := r.acceptConnections(ctx, endpoint, &connections)
	if acceptErr != nil {
		r.startDrain()
		r.forceClose()
	}
	endpoint.StopAccepting()
	close(r.acceptStopped)
	rejectCtx, stopRejecting := context.WithCancel(forceCtx)
	defer stopRejecting()
	connectionsDone := make(chan struct{})
	go func() {
		connections.Wait()
		stopRejecting()
		close(connectionsDone)
	}()
	if acceptErr == nil {
		acceptErr = rejectH3ServerConnections(rejectCtx, forceCtx, endpoint)
	}
	if acceptErr != nil || forceCtx.Err() != nil {
		r.forceClose()
		r.abortConnections()
	}
	r.closeErr = closeH3ServerEndpoint(endpoint)
	close(r.transportDone)
	<-connectionsDone
	if err := errors.Join(acceptErr, r.closeErr); err != nil {
		return err
	}
	return stdhttp.ErrServerClosed
}

func closeH3ServerEndpoint(endpoint *quic.Endpoint) error {
	ctx, cancel := context.WithTimeout(context.Background(), h3ServerCloseTimeout)
	defer cancel()
	err := endpoint.Close(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(err, endpoint.Close(context.Background()))
	}
	return err
}

func (r *h3ServerRun) acceptConnections(ctx context.Context, endpoint *quic.Endpoint, workers *sync.WaitGroup) error {
	for ctx.Err() == nil {
		conn, err := endpoint.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("http3: accept QUIC connection: %w", err)
		}
		if !r.serveConnection(conn, workers) {
			conn.Abort(&quic.ApplicationError{Code: h3ServerNoError})
			return nil
		}
	}
	return nil
}

func rejectH3ServerConnections(ctx context.Context, forceCtx context.Context, endpoint *quic.Endpoint) error {
	for forceCtx.Err() == nil {
		conn, err := endpoint.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("http3: reject pending QUIC connections: %w", err)
		}
		conn.Abort(&quic.ApplicationError{Code: h3ServerNoError})
	}
	return nil
}

func (r *h3ServerRun) serveConnection(conn *quic.Conn, workers *sync.WaitGroup) bool {
	r.admission.Lock()
	defer r.admission.Unlock()
	select {
	case <-r.draining:
		return false
	default:
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	c := &h3ServerConn{
		run:           r,
		conn:          conn,
		cancel:        cancel,
		settingsReady: make(chan struct{}),
		requests:      r.requests,
	}
	r.conns[c] = struct{}{}
	workers.Go(func() {
		defer r.releaseConnection(c)
		c.serve(ctx)
	})
	return true
}

func (r *h3ServerRun) releaseConnection(c *h3ServerConn) {
	r.admission.Lock()
	defer r.admission.Unlock()
	delete(r.conns, c)
}

func (r *h3ServerRun) abortConnections() {
	r.admission.Lock()
	conns := make([]*h3ServerConn, 0, len(r.conns))
	for c := range r.conns {
		conns = append(conns, c)
	}
	r.admission.Unlock()
	for _, c := range conns {
		c.abort(h3ServerNoError)
	}
}

type h3ServerConn struct {
	run    *h3ServerRun
	conn   *quic.Conn
	cancel context.CancelCauseFunc
	close  sync.Once

	settingsReady chan struct{}
	requests      h3RequestStreamConfig
	active        sync.WaitGroup
	nextRequestID uint64
}

func (c *h3ServerConn) abort(code uint64) {
	c.close.Do(func() {
		err := &quic.ApplicationError{Code: code}
		c.cancel(err)
		c.conn.Abort(err)
	})
}

func (c *h3ServerConn) serve(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Go(func() { c.cancel(c.conn.Wait(ctx)) })
	settingsCtx, cancelSettings := context.WithTimeout(ctx, h3ServerSettingsTimeout)
	defer cancelSettings()
	control, err := startH3Control(ctx, c.conn, c.requests.maxFieldSectionSize)
	defer func() {
		c.cancel(nil)
		workers.Wait()
		if control != nil {
			control.wait()
		}
	}()
	if err != nil {
		if ctx.Err() == nil {
			c.abort(h3ServerStreamCreationError)
		}
		return
	}
	workers.Go(func() {
		defer cancelSettings()
		c.awaitSettings(settingsCtx, control)
	})
	workers.Go(func() { c.drain(ctx, control) })
	for ctx.Err() == nil {
		stream, err := c.conn.AcceptStream(ctx)
		if err != nil {
			<-ctx.Done()
			return
		}
		workers.Go(func() {
			if stream.IsReadOnly() {
				// Protocol errors abort inside the control state. Conn.Wait
				// handles transport closure for all workers.
				_ = control.handlePeerUni(ctx, stream)
				return
			}
			c.serveRequest(ctx, stream)
		})
	}
}

func (c *h3ServerConn) awaitSettings(ctx context.Context, control *h3Control) {
	settings, err := control.awaitPeerSettings(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			c.abort(h3ServerMissingSettings)
		}
		return
	}
	c.requests.peerFieldSectionSize = settings.maxFieldSectionSize
	close(c.settingsReady)
}

func (c *h3ServerConn) drain(ctx context.Context, control *h3Control) {
	select {
	case <-ctx.Done():
		return
	case <-c.run.acceptStopped:
	}
	select {
	case <-c.run.forced:
		return
	default:
	}
	c.run.admission.Lock()
	cutoff := c.nextRequestID
	c.run.admission.Unlock()
	if err := control.sendGoAway(ctx, cutoff); err != nil {
		if ctx.Err() == nil {
			c.abort(h3ServerClosedCriticalStream)
		}
		return
	}
	c.active.Wait()
	if err := control.waitGoAwayAck(ctx); err != nil {
		if ctx.Err() == nil {
			c.abort(h3ServerClosedCriticalStream)
		}
		return
	}
	if ctx.Err() == nil {
		c.abort(h3ServerNoError)
	}
}

func (c *h3ServerConn) admitRequest(ctx context.Context, id uint64) bool {
	c.run.admission.Lock()
	defer c.run.admission.Unlock()
	select {
	case <-c.run.draining:
		return false
	default:
	}
	// Reserve the last client bidi ID so that the GOAWAY cutoff always fits.
	if id >= h3ServerLastRequestID || ctx.Err() != nil {
		return false
	}
	c.nextRequestID = max(c.nextRequestID, id+4)
	c.active.Add(1)
	return true
}

func (c *h3ServerConn) serveRequest(ctx context.Context, stream *quic.Stream) {
	select {
	case <-ctx.Done():
		return
	case <-c.run.draining:
	case <-c.settingsReady:
		if c.admitRequest(ctx, uint64(stream.ID())) {
			defer c.active.Done()
			serveH3RequestStream(ctx, c.conn, stream, c.requests)
			return
		}
	}
	stream.AbortRead(h3ServerRequestRejected)
	stream.Reset(h3ServerRequestRejected)
}
