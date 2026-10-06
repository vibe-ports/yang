// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/types/{inet_types,yang_types}.c
// (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
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
	const (
		datePat = `[0-9]{4}-(1[0-2]|0[1-9])-(0[1-9]|[1-2][0-9]|3[0-1])`
		zonePat = `(Z|[\+\-]((1[0-3]|0[0-9]):([0-5][0-9])|14:00))?`
		timePat = `(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]+)?`
	)
	ts["date"] = td(yt, "date", schema.String, pat(datePat+zonePat, false))
	ts["date-no-zone"] = td(yt, "date-no-zone", schema.String, pat(datePat+zonePat, false), pat(datePat, false))
	ts["date-no-zone"].From = ts["date"]
	ts["time"] = td(yt, "time", schema.String, pat(timePat+zonePat, false))
	ts["time-no-zone"] = td(yt, "time-no-zone", schema.String, pat(timePat+zonePat, false), pat(timePat, false))
	ts["time-no-zone"].From = ts["time"]
	ts["xpath1.0"] = td(yt, "xpath1.0", schema.String)
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

// TestOracleGoldensYangTypes replays the conformance fixtures ut-ietf-types/yang_types-* (libyang's
// yang_types.c test_data_xml, schema yt/a.yang, XML input) against their goldens: the canonical
// value of the leaf, or the first diagnostic's message and code. The oracle runs in UTC.
func TestOracleGoldensYangTypes(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	set := &schema.Set{Modules: []*schema.Module{
		{Name: "a", Namespace: "urn:tests:a", Implemented: true},
		{Name: "b", Namespace: "urn:tests:b", Implemented: true},
		{Name: "ietf-yang-library", Namespace: "urn:ietf:params:xml:ns:yang:ietf-yang-library", Implemented: true},
		{Name: "ietf-datastores", Namespace: "urn:ietf:params:xml:ns:yang:ietf-datastores", Implemented: true},
	}}
	leaves := map[string]string{"l": "date-and-time", "l2": "date", "l2nz": "date-no-zone", "l4": "time",
		"l4nz": "time-no-zone", "l21": "hex-string", "l22": "uuid", "l3": "xpath1.0"}
	const yl, ds = "urn:ietf:params:xml:ns:yang:ietf-yang-library", "urn:ietf:params:xml:ns:yang:ietf-datastores"
	cases := []struct {
		n         int
		leaf, lex string
		ns        map[string]string // the declarations besides xmlns="urn:tests:a"
	}{
		{1, "l", "2005-05-25T23:15:15.88888Z", nil}, {2, "l", "2005-05-31T23:15:15-08:59", nil},
		{3, "l", "2005-06-01T11:15:15-11:00", nil}, {4, "l", "1970-01-01T00:59:59-02:00", nil},
		{5, "l", "1969-12-31T23:59:59-02:00", nil}, {6, "l", "2005-02-29T23:15:15-02:00", nil},
		{7, "l", "2005-05-25T23:15:15.88888+04:30", nil}, {8, "l", "2017-02-01T00:00:00-00:00", nil},
		{9, "l", "2021-02-29T00:00:00-00:00", nil}, {10, "l", "2005-05-31T23:15:15.-08:00", nil},
		{11, "l", "2023-16-15T20:13:01+01:00", nil}, {12, "l", "2023-10-15T20:13:01+95:00", nil},
		{13, "l2", "2005-05-31-01:00", nil}, {14, "l2nz", "2005-05-31", nil},
		{15, "l4", "23:15:15-01:00", nil}, {16, "l4", "00:59:59.001-02:00", nil},
		{17, "l4nz", "23:15:15", nil}, {18, "l4nz", "00:59:59.100", nil},
		{19, "l21", "DB:BA:12:54:fa", nil}, {20, "l22", "f81D4fAE-7dec-11d0-A765-00a0c91E6BF6", nil},
		{21, "l3", "/aa:l3[. = '4']", map[string]string{"aa": "urn:tests:a"}},
		{22, "l3", "/yl:yang-library/yl:datastore/yl:name = 'ds:running'", map[string]string{"yl": yl, "ds": ds}},
		{23, "l3", "/a1:node1/a2:node2[a1:node3/bb:node4]/bb:node5 | bb:node6 and (bb:node7)",
			map[string]string{"a1": "urn:tests:a", "a2": "urn:tests:a", "bb": "urn:tests:b"}},
		{24, "l3", "/l3[. = '4']", nil}, {25, "l3", "/a:l3[. = '4']", nil},
		{26, "l3", "/yl:yang-library/yl:datastore/yl::name", map[string]string{"yl": yl}},
		{27, "l2", "1950-01-01-02:00", nil}, {28, "l2nz", "1950-01-01", nil},
	}
	withLocal(time.UTC, func() {
		for _, c := range cases {
			id := fmt.Sprintf("yang_types-test_data_xml-%02d", c.n)
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "ut-ietf-types", "golden", id+".json"))
			ns := map[string]string{"": "urn:tests:a"}
			maps.Copy(ns, c.ns)
			v, d := Store(ts[leaves[c.leaf]], c.lex, FormatXML, HintData, XMLNamespaces{Set: set, NS: ns}, nil)
			if g.Verdict == "invalid" {
				if want := g.Diagnostics[0]; d == nil || d.Msg != want.Msg || d.Code != want.VecodeName {
					t.Errorf("%s: got %+v, golden %+v", id, d, want)
				}
				continue
			}
			if want := g.canonical("/a:" + c.leaf); d != nil || v.Canonical() != want {
				t.Errorf("%s: got %v %q, golden %q", id, d, v.Canonical(), want)
			}
		}
	})
}

