// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/date_and_time.c, src/plugins_types/date.c,
// src/plugins_types/time.c, src/plugins_types/hex_string.c
// and src/tree_data_common.c (ly_time_str2time, ly_time_time2str, ly_time_tz_offset_at)
// (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"math"
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
	hex := &plugin{id: "hex-string", store: storeHexString, validate: true}
	for _, n := range []string{"phys-address", "mac-address", "hex-string", "uuid"} {
		plugins[pluginKey{yt, "", n}] = hex
	}
	date := &plugin{id: "date", store: storeDate, equal: equalDate, compare: compareDate, validate: true}
	tm := &plugin{id: "time", store: storeTime, equal: equalTime, compare: compareTime, validate: true}
	for _, n := range []string{"date", "date-no-zone"} {
		plugins[pluginKey{yt, "", n}] = date
	}
	for _, n := range []string{"time", "time-no-zone"} {
		plugins[pluginKey{yt, "", n}] = tm
	}
}

// time2str ports ly_time_time2str: unix in dateTimeZone with its offset, frac ("" or ".digits")
// after the seconds.
func time2str(unix int64, frac string) string {
	t := time.Unix(unix, 0).In(dateTimeZone)
	_, off := t.Zone()
	h, m := off/3600, off/60%60
	if m < 0 {
		m = -m
	}
	return formatTime(t, frac) + fmt.Sprintf("%+03d:%02d", h, m)
}

// cStrptime ports glibc strptime for the numeric formats "%Y-%m-%d" and "%H:%M:%S" (fields: the
// digits and the [min, max] of each conversion, literal separators between them): the values and
// the number of bytes consumed, ok false when the input does not fit.
func cStrptime(s string, sep byte, fields [3][3]int) (vals [3]int, n int, ok bool) {
	for f, fd := range fields {
		if f > 0 {
			if n >= len(s) || s[n] != sep {
				return vals, n, false
			}
			n++
		}
		for n < len(s) && (s[n] == ' ' || s[n] >= '\t' && s[n] <= '\r') { // get_number skips spaces
			n++
		}
		if n >= len(s) || !isDigit(s[n]) {
			return vals, n, false
		}
		v, digits := 0, fd[0]
		for {
			v = v*10 + int(s[n]-'0')
			n++
			digits--
			if digits == 0 || v*10 > fd[2] || n >= len(s) || !isDigit(s[n]) {
				break
			}
		}
		if v < fd[1] || v > fd[2] {
			return vals, n, false
		}
		vals[f] = v
	}
	return vals, n, true
}

// dateVal is the stored form of date and date-no-zone (lyd_value_date, lyd_value_date_nz).
type dateVal struct {
	unix      int64
	unknownTZ bool // date only: "Z"
}

// storeDate ports lyplg_type_store_date and its canonical print (lyplg_type_print_date). libyang
// picks the date form by the type's name, so a typedef derived from date stores as date-no-zone.
func storeDate(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if !a.only {
		if d := checkPatterns(a.t.Patterns, a.lex); d != nil {
			return Value{}, d
		}
	}
	isDate := a.t.Typedef == "date"
	var dv dateVal
	if isDate {
		s := a.lex + "T00:00:00" // the date-and-time of the day's start
		if len(a.lex) >= 10 {
			s = a.lex[:10] + "T00:00:00" + a.lex[10:]
		}
		dt, msg := timeStr2Time(s)
		if msg != "" {
			return Value{}, &Diag{Code: CodeNone, Msg: msg}
		}
		dv.unix = dt.unix
		dv.unknownTZ = strings.HasSuffix(a.lex, "Z")
	} else {
		ymd, n, ok := cStrptime(a.lex, '-', [3][3]int{{4, 0, 9999}, {2, 1, 12}, {2, 1, 31}})
		if !ok || n != len(a.lex) {
			return Value{}, errf("Failed to parse %s value \"%s\".", a.t.Typedef, a.lex)
		}
		dv.unix = time.Date(ymd[0], time.Month(ymd[1]), ymd[2], 0, 0, 0, 0, time.UTC).Unix() // timegm
	}
	v := Value{typ: a.t, ext: &dv, canon: a.lex}
	if a.f == FormatCanon {
		return v, nil
	}
	if !isDate || dv.unknownTZ { // in UTC
		t := time.Unix(dv.unix, 0).UTC()
		v.canon = fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
		if isDate {
			v.canon += "Z"
		}
	} else {
		s := time2str(dv.unix, "")
		v.canon = s[:10] + s[19:] // without the time of day
	}
	return v, nil
}

// equalDate ports lyplg_type_compare_date.
func equalDate(a, b Value) bool {
	x, y := a.ext.(*dateVal), b.ext.(*dateVal)
	return x.unix == y.unix && x.unknownTZ == y.unknownTZ
}

// compareDate ports lyplg_type_sort_date, ordering by the sign of the gap (D-0028: libyang casts
// difftime() to int).
func compareDate(a, b Value) int {
	x, y := a.ext.(*dateVal), b.ext.(*dateVal)
	return cmp3(x.unix < y.unix, x.unix > y.unix)
}

// timeVal is the stored form of time and time-no-zone (lyd_value_time, lyd_value_time_nz).
type timeVal struct {
	seconds   uint32 // libyang's uint32_t: a time before the epoch day wraps around
	frac      string
	hasFrac   bool
	unknownTZ bool // time only: "Z"
}

