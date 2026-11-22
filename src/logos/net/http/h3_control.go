// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/net/quic"
)

const (
	h3ControlUniControl      = 0x00
	h3ControlUniPush         = 0x01
	h3ControlUniQPACKEncoder = 0x02
	h3ControlUniQPACKDecoder = 0x03

	h3ControlFrameCancelPush    = 0x03
	h3ControlFrameSettings      = 0x04
	h3ControlFrameGoAway        = 0x07
	h3ControlFrameMaxPushID     = 0x0d
	h3ControlFramePriority      = 0xf0700
	h3ControlFramePriorityPush  = 0xf0701
	h3ControlMaxPriorityPayload = 4096
	h3ControlMaxVarint          = 1<<62 - 1
)

type h3ControlError uint64

const (
	h3ControlStreamCreationError  h3ControlError = 0x103
	h3ControlClosedCriticalStream h3ControlError = 0x104
	h3ControlFrameUnexpected      h3ControlError = 0x105
	h3ControlFrameError           h3ControlError = 0x106
	h3ControlIDError              h3ControlError = 0x108
	h3ControlSettingsError        h3ControlError = 0x109
	h3ControlMissingSettings      h3ControlError = 0x10a
)

func (e h3ControlError) Error() string {
	return fmt.Sprintf("http3: control stream error %#x", uint64(e))
}

type h3Control struct {
	ctx   context.Context
	conn  *quic.Conn
	local *quic.Stream

	goAway    chan uint64
	watchDone chan struct{}

	mu            sync.Mutex
	peerStreams   uint8
	settingsDone  chan struct{}
	settingsReady bool
	settings      h3Settings
	aborted       *quic.ApplicationError
}