// TestDateTimeTypes: the date and time typedefs beyond the fixtures: the libyang C tests' zone
// (UTC-2), the time-of-day wrap of a time before the epoch day (libyang's uint32_t seconds), the
// UTC forms of unknown zones and of the no-zone types, the canonical-format bypass, equality and
// order, and the strptime refusals of the no-zone types under StoreOnly.
func TestDateTimeTypes(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	cases := []struct{ typedef, lex, canon string }{
		{"date", "2005-05-31-01:00", "2005-05-30-02:00"},
		{"date", "2005-05-31Z", "2005-05-31Z"},
		{"date", "2005-05-31+14:00", "2005-05-30-02:00"},
		{"time", "23:15:15-01:00", "22:15:15-02:00"},
		{"time", "00:30:00+01:00", "03:58:16-02:00"}, // -1800 s as uint32_t: 2106-02-07T05:58:16Z
		{"time", "12:00:00.50Z", "12:00:00.50Z"},
		{"time-no-zone", "12:00:00", "12:00:00"},
		{"date-no-zone", "2005-05-31", "2005-05-31"},
	}
	withLocal(time.FixedZone("", -2*3600), func() {
		for _, c := range cases {
			if v, d := Store(ts[c.typedef], c.lex, FormatXML, HintData, nil, nil); d != nil || v.Canonical() != c.canon {
				t.Errorf("%s %q: %v %q, want %q", c.typedef, c.lex, d, v.Canonical(), c.canon)
			}
		}
	})
	if v, d := Store(ts["date"], "2005-05-31-01:00", FormatCanon, HintData, nil, nil); d != nil || v.Canonical() != "2005-05-31-01:00" {
		t.Errorf("canonical format: %v %q", d, v.Canonical())
	}
	st := func(typedef, lex string) Value {
		v, d := Store(ts[typedef], lex, FormatXML, HintData, nil, nil)
		if d != nil {
			t.Fatalf("%s %q: %v", typedef, lex, d)
		}
		return v
	}
	if !Equal(st("date", "2005-05-31+00:00"), st("date", "2005-05-31-00:00")) || Equal(st("date", "2005-05-31Z"), st("date", "2005-05-31+00:00")) ||
		Compare(st("date", "2005-05-30Z"), st("date", "2005-05-31Z")) >= 0 {
		t.Error("date equality/order")
	}
	if Equal(st("time", "12:00:00.5Z"), st("time", "12:00:00.50Z")) || !Equal(st("time-no-zone", "12:00:00"), st("time-no-zone", "12:00:00")) ||
		Compare(st("time-no-zone", "12:00:00"), st("time-no-zone", "12:00:00.1")) >= 0 ||
		Compare(st("time-no-zone", "12:00:01"), st("time-no-zone", "12:00:00.9")) <= 0 {
		t.Error("time equality/order")
	}
	for typedef, lex := range map[string]string{"date-no-zone": "2005-13-31", "time-no-zone": "24:00:00"} {
		want := fmt.Sprintf("Failed to parse %s value \"%s\".", typedef, lex)
		if _, d := StoreOnly(ts[typedef], lex, FormatXML, HintData, nil, nil); d == nil || d.Msg != want || d.Code != CodeData {
			t.Errorf("%s %q: %v, want %q", typedef, lex, d, want)
		}
	}
}

