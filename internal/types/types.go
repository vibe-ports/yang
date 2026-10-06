// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types.c (BSD-3-Clause, © CESNET).

// Package types is the YANG value model (docs/design/01-value-model.md): storing a lexical value
// of a compiled type, its canonical form, equality and ordering, with libyang-compatible
// diagnostics. It ports libyang's built-in type plugins (src/plugins_types/*.c).
package types

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
)

// Format is the encoding a lexical value comes in (libyang LY_VALUE_FORMAT without LYB).
type Format uint8

// Value formats.
const (
	FormatCanon          Format = iota // canonical, JSON-style prefixes (module names), trusted
	FormatSchema                       // schema text: prefixes are the module's own and import prefixes
	FormatSchemaResolved               // schema text with prefixes already resolved (schema.NSCtx)
	FormatXML                          // XML: prefixes are in-scope namespace declarations
	FormatJSON                         // RFC 7951 JSON: prefixes are module names
)

// Hints says how the encoding typed the value (libyang LYD_VALHINT_*); JSON takes part in type
// checks and union member selection with them (RFC 7951 §6).
type Hints uint32

// Value hints.
const (
	HintString          Hints = 0x0001 // JSON string
	HintDecNum          Hints = 0x0002 // decimal number
	HintOctNum          Hints = 0x0004 // octal number
	HintHexNum          Hints = 0x0008 // hexadecimal number
	HintNum64           Hints = 0x0010 // int64/uint64 (a JSON string)
	HintBoolean         Hints = 0x0020 // JSON true/false
	HintEmpty           Hints = 0x0040 // JSON [null]
	HintStringDatatypes Hints = 0x0080 // numbers and booleans may come as strings

	HintData   Hints = 0xFFF3 // XML and API data: no type information, decimal numbers only
	HintSchema Hints = 0xFFFF // schema defaults: no type information, any number base
)

// JSONHints returns the hints the JSON parser gives a token: kind is one of "string",
// "number", "bool" and "empty" ([null]) (libyang lydjson_value_type_hint).
func JSONHints(kind string) Hints {
	switch kind {
	case "string":
		return HintString | HintNum64
	case "number":
		return HintDecNum
	case "bool":
		return HintBoolean
	case "empty":
		return HintEmpty
	}
	return 0
}

// PrefixCtx resolves the prefixes in a value (libyang prefix_data). An empty prefix asks for
// the default: the XML default namespace or the module of schema text.
type PrefixCtx interface {
	Resolve(prefix string) *schema.Module
}

// ModuleNames resolves JSON and canonical prefixes, which are module names.
type ModuleNames struct{ Set *schema.Set }

// Resolve implements PrefixCtx.
func (m ModuleNames) Resolve(prefix string) *schema.Module {
	if prefix == "" || m.Set == nil {
		return nil
	}
	return m.Set.Implemented(prefix)
}

// XMLNamespaces resolves XML prefixes through the in-scope namespace declarations (key "" is the
// default namespace). Order lists the keys of NS in libyang's prefix-data order (the default
// namespace, then the prefixes as the value uses them), for printers that declare them.
type XMLNamespaces struct {
	Set   *schema.Set
	NS    map[string]string
	Order []string
}

// Resolve implements PrefixCtx.
func (x XMLNamespaces) Resolve(prefix string) *schema.Module {
	ns, ok := x.NS[prefix]
	if !ok || x.Set == nil {
		return nil
	}
	if m := x.Set.ByNamespace(ns); m != nil {
		return m
	}
	for _, m := range x.Set.Modules {
		if m.Namespace == ns {
			return x.Set.Module(m.Name, "")
		}
	}
	return nil
}

// SchemaText resolves prefixes of a value written in Module's schema text (FormatSchema).
type SchemaText struct{ Module *schema.Module }

// Resolve implements PrefixCtx.
func (s SchemaText) Resolve(prefix string) *schema.Module {
	if prefix == "" {
		return s.Module
	}
	return s.Module.Import(prefix)
}

