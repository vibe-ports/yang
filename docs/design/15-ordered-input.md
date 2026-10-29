# Ordered input: `ParseOptions.Ordered` (#247)

`ParseOptions.Ordered` is `LYD_PARSE_ORDERED`. The parser selects `LYD_INSERT_NODE_LAST`
(parser_common.c:416-421), which `lyd_insert_node_last` implements: the node goes after the last
sibling, whatever it is (tree_data.c:742-775). No schema-order search and no `lyds` tree is made.
The flag means "trust the supplied order", not "sort the input". libyang calls misuse undefined
(parser_data.h:152-157), and the port does not sort or repair the input either. Two kinds of tree
result that sibling storage could not hold before: schema nodes out of schema order
(`{"c":{"b":…,"a":…}}` keeps `b, a`), and a schema node after an opaque sibling
(`a, zz, b` with `zz` unknown, parsed opaque). D-0058 loses its parse-order part. Its
`lyd_insert_after` part stays (below). Code comments cite this document as "design 15".

## Entry points

`Parse` (JSON and XML) is the only entry point that takes `ParseOptions`. `ParseOp` has its own
options, and `lyd_parse_op` takes no parse options. The port has no LYB codec. The internal
`parseOpts.ordered` already routes `nodeInsert` to an append. That append now goes through the
storage change below, not `insertPos`'s schema-order search.

## Out-of-schema-order schema nodes

These need no new storage state. A parsed schema node is linked at the end of the schema nodes.
When that breaks schema order, `link` already calls `markUnordered`, the mode that copies and moves
use for the same thing. In that mode anchors follow `lyd_insert_get_next_anchor`, runs are found by
schema identity, and a run may come back later in the set. `insertPos` (and its `insertLast`
binary search, which forced schema order) is bypassed, as `dupLink` already bypasses it for
`lyd_dup_r`'s `LYD_INSERT_NODE_LAST`. A sorted run's leader that is in an RB tree becomes unsorted,
as in `dupLink` (`lyd_insert_node_last` adds nothing to the tree). A later
`lyds_insert` then rebuilds it (`lyds_additionally_create_rb_tree`), which the port models already.
The two LAST paths share one helper, `Tree.linkLast`.

## Schema nodes after opaque nodes: interleaved storage

Design 14 stores a set as one order-statistic sequence with the schema nodes in `[0, nschema)` and
the opaque ones after them. About 300 call sites use that coordinate system: `SchemaLen`,
`schemaSpan`, `opaqueSpan`, `opaqueAt`, `At(i)` for a schema position, `LowerBound` over the schema
prefix, and run bounds. They are correct when they speak about the schema nodes in order, and about
the opaque ones in order. The design keeps that coordinate system and separates it from the
physical one.

- **Logical positions are unchanged.** `[0, SchemaLen())` are the schema nodes in their storage
  order, and `[SchemaLen(), Len())` are the opaque nodes in theirs. Every existing span, position,
  run and index computation keeps its meaning.
- **The physical sequence is document order.** It may interleave the two kinds. A set whose
  sequence has a schema node after an opaque one is *interleaved* (`siblingStore.mixed`, a
  journaled counter set once and never cleared). In a set that is not interleaved, logical and
  physical positions coincide, and every read and write takes exactly the code path it takes now.
  Work, charging and counted expectations are unchanged.
- **opidx kind augmentation.** Every handle carries an immutable kind bit, set at creation by the
  sequence's weigh function (schema or opaque). It also keeps a subtree count of heavy (schema)
  handles, maintained wherever `size` is (fixNode, split, build, detach, creation) and recorded in
  the handle's journal image. New reads: `PeekKind(rank, heavy)` (select among one kind, levels
  counted), `PosKind(h)`, `NextKind`/`PrevKind` (the next handle of the same kind: O(1) when it is
  adjacent, otherwise one O(log n) climb and descent) and `HeavyLen`. `CheckInvariants` verifies the
  counts.
- **Translation in the facade** (`siblings_access.go`), used only when `mixed` is set:
  - `at`/`At`/`opaqueAt`: `PeekKind`.
  - `Index` and `orderPosition`: `PosKind` (an opaque node adds `nschema`). `orderPosition`
    keeps its read credit, since the climb is the same height.
  - Spans, iterators and cursors walk with the logical neighbour: the next handle of the same kind,
    and past the last schema handle the first opaque one. `LowerBound` binary-searches kind ranks
    with `PeekKind` probes. That is O(log² n) visits, counted as sibling visits like `BoundIn`'s
    levels.
  - Raw neighbour reads outside the facade (the unlink plan, the op index's gap accounting,
    `rb_remove`, leader updates, edit neighbours, `path.go` and `diff.go` run walks) go through
    facade helpers `prevOf`/`nextOf` with the same logical meaning. In a set that is not
    interleaved, these helpers return the physical neighbour.
