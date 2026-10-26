# Retained RB topology: stage 3 review report

This change is stacked on `92a0f94e` and implements the stage-3 mutation inventory for #301.
The source reference is libyang v5.8.6. All new evidence is executable Go tests or live oracle
requests; no golden or manifest files are created. The complete supplied call-path inventory,
including every caller row and Go mutation site, is reconciled in
[the appendix](12-retained-rb-stage3-callpaths.md).

## Previous findings → fixes → regression evidence

| Previous finding / root cause | Fix and affected native functions | Regression test; failure at `92a0f94e` | Native evidence |
|---|---|---|---|
| Parser maintained two trees and reconstructed one on resume. Publication and later mutation could follow different histories. | `lyd_parser_node_insert`, `lyds_insert`, `lyds_additionally_create_rb_tree`: parser aliases `siblings.rbOrders`; native creation remains in `rbPos`; publication only materializes the authoritative graph. | `TestStage3ParserOneTopology`: baseline reports “parser created a second topology”; `TestStage3RollbackPreservesParserAlias`, `TestStage3RollbackCancelsNewParserPublication` check both already-pending and newly-pending rollback. | `TestStage3MutationOracle/parser-resume-and-cleanup`, existing repeated-key parser fixtures. |
| Removal selected a cached group and bypassed `rb_find`, silently repairing histories where native would retain a detached reference. | `lyd_unlink`, `lyds_unlink`, `rb_find`, `rb_remove_node`: select the native leader, check its actual neighbors, descend using native operand order, search equal-value predecessors then successors, and refuse unsafe removal with logged D-0121 `LY_EINVAL`. | `TestStage3NativeRemovalSelectionRefusal`, `TestStage3NativeSearchRefusesUnreachable`: baseline returns nil for both. `TestStage3NativeRemovalReference` checks 4,096 transitions against an independent parent-link deletion reference, including duplicate values. | `TestStage3MutationOracle/remove-root-minimum-two-child-singleton`, eight sampled histories. Refused dangling-reference classes deliberately have Go diagnostic tests rather than crashing native requests. |
| Bulk unlink deferred leader transitions until final compaction, rather than exposing intermediate native state. | Repeated `lyd_unlink` / `lyds_unlink`: one virtual predecessor/successor view replays request order, including first-instance hash events and metadata changes; one physical compaction publishes even a refused operation's completed prefix. | `TestStage3BulkRemovalHistory` compares exact shape/colors with sequential removals across 32 persisted histories. `TestStage3BulkRefusalPublishesCompletedPrefix`: baseline incorrectly removes the refused member and returns nil. | Same removal and sampled-history live cases; `seq/trim-lyds-all-indexed`. |
| An isolated singleton's metadata was discarded on unlink. A later direct transfer followed by a LAST_BY_SCHEMA tail rebuilt a different tree. | `lyds_unlink` keeps the singleton on its detached owner. Direct linking transfers it intact; `lyds_insert` clears incoming singleton metadata only when sorted insertion actually occurs, as native lines 1225–1232 require. `lyd_free_tree` releases it. | `TestStage3DetachedSingletonKeepsMetadata`: baseline produces payload order `[carry tail new]`; native and fixed Go produce `[carry new tail]` for equal-key lists. | `TestStage3MutationOracle/detached-singleton-metadata` compares the complete observable history. |
| Hash-leader replacement implicitly moved RB metadata, including during positional operation cleanup. Hash records and topology ownership are separate native transitions. | `lyd_insert_hash_add`, `lyd_unlink_hash` only update records; sorted insertion/removal and parser leader changes move topology explicitly. `_lyd_validate_op` restores positional links and hashes, keeping the removed RB member outside the surviving source tree. Failed moves instead restore the pre-mutation graph. | `TestStage3TemporaryOperationNativeTopology`: baseline moves metadata back to a positional nonmember. The fixed test compares the surviving tree with independent native removal, then inserts again. | `TestStage3MutationOracle/temporary-operation-topology`; exhaustive `TestValidateOpHashBudgetChargePoints`. |
| Journal capture allocated before charging, cloned retained trees separately from pending parser references, and could continue work rebinding after a failed reservation. | Go journal: reserve every map/slice/node/record copy before allocation; restore topology objects in place; propagate `touch` and work-rebind failures; omit operation-local removal undo from retained snapshots. Whole-run recovery uses one journal for both forests, replacing topology-only snapshots and recovery relinking. Single-node recovery reserves a finite link allowance before unlink. | `TestStage3JournalReservesBeforeAllocation`: baseline allocates about 11.8 MB after a one-unit remaining quota; fixed capture refuses before publishing a sibling/node snapshot and allocates under 128 KiB. `TestStage3WholeRunBudgetRestoresBothForests`, parser alias tests, `TestStage3AnyTransferRollback`, `TestStage3RollbackDisarmsRemovalUndo`, `TestStage3RollbackPreservesReboundTopologyWork`, existing exhaustive charge-point suites. | Success-side mutation histories are covered by the live cases. Native has no Go budget/journal error-injection API. |
| A later logged error could hide a journal budget failure and prevent rollback. | Budget failures take precedence at the operation boundary, including before paired operation/dependency journals end. | `TestStage3JournalBudgetOutranksLoggedError`: baseline returns “later diagnostic” and keeps the partial mutation; fixed Go returns `ErrBudget` and restores the graph. | Go-only budget contract, alongside the same native success sequences. |
| Secondary and empty metadata ownership was approximated by a single run flag. Whole transfers could overwrite an additional tree; copies discarded NULL-root items. | `lyds_get_rb_tree`, `lyds_move_meta`, `lyds_merge_nodes1/2/3`, `lyplg_type_dupl_lyds`: retain ordered metadata identities, including empty selections; transfer every graph and keep the passed merge root throughout the batch. | `TestStage3DuplicateRetainsEmptyMetadata` fails on the original head; `TestStage3WholeMoveKeepsAdditionalMetadata` and `TestStage3MergeIntoEmptyMetadataSelection` check all owners and dual memberships through successive transfers/rebuilds. | `TestStage3NativeMetadataMoves`: six native histories, pointer identity, duplicate slot counts, and every printed intermediate tree. |
| Whole-tree discard used ordinary DFS and skipped owned anydata payloads. Payload cleanup could retain membership flags and bypass enforced cleanup accounting. | `lyd_free_all`, `lyd_free_subtree`, `lyd_any_copy_value`: reserve forest traversal and reciprocal-link cleanup before mutation; include any payloads; release wholly discarded forest owners without rebalancing; clear membership and pending references. Full payload adoption transfers storage/indexes/topology without sharing. | `TestAnyPayloadLinkCleanup/discard`: baseline leaves reciprocal records. `TestStage3DiscardReservationRefusal`: baseline ignores the quota and succeeds. `TestStage3AnyTransferAndDiscard`, `TestStage3AnyTransferRollback`, existing linear-discard tests and `TestMergeAnyHashBudgetChargePoints`. | `TestStage3MutationOracle/payload-copy-replace-and-free`; existing any-core payload and leafref-link cases. |

