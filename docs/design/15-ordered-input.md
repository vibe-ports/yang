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
  (tree_data_common.c:1126-1131). `schemaFind`, its foreign-name scan (`foreignScan`, also behind
  `sortedLeaderIndex`), `schemaLookup` and the first-instance lookup they
  share with `opRun` and `runBounds` (`schemaLookupOperation`) therefore find nothing past it
  (`linearBound`). `lyd_validate_new` stops there too
  (validation.c:1023). `lyd_find_sibling_opaq_next` searches the trailing opaque block, or every sibling from
  an opaque first one (`opaqNextSpan`: `opaqNext` and XPath, charged per step to the calling
  operation, never to a tree it only reads, such as `MergeDiff`'s source). XPath's hashed child
  lookup (`LookupPrepared`, and for the top level the root adapter `xroot` as
  `EvalContext.TreeLookup`) follows the same stops, matching a sibling of another context by
  module and node name as the linear lookup does (`TestRootLookupMixedContexts`), and translating
  a keyed probe into the siblings' context as `lyd_compare_single` compares equivalent schemas
  (`TestInterleavedLookupForeignInstance`), and a children-table miss ends in that
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

- Oracle fixtures `protocol-v2/ordered-*` (61; `ordered-split-list-merge-destruct` is unsupported,
U-0113) cover:
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
    opaque node of the name in the middle; value changes of singleton pieces of a split run; an insertion from another schema context next
    to a hidden run;
  - opaque appends; unlink and re-insert; merge; dup; diff; validation (implicit default, opaque
    error and the stop of `lyd_validate_new`);
  - XPath axes and positions; a key after an opaque child; a validated out-of-order parse.
  - JSON metadata past an opaque separator, on an opaque first match, and before the data; an
    alone leader unlinked and re-inserted; sorted insertions into a split run across the table
    threshold; a local insertion whose first sibling is opaque while the first schema node is
    foreign.
  - a Merge into a list run split by a schema node; XPath in a table level of two contexts; a
    diff insertion after an anchor followed by an opaque node (D-0058); module validation of
    duplicates and of a leafref after an opaque root; a replaced case holding the module's first
    node; a diff of another context applied past an opaque sibling; top-level leaf-list defaults
    past an opaque root; a list key run cut by an opaque child; a default in a top level out of
    module order; empty leaf-lists printed past an opaque node; lookups, an update, a
    duplicate-instance run and an insertion anchor in a table level of two contexts.
  The `seq/trim-*` and `ops/opidx-split-run-leader-switch` fixtures, which parse ordered, now run.
  The request-differential `ordered-input` class runs against the live oracle.
- Go: `TestSequenceKindModel` (opidx), `TestSiblingFacadeInterleavedModel`,
  `TestSiblingFacadeInterleaveRollback`, `TestOrderedParseKeepsInputOrder`, `TestOrderedParseFunded`
  (every budget limit and cancellation point of interleaving ordered parses), and
  `TestInterleavedEditBudgetPoints` (NewPath, sorted insertion, Merge, Unlink and Dup on interleaved
  trees, with and without a table, refused at every charge boundary). The storage invariant also
  checks the kinds, the kind threads and the document-order run gaps. `TestAloneLeaderUnlink` compares single and batch
  unlink and reinsertion, and `TestForeignFirstSchemaHashed` covers the physical first sibling's context.
  `TestForwardMetadataScaling` and `TestForwardMetadataCancel` cover the metadata index, and
  `TestSchemaSplitMergeUnbatched` covers a Merge after an insertion into a split run.

## Sweep of sibling reads (gate rounds 3 and 4)

Rule: where libyang reads `node->prev`/`node->next`, `LYD_CTX(first_sibling)` or iterates
siblings (`LY_LIST_FOR`, `LYD_LIST_FOR_INST`, `lyd_first_sibling`, `lyd_find_sibling_*`), the port
reads document order in an interleaved set, unless the result is the same for every interleaving.
The logical view is schema nodes in document order, then opaque nodes in document order. So a
walk that skips opaque nodes without stopping at them sees the same nodes in the same order.

Read in document order (fixed):
- round 3: `instanceRun` (`docFrom`), `visibleNeighbours`, `removalOrder`'s linear leader scan
  (`linearBound`), `splitOpRun`;
- round 4:
  - single unlink reads `node->prev`/`node->next` directly (`prepareUnlink`;
    `LYD_NODE_IS_ALONE` keeps an alone leader's RB metadata on the detached node);
  - JSON metadata after the data couples with document successors (`metaAttr`);
  - JSON metadata selects its target among schema and opaque matches in document order
    (`parseAttribute`);
  - JSON metadata before the data numbers schema and opaque matches together
    (`metadataFinish`). The siblings are indexed once in document order. That pass is charged
    to the parse's enforced budget and polls its context;
  - `LYD_CTX(first_sibling)` and `LYD_CTX(siblings)` read the physical first sibling, which may
    be opaque (`crossContextInsertPos`, `unorderedAnchor`, the foreign branches of
    `lyd_find_sibling_first`);
  - the cross-context table probe returns the bucket's first record (`leaderFirstMixed`);
  - `runBounds` cuts a gap-free operation or unordered run at its piece;
- round 16: the table primitive reads only the bucket's candidates, the equivalents of the
  schema node in the set's few contexts (`equivalents`). Each equivalent is found level by level:
  the parent's equivalent first, then that parent's child index in the target set, keyed by node
  type, module name and node name (`equivalentIn`, `schema.Set.ChildByName`). An index is built
  per parent on first use and paid before it is built; each level is paid through the caller's
  charge (a diff's lookup quota, a mutation's hash quota, XPath's step budget; read-only calls
  charge nothing), and a refusal is returned, never taken for a miss. Memory is linear in the
  schema, holds only the target set's nodes, and the cost is O(depth) (`TestEquivalentInColdBudgetAndDepth`,
  `TestEquivalentInOutputAndScaling`, `TestEquivalentCacheKeepsNoContext`). `leaderFirstMixed`
  uses the same candidates.
- round 18 (class-wide cross-context audit): a foreign match is chosen from the contexts of the
  schema nodes the siblings hold (`schemaContext`), not from an opaque first sibling or the
  parent (`findFirstWork`, `keylessFirst`, the XPath lookups, `FirstChild`); a probe of another
  context is compared with the candidate directly, as `lyd_find_sibling_first` does
  (`xprobe.crossEqual`, no translation through `lyd_find_schema_ctx`, so outputs are found); the
  instance lookups apply `lyd_find_sibling_first`'s schema-parent check on the physical first
  sibling, skipped when it is opaque (`parentMismatch`); schema-only lookups inside a mutation
  pay its quota. `findSchemaCtx` stays the model of `lyd_find_schema_ctx` for dup.
  (`TestHomogeneousForeignBehindOpaque`, `TestForeignOutputXPath`, `TestFirstSiblingParentGuard`,
  `TestMutationSchemaLookupCharged`, `TestThreeContextRegistrationOrder`). A lookup no longer scans every schema of the level. A mutation charges its steps
  to its own counter (Merge, ApplyDiff, edits); a diff charges its lookup quota; read-only calls
  charge nothing (`TestMixedTableMergeScaling`: flat work per schema, refusal at every charge
  point);
- round 15: one children-table primitive, `tableFirst`, mirrors `lyd_find_sibling_schema` and
  `lyd_find_sibling_first`'s table branch out of schema order. It reads the bucket of each
  equivalent schema node, takes the earliest registration, and matches by
  `lyd_hash_table_val_equal`: a list by its keys, a leaf-list by its value, any other node by its
  schema alone. Schema lookups (`tableSchemaFirst`), instance lookups (`findFirstMixedTable`,
  which compares a real target itself), XPath, the printer and insertion anchors all go through
  it; the anchor's walk is charged to the mutation (`TestTableLookupCallersAgree`);
- round 14:
  - `tableSchemaFirst` charges only inside an operation that runs a lookup quota (a diff), and
    its exhaustion is a miss with that operation's error, never another answer; read-only
    calls (Find, Print*, iteration) write no shared state (`TestMixedTableReadsShareNothing`,
    concurrent under -race);
  - a probe of another context is translated only when its schema compares equal with all the
    parents, as `lyd_compare_single` checks (`ordered-xpath-foreign-lineage`: c/l directly under
    c against c/l in a choice/case);
- round 13 (exhaustive audit):
  - in every set out of schema order (interleaved or `schemaUnordered`), lookups among
    siblings of several contexts with a children table follow the table's registration order
    and native boundaries:
    - schema lookups take the bucket's first registered equivalent record (`tableSchemaFirst`,
      also for XPath and `FirstChild`);
    - instance lookups take the first registered equal instance, and a duplicate-instance list
      walks only the run of its record (`findFirstMixedTable`);
    - insertion anchors take the records (`crossContextInsertPos`);
  - the JSON printer's empty-(leaf-)list check uses `lyd_find_sibling_schema` (`nextEmpty`);
  - `exposeListKeys` and `resetListKey` walk only up to the first opaque child; `prepareUnlink`
    reserves the exposure that removing that opaque child causes;
  - `unorderedAnchor` charges every step and stops at the first opaque sibling;
  - `termHashBound` reserves only the key run after the insertion;
  - the forward-metadata merge polls the context, also on a reused index;
