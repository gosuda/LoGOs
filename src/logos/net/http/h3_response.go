// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

type h3ResponseStream interface {
	io.Writer
	Flush() error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type h3ResponseWriter struct {
	stream              h3ResponseStream
	method              string
	maxFieldSectionSize uint64
	header              stdhttp.Header
	pending             h3ResponseFieldSection
	connectionFields    map[string]struct{}
	declaredTrailers    map[string]struct{}
	status              int
	remainingBody       int64
	sniff               bool
	finished            bool
	err                 error
	frameHeader         [16]byte
}

var (
	_ stdhttp.ResponseWriter = (*h3ResponseWriter)(nil)
	_ stdhttp.Flusher        = (*h3ResponseWriter)(nil)
)

func newH3ResponseWriter(stream h3ResponseStream, method string, maxFieldSectionSize uint64) *h3ResponseWriter {
	return &h3ResponseWriter{
		stream: stream, method: method, maxFieldSectionSize: maxFieldSectionSize,
		header: make(stdhttp.Header), remainingBody: -1,
	}
}

func (w *h3ResponseWriter) Header() stdhttp.Header {
	return w.header
}

func (w *h3ResponseWriter) WriteHeader(status int) {
	if w.status != 0 || w.finished || w.err != nil {
		return
	}
	if status < 100 || status > 999 || status == stdhttp.StatusSwitchingProtocols {
		panic(fmt.Sprintf("http3: invalid WriteHeader code %d", status))
	}
	section := h3ResponseFieldSection{remaining: w.maxFieldSectionSize}
	w.err = w.collectInitialFields(&section, status)
	if w.err != nil {
		return
	}
	if status < 200 {
		w.err = w.sendFieldSection(section.fields)
		if w.err == nil {
			w.err = w.stream.Flush()
		}
		return
	}
	w.status = status
	w.pending = section
}

func (w *h3ResponseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.finished {
		return 0, io.ErrClosedPipe
	}
	if w.status == 0 {
		w.WriteHeader(stdhttp.StatusOK)
		if w.err != nil {
			return 0, w.err
		}
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !h3ResponseAllowsBody(w.status) {
		return 0, stdhttp.ErrBodyNotAllowed
	}
	if w.remainingBody >= 0 {
		if int64(len(p)) > w.remainingBody {
			return 0, stdhttp.ErrContentLength
		}
		w.remainingBody -= int64(len(p))
	}
	if err := w.sendPendingFields(p); err != nil {
		return 0, err
	}
	if w.method == stdhttp.MethodHead {
		return len(p), nil
	}
	return w.writeFrame(0x0, p)
}

func (w *h3ResponseWriter) FlushError() error {
	if w.err != nil || w.finished {
		return w.err
	}
	if w.status == 0 {
		w.WriteHeader(stdhttp.StatusOK)
	}
	if err := w.sendPendingFields(nil); err != nil {
		return err
	}
	w.err = w.stream.Flush()
	return w.err
}

func (w *h3ResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *h3ResponseWriter) SetReadDeadline(deadline time.Time) error {
	return w.stream.SetReadDeadline(deadline)
}

func (w *h3ResponseWriter) SetWriteDeadline(deadline time.Time) error {
	return w.stream.SetWriteDeadline(deadline)
}

func (w *h3ResponseWriter) EnableFullDuplex() error {
	return nil
}

func (w *h3ResponseWriter) finish() error {
	if !w.finished {
		w.err = w.finishResponse()
		w.pending = h3ResponseFieldSection{}
		w.finished = true
	}
	return w.err
}

func (w *h3ResponseWriter) finishResponse() error {
	if w.err != nil {
		return w.err
	}
	if w.status == 0 {
		w.WriteHeader(stdhttp.StatusOK)
		if w.err != nil {
			return w.err
		}
	}
	if w.method != stdhttp.MethodHead && h3ResponseAllowsBody(w.status) && w.remainingBody > 0 {
		return fmt.Errorf("http3: %d response bytes missing: %w", w.remainingBody, stdhttp.ErrContentLength)
	}
	if err := w.sendPendingFields(nil); err != nil {
		return err
	}
	if !w.allowsTrailers(w.status) {
		return nil
	}
	return w.writeTrailers()
}

func h3ResponseAllowsBody(status int) bool {
	return status >= 200 && status != stdhttp.StatusNoContent && status != stdhttp.StatusResetContent && status != stdhttp.StatusNotModified
}

func (w *h3ResponseWriter) isTunnel(status int) bool {
	return w.method == stdhttp.MethodConnect && status >= 200 && status < 300
}

func (w *h3ResponseWriter) allowsTrailers(status int) bool {
	return w.method != stdhttp.MethodHead && h3ResponseAllowsBody(status) && !w.isTunnel(status)
}

func (w *h3ResponseWriter) sendPendingFields(body []byte) error {
	if w.err != nil || len(w.pending.fields) == 0 {
		return w.err
	}
	section := w.pending
	w.pending = h3ResponseFieldSection{}
	if w.sniff && len(body) != 0 {
		w.err = section.add("content-type", stdhttp.DetectContentType(body))
		if w.err != nil {
			return w.err
		}
	}
	w.err = w.sendFieldSection(section.fields)
	return w.err
}

func (w *h3ResponseWriter) sendFieldSection(fields []h3Field) error {
	if len(fields) == 0 {
		return nil
	}
	payload, err := appendH3Fields(nil, fields)
	if err != nil {
		return err
	}
	_, err = w.writeFrame(0x1, payload)
	return err
}

func (w *h3ResponseWriter) writeFrame(kind uint64, p []byte) (int, error) {
	if uint64(len(p)) >= 1<<62 {
		w.err = errors.New("http3: response frame exceeds the QUIC variable integer limit")
		return 0, w.err
	}
	prefix := appendH3FrameHeader(w.frameHeader[:0], kind, uint64(len(p)))
	if _, err := w.writeWire(prefix); err != nil {
		return 0, err
	}
	return w.writeWire(p)
}

func (w *h3ResponseWriter) writeWire(p []byte) (int, error) {
	n, err := w.stream.Write(p)
	if n < 0 || n > len(p) {
		n = 0
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

type h3ResponseFieldSection struct {
	fields    []h3Field
	remaining uint64
}

var errH3ResponseFieldSectionLimit = errors.New("http3: response field section exceeds peer limit")

func (s *h3ResponseFieldSection) add(name, value string) error {
	const overhead = h3QPACKFieldOverhead
	nameSize, valueSize := uint64(len(name)), uint64(len(value))
	if s.remaining < overhead || nameSize > s.remaining-overhead || valueSize > s.remaining-overhead-nameSize {
		return errH3ResponseFieldSectionLimit
	}
	s.remaining -= overhead + nameSize + valueSize
	s.fields = append(s.fields, h3Field{name: name, value: value})
	return nil
}

func (s *h3ResponseFieldSection) addValues(name string, values []string) error {
	for _, value := range values {
		if !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("http3: invalid response field value for %q", name)
		}
		if err := s.add(name, strings.Trim(value, " \t")); err != nil {
			return err
		}
	}
	return nil
}

type h3InitialResponseFields struct {
	status           int
	tunnel           bool
	allowTrailers    bool
	connectionFields map[string]struct{}
	trailers         map[string]struct{}
	contentLength    int64
	hasDate          bool
	hasType          bool
	encoded          bool
}

func (w *h3ResponseWriter) collectInitialFields(section *h3ResponseFieldSection, status int) error {
	if err := section.add(":status", strconv.Itoa(status)); err != nil {
		return err
	}
	connectionFields, err := h3ResponseConnectionFields(w.header)
	if err != nil {
		return err
	}
	initial := h3InitialResponseFields{
		status: status, tunnel: w.isTunnel(status), allowTrailers: w.allowsTrailers(status),
		connectionFields: connectionFields, contentLength: -1,
	}
	if initial.allowTrailers && !h3OmitResponseField("trailer", connectionFields) {
		initial.trailers, err = h3DeclaredResponseTrailers(w.header, connectionFields)
		if err != nil {
			return err
		}
	}
	for key, values := range w.header {
		if err := initial.addHeaderField(section, key, values); err != nil {
			return err
		}
	}
	if initial.contentLength >= 0 {
		if err := section.add("content-length", strconv.FormatInt(initial.contentLength, 10)); err != nil {
			return err
		}
	}
	if status >= 200 && !initial.hasDate && !h3OmitResponseField("date", connectionFields) {
		if err := section.add("date", time.Now().UTC().Format(stdhttp.TimeFormat)); err != nil {
			return err
		}
	}
	if status >= 200 {
		w.remainingBody = initial.contentLength
		w.connectionFields = connectionFields
		w.declaredTrailers = initial.trailers
		w.sniff = h3ResponseAllowsBody(status) && !initial.tunnel && !initial.hasType && !initial.encoded && !h3OmitResponseField("content-type", connectionFields)
	}
	return nil
}

func (f *h3InitialResponseFields) addHeaderField(section *h3ResponseFieldSection, key string, values []string) error {
	if strings.HasPrefix(key, stdhttp.TrailerPrefix) {
		return nil
	}
	if !httpguts.ValidHeaderFieldName(key) {
		return fmt.Errorf("http3: invalid response field name %q", key)
	}
	name := strings.ToLower(key)
	_, trailer := f.trailers[name]
	if trailer || h3OmitResponseField(name, f.connectionFields) {
		return nil
	}
	switch name {
	case "content-length":
		return f.acceptContentLength(values)
	case "trailer":
		if !f.allowTrailers {
			return nil
		}
	case "date":
		f.hasDate = true
	case "content-type":
		f.hasType = true
	case "content-encoding":
		for _, value := range values {
			f.encoded = f.encoded || strings.Trim(value, " \t") != ""
		}
	}
	return section.addValues(name, values)
}

func (f *h3InitialResponseFields) acceptContentLength(values []string) error {
	if f.status < 200 || f.status == stdhttp.StatusNoContent || f.tunnel {
		return nil
	}
	length, err := h3ParseResponseLength(f.contentLength, values)
	if err != nil {
		return err
	}
	if f.status == stdhttp.StatusResetContent && length > 0 {
		return stdhttp.ErrContentLength
	}
	f.contentLength = length
	return nil
}

var errH3ResponseContentLength = fmt.Errorf("http3: invalid response Content-Length: %w", stdhttp.ErrContentLength)

func h3ParseResponseLength(length int64, values []string) (int64, error) {
	const maxDigits = 19
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			digits := strings.Trim(item, " \t")
			if len(digits) > maxDigits {
				digits = strings.TrimLeft(digits, "0")
				if digits == "" {
					digits = "0"
				}
			}
			if len(digits) > maxDigits {
				return -1, errH3ResponseContentLength
			}
			n, err := strconv.ParseUint(digits, 10, 63)
			if err != nil || (length >= 0 && uint64(length) != n) {
				return -1, errH3ResponseContentLength
			}
			length = int64(n)
		}
	}
	return length, nil
}

func h3OmitResponseField(name string, connectionFields map[string]struct{}) bool {
	switch name {
	case "connection", "keep-alive", "proxy-connection", "te", "transfer-encoding", "upgrade":
		return true
	}
	_, omit := connectionFields[name]
	return omit
}

func h3ResponseConnectionFields(header stdhttp.Header) (map[string]struct{}, error) {
	var fields map[string]struct{}
	for name, values := range header {
		if !strings.EqualFold(name, "connection") {
			continue
		}
		var err error
		fields, err = h3ResponseFieldNames(fields, values)
		if err != nil {
			return nil, err
		}
	}
	return fields, nil
}

func h3ResponseFieldNames(names map[string]struct{}, values []string) (map[string]struct{}, error) {
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			name := strings.Trim(item, " \t")
			if name == "" {
				continue
			}
			if !httpguts.ValidHeaderFieldName(name) {
				return nil, errors.New("http3: invalid response field list")
			}
			if names == nil {
				names = make(map[string]struct{})
			}
			names[strings.ToLower(name)] = struct{}{}
		}
	}
	return names, nil
}