The original-head regression experiment ran the same behavior assertions in an isolated copy of
`92a0f94e`. Capture/charge calls were adapted only for their former void return signatures.
All twelve selected regression probes failed for the reasons above. No implementation was changed
in that baseline. `TestStage3BulkRemovalHistory` and the independent deletion reference establish
native transition correctness; they are not claimed to fail on already-correct same-context
baseline deletion.

## Per-row transition contract

Each evidence code below applies to every corresponding caller row in the appendix. Structural
checks use `checkTreeRetainedRB`: reachable/mapped-node bijection, parent links, acyclicity,
root/red/black-height constraints, subtree sizes, ownership, and in-order projection of the
**logical** sibling sequence. Value ordering is intentionally not asserted for native in-place
key/store changes. Exact shape/color checks additionally use the independent `refRB` insertion
and deletion reference; structural validity alone is insufficient.

| Code / branch row | Native function(s) | Required transition and concrete proof |
|---|---|---|
| R: `unlink`, `unlinkRetainingHistory`, `detach`, `removeRBNode` | `lyd_unlink`, `lyds_unlink`, `rb_find`, `rb_remove_node`, `rb_remove` | Native owner selection and search; exact rebalancing; isolated singleton preservation; explicit nonmembership after detach. `TestStage3NativeRemovalReference`, both native refusal tests, detached-singleton test, `TestSortedRemoveReleasesRBNode`; live removals/singleton histories. |
| B: `unlinkAll`, `unlinkAllDefaults`; validation, trim and cleanup | Repeated `lyd_unlink`, `lyd_free_tree`, `lyd_free_siblings` | Request-order removals and intermediate metadata/hash-leader transitions before one compaction. Exact-shape bulk/sequential comparison, completed-prefix refusal, existing `TestRBTreeRunEmptied` / `TestRBTreeBulkUnlinkMatchesSequential`; live removals and sampled histories. |
| F: `freeTree`, `nodeFree`, `freeParsed`, discard and payload destruction | `lyd_free_tree`, `lyd_free_subtree`, `lyd_free_all`, `lyd_parser_node_free` | Remove from surviving owners; release discarded owners without rotations; include payload links; drop pending references. `TestStage3FreeDropsOwnedTopology` (baseline retains freed owner graphs), payload/discard tests, parser error cleanup and charge-point tests; live payload and parser-cleanup cases. |
| D: `dupWork`, `dupLink`, `dupNodes`, duplication into existing parents | `lyd_dup_r`, `lyd_dup` | Copies preserve native empty metadata slots without populated source topology (`lyplg_type_dupl_lyds`). Recursive child copies use LAST; parent insertion follows DEFAULT / LAST_BY_SCHEMA / continued-LAST native dispatch. `TestStage3DupIntoParentTopology`, `TestStage3DuplicateRetainsEmptyMetadata`, detached singleton plus NoSort tail, mixed multikey history; live multikey duplication and singleton cases; direct native `TestStage3NativeMetadataMoves` checks one/two/33 copied metadata slots. |
| P: `nodeInsert`, `appendSorted`, `applyLeaders`, `flushDirtyRun`, `buildRBOrder` | `lyd_parser_node_insert`, `lyds_insert`, additional-tree creation | One authoritative retained graph. No parser reconstruction on resume. `buildRBOrder` remains charged test setup; production parser creation is `rbPos`. Publication has no topology mutation. Parser identity/resume/rollback tests, existing repeated-key scaling and dirty-publication tests; live parser resume and cleanup. |
| V: `changeTermVal`, `changeNodeValue`; repeated keys / value resolution | `lyd_change_term_val`, `lyd_change_node_value`, parser/store callbacks | Native physical-neighbor predicate decides unlink/change/reinsert; in-place stores retain topology. Value failure recovery preserves old graph. `TestSortedSetValueRetainedRBTopologyBounded`, `TestStage3MixedMultikeyHistory`, native-search refusal, exhaustive singleton/compared-parent hash-budget tests; live multikey value changes and repeated-key corpus. |
| A: `takeAnyChildren`, `clearAny`, `copyAnyValue` | `lyd_create_any`, `lyd_any_copy_value` | Move an entire owned forest intact, or perform independent suffix removals; never invoke schema-mount split. New copies do not inherit source trees. Adoption/discard/rollback tests and payload-link cleanup; live payload case. |
| J: `hashTransaction.restore`, `linkOrigin`, `restoreLink`, move-error recovery | Go rollback machinery | Restore authoritative graph, storage, indexes, generations, ownership and work pointers. Capture once per sibling set; reserve before allocation; reserve finite recovery work before mutation. Whole-run and alias rollback tests and all charge-point tests; native success sequences only for the Go-only failure contract. |
| I: insertion primitives and callers | `lyd_insert_node`, `lyds_insert`, `lyds_link_data_node`, `lyds_move_meta` | Retain stage-2 charged insertion history and explicit DEFAULT versus LAST membership. Stage 3 separates hash leadership from metadata movement. `TestRetainedRBShapeFollowsInsertionHistory`, `TestLastBySchemaTailOutsideTree`, singleton test; existing stage-2 live histories plus new insertion-after-removal cases. |
| M: whole-run transfer and merges | `lyd_move_nodes*`, `lyds_merge_nodes1/2/3`, `lyd_merge_sibling_r` | Keep stage-2 encounter/post-order replay and whole-run topology transfer. Recovery now snapshots both forests once through the journal. Selected and secondary metadata are transferred by identity and in native list order; nodes1/3 retain their passed tree throughout the batch. `TestWholeRunTransferThenForeignInsert`, `TestMergeNodes3RBIterOrder`, `TestMergeNodes2HashOrder`, `TestStage3WholeRunBudgetRestoresBothForests`, `TestStage3WholeMoveKeepsAdditionalMetadata` (including first-item promotion); direct native `TestStage3NativeMetadataMoves` and existing/new merge histories. |

