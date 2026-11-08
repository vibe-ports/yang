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
Both replay entry points accept `--driver` and `--comparison-driver`, with
`YANG_NATIVE_REGISTRATION_DRIVER` and `YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER`
as defaults. Only an unset/empty comparison setting falls back to
`driver-unmodified` beside the selected instrumented executable. Explicit
arguments take precedence over the environment. The replay check verifies their
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
Other requests retain the original schema, including their
allocator address-reuse setup. `frontier PARENT A B C D` is a **synthetic low-level**
four-child frontier with real native allocations and untouched schema pointers;
it provides no evidence of public mixed-schema reachability. Both schema modes
emit stable schema-pointer identities; the event schema also emits module names.
Every snapshot retains an injective compiled-schema registry with context,
module, name, node type and compiled-parent identity, including schema-only
ancestors. Live allocation generations keep their original schema binding.
Cross-build state compares these descriptors and their ancestry independently
of process-local schema numbering, in both schema modes.
`instance START TARGET` calls `lyd_find_sibling_first` with the supplied start
(zero means NULL), and `opaq-find START NAME` calls `lyd_find_sibling_opaq_next`.
Read evidence binds START, TARGET (for instance/schema/NULL probes), queried
parent and opaque NAME to the preceding completed handle bindings and request.
`null-reads PARENT TARGET` requires an empty parent and probes the schema,
instance and value APIs with NULL, retaining snapshots of the parent's table.
Its invalid binary value checks that value preparation is bypassed. These are
finite dispatch controls, not a general G2 comparison model. During `instance`
only, the instrumented build emits read-only `read.compare` operand/option
observations from `lyd_compare_single`; the comparison build remains untouched.
The finite order and duplicate controls pin each comparison's operation,
ordered operands and exact option bits. Duplicate probes require handle pairs
`(11,4), (11,5), (11,5), (11,4)` with `LYD_COMPARE_FULL_RECURSION` (`1`).
The checker and sweep reject swapped operands, wrong flags and wrong operations.

The G1 entry audit inventories all sibling-read consumers: the driver's
instance, opaque, schema, NULL and first-sibling emitters; `identity.validate`;
the checker's invariants, reference/deletion controls and cross-build comparison;
the sweep's quiet/verbose pairs and finite dispatch assertions; and the Go
wrapper's full-evidence validation. `TestNativeEntryAuditRejections` runs the
live request cases with one-sided and equal trace corruption for request
binding and schema collapse, split and drift. An internally consistent change
to a schema-only ancestor must also fail cross-build state comparison.
These changes observe `lyd_find_sibling_first`, `lyd_find_sibling_opaq_next`,
`lyd_find_sibling_val`, `lyd_find_sibling_schema`, `lyd_first_sibling` and
`lyd_compare_single`; they change no native algorithms or data mutation paths.
Foreign-context equivalence, non-NULL typed-value dispatch, nested selection
and instrumented verbose generic retained-lifetime coverage remain excluded.
Allocator reservation, constructor address checks and tcache setup are unchanged.

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

## PR #350 merge-gate corrections (G1)

The null-entry finding came from selecting retained registrations before
checking whether the caller supplied any sibling. `identity.validate` now
checks `parent.first` before schema preparation or table selection, requires
winner/current-parent zero and LY_ENOTFOUND (5), and independently verifies
that the full before/after table image equals the preceding parent snapshot.
Non-null schema reads also require a status consistent with their winner.
`TestNativeRetainedReadEntries` and
`IdentitySweep.test_retained_null_and_opaque_entries` replay the exact gate
sequence and its one-opaque-child variant in both builds; the sweep runs quiet
and verbose. Restoring HEAD's original `identity.py` made the Go null case fail
in both builds with `wrong schema winner`, while both opaque controls passed.
Winner/status substitutions and erasing both retained read images are rejected.
The null control additionally calls all three native read APIs with non-null
output sentinels, requires each to clear its output and return ENOTFOUND, and
supplies invalid binary text to the value API. It checks that no target
allocation occurred and that the complete registration image survived.

The validator inventory found a related same-context error in the default
schema mode: the fallback to a schema name conflated `c/v` and RPC-input `v`.
Both modes now emit and require schema-pointer identities. The new
`entry-schema-lineage` control checks misses in both directions, before and
after table creation, in both modes/builds. `TestNativeSchemaReadLineage` fails
with `wrong schema winner` when matching is mutated back to names alone.
There is one schema selection model, in `identity.py`; `check.py`, the sweep,
and every Go differential caller use it. The private Go kernel's `find` is a
modification-callback primitive, not a sibling read/expectation adapter.

The driver-path finding came from deriving the comparison executable inside
`sweep_test.execute`, discarding the caller's configured path. Both Python
entry points now accept explicit driver arguments, use the two corresponding
environment variables as defaults, and use the sibling comparison path only
when its setting is unset or empty. The Go sweep passes both paths explicitly.
`TestNativeSeparateDriverPaths` copies the executables into separate temporary
directories (symlinks would resolve back to the build directory), places an
exit-86 trap at the unwanted sibling path, and checks explicit arguments over
a poisoned environment, environment-only selection, and a missing configured
comparison. Both entry points pass. Restoring only the old sibling-selection
line makes the sweep's argument and environment subtests fail with exit 86.
All temporary mutants were restored; none is a checked-in alternative driver.

### Section 2.1 control inventory

