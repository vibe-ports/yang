# lyoracle — libyang v5.8.6 oracle helper (protocol 2)

Test-only C program. One JSON request on stdin, one JSON response on stdout, one request per
process. Not part of the Go module; the Go port stays cgo-free and talks to this binary only
through files/pipes in the conformance lane.

## Build and run

Authoritative (dev container, repo root; builds lyoracle against /opt/libyang):

```sh
./dev make oracle-check     # compare with committed goldens
./dev make oracle-golden    # regenerate goldens
# = cd conformance && go run ./cmd/golden [-check] [-run REGEX] [-oracle PATH]
echo '{"op":"schema","base_dir":"basic","searchdirs":["schemas"],"modules":[{"name":"basic"}]}' \
  | ./dev sh -c 'cd conformance/corpus && ../oracle/lyoracle'
```

Native (manual poking only; macOS brew libyang 5.8.6):

```sh
make -C conformance/oracle                         # LIBYANG_PREFIX=/opt/homebrew by default
```

## Pinned versions

| Item | Value |
|---|---|
| base image | `debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a` |
| libyang | tag `v5.8.6`, commit `47351e59e2965e350f9d4098f15ecbe6b3850f6f` (build fails if the tag moves) |
| pcre2 | `libpcre2-dev` / `libpcre2-8-0` `10.46-1~deb13u3` (apt-pinned) |
| compiler | gcc 14.2.0, cmake 3.31.6 (debian trixie, not apt-pinned; recorded) |
| libyang flags | `CMAKE_BUILD_TYPE=Release` (libyang adds `-DNDEBUG -O2`, cmake adds `-O3 -DNDEBUG`; effective `-O3`), `-Wall -Wextra -Wpedantic -std=c11`, `ENABLE_TESTS=OFF`, `ENABLE_YANGLINT_INTERACTIVE=OFF`, xxhash **not** installed (built-in hash) |
| install prefix | `/opt/libyang` (compiled-in `YANG_MODULE_DIR=/opt/libyang/share/yang/modules/libyang`) |
| cJSON | v1.7.19 (commit `c859b25da02955fef659d658b8f324b5cde87be3`), vendored in `cjson/`, MIT, `cjson/LICENSE` |

The pins live in the repo-root `Dockerfile` (stage `libyang`), which records them in
`/opt/libyang/BUILDINFO` inside the dev image.
Architecture is whatever docker builds for (arm64 on Apple silicon); outputs are not expected to
differ by arch, but goldens were produced on arm64.

## Common request fields

| Field | Meaning |
|---|---|
| `op` | `schema` \| `data` \| `xpath` \| `diff` \| `sequence` |
| `base_dir` | optional; `chdir` before anything, all relative paths resolve from it |
| `searchdirs` | module search dirs. libyang's installed module dir is always searched first (it holds the internal modules); CWD is never searched |
| `modules` | ordered list `{name, revision?, features?}` to **implement**; `features`: omitted = none enabled, `["*"]` = all, else list |
| `context_options` | optional list: `all_implemented`, `ref_implemented`, `no_yanglibrary`, `enable_imp_features`, `compile_obsolete`, `leafref_extended`, `leafref_linking`, `builtin_plugins_only` |

Any input `X` can be given inline (`"X": "<text>"`) or as a file (`"X_file": "path"`).

Every response has `libyang`, `protocol` (`2`; contract: `docs/design/04-oracle-protocol.md`),
`op`, `verdict`, `modules` (per module: `name`, `accepted`,
`phase` if rejected, `revision`, `diagnostics`) and `context_diagnostics`.
`verdict: "request-error"` + `request_error` means the request itself was bad (exit code 2).

### Diagnostic item

Taken from `ly_err_first(ctx)` with `ly_log_options(LY_LOSTORE)` (log callback unused), cleared
after each phase:

```json
{"phase": "data", "level": "error", "code": {"err": 7, "name": "LY_EVALID"},
 "vecode": 9, "vecode_name": "LYVE_DATA", "data_path": "/basic:sys/max",
 "schema_path": null, "apptag": "max-below-min", "line": 0, "msg": "max must not be below min"}
```