The mixed-context randomized test persists seed `(0x301, 5)` and runs 4,096 operations over the
same retained forest: local/foreign inserts, single/bulk removal, value change, Merge,
Diff/ApplyDiff, duplication, parser resume, whole-run moves, payload adoption/discard, and forced
journal rollback. The multikey stream adds 2,048 operations with seed `(0x301, 33)`. The independent
removal reference adds 4,096 transitions with seeds `(0x301, 0..3)`. Bulk history uses `(0x301, 3)`.
Invariants run after successes, explicit refusals and injected rollback. Direct budget tests
sweep the actual charge thresholds, rather than guessing a few limits.

## The ten additional caller paths

| Caller | Native counterpart | Transition / invariant and oracle evidence |
|---|---|---|
| `Tree.newPath` failure cleanup | `lyd_new_path_create` → `lyd_free_tree` / `lyd_unlink` | R/F; existing NewPath cleanup tests and sequence-edits corpus; no independent tree manipulation in this caller. |
| `Tree.newPathUpdate` | `lyd_new_path_update` → `_lyd_change_term` | V; `TestStage3DefaultsDummyWhenAndPathUpdate`, mixed multikey history; live defaults/dummy/path sequence. |
| `newBuilder.changeTerm`, `Node.SetValue` | `lyd_change_term` → `_lyd_change_term` | V/R/J; sorted value-change, dual-membership refusal and charge-point tests; live value-change sequence. |
| `valCtx.dummyWhen` insert/unlink | `lyd_validate_dummy_when` | I/R with opaque stand-in (no RB membership); `TestStage3DefaultsDummyWhenAndPathUpdate`, `TestDummyWhen`; live defaults/dummy/path sequence. |
| `lydCtx.envEventTime` cleanup | `lyd_parse_xml_netconf` → `lyd_unlink` | R/F with opaque envelope node; `TestStage3EnvelopeCleanupInvariants`; live NETCONF cleanup. |
| `parseJSONRestconf` eventTime cleanup | `lyd_parse_json_restconf` → `lyd_unlink` | R/F with opaque envelope node; same invariant wrapper; live RESTCONF cleanup. |
| `valCtx.modInsert` | Implicit-default insertion → `lyd_insert_node` | I/B; defaults/dummy/path test checks tree state before and after explicit values replace implicit defaults; matching live case. |
| `anyWork.createAny` → `copyAnyValue` | `lyd_create_any` → `lyd_any_copy_value` | A/D; adoption and any-copy tests; live payload case and existing any-core constructors. |
| `Tree.mergeMatch` → copy / term change | `lyd_merge_sibling_r` | A/V/J; payload cleanup and exhaustive merge-any budget tests; live payload replacement. |
| `Node.copyAnyValue` → duplicator | `lyd_any_copy_value` → `lyd_dup_r` | A/D; payload-transfer/discard and duplication tests; live payload copy/replace/free. |

