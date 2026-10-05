// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/ipv4_address.c, ipv4_address_no_zone.c,
// ipv4_address_prefix.c, ipv6_address.c, ipv6_address_no_zone.c and ipv6_address_prefix.c
// (BSD-3-Clause, © CESNET); inet_ntop6 follows the glibc/BIND algorithm libyang relies on.

package types

import (
	"bytes"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

func init() {
	reg := func(module, rev, id string, p *plugin, names ...string) {
		p.id = id
		for _, n := range names {
			plugins[pluginKey{module, rev, n}] = p
		}
	}
	const inet = "ietf-inet-types"
	reg(inet, "", "ipv4-address", &plugin{store: storeIPv4Addr, equal: equalIP, compare: compareIP},
		"ipv4-address", "ipv4-address-link-local")
	reg(inet, "", "ipv4-address-no-zone", &plugin{store: storeIPv4NoZone, equal: equalIP, compare: compareIP},
		"ipv4-address-no-zone")
	reg(inet, "", "ipv4-address-prefix", &plugin{store: storeIPv4Prefix, equal: equalIP, compare: compareIP},
		"ipv4-prefix", "ipv4-address-and-prefix")
	reg(inet, "", "ipv6-address", &plugin{store: storeIPv6Addr, equal: equalIP, compare: compareIP},
		"ipv6-address", "ipv6-address-link-local")
	reg(inet, "", "ipv6-address-no-zone", &plugin{store: storeIPv6NoZone, equal: equalIP, compare: compareIP},
		"ipv6-address-no-zone")
	reg(inet, "", "ipv6-address-prefix", &plugin{store: storeIPv6Prefix, equal: equalIP, compare: compareIP},
		"ipv6-prefix", "ipv6-address-and-prefix")
}

// ipValue is the stored form of the inet plugins: the address in network byte order, the zone
// and the prefix length.
type ipValue struct {
	addr   []byte
	zone   string
	hasZ   bool
	prefix uint8
}

// equalIP ports the inet compare callbacks (address, zone, prefix length).
func equalIP(a, b Value) bool {
	x, y := a.ext.(*ipValue), b.ext.(*ipValue)
	return bytes.Equal(x.addr, y.addr) && x.hasZ == y.hasZ && x.zone == y.zone && x.prefix == y.prefix
}

// compareIP ports the inet sort callbacks: address bytes, then no zone < zone, then zone text
// (prefixes: address, then prefix length, as libyang's memcmp of the struct).
func compareIP(a, b Value) int {
	x, y := a.ext.(*ipValue), b.ext.(*ipValue)
	if c := bytes.Compare(x.addr, y.addr); c != 0 {
		return c
	}
	if x.prefix != y.prefix {
		return cmp3(x.prefix < y.prefix, true)
	}
	if x.hasZ != y.hasZ {
		return cmp3(!x.hasZ, true)
	}
	return strings.Compare(x.zone, y.zone)
}

// ptonIP ports inet_pton: IPv4 dotted quad or IPv6 text without zone.
func ptonIP(s string, v6 bool) ([]byte, bool) {
	if strings.ContainsAny(s, "%\x00") {
		return nil, false
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Is4() == v6 {
		return nil, false
	}
	if v6 {
		b := a.As16()
		return b[:], true
	}
	b := a.As4()
	return b[:], true
}

// ntop6 ports inet_ntop(AF_INET6): lower-case hex, the first longest run (≥ 2) of zero groups
// as "::", IPv4-compatible and -mapped addresses with a dotted quad.
func ntop6(a []byte) string {
	var w [8]int
	for i := range w {
		w[i] = int(a[2*i])<<8 | int(a[2*i+1])
	}
	bestBase, bestLen, curBase, curLen := -1, 0, -1, 0
	for i := 0; i < 8; i++ {
		if w[i] == 0 {
			if curBase == -1 {
				curBase, curLen = i, 1
			} else {
				curLen++
			}
			continue
		}
		if curBase != -1 && (bestBase == -1 || curLen > bestLen) {
			bestBase, bestLen = curBase, curLen
		}
		curBase = -1
	}
	if curBase != -1 && (bestBase == -1 || curLen > bestLen) {
		bestBase, bestLen = curBase, curLen
	}
	if bestBase != -1 && bestLen < 2 {
		bestBase = -1
	}
	var b strings.Builder
	for i := 0; i < 8; i++ {
		if bestBase != -1 && i >= bestBase && i < bestBase+bestLen {
			if i == bestBase {
				b.WriteByte(':')
			}
			continue
		}
		if i != 0 {
			b.WriteByte(':')
		}
		if i == 6 && bestBase == 0 && (bestLen == 6 || bestLen == 5 && w[5] == 0xffff) {
			fmt.Fprintf(&b, "%d.%d.%d.%d", a[12], a[13], a[14], a[15])
			return b.String()
		}
		b.WriteString(strconv.FormatInt(int64(w[i]), 16))
	}
	if bestBase != -1 && bestBase+bestLen == 8 {
		b.WriteByte(':')
	}
	return b.String()
}

func ntop(a []byte) string {
	if len(a) == 4 {
		return fmt.Sprintf("%d.%d.%d.%d", a[0], a[1], a[2], a[3])
	}
	return ntop6(a)
}

// storeIPAddr ports lyplg_type_store_ipv4_address / _ipv6_address (ipv4address_str2ip,
// ipv6address_str2ip): an optional "%zone" suffix, then inet_pton. The plugins do not run the
// typedef's pattern.
func storeIPAddr(a *storeArgs, v6 bool) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	ip := &ipValue{}
	addr := a.lex
	if i := strings.IndexByte(a.lex, '%'); i >= 0 {
		addr, ip.zone, ip.hasZ = a.lex[:i], a.lex[i+1:], true
	}
	var ok bool
	if ip.addr, ok = ptonIP(addr, v6); !ok {
		return Value{}, errf("Failed to store IPv%s address \"%s\".", map[bool]string{false: "4", true: "6"}[v6], addr)
	}
	v := Value{typ: a.t, ext: ip, canon: a.lex}
	if v6 && a.f != FormatCanon { // libyang keeps IPv4 text as given, prints IPv6 with inet_ntop
		v.canon = ntop6(ip.addr)
		if ip.hasZ {
			v.canon += "%" + ip.zone
		}
	}
	return v, nil
}

func storeIPv4Addr(a *storeArgs) (Value, *Diag) { return storeIPAddr(a, false) }
func storeIPv6Addr(a *storeArgs) (Value, *Diag) { return storeIPAddr(a, true) }

// storeIPNoZone ports lyplg_type_store_ipv4_address_no_zone / _ipv6_address_no_zone.
func storeIPNoZone(a *storeArgs, v6 bool) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	addr, ok := ptonIP(a.lex, v6)
	if !ok {
		return Value{}, errf("Failed to store IPv%s address \"%s\".", map[bool]string{false: "4", true: "6"}[v6], a.lex)
	}
	v := Value{typ: a.t, ext: &ipValue{addr: addr}, canon: a.lex}
	if v6 && a.f != FormatCanon {
		v.canon = ntop6(addr)
	}
	return v, nil
}

