/* SPDX-License-Identifier: BSD-3-Clause */
/* Exact RPC probes for operations the sequence protocol cannot parse as sources.
 * argv: schema, apply|merge|move, target RPC JSON, source/diff RPC JSON, optional reply JSON.
 * Built only by the oracle-tagged Go regression inside the dev container. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static LY_ERR
parse_rpc(struct ly_ctx *ctx, const char *json, struct lyd_node **tree, struct lyd_node **op)
{
    struct ly_in *in = NULL;
    LY_ERR rc = ly_in_new_memory(json, &in);
    if (!rc) {
        rc = lyd_parse_op(ctx, NULL, in, LYD_JSON, LYD_TYPE_RPC_YANG, 0, tree, op);
    }
    ly_in_free(in, 0);
    return rc;
}

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lyd_node *tree = NULL, *op = NULL, *source = NULL, *source_op = NULL, *roots = NULL;
    struct lys_module *module = NULL;
    struct ly_in *reply = NULL;
    char *printed = NULL;
    LY_ERR rc;

    if ((argc != 5) && (argc != 6)) {
        return 2;
    }
    if (strcmp(ly_version_proj_str(), "5.8.6")) {
        fprintf(stderr, "expected libyang 5.8.6, got %s\n", ly_version_proj_str());
        return 2;
    }
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx);
    if (!rc) rc = lys_parse_path(ctx, argv[1], LYS_IN_YANG, &module);
    if (!rc) rc = parse_rpc(ctx, argv[3], &tree, &op);
    if (!rc && (argc == 6)) {
        rc = ly_in_new_memory(argv[5], &reply);
        if (!rc) rc = lyd_parse_op(ctx, op, reply, LYD_JSON, LYD_TYPE_REPLY_YANG, 0, NULL, NULL);
    }
    if (!rc) rc = parse_rpc(ctx, argv[4], &source, &source_op);
    if (!rc) {
        if (!strcmp(argv[2], "apply")) {
            rc = lyd_diff_apply_all(&tree, source);
        } else if (!strcmp(argv[2], "merge")) {
            rc = lyd_merge_siblings(&tree, source, 0);
        } else if (!strcmp(argv[2], "move")) {
            rc = lyd_dup_siblings(lyd_child(source_op), NULL, LYD_DUP_RECURSIVE, &roots);
            if (!rc) rc = lyd_insert_child(op, roots);
            if (!rc) roots = NULL; /* target owns the whole copied sibling forest */
        } else {
            rc = LY_EINVAL;
        }
    }
    if (!rc) rc = lyd_print_mem(&printed, tree, LYD_JSON, LYD_PRINT_SHRINK | LYD_PRINT_SIBLINGS);
    if (!rc) puts(printed);
    if (rc) fprintf(stderr, "native RPC probe failed: %d: %s\n", rc, ctx ? ly_errmsg(ctx) : "context creation");
    free(printed);
    ly_in_free(reply, 0);
    lyd_free_all(roots);
    lyd_free_all(source);
    lyd_free_all(tree);
    ly_ctx_destroy(ctx);
    return rc ? 1 : 0;
}
