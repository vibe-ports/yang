# Sibling storage cutover: #240 step 5

Step 5 of #240 makes the order-statistic sequence the only physical sibling storage and attaches
the ordered index to it (contract `c240-sibling-sequence.astra.md` §5, step 5; design 13 is
step 4). Behaviour, verdicts and libyang event order do not change; no new deviation.

## What becomes authoritative

- `siblings` keeps one `*siblingStore` (allocated at its first child): an `opidx.Seq[*Node]`
  holding the schema children in `[0, schemaLen)` and the opaque ones after them, and the one
  `orderBinding` its hooks report to. The store is reached through a pointer, so moving or
  capturing a `siblings` value never copies a `Seq` (handles keep their owner).
- A linked node finds its handle in O(1) through `Node.slot`. A non-owning view (`sibsOf`, the
  print and any-copy views) gets handles without slots and is marked as a view; building one
  neither reparents nor re-slots the original nodes.

## What goes away

- `nodeDeque.list`, `siblings.opq` and the deque code (the edit.go duplicate cache keeps its own
  local deque); spans over slices of live storage (a span is a view of the sequence, or of a
  detached slice such as `logical()`); `cloneStorage`, `snapshotStorage`, `restoreStorage`'s
  copy, `exposeSchema`, `retainsBase`/`retainsList`.
- `childOrder.seq` and `childOrder.byNode`: the occurrence and rank indexes register the store's
  handles; `orderBuild` registers existing handles and builds no second sequence; `orderReorder`
  is folded into `replaceSchema`, which unregisters the run, republishes its handles with one
  `ReplaceHandles`, and registers them again.
- Position scans that existed because a slice had no positions: `indexFromEnd`, the scan
  fallbacks of `chargedIndex`, `orderPosition` and `originIndex`, and `indexOf` become one
  handle rank (O(log n)), charged as `orderPosition` charges it today.

## Obsolete bounds

`insertCost`, `opaqueInsertCost`, `linkSlots`, `runSlots`, the `2*linkSlots` recovery term and
`work.shifts` modelled slice shifts and copies. They are replaced by the sequence's point bounds
(`InsertBound`, `RemoveBound`, `ReplaceHandlesBound`, journal-aware), folded into the existing
`orderAddBound`/`orderRemoveBound`/`publicationIndexBound` so `link`, the single `unlink` and the
publications keep prepaying one total. `InsertAt`/`DeleteAt` no longer return shift counts.

## Invariants

1. Every linked child of a set occurs exactly once in its store, schema before opaque;
   `n.slot` is its handle and belongs to that store; an unlinked node has no slot.
2. An ordered index registers exactly the store's handles and owns no sequence.
3. Every store write while a state runs is journaled in that state (`bind.state`, bound by the
   whole capture, by a narrow write or by the store's allocation under a capturer); rollback
   restores the sequence, handles and slots by replay and allocates nothing.
4. Every store write inside a hash-work scope is paid: by its enclosing scope's bound or by its
   own prepay before the first write. Reads by position are uncounted (`At`, iteration); rank
   lookups on a mutation path open a read credit.
5. Retained-RB in-order projection (design 12) is unchanged.

## Test plan

opidx: uncounted access, republish with fresh handles, slot cells, bounds against reported work.
data: a store invariant checker (bijection, slots, partition, `CheckInvariants`) wired into the
tree invariant helpers the sweeps already call; the facade model test against a slice model;
counted scaling of middle insertions and removals in a large user-ordered run and an ordinary
container (1K/8K/64K, logarithmic per edit); a limit sweep over a middle insert and a move that
checks restored storage and a later edit; view builds leave slots alone; a test-build check that
a span is not read after its store changed. Existing S4 sweeps, histories,
`TestAstra305Round13CountedWork` and the repeated-key scaling test must stay green.

## Risks

- Constant factors: one handle per child and AVL work per link (memory, the `./data` race run):
  measured before and after; heavy new tests use `raceScale`.
- Counted-work and budget numbers move where they modelled shifts; each changed expectation is
  justified by the replaced bound, never loosened to pass.
- Live spans: a caller that kept a span across a mutation saw the old slice before; the stale-span
  check finds them.
- Binary search over a span keeps its predicate sequence (its calls are charged), so probes use
  `At` (O(log² n) uncounted structure, O(log n) charged predicates).
