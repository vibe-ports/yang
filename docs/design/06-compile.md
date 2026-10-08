# 06 — Schema compilation (M1-5)

Status: design for M1-5 (2026-10-06). Builds on 01–05. Reference: libyang v5.8.6
`src/schema_compile.c` (SC), `schema_compile_node.c` (SCN), `schema_compile_amend.c` (SCA),
`schema_features.c` (SF), `tree_schema.c` (TS), `tree_schema_common.c` (TSC), `context.c` (CTX),
`path.c`, `xpath.c`. Line numbers are v5.8.6 (`make libyang-src`). Inputs from merged / open PRs:
`internal/parser` (`Stmt`, `Build` → `Module`/`Node`/`Type`/`IfFeature{Expr, AST *IffExpr, Err}`),
`internal/ly.Code`, `internal/schema` (merged), `internal/types` (`Store`, `ValidateTree`, plugin
registry keyed by (module, revision, typedef)), `internal/xpath` (`Compile`, `Eval`, `Node`,
`SchemaNode`, `Value`, `NamespaceCtx`).

Scope M1: everything below except deviations, the extension plugins listed as unsupported in §2.17 and
YIN. Deviations are parsed but applied in M2. libyang applies a module's deviations only when that
module becomes **implemented** (`lys_implement` → `lys_precompile_augments_deviations`, SC:2098,
SCA:2526-2541); an import-only module with `deviation` statements is harmless. So `Load` returns
`ErrUnsupported` exactly when a module that has deviations (module or submodule) becomes implemented,
explicitly or implicitly (§1.5); the PR that adds this check records it as **U-0020** (deviations not
applied until M2) in deviations.md "Unsupported". A `.yin` file chosen by the search (§1.2) is `ErrUnsupported` too
(**U-0021**), never skipped in favour of a `.yang` file, because skipping would change which revision
is loaded. "VERIFY(x)" = behaviour read from source but not yet pinned by a golden; fixture `x` must
exist (and agree) before the code relying on it merges. All fixture ids are collected in §6.

## 0. Architecture decision: port libyang's *target-driven* compile

libyang does not compile module by module and then graft augments. It compiles each **implemented**
module (`lys_compile`, SC:1745); while compiling a node it applies the refines of enclosing `uses`
and every augment that targets that node — from any module (`lys_compile_node_augments`,
SCA:2135). The augmenting module's own compiled tree stays empty: in `m1/schema-tree` the dump of
`ietf-ip` and `m1-ext` has 0 nodes, all 141 nodes live under `ietf-interfaces`. We port that shape
1:1 (it decides node order, `module` of augmented nodes, error paths), not a two-pass
"compile then merge" design. Consequence: a compiled node's `Module` is the module whose namespace
it is in (`ctx->cur_mod`, SCN:2521), never the grouping's module.

## 1. Module loading (package `yang`, file `context.go` + `internal/compile/load.go`)

**1.1 Context.** `yang.NewContext(opts Options, dirs ...fs.FS)` = `ly_ctx_new` (CTX:~270).
`Options` is a typed struct for the libyang flags the oracle exposes: `AllImplemented`,
`RefImplemented`, `EnableImportFeatures`, `CompileObsolete`, `DisableSearchdirs`,
`PreferSearchdirs`, plus `Loader ModuleLoader` (the only public callback, PLAN §2 = `imp_clb`).
The context first loads libyang's **internal modules** in this order with these implemented flags
(CTX:58-69): ietf-inet-types@2025-12-22 (no), ietf-yang-types@2025-12-22 (no), ietf-yang-metadata
(yes), yang@2025-01-29 (yes), default@2025-06-18 (yes), ietf-yang-schema-mount (yes),
ietf-yang-structure-ext (no), ietf-datastores (yes), ietf-yang-library (yes). Their texts are
embedded (`internal/models`, copied from libyang's `modules/` directory, licence per file in `conformance/NOTICE`
style). The oracle reads them from `ly_yang_module_dir()` and m1 imports the bundled RFC 9911
revisions from there, so without this step nothing in m1 compiles. `LY_CTX_NO_YANGLIBRARY` drops the
last two (CTX:322).

**1.2 Search** = `lys_search_localfile` (TS:3027) over each `fs.FS` (path "." walked recursively).
File name `name.yang`, `name@REV.yang`, and the same with `.yin` (TS:3118-3125; a chosen `.yin` →
U-0021). With a revision: the first exact
`@REV` file wins; a plain `name.yang` is remembered as fallback and later checked by
`lysp_load_module_data_check` (TS:2019: "Module \"%s\" parsed with the wrong revision" LY_EINVAL).
Without a revision: the newest valid `@REV` across all dirs; a plain `name.yang` only when no dated
file exists. libyang pops directories LIFO (TS:3083) and depends on `readdir` order for ties; we walk
the directories in lexical order and treat any tie (same module+revision in two places) as a
VERIFY(load/tie) case — goldens must not depend on it. **Symlinks:** libyang follows them — an entry
with `d_type` `DT_LNK`/`DT_UNKNOWN` is `stat`ed (target type), so a symlinked directory is descended
into and a symlinked file is a candidate (TS:2957-3020); there is no cycle detection, a symlink loop
only ends when `opendir` fails on an over-long path (`LOGWRN(NULL, …)`, not stored, TS:3091-3095).
`fs.WalkDir` does not follow symlinks, so the search is our own walk: `fs.ReadDir`, and for
`ModeSymlink` entries `fs.Stat` to get the target type (works for `os.DirFS`; an `fs.FS` without
symlinks is unaffected). Bounds: a directory path longer than 4096 bytes (`PATH_MAX`) is skipped
silently like libyang's failing `opendir`, and `Budget.MaxSearchDirs` (default 10 000 directories
per search) turns a symlink cycle into `ErrBudget` instead of 4096-byte-deep churn — **U-0022**,
added with the code. Fixtures: load/symlink-dir (+, module only reachable through a symlinked
directory; corpus symlink committed to git), load/symlink-file (+); a Go test builds a symlink cycle
in `t.TempDir()` and checks the budget error and its run time. Order of sources: `Loader` first unless
`PreferSearchdirs` (TSC:834-865). Not found: `Loading "%s@%s" module failed, not found.`
LYVE_REFERENCE (TSC:868). A module read from a file is checked by `ly_check_module_filename`
(TS:1970, called at TS:2744 and TS:2066): warnings `File name "%s" does not match module name "%s".`
and `File name "%s" does not match module revision "%s".` (the latter also for a dated file name of a
module without revision). Fixture load/filename-warning.

**1.3 Revision selection** = `lys_parse_load` (TSC:938). Revision given → exact context lookup,
else load. No revision → `lys_get_module_without_revision` (TSC:887): the module flagged
`IMPORTED_REV`, else the implemented one, else the latest in context; a found module that is neither
implemented nor `IMPORTED_REV` is only a candidate (`mod_latest`): the searchdirs/loader are asked
for a newer one first (TSC:954-979). An import without `revision-date` sets `IMPORTED_REV` on the
chosen module (TS:1425-1431), so later revision-less imports bind to the same revision. Several
revisions of one module may coexist (only one implemented: `Module "%s@%s" is already implemented in
revision "%s".` LY_EDENIED, SC:2071). Duplicate name+revision → reuse (TS:2725); same namespace,
same revision, different name → LY_EINVAL (TS:2734).

