# Operation index primitives (#240, part A)

`internal/opidx` is original Go infrastructure for the later operation-child
integration. It has no dependency on `data/` and does not emulate libyang hash
records. The caller owns schema identities, equivalence keys, and the choice of
which nodes are lookup eligible.

| Structure | Representation | Bound |
|---|---|---|
| `Seq` | Implicit AVL tree with subtree sizes, parent pointers, and threaded physical neighbors | Insert/remove/rank/select O(log n); first/last/neighbor and order comparison O(1) |
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
