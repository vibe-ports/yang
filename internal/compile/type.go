// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_node.c (lys_compile_type, lys_compile_type_,
// lys_compile_type_union, lys_compile_type_enums, lys_compile_type_patterns,
// lys_compile_node_type), src/schema_compile.c (lys_compile_identity_bases) and
// src/tree_data_common.c (ly_store_prefix_data for LY_VALUE_SCHEMA) (BSD-3-Clause, © CESNET).

package compile

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Restriction substatement flags of a parsed type (libyang LYS_SET_*).
const (
	setBase uint16 = 1 << iota
	setBit
	setEnum
	setFrDigits
	setLength
	setPath
	setPattern
	setRange
	setType
	setReqInst
)

// typeFlags are the LYS_SET_* flags the parser records for t's restrictions.
func typeFlags(t *parser.Type) uint16 {
	var f uint16
	if len(t.Bases) > 0 {
		f |= setBase
	}
	if len(t.Bits) > 0 {
		f |= setBit
	}
	if len(t.Enums) > 0 {
		f |= setEnum
	}
	if t.FractionDigits != 0 {
		f |= setFrDigits
	}
	if t.Length != nil {
		f |= setLength
	}
	if t.Path != "" {
		f |= setPath
	}
	if len(t.Patterns) > 0 {
		f |= setPattern
	}
	if t.Range != nil {
		f |= setRange
	}
	if len(t.Types) > 0 {
		f |= setType
	}
	if t.RequireInstance != nil {
		f |= setReqInst
	}
	return f
}

// substmtMap is libyang's type_substmt_map: the restrictions each built-in type allows.
var substmtMap = [...]uint16{
	schema.Binary: setLength, schema.Uint8: setRange, schema.Uint16: setRange, schema.Uint32: setRange,
	schema.Uint64: setRange, schema.String: setLength | setPattern, schema.Bits: setBit,
	schema.Dec64: setFrDigits | setRange, schema.Enumeration: setEnum, schema.IdentityRef: setBase,
	schema.InstanceID: setReqInst, schema.Leafref: setReqInst | setPath, schema.Union: setType,
	schema.Int8: setRange, schema.Int16: setRange, schema.Int32: setRange, schema.Int64: setRange,
}

// typeStr is libyang's ly_data_type2str, used in messages.
var typeStr = [...]string{"unknown", "binary", "8bit unsigned integer", "16bit unsigned integer",
	"32bit unsigned integer", "64bit unsigned integer", "string", "bits", "boolean", "decimal64", "empty",
	"enumeration", "identityref", "instance-identifier", "leafref", "union", "8bit integer", "16bit integer",
	"32bit integer", "64bit integer"}

// typeDefault is the default inherited from the nearest typedef that has one: its text and the
// (sub)module it is written in.
type typeDefault struct {
	lex string
	pm  *pmod
}

// compileNodeType ports lys_compile_node_type without the unres bookkeeping (leafref, default and
// disabled bit/enum sets are the node walk's): compile the type of a leaf or leaf-list and hold it.
// units reports an inherited units statement when wantUnits (the node has none of its own); dflt
// the typedef default unless the node has its own.
func (c *typeCtx) compileNodeType(sc *scope, n *schema.Node, tp *parser.Type, pm *pmod, wantUnits bool) (
	t *schema.Type, units *string, dflt *typeDefault, err error) {
	t, units, dflt, err = c.compileType(sc, n.Status, n.Name, tp, pm, wantUnits, true)
	if err != nil {
		return nil, nil, nil, err
	}
	c.cache.hold(t)
	members := []*schema.Type{t}
	if t.Base == schema.Union {
		members = t.Union
	}
	for _, m := range members {
		if m.Base == schema.Empty && n.Kind == schema.LeafList && !c.pmod.v11() {
			c.cache.release(t) // libyang frees the failed node, and with it this holder
			return nil, nil, nil, verr(ly.Semantics, "Leaf-list of type \"empty\" is allowed only in YANG 1.1 modules.")
		}
	}
	return t, units, dflt, nil
}