| Rule | Live control and independent assertion |
|---|---|
| NULL before table selection or value preparation; history retained | `retained-null`: exact p/q/ll/ll + I(p) + four unlinks; schema miss, then schema/instance/value NULL reads; one residual p occurrence unchanged |
| Non-null opaque sibling permits retained-table selection | `retained-opaque`: identical retained p occurrence, one opaque child; detached p wins with current parent zero |
| Instance data-parent guard before rewind, only when both nodes are schema-bound; no such guard on schema reads | `entry-guard`: synthetic mixed c/ll and d/ll frontier; both instance directions miss before any comparison, with and without a table; querying d/ll schema from a c/ll start still succeeds. `entry-opaque` supplies an opaque start and still finds the schema-bound instance |
| Table target-first, linear candidate-first | `entry-order`: both lookup directions in both branches, six exact observed operand pairs `(4,11), (11,4), (11,4), (11,4), (4,11), (14,5)` with options zero; reversing the operand trace fails |
| Same-context schema pointer/lineage, not module/name alone | `entry-schema-lineage`: c/v versus RPC-input v; four misses spanning both directions and table states; both schema modes |
| Linear schema stops at first opaque; linear instance scans across opaque | `entry-opaque`: schema misses while instance reaches the linked leaf-list beyond the separator |
| Opaque supplied start scans forward; schema start uses trailing opaque block or misses on schema tail | `entry-opaque`: six opaque-name probes check start-relative selection, trailing-block selection, and schema-tail miss despite an earlier opaque node |
| Duplicate-instance continuation uses current next, full recursion, and stops at separator | `entry-duplicate`: unequal keyless-list descendants skip the first schema winner and select the second; reverse linear read succeeds; adding an opaque separator causes a miss; observed comparisons use full recursion |

Exact finite-control winners/statuses are asserted independently on each build,
in addition to normalized control equality. They are not a general G2 instance
comparison model. Keyless-list nodes require the same inner-node table evidence;
deleting their table snapshots is rejected on either build. New read-result kinds participate in replay completeness in
both Python consumers and the Go wrapper. The instrumented comparison hook
observes operands only during explicit `instance` commands and never changes a
comparison result. The unmodified build has no such hook. All commands retain
the existing tombstone boundary, process watchdog, identity/table caps and
bounded snapshot traversals.

The native functions exercised are `lyd_find_sibling_schema`,
`lyd_find_sibling_first`, `lyd_find_sibling_val`,
`lyd_find_sibling_opaq_next`, and `lyd_compare_single` (observation only).
The read corrections affect read controls, event emission, and schema identity
metadata; the continuation below also adds explicit test allocator setup.
No registration or production mutation path changed; I/U, physical unlink,
resize, validation, native allocation/free, #257 journaling, and existing work
budgets retain their library implementations. No Go ported function,
public API, dependency, policy file, or golden changed. Cross-context schema
equivalence, non-null typed-value construction, nested comparison selection,
and production G2 read adapters remain excluded from these finite controls;
the driver creates only one schema context. Existing remote-winner and lifetime
controls remain part of the full native suite.

### Main versus head counted work

`TestNativeOrdinaryWorkBaseline` was run unchanged on an archive of
origin/main `18373297ea975722f056b42837562b02e0ffe539` and on the working head.
All twelve tuples `(visits, charges, allocations, bytes)` were identical.
The input uses reverse insertion and duplicate-heavy values, with and without
the journal. No production hot path is changed by this correction.

| Children | Both revisions, no journal | Both revisions, journal |
|---:|---|---|
| 0 | (1, 0, 16, 2248) | (1, 11, 36, 12384) |
| 1 | (8, 17, 37, 4152) | (8, 42, 58, 18256) |
| 2 | (38, 155, 65, 6392) | (38, 187, 86, 20496) |
| 3 | (53, 208, 77, 7504) | (53, 247, 98, 21624) |
| 64 | (4504, 8099, 1147, 116176) | (4504, 8649, 1186, 345128) |
| 256 | (26608, 42844, 4207, 444112) | (26608, 44994, 4256, 1425512) |

### Deterministic stale-handle replay (gate-5 continuation)

The first full native rerun exposed allocator dependence in the old
`handle-tombstone` replays. Adding default-mode schema identities changed the
cJSON allocation pattern: the instrumented verbose replay recycled generation
10, then its replacements, while the stale handle still named generation 11.
Quiet observation and the comparison build recycled generation 11. A native
counterfactual with allocator setup disabled, changing only schema identities
back to their old conditional emission, restores generation-11 reuse in the
verbose build. Duplicate/ancestor discovery is unchanged in that counterfactual.
The identities and tombstone were correct; the replay assumed allocator order.

All three stale-handle requests now force actual reuse: `handle-tombstone`,
`handle-tombstone-probe`, and `lifetime/stale-handle`. The optional second operand
in `free 2 14` arms reservation of handle 14's term chunk. Both drivers interpose
`free`, call the real allocator first, immediately recover that exact chunk with
bounded same-size `malloc` probes, and hold it across subsequent trace output.
Nonmatching probes remain allocated until the forced construction finishes.
No retired object is dereferenced. An unexpected second native free fails.

The optional last operand in `term 104 1 p P 0 14` requests that reservation.
The runners set `GLIBC_TUNABLES=glibc.malloc.tcache_count=7` before startup.
Setup fills and drains the seven-entry bin, then returns the reserved chunk to
the real allocator immediately before the native `lyd_create_term` calloc.
The constructor must return the exact reserved address; failure to reserve,
allocate, or reuse fails the replay. The tested canonical container uses glibc
2.41, whose calloc consumes tcache. This finite allocator control is specific
to that environment; an incompatible allocator fails rather than skipping or
accepting missing evidence. No malloc/calloc implementation is substituted.

The normal identity observer independently emits the real `11 -> 15` address
reuse, the completed constructor binds live handle 104 to generation 15, and
the final probe still rejects handle 14's generation 11. The existing checker
assertion is unchanged. `TestNativeTombstoneAddressReuse` checks every request
in both builds with quiet and verbose observation, validates complete identity
evidence and command sequences, and requires a live replacement at handle 104.
A mutant bypassing reservation and preparation fails precisely the three
instrumented verbose lanes; the other nine lanes pass. It reproduces the
reported failure without weakening schema identity validation.

The changed native call paths are the test driver's release wrapper and its
explicit free/term replay setup, exercising `lyd_free_tree`/`lyd_free_all`,
`lyd_free_subtree`, `lyd_new_term`, and `lyd_create_term`. The pinned library
algorithms and the unmodified comparison objects are unchanged. Auxiliary
storage and recovery probes stay within the existing LIMIT cap and watchdog;
all allocator failures are checked. This adds no Go tree mutation, journal
operation, work-budget change, production hot path, public API, dependency,
policy change, or golden. Only the seven fixed requests listed in this section
use the controlled allocator configuration. No additional random seed is needed.

