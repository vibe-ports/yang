# lyoracle — libyang v5.8.6 oracle helper (protocol 2)

Test-only C program. One JSON request on stdin, one JSON response on stdout, one request per
process. Not part of the Go module; the Go port stays cgo-free and talks to this binary only
through files/pipes in the conformance lane.

## Build and run

Authoritative (dev container, repo root; builds lyoracle against /opt/libyang):

```sh
./dev make oracle-check     # compare with committed goldens
./dev make oracle-golden    # regenerate goldens
# = cd conformance && go run ./cmd/golden [-check] -require-protocol [-run REGEX] [-oracle PATH]
# requests: the fixtures' `request` (conformance/corpus/manifest.d/<set>/<name>.yaml, one file per
# fixture, format conformance/manifest.schema.md); -run matches fixture ids
# one ad-hoc request (./dev passes stdin through):
echo '{"op":"schema","base_dir":"basic","searchdirs":["schemas"],"modules":[{"name":"basic"}]}' \
  | ./dev sh -c 'cd conformance/corpus && ../oracle/lyoracle-$(uname -m)'
```

Native (manual poking only; macOS brew libyang 5.8.6):

```sh
make -C conformance/oracle                         # LIBYANG_PREFIX=/opt/homebrew by default
# builds lyoracle-$(uname -m): one binary per architecture, so containers of different
# DEV_PLATFORMs sharing the checkout never run each other's binary
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
The canonical architecture is **linux/amd64** (CI and typical deployments): libyang computes XPath
numbers in C `long double`, 80-bit x87 on amd64 but 128-bit on arm64, so e.g. `string(0.15)` differs.
`cmd/golden` refuses to write goldens from a non-x86_64 oracle and to `-check` when the oracle's
`arch` differs from the Go process's; conformance tests that exec the oracle check it too.
Produce goldens and `internal/xpath/testdata/oracle-pv2.jsonl` with
`DEV_PLATFORM=linux/amd64 ./dev make oracle-golden` (and `… ./dev go test -tags oracle ./internal/xpath/
-run Oracle -update`); on Apple silicon this runs under emulation.

## Common request fields

| Field | Meaning |
|---|---|
| `op` | `schema` \| `data` \| `xpath` \| `atoms` \| `diff` \| `sequence` |
| `base_dir` | optional; `chdir` before anything, all relative paths resolve from it |
| `searchdirs` | module search dirs. libyang's installed module dir is always searched first (it holds the internal modules); CWD is never searched |
| `modules` | ordered list `{name, revision?, features?}` to **implement**; `features`: omitted = none enabled, `["*"]` = all, else list |
| `context_options` | optional list: `all_implemented`, `ref_implemented`, `no_yanglibrary`, `enable_imp_features`, `compile_obsolete`, `leafref_extended`, `leafref_linking`, `builtin_plugins_only`, `pattern_compat` (no libyang flag: libyang always compiles patterns with PCRE2; it tells the Go engine to set `Options.PatternCompat`, D-0031) |

Any input `X` can be given inline (`"X": "<text>"`) or as a file (`"X_file": "path"`).

Every response has `libyang`, `arch` (`uname -m` of the oracle; additive, ignored by the comparator and
not stored in goldens), `protocol` (`2`; contract: `docs/design/04-oracle-protocol.md`),
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
`validate_op`, `operation_parent`, `first`, `second`, `context_path`, `xpath`, `path`, `diff`. Items with
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
  (`lyd_value_validate_dflt`, also when it returns `LY_EINCOMPLETE` because a leafref /
  instance-identifier instance check needs data; the original text on any other failure), or the default case name
  of a choice. `when`: `[{"expr", "context" (lysc_path, null = root), "module" (defining module)}]`
  — for a leaf the context is the leaf itself. `musts`: `[{"expr", "apptag", "message"}]`.
  `extensions`: `[{"module", "name", "argument"}]`.
  `type`: `range`/`length` are the *compiled* intervals (`"-1.50..10.00 | 20.00"`, decimal64 with
  its fraction-digits), not the original text; `typedefs` holds only the nearest typedef (libyang
  keeps no chain); `patterns` `[{"expr", "invert"}]`; `enums` `[{"name", "value"}]`; `bits`
  `[{"name", "position"}]`; `bases` `["mod:id"]`; `leafref` `{"path", "require_instance",
  "target"}` (`lysc_node_lref_target`; for leafref members of a union the targets of
  `lysc_node_lref_targets`, attributed in member order, null for every member when that list
  is shorter than the leafref members — it skips members whose target does not resolve); `union` =
  member types.
- `identities`: `[{"name": "pv2:one", "bases": ["pv2:base-id"], "derived": ["pv2:two"]}]` —
  `derived` as libyang links it (direct only); `bases` found by scanning every context module.
- `features`: `[{"name": "extra", "enabled": true}]` (`lysp_feature_next` + `lys_feature_value`).
- `ext_trees` (only when a top-level extension instance of the module compiled a schema subtree:
  yang-data, structure): `[{"module", "name", "argument", "schema_tree"}]` per instance, in
  `lysc_module.exts` order; `module`/`name` are the extension definition's. `schema_tree` holds the
  same node objects as above, pre-order (`lysc_tree_dfs_full`) from the root of every data-def
  substatement storage (`lysc_ext_instance.substmts`, each storage once), so structure's virtual
  top-level container (`/m:<argument>`, carrying the structure's must/status) comes first.

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
`with_defaults` (`explicit` | `trim` | `all` | `all-tagged` | `implicit-tagged`) plus
`print_options` (`empty_leaf_list` = `LYD_PRINT_EMPTY_LEAF_LIST`, JSON only in libyang).
`print_subtree: "<path>"` adds `subtree` in the same shape: the node `lyd_find_path` finds, printed
without `LYD_PRINT_SIBLINGS` (= `lyd_print_tree`), with the same options; a path that finds nothing
is a request-error.

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
"1", "hints": ["decnum"], ...}}`. `opaque` only keeps the nodes: validation rejects them for
datastore data as for operations (fixture `protocol-v2/unknown-opaque-validated`: without
`parse_only` the same input is `invalid`, `LYVE_REFERENCE` at `/pv2:c`).

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
           "union_member": null},    // unions: the member that stored the value, see below
 "meta": [],                         // [{module, name, value}]; internal meta (lyd_meta_is_internal) skipped
 "any": null}                        // anydata/anyxml: {"value_type": "datatree"|"string"|null, "text": "…"}
