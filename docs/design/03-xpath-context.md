# 03 — XPath evaluation context

Status: draft (M0). Reference: RFC 7950 §6.4.1, §7.5.3, §7.21.5, §9.9.2; RFC 8342 §6.1;
libyang v5.8.6 `LYXP_NODE_ROOT` / `LYXP_NODE_ROOT_CONFIG` (`src/xpath.h:168`),
`lyd_validate_dummy_when()` (`src/validation.c:1083`).

## Package graph (acyclic)
`internal/xpath` owns a narrow read-only `Node` interface (parent, children by name/module,
value string, schema node) and evaluates over it; `data` implements that interface and calls the
evaluator during validation. `internal/xpath` never imports `data`.

## Typed values
YANG functions read the **stored** value, not its string: `derived-from[-or-self]()` uses the
selected union member's identity (libyang `xpath.c:4278`), `enum-value()`/`bit-is-set()` the stored
enum/bits. `xpath.Node` therefore exposes a read-only typed value (canonical string, identity of the
selected member, enum, bits). Fixture: union {identityref; string} with identical canonical text
but different selected members → different `derived-from-or-self()` results.

## The context is an explicit value, never implicit
```go
type EvalContext struct {
    Node      *data.Node        // context node (may be a dummy, see below)
    Root      RootKind          // RootAll | RootConfig
    Tree      AccessibleTree    // which top-level trees are visible
    Prefixes  PrefixResolver    // bound to the DEFINING module of the expression
    Budget    *Budget           // step limit + ctx cancellation
}
```

## Rules to implement (each gets fixtures in the M1 slice)
1. **Name resolution** (RFC 7950 §6.4.1): *explicit prefixes* resolve against the imports of the
   module where the expression is written. *Unprefixed names* take the namespace of the current
   node — inside a grouping that depends on where the grouping is **used** (§7.13), inside a typedef
   on where the type is referenced. So the compiler stores two contexts per expression: the
   defining module (prefix bindings) and the instantiating node's module (default namespace).
   Fixtures: a grouping with unprefixed `must` used from a second module.
2. **Accessible tree** (RFC 7950 §6.4.1):
   - config true node → all config true data in the datastore (+ `RootConfig`);
   - config false node → all config + state data;
   - RPC/action input/output, notification → the operation tree plus the whole datastore
     (libyang takes the external operational tree for this, `-O` in yanglint);
   - NMDA (RFC 8342 §6.1): for `<operational>` the accessible tree is the operational datastore.
3. **Default creation follows libyang's phases** (`tree_data_new.c:1952,1980`): implicit defaults and
   non-presence containers are **materialised first** (flag `Default`, `WhenTrue` seeded per
   libyang), **then** their `when` conditions are resolved together with the rest; nodes whose
   `when` is false are removed. Pre-checking each absent default in isolation is wrong (it hides
   sibling defaults a later `when` depends on). Fixtures: false-`when` implicit default vs the same
   value given explicitly; a default whose `when` depends on another default declared later.
   **`when` on a node that does not exist yet** (deciding whether to create implicit defaults /
   non-presence containers): evaluate on a temporary dummy node inserted at the would-be position,
   exactly like `lyd_validate_dummy_when`. The dummy is never visible to callers.
   Open (M1): the full temporary evaluation view — whether an existing instance is replaced by the
   dummy, and how nodes contributed by augment/uses/choice/case are hidden while their own `when` is
   evaluated. Decide from libyang source + fixtures, not by assumption.
4. **`when` context node** (RFC 7950 §7.21.5): under `augment` → the augment's target node if it is a
   data node, else its closest data-node ancestor; under `uses`/`choice`/`case` → the closest data-node
   ancestor of the statement's node; otherwise the node itself.
5. **Evaluation order**: auto-deleting a node can make another `when` false, so conditions are
   re-evaluated until no change. Circular `when` dependencies must not be "accepted because stable":
   libyang rejects them at **schema compile** time (`lys_compile_unres_when_cyclic`,
   `src/schema_compile.c:457`, LYVE_SEMANTICS) — we do the same in `internal/compile`, so data
   evaluation never sees a cycle. M1 fixture: two leaves with mutually dependent `when`.
6. **Defaults are visible** to `must`/`when`: implicit defaults are materialised (flag `Default`)
   before `must`/`when` evaluation (this is where cambium diverges; see ADR 0002 cases 04/05/17).
7. **YANG functions**: `current()`, `deref()`, `derived-from()`, `derived-from-or-self()`,
   `re-match()` (XSD regex via internal/xsdre), `enum-value()`, `bit-is-set()`. Unknown function →
   compile-time error, never silent skip.
