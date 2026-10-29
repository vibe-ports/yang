# Ordered input: `ParseOptions.Ordered` (#247)

`ParseOptions.Ordered` is `LYD_PARSE_ORDERED`. The parser selects `LYD_INSERT_NODE_LAST`
(parser_common.c:416-421), which `lyd_insert_node_last` implements: the node goes after the last
sibling, whatever it is (tree_data.c:742-775). No schema-order search and no `lyds` tree is made.
The flag means "trust the supplied order", not "sort the input". libyang calls misuse undefined
(parser_data.h:152-157), and the port does not sort or repair the input either.

Two kinds of tree result that sibling storage could not hold before:
- schema nodes out of schema order (`{"c":{"b":…,"a":…}}` keeps `b, a`);
- a schema node after an opaque sibling (`a, zz, b` with `zz` unknown, parsed opaque).

D-0058 loses its parse-order part, and its `lyd_insert_after` part stays (last section). Code
comments cite this document as "design 15".

## Entry points

`Parse` (JSON and XML) is the only entry point that takes `ParseOptions`. `ParseOp` has its own
options, and `lyd_parse_op` takes no parse options. The port has no LYB codec. `nodeInsert`
links an ordered node through `Tree.insert(…, insertLast)`. For a schema node that is `lastPos`,
which is shared with `lyd_dup_r`'s copies (`dupLink`). It appends after the last sibling and does
not search: no `insertPos`, so the old schema-order binary search for `insertLast` is gone. A
sorted run's leader that is in an RB tree becomes unsorted (`lyd_insert_node_last` adds nothing to
the tree). A later `lyds_insert` rebuilds the tree (`lyds_additionally_create_rb_tree`). Input
out of schema order then switches the set to `schemaUnordered`, as copies and moves already do
(`link`'s `markUnordered`).

## Storage: logical positions over a document-order sequence

Design 14 stores a set as one order-statistic sequence with the schema nodes in `[0, nschema)` and
the opaque ones after them. That coordinate system stays: a *logical* position is a rank among the
schema nodes, or `nschema` plus a rank among the opaque nodes. Spans, `At`, `Index`, run bounds
and the first-instance and index structures keep using it. The physical sequence holds document
order instead. A set is *interleaved* (`siblingStore.mixed`, journaled, never cleared) once a schema
node follows an opaque one. Until then logical and physical positions coincide, and every read and
write takes the code path it took before (the counted-work and scaling tests are unchanged).

- **opidx.** Each handle has an immutable kind (`Seq.SetWeigh`, schema = heavy) and a subtree count
  of heavy handles, maintained wherever `size` is and recorded in the handle's image.
  `PeekKind`, `PosKind`, `RankKind` (charged as `Rank`), `First/LastKind` and `PeekNextOfKind` are
  O(log n). *Kind threads* (`kprev`, `knext`, turned on by `SetKindThreads` and kept from then on)
  make `NextKind`/`PrevKind` O(1). Every edit relinks the handles it moves: a point edit with two
  climbs, a range edit in O(range + log n), `Keep` in one pass. The point, republication and `Keep`
  bounds include that relinking, and `KindThreadsBound` prices the one-time pass.
  `TestSequenceKindModel` checks the counts, threads, rollback and bounds against a slice model.
- **Facade** (`siblings_access.go`). In an interleaved set, logical reads translate. `at` and `peek`
  use `PeekKind`. `Index` and `orderPosition` use `PosKind`/`RankKind`. Spans, iterators and
  cursors step with the logical neighbour, which is the kind thread, and past the last schema
  node the first opaque one. `LowerBound` binary-searches kind ranks: O(log² n) visits, counted
  as sibling visits. A *document-order* span (`docSpan`, `docNodes`, `docFirst`/`docLast`/`docNext`,
  `all`, `All`, `reverseAll`) reads the physical sequence. The append that interleaves a set turns
  its kind threads on, paid by the insertion's bound (`interleaveBound`). `TestSiblingFacadeInterleavedModel`
  checks every read and write against a slice model.
- **Physical placement.** `link` takes a logical position. The physical anchor is the logical
  default (`storeAnchor`) unless the caller set a placement for that node (`setPlace`; `link` clears
  it):
  - a schema node at logical `i < nschema`: immediately before schema node `i`
    (`lyd_insert_before_node` of the anchor);
  - a schema node at `nschema`: after the last schema node, before a trailing opaque block
    (tree_data.c:785-793);
  - an opaque node at opaque index `j`: after opaque node `j-1`, appended when out of range
    (`lyd_insert_node_last`);
  - `placeEnd`: after the last sibling (`lastPos`, a payload forest);
  - `placeAfter` / `placeBefore`: right after or before a node. This covers the XML parser's
    `lyd_insert_after` of the last same-name opaque sibling (`lydxml_get_hints_opaq`), which may
    lie before a schema sibling or be the last opaque node; a sorted insertion's
    `lyds_link_data_node` after its RB predecessor (`interleavedPos`); and the next members of a
    moved run.
- **Anchors** (`interleavedPos`). Without a children table, or at the top level,
  `lyd_insert_get_next_anchor` walks the siblings and stops at the first opaque one
  (tree_data.c:546-589). When that opaque node comes before the schema anchor, it is the anchor,
  and the logical position becomes the number of schema nodes before it. With a table the anchor is
  the first instance of the next schema sibling (tree_data.c:520-535), the logical default.
- **Lookups.** `lyd_find_sibling_schema` without a children table stops at the first opaque sibling
  (tree_data_common.c:1126-1131). `schemaFind` and `schemaLookup` therefore find nothing past it
  (`linearBound`), and neither does `runBounds`'s leader. `lyd_validate_new` stops there too
  (validation.c:1023). `lyd_find_sibling_opaq_next` searches only the trailing opaque block
  (`opaqTail`).
- **Runs.** In an interleaved set an opaque node ends a run, as another schema node does
  (`LYD_LIST_FOR_INST`, `lyds_additionally_create_rb_tree`). The set switches to the unordered
  mode when it interleaves. There the first-instance records use document neighbours
  (`lyd_insert_hash_add`/`lyd_unlink_hash`: `docNeighbours`), the index's run gaps count opaque
  separators (`orderBuild`, `orderAddSchema`, `orderRemove`, `opaqueSplit`), table and record builds
  walk document order (`logicalInterleaved`), and `runBounds`, `runEnd` and `retainedRunEnd` cut a
  run at its first opaque node (`runPiece`, one O(log n) read).
- **libyang's sibling list.** Code that models `lyd_child`/`next`/`prev` reads document order:
  `firstChild`, `hasNext`, `nativeTail`, the key prefix (`positionalChildren`, `keyLinked`,
  `checkKeys`, `listPredicate`), the validation sibling lists (`topList`, `slicesOf`) and the
  diff/merge sibling lists.
- **Republication** (`replaceSchema`, `publishRun`) in an interleaved set republishes the run's
  physical extent. Opaque nodes keep their slots, and new members follow the extent.
  `republishBound` prices the extent and its assembly, and `publicationIndexBound` and
  `rbPublish`'s reservation include it. The parser never defers a run in an interleaved set
  (`appendSorted`).

## Evidence

- Oracle fixtures `protocol-v2/ordered-*` (21) cover:
  - parse order, JSON and XML;
  - the XML opaque anchor;
  - later insertions with and without a children table, crossing the table threshold, and a sorted
    one after an opaque node;
  - a run hidden past the first opaque node;
  - split leaf-list and list runs, with and without a table;
  - opaque appends; unlink and re-insert; merge; dup; diff; validation (implicit default, opaque
    error and the stop of `lyd_validate_new`);
  - XPath axes and positions; a key after an opaque child; a validated out-of-order parse.
  The `seq/trim-*` and `ops/opidx-split-run-leader-switch` fixtures, which parse ordered, now run.
  The request-differential `ordered-input` class runs against the live oracle.
- Go: `TestSequenceKindModel` (opidx), `TestSiblingFacadeInterleavedModel`,
  `TestSiblingFacadeInterleaveRollback`, `TestOrderedParseKeepsInputOrder`, `TestOrderedParseFunded`
  (every budget limit and cancellation point of interleaving ordered parses), and
  `TestInterleavedEditBudgetPoints` (NewPath, sorted insertion, Merge, Unlink and Dup on interleaved
  trees, with and without a table, refused at every charge boundary). The storage invariant also
  checks the kinds, the kind threads and the document-order run gaps.

## Remaining restriction

A merge keeps the leader it found for the following source instances of the same schema node
(`lyds_insert2`'s `leader_p`, tree_data.c:2755-2760; `lyds_insert2`, tree_data_sorted.c:1256-1279).
The port looks the leader up again per instance once the run has no RB tree (`sortedRunBatchable`).
The two differ only in a run split into pieces, by an opaque node or, in unordered copies, by
another schema node. There, a new run start in the first piece replaces its first-instance
record, and a re-lookup then finds the other piece. A merge of `k0, k3` into `k2 | zz | k1` puts
`k3` after `k1`, where libyang puts it after `k2`. `protocol-v2/ordered-split-list-hashed`
therefore merges only `k1, k3`. The same path serves unordered copies, so this predates this
change, and it is a follow-up.

## D-0058 after this change

The row keeps its id and keeps only `lyd_insert_after`. libyang links the node immediately after
the anchor. `InsertAfter` places:
- a schema node after an opaque anchor after the last instance of its run;
- an opaque node after a schema anchor first among the opaque nodes;
- a schema node after a schema anchor before the next schema node, which in an interleaved set is
  past any opaque node that follows the anchor.

The parse-order text, the `ordered-input` B class, the engine refusal and the engine's D-0058
allowlist entries are gone.
