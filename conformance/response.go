// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Response is an oracle response (or an engine's equivalent) as generic JSON. Numbers are
// json.Number so goldens round-trip byte-exactly.
type Response map[string]any

// ParseResponse decodes one JSON document.
func ParseResponse(b []byte) (Response, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var r Response
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return r, nil
}

// LoadGolden reads a golden file.
func LoadGolden(path string) (Response, error) {
	b, err := os.ReadFile(path) //nolint:gosec // dev tool, caller-chosen path
	if err != nil {
		return nil, err
	}
	r, err := ParseResponse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Verdict is the response's verdict, "" if absent.
func (r Response) Verdict() string { v, _ := r["verdict"].(string); return v }

// Diagnostics returns every diagnostic item: top-level, context_diagnostics and per-module.
func (r Response) Diagnostics() []map[string]any {
	var out []map[string]any
	collect := func(v any) {
		l, _ := v.([]any)
		for _, e := range l {
			if d, ok := e.(map[string]any); ok {
				out = append(out, d)
			}
		}
	}
	collect(r["diagnostics"])
	collect(r["context_diagnostics"])
	mods, _ := r["modules"].([]any)
	for _, m := range mods {
		if mm, ok := m.(map[string]any); ok {
			collect(mm["diagnostics"])
		}
	}
	return out
}

// Marshal renders the golden format: indent 2, sorted keys, ASCII-only escapes, trailing newline
// (matches Python's json.dumps(indent=2, sort_keys=True) + "\n" for the current oracle output;
// numbers keep their source text, so exotic float spellings could differ, which -check would show).
func (r Response) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any(r)); err != nil {
		return nil, err
	}
	// Python's ensure_ascii escapes everything outside 0x20..0x7e; only string contents can
	// hold such bytes.
	in := buf.Bytes()
	var out strings.Builder
	for i := 0; i < len(in); {
		b := in[i]
		if b < 0x7f {
			out.WriteByte(b)
			i++
			continue
		}
		c, n := utf8.DecodeRune(in[i:])
		i += n
		if c >= 0x10000 {
			c -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xd800+(c>>10), 0xdc00+(c&0x3ff))
		} else {
			fmt.Fprintf(&out, `\u%04x`, c)
		}
	}
	return []byte(out.String()), nil
}