// storeTime ports lyplg_type_store_time and its canonical print (lyplg_type_print_time); the
// form is picked by the type's name as for date.
func storeTime(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if !a.only {
		if d := checkPatterns(a.t.Patterns, a.lex); d != nil {
			return Value{}, d
		}
	}
	isTime := a.t.Typedef == "time"
	var tv timeVal
	if isTime {
		dt, msg := timeStr2Time("1970-01-01T" + a.lex)
		if msg != "" {
			return Value{}, &Diag{Code: CodeNone, Msg: msg}
		}
		tv.seconds = uint32(dt.unix) //nolint:gosec // libyang stores the time_t in a uint32_t
		tv.frac, tv.hasFrac = dt.frac, dt.hasFrac
		tv.unknownTZ = strings.HasSuffix(a.lex, "Z")
	} else {
		hms, n, ok := cStrptime(a.lex, ':', [3][3]int{{2, 0, 23}, {2, 0, 59}, {2, 0, 61}})
		if !ok || n != 8 {
			return Value{}, errf("Failed to parse %s value \"%s\".", a.t.Typedef, a.lex)
		}
		tv.seconds = uint32(hms[0]*3600 + hms[1]*60 + hms[2]) //nolint:gosec // at most 86461
		if n < len(a.lex) && a.lex[n] == '.' {
			tv.frac, tv.hasFrac = a.lex[n+1:], true
		}
	}
	v := Value{typ: a.t, ext: &tv, canon: a.lex}
	if a.f == FormatCanon {
		return v, nil
	}
	frac := ""
	if tv.hasFrac {
		frac = "." + tv.frac
	}
	if !isTime || tv.unknownTZ { // in UTC
		t := time.Unix(int64(tv.seconds), 0).UTC()
		v.canon = fmt.Sprintf("%02d:%02d:%02d%s", t.Hour(), t.Minute(), t.Second(), frac)
		if isTime {
			v.canon += "Z"
		}
	} else {
		v.canon = time2str(int64(tv.seconds), frac)[11:] // without the date
	}
	return v, nil
}

// equalTime ports lyplg_type_compare_time.
func equalTime(a, b Value) bool {
	x, y := a.ext.(*timeVal), b.ext.(*timeVal)
	return x.seconds == y.seconds && x.unknownTZ == y.unknownTZ && x.hasFrac == y.hasFrac && x.frac == y.frac
}

// compareTime ports lyplg_type_sort_time (lyplg_type_sort_by_fractions), ordering by the sign of
// the gap (D-0028).
func compareTime(a, b Value) int {
	x, y := a.ext.(*timeVal), b.ext.(*timeVal)
	if x.seconds != y.seconds {
		return cmp3(x.seconds < y.seconds, true)
	}
	return compareFractions(x.frac, y.frac)
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
		return Value{}, &Diag{Code: CodeNone, Msg: msg}
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
		v.canon = formatTime(time.Unix(dt.unix, 0).UTC(), frac) + tz
	} else {
		// libyang ly_time_time2str prints in the host's local zone (localtime_r); we print in
		// dateTimeZone, UTC unless a test changes it (D-0025).
		v.canon = time2str(dt.unix, frac)
	}
	return v, nil
}

// dateTimeZone is the zone known-offset date-and-time values are printed in. Always UTC; the
// libyang unit tests assume UTC-2, so the ported tests switch it (D-0025).
var dateTimeZone = time.UTC

// formatTime prints like libyang's "%04d-%02d-%02dT%02d:%02d:%02d" (years below 1000 and
// negative years as printf does, e.g. "0000", "-001").
func formatTime(t time.Time, frac string) string {
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d%s", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), frac)
}

// cStrtol is C strtol(s, &end, 10): leading space, sign, digits, LONG_MIN/LONG_MAX on overflow;
// end is 0 (the start) when no digits were read.
func cStrtol(s string) (int64, int) {
	t := trimCSpace(s)
	mag, neg, end, ok := cStrtou(t, 10)
	if end == 0 {
		return 0, 0
	}
	end += len(s) - len(t)
	switch {
	case neg && (!ok || mag > 1<<63):
		return math.MinInt64, end
	case !neg && (!ok || mag > math.MaxInt64):
		return math.MaxInt64, end
	case neg && mag == 1<<63:
		return math.MinInt64, end
	case neg:
		return -int64(mag), end //nolint:gosec // mag < 2^63 checked above
	}
	return int64(mag), end //nolint:gosec // mag <= MaxInt64 checked above
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
	shift, end := cStrtol(rest)
	if shift > 23 || shift < -23 {
		return nil, fmt.Sprintf("Invalid date-and-time timezone hour \"%d\".", shift)
	}
	after := rest[end:]
	if after == "" || after[0] != ':' {
		return nil, fmt.Sprintf("Invalid date-and-time timezone hour \"%s\".", rest)
	}
	// "-00:30": the sign is taken from the hour value, so libyang applies +00:30 (D-0026).
	shiftM, _ := cStrtol(after[1:])
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

// compareDateAndTime ports lyplg_type_sort_date_and_time (lyplg_type_sort_by_fractions), ordering
// by the sign of the gap (D-0028).
func compareDateAndTime(a, b Value) int {
	x, y := a.ext.(*dateTime), b.ext.(*dateTime)
	if x.unix != y.unix {
		return cmp3(x.unix < y.unix, true)
	}
	return compareFractions(x.frac, y.frac)
}

// compareFractions ports lyplg_type_sort_by_fractions: an all-zero (or absent) fraction first,
// then by the digits as strings.
func compareFractions(fx, fy string) int {
	zx, zy := strings.Trim(fx, "0") == "", strings.Trim(fy, "0") == ""
	switch {
	case zx && zy:
		return 0
	case zx:
		return -1
	case zy:
		return 1
	}
	return strings.Compare(fx, fy)
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
