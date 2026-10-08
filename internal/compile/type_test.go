// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/schema/test_tree_schema_compile.c
// (refcount assertions of test_type_range, test_type_length) (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func compileOne(t *testing.T, src string) ([]compiledLeaf, *harness, error) {
	t.Helper()
	h := newHarness(t)
	pm := h.add(src)
	ls, err := h.compile(pm.Name)
	return ls, h, err
}

// TestTypeRefcount checks the holder counts libyang's test_type_range/test_type_length assert.
func TestTypeRefcount(t *testing.T) {
	ls, h, err := compileOne(t, `module i {namespace urn:i;prefix i;typedef mytype {type uint8 {range 10..100;}}
		typedef mytype2 {type mytype;} leaf l {type mytype2;}}`)
	if err != nil {
		t.Fatal(err)
	}
	if typ := ls[0].node.Type; h.cache.refs[typ] != 3 || typ.Typedef != "mytype" {
		t.Fatalf("refcount %d typedef %q, want 3 mytype", h.cache.refs[typ], typ.Typedef)
	}
	ls, h, err = compileOne(t, `module j {namespace urn:j;prefix j;
		typedef mytype {type uint8 {range 1..100{description "one to hundred";reference A;}}}
		leaf l {type mytype {range 1..10 {description "one to ten";reference B;}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if typ := ls[0].node.Type; h.cache.refs[typ] != 1 || typ.Range.Parts[0].MinU != 1 || typ.Range.Parts[0].MaxU != 10 {
		t.Fatalf("refcount %d range %+v", h.cache.refs[typ], typ.Range)
	}
	ls, h, err = compileOne(t, `module k {namespace urn:k;prefix k;typedef mytype {type binary {length 10..100;}}
		leaf l {type mytype {length "10..80";}}leaf ll {type mytype;}}`)
	if err != nil {
		t.Fatal(err)
	}
	if l, ll := ls[0].node.Type, ls[1].node.Type; h.cache.refs[l] != 1 || h.cache.refs[ll] != 2 || l.From == ll { // ll recompiled mytype: only the cache held it
		t.Fatalf("refcounts %d %d", h.cache.refs[l], h.cache.refs[ll])
	}
}

// TestTypedefPluginReuse: an unchanged typedef is merged into its base only when it has no
// type handler record of its own (the plugin clause of SCN:2084). Records are per typedef name
// (lyplg_type_record), also where names share one handler: ipv4-address and
// ipv4-address-link-local, hex-string and mac-address.
func TestTypedefPluginReuse(t *testing.T) {
	h := newHarness(t)
	h.add(`module ietf-inet-types {namespace urn:ietf:params:xml:ns:yang:ietf-inet-types;prefix inet;
		typedef ipv4-address {type string {pattern '[0-9.]*';}}
		typedef ipv4-address-link-local {type ipv4-address;}
		typedef ipv4-address-no-zone {type ipv4-address;}}`)
	h.add(`module ietf-yang-types {namespace urn:ietf:params:xml:ns:yang:ietf-yang-types;prefix yang;
		typedef hex-string {type string {pattern '[0-9a-fA-F:]*';}}
		typedef mac-address {type hex-string;}}`)
	h.add(`module m {namespace urn:m;prefix m;import ietf-inet-types {prefix inet;}
		import ietf-yang-types {prefix yang;}
		typedef my-addr {type inet:ipv4-address;}
		leaf a {type inet:ipv4-address-link-local;}
		leaf b {type inet:ipv4-address-no-zone;}
		leaf c {type my-addr;}
		leaf d {type yang:mac-address;}}`)
	ls, err := h.compile("m")
	if err != nil {
		t.Fatal(err)
	}
	a, b, c, d := ls[0].node.Type, ls[1].node.Type, ls[2].node.Type, ls[3].node.Type
	if c.Typedef != "ipv4-address" {
		t.Errorf("my-addr: %q, want the reused ipv4-address type", c.Typedef)
	}
	for _, x := range []struct {
		typ          *schema.Type
		module, name string
	}{{a, "ietf-inet-types", "ipv4-address-link-local"}, {b, "ietf-inet-types", "ipv4-address-no-zone"},
		{d, "ietf-yang-types", "mac-address"}} {
		if x.typ.Typedef != x.name || x.typ.From == nil || types.Plugin(x.typ) != types.TypedefPlugin(x.module, "", x.name) {
			t.Errorf("%s: compiled as %q, want its own type", x.name, x.typ.Typedef)
		}
	}
	if a.From.Typedef != "ipv4-address" || b.From.Typedef != "ipv4-address" || d.From.Typedef != "hex-string" ||
		types.Plugin(a) == types.Plugin(a.From) || types.Plugin(d) == types.Plugin(d.From) {
		t.Errorf("link-local, no-zone, mac-address must derive from their bases with their own records")
	}
}

// TestLeafrefTypedefSharing pins design 06 §2.3 "Leafref typedef consequences" at the type level.
func TestLeafrefTypedefSharing(t *testing.T) {
	h := newHarness(t)
	h.add(`module lt {yang-version 1.1;namespace urn:lt;prefix lt;
		typedef ref {type leafref {path "../x";}}
		typedef u {type union {type leafref {path "../x";} type boolean;}}
		typedef d {type u;}}`)
	for _, n := range []string{"a", "b"} {
		h.add(fmt.Sprintf(`module %s {yang-version 1.1;namespace urn:%[1]s;prefix %[1]s;import lt {prefix lt;}
			leaf x {type string;} leaf r {type lt:ref;} leaf ru {type lt:u;} leaf rd {type lt:d;}}`, n))
	}
	la, err := h.compile("a")
	if err != nil {
		t.Fatal(err)
	}
	lb, err := h.compile("b")
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := leafAt(t, la, "/a:r").node.Type, leafAt(t, lb, "/b:r").node.Type
	if ra.Prefixes[""].Name != "a" || rb.Prefixes[""].Name != "b" {
		t.Errorf("direct leafref typedef: bound to %s, %s", ra.Prefixes[""].Name, rb.Prefixes[""].Name)
	}
	// u is held by d's reuse after a's compile: from then on every user shares its members
	ua, ub := leafAt(t, la, "/a:ru").node.Type, leafAt(t, lb, "/b:ru").node.Type
	da, db := leafAt(t, la, "/a:rd").node.Type, leafAt(t, lb, "/b:rd").node.Type
	if ua.Union[0] == ub.Union[0] || ua.Union[0].Prefixes[""].Name != "a" {
		t.Errorf("union typedef used directly before any derived use must not be shared")
	}
	if da.Union[0] != db.Union[0] || db.Union[0].Prefixes[""].Name != "a" || db.Union[0] != ub.Union[0] {
		t.Errorf("via derived typedef: members %p %p %p, binding %s", da.Union[0], db.Union[0], ub.Union[0],
			db.Union[0].Prefixes[""].Name)
	}
	if da == db || da.Typedef != "d" {
		t.Errorf("each leaf has its own union type named d: %p %p %q", da, db, da.Typedef)
	}
}

// TestTypedefCacheRecompile: a cached type held only by the cache is compiled anew at the next
// use; one with another holder is reused (SCN:1986).
func TestTypedefCacheRecompile(t *testing.T) {
	h := newHarness(t)
	h.add(`module m {namespace urn:m;prefix m;typedef p {type uint8 {range 1..2;}} leaf a {type p;} leaf b {type p;}}`)
	ls, err := h.compile("m")
	if err != nil {
		t.Fatal(err)
	}
	a := ls[0].node.Type
	if ls[1].node.Type != a || h.cache.refs[a] != 3 {
		t.Fatalf("leaves share the cached type: %d holders", h.cache.refs[a])
	}
	snap := h.cache.clone()
	h.cache.release(a) // the module is dropped at recompilation: both leaves release
	h.cache.release(a)
	ls, err = h.compile("m")
	if err != nil {
		t.Fatal(err)
	}
	if ls[0].node.Type == a || h.cache.refs[a] != 0 {
		t.Errorf("type held only by the cache must be recompiled")
	}
	if snap.refs[a] != 3 {
		t.Errorf("clone shares state: %d", snap.refs[a])
	}
}

// TestUnionBudget: union flattening doubles per typedef; the chain fails with ErrBudget fast.
func TestUnionBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("module u {namespace urn:u;prefix u;typedef u0 {type union {type int8; type string;}}\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "typedef u%d {type union {type u%d; type u%[2]d;}}\n", i, i-1)
	}
	b.WriteString("leaf l {type u30;}}")
	// the work is linear in MaxTypes: the defaults stop it after at most 1<<20 units (~0.3 s,
	// several s under -race on CI); a 1<<16 budget stops it well within a second
	h := newHarness(t)
	h.add(b.String())
	if _, err := h.compile("u"); !errors.Is(err, ErrBudget) || h.last.types > DefaultMaxTypes {
		t.Fatalf("defaults: got %v after %d types, want ErrBudget", err, h.last.types)
	}
	h = newHarness(t)
	h.budget = Budget{MaxTypes: 1 << 16}
	h.add(b.String())
	start := time.Now()
	if _, err := h.compile("u"); !errors.Is(err, ErrBudget) {
		t.Fatalf("got %v, want ErrBudget", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	// below the budget the same shape compiles, flattened
	ls, _, err := compileOne(t, strings.Replace(b.String(), "leaf l {type u30;}", "leaf l {type u3;}", 1))
	if err != nil || len(ls[0].node.Type.Union) != 16 {
		t.Fatalf("u3: %v", err)
	}
	// MaxUnionMembers caps one flattened union: u2 has 8 members, u3 16
	for leaf, want := range map[string]string{"u2": "", "u3": "more than 8 union members"} {
		h := newHarness(t)
		h.budget = Budget{MaxUnionMembers: 8}
		h.add(strings.Replace(b.String(), "leaf l {type u30;}", "leaf l {type "+leaf+";}", 1))
		if _, err := h.compile("u"); want == "" && err != nil || want != "" && (!errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s with MaxUnionMembers 8: %v", leaf, err)
		}
	}
	// MaxTypes counts every compiled type and member slot of the load
	h = newHarness(t)
	h.budget = Budget{MaxTypes: 20}
	h.add(strings.Replace(b.String(), "leaf l {type u30;}", "leaf l {type u3;}", 1))
	if _, err := h.compile("u"); !errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "compiled types") {
		t.Fatalf("MaxTypes 20: got %v", err)
	}
}

// TestEnumIfFeatureWritingModule: enum/bit if-features resolve in the module that writes the
// items (lysp_qname.mod), not in the module being compiled — own items of a typedef, and items
// re-read from the deepest typedef by a new type of another module.
func TestEnumIfFeatureWritingModule(t *testing.T) {
	h := newHarness(t)
	a := h.add(`module a {yang-version 1.1;namespace urn:a;prefix a;feature f;
		typedef e {type enumeration {enum x {if-feature f;} enum y;}}
		typedef b {type bits {bit p {if-feature f;} bit q;}}}`)
	h.add(`module b {yang-version 1.1;namespace urn:b;prefix b;import a {prefix a;}extension ext;
		leaf l {type a:e;}
		leaf m {type a:e {b:ext;}}
		leaf n {type a:b {b:ext;}}}`)
	a.mod.Feature("f").Enabled = true
	ls, err := h.compile("b")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		typ := l.node.Type
		if len(typ.Enums) == 2 && !typ.Enums[0].Disabled || len(typ.Bits) == 2 && !typ.Bits[0].Disabled {
			continue
		}
		t.Errorf("%s: items %+v %+v, want the f-guarded item enabled", l.path, typ.Enums, typ.Bits)
	}
}

// TestFindTypeSubmodule: lysp_type_find through the loader's Module/Submodule/Include records —
// from the main module into an included submodule and from the submodule back to the main.
func TestFindTypeSubmodule(t *testing.T) {
	h := newHarness(t)
	m := h.add(`module m {namespace urn:m;prefix m;include s;typedef tm {type uint8;}}`)
	st, err := parser.Parse("s.yang", []byte(`submodule s {belongs-to m {prefix m;}typedef ts {type string;}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := parser.Build(st)
	if err != nil {
		t.Fatal(err)
	}
	sub := &Submodule{pmod: pmod{Parsed: sp, mod: m.mod}, Name: "s", Main: m}
	m.Includes = []*Include{{Name: "s", Sub: sub}}
	c := &typeCtx{cur: m.mod, pmod: &m.pmod, parsed: h.parsed}
	for _, x := range []struct {
		from     *pmod
		id       string
		inModule *pmod
	}{{&m.pmod, "ts", &sub.pmod}, {&m.pmod, "m:ts", &sub.pmod}, {&sub.pmod, "tm", &m.pmod}, {&sub.pmod, "m:tm", &m.pmod}} {
		if _, it, ok := c.findType(x.id, nil, x.from); !ok || it == nil || it.pm != x.inModule {
			t.Errorf("%s from %s: found %v", x.id, x.from.Parsed.Name, ok)
		}
	}
}

// TestChainResetOnError: a failed type leaves no typedef of its chain in the context's chain.
func TestChainResetOnError(t *testing.T) {
	h := newHarness(t)
	h.add(`module m {namespace urn:m;prefix m;typedef t {type string {range 1..2;}} leaf l {type t;}}`)
	if _, err := h.compile("m"); err == nil || len(h.last.chain) != 0 {
		t.Fatalf("err %v, chain %d", err, len(h.last.chain))
	}
}

// TestDefaultOverDiscardedChain is D-0040: libyang 5.8.6 crashes on this input (the leaf with
// units walks past a's cached type into b, whose cache-only type it discards, then reads the
// discarded type when c supplies the default). Ours keeps walking: both leaves get a's type and
// c's default.
func TestDefaultOverDiscardedChain(t *testing.T) {
	ls, h, err := compileOne(t, `module m {namespace urn:m;prefix m;
		typedef c {type string; default "x";}
		typedef b {type c {length "1..5";}}
		typedef a {type b {length "1..3";}}
		leaf l1 {type a;}
		leaf l2 {type a; units "s";}}`)
	if err != nil {
		t.Fatal(err)
	}
	l1, l2 := ls[0], ls[1]
	if l2.node.Type != l1.node.Type || l2.node.Type.Typedef != "a" || l2.dflt == nil || l2.dflt.lex != "x" ||
		h.cache.refs[l1.node.Type] != 3 {
		t.Fatalf("l2: type %p (l1 %p) default %+v", l2.node.Type, l1.node.Type, l2.dflt)
	}
	if p := l2.node.Type.Length.Parts[0]; p.MinU != 1 || p.MaxU != 3 {
		t.Errorf("length %+v", p)
	}
}

func TestBitPositions(t *testing.T) {
	if _, _, err := compileOne(t, "module b {namespace urn:b;prefix b;leaf l {type bits {bit a {position 4294967295;}}}}"); err != nil {
		t.Fatal(err)
	}
	ls, _, err := compileOne(t, "module b {namespace urn:b;prefix b;leaf l {type bits {bit a {position 9;} bit b {position 2;} bit c;}}}")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, bit := range ls[0].node.Type.Bits {
		got = append(got, fmt.Sprint(bit.Name, bit.Position))
	}
	if strings.Join(got, " ") != "b2 a9 c10" {
		t.Errorf("bits %v", got)
	}
}

func TestEnumIfFeature(t *testing.T) {
	h := newHarness(t)
	pm := h.add(`module e {yang-version 1.1;namespace urn:e;prefix e;feature f;
		leaf l {type enumeration {enum a {if-feature f;} enum b;}}}`)
	ls, err := h.compile("e")
	if err != nil {
		t.Fatal(err)
	}
	if es := ls[0].node.Type.Enums; !es[0].Disabled || es[1].Disabled || pm.mod.Feature("f").Enabled {
		t.Errorf("enum a must be disabled")
	}
}

// TestInheritedItemsWithoutChain is D-0038: libyang dereferences NULL here.
func TestInheritedItemsWithoutChain(t *testing.T) {
	_, _, err := compileOne(t, `module m {namespace urn:m;prefix m;extension e;
		typedef e1 {type enumeration {enum a;}}
		typedef e2 {type e1; default a;}
		leaf x {type e2;}
		leaf y {type e2 {m:e;} units u;}}`)
	var ve *vErr
	if !errors.As(err, &ve) || ve.Msg != "Internal error: no enumeration items to inherit." {
		t.Fatalf("got %v", err)
	}
}

func TestValuePrefixes(t *testing.T) {
	got := valuePrefixes("/a:x[b:k = current()/../c:y]/1d:z/é:w")
	if strings.Join(got, ",") != "a,b,c,d,é" {
		t.Errorf("%v", got)
	}
}