The first focused observer rerun then exposed the same assumption in
`retained-address-reuse`, which the original failure had prevented the checker
from reaching. The reuse inventory covers every positive assertion in
`check.py`, `sweep_test.py`, and their Go callers. All four generic retained
generation controls now use the same setup: `retained-address-reuse`,
`lifetime/retained-same-hash`, `lifetime/retained-resize`, and
`lifetime/retained-remove`. They reserve with `free 1 1` and force the lazy
term duplicate with the extra operand in `insert 1 <hash> 0 1`.
The actual `lyd_dup_single`/`lyd_dup_r` calloc must return the reserved address.
Preparation and result checking are shared with the explicit term constructor.

`TestNativeRetainedAddressReuse` requires completed quiet and verbose replays
in both builds, two distinct occurrences, the dead original generation and
live replacement generation, and the original occurrence serial unchanged.
The original checker assertions are shared with this focused entry point.
Bypassing the allocator setup makes this Go regression fail as well. All eleven
`TestNativeComparisonLifetimes` controls pass after the family correction.

Explicit exclusions from forced allocation are `driver-lifetime`'s repeated
subtree-release replay; the other seven lifetime controls (`recreate-print`,
`recreate-lookup`, `dup-descendant`, `parse-descendant`, `list-key`, `table-reuse`,
`validation-delete`); and the sweep's 32-cycle ancestor reuse control. These
require observed reuse across repeated cycles instead of a particular released
script handle or retained generic pointer. Their existing positive and negative
reuse assertions remain enabled. The comparison lifetime and identity sweeps
exercise them in both builds; no assertion is removed or relaxed.

Once the native checker could finish, its Go caller exposed a second integration
omission: the seven new entry-rule requests were not included in the checker's
per-request deletion matrix. `TestNativeObserverEvidence` already requires
these controls for every top-level request and failed with the missing labels.
All seven now run through native primitive/lifetime/resize counts, identity and
metadata deletions, unresolved-reference mutants, and two-build comparisons.
The new read event shapes are checked before indexing the request by operation;
the empty-result mutant removes instance, opaque, and null reads as well.
Keyless inner nodes also require their child/table snapshot in this checker.
The Go requirement is unchanged. The focused seven-request run rejects every
applicable mutant and completes both-build comparisons successfully.

### Correction check outcomes

**FULL CHECKS PENDING.** `flock -w 1800 -E 75` exhausted the 30-minute wait
without acquiring the heavy lock (exit 75). No full suite started. The lead must
run the three full checks below under the lock. All dev invocations used the
normal dev user and `</dev/null`; no sudo/root container execution was used.

| Dev-container command/check | Outcome |
|---|---|
| `bash conformance/oracle/native_registration/build.sh` | Exit 0; both canonical static drivers rebuilt |
| Driver-enabled `go test -tags oracle ./data -run 'TestNative(RetainedReadEntries\|SeparateDriverPaths\|SchemaReadLineage\|IdentitySweep\|ControlReplayRejections\|ReplayCompletion)' -count=1 -v` | Exit 0; 29.204s; no skips |
| Final driver-enabled `go test -tags oracle ./data -run '^TestNativeIdentitySweep$' -count=1 -v` | Exit 0; 20.310s; all 11 Python sweep tests passed; no skips |
| `go test -tags oracle ./data -run '^TestNative(ControlReplayRejections\|DriverReplayCompletion)$' -count=1 -v` | Exit 0; 0.198s |
| `go test ./data -run '^TestNativeOrdinaryWorkBaseline$' -count=1 -v` on main and head | Exit 0 on each; all twelve tuples identical |
| `golangci-lint run --build-tags oracle --new-from-rev HEAD ./data/...` | Exit 0; zero issues |
| `git diff --check` | Exit 0 |
| `./dev make ci </dev/null` under the lock | NOT RUN: lock wait expired |
| `DEV_PLATFORM=linux/amd64 ./dev make test-oracle oracle-check </dev/null` under the lock | NOT RUN: lock wait expired |
| Driver-enabled `go test -tags oracle ./data -run '^TestNative' -count=1 -v` under the lock, requiring no skips | NOT RUN: lock wait expired |

Expected failure runs: original HEAD `identity.py` with
`TestNativeRetainedReadEntries` exited 1 (both null cases failed, both opaque
cases passed); sibling-path-selection mutant with `TestNativeSeparateDriverPaths`
exited 1 (argument and environment lanes hit the exit-86 trap); schema-name-only
matching mutant with `TestNativeSchemaReadLineage` exited 1 (`wrong schema
winner`). All were restored before the final passing sweep. Its trace mutants
also reject wrong winners/statuses, erased retained images, reversed operands,
and missing keyless-inner table evidence.

No new random seed is needed; these seven request cases are fixed replays.
Existing primitive seeds remain 247, 0x7be55cae, 0x257. Changes remain
uncommitted. The full checks are the only remaining verification work.

New manifest fixture ids: none. New live cases: `retained-null`,
`retained-opaque`, `entry-guard`, `entry-order`, `entry-opaque`,
`entry-duplicate`, `entry-schema-lineage`.


## G2 part 1: complete native read evidence and normalization

Based on `a10a82a`, this extraction establishes native evidence before later Go
kernels consume it. Run read requests with `--quiet --read-schema` on both drivers.
Require the ready snapshot, every completed command, exactly one separate result
per read/equality request, and the exact terminal command. G1's inventory remains active.

New protocol commands (covered by retained requests or `TestNativeG2ProtocolSmoke`):