func startH3Control(ctx context.Context, conn *quic.Conn, maxFieldSectionSize uint64) (*h3Control, error) {
	if maxFieldSectionSize > h3ControlMaxVarint && maxFieldSectionSize != h3UnlimitedFieldSectionSize {
		return nil, errors.New("http3: field section limit does not fit in SETTINGS")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	setupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := conn.NewSendOnlyStream(setupCtx)
	if err != nil {
		return nil, err
	}
	control := &h3Control{
		ctx:          ctx,
		conn:         conn,
		local:        stream,
		goAway:       make(chan uint64, 1),
		settingsDone: make(chan struct{}),
	}
	control.goAway <- h3ControlMaxVarint

	var wire [14]byte
	initial := appendH3Varint(wire[:0], h3ControlUniControl)
	initial = appendH3FrameHeader(initial, h3ControlFrameSettings, 0)
	headerLen := len(initial)
	initial = appendH3Varint(initial, h3SettingEnableConnectProtocol)
	initial = appendH3Varint(initial, 1)
	if maxFieldSectionSize != h3UnlimitedFieldSectionSize {
		initial = appendH3Varint(initial, h3SettingMaxFieldSectionSize)
		initial = appendH3Varint(initial, maxFieldSectionSize)
	}
	initial[headerLen-1] = byte(len(initial) - headerLen)
	if err := control.write(setupCtx, initial); err != nil {
		if control.aborted == nil {
			stream.Reset(uint64(h3ControlClosedCriticalStream))
		}
		return nil, err
	}
	control.watchDone = make(chan struct{})
	go control.watchLocal()
	return control, nil
}

func (c *h3Control) watchLocal() {
	defer close(c.watchDone)
	select {
	case <-c.ctx.Done():
	case <-c.local.PeerCanceled():
		if c.ctx.Err() == nil {
			c.abort(h3ControlClosedCriticalStream)
		}
	}
}

func (c *h3Control) wait() {
	<-c.watchDone
}

func (c *h3Control) awaitPeerSettings(ctx context.Context) (h3Settings, error) {
	select {
	case <-c.settingsDone:
	case <-ctx.Done():
	case <-c.ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settingsReady {
		return c.settings, nil
	}
	if c.aborted != nil {
		return h3Settings{}, c.aborted
	}
	if err := context.Cause(c.ctx); err != nil {
		return h3Settings{}, err
	}
	return h3Settings{}, context.Cause(ctx)
}

func (c *h3Control) sendGoAway(ctx context.Context, id uint64) error {
	if id > h3ControlMaxVarint || id%4 != 0 {
		return errors.New("http3: GOAWAY requires a client-initiated bidirectional stream ID")
	}
	last, err := c.takeGoAway(ctx)
	if err != nil {
		return err
	}
	defer func() { c.goAway <- last }()
	if id > last {
		return errors.New("http3: GOAWAY stream ID increased")
	}
	var wire [10]byte
	frame := appendH3FrameHeader(wire[:0], h3ControlFrameGoAway, 0)
	headerLen := len(frame)
	frame = appendH3Varint(frame, id)
	frame[headerLen-1] = byte(len(frame) - headerLen)
	ctx, release := c.operationContext(ctx)
	if release != nil {
		defer release()
	}
	if err := c.write(ctx, frame); err != nil {
		return err
	}
	last = id
	return nil
}

func (c *h3Control) waitGoAwayAck(ctx context.Context) error {
	last, err := c.takeGoAway(ctx)
	if err != nil {
		return err
	}
	defer func() { c.goAway <- last }()
	ctx, release := c.operationContext(ctx)
	if release != nil {
		defer release()
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	err = c.local.WaitFlushed(ctx)
	if cause := ctx.Err(); cause != nil && errors.Is(err, cause) {
		return context.Cause(ctx)
	}
	return err
}

func (c *h3Control) takeGoAway(ctx context.Context) (uint64, error) {
	select {
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	case <-c.ctx.Done():
		return 0, context.Cause(c.ctx)
	case last := <-c.goAway:
		return last, nil
	}
}

func (c *h3Control) write(ctx context.Context, payload []byte) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	c.local.SetWriteContext(ctx)
	n, err := c.local.Write(payload)
	c.local.SetWriteContext(c.ctx)
	if err != nil {
		if n == 0 && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return context.Cause(ctx)
		}
	} else if n != len(payload) {
		err = io.ErrShortWrite
	} else {
		err = c.local.Flush()
	}
	if err != nil {
		c.abort(h3ControlClosedCriticalStream)
	}
	return err
}

func (c *h3Control) operationContext(ctx context.Context) (context.Context, func()) {
	connDone, callerDone := c.ctx.Done(), ctx.Done()
	if connDone == nil || callerDone == connDone {
		return ctx, nil
	}
	if callerDone == nil || c.ctx.Err() != nil {
		return c.ctx, nil
	}
	merged, cancel := context.WithCancelCause(ctx)
	unlink := context.AfterFunc(c.ctx, func() { cancel(context.Cause(c.ctx)) })
	return merged, func() {
		unlink()
		cancel(nil)
	}
}

func (c *h3Control) handlePeerUni(ctx context.Context, stream *quic.Stream) error {
	defer stream.AbortRead(uint64(h3ControlClosedCriticalStream))
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil
	}
	readCtx, release := c.operationContext(ctx)
	if release != nil {
		defer release()
	}
	stream.SetReadContext(readCtx)
	r := h3PeerUniReader{ctx: readCtx, stream: stream}
	kind, err := readH3Varint(r)
	if err != nil {
		if _, reset := errors.AsType[quic.StreamErrorCode](err); reset || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		if ctx.Err() != nil || c.ctx.Err() != nil {
			return nil
		}
		return err
	}
	switch kind {
	case h3ControlUniControl, h3ControlUniQPACKEncoder, h3ControlUniQPACKDecoder:
		if !c.claimPeerUni(kind) {
			err = h3ControlStreamCreationError
		} else if kind == h3ControlUniControl {
			err = c.readPeerControl(r)
		} else {
			// With a zero QPACK table, there are no instructions to consume.
			_, err = io.Copy(io.Discard, r)
			if err == nil {
				err = h3ControlClosedCriticalStream
			} else {
				err = h3ControlPayloadError(err)
			}
		}
	case h3ControlUniPush:
		err = h3ControlStreamCreationError
	default:
		stream.AbortRead(uint64(h3ControlStreamCreationError))
		return nil
	}
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil
	}
	if code, ok := errors.AsType[h3ControlError](err); ok {
		c.abort(code)
		return nil
	}
	return err
}

type h3PeerUniReader struct {
	ctx    context.Context
	stream *quic.Stream
}

func (r h3PeerUniReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.stream.Read(p)
}

func (r h3PeerUniReader) ReadByte() (byte, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.stream.ReadByte()
}

func (c *h3Control) claimPeerUni(kind uint64) bool {
	bit := uint8(1 << kind)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peerStreams&bit != 0 {
		return false
	}
	c.peerStreams |= bit
	return true
}

func (c *h3Control) abort(code h3ControlError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.aborted != nil {
		return
	}
	c.aborted = &quic.ApplicationError{Code: uint64(code)}
	c.conn.Abort(c.aborted)
	if !c.settingsReady {
		close(c.settingsDone)
	}
}

