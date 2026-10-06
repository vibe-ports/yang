# 05 — M1 vertical slice: package graph, APIs, task split

Status: plan for M1 (2026-10-05). Builds on 01–04. Scope = PLAN §4 row M1: a real module set
parsed, compiled, data parsed (JSON+XML), defaults, XPath subset, must/when/leafref/mandatory
validation — agreeing with the oracle. Shallow everywhere, deep nowhere; breadth is M2+.

## Slice corpus
`conformance/corpus/m1/`: ietf-interfaces@2018-02-20 (RFC 8343), ietf-ip@2018-02-22 (RFC 8344),
ietf-yang-types + ietf-inet-types (RFC 9911 revisions as bundled by libyang 5.8.6), iana-if-type,
plus a hand-written `m1-ext` module that augments ietf-interfaces with: a grouping used with
`refine`, `if-feature`, a `choice` with default case, `must` + `when`, a `leafref`, a
`union`, a `decimal64`, a keyless state list. Fixtures: valid + invalid data in JSON and XML for
each rule, schema-tree dump, a `sequence` with when auto-delete.

## Package graph (acyclic, enforced by `go list -deps` in a test)
```
internal/parser   (stdlib only)               YANG text → statement tree → typed parsed module
internal/schema   (stdlib only)               compiled schema types: Module, Node, Type, Must, When, Identity
internal/snap     → schema (stdlib)           opaque read-only handles (Schema, Module, SchemaNode, …) and their methods (design 07 §0.4)
internal/xsdre    (stdlib)                    done
internal/xpath    → xsdre                     parse + evaluate over xpath.Node / SchemaNode / Value (interfaces it owns; no schema import)
internal/types    → schema, xsdre             value model (design 01): Store/Canonical/Compare/Resolve
internal/compile  → parser, schema, types, xpath   parsed modules → compiled schema
data              → yang, snap, schema, types, xpath   tree (design 02), JSON/XML codecs, defaults, validation (design 07)
yang (root)       → parser, compile, schema, snap   Context: load modules (fs.FS), compile; aliases for schema types
cmd/yanglint-go, conformance engine → yang, data
```
The compiled schema is exposed from `yang` through **read-only handles** (accessor methods,
iterators, copies) over `internal/schema` — never type aliases with writable fields — so the
compiled Context stays immutable for concurrent readers (PLAN §2). An external-package test proves
returned collections cannot mutate the schema.

## Minimal API per package (M1; may grow, never by exporting internals "just in case")
- **parser**: `Parse(name string, src []byte, b *Budget) (*Stmt, error)`;
  `Stmt{Keyword, ExtPrefix, Arg string; HasArg bool; Pos Pos; Subs []*Stmt}`;
  `Build(*Stmt) (*Module, error)` → typed parsed module (lysp equivalent) for the M1 statement set;
  errors `*Error{Pos, Code, Msg}` with libyang-compatible codes (LYVE_SYNTAX_YANG, …).
- **schema**: plain structs, no behaviour beyond lookups: `Module{Name, Revision, Namespace,
  Prefix, Features, Identities, Top []*Node}`; `Node{Kind, Name, Module, Parent, Children,
  Config, Mandatory, Presence, Keys, Min, Max, OrderedBy, Defaults []DefaultValue{Lex, NS}, Type *Type, Musts,
  Whens, Status}`; `Type{Base, Typedef, Range, Length, Patterns, FracDigits, Enums, Bits, Bases,
  Path, RequireInstance, Union []*Type}`; `Must{Src, AppTag, Msg, Ctx NSCtx, Compiled any}`,
  `When{Src, Ctx NSCtx, ContextNode *Node, Compiled any}` — `Compiled` holds the `*xpath.Expr` set by
  compile; schema stays stdlib-only, and xpath does not import schema either (compile adapts schema to xpath.SchemaNode).
- Defaults keep the original lexical text + prefix context (libyang `schema_compile.c:976`); the
  canonical value is derived by `types` at use time, because a union default (e.g. "01" for
  `union { leafref→uint8; string }`) resolves differently depending on data.
- **types**: design 01 `Value` (answers which union member was selected and which identity); `Store(t *schema.Type, lex string, f Format, h Hints, pc
  PrefixCtx, ctx *schema.Node) (Value, *Diag)`; `Canonical`, `Equal`; union keeps original.
- **xpath**: `Compile(src string, ns NamespaceCtx) (*Expr, error)`; `(*Expr).Eval(ctx
  EvalContext) (Result, error)`; `Node` interface + `EvalContext` per design 03. M1 subset:
  location paths (all axes used by YANG models), predicates, `= != < <= > >=`, `and/or/not`,
  `count`, `current`, `deref`, `derived-from[-or-self]`, `string`, `number`, `boolean`,
  `re-match`, `starts-with`, `contains`. Unknown function = compile error.
- **compile**: `Compile(mods []*parser.Module, opts Options) (*schema.Set, []Diagnostic, error)`:
  imports/includes, typedef chains, grouping/uses/refine/augment (top-level + uses), if-feature,
  identities, choice/case, defaults canonicalised via types, xpath compile of must/when/leafref.
  Deviations are M2.
- **data** (amended by design 07 §0.4/§2): `Parse(ctx context.Context, r io.Reader, f Format,
  s *yang.Schema, opts ParseOptions) (*Tree, []yang.Diagnostic, error)` where `yang.Schema` is the
  opaque immutable snapshot `(*yang.Context).Schema()` returns (`internal/snap.Schema`, no exported
  fields; `data` reaches the set through `snap.Set`; a Tree pins it);
  `(*Tree).Validate(ctx, opts) ([]yang.Diagnostic, error)` (design 02 flags, defaults, when history);
  `Print(w, f, PrintOptions)`. M1: datastore data types, unknown reject/skip/opaque.
- **conformance engine** (in the conformance module): adapter implementing `conformance.Engine`
  for ops `schema` (schema_tree subset), `data` and `sequence` (retained tree, edits, validation
  implicit diff, per-step `typed` flags — M1-6 provides the tree API, M1-7 the adapter), so `go run ./cmd/report -engine go` reports
  agreement on the m1 fixtures.

## Task split (each ≤ ~1.5k Go lines, own branch + PR + reviews)
| # | Task | Depends | Worker |
|---|---|---|---|
| M1-1 | slice corpus + fixtures + goldens (above) | — | Sonnet |
| M1-2 | parser: tokenizer port of `parser_yang.c` + stmt tree + typed build (M1 set) + fuzz + budgets | — | Opus |
| M1-3 | xpath: lexer/parser/evaluator over `Node` + M1 function set + fuzz | design 03 | Opus |
| M1-4 | schema + types: structs, value model, built-in types, union, decimal64, xsdre patterns | 01 | Opus |
| M1-5 | compile (M1 set) | 2, 3, 4 | Opus |
| M1-6 | data: tree, JSON/XML codecs, defaults, validation | 3, 4, 5 | Opus (split if > 1.5k) |
| M1-7 | Context API + conformance engine + report in CI summary | 5, 6 | Sonnet |

Wave 1 = M1-1…M1-4 in parallel. Exit criterion (PLAN): every m1 fixture agrees (or is a recorded
deviation) — **including the sequence fixtures with intermediate flags and the deletion diff**;
design notes 01–03 revised from what the slice taught us.

Plan review (astra, 2026-10-05): 6 findings, all accepted — raw defaults, typed XPath values,
default materialise-then-resolve (03), per-constraint operational severity (PLAN), read-only schema
handles, sequence in the M1 exit criterion.