// compileType ports lys_compile_type: resolve the typedef chain of tp, compile every typedef not
// cached yet from the built-in outwards, then the type itself. st and name are the status and
// name of the referring node or typedef (status checks); sc and pm are where tp is written.
func (c *typeCtx) compileType(sc *scope, st schema.Status, name string, tp *parser.Type, pm *pmod,
	wantUnits, wantDflt bool) (t *schema.Type, units *string, dflt *typeDefault, err error) {
	var chain []*tpdfItem
	var prev *tpdfItem
	var dfltItem *tpdfItem
	dummy := false
	basetype := schema.Unknown
	id, start, spm := tp.Name, sc, pm
	for {
		bt, it, ok := c.findType(id, start, spm)
		basetype = bt
		if !ok || bt != schema.Unknown {
			break
		}
		if err := checkStatus(st, pm, name, parsedStatus(it.tpdf.Status), it.pm, it.name2()); err != nil {
			return nil, nil, nil, err
		}
		if wantUnits && units == nil && it.tpdf.Units != nil {
			units = it.tpdf.Units
		}
		if wantDflt && dfltItem == nil && len(it.tpdf.Defaults) > 0 {
			dfltItem = it
		}
		if dummy && (!wantUnits || units != nil) && wantDflt && dfltItem != nil {
			// libyang reads the last chain item's compiled type; one discarded on the previous
			// hop would be a NULL dereference there, so that case keeps walking instead
			if ct := c.cache.compiled[chain[len(chain)-1].tpdf]; ct != nil {
				basetype = ct.Base
				break
			}
		}
		if ct := c.cache.compiled[it.tpdf]; ct != nil && c.cache.refs[ct] == 1 {
			// held by the cache only: discard and recompile in the current context
			c.cache.release(ct)
			delete(c.cache.compiled, it.tpdf)
		}
		if ct := c.cache.compiled[it.tpdf]; ct != nil {
			// the rest of the chain is compiled; keep walking only to inherit units/default
			basetype = ct.Base
			chain = append(chain, it)
			if (wantUnits && units == nil) || (wantDflt && dfltItem == nil) {
				dummy = true
				prev = it
				id, start, spm = it.tpdf.Type.Name, it.node, it.pm
				continue
			}
			break
		}
		for _, x := range chain {
			if x.tpdf == it.tpdf {
				return nil, nil, nil, verr(ly.Reference, "Invalid \"%s\" type reference - circular chain of types detected.", it.tpdf.Name)
			}
		}
		for _, x := range c.chain {
			if x.tpdf == it.tpdf {
				return nil, nil, nil, verr(ly.Reference, "Invalid \"%s\" type reference - circular chain of types detected.", it.tpdf.Name)
			}
		}
		chain = append(chain, it)
		prev = it
		id, start, spm = it.tpdf.Type.Name, it.node, it.pm
	}
	if dfltItem != nil {
		dflt = &typeDefault{lex: dfltItem.tpdf.Defaults[0], pm: dfltItem.pm}
	}

	if basetype == schema.Unknown {
		missing := tp.Name
		if prev != nil {
			missing = prev.tpdf.Type.Name
		}
		return nil, nil, nil, verr(ly.Reference, "Referenced type \"%s\" not found.", missing)
	}
	if ^substmtMap[basetype]&typeFlags(tp) != 0 {
		return nil, nil, nil, verr(ly.SyntaxYang, "Invalid type restrictions for %s type.", typeStr[basetype])
	}

	// compile the typedefs from the built-in outwards
	outer := len(c.chain)
	defer func() { c.chain = c.chain[:outer] }() // error returns too
	var base *schema.Type
	for u := len(chain) - 1; u >= 0; u-- {
		it := chain[u]
		c.chain = append(c.chain, it)
		if ct := c.cache.compiled[it.tpdf]; ct != nil {
			base = ct
			continue
		}
		plugin := types.TypedefPlugin(it.pm.mod.Name, it.pm.mod.Revision, it.tpdf.Name)
		if plugin == nil && base != nil {
			plugin = types.Plugin(base)
		}
		if basetype != schema.Leafref && u != len(chain)-1 && typeFlags(it.tpdf.Type) == 0 &&
			len(it.tpdf.Type.Exts) == 0 && plugin == types.Plugin(base) {
			// no change: reuse the compiled base, name included
			c.cache.compiled[it.tpdf] = base
			c.cache.hold(base)
			continue
		}
		if ^substmtMap[basetype]&typeFlags(it.tpdf.Type) != 0 {
			return nil, nil, nil, verr(ly.SyntaxYang, "Invalid type \"%s\" restriction(s) for %s type.",
				it.tpdf.Name, typeStr[basetype])
		}
		if basetype == schema.Empty && len(it.tpdf.Defaults) > 0 {
			return nil, nil, nil, verr(ly.Semantics, "Invalid type \"%s\" - \"empty\" type must not have a default value (%s).",
				it.tpdf.Name, it.tpdf.Defaults[0])
		}
		nt, err := c.newType(it.node, parsedStatus(it.tpdf.Status), it.tpdf.Name, it.tpdf.Type, it.pm,
			basetype, it, base, chain, u+1)
		if err != nil {
			return nil, nil, nil, err
		}
		c.cache.compiled[it.tpdf] = nt
		c.cache.hold(nt)
		base = nt
	}
	c.chain = c.chain[:outer]

	// a type with a leafref is never shared: it may resolve differently per instantiation
	hasLeafref := basetype == schema.Leafref
	if basetype == schema.Union && base != nil {
		for _, m := range base.Union {
			if m.Base == schema.Leafref {
				hasLeafref = true
				break
			}
		}
	}
	if typeFlags(tp) == 0 && len(tp.Exts) == 0 && base != nil && !hasLeafref {
		return base, units, dflt, nil
	}
	t, err = c.newType(sc, st, name, tp, pm, basetype, nil, base, chain, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(chain) > 0 {
		t.Typedef, t.TypedefModule = chain[0].tpdf.Name, chain[0].pm.mod
	}
	return t, units, dflt, nil
}

// name2 is the name libyang reports for a referenced typedef in status errors: the enclosing
// node's name for a scoped typedef (tctx->node->name), else the typedef's.
func (it *tpdfItem) name2() string {
	if it.node != nil {
		return it.node.node.Name
	}
	return it.tpdf.Name
}

// missing formats LY_VCODE_MISSCHILDSTMT for a type (with the typedef name when compiling one).
func missing(stmt, what string, tpdf *tpdfItem) error {
	if tpdf != nil {
		return verr(ly.SyntaxYang, "Missing %s substatement for %s type %s.", stmt, what, tpdf.tpdf.Name)
	}
	return verr(ly.SyntaxYang, "Missing %s substatement for %s type.", stmt, what)
}

// notDirect is the "not directly derived" error of fraction-digits, base and union types.
func notDirect(sub, builtin string, tpdf *tpdfItem) error {
	if tpdf != nil {
		return verr(ly.SyntaxYang, "Invalid %s substatement for the type \"%s\" not directly derived from %s built-in type.",
			sub, tpdf.tpdf.Name, builtin)
	}
	return verr(ly.SyntaxYang, "Invalid %s substatement for the type not directly derived from %s built-in type.", sub, builtin)
}

// newType ports lys_compile_type_: a new type of basetype from tp derived from base (nil =
// the built-in). tpdf is the typedef being compiled (nil for a node's own type); chain[last:]
// are the typedefs below it, nearest last.
func (c *typeCtx) newType(sc *scope, st schema.Status, name string, tp *parser.Type, pm *pmod,
	basetype schema.BaseType, tpdf *tpdfItem, base *schema.Type, chain []*tpdfItem, last int) (_ *schema.Type, err error) {
	if err := c.countTypes(1); err != nil {
		return nil, err
	}
	t := &schema.Type{Base: basetype, From: base}
	if tpdf != nil {
		t.Typedef, t.TypedefModule = tpdf.tpdf.Name, tpdf.pm.mod
	}
	defer func() {
		if err != nil {
			c.cache.hold(t) // as libyang: count the half-built type once, then free it
			c.cache.release(t)
		}
	}()
	switch basetype {
	case schema.Binary:
		if tp.Length != nil {
			if t.Length, err = compileRange(tp.Length, basetype, true, 0, lengthOf(base)); err != nil {
				return nil, err
			}
		}
	case schema.Bits, schema.Enumeration:
		what := "bit"
		itemsOf := func(t *parser.Type) []*parser.Enum { return t.Bits }
		if basetype == schema.Enumeration {
			what = "enum"
			itemsOf = func(t *parser.Type) []*parser.Enum { return t.Enums }
		}
		// if-features are evaluated in the (sub)module where the items are written
		// (lysp_qname.mod): pm for own items, the defining typedef's for inherited ones
		switch items := itemsOf(tp); {
		case len(items) > 0:
			err = c.compileEnums(items, pm, basetype, base, t)
		case base != nil:
			// recompile the items of the deepest typedef below this one that has them
			var from *tpdfItem
			for i := len(chain) - 1; i >= last && from == nil; i-- {
				if len(itemsOf(chain[i].tpdf.Type)) > 0 {
					from = chain[i]
				}
			}
			if from == nil { // libyang asserts here (reachable only with a cached chain and an extension)
				return nil, verr(ly.Other, "Internal error: no %s items to inherit.", typeStr[basetype])
			}
			err = c.compileEnums(itemsOf(from.tpdf.Type), from.pm, basetype, nil, t)
		default:
			err = missing(what, typeStr[basetype], tpdf)
		}
		if err != nil {
			return nil, err
		}
	case schema.Dec64:
		switch {
		case base == nil && tp.FractionDigits == 0:
			return nil, missing("fraction-digits", "decimal64", tpdf)
		case base == nil:
			t.FracDigits = tp.FractionDigits
		case tp.FractionDigits != 0:
			if tpdf != nil {
				return nil, verr(ly.SyntaxYang, "Invalid fraction-digits substatement for type \"%s\" not directly derived from decimal64 built-in type.",
					tpdf.tpdf.Name)
			}
			return nil, verr(ly.SyntaxYang, "Invalid fraction-digits substatement for type not directly derived from decimal64 built-in type.")
		default:
			t.FracDigits = base.FracDigits
		}
		if tp.Range != nil {
			if t.Range, err = compileRange(tp.Range, basetype, false, t.FracDigits, rangeOf(base)); err != nil {
				return nil, err
			}
		}
	case schema.String:
		switch {
		case tp.Length != nil:
			if t.Length, err = compileRange(tp.Length, basetype, true, 0, lengthOf(base)); err != nil {
				return nil, err
			}
		case base != nil && base.Length != nil:
			t.Length = &schema.Range{Parts: base.Length.Parts, Msg: base.Length.Msg, AppTag: base.Length.AppTag}
		}
		if base != nil {
			t.Patterns = append([]*schema.Pattern(nil), base.Patterns...)
		}
		for _, p := range tp.Patterns {
			cp := &schema.Pattern{Expr: p.Arg, Invert: p.Invert, Msg: p.ErrorMessage, AppTag: p.ErrorAppTag}
			if perr := types.CompilePattern(cp); perr != nil {
				// libyang logs the PCRE2 message without a validation code (LYVE_SUCCESS)
				return nil, &vErr{Err: "LY_EVALID", Code: ly.Success,
					Msg: "Regular expression \"" + p.Arg + "\" is not valid (" + perr.Error() + ")."}
			}
			t.Patterns = append(t.Patterns, cp)
		}
	case schema.Int8, schema.Uint8, schema.Int16, schema.Uint16, schema.Int32, schema.Uint32, schema.Int64, schema.Uint64:
		if tp.Range != nil {
			if t.Range, err = compileRange(tp.Range, basetype, false, 0, rangeOf(base)); err != nil {
				return nil, err
			}
		}
	case schema.IdentityRef:
		switch {
		case len(tp.Bases) > 0 && base != nil:
			return nil, notDirect("base", "identityref", tpdf)
		case len(tp.Bases) > 0:
			if t.Bases, err = c.identityBases(pm, tp.Bases); err != nil {
				return nil, err
			}
		case base != nil:
			t.Bases = append([]*schema.Identity(nil), base.Bases...)
		default:
			return nil, missing("base", "identityref", tpdf)
		}
	case schema.Leafref:
		switch {
		case tp.RequireInstance != nil && !pm.v11():
			if tpdf != nil {
				return nil, verr(ly.Semantics, "Leafref type \"%s\" can be restricted by require-instance statement only in YANG 1.1 modules.",
					tpdf.tpdf.Name)
			}
			return nil, verr(ly.Semantics, "Leafref type can be restricted by require-instance statement only in YANG 1.1 modules.")
		case tp.RequireInstance != nil:
			t.RequireInstance = *tp.RequireInstance
		case base != nil:
			t.RequireInstance = base.RequireInstance
		default:
			t.RequireInstance = true
		}
		switch {
		case tp.Path != "":
			t.Path = tp.Path
			t.Prefixes = c.pathPrefixes(tp.Path, pm)
		case base != nil:
			t.Path = base.Path
			t.Prefixes = make(schema.NSCtx, len(base.Prefixes))
			for k, v := range base.Prefixes {
				t.Prefixes[k] = v
			}
		default:
			return nil, missing("path", "leafref", tpdf)
		}
	case schema.InstanceID:
		t.RequireInstance = tp.RequireInstance == nil || *tp.RequireInstance
	case schema.Union:
		switch {
		case len(tp.Types) > 0 && base != nil:
			return nil, notDirect("type", "union", tpdf)
		case len(tp.Types) > 0:
			if t.Union, err = c.compileUnion(tp.Types, sc, st, name, pm); err != nil {
				return nil, err
			}
		case base != nil:
			if err := c.countTypes(len(base.Union)); err != nil {
				return nil, err
			}
			t.Union = append([]*schema.Type(nil), base.Union...)
			for _, m := range t.Union {
				c.cache.hold(m)
			}
		default:
			return nil, missing("type", "union", tpdf)
		}
	}
	// ponytail: extension instances on types are compiled with the other instances (design 06
	// §2.17, C4b); until then Type.Exts stays empty while their presence already counts above.
	return t, nil
}

func rangeOf(t *schema.Type) *schema.Range {
	if t == nil {
		return nil
	}
	return t.Range
}

func lengthOf(t *schema.Type) *schema.Range {
	if t == nil {
		return nil
	}
	return t.Length
}

// compileUnion ports lys_compile_type_union: members in order, a member that is a union replaced
// in place by its (already flat) members. Every slot is held and counted against MaxTypes, and
// the width against MaxUnionMembers, before the array grows.
func (c *typeCtx) compileUnion(ptypes []*parser.Type, sc *scope, st schema.Status, name string, pm *pmod) ([]*schema.Type, error) {
	if err := c.countTypes(len(ptypes)); err != nil {
		return nil, err
	}
	width := c.budget.MaxUnionMembers
	if width <= 0 {
		width = DefaultMaxUnionMembers
	}
	if len(ptypes) > width {
		return nil, budgetErr("more than %d union members", width)
	}
	var out []*schema.Type
	release := func() {
		for _, m := range out {
			c.cache.release(m)
		}
	}
	for i, p := range ptypes {
		m, _, _, err := c.compileType(sc, st, name, p, pm, false, false)
		if err != nil {
			release()
			return nil, err
		}
		c.cache.hold(m)
		if m.Base != schema.Union {
			out = append(out, m)
			continue
		}
		err = c.countTypes(len(m.Union))
		if err == nil && len(out)+len(m.Union)+len(ptypes)-i-1 > width { // the rest add one slot or more
			err = budgetErr("more than %d union members", width)
		}
		if err != nil {
			c.cache.release(m)
			release()
			return nil, err
		}
		for _, sub := range m.Union {
			c.cache.hold(sub)
			out = append(out, sub)
		}
		c.cache.release(m) // the replaced union itself
	}
	return out, nil
}

// compileEnums ports lys_compile_type_enums into t.Enums or t.Bits: values and positions
// assigned or inherited from base, checked as a subset of base, bits ordered by position. pm is
// the (sub)module the items are written in, which resolves their if-feature prefixes.
func (c *typeCtx) compileEnums(items []*parser.Enum, pm *pmod, basetype schema.BaseType, base *schema.Type, t *schema.Type) error {
	isEnum := basetype == schema.Enumeration
	what, word := "bits", "Bits"
	if isEnum {
		what, word = "enumeration", "Enumeration"
	}
	if base != nil && !c.pmod.v11() {
		return verr(ly.SyntaxYang, "%s type can be subtyped only in YANG 1.1 modules.", word)
	}
	maxPos := c.budget.MaxBitPosition
	if maxPos == 0 {
		maxPos = DefaultMaxBitPosition
	}
	highestVal, curVal := int64(math.MinInt32), int64(0)
	var highestPos, curPos uint32
	for u, e := range items {
		var bEnum *schema.Enum
		var bBit *schema.Bit
		if base != nil {
			if isEnum {
				for _, b := range base.Enums {
					if b.Name == e.Name {
						bEnum = b
					}
				}
			} else {
				for _, b := range base.Bits {
					if b.Name == e.Name {
						bBit = b
					}
				}
			}
			if bEnum == nil && bBit == nil {
				return verr(ly.SyntaxYang, "Invalid %s - derived type adds new item \"%s\".", what, e.Name)
			}
		}
		if isEnum {
			switch {
			case e.Value != nil:
				curVal = int64(int32(*e.Value)) //nolint:gosec // the parser bounds value to int32
				for _, o := range t.Enums {
					if int64(o.Value) == curVal {
						return verr(ly.SyntaxYang, "Invalid enumeration - value %d collide in items \"%s\" and \"%s\".", curVal, e.Name, o.Name)
					}
				}
			case bEnum != nil:
				curVal = int64(bEnum.Value)
			case u == 0:
				curVal = 0
			case highestVal == math.MaxInt32:
				return verr(ly.SyntaxYang, "Invalid enumeration - it is not possible to auto-assign enum value for \"%s\" since the highest value is already 2147483647.", e.Name)
			default:
				curVal = highestVal + 1
			}
			highestVal = max(highestVal, curVal)
			if bEnum != nil && curVal != int64(bEnum.Value) {
				return verr(ly.SyntaxYang, "Invalid enumeration - value of the item \"%s\" has changed from %d to %d in the derived type.",
					e.Name, bEnum.Value, curVal)
			}
		} else {
			switch {
			case e.Value != nil:
				curPos = uint32(*e.Value) //nolint:gosec // the parser bounds position to uint32
				for _, o := range t.Bits {
					if o.Position == curPos {
						return verr(ly.SyntaxYang, "Invalid bits - position %d collide in items \"%s\" and \"%s\".", curPos, e.Name, o.Name)
					}
				}
			case bBit != nil:
				curPos = bBit.Position
			case u == 0:
				curPos = 0
			case highestPos == math.MaxUint32:
				return verr(ly.SyntaxYang, "Invalid bits - it is not possible to auto-assign bit position for \"%s\" since the highest value is already 4294967295.", e.Name)
			default:
				curPos = highestPos + 1
			}
			highestPos = max(highestPos, curPos)
			if bBit != nil && curPos != bBit.Position {
				return verr(ly.SyntaxYang, "Invalid bits - position of the item \"%s\" has changed from %d to %d in the derived type.",
					e.Name, bBit.Position, curPos)
			}
			if curPos > maxPos {
				return budgetErr("bit \"%s\" position %d is above %d", e.Name, curPos, maxPos)
			}
		}
		enabled := true
		if len(e.IfFeatures) > 0 {
			if c.iff == nil {
				return verr(ly.Other, "Internal error: if-feature of %s \"%s\" without an evaluator.", what, e.Name)
			}
			var err error
			if enabled, err = c.iff(pm, e.IfFeatures); err != nil {
				return err
			}
		}
		if isEnum {
			t.Enums = append(t.Enums, &schema.Enum{Name: e.Name, Value: int32(curVal),
				Status: parsedStatus(e.Status), Disabled: !enabled})
			continue
		}
		b := &schema.Bit{Name: e.Name, Position: curPos, Status: parsedStatus(e.Status), Disabled: !enabled}
		v := len(t.Bits) // keep bits ordered by position
		for v > 0 && t.Bits[v-1].Position > curPos {
			v--
		}
		t.Bits = append(t.Bits, nil)
		copy(t.Bits[v+1:], t.Bits[v:])
		t.Bits[v] = b
	}
	return nil
}

// identityBases ports lys_compile_identity_bases for an identityref type: bases are resolved
// through pm (the type's module), the YANG version is the compiled module's.
func (c *typeCtx) identityBases(pm *pmod, bases []string) ([]*schema.Identity, error) {
	if len(bases) > 1 && !c.pmod.v11() {
		return nil, verr(ly.SyntaxYang, "Multiple bases in identityref type are allowed only in YANG 1.1 modules.")
	}
	out := make([]*schema.Identity, 0, len(bases))
	for _, b := range bases {
		prefix, name := "", b
		if i := strings.IndexByte(b, ':'); i >= 0 {
			prefix, name = b[:i], b[i+1:]
		}
		mod := pm.resolve(prefix)
		if mod == nil {
			return nil, verr(ly.SyntaxYang, "Invalid prefix used for base (%s) of identityref.", b)
		}
		id := mod.Identity(name)
		if id == nil {
			return nil, verr(ly.SyntaxYang, "Unable to find base (%s) of identityref.", b)
		}
		out = append(out, id)
	}
	return out, nil
}

// pathPrefixes ports ly_store_prefix_data for LY_VALUE_SCHEMA over a leafref path, then binds
// "" to the module instantiating the type (SCN:1841): every prefix used in the path that pm
// resolves, unknown ones left out.
func (c *typeCtx) pathPrefixes(path string, pm *pmod) schema.NSCtx {
	ns := schema.NSCtx{"": c.cur}
	for _, p := range valuePrefixes(path) {
		if _, ok := ns[p]; ok {
			continue
		}
		if mod := pm.resolve(p); mod != nil {
			ns[p] = mod
		}
	}
	return ns
}

// valuePrefixes ports the prefix scan of ly_value_prefix_next: every XML QName run directly
// followed by ':' is a prefix.
func valuePrefixes(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isQNameStart(r) {
			i += n
			continue
		}
		j := i + n
		for j < len(s) {
			r, n := utf8.DecodeRuneInString(s[j:])
			if !isQNameChar(r) {
				break
			}
			j += n
		}
		if j < len(s) && s[j] == ':' {
			out = append(out, s[i:j])
			j++
		}
		i = j
	}
	return out
}

// isQNameStart and isQNameChar are libyang's is_xmlqnamestartchar / is_xmlqnamechar (xml.h).
func isQNameStart(c rune) bool {
	return (c >= 'a' && c <= 'z') || c == '_' || (c >= 'A' && c <= 'Z') ||
		(c >= 0x370 && c <= 0x1fff && c != 0x37e) || (c >= 0xc0 && c <= 0x2ff && c != 0xd7 && c != 0xf7) ||
		c == 0x200c || c == 0x200d || (c >= 0x2070 && c <= 0x218f) || (c >= 0x2c00 && c <= 0x2fef) ||
		(c >= 0x3001 && c <= 0xd7ff) || (c >= 0xf900 && c <= 0xfdcf) || (c >= 0xfdf0 && c <= 0xfffd) ||
		(c >= 0x10000 && c <= 0xeffff)
}

func isQNameChar(c rune) bool {
	return isQNameStart(c) || c == '-' || (c >= '0' && c <= '9') || c == '.' || c == 0xb7 ||
		(c >= 0x300 && c <= 0x36f) || (c >= 0x203f && c <= 0x2040)
}
