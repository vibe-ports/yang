// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/types/{inet_types,yang_types}.c
// (BSD-3-Clause, © CESNET).

package types

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-ports/yang/internal/schema"
)

// ietfTypes builds the compiled typedefs of ietf-inet-types / ietf-yang-types the plugins hook.
func ietfTypes(yangRev string) map[string]*schema.Type {
	inet := &schema.Module{Name: "ietf-inet-types", Revision: "2013-07-15", Implemented: true}
	yt := &schema.Module{Name: "ietf-yang-types", Revision: yangRev, Implemented: true}
	td := func(m *schema.Module, name string, b schema.BaseType, opts ...opt) *schema.Type {
		t := typ(b, opts...)
		t.Typedef, t.TypedefModule = name, m
		return t
	}
	ts := map[string]*schema.Type{}
	for _, n := range []string{"ipv4-address", "ipv6-address", "ipv4-address-no-zone", "ipv6-address-no-zone",
		"ipv4-prefix", "ipv6-prefix"} {
		ts[n] = td(inet, n, schema.String)
	}
	union := func(name string, members ...string) {
		ts[name] = td(inet, name, schema.Union, func(t *schema.Type) {
			for _, m := range members {
				t.Union = append(t.Union, ts[m])
			}
		})
	}
	union("ip-address", "ipv4-address", "ipv6-address")
	union("ip-address-no-zone", "ipv4-address-no-zone", "ipv6-address-no-zone")
	union("ip-prefix", "ipv4-prefix", "ipv6-prefix")
	ts["date-and-time"] = td(yt, "date-and-time", schema.String, pat(dateAndTimePattern, false))
	ts["hex-string"] = td(yt, "hex-string", schema.String, pat(`([0-9a-fA-F]{2}(:[0-9a-fA-F]{2})*)?`, false))
	ts["mac-address"] = td(yt, "mac-address", schema.String, pat(`[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}`, false))
	ts["uuid"] = td(yt, "uuid", schema.String,
		pat(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`, false))
	return ts
}

const dateAndTimePattern = `[0-9]{4}-(1[0-2]|0[1-9])-(0[1-9]|[1-2][0-9]|3[0-1])T(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]+)?(Z|[\+\-]((1[0-3]|0[0-9]):([0-5][0-9])|14:00))?`

// withLocal runs f with known-offset date-and-time values printed in loc (libyang prints them
// in the host's zone and its C tests run with UTC-2; ours is always UTC, D-0025).
func withLocal(loc *time.Location, f func()) {
	old := dateTimeZone
	dateTimeZone = loc
	defer func() { dateTimeZone = old }()
	f()
}

func TestInetTypes(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	cases := []struct{ src, typedef, lex, canon, err string }{
		{"inet_types.c:112", "ip-address", "192.168.0.1", "192.168.0.1", ""},
		{"inet_types.c:113", "ip-address", "192.168.0.1%12", "192.168.0.1%12", ""},
		{"inet_types.c:114", "ip-address", "2008:15:0:0:0:0:feAC:1", "2008:15::feac:1", ""},
		{"inet_types.c:117", "ipv6-address", "FAAC:21:011:Da85::87:daaF%1", "faac:21:11:da85::87:daaf%1", ""},
		{"inet_types.c:120", "ip-address-no-zone", "127.0.0.1", "127.0.0.1", ""},
		{"inet_types.c:121", "ip-address-no-zone", "0:00:000:0000:000:00:0:1", "::1", ""},
		{"inet_types.c:124", "ipv6-address-no-zone", "A:B:c:D:e:f:1:0", "a:b:c:d:e:f:1:0", ""},
		{"inet_types.c:127", "ip-prefix", "158.1.58.4/1", "128.0.0.0/1", ""},
		{"inet_types.c:128", "ip-prefix", "158.1.58.4/24", "158.1.58.0/24", ""},
		{"inet_types.c:129", "ip-prefix", "2000:A:B:C:D:E:f:a/16", "2000::/16", ""},
		{"inet_types.c:132", "ipv4-prefix", "0.1.58.4/32", "0.1.58.4/32", ""},
		{"inet_types.c:133", "ipv4-prefix", "12.1.58.4/8", "12.0.0.0/8", ""},
		{"inet_types.c:136", "ipv6-prefix", "::C:D:E:f:a/112", "::c:d:e:f:0/112", ""},
		{"inet_types.c:137", "ipv6-prefix", "::C:D:E:f:a/110", "::c:d:e:c:0/110", ""},
		{"inet_types.c:138", "ipv6-prefix", "::C:D:E:f:a/96", "::c:d:e:0:0/96", ""},
		{"inet_types.c:139", "ipv6-prefix", "::C:D:E:f:a/55", "::/55", ""},
		{"inet_types.c:151", "ipv4-address", "192.168.0.1", "192.168.0.1", ""},
		{"inet_types.c:152", "ipv4-address", "192.168.0.333", "", `Failed to store IPv4 address "192.168.0.333".`},
		{"ipv4-leading-zero", "ipv4-address-no-zone", "01.2.3.4", "", `Failed to store IPv4 address "01.2.3.4".`},
		{"ipv6-mapped", "ipv6-address", "::FFFF:1.2.3.4", "::ffff:1.2.3.4", ""},
		{"ipv6-compat", "ipv6-address", "::0102:0304", "::1.2.3.4", ""},
		{"ipv6-first-longest-run", "ipv6-address", "1:0:0:2:0:0:3:4", "1::2:0:0:3:4", ""},
		{"ipv6-single-zero", "ipv6-address", "1:0:2:3:4:5:6:7", "1:0:2:3:4:5:6:7", ""},
		{"ipv6-bad", "ipv6-address", "1:::2", "", `Failed to store IPv6 address "1:::2".`},
		{"ipv6-v4", "ipv6-address-no-zone", "1.2.3.4", "", `Failed to store IPv6 address "1.2.3.4".`},
		{"ipv4-nul (D-0027)", "ipv4-address", "1.2.3.4\x00junk", "", "Failed to store IPv4 address \"1.2.3.4\x00junk\"."},
	}
	for _, c := range cases {
		v, d := Store(ts[c.typedef], c.lex, FormatXML, HintData, nil, nil)
		switch {
		case c.err != "" && (d == nil || d.Msg != c.err):
			t.Errorf("%s: got %v %q, want error %q", c.src, d, v.Canonical(), c.err)
		case c.err == "" && (d != nil || v.Canonical() != c.canon):
			t.Errorf("%s: got %v %q, want %q", c.src, d, v.Canonical(), c.canon)
		}
	}
	// equality and order use the binary form
	a, _ := Store(ts["ipv6-address"], "::1", FormatXML, HintData, nil, nil)
	b, _ := Store(ts["ipv6-address"], "0::0001", FormatXML, HintData, nil, nil)
	z, _ := Store(ts["ipv6-address"], "::1%eth0", FormatXML, HintData, nil, nil)
	if !Equal(a, b) || Equal(a, z) || Compare(a, z) >= 0 {
		t.Error("ipv6 equality/order")
	}
	// a derived typedef inherits the plugin but not the host-bit zeroing (libyang tests the name)
	derived := &schema.Type{Base: schema.String, Typedef: "my-prefix", TypedefModule: &schema.Module{Name: "m"}, From: ts["ipv4-prefix"]}
	if v, d := Store(derived, "12.1.58.4/8", FormatXML, HintData, nil, nil); d != nil || v.Canonical() != "12.1.58.4/8" {
		t.Errorf("derived prefix: %v %q", d, v.Canonical())
	}
}

func TestYangTypes(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	pe := func(v string) string {
		return `Unsatisfied pattern - "` + v + `" does not match "` + dateAndTimePattern + `".`
	}
	cases := []struct{ src, typedef, lex, canon, err string }{
		{"yang_types.c:100", "date-and-time", "2005-05-25T23:15:15.88888Z", "2005-05-25T23:15:15.88888Z", ""},
		{"yang_types.c:101", "date-and-time", "2005-05-31T23:15:15-08:59", "2005-06-01T06:14:15-02:00", ""},
		{"yang_types.c:102", "date-and-time", "2005-06-01T11:15:15-11:00", "2005-06-01T20:15:15-02:00", ""},
		{"yang_types.c:105", "date-and-time", "1970-01-01T00:59:59-02:00", "1970-01-01T00:59:59-02:00", ""},
		{"yang_types.c:106", "date-and-time", "1969-12-31T23:59:59-02:00", "1969-12-31T23:59:59-02:00", ""},
		{"yang_types.c:109", "date-and-time", "2005-02-29T23:15:15-02:00", "2005-03-01T23:15:15-02:00", ""},
		{"yang_types.c:112", "date-and-time", "2005-05-25T23:15:15.88888+04:30", "2005-05-25T16:45:15.88888-02:00", ""},
		{"yang_types.c:115", "date-and-time", "2017-02-01T00:00:00-00:00", "2017-02-01T00:00:00Z", ""},
		{"yang_types.c:116", "date-and-time", "2021-02-29T00:00:00-00:00", "2021-03-01T00:00:00Z", ""},
		{"yang_types.c:118", "date-and-time", "2005-05-31T23:15:15.-08:00", "", pe("2005-05-31T23:15:15.-08:00")},
		{"yang_types.c:124", "date-and-time", "2023-16-15T20:13:01+01:00", "", pe("2023-16-15T20:13:01+01:00")},
		{"yang_types.c:130", "date-and-time", "2023-10-15T20:13:01+95:00", "", pe("2023-10-15T20:13:01+95:00")},
		{"yang_types.c:157", "hex-string", "DB:BA:12:54:fa", "db:ba:12:54:fa", ""},
		{"yang_types.c:158", "uuid", "f81D4fAE-7dec-11d0-A765-00a0c91E6BF6", "f81d4fae-7dec-11d0-a765-00a0c91e6bf6", ""},
		{"hex-pattern", "hex-string", "D", "", `Unsatisfied pattern - "d" does not match "([0-9a-fA-F]{2}(:[0-9a-fA-F]{2})*)?".`},
	}
	withLocal(time.FixedZone("", -2*3600), func() {
		for _, c := range cases {
			v, d := Store(ts[c.typedef], c.lex, FormatXML, HintData, nil, nil)
			switch {
			case c.err != "" && (d == nil || d.Msg != c.err):
				t.Errorf("%s: got %v %q, want error %q", c.src, d, v.Canonical(), c.err)
			case c.err == "" && (d != nil || v.Canonical() != c.canon):
				t.Errorf("%s: got %v %q, want %q", c.src, d, v.Canonical(), c.canon)
			}
		}
		// 2013-07-15: only "-00:00" is an unknown zone and stays so; "Z" is UTC.
		old := ietfTypes("2013-07-15")["date-and-time"]
		if v, _ := Store(old, "2017-02-01T00:00:00-00:00", FormatXML, HintData, nil, nil); v.Canonical() != "2017-02-01T00:00:00-00:00" {
			t.Errorf("old -00:00: %q", v.Canonical())
		}
		if v, _ := Store(old, "2017-02-01T00:00:00Z", FormatXML, HintData, nil, nil); v.Canonical() != "2017-01-31T22:00:00-02:00" {
			t.Errorf("old Z: %q", v.Canonical())
		}
	})
	dt := ts["date-and-time"]
	a, _ := Store(dt, "2005-05-25T23:15:15.5Z", FormatXML, HintData, nil, nil)
	b, _ := Store(dt, "2005-05-25T23:15:15.50Z", FormatXML, HintData, nil, nil)
	c, _ := Store(dt, "2005-05-25T23:15:15Z", FormatXML, HintData, nil, nil)
	if Equal(a, b) || Compare(c, a) >= 0 || Compare(a, b) >= 0 {
		t.Error("date-and-time equality/order")
	}
	// StoreOnly skips the pattern; the parser still rejects gross errors
	if _, d := StoreOnly(dt, "2005-13-25T23:15:15Z", FormatXML, HintData, nil, nil); d == nil || d.Msg != `Invalid date-and-time month "12".` {
		t.Errorf("month: %v", d)
	}
}

// TestOracleGoldensIETF replays the ietf-type fixtures of conformance/corpus/types (schema
// ty3.yang); the oracle runs with the UTC local zone.
func TestOracleGoldensIETF(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	leaves := map[string]string{"ip": "ip-address", "v4": "ipv4-address", "v6": "ipv6-address",
		"v6nz": "ipv6-address-no-zone", "p4": "ipv4-prefix", "p6": "ipv6-prefix", "ipp": "ip-prefix",
		"dt": "date-and-time", "mac": "mac-address", "uuid": "uuid"}
	cases := map[string][][2]string{
		"inet-canonical": {{"ip", "2008:15:0:0:0:0:feAC:1"}, {"v4", "192.168.0.1%12"}, {"v6", "FAAC:21:011:Da85::87:daaF%1"},
			{"v6nz", "::0102:0304"}, {"p4", "12.1.58.4/8"}, {"p6", "::C:D:E:f:a/110"}, {"ipp", "2000:A:B:C:D:E:f:a/16"}},
		"inet-mapped":        {{"v6", "1:0:0:2:0:0:3:4"}, {"v6nz", "::FFFF:1.2.3.4"}},
		"inet-bad-v4":        {{"v4", "192.168.0.333"}},
		"inet-bad-v6":        {{"v6nz", "1:::2"}},
		"dt-known-zone":      {{"dt", "2005-05-25T23:15:15.88888+04:30"}},
		"dt-unknown-zone":    {{"dt", "2021-02-29T00:00:00-00:00"}},
		"dt-pattern":         {{"dt", "2023-16-15T20:13:01+01:00"}},
		"dt-minus-half-hour": {{"dt", "2005-05-25T12:00:00-00:30"}},
		"hex-lowercase":      {{"mac", "DB:BA:12:54:fa:00"}, {"uuid", "f81D4fAE-7dec-11d0-A765-00a0c91E6BF6"}},
	}
	withLocal(time.UTC, func() {
		for id, cs := range cases {
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", id+".json"))
			for _, c := range cs {
				v, d := Store(ts[leaves[c[0]]], c[1], FormatXML, HintData, nil, nil)
				path := "/ty3:c/" + c[0]
				if g.Verdict == "invalid" {
					if want := g.Diagnostics[0]; d == nil || d.Msg != want.Msg || want.DataPath != path {
						t.Errorf("%s %s: got %v, golden %+v", id, c[0], d, want)
					}
					continue
				}
				if want := g.canonical(path); d != nil || v.Canonical() != want {
					t.Errorf("%s %s: got %v %q, golden %q", id, c[0], d, v.Canonical(), want)
				}
			}
		}
	})
}

// FuzzIETF: the ietf-type handlers never panic and their canonical forms re-store unchanged.
func FuzzIETF(f *testing.F) {
	for _, s := range []string{"1.2.3.4%x", "::ffff:1.2.3.4", "1::/0", "10.0.0.1/33", "2005-05-25T23:15:15.5+04:30",
		"2005-02-29T23:15:15-00:00", "AB:cd", "/", "%", "1:2:3:4:5:6:7:8/128"} {
		f.Add(s)
	}
	ts := ietfTypes("2025-12-22")
	names := []string{"ip-address", "ip-address-no-zone", "ip-prefix", "date-and-time", "hex-string", "uuid"}
	f.Fuzz(func(t *testing.T, lex string) {
		for _, n := range names {
			for _, st := range []func(*schema.Type, string, Format, Hints, PrefixCtx, *schema.Node) (Value, *Diag){Store, StoreOnly} {
				v, d := st(ts[n], lex, FormatXML, HintData, nil, nil)
				if d != nil {
					continue
				}
				v2, d := Store(ts[n], v.Canonical(), FormatXML, HintData, nil, nil)
				if d == nil && v2.Canonical() != v.Canonical() {
					t.Fatalf("%s: %q -> %q -> %q", n, lex, v.Canonical(), v2.Canonical())
				}
			}
		}
	})
}

// TestDateAndTimeEdges: UTC output by default, libyang's "-00:30" sign quirk, printf-style years
// and strtol clamping of the zone hour.
func TestDateAndTimeEdges(t *testing.T) {
	dt := ietfTypes("2025-12-22")["date-and-time"]
	cases := []struct{ lex, canon, err string }{
		{"2005-05-25T23:15:15.88888+04:30", "2005-05-25T18:45:15.88888+00:00", ""}, // D-0025
		{"2005-05-25T12:00:00-00:30", "2005-05-25T11:30:00+00:00", ""},             // D-0026: applied as +00:30
		{"2005-05-25T12:00:00-01:30", "2005-05-25T13:30:00+00:00", ""},
		{"0000-01-01T00:00:00Z", "0000-01-01T00:00:00Z", ""},
		{"0000-01-01T00:00:00+01:00", "-001-12-31T23:00:00+00:00", ""},
	}
	for _, c := range cases {
		v, d := Store(dt, c.lex, FormatXML, HintData, nil, nil)
		if d != nil || v.Canonical() != c.canon {
			t.Errorf("%s: %v %q, want %q", c.lex, d, v.Canonical(), c.canon)
		}
	}
	// the pattern rejects these; the parser behind it (StoreOnly) clamps like strtol
	for lex, want := range map[string]string{
		"2005-05-25T12:00:00+99999999999999999999:00": `Invalid date-and-time timezone hour "9223372036854775807".`,
		"2005-05-25T12:00:00-99999999999999999999:00": `Invalid date-and-time timezone hour "-9223372036854775808".`,
		"2005-05-25T12:00:00+01:99999999999999999999": `Invalid date-and-time timezone minutes "9223372036854775807".`,
		"2005-05-25T12:00:00+01":                      `Invalid date-and-time timezone hour "+01".`,
	} {
		if _, d := StoreOnly(dt, lex, FormatXML, HintData, nil, nil); d == nil || d.Msg != want || d.Code != CodeNone {
			t.Errorf("%s: %v, want %q", lex, d, want)
		}
	}
}
