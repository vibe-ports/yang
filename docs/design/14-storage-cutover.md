# Sibling storage cutover: #240 step 5

Step 5 of #240 makes the order-statistic sequence the only physical sibling storage and attaches
the ordered index to it (contract `c240-sibling-sequence.astra.md` §5, step 5; design 13 is
step 4). Behaviour, verdicts and libyang event order do not change; no new deviation. Code
comments cite this document as "design 14".

## What is authoritative

- `siblings` keeps one `*siblingStore`, allocated with its first child: an `opidx.Seq[*Node]`
  holding the schema children in `[0, nschema)` and the opaque ones after them, and the one
  `orderBinding` its hooks (and the index's) report to. The store is reached through a pointer,
  so moving or capturing a `siblings` value never copies a `Seq`.
- A linked node finds its handle in O(1) through `Node.slot`, an `opidx.Slot` whose writes are
  journal images. A non-owning view (`sibsOf`, the print view, a payload copy's source) is a
  store marked `view`: its handles are not its nodes' slots, and building it neither reparents
  nor re-slots anything.
- The sequence maintains no order labels until an ordered index is built over it
  (`SetLabeled(true)` in `orderBuild`: a unit per handle and the header's image, prepaid;
  `LabelBound`). `Compare` falls back to ranks meanwhile. Unlabeled point edits are pure AVL
  work, so an ordinary set pays no relabelling. A link that builds the index reserves the
  labelling with its other work (`indexAddBound`, which predicts the build: `linkBuildsIndex`
  mirrors link's operation-child, markUnordered and markMixed decisions).
- Disposal (`reset`, for `DiscardTree` and a replaced payload) gives every node's slot back,
  journaled and paid, so a discarded root a caller keeps does not keep its former siblings
  alive through the storage (TestDiscardReleasesFormerSiblings).

## What went away

- `nodeDeque.list`, `siblings.opq` and the deque (the duplicate cache keeps its own local
  deque); `Build`, `Splice`, `buildSchema`, `exposeSchema`, `cloneStorage`, `retainsBase`,
  `retainsList`; `childOrder.seq` and `childOrder.byNode`; `orderReorder` (folded into
  `replaceSchema`, which unregisters the run, republishes its handles with one `Republish` and
  registers them again); the parser's position cache.
- The position scans a slice needed: `indexFromEnd` over sibling storage, the scan fallbacks
  of `chargedIndex`, `orderPosition`, `originIndex`, `prepareUnlink` and `indexOf`. Each is one
  rank walk (`orderPosition`), paid by a read credit on a mutation path (design 13 §4.4).
- Obsolete bounds: `insertCost`, `opaqueInsertCost`, `linkSlots`, `runSlots`, the `2*linkSlots`
  recovery term, the parser's `3*runLen` slot pre-charge and `work.shifts`. The sequence's point
  bounds (`InsertBound`, `RemoveBound`, `RepublishBound`, `KeepBound`, journal-aware) replace
  them inside the existing `orderAddBound`/`orderRemoveBound`/`publicationIndexBound` totals.

## Invariants

1. Every linked child of a set occurs once in its store, schema before opaque; its slot is its
   handle there (`storeCheckInvariant`, run by every tree invariant and budget sweep).
2. An ordered index registers exactly the store's handles and owns no sequence.
3. Every store write while a state runs is journaled in that state (`bind.state`: bound by the
   whole capture, by a narrow write, or with the store's allocation); rollback restores the
   sequence, handles, slots and the label mode by replay and allocates nothing.
4. Every store write in a hash-work scope is paid: by its enclosing scope's bound or its own
   prepay before the first write; it is charged its actual units and refunded the rest, as the
   index is (design 13 §4.1).
5. Spans and iterators borrow the live store. A write that moves positions bumps `ver`; a test
   build panics on a read through an older borrow. A republication (`replaceSchema`) keeps every
   position and bumps `moves` instead: an iterator reseeks by position, as indexing an
   overwritten slice did. `reborrow` takes a span again after a temporary node was linked and
   unlinked (`lyd_validate_dummy_when`).
6. Retained-RB in-order projection (design 12) is unchanged.

## Attach replay (was `exposeSchema`)

`rbPublish` republishes its run at once and then attaches the new members one by one. While
it does, `siblings.unattached` names the members not attached yet: the table threshold counts
`SchemaLen() - len(unattached)`, and table construction (`logical`) skips them, so a children
table created during the replay holds the siblings attached so far, in sibling order, and the
later members register after them in attach order. A member's record neighbours among the
attached siblings are precomputed in O(run) (`visibleNeighbours`: a list over the run and its two
outer neighbours, from which the members leave in reverse attach order), and the publication's
charge is linear in the run. TestRBPublishTableHistory checks the registration order around the
threshold for several attach orders (and fails without the filter); TestMergeAttachReplayScaling
merges M ascending values into an indexed run with a table: linear in M.

## Completion funding of deferred publication

A parser run deferred for publication is funded as it grows: before linking a member,
`payPublication` tops up its set's completion credit to `publicationNeed` (per pending run
`RepublishBoundAny` of its members, its index re-registration when indexed, and its reads, for
the set's length after the link, journaled). The credit is kept as aggregates per set (runs,
members, indexed), so a top-up is O(1) however many runs are pending
(TestParserManyDeferredRunsScaling). `publishDirty` opens a prepaid scope with that
already-charged credit, so a publication charges nothing fallible and refunds the rest. Test
builds panic if a publication refuses (`checkPublish`). An opaque XML link that grows a set with
pending runs tops it up too. Each run is located by its own logarithmic lookup at publication,
not by a pass over the whole set (TestPublishSparseDirtyRuns). TestParserPublicationFunded sweeps every budget limit, every
cancellation point and truncated inputs over data and operation parses.

## Complexity

Positional reads are descents: `At` counts its levels as sibling visits (test builds); a
position closer to either end than the tree's height is walked along the threads from that end
instead (a list's key prefix, an end's neighbour: O(distance)), and every sequential consumer walks the threads instead (iterators,
`siblingSpan.cursor`, handle neighbours); lookups return the node they found rather than a
position to read again (`schemaFind`, `foreignSchemaFind`). Point reads stay O(log n) and
uncharged, as slice indexing was; they are O(1) per edit. A point edit costs O(log n + L)
sequence work: L is the relabel, zero while unlabeled and
amortized O(log n) in an indexed set (Bender et al.). `LowerBound` over storage is one bounded
descent (`BoundIn`): O(log n) structural visits, counted as sibling visits, and at most one
predicate call per level. A bulk removal (`Keep`) is O(n) however many go. A republished run of
k is O(log n + k). TestStorageMiddleEditScaling measures middle insertions and removals in a
user-ordered run of 1K and 16K, ordinary and indexed, with and without one journaled state:
per-edit work and hash grow by about 1.3x (main grows 15x). A state's first write to a set
still captures its compatibility records and node structs whole, O(set size) once per state
level; repeated relocations in one set open no level of their own (step 6).

## Deviations from the plan

1. **Refunded, not priced, storage charges.** Pricing every write at its bound would make a
   limit that sufficed once suffice again, but it raised a 250K-link operation parse above the
   default budget; the index's refund model is kept and the tests that derived a limit from a
   probe's usage now search the smallest sufficient limit (TestMergeDiffHashWorkScope) or add
   `storageAllowance`.
2. **Unlabeled storage** (not in the plan): labels cost about 130 units per middle insertion and
   no unindexed reader needs them. Turning them on is paid like the rest of the build's reach
   (snapshot review 1).
3. **Handle identity across an unjournaled relocation's forward recovery** is not kept: the
   recovery relinks the node with a fresh handle (`restoreLink`); order, slots and index are
   restored, and nothing outside the storage observes the handle.
4. **Counted expectations moved** where they modelled the deque: appends and middle edits are
   O(log n) (TestInsertWork, TestBulkWork), the bulk compaction scans all siblings once, value
   lookups now count their descent (TestUniqueMissingDefault), and an origin lookup is a rank
   read credit (TestSiblingLinkOriginBudgetThreshold, TestStoredHashMaxLeafListSetValueWork).

## Evidence

- opidx: TestRepublishModel, TestRepublishRejectsBeforeWrite, TestSlotJournal, TestKeepModel,
  TestUnlabeledSequence, TestUnlabeledBoundsCover, TestBoundInModel.
- data: TestSiblingFacadeModel, TestSiblingFacadeStorageRollback, TestSiblingFacadeView,
  TestSiblingFacadeCaptureFailure, TestSiblingStorageBoundary (no storage write outside the
  facade), TestRBPublishTableHistory, TestParserPublicationFunded,
  TestParserManyDeferredRunsScaling, TestStorageMiddleEditScaling,
  TestDiscardReleasesFormerSiblings; the storage invariant in every tree invariant check and budget
  snapshot; every existing S4 sweep and history, TestAstra305Round13CountedWork and the
  repeated-key scaling tests unchanged.

The first snapshot review (`240s5-snap1`) found three blocking issues, all fixed: the
completion planner rescanned every pending run of its set per top-up (now O(1) aggregates);
disposal kept the discarded forest reachable through the kept roots' slots; label activation
reported no work and was not reserved by the link that builds the index.

The second snapshot review (`240s5-snap2`) found three more, fixed: publication located its runs
by a pass over the whole set; sequential consumers read by position (O(n log n), uncounted);
the attach replay scanned past pending members (O(M²)).

The third snapshot review (`240s5-snap3`) found two more: the index rebuild after a batch
publication had lost its reservation when the publication charge became run-sized (now reserved
with it: TestMergeSparseBatchChargesRebuild), and sequential readers remained (key comparisons,
run ends, metadata walks, the removal neighbours), now threaded walks.

The PR reviews found loops that had read a slice by index and now descended per read
(O(n log n), uncharged): the JSON metadata cursor and metadata walks, and the left-run scan of a
removed separator, which now runs in the unlink preflight as a walk back from the predecessor,
one charged unit per member, its leader cached in the plan (TestUnlinkSeparatorLeftRunCharged:
linear, and a budget one unit short refuses before any write). A sweep of every remaining
positional read outside tests left no per-element loop over positions: the dup-cache run slot
(`runSlot`) and `firstModuleSibling` walk the threads, the unlink plan reads its neighbours from
the handle, and the key-prefix loops (`keyLinked`, `checkKeys`, `termHashBound`,
`HashKeyValuesEqual` and the `positionalChildren` users) walk a cursor. A repeated-key prefix is
not bounded by the key count: removing the non-key in front of it exposes all of it
(`exposeListKeys`), so the unlink preflight walks the prefix, two charged units per key
(TestUnlinkExposesRepeatedKeysCharged: linear, refused before any write one unit short); a
batch removal's per-set charge covers it. The rest are O(1) point reads per edit: neighbours of a link or
unlink position, run leaders and ends, a first or last sibling.

**Open:** a point read by position (`At`) is an O(log n) descent counted as sibling visits but
not charged to the enforced quota, as slice indexing was not; every remaining use reads O(1)
positions per edit (the step-6 consult found no input-sized loop of them and deferred charging
them). Rank and index reads are charged (D-0149). The index rebuild after a batch publication is
linear since step 6; keeping the index across it is not needed for the bound.

## Step 6: complexity of the index rebuild and of per-move recovery

The step-6 consult (`c240-step6`) blocked two complexity defects and asked for D-0149's wording to
be narrowed; this section is its plan.

1. **Linear index build.** `orderBuild` registered every handle through an ordinary AVL insertion
   from the root of its occurrence class, under an unbound binding that reports nothing: a
   rebuild after a batch publication (`rbPublish`, reserved at 8 units per sibling) was
   Θ(N log N) work charged as O(N). The build now groups the handles, which it visits in
   sequence order (ascending labels), by occurrence class and builds each class's tree balanced
   from its ordered slice (`Occurrences.Build`), and the operation children's rank sets from
   their collected ranks (`Ranks.Build`: an LSD radix sort by bytes, at most eight passes, then a
   balanced tree; one insertion per distinct schema was Θ(S log S) for a wide operation): O(N),
   at most `orderBuildBound` = 12 units per handle. The building binding reports that work: it
   draws from an open credit (a link that builds the index: `indexAddBound` includes
   `orderBuildBound`, so link's refund no longer returns it), and is counted only otherwise
   (`rbPublish` charges `orderBuildBound` for its schema and opaque siblings with its
   publication, before its first write). Distinct schema nodes can share a rank (the first input
   leaves of two RPCs under one opaque parent): the rank set keeps it once, as insertion did.
   Keeping the index across the publication (re-registering only the run) is not needed for the
   bound and is not done. Evidence: TestMergeSparseBatchChargesRebuild (counted work per charged
   unit flat over N = 256..16K, 0.42 to 0.71 before; a budget one unit short refuses before any
   write; also beside N opaque siblings), TestMergeWideBatchRebuildRanks (N distinct input
   leaves), TestOrderBuildRepeatedRanks, TestLinkBuildsIndexCharged
   (a link that makes N siblings unordered: charged at least 3 units per handle, about 1 before;
   flat; refused one unit short), opidx TestOccurrencesBuildModel and TestRanksBuildModel
   (against one-by-one insertion, balanced, exact work).

   The implementation's first snapshot review (`240s6-snap1`) found three blocking issues, all
   fixed: a repeated rank refused the build (and panicked); the publication's reservation left out
   the opaque siblings the rebuild registers; a test constant overflowed `int` on 386.
2. **Per-move recovery inside a state.** A one-node relocation inside a running state opened a
   level, and a level captures every object afresh (design 13), so each move copied the whole
   sibling set's compatibility records and node structs: an ApplyDiff of N moves before the
   first instance of a user-ordered leaf-list of N was Θ(N²) capture work and storage. Now a
   relocation within one set of the tree that the state's current level already holds whole
   opens no level (unless it is a child of a list instance, below): it rolls forward on failure
   (linkOrigin's restore, its recovery reserved before the unlink, as an unjournaled relocation
   does), and the state still restores a budget or guard refusal. The first move of a set still opens a level and captures there; the level
   commits into the enclosing one, so later moves find the set held. Savepoint semantics do not
   change (journal_levels_s4_test's "successive sibling levels capture afresh" stands: a level
   that captures is still exact); per-entry compat journaling of a level stays future work.
   The forward recovery relinks the node with a fresh handle (deviation 3 above). Evidence:
   TestApplyDiffRepeatedMoveFirstScaling (the consult's reproducer: per entry flat, one whole
   capture; before, N=256 to 2048 charged 0.9M to 55M units with one capture per move, and 4096
   exceeded the default budget), TestApplyDiffRepeatedMoveFirstRollback (every budget limit
   restores the tree exactly; an ordinary failure at a later entry keeps the moves before it),
   TestRelocationForwardRecoveryInState (a failure injected after the unlink, in a plain set, an
   indexed one and for a separator between two runs: the node returns, the earlier change stays,
   and the state then commits that or restores the original exactly). The second snapshot review
   (`240s6-snap2`) found the forward recovery left the unsorted mark a removed separator puts on
   the left run (the level had restored it); linkOrigin now saves and restores that mark, which
   also fixes the unjournaled forward recovery. The third (`240s6-snap3`) found that removing a
   list's non-key in front of its keys exposes them (`exposeListKeys` rewrites their key slots),
   which relinking does not undo: a child of a list instance keeps its level (the repeated moves
   the consult names are a leaf-list's or a list's instances, whose parent is not a list instance).
   The unjournaled forward recovery has the same key-slot gap on main (a follow-up).
3. **D-0149 wording.** Rank and index reads on a mutation path are charged (read credits);
   point reads by position (`At`) are O(log n) descents counted as sibling visits but not
   charged, as slice indexing was not. The Open note is narrowed to that.