- `context [YANG MODULE]`, `context-keyless`: retain independent contexts.
- `child ID PARENT INDEX`: bind a child alias without changing its allocation label.
- `parse-xml ID XML`, `opaque-invalid-xml ID PARENT`: construct XML operands.
- `dup-to-context ID SOURCE PARENT OPTIONS`: duplicate into the current context.
- `read-schema START TARGET PARENT`, `read-instance START TARGET PARENT`, `read-value START TARGET PARENT VALUE`, `read-opaque START TARGET PARENT NAME`: native selection; START zero supplies null; PARENT identifies observed storage.
- `equal A B OPTIONS`: native `lyd_compare_single`, observing both operand generations.
- `record PARENT NODE HASH CHECK`, `record-as PARENT NODE HASH_SOURCE CHECK`: synthetic occurrence insertion, with no public-producer claim.
- `list ID PARENT NAME ~`: supply an empty key.

Generations resolve injectively through completed-operation component keys and
versioned bindings; reused handles retain distinct incarnations and aliases preserve
birth labels. Context collection and command-boundary cleanup cover every retained
context in creation order, within the existing 16-slot cap, including old operands.

Snapshot obligations derive from bootstrap/completed-command identity and the request,
before field discovery. Missing nodes, tables, schemas or bindings fail on either or
both peers. The audit's `root; dump; equal` reproducer also rejects missing dump nodes
with differing statuses. Shared `nativeCompletedCommandEvidence` retains normalized
state, exact statuses and both diagnostic channels for validation-only requests;
`nativeG2Evidence` additionally requires and normalizes selection/equality results.

Live state includes current node hashes separately from immutable occurrence hashes;
they may differ. Equality resolves live operand bindings, context, kind, flags, links
and inner storage. Selection retains winner/current-parent/next identities, layout,
slots, bucket order, recorded hashes and free chains. Only process-local observer
serials and embedded C source locations are normalized away; messages, code/path/app-tag,
diagnostic context and log fields remain evidence.

Live requests: `read-empty-prefix`, `read-equality-incarnations`, `read-retained-diagnostics`;
inline requests cover audit reproducers, populated equality storage, command-only
projection and protocol smoke. Empty-prefix requests here test evidence, not the later
Go prefix implementation. No manifest fixture IDs or golden changes.
`g2_mutants.py` uses compiler overlays and copied observer binaries; build errors or
missing tests cannot count as rejection. Its equality shortcut needs no historical ref.

Ordinary work on archived `a10a82a` and this worktree matches in all twelve unchanged
`TestNativeOrdinaryWorkBaseline` cases. At 64 children: `(4504,8099,1147,116176)` without
a journal, `(4504,8649,1186,345128)` with one; at 256: `(26608,42844,4207,444112)` and
`(26608,44994,4256,1425512)` (visits, charges, allocations, bytes). No production Go
hot path, mutation/journal ownership, routing, layout, public API or dependency changes.

Deferred: prefix snapshots/scanning (part 2); schema/opaque selection, actual cursors
and native input/output/type/revision discrimination (part 3); instance/value selection,
recursive comparison, typed-target/separator/option discrimination (part 4); validation
duplicate reads, exemptions and audit-3 finding 3 (part 5).

Targeted G2/completion/common-projection Go tests passed (11.564s); Python checker
passed 12 tests. Both native drivers rebuilt. Base/head ordinary-work tests, registry
checks, oracle-tag vet and lint for new issues passed. Sensitivity controls rejected
eight defects; two check removals remain independently protected by shared validation.

Locked amd64 results (normal dev user; each prescribed full command invoked once):
- `build.sh`: exit 0; `make ci`: exit 2; `make test-oracle oracle-check`: exit 2.
- Both make commands stop at F2 ancestor-reuse with `TZ=UTC`: base and head each have zero ancestor reuses in 5/5 trials; without TZ both have one reuse in 5/5 trials. This inherited control remains open; neither named #351 flake occurred.
- `go test -tags oracle ./data -run '^TestNative' -count=1 -timeout=30m -v`: exit 0, 57 top-level passes, zero skips; `check.py`: exit 0; `g2_mutants.py`: exit 0.
- Previously unrun conformance oracle tests, `make test-gates vuln secrets sensitive registries`, and `make oracle-check` ran separately under the lock: all exit 0. Goldens match.
- Isolated identity sweep: pass without TZ (29.507s), fail with UTC (26.898s); TZ-only reproduces the base/head failure, while `YANG_ORACLE_REQUIRED=1` alone does not.

## Deterministic reuse continuation

This continuation resolves the inherited F2 blocker recorded above. Under UTC,
startup/trace allocation activity could keep every ancestor address out of the
next 32 duplications. More repetition did not force the required native path.
The address-reuse assertion and all structural/mutant assertions remain active.

The same bounded allocator setup used by the seven G1 exact-address requests
now accepts inner nodes as well as terms. `# reuse-free HANDLE PATH` selects a
live allocation through `self`, `parent`, or `child` (first child).
The real native free runs first; its wrapper immediately reacquires that exact
chunk and holds it across observer output. `# reuse-next PATH` drains the same
seven-entry glibc bin, releases the reservation immediately before the next
supported public constructor/validation call, and requires the node returned
through PATH to have exactly that address. These test-setup directives create
no aliases, identities, snapshots, or command ordinals. Native calloc/free and
all pinned library objects remain unchanged. EOF with unused setup fails.

`lyd_dup_single(... WITH_PARENTS ...)` creates the list ancestor/key before the
top container. F2 therefore reserves the actual unhandled list parent from the
first duplicate and requires the next duplicate's list parent to reuse it.
The other 31 cycles and the original positive ancestor-reuse assertion remain.

The complete positive-address-reuse inventory is:

| Control | Forced allocation and next native path | Status |
|---|---|---|
| `IdentitySweep.test_f2_ancestor_reuse` | Unhandled list parent → `lyd_dup_single` list parent | Added |
| `driver-lifetime` | Handle 100 term → handle 101 `lyd_new_term` | Added |
| `lifetime/recreate-print` | Handle 11 term → next `lyd_new_term` | Added |
| `lifetime/recreate-lookup` | Handle 11 term → next `lyd_new_term` | Added |
| `lifetime/dup-descendant` | First internal child → next `lyd_dup_single` first child | Added |
| `lifetime/parse-descendant` | First internal child → next `lyd_parse_data_mem` first child | Added |
| `lifetime/list-key` | Internal key → next `lyd_new_list` key | Added |
| `lifetime/table-reuse` | Root container → next `lyd_new_inner` root | Added |
| `lifetime/validation-delete` | Internal default removed by explicit term creation → next `lyd_validate_all` default | Added |
| `handle-tombstone` | Handle 14 term → handle 104 term | Already forced in G1 |
| `handle-tombstone-probe` | Handle 14 term → handle 104 term | Already forced in G1 |
| `retained-address-reuse` | Generic handle 1 term → replacement handle 1 term | Already forced in G1 |
| `lifetime/stale-handle` | Handle 14 term → handle 104 term | Already forced in G1 |
| `lifetime/retained-same-hash` | Generic handle 1 term → replacement handle 1 term | Already forced in G1 |
| `lifetime/retained-resize` | Generic handle 1 term → replacement handle 1 term | Already forced in G1 |
| `lifetime/retained-remove` | Generic handle 1 term → replacement handle 1 term | Already forced in G1 |

Go consumers are `TestNativeIdentitySweep`, `TestNativeObserverEvidence`,
`TestNativeTombstoneAddressReuse`, `TestNativeRetainedAddressReuse`, and
`TestNativeComparisonLifetimes`. Their trace-corruption/completion controls use
the same requests. The other `native_table_*_test.go` controls assert modeled
slots/free chains, identity continuity, allocation counts, or synthetic pointer
keys; they do not require Go allocator address reuse. `table-reuse` requires
observed node reuse and sixteen distinct live table generations, not repeated
physical table addresses. Table identity/retirement mutants remain active.
The F5 winner sweep and `TestNativeIdentityEvidenceRejections` recreate a handle
with a new generation; their stale-generation mutants require no physical address
reuse. There are no other positive allocator-dependent requirements in this inventory.

`TestNativeReuseEnvironmentMatrix` runs `reuse_test.py`: all sixteen controls in
both builds and trace modes under UTC/C locale, unset TZ/C.UTF-8 locale, and
UTC/C.UTF-8 with a 65,536-byte padding environment value. All 192 runs must pass.
A separately compiled variant omits only the reservation release, keeping the
exact chunk allocated across the native call. All 192 corresponding runs must
exit 100 with the constructor-address assertion; compile/setup errors do not
count as rejection. This regression reaches the actual free/constructor path.

Changed native harness paths: free interception, bounded bin preparation,
term/inner sizing, test-only path selection, and constructor address checks for
root, term, list, duplicate, JSON parse, and validation. The seven G1 controls
retain their request syntax and assertions. Excluded: production Go, native
library algorithms/instrumentation, public API, journals, budgets, caches,
ownership, dependencies, policy files, and golden files. Existing observer
LIMIT/watchdog bounds still cap recovery/storage/traversal; every new malloc
and constructor result is checked. No production hot path changed.

The existing counted-work regression ran on current upstream main `a10a82a`
(verified with `git ls-remote`) and this worktree: all twelve visit/charge/
allocation/byte tuples match, including duplicate-heavy adverse insertion order
at 64 and 256 children, with/without a journal. No new manifest fixture IDs.

Continuation verification (amd64, normal dev user, UTC exported outside and
inside the container, stdin redirected from `/dev/null`):

| Command/check | Exit | Evidence |
|---|---:|---|
| `build.sh` | 0 | Both binaries rebuilt; driver source matches BUILDINFO |
| Targeted Go reuse/lifetime/sweep regressions | 0 | Includes the new Go matrix regression |
| `reuse_test.py -v` | 0 | 192 positive runs; 192 exact constructor-assertion rejections |
| Go matrix regression with both release-disabled driver paths | 1 (expected) | All 192 positive cases fail at the constructor address check |
| Standalone reuse patch on main `a10a82a` | 0 | Applies cleanly; both binaries build; all 192 positive matrix runs pass |
| Main/head ordinary-work regression | 0 / 0 | All twelve tuples identical |
| Oracle-tag `go vet ./data`; Python checker unit tests | 0 / 0 | 12 checker tests passed |
| `make ci` | 0 | Race, 386, minimum Go, oracle, security and golden checks green |
| `make test-oracle oracle-check` | 0 | Both oracle modules pass; goldens match |
| Driver-enabled `go test -tags oracle ./data -run '^TestNative' -count=1 -timeout=30m -v` | 0 | 58 top-level passes, zero skips |
| `check.py --driver … --comparison-driver …` | 0 | Both drivers; complete positive and rejecting replay controls |
| `g2_mutants.py` | 0 | Eight rejecting mutants, two independently protected removals |

The five full checks above ran in one `flock -w 1800` critical section; each
prescribed make command ran once. The lock was acquired without a timeout.
These full results certify part 1 plus the reuse fix; the standalone main patch
was separately checked with both native builds and the 192-run positive matrix.
Neither named #351 flake occurred. Unfiltered oracle-tag lint still exits 1 for
the same twelve existing findings (two gocritic, ten gosec); none names the new
Go regression. The default CI lint passed. No reuse finding remains open.

Full logs and exact statuses are under ignored `.build/reuse-checks/`;
targeted/sensitivity/standalone logs and all twelve work tuples are under
`.build/reuse-evidence/`. No manifest fixture IDs were added.

## Part 1 evidence field inventory (pre-gate audit)

The inventory was made before changing the acceptance predicate. `snapshot()` is
shared by bootstrap, every completed command, instrumented native events and
snapshot boundaries. Commands without a separate result still emit this full
snapshot. The following table records the former validation/comparison gaps and
the contracts the audit repair must enforce. All names refer to JSON keys.

Command-to-channel dispatch is exhaustive for this extracted driver:

| Request | Output in addition to the common completed snapshot |
|---|---|
| `new`, `root`, `term`, `list`, `frontier`, `opaque`, `opaque-invalid-xml`, `dup`, `dup-to-context`, `unlink`, `attach`, `before`, `detach-siblings`, `change`, `parse`, `parse-xml`, `context`, `context-keyless`, `child`, `record`, `record-as`, `insert`, `remove`, `I`, `U`, `free`, `free-root`, `validate`, `check-handle`, `dump`, `find-next-loop` | No separate successful result record. Instrumented events use the snapshot contract; explicit precondition/watchdog stops may replace completion, checked against the request's expected terminal. |
| `schema` | `read.schema` |
| `instance`, `opaq-find` | `read.instance`, `read.opaque`; instrumented comparison callbacks may accompany instance lookup. |
| `null-reads` | `read.null`, containing all three native statuses/winners |
| `read-schema`, `read-instance`, `read-value`, `read-opaque` | Corresponding `read.selection.*` |
| `equal` | `read.equal` |
| `rewind` | `read.first-sibling` |
| `print` | `public.result` |
| Bootstrap | One `driver.ready.actual-children-callback` snapshot at operation 0 |
| `# reuse-free`, `# reuse-next` | Directives do not consume an operation; successful reuse may emit `allocation.address-reuse`. Existing positive/held-target rejection controls pin this channel. |

The process exit status is checked before accepting a replay (the two rejected
constructor controls explicitly expect exit 5). Stderr contains transport/control
failure explanations and is retained for failure reporting, not semantic equality:
these failures cannot certify successful evidence. Existing watchdog and held-target
rejection controls pin that exclusion. Native semantic diagnostics use the JSON
queue and logger channels below, including successful operations with diagnostics.

| Channel / emitter | Complete emitted fields | Validation and comparison |
|---|---|---|
| Snapshot envelope | event, operation, rc, node_generation, source_generation, optional working_rb_root/table, output_handle, output_generation, node_count, table_count, allocation_scope, bindings, tables, nodes, schemas, diagnostics, optional modules; logs | `identity.validate` checks registries/provenance and request bindings. Previously bootstrap rc/logs and incidental envelope state were dropped. Require shape independently of field presence; compare bootstrap and completed semantic envelopes. |
| Bindings | handle, generation, revision, operation | Request binding history validated; generations mapped injectively; compare all binding history fields. |
| Node lifetime | generation, id, creation_operation, creation_ordinal, source, attempt, alive; dead/unsafe current | Provenance, tombstones, source ordering and birth labels validated. Observer ordering/provenance excluded only as described below; live component keys and bindings compared. |
| Live common node | context, optional context_generation, hash, flags, prev, next, parent, metadata | References validated. Previously metadata contents and the current-context bit were dropped; preserve both, current hash separately from recorded hashes. |
| Schema node | nodetype, key, schema, schema_identity, optional module/value; inner first, tail, table | Schema registry and ancestry validated; normalize schema identity through its descriptor, compare all semantic content. |
| Opaque node | first, tail, prefix_format, prefix_bound | Previously name, module/namespace, prefix and raw value were not emitted. Add required opaque_name/opaque_module/opaque_prefix/opaque_value; preserve null versus empty and all opaque text bytes, including invalid XML. Prefix payload traversal remains part 2. |
| Sorted metadata | rb_stored_root, rb_root, rb entries: owner, node, parent, left, right, color | References validated; previously topology omitted from shared projection. Preserve topology and metadata names. No new sorted-tree algorithm. |
| Compiled schemas | identity, context, parent, nodetype, module, name | Immutable registry/ancestry validated; compare current live descriptors through structural keys. Unreferenced discovery history is excluded, with the temporary-parser control described below. |
| Current context modules | name, optional namespace/revision, implemented, latest | Previously only key presence checked and contents dropped. Validate record types/nonempty registry and compare full ordered contents, including bootstrap. |
| Table registry | identity, alive, optional storage at unsafe boundary | Registry/retirement/ownership validated. Observer history excluded across builds; current storage compared. |
| Table observation | present; if present identity, optional retired, continuity, size, used, mode, free, buckets(first,last), slots(next,node,hash), chains(bucket,slot,ordinal,node,generation,occurrence,hash,alive), free_chain, free_terminal | Validate full shape/layout, owner, occurrence binding and continuity before normalization. Previously `read.selection.*` escaped this registry check. Compare every layout/content field after local identity resolution. |
| Diagnostic queue | code, context_generation, optional msg/path/apptag | Validate field types/context; compare complete records at bootstrap and command completion. |
| Logger (always present, including an empty bootstrap array) | level, line, optional msg/path/schema_path | Validate types and compare full records, including bootstrap. Normalize only the embedded Internal error C source location and the exact observer wrapper annotation described below. |
| equal result | event, operation, rc, left_generation, right_generation, options | Validate by event and request regardless of `nodes`; reject snapshot fields here. Bind both operands to the preceding registry and compare identities/options/status. |
| read-selection result (schema/instance/value/opaque) | event, operation, rc, winner, winner_script, current_parent, parent_script, next_generation, next_script, before, after | Validate request dispatch and all identities/storage against preceding snapshot, then compare semantic result and tables. |
| G1 schema result | event, operation, rc, queried_parent, target, schema_hash, winner, current_parent, before, after | Existing request/winner validation retained; preserve every result field after reference resolution. |
| G1 instance/opaque result | event, operation, rc, supplied, winner, current_parent, target or name, before, after | Existing dispatch validation retained; preserve complete result after reference resolution. |
| Null result | event, operation, queried_parent, target, before, after, schema_rc/schema_winner, instance_rc/instance_winner, value_rc/value_winner | Existing null preconditions retained; preserve all three results and storage. |
| Rewind / public result | event, operation, winner / event, operation, rc, optional json | Validate dispatch, compare complete normalized result; failed printing may omit json. |
| Callback comparison | event, operation, left, right, options | Instrumented-only `read.compare`; validate live operands. Excluded across builds because the untouched library emits no callback observations. |
| Reuse / tombstone boundary | event, old_generation, new_generation / event, operation, handle, generation, current | Validate against retained identities and bindings. Process-local reuse notices excluded across builds; tombstone boundary identity is validated. |
| ABI query | pointer_bytes, long_double_bytes, long_double_mantissa_bits, pointer_record_bytes, rb_node_bytes, ndebug, instrumented | Separate `--abi` channel: checker asserts all seven exact expected constants (instrumented differs deliberately). No replay projection. |

