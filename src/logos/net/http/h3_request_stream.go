// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	stdhttp "net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"golang.org/x/net/quic"
)

const (
	h3RequestCodeNoError       = 0x100
	h3RequestCodeInternal      = 0x102
	h3RequestCodeUnexpected    = 0x105
	h3RequestCodeFrameError    = 0x106
	h3RequestCodeExcessiveLoad = 0x107
	h3RequestCodeCancelled     = 0x10c
	h3RequestCodeIncomplete    = 0x10d
	h3RequestCodeMessageError  = 0x10e
	h3RequestCodeQPACK         = 0x200
)

var errH3MissingRequestHeaders = errors.New("http3: request stream ended before HEADERS")

type h3RequestStreamConfig struct {
	handler              stdhttp.Handler
	maxHeaderBytes       uint64
	maxFieldSectionSize  uint64
	peerFieldSectionSize uint64
}

func serveH3RequestStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, cfg h3RequestStreamConfig) {
	streamCtx, cancelStream := context.WithCancel(ctx)
	wire := newH3DeadlineStream(streamCtx, stream)
	requestCtx, cancelRequest := context.WithCancelCause(streamCtx)
	body := &h3RequestStreamBody{
		conn: conn, stream: stream, streamCtx: streamCtx, cancel: cancelRequest, cancelStream: cancelStream,
	}

	peerCanceled := stream.PeerCanceled()
	var watcher sync.WaitGroup
	watcher.Go(func() {
		select {
		case <-peerCanceled:
			cancelRequest(nil)
			stream.AbortRead(h3RequestCodeCancelled)
		case <-requestCtx.Done():
		}
	})
	defer func() {
		cancelRequest(nil)
		cancelStream()
		watcher.Wait()
	}()

	reader := bufio.NewReader(wire)
	req, err := readH3RequestHead(reader, cfg)
	if err != nil {
		body.rejectHead(wire, cfg.peerFieldSectionSize, err)
		return
	}
	select {
	case <-peerCanceled:
		body.abort(h3RequestCodeCancelled)
		return
	case <-requestCtx.Done():
		body.abort(h3RequestCodeCancelled)
		return
	default:
	}

	localAddr := net.UDPAddrFromAddrPort(conn.LocalAddr())
	*req = *req.WithContext(context.WithValue(requestCtx, stdhttp.LocalAddrContextKey, localAddr))
	req.TLS = new(conn.ConnectionState())
	req.RemoteAddr = conn.RemoteAddr().String()
	if req.Trailer == nil {
		req.Trailer = make(stdhttp.Header)
	}
	body.reader = newH3RequestBodyReader(reader, req.Trailer, h3RequestBodyLimits{
		contentLength:       req.ContentLength,
		maxTrailerBytes:     cfg.maxHeaderBytes,
		maxFieldSectionSize: cfg.maxFieldSectionSize,
	})
	req.Body = body
	writer := newH3ResponseWriter(wire, req.Method, cfg.peerFieldSectionSize)
	serveH3Request(req, body, writer, cfg.handler)
}

func serveH3Request(req *stdhttp.Request, body *h3RequestStreamBody, writer *h3ResponseWriter, handler stdhttp.Handler) {
	defer body.abort(h3RequestCodeInternal)
	if _, extended := req.Header[":protocol"]; extended {
		writer.Header()["Date"] = nil
		writer.WriteHeader(stdhttp.StatusNotImplemented)
	} else {
		if handler == nil {
			handler = stdhttp.DefaultServeMux
		}
		if invokeH3Handler(handler, writer, req) {
			body.cancel(nil)
			return
		}
	}
	body.cancel(nil)
	body.finish(writer, h3RequestCodeCancelled)
}

func invokeH3Handler(handler stdhttp.Handler, writer stdhttp.ResponseWriter, req *stdhttp.Request) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			panicked = true
			if err, ok := p.(error); !ok || !errors.Is(err, stdhttp.ErrAbortHandler) {
				slog.Error("http3: panic serving request", "stack", string(debug.Stack()))
			}
		}
	}()
	handler.ServeHTTP(writer, req)
	return false
}

func readH3RequestHead(reader *bufio.Reader, cfg h3RequestStreamConfig) (*stdhttp.Request, error) {
	frame, err := readH3InitialFrame(reader)
	if err != nil {
		return nil, err
	}
	if frame.length > cfg.maxHeaderBytes || frame.length > uint64(math.MaxInt) {
		return nil, errH3FieldSectionTooLarge
	}
	// Decoded fields can borrow this block after the buffered reader refills.
	block := make([]byte, int(frame.length))
	if _, err := io.ReadFull(reader, block); err != nil {
		return nil, h3RequestFrameReadError(err)
	}
	return decodeH3Request(block, cfg.maxFieldSectionSize)
}