**1.4 Imports/includes** = `lysp_resolve_import_include` (TS:1414) + `lysp_load_submodules`
(TSC:1246). Imports in order, recursively; a module whose `parsing` flag is still set →
`A circular dependency (import) for module "%s".` LYVE_REFERENCE (TSC:928). "Single revision of the
module imported twice" is a warning (TS:1436). Includes: belongs-to mismatch, `A circular dependency
(include)` (TS:2060), YANG 1.1 "all submodules must be included from main module" and revision
mismatch messages (TSC:1054-1070); YANG 1.0 submodule-only includes are injected into the main
module (TSC:1132). After resolution, per parsed module, in this order (TS:2780-2796, for implemented
**and** import-only modules):
1. extension-instance records (`lysp_resolve_ext_instance_records`, TS:1893): every instance is bound
   to its definition (`lysp_ext_find_definition`, error if the prefix or the extension is unknown);
   without plugins (M1) the instance keeps its raw substatements — this is the "generic" record of
   §2.17;
2. name-collision checks `lysp_check_dup_{typedefs,groupings,features,identities}` (TSC:366-660). The
   tokenizer on main (#13) does none of them and `parser.check` (#14) only checks identifiers of one
   statement kind within one scope, so C1a ports all four (shadowing across scopes and submodules
   included);
3. **P0** (§2), then `lys_compile_submodules` (TS:2611), which only records each included submodule's
   name, revision and file (→ `schema.Module.Submodules`); submodule *content* is compiled as part of
   the main module (SC:1793-1814, §2 P3).

**1.5 Implemented vs import-only.** `Context.Load(name, rev string, features []string)` =
`ly_ctx_load_module` (CTX:228) → `_lys_set_implemented` (TS:991) → `lys_implement` (SC:2060):
`lys_set_features` (SF:579: `nil` = untouched, `[]` = all off, `["*"]` = all on, unknown name →
`Feature "%s" not found in module "%s@%s".` LY_EINVAL). Import-only modules: all features off,
identities/features/typedefs/groupings usable, no data tree. Modules become **implemented
implicitly** at these points — all in M1 scope:
1. every module on the path of a top-level augment (and deviation, M2) target
   (`lys_precompile_augments_deviations`, SCA:2577-2626), features off unless `EnableImportFeatures`;
2. every module referenced by a prefix in a **leafref** path — always, even without
   `RefImplemented` (`lys_compile_expr_implement(..., implement=1)`, SC:1166/1178, SC:398);
3. modules referenced from `when`/`must` only with `RefImplemented`; otherwise the check is skipped
   with a warning `When|Must condition "%s" check skipped because referenced module "%s" is not
   implemented.` (SC:1188-1224);
4. identity / instance-identifier modules in **defaults** only with `RefImplemented`
   (`LYPLG_TYPE_STORE_IMPLEMENT`, SC:964; identityref.c:214), else the default fails
   (`identity found in non-implemented module`);
5. `AllImplemented`: every newly created module (TS:1017).

**1.6 Phase boundary.** The oracle runs `ly_ctx_load_module` under `LY_CTX_EXPLICIT_COMPILE`, then
`ly_ctx_compile` (lyoracle.c:929, :965, :974). With explicit compile `ly_ctx_load_module` stops after
`lys_parse_load` + `_lys_set_implemented` (CTX:234-240), so **phase `parse`** = search, parse,
imports/includes, everything of §1.4 (incl. P0 and leafref path syntax, §2.15), feature setting, and
all of `lys_implement` (SC:2060-2103): implemented-revision collision, `lys_set_features`, marking
augment/deviation target modules implemented (SCA:2577), `lys_has_compiled_import_r`. **Phase
`compile`** = `ly_ctx_compile` (CTX:568): dep sets, `lys_check_features` (SC:1584, SF:552:
`Feature "%s" cannot be enabled because its "if-feature" is not satisfied.` LY_EDENIED), `lys_compile`,
unres. Our `Load` keeps the two steps separate internally (`loadParsed`, then `compileDepSets`) and
tags each diagnostic with its phase; an error in either runs libyang's revert (CTX:257-260,
CTX:584-587), which is not fully atomic (§1.7).

**1.7 Dependency sets and recompilation** — ported, not approximated (recompiling everything would
re-emit compile warnings of untouched modules and change compile order). `lys_unres_dep_sets_create`
(TS:1198) with `compile_set = NULL` as `ly_ctx_compile` calls it: modules that are
`LYS_IS_SINGLE_DEP_SET` (no features, and not compiled or compiled-and-unchanged,
`tree_schema_internal.h:795`) get one-module sets first (TS:1168); the rest are grouped by the import relation in both
directions (TS:1068: imports, submodule imports, then importers in context order), and a set that
contains any `to_compile` module marks **all** its implemented modules `to_compile` (TS:1256-1264).
`lys_compile_depset_all` (SC:1604) then per set: `lys_check_features` for `to_compile` modules, and
`lys_compile_depset_r` (SC:1524): `lys_compile` for each `to_compile` module **in dep-set order**,
global unres; `LY_ERECOMPILE` (raised when unres implements a module that augments/deviates an
already compiled one, SCA:2620-2624, or that imports a compiled implemented module, SC:2035-2057)
restarts the whole set; a newly implemented module that needs no recompilation is compiled and unres
re-run (SC:1560-1567). Dep-set order is observable (§2.3 leafref typedef binding; first error).
**Rollback is not atomic in libyang, and we mirror it.** `lys_unres_glob_revert` (TS:1293-1343)
undoes exactly two things: modules *newly implemented* by the failed operation become import-only
again (compiled freed, `to_compile` cleared, augment/deviation links reverted), and modules *newly
created* are removed from the context; then the previous state is recompiled with logging suppressed
(an error there is logged as `LOGINT` after logging is restored, TS:1336-1341). It does **not** undo
`lys_set_features` on an already implemented module (`_lys_set_implemented`, TS:998-1010, which also
sets `to_compile`). Reproduced by astra with three loads of one module `m` (features `a` with
`if-feature b`, and `b`): (1) `features: []` → accepted, all off; (2) `features: [a]` → compile fails
`Feature "a" cannot be enabled …` — `a` stays enabled in the parsed module and `m` stays `to_compile`;
(3) `features: null` ("untouched") → fails again with the same error. C1b therefore ports
`lys_unres_glob_revert` in place rather than restoring a copy-on-write snapshot: the parsed feature
flags (and `to_compile`) of modules that were already implemented before the failed `Load` survive
the rollback by construction, exactly as above. The loader's `c.Modules[*].Schema` is therefore
**mutable working state**: a later `Load` appends `Identity.Derived` on older modules, sets
`Implemented`, and `free` clears `Features`/`Top` before a recompilation. Readers never see it
directly: C8 publishes a deep copy (§4). Fixture load/feature-rollback-3load (closed): (2) and (3) fail with the same error, no
internal error is logged (nothing newly implemented, so no recompilation), and the final dump shows
`a` enabled. An
atomic rollback would be friendlier, but it changes observable verdicts, so it is not done (no
deviation). Fixtures: load/two-failures (an older module that now fails
is reported under the new module's load), load/warning-once (a "Locally scoped grouping not used"
warning of module A is not repeated when unrelated module B is loaded later),
load/lref-implements-augmenter (a leafref prefix implements an import-only module whose augment then
appears in its target via `LY_ERECOMPILE`).

## 2. Compilation phases and order (`internal/compile`)

**P0 at load (all parsed modules).** `lys_compile_feature_iffeatures` (SF:695): compile each
feature's if-features, `Feature "%s" is referenced from itself.` / `is indirectly referenced from
itself.` LYVE_REFERENCE (BFS over `depfeatures`, SF:655). `lys_compile_extensions` (SC:1948)
definitions. `lys_compile_identities` (SC:1981): precompile all identities of module + submodules
(SC:177), then bases → **derived** back-links (`lys_compile_identities_derived` SC:369,
`lys_compile_identity_bases` SC:288): `Multiple bases ... only in YANG 1.1`, `Invalid prefix used for
base`, `Unable to find base`, `derived from itself`, `indirectly derived from itself` (BFS SC:246).
Errors in submodule identities carry the path `/<mod>:{submodule='<sub>'}/{identity='<id>'}` (main
module: `/<mod>:{identity='<id>'}`), derived by hand from `lysc_update_path` (SC:52-107) and
SC:2004-2011 — fixture errpath/submodule-identity pins it.
Identities exist for import-only modules (needed by identityref). `schema.Identity.Disabled` =
`lys_identity_iffeature_value` (SF:101) evaluated with the final feature set.

**P1** `lys_check_features` (above). **P2** `lys_compile` (SC:1745): copy enabled features; collect
augments of modules in `augmented_by` targeting this module (`lys_precompile_own_augments`,
SCA:2292: per augmenting module, module then its submodules, statement order). **P3** compile
data nodes, then rpcs, then notifications of the main module, then the same per submodule
(SC:1776-1814). **P4** validate unused groupings (SC:1817-1849, `LYS_COMPILE_GROUPING`: nothing goes
to unres, list-key rule relaxed SCN:3237; warning `Locally scoped grouping "%s" not used.` SCN:4066).
Scope, exactly: top-level groupings of the main module and of each submodule, and groupings
declared **directly** in a top-level data node (`lysp_node_groupings(pnode)` for `sp->data` only).
Groupings nested deeper, inside rpcs/notifications, inside other groupings, or in import-only modules
are never validated when unused. "Used" is the parsed-grouping flag `LYS_USED_GRP`, set by any
non-grouping-validation `uses` (SCN:3741-3744) — also one in a disabled subtree or in another
module — and **never cleared** (only SC:1821-1843 read it): once used in some compile, a grouping is
not validated again, so whether module A's unused-in-A grouping is validated depends on whether a
module compiled earlier in the dep set used it. Fixtures grp/unused-local-warning,
grp/nested-unused-not-validated (an invalid nested grouping is accepted), grp/used-elsewhere-first.
**P5** `lys_compile_unres_mod` (SC:1623): every augment left unapplied → `Augment target node "%s" from
module "%s" was not found.` LYVE_REFERENCE, **all** logged, then fail. **P6** global unres
`lys_compile_unres_depset` (SC:1318), exact order — the sets are drained in the stated direction,
which decides which error is reported first:

| step | what | order | ref |
|---|---|---|---|
| a | implement modules referenced by leafrefs (always), when/must (option) | FIFO, loop until stable | SC:1146 |
| b | leafrefs in disabled nodes: resolve target only | LIFO | SC:1341 |
| c | leafrefs round 1: target, status, require-instance config, circular chain | FIFO | SC:1360 |
| d | leafrefs round 2: `Realtype` = first non-leafref type | FIFO | SC:1370 |
| e | `when`: schema atomize, status warnings, own-children/value, cycles | LIFO | SC:1390 |
| f | `must`: schema atomize, status warnings | LIFO | SC:1403 |
| g | remove disabled enums/bits; none left → error | LIFO | SC:1416, SC:775 |
| h | defaults through `types.Store` | LIFO | SC:1428 |
| i | new items from h (implemented modules) → back to a | — | SC:1445 |
| j | free disabled nodes: `Key "%s" is disabled.` (only a key removed by if-feature; an obsolete key already failed the status check, §2.11), fix `unique` | FIFO | SC:1451, SC:1245 |
| k | leafref target must not be disabled | FIFO | SC:1462 |

**2.1 Per node** `lys_compile_node_` (SCN:2510), order matters: apply refines (and M2
deviations) to a *copy of the parsed node* (SCA:1885) → if-features (§2.2) → flags: config (§2.9),
status (§2.11), obsolete → disabled unless `CompileObsolete` (SCN:2550: obsolete nodes are **absent**
from the tree) → ordered-by (§2.9) → connect into parent (SCN:2302; name uniqueness) → own `when`
(context node = `lysc_data_node(node)`, SCN:2579) → kind-specific part → extension instances →
`mandatory` propagation to NP-container ancestors (SCN:2588, SCN:3623; this is why
`statistics` containers dump `mandatory: true`). This propagation runs for **disabled and obsolete
nodes too** (no check at SCN:2588), and removing them in step j does not undo it (the only "unset"
call is SCA:2036): an NP container whose only mandatory child is if-feature-disabled still dumps
`mandatory: true`. Fixture mand/disabled-child-propagates. Kind parts: container (SCN:2741: children, musts,
augments, actions, notifs), list (SCN:3207: min/max, children, musts, key checks and keys moved to
the front in key order, augments, uniques, actions/notifs, min>max), leaf (SCN:2847: musts, units,
type, default, mandatory+default error), leaf-list (SCN:2893: 1.0 default ban, min ⇒ mandatory,
state ⇒ ordered-by user, default+min error, min>max), choice (SCN:3528: cases incl. shorthand,
augments, default case), case, anydata, action/input/output (SCN:2613-2696; implicit input/output
always exist), notification. Actions/notifications inside data nodes are kept apart from data
children (they are dumped right after their parent, before its children — see `/pv2:c/l/reset`).

**Connect order** (SCN:2330-2370) is observable in every dump: keys first; a child of the parent's
module goes after the last same-module child; children added by augments go after the module's own
children, grouped per augmenting module, groups sorted by `strcmp` of module names (SCN:2350-2366). Observed (order/two-augmenters): modules
loaded base, z, a give `own`, `a:aa`, `z:zz`; the control order/two-augmenters-sorted-load (base, a, z)
gives the same order, so load order never matters.

**2.2 Features and if-feature.** Node, uses, augment, enum/bit, identity if-features are evaluated by
`lys_eval_iffeatures` (SF:520): expressions in order, **stop at the first false one** — so the parse
error or unknown-feature error (`Invalid value "%s" of if-feature - unable to find feature "%.*s".`,
SF:487) of a *later* if-feature is never reported. VERIFY(iff/err-after-false). The parser already
produced `IfFeature.AST`/`Err`; compile reports `Err` (with the compile path, it has no line) at the
moment libyang would call `lys_compile_iffeature`. Feature names resolve against the **defining**
module (`qname->mod`; a grouping's if-feature uses the grouping's imports, SF:182). Evaluation walks
`IffExpr` with an explicit stack (the tree depth is bounded only by the expression length).
A false if-feature does not skip compilation: the node is compiled with `LYS_COMPILE_DISABLED`
(nothing it contains enters the when/must/default/bitenum queues; its leafrefs go to the *disabled*
set, SCN:162) and is removed in step j. Same for children of a disabled `uses` (SCN:3893,
SCN:3806) and `augment` (SCA:2106, SCA:2050). Disabled enum/bit items are dropped in step g.

**2.3 Typedef chains** = `lys_compile_type` (SCN:1939). Walk the typedef chain with
`lysp_type_find` (scoped typedefs, then module/submodules, then imports), checking status per hop,
inheriting `units` and `default` from the nearest typedef that has them (SCN:1972-1980). Circular
chains: local chain + the context-wide stack used by nested unions (SCN:2009-2031, `Invalid "%s" type
reference - circular chain of types detected.` LYVE_REFERENCE). Unknown base → `Referenced type
"%s" not found.` Then compile from the built-in outwards (SCN:2059-2112); each typedef's compiled
type is cached on the parsed typedef with a **holder count** (§ "Typedef cache" below) and reused by
every user while it is held by someone besides the cache.
Rules that shape `schema.Type.Typedef` (= libyang `lysc_type.name`, which the types registry and the
oracle `typedefs` field read):
- a typedef that adds nothing (no restriction, no extension, same plugin, not leafref) **reuses its
  base's compiled type**, pointer and name included (SCN:2084-2088): `typedef my-addr { type
  inet:ipv4-address; }` compiles to the type named `ipv4-address`;
- a typedef with changes gets a new type named after itself; the plugin is looked up by
  (module, revision, typedef) first, else inherited from the base (SCN:2073-2081) — internal/types
  `pluginFor` walks `From`, which gives the same answer only if compile keeps the reused pointer;
- the leaf's own `type` statement creates a new type only if it has restrictions/extensions, no base,
  or a leafref anywhere (SCN:2135); that type is named after the nearest typedef (SCN:2147);
  otherwise the leaf shares the typedef's type.

**Typedef cache = libyang's refcount, mirrored.** libyang caches a typedef's compiled type on the
parsed typedef (`tpdf->type.compiled`, SCN:2110) and **frees and recompiles it whenever its refcount
is 1**, i.e. when nothing but the cache holds it (SCN:1986-1991) — not only "on recompilation". The
increments that make a second holder are: storing the cache (SCN:2111), an unchanged derived typedef
reusing it (SCN:2088), a union member slot (SCN:1438, nested-union members SCN:1449, members copied
from a union base SCN:1895), and a leaf/leaf-list taking a type unchanged (SCN:2804 after SCN:2153).
A leaf whose type contains a leafref always gets its own new type (SCN:2135) that *copies* the
base's path and prefixes (SCN:1842-1846) or member pointers (SCN:1887-1897) but never holds the base
itself. A leafref's `realtype` is one more holder (SC:936, SC:1382). Our cache (`compile.typeCache`):
`compiled map[*parser.Node]*schema.Type` plus `refs map[*schema.Type]int` — the count is **per type
object**, not per typedef, because an unchanged derived typedef stores its base's object (so `u` and
`d` above share one count, which never drops below 2 again). Incremented at exactly those points and
decremented when a holder's module is dropped from the snapshot at recompilation
(`lysc_module_free` of the modules of a recompiled dep set, SC:1539), cascading to union members
and realtype like `lysc_type_free`; lookup with `refs == 1` discards and recompiles, as SCN:1986.
Lifetime = the context **snapshot**, not one compile run: types held by modules of other, not
recompiled dep sets survive a later `Load`.

**Leafref typedef consequences** (all conditional on a second holder; verified in source):
1. *Direct use — no quirk.* `typedef ref { type leafref { path "../x"; } }` used by leaves in A and B:
   the cache holds `ref`'s type alone (the leaves hold their own copies), so every use recompiles it
   and `Prefixes[""]` = each instantiating `cur_mod` (SCN:1841). Same for a union typedef with a
   leafref member used directly: each leaf recompiles the base and gets fresh members. A derived
   typedef of a leafref is never reused (`basetype != LY_TYPE_LEAFREF`, SCN:2084), so it does not
   create a holder either. Fixtures lref/typedef-prefix-direct, lref/union-typedef-direct (positive:
   per-module binding, per-leaf targets).
2. *Via an unchanged derived union typedef — shared.* `typedef d { type u; }` with `u` a union
   containing a leafref: `d` reuses `u`'s compiled type (SCN:2088, holders 2), so `u` is not
   recompiled; leaves of type `d` copy the **same member pointers** (SCN:1895), the leafref member is
   resolved once, for the first node (SC:875-878 "already resolved ... shared union typedef with a
   leafref"), and its `Prefixes[""]` is the module that compiled `u` first. Fixture
   lref/union-typedef-via-derived-typedef (two leaves, in two modules, whose relative paths point to
   targets of different types and namespaces).
3. *Across Loads.* A holder created in an earlier `Load` whose dep set is not recompiled now keeps the
   cached type alive, so case 2 can bind to a module compiled in a previous `Load`. Fixture
   lref/typedef-across-loads (a `sequence`-style two-module `schema` request where the second module
   lands in another dep set).
Per base type (`lys_compile_type_`, SCN:1573): range/length parsed and intersected with the base
(SCN:859; must be a subset, inherited via `lysc_range_dup` SCN:356), patterns appended to the base's
(SCN:1139, compiled by `types.CompilePattern`/xsdre), enum/bit values and positions assigned and
checked as a subset of the base (SCN:1239), decimal64 `fraction-digits` only directly on decimal64,
identityref `base` only directly on identityref (SCN:1766), leafref `require-instance` 1.1-only and
inherited, path required, path `Prefixes` with `""` bound to the **instantiating** module
(`prefixes[0].mod = ctx->cur_mod`, SCN:1841), restriction-not-allowed checks via
`type_substmt_map` (SCN:2052, SCN:2092), `empty` typedef with default rejected (SCN:2097).

**2.4 Unions** (SCN:1426): members compiled in order; a member that is itself a union is replaced
in place by its members (recursively already flat) — `schema.Type.Union` is flat, member indexes
and the union error text depend on it. A union typedef used unchanged copies the member pointers
(SCN:1887). VERIFY(types/union-nested-index) for a nested union with a leafref member.
Flattening is **exponential** in the text: `typedef u1 { type union { type u0; type u0; } }` …
`u30` has 2^30 members from 31 lines of text. Every member slot counts against
`Budget.MaxTypes` and the union's width against `Budget.MaxUnionMembers` (§5), checked before the
array grows (SCN:1443).

**2.5 Groupings and uses** (SCN:3838). Find the grouping (SCN:3676): unprefixed or own prefix →
scoped groupings up the parsed parents, then top-level groupings of the main module and every
submodule; other prefix → that import's module. `Grouping "%s" referenced by a uses statement not
found.` LYVE_SEMANTICS. Recursion `Grouping "%s" references itself through a uses statement.`
(stack, SCN:3855). Then: action/notification placement checks, status of the grouping, precompile
this uses' refines and augments (SCA:329), uses status, uses if-feature, compile the grouping's
children, actions, notifications **with `pmod` = the grouping's module** (SCN:3783) but
`node.Module` = the module being compiled. This is design 03 rule 1, concretely:
- explicit prefixes in `when`, `must`, `type`, `default`, `if-feature`, `path` resolve through the
  **grouping's** module imports (`NSCtx` captured at node compile time);
- unprefixed XPath names resolve to `node.Module` (`set->cur_mod`, xpath.c:5750); unprefixed leafref
  path names to `cur_mod` (SCN:1841).
A `when` on the uses is compiled once and shared by every child (`when_shared`, SCN:3802), context
node = `lysc_data_node(parent)`. Afterwards every refine/uses-augment of *this* uses still
unapplied is an error: `Augment target node "%s" in grouping "%s" was not found.` /
`Refine(s) target node "%s" in grouping "%s" was not found.` LYVE_REFERENCE, all logged
(SCN:3914-3937). Uses and grouping extension instances move to the parent (SCN:3940).

**2.6 Refine** (SCA:893) edits the parsed copy before compilation, with `cur_mod`/`pmod` switched to
the refine's module: default (leaf 1, leaf-list 1.1 only, choice), description, reference, config
(warning inside rpc/notification), mandatory (leaf/choice/any), presence, must (appended), min/max,
if-feature (appended), extensions. Refines with the same target collected from nested uses are
merged into one record, keyed by node-id text and module (SCA:297-326), and applied in collection
order; observed (refine/nested-same-target): **the innermost uses' refine wins** (SCA:363-399,
1913-1915), so the compile must apply the outer refine first and let the inner overwrite. The merge ignores the context node: if a same-text inner refine is collected while the outer refine is still pending (the outer target is compiled after the inner uses), it is applied to the outer target and lost for its own (D-0048; refine/same-text = no overlap, refine/same-text-leak = leak). A failure adds a trailing
`Compilation of a deviated and/or refined node failed.` LYVE_OTHER (SCN:2605).

**2.7 Augments.** Top-level augments are applied when their target finishes its own children:
`lys_compile_node_augments` (SCA:2135) first applies matching **uses-augments**, then matching
**top-level augments**, restarting the scan after each application (so augments of nodes added by
augments work in any statement order). `lys_compile_augment` (SCA:2081): placement checks, augment
if-feature, children (case shorthand when the target is a choice), then actions, notifications,
extensions into the target. Mandatory rule (SCA:1994-2042): a config-true mandatory child is allowed
only if the augment has `when`, targets a choice, or targets its own module; otherwise
`Invalid augment adding mandatory node "%s" without making it conditional via when statement.`
LYVE_SEMANTICS. The augment's `when` is shared by all its children with context
`lysc_data_node(target)` (SCA:2046). Target nodeids: `lys_nodeid_mod_check` (SCA:202) +
`lysp_schema_nodeid_match` (SCA:1735).

**2.8 Choice/case.** Shorthand: a non-case child gets an implicit case of the same name whose status
is copied from the child (SCN:3460-3505). Default case (SCN:3402): prefix via the defining module,
`Default case "%s" not found.`, `Mandatory node "%s" under the default case "%s".`, `Invalid
mandatory choice with a default case.` Dump: a choice's `defaults` = default case name.

**2.9 Config / ordered-by.** `lys_compile_config` (SCN:2442): inside rpc/action/notification config is
dropped (`LYS_COMPILE_NO_CONFIG`); otherwise inherited, default true; `Configuration node cannot be
child of any state data node.` LYVE_SEMANTICS. List keys must match the list's config (SCN:3289).
Ordered-by: lists/leaf-lists that are state, output or notification content are always `user`
(SCN:2559-2571); keyless lists `user` (SCN:3257).

**2.10 Mandatory / cardinality.** Covered in 2.1/2.7/2.8; config lists need a key unless inside an
unused grouping without explicit config (SCN:3235-3251).

**2.11 Status** (SCN:439 + SC:559). Own status: explicit > inherited from uses/augment > parent >
current; conflicts are LYVE_SEMANTICS errors (three messages, SCN:448-460). **References**
(`lysc_check_status`, SC:559): an error (`A %s definition "%s" is not allowed to reference %s
definition "%s".` LYVE_REFERENCE) only if the referrer's status is *more current* than the referenced
one **and `mod1 == mod2`** (SC:567) — a reference into another module is never checked (RFC 7950 §7.21.2
says "within the same module"). The identity compared differs per call site, which matters for
submodules: uses→grouping compares **parsed modules** (`ctx->pmod` vs grouping's pmod, SCN:3879), so
main module ↔ submodule (or submodule ↔ submodule) references are not checked; type→typedef also
compares pmods (SCN:1968); list→key compares compiled modules (SCN:3310; an **obsolete key of a current list fails here**, `A current definition "l" is not allowed to reference obsolete definition "k".`, before step j — fixture obsolete/key; `Key "k" is disabled.` is only reachable through an if-feature, fixture list/key-iffeature-disabled); leafref→target compares
`local_mod->mod` with `target->module`, with "current" forced for a foreign definition (SC:901-910).
when/must use the same rule but only **warn** (`When|Must condition "%s" may be referencing %s node
"%s".`, SC:627-631, SC:711-724). Fixtures: status/same-module-error, status/cross-module-ok
(positive), status/submodule-uses-ok (positive), status/when-warning.

**2.12 Defaults.** Stored raw as `schema.DefaultValue{Lex, NS}` (NS = the module where the default
text is written: the leaf's, the refine's, or the typedef's) and validated in step h through
`types.Store(t, lex, FormatSchema, HintSchema, SchemaText{NS}, node)` = `lys_compile_unres_dflt`
(SC:954). `NeedsTree` results (leafref, instance-identifier, union with such a member) count as
success (SC:971 `LY_EINCOMPLETE`), so `union { leafref; string }` defaults are kept raw and decided
per data tree. Failure: `Invalid default - value does not fit the type (%s).` LYVE_SEMANTICS with the
types diagnostic text. Keys and mandatory leaves ignore defaults (SC:1026, SCN:3313). A leaf's own
default replaces the typedef's (SCN:2807, SCN:2869). Config leaf-list duplicate defaults compare the
**lexical** strings, not canonical values (SC:1077) — VERIFY(dflt/llist-dup-noncanon) with `"1"` vs
`"01"` on uint8. The oracle dumps canonical defaults via `lyd_value_validate_dflt`, raw text on
failure (lyoracle.c:713); the adapter does the same through `types.Store`.

**2.13 must / when compile.** At node compile time: `xpath.Compile(src, ns)` (syntax errors here,
LYVE_XPATH, path = node) where `ns` = prefixes of the **defining** module (`pmod`; must uses
`must_p->arg.mod`, SCN:570) and `Default()` = `node.Module.Name`. `schema.Must/When.Ctx` keep the
defining-module bindings (`""` = defining module, which the oracle dumps as `when.module`).
`When.ContextNode` = `lysc_data_node(...)` as in 2.1/2.5/2.7. Status of a when = inherited
(SCN:511). In unres (e, f) libyang runs `lyxp_atomize` (xpath.c:10218) over the schema with
`LYXP_SCNODE_SCHEMA` (+ `LYXP_SCNODE_OUTPUT` under output, SC:601, SC:690). That is a second
evaluator, not a flag on `Eval`: every axis, predicate and function has a schema branch
(`*_scnode` paths throughout xpath.c), and it is where libyang emits most compile **warnings** —
81 `LOGWRN` sites in xpath.c: "Schema node "%s" [for parent "%s"] not found; in expr …"
(xpath.c:8300-8327; ported with the C2a walk, the other warnings are C2b), operand checks (`warn_operands`, xpath.c:3570-3611: non-numeric node, node
type used as operand, incompatible comparison), literal-fits-type (`warn_equality_value`,
xpath.c:3631-3685: identityref without prefix; `Invalid value "%s" which does not fit the type
(%s).`, which runs the type plugin's **store**), 70 per-function argument-type warnings
(xpath.c:3716-5545: 42 string-argument and 28 node-set/numeric/YANG sites), each followed by `Previous warning generated by XPath subexpression[%u] "%.*s"
with context node "%s".` (xpath.c:3554). An atomize *error* is reported by the caller as `Invalid
when|must condition "%s".` LYVE_SEMANTICS at the node (SC:606-609, SC:697-700, and SC:499-502 in the
cycle check). Since the oracle stores warnings and the comparator compares them (§3), all of them
are ported — no deviation; a schema fixture that triggers a not-yet-ported warning simply stays
`differ` until C2b lands.

API (internal/xpath, still no import of schema/types):
`(*Expr).Atomize(ac AtomizeContext) ([]Atom, error)`; `Atom{Node SchemaNode; Use AtomUse}`,
`AtomUse int32` with libyang's values (xpath.h:268-277): `START` -2, `START_USED` -1, `ATOM_NODE` 0,
`ATOM_VAL` 1, `ATOM_CTX` 2, `ATOM_NEW_CTX` 3, and `ATOM_PRED_CTX` 4 **and above** (one level per
nested predicate) — the cycle check and "own value" check compare these numbers;
`AtomizeContext{Node, Root (all|config), Output bool, Schema SchemaInfo, Warn func(msg string),
MaxSteps}`. `SchemaNode` grows what the schema branches read: `Parent()`, `Status()`,
`InOutput()`/`IsInput()`, `NodeType` incl. choice/case/input/output, `Path()` (LYSC_PATH_LOG, for
the warning texts), `BaseType()` (+ member base types for unions), and a value-check callback
`CheckValue(lexical string, pc NamespaceCtx) (errMsg string, ok bool)` implemented by compile over
`types.Store(t, lex, <expression format>, HintData, <the expression's prefix context>, node)` —
exactly `store(..., set->format, set->prefix_data, LYD_HINT_DATA, scnode, ...)` at xpath.c:3666-3667,
not the schema hint of defaults; identityref leaves are skipped (xpath.c:3664); `LY_EINCOMPLETE`
counts as ok (xpath.c:3668) so xpath never imports types. The existing narrow
schema walk in `eval.go` (`walkAtoms`/`atomsOK`, the dependency check of
`eval_name_test_try_compile_predicate*`) is reimplemented on top of `Atomize` in C2a, so there is
one schema walker (and D-0013 may shrink — re-measure). Split: **C2a** atomize core (sets with
in-context marks, all axes over `SchemaNode`, predicates, every function's schema branch, node-not-
found warnings, step budget) ≈ 1.3k; **C2b** operand / value-fit / per-function argument warnings
+ subexpression trailer ≈ 0.9k. Fixtures must/unknown-node-warning, must/value-not-fit-warning,
when/func-arg-warning, when/invalid-condition.

After atomize: `When condition is accessing its own conditional node children.` / `... value.`
LYVE_SEMANTICS (SC:634-646), status warnings (§2.11), and the cycle check.

**2.14 when cycles** = `lys_compile_unres_when_cyclic` (SC:457), ported literally over `[]Atom`
(set closure with in-context marks, walking the whens of choice/case parents, replacing the context
node by the dependent node SC:528-539): `When condition cyclic dependency on the node "%s".`
LYVE_SEMANTICS. Iterative (worklist), no recursion. Closes design 03 rule 5.

**2.15 Leafref.** Path syntax is checked **at parse time** in libyang (parser_yang.c:2354
`ly_path_parse(..., LY_PATH_BEGIN_EITHER, LY_PATH_PREFIX_OPTIONAL, LY_PATH_PRED_LEAFREF)`), so its
errors must surface in the `parse` phase of `Load`, with the parser's line (closes U-0005: e.g.
`deref(../x)/.`). The grammar therefore has to be callable from `parser.Build`, which may import only
the stdlib and `internal/ly`. New leaf package **`internal/lyxp`** (stdlib + `internal/ly`): the
XPath tokenizer of `lyxp_expr_parse` (today duplicated as `internal/xpath/lex.go` and
`internal/types/xpathlex.go`) and `ly_path_parse` with all its modes (path.c:325: begin
absolute/either, prefix optional/mandatory/first, predicate simple/leafref, `deref()` path.c:280)
plus `ly_path_check_predicate` (path.c:468). Users: `parser.Build` (leafref `path`, raising the error
in the parse phase), `internal/types` (instance-identifier; its `pathParse` moves here) and
`internal/xpath` (lexer). `ly_path_compile_leafref` (path.c:1178, :1322) needs schema nodes and stays
in `internal/types/path.go` beside the instance-identifier `pathCompile`. Fixture
lref/path-syntax-parse-phase (verdict, phase `parse`, line). Step c (SC:866): target must be leaf/leaf-list (`... target node is %s instead of leaf or
leaf-list.`), status, require-instance config→state error, circular chain (SC:822, iterative over
union members). Step d: `Type.Realtype` = **first non-leafref type** of the chain (SC:1376), the same
pointer as the target leaf's `Type` when that is not a leafref — types `ValidateTree` relies on
pointer identity (fix the `tree.go` comment that says "target leaf's own type").
`Type.PathCompiled` = the compiled path. Disabled-node leafrefs: only target existence (step b).