## Native reachability corrections and explicit exclusions

`lyds_free_tree` is reached natively only through the `lyds_tree` type plugin's free callback
(`lyplg_type_free_lyds`), including when that plugin frees/replaces its stored value. It is not
a general direct data-tree mutation root. Go releases the corresponding owned topology when
its forest/metadata is discarded; no standalone C allocator/free routine is ported.

`lyd_unlink_siblings` and `lyds_split` have only the schema-mount caller in the supplied native
index. Schema mount remains unsupported (U-0024). `_lyd_validate_op` actually calls
`lyd_unlink`, not `lyd_unlink_siblings`. This change does not port schema-mount split or add a
second topology for it. Internal any-value use_value adoption reaches freshly owned parsed
forests; its suffix fallback performs independent removals and is not described as a native
schema-mount split.

Stage-1 compatibility profile/class boundaries, Diff user-order refusal policy, D-0062's
existing safe merge fallback, unsupported extension entry points, and unrelated API coverage
are unchanged. The stage-2 insertion/merge algorithms are composed, not independently
reimplemented here. No public API, dependency, policy file or golden is changed.

The appendix preserves original `no-go-mapping` and CHA-only annotations. They are evidence
limits: a mapped shared primitive is not proof that every unmapped native API is implemented.
Unsupported native entry points have no claimed direct oracle sequence. Supported parser,
constructor and validation chains exercise the corresponding common transition classes above.