Cross-build exclusions are limited to instrumented intermediate snapshots/callbacks
and reuse notices (no untouched counterpart), allocation_scope (different explicit
coverage claims), lifetime registry counts/retired rows and creation_ordinal/source/
attempt (transient observations and discovery order differ), numeric node/schema/
table/occurrence identities (process-local, validated before structural resolution),
and table continuity (instrumented `observed`, untouched children `unknown` or generic
`observed-generic`). Live state, schema descriptors, binding revisions/operations,
current tables, all result fields, bootstrap/command status and diagnostic contents
are compared. An excluded identifier must still be present, correctly typed, owned,
and consistent with retained history. Exclusions are pinned by positive equivalence
and invalid-identity rejection controls, not by accepting missing fields. Unknown
fields fail closed. Optional fields retain their presence in semantic comparison.


The watchdog's `event`/`classification` line is an unsuccessful transport
(exit 124), rejected before evidence comparison. It cannot certify a replay.
Instrumented non-completed snapshots, including native precondition boundaries,
are an observation lane: their closed JSON shape, identities and history are
validated, while their intermediate state is excluded from cross-build equality
because the untouched library does not emit the same event schedule. Existing G1
boundary/completion controls pin the requested stop and diagnostics separately.
The field sweep checks that this excluded lane still rejects malformed fields;
`pin_exclusions` also proves quiet/verbose equivalence, complete occurrence-token
renumbering, and the documented cross-build coverage/continuity difference.

Audit-5 regression mapping:

| Previous finding | Repair and live regression |
|---|---|
| Selection table identities disappeared | Closed table/occurrence shapes, preceding owner/storage equality, serial ownership and continuity checks before normalization. `TestNativeG2SelectionContract` deletes, adds and mutates table identity, occurrence and continuity on left/right/both peers. |
| `nodes` bypassed equality operands | Result shape selected by event/request; snapshot fields rejected on results. `TestNativeG2EqualityResultContract` covers missing operands, the injected completed snapshot and a wrong operand binding, including identical peers. |
| Opaque values absent | `node_json` emits required byte-exact hex name, nullable module/namespace, prefix and value bytes. `TestNativeG2OpaqueOperandContract` compares A/B with C/D at the same verdict and deletes/adds/mutates each new field. |
| Modules and metadata dropped | Closed nonempty module registry and typed metadata arrays preserved, along with current-context bit and sorted topology. `TestNativeG2ContextMetadataContract` exercises bootstrap and completed equality, deletion/null/empty/addition/content mutation. |
| Bootstrap status/diagnostics dropped | Bootstrap envelope includes rc and complete diagnostics/logs. `snapshot` always emits logs, including empty arrays. `TestNativeG2BootstrapChannelsContract` covers deletion/addition/mutation of all three channels. |

Every named Go regression fails against the original predicate from `0fdf084`;
all five failures are saved in `.build/audit5/predicate-before.log`. The generic
sweep also fails against that predicate (`old-fields.log`, missing bootstrap
operation). Self-contained audit mutants disable each repaired protection and
must fail the corresponding Go regression. The generic bucket-omission mutant
must fail specifically with an uncompared bucket, not an import or setup error.
The generic sweep exposed and fixed omitted chain bucket/ordinal content too.

`TestNativeG2EveryEmittedField` walks actual nested JSON, tries deletion, addition,
same-type mutation and wrong-type mutation for each distinct emitted field shape,
and exercises the documented exclusions. Synthetic queue/logger records cover
optional diagnostic fields absent from the selected native failures; these are
acceptance-predicate tests, not claims about native diagnostic semantics.

Native functions changed: observer-only `node_json`, `snapshot`, and new
`bytes_or_null`; library implementations remain pinned and unchanged. No production
Go mutation, journal, cache, ownership, budget, public API, dependency, policy or
golden path changed. Input lines, observer LIMIT bounds and native watchdog remain
in force; opaque byte emission is linear in the bounded operand value. Parts 2–5
(prefix payloads, read kernels/cursors, comparison kernels and validation adapters)
remain excluded. No new manifest fixture IDs; all new native cases are live inline
requests retained in the regression sources.

Fresh audit-5 targeted evidence: all five named Go contracts, existing identity
rejection controls, completed-command projection and protocol smoke passed (exit
0); oracle-tag vet passed (exit 0); checker unit tests passed 12/12 (exit 0).
The generic Go control passed (exit 0, 352.05s): 6,814 deletion controls, 1,270
addition controls, 6,238 same-type mutations and 6,814 wrong-type mutations,
plus 1,740 typed fields in the excluded intermediate lane. Its single accepted
semantic perturbation is the explicitly excluded callback comparison option.
All five new self-contained audit mutants and the generic bucket omission were
rejected. Unfiltered oracle-tag lint exits 1 for the same twelve inherited
findings (two gocritic, ten gosec); none is in the changed code.

The ordinary-work regression was rerun on an archive of the specified main base
`a10a82a` and this worktree (exit 0 / 0). All twelve visit/charge/memory-count/byte
tuples match exactly, including duplicate-heavy reverse insertion at 64 and 256
children, with and without the journal. At 64: `(4504,8099,1147,116176)` /
`(4504,8649,1186,345128)`; at 256: `(26608,42844,4207,444112)` /
`(26608,44994,4256,1425512)`. Full tuples are saved in
`.build/audit5/work-main.log` and `work-head.log`. Protocol shape validation scans
module evidence once per snapshot and checks each nested record once; no
production work path changed.


The final byte-boundary inventory also found a native-successful opaque name
containing byte `ff`: cJSON emitted that byte directly, and Go's ordinary JSON
decoder could replace it with Unicode replacement text. `bytes_or_null` therefore
hex-encodes all four opaque text fields, not just values. The Go transport rejects
invalid UTF-8 before decoding any channel. `TestNativeG2OpaqueByteNames` uses live
`ff` versus `fe` names with the same verdict and compares both native builds;
`TestNativeG2EvidenceUTF8` rejects malformed transport. Dedicated observer/Go
counterfactuals pin both protections. These remain evidence-only changes.

