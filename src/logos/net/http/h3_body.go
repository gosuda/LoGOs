// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	stdhttp "net/http"
)

var (
	errH3FrameUnexpected = errors.New("http3: unexpected frame on request stream")
	errH3FrameTruncated  = errors.New("http3: truncated request frame")
)

type h3RequestBodyLimits struct {
	contentLength       int64
	maxTrailerBytes     uint64
	maxFieldSectionSize uint64
}

type h3RequestBodyReader struct {
	reader                 *bufio.Reader
	trailers               stdhttp.Header
	pendingTrailers        stdhttp.Header
	maxTrailerBytes        uint64
	maxFieldSectionSize    uint64
	remainingContentLength int64
	remainingData          uint64
	seenTrailers           bool
	terminalErr            error
}

func newH3RequestBodyReader(reader *bufio.Reader, trailers stdhttp.Header, limits h3RequestBodyLimits) *h3RequestBodyReader {
	return &h3RequestBodyReader{
		reader:                 reader,
		trailers:               trailers,
		maxTrailerBytes:        limits.maxTrailerBytes,
		maxFieldSectionSize:    limits.maxFieldSectionSize,
		remainingContentLength: limits.contentLength,
	}
}

func (r *h3RequestBodyReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.terminalErr != nil {
		return 0, r.terminalErr
	}
	for r.remainingData == 0 {
		if err := r.readFrame(); err != nil {
			r.terminalErr = err
			r.pendingTrailers = nil
			return 0, err
		}
	}
	return r.readData(p)
}

func (r *h3RequestBodyReader) readFrame() error {
	if _, err := r.reader.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return r.finish()
		}
		return err
	}
	frame, err := readH3FrameHeader(r.reader)
	if err != nil {
		return h3RequestFrameReadError(err)
	}
	switch frame.kind {
	case 0x0:
		if r.seenTrailers {
			return fmt.Errorf("%w: DATA after trailers", errH3FrameUnexpected)
		}
		if r.remainingContentLength == 0 && frame.length != 0 {
			return h3MessageError("DATA exceeds Content-Length")
		}
		r.remainingData = frame.length
		return nil
	case 0x1:
		return r.readTrailers(frame.length)
	case 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xd, 0xf0700, 0xf0701:
		return fmt.Errorf("%w: type %#x", errH3FrameUnexpected, frame.kind)
	default:
		return r.discardFrame(frame.length)
	}
}

func (r *h3RequestBodyReader) readData(p []byte) (int, error) {
	limit := min(uint64(len(p)), r.remainingData)
	if r.remainingContentLength >= 0 {
		limit = min(limit, uint64(r.remainingContentLength))
	}
	n, err := r.reader.Read(p[:limit])
	r.remainingData -= uint64(n)
	if r.remainingContentLength >= 0 {
		r.remainingContentLength -= int64(n)
	}
	switch {
	case errors.Is(err, io.EOF) && r.remainingData == 0:
		r.terminalErr = r.finish()
	case err != nil:
		r.terminalErr = h3RequestFrameReadError(err)
	case r.remainingContentLength == 0 && r.remainingData != 0:
		r.terminalErr = h3MessageError("DATA exceeds Content-Length")
	}
	return n, r.terminalErr
}

func (r *h3RequestBodyReader) discardFrame(length uint64) error {
	for length != 0 {
		size := min(length, uint64(math.MaxInt))
		if _, err := r.reader.Discard(int(size)); err != nil {
			return h3RequestFrameReadError(err)
		}
		length -= size
	}
	return nil
}

func (r *h3RequestBodyReader) readTrailers(length uint64) error {
	if r.seenTrailers {
		return fmt.Errorf("%w: second trailer section", errH3FrameUnexpected)
	}
	if length > r.maxTrailerBytes || length > uint64(math.MaxInt) {
		return fmt.Errorf("%w: compressed trailer block", errH3FieldSectionTooLarge)
	}
	block, err := r.readTrailerBlock(int(length))
	if err != nil {
		return err
	}
	decoded, err := decodeH3Trailers(block, r.maxFieldSectionSize)
	if err != nil {
		return err
	}
	r.pendingTrailers = decoded
	r.seenTrailers = true
	return nil
}

func (r *h3RequestBodyReader) readTrailerBlock(length int) ([]byte, error) {
	window := min(r.reader.Size(), 4096)
	var block []byte
	for len(block) < length {
		size := min(window, length-len(block))
		chunk, err := r.reader.Peek(size)
		if err != nil {
			return nil, h3RequestFrameReadError(err)
		}
		block = append(block, chunk...)
		if _, err := r.reader.Discard(size); err != nil {
			return nil, h3RequestFrameReadError(err)
		}
	}
	return block, nil
}

func (r *h3RequestBodyReader) finish() error {
	if r.remainingContentLength > 0 {
		return fmt.Errorf("%w: Content-Length not met: %w", errH3MalformedMessage, io.ErrUnexpectedEOF)
	}
	if r.trailers != nil {
		maps.Copy(r.trailers, r.pendingTrailers)
	}
	return io.EOF
}

func h3RequestFrameReadError(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: %w", errH3FrameTruncated, io.ErrUnexpectedEOF)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: %w", errH3FrameTruncated, err)
	default:
		return err
	}
}