## Main versus head counted work

The portable `TestStage3CountedWork` body was run in isolated `origin/main` (`62c15e35`) and
stage-3 copies with the same minimal schema helper. Input-string construction and schema
compilation are outside allocation measurements. Numbers are work units, not timings.

| Input | Parse counted main → head | Parse enforced main → head | Bulk counted main → head | Bulk enforced main → head | Discard counted main → head | Discard enforced main → head |
|---|---:|---:|---:|---:|---:|---:|
| descending 256 | 18,663 → 11,844 | 2,069 → 18,066 | 513 → 1,666 | 128 → 25,779 | 256 → 512 | 0 → 769 |
| descending 512 | 42,354 → 26,698 | 4,117 → 40,094 | 1,025 → 3,587 | 256 → 56,119 | 512 → 1,024 | 0 → 1,537 |
| equal 256 | 9,317 → 7,583 | 2,069 → 16,027 | 513 → 10,847 | 128 → 73,632 | 256 → 512 | 0 → 769 |
| equal 512 | 21,160 → 17,184 | 4,117 → 36,007 | 1,025 → 42,175 | 256 → 253,472 | 512 → 1,024 | 0 → 1,537 |

Parser work remains O(N log N); its allocation measurements remain approximately 0.49–0.51 MB
at 256 and 0.96–1.02 MB at 512. Discard is linear and now enforced. Bulk removal includes the
native search and rebalancing that main omitted: distinct-value removal is O(M log R), plus
linear publication; native duplicate-value sequential search can be O(MR). That additional
native work is explicitly charged and bounded by the existing stored-hash quota. It is not
hidden in `work.Add` or moved into a smaller, unenforced allowance. The new budget tests reject
before snapshot/allocation/mutation when the quota cannot cover required work.

Additional portable operation measurements (destination counters; source construction excluded):

| Operation/input | Counted main → head | Enforced main → head | Allocated bytes main → head |
|---|---:|---:|---:|
| Dup descending 256 | 6,066 → 7,607 | 261 → 14,309 | 199,376 → 428,352 |
| Dup descending 512 | 13,623 → 17,212 | 517 → 32,536 | 396,528 → 858,184 |
| Dup equal 256 | 6,066 → 7,607 | 261 → 14,309 | 199,376 → 428,352 |
| Dup equal 512 | 13,623 → 17,212 | 517 → 32,536 | 396,560 → 852,864 |
| Whole move descending 256 | 776 → 2,064 | 0 → 12,109 | 63,432 → 448,064 |
| Whole move descending 512 | 1,544 → 4,113 | 0 → 24,143 | 123,880 → 885,536 |
| Whole move equal 256 | 776 → 2,072 | 0 → 12,124 | 63,432 → 447,872 |
| Whole move equal 512 | 1,544 → 4,122 | 0 → 24,160 | 123,880 → 885,344 |
| Single move descending 256 | 26 → 44 | 138 → 706 | 728 → 6,376 |
| Single move descending 512 | 27 → 47 | 267 → 1,107 | 728 → 6,376 |
| Single move equal 256 | 26 → 42 | 138 → 706 | 728 → 4,296 |
| Single move equal 512 | 27 → 45 | 267 → 1,107 | 728 → 6,376 |
| Value change descending 256 | 34 → 68 | 20 → 2,103 | 368 → 2,808 |
| Value change descending 512 | 37 → 76 | 21 → 3,682 | 368 → 4,888 |
| Value change equal 256 | 34 → 131 | 20 → 2,398 | 368 → 2,808 |
| Value change equal 512 | 37 → 203 | 21 → 4,294 | 368 → 4,888 |