func readH3InitialFrame(reader *bufio.Reader) (h3FrameHeader, error) {
	for {
		if _, err := reader.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				return h3FrameHeader{}, errH3MissingRequestHeaders
			}
			return h3FrameHeader{}, err
		}
		frame, err := readH3FrameHeader(reader)
		if err != nil {
			return h3FrameHeader{}, h3RequestFrameReadError(err)
		}
		switch frame.kind {
		case 0x1:
			return frame, nil
		case 0x0, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xd, 0xf0700, 0xf0701:
			return h3FrameHeader{}, fmt.Errorf("%w: type %#x before HEADERS", errH3FrameUnexpected, frame.kind)
		}
		for remaining := frame.length; remaining > 0; {
			size := min(remaining, uint64(math.MaxInt))
			if _, err := reader.Discard(int(size)); err != nil {
				return h3FrameHeader{}, h3RequestFrameReadError(err)
			}
			remaining -= size
		}
	}
}

// RFC 9114 and RFC 9204 make these connection errors, even on a request stream.
func h3RequestConnectionError(err error) (uint64, bool) {
	switch {
	case errors.Is(err, errH3FrameUnexpected):
		return h3RequestCodeUnexpected, true
	case errors.Is(err, errH3FrameTruncated):
		return h3RequestCodeFrameError, true
	case errors.Is(err, errH3QPACK):
		return h3RequestCodeQPACK, true
	default:
		return 0, false
	}
}

type h3RequestStreamBody struct {
	reader       *h3RequestBodyReader
	conn         *quic.Conn
	stream       *quic.Stream
	streamCtx    context.Context
	cancel       context.CancelCauseFunc
	cancelStream context.CancelFunc
	stopReading  sync.Once
	readClosed   atomic.Bool
	responseDone atomic.Bool
}

func (b *h3RequestStreamBody) rejectHead(wire *h3DeadlineStream, peerLimit uint64, err error) {
	if code, ok := h3RequestConnectionError(err); ok {
		b.conn.Abort(&quic.ApplicationError{Code: code})
		return
	}
	switch {
	case errors.Is(err, errH3FieldSectionTooLarge):
		b.stopRead(h3RequestCodeExcessiveLoad)
		writer := newH3ResponseWriter(wire, stdhttp.MethodGet, peerLimit)
		writer.Header()["Date"] = nil
		writer.WriteHeader(stdhttp.StatusRequestHeaderFieldsTooLarge)
		b.finish(writer, h3RequestCodeExcessiveLoad)
	case errors.Is(err, errH3MalformedMessage):
		b.abort(h3RequestCodeMessageError)
	case errors.Is(err, errH3MissingRequestHeaders):
		b.abort(h3RequestCodeIncomplete)
	default:
		b.abort(h3RequestCodeCancelled)
	}
}

func (b *h3RequestStreamBody) Read(p []byte) (int, error) {
	if b.readClosed.Load() {
		return 0, stdhttp.ErrBodyReadAfterClose
	}
	n, err := b.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.handleReadError(err)
	}
	return n, err
}

func (b *h3RequestStreamBody) handleReadError(err error) {
	if code, ok := h3RequestConnectionError(err); ok {
		b.cancel(err)
		b.responseDone.Store(true)
		b.readClosed.Store(true)
		b.cancelStream()
		b.conn.Abort(&quic.ApplicationError{Code: code})
		return
	}
	switch {
	case errors.Is(err, errH3MalformedMessage):
		b.cancel(err)
		b.abort(h3RequestCodeMessageError)
	case errors.Is(err, errH3FieldSectionTooLarge):
		b.cancel(err)
		b.abort(h3RequestCodeExcessiveLoad)
	case h3PeerCanceledRead(err, b.stream):
		b.cancel(err)
	}
}

func h3PeerCanceledRead(err error, stream *quic.Stream) bool {
	if _, ok := errors.AsType[quic.StreamErrorCode](err); !ok {
		return false
	}
	select {
	case <-stream.PeerCanceled():
		return true
	default:
		return false
	}
}

func (b *h3RequestStreamBody) Close() error {
	b.stopRead(h3RequestCodeNoError)
	return nil
}

func (b *h3RequestStreamBody) stopRead(code uint64) {
	b.stopReading.Do(func() {
		if b.readClosed.CompareAndSwap(false, true) {
			b.stream.AbortRead(code)
		}
	})
}

func (b *h3RequestStreamBody) abort(code uint64) {
	if b.responseDone.CompareAndSwap(false, true) {
		b.stopRead(code)
		b.stream.Reset(code)
	}
}

func (b *h3RequestStreamBody) finish(writer *h3ResponseWriter, failureCode uint64) {
	if b.responseDone.Load() {
		return
	}
	if err := writer.finish(); err != nil {
		b.abort(failureCode)
		return
	}
	if b.responseDone.CompareAndSwap(false, true) {
		// The peer must acknowledge FIN before a graceful connection shutdown.
		b.stopRead(h3RequestCodeNoError)
		b.stream.SetWriteContext(b.streamCtx)
		if err := b.stream.Close(); err != nil {
			b.stream.Reset(failureCode)
		}
	}
}
