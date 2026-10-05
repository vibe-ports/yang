# 04 — Oracle protocol v2 and fixture manifest v2 (M1-pre)

Status: contract for M1-pre. v1 = conformance/oracle/README.md. Every response gains
`"protocol": 2`. All v1 fields keep their meaning unless changed below.

## 1. Unknown-node policy (replaces the forced `LYD_PARSE_STRICT`)
Request field `unknown`: `reject` (default → `LYD_PARSE_STRICT`), `skip` (no STRICT: unknown
data silently dropped, libyang default), `opaque` (`LYD_PARSE_OPAQ`: kept as opaque nodes).
`parse_options` may no longer contain `strict`/`opaq` (request-error) — `unknown` is the only knob.
Operations (`lyd_parse_op`) accept `reject` and `opaque` only. `opaque` only keeps the nodes:
libyang validation rejects them (datastore data and operations alike).

## 2. Typed tree dump (all ops that produce a data tree)
Next to the printer output (`tree.json` / `tree.xml`, kept) the response carries `typed`: the tree
in pre-order (siblings in libyang order), one object per node:
```json
{"path": "/basic:sys/iface[name='eth0']/type",      // lyd_path(LYD_PATH_STD)
 "schema": "/basic:sys/iface/type",   // lysc_path(LYSC_PATH_LOG): with choice/case, input/output; null for opaque
 "kind": "leaf",   // container|list|leaf|leaflist|anydata|anyxml|opaque|rpc|action|notif
 "flags": {"default": false, "when_true": true, "new": false},   // LYD_DEFAULT/WHEN_TRUE/NEW
 "value": {"canonical": "eth", "type": "enumeration",           // realtype basetype name
           "typedef": "iface-type",                             // realtype typedef name or null
           "union_member": null},    // unions: {"index", "type", "typedef", "realtype": {"type", "typedef"}}
 "meta": [{"module": "ietf-netconf-with-defaults", "name": "default", "value": "true"}],
 "any": null}   // anydata/anyxml: {"value_type": "datatree"|"string"|null, "text": "…"}
```
libyang v5 anydata holds only a data tree or a string, so `value_type` has no xml/json/lyb.
`text` (JSON) loses XML namespaces, so a `datatree` payload is also dumped as nodes right after
the anydata node (opaque payload nodes carry their namespace in `opaque`).
`union_member` is the first union member whose type (leafref: its realtype) is the stored realtype
— libyang does not record the member, this is its own ordering rule (`lyplg_type_sort_union`);
two members with the same realtype report the earlier one. Opaque nodes: `value.canonical` is the
original text, type fields null, `value.hints` the parser's JSON type hints, attributes in `meta`,
and `"opaque": {"name", "prefix", "format", "namespace" (XML), "module" (JSON)}` (null for
schema-bound nodes) so XML namespaces are compared.
`typed` is omitted for `verdict != valid` unless the tree exists (parse_only / operational warnings).

## 3. Structured compiled schema (op `schema`)
Per accepted module, next to the existing `compiled` text (kept for humans): `schema_tree`, pre-order
over `lysc` nodes incl. rpc/action input/output and notifications:
```json
{"path": "/basic:sys/mtu", "nodetype": "leaf", "module": "basic", "config": true,
 "status": "current", "mandatory": false, "presence": false, "ordered_by": null, "keys": null,
 "min_elements": null, "max_elements": null, "defaults": ["1500"],
 "type": {"base": "uint16", "typedefs": ["mtu-type"], "range": "68..9000", "length": null,
          "patterns": [{"expr": "…", "invert": false}], "fraction_digits": null,
          "enums": null, "bits": null, "bases": null,
          "leafref": null /* {"path": "…", "require_instance": true, "target": "/m:…"} */,
          "union": null /* [type objects] */},
 "when": [{"expr": "../x = 'y'", "context": "/basic:sys", "module": "basic"}],
 "musts": [{"expr": "…", "apptag": "…", "message": "…"}],
 "extensions": [{"module": "…", "name": "…", "argument": "…"}]}
```
plus `identities`: `[{"name": "m:id", "bases": [...], "derived": [...]}]` and `features`
(enabled/disabled). Absent facts are `null`, not omitted. As implemented: `path` is
`lysc_path(LYSC_PATH_LOG)`; `typedefs` holds only the nearest typedef (compiled types keep no
chain); `range`/`length` are the compiled intervals, not the original text; `max_elements: null`
= unbounded; a choice's `defaults` is its default case name; a leafref member of a union gets its
target only when `lysc_node_lref_targets` resolves one per leafref member (it skips unresolved).

