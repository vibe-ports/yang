# lyoracle — libyang v5.8.6 oracle helper

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
| `op` | `schema` \| `data` \| `xpath` \| `diff` |
| `base_dir` | optional; `chdir` before anything, all relative paths resolve from it |
| `searchdirs` | module search dirs. libyang's installed module dir is always searched first (it holds the internal modules); CWD is never searched |
| `modules` | ordered list `{name, revision?, features?}` to **implement**; `features`: omitted = none enabled, `["*"]` = all, else list |
| `context_options` | optional list: `all_implemented`, `ref_implemented`, `no_yanglibrary`, `enable_imp_features`, `compile_obsolete`, `leafref_extended`, `leafref_linking`, `builtin_plugins_only` |

Any input `X` can be given inline (`"X": "<text>"`) or as a file (`"X_file": "path"`).

Every response has `libyang`, `op`, `verdict`, `modules` (per module: `name`, `accepted`,
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

`data_type` presets copy yanglint (`tools/lint/yl_opt.c`); all start from `LYD_PARSE_STRICT` +
`LYD_VALIDATE_MULTI_ERROR`:

| data_type | parse | validate | call |
|---|---|---|---|
| `data-operational` (default) | — | `+OPERATIONAL` (config violations become warnings) | `lyd_parse_data` |
| `data` | — | — | `lyd_parse_data` |
| `config` | `+NO_STATE` | `+NO_STATE` | `lyd_parse_data` |
| `get` | `+ONLY` | — | `lyd_parse_data` |
| `getconfig`, `edit` | `+ONLY +NO_STATE` | — | `lyd_parse_data` |
| `rpc`, `reply`, `notif` | STRICT/OPAQ only | `lyd_validate_op(tree, operational, type)` | `lyd_parse_op(LYD_TYPE_*_YANG)` |

`parse_only: true` adds `LYD_PARSE_ONLY` (and skips `lyd_validate_op`). `parse_options` /
`validate_options` add flags: parse `only strict opaq no_state ordered when_true store_only
json_null json_string_datatypes anydata_strict`; validate `no_state present multi_error
operational no_defaults not_final`.

Operations: `operational` / `operational_file` (+ `operational_format`, default = `format`) is
parsed `LYD_PARSE_ONLY` and used as the dependency tree. For a nested action/notification the
helper additionally requires its parent to exist in the operational tree (yanglint
`check_operation_parent`; libyang does not check it) → diagnostic `phase: operation_parent`.
For `reply`, `rpc` / `rpc_file` is the request: it is parsed as RPC, its input is dropped and the
reply (`data`, output children as top-level nodes, e.g. `{"ops:rtt": 5}`) is parsed under it.
Without `rpc`, `data` must be the full reply (`{"ops:ping": {"rtt": 5}}`).

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

## Known limitations

- XPath uses `lyd_eval_xpath4` (format `LY_VALUE_JSON`, no variables): node-set results keep only
  element nodes (root, text and metadata nodes are dropped by libyang), `when` is ignored during
  evaluation (`LYXP_IGNORE_WHEN`), the tree must be non-empty, numbers go through `double`.
  Schema-node XPath (`lys_find_xpath`/atomize) is not exposed.
- `with_defaults: all-tagged|implicit-tagged` emit `default` metadata only if
  `ietf-netconf-with-defaults` is in the context — add it to `searchdirs` + `modules`.
- No NETCONF/RESTCONF envelopes (`nc-rpc`, ...), LYB, yang-library contexts, merged multi-file
  input, anydata/extension-instance data, `lyd_validate_module`, stateful edit sequences — add when a
  fixture needs them.
- Compile phase is judged per module *in order*; the same set in another order can blame a
  different module.
- `msg` text is for humans; conformance compares code/vecode/paths/apptag/level only.
- Native brew libyang is a different build (flags, xxhash) — goldens come from the docker image.
