// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/ly_common.c (BSD-3-Clause, © CESNET).

package lyxml

// ValuePrefixNext is ly_value_prefix_next (ly_common.c:1260): the byte length
// of a literal chunk or an XML QName prefix without its colon, and the next
// chunk's byte offset (-1 at the end). ok distinguishes an empty/end scan from
// invalid input. The scanner uses the XML lexer's libyang-compatible decoder,
// including ly_getutf8's accepted non-canonical four-byte sequences.
// It uses xml.h's Unicode QName rules, shared with the XML lexer. The colon is
// skipped before the following chunk, even when no local name follows it.
//
// Keep all value-prefix consumers on this scanner: ly_store_prefix_data
// (tree_data_common.c:1321) and lyxml_value_compare (xml.c:1351) both call it.
// A false ok is never end-of-input: parse/store callers propagate LY_EINVAL,
// while comparison callers report unequal, as lyd_compare_single_value does.
func ValuePrefixNext(s string, begin int) (length int, isPrefix bool, next int, ok bool) {
	next = -1
	if begin >= len(s) {
		return 0, false, next, true
	}
	stop, prefix, found := begin, -1, false
	var c rune
	var bytesRead int
	read := func() bool {
		r, n := decodeUTF8(s, stop)
		if n == 0 {
			return false
		}
		c, bytesRead = r, n
		stop += n
		return true
	}
	for {
		for {
			if !read() {
				return 0, false, -1, false
			}
			if isNameStart(c) || stop >= len(s) {
				break
			}
		}
		if stop >= len(s) {
			break
		}
		prefix = stop - bytesRead
		for {
			if !read() {
				return 0, false, -1, false
			}
			if !isNameChar(c) || stop >= len(s) {
				break
			}
		}
		found = c == ':'
		if stop >= len(s) || found {
			break
		}
	}
	switch {
	case begin == prefix && found:
		if stop < len(s) {
			next = stop
		}
		return stop - bytesRead - begin, true, next, true
	case begin != prefix && found:
		return prefix - begin, false, prefix, true
	}
	return stop - begin, false, next, true
}
