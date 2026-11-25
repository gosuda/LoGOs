// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestStreamWaitFlushedRequiresContiguousACK(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := newFlushACKTestStream()
		writeFlushACKTestBytes(t, s, "abcdefgh")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		result := startFlushACKWaiter(ctx, s)
		checkFlushACKPending(t, result)

		s.ackOrLossData(2, 4, 8, false, packetAcked)
		checkFlushACKPending(t, result)
		s.ackOrLossData(1, 0, 4, false, packetLost)
		checkFlushACKPending(t, result)
		s.ackOrLossData(3, 0, 2, false, packetAcked)
		checkFlushACKPending(t, result)
		s.ackOrLossData(4, 2, 4, false, packetAcked)
		checkFlushACKResult(t, result, nil)

		checkFlushACKResult(t, startFlushACKWaiter(ctx, s), nil)
		writeFlushACKTestBytes(t, s, "still open")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStreamWaitFlushedSnapshotsOffset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := newFlushACKTestStream()
		writeFlushACKTestBytes(t, s, "abcd")
		checkFlushACKResult(t, startFlushACKWaiter(ctx, s), nil)
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		writeFlushACKTestBytes(t, s, "ef")
		first := startFlushACKWaiter(ctx, s)
		checkFlushACKPending(t, first)

		canWrite := s.outgate.lock()
		flushed, closed := s.outflushed, s.outclosed.isSet()
		s.outgate.unlock(canWrite)
		if flushed != 4 || closed {
			t.Fatalf("WaitFlushed changed send state: flushed offset = %d, write side closed = %t", flushed, closed)
		}
		writeFlushACKTestBytes(t, s, "gh")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		second := startFlushACKWaiter(ctx, s)
		third := startFlushACKWaiter(ctx, s)
		checkFlushACKPending(t, second)
		checkFlushACKPending(t, third)

		s.ackOrLossData(2, 7, 8, false, packetAcked)
		s.ackOrLossData(1, 0, 5, false, packetAcked)
		checkFlushACKResult(t, first, nil)
		checkFlushACKPending(t, second)
		checkFlushACKPending(t, third)
		s.ackOrLossData(3, 5, 7, false, packetAcked)
		checkFlushACKResult(t, second, nil)
		checkFlushACKResult(t, third, nil)
	})
}

func TestStreamWaitFlushedCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		short, cancelShort := context.WithCancel(ctx)
		defer cancelShort()
		s := newFlushACKTestStream()
		writeFlushACKTestBytes(t, s, "data")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		first := startFlushACKWaiter(short, s)
		second := startFlushACKWaiter(ctx, s)
		checkFlushACKPending(t, first)
		checkFlushACKPending(t, second)

		cancelShort()
		checkFlushACKResult(t, first, context.Canceled)
		checkFlushACKPending(t, second)
		s.ackOrLossData(1, 0, 4, false, packetAcked)
		checkFlushACKResult(t, second, nil)
	})
}

func TestStreamWaitFlushedCancellationWhileGateLocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := newFlushACKTestStream()
		canWrite := s.outgate.lock()
		defer s.outgate.unlock(canWrite)
		result := startFlushACKWaiter(ctx, s)
		checkFlushACKPending(t, result)
		cancel()
		checkFlushACKResult(t, result, context.Canceled)
	})
}

func TestStreamWaitFlushedTerminatesOnResetOrConnectionClose(t *testing.T) {
	const code = StreamErrorCode(0x10c)
	peerClose := &ApplicationError{Code: 0x102, Reason: "connection failed"}
	for _, tt := range []struct {
		name      string
		terminate func(*Stream) error
		want      error
	}{
		{"stop sending", func(s *Stream) error { return s.handleStopSending(uint64(code)) }, code},
		{"peer reset", func(s *Stream) error { return s.handleReset(uint64(code), 0) }, code},
		{"local reset", func(s *Stream) error { s.Reset(uint64(code)); return nil }, code},
		{"peer connection failure", func(s *Stream) error {
			s.conn.lifetime.state = connStatePeerClosed
			s.conn.setFinalError(peerClose)
			s.connHasClosed()
			return nil
		}, peerClose},
		{"local connection close", func(s *Stream) error {
			s.conn.lifetime.state = connStateClosing
			s.connHasClosed()
			return nil
		}, errConnClosed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				s := newFlushACKTestStream()
				writeFlushACKTestBytes(t, s, "data")
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				result := startFlushACKWaiter(ctx, s)
				checkFlushACKPending(t, result)
				if err := tt.terminate(s); err != nil {
					t.Fatal(err)
				}
				checkFlushACKResult(t, result, tt.want)
				checkFlushACKResult(t, startFlushACKWaiter(ctx, s), tt.want)
			})
		})
	}
}

func newFlushACKTestStream() *Stream {
	c := &Conn{side: serverSide, config: &Config{}}
	c.streamsInit()
	c.lifetimeInit()
	s := newStream(c, newStreamID(clientSide, bidiStream, 16))
	s.inmaxbuf = c.config.maxStreamReadBufferSize()
	s.inwin = s.inmaxbuf
	s.outmaxbuf = 128
	s.outwin = s.outmaxbuf
	s.inUnlock()
	s.outUnlock()
	return s
}

func writeFlushACKTestBytes(t *testing.T, s *Stream, data string) {
	t.Helper()
	if n, err := s.Write([]byte(data)); n != len(data) || err != nil {
		t.Fatalf("Write(%q) = %d, %v; want %d, nil", data, n, err, len(data))
	}
}

func startFlushACKWaiter(ctx context.Context, s *Stream) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- s.WaitFlushed(ctx)
	}()
	return result
}

func checkFlushACKPending(t *testing.T, result <-chan error) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-result:
		t.Fatalf("WaitFlushed returned %v before the requested bytes were acknowledged", err)
	default:
	}
}

func checkFlushACKResult(t *testing.T, result <-chan error, want error) {
	t.Helper()
	synctest.Wait()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("WaitFlushed returned %v; want %v", err, want)
		}
	default:
		t.Fatalf("WaitFlushed remained blocked; want %v", want)
	}
}