func storeIPv4NoZone(a *storeArgs) (Value, *Diag) { return storeIPNoZone(a, false) }
func storeIPv6NoZone(a *storeArgs) (Value, *Diag) { return storeIPNoZone(a, true) }

// storeIPPrefix ports lyplg_type_store_ipv4_address_prefix / _ipv6_address_prefix
// (ipv4prefix_str2ip, ipv6prefix_str2ip, *_zero_host): length and patterns of the typedef are
// checked, host bits are zeroed only when the type is the "ipv4-prefix"/"ipv6-prefix" typedef
// itself (libyang compares the compiled type's name).
func storeIPPrefix(a *storeArgs, v6 bool) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if !a.only {
		if a.t.Length != nil {
			if d := checkRange(schema.String, a.t.Length, int64(len(a.lex)), a.lex); d != nil {
				return Value{}, d
			}
		}
		if d := checkPatterns(a.t.Patterns, a.lex); d != nil {
			return Value{}, d
		}
	}
	ver := map[bool]string{false: "4", true: "6"}[v6]
	slash := strings.IndexByte(a.lex, '/')
	if slash < 0 { // the pattern guarantees a '/'; libyang would crash on StoreOnly input without one
		return Value{}, errf("Failed to store IPv%s address \"%s\".", ver, a.lex)
	}
	n, err := strconv.ParseUint(leadingInt(a.lex[slash+1:]), 10, 8)
	if err != nil { // ly_strntou8 fails, the prefix length stays 0
		n = 0
	}
	addrText := a.lex[:slash]
	addr, ok := ptonIP(addrText, v6)
	if !ok {
		return Value{}, errf("Failed to store IPv%s address \"%s\".", ver, addrText)
	}
	ip := &ipValue{addr: addr, prefix: uint8(n)}
	if a.t.Typedef == "ipv"+ver+"-prefix" {
		for i := range ip.addr {
			bits := int(ip.prefix) - 8*i
			switch {
			case bits <= 0:
				ip.addr[i] = 0
			case bits < 8:
				ip.addr[i] &= byte(0xff << (8 - bits))
			}
		}
	}
	v := Value{typ: a.t, ext: ip, canon: a.lex}
	if a.f != FormatCanon {
		v.canon = ntop(ip.addr) + "/" + strconv.Itoa(int(ip.prefix))
	}
	return v, nil
}

func storeIPv4Prefix(a *storeArgs) (Value, *Diag) { return storeIPPrefix(a, false) }
func storeIPv6Prefix(a *storeArgs) (Value, *Diag) { return storeIPPrefix(a, true) }