**2.16 Unres bookkeeping.** Exactly P6's sets, appended where libyang appends (SCN:63-302:
`lysc_unres_{when,must,leafref,leaf_dflt,llist_dflts,bitenum}_add`, all skipped in groupings and
disabled subtrees, defaults *replaced* when added twice for one node). No other deferred work.

**2.17 Extension instances.** The parse-phase record (§1.4 step 1) is bound to its definition,
compiled by `lys_compile_ext` (SC:114: argument, module = `cur_mod`, parent, nested instances, path
segment `{ext-inst}` + name) and stored as `schema.ExtInstance{Def *Module, Name, Argument, Exts}`.
Unknown definition at compile (`lysc_ext_find_definition`) is an error as in libyang. Placement as
libyang (node, uses+grouping → parent, augment → target, type exts incl. typedef chain
SCN:1915-1927). An instance whose definition has **no** plugin stays generic — that is all libyang
does with it too.

**Built-in extension plugins** (registered unconditionally, plugins.c:600-634) are not "generic":
their `parse` callbacks run in the parse phase for **every** parsed module, import-only included
(TS:1919-1950, error → the load fails), their `compile` callbacks during compile of implemented
modules. Their errors are logged as `LY_EPLUGIN | err` with LYVE_OTHER (log.c:769) at the
ext-instance path (`lysp_ext_instance_path`, TS:1929). Silent acceptance of an instance libyang
rejects is never allowed, so each plugin is either ported (checks + effect) or makes `Load` fail
with `ErrUnsupported` as soon as an instance of it is parsed (in any module, before compile):