type h3ControlReader interface {
	io.Reader
	io.ByteReader
}

func (c *h3Control) readPeerControl(r h3ControlReader) error {
	header, err := readH3ControlHeader(r)
	if err != nil {
		return err
	}
	if header.kind != h3ControlFrameSettings {
		return h3ControlMissingSettings
	}
	settings, err := readH3PeerSettings(r, header.length)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.aborted == nil {
		c.settings = settings
		c.settingsReady = true
		close(c.settingsDone)
	}
	c.mu.Unlock()

	ids := h3ControlPeerIDs{goAway: h3ControlMaxVarint}
	for {
		header, err := readH3ControlHeader(r)
		if err != nil {
			return err
		}
		if err := ids.readFrame(r, header); err != nil {
			return err
		}
	}
}

func readH3ControlHeader(r io.ByteReader) (h3FrameHeader, error) {
	header, err := readH3FrameHeader(r)
	if errors.Is(err, io.EOF) {
		return h3FrameHeader{}, h3ControlClosedCriticalStream
	}
	return header, h3ControlPayloadError(err)
}

func h3ControlPayloadError(err error) error {
	if err == nil {
		return nil
	}
	if _, reset := errors.AsType[quic.StreamErrorCode](err); reset {
		return h3ControlClosedCriticalStream
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return h3ControlFrameError
	}
	return err
}

func readH3PeerSettings(r io.Reader, length uint64) (h3Settings, error) {
	if length > h3MaxSettingsPayload {
		return h3Settings{}, h3ControlFrameError
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return h3Settings{}, h3ControlPayloadError(err)
	}
	settings, err := decodeH3Settings(payload)
	if err == nil {
		return settings, nil
	}
	for len(payload) > 0 {
		_, _, rest, err := parseH3Setting(payload)
		if err != nil {
			return h3Settings{}, h3ControlFrameError
		}
		payload = rest
	}
	return h3Settings{}, h3ControlSettingsError
}

type h3ControlPeerIDs struct {
	goAway    uint64
	maxPushID uint64
}

func (ids *h3ControlPeerIDs) readFrame(r h3ControlReader, header h3FrameHeader) error {
	switch header.kind {
	case 0x00, 0x01, 0x02, h3ControlFrameSettings, 0x05, 0x06, 0x08, 0x09:
		return h3ControlFrameUnexpected
	case h3ControlFrameGoAway, h3ControlFrameMaxPushID, h3ControlFrameCancelPush:
		return ids.readIDFrame(r, header)
	case h3ControlFramePriority, h3ControlFramePriorityPush:
		return readH3ControlPriority(r, header)
	default:
		_, err := io.CopyN(io.Discard, r, int64(header.length))
		return h3ControlPayloadError(err)
	}
}

func (ids *h3ControlPeerIDs) readIDFrame(r io.ByteReader, header h3FrameHeader) error {
	if header.length == 0 || header.length > 8 {
		return h3ControlFrameError
	}
	payload := h3ControlVarintReader{r: r, remaining: header.length}
	id, err := readH3Varint(&payload)
	if err != nil {
		return h3ControlPayloadError(err)
	}
	if payload.remaining != 0 {
		return h3ControlFrameError
	}
	switch header.kind {
	case h3ControlFrameGoAway:
		if id > ids.goAway {
			return h3ControlIDError
		}
		ids.goAway = id
	case h3ControlFrameMaxPushID:
		if id < ids.maxPushID {
			return h3ControlIDError
		}
		ids.maxPushID = id
	}
	return nil
}

func readH3ControlPriority(r h3ControlReader, header h3FrameHeader) error {
	if header.length == 0 || header.length > h3ControlMaxPriorityPayload {
		return h3ControlFrameError
	}
	payload := h3ControlVarintReader{r: r, remaining: header.length}
	id, err := readH3Varint(&payload)
	if err != nil {
		return h3ControlPayloadError(err)
	}
	if _, err := io.CopyN(io.Discard, r, int64(payload.remaining)); err != nil {
		return h3ControlPayloadError(err)
	}
	if header.kind == h3ControlFramePriorityPush || id%4 != 0 {
		return h3ControlIDError
	}
	return nil
}

type h3ControlVarintReader struct {
	r         io.ByteReader
	remaining uint64
}

func (r *h3ControlVarintReader) ReadByte() (byte, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	b, err := r.r.ReadByte()
	if err == nil {
		r.remaining--
	}
	return b, err
}