`phase` says which call produced it: `context`, `parse`, `compile`, `operational`, `rpc`, `data`,
`validate_op`, `operation_parent`, `first`, `second`, `context_path`, `xpath`, `diff`. Items with
`"source": "lyoracle"` are synthesized by the helper (see `operation_parent`). `line` is 0 when
libyang does not know it. `err` may carry the `LY_EPLUGIN` bit (name `LY_EPLUGIN|LY_E...`).

## op: schema

```json
{"op": "schema", "searchdirs": ["schemas"],
 "modules": [{"name": "basic", "features": ["jumbo"]}, {"name": "bad-compile"}]}
```
```json
{"verdict": "invalid",
 "modules": [
   {"name": "basic", "accepted": true, "revision": null, "diagnostics": [],
    "compiled": "module basic {\n  namespace ...  (lys_print_mem LYS_OUT_YANG_COMPILED)"},
   {"name": "bad-compile", "accepted": false, "phase": "compile",
    "rc": {"err": 7, "name": "LY_EVALID"},
    "diagnostics": [{"phase": "compile", "vecode_name": "LYVE_XPATH",
                     "schema_path": "/bad-compile:x", "msg": "Not found node \"nope\" in path.", "...": "..."}]}]}
```

Per accepted module (protocol 2) also:

- `schema_tree`: pre-order over the compiled nodes (`lysc_module_dfs_full`: data nodes incl.
  choice/case, after each node its actions and notifications, then module RPCs and notifications;
  rpc/action `input`/`output` are nodes). Absent facts are `null`:
  ```json
  {"path": "/pv2:c/d", "nodetype": "leaf", "module": "pv2", "config": true, "status": "current",
   "mandatory": false, "presence": null, "ordered_by": null, "keys": null,
   "min_elements": null, "max_elements": null, "defaults": ["7"],
   "type": {"base": "uint8", "typedefs": ["percent"], "range": "0..100", "length": null,
            "patterns": null, "fraction_digits": null, "enums": null, "bits": null, "bases": null,
            "leafref": null, "union": null},
   "when": null, "musts": null, "extensions": null}
  ```
  `path` = `lysc_path(LYSC_PATH_LOG)` (same form as `typed[].schema`); `nodetype` uses the `kind`
  names plus `choice`, `case`, `input`, `output`. `config` is null inside rpc/action/notification.
  `mandatory` = `LYS_MAND_TRUE` for leaf, choice, anydata/anyxml, and as libyang also sets it on
  NP containers with mandatory descendants and on (leaf-)lists with min-elements > 0; null for other
  node types. `presence` only for containers; `keys`, `ordered_by`, `min_elements`, `max_elements`
  (null = unbounded) only for lists / leaf-lists. `defaults`: canonical leaf / leaf-list defaults
  (`lyd_value_validate_dflt`; the original text if that needs a data tree), or the default case name
  of a choice. `when`: `[{"expr", "context" (lysc_path, null = root), "module" (defining module)}]`
  — for a leaf the context is the leaf itself. `musts`: `[{"expr", "apptag", "message"}]`.
  `extensions`: `[{"module", "name", "argument"}]`.
  `type`: `range`/`length` are the *compiled* intervals (`"-1.50..10.00 | 20.00"`, decimal64 with
  its fraction-digits), not the original text; `typedefs` holds only the nearest typedef (libyang
  keeps no chain); `patterns` `[{"expr", "invert"}]`; `enums` `[{"name", "value"}]`; `bits`
  `[{"name", "position"}]`; `bases` `["mod:id"]`; `leafref` `{"path", "require_instance",
  "target"}` (target via `lysc_node_lref_target`, null inside a union); `union` = member types.
- `identities`: `[{"name": "pv2:one", "bases": ["pv2:base-id"], "derived": ["pv2:two"]}]` —
  `derived` as libyang links it (direct only); `bases` found by scanning every context module.