| plugin (module / extension) | libyang | M1 |
|---|---|---|
| ietf-yang-metadata `annotation` | metadata.c:49-121 parse: only at module/submodule top level, not instantiated twice with one name, allowed substatements, **mandatory `type`** (`Missing mandatory keyword "type" as a child of "%s %s".`, metadata.c:111); compile: type compiled | **ported** (C4b) — the internal modules `yang`, `default` and ietf-netconf-with-defaults use it, and `data/` needs annotations |
| ietf-netconf-acm `default-deny-write` / `default-deny-all` (2012-02-22, 2018-02-14) | nacm.c:82-126 parse: placement (warnings), multiple instances (error); compile: inherited flags | **ported** (C4b): small, and NACM models are common |
| ietf-restconf `yang-data` | yangdata.c parse/compile: top-level only, one container, schema compiled into the extension | `ErrUnsupported` (**U-0023**) until its consumer (PLAN §1 lists yang-data for v1, later milestone) |
| ietf-yang-structure-ext `structure`, `augment-structure` | structure.c parse/compile, own data trees | `ErrUnsupported` (**U-0023**), same reason |
| ietf-yang-schema-mount `mount-point` | schema_mount.c | `ErrUnsupported` (**U-0024**): out of v1 (PLAN §1) |
| openconfig-extensions `regexp-posix`, `posix-pattern` | openconfig.c; changes pattern semantics (SCN:1106) | `ErrUnsupported` (**U-0025**) |

