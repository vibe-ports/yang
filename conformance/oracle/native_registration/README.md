<!-- SPDX-License-Identifier: BSD-3-Clause -->

# Native registration observer (G1 / 6a0)

This auxiliary oracle links an isolated, instrumented **static** libyang v5.8.6
(`47351e59e2965e350f9d4098f15ecbe6b3850f6f`). It does not change the installed
oracle, production Go paths, or golden files. `instrument.py` requires exact
source matches and adds observational hooks to the pinned functions. The hidden
comparison callback and its data are obtained from a children table constructed
by real `lyd_new_inner`/`lyd_new_term` calls. No callback implementation is copied.

The build is canonical linux/amd64, Release/NDEBUG, built-in Jenkins hash,
`BUILD_SHARED_LIBS=OFF`, GCC 14.2.0 and CMake 3.31.6. `.build/BUILDINFO` records
the source pin, instrumentation/driver digests, compiler, ABI architecture and
the effective CMake/compiler flags. The library remains dynamically linked to
its ordinary system dependencies; "static" refers to libyang itself.

## Prepare and run

The existing dev image has GCC and Python but CMake lives in its `libyang` build
stage. Preparation copies the already pinned source, untouched Release objects,
and CMake tools from that existing stage without executing a container as root,
installing anything, or changing a policy file. Run these Docker preparation
commands from the checkout on the host (Docker configuration may be supplied
through its normal environment):

```sh
docker build --platform linux/amd64 --target libyang -t yang-native-tools .
docker create --name yang-native-copy yang-native-tools true
mkdir -p conformance/oracle/native_registration/.build/tools/bin
mkdir -p conformance/oracle/native_registration/.build/tools/lib
mkdir -p conformance/oracle/native_registration/.build/tools/share
docker cp yang-native-copy:/src/libyang conformance/oracle/native_registration/.build/source
docker cp yang-native-copy:/src/build conformance/oracle/native_registration/.build/config
docker cp yang-native-copy:/usr/bin/cmake conformance/oracle/native_registration/.build/tools/bin/
docker cp yang-native-copy:/usr/share/cmake-3.31 conformance/oracle/native_registration/.build/tools/share/
for lib in libarchive.so.13 librhash.so.1 libuv.so.1 libjsoncpp.so.26 libacl.so.1; do
  docker cp -L yang-native-copy:/usr/lib/x86_64-linux-gnu/$lib conformance/oracle/native_registration/.build/tools/lib/
done
docker rm yang-native-copy
chmod -R a+rwX conformance/oracle/native_registration/.build
DEV_PLATFORM=linux/amd64 ./dev bash conformance/oracle/native_registration/build.sh </dev/null
DEV_PLATFORM=linux/amd64 ./dev python3 conformance/oracle/native_registration/check.py \
  --driver conformance/oracle/native_registration/.build/driver \
  --comparison-driver conformance/oracle/native_registration/.build/driver-unmodified </dev/null
```

The normal dev user performs compilation and execution. The preparation
container is never started. `.build` is ignored. A full-library check remains
the ordinary unmodified oracle lane; this build does not replace it.

`build.sh` also archives the **untouched Release objects** from the pinned
Dockerfile's oracle stage into a second library and links `driver-unmodified`.
The replay check requires both executable paths explicitly, verifies their
instrumented/unmodified ABI markers, and compares terminating public JSON,
validation statuses/diagnostics, and schema-lookup statuses plus normalized
winner/current-parent identities. Allocation generations are normalized injectively from completed-operation
components, anchored by operation-specific handle bindings. Internal allocations
and successive incarnations of a reused handle have distinct keys; process-local
generations are never compared directly. Every comparison checks the exact completed command
sequence, statuses and diagnostic code/path/app-tag, plus the independently
expected terminal command or stop boundary on both sides. LOGINT source paths
and line numbers differ between builds; full validation diagnostics are still
compared. An empty result set cannot satisfy a control. Constructor-rejection
controls instead compare their explicit rejection snapshot and nonempty log.
The Go evidence test passes its selected executable
and uses `YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER` when provided, otherwise
`driver-unmodified` beside that selected executable. Safe-prefix boundary
cases are never run to an accidental completed result or used as goldens.

## Replay protocol and evidence

Input is a bounded line-oriented public-call script. `cases/*.calls` are the
independent live-oracle request cases. IDs are script handles, never addresses.
`root`, `term`, `list`, `opaque`, `dup`, `unlink`, `detach-siblings`, `attach`, `before`, `change`, `parse`,
`schema`, `validate`, `print`, `free`, `free-root`, and `rewind` call their corresponding
libyang APIs. `opaque ID PARENT NAME VALUE` uses the JSON opaque constructor;
`before ANCHOR NODE` moves a node using public `lyd_insert_before`;
`free-root` invokes whole-root `lyd_free_all`. `dup` takes native option bits (66 means NO_META|NO_LYDS).
`new SIZE MODE`, `insert ID HASH CHECK`, `remove ID HASH`, `I ID`, `U ID`,
`find-next-loop` and `check-handle` are explicitly **low-level controls**, not evidence of
public reachability. Modification-callback controls hold distinct, equal-valued
real native nodes alive. `dump` observes state only.
`list ID PARENT NAME KEY` calls `lyd_new_list` with one string key.
The new list/schema-pointer requests select `--event-schema`, which adds a
keyed list and a presence container with a second equal-named leaf-list.
Other requests retain the original schema and snapshot fields, including their
allocator address-reuse setup. `frontier PARENT A B C D` is a **synthetic low-level**
four-child frontier with real native allocations and untouched schema pointers;
it provides no evidence of public mixed-schema reachability. Only this schema
mode emits module names and stable schema-pointer identities.

Stdout is JSONL. `--quiet` emits command snapshots; the default emits primitive
events as well. `--remote-stop` exits immediately after the remote-last
insert-after primitive. `--remote-watchdog` runs the separate bounded rewind
first-sibling control (which terminates for the parented remote-last replay).
`--merge2-ub-stop` exits before an exhausted forward iterator, unsafe continuation, or
heterogeneous-schema RB insertion. A 15-second process alarm and bounded table,
RB and find-next traversals label watchdog boundaries; they never invent a
completed match/miss.
`free-slot-cap.calls` fills a disabled eight-slot table and records its inert
terminal link. A checked insertion of an existing pointer/hash at exhausted capacity executes
and returns native EEXIST without a table change. The driver stops before an
insertion needing a nonexistent free slot; this is an observer resource
boundary, not a native insertion verdict.