## 4. op `sequence` — stateful runs on one retained tree
```json
{"op": "sequence", "searchdirs": [...], "modules": [...],
 "steps": [
   {"do": "parse", "format": "json", "data_type": "config", "data_file": "d1.json", "unknown": "reject"},
   {"do": "validate", "data_type": "config"},             // lyd_validate_all, returns implicit diff
   {"do": "edit", "merge_file": "e1.json"},               // lyd_parse_data ONLY + lyd_merge_siblings
   {"do": "edit", "set": {"path": "/m:c/x", "value": "5"}},  // lyd_new_path UPDATE / lyd_change_term
   {"do": "edit", "delete": "/m:c/y"},                    // lyd_find_path + lyd_free_tree
   {"do": "validate", "data_type": "config"},
   {"do": "dump", "with_defaults": "all-tagged"}]}
```
All steps are checked (request-error, also for keys a step kind — or edit kind: merge takes the
parse options, `set` and `delete` nothing else — does not take, and for empty keys) before any runs. Response: `steps`: one object per step with `rc`, `diagnostics`, and for `validate` the
`implicit_diff` (lyd_validate_all's diff: defaults added, nodes auto-deleted by `when`, printed as
JSON with `yang:operation`), for every step the resulting `typed` dump. The sequence stops at the
first failing step — rc not LY_SUCCESS or an error-level diagnostic logged (e.g. `lyd_free_tree`
refusing a list key) — later steps are `{"do", "skipped": true}`; the response adds `failed_step`
(index or null). `set` is `lyd_new_path` with `LYD_NEW_PATH_UPDATE` only. This is what makes
`WhenTrue`/`Default`/`New` history observable.

## 5. Manifest v2 (conformance/corpus/manifest.yaml)
```yaml
version: 2
fixtures:
  - id: basic/range
    dir: basic
    source: {url: …, commit: …, license: …}
    rfc: ["RFC7950#9.2.4"]
    areas: [types]            # schema|types|xpath|validation|defaults|codecs|diff|operations|nmda
    request: {…}              # oracle request (protocol v2)
    golden: golden/range.json # OBSERVED: generated by the oracle, never hand-edited
    assert:                   # NORMATIVE (optional): what the RFC requires, hand-written
      verdict: invalid
      diagnostics: [{vecode_name: LYVE_DATA, data_path: /basic:sys/mtu}]   # subset match
      deviation: null         # or "D-0001" when libyang (the golden) is known to differ
```
Rules: goldens record what libyang does; `assert` records what the spec requires. A fixture whose
`assert` contradicts its golden must name a deviation id from `conformance/deviations.md`,
otherwise the harness fails. Without a deviation our engine must match the `assert` (if present) AND the whole normalized golden.
With a deviation (an intentional difference of ours from libyang): matching the assert but not the
golden = `deviation`; not matching the assert = `differ`.

## 6. Go harness (separate module `github.com/vibe-ports/yang/conformance`)
- `go run ./cmd/golden [-check] [-run REGEX]` — runs lyoracle per fixture (replaces run_corpus.py):
  manifest validation (version, unique ids, required fields, files exist), 60 s timeout, fails on
  non-zero exit / `request-error` / missing `protocol` (flag `-require-protocol`; `make oracle-check`/`oracle-golden` pass it since oracle v2), writes or compares goldens byte-exactly.
- `go test ./...` — manifest well-formed, every assert consistent with its golden (or deviation).
- `Engine` interface (`Run(Request) (Response, error)`) + `Compare(e Engine) Report`; per-area report
  (agree / differ / deviation / unsupported) as Markdown for the CI job summary. No engine exists yet;
  a fake engine that replays goldens proves the plumbing.
