# Operation index primitives (#240, part A)

`internal/opidx` is original Go infrastructure for the later operation-child
integration. It has no dependency on `data/` and does not emulate libyang hash
records. The caller owns schema identities, equivalence keys, and the choice of
which nodes are lookup eligible.

| Structure | Representation | Bound |
|---|---|---|
| `Seq` | Implicit AVL tree with subtree sizes, parent pointers, and threaded physical neighbors | Insert/remove/move/rank/select O(log n); first/last/neighbor and order comparison O(1) |
| `Ranks` | Sparse AVL tree of populated integer schema ranks | Insert/remove/predecessor/successor O(log k), O(k) space |
| `Occurrences` | One AVL tree per `(schema, equivalence key)` ordered by sequence labels | Register/unregister/next O(log r); first/last O(1), O(r) space over registered instances |

Sequence handles remain stable while in the tree. The sequence assigns monotone
integer labels. On an exhausted gap it relabels the smallest enclosing tag
range below the density threshold of the tag-range algorithm in
[Bender et al., §3](https://erikdemaine.org/papers/DietzSleator_ESA2002/paper.pdf).
The fixed 63-bit tag universe is rebuilt at size changes by a factor of two;
relabel and rebuild work are amortized O(log n) per insertion. Relative order
does not change during relabeling, so an occurrence tree remains ordered.
Callers must unregister a handle before removing or moving it in the sequence.

Each structure offers `SetWorkHook(func(uint64))`, called for node visits,
rotations, and relabels. Hooks are synchronous and caller owned. Integration
will use them to charge structural work to a budget; this package does not
charge budgets. Input and output anchors use separate `Ranks` instances.
Likewise, document-first and exact-equivalence lookups use distinct
`Occurrences` instances or keys. Exact equivalence is determined by the caller,
not by canonical text or sorting comparison.

## Part B: `data` integration

Operation children now have one mutation-maintained `childOrder`. Its `Seq`
contains stable handles for every schema-backed and opaque child in physical
document order. Per-schema, exact leaf-list value, sorted-run group, opaque
name, and opaque name/namespace lookups are separate `Occurrences` indexes;
input and output anchors use separate sparse `Ranks` trees. Deferred parser
publication moves existing handles instead of rebuilding per-schema run
deques. The generic sibling slices remain the public iteration/storage view.

The ordered index is acceleration state only. It never creates or reconstructs
libyang-compatibility state: `hasChildrenHT`, ordinary hash records,
schema-alias records in registration order, and first-instance records
(including D-0117 ordering) keep their independent mutation and rollback
lifecycle. The access paths are intentionally distinct:

- `documentFirst` reads the ordered per-schema occurrence index;
- `schemaLookup` / `schemaLookupOperation` preserve native hash-record lookup;
- `walkFrom` follows physical sequence neighbors and stops at a run boundary;
- `allInstances` walks every ordered occurrence of a schema across split runs.

Structural node visits, rotations, and relabels feed the data-tree work
counter. Insert and remove reserve conservative structural work before
publishing a mutation, so an exhausted hash/work budget returns `ErrBudget`
without leaving a partial index update. Whole-subtree keys used to narrow
keyless-list merge comparisons charge the same budget; equality is still
confirmed with the libyang-compatible comparison.

The part-B counted-work tests close these constructions:

| Construction | Guard |
|---|---|
| identical replies with a large output leaf-list | `TestOpIndexMergeIdenticalReplyScaling` |
| split-run lookup and value changes far from either end | `TestOpIndexSplitValueChangeScaling` |
| XML operations with many distinct unknown names | `TestOpIndexXMLDistinctOpaqueNamesScaling` |
| sparse populated ranks in a wide action/RPC | `TestOpIndexSparseRanksScaling` |
| keyless-list merge candidate comparisons | `TestOpIndexKeylessMergeScaling` |

One construction remains: inserting into a generic `unsorted` run can still
move the shorter side of the shared `nodeDeque` slice. Replacing that storage
for all data-tree iteration is deliberately left for a follow-up; the
operation-specific deque and dense rank/run machinery are removed here.