- `features`: `[{"name": "extra", "enabled": true}]` (`lysp_feature_next` + `lys_feature_value`).

Phases: modules are processed in order in one context created with `LY_CTX_EXPLICIT_COMPILE`.
`parse` = `ly_ctx_load_module()` failed (syntax, missing import/include, unknown feature, ...);
`compile` = the following `ly_ctx_compile()` failed (libyang reverts it, later modules are still
tried). A module is judged in the context of the modules accepted before it. `compiled` is only
present for accepted modules.

## op: data

```json
{"op": "data", "searchdirs": ["schemas"], "modules": [{"name": "basic", "features": ["jumbo"]}],
 "format": "json", "data_type": "config", "data_file": "data/must.json",
 "parse_only": false, "parse_options": [], "validate_options": [],
 "with_defaults": "explicit"}
```
```json
{"verdict": "invalid", "rc": {"err": 7, "name": "LY_EVALID"},
 "diagnostics": [{"...": "..."}], "tree": null}
```
On success `tree` is `{"json": "<lyd_print_mem JSON>", "xml": "<lyd_print_mem XML>"}`, printed with
`with_defaults` (`explicit` | `trim` | `all` | `all-tagged` | `implicit-tagged`).

`data_type` presets copy yanglint (`tools/lint/yl_opt.c`); all start from the `unknown` policy flag
(below) + `LYD_VALIDATE_MULTI_ERROR`:

| data_type | parse | validate | call |
|---|---|---|---|
| `data-operational` (default) | — | `+OPERATIONAL` (config violations become warnings) | `lyd_parse_data` |
| `data` | — | — | `lyd_parse_data` |
| `config` | `+NO_STATE` | `+NO_STATE` | `lyd_parse_data` |
| `get` | `+ONLY` | — | `lyd_parse_data` |
| `getconfig`, `edit` | `+ONLY +NO_STATE` | — | `lyd_parse_data` |
| `rpc`, `reply`, `notif` | `unknown` flag only | `lyd_validate_op(tree, operational, type)` | `lyd_parse_op(LYD_TYPE_*_YANG)` |

`parse_only: true` adds `LYD_PARSE_ONLY` (and skips `lyd_validate_op`). `parse_options` /
`validate_options` add flags: parse `only no_state ordered when_true store_only json_null
json_string_datatypes anydata_strict`; validate `no_state present multi_error operational
no_defaults not_final`. `strict`/`opaq` in `parse_options` are a request-error since protocol 2.

`unknown` (all data parsing, also `sequence` parse/merge steps; not the `operational` tree, which
stays parse-only without STRICT) — what happens to data nodes the schema does not know:

| unknown | flag | effect |
|---|---|---|
| `reject` (default) | `LYD_PARSE_STRICT` | error `LYVE_REFERENCE` (`Node "bogus" not found as a child of "c" node.`) |
| `skip` | — | silently dropped (libyang default); request-error for `rpc`/`reply`/`notif` |
| `opaque` | `LYD_PARSE_OPAQ` | kept as opaque nodes (`kind: "opaque"` in `typed`) |

Example (`protocol-v2/data/unknown.json` = `{"pv2:c": {"mode": "on", "bogus": 1}}`, `config`,
`parse_only`): `reject` → `invalid`; `skip` → `valid`, typed `/pv2:c`, `/pv2:c/mode`; `opaque` →
`valid`, plus `{"path": "/pv2:c/bogus", "schema": null, "kind": "opaque", "value": {"canonical":
"1", ...}}`. For operations libyang validation still rejects opaque nodes (`validate_op`).

Operations: `operational` / `operational_file` (+ `operational_format`, default = `format`) is
parsed `LYD_PARSE_ONLY` and used as the dependency tree. For a nested action/notification the
helper additionally requires its parent to exist in the operational tree (yanglint
`check_operation_parent`; libyang does not check it) → diagnostic `phase: operation_parent`.
For `reply`, `rpc` / `rpc_file` is the request: it is parsed as RPC, its input is dropped and the
reply (`data`, output children as top-level nodes, e.g. `{"ops:rtt": 5}`) is parsed under it.
Without `rpc`, `data` must be the full reply (`{"ops:ping": {"rtt": 5}}`).

