# 01 — Value model

Status: drafted in M0, revised after M1 (2026-10-08) to match `internal/types`. Reference: libyang
v5.8.6 `struct lyd_value`, `struct lyd_value_union` (`src/tree_data.h:547,602`), `LY_VALUE_FORMAT`
(`src/tree.h:235`), `src/plugins_types/*.c`.

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

## Go shape (as built, `internal/types`; public read API is `data.Node.Value()`, the canonical text)
```go
type Value struct {
    typ   *schema.Type     // realtype: the type that stored it (leafref → target type, union → union)
    canon string           // canonical form, computed at store time
    i     int64            // int8..int64, decimal64 (scaled by 10^fd), boolean 0/1
    u     uint64           // uint8..uint64
    enum  *schema.Enum
    bits  []*schema.Bit    // set bits in position order
    bmap  []byte           // bits bitmap, libyang's little-endian layout (ordering = memcmp)
    bin   []byte           // binary
    ident *schema.Identity
    path  Path             // instance-identifier target (compiled schema path + predicates)
    union *UnionValue      // only for union types
    ext   any              // storage of an ietf-* type plugin (IP address, date-and-time, …)
    needsTree bool         // LY_EINCOMPLETE: require-instance leafref/instance-identifier pending
}

type UnionValue struct {
    member Value       // the selected member's value (typ = member realtype)
    index  int         // the member's position in the union
    orig   string      // original lexical form
    f      Format      // FormatCanon | FormatSchema | FormatSchemaResolved | FormatXML | FormatJSON
    h      Hints       // libyang LYD_VALHINT_* bit mask
    pc     PrefixCtx   // XML namespaces or JSON module names captured at parse time
    ctx    *schema.Node
}
```
- `Format` mirrors `LY_VALUE_FORMAT` minus LYB (out of v1); `FormatSchemaResolved` stores schema
  defaults, whose prefixes compile already resolved (`schema.DefaultValue{Lex, NS}`).
- The kind is the realtype's `Base` (or its plugin); no separate `kind` field.
- Store is `Store(t *schema.Type, lex string, f Format, h Hints, pc PrefixCtx, ctx *schema.Node)
  (Value, *Diag)`; `StoreOnly` is `LYD_PARSE_STORE_ONLY`, `StoreDefault` stores a schema default.
  Validation-time work is separate: `ValidateTree(t, v, tree)` (leafref/instance-identifier
  require-instance and union re-selection, over the `types.Tree` interface `data` implements) and
  `Validate(t, v)` (restriction re-check). `Print(v, f, *PrintCtx)` re-prefixes per format.
- Comparison: per type, mirroring libyang's per-plugin `compare`/`sort` callbacks (not a generic
  canonical-string compare); union delegates to the selected member. Values of different realtypes
  are never `Equal`; `Compare` orders different base types by canonical text and union members by
  member order (`lyplg_type_sort_union`). Store / equality / order / print are oracle-tested per type
  (`internal/types` oracle tests over the `types/*` goldens).

## Decided in M1 (former open questions)
- Interning of canonical strings: not done; nothing has needed it. Still only if profiling asks.
- `ietf-yang-types`/`ietf-inet-types` special types: libyang-style **plugins** with their own storage
  (`ext`), keyed by (module, revision, typedef); revision `""` matches every revision. A typedef
  inherits the plugin of the nearest typedef in its derivation chain, as `lys_compile_type` does.
  Ported: the `ietf-inet-types` address/prefix plugins, the `ietf-yang-types` `date-and-time`,
  `date`/`date-no-zone`, `time`/`time-no-zone`, hex-string family and `xpath1.0` plugins,
  `yang:instance-identifier-keys`, libnetconf2 `time-period` and ietf-netconf-acm
  `node-instance-identifier` (all listed handlers are ported).

## Revised after M1
What the slice changed, and why:
- **Union hints are a bit mask, not a token kind.** libyang selects members through
  `LYD_VALHINT_*` flags (`HintData` for XML, `HintSchema` for defaults, `JSONHints` per JSON token,
  plus `HintStringDatatypes`), so the M0 `Hints` enum became `uint32` with libyang's values.
- **Member selection** is libyang's: the first member that stores *and* validates wins; with
  `StoreOnly`, failing that, the first that only stores. A leafref member reports the reference error,
  not its target type's. `UnionValue` records the member index, which libyang does not (design 04 §2).
- **Leafref stores as its target type** (the realtype), and `needsTree` replaces the M0 idea that
  every reference value is resolved in a later pass: only values whose store returned
  `LY_EINCOMPLETE` are queued for `ValidateTree`.
- **Bits keep a bitmap** besides the item list: libyang orders bits by `memcmp` of the bitmap. The
  highest position 4294967295 is handled sparsely (D-0029).
- **Host-dependent outputs are pinned**: `date-and-time` prints UTC (D-0025, libyang uses `TZ`);
  IPv6 canonical form is glibc `inet_ntop` (design 04 §7); a NUL in an address is rejected (D-0027);
  wide date gaps order as on the amd64 oracle (D-0028).
- **Typed getters stay internal.** `data` exposes only `Node.Value()` (canonical text, as
  `lyd_get_value`); `xpath` reads the typed value through its own `Value` interface (design 03).
- **Open:** the report engine does not emit `typed.value.type`/`typedef`/`union_member` yet, so the
  data fixtures compare them as skipped fields; union members are checked against goldens only in
  `internal/types` tests.
  When it does, note the two answer different questions: `UnionValue.index` is the member that
  actually stored the value, the oracle's `union_member` the first member whose realtype is the
  stored one (design 04 §2); they differ when members share a realtype.