- **Physical placement of writes** follows libyang's anchors:
  - A schema node at logical `i < SchemaLen()` goes immediately before schema node `i`.
    `lyd_insert_node_ordby_schema` uses the anchor's `lyd_insert_before_node`, so opaque nodes
    before the anchor stay before the new node.
  - A schema node at `SchemaLen()` goes after the last schema node. That is "cannot insert data
    node after opaque nodes", before the trailing opaque block (tree_data.c:785-793), and the
    current placement.
  - `linkLast` (LYD_INSERT_NODE_LAST) appends at the physical end. When an opaque node precedes it,
    the set becomes interleaved.
  - An opaque node at opaque index `j > 0` goes immediately after opaque node `j-1`. That is the XML
    parser's `lyd_insert_after` of the last same-name opaque sibling (parser_xml.c:520-545),
    `lydxml_get_hints_opaq`, which searches backwards over all siblings. At `j = 0` it goes before
    the first opaque node. At the end it goes to the physical end, which is `lyd_insert_node_last`
    for every opaque insertion.
  - Removal removes the handle. `deleteMatching` picks the predicate by the node's kind, not its
    position.
  - A non-owning `view` takes its nodes in document order and is interleaved when they are.
- **Document-order traversal** (`siblings.all`, `reverseAll`, the `siblingCursor`) walks the
  physical sequence. That covers `Children`, `Top`, `ChildrenAny`, the printers (which take the
  `all()` slice), XPath (`Children`), diff, compare and dup sources. Walks that iterate
  `SchemaAll` and then `opaqueAll` and only need the set, not its order, stay as they are.
- **Dup** keeps `lyd_dup_r`'s LAST order through `linkLast`. A copy of an interleaved parent is
  interleaved too, and an opaque child keeps its place. For a source that is not interleaved, the
  result is the same as now, because its children arrive schema first. A `DupSiblings` copy of the
  next instances of a (leaf-)list into a parent with opaque children now follows the copy's physical
  append, as libyang does.
- **Republication** (`replaceSchema`, `publishRun`) in an interleaved set republishes the run's
  physical extent. Opaque nodes inside it keep their physical slots, and the run's members fill the
  schema slots in their new order. Fresh members of `publishRun` go after the extent's last
  slot. Work is the extent's `RepublishBound`, so the opaque nodes inside it are charged.

### Remaining restriction (tested, not a deviation of defined behaviour)

`LYD_PARSE_ORDERED` input whose (leaf-)list run is split by an opaque sibling, such as
`<ll>1</ll><zz/><ll>2</ll>` in XML, is undefined in libyang. libyang's `LYD_LIST_FOR_INST` stops at
the opaque node there. The port's run machinery (instance counts, position predicates, sorted
insertion) treats opaque nodes as absent, so the instances form one run. Traversal and printing still
follow document order. A Go test pins the port's result.

## D-0058 after this change

The row keeps its id and keeps only `lyd_insert_after`: `InsertAfter(anchor, n)` places a schema
node `n` after the last instance of its own run. libyang links it immediately after the anchor.
The two differ when an opaque sibling is the anchor or follows it, and when the anchor is not the
last instance of its run. The parse-order text, the "ordered parse option is not exported" text,
the `ordered-input` B class, the engine refusal and the engine's D-0058 allowlist entries for
ordered parses go. The `protocol-v2/ordered-*` fixtures and `seq/trim-*` runs that parse ordered
then run against their goldens.

## Evidence plan

- opidx: model tests of the kind counts under every edit and rollback, and of `PeekKind`, `PosKind`,
  `NextKind` and `PrevKind` against a slice model, in random interleavings (extending
  TestSequenceRandomizedTransactions and the edit-script model); `CheckInvariants` covers the
  counts.
- data: a facade model test over an interleaved set (logical spans, positions and walks against a
  model, document-order `all`), and storage invariants extended to the kind counts. Go tests of
  ordered parses in JSON and XML: out of schema order, an opaque node between schema nodes, the XML
  opaque anchor across a schema node, an opaque-split run. Then on each result: traversal
  (`Children`, `Top`, XPath), JSON/XML printing, a later default insertion (before an anchor and at
  the end, with trailing opaque nodes), `InsertBefore`, `Unlink`, `Dup` (interleaving kept),
  `Validate`, `Diff` against an ordered and an unordered tree, `Merge` into it, and the rollback of
  each refused edit (budget sweeps).
- Conformance: `protocol-v2/ordered-schema-order`, `ordered-opaque-between`, the `seq/trim-*`
  ordered fixtures, and the request-differential `ordered-input` class (39 single-field requests)
  against the live oracle. Goldens come only from the oracle tooling.
- `TestStorageMiddleEditScaling`, the budget sweeps and the counted-work tests are unchanged for
  sets that are not interleaved. A scaling test bounds an interleaved set's middle edits and walks
  at O(log n) and O(log² n) per operation.