// TestXPath10: the canonical forms of xpath1.0 by format (JSON and canonical input kept as given),
// literals with an unknown prefix copied, and ly_value_prefix_next's chunks.
func TestXPath10(t *testing.T) {
	xp := ietfTypes("2025-12-22")["xpath1.0"]
	a := &schema.Module{Name: "a", Namespace: "urn:a", Implemented: true}
	pc := XMLNamespaces{Set: &schema.Set{Modules: []*schema.Module{a}}, NS: map[string]string{"x": "urn:a"}}
	for _, c := range []struct {
		f         Format
		lex, want string
	}{
		{FormatXML, "/x:c/x:d = 'q:z'  or  count(x:e) > 1", "/a:c/d='q:z' or count(a:e)>1"}, // ">" is not spaced (Operator(Comparison)),
		{FormatXML, "x:c[x:k = 'x:v']/x:d + 2", "a:c[k='a:v']/d + 2"},
		{FormatJSON, "/a:c / a:d", "/a:c / a:d"},
		{FormatCanon, "/a:c  /  a:d", "/a:c  /  a:d"},
	} {
		if v, d := Store(xp, c.lex, c.f, HintData, pc, nil); d != nil || v.Canonical() != c.want {
			t.Errorf("%q: %v %q, want %q", c.lex, d, v.Canonical(), c.want)
		}
	}
	for s, want := range map[string][]string{
		"a:b":      {"a", ":", "b"},
		"'q:z'":    {"'", "q", ":", "z'"},
		"ab":       {"ab"},
		"x:":       {"x", ":"},
		"1 c:d e:": {"1 ", "c", ":", "d ", "e", ":"},
	} {
		var got []string
		for rest := s; rest != ""; {
			n, isPrefix, next := valuePrefixNext(rest)
			if n == 0 {
				break
			}
			got = append(got, rest[:n])
			if isPrefix {
				got = append(got, ":")
			}
			rest = next
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", s, got, want)
		}
	}
}

// FuzzIETF: the ietf-type handlers never panic and their canonical forms re-store unchanged.
func FuzzIETF(f *testing.F) {
	for _, s := range []string{"1.2.3.4%x", "::ffff:1.2.3.4", "1::/0", "10.0.0.1/33", "2005-05-25T23:15:15.5+04:30",
		"2005-02-29T23:15:15-00:00", "AB:cd", "/", "%", "1:2:3:4:5:6:7:8/128", "2005-05-31-01:00", "00:30:00+01:00",
		"23:15:15.5", "/a:b[. = 1] | c"} {
		f.Add(s)
	}
	ts := ietfTypes("2025-12-22")
	names := []string{"ip-address", "ip-address-no-zone", "ip-prefix", "date-and-time", "hex-string", "uuid", "date",
		"date-no-zone", "time", "time-no-zone", "xpath1.0"}
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
