// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/json.c (BSD-3-Clause, © CESNET).

package lyjson

import "strconv"

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// countInRow is lyjson_count_in_row over s[str:end] (indexes instead of pointers; the backward
// scan runs from end-1 down to str).
func countInRow(s string, str, end int, c byte, backwards bool) int {
	if str >= end {
		return 0
	}
	cnt := 0
	if !backwards {
		for ; str != end && s[str] == c; str++ {
			cnt++
		}
	} else {
		for end--; end >= str && s[end] == c; end-- {
			cnt++
		}
	}
	return cnt
}

// numberIsZero is lyjson_number_is_zero on s[in:end].
func numberIsZero(s string, in, end int) bool {
	if s[in] == '-' || s[in] == '+' {
		in++
	}
	if s[in] == '0' && in+1 < len(s) && s[in+1] == '.' {
		in += 2
		if in >= end {
			return true
		}
	}
	return countInRow(s, in, end, '0', false) == end-in
}

// expNumberCopyNumPart is lyjson_exp_number_copy_num_part: copy num without its decimal point
// (index decPoint, or -1) and put a new one before the digit that lands on dpPosition (if >= 0).
func expNumberCopyNumPart(dst []byte, num string, decPoint, dpPosition int) int {
	d := 0
	for n := 0; n < len(num); n++ {
		switch {
		case n == decPoint:
		case d == dpPosition:
			dst[d] = '.'
			dst[d+1] = num[n]
			d += 2
		default:
			dst[d] = num[n]
			d++
		}
	}
	return d
}

// expNumber is lyjson_exp_number: rewrite a JSON number with an exponent as YANG text. in is the
// whole number, exp the index of its 'e'/'E'.
func (l *Lexer) expNumber(in string, exp int) (string, error) {
	const leadingZero = 1
	if exp > 0xffff {
		return "", l.fail(VESemantics, "JSON number is too long.")
	}
	// strtoll(exponent+1): the digits end the number, so only the range can fail
	eVal, perr := strconv.ParseInt(in[exp+1:], 10, 64)
	if perr != nil || eVal > 0xffff || eVal < -0xffff {
		return "", l.fail(VESemantics, "Exponent out-of-bounds in a JSON Number value ("+in+").")
	}
	minus := 0
	if in[0] == '-' {
		minus = 1
	}
	var num int // index of the 'numeric part' in in
	lz := in[minus] == '0'
	if lz {
		num = minus + 1 // skip the leading zero, keep the '.'
	} else {
		num = minus
	}
	numLen := exp - num
	decPoint := -1 // relative to num
	for i := 0; i < numLen; i++ {
		if in[num+i] == '.' {
			decPoint = i
			break
		}
	}
	dpPosition := numLen + int(eVal)
	if decPoint >= 0 {
		dpPosition = decPoint + int(eVal)
	}

	// drop useless trailing zeros of the 'numeric part'
	if dpPosition > 0 {
		numLen -= countInRow(in, num+dpPosition-1, exp, '0', true)
	} else {
		numLen -= countInRow(in, num, exp, '0', true)
	}

	dot := 1
	if decPoint >= 0 && numLen-1 == dpPosition {
		dot = -1
	} else if decPoint >= 0 {
		dot = 0
	}

	var buf []byte
	var bufLen int
	alloc := func(n int) error {
		if n+1 > numberMaxLen {
			return l.fail(VESemantics, "Number encoded as a string exceeded the LY_NUMBER_MAXLEN limit.")
		}
		bufLen, buf = n, make([]byte, n+1)
		return nil
	}
	i := 0
	writeMinus := func() {
		if minus == 1 {
			buf[i] = '-'
			i++
		}
	}
	switch {
	case dpPosition <= 0: // decimal point before the integer, with zeros
		zeros := -dpPosition
		if err := alloc(minus + leadingZero + dot + zeros + numLen); err != nil {
			return "", err
		}
		writeMinus()
		buf[i], buf[i+1] = '0', '.'
		i += 2
		for z := 0; z < zeros; z++ {
			buf[i] = '0'
			i++
		}
		expNumberCopyNumPart(buf[i:], in[num:num+numLen], decPoint, -1)
	case lz && dpPosition < numLen: // decimal point inside the digits
		num++
		numLen--
		dpPosition--
		zeros := countInRow(in, num, num+dpPosition+1, '0', false)
		if zeros == dpPosition+1 {
			zeros--
			dpPosition = 1
			dot = 1
		} else {
			dot = 0
		}
		if err := alloc(minus + dot + (numLen - zeros)); err != nil {
			return "", err
		}
		writeMinus()
		expNumberCopyNumPart(buf[i:], in[num+zeros:num+numLen], -1, dpPosition)
	case dpPosition < numLen:
		if err := alloc(minus + dot + numLen); err != nil {
			return "", err
		}
		writeMinus()
		expNumberCopyNumPart(buf[i:], in[num:num+numLen], decPoint, dpPosition)
	case lz: // an integer; skip the '.' and the useless zeros, then append zeros
		num++
		numLen--
		zeros := countInRow(in, num, num+numLen, '0', false)
		if err := alloc(minus + dpPosition - zeros); err != nil {
			return "", err
		}
		writeMinus()
		i += expNumberCopyNumPart(buf[i:], in[num+zeros:num+numLen], -1, dpPosition)
		for ; i < bufLen; i++ {
			buf[i] = '0'
		}
	default:
		if err := alloc(minus + dpPosition); err != nil {
			return "", err
		}
		writeMinus()
		i += expNumberCopyNumPart(buf[i:], in[num:num+numLen], decPoint, dpPosition)
		for ; i < bufLen; i++ {
			buf[i] = '0'
		}
	}
	return string(buf[:bufLen]), nil
}

// skipDigits returns the offset (from the current position) of the first non-digit at or after off.
func (l *Lexer) skipDigits(off int) int {
	for isDigit(l.at(l.pos + off)) {
		off++
	}
	return off
}

// number is lyjson_number: lex a number at the current position, store its canonical text.
func (l *Lexer) number() error {
	at := func(i int) byte { return l.at(l.pos + i) }
	off, exp, minus := 0, -1, 0
	bad := func() error {
		if c := at(off); c != 0 {
			return l.fail(VESyntax, `Invalid character in JSON Number value ("`+string([]byte{c})+`").`)
		}
		return l.errEOF()
	}
	if at(off) == '-' {
		off++
		minus = 1
	}
	switch {
	case at(off) == '0':
		off++
	case isDigit(at(off)):
		off = l.skipDigits(off + 1)
	default:
		return bad()
	}
	if at(off) == '.' {
		off++
		if !isDigit(at(off)) {
			return bad()
		}
		off = l.skipDigits(off)
	}
	if at(off) == 'e' || at(off) == 'E' {
		exp = off
		off++
		if at(off) == '+' || at(off) == '-' {
			off++
		}
		if !isDigit(at(off)) {
			return bad()
		}
		off = l.skipDigits(off)
	}
	s := string(l.in[l.pos : l.pos+off])
	end := off
	if exp >= 0 {
		end = exp
	}
	switch {
	case numberIsZero(s, 0, end):
		l.value = s[:minus+1]
	case exp >= 0 && numberIsZero(s, exp+1, off):
		l.value = s[:exp]
	case exp >= 0:
		v, err := l.expNumber(s, exp)
		if err != nil {
			return err
		}
		l.value = v
	default:
		if off > numberMaxLen {
			return l.fail(VESemantics, "Number encoded as a string exceeded the LY_NUMBER_MAXLEN limit.")
		}
		l.value = s
	}
	l.pos += off
	return nil
}
