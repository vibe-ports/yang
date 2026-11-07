# SPDX-License-Identifier: BSD-3-Clause
"""Apply narrowly scoped observational hooks to an isolated pinned source copy.

Every replacement is required to match once; no algorithm is copied or replaced.
"""
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])


def replace(path, old, new, count=1):
    assert path.read_text().count(old) == count, (path, old)
    path.write_text(path.read_text().replace(old, new))


def wrap(file, name, node="NULL", table="NULL", args="", void=False, source="NULL"):
    path = root / "src" / file
    text = path.read_text()
    pattern = r"(?m)^(?P<prefix>(?:static |LIBYANG_API_DEF )?(?:LY_ERR|void))\n" + name + r"(?P<params>\([^;]*?\))\n\{"
    match = re.search(pattern, text)
    assert match, name
    start = match.end() - 1
    # Skip comments and literals when finding the closing function brace.
    token = re.compile(r'/\*.*?\*/|//[^\n]*|"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'|[{}]', re.S)
    depth = 0
    end = None
    for t in token.finditer(text, start):
        if t.group() == "{":
            depth += 1
        elif t.group() == "}":
            depth -= 1
            if depth == 0:
                end = t.end()
                break
    assert end
    signature = match.group("prefix") + "\n" + name + match.group("params")
    impl = text[match.start():end].replace(name + "(", name + "_nr_impl(", 1)
    call = name + "_nr_impl(" + args + ")"
    body = (call + ";\n" if void else "LY_ERR nr_rc = " + call + ";\n")
    body += '    nr_event("' + name + '.after", ' + node + ", " + table + ", " + ("0" if void else "nr_rc") + ", " + source + ");\n"
    if not void:
        body += "    return nr_rc;\n"
    wrapper = signature + '\n{\n    nr_event("' + name + '.before", ' + node + ", " + table + ", 0, " + source + ");\n    " + body + "}\n"
    text = text[:match.start()] + impl + "\n\n" + wrapper + text[end:]
    if '#include "native_observer.h"' not in text:
        text = '#include "native_observer.h"\n' + text
    path.write_text(text)


wrap("tree_data_hash.c", "lyd_insert_hash", "node", args="node")
wrap("tree_data_hash.c", "lyd_unlink_hash", "node", args="node", void=True)
wrap("tree_data_hash.c", "lyd_insert_hash_add", "node", "ht", "ht, node, empty_ht")
wrap("hash_table.c", "lyht_resize", table="ht", args="ht, operation, check")
wrap("hash_table.c", "_lyht_insert_with_resize_cb", table="ht", args="ht, val_p, hash, resize_val_equal, match_p, check")
wrap("hash_table.c", "lyht_remove_with_resize_cb", table="ht", args="ht, val_p, hash, resize_val_equal")
wrap("hash_table.c", "lyht_find_next_with_collision_cb", table="ht", args="ht, val_p, hash, val_equal, match_p")
wrap("tree_data.c", "lyd_insert_after_node", "node", args="first_sibling_p, sibling, node", void=True, source="sibling")
wrap("tree_data.c", "lyd_insert_before_node", "node", args="sibling, node", void=True, source="sibling")
wrap("tree_data.c", "lyd_unlink_ignore_lyds", "node", args="first_sibling_p, node", void=True)
wrap("tree_data.c", "lyd_insert_only_child", "node", args="parent, node", void=True, source="parent")
wrap("tree_data.c", "lyd_insert_node_last", "node", args="parent, first_sibling, node", void=True, source="parent")
wrap("tree_data_sorted.c", "lyds_move_meta", "dst", args="dst, meta", void=True)
wrap("tree_data_sorted.c", "lyds_merge_nodes2_front", "*leader_dst", args="leader_dst, first_src, leader_src, rbt_src, dst_iter, next_p")
wrap("tree_data_sorted.c", "lyds_merge_nodes2_among", "*leader_dst", args="first_dst, leader_dst, first_src, rbt_src, dst_iter, next_p")
wrap("tree_data_sorted.c", "lyds_merge_nodes2_back", args="first_dst, first_src, dst_iter, next_p", void=True)
wrap("validation.c", "lyd_validate_duplicates", "node", args="first, node, val_opts")
path = root / "src/hash_table.c"
replace(path, "    return ht;\n}\n\nLIBYANG_API_DEF lyht_value_equal_cb", '    nr_event("table.allocate", NULL, ht, 0, NULL);\n    return ht;\n}\n\nLIBYANG_API_DEF lyht_value_equal_cb')
replace(path, "    ++ht->used;", "    nr_occurrence(ht, rec_idx, val_p, hash, 1);\n    ++ht->used;")
replace(path, "    rec->next = ht->first_free_rec;", "    nr_occurrence(ht, rec_idx, NULL, hash, 0);\n    rec->next = ht->first_free_rec;")
replace(path, "lyht_free(struct ly_ht *ht, void (*val_free)(void *val_p))\n{", 'lyht_free(struct ly_ht *ht, void (*val_free)(void *val_p))\n{\n    if (ht) nr_event("table.retire", NULL, ht, 0, NULL);')
path = root / "src/tree_data_free.c"
replace(path, "    assert(node);\n\n    if (!node->schema)", '    assert(node);\n    nr_event("node.retiring", node, NULL, 0, NULL);\n\n    if (!node->schema)')
replace(path, "    free(node);\n}", '    nr_event("node.free", node, NULL, 0, NULL);\n    free(node);\n}')
path.write_text('#include "native_observer.h"\n' + path.read_text())
path = root / "src/tree_data.c"
replace(path, "    if (sibling->next) {\n        /* sibling had a succeeding node */", "    nr_remote(first_sibling_p ? *first_sibling_p : NULL, sibling, node);\n    if (sibling->next) {\n        /* sibling had a succeeding node */")
replace(path, "sizeof(struct lyd_node *), lyd_hash_table_val_equal, NULL, 1);\n            }\n\n            /* duplicate all the children */", 'sizeof(struct lyd_node *), lyd_hash_table_val_equal, NULL, 1);\n            }\n            nr_event("dup.table.initialized", dup, ((struct lyd_node_inner *)dup)->children_ht, 0, node);\n\n            /* duplicate all the children */')
replace(path, "    if (options & LYD_DUP_WITH_FLAGS) {", '    nr_event("node.dup.allocate", dup, NULL, 0, node);\n    if (options & LYD_DUP_WITH_FLAGS) {')
replace(path, "            free(dup);", '            nr_event("node.free", dup, NULL, rc, NULL);\n            free(dup);')
replace(path, "            rc = ly_dup_prefix_data(trg_ctx, opaq->format, orig->val_prefix_data, &opaq->val_prefix_data);", '            nr_event("prefix.copy.before", dup, NULL, 0, node);\n            rc = ly_dup_prefix_data(trg_ctx, opaq->format, orig->val_prefix_data, &opaq->val_prefix_data);\n            nr_event("prefix.copy.after", dup, NULL, rc, node);')
replace(path, "                ly_free_prefix_data(opaq_trg->format, opaq_trg->val_prefix_data);", '                nr_event("prefix.replace.before", match_trg, NULL, 0, sibling_src);\n                ly_free_prefix_data(opaq_trg->format, opaq_trg->val_prefix_data);')
replace(path, "                        &opaq_trg->val_prefix_data);", '                        &opaq_trg->val_prefix_data);\n                nr_event("prefix.replace.after", match_trg, NULL, 0, sibling_src);')
path = root / "src/tree_data_new.c"
replace(path, "    term->schema = schema;\n    term->prev = &term->node;\n    term->flags = LYD_NEW;", '    nr_event("node.allocate", &term->node, NULL, 0, NULL);\n    term->schema = schema;\n    term->prev = &term->node;\n    term->flags = LYD_NEW;', count=2)
replace(path, "    LY_CHECK_ERR_RET(ret, free(term), ret);", '    LY_CHECK_ERR_RET(ret, nr_event("node.free", &term->node, NULL, ret, NULL); free(term), ret);')
replace(path, "    in->schema = schema;", '    nr_event("node.allocate", &in->node, NULL, 0, NULL);\n    in->schema = schema;')
replace(path, "    any->schema = schema;", '    nr_event("node.allocate", &any->node, NULL, 0, NULL);\n    any->schema = schema;')
replace(path, "    opaq->prev = &opaq->node;", '    nr_event("node.allocate", &opaq->node, NULL, 0, NULL);\n    opaq->prev = &opaq->node;')
replace(path, "        LY_CHECK_ERR_RET(r, free(term), r);", '        LY_CHECK_ERR_RET(r, nr_event("node.free", &term->node, NULL, r, NULL); free(term), r);')
path.write_text('#include "native_observer.h"\n' + path.read_text())
path = root / "src/tree_data_sorted.c"
replace(path, "    begin = rb_next(dst_iter);\n    LY_CHECK_RET(!begin,; );", '    begin = rb_next(dst_iter);\n    if (!begin) nr_event("merge2.back.no-successor", RBN_DNODE(dst_iter), NULL, 0, NULL);\n    LY_CHECK_RET(!begin,; );')
replace(path, "        ret = rb_insert(node->next, rbt_src, &rbn);", '        if (node->next->schema != schema) nr_event("merge2.among.successor-schema", node->next, NULL, 0, node);\n        ret = rb_insert(node->next, rbt_src, &rbn);')

# Observe the evolving local RB root separately from the metadata pointer.
replace(path, "    ret = rb_insert(*leader_dst, rbt_src, dst_iter);", "    ret = rb_insert(*leader_dst, rbt_src, dst_iter);\n    nr_working_rb(*rbt_src);")
replace(path, "        ret = rb_insert(node->next, rbt_src, &rbn);", "        ret = rb_insert(node->next, rbt_src, &rbn);\n        nr_working_rb(*rbt_src);")
replace(path, "        for (iter = rb_next(rbn_prev); iter != rbn; iter = rb_next(iter)) {", "        for (iter = rb_next(rbn_prev); iter != rbn; iter = rb_next(iter)) {\n            if (!iter) nr_event(\"merge2.among.exhausted-forward\", node, NULL, 0, RBN_DNODE(rbn));")
replace(path, "cleanup:\n    RBT_SET(root_meta_src, rbt_src);", "cleanup:\n    RBT_SET(root_meta_src, rbt_src);\n    nr_working_rb(NULL);")
