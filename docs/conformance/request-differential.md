# Request differential replay and triage

## Fuzz the native oracle

The `oracle` build tag enables `FuzzOracleRequestDifferential`. It maps up to 256
input bytes to a `sequence` request using the fixed `pv2` or `pv2-edit` corpus
schema, JSON or XML data, and one of parse, validate, print, diff, merge, dup,
or new_path. The input limit prevents unbounded work during fuzzing. The target
compares each native response with the Go engine using the conformance report's
normalization. Only existing, predicate-matched
registered deviations are exempt. The failure prints the complete request JSON.

Build the pinned native oracle and run from the conformance module in the dev
container (the canonical oracle architecture is linux/amd64):

```sh
DEV_PLATFORM=linux/amd64 ./dev make oracle
DEV_PLATFORM=linux/amd64 ./dev sh -c 'cd conformance && TZ=UTC YANG_ORACLE_CACHE= YANG_ORACLE_REQUIRED=1 go test -tags oracle -run "^$" -fuzz "^FuzzOracleRequestDifferential$" -fuzztime 5m -timeout 30m'
```

Go writes a minimized failing byte input under
`conformance/testdata/fuzz/FuzzOracleRequestDifferential/`. Replay it with
`go test -tags oracle -run '^FuzzOracleRequestDifferential/<file>$' -v` and copy
the printed `request` JSON into a new
`corpus/manifest.d/<set>/<name>.yaml` fixture with its source, RFC and area
metadata. Place any input files under that fixture's corpus directory. Generate
the golden only through `make oracle-golden` on linux/amd64; never edit golden
files by hand. Record a library mismatch in the PR rather than weakening the
fuzz comparison.

## Scope

The M4 exit gate requires agreement for its validation and operation corpus
fixtures, subject only to explicitly registered deviations, unsupported
fixtures, and reported field exclusions. Existing corpus gates remain unchanged.

Outside the corpus, the engine must faithfully map supported requests to
library operations and compare their observable results. Full compatibility
with lyoracle's request-envelope validation is not an M4 requirement. A
demonstrated envelope-only discrepancy may be deferred under an explicitly
registered class with native evidence and a follow-up issue. Changes to library
arguments, options, context identity, execution, results, or diagnostics are not
envelope-only.

Deferred discrepancies are reported separately and never counted as MATCH or
library agreement. Unclassified discrepancies and failures of comparison,
generation, or evidence integrity remain blocking. This boundary does not relax
the library's libyang compatibility requirements.

The single-field matrix runs in CI (`make test-oracle`). The pairwise matrix
(`oracle_full`) stays outside routine CI and is re-run for the cases a change
affects.

The conformance engine is test tooling: it must match the oracle or return
unsupported. The native run and offline cache replay use the same generator,
A/B/C/DEV classifier, comparator and explicit Go registries.

* **A — WRONG:** a concrete engine result differs from the oracle, including
  verdict, error code, diagnostics, tree, diff or query output. Required: zero.
* **B — GAP:** the engine returns `ErrUnsupported` where the oracle produces
  any result, including an error. Allowed only for a documented class below.
  Unknown classes fail the test, even
  if their error mentions an existing registry ID.
* **C — ARTEFACT:** the generator changes setup or a step outside the subject
  under test. Required: zero, even if both engines agree on the altered request.
* **DEV — REGISTERED CRASH:** libyang terminates with the exact registered signal text
  for a request matching exactly one explicit crash class, and the engine returns that
  deviation's documented result. Counted separately from matches and gaps.

Equal concrete results are counted separately as `MATCH`. Missing cache entries,
unregistered or ambiguously classified crashes, and native transport errors other than a
registered signal fail the run; they are not B gaps or matches. A registered signal is DEV
only when its termination text is exact and the engine result has the documented failing-step
return code; any other signal text is A. A context deadline is always A (`native-transport`),
even if the operating system reports that the timed-out child was killed.
`TestRequestValidationDifferential` logs totals and counts per class, asserts
A=0 and C=0, and checks every B against `inventoryGapClasses`.
`TestRequestMatrixIsolation` checks C for every case without needing an oracle.

## Evidence and class counts

The canonical linux/amd64 native run of the single-field matrix (CI tag `oracle`)
after #247 API part 3 gives:

| Category | Class | Count |
|---|---|---:|
| MATCH | Equal execution and output | 135,719 |
| A — WRONG | Concrete execution mismatch | 0 |
| B — GAP | All registered B classes | 1,160 |
| C — ARTEFACT | Mutation outside the subject | 0 |
| DEV — REGISTERED CRASH | Registered signal with the documented engine result | 8 |
| **Total** | | **136,887** |

The run reported no A, C or deferred result.

## DEFERRED class list

`DEFERRED` is a separate, non-MATCH category for demonstrated envelope-only
discrepancies (see Scope). It is mirrored by `inventoryDeferredClasses`; a
concrete mismatch is deferred only when exactly one complete class matches.
Native transport failures, registered native terminations (DEV) and artefacts
(C) are never deferred. Each entry needs a narrow predicate, a native
reproducer, an explanation of why it cannot alter library execution, and a
follow-up issue (deferred envelope classes are tracked in
[#247](https://github.com/vibe-ports/yang/issues/247)).

| DEFERRED class | Predicate | Native reproducer | Explanation | Issue | Count |
|---|---|---|---|---|---:|
| (none) | | | | | 0 |

## B class list

This is the explicit list mirrored by `inventoryGapClasses`. Zero means that
the single-field matrix contains no instance, not that the capability exists.
Pairwise counts are reported separately below. #247 exports `NotFinal` and
`ValidateModuleFinal`: split-validation requests now execute against the live oracle,
including the formerly unsupported final-only corpus fixtures. `TestSplitValidationOracle`
compares phase checks, reference plugins, default flags, implicit diffs and argument errors.
Historical counts below predate that API expansion. Canonical/store-only
constructors, canonical changes and `NewList2` now execute through their public
APIs too, retiring `canonical-values` (U-0108) and `list2` (U-0109).

| B class | Registry ID | Capability gap | Single-field |
|---|---|---|---:|
| `ordered-input` | D-0058 | `LYD_PARSE_ORDERED` input ordering | 39 |
| `any-direct` | U-0112 | `any_direct` step: native pointer-level any-value calls | 230 |
| `operation-parent` | U-0106 | Operation input into an existing parent, or reply into an opaque request | 691 |
| `reply-non-operation-parent` | U-0110 | Reply into a parent that is not an RPC/action; `ParseOpOptions.Request` rejects it before parsing | 0 |
| `cross-context-diff` | D-0069 | Diff matching between populated trees from separate schema snapshots (#236) | 0 |
| **Total** | | | **960** |

The classifier requires the relevant step and the exact unsupported reason.

U-0110 now covers only `reply-non-operation-parent`, tracked in
[#247](https://github.com/vibe-ports/yang/issues/247). The six NETCONF/RESTCONF
message types (#95) run through the engine's envelope parser for `data`, XPath
and sequence parse/diff requests, including `print_subtree` and empty-input
error ordering. Live-oracle requests in `issue247_oracle_test.go` cover every
retired envelope class without generated goldens.
Existing IDs retain their scope. U-0107 now covers only opaque attribute
construction (`lyd_new_attr*`), not envelope parsing: `insert_opaq` edits and
`compare` roots run through `Tree.NewOpaque` (#247), with live-oracle requests
in `issue247_api4_oracle_test.go`. The two `any_direct` opaque-source cases
still need `lyd_new_any` and pointer-level any-value children, so they count
under `any-direct` (U-0112).

## DEV crash class list

This list is mirrored by `inventoryCrashClasses`. A native failure must carry the class's exact
registered termination text and match exactly one predicate; the engine result is then checked
before it is counted as DEV. A crashed native run has no response, so none of its steps can be
compared; the engine's result is checked against the registered one.

Completed-native deviations (`inventoryDeviationClasses`) are registered deviations where the
native run completes and the port refuses on purpose. A concrete mismatch (A) is DEV only when
exactly one class holds all three checks:
- its precondition, checked on the request and the native response, never on the engine's;
- every step before the refused one compared in full (diagnostics, trees, results);
- the refused step's exact engine result.

Only the refused step and the steps after it, which follow from the flipped verdict, may
differ. For `dup-mismatched-parent`, the precondition is read from the native typed dump before
the dup step: the node's data parent instance (an instance of its `lysc_data_parent`; none for
a top-level node) has a different schema node than the target parent.

| DEV class | Registry ID | Predicate | Exact termination text | Required engine result | Single-field |
|---|---|---|---|---|---:|
| `dup-term-parent` | D-0123 | A sequence `dup` without `with_parents` whose `parent` resolves in the parsed seed tree to a leaf or leaf-list instance | `signal: segmentation fault (core dumped)` | The `dup` step fails with LY_EINVAL | 3 |
| `op-parse-term-parent` | U-0106 | An op `data` request with data_type `rpc`, `keep_input` true and `unknown` `opaque` whose `rpc_parent` resolves in the inline rpc input to a leaf or leaf-list instance (parse_one parses the input opaque under that node, lyoracle.c:1307-1313) | `signal: segmentation fault (core dumped)` | Unsupported, B class `operation-parent` (the API cannot express an rpc input parent) | 0 |
| `dup-mismatched-parent` | D-0124 | A sequence `dup` with a `parent` (no `with_parents`, no `target`) that succeeds natively, where the native tree before it shows the parent is not an instance of the node's schema parent; every earlier step equal | none (the native run completes) | The `dup` step fails with LY_EINVAL and exactly the D-0124 diagnostic for that node and parent | 1 (engine-only count; was MATCH) |

## Generator and comparator changes

Schema/context keys (`base_dir`, `context_options`, `searchdirs`, `modules` and
nested module fields) are varied only by the schema case or explicit target
context cases. Non-schema cases keep their original schema setup. The schema
and target-context empty-revision probes remain, as does the engine's correct
revision-rejection behaviour. Setup steps and trailing observations remain
fixed; skipped probes replace setup with a known failure and retain the subject
and trailing steps. C checks verify that isolation for each generated request.
The `schema/repeated-load` seed first loads `pv2`, then applies nested
`modules[].revision` and `modules[].features` mutations to a second `pv2` entry,
so rejected repeated loads cannot hide context mutations.

The `step/dup-source` seed varies the separately parsed `source` object and its
fields. Preflight requires an object with `data` present and `parse_only` equal
to JSON true, checks string `data` through `input_of`, rejects unlisted source keys,
and forbids `target` alongside `source`. It also checks that exactly one of
the string selectors `node` and `node_xpath` is present. An empty `node` string
counts as present and reaches lookup after parsing. `source.data` may be an
empty string; null or absence does not satisfy the requirement. `source.rpc` is
optional and is read only when the dup step executes. `source.keep_input` uses JSON truth, so
nonboolean values act as false.

During execution, `dparams_of(source)` checks `source.format` (`json` or
`xml`), `source.unknown` (`reject`, `skip`, or `opaque`), and `source.data_type`
(`config`, `data-operational`, `data`, `get`, `getconfig`, `edit`, `rpc`, `reply`,
`notif`, `notif-netconf`, or `notif-restconf`) before reading the source inputs
and parsing. Missing or null values take their defaults; wrong types and
unrecognized strings are request errors. The separately parsed tree is then
used for `node`/`node_xpath` selection; `parent` still selects in the retained
tree. A missing `node` in the source reports `dup node … not found in the
source`. Source parse diagnostics carry phase `parse`; duplication diagnostics
carry phase `edit`. If an earlier step fails, these execution checks are
skipped, while preflight checks still run for every step.

The schema-prelude ordering audit follows `lyoracle.c`: context flags are read at
1094 before context creation at 1098; search directories are applied at 1100-1104
before requested modules; each module's name/revision and missing-name rejection
occur at 1113-1122 before its load at 1134; and feature collection at 1124-1128
does not reject non-string children. The Go adapter follows the same order. Its
empty-revision compatibility rejection probes the load in a disposable context,
so the real context is exactly as if that module entry had been omitted. When the
probe parses the requested module, the loader rejects the explicit empty revision
before resolving its imports and includes or compiling it, and names the parsed
module's revision in the diagnostic.

Generic sequence seed labels use `step/`, removing the old collision between
root `diff` and sequence `diff`. Requests use `base_dir: "."` and relative,
content-hashed input filenames in an isolated corpus copy, so native capture
and offline replay generate identical request keys. File inputs are recreated
before either mode runs.

Every path-valued key now has `valid-abs`, `spaced-abs` (an absolute path with
leading XML whitespace), `valid-rel`, `missing-abs`, `missing-rel`, and
`malformed` representatives. Single-field generation keeps all earlier
representatives and adds these six; pairwise generation uses these six in place
of its former generic valid/malformed pair (absent and null remain).
XPath has a seed for every data type, including both parsed-only and validated
RPC input, and data has an RPC-with-request seed so `rpc_parent` reaches lookup.

The shared classifier compares execution outcomes (including diagnostic text,
codes, paths and app-tags) and output fields, including JSON/XML trees, diffs,
XPath values and atoms. It retains the existing `FieldSkipper` exclusions for
compiled schema text and unexported typed metadata; it does not discard concrete
tree/diff results. Request-error comparisons retain the explicit text classes
for quoting and OS file-error differences, preserving the rejected field and
kind of error. Unclassified error text compares verbatim.

When the engine returns `ErrUnsupported` and the oracle returns a request-error,
the result is A only if the oracle rejected a pre-parse request-shape check the
engine can perform first. Selector lookup, `cur_module` lookup, namespace checks
and `xpath_format` semantics follow envelope parsing, so those errors retain the
documented B class for an unsupported envelope. The `any_direct` missing-target
check precedes its capability gap and remains A if the engine skips it.

Four parse-flag probes cover `when_true` on a false when and `anydata_strict` on
ordinary nodes, in JSON and XML. The sole new public field is
`data.ParseOptions.WhenTrue`: it exposes already ported `LYD_PARSE_WHEN_TRUE`
behaviour needed to seed the flag before validation. The A fixes also preserve
input presence, reproduce preflight error classes and deterministic key order,
check selectors and constructor preconditions, reproduce target schema failures,
and remove inherited trim-XPath locations. A relative set under a retained parent
now passes the original text and parent through `NewPathOptions.Parent` (#255),
including opaque parents and whitespace-led paths. The remaining B capabilities
remain unsupported.
Fifty-three representative native execution records remain runnable without an
oracle/cache in `TestLoggedRequestExecution`.

## Native capture and offline replay

Capture in the canonical amd64 container, using a fresh cache destination:

```sh
DEV_PLATFORM=linux/amd64 ./dev sh -c 'cd conformance && YANG_ORACLE_REQUIRED=1 YANG_ORACLE_CACHE_WRITE=../oracle-cache.jsonl go test -tags oracle -run "Differential$" -count=1 -timeout=45m -v'
```

Replay without the native binary or oracle build tag:

```sh
cd conformance
YANG_ORACLE_CACHE=../oracle-cache.jsonl go test -run 'Differential$' -count=1 -timeout=45m -v
```

`YANG_ORACLE_CACHE_WRITE` appends JSON-lines records containing the SHA-256 of
exact request JSON, that JSON, the complete native response, and any native
transport error. With the `oracle` build tag, `YANG_ORACLE_CACHE` and
`YANG_ORACLE_CACHE_WRITE` may be set together: cache hits replay, while misses
run natively and are appended to the write file. Without that tag, a miss still
fails. Replay verifies digests in a streaming indexing pass, loads responses on demand by file
offset, and preserves transport failures. Duplicate request hashes are accepted only for
byte-identical records. Do not commit the runtime cache. `YANG_REQUEST_MISMATCHES` writes the
A/C ledger and a separate B/DEV ledger, with the class and registry ID for every exception.

Audit cache error fields without replaying the matrices:

```sh
cd conformance
YANG_ORACLE_CACHE_ERROR_SCAN='../single.jsonl:../pairwise.jsonl' \
  go test -run TestRequestCacheErrorTexts -count=1 -v
```

Use the operating system's path-list separator (`:` on Unix, `;` on Windows).
The audit reports error, registered and unregistered counts per file and prints
every record whose extracted termination text is not exactly registered.

## Checks

The interaction regression `protocol-v2/reply-non-operation-parent` is explicitly
allowed under U-0110 in the M4 gate. The NETCONF/RESTCONF envelope requests now
execute and need no `validationUnsupported` entry. Existing U-0106 allowances
remain in place, including `ops/opidx-meta-doc-first`.

The single-field matrix runs natively in CI and the pairwise matrix is replayed
from canonical linux/amd64 responses; both must report A=0 and
C=0, with every B assigned to the registry above. The local isolation audits
cover the same mutations without requiring an oracle cache.

## PAIRWISE interaction matrix (`oracle_full`)

`TestRequestPairwiseDifferential` crosses every pair of inventory keys for every
operation and step seed, including nested edits, parents, output flags, target
contexts, and root schema-prelude keys. It has 92 seeds, 232,779 key
pairs and 5,185,088 requests (`TestRequestPairwiseInventory`). Ordinary dimensions
have four representatives (absent, null, malformed, valid); path dimensions have
eight (absent, null, and the six path classes). The malformed module value loads
`pv2-rejected`, a schema with an out-of-range default. Additional seeds exercise
RPC input/output lists with different key counts and replies into a non-operation
parent.

Prelude mutations are intentional PAIRWISE subjects, including for non-schema
operations. C checks allow only the two selected field roots and the documented
file/inline and namespace-format companions; all other setup and trailing steps
must stay fixed. Explicit inline/file pairs preserve both dimensions to test
conflicts. Ancestor/descendant pairs apply in lexical order; the child
materializes its containing object when necessary. Newly materialized target
contexts use the valid schema seed, so nested target probes can reach execution.
The reduced valid flag representative selects `output` where available (unless
the seed already supplies a live flag value). Values are representative
classes, not every value or higher-order combination.

The full test uses the same native runner, response cache, comparator and A/B/C
registry as the single-field matrix. It batches by seed and supports subtest
selection. `TestRequestPairwiseInventory` reports size without an oracle;
`TestRequestPairwiseIsolation` audits all generated requests without an oracle.
The separate build tag keeps these tests out of `make test-oracle` and CI.

Capture on canonical linux/amd64 (lead-run native binary):

```sh
cd conformance
YANG_ORACLE_REQUIRED=1 YANG_ORACLE_CACHE_WRITE=../pairwise-cache.jsonl \
  go test -tags oracle,oracle_full \
  -run '^TestRequestPairwiseDifferential$|^TestRequestInteractionsOracle$' \
  -count=1 -timeout=12h -v
```

Replay the supplied responses without a native binary:

```sh
cd conformance
YANG_ORACLE_CACHE=../pairwise-cache.jsonl \
  go test -tags oracle_full \
  -run '^TestRequestPairwiseDifferential$|^TestRequestInteractionsOracle$' \
  -count=1 -timeout=12h -v
```

Use a fresh cache destination. For smaller captures, select individual seed
subtests and use separate cache files; replay with the same selector. The full
capture is substantially larger than the single-field cache. Pairwise mismatch
ledgers append the seed name to `YANG_REQUEST_MISMATCHES`, retaining each batch.
The pairwise matrix was replayed offline from the canonical linux/amd64 native
responses: the earlier 4,183,464-record capture plus a native top-up for the
`spaced-abs` values and opaque-first seeds, and a second top-up for the ignored root keys
(`do` with every step kind, step-only keys) the `no_state` and false-`when` seeds, and an empty-tree `set` seed. The engine's schema-context cache
keeps a separate context per build call within a request, so a root and a
target context with the same configuration stay distinct, as in plain
`Yang.Run`.

The counts below are the pre-#247 capture. Its two notification-envelope rows,
the opaque-construction row, the canonical-values row and the list2 row are now
retired; the next canonical pairwise recapture is expected to move their 185,736
results from B to MATCH. The current single-field native run is recorded in the
B-class section above.

| Category | Class | Count |
|---|---|---:|
| MATCH | Equal execution and output | 4,951,222 |
| A — WRONG | Concrete execution mismatch | **0** |
| B — GAP | `split-validation` (U-0106) | 792 |
| B — GAP | `operation-parent` (U-0106) | 41,841 |
| B — GAP | `opaque-construction` (U-0107) | 3,469 |
| B — GAP | `canonical-values` (U-0108) | 81 |
| B — GAP | `list2` (U-0109) | 484 |
| B — GAP | `notification-netconf` (U-0110) | 89,905 |
| B — GAP | `notification-restconf` (U-0110) | 91,797 |
| B — GAP | `reply-non-operation-parent` (U-0110) | 3,893 |
| B — GAP | `cross-context-diff` (D-0069) | 1,272 |
| C — ARTEFACT | Mutation outside the subject | **0** |
| DEV | `dup-term-parent` (D-0123) | 240 |
| DEV | `op-parse-term-parent` (U-0106) | 1 |
| **Total** | **4,951,222 MATCH + 233,625 B + 241 DEV** | **5,185,088** |

Triage of these captures found A results that were all fixed at the root:
two malformed `delete` paths whose parse item lacked its location (`ly_path_parse`
logs at the context node, path.c:340; `Tree.Find` now does too); one native
signal on an opaque rpc input parsed under a leaf `rpc_parent`, registered as
`op-parse-term-parent`; and 289 `atoms` requests whose `context_path` had
leading whitespace (`lys_find_path` parses with lyxp, which skips XML
whitespace around tokens). The single-field run found the same `atoms` case.

Relative delete is now a concrete result rather than a B gap. Every remaining B
must belong to the explicit registry above.
The seven separate interaction fixtures also pass (including the documented
reply-parent B); they are excluded from the pairwise totals.

Replay was batched by subtest using verbatim record shards from the supplied
cache, preserving every request digest and complete native response. The original
cache remained unchanged; the XPath seed also passed directly against it.
An integrity pass confirmed that repeated request hashes have byte-identical
records throughout the original cache, so sharding preserves its lookup results.
Go runs a matching parent seed when selecting a slash-named child, so a shard
for `data/<type>` or `diff/<type>` includes that parent's records as well.
Aggregate tables count each of the 70 distinct seeds once, excluding repeated
parent executions. Missing records still fail the replay.

For a short run against the complete cache, select a seed explicitly:

```sh
YANG_ORACLE_CACHE=../pairwise-cache.jsonl go test -tags oracle_full \
  -run '^TestRequestPairwiseDifferential$/^step$/^diff$/^$' \
  -count=1 -timeout=10m -v
```

The trailing `/^$` excludes descendant seeds when the selected seed is also
a prefix (for example `data`). It does not suppress required parent executions.


### Interaction regression IDs

All IDs have the `protocol-v2/` prefix. Corpus fixtures with supplied amd64 goldens:

* `insert-output-fewer-keys`
* `reply-non-operation-parent` (documented U-0110 gap)
* `prelude-xpath-bogus-flag` (schema-error: data parse flags are not initialized
  when schema loading fails)

Request-layer fixtures under `conformance/testdata/request-interactions/`:

* `insert-output-more-keys`
* `prelude-data-bogus-flag`
* `prelude-xpath-init-type`
* `prelude-atoms-bogus-flag`

The latter expect request-errors, which `cmd/golden` explicitly refuses; they
carry source/license metadata and are exercised by `TestRequestInteractions`
and `TestRequestInteractionsOracle`. XPath has no init-time flag read: its
additional fixture combines a rejected schema, bogus parse flags and a malformed
XPath field to verify the init-time string check. Data and atoms check their
printer/atom flags after the failed prelude. Diff and sequence have no fallible
local initializers between the prelude and its failure check.

### Pairwise A classes and oracle ordering

Every one of the 613 native A results was `A/execution`. The complete ledger
is classified into the following five root causes; no output mismatch is omitted.
The fixes retain the generator and comparator unchanged.

| Root cause | Original A | Resolution | Oracle ordering / interaction |
|---|---:|---|---|
| Missing diff input with `data_node` | 52 | MATCH: resolve `data_node` even when the parsed second tree is NULL | `lyoracle.c:2365–2382` makes parsing conditional on input, then resolves the selector unconditionally |
| XPath envelope type with absent/null input | 4 | MATCH: preserve the `LY_EINVAL` input diagnostic | `lyoracle.c:1284,1317,1529–1532` passes NULL input into the operation parser and reports data-error |
| Populated diff trees in separate target contexts | 554 | B `cross-context-diff`, D-0069: unsupported matching, already tracked by #236 | `lyoracle.c:2366–2386` builds the target, parses, resolves selectors, then diffs across contexts |
| Relative delete path after removing `merge` / `merge_file` | 2 | MATCH: `Tree.Find` evaluates relative paths from the retained first node's children | `lyoracle.c:2305` starts lookup at the retained first node |
| `diff_merge` with both parent and source selectors | 1 | MATCH: resolve the parent before the source node | `lyoracle.c:2446–2450` checks the parent before evaluating `src_node` |
| **Total** | **613** | | |

The 52 missing-input selector cases comprise four cases in each of the twelve
`diff/<data_type>` seeds (including `reply-with-rpc`), plus four in `step/diff`.
Both inline and file input mutations cover absent and null values.

The diff capability check occurs after parsing and both selector checks, and
requires two populated trees from distinct snapshots. Empty-tree diff cases
and request errors retain their concrete results. Relative delete lookup now
uses the shared `Tree.Find` implementation; an empty tree still produces the
oracle's `LY_ENOTFOUND`.

`TestLoggedPairwiseExecution` retains one complete native response per root
cause in `testdata/request-pairwise-execution.json`, with original request
SHA-256 digests. It uses the same full comparator and explicit B registry and
runs without a native binary or runtime cache. These records supplement the
existing 53 execution regressions and the three corpus fixtures.
