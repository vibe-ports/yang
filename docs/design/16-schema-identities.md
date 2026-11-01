# Schema identities and bounded schema equivalence (#247, part 2)

Cross-context lookups compare schema nodes the way `lyd_compare_schema_equal` does: node type,
module name and node name, level by level. This part makes that comparison fixed-size and adds
the lookup that maps a node into another context. It changes no behaviour.

## Identities

`schema.Ident` is a node's module name and node name, each a `unique.Handle[string]`. The
snapshot interns every module's name once (`Module.SetID`) and every node's name on its own
(`Node.SetID`) while it copies the context, before the set is shared, so a snapshot is
immutable afterwards. The two handles are never interned as a pair: a pair handle would copy
the module name into every node (`TestSnapshotInternAllocs`). A node built outside a snapshot
(hand-built test sets) interns its names when `ID` is called.

Handle equality is string equality, so substituting `Ident` for the name strings preserves every
comparison: `compareSchemaEqual`, `schemaLookupMatch` and the per-tree lookup keys
(`foreignSchemaIndexKey`, `schemaLineageComponent`, `foreignInstanceKey`). Their charges stay
the same; what changes is that the work each charge stands for no longer grows with name length.

## Child indexes

`Set.ChildByName(parent, ChildKey{Kind, ID}, charge)` reads a per-parent index of the set's
children. The top level is every module's top nodes in load order, then the roots of the
modules' extension instances (a structure's `Root`, yang-data's `Nodes`); below it, a parent's
data children (a choice's cases, an operation's input and output), then its actions and
notifications. The first equal key wins, as a sibling walk would find it, with one exception:
an extension root is indexed only when no other top-level node has its key. A shared key is
ambiguous and returns `schema.ErrAmbiguousRoot`, a distinct outcome rather than a guess: which
of several equal roots holds a descendant is not decided here, and keeping candidate lists
would make warm lookups scan them. Later parts decide what an ambiguous root means to a caller.

- Ownership: the index lives in the set and is keyed by the set's own nodes. A parent of
  another set gets no children and stores nothing, so no set retains another context
  (`TestEquivalentCacheKeepsNoContext`). There is no process-wide or weak-keyed map.
- Construction pays as it goes: one unit up front, one per module and extension instance, one
  per child. A charge that polls a cancellation stops the build; a refusal stores nothing. Only a complete index is
  published, with `LoadOrStore`, so concurrent cold builders agree
  (`TestEquivalentInColdBudgetAndDepth`, `TestEquivalentInCancel`,
  `TestEquivalentInConcurrentColdBuild`). A warm read charges nothing.

## Equivalence

`equivalentIn(sn, set, charge)` (package data) returns sn itself when sn is a node of set: within
one context schema pointers are the identity. Otherwise it collects sn's lineage, one unit per
ancestor paid before it is recorded, then maps it from its root down, one unit and one
`ChildByName` read per level. Choice, case, input, output and extension roots are levels of that
lineage, so a case's leaf does not map to an equally named leaf outside the choice, an rpc
output descendant maps (`TestEquivalentInOutputAndScaling`), and so do structure and yang-data
trees with unique roots; under a duplicate root the mapping returns `ErrAmbiguousRoot`
(`TestEquivalentInExtensionRoots`). A level without a module maps to nothing, as
`compareSchemaEqual` now never finds such a level equal (main dereferenced the missing module;
`TestCompareSchemaEqualNilModule`). A deep lineage whose root is missing pays per level, and a
refusing charge stops before anything is collected (`TestEquivalentInDeepRootMiss`). Cost is
O(depth) map reads once the indexes are built; memory is linear in the schema, never in
concatenated ancestry.

`equivalentIn` has only test callers here. `findSchemaCtx` keeps `lyd_find_schema_ctx`'s own
input-first walk, which is not the same mapping; later parts route table lookups through
`equivalentIn`.
