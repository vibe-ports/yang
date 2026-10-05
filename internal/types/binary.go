// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/binary.c (BSD-3-Clause, © CESNET).

package types

import (
	"encoding/base64"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// storeBinary ports lyplg_type_store_binary + lyplg_type_validate_value_binary. The canonical
// form is the base64 text as given (PEM newlines removed), not a re-encoding, as in libyang.
func storeBinary(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	s := a.lex
	if a.f != FormatCanon {
		var d *Diag
		if s, d = base64Newlines(s); d != nil {
			return Value{}, d
		}
		if d := base64Validate(s); d != nil {
			return Value{}, d
		}
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil { // FormatCanon input is trusted by libyang; reject instead of decoding garbage
		return Value{}, errf("Invalid Base64 value \"%s\".", s)
	}
	v := Value{typ: a.t, bin: data, canon: s}
	if !a.only && a.t.Length != nil {
		if d := checkRange(schema.Binary, a.t.Length, int64(len(data)), s); d != nil {
			return Value{}, d
		}
	}
	return v, nil
}

// base64Newlines ports binary_base64_newlines: PEM-style data with a newline after every 64
// characters, recognised by a newline as the 65th character.
func base64Newlines(s string) (string, *Diag) {
	if len(s) < 65 || s[64] != '\n' {
		return s, nil
	}
	var b strings.Builder
	for len(s) > 64 {
		if s[64] != '\n' {
			return "", errf("Newlines are expected every 64 Base64 characters.")
		}
		b.WriteString(s[:64])
		s = s[65:]
	}
	b.WriteString(s)
	return b.String(), nil
}

// base64Validate ports binary_base64_validate.
func base64Validate(s string) *Diag {
	idx := 0
	for idx < len(s) && isBase64Char(s[idx]) {
		idx++
	}
	pad := 0
	for idx+pad < len(s) && pad < 2 && s[idx+pad] == '=' {
		pad++
	}
	if len(s) != idx+pad {
		c := s[idx+pad]
		if c >= 0x20 && c < 0x7f {
			return errf("Invalid Base64 character '%c'.", c)
		}
		return errf("Invalid Base64 character 0x%x.", uint32(int32(int8(c)))) //nolint:gosec // C prints a signed char as %x
	}
	if len(s)%4 != 0 {
		return errf("Base64 encoded value length must be divisible by 4.")
	}
	return nil
}

func isBase64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
}
