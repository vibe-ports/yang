/* SPDX-License-Identifier: BSD-3-Clause */
/* Native document-order key-prefix probe. argv: schema, ordered JSON input.
 * Built only by the oracle-tagged Go regression in the dev container. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <inttypes.h>
#include <stdio.h>
#include <string.h>

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *module = NULL;
    struct lyd_node *tree = NULL, *list, *child, *last_key = NULL;
    unsigned prefix = 0, count = 0;
    int leading = 1;
    LY_ERR rc;

    if (((argc != 3) && (argc != 4)) || strcmp(ly_version_proj_str(), "5.8.6")) return 2;
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx);
    if (!rc) rc = lys_parse_path(ctx, argv[1], LYS_IN_YANG, &module);
    if (!rc) rc = lyd_parse_data_mem(ctx, argv[2], LYD_JSON,
            LYD_PARSE_ONLY | LYD_PARSE_OPAQ | LYD_PARSE_ORDERED, 0, &tree);
    if (!rc) {
        list = lyd_child(tree);
        if (!list || !list->schema || (list->schema->nodetype != LYS_LIST)) {
            rc = LY_EINVAL;
        } else {
            if (argc == 4) {
                LY_LIST_FOR(lyd_child(list), child) {
                    if (child->schema && (child->schema->flags & LYS_KEY)) last_key = child;
                }
                rc = last_key ? lyd_change_term(last_key, "S") : LY_EINVAL;
            }
            LY_LIST_FOR(lyd_child(list), child) {
                ++count;
                leading = leading && child->schema && (child->schema->flags & LYS_KEY);
                prefix += leading;
            }
            if (!rc) printf("%" PRIu32 " %u %u\n", list->hash, prefix, count);
        }
    }
    if (rc) fprintf(stderr, "native key-prefix probe: %d: %s\n", rc, ctx ? ly_errmsg(ctx) : "context creation");
    lyd_free_all(tree);
    ly_ctx_destroy(ctx);
    return rc ? 1 : 0;
}
