# 07 — Data tree, codecs, validation (M1-6)

Status: design for M1-6 (2026-10-06). Builds on 01–06. Reference: libyang v5.8.6 `src/tree_data.c`
(TD), `tree_data_new.c` (TDN), `tree_data_common.c` (TDC), `tree_data_sorted.c` (TDS),
`tree_data_free.c` (TDF), `parser_common.c` (PC), `parser_json.c` (PJ), `parser_xml.c` (PX),
`json.c` (JS), `xml.c` (XM), `validation.c` (VAL), `printer_json.c` (PRJ), `printer_xml.c` (PRX),
`out.c` (OUT), `log.c` (LOG), `diff.c` (DIFF, implicit-diff subset only). Line numbers are v5.8.6
(`make libyang-src`). Inputs: `internal/schema` (+ C0 #24), `internal/types` (`Store`, `StoreOnly`,
`ValidateTree`, `Equal`, `Compare`; ietf handlers #17), `internal/xpath` (`Compile`, `Eval`, `Node`,
`SchemaNode`, `Value`, `WhenState`, `ErrIncomplete`; evaluator #16), `internal/lyxp` (path grammar),
`internal/ly.Code`. "VERIFY(x)" = read from source, not yet pinned by a golden; fixture `x` must exist
and agree before code relying on it merges. "PROBED" = checked with the amd64 yanglint while writing
this note (2026-10-06), still to be pinned by a fixture.

Scope M1: datastore data (`data_type` config / data-operational / data / get / getconfig / edit) in
JSON and XML, parse + validate + print, defaults, when/must/leafref/instance-identifier/mandatory/
min/max/unique/choice/duplicate checks, opaque nodes for the `unknown` policies, RFC 7952 metadata
as far as with-defaults and the implicit diff need it, and the tree edits the oracle `sequence` op
uses. Out of M1 (§6): operations (rpc/action/notification/reply, M4), anydata/anyxml payloads (M5),
extension data (`LYD_EXT`), LYB, RESTCONF/NETCONF envelopes, full diff/merge options (M6).

## 0. Architecture decisions

1. **Parse and validate are interleaved, as in libyang.** `lyd_parse` (TD:101) does not parse a tree
   and then validate it: while parsing, every inner node is validated when it closes
   (`lyd_parser_validate_new_implicit`, PC:346: new-node checks + its implicit defaults — **only if
   no error occurred inside that node**, `!rc` at PJ:1427 and the XML twin; the node's own missing-key
   error counts too; under multi-error a bad child or key therefore suppresses its parent's duplicate checks and implicit defaults, so a must reading
   such a default later fails), and the
   parser collects the `when`, type and metadata work queues (`node_when`, `node_types`,
   `meta_types`) in parse order; `lyd_validate` (VAL:2106) then runs with `validate_subtree = 0` on
   those queues. A parse-then-validate design gives different error order, different queue order
   (the `when` queue is **post-order**, §1.6) and different flags. Our `Parse` ports that shape;
   `(*Tree).Validate` is `lyd_validate_all` (`validate_subtree = 1`, VAL:2287), a different walk.
2. **Multi-error is the compared mode.** The oracle always sets `LYD_VALIDATE_MULTI_ERROR`
   (`lyoracle.c` `dparams_of`) and the comparator checks the full ordered diagnostic list (level,
   code, vecode, data path, schema path, app-tag; not msg/line). So the continuation rules
   (`LY_VAL_ERR_GOTO`, `LY_DPARSER_ERR_GOTO` parser_internal.h:43: continue after `LY_EVALID`, stop
   on anything else and when the last error's vecode is exactly `LYVE_SYNTAX` — not
   `LYVE_SYNTAX_JSON`/`_XML`, so a JSON representation error is followed by e.g. a mandatory error)
   are ported literally, and so is the order of every pass (§3.3).
   Single-error mode = same walk, stop at the first error.
3. **Own lexers, not `encoding/json` / `encoding/xml`.** Diagnostics (`LYVE_SYNTAX_JSON`/`_XML`
   messages and line numbers), JSON number lexemes kept as text (int64/decimal64 exactness, the
   exponent rewrite in `lyjson_exp_number`, JS:448), XML namespace scoping per element, the refusal of
   DOCTYPE and non-predefined entities (XM:323, XM:509) and libyang's nesting limits are all part of
   the observable behaviour. Port `json.c` and `xml.c`.
4. **Schema snapshot, not a live context** (lead default, maintainer may revisit). `(*yang.Context)
   .Schema()` returns `*yang.Schema`, an immutable snapshot handle. It is **opaque** (design 05: never
   an alias with writable fields): the struct `Schema{ s *schema.Set }` is defined in a new leaf
   package `internal/snap` (imports `internal/schema` and the leaf packages `internal/types`, `internal/lyxp`: canonical defaults and leafref targets) together with its read-only accessors —
   all C8 handle types and their methods live there, `yang` only aliases them — and
   `snap.Set(*Schema) *schema.Set`; `yang` re-exports it as `type Schema = snap.Schema` — an alias of
   a struct with no exported fields, so callers outside the module can neither name the inner set nor
   write it. Chosen over an `init`-registered bridge function because it is typed (no `any`), needs no
   init ordering or mutable package variable, and `snap.Set` is unreachable from outside the module by
   Go's `internal/` rule. All data PRs call internal `parse(ctx, r, f, *schema.Set, o)` /
   `validate(...)` with hand-built sets that follow design 06 §4; only D12 adds the public wrappers.
   A `Tree` pins the snapshot it was parsed with; a later `Load` does not affect it. libyang trees use
   the live `LYD_CTX` instead — **U-0045**. `data` → `yang`/`internal/snap` is acyclic (`yang` never
   imports `data`; the yang-library builder of M6 lives in `data`).

## 1. libyang flow

**1.1 Entry.** `lyd_parse_data` (TD:195) → `lyd_parse` (TD:101): format dispatch with
`LYD_INTOPT_WITH_SIBLINGS`; on a parser error continue only if `LY_EVALID`, multi-error, and the last
error's vecode is not exactly `LYVE_SYNTAX` (TD:151); unless `LYD_PARSE_ONLY` → `lyd_validate(validate_subtree=0, parser
queues)`. **Any** error frees the whole tree (TD:180-189): an invalid input yields no tree, which is
why the oracle has `typed` only for valid results.

**1.2 JSON** `lyd_parse_json` (PJ:1977) → per top-level member `lydjson_subtree_r` (PJ:1581):
1. name split (`lydjson_parse_name`, `@` = metadata) → `lydjson_get_snode` (PJ:256):
   `lys_find_child_node` (tree_schema.c:541) under the parent's schema (`LYS_GETNEXT_OUTPUT` for
   replies) → found: `lyd_parser_check_schema` (PC:138: `LYD_PARSE_NO_STATE` + config false →
   `Unexpected data state node "%s" found.` at the schema path; rpc/action/notification in datastore
   data → `Unexpected %s element "%s".`). Not found: top-level unprefixed → `Top-level JSON object
   member "%s" must be namespace-qualified.` LYVE_SYNTAX_JSON (always); otherwise only with
   `LYD_PARSE_STRICT` (`unknown: reject`): `No module named "%s" in the context.` /
   `Node "%s" not found as a child of "%s" node.` / `Node "%s" not found in the "%s" module.`
   LYVE_REFERENCE at the **parent's** data path (m1/invalid-unknown-node, m1/invalid-if-feature-
   disabled: a disabled node is absent from the compiled schema). Without STRICT: skip the value
   (`lydjson_data_skip`) or, with `LYD_PARSE_OPAQ`, build an opaque node.
2. representation check per node type (`name/array of values`, `name/object`, … PJ:1806 message
   `Expecting JSON %s but %s "%s" is represented in input data as name/%s.` LYVE_SYNTAX_JSON, data
   skipped under multi-error);
3. `lydjson_parse_instance` (PJ:1520): `lydjson_data_check_opaq` (a value that does not fit with
   `LYD_PARSE_OPAQ` becomes opaque) → term `lydjson_parse_instance_term` (PJ:1457:
   `lyd_parser_create_term` PC:205 → `lyd_create_term` → `lyd_value_store` TDC:482 = `types.Store`
   with `FormatJSON`, the token hints of `lydjson_value_type_hint` PJ:336 — `types.JSONHints` — and
   `LYD_VALHINT_STRING_DATATYPES` for `json_string_datatypes`; `LY_EINCOMPLETE` → `node_types`) or
   inner `lydjson_parse_instance_inner` (PJ:1382: create → insert (a list only once all keys are
   present, `lyd_parser_node_insert` PC:392) → children → insert again → `lydjson_metadata_finish` →
   `lyd_parser_check_keys` (PC:264, `List instance is missing its key "%s".`) →
   `lyd_parser_validate_new_implicit`) or any (M5) → `lyd_parser_set_data_flags` (PC:285: node with a
   `when` → `node_when` **after** its children were parsed; `LYD_PARSE_WHEN_TRUE` seeds `WhenTrue`;
   `default`/`ietf-netconf-with-defaults:default="true"` metadata → `Default` flag, metadata removed,
   `lyd_np_cont_dflt_set` on the parent);
4. after the loop: `lydjson_metadata_finish` for the top level.

**1.3 XML** `lyd_parse_xml` (PX:1220) → `lydxml_subtree_r` (PX:1001), same skeleton:
`lydxml_subtree_get_snode` (PX:563, namespace errors via `lydxml_log_namespace_err`; non-STRICT
unknown = skip, logged only at verbose level) → `lydxml_metadata` (PX:91) → term (PX:758, value with
`FormatXML` and the element's in-scope namespaces as prefix context, `HintData`; the **full** text,
whitespace-only content included, goes to the type — a string leaf of three spaces keeps them (PX:768);
a key whose schema-order anchor is another key → `Invalid position of the key "%s" in a list.`
LYVE_DATA under STRICT, a warning otherwise, PX:779; JSON has no such check) / inner (PX:824) / opaque (PX:652) / any (PX:916) → `set_data_flags`.

**1.4 Insertion order** (`lyd_insert_node` TD:801, used by parsers and implicit defaults):
- schema order among siblings (`lyd_insert_get_next_anchor` TD:503: before the first instance of the
  closest following schema sibling); keys are first because they are first in `lys_getnext`;
- top level: data grouped by module, modules ordered by `strcmp` of module names (TD:543);
- `ordered-by system` leaf-lists and keyed lists are kept **sorted by value** (`lyds_insert`,
  TDS; `rb_compare_leaflists`/`rb_compare_lists` TDS:212-270: type sort callbacks on the value,
  lists key by key) — PROBED: `eth1, eth0` prints as `eth0, eth1`. Keyless lists, user-ordered and
  `LYD_PARSE_ORDERED` input keep input order;
- opaque nodes always after schema nodes (TD:712-800).

**1.5 `lyd_validate`** (VAL:2106), per **implemented module in context order**
(`lyd_mod_next_module`, TDC:423); with `LYD_VALIDATE_PRESENT` instead per module **in tree order**
of the top-level data (`lyd_data_next_module`, TDC:458; VAL:2130) — a different order, not just a
subset:
1. `lyd_validate_new` on the module's top-level nodes (VAL:1008, §1.7);
2. top-level implicit nodes: `lyd_new_implicit_r` (parse path) or `lyd_new_implicit` + per top node
   `lyd_validate_tree` (Validate path, VAL:2020: DFS pre-order; term → plugin `validate_value`
   = restriction re-check, value with `validate_tree` → `node_types`; inner → `lyd_validate_new` on
   its children + their implicit nodes; node with `when` → `node_when`). Implicit top-level containers
   are created for **every** implemented module, internal ones included (why `typed` of
   m1/valid-oper has nodes of `ietf-yang-library` etc., and config dumps fewer: `NO_STATE`);
3. `lyd_validate_unres` (VAL:530): extension data (none in M1) → `when` queue (§1.6) → `node_types`
   **from the end** (`lyd_value_validate_incomplete` = `types.ValidateTree`) → `meta_types` from the
   end. PROBED: three dangling leafrefs in document order eth1, eth0, eth2 report eth2, eth0, eth1
   (reverse parse order, not sorted order). **The queues are not per module on the Parse path**:
   `lyd_parse` hands the parser's shared queues to `lyd_validate` (TD:163), so the unres of the
   *first* module in the traversal (context order: an internal module) drains them for every module —
   all parse-time `when`/leafref/metadata errors come out in global reverse parse order, *before* the
   top-level `lyd_validate_new` (duplicates, both-cases) of every later module. Probe (reviewer,
   modules `za`, `aa`; input `aa:r, za:r, za:ll[d,d], aa:ll[e,e], za:w`): when `za:w` → leafref
   `za:r` → leafref `aa:r` → dup `za` ×2 → dup `aa` ×2; with `present` (tree order) dup `aa` ×2 come
   first. Only `Validate` (`lyd_validate_all`, own queues filled by `lyd_validate_tree` per module)
   drains per module;
4. after all modules, unless `LYD_VALIDATE_NOT_FINAL`: `lyd_validate_final_r` (VAL:1798) per module:
   for each sibling: unexpected state / output / input / rpc / action / notification
   (`Unexpected data %s node "%s" found.`), skip `WhenFalse` nodes, obsolete warning
   (`Obsolete schema node "%s" instantiated in data.`), `lyd_validate_must` (VAL:1707); then
   `lyd_validate_siblings_schema_r` (VAL:1597): choices (mandatory choice; recurse into the existing
   case only), then schema siblings in `lys_getnext` order: list min/max + unique, leaf-list min/max,
   mandatory leaf/container/any; then recurse into each child and `lyd_np_cont_dflt_set`. PROBED:
   musts follow tree (sorted) order, then the mandatory error of a later sibling. A tree of only
   top-level opaque nodes reports `lyd_parse_opaq_error` (TDC:859) **once per implemented module**:
   `lyd_first_module_sibling` finds no data of the module and the walk starts at the opaque node again
   (reviewer probe: `{"zz:x":1}` with `unknown: opaque`, config → 8 identical LYVE_REFERENCE errors;
   once schema data exists, 1) — D-0057 candidate.

**1.6 `when`.** Queue = `node_when`: parse post-order plus implicit nodes in creation order.
`lyd_validate_unres_when` (VAL:462) walks it **from the end**, per node `lyd_validate_node_when`
(VAL:270): the node's own whens and those of its choice/case ancestors, context node = the node or
its parent (`when->context`), `lyxp_eval(..., LYXP_SCHEMA)` with the root type taken from the context
node (`lyxp_get_root_type`, xpath.c:9894). Results: `LY_EINCOMPLETE` (hit a node whose when is
unresolved) → stays queued; true → `WhenTrue`, clear `WhenFalse`; false with `WhenTrue` →
auto-delete (diff `delete`, VAL:334, nested nodes removed from `node_types`); false + operational →
warning `When condition "%s" not satisfied.`; false otherwise → error LYVE_DATA at the node, and under
multi-error the node is kept, flagged **`WhenFalse`** (VAL:408: XPath treats it as absent, its
descendants leave both queues). Resolved nodes leave the queue (order kept); passes repeat while the
queue shrinks; leftovers only exist after an error (`ly_set_erase`). Design 03 rule 5 applies
unchanged. **Dummy when** (`lyd_validate_dummy_when`, VAL:1083) for absent mandatory nodes and
unsatisfied min-elements: an opaque node named like the schema node is inserted at the would-be place,
whens evaluated with accessible tree config/all from the schema node's config flag, then freed;
`LY_EINCOMPLETE` is ignored under multi-error and is an internal error (`LY_EINT`) otherwise.

**1.7 New nodes** (`lyd_validate_new`, VAL:1008): `lyd_validate_choice_r` (VAL:977) →
`lyd_validate_cases` (VAL:699: two cases with NEW data, or two with old data → `Data for both cases
"%s" and "%s" exist.` with **no data path**, schema path of the choice — m1/invalid-choice-both-cases;
old + new → old case auto-deleted); then per node with `New` or `Default`: auto-delete defaults
superseded by an explicit instance (VAL:816, VAL:863), duplicate check for `New` nodes (VAL:639: every
`New` instance is checked, so **both** members of a duplicate pair report `Duplicate instance of
"%s".` — m1/invalid-duplicate-key has two identical errors; operational: lists/leaf-lists only warn),
clear `New`, auto-delete defaults of a case that no longer exists (VAL:920).

**1.8 Implicit nodes** (`lyd_new_implicit`, TDN:1890; `_r` TDN:2037 recurses into default
containers): choices first (default case if no case has data, else defaults of the existing case),
then schema siblings: NP container, leaf with default (stored from the raw default text with
`FormatSchemaResolved` + `HintSchema`; incomplete → `node_types`), all leaf-list defaults; skipped:
state under `NO_STATE`, obsolete nodes, defaults under `NO_DEFAULTS` — but the parser's per-node
close forwards only `NO_STATE` (PC:367), so `Parse` with `NoDefaults` still creates nested defaults
(only top-level ones are suppressed), while parse-only + `Validate(NoDefaults)` creates none; both
paths are pinned by fixtures. Flags `Default | (WhenTrue if
the schema node has a when)` — the seed that turns a false `when` on an implicit node into a silent
auto-delete instead of an error (design 03 rule 3); queued into `node_when`/`node_types`; diff
`create`.

**1.9 Printers.** `json_print_data` (PRJ:1136) / `xml_print_data` (PRX:581); filter
`lyd_node_should_print` (OUT:44) per with-defaults mode (trim: drop `Default` nodes, explicit nodes
equal to the default (`lyd_is_default` TDC:711) and NP containers without printed children;
explicit: drop default config nodes unless a state node is inside; all: everything; all-tagged /
implicit-tagged: add `ietf-netconf-with-defaults:default` metadata); default containers without
printable descendants are never printed. JSON: member qualified when its module differs from the
parent's (`json_nscmp`), values per base type of the stored (union: selected) type — int64/uint64/
decimal64/string/enum/bits/identityref/instance-identifier/binary as strings, int8–32/uint8–32/bool
bare, empty `[null]` (PRJ:388); string escapes `"` `\` `\r` `\t` and other control bytes as `\u00XX`
upper-case hex, everything else raw (PRJ:247); list/leaf-list instances of one schema node as one
array; metadata as `"@name"`. XML: namespace declarations only where the namespace changes, value
prefixes from the value's own prefix data (`xml_print_ns_prefix_data`). Oracle output = siblings,
default formatting (2-space indent, `\n`).