Final byte-boundary checks: rebuilt targeted drivers, all G2 contracts (including
byte names/transport), completed-command projection, oracle-tag vet and 12 checker
unit tests pass (exit 0). The generic sweep passes again with unchanged counts
(exit 0, 345.91s). Both additional byte-boundary mutants are rejected. Oracle-tag
lint still exits 1 for exactly the twelve inherited findings, with no new issue.

The completed metadata corruption explicitly selects handle 1 from the audit
request; the bootstrap case selects its independent seed. This oracle-only
control was tightened while CI was in its untagged race phase, before oracle
compilation; its focused rerun and oracle-tag vet both exit 0.

Two observation differences are now pinned explicitly. Compiled-schema registry
entries seen only while temporary data exists are coverage history: the untouched
observer cannot discover them after cleanup. Every descriptor remains typed,
unique, ancestry checked and immutable locally; descriptors of all current live
nodes (with complete compiled ancestry) remain compared. The real missing-key
parser case in `TestNativeG2TransientSchemaRegistry` compares verbose traces and
then creates a live list, so a later referenced descriptor is still evidence.

`instrument.py:wrap` adds `_nr_impl` to `__func__`. Only that suffix in the final
function annotation of an `Invalid argument … (FUNCTION()).` message is normalized,
and only for the exact wrapped-function inventory. Argument text, base function,
code, context, path, app-tag, level and separate line remain exact. The real
zero-hash remove in `TestNativeG2ObserverFunctionAnnotation` pins equivalence and
rejects changed argument text. `TestNativeG2ObserverWithoutStatus` reaches the
instrumented comparison callback, whose contract has no rc; status projection is
selected by completed-command identity, without imposing rc on observer notices.

Read storage identity is checked across the preceding and completed snapshots,
including generic storage. All read, alias, print, dump and context commands are
inventoried in `STABLE_STORAGE_COMMANDS`; constructors, index/data mutations,
validation and synthetic schema mutation are excluded from that purity rule.
`TestNativeG2ReadStorageContinuity` retires an old table and supplies a fresh,
internally consistent registry/occurrence set with identical contents. Both owned
and generic variants must fail on left/right/both peers before IDs are removed.
Its disabled-guard counterfactual is rejected by the regression.

Validation-only consumers now skip constructing comparison output, while keeping
all shape, registry, reference, ownership, lifetime and read checks. The generic
field sweep requires every wrong-type perturbation to fail in both lanes. Static
layout contracts are bounded and contain no trace data; table obligations are
checked before walking values. The existing checker-wrapper time budget is kept.
Incomplete schedules and missing event counts are rejected before walking retained
history. Each peer's complete projection is reused only within one comparison;
mutable traces are never cached across comparisons. These changes retain every
positive and negative acceptance obligation.

The once-only locked full run acquired the heavy-check lock within the requested
hour. Commands ran as the normal amd64 dev user, with UTC set outside and inside
the container and both driver variables set inside. Exact command exits:

| Command | Exit | Evidence / qualification |
|---|---:|---|
| `bash conformance/oracle/native_registration/build.sh` | 1, retry 0 | Existing cached build files had incompatible ownership; rebuilding with dev-owned copies succeeded. Fresh BUILDINFO matches driver.c and pinned native commit `47351e5`. |
| `make ci` | 2 | Reached oracle tests after race, 386 and minimum-Go checks; exposed transient-schema and observer-function diagnostic differences now covered by focused regressions. |
| `make test-oracle oracle-check` | 2 | Stopped in test-oracle on those differences and an invalid resize mode in the new storage-continuity test, since corrected to mode 1. Did not reach oracle-check in this invocation. |
| Driver-enabled `go test -tags oracle ./data -run '^TestNative' -count=1 -timeout=30m -v` | 1 | Zero skips; only failure was the checker wrapper's three-minute deadline. |
| `python3 .../check.py --driver ... --comparison-driver ...` | 0 | Both native binaries, all checker controls. |
| `python3 .../g2_mutants.py` | 0 | All then-present sensitivity controls rejected their counterfactuals. Four subsequently added exclusion/status controls also passed in a targeted run. |
| `make test-gates vuln secrets sensitive oracle-check registries` | 0 | Separately locked, previously unreached CI components; no golden edits. |
| `go vet -tags oracle ./data` | 0 | Focused post-fix check. |

Raw full statuses are in `.build/audit5/full-status.txt`; individual logs preserve
the failures. The outer full-run controller accidentally used unavailable `rg`
for aggregation: its exit 0 is **not** a passing aggregate. Individual command
statuses above are authoritative; zero skips was checked separately on the host.
The later focused-run statuses are in `final-status.txt`. No failure is attributed
to the known unrelated flakes. A green full CI/oracle/native gate on the final
patch remains pending: the user limited full suites to one run, so they were not
repeated after fixes. No commit or push was made.

Post-fix targeted verification: the complete G2 contract set (including the field
sweep), completed-command projection, identity rejection controls and native
identity sweep pass together (exit 0, 393.62s). The checker wrapper passes within
its unchanged three-minute budget (exit 0, 125.75s), and checker unit tests pass
12/12 (exit 0). Logs: `current-contracts.log`, `checker-budget-final3.log` and
`current-units.log`. The earlier deadline failures are retained, rather than
reclassified as unrelated flakes.

Final validation-order check: `TestNativeG2EveryEmittedField` passes in 298.64s
(exit 0), with the same 6,814 deletions, 1,270 additions, 6,238 value mutations,
6,814 wrong-type controls and 1,740 typed excluded-lane fields. Oracle-tag vet and
gofmt checks exit 0. Focused reruns of all five contract counterfactuals, generic
bucket omission and four exclusion/status counterfactuals each exit 0 as runners:
every intended broken predicate is rejected by a real failing regression. Logs:
`current-fields.log` and `current-mutants.log`. Final sources are recorded in
`final-sources.sha256`.