func h3DeclaredResponseTrailers(header stdhttp.Header, connectionFields map[string]struct{}) (map[string]struct{}, error) {
	var names map[string]struct{}
	for name, values := range header {
		if !strings.EqualFold(name, "trailer") {
			continue
		}
		var err error
		names, err = h3ResponseFieldNames(names, values)
		if err != nil {
			return nil, err
		}
	}
	for name := range names {
		if !httpguts.ValidTrailerHeader(name) || h3OmitResponseField(name, connectionFields) {
			return nil, fmt.Errorf("http3: invalid response trailer name %q", name)
		}
	}
	return names, nil
}

func (w *h3ResponseWriter) writeTrailers() error {
	section := h3ResponseFieldSection{remaining: w.maxFieldSectionSize}
	for key, values := range w.header {
		rawName, prefixed := strings.CutPrefix(key, stdhttp.TrailerPrefix)
		if !prefixed && len(w.declaredTrailers) == 0 {
			continue
		}
		name := strings.ToLower(rawName)
		if _, declared := w.declaredTrailers[name]; !declared && !prefixed {
			continue
		}
		if !httpguts.ValidHeaderFieldName(rawName) || !httpguts.ValidTrailerHeader(name) || h3OmitResponseField(name, w.connectionFields) {
			return fmt.Errorf("http3: invalid response trailer name %q", rawName)
		}
		if err := section.addValues(name, values); err != nil {
			return err
		}
	}
	return w.sendFieldSection(section.fields)
}