**1.10 Error locations** (`ly_vlog_build_path_line`, LOG:657): with a data node `lnode`:
`lyd_path(lnode, LYD_PATH_STD)` (TD:2974: `/mod:name` on module change, list keys `[k='v']` (`"`
if the value contains `'`, no escaping beyond that), config leaf-list `[.='v']`, keyless list and
state leaf-list `[n]` position), plus `/[mod:]name` of the schema node being stored when its data
parent is `lnode`'s schema (a term that failed to store has no node yet → `.../m1-ext:m1/gain`). A
list whose keys are not complete is not linked to its parent yet, so its path starts at the list:
m1/invalid-key-value-type reports `/ietf-ip:address/ip` and `/ietf-ip:address`. Without a node:
`schema_path` = `lysc_path(LYSC_PATH_LOG)` of the innermost `LOG_LOCSET` node, `data_path` null
(choice cases, top-level mandatory, min-elements with no instance, `Unexpected data state node`).

**1.11 Edits used by `sequence`** (design 04 §4): `lyd_new_path` with `LYD_NEW_PATH_UPDATE` (TDN:1865;
creates missing parents, changes a term value through `lyd_change_term` which clears `Default` on it
and its NP-container ancestors, `lyd_np_cont_dflt_del`; created/changed nodes get `New`),
`lyd_find_path` (TD:3700), `lyd_free_tree` (TDF:265: a list key → `Cannot free a list key "%s", free
the list instance instead.` LY_EINVAL, LOGERR, nothing freed), `lyd_merge_siblings` (TD:2850, no
options). Implicit diff: `lyd_val_diff_add` (VAL:188) → `lyd_diff_add` (DIFF:383) +
`lyd_diff_merge_all` (DIFF:3034) restricted to create/delete/none (user-ordered `key`/`value`/
`position` metadata included).