// Diagnostic codes (libyang LY_VECODE).
const (
	CodeData      = "LYVE_DATA"
	CodeReference = "LYVE_REFERENCE"
	CodeNone      = "LYVE_SUCCESS" // plain errors without a validation code (ly_err_new with vecode 0)
)

// Diag is a rejected value: libyang's ly_err_item without the data path, which the caller adds.
type Diag struct {
	Code   string
	Msg    string
	AppTag string
	Err    string // the LY_ERR of the plugin's error item when not LY_EVALID ("" = LY_EVALID)
}

func (d *Diag) Error() string { return d.Msg }

// RC is the LY_ERR of the plugin's error item: Err when set, LY_EINVAL for an item without a
// validation code (ly_err_new(err, LY_EINVAL, 0, ...)), else LY_EVALID.
func (d *Diag) RC() string {
	switch {
	case d.Err != "":
		return d.Err
	case d.Code == CodeNone:
		return "LY_EINVAL"
	}
	return "LY_EVALID"
}

func errf(format string, args ...any) *Diag {
	return &Diag{Code: CodeData, Msg: fmt.Sprintf(format, args...)}
}

// Store parses lex of type t into a Value, checking every restriction of the type. ctx is the
// schema node the value belongs to (nil when there is none); pc resolves prefixes for f.
func Store(t *schema.Type, lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node) (Value, *Diag) {
	return store(t, lex, f, h, pc, ctx, false)
}

// StoreOnly is Store without the restriction checks (range, length, pattern), like libyang
// LYPLG_TYPE_STORE_ONLY / LYD_PARSE_STORE_ONLY.
func StoreOnly(t *schema.Type, lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node) (Value, *Diag) {
	return store(t, lex, f, h, pc, ctx, true)
}

// Implementer is the LYPLG_TYPE_STORE_IMPLEMENT callback of a store: it makes a module referenced
// by the value implemented (and compiled). importFeatures selects the features: the context's
// import features (lys_compile_expr_implement) or none (lyplg_type_make_implemented).
type Implementer func(m *schema.Module, importFeatures bool) error

// StoreImplement is Store with LYPLG_TYPE_STORE_IMPLEMENT: an identityref naming an identity of a
// module that is not implemented, and the modules an instance-identifier names, are implemented
// through impl instead of failing. A failure of impl is a Diag without a message for an
// identityref (the caller sees impl's error too), and the instance-identifier's own message for an
// instance-identifier, which swallows the error, LY_ERECOMPILE included (lyplg_type_lypath_new).
// Union members are stored without impl: union_store_type never passes the option on.
func StoreImplement(t *schema.Type, lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node, impl Implementer) (Value, *Diag) {
	return storeArgsDispatch(&storeArgs{t: t, lex: lex, f: f, h: h, pc: pc, ctx: ctx, impl: impl})
}

// storeArgs carries one store call (the arguments of libyang lyplg_type_store_clb).
type storeArgs struct {
	t    *schema.Type
	lex  string
	f    Format
	h    Hints
	pc   PrefixCtx
	ctx  *schema.Node
	only bool
	// quiet: inside a union libyang turns logging off, so messages built from the context log
	// (instance-identifier details) are missing.
	quiet bool
	impl  Implementer // LYPLG_TYPE_STORE_IMPLEMENT
}

func store(t *schema.Type, lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node, only bool) (Value, *Diag) {
	return storeArgsDispatch(&storeArgs{t: t, lex: lex, f: f, h: h, pc: pc, ctx: ctx, only: only})
}