Snapshots contain stable table identities, presence/capacity/used/mode, bucket endpoints, slots,
free-list order and its inert terminal `size` link, and ordered chains with
recorded hash, allocation generation and trace-only occurrence serial. Resizes
carry occurrence serials forward; checked duplicate elimination retires the
eliminated occurrence. In the untouched build, children-table reconciliation
tokens have `continuity: unknown` and do not establish occurrence lifetimes.
Nodes carry `(operation, creation ordinal)`, an immutable successful-return label,
a separate attempted-output label, dup source generation, hash/flags/schema, raw link identities,
parent/first/tail, context identity, metadata ownership and bounded RB topology.
`rb_stored_root` records the metadata pointer separately from `rb_root`, obtained
by following the RB parent links before walking the complete component. Merge-2
also records its explicit local `working_rb_root` after each destination RB
insertion. Stable boundary assertions require complete membership, one root,
reciprocal parent/child edges and complete reachability. `logs` captures
the native logging callback separately from context `diagnostics`, including
LOGINT with a null context. Helper and enclosing return codes remain separate.

All native data-node allocation sites were inventoried: `lyd_create_term`,
`lyd_create_inner`, `lyd_create_any`, `lyd_create_opaq`, raw-term construction,
and the four layouts allocated by `lyd_dup_r`. Parse, implicit creation and any
payload creation reach those constructors. Hooks register allocations before
their first link/registration, including failed value storage and cross-context
dup failure. Before actual free the registry tombstones the generation; retired
tables are never dereferenced. Current metadata on tombstones is explicitly
unavailable. A reused address gets a fresh generation. Semantic probes stop if
a retained occurrence names a tombstone. The observer never follows a freed
pointer to recover its identity.
Script handles independently retain their allocation generations. The
`handle-tombstone` controls free a parent containing an explicitly handled
child, reuse that child's address, and stop both a registry-only check and a
public schema probe before dereferencing the stale handle. Live output handles
cannot be overwritten; a new creation may reuse a retired handle number.
`dead-occurrence.calls` keeps two generic occurrences after node release and
checks that a native find-next probe stops without entering its read callback.
The same tombstone gate covers public producers that can internally select
through a children table, as well as explicit read commands.

## Measured prerequisites

`check.py` writes complete labeled JSONL to `.build` and emits compact summaries.
It checks the following outcomes; these are runtime results, not predictions:

Every replay requires its ready event and each completed request's command
snapshot at the expected operation. Completed `print`, `schema` and `rewind`
requests also require their separate public/read result at that operation.
Fixed per-script counts require both boundaries of I/U, insertion/removal,
validation, physical link/unlink and metadata/merge primitives, and allocation,
retirement and free events. Deliberately stopped merge prefixes require only
the boundaries actually reached. Address-reuse controls require at least one
reuse event; their exact count is allocator-dependent.

