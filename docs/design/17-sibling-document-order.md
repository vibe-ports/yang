# Sibling storage: logical ranks and document order (#247, part 4)

This is the storage portion of design 15, extracted from `207566b2` onto the
kind-aware sequence of part 3. Public producers keep their existing placement;
Ordered parsing and interleaved native lookup/run rules belong to later parts.
Only package tests select an explicit physical placement that interleaves kinds.

`At`, `Index`, `SchemaLen`, `OpaqueLen`, partition spans, range searches and
`orderPosition` use logical coordinates: schema ranks followed by opaque ranks.
The sequence itself holds document order. `docSpan`, `All`, `all`, reverse walks,
`docFirst`, `docLast`, `docNext` and `docPrev` follow physical positions/threads.
After interleaving, logical iterators use the sequence's kind threads, crossing
from the last schema node to the first opaque node. Kind-rank selection and rank
walks cost O(log n); logical lower bounds cost O(log² n); iteration stays linear.

A sticky storage flag records the first schema-after-opaque layout. Its transition
and the sequence's kind threads share the existing journal, so rollback restores
both together. The flag shares the former non-owning-view cell, retaining the
ordinary store's allocation size. All ordinary layouts take their previous
sequence paths and retain their counted work and allocations.

A `siblingPlacement` is a local result containing the physical anchor, logical
partition position and physical position. Reservation (`linkPlacedBound`) and
application (`linkPlaced`) receive the same value; storage bounds, `linkAnchor`,
`linkDocPos` and the key preflight consume that result. It is valid until a storage
mutation. Legacy `link`/`linkBound` wrappers resolve their current logical
placement, so existing callers preserve their behavior. Neither a destination nor
a journal stores a pending placement, target node or source tree. A refused plan
needs no cleanup and cannot retain its source through the destination.

The default schema anchor is the next schema rank, or the physical successor of
the last schema node. Opaque insertion at rank j follows opaque rank j−1 (rank 0
precedes the first opaque node; out-of-range appends). Explicit physical anchors
are resolved directly. The key preflight and `keyLinked` share `keysLead`, which
reads the physical prefix, stops at an opaque child, and charges each inspected
key through its existing caller's counter. A prefix reservation also checks the
prospective layout, so the first opaque-before-schema insertion uses its physical
position before the sticky interleaving flag is published.

Republication expands a logical schema range to its physical extent. Schema
slots take the new schema order; opaque slots keep their handles and positions;
fresh schema nodes follow the extent. Bounds include extent assembly, sequence
republication, journal images and kind-thread maintenance. First interleaving
also reserves the newly inserted handle's part of the thread-enabling pass.
Every storage write touches/captures first, or uses the existing narrow journal.
Legacy append keeps its whole capture even inside a narrow transaction; only
InsertAt and explicit placement use the narrow insertion path.

Recursive duplication that preserves a native children table also allocates its
storage, even if empty. This restores the invariant expected by table lookups.
The regression has local and foreign-context live-oracle requests and never
requires a generated golden. Model seeds 3, 99 and 4099 cover both rank spaces,
physical anchors, republication and deletion; rollback tests cover initial and
already-active kind threads. Work tests compare descending duplicate-heavy
public construction and publication with `origin/main`, with and without a
journal; weak-pointer tests cover reservation and application refusal.

Excluded: ambient placement state, Ordered flags/producers, sorted-placement
routing, interleaved lookup/history adaptation, and dead-node/view machinery.
Run bookkeeping is added in part 5 below.

## Runs

Part 5 extracts physical run boundaries and mutation bookkeeping from `ref332`.
A run piece ends at an opaque sibling or a different schema. `runPiece` and
`runPieceStart` locate the nearest opaque boundary with logarithmic storage
reads before a logical run walk starts. Bounds, retained-run walks and backward
leader walks therefore visit the selected piece, rather than all later instances.
Singleton pieces remain singletons even when another piece uses the same schema.

Document order and registration order remain independent. Interleaved table
construction includes opaque separators in its physical traversal. Leader insert
and unlink read document neighbours, while replacement appends its record to the
native registration chain. Ordinary layouts retain their original reads and
allocation paths. No lookup kernel, equivalent-schema candidate search or
lookup charging policy changes here.

Opaque insertion counts the gap it opens; removal closes that gap before storage
removal. Removing either kind of separator marks the left tree unsorted, while
retaining its RB topology. Subsequent sorted insertion uses the selected retained tree; the
unsorted flag prevents treating the joined run as entirely represented by it. Single and batch unlink use the same physical neighbours and cyclic
tail for singleton metadata selection. Batch compaction rebuilds its summaries
before returning, including on a published refusal prefix.

Sorted placement is an explicit local `siblingPlacement`, after the RB predecessor
or before the leader. Reservation and application consume that placement; after
publication it is resolved again against the current handles. The first
interleaving reserves the ordered-index transition as well as storage kind threads.
Physical table-record construction pays for document assembly and both registration
walks, including opaque separators; point and shared batch bounds reserve those
passes. Existing prepaid scopes refund unused write credit, so charged work equals
consumed work. No pending placement or source node is stored on a destination or journal.
Every mutation uses the existing whole capture or index journal before writing.

