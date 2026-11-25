// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import "testing"

func TestStreamStopSendingCode(t *testing.T) {
	const (
		h3NoError          = 0x100
		h3RequestCancelled = 0x10c
		otherCode          = 0x102
	)
	for _, tt := range []struct {
		name  string
		close func(*Stream)
		want  uint64
	}{
		{"CloseRead", (*Stream).CloseRead, 0},
		{"NoError", func(s *Stream) { s.AbortRead(h3NoError) }, h3NoError},
		{"RequestCancelled", func(s *Stream) { s.AbortRead(h3RequestCancelled) }, h3RequestCancelled},
		{"Saturation", func(s *Stream) { s.AbortRead(^uint64(0)) }, 1<<62 - 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopSendingTestStream()
			tt.close(s)
			s.AbortRead(otherCode)
			s.CloseRead()
			checkStopSendingPayload(t, stopSendingTestPayload(t, s, 1, false), uint64(s.ID()), tt.want)

			s.CloseRead()
			s.AbortRead(otherCode)
			if got := stopSendingTestPayload(t, s, 2, false); len(got) != 0 {
				t.Fatalf("ordinary packet before loss has payload %x, want empty", got)
			}
			checkStopSendingPayload(t, stopSendingTestPayload(t, s, 2, true), uint64(s.ID()), tt.want)

			s.ackOrLoss(2, frameTypeStopSending, packetLost)
			checkStopSendingPayload(t, stopSendingTestPayload(t, s, 3, false), uint64(s.ID()), tt.want)

			s.ackOrLoss(3, frameTypeStopSending, packetAcked)
			s.AbortRead(otherCode)
			if got := stopSendingTestPayload(t, s, 4, true); len(got) != 0 {
				t.Fatalf("probe after acknowledgement has payload %x, want empty", got)
			}
		})
	}
}

func TestStreamAbortReadAfterPeerFinishes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		finish func(*Stream) error
	}{
		{"FIN", func(s *Stream) error { return s.handleData(0, []byte("done"), true) }},
		{"RESET_STREAM", func(s *Stream) error { return s.handleReset(0x10c, 0) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopSendingTestStream()
			if err := tt.finish(s); err != nil {
				t.Fatal(err)
			}
			s.AbortRead(0x100)
			if got := stopSendingTestPayload(t, s, 1, true); len(got) != 0 {
				t.Fatalf("packet after peer finished has payload %x, want empty", got)
			}
		})
	}
}

func newStopSendingTestStream() *Stream {
	c := &Conn{side: serverSide, config: &Config{}}
	c.streamsInit()
	s := newStream(c, newStreamID(clientSide, bidiStream, 16))
	s.inmaxbuf = c.config.maxStreamReadBufferSize()
	s.inwin = s.inmaxbuf
	s.inUnlock()
	s.outUnlock()
	return s
}

func stopSendingTestPayload(t *testing.T, s *Stream, pnum packetNumber, pto bool) []byte {
	t.Helper()
	var w packetWriter
	w.reset(smallestMaxDatagramSize)
	w.start1RTTPacket(pnum, 0, nil)
	s.ingate.lock()
	ok := s.appendInFramesLocked(&w, pnum, pto)
	s.inUnlock()
	if !ok {
		t.Fatalf("inbound frames did not fit in packet %d", pnum)
	}
	return w.payload()
}

func checkStopSendingPayload(t *testing.T, payload []byte, wantID, wantCode uint64) {
	t.Helper()
	frameType, rest := decodeStopSendingTestVarint(t, payload)
	id, rest := decodeStopSendingTestVarint(t, rest)
	code, rest := decodeStopSendingTestVarint(t, rest)
	if frameType != 0x05 || id != wantID || code != wantCode || len(rest) != 0 {
		t.Fatalf("payload %x: type %#x, stream ID %d, code %#x, trailing bytes %x; want type 0x05, stream ID %d, code %#x, no trailing bytes", payload, frameType, id, code, rest, wantID, wantCode)
	}
}

func decodeStopSendingTestVarint(t *testing.T, b []byte) (uint64, []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Fatal("missing QUIC varint")
	}
	size := 1 << (b[0] >> 6)
	if len(b) < size {
		t.Fatalf("truncated QUIC varint: %x", b)
	}
	v := uint64(b[0] & 0x3f)
	for _, octet := range b[1:size] {
		v = v<<8 | uint64(octet)
	}
	return v, b[size:]
}
