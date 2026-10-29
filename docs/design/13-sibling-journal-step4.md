# Sibling-sequence journal: #240 step 4

Step 4 of #240 gives every mutating operation one recovery state whose rollback restores the
ordered sibling index (`childOrder`) value by value, with nested savepoints, prepaid suffixes and
a guard against user code that reaches a state it is waiting on. Slices stay authoritative; the
storage switch, index attachment to storage and per-entry compat journaling are steps 5–6.

It implements the approved design (consult `c240-step4-journal`, revision 7, and its astra reviews
r5–r7; the contract is `c240-sibling-sequence.astra.md`). It reuses #318's narrow mutation journal
([design 12, "NewPath mutation journal"](12-retained-rb-stage3.md#newpath-mutation-journal)). Code
comments cite this document as "design 13" with the consult's section numbers.

## Recovery state and its logs

A `hashTransaction` holds four logs, each replayed newest first:

| Log | Records |
|---|---|
| whole | one image per whole capture, in capture order (`whole` kinds, `img` typed slices) |
| undo | #318's closures (`recordField`, `recordSlot`, chain, record, record path) |
| placed | #318's topology images |
| index | `opidx.Journal`: typed `Entry` values for every index write |

- **Index entries** (`internal/opidx/journal.go`). A Seq handle or header, an occurrence tree or
  node, a rank node or header, and the data-side cells (`byNode`, `entries`) and `opEntry`
  counters record their value before they change. Entries are typed values with an object
  interface, not closures. While a journal is installed the index maps never delete on the
  forward path: a removal stores the nil tombstone (`Occurrences.by` values are key pointers),
  readers treat nil as absent, and the outermost commit purges the tombstones the log names.
- **Binding.** `orderBinding{owner, state}` replaces `setOrderHooks`: hooks are installed once,
  on the binding. `state` is the state that captured the owner set whole; every index write
  reports to its journal until that state ends. A whole-forest transfer rewrites
  `bind.owner` (one undo entry); restore reinstalls nothing. `cloneChildOrder` is gone.
- **Whole captures** keep a serial per object (`wholeAt`, the former capture maps with `uint32`
  values). A level captures iff the object's serial is below the level's own; a capture stores
  the previous serial and replay restores it. Commit of a level is O(1).
- **Levels.** `savepoint`/`commitLevel`/`rollbackLevel` push and pop marks over all four logs
  (one charged unit). `moveNodes` rolls back its own level instead of the whole running journal.
  The one-node relocations (`insertChild`, `insertSibling`, `insertAfter`, the diff
  before-anchor move, a value change's re-insertion) open a level before the unlink inside a
  state, or an operation-local transaction (`relocation`) that captures both sets whole first.
  `restoreLink` stays only for ValidateOp's deliberate move back.
- **One state per operation.** ValidateOp opens one state over the operation's tree and the
  dependency (`beginJointTransaction`), the other tree's counter lent to it.

Rollback allocates nothing: replay assigns existing keys, deletes inserted ones and copies
values, and the hooks are never reinstalled (TestS4LevelIndexRollbackAllocs,
TestS4CompleteRollbackAllocs, TestJournalNestedModel).

## Prepaid scopes, read credits and bounds

- `workCounter.prepay(amount, read)` debits now (fallible) and opens a scope: until
  `closeCredit` every `chargeHash` of that counter draws from the credit and cannot refuse. A
  write credit refunds what is left; a read credit is not refunded (its bound is the charge).
  Journal captures use `chargeLimited`, checked against `hashUsed`, which includes the prepaid
  amount.
- Scopes: `link` (prepays `linkApplyBound` plus the index add's point bound), the single
  `unlink` (the index removal and the parent-list reindex, after its plan), the in-place value
  change (the full `changedIndexAmount` plus the `refreshExact` bound).
- **Overdraft.** Test builds panic (`overdraftPanics`, set by `data/overdraft_switch_test.go`),
  so every sweep, history and fuzz seed checks every bound. Production debits the excess
  unchecked; the over-limit counter refuses at the next fallible check outside a scope
  (TestS4PrepaidOverdraftProductionPolicy).
- **Read accounting.** Class A (an index read on a mutation path outside a scope) opens a read
  credit before it walks: `orderPosition` (the rank walk; `chargedIndex` keeps main's units and
  trace point), an occurrence class's `Next` in `allInstances` and `exactAtOrAfter`, and
  `opAnchor`'s rank successors. Class B reads inside a scope draw from the write's credit. Class C
  (no hash-work scope, or user code under a suspended state) pays nothing. A refused read credit
  is sticky and short-circuits the fallback scans. Test builds panic on an index hook that fires
  unpaid inside a hash-work scope, and on any journaled index entry outside a credit
  (`unpaidReadPanics`).
- **Point bounds** (`internal/opidx/bounds.go`): `c·(h+2)+4` for the actual AVL heights, with
  c measured (worst cases below 1.8 per level without images and 4.7 with them, 12.2 and 18.6
  for a move) and kept at 3/6 and 14/24. The relabel is counted exactly by a read-only planner
  that mirrors `labelRange`/`rebuildTags`; it charges its own walk step by step through an
  `opidx.Pay` before each visit (`workCounter.planPay`), so a refused budget stops it at once. `InsertBoundAny` is the position-free bound for a
  preflight that precedes a reorder (`insertPositionBound`). TestPointBoundsCoverJournaledEdits
  checks every bound against the units its edit reports.

Every other journaled index write prepays its own point bound before its first write, or draws
from an enclosing scope's credit (a nested prepay opens nothing):

| Write | Caller | Prepaid |
|---|---|---|
| `orderReorder` | sorted and parser publication | one `ReplaceHandles` over the run (its handles occupy one contiguous range), its relabel planned exactly, with the occurrence updates; `rbPos` reserves it, and its `refreshRB`s, in its check before the run changes (`publicationIndexBound`) |
| `refreshRB` | `rbPos`'s new tree | its point bound |
| `refreshExact` | `validateIncomplete` | its point bound |

## Suspension guard

User code runs during a mutation only in the diff callbacks, the Reader of a ParseOp with a
parent, and the methods of a context (and of the errors they all return). Around each the
operation raises its state's `suspendDepth` and lowers it in a defer (`callUser`, `readUser`,
`suspendCtx`), so a panic restores it and a nested site cannot unmask an outer one. Public
entries wrap the caller's context (`suspending`); every poll in data and internal/xpath goes
through one helper, which TestS4PollsGoThroughOneHelper checks.

User errors are classified while suspended and sealed as `userErr` (flags only; its `Is` answers
from them, it has no `Unwrap` or `As`). The public return restores the original error once, after
the state has ended (`restoreUser`, which walks only this module's and the standard library's
wrappers, so it never calls a user method; a wrapping of ours is kept around the original).

A capture, begin, lend or link-record edit that reaches an object governed by a suspended state
is refused with LY_EINVAL (`errInProgress`) before it writes (D-0148):

1. a begin on a suspended tree returns a detached refusing state (not installed, no hash-work
   scope; its `end` only reports);
2. `siblings.touch`, `Tree.touch`, `Node.touch` (and `touchLazy` through `narrow`) refuse;
3. `joinLinkJournal`, `moveNodes`, `rebind`, `beginJointTransaction` refuse to lend;
4. `captureNode` and `captureOwner` refuse a suspended counterpart, and every set
   `captureDetachedParent` captures and every link counterpart is enrolled until `end`
   (`enroll`): its counter is lent to the state, or a set without a counter is bound to the
   state's temporary counter (a works entry restores it) and unbound at end on both outcomes;
5. operations without a journal check the link records they will edit first (`linkGuard` in
   `prepareUnlink`, the value change, `reserveFreeLinks`, `linkLeafrefNode`), each visit charged
   before it is made.

`freeLinks` and `linkLeafrefNode` return their errors. `parseOpWith` and `ValidateOp` arm a
panic-safe finalization (`abandon`, `endPanicSafe`) before user code can run, so a panic in a
Reader, a context or an error method restores and detaches the state and closes its hash-work
scope.

## Mutation inventory (step 4)

Contracts: **P** refused in a preflight before any write; **S** prepaid scope, no budget
refusal after it opens; **J** the enclosing level restores; **J+prefix** a budget failure
restores, an ordinary or D-0121 refusal publishes the completed prefix; **V** unbound view.

| Mutator | Step-4 undo record | Contract |
|---|---|---|
| `Build` (diff and print views) | none | V |
| `buildSchema`, `exposeSchema`, `replaceSchema` | whole capture | J |
| `appendNode`, `InsertAt` (in `link`) | whole capture, or #318's slot entry | P→S |
| `DeleteAt` (single `unlink`) | whole capture | P→S |
| `DeleteAt`, `deleteMatching` (bulk) | whole capture | J+prefix |
| `transferFrom` | whole capture of both owners, binding owner entry | J |
| `reset` | whole capture | J / P |
| `orderBuild`/`opBuild` | new index unreachable on rollback; the old one untouched | S (in `link`) / J |
| `orderAddSchema`, `orderAddOpaque` | index entries | S |
| `orderRemove`, `orderRemoveOpaque` | index entries | S (single) / J+prefix (bulk) |
| `orderReorder` | index entries | S (its own prepay) |
| `refreshRB` | index entries | S (in `link`, or its own prepay in `rbPos`) |
| `refreshExact` | index entries | S (value change, or its own prepay in `validateIncomplete`) |
| `cloneChildOrder` | — | deleted |

Every index mutator that swallowed a refused touch returns it and makes it sticky
(`touchSticky`): `orderBuild`, `opBuild`, `orderAddSchema`, `orderAddOpaque`, `opOrderAdd`,
`opRemove`, `refreshRB`; a refused parser publication fails the parse.

## Deviations from the approved design

1. **Index entries are recorded once per epoch** per index node or header (`Journal.first`), not
   at every write. An epoch never spans a mark, rollback or commit, so a skipped write always
   follows an older entry of the same object in every tail that contains it. Map cells and
   counters are recorded at every write. Per-write images made allocation follow relabel work
   (TestSiblingFacadePublicWork).
2. **The index journal is the capturing state's**, found through `orderBinding.state`, not
   through `bind.owner.work.journal`: a set captured through another tree's state (a detached
   parent, a lent counter) has a counter without that journal.
3. **A rolled-back first capture deletes its `wholeAt` key.** A full small Go map grows on any
   assignment, even of a present key, so restoring a 0 serial would allocate. No image of the
   object remains in any tail, so the set may get narrow entries again.
4. **An unjournaled relocation opens the local transaction only when a set keeps an ordered
   index** (the only place a node has a handle); otherwise it keeps `restoreLink`'s linear
   forward recovery. Capturing both sets whole made repeated relocations in one set quadratic
   (TestStoredHashMaxLeafListSetValueWork, TestInsertDetachedOperationLeafListScaling). A
   journaled relocation no longer reserves the forward recovery.
5. **Bounds**: measured constants with a margin instead of derived ones; journal-aware (no image
   units without a journal). `labelRange`'s window search is capped at `32·len+64`, then
   `rebuildTags` (never more work), which makes a position-free bound exist. `rebuildTags`
   leaves the last quarter of the label universe to appends at a bounded stride: appends had
   halved one step's gap each time, which charged index work exposed (the 250K-link default
   budget, TestOpIndexDefaultBudgetAllows250KLinks).
6. **One work hook.** The binding has one `Work` hook (no separate read hook); class A credits
   open inside the index read functions when the counter is on a mutation path, rather than in
   per-site wrappers.
7. **`orderReorder` republishes with one `ReplaceHandles`** instead of a move per node, which
   makes an exact prepaid bound possible (the moves' relabels depend on each other).
8. **`userErr` has an `Is` answering from its flags**, so internal `errors.Is` keeps working
   without calling a user method.
9. **Context suspension is a wrapping context** created at the public entries, plus one poll
   helper per package and a grep test, rather than a `w.poll(ctx)` at each site.
10. **Enrollment binds a counterless set to a per-state temporary counter**, not to the state's
    tree counter: a later legitimate binding to the tree's counter must survive the unbinding at
    end.
11. **ValidateOp attribution**: captures of the dependency's objects charge the operation tree's
    counter (the state's), as every capture of a state does.
12. **A panicking diff callback** ends ApplyDiff through its deferred `end` with no result (the
    partial work is kept, as before); the guard only guarantees no suspension or installed state
    is left behind.

The second snapshot review (`240s4-snap2`) found three more, fixed: `rbPos` reached the new
index prepays after the run had changed (now reserved before it); `ValidateOp` did not arm a
panic-safe finalization; `captureNode` scanned counterparts before charging them.

**Open (direction, from `240s4-snap2`):** the parser's publication reaches a fresh fallible
prepay (`orderReorder` in `publishDirty`) instead of drawing on a completion credit retained on
the pending run (design 13 §6). A refusal there fails the parse, which its state (or, without a
parent, discarding the parsed tree) restores, so nothing is left half published; but the
funding model is not the design's yet.

The first snapshot review (`240s4-snap1`) found five blocking issues, all fixed: a value change's
relocation could return without ending its scope; captured detached sets were not enrolled;
`ParseOp` restored a user error twice; the exempt writes appended entries past a refused budget;
the planner and the link guard walked input-sized structures before their first charge.

Found while implementing: `linkApplyBound` lacked the reindex of a list whose compared prefix a
non-key enters (`keyLinked` → `reindexComparedParent`), and `opAnchor` located its anchor with an
uncharged linear scan (now the index). `TestStoredHashValueRelinkBudgetKeepsOldValue` raises its
removal limit from 128 to 192: the index removal's point bound is prepaid before it.

## Evidence

- opidx: TestJournalNestedModel, TestJournalCellHistories, TestPointBoundsCoverJournaledEdits.
- Levels (G3, G2, G5, G7): TestS4LevelWholeCaptureSerials, TestS4LevelIndexRollbackAllocs,
  TestS4RefusedLocalMoveRevivesHandle, TestS4TransferBindingCharges, TestS4CompleteRollbackAllocs.
- ValidateOp (G1, G6): TestS4ValidateOpOneState, TestS4RefreshExactThroughValidation, op/validate in
  TestS4JournalRefusalHistories.
- Scopes (G4, G9): TestS4PrepaidSuffixPreconditions, TestS4PrepaidOverdraftProductionPolicy,
  TestS4PrepaidValueChangeAncestorReindex, TestS4PrepaidCumulativeEnforcement.
- Reads (G10): the detector in every test build, TestS4ReadCreditFirstUnitRefusal,
  TestS4ReadCreditRefusesBeforeWrite, TestS4ReadCreditChargedIndexUnits,
  TestS4ReadOnlyFindFromSuspendedCallback.
- Guard (G1, G8): TestS4CrossStateReachableFromCallback, TestS4GuardMergeDiffCallback,
  TestS4GuardLinkedCallbackEdits, TestS4GuardThirdTreeAllowed, TestS4GuardSameTreeBegin,
  TestS4GuardEnrolledCounterpart, TestS4GuardCallbackPanic, TestS4GuardNesting,
  TestS4GuardReaderAndContextDuringParse, TestS4GuardUserErrorMethods, TestS4GuardUserPanics,
  TestS4GuardValidationPolls, TestS4PollsGoThroughOneHelper, TestS4IndexEditErrorsPropagate,
  TestS4GuardDetachedRequest, TestS4RestoreUserKeepsOurWrapping, TestS4LinkGuardRefusalScaling,
  TestS4KeyRelocationRefusalsEndTheirScope, TestPlannerPaysBeforeEachVisit,
  TestS4ValidateOpPanickingContextError, TestS4CounterpartCaptureRefusalScaling, and the 40-instance
  run of TestHashBudgetCopiedRPCSortReservesOrderedLink.
- Under the race detector the S4 budget sweeps keep the first, a middle and the last limit
  (`s4RaceLimit`); the normal suite sweeps every limit.
- The step-4 preparation histories (TestS4JournalRefusalHistories,
  TestS4BulkPrefixPublicationRefusal) pass unchanged.