Tests build these interleaved layouts internally; public producers remain on the
ordinary schema/opaque layout. Live native replay checks table-threshold changes,
leader insertion/deletion, singleton unlink and unlink-between, including a run
split by another schema node. Regression probes cover physical record construction,
registration ordering, single/bulk parity, separator gaps, retained topology,
opaque-triggered index transitions and budget rollback. The portable ordinary-work
probe compares descending duplicate-heavy insertion and both unlink forms against
`origin/main`, in both journal modes.

Still excluded: Ordered parsing/producers, lookup kernel and consumers, key-prefix
hash reservations, merge/dup/move/diff routing, validation/defaults, codecs, ambient
placement and dead-node/live-view machinery.

Interleaved retained insertion asks only for its selected leader; full boundary consumers still
walk the selected piece and debit each inspected member. The first physical
interleaving consumes its materialization credit before changing lookup mode.
Physical index builds consume their handle-assembly and summary passes separately
from occurrence hooks. Physical table assembly charges at its allocation site;
hash-table creation pays only its two remaining registration passes, avoiding a
double debit. A read-only mixed-leader probe walks document storage directly.

Single and batch unlink share branch-based removal accounting: alone leaders keep
their topology, only removed leaders transfer metadata, and only empty topology
compacts metadata. A transferred item is excluded from the removed node's remaining
queue work. Interior separator-owner collections use the unused cached-tail slot for optional
plan storage, keeping
the ordinary plan at 88 bytes on amd64 (the original 96-byte allocation class).
Selected-piece scaling, actual prepaid-credit consumption, trailing opaque work
comparisons and allocation-byte probes supplement the existing rollback and native
replays. Error propagation at boundary consumers preserves budget refusals.

Ordinary boundary scans retain their original quota even for equivalent foreign-context
schemas and schema-separated fallback runs. Physical scan debits, rebuild reservations,
and opaque-gap maintenance stay on the interleaved branch; ordinary removal keeps its
original conservative allowances. Recovery snapshots for interior owners wrap the existing
record undo only on interleaved layouts with such owners. Ordinary schema-only joins
collect and mark only their physical first owner, as main's single and batch paths do.
Ordinary unlink plans keep partition neighbours for every consumer, including metadata
recovery and transfer reservations, so a retained singleton before an opaque tail does
not snapshot or reserve a transfer to that opaque node.
Portable probes compare credit, visits and allocation bytes with part 4 at
64/256 siblings and both journal modes; native replay includes ordinary relocation and
cold local/foreign insertion.

## Physical key prefixes and reservations

Part 8 shares `keyPrefix` across native key hash preflights, unconditional hash
resets, separator exposure, sort comparisons and their work bounds, path predicates,
and instance comparison keys. It is the document prefix before the first opaque
child; each caller stops at its first non-key. Ordinary layouts keep their previous
schema-length span and counted visits. The first opaque position is a storage
rank read, never a scan of the repeated-key suffix. `keyRun` uses PR 4's already
physical `keyN`; it does not restore ref332's logical-key bookkeeping.

`keysLead` remains the single declared-key completion predicate for reservation
and application. Both receive the resolved document position from the same local
`siblingPlacement`. Value reservation simulates insertion into `keyPrefix` at that
position: a key beyond an opaque child can trigger a hash event when the declared
prefix is complete, while contributing no bytes itself. Reservation and application
also share the historical-prefix restart condition. Positional discriminators use
document positions, including an opaque child within an incomplete copied prefix;
an inner hash event reserves any exposed outer-list reindex as well.

Unlinking a separator exposes consecutive physical keys without changing the
stored hash, captured hash input, or native registration history. If the declared
key prefix was already complete, removing a later separator also leaves every
compared position unchanged. Both unlink forms therefore retain the existing
comparison index, avoiding an unreserved scan of long key values. The next native
hash event replaces or extends the historical input. Existing node touches and
whole/narrow journals capture every prefix/slot write before mutation. No pending
placement, reservation or source node is retained, and refused preflight publishes
nothing. Tests enable overdraft panics and sweep public edit budget boundaries.

Internal construction adapts the six key-prefix regressions from ref332; public
Ordered parsing remains inactive. Live native replay uses repeated-key counts
1, 3, 4 and 17 across parse, change, separator unlink and unlink-then-change,
comparing stored hashes, key-run lengths, child counts and paths. A displaced
multi-key prefix additionally checks native sorted insertion. The ordinary-work
probe runs unchanged on origin/main with long, descending duplicate-heavy values
and both journal modes, measuring hash work, visits and allocations.

Excluded: lookup kernel and consumer routing, duplication/move/merge/diff routing,
validation, codecs, Ordered producers, ambient placement and dead-node views.
The shared path-predicate reader changes here; diff-specific key-copy loops remain
for part 9. Several requested key bookkeeping and document-placement changes were
already supplied by PR 4 and the PR 5 base and are retained rather than re-ported.
