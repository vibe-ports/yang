# 03 — XPath evaluation context

Status: drafted in M0, revised after M1 (2026-10-08) to match `internal/xpath` and `data` (design 07
§1.6, §2). Reference: RFC 7950 §6.4.1, §7.5.3, §7.21.5, §9.9.2; RFC 8342 §6.1; libyang v5.8.6
`LYXP_NODE_ROOT` / `LYXP_NODE_ROOT_CONFIG` (`src/xpath.h:168`), `lyd_validate_dummy_when()`
(`src/validation.c:1083`).

## Package graph (acyclic)
`internal/xpath` owns narrow read-only interfaces — `Node` (parent, children, name, module, schema
node, typed value, when state), `SchemaNode`, `SchemaType`, `Value`, `SchemaInfo` — and evaluates over
them; `data` implements them (`xpathnode.go`) and calls the evaluator during validation, `compile`
implements the schema side. `internal/xpath` imports neither `data` nor `schema`/`types`.

## Typed values
YANG functions read the **stored** value, not its string: `derived-from[-or-self]()` uses the
selected union member's identity (libyang `xpath.c:4278`), `enum-value()`/`bit-is-set()` the stored
enum/bits. `xpath.Value` therefore exposes `String()` (canonical), `Identity()`, `Enum()` and `Bits()`.
As libyang, `Enum()` answers only when the leaf's *schema* type is an enumeration, so `enum-value()` of
an enumeration union member is NaN. Fixtures: `protocol-v2/xpath-union-derived-from-or-self-*`
(union {identityref; string}, identical text, different members).

## The context is an explicit value, never implicit
As built (`internal/xpath`):
```go
type EvalContext struct {
    Ctx        context.Context // cancellation
    Node       Node            // context node (may be a dummy, rule 3); nil = document root
    Current    Node            // current(); nil = Node
    Tree       []Node          // top-level siblings: the accessible tree
    TreeLookup ChildLookup     // optional index over Tree
    Root       RootKind        // RootAll | RootConfig
    IgnoreWhen bool            // LYXP_IGNORE_WHEN
    Schema     SchemaInfo      // identities, top-level schema nodes, module order
    Deref      func(Node) ([]Node, error)
    MaxSteps   int             // per-evaluation step budget
    Vars       []Var
}
```
Prefixes are not part of it: `Compile(src, NamespaceCtx)` binds them to the expression (rule 1). The
budget is `MaxSteps` + `Ctx`; `Result.Steps` lets `data` keep a cumulative budget per validation
(U-0042).

