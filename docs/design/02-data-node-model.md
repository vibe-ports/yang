# 02 — Data node model

Status: drafted in M0, revised after M1 (2026-10-08) to match package `data` (design 07). Reference:
libyang v5.8.6 `lyd_node_inner/term/any/opaq`, node flags `LYD_DEFAULT`, `LYD_WHEN_TRUE`, `LYD_NEW`,
`LYD_WHEN_FALSE` (`src/tree_data.h:799-803`).

## Requirements
- Instance order is data: user-ordered lists/leaf-lists keep insertion order and support moves;
  system-ordered ones are kept **sorted by value at insertion** (libyang `lyds`, design 07 §1.4),
  confirmed by the oracle (`eth1, eth0` prints as `eth0, eth1`).
- Keyless `config false` lists and duplicate `config false` leaf-lists must be representable →
  children are an ordered slice, **not** a map keyed by key values. The index is libyang's children
  hash table (`children_ht`): built once a parent has 4 children, kept afterwards, buckets in
  insertion order; lookups confirm with `types.Equal`, since canonical text alone is not equality
  across union members.
- Stable handles: `*Node` pointers stay valid across unrelated edits.
- Node kinds: container, leaf, leaf-list entry, list entry, anydata/anyxml, opaque (unknown or
  not-yet-schema-bound node from lenient parsing, needed for RPC envelopes and parse-only mode —
  libyang `lyd_node_opaq`). M1 has opaque nodes for the `unknown: opaque` policy; anydata/anyxml
  instances are `ErrUnsupported` until M5 (U-0043), operations until M4.
- Per-node flags exactly as libyang, because they carry validation history:
  - `Default` — implicit node added by validation, an NP container with only default descendants, or
    an explicit node tagged `ietf-netconf-with-defaults:default="true"` (the metadata is dropped);
    drives `report-all-tagged` / `trim`.
  - `WhenTrue` — all `when` conditions evaluated true at last validation. A node **with** this flag
    whose `when` turns false is auto-deleted; a node **without** it whose `when` is false is an error.
    This is the whole "fresh-invalid vs became-invalid" rule (review #2). Implicit nodes are created
    with it when their schema node has a `when` (design 03 rule 3).
  - `New` — created/changed since last validation; limits what the next validation revisits.
  - `WhenFalse` — (libyang 5.8.6 `LYD_WHEN_FALSE`) `when` evaluated false during a multi-error
    validation; the node is kept, invisible to XPath and skipped by the final pass (its musts,
    obsolete check, children, NP-container default), but still counted by its siblings'
    mandatory/min/max/unique checks; cleared only when its `when` is re-evaluated true (design 07 §2).
- anydata/anyxml payload (M5): libyang v5 holds only a data tree or a string (design 04 §2), not the
  four M0 variants (`Tree`, `XML`, `JSON`, `String`); M5 decides from that. Cross-format conversion
  that would lose information returns an explicit error.
- Metadata (RFC 7952) as an ordered list on the node: `ietf-origin:origin`, `yang:operation`,
  `nc:operation`, `yang:insert/key/value`, `ietf-netconf-with-defaults:default`. M1 stores
  annotations of `ietf-yang-metadata` and prints the with-defaults and implicit-diff metadata; origin
  inheritance is M4–M5. Opaque nodes keep unresolved attributes as text.

## Go shape (as built)
```go
type Node struct {
    schema *schema.Node   // nil for opaque
    parent *Node          // nil at the top level, and for a list instance still waiting for its keys
    tree   *Tree          // set for linked top-level nodes
    kids   siblings       // inner nodes only
    value  types.Value    // leaf / leaf-list
    opaq   *opaque        // name, prefix, module/namespace, value, format, hints, attributes
    flags  Flags          // Default | WhenTrue | New | WhenFalse (libyang's values)
    meta   []*meta        // annotation module, name, stored value; in order
    hkey   idxKey; hashed bool // bucket in the parent's children index
    inRB   bool           // in libyang's RB tree of a sorted run (lyds)
    links  *leafrefLinks  // LY_CTX_LEAFREF_LINKING records
}
type siblings struct {
    list []*Node          // schema nodes in libyang order (schema order, sorted runs)
    opq  []*Node          // opaque nodes, always after all schema nodes
    ht   map[idxKey][]*Node
    …                     // sorted/unsorted run bookkeeping
}
type Tree struct { set *schema.Set; top siblings; … } // pins the schema snapshot (U-0045)
```
- Inner children as slice: O(n) insert-in-middle is acceptable for v1 (user-ordered moves are
  rare); `ponytail:` revisit with an order-statistic structure if operational trees with 100k+
  siblings show up in benchmarks.
- Public API exposes iteration (`iter.Seq[*Node]`: `Tree.Top`, `Node.Children`, `Node.All`), path
  lookup and typed mutators (`NewPath`, `Find`, `Remove`, `Merge`); never the slice. A `Tree` is not
  safe for concurrent use (lookups build indexes lazily).

## Revised after M1
What the slice changed, and why:
- **System order is a sort at insertion, not at print.** libyang keeps system-ordered runs sorted
  (`lyds_insert`), and edits, merge and duplication depend on it, so the port keeps them sorted too;
  where libyang's run merging is shape-dependent or undefined, the port does a stable merge (D-0061,
  D-0062).
- **The index is the children hash table, not a per-list key map.** Duplicate detection and sibling
  lookups go through it, as in libyang; XPath steps still scan (`xpath.ChildLookup` over it is [#16](https://github.com/vibe-ports/yang/issues/16)).
- **Opaque nodes live in their own slice**, always after schema nodes; schema nodes stay in schema
  order even where libyang's `LYD_PARSE_ORDERED` or `lyd_insert_after` would place them after an
  opaque sibling (D-0058, not mirrored).
- **A list waits unlinked for its keys** (`parent == nil` while its children point to it), which is
  also why its error path starts at the list (design 07 §1.10).
- **`WhenFalse` was missing** from the M0 flag list; it decides multi-error results (design 07 §2).
- **The tree pins an immutable schema snapshot** instead of a live context, so trees need no lock
  against concurrent loads (U-0045).
- Duplicating subtrees fixes three libyang crashes or data losses instead of mirroring them (D-0065,
  D-0066, D-0067).
- **Open:** the report engine does not run `sequence` requests yet, so the flag history is checked
  only by Go tests replaying protocol-v2 `seq-*` goldens; `m1/sequence-when-auto-delete` is not
  compared yet.