func storeArgsDispatch(a *storeArgs) (Value, *Diag) {
	t := a.t
	if p := pluginFor(t); p != nil {
		return p.store(a)
	}
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		return storeInt(a)
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return storeUint(a)
	case schema.Dec64:
		return storeDec64(a)
	case schema.String:
		return storeString(a)
	case schema.Bool:
		return storeBool(a)
	case schema.Empty:
		return storeEmpty(a)
	case schema.Enumeration:
		return storeEnum(a)
	case schema.Bits:
		return storeBits(a)
	case schema.Binary:
		return storeBinary(a)
	case schema.IdentityRef:
		return storeIdentityRef(a)
	case schema.InstanceID:
		return storeInstanceID(a)
	case schema.Leafref:
		return storeLeafref(a)
	case schema.Union:
		return storeUnion(a)
	}
	return Value{}, &Diag{Code: CodeData, Msg: fmt.Sprintf("Internal error: no handler for type %s.", t.Base)}
}

// checkHints ports lyplg_type_check_hints: the encoding must allow the type's lexical kind.
// It returns the number base for integers (0 = C strtol auto-detection).
func checkHints(h Hints, lex string, b schema.BaseType) (int, *Diag) {
	base := 0
	switch b {
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Int8, schema.Int16, schema.Int32:
		if h&(HintDecNum|HintOctNum|HintHexNum) == 0 && h&HintStringDatatypes == 0 {
			return 0, errf("Invalid non-number-encoded %s value \"%s\".", b, lex)
		}
		base = hintsBase(h)
	case schema.Uint64, schema.Int64:
		if h&HintNum64 == 0 && h&HintStringDatatypes == 0 {
			return 0, errf("Invalid non-num64-encoded %s value \"%s\".", b, lex)
		}
		base = hintsBase(h)
	case schema.String, schema.Dec64, schema.Enumeration, schema.Bits, schema.Binary, schema.IdentityRef, schema.InstanceID:
		if h&HintString == 0 {
			return 0, errf("Invalid non-string-encoded %s value \"%s\".", b, lex)
		}
	case schema.Bool:
		if h&HintBoolean == 0 && h&HintStringDatatypes == 0 {
			return 0, errf("Invalid non-boolean-encoded %s value \"%s\".", b, lex)
		}
	case schema.Empty:
		if h&HintEmpty == 0 {
			return 0, errf("Invalid non-empty-encoded %s value \"%s\".", b, lex)
		}
	}
	return base, nil
}

// hintsBase ports type_get_hints_base.
func hintsBase(h Hints) int {
	switch h & (HintDecNum | HintOctNum | HintHexNum) {
	case HintDecNum:
		return 10
	case HintOctNum:
		return 8
	case HintHexNum:
		return 16
	}
	return 0
}

// checkRange ports lyplg_type_validate_range. v is the value (for unsigned bases reinterpreted
// as uint64), canon the string the message quotes.
func checkRange(b schema.BaseType, r *schema.Range, v int64, canon string) *Diag {
	isLength := b == schema.Binary || b == schema.String
	fail := func() *Diag {
		if r.Msg != "" {
			return &Diag{Code: CodeData, Msg: r.Msg, AppTag: r.AppTag}
		}
		msg := "Unsatisfied range - value \"%s\" is out of the allowed range."
		if isLength {
			msg = "Unsatisfied length - string \"%s\" length is not allowed."
		}
		return &Diag{Code: CodeData, Msg: fmt.Sprintf(msg, canon), AppTag: r.AppTag}
	}
	for i, p := range r.Parts {
		last := i == len(r.Parts)-1
		if b < schema.Dec64 { // unsigned
			u := uint64(v) //nolint:gosec // C reads the lysc_range_part union as unsigned
			switch {
			case u < p.MinU:
				return fail()
			case u <= p.MaxU:
				return nil
			case last:
				return fail()
			}
			continue
		}
		switch {
		case v < p.Min:
			return fail()
		case v <= p.Max:
			return nil
		case last:
			return fail()
		}
	}
	return nil
}

// trimCSpace strips leading C isspace characters.
func trimCSpace(s string) string {
	i := 0
	for i < len(s) && cIsSpace(s[i]) {
		i++
	}
	return s[i:]
}

// cIsSpace is C isspace in the "C" locale.
func cIsSpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}
