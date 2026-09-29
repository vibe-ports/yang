# Conformance fixture manifest (`corpus/manifest.yaml`)

YAML, one file for the whole corpus. Top level:

```yaml
version: 1
oracle: {libyang: v5.8.6, libyang_commit: <sha>}   # build the goldens came from
fixtures: [<fixture>, ...]
```

## Fixture

| Key | Required | Meaning |
|---|---|---|
| `id` | yes | unique, `<set>/<name>` |
| `dir` | yes | fixture root, relative to `corpus/`; becomes the oracle `base_dir` |
| `source.url` | yes | where the schemas/data came from (repo URL + path, RFC URL, or `hand-written (this repo)`) |
| `source.commit` | yes | upstream commit/tag, `null` for hand-written |
| `source.license` | yes | SPDX id of the fixture files (e.g. `BSD-3-Clause`, `Apache-2.0`, IETF Trust `BSD-2-Clause`) |
| `rfc` | yes | normative tags `RFC7950#9.9.3`, `RFC7951#6.4`, `XPath1.0#4.1`; `[]` only for libyang-specific behaviour (say why in a comment) |
| `expected_from` | yes | `oracle` (golden = lyoracle output) or `rfc` (golden hand-edited; disagreement recorded in `conformance/deviations.md`) |
| `request` | yes | the lyoracle request, verbatim (see `oracle/README.md`) — this is the invocation policy |
| `golden` | yes | response file, relative to `dir` |

The invocation policy lives only in `request` — never implied by the runner:

| Policy item | request key |
|---|---|
| op | `op` |
| schemas dir(s) | `searchdirs` (relative to `dir`) |
| implemented modules + enabled features | `modules: [{name, revision?, features?}]` |
| context options | `context_options` |
| data type | `data_type` (`config`, `data-operational`, `get`, `getconfig`, `edit`, `rpc`, `reply`, `notif`, `data`) |
| format | `format` (`json` \| `xml`) |
| parse vs validate | `parse_only`, `parse_options`, `validate_options` |
| with-defaults mode | `with_defaults` |
| external operational tree / rpc request | `operational_file`, `rpc_file` |
| xpath / diff inputs | `xpath`, `context_path`, `cur_module` / `first_file`, `second_file`, `diff_options` |

Goldens are the full oracle response (`json.dumps(indent=2, sort_keys=True)`). What the Go
harness compares from them is fixed by PLAN.md §5: accepted/phase, compiled dump, verdict +
diagnostics (level, code, vecode, data_path, schema_path, apptag — not `msg`), semantic tree
equality (`tree.json`), typed xpath result, diff tree.

Runner: `./dev make oracle-check` / `./dev make oracle-golden` (repo root). Goldens are authoritative only when
generated inside the dev container (pinned image).
