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
    `lyds_link_data_node` after its RB predecessor, else before the leader (`sortedPlace`, set
    in `rbPos` before it reserves the link, and in a sorted forest move); the next members of a moved run; and a forest moved
    into an empty set; and a node put back after a temporary move, between its document
    neighbours (`originPlace`: `restoreLink` for `ValidateOp` and failed moves, its recovery
    reserved with the placement).
- **Anchors** (`interleavedPos`). Without a children table, or at the top level,
  `lyd_insert_get_next_anchor` walks the siblings and stops at the first opaque one
  (tree_data.c:546-589). When that opaque node comes before the schema anchor, it is the anchor,
  and the logical position becomes the number of schema nodes before it. With a table the anchor is
  the first instance of the next schema sibling (tree_data.c:520-535), the logical default. The XML
  parser's key-order check uses the same stop (`walkStop`).
- **Lookups.** `lyd_find_sibling_schema` without a children table stops at the first opaque sibling
  (tree_data_common.c:1126-1131). `schemaFind`, `schemaLookup` and the first-instance lookup they
  share with `opRun` and `runBounds` (`schemaLookupOperation`) therefore find nothing past it
  (`linearBound`). `lyd_validate_new` stops there too
  (validation.c:1023). `lyd_find_sibling_opaq_next` searches the trailing opaque block, or every sibling from
  an opaque first one (`opaqNextSpan`: `opaqNext` and XPath, charged per step to the calling
  operation, never to a tree it only reads, such as `MergeDiff`'s source). XPath's hashed child
  lookup (`LookupPrepared`, and for the top level the root adapter `xroot` as
  `EvalContext.TreeLookup`) follows the same stops, matching a sibling of another context by
  module and node name as the linear lookup does (`TestRootLookupMixedContexts`), and a children-table miss ends in that
  fallback, not in the evaluator's scan of every child.
- **Undefined merge refused.** From an opaque first sibling `lyd_find_sibling_opaq_next` compares
  `LYD_NAME`, so an opaque source node can find a data node of its name. libyang then merges an
  opaque value into a data node, which crashes (native probe; `conformance/deviations.md`, libyang crash cases). `Merge` refuses it before any change with
  LY_EINVAL "Cannot merge opaque node "x" into data node "x"." (`TestMergeOpaqueIntoDataNodeRefused`).
- **Value changes.** `LYD_NODE_IS_ALONE` reads the document neighbours, the first sibling's
  cyclic predecessor included, so a leaf-list instance alone in its piece of a split run keeps its
  place when its value changes.
- **Runs.** In an interleaved set an opaque node ends a run, as another schema node does
  (`LYD_LIST_FOR_INST`, `lyds_additionally_create_rb_tree`). The set switches to the unordered
  mode when it interleaves. There the first-instance records use document neighbours
  (`lyd_insert_hash_add`/`lyd_unlink_hash`: `docNeighbours`), the index's run gaps count opaque
  separators (`orderBuild`, `orderAddSchema`, `orderRemove`, `opaqueSplit`), table and record builds
  walk document order (`logicalInterleaved`), unlinking an opaque separator marks the left run
  unsorted (its RB tree does not take the right piece), Merge never batches a run (each
  insertion selects its leader again), and `runBounds`, `runEnd`, `retainedRunEnd` and the
  other run walks stop at the run's opaque neighbours (`runPiece`, `runPieceStart`: one O(log n)
  read each) before they read the run, so a walk costs its piece, not the set
  (`TestSplitRunInsertScaling`).
- **libyang's sibling list.** Code that models `lyd_child`/`next`/`prev` reads document order:
  `first` (`lyd_first_sibling`), `firstChild`, `hasNext`, `nativeTail`, the key prefix (`positionalChildren`, `keyLinked`,
  `checkKeys`, `listPredicate`), the validation sibling lists (`topList`, `slicesOf`), the
  diff/merge sibling lists and a batch removal's virtual links (`unlinkAllDefaults`).
- **Republication** (`replaceSchema`, `publishRun`) in an interleaved set republishes the run's
  physical extent. Opaque nodes keep their slots, and new members follow the extent.
  `republishBound` prices the extent and its assembly, and `publicationIndexBound` and
  `rbPublish`'s reservation include it. The parser never defers a run in an interleaved set
  (`appendSorted`).

## Evidence

- Oracle fixtures `protocol-v2/ordered-*` (34) cover:
  - parse order, JSON and XML;
  - the XML opaque anchor;
  - later insertions with and without a children table, crossing the table threshold, and a sorted
    one after an opaque node;
  - a run hidden past the first opaque node;
  - split leaf-list and list runs, with and without a table, including an insertion before the
    second piece's leader;
  - a set that lost its opaque node; an ordered forest whose first root is opaque, moved whole;
  - freeing a split run's second-piece leader; a merge from an opaque first sibling; a positional
    path past the first opaque sibling; Merge (no `LYD_MERGE_DESTRUCT`) into a split run;
  - a merge after the separator is gone; a sorted forest moved next to an opaque node; XPath name
    tests past the first opaque sibling, at the top level, and after a children-table miss with an
    opaque node of the name in the middle; value changes of singleton pieces of a split run;
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

## Merge and the destructive merge

`Tree.Merge` is `lyd_merge_siblings` without options. Each new instance goes through
`lyd_insert_node` with a fresh leader lookup. The oracle's `edit merge` step uses
`LYD_MERGE_DESTRUCT`, which pools the source's RB nodes and keeps the leader of the previous
instance (`lyds_insert2`, tree_data.c:2721-2760). The two agree unless a run is split. In
`k2 | zz | k1`, merging `k0, k3` puts `k3` after `k1` without `DESTRUCT`, which is the port's and
libyang's result (`protocol-v2/ordered-split-list-merge-into`, a `merge_into` step), and after
`k2` with it. The port exports no destructive merge, so `protocol-v2/ordered-split-list-hashed`
merges only `k1, k3` through the `edit merge` step.

## D-0058 after this change

The row keeps its id and keeps only `lyd_insert_after`. libyang links the node immediately after
the anchor. `InsertAfter` places:
- a schema node after an opaque anchor after the last instance of its run;
- an opaque node after a schema anchor first among the opaque nodes;
- a schema node after a schema anchor before the next schema node, which in an interleaved set is
  past any opaque node that follows the anchor.

The parse-order text, the `ordered-input` B class, the engine refusal and the engine's D-0058
allowlist entries are gone.
