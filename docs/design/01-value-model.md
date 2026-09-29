# 01 — Value model

Status: draft (M0). Reference: libyang v5.8.6 `struct lyd_value`, `struct lyd_value_union`
(`src/tree_data.h:547,602`), `LY_VALUE_FORMAT` (`src/tree.h:235`).

## Requirements
- Exact for every YANG built-in type: no float64 anywhere (decimal64 = scaled int64, 64-bit ints exact
  on 386).
- Canonical form available cheaply (printing, comparison, keys, XPath string value).
- Union: remember the **original lexical value + its format + prefix context**, because libyang
  re-resolves a union at validation time (a leafref/instance-identifier member can only be decided
  against data) and because the JSON token kind (string vs number) takes part in member selection
  (RFC 7951 §6.10).
- Namespace-aware values (identityref, instance-identifier, prefixed strings) resolved to schema
  objects at store time; printing re-prefixes per output format (XML namespace prefix vs JSON module
  name).

## Go shape (internal until M3; public read API is `Value.String()`/typed getters)
```go
type Value struct {
    typ   *schema.Type // realtype: the type that stored it (leafref → target type, union → union)
    canon string       // canonical cache (computed at store time)
    kind  valueKind    // which field below is valid
    i     int64        // int8..int64, decimal64 (scaled by 10^fd), boolean, enum value
    u     uint64       // uint8..uint64
    bits  []*schema.Bit
    ident *schema.Identity
    enum  *schema.Enum
    path  *Path        // instance-identifier target (resolved schema path + predicates)
    bin   []byte       // binary
    union *UnionValue  // only for union types
}

type UnionValue struct {
    Value              // the selected member's value (typ = member type)
    orig    string     // original lexical form
    format  Format     // FormatXML | FormatJSON | FormatSchema | FormatCanon
    hints   Hints      // JSON token kind: string / number / bool / null
    prefix  PrefixCtx  // XML ns map or JSON module names captured at parse time
    ctxNode *schema.Node
}
```
- `Format` mirrors `LY_VALUE_FORMAT` minus LYB (out of v1).
- Store is `func (t *Type) Store(lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node) (Value, *Diagnostic)`;
  validation-time resolution (leafref require-instance, instance-identifier existence, union
  re-resolution) is a separate pass with access to the data tree.
- Comparison = canonical string compare except union (compare member type + canonical) —
  same as libyang `lyplg_type_compare`.

## Open questions (decide in M1 slice)
- Interning of canonical strings: only if profiling shows need.
- `ietf-yang-types`/`ietf-inet-types` special types (ip-address canonical zone/compression,
  date-and-time) as dedicated kinds vs string with canonicalizer — libyang uses plugins; start with
  canonicalizer funcs keyed by (module, typedef).
