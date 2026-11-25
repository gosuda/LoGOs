// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quic

import "testing"

func TestStreamPeerCanceled(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cancel func(*Stream) error
	}{
		{"RESET_STREAM", func(s *Stream) error { return s.handleReset(0x10c, 0) }},
		{"STOP_SENDING", func(s *Stream) error { return s.handleStopSending(0x10c) }},
	} {
		t.Run(tt.name+"/subscribed", func(t *testing.T) {
			s := newStopSendingTestStream()
			canceled := s.PeerCanceled()
			checkPeerCancelPending(t, canceled)
			if got := s.PeerCanceled(); got != canceled {
				t.Fatal("PeerCanceled returned different channels before cancellation")
			}
			for range 2 {
				if err := tt.cancel(s); err != nil {
					t.Fatal(err)
				}
				checkPeerCanceled(t, s, canceled)
			}
		})
		t.Run(tt.name+"/late", func(t *testing.T) {
			s := newStopSendingTestStream()
			if err := tt.cancel(s); err != nil {
				t.Fatal(err)
			}
			checkPeerCanceled(t, s, s.PeerCanceled())
		})
		t.Run(tt.name+"/concurrent", func(t *testing.T) {
			s := newStopSendingTestStream()
			start := make(chan struct{})
			subscribed := make(chan (<-chan struct{}), 1)
			processed := make(chan error, 1)
			go func() {
				<-start
				subscribed <- s.PeerCanceled()
			}()
			go func() {
				<-start
				processed <- tt.cancel(s)
			}()
			close(start)
			canceled := s.PeerCanceled()
			other, err := <-subscribed, <-processed
			if err != nil {
				t.Fatal(err)
			}
			if canceled != other {
				t.Fatal("concurrent PeerCanceled calls returned different channels")
			}
			checkPeerCanceled(t, s, canceled)
		})
	}
}

func TestStreamPeerCanceledIgnoresFINAndInvalidReset(t *testing.T) {
	s := newStopSendingTestStream()
	canceled := s.PeerCanceled()
	if err := s.handleReset(0x10c, s.inwin+1); err == nil {
		t.Fatal("RESET_STREAM exceeding the receive window was accepted")
	}
	checkPeerCancelPending(t, canceled)
	if err := s.handleData(0, []byte("done"), true); err != nil {
		t.Fatal(err)
	}
	checkPeerCancelPending(t, canceled)
	if err := s.handleReset(0x10c, 0); err == nil {
		t.Fatal("RESET_STREAM changing the final size was accepted")
	}
	checkPeerCancelPending(t, canceled)
}

func TestStreamPeerCanceledIgnoresLocalOperations(t *testing.T) {
	for _, tt := range []struct {
		name  string
		close func(*Stream)
	}{
		{"CloseRead", (*Stream).CloseRead},
		{"AbortRead", func(s *Stream) { s.AbortRead(0x100) }},
		{"Reset", func(s *Stream) { s.Reset(0x100) }},
		{"ConnectionClose", (*Stream).connHasClosed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStopSendingTestStream()
			canceled := s.PeerCanceled()
			tt.close(s)
			checkPeerCancelPending(t, canceled)
		})
	}
}

func TestStreamPeerCanceledAfterLocalReset(t *testing.T) {
	s := newStopSendingTestStream()
	canceled := s.PeerCanceled()
	s.Reset(0x100)
	if err := s.handleStopSending(0x10c); err != nil {
		t.Fatal(err)
	}
	checkPeerCanceled(t, s, canceled)
}

func checkPeerCancelPending(t *testing.T, canceled <-chan struct{}) {
	t.Helper()
	if canceled == nil {
		t.Fatal("PeerCanceled returned a nil channel")
	}
	select {
	case <-canceled:
		t.Fatal("PeerCanceled channel closed without a peer cancellation")
	default:
	}
}

func checkPeerCanceled(t *testing.T, s *Stream, canceled <-chan struct{}) {
	t.Helper()
	select {
	case _, ok := <-canceled:
		if ok {
			t.Fatal("PeerCanceled channel yielded a value instead of closing")
		}
	default:
		t.Fatal("PeerCanceled channel remained open after peer cancellation")
	}
	if got := s.PeerCanceled(); got != canceled {
		t.Fatal("PeerCanceled returned a different channel after cancellation")
	}
}
