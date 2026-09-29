# 03 — XPath evaluation context

Status: draft (M0). Reference: RFC 7950 §6.4.1, §7.5.3, §7.21.5, §9.9.2; RFC 8342 §6.1;
libyang v5.8.6 `LYXP_NODE_ROOT` / `LYXP_NODE_ROOT_CONFIG` (`src/xpath.h:168`),
`lyd_validate_dummy_when()` (`src/validation.c:1083`).

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
1. **Prefix resolution** uses the module where the expression is *written*: an expression inside a
   grouping resolves prefixes against the grouping's module, even when the grouping is used from
   another module; unprefixed names in YANG 1.1 belong to the defining module (RFC 7950 §6.4.1).
   The compiler stores (expression, defining module) pairs; evaluation never guesses.
2. **Accessible tree** (RFC 7950 §6.4.1):
   - config true node → all config true data in the datastore (+ `RootConfig`);
   - config false node → all config + state data;
   - RPC/action input/output, notification → the operation tree plus the whole datastore
     (libyang takes the external operational tree for this, `-O` in yanglint);
   - NMDA (RFC 8342 §6.1): for `<operational>` the accessible tree is the operational datastore.
3. **`when` on a node that does not exist yet** (deciding whether to create implicit defaults /
   non-presence containers): evaluate on a temporary dummy node inserted at the would-be position,
   exactly like `lyd_validate_dummy_when`. The dummy is never visible to callers.
4. **`when` context node** (RFC 7950 §7.21.5): under `augment` → the augment's target node if it is a
   data node, else its closest data-node ancestor; under `uses`/`choice`/`case` → the closest data-node
   ancestor of the statement's node; otherwise the node itself.
5. **Evaluation order**: `when` conditions are resolved to a fixpoint — auto-deleting a node can make
   another `when` false; libyang iterates until no change. Same loop in Go, with the budget as guard.
6. **Defaults are visible** to `must`/`when`: implicit defaults are materialised (flag `Default`)
   before `must`/`when` evaluation (this is where cambium diverges; see ADR 0002 cases 04/05/17).
7. **YANG functions**: `current()`, `deref()`, `derived-from()`, `derived-from-or-self()`,
   `re-match()` (XSD regex via internal/xsdre), `enum-value()`, `bit-is-set()`. Unknown function →
   compile-time error, never silent skip.