Definitions alone (the internal modules define `mount-point`, `structure`, `annotation`) are fine;
only instances trigger the rule. Fixtures ext/annotation-no-type (implemented),
ext/annotation-no-type-import-only (the error still fails the load: parse phase),
ext/annotation-not-top-level, ext/annotation-twice, ext/nacm-placement-warning,
ext/nacm-twice; engine-only (no golden comparison of the Go error): ext/yang-data-unsupported.

## 3. Error reporting

`compile.Diagnostic{Level, Err (LY_ERR name), Code ly.Code, SchemaPath, Msg}`; `Load` returns
`(*Module, []Diagnostic, error)` (PLAN §2: warnings with nil error). Paths:
- during P3/P4: a path builder porting `lysc_update_path` (SC:52) — `/mod:a/b`, module prefix only on
  module change, special segments `{uses='g'}`, `{augment='<nodeid>'}`, `{refine='x'}`,
  `{grouping='g'}`, `{identity='x'}`, `{submodule='s'}` (P0, SC:2004), `{extension='x'}`,
  `{ext-inst}`, input/output; unused-grouping
  paths from `lys_compile_grouping_pathlog` (SCN:3965); top-level augments restart the path at `/`
  with `cur_mod` = augmenting module (SCA:2184-2189); truncated at `LYSC_CTX_BUFSIZE` like libyang;
