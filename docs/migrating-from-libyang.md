# Migrating from libyang (cgo) to this port

For Go services that call libyang v5.8.6 through cgo and want to drop the C dependency. The port
follows libyang's behaviour call for call, so most code translates one call at a time: the table
below maps the commonly used C calls to the public Go API on `main`. Packages: `yang`
(`github.com/vibe-ports/yang`, contexts and schema) and `data` (`github.com/vibe-ports/yang/data`,
data trees). The API is v0 and may change in any release; pin a version.

What the port does not cover yet is in the README [status table](../README.md#status) and in the
*Unsupported* section of [conformance/deviations.md](../conformance/deviations.md). The full
function-level list is [port-map.md](port-map.md).

## Call mapping

"Not public yet" means the port has the function internally or plans it, but exports no API for
it; the issue tracks the public API.

### Context and schema

| libyang | Go |
|---|---|
| `ly_ctx_new(searchdir, options, &ctx)` | `yang.NewContext(yang.Options{...}, dirs ...fs.FS)`: search directories are `fs.FS` values (`os.DirFS`, `embed.FS`, `fstest.MapFS`) |
| `LY_CTX_*` options | `yang.Options` fields: `AllImplemented`, `NoYangLibrary`, `DisableSearchdirs`, `PreferSearchdirs`, `EnableImportFeatures`, `CompileObsolete`, `RefImplemented`, `LeafrefExtended`, `BuiltinPluginsOnly` |
| `ly_ctx_set_module_imp_clb` | `yang.Options.Loader` |
| `lys_parse_mem` / `lys_parse_path` | put the text in an `fs.FS` (`fstest.MapFS` for in-memory text) and `Context.Load` it |
| `ly_ctx_load_module(ctx, name, revision, features)` | `Context.Load(name, revision, features)` |
| `ly_ctx_destroy` | nothing: garbage collected |
| (the compiled context passed to data calls) | `Context.Schema()`: an immutable `*yang.Schema` snapshot, safe to share between goroutines |
| `ly_ctx_get_module` / `ly_ctx_get_module_implemented` | `Schema.Module(name, revision)` / `Schema.Implemented(name)` |
| `lys_find_path` | `Schema.FindSchema(path)` (data paths, module names as prefixes) |
| `lysc_node` traversal (`lysc_node_child`, `->next`, `->parent`) | `Module.Top()`, `SchemaNode.Children()`, `SchemaNode.Child(module, name)`, `SchemaNode.Parent()` |
| `lysc_is_key`, `LYS_KEY` | `SchemaNode.IsKey()` |
| `LYS_SET_DFLT` | `SchemaNode.DefaultSet()` |
| `lysc_is_dup_inst_list` | `SchemaNode.IsDupInstList()` |
| `lysc_is_userordered`, `LYS_ORDBY_USER` | `SchemaNode.UserOrdered()` |
| `LYS_CONFIG_W` / `LYS_MAND_TRUE` / `LYS_PRESENCE` | `SchemaNode.Config()`, `Mandatory()`, `Presence()` |
| PCRE2-compatible `pattern` semantics | `yang.Options.PatternCompat` (opt-in; strict XSD patterns by default, D-0002…D-0008; compat-mode differences D-0031…D-0033, U-0011) |

### Data trees

| libyang | Go |
|---|---|
| `lyd_parse_data(ctx, NULL, in, format, parse_opts, val_opts, &tree)` | `data.Parse(ctx, r, data.FormatJSON` or `data.FormatXML, schema, data.ParseOptions{...})` |
| `LYD_PARSE_STRICT` / neither / `LYD_PARSE_OPAQ` | `ParseOptions.Unknown`: `data.Reject` (the zero value) / `data.Skip` / `data.Opaque` |
| `LYD_PARSE_ONLY` | `ParseOptions.ParseOnly` |
| `LYD_PARSE_NO_STATE` | `ParseOptions.NoState` (with `ValidateOptions.NoState` for the validation part) |
| `LYD_PARSE_STORE_ONLY` | `ParseOptions.StoreOnly` (implies `ParseOnly`, as in libyang) |
| `LYD_PARSE_JSON_NULL`, `LYD_PARSE_JSON_STRING_DATATYPES` | `ParseOptions.JSONNull`, `ParseOptions.JSONStringDatatypes` |
| `LYD_VALIDATE_MULTI_ERROR` | `ParseOptions.Validate.MultiError` (or `ValidateOptions.MultiError` for `Validate`) |
| `LYD_VALIDATE_NO_STATE`, `_PRESENT`, `_OPERATIONAL`, `_NO_DEFAULTS` | `ValidateOptions.NoState`, `Present`, `Operational`, `NoDefaults` |
| `lyd_validate_all(&tree, ctx, opts, NULL)` | `Tree.Validate(ctx, data.ValidateOptions{...})` |
| `lyd_validate_all(&tree, ctx, opts, &diff)` | `Tree.ValidateDiff(ctx, data.ValidateOptions{...})` |
| `lyd_print_mem` / `lyd_print_all` (`LYD_PRINT_WITHSIBLINGS`) | `Tree.PrintJSON(w, data.PrintOptions{...})`, `Tree.PrintXML(w, ...)`: print into a `bytes.Buffer` or `strings.Builder` |
| `lyd_print_tree` (one subtree) | `Node.PrintJSON`, `Node.PrintXML` |
| `LYD_PRINT_SHRINK`, `LYD_PRINT_EMPTY_LEAF_LIST` | `PrintOptions.Shrink`, `PrintOptions.EmptyLeafList` |
| `LYD_PRINT_WD_EXPLICIT`, `_TRIM`, `_ALL`, `_ALL_TAG`, `_IMPL_TAG` | `PrintOptions.WithDefaults`: `data.WDExplicit`, `WDTrim`, `WDAll`, `WDAllTagged`, `WDImplicitTagged` |
| `lyd_free_all` | nothing: garbage collected |
| an empty tree (`struct lyd_node *tree = NULL`) | `data.NewTree(schema)` (a zero `Tree` has no schema) |
| `lyd_new_path(NULL, ctx, path, value, opts, &node)` | `Tree.NewPath(path, value, data.NewPathOptions{...})`; `LYD_NEW_PATH_UPDATE` is `NewPathOptions.Update` |
| `lyd_new_list`, `lyd_new_list2`, `lyd_new_list3` | not public yet (#98, M4): create the instance with `Tree.NewPath("/mod:a/l[k='v']", "", ...)`, the keys come from the predicate |
| `lyd_new_term`, `lyd_new_inner` | not public yet (#98, M4): use `Tree.NewPath` |
| `lyd_dup_single`, `lyd_dup_siblings` | not public yet (#92, M4) |
| `lyd_merge_siblings` (no options) | `Tree.Merge(src)`; the merge options are not public yet (#127, M6) |
| `lyd_free_tree` | `Node.Remove()` |
| `lyd_find_path(tree, path, 0, &node)` | `Tree.Find(path)`: absolute JSON paths from the top level; `nil, nil` when nothing matches |
| `lyd_find_xpath`, `lyd_find_xpath2/3` | `Tree.FindXPath(expr, XPathOptions{Node, Vars})`: JSON-format expressions; returns the nodes, the call's diagnostics and an error |
| `lyd_eval_xpath4` (and `lyd_eval_xpath`/`2`/`3` for booleans) | `Tree.EvalXPath(expr, o)` returns the result in its own type; `Tree.EvalXPathAs(expr, typ, o)` converts it. `format`/`prefix_data`/`cur_mod` are not public yet (#171) |
| `lyd_path(node, LYD_PATH_STD, NULL, 0)` | `Node.Path()` |
| `lyd_get_value` | `Node.Value()` |
| `node->schema`, `node->parent`, `lyd_child` | `Node.Schema()`, `Node.Parent()`, `Node.Children()` |
| `lyd_child_no_keys` | `Node.ChildrenNoKeys()` (an iterator, not the first child) |
| `LYD_TREE_DFS_BEGIN` / `LYD_TREE_DFS_END` | `Node.All()`; the top level is `Tree.Top()` |
| `node->flags & LYD_DEFAULT` (`LYD_WHEN_TRUE`, `LYD_NEW`) | `Node.Flags()&data.FlagDefault` (`FlagWhenTrue`, `FlagNew`) |
| `lyd_compare_single(a, b, opts)` | `a.Equal(b, data.CompareOptions{...})`: `LYD_COMPARE_FULL_RECURSION`, `_DEFAULTS`, `_OPAQ` are `FullRecursion`, `Defaults`, `Opaque`; returns a bool instead of `LY_SUCCESS`/`LY_ENOT` |

### Metadata

| libyang | Go |
|---|---|
| `lyd_find_meta(node->meta, NULL, "mod:name")` | `Node.FindMeta("mod:name")` (`nil, nil` when there is none) |
| `lyd_new_meta(ctx, node, NULL, "mod:name", value, 0, &meta)` | `Tree.NewMeta(node, "mod:name", value)` |
| `lyd_free_meta_single(meta)` | `Meta.Remove()` |
| `lyd_free_meta_siblings(node->meta)` | no direct equivalent: call `Meta.Remove` on each instance `Node.Meta()` yields (removing while iterating is safe) |
| `node->meta` list, `meta->name`, `meta->annotation->module`, `lyd_get_meta_value` | `Node.Meta()`, `Meta.Name()`, `Meta.Module()`, `Meta.Value()` |

### Diff

| libyang | Go |
|---|---|
| `lyd_diff_siblings(first, second, opts, &diff)` | `data.Diff(first, second, data.DiffOptions{...})` for whole trees, `data.DiffSiblings(firstNode, secondNode, ...)` from given siblings; `nil` diff when equal |
| `lyd_diff_tree` | `data.DiffTree(firstNode, secondNode, ...)` |
| `LYD_DIFF_DEFAULTS`, `LYD_DIFF_META` | `DiffOptions.Defaults`, `DiffOptions.Meta` |
| `lyd_diff_apply_all` / `lyd_diff_apply_module` | `Tree.ApplyDiff(diff, data.ApplyDiffOptions{Module: ..., Callback: ...})` |
| `lyd_diff_merge_all` / `lyd_diff_merge_module` | `Tree.MergeDiff(src, data.MergeDiffOptions{...})` (limitations below) |
| `lyd_diff_merge_tree` | `Tree.MergeDiffTree(parent, src, ...)` |
| `lyd_diff_reverse_all` | `Tree.ReverseDiff()` (limitation below) |

### Errors

| libyang | Go |
|---|---|
| the `LY_ERR` a call returns | `error`; for data calls a `*data.ValidationError` whose `RC()` is the `LY_ERR` name (`"LY_EVALID"`, `"LY_EINVAL"`, …) |
| `ly_err_first` / `ly_err_last` / the `ly_err_item` list | the `[]yang.Diagnostic` a call returns, or `ValidationError.Diags`, in log order |
| `ly_err_item` fields `err`, `vecode`, `msg`, `data_path`, `schema_path`, `apptag`, `line`, `level` | `yang.Diagnostic` fields `Err`, `Code` (both as names), `Msg`, `DataPath`, `SchemaPath`, `AppTag`, `Line`, `Warning` |
| input libyang accepts but the port does not handle yet | `errors.Is(err, yang.ErrUnsupported)` |
| resource limits (no libyang equivalent) | `errors.Is(err, yang.ErrBudget)`; limits in `yang.ParseBudget` and `data.Budget` |

## Inherited libyang limitations

These are libyang v5.8.6 behaviours that the port copies on purpose. They are not port bugs. Both
were confirmed with the libyang oracle. The godoc of `Tree.ReverseDiff` and `Tree.MergeDiff` has
the details, and `ExampleTree_ReverseDiff` and `ExampleTree_MergeDiff` in
[data/example_test.go](../data/example_test.go) show them.

- **`ReverseDiff` of a deleted subtree with user-ordered entries.** A diff that deletes a subtree
  holding user-ordered list or leaf-list instances below its top node cannot be reversed. Diff
  records the anchor metadata only on the top deleted node, but `lyd_diff_reverse_siblings_r`
  reverses every user-ordered instance below it as a delete of its own and has to rename its
  anchor. The result is `LY_EINVAL` with
  `Failed to find metadata "orig-value" for node "<path>".` (`orig-key` for a list, `orig-position`
  for a key-less list or state leaf-list). Workaround: keep the old tree and build the reverse as
  `data.Diff(new, old, data.DiffOptions{Defaults: true})`. Without `Defaults`, a non-presence
  container that exists in `new` only as a default node is created again, and applying that diff
  adds a second instance of it, as libyang does.
- **`MergeDiff` / `MergeDiffTree` with user-ordered moves.** Merging diffs that create, delete or
  move user-ordered entries is lossy. A create followed by a delete of the same entry cancels out
  and leaves the merged diff, but a later entry can still name it as its `yang:value` /
  `yang:key` / `yang:position` anchor. `ApplyDiff` of the merged diff then fails with `LY_EINVAL`
  `Node "<name>" instance to insert next to not found.` (or `LY_EINVAL` from the sibling check
  of `lyd_insert_before`). When the first diff deletes an entry inside a deleted subtree and the
  second creates it again, the merge itself fails: `LY_EINVAL`
  `Failed to find metadata "yang:orig-value" for node "<path>".` (`yang:orig-key`,
  `yang:orig-position` likewise), then `Merging operation "create" failed.` Workaround: keep the original tree and diff it against the
  final one (`data.Diff(first, last, data.DiffOptions{Defaults: true})`) instead of merging the
  intermediate diffs.

## Deliberate differences

[conformance/deviations.md](../conformance/deviations.md) lists every difference from libyang.
A D-id is an intentional difference, where libyang contradicts the RFC, crashes or depends on the
host. A U-id is a feature this port does not support yet, and the port refuses that input,
usually with `ErrUnsupported`, instead of answering differently. Any other difference is a bug.

## Porting your own C helpers

The library contains only libyang v5.8.6 functions. Helpers that your service built on top of
libyang belong in your service: port them call for call against the Go API and keep them there.
For example, a helper that finds a node by path, compares it with the same node of an earlier
tree and, when it changed, sets a metadata annotation:

```go
// markIfChanged finds the node at path in cur and compares it with the node at the same path in
// prev; when they differ it sets the annotation ann ("module:name") on it to value.
func markIfChanged(prev, cur *data.Tree, path, ann, value string) (bool, error) {
	n, err := cur.Find(path) // lyd_find_path
	if err != nil || n == nil {
		return false, err
	}
	old, err := prev.Find(path)
	if err != nil {
		return false, err
	}
	if n.Equal(old, data.CompareOptions{FullRecursion: true}) { // lyd_compare_single
		return false, nil
	}
	if m, err := n.FindMeta(ann); err != nil { // lyd_find_meta
		return false, err
	} else if m != nil {
		m.Remove() // lyd_free_meta_single
	}
	if _, err := cur.NewMeta(n, ann, value); err != nil { // lyd_new_meta
		return false, err
	}
	return true, nil
}
```

The annotation has to be defined by a loaded module (`md:annotation`, RFC 7952). Where a C
helper uses a call that is not public yet (see the table), build it from the public calls, as
`NewPath` stands in for `lyd_new_list`, or wait for the linked issue; packages under `internal/`
cannot be imported from outside the module.