Whole moves now retain and snapshot the complete native graph and both forests for rollback:
linear additional storage/work, charged before allocation. Duplication adds empty metadata slots
and retained graph insertion. Value changes include native search and a prepaid removal/recovery
journal. The duplicate-heavy native search is the same explicit bounded exception described above.
Validation's old `6*N` membership-only test bounds were updated to logarithmic distinct-value
native search/removal bounds; no production budget limit was enlarged.

Keyed-list prospective reinsertion uses a prepaid O(log N) native removal undo and read-only
search before changing registrations. A deterministic indexed-parent test fails with only this
preflight omitted: a rejected insertion used to restore the shape but advance registration history.
The test is `TestStage3KeylessReinsertRefusalPreservesHistory`; the omission was tested with a Go
compiler overlay, leaving the working source unchanged. This undefined native keyless-comparison
case is refused under existing D-0121, rather than executed against C. Same-context refusals retain
`sortedShapeError` diagnostics. Normal key changes remain covered by live multikey histories.

| Key change input/direction | Counted main → head | Enforced main → head | Allocated bytes main → head |
|---|---:|---:|---:|
| descending 256/up | 39 → 107 | 22 → 797 | 368 → 4,432 |
| descending 256/down | 57 → 137 | 22 → 787 | 368 → 4,112 |
| descending 512/up | 42 → 120 | 23 → 862 | 368 → 8,592 |
| descending 512/down | 62 → 155 | 23 → 855 | 368 → 8,272 |
| equal 256/up | 39 → 194 | 22 → 2,702 | 368 → 4,432 |
| equal 256/down | 57 → 349 | 22 → 3,016 | 6,512 → 10,256 |
| equal 512/up | 42 → 275 | 23 → 4,686 | 384 → 8,592 |
| equal 512/down | 62 → 616 | 23 → 5,377 | 12,656 → 20,560 |

The counted key test exercises both comparison directions. The original main measurement uses
its existing slice representation; head's distinct-value search/undo grows logarithmically, with
charged native duplicate scanning as above. New small map allocations have allocator size-class
jumps; the test records allocations independently of work counts. A full-allocation profile of
`TestStage3KeyChangeCountedWork/equal=false/n=512/lower=false`, filtered to `SetValue`, attributes
7.08 KiB of its 8.39 KiB to the two charged `dirtyOrder.keep` undo maps. No whole-sibling copy
appears in that measured path. The remaining allocations are the local link/metadata recovery,
value storage and one topology node.

## Additional metadata audit

Temporary operation restoration can leave old metadata on an interior node. A later tree creation
may add another metadata item to the same owner after successive leader removals. The implementation
retains the ordered primary/secondary metadata list (`rbGroups`/`rbMore`), empty NULL-root items,
all owned graphs and dual memberships. `lyds_merge_nodes1`/`nodes3` keep the passed root metadata;
calling ordinary insertion repeatedly would incorrectly reselect the owner's first item. Whole-run
moves carry all other items through native `lyd_unlink_ignore_lyds` semantics, and empty-destination
publication prepends the selected item without overwriting a secondary graph. Duplication copies
every item with an empty root; sorted single insertion clears only its first incoming item.

`TestStage3WholeMoveKeepsAdditionalMetadata` checks graph pointer identity, work-counter ownership,
source emptiness and all invariants through three transfers, both with and without first-item
promotion. `TestStage3NativeMetadataMoves` compiles the test-only oracle probe, asserts native
metadata pointer identity, checks duplicate slot counts (one/two/33), and compares all three JSON
outputs directly with Go. A third direct native history starts with a duplicate NULL selection,
merges a populated source via nodes2, and checks that its transferred metadata stays secondary
until the next insertion rebuilds the selected root; `TestStage3MergeIntoEmptyMetadataSelection`
checks ownership, selection and dual memberships at each step. The two added baseline probes independently fail on retained freed
owner graphs and missing empty duplicate metadata. Schema-mount split remains excluded as above.

The final native probes also cover a 33-item metadata queue and transfer without rebuilding
after temporary validation. The latter keeps an interior owner inside one native nodes1 walk;
a parser/merge helper must not split the physical schema run into a second topology there.
`TestStage3NonmemberMetadataUnlink` and the native `empty-unlink` probe verify that metadata
moves to the successor before native search, including an empty selected tree and an owner
outside that tree. Omitting only the pre-search transfer makes
`TestStage3NonmemberMetadataUnlink` fail on the missing successor ownership.