- round 12: every top level out of schema order, interleaved or only `schemaUnordered`, takes
  the one module-relative implicit insertion (`modInsertInterleaved`). libyang's walks from the
  module's first node read the physical order with or without opaque nodes
  (`ordered-implicit-reversed-modules`). The other branches on `interleaved()` choose between
  logical and document order, which are the same order when the set is not interleaved;
- round 11: a list's key run is its leading key children in document order, stopping at an
  opaque or non-key child, as `lyd_hash` reads it; ParseOnly skips `checkKeys`, so an opaque child
  can sit between keys (`ordered-list-key-past-opaque`). `keyRun` (the logical run `keyN` cut at
  the first opaque child) sets the hashed prefix on link and reset. `keyPrefix` (the schema
  children before the first opaque one) bounds every key walk: the key hashes,
  `rb_compare_lists` and its D-0121 profile, `HashKeyValuesEqual`, the instance keys of
  comparisons, the dup and diff key loops, and the key-change and hash bounds. An unlink that
  joins the run does not rehash (`TestKeyRunPastOpaque`);
- round 10: an implicit sorted instance whose leader exists takes the ordinary sorted insertion
  (lyds_insert) also in an interleaved top level; only without a leader does the anchor walk
  from the module's first node place it;
- round 8:
  - an auto-deletion moves lyd_validate's first on before it unlinks the node, as
    `lyd_del_move_root` does (`delMoveRoots`, for choice cases and defaults). libyang frees each
    node right after; the port unlinks the batch afterwards in one compaction, so the root
    update counts the batch members before the node as freed. An old case kept out of schema
    order (b, a) then moves first past a to the replacement (round 9);
  - a Present run's first node that an earlier module's validation deleted is found again;
  - the lookup fallbacks of other contexts honour the linear walk's stop at the first opaque
    sibling (`diff_gen`'s lineage fallback, `foreignSchemaFind` without a table);
- round 7: top-level validation follows `lyd_validate` in document order:
  - each module starts at its first node (`lyd_mod_next_module`, `moduleFirst`);
  - `lyd_validate_new` walks from that node to an opaque node or another module's node;
  - `modStart`, the implicit-node walk and the subtree walk use document positions;
  - an implicit top-level node is placed by the anchor walk from the module's first node, and
    `first` moves only when the node lands right before it (`modInsertInterleaved`);
  - `FirstChild` charges its table lookup to the evaluation (the `FirstLookup` charge);
- round 6: XPath child lookups in an interleaved set with a children table, either holding
  siblings of several contexts or looking up a duplicate-instance list, read the table's
  buckets of every equivalent schema node (`tableFirst`), then `lyd_find_sibling_opaq_next`.
  They no longer decline to the evaluator's scan of all children. `FirstChild` does the same
  for its mixed-context case, and a probe that cannot be stored in the siblings' context misses
  (`TestInterleavedLookupMatrix`: table present or absent, one or two contexts, a probe of
  either context, the document root);
- round 5: Merge does not batch copies into a run that has several pieces, even when the
  pieces are split by schema nodes only (`sortedRunBatchable`, the run's gaps). An insertion
  can move a piece's first-instance record, and the next copy then selects its leader again;
- earlier rounds: run bounds and walks (`runPiece`, `runPieceStart`), first-instance records and
  index gaps (`docNeighbours`, `opaqueSplit`), lookups (`linearBound`, `foreignScan`),
  predicates, the printers' sibling lists and every `lyd_first_sibling` read (`docSpan`, `all`).

Kept logical, with the proof of equivalence:
- Positions that `link` places physically. Anchor position `at` links right before
  `At(at)`. The append position links right after the last schema node, which is where
  `lyd_insert_node_ordby_schema` goes past the trailing opaque block. The linear anchor walks
  (`unorderedAnchor`, `opAnchor`, `crossContextInsertPos`) decide only on the schema prefix
  before the first opaque node. Any result past that prefix is replaced by that opaque node
  (`walkStop`). Sorted links place after the RB predecessor (`sortedPlace`). Origins use
  `originPlace`. `insertBefore` links before its anchor.
- Lookups that skip opaque nodes and never stop at them. `has`, `dupIndex`,
  `foreignInstances`, the dup-instance set (`lyd_find_sibling_dup_inst_set`'s no-table branch
  compares every sibling, and opaque and schema nodes never match), `opInst`, `allInstances`.
  The opaque-only scans `opqFirst`, `opaqueFirst` and `opaqueLast` (`lydxml_get_hints_opaq`'s
  backward walk) see opaque nodes in document order.
- Context routing: `mixedSortedLookup`, `foreignFor`, `mixedSwitch` and the refusal modes.
  Opaque nodes are never candidates of `lyd_find_sibling_schema`, and `contexts` counts only
  schema nodes.
- Comparisons: `compareSiblingFrames` walks document order. The narrowing digest is built over
  the logical order. Pairwise-equal document sequences partition into pairwise-equal logical
  ones, so the digest never rejects an equal pair.
- Order-free scans: hash and work bounds, binding and capture stacks, free order, test-only
  record checks.
- Sets that are never interleaved:
  - `markUnordered`'s record materialization runs before the first out-of-order link, while
    the set is still in schema order;
  - the parser's deferred runs (`appendSorted` refuses an interleaved set, and a parse without
    `Ordered` never interleaves);
  - `nextAnchor`'s ordered branch (an interleaved set is `schemaUnordered`);
  - the eventTime check (operation parses take no `Ordered`);
  - unindexed singleton sets.

The XML parser couples attributes to their own element and reads no sibling. Both printers walk
`all()`.

## Merge and the destructive merge

`Tree.Merge` is `lyd_merge_siblings` without options. Each new instance goes through
`lyd_insert_node` with a fresh leader lookup. The oracle's `edit merge` step uses
`LYD_MERGE_DESTRUCT`, which pools the source's RB nodes and keeps the leader of the previous
instance (`lyds_insert2`, tree_data.c:2721-2760). The two agree unless a run is split. In
`k2 | zz | k1`, merging `k0, k3` puts `k3` after `k1` without `DESTRUCT`, which is the port's and
libyang's result (`protocol-v2/ordered-split-list-merge-into`, a `merge_into` step), and after
`k2` with it. The port exports no destructive merge (U-0113): the conformance engine reports an `edit merge`
step unsupported when a destination run is split and at least two source instances of it are new
(`protocol-v2/ordered-split-list-merge-destruct`, whose golden shows `k3` after `k2`).

## D-0058 after this change

The row keeps its id and keeps only `lyd_insert_after`. libyang links the node immediately after
the anchor. `InsertAfter` places:
- a schema node after an opaque anchor after the last instance of its run;
- an opaque node after a schema anchor first among the opaque nodes;
- a schema node after a schema anchor before the next schema node, which in an interleaved set is
  past any opaque node that follows the anchor.

`protocol-v2/ordered-diff-insert-after-opaque` measures the last case through ApplyDiff. In
`u1, zz, u2`, inserting `u3` after `u1` gives libyang `u1, u3, zz, u2` and the port
`u1, zz, u3, u2`.

The parse-order text, the `ordered-input` B class, the engine refusal and the engine's D-0058
allowlist entries are gone.
