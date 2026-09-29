# 02 — Data node model

Status: draft (M0). Reference: libyang v5.8.6 `lyd_node_inner/term/any/opaq`, node flags
`LYD_DEFAULT`, `LYD_WHEN_TRUE`, `LYD_NEW` (`src/tree_data.h:799-801`).

## Requirements
- Instance order is data: user-ordered lists/leaf-lists keep insertion order and support moves;
  system-ordered ones are printed in libyang's order (compare against oracle, not assumed).
- Keyless `config false` lists and duplicate `config false` leaf-lists must be representable →
  children are an ordered slice/linked list, **not** a map keyed by key values. A key index
  (map) is an optional accelerator built lazily per list.
- Stable handles: `*Node` pointers stay valid across unrelated edits.
- Node kinds: container, leaf, leaf-list entry, list entry, anydata/anyxml, opaque (unknown or
  not-yet-schema-bound node from lenient parsing, needed for RPC envelopes and parse-only mode —
  libyang `lyd_node_opaq`).
- Per-node flags exactly as libyang, because they carry validation history:
  - `Default` — implicit node added by validation, or explicit node equal to default when parsed
    with with-defaults tagging; drives `report-all-tagged` / `trim`.
  - `WhenTrue` — all `when` conditions evaluated true at last validation. A node **with** this flag
    whose `when` turns false is auto-deleted; a node **without** it whose `when` is false is an error.
    This is the whole "fresh-invalid vs became-invalid" rule (review #2).
  - `New` — created/changed since last validation; limits what the next validation revisits.
- anydata/anyxml payload variants: `Tree` (schema-bound subtree), `XML` (opaque XML with namespaces
  preserved), `JSON` (raw JSON value), `String`. Cross-format conversion that would lose
  information returns an explicit error.
- Metadata (RFC 7952) as an ordered list on the node: `ietf-origin:origin`, `yang:operation`,
  `nc:operation`, `yang:insert/key/value`, `ietf-netconf-with-defaults:default`.

## Go shape (sketch)
```go
type Node struct {
    schema   *schema.Node   // nil for opaque
    parent   *Node
    children []*Node        // ordered; inner nodes only
    value    Value          // leaf / leaf-list
    any      *AnyPayload    // anydata / anyxml
    opaq     *Opaque        // name, module, prefix ctx, attributes for opaque nodes
    meta     []Meta
    flags    nodeFlags      // Default | WhenTrue | New
}
```
- Inner children as slice: O(n) insert-in-middle is acceptable for v1 (user-ordered moves are
  rare); `ponytail:` revisit with an order-statistic structure if operational trees with 100k+
  siblings show up in benchmarks.
- Public API exposes iteration (`iter.Seq[*Node]`), path lookup and typed mutators; never the slice.