Every nonzero generation in event fields, physical links (including first/tail),
duplication sources, RB topology and table occurrences must resolve to an
allocation record. Snapshot references resolve in that snapshot; standalone
read/handle events and address-reuse notices resolve in the retained registry
(the reuse notice precedes the new allocation's first snapshot). Snapshot node
arrays retain all previously seen identities, including tombstones, without
duplicate generations. Every identity requires its generation, script ID, creation
operation/ordinal, source and liveness fields. Standalone reuse, read, public-result
and stale-handle events require their own complete field sets; successful public
results require JSON, and reuse requires two nonzero allocation generations.
Five bootstrap allocations are required independently.
Creation identities must first appear in
their creation operation, remain stable per generation, and have unique,
contiguous positive ordinals within each operation (including bootstrap zero).
Duplication sources must be earlier allocations and match the requested source
handle; recursive children must match all corresponding source children,
including the source suffix. `internal-build` independently requires the four
parser children at `(operation, ordinal)` `(1,2)` through `(1,5)`, their schemas
and parent, plus the failed term allocation `(3,1)`, its tombstone, EVALID result
and free event. These expectations do not use raw generation numbers or surviving
references to infer the required allocations.
The recursive duplication boundary requires a distinct table at source capacity,
mode 1, used 0, an entirely free chain, and no children. Each relevant case
requires its fixed count of resize before/after records, paired by operation
and table identity, before occurrence continuity is checked. All present live
tables require their snapshot fields and exactly `used` occurrences; serials
cannot change table/generation/hash identity. Occurrence liveness agrees with
the snapshot registry, and tombstoned generations cannot become live again.
Occurrence, bucket, slot and RB records require all fields emitted by the driver;
bucket/slot counts equal capacity and free-chain length equals capacity minus
used. Diagnostic codes and log level/line fields are required when those records
exist; optional native message/path/app-tag fields remain optional.
After successful generic-table creation, every subsequent command snapshot and
snapshot stop boundary requires its table dump (a failed replacement constructor
has no table). Each such dump has an independent deletion control.
Live nodes require current metadata and links; inner nodes require child/table
snapshots. `lyds_tree` metadata requires its RB roots and complete topology.
Retired tables and tombstoned current metadata are deliberately unavailable.
Unobserved native `table.allocate` events have no table dump by driver design.

Automated in-memory negative controls zero all creation/source fields, substitute
the completed duplication snapshot for its initialization, and remove all resize
records. Additional controls zero each duplicate identity field independently,
substitute a wrong existing duplication source, change each duplication initial
capacity/mode/used/child field independently, drift an identity, remove duplication
initialization, remove either resize boundary, and move an after record before
its before record. `TestNativeObserverEvidence` requires these
controls to execute and reject their mutants; missing evidence cannot pass by
making the assertions vacuous.

On every instrumented replay (including rewind, invalid-constructor and watchdog
controls), additional negative controls delete all parser-child, internal,
failed-constructor, tombstone and bootstrap records wherever those categories
apply. Internal and failed allocations are also deleted individually, including
each operation's final ordinal. Independent `internal-build` controls remove
the expected creation keys directly, so reference validation cannot mask those
assertions. Every nonzero reference-field category receives an unresolved
generation mutant. Other controls delete required snapshot fields, one retained
identity, each required bootstrap container field, RB topology, table occurrences,
either lookup dump, separate public/read
results, each required event class and both boundaries of each reached primitive.
Each identity field and each required standalone-event field is also deleted
independently; reuse-generation zeroing has separate controls.
Record-field controls delete every occurrence, bucket, slot, RB, diagnostic and
log field independently; array controls remove all buckets, slots or free entries.
Fixed per-request counts independently require context diagnostics and callback
logs at completed-command/terminal snapshots, including logged errors with no
context diagnostic. Each nonempty channel has a whole-channel deletion control.
The recursive-dup control omits its entire child suffix. The Go regression
requires the general controls for all case scripts and the applicable allocation
controls for parser, recursive-dup and list-key creation. Unmodified comparison
traces lack constructor-hook provenance and are compared for public outcomes;
allocation-identity controls apply to instrumented traces.

* **remote-last.calls:** the exact public sequence from design §2.3 reaches the
  remote-last insert-after with every earlier public call returning success.
  P retains schema records for moved v3 before local v2. The primitive changes
  P's first child's tail link to new v4, links v4 after v3 in Q, and gives v4 Q's
  parent, while Q's first retains its stale singleton tail. Observation stops
  at that primitive. No synthetic table state establishes this reachability.
  `TestNativePublicRemotePrefix` independently runs the preceding public Go
  calls and confirms there is no earlier D-0121 refusal. It excludes the final
  insertion because G1 adds no production routing.
* **First-sibling termination control:** the separate, alarm-protected
  `lyd_first_sibling(v3)` public call **terminates** for this exact sequence.
  `lyd_first_sibling` immediately returns `parent->child` for these parented
  nodes (`tree_data_common.c:780`); it does not enter its predecessor loop.
  The corrupting write is established; the parentless predecessor-loop branch
  would require separate public reachability evidence. No pointer mutation is
  used to manufacture a cycle. `remote-last-results.calls` separately completes
  insertion and compares P/Q JSON and schema lookup through P (winner v3,
  current parent Q) with the unmodified library. `remote-last-release.calls`
  separately checks whole-root release of P/Q/R, leaving already detached v1
  and o as independent live allocations, then prints both. Both drivers must
  complete all three releases; the Go regression checks each release's exact
  liveness transition. The untouched driver observes actual frees through linker interposition and
  retires disposed tables' occurrences. It also reconciles removed/reused slots,
  rehashed records and generic-table replacement, while preserving
  retained generic/foreign tombstones. These concrete controls do not establish
  arbitrary unlink/free or traversal safety.
* **remote-nonlast.calls:** adding v5 in Q before creating v4 in P selects the
  remote nonlast predecessor and completes with valid links. Public JSON for
  both P and Q matches the untouched native control.
* **collision-build.calls:** the first threshold build stops at the first
  binary collision with helper EINT and keeps size8/used3. It does not register
  the QQ suffix. Later removal and growth/shrink retain that history. Validation's
  missing initial re-find returns EINT while the duplicate helper and enclosing
  validation succeed and clear reachable NEW flags. The LOGINT belongs to the
  logging callback with no context error item; the two channels are recorded
  separately. Later public JSON and validation outcomes match untouched native.
* **repeated-pointer.calls:** low-level repeated I is explicitly separate from
  public collision reachability. Before shrink, repeated registration of the
  same Qg pointer makes validation return EVALID with the duplicate data path.
  Checked shrink eliminates the repeated pointer/hash occurrence, and later
  validation succeeds. The native I helper's schema-record stop remains visible.
* **collision-resize.calls:** Qg leader plus the binary collision and QQ, sixteen
  temporary inserts/removes, then collision-node change to RA is an independent
  public growth/shrink and later-winner control. Schema reads and table snapshots
  retain the same Qg winner throughout. This public script does not by itself
  demonstrate a winner change; claiming one would overstate the evidence.
* **collision-history-retained/shrunk.calls:** after the independent public and
  repeated-I prefixes above, a labeled low-level I repeats the collision pointer.
  The retained control later selects that pointer at schema hash. Sixteen
  temporary inserts/removes trigger checked shrink in the other control; after
  removing Qg and changing the collision node to RA, schema lookup instead
  returns ENOTFOUND. This establishes the later selection difference caused by
  checked deduplication without calling the repeated I a public API sequence.
* **merge2-defined.calls:** destination without RB metadata and source with RB
  metadata enters native variant 2. The trace confirms front → metadata transfer
  → among → back. The completed public JSON matches untouched native.
* **merge2-successor.calls:** the trace reaches the wrong successor-schema
  boundary after front and metadata transfer and stops before the incompatible
  RB insertion. **merge2-before.calls:** all source nodes before destination
  reach front → metadata → among → back's no-successor return; observation stops
  before the subsequent uninitialized continuation read. Neither is a completed
  golden or a claim of exact native behavior beyond the safe prefix.
* **merge2-unsorted.calls:** public NoSort/NoMeta copies create D=[D4,D1]
  without RB metadata and detached S=[S2,S3] with RB metadata. The trace records
  native's source-only prefix and stops before the exhausted `_among` forward
  iterator is dereferenced. At the stop, complete RB topology contains
  D4,D1,S2,S3, separately from the metadata's older stored pointer. This is the
  native UB-prefix control. The Go regression controls for §4.1 separately
  verify D-0062's [D1,S2,S3,D4] permutation, metadata-before-replay publication,
  A=[S3,S2], absent-table threshold frontiers and existing-table growth without
  inventing destination registration events.
* **opaque-threshold.calls:** three schema children plus one opaque remain
  table-absent; the fourth schema child creates the table. **opaque-separated.calls**
  uses public placement of an opaque separator between same-schema children
  before first build and observes both run-start registrations.
  **opaque-eligibility.calls** covers top-level, opaque-node and opaque-parent
  I/U no-ops. Go assertions and live requests cover these independently.
* **resize-disabled.calls:** removing the only occurrence below 25% in size16
  mode0 and still-mode1 retains capacity and mode; Go/native primitive snapshots
  compare these controls exactly.
* **list-events.calls:** public list creation builds the first-instance schema
  registration, unlink promotes the successor, insertion before that successor
  replaces its registration, and a second unlink promotes it again. Four exact
  table snapshots and four lookup winners are checked independently.
* **schema-pointer.calls / schema-pointer-reverse.calls:** synthetic low-level
  frontiers `[p,q,ll(A),ll(B)]` with equal module/name but distinct native schema
  pointers build six occurrences in either pointer order. U of A leaves four
  occurrences without promoting B. Go controls also distinguish all three
  comparison sites: run start, existing I successor replacement and U promotion.
* **invalid-capacity.calls / invalid-mode.calls:** independent native constructor
  requests reject size 9 and mode 3 before a table allocation event. Separately,
  `TestNativeInvalidCapacityAndMode` uses a fresh bounded Go scope for each invalid
  capacity and all modes 3..255: only the scalar journal touch may be charged,
  with no initialized state or paid page allocation. The Go arithmetic domain
  also rejects sizes outside the design's resource limits; native rounds small
  powers of two up to eight. Go initialization accepts internal resize mode 2;
  native's public constructor accepts only modes 0/1. Sticky read-error
  continuation remains a separate `TestNativePageAddressDomain` control.

Arithmetic sensitivity controls independently remove the minimum-capacity,
maximum-capacity, power-of-two and mode guards. Each fails
`TestNativeInvalidCapacityAndMode`; size 9 cannot reuse an earlier refusal.
Removing list eligibility fails `TestNativeListRegistrationAndPromotion` with
four rather than five occurrences. Replacing each schema-pointer comparison
site independently with nil-safe module/name comparison fails
`TestNativeSchemaPointerFrontiers`: run start gives five rather than six,
existing I removes the distinct-schema successor, and U attempts an already
registered successor and returns EINT. Restoring the old `invariants` assertions
makes the automated evidence controls fail because removed resize records pass.

`retained-address-reuse.calls` leaves a generic occurrence alive across node
release and immediate allocator address reuse. Its old generation stays dead
and its occurrence serial remains distinct from the new live allocation.
`driver-lifetime.calls` observes recursive dup's present-empty table before any
child, table retirement, tombstones and actual allocator address reuse.
`internal-build.calls` exercises parser-created IDs through an aborted binary
build and a failed value constructor. `find-next-watchdog.calls` is a separate
low-level control: 128 real native find-next calls repeatedly return the same
pointer, and the driver labels the bounded nonprogress boundary.

The observer does not enable any production adapter. Main has selected a narrow
pre-write safety refusal for remote-last insertion; implementing that decision
belongs to the later producer adapter. Opaque-prefix copy/replacement hooks are installed;
producer-specific prefix controls belong to the later adapter evidence.

## Private Go kernel (6a1 / 6a2)

`data/native_table_kernel_test.go` implements lifecycle A only. Its occurrence
and bucket pages use fixed-depth radix directories, 8/16/32-cell small pages and
at most 64 cells thereafter. A shared owner is established before model forest
construction. Every edit touches scalar state and secures bounded page copies
before writes, using the existing cumulative hash allowance. Go failure or
panic restores old roots without allocation, polling or refunds of executed
work. Native semantic stops keep the successful prefix. The harness rejects
production journals/prepaid suffixes and foreign owners: G3 owns that bridge.

The port includes checked/unchecked FIFO insertion, earliest pair removal,
free-list reuse, checked arithmetic/refusal, strict native thresholds and resize
iteration order, checked deduplication, interleaved aborting first build, and
existing-table I/U. It clears inactive node references. It changes no production
layout, routing, public API, physical links, stored hashes, sorting, validation
or ownership. Read/value callback selection and find-next's Go port belong to G2
and later consumers; the native driver already observes them independently.

Live differential invocation after the auxiliary build:

```sh
DEV_PLATFORM=linux/amd64 ./dev bash -c 'YANG_NATIVE_REGISTRATION_DRIVER="$PWD/conformance/oracle/native_registration/.build/driver" go test -tags oracle ./data -run TestNative -count=1 -v' </dev/null
```

Three fixed seeds (`247`, `0x7be55cae`, `0x257`) and modes 0/1 compare 1,158
primitive snapshots exactly, including all bucket/slot chains and the free list.
Both collision build orders, low-level existing I/U, and all five history
scripts compare exact event states. No golden file is created. The auxiliary
driver tests opt in through the explicit environment variable so a clean normal
CI checkout does not require these separately prepared build-stage tools.

Sensitivity checks remove one rule at a time and restore the source afterward:
continuing a failed build fails `TestNativeBuildAbortsInterleavedCollision`;
unchecked shrink fails `TestNativeResizeIdentityAndBoundaries`; omitting restore
fails `TestNativeRefusalAndTouchRestore` at allowance 578; omitting directory-copy
charges fails `TestNativePageTouchAccounting` (2,871 versus 3,031 units on amd64).
The unmodified regressions pass. Exhaustive budget boundaries of a growing
insertion assert unchanged roots/scalars and live/free topology after refusal.
`TestNativeForeignFrontierRefusedBeforeWrite` covers both neighbors and I/U;
an isolated copy without the guard fails with a table write before refusal.
An independently built observer that assigns fresh serials during reinsertion
fails `TestNativeObserverEvidence` at its occurrence-identity invariant; the
native table algorithm is unchanged in this sensitivity control.
`TestNativeStickyBudgetPrecedesCancellation` fails without the sticky-error
precheck (cancellation wins and polls once); the fixed path preserves ErrBudget
without polling. These cancellation/quota controls are Go policy, while the
successful table transitions have the independent native replay evidence above.

`TestNativeOrdinaryWorkBaseline` runs against the actual `origin/main` during
the audit closure. The original G1 record used `ecb6fd1`, the design baseline. All twelve scenarios
match head exactly in visits, cumulative charges, allocated bytes and
allocations. Each allocation result takes the minimum of five samples. The
64/256 duplicate-heavy descending scenarios are:

| Children / journal | Visits | Hash/journal units | Allocations | Allocated bytes |
|---|---:|---:|---:|---:|
| 64 / no | 4,504 | 8,099 | 1,147 | 116,176 |
| 64 / yes | 4,504 | 8,649 | 1,186 | 345,128 |
| 256 / no | 26,608 | 42,844 | 4,207 | 444,112 |
| 256 / yes | 26,608 | 44,994 | 4,256 | 1,425,512 |

Empty-container and 1/2/3-child cases also match exactly. This proves ordinary
paths remain unaffected by A; it is not the later real-B cost gate.

## Original G1 check record

The original G1 run recorded the following checks; current revision results
are reported separately in the review request. All requested commands exited zero in one serialized run under the shared
heavy-check lock, through the canonical linux/amd64 dev container as its normal
user: `go test ./...`, `go test -race ./data`, `go vet ./...`,
`golangci-lint run ./data/... ./internal/...`, `bash -c scripts/check-registries`,
`make ci`, and `make test-oracle oracle-check`. CI's race, 386, minimum-Go,
security, registry and ordinary-oracle checks passed. Final private guard work
also passed the complete `TestNative` suite with the auxiliary driver enabled
and the requested lint command. No golden or policy file was modified.

There are no new manifest fixture IDs. Compatibility requests are the independent live scripts under `cases/`, plus the fixed-seed primitive replays.

## Replay-completion regression controls

The merge-gate sweep found empty comparisons in `remote-last-release`,
`resize-disabled`, `free-slot-cap`, `schema-pointer`, and
`schema-pointer-reverse`. Each now has an explicit public print probe. The
remote parented-rewind continuation also has an untouched-library comparison.
`invalid-capacity` and `invalid-mode` now use the same completion checker with
their expected rejection boundary and nonempty native log as evidence.
All 30 scripts, including intentionally stopped prefixes and lifetime-only
controls, require independently expected completed commands and terminal events.
The Go driver's shared helper enforces command/read completion for every Go
differential caller too. This closes the opaque-parent loop's empty-replay
acceptance and the list/schema-pointer checks' selected-prefix acceptance;
intentional tombstone stops require their explicit final event and operation.
`TestNativeDriverReplayCompletion` covers these helper rejection paths directly.

`TestNativeControlReplayRejections` runs nine focused comparator tests, including
equal truncations with nonempty results and a fully completed replay with empty
results. Against the pre-fix checker it fails eleven assertions; its valid
nonempty control passes. Every live comparison additionally rejects truncation,
an unexpected early stop, a missing command and empty results on either side
and both sides together. Direct empty-result validation has a separate mutant
so command validation cannot mask the empty-set assertion.
`TestNativeReleaseControlCompletion` fails against the pre-fix untouched driver
at operation 16. `TestNativeReleaseRetirementPaths` separately covers generic
replacement, single-child free, retained generic occurrences, and retained
foreign occurrences; the first two fail against that driver. These live
requests complement the fixed case scripts without introducing goldens.


## Audit closure controls

`TestNativeResourceRefusalsAreSticky` covers free-slot, used-domain and doubling
refusals, continuation and cancellation precedence. Its original-G1 sensitivity
run fails all three because `work.hashErr` remains nil.
`TestNativeFirstBuildChecksEveryFrontier` rejects each child's foreign-owned
predecessor/successor before initialization; its predecessor control fails on
original G1 with a successful table write. `TestNativeBuildAndShrinkRollback`
injects cancellation and panic throughout build (174 polls), shrink (291), and
a multi-page shrink (4,570 polls, sampled every 71). Frontier accounting at
64/256 children remains 4,305/17,200 visits and 21,237/58,864 charges, identical
to original G1. The ordinary-path comparison against actual `origin/main` also
matches all twelve scenarios, with allocations measured as the minimum of five
samples. Final command outcomes belong to the review request.

`TestNativeMerge2GoPolicyPermutation` observes the real Go producer through an
isolated compiler overlay: exact source matches add read-only calls before and
after each attachment and at initial frontier publication. Both table-presence
controls require metadata on D1 before any attachment, the frontiers
[D1,D4] → [D1,S3,D4] → [D1,S2,S3,D4], and arrival order S3,S2. The overlay
exists only in a temporary test directory and leaves tracked production files
unchanged. `TestNativeMerge2PolicyFrontiers` separately applies these frontiers
to lifecycle A, including opaque separators, and asserts first-build versus
existing-table occurrence history and the retained D4 winner.


## Comparison-driver lifetime closure (second merge-gate round)

The untouched static library still executes its original algorithms. Its driver
now links with `--wrap=free` to observe actual frees of known data nodes and
tables, including frees inside constructors and validation. This replaces the
explicit-release simulation. A bounded, visited live-storage walk registers
returned allocations, their descendants and siblings, and allocations added to
previously known live forests before any output-handle or snapshot binding.
The same allocation helper serves instrumented constructor hooks and untouched
boundary discovery: address reuse appends a generation and retains the old
identity, stale handles and occurrence references.

The identity/lifetime sweep covers these differences between builds:

| Instrumented observation | Untouched comparison observation | Evidence |
|---|---|---|
| Constructor hooks before linking | Live-boundary discovery before handle/snapshot binding, including bootstrap and implicit generic dup | `recreate-print`, `recreate-lookup`, `retained-same-hash` |
| Recursive dup, parser and list-key allocation hooks | Discovery of all surviving descendants, including schema keys | `dup-descendant`, `parse-descendant`, `list-key` |
| Implicit creation and node-retirement/free hooks | Discovery after validation and actual-free interposition, including default replacement | `validation-delete`; existing release retirement controls |
| Table allocation/retirement hooks | Known-table free observation and fresh table identity on address reuse | `table-reuse`; existing generic replacement control |
| Occurrence hooks carrying generations through resize | Preserve retained generation references across layout changes; retire the exact earliest removed generic occurrence before shrink; children-table continuity is explicitly unknown | `retained-resize`, `retained-remove` |
| Generation-bound script handles and retained records | Keep earlier handles and tombstones after replacement; never discover allocations through occurrence pointers | `stale-handle`, `retained-same-hash`, existing retained-foreign control |
| Constructor hooks register identities before primitive snapshots | Finish live-boundary discovery before emitting snapshots; table registration itself emits no partial-forest snapshot | All lifetime cases compare quiet and verbose untouched runs |

The eleven allocator-reuse request cases are in `cases/lifetime/` and run
through `TestNativeComparisonLifetimes` and the complete observer checker. Both
processes must emit actual address-reuse evidence with resolved old and new
generations. Descendant cases additionally require reuse between allocations
created internally by dup, parse or list-key construction. Each public comparison requires the full independently expected
command/result sequence and terminal event, equal statuses, diagnostics and
nonempty public results. Every case also compares verbose untouched observation
against the quiet instrumented replay. Negative controls delete reuse evidence,
zero replacement generations and independently
truncate, stop early, omit commands or empty the results on either side and on
both sides together. Intended tombstone boundaries have explicit operation and
boundary assertions; they cannot excuse an earlier stop.

The three requested controls fail against the original `2f3cdb13` drivers:
`recreate-print` stops at operation 6, `recreate-lookup` at operation 39, and
`dup-descendant` at operation 40. The fixed controls complete and require actual
reuse in each process. Independent sensitivity builds omitting descendant
discovery, resize-generation carry or exact removed-occurrence retirement fail
`dup-descendant`, `retained-resize` and `retained-remove`, respectively. In
particular, the resize mutant incorrectly completes a probe of a retained dead
allocation, while the removal mutant incorrectly stops a valid later print.
A further sensitivity build emits a table-allocation snapshot inside unfinished
discovery: quiet replay succeeds, but verbose `dup-descendant` stops at operation
10. The fixed driver completes both modes with equivalent public outcomes.
A control that observes frees only during explicit release requests fails
`validation-delete` with the same schema and requests, establishing the need
to observe internally triggered retirement too.

Constructor-hook provenance, allocations created and freed wholly inside one
public call without escaping, primitive-event schedules, dup source ordinals,
and trace-only occurrence serials remain instrumented evidence, not cross-process
comparison keys. The untouched driver observes surviving data allocations and
actual frees without inferring allocation identities from freed pointers.
Anydata payloads, foreign contexts and representations that the request protocol
does not construct are excluded. No production path, journal, public API, native
algorithm or golden changes. The observer keeps its existing identity/occurrence
limits, traversal cap and process watchdog. The ordinary Go work baseline was
rerun on actual `origin/main` (`18373297`) and head: all twelve
visit/charge/allocation/byte tuples above remain identical.


## Identity/lifetime sweep closure (F1–F8)

Allocation registration never binds a handle. `pending_script` records the
attempt only; all allocations made in the call can name that attempt without
claiming its output handle. After native return and complete discovery,
`bind_handle()` records the actual returned generation, binding revision and
operation. Snapshots retain the complete binding map, including aliases, null
refreshes and tombstoned targets. `id` is the first successful birth-operation
return label and cannot be overwritten by a later alias or refresh.
`output_handle`/`output_generation` independently record the returned pointer.

Reference lookup cannot invent allocations. The untouched observer traverses
parent, previous sibling, child and next sibling, bounded by the existing visited
set, 4096-node traversal cap and watchdog. This discovers copied ancestors,
ancestor keys, opaque children and tables before any live reference is serialized.
It never discovers through a retained occurrence pointer. Reused addresses append
a fresh generation; ordinary references continue resolving old tombstones.

`identity.py` validates every trace consumed by `check.py`, including quiet,
verbose, lifetime and comparison traces. The Go wrapper invokes the same validator
on the full raw JSON before decoding `nativeImage` or indexing slots, and retains
that raw evidence. Independent permanent bootstrap nodes and their required table, inner-node table
presence, registry counts, contiguous
positive generations/table identities, retained tombstones, immutable provenance,
unique allocation labels, complete bindings, returned-pointer agreement, live
completed-operation links, table ownership and occurrence references are required.
Read winners must resolve to live allocations at the read operation. Schema reads
record the native query hash and are checked against its ordered registration run;
first-sibling reads are checked against the recorded component. Normalization
assigns each surviving allocation an immutable `(birth operation, component
ordinal)` key, including internal nodes. Comparisons check every completed live
component and binding map as well as public results and reads. Null alone maps to
zero. Allocation labels, raw serials and process-local generations are not comparison
keys. RB membership uses generations, so equal script labels cannot collapse it.

The unchanged comparison library has two explicit assurance limits:

* Children-table snapshots say `continuity: unknown`. Same pointer/hash/slot and
  final capacity do not establish retention across a native call. Their serialized
  occurrence tokens preserve retained references for reconciliation and must not
  be interpreted as known lifetime serials. Instrumented hooks establish actual
  retirement/recreation; generic explicit operations retain their narrower
  `observed-generic` evidence. The checker rejects a comparison trace claiming
  observed children-table continuity.
* `allocation_scope: survivors-and-observed-frees` covers surviving allocations
  and subsequent actual frees of known allocations. It cannot establish temporary
  objects created and freed inside a call. Only `every-allocation`, emitted by the
  instrumented library, claims that coverage. Failed terms, partial parser trees
  and successful leading metadata each have required allocation/free pairs;
  deleting an entire pair fails that lane. The comparison does not claim it.

The inventory below revisits every row of the sweep. **H** is the instrumented
allocation hook; **D** is complete-component discovery in the untouched build;
**B** is the separate returned-pointer binding; **F** is native retirement/free
hooks (instrumented) or actual-free interposition for registered objects
(untouched); **S** is discovery before serialization; **R** appends identities on
reused node/table addresses; **T** retains tombstones, bindings and generation
references. **Transient** means H/F/T evidence only, excluded from the untouched
allocation assurance. “Existing” means the path moves or reads allocations already
registered; it must not allocate a replacement identity.

| Native path from the sweep | (a) Registration, instrumented / untouched | (b) Binding | (c) Free observation | (d) Survivors before serialization | (e) Address reuse | (f) Tombstones/references and controls |
|---|---|---|---|---|---|---|
| Bootstrap and `root` | H / D, five bootstrap nodes | B only returned root | F | S complete root component | R | T; bootstrap deletion mutants, recreate-print |
| `term`, RPC input/output | H / D; failures transient | B successful output; attempt separate | F; failed value storage H/F | S | R | T; failed-term control, remote-last RPC cases |
| `list` and keys | H / D list and keys | B list only | F; internal failed construction transient | S keys and tables | R | T; matrix shallow list, list-key reuse |
| `opaque`, including children | H / D via lyd_child | B returned opaque only | F; failures transient | S opaque first/tail and descendants | R | T; opaque-eligibility, matrix list non-key child |
| Shallow `dup` | H / D, keys-only shallow lists | B returned node | F | S | R | T; 32-mask container/list matrix, F7 old-assumption controls |
| Recursive `dup` | H / D all descendants and tables | B returned node | F | S | R | T; matrix, dup-descendant, dup.table.initialized |
| `dup WITH_PARENTS` | H / D ancestors, keys, descendants | B returned descendant, ancestors unhandled | F | S complete ancestry component | R required in quiet/verbose builds | T; ancestor-reuse and matrix |
| Duplication modifiers | H / D for masks 0–15, 64–79 | B, independent of allocation order | F | S | R | T; all 224 shape/mask controls, fixed primitive schedules |
| Existing-key duplicate return | Existing, zero allocations | B aliases; immutable birth labels | F through any alias | S existing component | No new generation on alias | T; repeated aliases, rewind and stale alias probe |
| Duplication error cleanup | H / D survivors; failed copies transient | No failed output binding | F incl raw free | S surviving components | R | T; existing error cleanup/lifetime controls; foreign-context branch excluded |
| JSON `parse` | H / D returned forest | B actual returned first data node | F; errors transient | S forest and ancestry | R | T; parse-descendant, internal-build |
| Leading JSON metadata temporary nodes | H only for wholly transient opaque nodes | B returned data; temporary nodes unhandled | H/F pair required | S returned forest | R for subsequently reused observed addresses | T instrumented; successful-metadata pair-deletion mutant |
| Parser errors, missing key/invalid value | H; D only surviving results | Null binding on failed parse | H/F partial forest required | S survivors even after error | R | T instrumented; partial-parser pair-deletion mutant |
| Validation implicit nodes/defaults | H / D even after error | B refreshed root, including null | F | S new defaults/NP containers | R | T; validation-delete and d-to-c root-refresh control |
| Validation autodeletion | Existing registered default identities | Retain other handles; B refreshed root | Actual F, no link-based inference | S survivors | R | T; validation-delete, stale-validation mutant |
| Single insert/unlink/move | Existing generations | Existing B | F only if native frees | S current components | No allocation on relink | T; before/attach continuity control; untouched continuity unknown |
| Sibling move/detach/sorted merge | Existing generations; RB nodes separate | Existing B | F | S surviving components | No replacement identity for moves | T; remote/merge cases; untouched occurrence continuity unknown |
| Value/key `change` | Existing node generation | Existing B | F for any actual release | S | No generation for value replacement | T; change reinsertion control; untouched occurrence continuity unknown |
| Synthetic `frontier` | Four existing allocations | Existing B | No free | S | No new generation | T; schema-pointer controls, synthetic reachability only |
| `free`, `free-root`, sibling/subtree destruction | Existing registered nodes/tables | Bindings retain generation | F; parented-key refusal makes no tombstone | Bounded known-link scan replaces D after free; unsafe native links terminate before traversal | R on later allocations | T; release controls, retained-links safe terminal evidence, stale aliases, retained foreign/generic cases |
| Children-table/generic/recursive-dup table creation | Native table H / explicit generic notice or D | Node handles independent of table IDs | F table and owned occurrences | S table registry | Fresh table identity on R | T table registry; table-reuse, zero/collapse-table mutants |
| `I`/`U`, generic insert/remove, lazy duplicates | Existing nodes; lazy dup H / D | B lazy returned duplicate | F | S | R | T; earliest generic removal, retained-remove; child continuity unknown |
| Table grow/shrink | Same ly_ht identity; storage replacement only | Existing B | Native F for backing arrays, not table retirement | S final layout | No fresh table identity on resize | T instrumented occurrence carry; retained-resize/remove; untouched continuity unknown |
| `schema`, `rewind`, `print` reads | Existing allocations | B refresh for rewind | No data-node free | S already-complete component | No allocation on read | T; winner old-incarnation/internal-node/duplicate-label mutants |
| Unreachable XML/anydata/raw/path/sibling-dup/raw-table-dup | Excluded from protocol | N/A | N/A | N/A | N/A | No claim; anydata traversal would need a payload-aware extension |

Per-finding controls and counterfactual evidence:

| Finding and root cause | Fix | Regression and rejected counterfactual |
|---|---|---|
| F1: first allocator consumed the requested handle | Register allocations and attempts independently; bind actual return | `test_f1_f7_flag_matrix`, leading metadata control; incidental-first-ancestor label mutant rejected |
| F2: discovery omitted ancestry and reused parents resolved to tombstones | Discover complete connected component before serialization; references cannot create identities | `test_f2_ancestor_reuse`, 32 cycles in both builds and modes require actual ancestor reuse; dead-parent mutant rejected |
| F3: aliases overwrote allocation labels; refreshes missing | Separate versioned bindings, immutable birth label, returned-pointer evidence | `test_f3_aliases_refresh_and_release`; stale rewind/validation and alias-label overwrite mutants rejected; release through alias then stale-alias probe |
| F4: equal final slots falsely implied continuity | Untouched children continuity explicitly unknown; instrumented hooks retain lifetime evidence | `test_f4_occurrence_continuity`; before/attach/change end at original capacity; instrumented serials retire/recreate; false observed-continuity mutant rejected |
| F5: handles/internal zero labels collapsed allocations | Injective per-operation component keys, complete binding history, independent read resolution | `test_f5_winners_and_f6_structural_mutants`; old incarnation, other internal child and duplicate label rejected on either side and both sides |
| F6: comparison could omit identities and Go discarded evidence | Common validator before comparison and Go projection; raw evidence retained | Same Python controls and `TestNativeIdentityEvidenceRejections`; complete classes, an internal survivor, zero/collapsed tables, occurrence IDs and tombstone links rejected |
| F7: unconditional recursion/ordinal-one assertions | Flag/branch-specific allocation multisets and child correspondence, including ancestor keys and existing-key aliases | 224 shape/mask requests with fixed before/after primitive schedules; correct shallow/parented copies explicitly fail the pre-fix assumptions |
| F8: boundary discovery cannot observe internal transient lifetimes | Narrow untouched assurance explicitly; every-allocation lane independently requires transient pairs | `test_f8_transient_scope`: failed term (1), partial parser (3), successful metadata (1) dead allocations; dropping each entire lifetime fails |

The Go entry points are `TestNativeIdentitySweep` and
`TestNativeIdentityEvidenceRejections`; `TestNativeControlReplayRejections` retains
all nine comparison-completion controls. `sweep_test.py` contains the independent
live request inputs and writes measured primitive schedules under ignored `.build/`.
No manifest fixture IDs or goldens are added. Fixed primitive seeds remain
247, 0x7be55cae and 0x257, in both resize modes.

Native algorithms, instrumentation sites and the hook interface are unchanged.
Changed observer paths are allocation/reference lookup, boundary component
discovery, bounded post-free retirement checks, safe terminal serialization,
returned bindings and schema-read query observation;
checker paths are common structural validation, normalization and duplication
expectations. XML, anydata/anyxml payloads, raw/path constructors, direct sibling
or raw-table duplication, foreign-context duplication, NO_EXT extension data and
WITH_PRIV private pointers remain outside the reachable protocol. No production
Go mutation path, ownership, cache, journal or resource budget is changed. All
C observer allocations/visits retain the existing caps and watchdog. The unchanged
ordinary-work test was run against actual origin/main 18373297 and head: all twelve
visit/charge/allocation/byte tuples were identical, including 256-node adverse
insertion order and duplicate-heavy input, with and without a journal.

Counterfactual execution rebuilt the reviewed 83f3aee3 driver source against the
same pinned objects in both modes. The parents and metadata F1 controls, the F2
128-cycle list-ancestor reuse control, the F3 existing-key alias control and the
F4 continuity-assurance control all failed on that source and passed on the fixed
driver. The pre-fix checker accepted a stale-incarnation schema winner and a
comparison trace with every node record removed; the new checker rejected each
mutation independently on either side and both sides. F7's old recursive-copy and
ordinal-one assertions are executed as expected failures on the correct matrix
results. F8 removes each entire transient allocation/free lifetime and requires
rejection from the every-allocation evidence lane.

The final F6 table-class deletion control first failed with “accepted malformed
identity evidence” when both the registry and every table snapshot were removed.
The validator now independently requires the permanent bootstrap table and table
presence fields for every live inner node. The same control is registered in
both the Go wrapper and the either-side/both-sides Python mutation lane.

The F7 flag matrix also includes 64 key cases: returning an existing key in a
supplied parent, copying a list ancestor into a supplied higher container, and
copying a detached key with WITH_PARENTS (which leaves its native local parent
null even when a parent argument was supplied). Before the final checker fix,
all 32 WITH_PARENTS cases failed: a higher matching supplied ancestor overwrote
the immediate copied parent, and detached keys incorrectly reused the supplied
parent. The checker now follows lyd_dup_get_local_parent's immediate-parent
selection, requires the returned copied key to belong to that copied parent,
and uses the native LYS_LIST kind and contiguous key prefix for shallow copies.
All 64 cases have independent allocation/return assertions and fixed primitive
counts, in addition to the original 160 scenarios.

The first full driver-enabled run exposed the retained-foreign release edge:
instrumented evidence contained a completed live tail reference to a freed node,
and untouched discovery followed that pointer and crashed. Explicit destruction
now checks known retirement before any discovery. It creates no data nodes or
ly_ht objects, so this bounded check replaces post-free discovery rather than
adding an uncharged traversal. The untouched cap remains 4096 live visits; the
instrumented registry keeps its existing LIMIT and watchdog bounds. A retired
physical link or owned table ends at `boundary.retained-dangling-links`.

At that terminal boundary, bootstrap state remains available, every allocation
and binding remains recorded, script-created live allocations have explicit
`current: unsafe-native-links`, and every still-live table has a safe storage
snapshot in its registry entry. Table records retain references to freed node
generations. The observer does not dereference unsafe data links or claim a
completed usable tree. Ordinary completed-operation links still must be live.
`cases/lifetime/retained-links.calls` supplies a public result before the release; quiet and
verbose runs in both builds must reach the exact boundary and retain the dead
foreign occurrence. Missing table storage and relabeling the boundary as a
completed free are rejected. Existing normal-release, alias, reuse and
free-refused-key semantics retain their ordinary boundaries.


### Final verification outcomes

Changes are left uncommitted. No production source, policy file, dependency,
public API or golden was changed. No manifest fixtures were added; the new live
request is `lifetime/retained-links`, with the generated 224-case flag matrix in
`sweep_test.py`.

| Command/check | Outcome |
|---|---|
| `./dev make ci </dev/null` under the heavy lock | Exit 0; run once |
| `DEV_PLATFORM=linux/amd64 ./dev make test-oracle oracle-check </dev/null` under the lock | Exit 0; run once |
| Driver-enabled `go test -tags oracle ./data -run '^TestNative' -count=1 -v` under the lock | Exit 1, zero skips: retained-foreign release failed in both builds; all other selected tests passed |
| After fixing that release: driver-enabled `go test -tags oracle ./data -run 'TestNative(Release|Identity|ComparisonLifetimes|ControlReplay)' -count=1 -v` | Exit 0; 31.373s; no skips |
| After integrating the terminal case: driver-enabled `go test -tags oracle ./data -run '^TestNativeObserverEvidence$' -count=1 -v` under the lock | Exit 0; 125.291s; no skips |
| `sweep_test.py ... IdentitySweep.test_retained_links_stop_before_discovery` after the fixture move | Exit 0; both builds, quiet and verbose |
| `./dev make fmt-check registries sensitive </dev/null` | Formatting and registries passed; history scan initially lacked a Git blob |
| `make secrets sensitive` in dev with cached objects supplied through `GIT_ALTERNATE_OBJECT_DIRECTORIES` | Exit 0; 646 commits scanned, no leaks or generic-sensitive findings; cached objects supplied without changing Git metadata |
| `golangci-lint run --build-tags oracle --new-from-rev HEAD ./data/...` | Exit 0; zero new findings |
| Optional whole oracle-tag lint | 12 pre-existing findings; no new findings after the focused oracle-tag lint above |
| Ordinary counted-work test on origin/main 18373297 and head | Exit 0 on each; all twelve tuples in the work table above identical |
| `git diff --check` | Exit 0 |

The full driver-enabled suite was not repeated after correction, respecting the
once-only full-run limit. Its failing paths and every affected observer/lifetime
lane were rerun successfully as focused tests. A fresh aggregate full-suite result
on the final worktree remains for the lead if required by the gate. The comparison
assurance limits for transient allocations and occurrence continuity are explicit;
no requested identity/lifetime code fix remains open.
