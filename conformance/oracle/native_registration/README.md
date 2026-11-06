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
winner/current-parent identities. Allocation generations are normalized through
script handles before comparison; generations from different processes are
never compared directly. The Go evidence test passes its selected executable
and uses `YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER` when provided, otherwise
`driver-unmodified` beside that selected executable. Safe-prefix boundary
cases are never run to an accidental completed result or used as goldens.

## Replay protocol and evidence

Input is a bounded line-oriented public-call script. `cases/*.calls` are the
independent live-oracle request cases. IDs are script handles, never addresses.
`root`, `term`, `opaque`, `dup`, `unlink`, `detach-siblings`, `attach`, `before`, `change`, `parse`,
`schema`, `validate`, `print`, `free`, `free-root`, and `rewind` call their corresponding
libyang APIs. `opaque ID PARENT NAME VALUE` uses the JSON opaque constructor;
`before ANCHOR NODE` moves a node using public `lyd_insert_before`;
`free-root` invokes whole-root `lyd_free_all`. `dup` takes native option bits (66 means NO_META|NO_LYDS).
`new SIZE MODE`, `insert ID HASH CHECK`, `remove ID HASH`, `I ID`, `U ID`,
`find-next-loop` and `check-handle` are explicitly **low-level controls**, not evidence of
public reachability. Modification-callback controls hold distinct, equal-valued
real native nodes alive. `dump` observes state only.

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
eliminated occurrence. Nodes carry `(operation, creation ordinal)`, script ID
where supplied, dup source ID, hash/flags/schema, raw link identities,
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
  and o as independent live allocations. These concrete controls do not establish
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
