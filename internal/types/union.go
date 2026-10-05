// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/union.c (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// UnionValue is what a union value keeps besides its selected member (libyang struct
// lyd_value_union): the original lexical form with its format, hints and prefix context, so
// the member can be chosen again at validation time (a leafref member depends on data).
type UnionValue struct {
	member Value
	index  int
	orig   string
	f      Format
	h      Hints
	pc     PrefixCtx
	ctx    *schema.Node
}

// Member returns the selected member's value and its index in the union's member types.
func (u *UnionValue) Member() (Value, int) { return u.member, u.index }

// Original returns the lexical form the value was stored from and its format.
func (u *UnionValue) Original() (string, Format) { return u.orig, u.f }

// storeUnion ports lyplg_type_store_union: the first member that stores and validates the value
// wins; with StoreOnly, when none validates, the first that only stores.
func storeUnion(a *storeArgs) (Value, *Diag) {
	u := &UnionValue{orig: a.lex, f: a.f, h: a.h, pc: a.pc, ctx: a.ctx}
	d := unionFind(a.t, u, false, nil)
	if d != nil && a.only {
		d = unionFind(a.t, u, true, nil)
	}
	if d != nil {
		return Value{}, d
	}
	return Value{typ: a.t, canon: u.member.canon, union: u, needsTree: u.member.needsTree}, nil
}

// unionFind ports union_find_type; tree non-nil also runs tree validation of each member.
func unionFind(t *schema.Type, u *UnionValue, only bool, tree Tree) *Diag {
	errs := make([]*Diag, len(t.Union))
	for i, mt := range t.Union {
		v, d := unionStoreType(mt, u, only, tree)
		if d == nil {
			u.member, u.index = v, i
			return nil
		}
		errs[i] = d
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Invalid union value \"%s\" - no matching subtype found:\n", u.orig)
	appTag, useAppTag := "", false
	for i, d := range errs {
		if d.AppTag != "" {
			if appTag == "" {
				appTag, useAppTag = d.AppTag, true
			} else if d.AppTag != appTag {
				useAppTag = false
			}
		}
		fmt.Fprintf(&b, "    %s: %s\n", pluginID(t.Union[i]), d.Msg)
	}
	if !useAppTag {
		appTag = ""
	}
	return &Diag{Code: CodeData, Msg: b.String(), AppTag: appTag}
}

// unionStoreType ports union_store_type (+ union_update_lref_err).
func unionStoreType(mt *schema.Type, u *UnionValue, only bool, tree Tree) (Value, *Diag) {
	a := &storeArgs{t: mt, lex: u.orig, f: u.f, h: u.h, pc: u.pc, ctx: u.ctx, only: only, quiet: true}
	v, d := storeArgsDispatch(a)
	if d != nil {
		if mt.Base == schema.Leafref { // report the reference, not the target type's complaint
			d = &Diag{Code: CodeData, AppTag: "instance-required", Msg: noLeafrefMsg(u.orig, mt.Path)}
		}
		return Value{}, d
	}
	if tree != nil {
		if v, d = ValidateTree(mt, v, tree); d != nil {
			return Value{}, d
		}
	}
	return v, nil
}

// pluginID is the libyang type plugin id quoted in union errors.
func pluginID(t *schema.Type) string {
	if p := pluginFor(t); p != nil {
		return "ly2 " + p.id
	}
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return "ly2 integers"
	}
	return "ly2 " + t.Base.String()
}

// compareUnion ports lyplg_type_sort_union: same member type → that type's order, else the
// order of the member types in the union (as libyang: the value whose type comes first is
// greater).
func compareUnion(a, b Value) int {
	ma, mb := a.union.member, b.union.member
	if ma.typ == mb.typ {
		return Compare(ma, mb)
	}
	for _, mt := range a.typ.Union {
		if mt.Base == schema.Leafref {
			mt = mt.Realtype
		}
		switch mt {
		case ma.typ:
			return 1
		case mb.typ:
			return -1
		}
	}
	return 0
}