- in P6: `lysc_path(node, LYSC_PATH_LOG)` (log.c:687) — choice/case and input/output included.
VERIFY(errpath/*): one negative fixture per special segment, plus one unres error, since log.c:694
appends any pending location string to the node path.
**Multi-error.** First error stops (`LY_CHECK_GOTO`) except the "all logged" loops (P5, SCN:3914-3937),
the trailing LYVE_OTHER refine/deviation note, and `Parsing module "%s" failed.` LY_EOTHER added when
the last error has no schema path or has a line (TS:2799). Warnings are diagnostics too: the oracle
stores everything logged against the context (`LY_LOSTORE`, lyoracle.c:1635) and the comparator
checks level/code/path, so every context `LOGWRN` site is ported; `LOGVRB` and `LOGWRN(NULL, …)`
(TS:2990, TS:3094: directory errors) are not stored. Plan of all non-XPath sites (grep of SC, SCN,
SCA, SF, TS, TSC, path.c, context.c): `Locally scoped grouping` (SCN:4066, C4b), `When|Must condition
may be referencing` (SC:629, SC:721, C7), `check skipped because referenced module` (SC:1191,
SC:1212, C7), `Refining config inside %s has no effect` (SCA:961, C6), `Single revision of the
module imported twice` (TS:1436, C1a), `File name does not match module name|revision` (TS:1996,
TS:2001, C1a); revision-order, duplicate-revision, control characters, 1.1 submodule includes
(TSC:94-122, parser_yang.c:4713) are parser warnings, reported through `parser.Context.Warn` (the former U-0006)
in `parser`. The 81 XPath sites are §2.13 (C2a/C2b). **Revert:** any error reverts the
whole `Load` (§1.6); there is no partially compiled module.

## 4. Output

**`internal/schema` changes** (one small PR before compile code, all additive):
1. `Node.Actions, Node.Notifs []*Node` (data children stay in `Children`, so `Child`/xpath never
   see operations); `Module.Top` order = data, rpcs, notifications.
2. `Node.DefaultCase *Node` (choice), `Node.Units string`, `Node.Uniques [][]*Node`,
   `Node.Exts []*ExtInstance`, `Node.Keyless` derived (`List && Keys == nil`, no field).
3. `Module.Version uint8`, `Module.Exts`, `Module.Submodules []Submodule{Name, Revision}`
   (`lys_compile_submodules`, TS:2611; yang-library later).
4. `Type.Exts`; documented invariants: `Typedef` = libyang `lysc_type.name`, `From` = libyang
   `base`, `Realtype` per 2.15, `Union` flat, `Prefixes[""]` = instantiating module (leafref) vs
   `Must/When.Ctx[""]` = defining module.
5. `ExtInstance` struct (2.17). No `Identity.Bases` (the oracle derives bases by scan; YAGNI).
6. `Set.All(name)` iterator over every revision (import-only included) for the loader.

**Public read-only handles (package `yang`)** — the handle types and their methods are defined in
`internal/snap` (imports `internal/schema` and the leaf packages `internal/types`, `internal/lyxp`, for
canonical defaults and leafref targets) and `yang` re-exports them by alias, so `data` can reach the
inner set through `snap.Set` without exporting it (design 07 §0.4). Reason: the API PLAN §1 promises;
each wraps an internal pointer and returns values/iterators, never slices, maps or writable structs.
Handles are fresh wrappers per call: compare what they return (names, paths), not the handles (`==`).
`Context`: `NewContext`, `Load`, `Schema() *Schema`. `Schema`: `Modules() iter.Seq[*Module]`,
`Module(name, rev)`, `Implemented(name)`, `FindSchema(path) (*SchemaNode, error)`. `Module`: `Name,
Revision, Namespace, Prefix, Implemented, FeatureEnabled(name), Features() iter.Seq2[string,bool],
Identities(), Top()`. `SchemaNode`: `Kind, Name, Module, Parent, Children(), Actions(),
Notifications(), Child(mod, name), Path()` (LYSC_PATH_LOG), `Config, Mandatory, Presence,
UserOrdered, Keys(), MinElements, MaxElements, Defaults() iter.Seq[string]` (canonical, the text when
it cannot be stored without data: lyd_value_validate_dflt), `DefaultCase, DefaultCaseName` (D-0070), `Type, Musts(), Whens(),
Status, Units, Extensions(), HasExtensionList, LeafrefTargets()`. `Type`: `Base, Typedef, Members(),
LeafrefPath, RequireInstance, FractionDigits, Enums(), Bits(), Bases(), Patterns(), Range(),
Length()`. `Identity`: `Name, Module, Derived()`. `Must`: `Expr, ErrorAppTag, ErrorMessage`. `When`:
`Expr, ContextNode, Module`. `Extension`: `Module, Name, Argument`. **Immutability:** the loader's
`schema.Module`s are working state that later loads write (§1.7), so after every `Load` (and
`NewContext`) C8 publishes a **deep copy** of every module, sharing nothing with
`c.Modules[*].Schema`: modules, nodes, identities, features, musts, whens, extension instances and
**types** (one copy per type object, sharing between types kept). Types are copied too because a
shared type would keep pointing at context-side identities, whose `Derived` lists a later `Load`
appends to. Only compiled XPath expressions (whose prefix bindings name context-side modules, read
for their immutable `Name` only) and patterns are shared. That copy is never written again. `Load`
publishes it through `atomic.Pointer` under a mutex (one writer); handles keep their snapshot alive
(old handles stay valid and consistent, they just do not see later loads — documented). An external
test proves no handle exposes a field or returns a slice or map; `-race` tests with concurrent
readers during `Load`.

## 5. Budgets / DoS (`compile.Budget`, zero = default; exceeding → error wrapping `ErrBudget`)

libyang has none of these limits, so each becomes a `U-00xx` entry in deviations.md
("Unsupported") in the PR that implements it — not before (same for U-0020/U-0021).
The compile limits are one struct, `compile.Options.Budget` (`MaxTypes`, `MaxUnionMembers`;
later `MaxNodes`, `MaxDepth`), next to the loader's `Options.MaxSearchDirs` and
`Options.Parse`; every limit wraps the package's single `ErrBudget`.
- **Grouping expansion:** `g(n)` using `g(n-1)` twice gives 2^n nodes from linear text. Cap total
  compiled nodes per `Load` (`MaxNodes`, default 1<<20) and check `context.Context` every 1k nodes.
- **Recursion:** compile recursion = nesting through uses/augment chains, not bounded by the parser's
  500-block depth; cap `MaxDepth` (default 10 000 levels) — the Go stack itself is not a budget.
- **Types:** `MaxTypes` (default 1<<20, the order of `MaxNodes`: new types scale with compiled
  leaves) caps compiled type objects plus union member slots per `Load` — union flattening is 2^n
  from linear text (§2.4), and a union typedef held by the cache only is recompiled at every use.
  `MaxUnionMembers` (default 1<<16) caps the flattened width of one union, checked before its array
  grows. Test: the `u0…u30` chain fails with `ErrBudget` after at most `MaxTypes` units of work
  (< 1 s at `MaxTypes` 1<<16; ~0.3 s at the default, several seconds under `-race`).
- **IffExpr:** explicit-stack evaluation; `Err` from the parser already bounds pathological input.
- **Cycles** (all detected without recursion): imports/includes (`parsing` flag), groupings (uses
  stack), typedefs (chain sets), identities and features (BFS as SC:246/SF:655), leafref chains,
  when dependencies (worklist). Bits: positions are not bounded; a value keeps its set bits
  sparse (no bitmap up to the highest position), so position 4294967295 costs nothing.
- XPath: `xpath.MaxTokens` per expression; atomize gets the same step budget as `Eval`.

## 6. Conformance

Every fixture id named in this document, by phase (all `op: schema` unless noted; "+" = positive,
otherwise negative; most are new and owned by stream F):

| Phase | Existing | To add |
|---|---|---|
| loading (§1.2-1.4) | m1/* (internal modules), basic/schema-dump | load/import-cycle, load/include-cycle, load/wrong-revision-file, load/imported-rev-binding +, load/tie (VERIFY), load/filename-warning, load/dup-typedef-scopes, load/symlink-dir +, load/symlink-file +, load/yin-unsupported (engine-only); Go test: symlink-cycle budget |
| implement, phases, dep sets (§1.5-1.7) | m1/schema-tree (augment targets implemented) | load/two-failures, load/warning-once, load/lref-implements-augmenter +, load/lref-implements-target +, load/when-check-skipped-warning, load/feature-not-satisfied (phase compile), load/feature-not-found (phase parse), load/deviation-import-only +, load/feature-rollback-3load (VERIFY) |
| features, if-feature (§2.2) | basic/schema-dump, m1/schema-tree{,-no-features} | iff/feature-cycle, iff/err-after-false (VERIFY), iff/unknown-feature, iff/grouping-prefix + |
| identities (P0) | m1 identities | ident/cycle, ident/unknown-base, errpath/submodule-identity |
| typedefs (§2.3) | m1 (`counter64`, `date-and-time`, `ipv4-address-no-zone`), types/* | types/unchanged-typedef-reuse +, types/typedef-cycle, types/restriction-wrong-type, lref/typedef-prefix-direct +, lref/union-typedef-direct +, lref/union-typedef-via-derived-typedef, lref/typedef-across-loads |
| unions (§2.4) | m1 `weight`, types/union-* | types/union-nested-index (VERIFY), budget test (Go unit test, not a fixture) |
| groupings, uses, refine (§2.5-2.6, P4) | m1 `limits` | grp/unused-top-level-invalid (control), grp/self-ref, grp/unused-local-warning, grp/nested-unused-not-validated +, grp/used-elsewhere-first +, refine/nested-same-target (innermost wins), refine/same-text, refine/target-missing, refine/config-in-rpc-warning |
| augments (§2.7) | m1 (two augmenters), protocol-v2/schema-tree | order/two-augmenters + order/two-augmenters-sorted-load (control), aug/mandatory-without-when, aug/mandatory-own-module + (SCA:1994), aug/target-missing, aug/of-augment + |
| choice/case, config, mandatory, status (§2.1, 2.8-2.11) | m1, protocol-v2 | choice/default-case-mandatory, config/under-state, mand/disabled-child-propagates +, obsolete/removed +, obsolete/key (status check, SCN:3310), obsolete/list-and-key +, list/key-iffeature-disabled (`Key "%s" is disabled.`), list/key-iffeature-enabled +, status/same-module-error, status/cross-module-ok +, status/submodule-uses-ok +, status/when-warning |
| defaults (§2.12) | m1, protocol-v2/schema-limits | dflt/invalid, dflt/union-leafref + , dflt/llist-dup-noncanon (VERIFY) |
| must/when (§2.13-2.14) | m1 `boost`/`mtu-limit`, protocol-v2 | when/cycle (design 03), when/own-children, when/invalid-condition, when/func-arg-warning, must/unknown-node-warning, must/value-not-fit-warning |
| leafref (§2.15) | m1 `peer`, ietf `interface-ref` | lref/path-syntax-parse-phase (U-0005), lref/non-leaf-target, lref/config-to-state, lref/circular, lref/disabled-target |
| extension plugins (§2.17) | — | ext/annotation-no-type, ext/annotation-no-type-import-only, ext/annotation-not-top-level, ext/annotation-twice, ext/nacm-placement-warning, ext/nacm-twice, ext/yang-data-unsupported (engine-only) |
| data `when` order (design 03 rule 5, op `sequence`) | — | when/order-a-after-b, when/order-b-after-a |
| errors (§3) | — | errpath/uses, errpath/augment, errpath/refine, errpath/grouping, errpath/ext-inst, errpath/unres-node |

**YANG 1.0 vs 1.1 gates (#42).** One `ver/<gate>-10` fixture (the module is `yang-version 1`) and
one `ver/<gate>-11` fixture (`yang-version 1.1`) per gate, all in `compile/` (modules
`schemas/ver-*.yang`). The `x-*` gates put the construct in the YANG 1.1 module `ver-def-11` and
use it from the fixture's module, so they show whose version libyang checks. All agree.

| gate (libyang v5.8.6) | fixtures | 1.0 | 1.1 |
|---|---|---|---|
| several `base` in identity (parser_yang.c:4383) | ver/ident-bases-{10,11} | invalid (parse) | valid |
| several `base` in identityref (schema_compile.c:298) | ver/idref-bases-{10,11} | invalid | valid |
| if-feature expression (schema_features.c:417) | ver/iffeat-expr-{10,11} | invalid | valid |
| `if-feature` in enum / bit (parser_yang.c:2000) | ver/enum-iffeat-{10,11}, ver/bit-iffeat-{10,11} | invalid (parse) | valid |
| restricted enumeration / bits (schema_compile_node.c:1249) | ver/enum-subtype-{10,11}, ver/bits-subtype-{10,11} | invalid | valid |
| leafref `require-instance`, inline / in a typedef (schema_compile_node.c:1811) | ver/reqinst-{10,11}, ver/reqinst-tpdf-{10,11} | invalid | valid |
| leaf-list of `empty` (schema_compile_node.c:2824) | ver/llist-empty-{10,11} | invalid | valid |
| leaf-list `default` (parser_yang.c:2710; schema_compile_node.c:2916) | ver/llist-dflt-{10,11} | invalid (parse) | valid |
| list key of `empty` (schema_compile_node.c:3293) | ver/key-empty-{10,11} | invalid | valid |
| submodule includes a submodule the main module does not (tree_schema_common.c:1066) | ver/subinc-{10,11} | valid | invalid (parse) |
| include in a submodule (parser_yang.c:4712) | ver/subwarn-{10,11} | valid | valid + warning |
| if-feature expression in a 1.1 grouping | ver/x-iffeat-grp-{10,11} | valid | valid |
| leaf-list of `empty` in a 1.1 grouping | ver/x-llist-empty-grp-{10,11} | valid | valid |
| leaf-list `default` in a 1.1 grouping | ver/x-llist-dflt-grp-{10,11} | valid | valid |
| 1.1 typedef restricting an enumeration, used by the module | ver/x-enum-subtype-{10,11} | invalid (the using module's version is checked) | valid |
| the module restricts a 1.1 enumeration typedef | ver/x-enum-restrict-{10,11} | invalid | valid |
| refine `default` of a 1.1 grouping's leaf-list (schema_compile_amend.c:921) | ver/x-refine-dflt-{10,11} | invalid | valid |

The internal ietf-netconf statements that tree_schema.c:2310 adds (enum `if-feature` only for a
1.1 ietf-netconf) are covered by public-ietf/ietf-netconf-{all,no}-features (#39; a 1.0 module).
Deviation-reached version checks are out of scope (deviation fixtures, track A).

libyang utests: `test_tree_schema_compile.c` (151 tier-B cases) maps by function: `test_module`,
`test_submodule`, `test_name_collisions` → §1; `test_type_*`, `test_type_union`, `test_type_dflt`
→ 2.3–2.4, 2.12; `test_identity` → P0; `test_status` → 2.11; `test_node_*`, `test_action`,
`test_notification` → 2.1, 2.8–2.10; `test_grouping`, `test_uses`, `test_refine`, `test_augment` →
2.5–2.7; `test_when`, `test_must` → 2.13–2.14; `test_unique_disabled` → step j; `test_deviation` M2.
`test_schema.c`: `test_identity`, `test_feature`, `test_includes`, `test_key_order`,
`test_disabled_enum`, `test_lysc_path`, `test_obsolete`, `test_collision_*`. M1 target: every case of
these functions whose schema needs no deviation, as `schema` fixtures with `assert` (needs the
multi-module `schema` request the extractor backlog lists, inventory §5.4).

**Engine adapter for op `schema`** (conformance module): map `context_options` → `Options`,
`searchdirs` → `os.DirFS`, `modules[]` → `Load` in order (keep going after a rejected module, like
`build_ctx`); per module `name, accepted, phase, rc, revision, diagnostics` and, for accepted
implemented modules, `schema_tree` in `lysc_module_dfs_full` order (TS:62-121: node, its actions and
notifications subtrees, then its children; module data → rpcs → notifs — observed in
protocol-v2/schema-tree, `/pv2:c/l/reset` before `/pv2:c/l/k`), `identities` (`bases` by scan),
`features` with the oracle's field rules (design 04 §3, lyoracle.c:729-907).
The `compiled` field (YANG printer text, out of v1) is **not** dropped globally: the comparator keeps
comparing it for any engine that produces it. Instead an engine may implement an optional
`conformance.FieldSkipper{ SkippedFields(op string) []string }`; the comparator removes only those
fields from both sides for that engine, and the report shows such fixtures as "agree (skipped:
compiled)" in their own column, so a skipped field is visible, never silent.

## 7. Task split (≤ ~1.5k Go lines incl. tests each; own branch + PR + astra review)

Re-estimated against the libyang LOC each PR ports (≈ 0.7–0.8 Go per C line + tests ≈ 1:1);
total ≈ 15k incl. tests (sum of the table: 14.95k).

| # | PR | ~LOC | Depends | Worker |
|---|---|---|---|---|
| C0 | `internal/schema` additions (§4) + invariant comments | 250 | — | Sonnet |
| C1a | loader I: `yang.Context` skeleton, embedded internal modules, symlink-aware fs.FS search (+ `.yin` → U-0021, `MaxSearchDirs`), revision selection + `IMPORTED_REV`, imports/includes + cycles + submodule injection, filename warnings, dup checks | 1.4k | parser #13/#14 | Opus |
| C1b | loader II: ext-instance records, P0 (feature if-features + cycles, identities + derived), `lys_implement` (features, augment-target implementation, deviation → U-0020), phase tagging, dep sets + `lys_compile_depset_r` restart, snapshot/revert | 1.4k | C0, C1a | Opus |
| C2a | xpath `Atomize` core (+ `SchemaNode` extension, walkAtoms/atomsOK rebased on it), including the node-not-found warning and the skip of the rest of the path | 1.3k | xpath-eval | Opus |
| C2b | xpath compile warnings, in five PRs: operands + type predicates + subexpression trailer (1/5), `warn_equality_value` + `SchemaNode.CheckValue` (2/5), 42 string-argument sites (3/5), 28 node-set/numeric/YANG function sites (4/5), the end-to-end replay of the warning fixtures (5/5) | 0.9k | C2a | Sonnet (mechanical, port map per site) |
| C3 | `internal/lyxp` leaf package: tokenizer + `ly_path_parse` (all modes) + `ly_path_check_predicate`; hook in `parser.Build`; types/xpath switched to it | 1.1k | parser-build #14 | Sonnet |
| C3b | `ly_path_compile_leafref` in `types/path.go` | 0.6k | C3 | Sonnet |
| C4a | compile core I: node walk P3, `lys_compile_node_` order, connect order, config/ordered-by, container/leaf/leaf-list/list (keys, uniques)/choice/case/any/action/notif, mandatory propagation, error-path builder | 1.5k | C0, C1b | Opus |
| C4b | compile core II: if-feature eval + disabled/obsolete sets, status (§2.11), ext instances + metadata/NACM plugins + `ErrUnsupported` for the others, P4 grouping validation scope, P5 | 1.4k | C4a | Opus |
| C5 | types compile: typedef chain + reuse rule + leafref typedef quirks, range/length/pattern/enum/bits/dec64/identityref, union flattening, `MaxTypes` | 1.4k | C0 only (driven by a test harness; joins C4a's walk at merge) | Opus |
| C6 | groupings/uses/refine/uses-augment/top-level augment | 1.4k | C4b, C5 | Opus |
| C7 | unres P6: leafref targets/realtype, when/must checks + cycles, bit/enum removal, defaults via types, disabled removal | 1.3k | C2a, C3b, C6 | Opus |
| C8 | public handles + immutability/race tests + engine adapter op `schema` + `FieldSkipper` | 1.0k | C7 | Sonnet |
| F | fixtures of §6 (from libyang tests + hand-written), independent of the code | — | — | codex sol / Sonnet |

Waves: **1** {C0, C1a, C2a, C3, C5, F} → **2** {C1b, C2b, C3b} → **3** C4a → C4b → C6 → C7 → C8.
Exit: all `schema` fixtures agree (warnings included); m1 `schema-tree` and
`schema-tree-no-features` identical after normalization.

## Riskiest points (review focus)
1. Target-driven compile (§0) — a merge-based design silently breaks node order and error paths.
2. Dep sets + `LY_ERECOMPILE` (§1.7): compile order is observable (warnings, typedef binding).
3. Schema-mode XPath (§2.13): a second evaluator with 81 warning sites that the comparator checks.
4. Typedef cache holder counts (§2.3): leafref binding/sharing only with a second holder.
5. Phase boundary (§1.6): leafref path syntax and `lys_implement` are `parse`; check_features is `compile`.
6. Typedef reuse rule (SCN:2084-2088) vs internal/types plugin lookup through `From`.
7. Unres LIFO/FIFO order decides the first reported error.
8. Disabled/obsolete nodes: compiled, removed, but their mandatory flag stays on ancestors.
9. Grouping validation scope and the sticky `LYS_USED_GRP`.
10. Error paths (special segments, log.c concatenation) and non-atomic rollback of feature changes (§1.7).
11. Built-in extension plugins (§2.17): checks run at parse time for import-only modules too.