```
`value` is null for inner nodes. Opaque nodes: `canonical` is the original text, the type fields
are null, `hints` lists the parser's value/node hints (the JSON type the value came as:
`string`, `decnum`, `octnum`, `hexnum`, `num64`, `boolean`, `empty`, `string_datatypes`, `list`,
`leaflist`, `container`; `1` → `["decnum"]`, `"1"` → `["string"]`), attributes are listed in
`meta` (`module` = module name for JSON input, namespace for XML). Opaque nodes also carry
`"opaque": {"name", "prefix", "format": "xml"|"json", "namespace" (XML), "module" (JSON, inherited)}`
— `struct ly_opaq_name` as libyang stores it; `path` alone does not show an XML namespace (fixtures
`protocol-v2/opaque-xml-ns-a` / `-b` differ only there). Schema-bound nodes have `"opaque": null`. `any.text` is `lyd_any_value_str(LYD_JSON)`,
which loses XML namespaces; the payload tree of a `datatree` anydata/anyxml is therefore also
listed, right after the anydata node (`lyd_child_any`), e.g. `/pv2:c/any/foo` with `"opaque":
{"namespace": "urn:a", ...}` (fixtures `protocol-v2/anydata-xml-ns-a` / `-b`). Payload nodes
libyang could bind to a schema (JSON content of a known module) appear as schema-bound nodes.

`union_member` = `{"index", "type", "typedef", "realtype": {"type", "typedef"}}`: `index`, `type`
and `typedef` describe the union member (unions are flattened by libyang), `realtype` the type the
value is stored as. libyang keeps only the stored realtype (a leafref member stores its target's
type), so the member is found the way `lyplg_type_sort_union()` orders values: the first member
whose type, or leafref realtype, is that realtype. When an earlier member shares the realtype
(same typedef as a later leafref's target), the earlier one is reported; `index: null` if nothing
matches. Examples (`pv2` `u2`: `percent | leafref ../l/k`): `"a"` → {"index": 1, "type": "leafref", "typedef": null, "realtype": {"type": "string", "typedef": null}}; `"50"` →
`{"index": 0, "type": "uint8", "typedef": "percent", ...}`.

## op: xpath

```json
{"op": "xpath", "...data fields...": "", "data_file": "data/valid.json",
 "context_path": "/basic:sys", "xpath": "count(iface[type = 'eth'])", "cur_module": null,
 "vars": {"min": "3"}}