## 2. Go package `data`

Files (each ported file carries the provenance header of its C source; `docs/port-map.md` rows per
function): `node.go` (Node, flags, iteration), `insert.go` (TD:503-865 + TDS ordering), `path.go`
(`lyd_path`, find/new path over `internal/lyxp`), `internal/lyxml` (XM) and `internal/lyjson` (JS) as separate packages, `parse.go` (TD
`lyd_parse`, PC), `parse_json.go`, `parse_xml.go`, `opaque.go`, `meta.go`, `defaults.go` (TDN implicit,
TDC `lyd_np_cont_dflt_*`), `validate.go` + `when.go` (VAL), `xpathnode.go` (adapters for
`xpath.Node`/`SchemaNode`/`Value` and `types.Tree`), `print_json.go`, `print_xml.go`, `wd.go` (OUT),
`diff.go` (implicit diff subset), `edit.go`, `diag.go`, `budget.go`.

**Node** = design 02, children as an ordered slice. Flags (Go names `FlagDefault`, `FlagWhenTrue`, `FlagNew`, `FlagWhenFalse`) `Default`, `WhenTrue`, `New`, and
**`WhenFalse`** (libyang 5.8.6 `LYD_WHEN_FALSE` 0x10, VAL:408 — design 02 is amended in this PR).
Only XPath (absent) and `lyd_validate_final_r` (no musts, no obsolete warning, no recursion into it,
no NP-container default flag) honour `WhenFalse`; mandatory/min/max/unique of its siblings still
count the node. It is cleared only when its `when` is re-evaluated true.
`xpath.Node.When()` = `WhenFalse` if flagged; `WhenUnresolved` if the node or a choice/case ancestor
has a `when` (`lysc_has_when`) and neither flag is set; else `WhenTrue` — the same test as
xpath.c:5863/6378 (the context node itself is exempt there, handled by xpath). Values are
`types.Value`. A list instance waiting for its keys has `parent == nil` while its children point to
it (§1.10 path rule falls out of that). Sibling lookups for duplicates/unique/`lyd_find_sibling_val`
use a lazily built per-parent index keyed by schema node and canonical key text, confirmed with
`types.Equal` (canonical text alone is not equality across union members); no O(n²) scans.
`SchemaNode` adapter is a value type `snode{*schema.Node}` (comparable, equal per pointer).

