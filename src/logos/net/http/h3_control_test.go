// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"golang.org/x/net/quic"
)

func TestH3ControlPeerFrameErrors(t *testing.T) {
	const initial = "\x04\x00"
	const priority = "\x80\x0f\x07\x00"
	const pushPriority = "\x80\x0f\x07\x01"
	for _, tc := range []struct {
		name string
		wire string
		code h3ControlError
	}{
		{"FIN before first frame", "", 0x104},
		{"unknown before SETTINGS", "\x21\x00", 0x10a},
		{"second SETTINGS", initial + "\x04\x00", 0x105},
		{"DATA on control", initial + "\x00\x00", 0x105},
		{"HEADERS on control", initial + "\x01\x00", 0x105},
		{"reserved control frame", initial + "\x02\x00", 0x105},
		{"PUSH_PROMISE on control", initial + "\x05\x00", 0x105},
		{"truncated frame kind", initial + "\x40", 0x106},
		{"absent frame length", initial + "\x21", 0x106},
		{"truncated frame length", initial + "\x21\x40", 0x106},
		{"truncated unknown frame", initial + "\x21\x02\x00", 0x106},
		{"truncated SETTINGS", "\x04\x02\x08", 0x106},
		{"absent setting value", "\x04\x01\x08", 0x106},
		{"oversized SETTINGS", "\x04\x60\x01", 0x106},
		{"forbidden setting ID", "\x04\x02\x02\x00", 0x109},
		{"invalid CONNECT setting", "\x04\x02\x08\x02", 0x109},
		{"invalid DATAGRAM setting", "\x04\x02\x33\x02", 0x109},
		{"duplicate setting ID", "\x04\x04\x08\x01\x08\x01", 0x109},
		{"trailing GOAWAY byte", initial + "\x07\x02\x00\x00", 0x106},
		{"empty GOAWAY", initial + "\x07\x00", 0x106},
		{"GOAWAY must not consume next frame", initial + "\x07\x01\x40\x21\x00", 0x106},
		{"increasing GOAWAY", initial + "\x07\x01\x00\x07\x01\x01", 0x108},
		{"decreasing MAX_PUSH_ID", initial + "\x0d\x01\x01\x0d\x01\x00", 0x108},
		{"invalid request priority ID", initial + priority + "\x01\x01", 0x108},
		{"invalid push priority ID", initial + pushPriority + "\x01\x00", 0x108},
		{"oversized priority", initial + priority + "\x50\x01", 0x106},
		{"truncated priority field", initial + priority + "\x02\x00", 0x106},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := h3Control{settingsDone: make(chan struct{})}
			if err := c.readPeerControl(strings.NewReader(tc.wire)); !errors.Is(err, tc.code) {
				t.Fatalf("readPeerControl(% x) = %v, want %#x", tc.wire, err, uint64(tc.code))
			}
		})
	}
}

func TestH3ControlPeerSettingsSurviveClose(t *testing.T) {
	const wire = "\x04\x05\x06\x42\x00\x07\x02" + // SETTINGS: field limit 512, two blocked streams
		"\x21\x03\x07\x00\x00" + // unknown frame with a frame-like payload
		"\x07\x01\x03\x07\x02\x40\x03\x07\x01\x01" + // client GOAWAY carries a push ID
		"\x0d\x01\x00\x0d\x01\x03" + // increasing MAX_PUSH_ID
		"\x80\x0f\x07\x00\x04\x04u=1" // request priority
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c := h3Control{ctx: ctx, settingsDone: make(chan struct{})}
	if err := c.readPeerControl(strings.NewReader(wire)); !errors.Is(err, h3ControlError(0x104)) {
		t.Fatalf("readPeerControl valid frames followed by FIN = %v, want 0x104", err)
	}
	want := h3Settings{maxFieldSectionSize: 512, qpackBlockedStreams: 2}
	got, err := c.awaitPeerSettings(ctx)
	if err != nil || got != want || ctx.Err() != nil {
		t.Fatalf("awaitPeerSettings before cancellation = %+v, %v (context %v); want %+v, nil", got, err, ctx.Err(), want)
	}
	cancel()
	got, err = c.awaitPeerSettings(ctx)
	if err != nil || got != want {
		t.Fatalf("awaitPeerSettings after cancellation = %+v, %v; want %+v, nil", got, err, want)
	}
}

func TestH3ControlPeerResetAndConnectionClose(t *testing.T) {
	const incompletePayload = "\x04\x00\x21\x02\x00"
	connErr := &quic.ApplicationError{Code: 0x100}
	for _, tc := range []struct {
		name string
		end  error
		want error
	}{
		{"critical stream reset", quic.StreamErrorCode(0x100), h3ControlError(0x104)},
		{"connection close", connErr, connErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(io.MultiReader(strings.NewReader(incompletePayload), iotest.ErrReader(tc.end)))
			c := h3Control{settingsDone: make(chan struct{})}
			if err := c.readPeerControl(r); !errors.Is(err, tc.want) {
				t.Fatalf("readPeerControl = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestH3ControlAwaitPeerSettingsCancellation(t *testing.T) {
	for _, name := range []string{"caller", "connection"} {
		t.Run(name, func(t *testing.T) {
			conn, cancelConn := context.WithCancelCause(t.Context())
			defer cancelConn(nil)
			caller, cancelCaller := context.WithCancelCause(t.Context())
			defer cancelCaller(nil)
			c := h3Control{ctx: conn, settingsDone: make(chan struct{})}
			want := errors.New(name + " stopped before SETTINGS")
			switch name {
			case "caller":
				cancelCaller(want)
			case "connection":
				cancelConn(want)
			}
			if _, err := c.awaitPeerSettings(caller); !errors.Is(err, want) {
				t.Fatalf("awaitPeerSettings = %v, want %v", err, want)
			}
		})
	}
}

func TestH3ControlWaitGoAwayAckCancellation(t *testing.T) {
	for _, name := range []string{"caller", "connection"} {
		t.Run(name, func(t *testing.T) {
			conn, cancelConn := context.WithCancelCause(t.Context())
			defer cancelConn(nil)
			caller, cancelCaller := context.WithCancelCause(t.Context())
			defer cancelCaller(nil)
			c := h3Control{ctx: conn, goAway: make(chan uint64, 1)}
			want := errors.New(name + " stopped while a GOAWAY write owns the token")
			if name == "caller" {
				cancelCaller(want)
			} else {
				cancelConn(want)
			}
			if err := c.waitGoAwayAck(caller); !errors.Is(err, want) {
				t.Fatalf("waitGoAwayAck = %v, want %v", err, want)
			}
		})
	}
}