Two recovery regressions were found with that long-queue public history. First, a failed move
could exhaust a fixed restoration allowance and panic; adoption and queue recovery are now
reserved proportionally before unlink. Second, recovery advanced source registration history.
`linkRecordOrigin` preserves only the affected records and neighbours (ordered buckets reinsert
in logarithmic work), while `rbMetadataOrigin` preserves the two affected metadata owners.
`TestStage3ManyMetadataMoveFailure` reproduced the panic before the fixes;
`TestStage3ManyMetadataMoveBudget` failed on changed source registration ordinals and now
checks every source/destination charge boundary with all 33 items present.
Whole-forest memberships are collected and finalized once using temporary sets, charged before
allocation: overlapping retained graphs cost O(S) for S memberships, without repeated scans of
growing per-node lists. Source and destination keep one authoritative graph per metadata identity.

Subtree work binding now reserves traversal slots, metadata/pending-root rebinding and journal
entries before allocating a traversal stack or publishing the link. Future link planning is
included in the reservation made before sorted publication. `TestStage3BindingReservesBeforeAllocation`
fails with just the charging omitted: 524,432 bytes allocated after only three units remained,
with no reported error. Fixed binding returns `ErrBudget` before the wide stack allocation and
preserves the original work counter. Existing sorted-publication quota tests fail if the later
charged planning is omitted from the earlier reservation. All binding callers were inventoried:
constructors, detached value changes/removal, attach, payload copy/adoption and temporary
operation moves use the same bound/error propagation. No additional budget was introduced.

The persisted mixed-context multikey history additionally inserts/removes an action and runs
temporary operation validation amid key changes, duplication, unlink, merge and rollback;
the checker runs after every transition. The mixed leaf-list history retains its parser,
whole-move and payload transitions with seed `(0x301, 5)`; multikey uses `(0x301, 33)`.

## Evidence index

Removal failures propagate: `unlink`, `unlinkRetainingHistory` and `bindSubtreeWork` return
their error, and no caller links, inserts or rebinds a node whose removal or rebinding failed
(`TestStage3MoveRBRootBudgetPoints`, `TestStage3BindingReservesBeforeAllocation`). Bulk
removal charges each native removal as it runs; without an enclosing journal it opens a
batch-local one, so a budget failure restores the whole batch while a D-0121 refusal publishes
the completed prefix (`TestStage3TrimBudgetRestoresBatch`,
`TestStage3BulkRefusalPublishesCompletedPrefix`). Locating a removed node charges every
inspected slot before reading it (`TestStage3UnlinkPreflightChargesScan`).
A refusal's published prefix still gets its descendants' reciprocal leafref cleanup, which
callers skip on error (`TestStage3TrimRefusalCleansPrefixLinks`). Temporary operation
validation that cannot move its subtree back (a D-0121 cleanup refusal) restores both trees
from their journals (`TestStage3ValidateOpCleanupRefusalRestores`).

Live request ids in `conformance/retained_rb_stage3_oracle_test.go`:
`stage3/removals`, `stage3/parser-resume`, `stage3/multikey`, `stage3/payload`,
`stage3/defaults-dummy-path`, `stage3/netconf-cleanup`, `stage3/restconf-cleanup`,
`stage3/detached-singleton`, `stage3/temporary-operation`, and
`stage3/seed-301-3-0` through `stage3/seed-301-3-7`.
The direct metadata probe is `TestStage3NativeMetadataMoves/{empty-selection,empty-unlink,retain,promote,many,unrebuilt}` in
`data/retained_rb_stage3_oracle_test.go`, using `conformance/oracle/retained_rb_stage3.c`.

The cancellation test now accepts an empty run when cancellation precedes the first insertion;
it still checks every cancellation point, retained ordering and a subsequent insertion. The list
index history test retains all 28 executions of every operation, checks its full model on each
D-0121 refusal, and chooses a safely removable candidate when its retained history reaches the
existing size cap. Neither test skips the regression path.

New manifest fixture ids: none (live oracle requests only).