### Typed tree (`typed`)

Every response that yields a data tree carries, next to the printer output, `typed`: the tree in
pre-order (siblings in libyang order, list keys first), one object per node. Present in `data`
(when `tree` is not null), `diff` (the diff tree, when not null) and after every executed
`sequence` step (possibly `[]`); omitted when there is no tree.

```json
{"path": "/pv2:c/d",                 // lyd_path(LYD_PATH_STD)
 "schema": "/pv2:c/d",               // lysc_path(LYSC_PATH_LOG): includes choice/case, input/output; null for opaque
 "kind": "leaf",                     // container|list|leaf|leaflist|anydata|anyxml|opaque|rpc|action|notif
 "flags": {"default": true, "when_true": false, "new": false},    // LYD_DEFAULT / LYD_WHEN_TRUE / LYD_NEW
 "value": {"canonical": "7",         // lyd_get_value()
           "type": "uint8",          // value.realtype basetype (YANG name); a leafref reports its target's type
           "typedef": "percent",     // value.realtype->name (nearest typedef) or null
           "union_member": null},    // unions: {"type", "typedef"} of subvalue->value.realtype
 "meta": [],                         // [{module, name, value}]; internal meta (lyd_meta_is_internal) skipped
 "any": null}                        // anydata/anyxml: {"value_type": "datatree"|"string"|null, "text": "…"}
```
`value` is null for inner nodes. Opaque nodes: `canonical` is the original text, the type fields
are null, attributes are listed in `meta` (`module` = module name for JSON input, namespace for
XML). Union example: `u` = `"abc"` → `"value": {"canonical": "abc", "type": "union", "typedef":
null, "union_member": {"type": "string", "typedef": null}}`; `-5` → member `int8`. `any.text` is
`lyd_any_value_str(LYD_JSON)`; anydata content is not listed as separate nodes.

## op: xpath

```json
{"op": "xpath", "...data fields...": "", "data_file": "data/valid.json",
 "context_path": "/basic:sys", "xpath": "count(iface[type = 'eth'])", "cur_module": null}
```
```json
{"verdict": "valid", "result": {"type": "number", "value": 1}}
```
`result.type`: `node-set` (`nodes`: list of `lyd_path(LYD_PATH_STD)`), `string`, `number`
(`NaN`/`Infinity`/`-Infinity` as strings), `boolean`. The tree is parsed/validated as in `data`
(`verdict: "data-error"` if that fails). `context_path` is an XPath that must select exactly one
node; omitted = document root.

## op: diff

```json
{"op": "diff", "...data fields...": "", "first_file": "data/valid.json",
 "second_file": "data/valid2.json", "diff_options": ["defaults"]}
```
```json
{"verdict": "valid", "diff": {"json": "{ \"basic:sys\": { \"@\": {\"yang:operation\": \"none\"}, ...", "xml": "..."}}
```
Both trees are parsed/validated as in `data` (datastore types only). `lyd_diff_siblings()` with
`diff_options` (`defaults`, `meta`); the diff is printed with `LYD_PRINT_WD_ALL`; `diff: null` when
equal.

## op: sequence

Stateful run on one retained data tree, so `LYD_WHEN_TRUE` / `LYD_DEFAULT` / `LYD_NEW` history
becomes observable. Context fields as in `schema`; step fields are per step (not inherited).

```json
{"op": "sequence", "base_dir": "protocol-v2", "searchdirs": ["schemas"], "modules": [{"name": "pv2"}],
 "steps": [
   {"do": "parse", "format": "json", "data_type": "config", "data_file": "data/valid.json"},
   {"do": "edit", "set": {"path": "/pv2:c/mode", "value": "off"}},
   {"do": "validate", "data_type": "config"},
   {"do": "edit", "delete": "/pv2:c/nope"},
   {"do": "dump", "with_defaults": "trim"}]}
```

