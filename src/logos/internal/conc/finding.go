// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"os"
	"strconv"
	"sync"
	"unicode/utf8"
)

// SinkEnv names the file that receives the findings: one JSON object
// per line, written with one write call, at most MaxLine bytes. The
// `logos conc` command sets it to a named pipe. When it is empty or the
// file cannot be opened, the findings go to standard error.
const (
	SinkEnv = "LOGOSCONC_SINK"
	MaxLine = 4000
)

type position struct {
	file string
	line int
	role string
}

type finding struct {
	rule      string
	message   string
	positions []position
	hint      string
}

var sink struct {
	open sync.Once
	file *os.File
	mu   sync.Mutex
}

func write(f finding) {
	sink.open.Do(func() {
		sink.file = os.Stderr
		path := os.Getenv(SinkEnv)
		if path == "" {
			return
		}
		if file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
			sink.file = file
		}
	})
	line := f.encode(len(f.positions))
	for kept := len(f.positions) - 1; len(line) > MaxLine && kept >= 0; kept-- {
		line = f.encode(kept)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if _, err := sink.file.Write(line); err != nil && sink.file != os.Stderr {
		os.Stderr.Write(line)
	}
}

func (f finding) encode(positions int) []byte {
	b := make([]byte, 0, 512)
	b = append(b, `{"rule":`...)
	b = appendJSONString(b, f.rule)
	b = append(b, `,"message":`...)
	b = appendJSONString(b, f.message)
	b = append(b, `,"positions":[`...)
	for i, p := range f.positions[:positions] {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"file":`...)
		b = appendJSONString(b, p.file)
		b = append(b, `,"line":`...)
		b = strconv.AppendInt(b, int64(p.line), 10)
		b = append(b, `,"role":`...)
		b = appendJSONString(b, p.role)
		b = append(b, '}')
	}
	b = append(b, `],"fix":{"hint":`...)
	b = appendJSONString(b, f.hint)
	b = append(b, `},"pid":`...)
	b = strconv.AppendInt(b, int64(os.Getpid()), 10)
	return append(b, '}', '\n')
}

func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
			i++
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			i++
		case c < utf8.RuneSelf:
			b = append(b, c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b = append(b, `\ufffd`...)
			} else {
				b = append(b, s[i:i+size]...)
			}
			i += size
		}
	}
	return append(b, '"')
}
