# Native registration table (#247, part 6 redesign)

**Authored by astra; jointly reviewed by the lead (Opus) and astra on 2026-11-05; signed off by both on sha256 5783048…f064; maintainer decisions recorded in §9.1.** Production lifecycle **B is
settled**: compact registration state is authoritative from construction;
expensive accelerators are optional, paid projections. Lifecycle A is only the
initial private development harness. Accepting A does **not** enable PR 7.

Baseline: `ecb6fd1d0fd6367809b70f442a2a4eb5c7063cfc` (parts 1–5 and 8 merged).
Every `src/` citation below means libyang v5.8.6,
`47351e59e2965e350f9d4098f15ecbe6b3850f6f`. Native semantics mean the canonical
linux/amd64 **Release/NDEBUG, built-in hash** build
(`conformance/oracle/README.md:55–65`). Citations were checked against these
sources, not the rejected `old-p6-lookup` implementation (`ed433f87`).
Appendix A records the sweep, including exclusions. Trace names are acceptance
requirements, **not executed results or manifest IDs**. Docker access was denied;
no runtime measurement, replay, or CI success is claimed by this document.

## 1. Registration semantics

A registration is one occurrence of **(node pointer, recorded hash)**. There is
no schema/instance-origin discriminator. Current value, current hash, physical
position, and registration history are independent. This replaces the split
`ht`/`history`/`leaders` authorities at `data/siblings.go:45–123`; it does not
reconstruct them more accurately after a mutation.

### 1.1 Generic primitives

| Operation | Required transition | Checked native source |
|---|---|---|
| State | Power-of-two capacity, minimum 8; `used` counts occurrences; bucket `hash & (size-1)`; FIFO chains; free slots reused. Resize modes 0 disabled, 1 growth only, 2 growth/shrink. | `src/hash_table_internal.h:27–90`; `src/hash_table.c:86–141,282–307,403–424` |
| Checked insert | Search requested full hash with modification callback (pointer equality). Existing pointer/hash returns EEXIST without a write. Otherwise append one occurrence, increment used, apply thresholds. | `src/hash_table.c:385–449,460–469`; `src/tree_data_hash.c:92–105` |
| Unchecked insert | Append even the same pointer/hash again; growth reinserts unchecked. | `src/hash_table.c:394–437,466–469` |
| Remove | Find earliest pointer/full-hash occurrence; absent means ENOTFOUND, no write. Unlink that slot, push it onto free-list head, decrement used, possibly shrink. | `src/hash_table.c:472–529` |
| Thresholds | After insert, percentage `uint32(used*100)/size`: mode 1 becomes 2 at 50%; mode 2 grows at 75%, in that order. Remove in mode 2 shrinks below 25% if size > 8. | `src/hash_table.c:423–447,510–526` |
| Resize | Fresh arrays; preserve mode, reset used, visit old buckets by index and chains forward, reinsert recorded hashes with ordinary insertion logic. Grow inherits check flag; shrink checks. Release ignores EEXIST from checked reinsertion: only the first identical pointer/hash survives. Distinct equal-valued pointers and one pointer at different hashes survive. | `src/hash_table.c:211–266,437,520`; `src/tree_data_hash.c:99–105` |
| Find | Full hash filter precedes the selected read callback; earliest qualifying occurrence wins. Callback override is local to the read, never a mutation of the shared table callback. | `src/hash_table.c:282–333` |
| Find-next | Re-find the **earliest** matching occurrence using `mod=1`, then search later equal-hash occurrences using `mod=0`. This is not a slot cursor. An override replaces the callback in **both** phases. Initial re-find failure logs LOGINT and returns EINT. | `src/hash_table.c:336–382` |

Supported arithmetic domain: capacity `8..1<<25`, used at most
`floor((2^32-1)/100) = 42,949,672`, sentinel `0xffffffff` never live. Check used+1,
percentage multiplication, doubling, free-slot availability, page/address
arithmetic before each insertion, including reinsertion. Compute percentages
with widened checked arithmetic only inside that domain. Exceeding it is a Go
resource refusal governed by §5, not native semantic failure or wrapping
arithmetic. Native's last initialized free link is `size`, not the sentinel
(`src/hash_table.c:93–100`); resizing children tables grow before using it. Keep
that inert link in primitive traces; never dereference it. No children-table
caller uses the generic resize-callback override (Appendix A).

### 1.2 Children-table events

`I(n)` is `lyd_insert_hash`; `U(n)` is `lyd_unlink_hash`. Every sub-operation
includes its resulting resize **before** the next sub-operation.

| Event | Contract | Native source |
|---|---|---|
| Eligibility | I does nothing for top-level/opaque nodes, opaque parents, or anydata/anyxml parents. U uses its own native guard; do not replace it with I's guard. | `src/tree_data_hash.c:167–178,207–218` |
| First build | Count linked schema children, skipping opaque children for the count. At **four**, allocate `max(8,nextPowerOfTwo(count))`, mode 1. Traverse physical order including opaque separators; finish each schema child's entire add before advancing. | `src/tree_data.h:858`; `src/tree_data_hash.c:181–197`; `src/hash_table.c:538–553` |
| Per-child build add | Unchecked current-hash insertion; if list/leaf-list run start, checked schema-hash insertion. Run start is `!prev->next || prev->schema != node->schema`, with cyclic tail and **schema pointer** identity. | `src/tree_data_hash.c:128–164` |
| Build stop | First failed add stops traversal. Retain allocated partial table and successful prefix, including the failing child's instance occurrence. Later insert uses this table; never fill the missing suffix or rebuild from current children. | `src/tree_data_hash.c:135–160,192–200` |
| Existing-table I | Unchecked instance insertion; if run start, remove same-schema successor at schema hash when present, then checked-insert n at schema hash. Stop on first failure. Either removal may consume an instance-origin collision. | `src/tree_data_hash.c:135–160` |
| U while linked | Remove n at its **current** hash; stop if absent. If run start, remove n at schema hash; stop if absent. Checked-insert identical-schema successor if present. Only then may physical unlink occur. | `src/tree_data_hash.c:207–246`; `src/tree_data.c:1188–1239` |
| Parent key completion | After child L/I, execute H(saved parent) then I(saved parent), **without U(parent), only if the inserted node is schema-bound, has `LYS_KEY`, the saved parent and its schema are non-null, and `lyd_insert_has_keys(saved parent)` succeeds**. A non-key or opaque insertion does not refresh or re-register its parent, even if the parent's leading keys are complete. Repeated key insertions satisfying this guard can create repeated parent occurrences. The I(child) helper stopping does not itself prevent this later guard in the void caller. | `src/tree_data.c:835–842`; Go `data/lyd_hash.go:172–205`, `data/insert.go:2844–2859` |
| Recursive dup initialization | Before children, create present-empty destination table at **source capacity, mode 1**; copy children with LAST insertion. Do not copy occurrences/mode or infer capacity from child count. Nonrecursive inner copy does not inherit the table, but keyed lists copy leading keys. Any payload copying has separate recursion (§4). | `src/tree_data.c:2296–2329`; Go `data/dup.go:198–359,441–446` |

Native void callers may continue after an I/U helper stops
(`src/tree_data.c:835–842,1193`). Keep that successful prefix. D-0118 suppresses
only the declared internal first-instance record diagnostics while preserving
records (`conformance/deviations.md:54`); it does not authorize build continuation,
resize-independent identity, or suppressing validation's different helper (§2.2).
Table presence survives empty physical storage and is not inferred from a count.

## 2. Reads, comparison and actual sibling cursors

### 2.1 Dispatch and operand order

The kernel receives the **supplied starting node**, operation, target/schema,
read allowance and polling mode. An existing empty `siblings` is not a non-null
native sibling argument. Schema, instance **and value** APIs return ENOTFOUND
for a null supplied sibling **before table selection or target construction**;
retain the table/history untouched (`src/tree_data_common.c:1093–1099`;
`src/tree_data.c:3189–3195,3274–3280`). T-null distinguishes empty storage with a
live residual winner, that same table with one opaque child, and a later insertion.

| Read | Native rule; no fallback after a handled miss/error | Sources and Go replacement site |
|---|---|---|
| Schema table | Hash module/name/finalization. All occurrences at that full hash participate; compare schema with parents, ignoring registration origin/current candidate hash/value. **No instance parent guard.** | `src/tree_data_common.c:1075–1115`; `data/siblings.go:1652,1684` |
| Instance entry/table | If both supplied sibling and target are schema-bound, compare their data-parent schemas with lineage **before rewind**. Then rewind; table branch uses target stored hash and target-first comparison for lists/leaf-lists, schema-with-parents for other kinds. | `src/tree_data.c:3197–3236`; `src/tree_data_hash.c:107–117`; `data/siblings.go:1991` |
| Duplicate-instance first | Keyless list/non-config leaf-list: schema lookup, then the winner's current next chain while schema-equivalent without parents; compare target first with full recursion. Stop at opaque/different-schema separator, never jump runs. | `src/tree_schema.h:1793–1795`; `src/tree_data.c:3213–3227`; `data/edit.go:398` |
| No table, schema | Rewind to first; stop at first opaque. Same context compares schema pointer; foreign context compares module/name only. | `src/tree_data_common.c:1117–1142`; `data/siblings.go:1684` |
| No table, instance | Rewind and walk all physical siblings across opaque separators. Compare **candidate,target**; full recursion only for duplicate-instance target. | `src/tree_data.c:3238–3250`; `data/siblings.go:1991` |
| Opaque name | Opaque supplied start: scan forward from it. Schema start: rewind; schema tail means miss; otherwise rewind trailing opaque block and scan forward. Do not scan a logical opaque partition. | `src/tree_data.c:3388–3418`; `data/edit.go:2049` |
| Value API | Non-null list key/leaf-list value constructs a typed target then instance lookup; otherwise schema lookup. Preserve null-start early return before this preparation. | `src/tree_data.c:3267–3309`; `data/edit.go:328–377`, `data/diff_apply.go:455` |
| Nested comparison | System-ordered keyed/config children select through sibling-first, then compare full data. Both sibling cursors advance independently; the selected winner need not be the second cursor node. | `src/tree_data.c:1830–1865`; `data/siblings.go:2264,2320–2456` |

Cross-context schema comparison checks kind, module/name and requested complete
ancestor chain, including choice/case/input/output, **not** key declarations,
types or module revision. Without parents it still checks immediate-parent
presence parity. Same context means pointer equality. List comparison uses the
first operand's key positions after the stored-hash gate; keyed/keyless foreign
schemas must not be excluded by a Go discriminator
(`src/tree_data.c:1509–1543,1707–1785`; `data/dup.go:61–117`,
`data/lyd_hash.go:106–126`, `data/siblings.go:2361–2456`). Preserve existing D-0129's safe any-representation
inequality (`conformance/deviations.md:72`).

`Node.Equal` is an explicit exception to the paid/cancelable entry contract:
retain an **unbudgeted, non-cancelable, read-only** mode, with the same native
selection semantics and `Defaults`/`Opaque` options. No default quota, hidden
ErrBudget→false conversion, or new API is proposed
(`data/compare.go:24–33`). Nested winner tests must enter through public Equal as
well as paid mutation comparators. Use incremental cursors/frames in both modes;
never materialize whole child sets before a paid comparison's first debit.

### 2.2 Two distinct duplicate operations

| Operation | Required behavior and stopping | Baseline migration / acceptance |
|---|---|---|
| **Validation duplicate check** | Skip duplicate-instance schemas. With parent's table, use find-next override `lyd_val_dup_val_equal`, which always forces **read/value equality, even during initial re-find**. Find first equivalent occurrence at n's stored hash, then another. Two registrations of n itself can mean duplicate. Do not skip same pointer or moved candidates. No table: supplied-first forward scan, skip n itself; leaf/any compare schema pointer, other kinds compare candidate,n with options 0. Duplicate is EVALID; Operational list/leaf-list emits warning and succeeds. | Native `src/validation.c:624–687`; replace `dupIndex.bucket` and `duplicates`, not just their index (`data/validate_new.go:428–503`). **6b5d/T-val-dup** before validation acceptance. |
| Validation initial re-find failure | Partial table may lack even the first equivalent occurrence. Find-next logs LOGINT/EINT; `lyd_validate_duplicates` tests only success, so returns success without a duplicate verdict. Preserve diagnostic and return status separately; NEW clearing and subsequent autodeletes follow that return, including MultiError's existing stop rules. Never suppress this with D-0118 or turn it into an ordinary miss silently. | `src/hash_table.c:343–347`; `src/validation.c:655–658,1048–1065`; Go `data/validate_new.go:80–98`, `data/defaults.go:143–194`. Trace helper rc, enclosing rc, diagnostics, flags and following deletion separately. |
| Consumed-instance collection | Same-context/parent preconditions; distinguished first from sibling-first. Table: find target/hash, filter other candidates **candidate,target**, then continue with **iter's current hash**. No table: **target,candidate**. Full recursion only for duplicate-instance schemas, otherwise options 0. Equal keys/different descendants therefore both enter a keyed-list set. | `src/tree_data.c:3312–3385`; replace candidates in `data/edit.go:1699–1746`. **6b4m/T-set**; used by merge/diff before any standalone collection API. |
| Cached consumption | Cache lookup uses first pointer plus current hash; collect once when used==0, over the selected instance's **current siblings**. Preserve saved pointer-set/order and used position across relocation/value changes. Exhausted dup-instance/user-ordered kinds return nil; other kinds retain supplied instance. Derived candidate indexes may invalidate; collected sets may not rebuild. | `src/tree_data_common.c:56–143`; `data/edit.go:1079–1170,1699–1746`. Journal set/used at paired marks. |
| Repeated-pointer cycle | Ordinary find-next can repeatedly re-find the same earliest pointer and return that pointer again. Set uniqueness does not ensure progress. Keep bounded producer collection; exhaust its existing allowance with an error and no partial successful set. A slot cursor is not equivalent. No new standalone collection API is included. | `src/hash_table.c:344–375`; `src/tree_data.c:3353–3364`. Native watchdog marks nontermination; terminating controls compare exact order. |

### 2.3 Selected node versus queried storage

Return `(queried table, winner, actual storage/parent, cursor, generations)`.
Derive a handle cursor in **winner's current siblings**, or a detached singleton;
never require a source-storage rank. Duplicate-instance continuation follows that
cursor; LYD_LIST_FOR_INST pointer-tests even the first match, whereas getnext-data
accepts the first foreign match then pointer-tests successors
(`src/tree_data.h:495–498`; `src/tree_data_common.c:177–185`). Replace integer-only
semantic `schemaIndex`, `instanceRun`, `opRun`, and sorted-leader adapters
(`data/edit.go:398–443`; `data/insert.go:1178–1268,1604–1627`).

Macro callers are semantic consumers too. `LYD_LIST_FOR_INST_SAFE` saves the
actual current `next` **before** each body (`src/tree_data.h:508–511`). Min/max
counts only that selected run and stops at its native threshold; default deletion
performs its explicit-instance search and then a fresh selected-run traversal,
preserving the first-element pointer test. User-order diff collection rewinds the
supplied first node and searches **first->schema**, which need not equal the
separate array-cache schema argument (`src/validation.c:816–907,1220–1246`;
`src/diff.c:539–568`). Migrate `instances`, `minmax`, both default-delete selectors,
and errorless `userordGet` (`data/validate_new.go:209–286`, `data/validate.go:582`,
`data/diff_gen.go:337`) with explicit error propagation. Keep D-0121's array
preconditions; never collect an entire physical schema partition instead.

Reads guard current continuation storage and descendants/prefixes (§3), not only
the queried forest. Mutations of a selected node use its actual binding. Removal
uses actual predecessor rule, then selected RB metadata, then U/X in the removed
node's own storage (`src/tree_data.c:1251–1262`; `data/rb_remove.go:125–220`).
Existing D-0121 applies at its actual RB/profile/refusal boundary, never merely
because a selected node moved (`conformance/deviations.md:55`).

