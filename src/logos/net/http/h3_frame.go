// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"errors"
	"fmt"
	"io"
)

type h3FrameHeader struct {
	kind   uint64
	length uint64
}

func readH3FrameHeader(r io.ByteReader) (h3FrameHeader, error) {
	kind, err := readH3Varint(r)
	if err != nil {
		return h3FrameHeader{}, fmt.Errorf("http3: read frame kind: %w", err)
	}
	length, err := readH3Varint(r)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return h3FrameHeader{}, fmt.Errorf("http3: read frame length: %w", err)
	}
	return h3FrameHeader{kind: kind, length: length}, nil
}

func appendH3FrameHeader(dst []byte, kind, length uint64) []byte {
	dst = appendH3Varint(dst, kind)
	return appendH3Varint(dst, length)
}

const (
	h3MaxSettingsPayload        = 8192
	h3UnlimitedFieldSectionSize = ^uint64(0)

	h3SettingQpackMaxTableCapacity = 0x01
	h3SettingMaxFieldSectionSize   = 0x06
	h3SettingQpackBlockedStreams   = 0x07
	h3SettingEnableConnectProtocol = 0x08
	h3SettingEnableDatagram        = 0x33
)

var (
	errH3SettingsMalformed = errors.New("http3: malformed SETTINGS frame")
	errH3SettingsTooLarge  = fmt.Errorf("http3: SETTINGS payload exceeds %d bytes", h3MaxSettingsPayload)
)

type h3Settings struct {
	maxFieldSectionSize   uint64
	qpackMaxTableCapacity uint64
	qpackBlockedStreams   uint64
	enableConnectProtocol bool
	enableDatagram        bool
}

func decodeH3Settings(payload []byte) (h3Settings, error) {
	if len(payload) > h3MaxSettingsPayload {
		return h3Settings{}, errH3SettingsTooLarge
	}
	settings := h3Settings{maxFieldSectionSize: h3UnlimitedFieldSectionSize}
	seen := make(map[uint64]struct{}, 8)
	for len(payload) != 0 {
		id, value, rest, err := parseH3Setting(payload)
		if err != nil {
			return h3Settings{}, err
		}
		payload = rest
		if _, exists := seen[id]; exists {
			return h3Settings{}, fmt.Errorf("http3: duplicate SETTINGS id %#x: %w", id, errH3SettingsMalformed)
		}
		seen[id] = struct{}{}
		if id >= 0x02 && id <= 0x05 {
			return h3Settings{}, fmt.Errorf("http3: forbidden HTTP/2 SETTINGS id %#x: %w", id, errH3SettingsMalformed)
		}
		if err := applyH3Setting(&settings, id, value); err != nil {
			return h3Settings{}, err
		}
	}
	return settings, nil
}

func parseH3Setting(payload []byte) (uint64, uint64, []byte, error) {
	id, idLen, err := parseH3Varint(payload)
	if err != nil {
		return 0, 0, nil, errH3SettingsMalformed
	}
	value, valueLen, err := parseH3Varint(payload[idLen:])
	if err != nil {
		return 0, 0, nil, errH3SettingsMalformed
	}
	return id, value, payload[idLen+valueLen:], nil
}

func applyH3Setting(settings *h3Settings, id, value uint64) error {
	switch id {
	case h3SettingMaxFieldSectionSize:
		settings.maxFieldSectionSize = value
	case h3SettingQpackMaxTableCapacity:
		settings.qpackMaxTableCapacity = value
	case h3SettingQpackBlockedStreams:
		settings.qpackBlockedStreams = value
	case h3SettingEnableConnectProtocol:
		if value > 1 {
			return fmt.Errorf("http3: invalid SETTINGS_ENABLE_CONNECT_PROTOCOL value %d: %w", value, errH3SettingsMalformed)
		}
		settings.enableConnectProtocol = value == 1
	case h3SettingEnableDatagram:
		if value > 1 {
			return fmt.Errorf("http3: invalid SETTINGS_H3_DATAGRAM value %d: %w", value, errH3SettingsMalformed)
		}
		settings.enableDatagram = value == 1
	}
	return nil
}