| do | fields | libyang |
|---|---|---|
| `parse` | `format`, `data_type` (datastore types only), `data`/`data_file`, `unknown`, `parse_only`, `parse_options`, `validate_options` | as op `data`; on success replaces the tree |
| `validate` | `data_type`, `validate_options` (validate flags of the preset) | `lyd_validate_all(&tree, ctx, opts, &diff)` |
| `edit` | exactly one of `merge` / `merge_file` (+ `format`, `data_type`, `unknown`) | `lyd_parse_data(… LYD_PARSE_ONLY …)` + `lyd_merge_siblings(LYD_MERGE_DESTRUCT)` |
| | `set: {"path", "value"}` (value in JSON format, omit for containers/lists) | `lyd_new_path(tree, ctx, path, value, LYD_NEW_PATH_UPDATE)` |
| | `delete: "<path>"` | `lyd_find_path` + `lyd_free_tree` (`LY_EINCOMPLETE` if only a parent exists) |
| `dump` | `with_defaults` | `tree` = `{"json", "xml"}` as op `data` |

Response: `steps` (one per request step), `verdict` (`valid` iff every step returned
`LY_SUCCESS`), `rc` (of the last executed step), `failed_step` (index or null). Executed step:
`{"do", "diagnostics" (phase `parse` | `validate` | `edit`), "rc", "typed"}`, plus
`implicit_diff` for `validate` (the diff printed as JSON with `LYD_PRINT_WD_ALL`, carrying
`yang:operation`; null when validation changed nothing) and `tree` for `dump`. The first step whose
rc is not `LY_SUCCESS` stops the run; later steps are `{"do": "...", "skipped": true}`. A failed
step reports the tree as it is after the call (a failed `parse` keeps the previous tree).

For the request above (`x` has `when "../mode = 'on'"`): step 0 `typed` has `/pv2:c/x` with
`when_true: true` and the implicit default `/pv2:c/d` with `default: true`; step 1 marks `mode`
`new: true`; step 2 auto-deletes `x` (it had `when_true`) and returns (pretty-printed in reality)
```json
"implicit_diff": "{\"pv2:c\": {\"@\": {\"yang:operation\": \"none\"}, \"x\": \"hi\", \"@x\": {\"yang:operation\": \"delete\"}}}"
```
step 3 fails with `LYVE_XPATH` (`Not found node "nope" in path.`), step 4 is `skipped`,
`verdict: "invalid"`, `failed_step: 3`.

## Known limitations

- XPath uses `lyd_eval_xpath4` (format `LY_VALUE_JSON`, no variables): node-set results keep only
  element nodes (root, text and metadata nodes are dropped by libyang), `when` is ignored during
  evaluation (`LYXP_IGNORE_WHEN`), the tree must be non-empty, numbers go through `double`.
  Schema-node XPath (`lys_find_xpath`/atomize) is not exposed.
- `with_defaults: all-tagged|implicit-tagged` emit `default` metadata only if
  `ietf-netconf-with-defaults` is in the context — add it to `searchdirs` + `modules`.
- No NETCONF/RESTCONF envelopes (`nc-rpc`, ...), LYB, yang-library contexts, merged multi-file
  input, extension-instance data, `lyd_validate_module`, sequences over operations
  (rpc/reply/notif), insert/move edits — add when a fixture needs them.
- `typed`: xpath responses carry none; libyang v5 anydata holds only a data tree or a string, so
  `value_type` is never `xml`/`json`/`lyb`; `with_defaults` tagging is printer-only, not tree
  metadata. Schema dumps skip parsed-only facts (typedef chains, original range text, `units`,
  descriptions, if-features, uniques).
- Compile phase is judged per module *in order*; the same set in another order can blame a
  different module.
- `msg` text is for humans; conformance compares code/vecode/paths/apptag/level only.
- Native brew libyang is a different build (flags, xxhash) — goldens come from the docker image.