**Remote write boundary.** Valid current-storage reads do not prove that a native write preserves consistent sibling lists. The 6a0 public replay reaches `lyds_link_data_node` with remote predecessor v3, last in Q, and the querying first-sibling variable from P (`src/tree_data_sorted.c:1020–1039`). `lyd_insert_after_node` sets P.first.prev to v4, links v4 after v3 with Q as its parent, and leaves Q.first.prev at v3 (`src/tree_data.c:595–627`). The forward chains remain P=[a,a2,v2] and Q=[v3,v4], but their tail/back-link invariants are inconsistent. `lyd_first_sibling(v3)` terminates by returning Q.child through its parent fast path (`src/tree_data_common.c:770–791`); this replay does not establish a rewind cycle. Specific subsequent reads remain defined. Refusal is therefore an intentional restriction of native-success behavior with observable results, justified by preserving the port's forest representation, rather than by claiming the whole continuation is undefined.

**Public reachability is established by 6a0.** The executed sequence uses one context with
`rpc r { input { leaf a {type string;} leaf a2 {type string;} leaf-list v {type uint32;} } output { leaf o {type string;} } }`
and three `r` roots P, Q, R. In P create `a`, `a2`, `v=1`, `v=2` and output `o`;
in R create seed3 with `v=3`. Let v3 be the new copy returned by `Dup(seed3→P, NoSort+NoMeta)`; every subsequent v3 denotes this copy, not seed3. The anchor search only looks at
following *input* schemas, so the copy is appended after `o`
(`src/tree_data.c:518–534,2466–2470`) and becomes a second `v` schema occurrence.
Unlink `o`; insert v3 into empty Q (P's U removes only v3's instance occurrence,
`src/tree_data_hash.c:220–246`; it is an RB nonmember, `src/tree_data_sorted.c:966–970`,
`data/rb_remove.go:230–237`); unlink v1, so P's schema occurrences are [v3, v2];
then `NewTerm(P, v=4)`. Schema selection returns remote v3, and insert-after
receives P's first variable (`src/tree_data_sorted.c:1027–1038`). Go equivalents:
`Tree.NewInner`, `Tree.NewTerm` (`Output` for o), `Tree.Dup(seed3, P, DupOptions{NoSort: true, NoMeta: true})`,
`Unlink`, `Tree.Insert`, `Tree.NewTerm`. T-remote retains this exact public replay and its intermediate registrations, RB membership and links. It establishes the boundary without seeded private table state or an earlier D-0121 refusal. The remote-nonlast control completes with consistent links and matching native JSON. The maintainer's refusal decision remains separate from this reachability evidence.

Safety policy (maintainer decision 2026-11-06, §9.1: refuse): use a **bounded plan for the
affected native link primitives**, recording their first-variable, parent-child,
predecessor and successor writes. Check explicit before/after/nonlast/last and
root-update cases; no general arbitrary-graph representation or whole-forest
validity scan is required. The complete insertion plan, including cold RB
creation/relocations, must be accepted before any of its RB, link or registration
writes or source detachment. Accept remote nonlast/before cases only when that plan
preserves the valid doubly-linked forest represented by parts 4/5; register using
resulting parent, and retain native's saved parent for later key completion
(`src/tree_data.c:630–645,801–847`). If it would cross-wire first/tail links,
create a cycle, or alias forest roots, refuse the failing insertion unit with
LY_EINVAL before its RB/link/registration writes or source detachment. Do not
silently repair links, drop the winner, or use a local leader. Earlier completed
units follow §5; a containing relocation/whole-move savepoint restores its unit.
Planning budget/guard failure retains its normal precedence over this refusal.
This is a **new narrow M5 deviation** with its own entry and the 6a0 replay as its fixture, not implicitly D-0117/D-0121. D-0121 keeps its closed boundary; D-0117's deletion cleanup does not apply to a live move and must not purge P's occurrence. P's residual v3 occurrence is preserved, so later schema lookups still select v3 in Q, and a repeated attempt refuses again. Registry rationale: an API representation-safety restriction, not an RFC validity verdict. RFC 7950 §7.7.7.1 permits implementation-chosen system ordering but does not prescribe sibling links or require LY_EINVAL here; the port deliberately rejects a native-success insertion whose primitive would leave inconsistent sibling lists.

T-remote retains the representable remote-read/write controls and the exact remote-last replay, recording both forests' first/tail/prev/next/parent links and RB ownership. Keep the observer stop immediately after the inconsistent link primitive. The separate bounded `lyd_first_sibling(v3)` control must assert termination and Q.child, not nontermination. In isolated continuations that complete the public insertion, record JSON for P/Q, the public schema lookup through P, and whole-root release in a separately checked cleanup control; do not infer safety for arbitrary unlink/free routes. The Go refusal control must preserve the pre-insertion links, RB metadata and registrations, including P's residual v3 winner, with §5's prefix/rollback behavior and budget precedence. Before-remote-first/root-variable variants use the same bounded primitive plan; insert-before is not presumed universally representable.

## 3. Production state, ownership and lifetime

Replace the seven registration fields (`ht`, `exactByNode`, `history`,
`exactOrder`, `leaders`, `leaderByNode`, `leaderSeq`) at
`data/siblings.go:47–98`; retain document handles, physical runs/RB topology and
key-prefix machinery (design 17; `data/siblings_access.go:29–54`,
`data/node.go:64–88`). `firstRun` is physical/RB information, never a substitute
for a native schema winner.

Proposed compact fields (layout arithmetic only, **not measured heap savings**):

```go
// Private conceptual types; one record per occurrence, no serial/origin bit.
type nativeState struct {
    pages *nativePages
    domain *registrationDomain
    size, used, free uint32
    mode, flags uint8 // includes present; absent differs from present-empty
}
type nativeOccurrence struct { node *Node; recordedHash, next uint32 }
type nativeBucket struct { first, last uint32 }
```

On a 64-bit ABI these fields require 32/16/8 bytes respectively. Use small
8/16/32-cell pages then bounded pages of at most 64 cells and fixed-depth
indexed directories; charge initialization, copying, inactive cells and
allocator rounding. Optional full-hash/radix indexes hold **slot references in
native subsequence order**, never independent membership. No schema/value/key
filter may exclude a callback match. Read probes may privately build/discard a
paid projection; only a prepared mutation/setup publishes one. No ordinary-path
mirror, mandatory prefix trie, sparse-map enumeration, or timestamp merge.

| Binding/lifetime | B contract | Baseline integration |
|---|---|---|
| Construction | Bind a domain before first native event/publication; initial table absent. Create at most one new durable domain for each genuinely independent public forest/result being constructed (new Tree, returned diff result, independent Dup), and bind it before its first native event/publication even if construction later fails. Survival controls eligibility for §7's ownership allowance, not whether construction needs a binding. A helper Tree or Dup into an existing destination is not a new independent result. Nodes destined for an existing forest bind its domain before their first event, even while detached/pending: merge-miss copies (`data/edit.go:1830–1857`), ApplyDiff creates (`diffDupCreate` takes the destination binding, keeps the source schema context; `data/diff_apply.go:240–242`), implicit/default and dummy-when nodes (`data/defaults.go:296,316,331`), payloads for adoption. Diff roots/ancestors/key copies share the one result domain (`data/diff_gen.go:113`, `data/diff.go:395–446`). Find-by-value targets (`data/edit.go:328–377,450–480`) and XPath probes (`PrepareInstance`, `toCtx`, `data/xpathnode.go:491–550`) are operation-private scratch, with no domain per target. Moved/adopted public nodes keep their original domains. | `data/node.go:165–212`; `data/new.go:36–48,539–554`; `data/dup.go:198`; `data/defaults.go:331` |
| Tree/Node entry | Tree resolves through top state; every Node, including terminal/opaque/detached, through its own embedded state. Any payload storage also carries a binding. No global pointer map, ancestor search, schema cache rooting data, or activation at first lookup. | `data/node.go:60–95`; `data/insert.go:2489–2546`; `data/suspend.go:42–50` |
| Transfer/reset | Keep descendant table identities. A moved candidate keeps its domain; destination parent owns its new occurrences. Work-counter adoption is separate. Reset leaves a surviving public container discoverable, not an unbound zero state. | `data/siblings_access.go:568–608`; `data/move.go:803`; `data/any.go:376–400` |
| Residual live references | U removes only specified occurrences. Remaining source slots strongly retain moved/detached candidate, **current siblings/continuation**, children/any tables and prefixes. Follow and guard actual reached bindings, even if a same-domain hint predates a move. | Native U `src/tree_data_hash.c:207`; Go broad `hashForget` at `data/siblings.go:1131–1137` must cease being authority. |
| Guards/enrollment | One paired operation enrolls all writing domains before writes; refuses an already-held/differently governed domain. Before calling user code/polling, acquire guards for reached candidate/current-storage/payload bindings. Charge discovery; no full-forest scan on ordinary local path. Dynamic paged participant set replaces any assumption that four held counters suffice. | `data/hash_transaction.go:692–717,1206–1244`; `data/suspend.go:241–254` |
| Disposal | Drop table's outgoing occurrences/binding; do not clear descendants/prefixes still reachable through another slot/public node. Idle domain descriptors retain no forest root, work pointer, journal or inventory. A separately collectible descriptor must not coallocate with and retain its originating Tree. | Native whole-table-before-subtree free `src/tree_data_free.c:234–246`; Go `data/any.go:403–500`, `data/siblings_access.go:590` |
| Inactive cells/undo | Clear inactive node/projection cells on removal/retirement; free links are integers. Retain old page/root in undo until last restoring frame ends; clear popped entries and retired references on outer end. Slot/address reuse never revives an old occurrence. | Native free-list transition `src/hash_table.c:507–511`; baseline whole captures `data/hash_transaction.go:991–1098` |

No generic dead-node filter or purge is introduced for other residual references.
Go retains referenced data safely; native post-free dereference has no defined
winner and is excluded from equivalence claims. Logical deletion does not grant
permission to sweep every table. Whole-forest disposal is not N synthetic unlinks.

**D-0117 cleanup is explicit and deterministic.** Only for the declared removed
operation child (`conformance/deviations.md:53`), after U/X and before release,
clean its residual pointer/schema-hash pairs from its just-unlinked table. In
producer deletion order, repeatedly probe the earliest such pair, execute
ordinary checked removal (free-list push, used decrement, normal mode-2 shrink
and checked reinsertion), then re-find. A shrink may eliminate other repeated
pairs. Stop on no pair without an absent-remove diagnostic; on transition/resource
failure use §5. Retain table presence/mode; do not clean mere moves, other hashes
or other forests. Specify this refinement in D-0117's eventual implementation
PR; do not pretend it is native free behavior. T-D0117 starts size16/mode2/used4,
removes one residual pair, shrinks, deduplicates another repeated pair, and ends
size8/used2; add no-match/repeat/rollback controls.

A's private harness uses **one shared owner for all interacting forests**, known
before construction, and rejects unsupported routes/cross-owner access before
mutation. It leaves main's ordinary layouts/costs untouched. A never promotes a
legacy mutated tree; B supports persistent cross-domain references as above.

## 4. Producer events and reads that choose them

Notation: **L/X** physical link/unlink, **I/U** §1 table operations, **H** stored
hash refresh, **V** value change, **P** prefix binding change, **F** table disposal.
Metadata selection precedes normal U/X. An opaque node has L/X effects on
neighbours even though its own I/U is ineligible. Native event order is distinct
from Go storage publication order.

Each adapter implements `prepare(event) -> plan, semanticStatus, error`, then
`publish(plan)`. Event contains node/table binding, before/after
`{first,tail,prev,next,parent}`, saved parent/first-sibling arguments, current
value/hash overlay, selected RB owner, **native attachment frontier**, and paired
frame/generations. Preparation is charged, cancelable and private (§5); it
secures undo/write credit. Publication is single-use and nonfallible. Any
intervening change invalidates the plan before publication. Initial builds and
existing-table adds consume the same frontier: already-attached nodes in native
physical order, including opaque separators, excluding later batch nodes.

Every mutation-required lookup must migrate **with or before** its adapter,
including sorting/removal, ordinary anchors, duplicate validation, implicit
presence, mandatory/unique, instance-id/leafref/when/must, dup key reuse and diff
metadata matching. Neither single-context nor contiguous-run status proves that
legacy lookup is equivalent after collisions/partial build/shrink. Appendix A
maps each native direct consumer; no such decision may wait for PR 7.