```
```json
{"verdict": "valid", "result": {"type": "number", "value": 1}}
```
`result.type`: `node-set` (`nodes`: list of `lyd_path(LYD_PATH_STD)`), `string`, `number`
(`NaN`/`Infinity`/`-Infinity` as strings), `boolean`. The tree is parsed/validated as in `data`
(`verdict: "data-error"` if that fails). `context_path` is an XPath that must select exactly one
node; omitted = document root. `vars` (optional) binds XPath variables: each member goes to
`lyxp_vars_set` in member order (the value is an XPath expression, `"'x'"` for a string) and the
list to `lyd_eval_xpath4`.

## op: atoms

```json
{"op": "atoms", "searchdirs": ["a.1"], "modules": [{"name": "a"}],
 "context_path": "/a:c/ll", "xpath": "ll[a = current()/a]/b", "atom_options": []}
```
```json
{"verdict": "valid", "rc": {"err": 0, "name": "LY_SUCCESS"}, "diagnostics": [],
 "atoms": ["/a:c/ll", "/a:c/ll/ll", "/a:c/ll/ll/a", "/a:c/ll/a", "/a:c/ll/ll/b"]}
```
The schema nodes an expression needs, without data. `xpath` goes to `lys_find_xpath_atoms`, `path`
(a JSON data path, simple predicates) to `lys_find_path_atoms`; exactly one of them. `context_path`
is a schema path for `lys_find_path` (output nodes when `atom_options` has `output`); omitted = the
document root. `atom_options`: `schema` (`LYS_FIND_XP_SCHEMA`, the when/must accessible tree),
`output` (`LYS_FIND_XP_OUTPUT`, the `output` argument of `lys_find_path_atoms`), `no_match_error`
(`LYS_FIND_NO_MATCH_ERROR`: a step that matches nothing is an `LY_ENOTFOUND` error, not a warning);
`path` reads only `output`. `atoms` is the result set in order, each node as
`lysc_path(LYSC_PATH_LOG)`; `null` on error (`verdict: "invalid"`). A rejected module gives
`verdict: "schema-error"`.

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
| `edit` | exactly one of `merge` / `merge_file` (+ `format`, `data_type`, `unknown`, `parse_options`, merge only) | `lyd_parse_data(… LYD_PARSE_ONLY …)` + `lyd_merge_siblings(LYD_MERGE_DESTRUCT)` |
| | `set: {"path", "value"}` only (value in JSON format, omit for containers/lists) | `lyd_new_path(tree, ctx, path, value, LYD_NEW_PATH_UPDATE)` (an existing default leaf-list instance is left untouched: use `insert_term` to make it explicit) |
| | `delete: "<path>"` only | `lyd_find_path` + `lyd_free_tree` (`LY_EINCOMPLETE` if only a parent exists; a list key is refused, see below) |
| | `insert_term: {"module", "parent" \| "parent_opaq", "name", "value", "options"}` only | `lyd_new_term` with `options` (`output store_only canon`: LYD_NEW_VAL_*); a module and/or a parent: `parent` is the path of an existing node, `parent_opaq` the name of a top-level opaque node (with a module: libyang searches its top level); without a parent the node is inserted with `lyd_insert_sibling` |
| | `insert_inner: {"module", "parent" \| "parent_opaq", "name", "options"}` only | `lyd_new_inner` (`options`: `output`), inserted like `insert_term` |
| | `insert_list: {"module", "parent" \| "parent_opaq", "name", "keys", "options"}` only | `lyd_new_list3` (`keys`: array of strings or nulls, at most 16; absent: a NULL array; fewer values than the list's keys: request-error), inserted like `insert_term` |
| | `insert_list2: {"module", "parent" \| "parent_opaq", "name", "keys", "options"}` only | `lyd_new_list2` (`keys`: the predicates string, absent: NULL), inserted like `insert_term` |
| | `insert_opaq: {"parent" \| "parent_opaq", "name", "value", "prefix", "module", "xml"}` only | `lyd_new_opaq` (`module`: the module name), with `xml: true` `lyd_new_opaq2` (`module`: the namespace); top level: `lyd_insert_sibling` |
| | `new_meta: {"node", "name", "value"}` only (`name` = `module:name`, value in JSON format) | `lyd_find_path` + `lyd_new_meta(NULL, node, NULL, name, value, 0, NULL)` |
| | `free_meta: {"node", "name"}` only (`name` = `module:name`) | `lyd_find_path` + `lyd_free_meta_single(lyd_find_meta(node->meta, NULL, name))` (nothing found: nothing freed) |
| `change_term` | `node` (path), `value`, `canon` (bool) | `lyd_change_term` (`canon`: `lyd_change_term_canon`) of the node; `change` = its rc, where `LY_EEXIST` (only the default flag changed) and `LY_ENOT` (no change) are results and the step succeeds (diagnostics phase `edit`) |
| `dump` | `with_defaults` | `tree` = `{"json", "xml"}` as op `data` |
| `link` | — | `lyd_leafref_link_node_tree(tree)` (`LY_EDENIED` without the `leafref_linking` context option) |
| `links` | — | `leafref_links`: the record of every term node that has one (`lyd_leafref_get_links`), in DFS order: `{"node", "leafref_nodes", "target_nodes"}` as `lyd_path(LYD_PATH_STD)` lists in record order |
| `dup` | `node` (path), `parent` (path, optional), `options` (`recursive no_meta with_parents with_flags no_lyds`), `siblings` (bool) | `lyd_dup_siblings` (`siblings: true`) or `lyd_dup_single` of `node` into `parent`; without `parent` the duplicate, from its top duplicated parent, replaces the tree (diagnostics phase `edit`); with `target` (`searchdirs`, `modules`, `context_options`, no `parent`) `lyd_dup_single_to_ctx` / `lyd_dup_siblings_to_ctx` into a second context built like the request's, the duplicate replacing the tree (its diagnostics: the request context's, then the target's). Every step runs in the retained tree's context, the target's after such a dup |
| `compare` | the fields of `parse` for a second tree, `first` / `second` (paths in the retained / second tree, omitted: its first top-level node), `options` (`full_recursion defaults opaq`) | `lyd_compare_single(first, second, options)`; `compare` = its rc (`LY_SUCCESS` equal, `LY_ENOT` not); the step's rc is the second parse's, the retained tree is unchanged |
| `diff` | the parse fields of `parse` (`data`/`data_file` optional: none is a NULL tree), `node` (path in the tree), `data_node` (path in the parsed data), `single` (bool), `options` (`defaults meta`), `merge` (bool), `merge_options` (`defaults`) | `lyd_diff_siblings` (`single`: `lyd_diff_tree`) of the tree (or its node at `node`) and the parsed data (or its node at `data_node`); the diff replaces the diff register, or with `merge` is merged into it (`lyd_diff_merge_all` with `merge_options`) and reported as `new_diff` (diagnostics phases `parse`, `diff`) |
| `diff_parse` | `format`, `data_type`, `data`/`data_file`, `unknown`, `parse_options` | the data, parsed with `LYD_PARSE_ONLY` as test_diff.c parses diffs, replaces the diff register |
| `diff_merge` | as `diff_parse`, plus `options` (`defaults`), and `module` or `src_node` (path in the parsed source) with an optional `parent` (path in the register) | `lyd_diff_merge_module` (`lyd_diff_merge_all` without `module`) of the parsed source into the register; with `src_node` `lyd_diff_merge_tree` of that subtree under `parent` (none: the top level) |
| `diff_apply` | `module` (optional) | `lyd_diff_apply_module(&tree, register, module)` (`lyd_diff_apply_all` without `module`) |
| `diff_reverse` | — | `lyd_diff_reverse_all` of the register replaces it |
| `trim` | `xpath`, `vars` (object of strings, as op `xpath`) | `lyd_trim_xpath(&tree, xpath, vars)`: every node neither selected nor an ancestor of a selected node is freed (diagnostics phase `xpath`) |

The diff register is the second state of a sequence next to the tree: NULL at the start, set by
`diff`, `diff_parse` and `diff_reverse`, merged into by `diff_merge` and `diff` with `merge`. Every
executed `diff*` step reports it after the call as `diff` (`{"json", "xml"}` printed with
`LYD_PRINT_WD_ALL`, as op `diff`, or null) and `diff_typed` (its typed dump). A `node`, `data_node`,
`src_node` or `parent` path that selects nothing is a request-error raised when the step runs.

An `insert_*` edit with a `parent` path or a `parent_opaq` name, and `new_meta` / `free_meta` with a `node` path, that does not exist in the tree is a request-error raised when the step runs (not in the pre-check).

All steps are checked before the first runs (unknown `do`, keys not listed in the table for that
step or edit kind or in `set` — matched as whole names, an empty key never matches — missing/extra
edit fields, bad enums, unreadable files → request-error), so a run that stops early never hides a malformed later step.
A step fails when its call returns an error **or** it logged an error-level diagnostic (void
APIs: `lyd_free_tree` refuses a list key with `LY_EINVAL` "Cannot free a list key"); its `rc` is
then that item's code.

Response: `steps` (one per request step), `verdict` (`valid` iff every step succeeded), `rc` (of the last executed step), `failed_step` (index or null). Executed step:
`{"do", "diagnostics" (phase `parse` | `validate` | `edit`), "rc", "typed"}`, plus
`diff`/`diff_typed` (and `new_diff` for `diff` with `merge`) for the `diff*` steps,
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
`verdict: "invalid"`, `failed_step: 3`. Fixture `protocol-v2/sequence-edits` covers `merge_file`,
`delete`, a non-null then a null `implicit_diff`, and the refused key delete.

The Go harness (`conformance/`) reads `steps[].diagnostics` for asserts, ignores their
`msg`/`line` and `steps[].tree.xml`, and a deviation waives `failed_step` and every step's
`rc`/`diagnostics` (not the steps' trees or `skipped`).

## Known limitations

- XPath uses `lyd_eval_xpath4` (format `LY_VALUE_JSON`, no variables): node-set results keep only
  element nodes (root, text and metadata nodes are dropped by libyang), `when` is ignored during
  evaluation (`LYXP_IGNORE_WHEN`), the tree must be non-empty, numbers go through `double`.
  Schema-node XPath is exposed only as atoms (`lys_find_xpath_atoms`, `lys_find_path_atoms`), not as
  `lys_find_xpath`.
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