**API** (exported; everything else unexported):
```go
type Format uint8                 // FormatJSON, FormatXML
type UnknownPolicy uint8          // Reject (LYD_PARSE_STRICT), Skip, Opaque (LYD_PARSE_OPAQ)
type ParseOptions struct {
    Unknown   UnknownPolicy
    ParseOnly bool            // LYD_PARSE_ONLY
    NoState   bool            // LYD_PARSE_NO_STATE (+ LYD_VALIDATE_NO_STATE for the validation part)
    Validate  ValidateOptions // ignored with ParseOnly
    Budget    Budget
}
type ValidateOptions struct {
    NoState, Present, MultiError, Operational, NoDefaults bool // LYD_VALIDATE_*
}
func Parse(ctx context.Context, r io.Reader, f Format, s *yang.Schema, o ParseOptions) (*Tree, []yang.Diagnostic, error)
func (t *Tree) Validate(ctx context.Context, o ValidateOptions) ([]yang.Diagnostic, error)
func (t *Tree) ValidateDiff(ctx context.Context, o ValidateOptions) (diff *Tree, d []yang.Diagnostic, err error) // lyd_validate_all's diff
func (t *Tree) Print(w io.Writer, f Format, o PrintOptions) error // PrintOptions{WithDefaults WD; Shrink bool}
func (t *Tree) NewPath(path, value string, o NewPathOptions) (*Node, error) // o.Update
func (t *Tree) Find(path string) (*Node, error)
func (n *Node) Remove() error
func (t *Tree) Merge(src *Tree) error
func (t *Tree) Top() iter.Seq[*Node]   // + Node: Schema(), Value(), Name(), Children(), All() (pre-order), Parent(), Path(), Flags()
func (e *ValidationError) RC() string // the call's LY_ERR name (LY_EVALID, LY_EINVAL, LY_ENOTFOUND, ...): that of the last error logged
```
`ValidateDiff` is the second Validate method because the implicit diff is libyang's out-parameter
(the oracle and NETCONF servers need it); PLAN §2's `Validate` shape stays, with `context.Context` as
the first parameter (cancellation checked every 1k nodes and between XPath evaluations), never in an
options struct (lead default, maintainer may revisit). Exported knobs are only those the M1 fixtures
use (`unknown`, `parse_only`, the `data_type` presets = NoState/Operational, the oracle's
MultiError, and NoDefaults/Present for §5's fixtures); `LYD_PARSE_ORDERED`, `WHEN_TRUE`,
`STORE_ONLY`, `JSON_NULL`, `JSON_STRING_DATATYPES`, `LYD_VALIDATE_NOT_FINAL` exist internally where
the port needs them and are exported later, when a fixture or user asks. The PRs and their tests call
the internal twins (`parse`/`validate` over `*schema.Set`, §0.4) with hand-built sets; D12 wraps them.

**Diagnostics** (PLAN §2): reuse `yang.Diagnostic` as shipped (`Warning bool; Err, Code string`
— LY_ERR and LY_VECODE names — plus the path/line/message fields; this stream adds `DataPath` and
`AppTag` to it, additive) and the single `yang.ErrBudget` (lead default, maintainer may revisit);
`internal/ly.Code` stays internal. `error` is nil when only warnings were produced, else
`*ValidationError{Diags}`, or an error wrapping `yang.ErrBudget` / `ctx.Err()`. `Err` is
`LY_EVALID` for LOGVAL sites, `LY_EINVAL` for the depth limits and the key refusal (LOGERR, code
`LYVE_SUCCESS`), `LY_EINCOMPLETE` for `Must "%s" depends on a node with a when condition, which has
not been evaluated.` (VAL:1749), `LY_EINT` for the dummy-when case (§1.6). Line numbers come from the
lexers and are reported only while the lexer is alive (libyang frees it before validation: unres and
final errors have no line — PJ:2036). The location stack is a small port of `LOG_LOCSET`/
`ly_log_location` held in the parser context, never global.

**Accessible tree and prefixes.** must/when evaluate `schema.Must/When.Compiled` (`*xpath.Expr`
bound at compile time to the defining module, `Default()` = the node's module, design 06 §2.13) with
`EvalContext{Node: ctx node, Tree: top-level siblings, Root: config iff the context schema node is
config (LYXP_SCHEMA), Schema: identity/top-level hook over the snapshot, Deref, MaxSteps}`.
Leafref `require-instance` = `types.ValidateTree` with `data` implementing `types.Tree`
(`LeafrefTarget`: as `lyplg_type_resolve_leafref` (plugins_types.c:994), the predicate is built from
**each value's** canonical text — only the static path (template) is compiled once per type and
cached, the value predicate is bound per call; values containing both quote kinds evaluate the plain
path and compare; `InstanceExists` over the instance-identifier path).

## 3. Errors and their order

**3.1 Messages used in M1** (ly_common.h:272-287 unless noted), all LYVE_DATA LY_EVALID:
`When condition "%s" not satisfied.` · `Mandatory node "%s" instance does not exist.` (parent path) ·
`Mandatory choice "%s" data do not exist.` app-tag `missing-choice` · `Duplicate instance of "%s".` ·
`Data for both cases "%s" and "%s" exist.` · `Unexpected data %s node "%s" found.` ·
`List instance is missing its key "%s".` · `Unique data leaf(s) "%s" not satisfied in "%s" and
"%s".` app-tag `data-not-unique`, at the second instance · `Too many "%s" instances.` app-tag
`too-many-elements` / `Too few "%s" instances.` `too-few-elements` (at the last instance, else the
parent; schema path when neither) · `Must condition "%s" not satisfied.` app-tag `must-violation`
unless `error-app-tag`, message replaced by `error-message` (m1/invalid-must-false) · type errors
from `types.Diag` (code and app-tag pass through, e.g. leafref `instance-required`) · opaque
nodes under validation: `lyd_parse_opaq_error` (TDC:859, LYVE_REFERENCE). Syntax errors from the
lexers keep libyang's texts (`LY_VCODE_INSTREXP`, `NTERM`, …).

**3.2 Severity per constraint** (`LYD_VALIDATE_OPERATIONAL`): warnings for `when` false, must false,
mandatory, min/max, unique, duplicate list/leaf-list instances; still errors: type store errors,
leafref/instance-identifier require-instance, missing keys, both cases, unknown nodes (PLAN §1).

**3.3 Order under multi-error** — the contract the fixtures pin:
1. parse, document order: per node on store/representation errors; per inner node at its close:
   missing key, then — only if nothing inside it failed, the missing key included (`!rc`, PJ:1427) —
   `lyd_validate_new` of its children (both-cases, duplicates) and its implicit defaults;
2. **Parse path**: modules in traversal order (context order; `Present`: tree order). The first
   module's top-level `lyd_validate_new` and implicit nodes, then the shared queues drained once for
   all modules: `when` errors (queue end first), type `ValidateTree` errors (global reverse parse
   order, PROBED), metadata; then per later module: its top-level `lyd_validate_new`, its top-level
   `lyd_new_implicit_r`, and `lyd_validate_unres` over only what those implicit nodes queued.
   **Validate path**: per module: top-level `lyd_validate_new`, its `lyd_validate_tree`, then its own
   `when` / type / metadata queues;
3. per module: `lyd_validate_final_r` — unexpected nodes and musts per sibling in tree order, then
   that sibling set's choice/min/max/unique/mandatory, then children depth-first.
Single-error mode stops at the first item of this sequence.

## 4. Budgets for untrusted input (`data.Budget`, zero = default; exceeding → error wrapping `yang.ErrBudget`)

- **Nesting**: XML 500 open elements (`LY_MAX_BLOCK_DEPTH`, XM:730, `The maximum number of open
  elements has been exceeded.` LY_EINVAL) and the JSON lexer's status stack (JS:905: count > 5000,
  `Maximum number %d of nestings has been exceeded.`; reviewer probe: 4998 nested values pass, 4999
  fail — boundary pinned by depth/json-5000 and its 4998 control): libyang's own limits, ported, no deviation. Parser recursion is
  therefore bounded; validation recursion follows tree depth, which is bounded by the input or, for
  API-built trees, by the schema depth.
- **Input size** `MaxBytes` (default 256 MiB, read through `io.LimitReader` + 1) — **U-0040**.
- **Nodes** `MaxNodes` per tree incl. implicit nodes (default 1<<22) — **U-0041**.
- **XPath work**: per evaluation `xpath.DefaultMaxSteps`; per `Parse`/`Validate` a cumulative
  `MaxXPathSteps int64` (default 1<<30, < 2^31) because the `when` queue may repeat passes (O(n²)
  evaluations) and every node may carry musts — **U-0042**. Needs a small xpath addition: `Eval`
  reports the steps it consumed (task X1). Exceeding either budget **aborts** with an error wrapping
  `yang.ErrBudget`; it is never turned into a must/when diagnostic. `ctx` checked between evaluations.
- **Values**: lexemes are bounded by `MaxBytes`; pattern/union cost is `types`' budget.
- **Ordering**: sibling order must be libyang's whenever anything reads it — JSON metadata
  attachment (`lydjson_parse_attribute` PJ:1166 and `lydjson_metadata_finish` PJ:583 attach `@ll` entries by position to the already
  sorted instances), the parent's close, the end of parse (parse-only too), XML key-position checks.
  A run of system-ordered instances is appended during parse and stably sorted **before the first such
  read** (metadata finish, parent close, end of parse); equal to libyang's incremental RB insert except
  the order of equal values — duplicates, errors anyway: VERIFY(order/sorted-dup). `NewPath`/`Merge`
  insert with binary search (append fast path). No quadratic insertion on reversed input.
- Fuzz targets: `FuzzJSONLex`, `FuzzXMLLex`, `FuzzParseJSON`, `FuzzParseXML` (over a fixed m1-like
  hand-built schema), round-trip property parse → print → parse.

## 5. Conformance

**Invocation policy** (manifest, design 04 §5): every data fixture names `format`, `data_type`,
`unknown` (default reject), `with_defaults` (explicit unless the mode is under test) and only the
`parse_options`/`validate_options` it tests; codec-independent rules come as a JSON + XML pair;
`areas` tags; `assert` with `vecode_name` + `data_path` (+ `apptag`) where the RFC is clear. Goldens
only via `DEV_PLATFORM=linux/amd64 ./dev make oracle-golden`.

**Existing fixtures this stream must agree with**: m1/valid-{config,oper,union-number}-{json,xml},
all 31 m1/invalid-*, m1/sequence-when-auto-delete, basic/{range,mandatory-missing,leafref,when-false,
must-apptag,valid-all-tagged}, protocol-v2/{implicit-defaults,unknown-*,opaque-xml-ns-*,when-auto-
delete,union-member,choice-default-case,sequence-edits}, when/order-{a-after-b,b-after-a}, the
`types/*` data fixtures (codec ↔ types plumbing). protocol-v2/anydata-* are out (U-0043).

**New fixtures** (stream F; ids are proposals, "+" = valid):

| area | fixtures |
|---|---|
| order (§3.3) | order/leafref-reverse (3 dangling leafrefs, PROBED), order/parse-cross-module (`aa:r, za:r, za:ll[d,d], aa:ll[e,e], za:w`: when, leafrefs, then dups — Parse path) + order/parse-cross-module-present + order/validate-cross-module (same input parse-only, then Validate: per-module order), order/parse-unres-final (store error + must + mandatory in one input), order/module-context-order (modules loaded za then aa, only top-level duplicate errors in both: Parse path reports them in context order) + order/module-present (same with `present`: tree order), order/sorted-list +, order/sorted-leaflist +, order/sorted-leaflist-meta (reversed values with distinct annotations, `@ll` before and after the values), order/user-ordered-kept +, order/toplevel-module-sort +, order/sorted-dup (VERIFY) |
| parse/validate interplay (§0) | interplay/inner-close-guard (invalid child + duplicate siblings + a default + a must reading it), interplay/repr-then-mandatory (container given as a JSON number, then a missing mandatory leaf), dflt/no-defaults-parse vs dflt/no-defaults-parse-only-validate (JSON + XML), lref/shared-type-mixed (one valid, one dangling reference of one type, both input orders) |
| when (§1.6) | when/implicit-default-false + (silently removed), when/explicit-false (error), when/dummy-mandatory + (mandatory under false when), when/false-subtree-no-cascade (dangling leafref below a false-when node: one error), when/oper-warning |
| defaults (§1.8) | dflt/np-container-flags +, dflt/leaflist-replaced-by-explicit +, dflt/case-default-removed (sequence), dflt/no-defaults-option + |
| choice/dup (§1.7) | choice/old-case-autodelete (sequence), dup/leaflist-config, dup/oper-leaflist-warning, dup/keyless-allowed + |
| final (§1.5) | mand/top-level (schema path only), mand/choice (missing-choice), minmax/too-few, minmax/too-many (path of last instance), unique/violation, unique/default-participates, state/no-state-config (parse-time), oper/must-warning, oper/leafref-still-error |
| codecs | json/representation, json/unqualified-top, json/exp-number, json/empty-null +, json/string-escapes + (print), xml/doctype, xml/entity, xml/cdata +, xml/unknown-namespace, xml/whitespace-string + (vs empty string), xml/keys-out-of-order (reject: LYVE_DATA), xml/keys-out-of-order-skip + (warning), json/keys-out-of-order +, depth/xml-500, depth/json-5000 (4999 nested: fail) + depth/json-4998 + |
| opaque | opaque/toplevel-only (8× the same error, D-0057), opaque/toplevel-with-data (1×) |
| paths (§1.10) | path/key-with-apostrophe, path/keyless-position, path/state-leaflist-position |
| print (§1.9) | print/wd-trim, print/wd-all-tagged-xml, print/wd-explicit-state-in-default-container, print/xml-identityref-prefix |
| edits (§1.11) | seq/new-path-clears-default, seq/free-key-refused (exists in sequence-edits), seq/merge-basic |

Engine-only (Go tests, no golden — libyang has no such limits): MaxBytes, MaxNodes, MaxXPathSteps,
cancellation. libyang utests mapping: `tests/utests/data/test_validation.c` (when, mandatory,
minmax, unique, dup, defaults, choice, must), `test_parser_json.c`, `test_parser_xml.c`,
`test_printer_*.c`, `test_tree_data.c`, `test_new.c` — every case whose schema compiles in M1 becomes
a fixture with `assert` (extractor, inventory §5.4).

## 6. M1 subset vs later

| feature | M1-6 | later |
|---|---|---|
| datastore data JSON/XML, all `data_type`s except operations | yes | — |
| rpc/action/notification/reply (`lyd_parse_op`, `lyd_validate_op`) | data-tree parse rejects op nodes as libyang does | M4 (U-0044 for the adapter's op types) |
| anydata/anyxml | instance → `ErrUnsupported` (U-0043) | M5 |
| metadata | annotation lookup + store, `default`, `yang:operation`, with-defaults tags | full RFC 7952 / origin inheritance M4–M5 |
| opaque nodes | `unknown: opaque`, printing, validation error | envelopes M4 |
| diff | implicit diff (create/delete/none) | full diff/merge/apply M6 |
| edits | NewPath(Update), Find, Remove, Merge without options | `ApplyEdit` M6 |
| extension data (`LYD_EXT`, schema-mount, yang-data) | never reached (compile rejects instances, U-0023/U-0024) | later |
| LYB, `lyd_parse_value_fragment`, RESTCONF/NETCONF wrappers | no | out of v1 / M4 |

## 7. Deviation candidates (D-0050…D-0069, U-0040…U-0059)

Recorded in `conformance/deviations.md` by the PR that implements the behaviour, as for 06. Existing
entries owned by this stream: **D-0001** (candidate: `when` reading a top-level default) and
**D-0047** (candidate: data `when` evaluation order, design 03 rule 5) — D9 resolves or keeps them.
| id | behaviour (mirrored unless stated) | RFC |
|---|---|---|
| D-0050 (candidate) | both members of a duplicate pair are reported (two identical errors) | RFC 7950 §7.8.3 |
| D-0051 (candidate) | error path of a list with missing/invalid keys omits its ancestors (`/ietf-ip:address/ip`) | RFC 6241 §4.3 error-path |
| D-0052 (candidate) | a key value containing both quote kinds yields an unparsable path predicate | RFC 7950 §9.13.2 |
| D-0053 (candidate) | Parse path: when/require-instance/metadata errors of **all** modules in one global reverse parse order, before later modules' top-level duplicate/case errors (§3.3) | — (order unspecified) |
| D-0054 (candidate) | JSON numbers with exponent accepted for integer/decimal types after `lyjson_exp_number` rewriting; libyang's rewrite is wrong when a leading-zero mantissa shifts the point inside its digits (`0.5e1` → `.`, `0.123e3` → `12.`), reproduced as is (D3), so such values fail type parsing | RFC 7951 §6.1 |
| D-0055 (candidate) | dummy-when: an unresolvable `when` on an absent mandatory node skips the check under multi-error but is `LY_EINT` otherwise | RFC 7950 §7.21.5 |
| D-0056 (candidate) | a must reaching a node left unresolved after a when error fails with `LY_EINCOMPLETE` (LOGERR, no vecode) | — |
| D-0057 (candidate) | a tree of only top-level opaque nodes reports the opaque error once per implemented module | — |
| D-0058 (candidate) | **not mirrored**: `LYD_INSERT_NODE_LAST` (`LYD_PARSE_ORDERED`) appends after the last sibling whatever it is; the port keeps schema nodes in schema order and before all opaque nodes (opaque nodes are a separate slice), so input that is not in schema order or mixes unknown nodes is placed by schema; likewise `lyd_insert_after` of a schema node after an opaque sibling (libyang links it there) places it after the last instance of its own run, keeping schema order | RFC 7950 §7.5.7 (data order is the schema order; libyang's own default insertion agrees) |
| D-0061 (candidate) | **not mirrored**: moving a sorted run into a sorted run (`lyds_merge_nodes3`): equal source values in data order, libyang in `rb_iter` order (tree-shape dependent) | RFC 7950 §7.7.7 |
| D-0062 | **not mirrored**: moving a sorted run into an untreed run (`lyds_merge_nodes2`): libyang is UB (SIGSEGV / allocation failure and hang); the port does the stable merge, source first on equal values | RFC 7950 §7.7.7 |
| U-0040 | input size budget | libyang reads anything |
| U-0041 | node-count budget | — |
| U-0042 | cumulative XPath step budget per Parse/Validate | — |
| U-0043 | anydata/anyxml instances → `ErrUnsupported` until M5 | — |
| U-0044 | engine: operation `data_type`s unsupported until M4 | — |
| U-0045 | a `Tree` pins the schema snapshot it was parsed with; libyang trees see the live context (`LYD_CTX`) | — |

## 8. Task split (≤ ~1.5k Go lines incl. tests; own branch + PR + astra review)

None of the code PRs needs the compiler: tests build `schema.Node`/`Type`/`Must`/`When` by hand
(must/when via `xpath.Compile`, leafref `Path`/`Prefixes`/`Realtype` per design 06 §4 invariants),
plus lexer/parser corpora. **Oracle agreement** (the `get` and all other data fixtures) needs compiled
m1 schemas (C7) and the opaque `yang.Schema` snapshot (C8, `internal/snap`): it is the gate of D12 and M1-7, not of D5/D6.

| # | PR | ~LOC | Depends | Start | Worker |
|---|---|---|---|---|---|
| S1 | `internal/schema` helpers, one home for all users: `lys_getnext` order (with/without choice, output), `lysc_path(LYSC_PATH_LOG)`, `lysc_data_parent`, `lysc_has_when`; `internal/types` copies switch to them (C4a told the same) | 400 | C0 #24 | **now** | Sonnet |
| X1 | xpath: `Eval` reports steps consumed (for `MaxXPathSteps`) | 100 | #16 | **now** (on #16) | Sonnet |
| D0 | types additions: `Print(v, f, *PrintCtx)` (plugin print for XML/JSON prefixes, `prefix_data` capture for XML namespaces), `Validate(t, v)` (= `validate_value` restriction re-check), default store helper | 400 | #17 | **now** | Sonnet |
| D1 | tree core: Node/Tree/flags, insertion (schema order, top-level module order, sorted system-ordered, opaque last), unlink/free + key refusal, sibling index, iterators | 1.1k | C0, S1 | **now** (S1 in parallel) | Opus |
| D1b | `lyd_path` (STD), diagnostics (`yang.Diagnostic` fill, `ValidationError`), `LOG_LOCSET` location stack, error-path builder §1.10 | 0.5k | D1, S1 | after D1 | Sonnet |
| D2 | XML lexer (`xml.c`): elements, attributes, ns stack, values/entities/CDATA, DOCTYPE refusal, depth 500, lines, backup/restore + fuzz | 1.2k | — | **now** | Sonnet |
| D3 | JSON lexer (`json.c`): status machine, strings/UTF-8, numbers incl. exp rewrite, status-stack limit 5000, backup/restore + fuzz | 1.0k | — | **now** | Sonnet |
| D4 | parser common (`lyd_parse` driver, shared queues, create_term/meta, check_schema, check_keys, node_insert, set_data_flags, opaque + `lyd_parse_opaq_error`, multi-error continuation, `!rc` close guard, budgets MaxBytes/MaxNodes) with a `validateNewImplicit` hook (no-op until D8) | 0.9k | D1b, D0 | after D1b | Opus |
| D5 | JSON data parser (PJ minus ops/any/ext), hand-built-schema tests + parser corpus | 1.1k | D3, D4 | after D4 | Sonnet |
| D6 | XML data parser (PX minus ops/any/ext, key-position check), hand-built-schema tests + parser corpus | 1.0k | D2, D4 | after D4 | Sonnet |
| D7 | printers JSON + XML + metadata printing | 1.1k | D0, D1b | after D1b | Sonnet |
| D7b | with-defaults filter (`lyd_node_should_print`, `lyd_is_default`), tagged modes | 0.4k | D7 | after D7 | Sonnet |
| D8 | `lyd_new_implicit[_r]`, `lyd_validate_new` (cases, autodel, duplicates), `np_cont_dflt_*`; a `diff(node, op)` hook at every `lyd_val_diff_add` call site | 1.0k | D1b, D0 | after D1b | Opus |
| D8b | implicit diff tree behind D8's hook: `lyd_val_diff_add` = create/delete subset of `lyd_diff_add` + `lyd_diff_merge_all` (yang:operation, user-ordered key/value/position metadata); needed by ValidateDiff (M6), not by the M1 pilot | 0.6k | D8 | by priority | Opus |
| D9 | xpath/types adapters, `when` queue + auto-delete + `WhenFalse`, dummy when, `node_types` resolution (per-value leafref predicate), musts | 1.2k | D8, #16 | after D8 | Opus |
| D10 | `lyd_validate` (Parse path shared queues vs Validate path per module, `Present` order), `lyd_validate_tree`, `final_r`, siblings-schema (mandatory/min/max/unique), operational severities, Validate/ValidateDiff, parse → validate wiring, `MaxXPathSteps` + ctx | 1.3k | D9, X1, D5 or D6 | after D9 | Opus |
| D11 | edits: NewPath(Update) over `internal/lyxp`, Find, Remove, Merge (no options) | 1.1k | D1b, D8 (flags) | after D8 | Sonnet |
| D12 | public wrappers over `*yang.Schema` via `snap.Set` (+ `internal/snap` and `Context.Schema()` if C8 has not added them), doc.go, Examples, parse fuzz targets, round-trip property, race test; first oracle-agreement run | 0.6k | C8, D7b, D10, D11 | after C8 | Sonnet |
| F | fixtures of §5 (oracle goldens, asserts) | — | — | **now** | codex sol |

Sum ≈ 14.7k incl. tests (≈ 9k libyang C lines in scope × 0.75 + tests). Waves: **now** {S1, X1, D0,
D1, D2, D3, F} → D1b → {D4, D7, D8} → {D5, D6, D7b, D9, D11} → D10 → D12 (needs C8) → M1-7 adapter.
Exit of M1-6: every listed existing fixture and §5 fixture agrees through M1-7 (or is a recorded
deviation), including the `sequence` fixtures with flags and implicit diffs.

## Riskiest points (review focus)
1. Interleaved parse/validate (§0.1): queue orders and per-node-close validation decide error order.
2. Error order under multi-error (§3.3), incl. reverse `node_types` and context module order.
3. `when` queue, `WhenTrue` seeding of implicit nodes, `WhenFalse` marking and descendant removal.
4. Value-sorted system-ordered siblings and top-level module order (§1.4) in every printed tree.
5. Error paths of not-yet-linked lists and schema-only errors (§1.10).
6. with-defaults filtering incl. default containers with state descendants (§1.9).
7. Lexer fidelity (line numbers, exponent rewrite, entity/DOCTYPE refusal) vs fuzz-hardening.
8. Parse-path shared queues vs Validate-path per-module queues (§1.5, §3.3).
9. Phase-dependent details found by review: the `!rc` guard on inner-node close, exact
   `LYVE_SYNTAX` stop, `NoDefaults` honoured only outside the parser's per-node close, `PRESENT`
   traversal order.

## Review log — codex `gpt-6-astra`, 2026-10-06

8 findings (3 high, 5 med), all checked in v5.8.6 source and all accepted: XML whitespace-only term
text is kept (§1.3, PX:768); sibling order must be sorted before JSON metadata attachment, parse-only
included (§4, PJ:1166); leafref value predicate built per value, only the path template cached (§2,
plugins_types.c:994); inner-node close validation guarded by `!rc` (§0.1, PJ:1427); stop only on exact
`LYVE_SYNTAX` (§0.2, parser_internal.h:43, TD:151); `NoDefaults` not forwarded by the parser's close
(§1.8, PC:367); `PRESENT` traverses modules in tree order (§1.5, VAL:2130); XML key position is
checked (STRICT error / warning), JSON is not (§1.3, PX:779). Each has a fixture in §5.

## Review log — port-reviewer (Opus) on `aaa8a22`, 2026-10-06

12 items, all accepted: Parse-path queues shared across modules (§1.5, §3.3, D-0053 restated, cross-
module fixtures); opaque-only tree error per module (D-0057, fixtures); D5/D6 tested on hand-built
schemas, oracle agreement moved to D12/M1-7; `yang.Schema` snapshot + U-0045 and design 05 amended;
`yang.Diagnostic` + single `yang.ErrBudget` reused; `MaxXPathSteps int64` < 2^31 with xpath task X1,
budget errors abort; `context.Context` first parameter, only M1 knobs exported; D8 needs D0, D12 needs
D7b, schema helpers task S1, D1 split (D1b), D7 split (D7b); `WhenFalse` scope reworded (here and
design 02); JSON limit boundary 4998/4999 pinned; `!rc` guard covers missing keys; D-0001/D-0047 cited.
API decisions (4, 5, 7) and the budget type (6) are lead defaults; the maintainer may revisit them.

Re-review (`bbe562d`): `yang.Schema` is an opaque struct in `internal/snap` re-exported by alias,
not an alias of `schema.Set` (design 05 rule kept); §3.3 step 2 includes later modules' implicit
nodes and their unres; order/module-context-order narrowed to top-level duplicate errors.