| Family | Native event contract → current Go site | Stage / trace |
|---|---|---|
| Builders/NewPath | Place L(n); call I(n); **only under the complete key-insertion guard in §1.2**, H(saved parent); I(saved parent), with no U(parent). Neither an ordinary child nor an opaque child causes parent H/I. Existing-path/key lookup uses §2. Late creation failure cleans first created subtree with U/X/F. Native `src/tree_data_new.c:1568–1862`, `src/tree_data.c:801–842`; Go `data/new.go:183–424,539–684`, `data/edit.go:491–650,2063–2069`. | 6b1i / T-build, T-new, T-key |
| Single/bulk unlink/free | Select actual RB owner; U while linked; X; logical deletion policy/F as appropriate. Batch uses each step's then-current neighbours and intervening shrink, not original/final batch neighbours. Native `src/tree_data.c:1188–1290`, `src/tree_data_free.c:214–262`; Go `data/insert.go:2920–3079,3154–3379`, `data/unlink_plan.go:29`, `data/rb_remove.go:125`. | 6b1u / T-remove, T-D0117 |
| Cold sorted insertion | Walk original run, relocate every non-running maximum: U; X; L at RB position; I. Running maxima have no registration event. Then incoming L; I. Existing retained root does **not** replay old members. Native `src/tree_data_sorted.c:1052–1104,1212–1253`; Go `data/insert.go:330,814,1178,2640–2790,2881`. Remote links use §2.3. | 6b1s / T-sort, T-remote |
| Immediate parse | `nodeInsert` preserves ordered/opaque/default placement and native key-completion events. Existing-parent enrollment precedes Reader callback. Native `src/tree_data.c:801`; Go `data/parse.go:227–363,788–827`. Envelopes reuse this route (`data/envelope_json.go:45`, `data/envelope_xml.go:152`). | 6b1pi / T-parse |
| Deferred parse | Native has already sorted n when I runs. `appendSorted` must use **sorted virtual neighbours even with an existing table**, not its Go appended neighbours. `applyLeaders` publishes metadata only; `publishDirty`/`flushDirtyRun`/`flushDirtyAll` generate **zero I/U**, including repeated/reader/error flush. Native `src/tree_data.c:820–842`; Go `data/parse.go:829–965,1068–1220`. | 6b1pd / T-deferred |
| JSON metadata/physical publication | Annotation matching is a native document scan; placeholders change frontier, not registration. Split shared `schemaIndex` calls into **physical helpers** at `data/parse_json.go:449,969` and `data/parse.go:1174`; a partial/remote table winner must not locate a dirty run. Native `src/parser_json.c:582–730,1146–1219`; Go `metadataFinish`/`parseAttribute` at `data/parse_json.go:397,937`. | 6b1pm / T-physical |
| Parse failure | Publish funded dirty native order, then reverse parsed-set cleanup, retaining refused keys. This is forward U/X/F, not journal undo; ordinary error/cancel versus hash/guard recovery differs (§5). Native cleanup `src/tree_data.c:482–489`; Go `data/parse.go:298–361,1245`. | 6b1pc / T-completion |
| Value/key changes | Leaf-list target=n; a key with a data parent targets its parent list. Non-relocation: U(target at old hash); V; H(target); I(target). Sorted relocation: U/X; V/H; cold/retained placement; L/I. Other terms, including a detached key, change only V, with no H/U/I. Equal/flag-only changes have no registration event. A detached leaf-list or list target's I/U guards naturally no-op. This value-change path is distinct from §1.2's key-insertion completion. Native `src/tree_data_new.c:1130–1263`; Go `data/edit.go:827–1077`, `data/value_reinsert.go:11`. | 6b1v / T-value |
| Dup inner/keys/parents | Recursive inherited table before first child (§1); each LAST child insertion obeys §1.2's key-insertion guard. After copying its children, `lyd_dup_r` explicitly computes H(dup) for an inner node, then performs the outer L/I; this final hash is distinct from insertion-triggered parent H/I. Nonrecursive leading keys can **naturally build** a fresh table at threshold, but never inherit source capacity. Existing-key reuse uses schema lookup and no new key insertion event. WithParents copies ancestors/keys; preserve D-0065 cleanup of only new copies. Native `src/tree_data.c:2169–2507`; Go `data/dup.go:198–359,376–612`; deviation `conformance/deviations.md:47`. | 6b2d / T-dup |
| Dup any payload | Even nonrecursive outer dup calls any-copy, which recursively copies payload siblings, retaining descendant table capacities and payload contexts. Native `src/tree_data.c:2321–2325`, `src/tree_data_new.c:352–359`; Go `data/dup.go:233–291`. | 6b2d / T-dup-any |
| Any adoption/clear/copy | Fresh parsed payload adoption transfers bindings without re-registering descendants; its any parent has no children table. Clear old value first; empty source returns; otherwise parse/adopt or recursive copy, then hints. Dispose outgoing tables without destroying externally referenced state (§3). Native `src/tree_data_new.c:320–369`; Go `data/any.go:238–309,346–500`, `data/siblings_access.go:568`. Partial-forest route needs explicit native-equivalent U/X history before adoption, never invented whole-move replay. | 6b2a / T-any |
| Prefix producers | Capture/copy/rebind immutable prefix state; opaque merge atomically replaces value/format/P. No registration merely for P. Native `src/tree_data.c:2270–2278,2672–2690`; Go `data/parse_xml.go:147–215,312–317,576–583`, `data/any.go:502–511`, `data/edit.go:1873–1888`, `data/dup.go:376–454`. | 6b2p / T-prefix |
| Ordinary/whole move | Per native branch, U/X source then L/I destination; descendant tables persist. Empty destination, by-schema and sorted-merge orders differ. Native `src/tree_data.c:891–1044`; Go `data/move.go:755–997,1016`. Main's upfront top-level detach (`:903–934`) is storage scheduling, not authority to reorder native events/metadata visibility; a top-level U has no children-table write. | 6b3m / T-move |
| Sorted merge 1 | Cold-build destination if absent, then source physical order, each U/X/L/I; retain passed metadata. Native `src/tree_data_sorted.c:1444–1469,1693–1728`; Go `data/move.go:1000–1178`, `data/insert.go:814,2675`. | 6b3s1 / T-merge1 |
| Sorted merge 2 | Native front → metadata → among/back → nonmember tail is distinct from the **D-0062 Go alternative in §4.1**: replay destination nodes into source RB topology, permute the destination without its U/I, then register source members against explicit filtered frontiers and process the nonmember tail. Do not combine native source-only link steps with a globally sorted Go result or claim completed native evidence for UB inputs. Native `src/tree_data_sorted.c:1488–1642`; Go `data/move.go:1060–1099`, `data/insert.go:2675–2785`. | 6b3s2 / T-merge2 |
| Sorted merge 3 | First clear/free source metadata root, retain RB nodes for traversal; source RB postorder U/X/L/I, then nonmember tail via variant 1. Native `src/tree_data_sorted.c:1661–1690`; Go `data/move.go:1031–1041,1100–1178`. | 6b3s3 / T-merge3 |
| Relocation rollback | Paired savepoint restores old storage **and table**, no compensating I/U. Successful forward restore is different. Go `data/move.go:373–702`; native positional restore `src/validation.c:2501–2512`. | 6b3m / T-undo |
| ValidateOp | Native match determines temporary attachment; U/X/L/I, validation events, then U/X and positional L/I at saved before/after, else normal parent insertion; detached root stays detached. Forward restoration creates new history, including on ordinary failure/cancel. Native `src/validation.c:2353–2520`; Go `data/validate_op.go:96–246,298–318`, `data/insert.go:2395`. | 6b3o / T-op, T-completion |
| Merge | Select opaque/schema/first plus consumed set (§2.2). Miss recursively copies then L/I; hit changes leaf/any or opaque V/P, then merges children. `flushMerge` must replay decisions/frontiers in source order; pending matches see earlier virtual events. Native `src/tree_data.c:2640–2775`; Go `data/edit.go:1762–2046`. Preserve D-0133 (`conformance/deviations.md:76`). | 6b4m / T-merge, T-set |
| ApplyDiff | **Nonrecursive create**, link, metadata, callback, then individual non-key child actions (§4.2). Delete looks up then U/X/F and returns without callback/child traversal. Replace uses value path; metadata/flags alone no registration. Native `src/diff.c:1799–1943`; Go `data/diff_apply.go:98–242`. | 6b4a / T-apply |
| Diff anchors | Schema/value or positional selected-run lookup; move existing node U/X then anchor L/I; new copy only L/I. Use actual anchor storage and §2.3 write-set check, with existing move-self/context/D-0121 refusals. Native `src/diff.c:1451–1543`; Go `data/diff_apply.go:350–431,455–500`. | 6b4a / T-anchor |
| Diff generation/merge | Track result/ancestor/key copies from creation. Match for metadata recursion in **both** input trees; diff-merge delete child-presence lookup also uses authority. Metadata-only writes have no registration. Native `src/diff.c:383–528,991–1018,1161–1181,2503–2597`; Go `data/diff.go:395–528,593–711,851–947`, `data/diff_gen.go:282,656–689`. | 6b4g, 6b4d / T-diff |
| ReverseDiff | Private recursive duplicate inherits capacities; reverse values/defaults/metadata. For each collected user-order group, **all U/X except last before any reinsert**, then reverse predecessors after evolving anchor with L/I. Source unchanged; failed result discarded. Native `src/diff.c:3273–3323,3473–3504`; Go `data/diff_reverse.go:33–53,163–280`. | 6b4r / T-reverse |
| Validation reads | Special duplicate operation §2.2; mandatory presence/getnext; unique nested-leaf lookup; instance-id/leafref/when/must through paid XPath. These decide errors, NEW clearing and later mutations. Native `src/validation.c:639,1155,1313`, `src/tree_data_common.c:164`; Go `data/validate_new.go:25–105,428–503`, `data/validate.go:541–578,636–668`, `data/defaults.go:206–233`, `data/unres.go:1175–1240`. | 6b5d, 6b5q / T-val-dup, T-val-read |
| Canonical-only validation | V and comparison-projection invalidation only; no H/U/I. Mark captured keys dirty for a later real hash event. Includes key/leaf-list/leaf/metadata updates. Native `src/validation.c:580–609`, `src/tree_data_common.c:538–562`; Go `data/unres.go:897–991`, `data/lyd_hash.go:211–242`. | 6b5v / T-value |
| Implicit creation | Native presence lookup → create/flags → L/I → queues/diff create; retain completed prefix on non-hash validation stops. Native `src/tree_data_new.c:1890–2056`; Go `data/defaults.go:252–397,420`, `data/validate.go:850–867`. | 6b5i / T-implicit |
| When/default deletion | Prepare table/link/queue/**diff** resources before emission. Complete diff → queue removal → U/X/deletion cleanup → links/dead marking before observing cancellation. Default autodel secures entire batch. Native `src/validation.c:333–527`; Go `data/unres.go:479–550`, `data/validate_new.go:322–370`. | 6b5w / T-delete |
| Dummy when | Temporary opaque L; evaluate; mandatory X/free even on error/cancel. No own I/U, but its neighbours affect other events. Native `src/validation.c:1082–1143`; Go `data/unres.go:1371–1399`. | 6b5w / T-completion |
| TrimXPath | Paid XPath selection first; preserve subtrees/ancestors/keys, native DFS deletion-root order. Replace legacy `SchemaLen+4*count` preflight with **actual sequential** U/X/cleanup/shrink work. Planning uses remaining `trimStepBudget-r.Steps`; hash/journal work uses existing hash allowance, no double debit. Preserve D-0101 result-set policy. Native `src/tree_data.c:3581–3697`; Go `data/trim.go:61–219`; deviation `conformance/deviations.md:63`. | 6b6t / T-trim |

### 4.1 Sorted merge 2 and D-0062

Keep the existing deviation (`conformance/deviations.md:38`). Native nodes2
inserts the destination leader into the source RB tree, moves its source
predecessors backwards before it, transfers source metadata after that front
phase, then inserts remaining destination members into the RB tree while moving
source successors among/after them (`src/tree_data_sorted.c:1488–1642`). It does
not relocate existing destination data nodes. `_among` tests the current schema
instead of its successor (`:1542`), and `_back` can leave continuation unset
(`:1583–1584`, read at `:1634`). An unsorted destination can also make `_among`'s
forward iterator unable to reach the newly inserted destination RB node. These
undefined paths cannot supply a completed exact-native trace.

An unsorted destination without an RB tree is publicly reachable: two separate
NoSort/NoMeta duplications of values 4 then 1 into the same parent retain [D4,D1]
without sorting. Moving a parentless source sibling forest [S2,S3] with an RB
tree into that parent selects nodes2. T-merge2 must construct this state through
public APIs and verify both RB-presence preconditions, not seed a private flag.

The existing **Go D-0062 alternative** additionally permutes destination nodes.
Its deterministic registration frontier is defined by the main implementation
(`data/move.go:1060–1099`; `data/insert.go:2675–2785`):

1. Save the destination run D in its original data order and its original first
   node d0. Keep source RB topology and insert all D members into it in that
   order. This RB replay emits no data-table I/U. Let F be the resulting in-order
   run; source instances precede destination instances on equal values.
2. Form A from source RB members only: those before d0 in F, visited backwards,
   followed by those after d0 in F, visited forwards. Destination nodes are
   excluded from A. Source nonmembers remain for the variant-1 tail.
3. Republish the affected destination range in F order, including **reordering
   existing destination nodes**. This is one Go-policy permutation, not a series
   of native destination U/X/L/I relocations: it emits no U/H/I for existing
   destination members and leaves their registrations unchanged. Preserve opaque
   slots using the existing physical-extent rule
   (`data/siblings_access.go:654–682,717–735`). Install the retained topology and
   transfer its metadata group to the final run's first member before the
   attachment replay, preserving the order of additional/empty metadata items
   (`data/insert.go:2730–2758`). Do not claim native “metadata after front” timing
   for this Go-policy publication.
4. Replay A one member at a time. Its registration frontier is the republished
   physical sequence with all later members of A hidden; existing destination
   members are already in their permuted positions. Publish the arriving
   member's attachment, then execute I against that frontier. An absent table
   reaching four schema children builds in this frontier's physical order; an
   existing table performs the arriving member's normal I sub-operations using
   its then-visible predecessor/successor. Keep opaque separators and native
   helper-stop/resize behavior. Do not pre-register future arrivals, rebuild an
   existing table, or synthesize events for the destination permutation.
5. Process source RB nonmembers in original source data order through variant 1
   using the retained root. The source of this public whole-forest route is
   parentless, so its U calls have no children-table effect. Main physically
   detaches that source batch before `merge` (`data/move.go:895–934`); this storage
   scheduling does not add destination registrations. Preserve the paired
   whole-move rollback boundary for refusal/resource failure (§5).

Thus [D4,D1] + [S2,S3] has F=[D1,S2,S3,D4], A=[S3,S2], and visible frontiers
[D1,D4] → [D1,S3,D4] → [D1,S2,S3,D4]. Existing destination registrations can
therefore retain a winner that is no longer physically first. B preserves this
specified alternative and applies §1's occurrence-table primitives to its
frontiers; it must not mix native source-only links with the Go sorted result.
Document this frontier/metadata refinement with the existing D-0062 entry when
implementing it, rather than claiming those permutation steps are native.

T-merge2 keeps native-defined controls and D-0062 policy controls separately
labeled. The native observer records native front/metadata/among/back events;
the Go alternative records the explicit permutation and attachment frontiers
above. Exact native event equality is asserted only where the policies agree;
a completed native-defined public result still needs oracle agreement or an
explicitly applicable documented deviation. This clarification does not waive
new mismatches outside D-0062. Undefined controls record only a safe native
prefix plus the failure/watchdog classification, never an accidental completed
result. Keep `TestMergeSorted` (`data/move_test.go:164`) and the ordered
empty-metadata control (`data/retained_rb_stage3_test.go:476`). Destructive-merge
pooled RB reuse remains excluded (Appendix A).

### 4.2 ApplyDiff create boundary

Both create branches call `lyd_dup_single(..., LYD_DUP_NO_META)`, without recursive
flag (`src/diff.c:1821,1867`); main already does this in
`diffDupCreate` (`data/diff_apply.go:240–242`). Copy leading list keys, **not**
non-key descendants or source inner-table capacity. Separately, anydata payload
copy still recurses (`src/tree_data.c:2311–2325`). Then link, apply metadata, invoke
parent callback, and recursively apply each non-key child action in order
(`src/diff.c:1925–1941`; `data/diff_apply.go:211–233`).

T-apply creates from a source with retained oversized inner table and shows the
new parent initially absent-table (or fresh key-induced table), with no non-key
children. Parent callback returning an ordinary error leaves this created prefix
and applied metadata, **no non-key descendants**; callback hash/guard failure
uses rollback. A later child error retains only earlier child actions. Include
user-ordered create, list-key and nonrecursive-any-payload controls. Existing
any-replace unsupported boundary remains before writes
(`data/diff_apply.go:196–198`; `conformance/deviations.md:156`).

## 5. Transactions, publication and failure boundaries

### 5.1 Eager paired frames

Use one chronological native undo log across enrolled domains, with **eager
paired frames** `(main marks/serial, native mark)` at outer entry and every
savepoint. Frames/enrollment/undo use bounded pages, not a first-write callback
appended to `j.undo` or another flat `j.levels` append. Dispatch **all** enabled
savepoint/end/abandon paths through the wrapper; no unpaired child levels.
Capture/touch before writing, secure undo capacity before forward changes;
rollback allocates/charges/polls nothing and calls no user code. Preserve main's
whole → undo → placed → index restoration phases, then restore native captured
state before exposing results (`data/hash_transaction.go:179–188,372–400,1112–1169`).
Never derive undo from temporarily restored topology.

Example: parent mark0; child1 writes to3/commits; child2 mark3 writes to6/commits;
parent's first write reaches7. Parent rollback restores7→0 **once**, with no later
child callback naming truncated mark3/6. Child commit retains entries for outer
rollback. Late domain enrollment records binding/guard adoption in the same log;
all open frames already mark it. Never combine different active journals
(`data/hash_transaction.go:692–717,1206–1244`). Clear retired roots and pointer
cells when their last restoring horizon ends.

Audit newly reached main journal growth too: paged native state does not excuse
an input-sized `append`/map clone/capture in the bridge. Prepare bounded charged
copies outside publication or migrate that path's storage. T-undo tests populated
logs exactly at capacity, two committed children then parent write, nested
rollback, resize, cross-domain late enrollment, held second domain and panic.

### 5.2 Preparation versus prepaid suffix

Existing prepaid scopes cannot execute context methods/user code
(`data/suspend.go:34–74,224–279`; call sites
`data/insert.go:2112–2120,2938–2945`, `data/edit.go:1032–1049`). Therefore no
cancellable build/resize can be inserted casually at `attach`.

| Phase | Obligations |
|---|---|
| Prepare outside suffix | Paid/polled lookups, native virtual replay, data/hash/prefix comparison, current-reference guards, replacement roots or bounded overlay, all captures/undo/enrollment and cleanup resources. Include late parent-key events and cold sorted relocation. Record native semantic stop separately from Go error. |
| Before publication | Validate generations/guards and sticky errors, final ordinary context check, reserve secured apply writes. Bounds include table/projection/page/bridge work, not only legacy link/unlink cost. No published write before successful funding. |
| Prepaid suffix | Apply secured writes/touches, consume existing credit. No context poll, callback, allocation, resize walk, fresh savepoint, fallible capture/enrollment or nested new quota. Overdraft is a bug, not a partial-result license. |
| After suffix | Refund only unused apply credit. Never refund executed preparation. Poll before next ordinary unit/result where that API allows it. Completed validation deletion retains its prefix if cancellation is observed here. |

Callbacks remain outside prepaid scopes under every participant's suspension
guard. Autodelete's **entire batch**, including diff creation, must be funded
before emission. A callback that cancels and returns nil does not insert a new
poll between emitted deletion and unlink/queues/cleanup; finish the funded unit.
A hash/guard failure uses existing paired data/diff rollback. Preserve sticky
hash errors; do not store cancellation in `hashErr` to force a different policy.

### 5.3 Mandatory completion after cancellation

ValidateOp restores after validation, ParseOp cleans parsed nodes on failure,
and dummy-when defers unlink (`data/validate_op.go:194–246`,
`data/parse.go:298–361`, `data/unres.go:1376–1386`). Their context may already be
canceled. Before temporary attachment/parse insertion, secure a **completion
ticket** with origins/anchors or parsed-node ledger, guards and paired rollback
marks. A ticket is not a stale restore write-set: validation can change children
and table capacity.

Completion preparation recomputes the **current** native frontier and forward
U/X/L/I plan. Seal the original error; use a private completion polling mode
that defers context cancellation checks throughout nested preparation, final
check and publication. Do not call canceled context's Err/Done/tick, re-enter
Reader, or emit validation/diff callbacks. Any required type callback remains
outside prepay under guards; its own error/panic is not suppressed. Structural
charging, arithmetic, sticky hash errors, generation checks and guards remain
active. Pay from the **same finite governing hash allowance**, including any
recovery credit already charged once; no unlimited recovery, cleared sticky
error, quota reset, or borrowing XPath/MaxNodes allowance. Legacy restore bounds
(`data/validate_op.go:128–151`, `data/insert.go:2395–2408`) must include actual new
table writes and current-state planning, not assume them free.

After securing all writes/cleanup, publish a nonpolling suffix and finish every
ticket before returning the sealed cancellation. Completion hash/allocation/guard
failure instead triggers the governing coupled rollback/error precedence; no
operation subtree or dummy is stranded. Ordinary D-0121 parse-cleanup refusal
keeps the existing completed cleanup prefix. T-completion cancels after an
implicit change, a dirty parsed insertion, and dummy attachment; repeat each
with recovery quota/guard failure. Native controls use ordinary errors to test
cleanup order; Go tests separately prove finite, narrowly scoped cancellation
deferral.

### 5.4 Failure matrix

| Stop/entry | Required result and state | Baseline contract |
|---|---|---|
| Paid read error/cancel | No partial answer/fallback, no queried-state mutation. Discard private projections. Errorless Equal is §2's explicit unbudgeted mode. | `data/siblings.go:1983–1991`; `data/compare.go:24` |
| I/U semantic stop | Keep exact successful prefix; D-0118 only where declared. No rebuild or completion of missing entries. Validation find-next diagnostics follow §2.2 instead. | `src/tree_data_hash.c:135–160,195,220–244` |
| Journaled edit/NewPath/Merge/ApplyDiff hash budget, guard or panic | Restore governing operation, storage/native state/domains together. Retain original sticky-error precedence. | `data/hash_transaction.go:488–563`; `data/diff_apply.go:53–89` |
| Ordinary Merge/ApplyDiff error/refusal | Completed prefix remains. Failed relocation/whole-move restores its paired child level; outer prefix remains unless enclosing error requires rollback. Remote-link refusal uses this same boundary. | `data/move.go:373–413,803–832`; `data/edit.go:191–220`; `data/diff_apply.go:53–89` |
| Parse existing parent | Hash/guard/panic restores operation. Ordinary syntax/validation/cancel publishes dirty order then reverse parsed-set cleanup; keys stay refused, original rc handling retained. Completion exhaustion rolls back; ordinary D-0121 cleanup refusal keeps completed cleanup prefix. | `data/parse.go:227–363`; `data/insert.go:3299–3323` |
| Validation ordinary error/cancel, MaxNodes, XPath limit | `endKeeping`: completed prefix remains, including flags, canonical values, tables and matching partial diff. These limits are not stored-hash failure just because they wrap ErrBudget. | `data/hash_transaction.go:533–541`; `data/validate.go:952–986`; `data/diff.go:44–66,123–130` |
| Validation hash/journal, guard or panic | Roll back data and registration state; diff-returning calls drop diff when restored. | Same sources; `data/diff.go:123–130` |
| When/autodel after emission begins | Finish funded deletion unit/batch with queues, links and diff consistent; then observe cancel and retain it. | `data/unres.go:479–550`; `data/validate_new.go:322–370`; `data/validate_cancel_test.go:157` |
| ValidateOp success/ordinary error/cancel | Forward restore with new registration history, retaining validation prefix. Restoration refusal/hash/guard failure rolls back both forests. | `data/validate_op.go:180–246` |
| Dummy when error/cancel | Mandatory X/free; prior validation prefix remains. Completion hash/guard failure rolls back. | `data/unres.go:1371–1399` |
| TrimXPath read/remaining-step/preparation error | Input unchanged. Stored-hash/guard/panic uses batch paired rollback; fund final deletion suffix and insert no new cancellation checkpoint within/after it. | `data/trim.go:102–219`; `data/insert.go:3171–3182` |
| Trim ordinary D-0121 removal refusal | Preserve completed deletion prefix and cleanup; cleanup hash failure outranks refusal and restores batch. This qualifies the broad unchanged-on-error API comment. | `data/insert.go:3299–3323`; `conformance/deviations.md:55` |
| Dup ordinary failure | Only new copies cleaned per D-0065; hash/guard uses governing rollback. | `data/dup.go:522–612`; `conformance/deviations.md:47` |
| ReverseDiff failure | Nil result; private result/bindings discarded, source unchanged, even after unlink phase. | `data/diff_reverse.go:33–53,163–180` |
| Any-copy ordinary error after clear | Clear-before-copy prefix remains unless outer API mandates rollback; old roots stay restorable to its horizon. | `src/tree_data_new.c:331–356`; `data/any.go:403–500` |
| Sorted merge 2 D-0062 | Supported alternative §4.1, not a new error/recovery contract. Do not convert a native UB trace into a claimed completed oracle result. | `conformance/deviations.md:38`; `data/move.go:1005–1099` |

## 6. Work, prefixes and error-capable consumers

Use existing cumulative mutation hash/journal allowance or consumer read/evaluator
allowance through nested calls; never debit a queried Tree's mutable counter for
a read. Exceptions are Equal/public-print structural unbudgeted modes and
completion's finite noncanceling mode, explicitly selected at entry. No new default quota, nested reset or error
converted to optimization decline.

| Actual work | Before-execution debit/checkpoint |
|---|---|
| Bucket/slot/free-list/overlay | Every inspected/updated item, including skipped hashes; projection probes charge their own actual visits, not imaginary skipped native work. |
| Build/resize | Every counted/traversed child, opaque separator, old bucket including empty ones, chain item, checked duplicate rejection, allocation/initialization/copy and undo entry. Stop at native semantic abort. |
| Storage/frames/lineage | Each visited parent/frame/handle/memo entry and bounded page growth/copy. No sparse-map range charged only for live yields, hidden map clone or flat-slice growth. |
| Names/values/hash/XML | Charge bytes actually scanned/compared/hashed, including rescans; poll at most every 256 executed bytes and each structural step. No charging an unread suffix after a mismatch. |
| Prefix resolution | Nil test first; then only selected resolver work. Paid cancelable entries poll at entry and final result, even for immediate hit/miss. Equal/public-print structural modes never poll; finite completion defers cancellation polls. |

Baseline integration: `data/hash_budget.go:19,51,142`, `data/lookup_work.go:16–38`,
`data/suspend.go:34–74`; current comparator and XML resolver entry points are
`data/siblings.go:2264,2320`, `data/opaque_compare.go:39–48`. Use physical handles
through views; repeated `handle(node)` scans can be quadratic
(`data/siblings_access.go:324–340,933–954,1058–1075`). No pre-materialization of
both child slices or hidden `newCompareIndex` build before debiting.

### 6.1 Prefix state

Mandatory prefix information is a **bounded enumerable immutable snapshot**,
preserving nil/non-nil-empty, namespace order and source context. Capture/copy
it at producers, not by ranging arbitrary maps on every read. An optional trie
is a paid projection; ordinary same-context operations do not build it. Without
trie, compare enumerable entries with bounded byte scans. Never call an unknown
Resolve-only interface or ordinary long-string map lookup inside a paid probe;
prehashing in chunks then performing the map lookup still hashes uninterruptibly.

XML/XML comparison scans both tokens, resolves each side **only if that side has
prefix data**, then tests unresolved modules. A nil first side does not skip
non-nil second-side resolution (`src/xml.c:1373–1436`). Nil resolution costs only
a constant branch beyond already-performed scanning. Mixed XML/JSON follows its
own first-colon rule (`src/tree_data.c:1586–1648`). Implemented-module then latest
namespace resolution must use bounded module/name/revision traversal
(`src/tree_schema.c:172–226`).

| Stored representation at baseline | Required adapter/producer audit |
|---|---|
| `XMLNamespaces`, parser snapshots (`internal/types/types.go:82–112`; `data/parse_xml.go:147–215`) | Capture entries/order while produced, preserve nil; bounded copies and context namespace resolution. |
| `ModuleNames` (`internal/types/types.go:69–77`) | Bounded immutable module-name enumeration/index, retaining actual payload context. |
| `SchemaText`, `schema.NSCtx` (`internal/types/types.go:123–131`; `internal/schema/schema.go:503–506`) | Internal enumerable prefix/module adapter; no hidden Resolve fallback. |
| `prefixSnap`, copied snapshots (`internal/types/instanceid_keys.go:32–35`; `data/any.go:502–511`) | Typed internal enumeration/copy/rebind; snapshot replacement restores old binding on rollback. |
| `nsPrefixes` (`data/xpathnode.go:764–790`) | Unwrap supported backing representation at target preparation. All current producer representations are required for B; newly reachable ones require an adapter before publication. |

T-prefix includes cold parse/dup, replacement/rebinding, nil/empty, >8 bindings,
megabyte prefix hit/miss, colonless token, unresolved short prefixes with long
unread suffixes, mixed formats, and memo publication. Instrument **executed work
between polls**; tests that just count available polls cannot detect hidden work.

### 6.2 Routing contract

Mutation-required helpers are accepted before their producers; general public
routing waits for 6d1/6d2. Replace errorless semantic internals where needed,
propagating kernel errors explicitly through mandatory/unique/default helpers,
`getnextData`, comparison, diff matching and printer callers (Appendix A).
Physical JSON annotation and dirty-run publication helpers remain separate (§4).

| Route | Contract / gate |
|---|---|
| schemaLookup/schemaIndex, findFirst* | Node + actual-storage cursor when needed, §2 null start/guards/linear rules. Retire integer-only semantic index; preserve `documentFirst` as physical scan (`data/siblings.go:1652–1684,1978–2108`). |
| Nested comparison / public Equal | Same registration state and operand order in all frames. Paid mode propagates errors; Equal retains §2 unbudgeted mode (`data/compare.go:24`). |
| `opaqNext` | Explicit cursor/start, charged/error-capable form for paid callers (`data/edit.go:2049`). Native fallback to opaque only for legitimate native miss, never Go error. |
| `xn.LookupPrepared` | Carry evaluator allowance/context through typed and schema-only cases; legacy errorless `LookupChild` is not a route for fallible kernel work (`data/xpathnode.go:70–151`). |
| **`xn.FirstChildPrepared`** | Add private evaluator structural interface `(schema, charge, context) -> (node, handled, error)`. Change `firstInstance` and its `hashPredicates` caller to return/propagate error and preserve speculative failures. B handles miss `(nil,true,nil)` and error `(nil,true,error)`; neither invokes legacy FirstChild/Children fallback. Existing errorless interface is insufficient (`internal/xpath/xpath.go:138–142`; `internal/xpath/eval.go:125–140,766,818–820,854–893`; `data/xpathnode.go:179–210`). **6b0x before validation/trim**. |
| Validation duplicate | Separate operation §2.2, not public sibling-first, generic duplicate-set, or `bucketNodes` substitution. **6b5d before any validation acceptance**. |
| JSON empty-list print | `nextEmpty` presently scans a materialized slice for pointer equality (`data/print_json.go:462–473`); native calls schema lookup (`src/printer_json.c:1040–1059`). Add an error-capable adapter with real supplied sibling, retaining shouldPrint. XPath casts supply `step`/`byteStep`; public printing preserves unbudgeted/noncancelable structural work when these are nil, with its separate existing default-check XPath budget unchanged (`data/print.go:48–68,93–158,415–425,440–449`). No new default quota. **6cJ before PR 7**; this existing codec caller is not deferred to later new codec work. |

PR 7 enables these audited existing private routes through their public wrappers;
no new exported root API, lazy legacy-tree promotion, or standalone duplicate-set
API. Required producer-cache consumption already ships at 6b4m. Later parts 9–12
may add ordering/codec/validation features; they cannot supply missing history
or selection for existing operations after routing.

## 7. Acceptance units and cost gates

The rows below are acceptance checkpoints, not individual PRs and not minimum
audit rounds. Group them into the twelve adapter-focused PRs listed below,
preserving every dependency and trace. Review each group once in full, then
review finding-driven changes and their affected dependencies. Split a group when
its implementation cannot be reviewed as one coherent change. The early B cost
gate remains before broad producer migration; the final gate remains before
public routing. Stacked reviews name their immediate reviewed base so each review
does not re-audit the accumulated stack. Later adapter PRs reuse the established
prepare/publish/completion machinery; PR G12 stays mechanical, and a newly found
producer behavior goes back to its adapter group.

| PR | Scope | Checkpoints |
|---|---|---|
| G1 | Native driver and occurrence-table kernel | 6a0, 6a1, 6a2 |
| G2 | Read selection, actual cursors, prefix representation, incremental comparison | 6b0r, 6b0p, 6b0c |
| G3 | Paired journal, prepare/completion infrastructure, early real B cost prototype | 6b0j, 6b0f, 6p |
| G4 | Construction, removal, sorted placement, explicit value changes | 6b1i, 6b1u, 6b1s, 6b1v |
| G5 | Immediate/deferred parsing, metadata publication, failure cleanup | 6b1pi, 6b1pd, 6b1pm, 6b1pc |
| G6 | Duplication, any payload adoption/disposal, prefix producer bindings | 6b2d, 6b2a, 6b2p |
| G7 | Moves, relocation, all three sorted-merge branches | 6b3m, 6b3s1, 6b3s2, 6b3s3 |
| G8 | Merge, consumed sets, ApplyDiff and anchors | 6b4m, 6b4a |
| G9 | Diff generation/merge/reversal and result ownership | 6b4g, 6b4d, 6b4r |
| G10 | Paid XPath, validation reads/canonical updates, JSON selection | 6b0x, 6b5d, 6b5q, 6b5v, 6cJ |
| G11 | Implicit creation/deletion, ValidateOp completion, trim | 6b5i, 6b5w, 6b3o, 6b6t |
| G12 | Public-wrapper audit, mechanical cutover, final cost and routing gates | 6c, 6d1, 6d2, PR 7 |

G3, G4 and G11 split if they contain two independently novel mechanisms or
exceed a reviewable diff. Acceptance coverage is never cut to reach twelve.

| Gate | Before |
|---|---|
| 6a0's three native experiments (below) replayed | any dependent semantic adapter merges |
| 6p real B costs pass | broad producer migration (G4 onward) |
| Public-wrapper evidence, 6d2 and full CI | public routing (PR 7) |

**Can start immediately, independent of unanswered maintainer questions:** 6a0,
6a1, bounded-prefix 6b0p, paired-frame 6b0j, and freezing the 6p baseline/cost
harness. They add no production routing. Subsequent local-only kernel work has
only the stated implementation dependencies. Remote-write acceptance waits for
§9's safety decision/evidence; production migration waits for early B cost proof.
No need to settle optional accelerators or new semantic deviations to begin.

Intermediate adapters are accepted on controlled, continuously tracked test
forests; that acceptance does not switch ordinary public construction or enable
mixed tracking. Keep the actual B replacement prototype/migration on an
experimental branch until its required producers are complete. It must compile
the proposed real layouts, owners and journal integration: a mirror or standalone
table benchmark is insufficient. The final cutover switches construction and
authority together, removes obsolete semantic indexes, and never promotes an
already-mutated legacy object. Small units may be reviewed independently without
merging an incomplete production backend.

| Unit | Deliverable / dependencies | Acceptance traces |
|---|---|---|
| 6a0 | Isolated native observer/driver; no production changes | T-driver |
| 6a1 | A occurrences/pages, checked/unchecked/remove, arithmetic | T-primitive |
| 6a2 | Interleaved build/resize; 6a0/1 | T-build, T-resize |
| 6b0r | Selection/start/actual cursor; 6a2 | T-null, T-read, T-live |
| 6b0p | Bounded enumerable prefixes, optional projection interface | T-prefix |
| 6b0c | Incremental nested comparison; 6b0r/p | T-compare, Equal controls |
| 6b0j | Eager paired frames, bounded bridge, domain enrollment | T-undo |
| 6b0f | Private prepare/prepay/completion; 6b0j/c | T-recover, T-completion infrastructure |
| **6p** | **Early real B replacement cost prototype**; 6a2, 6b0j/f/p. Basic construct/unlink/copy adapters only; no mirror/general routing. Before broad producer work. | Cost matrix below, native controls for prototype |
| 6b0x | Paid typed/first XPath adapters; 6p, 6b0r/c/p | T-xfirst |
| 6b1i | Builders/anchors/repeated key events; 6p, 6b0r/c/f | T-new, T-build, T-key |
| 6b1u | Single/bulk removal, D-0117; 6b1i | T-remove, T-D0117 |
| 6b1s | Cold/retained sorted insertion; 6b1i/u; remote policy gate | T-sort, T-remote |
| 6b1pi | Immediate/existing-parent parse; 6b1s | T-parse |
| 6b1pd | Deferred frontier and publication; 6b1pi | T-deferred |
| 6b1pm | Physical JSON metadata; 6b1pd | T-physical |
| 6b1pc | Failed parse completion; 6b1pd/pm, 6b0f | T-completion parse branch |
| 6b1v | Explicit key/leaf-list/value changes; 6b1s/u | T-value |
| 6b2d | Dup capacity/keys/parents/any recursion; 6b1i/u, 6b0p | T-dup, T-dup-any |
| 6b2a | Any adopt/clear/copy lifetime; 6b2d | T-any |
| 6b2p | Producer snapshot copy/rebind/replacement; 6b2d/a | T-prefix |
| 6b3m | Moves/relocation/domain lifetime; 6b1s/v, 6b2a/p | T-move, T-live, T-undo |
| 6b3s1 | Sorted merge 1; 6b3m | T-merge1 |
| **6b3s2** | Sorted merge 2 **with D-0062**; 6b3s1 | T-merge2 native-defined + deviation controls |
| 6b3s3 | Sorted merge 3; 6b3s1 | T-merge3 |
| 6b3o | Temporary op move/current-state restore; 6b3m, 6b0f/x | T-op/completion unit driver; repeat with real validation later |
| 6b4m | Merge/consumed-set/pending frontier; 6b3s1/2/3, 6b2p | T-merge, T-set |
| 6b4a | ApplyDiff actions/anchors; 6b4m | T-apply, T-anchor |
| 6b4g | Diff generation, user-order arrays, metadata matching/result owners; 6b4m | T-diff generation, T-macro |
| 6b4d | Diff merge and validation diff producers; 6b4g/a | T-diff merge/delete/ancestor |
| 6b4r | ReverseDiff two-phase reversal; 6b2d, 6b1v, 6b4g | T-reverse |
| **6b5d** | Validation-specific duplicate operation; 6b0c, 6b1u/v | T-val-dup |
| 6b5q | Mandatory/minmax/unique/getnext/path/constraint reads; 6b5d, 6b0x | T-val-read, T-macro |
| 6b5v | Canonical validation updates; 6b5q, 6b1v | T-value canonical |
| 6b5i | Implicit creation/data+diff prefixes; 6b5v, 6b4d, 6b3o | T-implicit, real T-op completion |
| 6b5w | When/autodel selectors and completion/dummy; 6b5i, 6b1u, 6b0f | T-delete, T-macro, T-completion |
| 6b6t | Trim selection/preflight/failure boundary; 6b0x/f, 6b1u | T-trim |
| 6cJ | Existing JSON empty-list selection; 6b0r/c, prefix producers | T-print |
| 6c | Remaining read/poll/concurrency audit; all above | T-read/compare/prefix/churn through each wrapper |
| 6d1 | Mechanical B authority/owner/frame cutover; all above | Every producer through public API; no legacy semantic read |
| **6d2** | **Final comprehensive B cost gate**; 6d1 | Expanded cost matrix, lifetime/recovery |
| PR 7 | §6.2 public consumer routing only; 6d2 green | Public reads after every producer history, no fallback on error |

6p must measure **allocated bytes, allocations, executed visits, and cumulative
hash/journal charges separately**. For every scenario/metric:
`B <= ecb6fd1 baseline + that metric's existing documented tolerance (zero if
none) + the approved ownership allowance`. **Ownership allowance (maintainer
decision, §9.1):** at most 32 additional allocated bytes and one additional
allocation for each genuinely new independent public forest/result that survives
the creating public call, solely for its durable registration-domain descriptor.
It does not apply per node, subtree, schema context, internal payload, temporary
Tree, lookup target, merge miss or diff action. Newly created nodes destined for
an existing forest bind its existing domain before their first native event,
including while detached or pending; newly created result nodes share the
result's one domain. Private targets/probes use an existing binding or a proven
allocation-free scoped binding, with operation-private mutable work and no
escaping scoped references. "Zero cost" there means no additional domain heap
allocation or domain-byte allowance, not free target construction or uncharged
executed work. Stack-local syntax is not evidence: verify B/op and allocs/op on
the pinned container. Existing public nodes moved or adopted keep their original
bindings. Discarded result attempts receive no ownership allowance, including
allocations made before failure or rollback. Empty/no-change paths that return no
independent result receive none. A retained empty public Tree, or an independent
partial result returned with an ordinary error, remains eligible for the one
descriptor allowance because it survives the call. Measure these cases separately. No other allowance: none for pages, frames,
projections, prefixes, visits or charges. No slack transfer across
scenarios/metrics, wider tolerances, extra journal units or speculative savings.
If the descriptor does not fit, or another metric fails, stop at 6p and return
to the maintainer with evidence. Reductions pass: replace obsolete equality
checks with ceilings, not fake old debits (`data/sibling_runs_work_test.go:68–90`).
Separate exact-accounting tests assert **new executed** charges, prepaid refunds
and the exact refusal boundary. Layout arithmetic is not allocation evidence.

| Cost scenarios, all individually gated | Included costs |
|---|---|
| Empty retained Tree; present-empty table; 1/2/3 children; top-level-only; detached singleton/parent | Domain discovery/allocation, small pages, guards and empty frames. Moved nodes must not retain an empty source Tree allocation through a coallocated domain. |
| 64/256 distinct and duplicate-heavy descending values | Construction, threshold build, cold/retained sort, full-hash collisions and grow/shrink. |
| Whole/narrow populated journals, open/commit/rollback; capacity-boundary logs | All captures/frames/enrollment/page copies and retired roots, not only allocation counts. |
| Value/key change; single/bulk removal; duplication; cold XML parse/dup | Mandatory snapshot bytes/copies and nil/empty cases; optional trie charged separately to requesting operation and included in its total. |
| Per-call Find-by-value on a built forest: leaf-list hit/miss; keyed list 1 and ≥4 keys hit/miss; null-start and typed-target failure | Whole call including target preparation; no forest allowance. Prepared XPath reuse and cross-context translation join the final consumer matrix. |
| Public Merge, 1/64/256 distinct misses into an existing destination, real B copy/link prototype on a narrow same-context user-ordered all-miss route | Everything inside the call; domain allocations attributable to the additions stay **zero as N grows**. Proves cost for this route only, not the Merge adapter (nested/cross-context/sorted/error at 6b4m and 6d2). |
| Empty/discarded construction paths the prototype supports | Allocations counted. Discarded attempts and empty/no-change paths with no independent result get no allowance; a retained empty Tree or an independent partial result returned with an ordinary error is eligible for the one descriptor allowance. Measured separately. |
| Final 6d2 additions: diff results with 1/64/256 copied nodes (at most one allowance per surviving result), empty/no-change and discarded results (no allowance), partial results returned with an ordinary error such as ValidateDiff's (one allowance), ApplyDiff and implicit-creation per-node scaling | Every public constructor/producer, cross-domain references/cleanup, prefix replacement, partial/inherited tables, operation restoration and failed completion. |

6p uses actual replacement storage/ownership/frame costs and frozen baseline,
not a scratch `sizeof` test. If a cheap case fails, revise/optimize before broad
migration; do not conceal overhead with populated-table savings. Final 6d2 repeats
and broadens the evidence; early success is not a waiver.

## 8. Trace evidence and review closure

### 8.1 Native observer

Build a test-only driver/helper against an **isolated pinned static** libyang
copy (`BUILD_SHARED_LIBS=OFF`; native `CMakeLists.txt:258,362–382`), matching the
canonical Release/amd64 flags. A standalone shared `-lyang` link cannot directly
name hidden `lyd_hash_table_val_equal`; generic tests obtain the real
`ht->val_equal`/cb_data from a children table created through native APIs and keep
that context/nodes alive (`src/tree_data_hash.c:192`;
`src/hash_table_internal.h:79–90`). Never copy its callback logic or relax
production visibility. Narrow source instrumentation is needed for static/local
boundaries and recursive dup's **post-table-init, pre-child** point
(`src/tree_data.c:2302–2309`); public dup alone cannot observe it.

Observe before/after each I/U primitive, resize/reinsertion, L/X, metadata transfer,
semantic stop, prefix replacement, table retirement and node free. Observers may
register IDs/dump state, never mutate algorithms, mask errors or invoke semantic
lookups during a half-written primitive. Compare final public results against an
**unmodified** pinned oracle to detect instrumentation interference. Record source
pin, patch digest, ABI/build flags; the auxiliary build does not replace goldens.

Externally created nodes get script IDs before insertion; internal parse/dup/
implicit/payload nodes get `(operation, creation ordinal)` before registration,
with source ID on dup. Maintain trace-only occurrence identity across resize.
Dump ordered `(bucket, chain ordinal, allocation generation, recorded hash)`;
also present/size/used/mode/free list, queried table, winner/current storage,
frontier/first/tail/prev/next/parent, selected RB metadata, current hash/context,
helper and enclosing rc, diagnostics and flags. Before/after lookup dumps must
be identical. Values such as `list=` are not identifying evidence.

Before free, tombstone allocation generation and retire the table before its
storage is freed (`src/tree_data_free.c:234–261`). Residual dumps read recorded
identity/hash from live table+observer registry only; current metadata becomes
**unavailable**, never dereferenced/cached-as-current. Stop any semantic probe
whose traversal could touch tombstoned data. Reused raw address gets a new
generation and never revives old occurrences. T-driver covers address reuse,
retired tables and internally allocated failed-build nodes. Corrupt-link and
find-next cycles use bounded observer/driver watchdogs, labeled boundaries rather
than match/miss or completed native results.

**6a0 prerequisite experiments.** Driver acceptance includes three experiments,
each emitting a replayable public-call script and labeled event evidence before
any dependent semantic adapter merges:
1. **Public remote-last reachability** (§2.3's sequence) plus a remote nonlast
   control. Verify no earlier D-0121 boundary is crossed; stop observation at the
   inconsistent link primitive; use a separate bounded first-sibling control that expects termination and Q.child. This
   decides the remote-write scope and deviation.
2. **Collision-aborted history through later removal, checked shrink and
   validation.** Run the binary collision build and the events after it.
   Separately build repeated same-pointer registrations, trigger a checked shrink,
   and validate before and after. Observe helper status against enclosing
   validation status, diagnostics and NEW flags. Keep the partial-build and shrink
   scripts separate until each prefix is established.
3. **Sorted-merge-2's defined/UB boundary and metadata timing:** one defined
   control, one successor-schema case, and the all-source-before-destination
   boundary. Record native front → metadata → among/back → tail where defined; separately verify §4.1's Go-policy permutation, metadata publication and attachment frontiers. Include the publicly constructed unsorted destination and both table-presence controls; classify the
   uninitialized-continuation path as UB without using its accidental result as
   a golden.
A contradictory result reopens the affected contract. Source citations alone are
not passing evidence. Low-level table construction may be used for primitive
tests but is labeled separately from public reachability. Terminating public
controls are compared against the uninstrumented pinned oracle. The B cost
hypothesis and finite completion funding are proved by the Go 6p/transaction
prototype, not by this C observer.

### 8.2 Discriminating acceptance traces

Every supported changed behavior needs an independent oracle fixture/request,
plus a Go regression that fails if the fix is removed. Ordinary native results
compare full identities, diagnostics/paths/app-tags, links and intermediate table
state. Declared UB/deviation cases label the safe native prefix and independently
assert the Go policy; no fabricated completed golden. Fixtures use their own
manifest files; never hand-edit goldens (`conformance/AGENTS.md:5–13`).

| Trace | Minimum discriminating control |
|---|---|
| T-primitive / T-resize | Same pointer/hash repeated; distinct equal nodes; one pointer under old/new hashes. Checked growth vs unchecked growth vs checked shrink, 50/75/25 boundaries, repeated removes/free-slot reuse. Integrate `Qg==` leader, binary collision, 16 temporary inserts/removes, then collision node→`RA==`; shrink changes later winner. Lowered artificial arithmetic limits test refusal separately from native semantics. |
| T-build | Module m, c with p/q and user binary ll: `KpfzBg==` before `QQ==` crossing threshold; verify collision `0x7be55cae`, vary placement. Failed child's instance survives, later linked suffix misses, subsequent insert does not reconstruct it. |
| T-null | Physically empty present table retaining live moved occurrence: null start misses without reading table; add opaque child: non-null schema-table lookup can see it; next schema insert preserves retained capacity/history. Instance/value entry controls too. |
| T-read / T-compare | Three equivalent contexts interleaving both registration origins; schema-origin instance winner and instance-origin schema winner. Foreign keyed↔keyless empty-key schemas; full choice/case/input/output lineage; table target-first versus linear candidate-first. Nested equal-key i nodes with `v=first/second`, re-register physical first, assert second registered winner and outer mismatch; repeat through Equal with Defaults/Opaque. |
| T-live / T-remote | Source residual winner moved into Q, changed so it fails full compare but Q successor matches. Schema lookup, duplicate continuation and positional caller use Q. Detached singleton and detached parent controls. Remote nonlast/before write versus remote-last corruption (§2.3), both forests' links and all held domains. Dispose source/payload, keep other references, then drop last occurrence/undo; no idle-domain retention. |
| T-sort / T-remove | Cold descending/equal runs, retained roots/nonmembers, opaque/different-schema separators, cyclic-tail singleton. Selected metadata differs from physical leader; per-step neighbour and shrink effects; single/bulk parity, D-0121 prefix and hash rollback. |
| T-new / T-key / T-value | Partial NewPath then late error; repeated leading keys and re-registration without U; prefix stopped by opaque/nonkey; leaf-list/key relocation and hash-only change; ordinary leaf/flags/equality/canonical-only updates have no invented events. |
| T-parse / T-deferred / T-physical | JSON/XML and supported envelope/existing-parent routes; new minimum/middle/maximum with pre-existing table and at threshold. Probe after each logical insertion; two flushes emit zero I/U. Annotation-before-target, placeholder separator and physical helper with partial/non-first/remote winner. |
| T-dup / T-dup-any | Source oversized/shrunk/present-empty/partial table; inspect destination before first child, then future lookup. Recursive, nonrecursive keys, parents/existing-key reuse, cross-context, NoLyds, late D-0065 failure. Nonrecursive outer any still recurses payload and captures prefixes. |
| T-any / T-prefix | Fresh payload adoption, clear-before-copy failure, context retention/transfer and descendant references. All §6.1 cold/warm/early-stop/cancel controls. |
| T-move / T-merge1/2/3 | Empty/nonempty destination; per-node leader decisions after earlier events; each sorted variant's metadata timing, nonmembers and shrink. T-merge2 separates terminating native controls from D-0062 alternatives, including all-source-before-destination and schema successor after run. |
| T-op / T-completion | Saved before/after/no-sibling and detached origins; success creates new restore events. Cancel after real implicit child, parsed dirty insertion, or dummy attach; complete restoration/cleanup under original finite allowance. Exhaustion/held-domain/panic restores both endpoints. |
| T-set / T-merge | Keyed equal keys/different descendants collected with options0; keyless/state full recursion. Consume, move/change child, consume again without resetting set/used; moved winner collection in current siblings. Pending-equal merge sees prior frontier; find-next cycle returns resource error, never a partial set. |
| T-apply / T-anchor | Oversized source table, parent callback failure before non-key actions; keys/any exceptions, user-order create, later child failure. Selected remote/partial-table anchors and savepoint recovery; no callback on delete. |
| T-diff / T-reverse | Diff ancestor/key generation, two-sided metadata lookup and diff-merge-delete presence decisions. Result trees reach threshold and are queried later. Reverse inherited capacity; dump after **all unlinks**, before first reinsert, with collision/shrink, and fail there: nil result/source unchanged. |
| **T-val-dup** | Repeated **same pointer** before/after checked-shrink dedup; moved live candidate; asymmetric target-first table versus candidate-first no-table; partial-table initial re-find EINT diagnostic with success return/NEW clearing; real duplicate fail/warn and MultiError continuation versus stop. Assert later autodelete executed/skipped appropriately. |
| T-val-read / T-implicit | Mandatory/choice/getnext and nested unique select registration winner, including null-start residual and foreign continuation. Partial table changes implicit creation; canonical/instance-id/leafref/when/must decisions see authority. Check error propagation through formerly errorless helpers. |
| T-macro | Min/max selects a nonphysical-first/remote run; assert stopping node, count, app-tag/path and Operational warning. Default deletion tests explicit search then SAFE next capture, skipped foreign-first and opaque separator. Diff user-order collection distinguishes first->schema from cache schema and preserves D-0121 refusals. Paid errors propagate through instances/userordGet, never a truncated successful array. |
| T-delete / T-recover | Every §5 matrix row, each executed debit/poll, partial/empty/large table, sticky-error precedence. Extend cancellation-after-diff consistency tests (`data/validate_cancel_test.go:157`; `data/when_delete_test.go:82`), assert data/diff/queues/flags agree. |
| T-trim | Real XPath firstInstance refusal; differing registered/physical winner; actual shrink exceeding old preflight; no-op/EEXIST; budget/guard leaves input unchanged, D-0121 later refusal keeps cleanup prefix, D-0101 unchanged. |
| T-print | EmptyLeafList JSON with table winner differing from physical-first, partial table, default/shouldPrint and foreign schema, empty start. Paid cast error has no fallback/partial output; public print preserves its existing unbudgeted structural mode (§6.2). |
| T-undo / T-D0117 | §3/§5 savepoint, cleanup shrink, pointer clearing and lifetime controls. Compare alive-node primitive sequence natively; label cleanup itself Go policy. |
| T-churn | Grow/delete large populations, retain few entries, repeat probes; actual visits track enumerated storage, not only live map yields. Opaque view N-step miss uses O(N) handle advances, no repeated O(N) handle resolution. |
| T-xfirst | Real hashPredicates→firstInstance route; exhaust/cancel at bucket, lineage and final result. Original error, zero fallback Children/FirstChild calls, no partial successful result. Genuine misses/top-level controls remain valid. |

All units run relevant dev-container tests; implementation acceptance additionally
requires `./dev make ci`, and oracle-touching units use canonical
`DEV_PLATFORM=linux/amd64 ./dev make test-oracle oracle-check` (root `AGENTS.md:12–24`).
Add provenance/port-map entries with each implementation. This document authorizes
neither code changes nor a claim that these future gates have passed.

**T-key addition.** **Non-key insertion control:** create a fully keyed list L beneath P with P's children table already present. Record L's stored hash and all of P's occurrence/bucket/free-list/size/used/mode state. Insert an ordinary non-key child into L through NewTerm and a separate NewPath control. Only the child's L/I occurs: L's stored hash and P's table remain identical, and duplicate validation does not acquire a second occurrence of L. Repeat while L's own children cross the first-build threshold, and with an opaque child; changes to L's child table must not be mistaken for re-registration of L in P. Pair with a real key insertion satisfying §1.2's full guard, which does emit H(L)/I(L), and an incomplete-key-prefix control, which does not. Recursive Dup's final H(dup) is tested separately from these insertion-triggered events.

**T-merge2 additions.**

**Unsorted destination:** publicly construct D=[D4,D1] with NoSort/NoMeta dup and no RB tree, and parentless source S=[S2,S3] with an RB tree. Require final physical order [D1,S2,S3,D4], A=[S3,S2], zero U/H/I for D's permutation, and the three frontiers in §4.1. Dump destination registrations before/after permutation, each arrival's predecessor/successor, metadata owner/order, and RB shape. Native evidence stops before `_among` dereferences the exhausted forward iterator; record its earlier source-only link prefix separately.

**Threshold build:** start with exactly D4,D1 under P and no children table. I(S3) sees three children and creates no table. I(S2) sees four and builds in [D1,S2,S3,D4] order, completing each child's instance/schema add before advancing. For noncolliding hashes, schema lookup selects D1. Never use the native UB-prefix order [S2,S3,D4,D1] for this Go-policy build.

**Existing table:** put two distinct non-v schema children before D4,D1, so P already has its children table and v's schema occurrence names D4. The Go permutation alone changes no occurrence or table size/used/mode/free-list state. I(S3) sees neighbours (D1,D4); I(S2) sees (D1,S3); each is a non-run-start, so neither replaces D4's schema occurrence. With noncolliding hashes this setup starts size8/mode2/used5: I(S3) grows normally to size16/used6, then I(S2) reaches used7. Include that growth/reinsertion in the trace; it does not invent a D1 schema occurrence, so schema lookup still selects D4 although physical-first v is D1. Verify the future lookup and duplicate/consumer behavior, not only sorted printed values. Add an opaque-separator control using the physical-extent rule and preserving the separator in every frontier.

**Recovery:** refuse/exhaust preparation or publication funding at the governing whole-move unit and assert both forests, table roots/history and metadata are restored. Retain the source nonmember-tail and existing empty/additional-metadata controls. The metadata publication difference is labeled Go policy, not an exact-native claim.

### 8.3 Prior-review closure index

This index records design closure, not implementation approval. R1/R2/R3 refer to
the supplied three design reviews; A1/A2/A3 to the rejected-code audits.

| Review items | Contract / evidence |
|---|---|
| R1.1–2 mutation reads/producers; R2.4 trim/reverse | §4 + Appendix A, all producer traces; validation duplicate and printer omissions now explicit. |
| R1.3,11 undo ordering/flat growth | §5.1, T-undo including populated capacity boundaries. |
| R1.4–5; R2.3 prepay/prefix/mandatory completion | §5.2–5.4, T-completion/delete/recover; finite same-allowance recovery, no canceled-context check in mandatory completion. |
| R1.6–7; R2.2,6 lifetime/lifecycle/cleanup | §3 B, actual cursors §2.3, deterministic D-0117 shrink, T-live/remote/D0117; A never enables routing. |
| R1.8–10 arithmetic/find-next/driver | §1 domain, §2.2 earliest re-find, §8.1 static observer/internal IDs. |
| R2.1 collection/options/cache | §2.2 conditional recursion, operand order, saved set/used; T-set. |
| R2.5 FirstChild | §6.2 6b0x, T-xfirst; error-capable evaluator and speculative propagation. |
| R2.7–8 prefix/cost | §6.1 snapshots vs optional trie; §7 early/final cost gates and cheap scenarios. |
| R2.9–11 physical helpers/schema guard/metadata timing/free safety | §§2.1,4,8.1; T-physical/merge2/merge3/driver. |
| R3.1–5 + Equal | §§4.2,2.2,2.3,2.1,4.1 respectively; Equal §2.1; T-apply/val-dup/remote/null/merge2/compare. |
| A1.1–5 | Foreign-key equivalence, nested authority/operand order, incremental paid comparison/polling, handle cursors: §§2,6; T-read/compare/churn. |
| A2.1–4 | Both registration origins, live enumeration, XML early termination and executed polling: §§1,6; T-build/read/churn/prefix. |
| A3.1–5 | Build abort, checked-resize elimination, ordinary cost ceilings, bounded prefix resolution and nil-before-charge: §§1,6,7; T-build/resize/prefix plus 6p/6d2. |

## Appendix A. Exhaustive native/Go sweep

Method: scan every `.c`/`.h` in pinned library `src/`, masking comments and
literals; inventory every `children_ht` access and `lyd_find_sibling_*`/`lyht_*`
call/reference, recursively expand call-bearing macros, then union I/U event
callers and primitive definitions. Map
matches to cached native function start/end tags and inspect the full bodies;
`lyfn` could not run because its ctags/cscope prerequisites were unavailable.
The union below accounts for **134 functions**, including non-data tables rather
than silently discarding them. Multiple functions sharing a row share its scope,
not an assumed implementation. Declaration-only prototypes and the
`children_ht` fields/threshold (`src/tree_data.h:856–858,917`) add no independent
caller. `LYHT_ITER_HLIST_RECS`/`LYHT_ITER_ALL_RECS`
(`src/hash_table_internal.h:104–114`) and `LYD_LIST_FOR_INST`/`_SAFE`
(`src/tree_data.h:495–511`) are expanded; their callers are included below. Tests/examples/tools are drivers, outside the library-port inventory.

The Go column names the **current migration site**, not a claim that it already
has native semantics. “No counterpart” is an explicit exclusion, not a future
implementation promise. Search Go semantic and physical callers separately:
`schemaLookup/schemaIndex`, `findFirst*`, `instanceRun`, `bucketNodes`, `opaqNext`,
registration writes and storage publication; the resulting producer/frontier
contract is §4. Additional transitive boundaries appear in A.4. Stage codes refer
to §7 and trace names to §8. An exclusion cannot become reachable through a new
public wrapper without its own design/fixtures and registration audit.

### A.1 Kernel and children-table authority

| Native function, checked file:line | Go site at baseline / exclusion reason | Contract | Stage | Acceptance trace |
|---|---|---|---|---|
| `lyht_hash_multi` (`src/hash_table.c:34`)<br>`lyht_hash` (`src/hash_table.c:69`) | `HashMulti32`/`FinishHash32`, `internal/types/lyb_hash.go:19,35`; schema hash `data/lyd_hash.go:12` | §§1,6 | 6a1 | T-primitive, T-prefix |
| `lyht_init_hlists_and_records` (`src/hash_table.c:86`)<br>`lyht_new` (`src/hash_table.c:114`)<br>`lyht_get_rec` (`src/hash_table_internal.h:98`)<br>`lyht_get_fixed_size` (`src/hash_table.c:538`) | No occurrence-array equivalent; replace `hashAdd`/`leaderBuild`, `data/siblings.go:853,1199` | §§1,3 | 6a1/2, 6p | T-build, T-null, T-dup |
| `lyht_find_rec` (`src/hash_table.c:282`)<br>`lyht_find` (`src/hash_table.c:310`)<br>`lyht_find_with_val_cb` (`src/hash_table.c:323`) | `schemaLookup`/`findFirstWork`, `data/siblings.go:1652,1991` | §2.1 | 6b0r/c | T-read, T-compare, T-null |
| `lyht_find_next_with_collision_cb` (`src/hash_table.c:336`)<br>`lyht_find_next` (`src/hash_table.c:379`) | No exact primitive; consumed candidates `data/edit.go:1699`; duplicate validation `data/validate_new.go:460` | §2.2 | 6a1, 6b4m, 6b5d | T-set, T-val-dup |
| `_lyht_insert_with_resize_cb` (`src/hash_table.c:385`)<br>`lyht_insert` (`src/hash_table.c:460`)<br>`lyht_insert_no_check` (`src/hash_table.c:466`) | Replace `hashPut`/`hashPutKey`/`leaderInsert`, `data/siblings.go:937,949,1466` | §1 | 6a1/2 | T-primitive, T-build |
| `lyht_resize` (`src/hash_table.c:211`)<br>`lyht_remove_with_resize_cb` (`src/hash_table.c:472`)<br>`lyht_remove` (`src/hash_table.c:532`) | Replace `hashRemove`/`leaderUnlink`, `data/siblings.go:1107,1551`; **callback override excluded**, unused by children tables | §§1,3 | 6a2, 6b1u | T-resize, T-D0117 |
| `lyht_free` (`src/hash_table.c:182`) | Reset/disposal `data/siblings_access.go:590`; undo horizons `data/hash_transaction.go:1112`; distinguish live retained domains | §§3,5,8.1 | 6b0j, 6b1u, 6b2a | T-live, T-undo, T-driver |
| `lyht_set_cb` (`src/hash_table.c:144`)<br>`lyht_set_cb_data` (`src/hash_table.c:154`)<br>`lyht_insert_with_resize_cb` (`src/hash_table.c:453`) | **Excluded API knobs:** children tables keep fixed callbacks/data; no Go counterpart needed. Read overrides are local (§2), not callback mutation | §1.1 / A.3 | — | T-read verifies immutability; no separate API |
| `lyht_dup` (`src/hash_table.c:164`) | **Excluded children operation:** only independent XPath set-table duplication; `internal/xpath/atomize.go:254`, `internal/xpath/eval.go:43`. Recursive data dup uses fresh capacity, below | §1.2 / A.3 | — | T-dup rejects copying children occurrences |
| `lyd_hash` (`src/tree_data_hash.c:32`) | `hashValue`/`refreshHash`, `data/lyd_hash.go:56,71`; keep existing stored-value deviations | §§1,4,6 | 6b1i/v | T-key, T-value |
| `lyd_insert_hash_add` (`src/tree_data_hash.c:128`)<br>`lyd_insert_hash` (`src/tree_data_hash.c:167`) | `hashAdd`/`leaderBuild`/`leaderInsertBetween`, `data/siblings.go:853,1199,1495`; parent completion `data/insert.go:2844` | §§1.2,4 | 6a2, 6b1i | T-build, T-key |
| `lyd_unlink_hash` (`src/tree_data_hash.c:207`) | `hashRemove`/`leaderUnlinkBetween`, `data/siblings.go:1107,1566`; not generic `hashForget` | §§1.2,3,4 | 6b1u | T-remove, T-resize |

### A.2 Data operations and their consumers

| Native function, checked file:line | Go site at baseline / exclusion reason | Contract | Stage | Acceptance trace |
|---|---|---|---|---|
| `lyd_find_sibling_schema` (`src/tree_data_common.c:1087`) | `schemaLookup`/`schemaIndex`, `data/siblings.go:1652,1684` | §2.1 | 6b0r; PR 7 wrappers | T-null, T-read, T-remote |
| `lyd_find_sibling_first` (`src/tree_data.c:3182`) | `findFirstWork`, `data/siblings.go:1991`; `instanceRun`, `data/edit.go:398` | §§2.1,2.3 | 6b0r/c | T-null, T-compare, T-live |
| `lyd_find_sibling_val` (`src/tree_data.c:3267`) | Path lookup `data/edit.go:328`; diff anchors `data/diff_apply.go:455,500`; typed XPath `data/xpathnode.go:70` | §§2.1,4,6.2 | 6b0r/x, 6b4a | T-null, T-anchor, T-xfirst |
| `lyd_find_sibling_opaq_next` (`src/tree_data.c:3388`) | `opaqNext`, `data/edit.go:2049` | §§2.1,6.2 | 6b0r | T-read, T-churn |
| `lyd_find_sibling_dup_inst_set` (`src/tree_data.c:3312`) | Consumed candidates `data/edit.go:1699–1746`; standalone public API absent | §2.2 | 6b4m | T-set |
| `lyht_dup_inst_ht_equal_cb` (`src/tree_data_common.c:56`)<br>`lyd_dup_inst_get` (`src/tree_data_common.c:80`)<br>`lyd_dup_inst_next` (`src/tree_data_common.c:108`) | `dupCache`/`dupInst` and consumption, `data/edit.go:1079–1170,1699–1746` | §§2.2,5 | 6b4m | T-set, T-merge |
| `lyht_dup_inst_ht_free_cb` (`src/tree_data_common.c:149`)<br>`lyd_dup_inst_free` (`src/tree_data_common.c:158`) | Go-owned cache lifetime, `data/edit.go:1079–1170`; release references at paired horizon | §§2.2,3,5 | 6b4m | T-set, T-live |
| `lys_getnext_data` (`src/tree_data_common.c:164`) | `getnextData`/`firstData`, `data/defaults.go:206,229` | §§2.3,6.2 | 6b5q | T-val-read, T-implicit |
| `lyd_insert_get_next_anchor` (`src/tree_data.c:503`) | `unorderedAnchor`/`opAnchor`, `data/insert.go:1413,1668` | §§2.3,4 | 6b1i | T-new, T-key, T-remote |
| `lyd_insert_node` (`src/tree_data.c:801`) | `insertPos`/`linkPlaced`/`registerParent`, `data/insert.go:374,2090,2844` | §§1.2,4 | 6b1i/s | T-new, T-sort, T-key |
| `lyd_insert_before` (`src/tree_data.c:1140`)<br>`lyd_insert_after` (`src/tree_data.c:1164`) | `insertBefore`/`linkPlaced`, `data/insert.go:2066,2090`; `insertAfter`, `data/move.go:276` | §§2.3,4 | 6b1i, 6b3m, 6b4a | T-remote, T-anchor |
| `lyd_unlink_ignore_lyds` (`src/tree_data.c:1188`)<br>`lyd_unlink` (`src/tree_data.c:1242`) | `unlinkRetainingHistory`/`unlinkWithCleaner`, `data/insert.go:2881,2920`; RB selection `data/rb_remove.go:125` | §§2.3,3,4 | 6b1u | T-remove, T-D0117 |
| `lyd_unlink_siblings` (`src/tree_data.c:1266`) | **Excluded standalone sibling-suffix detach API:** none at baseline; public `Node.Unlink` removes one (`data/edit.go:143`). Do not equate batch deletion with native split/relink/I. Shared U/I primitives included above | §4 / A.3 | — | Future API needs split + per-node relink trace |
| `lyd_move_nodes_ordby_schema` (`src/tree_data.c:891`)<br>`lyd_move_nodes_at_once` (`src/tree_data.c:944`)<br>`lyd_move_nodes_by_schema` (`src/tree_data.c:982`) | `moveNodes`/`merge`, `data/move.go:803,1016`; source upfront detach at `:903` must not reorder events | §4 | 6b3m | T-move |
| `lyds_additionally_create_rb_nodes` (`src/tree_data_sorted.c:1052`) | `rbPos`/`applyRunReorderHash`/`rbPublish`, `data/insert.go:814,330,2675` | §§2.3,4 | 6b1s | T-sort, T-remote |
| `lyds_additionally_reuse_rb_tree` (`src/tree_data_sorted.c:1118`)<br>`lyds_insert2` (`src/tree_data_sorted.c:1256`) | **Excluded destructive-merge RB pool reuse:** no corresponding Go mode; `Tree.Merge` preserves source (`data/edit.go:191–220`). Ordinary retained-root reuse remains included | §4 / A.3 | — | Future destructive mode needs separate history traces |
| `lyds_merge_nodes1` (`src/tree_data_sorted.c:1444`) | `merge`/`rbPublish`, `data/move.go:1016–1059,1100–1178`, `data/insert.go:2675` | §4 | 6b3s1 | T-merge1 |
| `lyds_merge_nodes2_front` (`src/tree_data_sorted.c:1488`)<br>`lyds_merge_nodes2_among` (`src/tree_data_sorted.c:1532`)<br>`lyds_merge_nodes2_back` (`src/tree_data_sorted.c:1577`) | `merge`, `data/move.go:1005–1099`; D-0062 (`conformance/deviations.md:38`) | §4.1 | 6b3s2 | T-merge2: defined / deviation controls separately |
| `lyds_merge_nodes3` (`src/tree_data_sorted.c:1661`) | `merge`, `data/move.go:1031–1041,1100–1178`; metadata clear before traversal | §4 | 6b3s3 | T-merge3 |
| `lyd_change_node_value` (`src/tree_data_new.c:1130`) | `changeNodeValue`, `data/edit.go:879`; relocation `data/move.go:373`; key history `data/lyd_hash.go:211–290` | §§4,5 | 6b1v | T-value, T-key, T-undo |
| `lyd_new_path_create` (`src/tree_data_new.c:1568`) | `newPath`, `data/edit.go:491`; existing key reuse, insert and partial failure | §4 | 6b1i/v | T-new, T-key |
| `lyd_new_implicit` (`src/tree_data_new.c:1890`) | `newImplicit`/`modInsert`/`newImplicitR`, `data/defaults.go:252,373,420` | §§4,5.4 | 6b5i | T-implicit, T-completion |
| `lyd_parse_value_fragment` (`src/tree_data.c:252`) | **Excluded endpoint:** no Go value-fragment API. `newPath` (`data/edit.go:491`) and parsers (`data/parse.go:419`) are separate operations; fragment-specific duplicate-key U/H/I at native `:317–327` requires its own future adapter | §4 / A.3 | — | Future fragment duplicate-key success/error trace |
| `lyd_compare_siblings_` (`src/tree_data.c:1830`) | `compareSiblingFrames`/`compareDataStack`, `data/siblings.go:2320,2361`; public `Equal`, `data/compare.go:24` | §§2.1,6 | 6b0c | T-compare through paid + Equal entries |
| `lyd_dup_r` (`src/tree_data.c:2169`)<br>`lyd_dup` (`src/tree_data.c:2424`) | `dupWork`/`dupSingle`/`dupNodes`, `data/dup.go:198,376,522`; capacity `:441`, reuse `:566` | §§1.2,4.2 | 6b2d | T-dup, T-dup-any, T-apply |
| `lyd_merge_sibling_r` (`src/tree_data.c:2640`) | `mergeSibling`/`flushMerge`, `data/edit.go:1762,1975`; opaque prefixes `:1873–1888` | §§2.2,4 | 6b4m, 6b2p | T-merge, T-set, T-prefix |
| `lyd_diff_add` (`src/diff.c:383`) | `diffAdd`/`diffDup`/`valDiffAdd`, `data/diff.go:456,395,164` | §4 | 6b4g/d | T-diff ancestor/key, T-delete |
| `lyd_diff_userord_get` (`src/diff.c:539`) | `userordGet`, `data/diff_gen.go:337`; macro-selected run, error-capable collection | §2.3 | 6b4g | T-macro, T-diff |
| `lyd_diff_find_match` (`src/diff.c:991`) | `findMatch`/`diffFind`, `data/diff_gen.go:282`, `data/diff.go:593` | §§2.2,4 | 6b4g/d | T-set, T-diff |
| `lyd_diff_node_metadata_r` (`src/diff.c:1161`) | `nodeMetaR`, `data/diff_gen.go:656`; both inputs | §4 | 6b4g | T-diff metadata |
| `lyd_diff_insert` (`src/diff.c:1451`) | `diffInsert`/`preflightDiffUserordCreate`, `data/diff_apply.go:350,272`; actual-anchor adapter | §§2.3,4.2 | 6b4a | T-anchor, T-remote |
| `lyd_diff_merge_delete` (`src/diff.c:2503`) | `diffMergeDelete`, `data/diff.go:851`; child-presence decisions | §4 | 6b4d | T-diff merge/delete |
| `lyd_validate_duplicates` (`src/validation.c:639`) | `dupIndex.bucket`/`duplicates`, `data/validate_new.go:439,460`; enclosing `validateNew`, `:25` | §2.2 | 6b5d | T-val-dup |
| `lyd_validate_mandatory` (`src/validation.c:1155`) | `mandatory`, `data/validate.go:541`; lookup or dummy when | §§4,6.2 | 6b5q/w | T-val-read, T-completion |
| `lyd_validate_minmax` (`src/validation.c:1220`) | `minmax`, `data/validate.go:582`; macro-selected run and threshold stop | §2.3 | 6b5q | T-macro |
| `lyd_validate_autodel_leaflist_dflt` (`src/validation.c:816`)<br>`lyd_validate_autodel_cont_leaf_dflt` (`src/validation.c:863`) | `autodelLeafListDflt`/`autodelContLeafDflt`, `data/validate_new.go:234,248`; `instances`, `:209`; secure batch after correct selection | §§2.3,5.2 | 6b5w | T-macro, T-delete |
| `lyd_val_uniq_find_leaf` (`src/validation.c:1313`) | `uniqFindLeaf`/`uniqValue`, `data/validate.go:636,650` | §§4,6.2 | 6b5q | T-val-read nested unique |
| `lyd_validate_unique` (`src/validation.c:1480`) | `unique`, `data/validate.go:770`; include nested leaf selection. **Exclude separate unique-value hash tables**, not children state | §4 / A.3 | 6b5q | T-val-read, existing unique controls |
| `lyd_val_op_merge_find` (`src/validation.c:2353`)<br>`_lyd_validate_op` (`src/validation.c:2423`) | Dependency match/temporary move/restore, `data/validate_op.go:96–246,298–318`, `data/insert.go:2395` | §§4,5.3 | 6b3o; repeat 6b5i | T-op, T-completion |
| `ly_path_eval_partial` (`src/path.c:1331`) | `findPath`, `data/edit.go:276–396`; typed instance-id `data/unres.go:1175–1240` | §§2.1,6.2 | 6b0r/x, 6b5q | T-null, T-val-read |
| `moveto_node_hash_child` (`src/xpath.c:6309`) | `hashChild`/`xn.LookupPrepared`, `internal/xpath/eval.go:1006`, `data/xpathnode.go:70` | §6.2 | 6b0x | T-xfirst, T-val-read |
| `eval_name_test_try_compile_predicate_append` (`src/xpath.c:7910`) | `hashPredicates`→`firstInstance`→`xn.FirstChild`, `internal/xpath/eval.go:766,854`, `data/xpathnode.go:179` | §6.2 | 6b0x | T-xfirst |
| `json_print_next_empty_leaf_list` (`src/printer_json.c:1040`) | `nextEmpty`, `data/print_json.go:462`; paid cast vs public mode `data/print.go:48–68` | §6.2 | 6cJ | T-print |
| `lyd_trim_xpath` (`src/tree_data.c:3581`) | `TrimXPath`, `data/trim.go:61–219`; **exclude** temporary parent/result-set hash mechanics; preserve D-0101 | §§4,5.4 | 6b6t | T-trim |
| `lyd_free_subtree` (`src/tree_data_free.c:214`) | Table disposal `data/siblings_access.go:590`; any clear `data/any.go:403`; node cleanup `data/parse.go:1245`, `data/insert.go:2920`; no manual Go free | §§3,5,8.1 | 6b1u, 6b2a | T-live, T-driver, T-D0117 |

### A.3 Other tables and explicit exclusions

| Native function, checked file:line | Go site at baseline / exclusion reason | Contract | Stage | Acceptance trace |
|---|---|---|---|---|
| `lyd_get_or_create_leafref_links_record` (`src/tree_data.c:3757`)<br>`lyd_free_leafref_nodes` (`src/tree_data_free.c:178`) | **Exclude context leafref_links_ht mechanics**; `linksOf`/`freeLinks`, `data/links.go:33,100`. Include reached guards/cleanup costs in deletion/ownership | §§3,5 | 6b1u/5w lifetime only | T-live, T-delete; no registration primitive change |
| `lyd_val_getnext_ht_new` (`src/validation.c:89`)<br>`lyd_val_getnext_ht_free` (`src/validation.c:101`)<br>`lyd_val_getnext_get` (`src/validation.c:118`) | **Exclude schema-enumeration cache table**; `getnextOf`, `data/defaults.go:98`. Its data lookup consumer is `lys_getnext_data`, A.2 | §4 | — | T-val-read covers data decisions, not cache layout |
| `set_insert_node_hash` (`src/xpath.c:733`)<br>`set_remove_node_hash` (`src/xpath.c:784`)<br>`set_dup_node_hash_check` (`src/xpath.c:819`)<br>`lyxp_set_free_content` (`src/xpath.c:849`)<br>`set_copy` (`src/xpath.c:926`)<br>`set_fill_set` (`src/xpath.c:1041`)<br>`set_sort` (`src/xpath.c:1749`) | **Exclude XPath result-set hash history**; `value`, `moveto`, `sortSet`, `internal/xpath/eval.go:43,1192,1579`; schema-set clone `internal/xpath/atomize.go:254`. D-0101 remains (`conformance/deviations.md:63`) | §4 | — | Existing XPath/trim tests; T-trim preserves D-0101 |
| `lydict_init` (`src/dict.c:54`)<br>`lydict_clean` (`src/dict.c:64`)<br>`_lydict_remove` (`src/dict.c:128`)<br>`dict_insert` (`src/dict.c:216`)<br>`dict_dup` (`src/dict.c:341`) | **Exclude native string-intern/refcount table**; no dictionary API in Go; managed node strings/value storage `data/node.go:40–81` | §3 | — | No children-table acceptance unit |
| `ly_ctx_set_options` (`src/context.c:601`)<br>`ly_ctx_unset_options` (`src/context.c:679`)<br>`ly_ctx_shared_data_remove_and_free` (`src/ly_common.c:295`)<br>`ly_ctx_shared_data_create` (`src/ly_common.c:339`) | **Exclude context string/pattern/leafref table management**; leafref equivalent `data/links.go:33,100`; compiled patterns `internal/types/string.go:65`. No native shared-data allocator counterpart | §3 | — | No children-table acceptance unit |
| `ly_ctx_shared_data_pattern_get` (`src/ly_common.c:569`)<br>`ly_ctx_shared_data_pattern_del` (`src/ly_common.c:627`) | **Exclude shared compiled-pattern cache**; Go compiles/stores on schema pattern, `internal/types/string.go:65–76` | A.3 | — | No children-table acceptance unit |
| `ly_ctx_pattern_ht_erase` (`src/ly_common.c:547`) | **Exclude shared pattern-table macro traversal/reset**; Go schema-owned programs, `internal/types/string.go:65–76` | A.3 | — | No children-table acceptance unit |
| `ly_ctx_new_change` (`src/context.c:746`) | **Exclude module-set fingerprint** (`lyht_hash_multi`, not children state); no corresponding mutable native context fingerprint API | A.3 | — | No children-table acceptance unit |
| `ly_ctx_compiled_size` (`src/context.c:1365`)<br>`ly_ctx_compiled_print` (`src/context.c:1395`)<br>`ctxs_when` (`src/printer_context.c:163`)<br>`ctxs_patterns` (`src/printer_context.c:205`)<br>`ctxs_type` (`src/printer_context.c:235`)<br>`ly_ctx_compiled_ext_stmts_storage_size` (`src/printer_context.c:568`)<br>`ly_ctx_compiled_addr_ht_get` (`src/printer_context.c:1941`)<br>`ly_ctx_compiled_addr_ht_add` (`src/printer_context.c:1966`) | **Exclude compiled-context sizing/serialization shared-address tables**; no corresponding Go native-memory-format API | A.3 | — | No children-table acceptance unit |
| `ctxs_dict_strings` (`src/printer_context.c:77`)<br>`ctxp_dict_strings` (`src/printer_context.c:715`) | **Exclude compiled-context dictionary macro enumeration**; no corresponding Go native-memory-format API | A.3 | — | No children-table acceptance unit |
| `lyb_generate_hash` (`src/lyb.c:35`)<br>`lyb_print_ctx_free` (`src/printer_lyb.c:52`)<br>`lyb_hash_sequence_check` (`src/printer_lyb.c:124`)<br>`lyb_hash_siblings` (`src/printer_lyb.c:163`)<br>`lyb_hash_find` (`src/printer_lyb.c:245`) | **Exclude LYB wire-schema hash tables/codec**, no Go LYB data format (`data/parse.go:26–32`). This does **not** exclude LYB value bytes used by stored hashes (`internal/types/lyb_hash.go:43–56`) | §1 / A.3 | — | Stored-hash controls retained; no LYB codec unit |
| `schema_mount_get_content_id` (`src/plugins_exts/schema_mount.c:422`)<br>`schema_mount_dup_parent_ref` (`src/plugins_exts/schema_mount.c:1018`)<br>`schema_mount_compiled_size` (`src/plugins_exts/schema_mount.c:1450`) | **Excluded schema-mount extension**, U-0024 (`conformance/deviations.md:139`); no supported Go counterpart. Its lookup must be audited before enabling mount | A.3 | — | Existing unsupported gate |
| `lysp_check_dup_ht_insert` (`src/tree_schema_common.c:335`)<br>`lysp_check_dup_typedef` (`src/tree_schema_common.c:366`)<br>`lysp_check_dup_typedefs` (`src/tree_schema_common.c:446`)<br>`lysp_check_dup_grouping` (`src/tree_schema_common.c:506`)<br>`lysp_check_dup_groupings` (`src/tree_schema_common.c:563`)<br>`lysp_check_dup_features` (`src/tree_schema_common.c:604`)<br>`lysp_check_dup_identities` (`src/tree_schema_common.c:636`) | **Exclude parser/schema declaration duplicate tables**; `checkDups`/`inAncestors`, `internal/compile/dup.go:83,184`; no data-node registration | A.3 | — | Existing compile tests |

### A.4 Transitive boundaries needed to accept the direct callers

These are additional checked callbacks/frontiers, not extra direct-call matches
in the 134-function count. Their inclusion prevents an apparently complete
primitive sweep from missing ordering, producer or budget obligations.

| Native function, checked file:line | Go site at baseline | Contract | Stage | Acceptance trace |
|---|---|---|---|---|
| `lyd_hash_table_val_equal` (`src/tree_data_hash.c:92`); `lyd_hash_table_schema_val_equal` (`src/tree_data_common.c:1075`) | `findFirstWork`/`schemaLookup`, `data/siblings.go:1991,1652` | §§1,2.1 | 6a1, 6b0r/c | T-read, T-compare |
| `lyd_val_dup_val_equal` (`src/validation.c:624`); `lyd_validate_new` (`src/validation.c:1008`) | `duplicates` then NEW/autodel, `data/validate_new.go:460,80–98` | §2.2 | 6b5d | T-val-dup |
| `lyd_compare_schema_equal`/`lyd_compare_single_data` (`src/tree_data.c:1509,1707`) | `compareSchemaEqual`, `data/dup.go:61`; `compareDataStack`, `data/siblings.go:2361`; `Equal`, `data/compare.go:24` | §2.1 | 6b0c | T-compare, foreign keys and nested winners |
| `lyd_insert_after_node`/`lyd_insert_before_node` (`src/tree_data.c:596,630`); `lyds_link_data_node` (`src/tree_data_sorted.c:1021`) | `linkPlaced`/`rbPos`, `data/insert.go:2090,814`; actual-storage write-set adapter is new | §2.3 | 6b1s, 6b3m | T-remote, both forests |
| `lyds_insert`/`lyds_merge`/`lyds_merge_nodes2` (`src/tree_data_sorted.c:1213,1693,1612`) | `rbPos`/`rbPublish`, `data/insert.go:814,2675`; `merge`, `data/move.go:1016` | §§4,4.1 | 6b1s, 6b3s1/2/3 | T-sort, T-merge1/2/3 |
| `lyd_parser_node_insert` (`src/parser_common.c:392`) | `nodeInsert`, `data/parse.go:788`; deferred `appendSorted`, `:829`; `applyLeaders`, `:951`; `publishDirty`/`flushDirtyRun`, `:1068,1147` | §4 frontier, publish once | 6b1pi/pd/pc | T-parse, T-deferred, T-completion |
| `lydjson_metadata_finish`/`lydjson_parse_attribute` (`src/parser_json.c:583,1147`) | Physical scans at `data/parse_json.go:397–449,937–969`; separate from semantic schema lookup | §4 | 6b1pm | T-physical |
| `lyd_parse_json_restconf`/`lyd_parse_xml_netconf` (`src/parser_json.c:2117`; `src/parser_xml.c:1451`) | `parseJSONRestconf`/`parseXMLNetconf`, `data/envelope_json.go:45`, `data/envelope_xml.go:152`; shared insertion/cleanup | §§4,5.3 | 6b1pi/pc | T-parse envelopes, T-completion |
| `lyd_change_term_val` (`src/tree_data_new.c:1200`) | `changeNodeValue`, `data/edit.go:879`; canonical-only path below must not invent these events | §4 | 6b1v | T-value |
| `lyd_create_any_datatree`/`lyd_any_copy_value` (`src/tree_data_new.c:189,320`) | `takeAnyChildren`/`clearAny`/`copyAnyValue`, `data/any.go:346,403,460`; `dupWork`, `data/dup.go:198` | §§3,4 | 6b2d/a/p | T-any, T-dup-any, T-prefix |
| `lyd_diff_apply_r` (`src/diff.c:1799`) | `applyR`/`diffDupCreate`, `data/diff_apply.go:98,240` | §4.2 | 6b4a | T-apply, parent callback before descendants |
| `lyd_diff_reverse_userord`/`lyd_diff_reverse_all` (`src/diff.c:3273,3473`) | `reverseOrder`/`ReverseDiff`, `data/diff_reverse.go:163,33` | §§4,5.4 | 6b4r | T-reverse, all-unlinks boundary |
| `lyd_value_validate_incomplete`/`lyd_validate_unres` (`src/tree_data_common.c:539`; `src/validation.c:530`) | Incomplete updates, `data/unres.go:897–991`; dirty comparison keys `data/lyd_hash.go:211–242` | §4 canonical-only | 6b5v | T-value |
| `lyd_validate_unres_when`/`lyd_validate_autodel_node_del`/`lyd_validate_autodel_case_dflt` (`src/validation.c:462,334,920`) | `whenPass.del`, `data/unres.go:479`; `autodelCaseDflt`/`autodel`, `data/validate_new.go:292,322` | §§4,5.2–5.4 | 6b5w | T-delete, T-recover |
| `lyd_validate_dummy_when` (`src/validation.c:1083`) | `dummyWhen`, `data/unres.go:1371` | §§4,5.3 | 6b5w | T-completion |
| `lyxml_value_compare` (`src/xml.c:1351`) | `compareXMLValue`/`resolveOpaque`, `data/opaque_compare.go:48,39`; stored prefix adapters §6.1 | §6.1 | 6b0p/c, 6b2p | T-prefix, nil and early termination |

## 9. Decisions for the joint review

### 9.1 Questions the maintainer must decide

**Maintainer decisions, 2026-11-05.** Ownership allowance (§7): **approved** as
written. D-0117 cleanup refinement (§3): **approved**. Failed cost gate: **stop and
return** with the measured failing cases; no widened tolerance or cross-scenario
trade. Remote-write safety refusal (§2.3): 6a0 confirmed public reachability
(no rewind cycle; defined later results). **Decided 2026-11-06: refuse** the
inconsistent remote-last insertion with LY_EINVAL before any write, as a new
narrow M5 deviation. The remote-write portion of 6b1s is unblocked. The table below records the questions as reviewed.

| Question | Recommendation and dependency |
|---|---|
| Accept the narrow malformed remote-link boundary in §2.3, or represent native's arbitrary link graph? | **Accept refusal with LY_EINVAL before the failing insertion unit's writes**, retaining the §5 prefix/rollback boundary. Accept only after 6a0 replays §2.3's public sequence and confirms reachability (if it does not reach, record the earlier boundary and narrow the scope). Require both-forest native prefix evidence and valid remote controls. If a supported public history reaches it, the refusal is an intentional safety difference with a fixture and a registry entry. Do not redesign parts 4/5 around corrupt links. This gates the remote-write portion of 6b1s and final routing; the immediate units in §7 can start now. |
| Accept §3's exact D-0117 cleanup transition? | **Yes:** repeated earliest pointer/schema-hash removal in the just-unlinked table, including normal shrink and checked deduplication; no move purge or global sweep. Record the refinement with its eventual implementation and native-alive/Go-policy controls before 6b1u acceptance. |
| Approve the ownership allowance in §7: at most +32 B and +1 allocation per genuinely new independent public forest/result that survives its creating call, only for the domain descriptor? | **Approve.** Zero slack is not credible: a ~24-byte descriptor adds one allocation per new forest, and the Node layout shrink may not cross an allocator size class. The allowance never applies per node, target, merge miss, diff action or discarded attempt. Without it, 6p would likely stop the migration on the empty-Tree and detached-singleton cases. |
| Confirm the cost gate's outcome if early B fails a cheap case? | **Stop broad migration and revise the replacement until each metric passes.** Any proposed change to the performance contract returns to the maintainer with the measured failing cases; never silently widen a tolerance or trade one scenario's savings for another's cost. Freeze baseline evidence now. |

Lifecycle B and A's restricted role are already decided, not questions reopened
here. Exact build abort, checked-resize identity, unbudgeted Equal and existing
failure boundaries are this proposal's defaults. Optional accelerator choice
needs evidence, not an advance policy decision. General routing remains blocked
until all producer/read units, production ownership and both cost gates pass.

### 9.2 Deviation candidates and limits

No registry is edited by this document. An accepted new M5 deviation receives an
ID from the stream's **D-0125…D-0149** range, never “next free” across streams
(root `AGENTS.md:64–78`). Its implementation PR must supply a precise boundary,
alternative, rationale and safe evidence (`conformance/AGENTS.md:9–16`).

| Candidate | Recommendation / necessary evidence |
|---|---|
| Malformed remote sorted-anchor link graph | **New narrow safety deviation**, §2.3; cannot be folded silently into D-0121's closed classes or D-0117's removed operation-child case (`conformance/deviations.md:53,55`). Native link writes/watchdog establish the boundary; Go tests assert refusal, valid forests and recovery. RFC 7950 describes a data hierarchy and system ordering, not an obligation to reproduce corrupt memory links; that is the safety rationale, not a normative registration algorithm. [§7](https://www.rfc-editor.org/rfc/rfc7950.html#section-7), [§7.7.7](https://www.rfc-editor.org/rfc/rfc7950.html#section-7.7.7). |
| Deterministic removal/shrink under D-0117 | **Refine the existing entry's implementation**, subject to §9.1; exact cleanup sequence and scope in §3. Its broader outcome “drop with node” does not select a shrink policy. Do not claim native equivalence for post-free reads. |
| Continue initial build after schema-record EEXIST | **Do not adopt; port abort.** Would change future findability and needs a separately reviewed semantic deviation with collision fixtures. Binary's octet-sequence semantics offer a possible motivation, not an RFC rule for this table. [RFC 7950 §9.8](https://www.rfc-editor.org/rfc/rfc7950.html#section-9.8). |
| Preserve duplicate occurrences across checked resize | **Do not adopt; port elimination.** A capacity-independent identity alternative must define insertion, removal, future winners and duplicate validation, with grow/shrink evidence. List-key uniqueness may motivate a different API, but does not specify occurrence identity. [RFC 7950 §7.8.2](https://www.rfc-editor.org/rfc/rfc7950.html#section-7.8.2). |
| Sorted merge 2 | **Keep D-0062**, §4.1. Supported Go event schedule and native-defined controls remain separate from UB controls (`conformance/deviations.md:38`). |
| Other residual freed references / find-next cycles | **No silent broader cleanup or invented completed result.** Retain live Go references; stop unsafe native observation. Paid nontermination exhausts the existing allowance with no partial set (§§2.2,3,8.1). A later broader semantic policy needs its own boundary and evidence. |
| Internal diagnostics | **Keep D-0118's existing narrow scope**, never use it for validation find-next EINT or changed table state (`conformance/deviations.md:54`; §2.2). |
| Extra ordinary bytes/charges, unpolled hashing, nil-before-charge, hidden default quotas | **Not conformance deviations.** These are Go cost/cancellation/API contracts; fix implementation or explicitly revisit that project contract. RFC arguments cannot waive them. |

### 9.3 What a reviewer should check first

1. **Selection completeness:** Appendix A's 134-function inventory and macro
   expansion, null-start rules, both callback operands, validation's forced-read
   re-find, and Equal/public-print entries. Trace T-val-dup through NEW/autodelete.
2. **Producer order:** ApplyDiff's parent callback before non-key actions; deferred
   parse frontiers; all-unlinks-before-reinsert reversal; D-0062's explicit destination permutation, attachment frontiers and Go-policy metadata timing, distinguished from native front/among/back;
   remote-last writes to both forests, with the explicit safety decision.
3. **Recovery and lifetime:** two committed child frames before the parent's first
   write; finite completion after cancellation; D-0117 shrink; moved winners'
   current siblings/descendants and disposal without idle-domain retention.
4. **Cost evidence before migration:** 6p's real B replacement, retained empty and
   one-to-three-child cases, cold prefixes, populated journal boundaries, and
   separate per-scenario byte/allocation/visit/charge ceilings; repeat at 6d2.
5. **Evidence honesty:** actual callback/static-driver build, internal allocation
   IDs and tombstones, safe native prefixes for UB, independent public-oracle
   controls and fixtures. Source inspection and layout arithmetic do not satisfy
   runtime acceptance; A's approval never authorizes PR 7.