## Rules to implement (each gets fixtures in the M1 slice)
1. **Name resolution** (RFC 7950 §6.4.1): *explicit prefixes* resolve against the imports of the
   module where the expression is written. *Unprefixed names* take the namespace of the current
   node — inside a grouping that depends on where the grouping is **used** (§7.13), inside a typedef
   on where the type is referenced. So the compiler stores two contexts per expression: the
   defining module (prefix bindings, `schema.Must/When.Ctx`) and the instantiating node's module
   (`NamespaceCtx.Default()` = the node's module, design 06 §2.13). JSON-format names match any
   module when unprefixed. Open: no fixture yet for an unprefixed `must` inside a grouping used from a
   second module (protocol-v2 `pv2-aug` covers the foreign grouping's names only).
2. **Accessible tree** (RFC 7950 §6.4.1):
   - config true node → all config true data in the datastore (+ `RootConfig`);
   - config false node → all config + state data;
   - RPC/action input/output, notification → the operation tree plus the whole datastore
     (libyang takes the external operational tree for this, `-O` in yanglint) — M4;
   - NMDA (RFC 8342 §6.1): for `<operational>` the accessible tree is the operational datastore.
   As built, the root kind comes from the **context node's** schema config (`lyxp_get_root_type`), any
   node inside an operation gives `RootAll`, and `Tree` is the whole data tree being validated.
   `ValidateOptions.Operational` changes severities only (a false `when` or `must` warns), not the tree.
3. **Default creation follows libyang's phases** (`tree_data_new.c:1952,1980`): implicit defaults and
   non-presence containers are **materialised first** (flag `Default`, plus `WhenTrue` when their
   schema node has a `when`), **then** their `when` conditions are resolved together with the rest
   through the queue (rule 5); a false one deletes them silently. No dummy node is involved, and no
   absent default is pre-checked in isolation. Fixtures: `when/order-*`; protocol-v2 `seq-diff-*` goldens replayed in `data/diff_test.go`.
   **`when` on a node that does not exist** is evaluated only by the final checks — mandatory
   (absent mandatory node or choice) and min-elements — exactly like `lyd_validate_dummy_when`: an
   opaque node named like the schema node is linked at the would-be position (opaque, so it matches no
   name test), the whens of the node and its choice/case ancestors are evaluated (context node = the
   dummy or its parent, per `When.ContextNode`), root kind from the schema node's config, then the
   dummy is unlinked; a false `when` waives the check. An unresolved `when` is ignored under
   multi-error and `LY_EINT` otherwise (design 07 §7, candidate D-0055). The dummy is never visible to callers.
4. **`when` context node** (RFC 7950 §7.21.5): under `augment` → the augment's target node if it is a
   data node, else its closest data-node ancestor; under `uses`/`choice`/`case` → the closest data-node
   ancestor of the statement's node; otherwise the node itself. Compile stores it as
   `When.ContextNode`; data evaluates on the node or its parent accordingly.
5. **Evaluation order** — libyang's ordered unresolved-`when` queue, **not** a fixpoint
   (`lyd_validate_unres_when`, validation.c:461-524, driven by `lyd_validate_unres`,
   validation.c:556-565). The queue holds the nodes whose `when` is still unresolved, in the order they
   were collected (parse post-order, then implicit nodes in creation order); one pass walks it from the
   **end** to the start. Per node, `lyd_validate_node_when` evaluates every `when` affecting it: if
   evaluation hits a node whose own `when` is unresolved it returns `LY_EINCOMPLETE` and the node stays
   queued for the next pass; otherwise the condition is **resolved** — true sets `WhenTrue`, false
   auto-deletes (node had `WhenTrue`), warns (operational) or errors (multi-error: kept and flagged
   `WhenFalse`) — and the node leaves the queue for good (`ly_set_rm_index_ordered`,
   validation.c:517). Passes repeat only while the queue shrinks. A resolved condition is never
   re-evaluated in the same validation, even if a later auto-delete changes what it read. Confirmed by
   the oracle: defaults `b` (false `when`) and `a` with `when "../b"` declared after `b` → `a` is
   evaluated first, sees `b`, stays; declared before `b` → both are removed (`when/order-a-after-b`,
   `when/order-b-after-a`; mirrored as candidate D-0047). Circular `when` dependencies are rejected at
   **schema compile** time (`lys_compile_unres_when_cyclic`, `src/schema_compile.c:457`,
   LYVE_SEMANTICS) in `internal/compile`, so data evaluation never sees a cycle (`when/cycle`).
6. **Defaults are visible** to `must`/`when`: implicit defaults are materialised (flag `Default`)
   before `must`/`when` evaluation (this is where cambium diverges; see ADR 0002 cases 04/05/17).
   D-0001 (a `when` reading a top-level default) is still a candidate, mirrored.
7. **YANG functions**: `current()`, `deref()`, `derived-from()`, `derived-from-or-self()`,
   `re-match()` (XSD regex via internal/xsdre), `enum-value()`, `bit-is-set()`, plus the whole XPath
   1.0 core library. Unknown function → compile-time error (`Unknown XPath function "%s".`), never
   silent skip.

## Revised after M1
What the slice changed, and why:
- **The dummy node is not how defaults are decided** (rule 3, M0 text). libyang creates implicit
  nodes first and lets the `when` queue delete them; `lyd_validate_dummy_when` serves only the
  mandatory and min-elements checks of absent nodes. That also settles the M0 open question: the dummy
  never replaces an existing instance (it is used only when none exists), and the whens it evaluates
  are those compiled onto the schema node (from `uses`/`augment`) and its choice/case ancestors.
- **Prefixes moved from `EvalContext` to `Compile`**, and the context gained what libyang's
  `lyxp_set` carries: `Current`, `IgnoreWhen`, `Deref`, `Vars`, the schema hook and an optional
  lookup index. `Node` is the xpath interface, not `*data.Node`, so the graph stays acyclic.
- **`WhenFalse`** (multi-error) makes a node invisible to XPath; descendants stay reachable through
  `//` as in libyang (deviations.md, "Known libyang behaviour").
- **Numbers are libyang's `long double`**, emulated as 80-bit x87 (D-0010), with its integer
  conversions pinned to amd64 (D-0011); libyang's other XPath quirks are mirrored and listed in
  deviations.md, its crashes answered (D-0012, D-0063).
- **Open:** the report engine does not run `sequence` (the `when/order-*` and
  `m1/sequence-when-auto-delete` goldens) or `xpath` requests yet; the queue is covered by `data` unit
  tests and the protocol-v2 `seq-diff-*` goldens replayed in `data/diff_test.go`, the evaluator by `internal/xpath`'s oracle replay.
