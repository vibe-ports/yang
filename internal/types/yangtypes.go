// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/date_and_time.c, src/plugins_types/hex_string.c
// and src/tree_data_common.c (ly_time_str2time, ly_time_time2str, ly_time_tz_offset_at)
// (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	const yt = "ietf-yang-types"
	dtOld := &plugin{id: "date-and-time", store: func(a *storeArgs) (Value, *Diag) { return storeDateAndTime(a, true) },
		equal: equalDateAndTime, compare: compareDateAndTime}
	dtNew := &plugin{id: "date-and-time", store: func(a *storeArgs) (Value, *Diag) { return storeDateAndTime(a, false) },
		equal: equalDateAndTime, compare: compareDateAndTime}
	plugins[pluginKey{yt, "2013-07-15", "date-and-time"}] = dtOld
	plugins[pluginKey{yt, "2025-12-22", "date-and-time"}] = dtNew
	hex := &plugin{id: "hex-string", store: storeHexString}
	for _, n := range []string{"phys-address", "mac-address", "hex-string", "uuid"} {
		plugins[pluginKey{yt, "", n}] = hex
	}
}

// dateTime is the stored form of date-and-time (libyang struct lyd_value_date_and_time).
type dateTime struct {
	unix      int64
	frac      string // fraction-of-second digits, "" when none
	hasFrac   bool
	unknownTZ bool // "-00:00" (and "Z" in the 2025 revision): offset to local time unknown
}

// storeDateAndTime ports lyplg_type_store_date_and_time; oldRev is the 2013-07-15 revision,
// where only "-00:00" means an unknown time zone.
func storeDateAndTime(a *storeArgs, oldRev bool) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if !a.only {
		if d := checkPatterns(a.t.Patterns, a.lex); d != nil {
			return Value{}, d
		}
	}
	dt, msg := timeStr2Time(a.lex)
	if msg != "" { // ly_err_new(err, LY_EINVAL, 0, ...): no validation error code
		return Value{}, &Diag{Code: "LYVE_SUCCESS", Msg: msg}
	}
	if strings.HasSuffix(a.lex, "-00:00") || !oldRev && strings.HasSuffix(a.lex, "Z") {
		dt.unknownTZ = true
	}
	v := Value{typ: a.t, ext: dt, canon: a.lex}
	if a.f == FormatCanon {
		return v, nil
	}
	frac := ""
	if dt.hasFrac {
		frac = "." + dt.frac
	}
	if dt.unknownTZ {
		tz := "Z"
		if oldRev {
			tz = "-00:00"
		}
		v.canon = time.Unix(dt.unix, 0).UTC().Format("2006-01-02T15:04:05") + frac + tz
	} else { // ly_time_time2str: the host's local time zone, like libyang
		t := time.Unix(dt.unix, 0).In(time.Local)
		_, off := t.Zone()
		h, m := off/3600, off/60%60
		if m < 0 {
			m = -m
		}
		v.canon = t.Format("2006-01-02T15:04:05") + frac + fmt.Sprintf("%+03d:%02d", h, m)
	}
	return v, nil
}

// cAtoi is C atoi: optional space and sign, then digits.
func cAtoi(s string) int {
	s = trimCSpace(s)
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg, s = s[0] == '-', s[1:]
	}
	n := 0
	for i := 0; i < len(s) && isDigit(s[i]) && n < 1<<30; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}

// timeStr2Time ports ly_time_str2time (timegm normalises out-of-range days, as Go's time.Date).
func timeStr2Time(v string) (*dateTime, string) {
	if len(v) < 18 {
		return nil, "Invalid argument value (ly_time_str2time())."
	}
	at := func(i int) byte {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	year, mon, day := cAtoi(v[0:]), cAtoi(v[5:])-1, cAtoi(v[8:])
	hour, minute, sec := cAtoi(v[11:]), cAtoi(v[14:]), cAtoi(v[17:])
	switch {
	case mon > 11:
		return nil, fmt.Sprintf("Invalid date-and-time month \"%d\".", mon)
	case day < 1 || day > 31:
		return nil, fmt.Sprintf("Invalid date-and-time day of month \"%d\".", day)
	case hour > 23:
		return nil, fmt.Sprintf("Invalid date-and-time hours \"%d\".", hour)
	case minute > 59:
		return nil, fmt.Sprintf("Invalid date-and-time minutes \"%d\".", minute)
	case sec > 60:
		return nil, fmt.Sprintf("Invalid date-and-time seconds \"%d\".", sec)
	}
	dt := &dateTime{unix: time.Date(year, time.Month(mon+1), day, hour, minute, sec, 0, time.UTC).Unix()}
	i := 19
	if at(i) == '.' {
		i++
		n := 0
		for isDigit(at(i + n)) {
			n++
		}
		if n == 0 {
			return nil, "Missing date-and-time fractions after '.'."
		}
		dt.frac, dt.hasFrac = v[i:i+n], true
		i += n
	}
	if c := at(i); c == 'Z' || c == 'z' {
		return dt, ""
	}
	rest := ""
	if i < len(v) {
		rest = v[i:]
	}
	mag, neg, end, _ := cStrtou(trimCSpace(rest), 10)
	shift := int64(mag) //nolint:gosec // bounded below
	if neg {
		shift = -shift
	}
	if shift > 23 || shift < -23 {
		return nil, fmt.Sprintf("Invalid date-and-time timezone hour \"%d\".", shift)
	}
	after := trimCSpace(rest)[end:]
	if after == "" || after[0] != ':' {
		return nil, fmt.Sprintf("Invalid date-and-time timezone hour \"%s\".", rest)
	}
	shiftM := int64(cAtoi(after[1:]))
	if shiftM < 0 || shiftM > 59 {
		return nil, fmt.Sprintf("Invalid date-and-time timezone minutes \"%d\".", shiftM)
	}
	if shift < 0 {
		shiftM = -shiftM
	}
	dt.unix -= shift*3600 + shiftM*60
	return dt, ""
}

// equalDateAndTime ports lyplg_type_compare_date_and_time.
func equalDateAndTime(a, b Value) bool {
	x, y := a.ext.(*dateTime), b.ext.(*dateTime)
	return x.unix == y.unix && x.unknownTZ == y.unknownTZ && x.hasFrac == y.hasFrac && x.frac == y.frac
}

// compareDateAndTime ports lyplg_type_sort_date_and_time (lyplg_type_sort_by_fractions).
func compareDateAndTime(a, b Value) int {
	x, y := a.ext.(*dateTime), b.ext.(*dateTime)
	if x.unix != y.unix {
		return cmp3(x.unix < y.unix, true)
	}
	zx, zy := strings.Trim(x.frac, "0") == "", strings.Trim(y.frac, "0") == ""
	switch {
	case zx && zy:
		return 0
	case zx:
		return -1
	case zy:
		return 1
	}
	return strings.Compare(x.frac, y.frac)
}

// storeHexString ports lyplg_type_store_hex_string: lower-cased (ASCII), then the string
// restrictions of the typedef.
func storeHexString(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	canon := a.lex
	if a.f != FormatCanon {
		b := []byte(a.lex)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + 'a' - 'A'
			}
		}
		canon = string(b)
	}
	b := *a
	b.lex, b.f = canon, FormatCanon
	if b.only {
		return Value{typ: a.t, canon: canon}, nil
	}
	v, d := storeStringRestrictions(&b)
	return v, d
}
