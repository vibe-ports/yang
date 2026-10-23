// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// The callback must stop an expanding xpath1.0 token before strings.Builder receives the long
// XML prefix. Charging only canonical length would let all repeated expansions allocate first.
func TestPrintXPath10ChargesExpandedPrefixBeforeAppend(t *testing.T) {
	mod := &schema.Module{Name: "m", Prefix: strings.Repeat("p", 64<<10)}
	parts := make([]string, 256)
	for i := range parts {
		parts[i] = "m:x"
	}
	src := strings.Join(parts, " | ")
	e, msg := lyxp.Lex(src)
	if msg != "" {
		t.Fatal(msg)
	}
	v := Value{typ: &schema.Type{Base: schema.String}, canon: src,
		ext: &keysValue{e: e, f: FormatJSON, pc: prefixSnap{"m": mod}}}
	stop := errors.New("stop conversion")
	calls := 0
	_, err := Print(v, FormatXML, &PrintCtx{}, func() error {
		calls++
		if calls == 4 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || calls != 4 {
		t.Fatalf("Print error=%v calls=%d; want callback stop on fourth 64-byte unit", err, calls)
	}
}

func TestPrintTypeFamiliesUseConversionBudget(t *testing.T) {
	canonical := strings.Repeat("x", 65)
	inetModule := &schema.Module{Name: "ietf-inet-types"}
	yangModule := &schema.Module{Name: "ietf-yang-types"}
	simple := []struct {
		name string
		v    Value
	}{
		{"bits", Value{typ: &schema.Type{Base: schema.Bits}, canon: canonical}},
		{"binary", Value{typ: &schema.Type{Base: schema.Binary}, canon: canonical}},
		{"inet", Value{typ: &schema.Type{Base: schema.String, Typedef: "ipv6-address",
			TypedefModule: inetModule}, canon: canonical, ext: &ipValue{}}},
		{"date", Value{typ: &schema.Type{Base: schema.String, Typedef: "date",
			TypedefModule: yangModule}, canon: canonical, ext: dateVal{}}},
	}
	member := simple[0].v
	simple = append(simple, struct {
		name string
		v    Value
	}{"union", Value{typ: &schema.Type{Base: schema.Union}, union: &UnionValue{member: member}}})
	for _, tc := range simple {
		t.Run(tc.name, func(t *testing.T) {
			stop := errors.New("stop conversion")
			calls := 0
			_, err := Print(tc.v, FormatXML, nil, func() error {
				calls++
				if calls == 2 {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) || calls != 2 {
				t.Fatalf("Print error=%v calls=%d; want second-unit stop", err, calls)
			}
		})
	}

	longPrefix := strings.Repeat("p", 128)
	mod := &schema.Module{Name: "m", Prefix: longPrefix}
	identity := Value{typ: &schema.Type{Base: schema.IdentityRef}, canon: "m:id",
		ident: &schema.Identity{Name: "id", Module: mod}}
	node := &schema.Node{Name: "n", Module: mod}
	instanceID := Value{typ: &schema.Type{Base: schema.InstanceID}, canon: "/m:n",
		path: Path{{Node: node}}}
	for _, tc := range []struct {
		name string
		v    Value
	}{{"identityref", identity}, {"instance-identifier", instanceID}} {
		t.Run(tc.name, func(t *testing.T) {
			stop := errors.New("stop expansion")
			calls := 0
			_, err := Print(tc.v, FormatXML, &PrintCtx{}, func() error {
				calls++
				if calls == 2 {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) || calls != 2 {
				t.Fatalf("Print error=%v calls=%d; want expanded-prefix stop", err, calls)
			}
		})
	}
}
