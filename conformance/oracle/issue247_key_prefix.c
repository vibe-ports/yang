/* SPDX-License-Identifier: BSD-3-Clause */
/* Native document-order key-prefix probe. argv: schema, ordered JSON input.
 * Built only by the oracle-tagged Go regression in the dev container. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *module = NULL;
    struct lyd_node *tree = NULL, *list, *child, *last_key = NULL, *separator = NULL;
    char *path = NULL;
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
            LY_LIST_FOR(lyd_child(list), child) {
                if (child->schema && (child->schema->flags & LYS_KEY)) last_key = child;
                if (!separator && (!child->schema || !(child->schema->flags & LYS_KEY))) separator = child;
            }
            if ((argc == 4) && !strcmp(argv[3], "sort")) {
                /* Begin with valid declared keys and displace one physically. This
                 * preserves the native hash history, as an internal/copy layout can. */
                struct lyd_node *other = list->next;
                if (!separator || !last_key || !other) {
                    rc = LY_EINVAL;
                } else {
                    rc = lyd_insert_before(last_key, separator);
                    if (!rc) rc = lyd_unlink_tree(other);
                    if (!rc) rc = lyd_insert_child(tree, other);
                    if (!rc) printf("sort %d\n", lyd_child(tree) == list);
                }
            } else if (argc == 4) {
                if (!strcmp(argv[3], "unlink") || !strcmp(argv[3], "unlink-change")) {
                    rc = separator ? lyd_unlink_tree(separator) : LY_EINVAL;
                    if (!rc) lyd_free_tree(separator);
                }
                if (!rc && strcmp(argv[3], "unlink")) {
                    rc = last_key ? lyd_change_term(last_key, "S") : LY_EINVAL;
                }
            }
            LY_LIST_FOR(lyd_child(list), child) {
                ++count;
                leading = leading && child->schema && (child->schema->flags & LYS_KEY);
                prefix += leading;
            }
            if (!rc) {
                path = lyd_path(list, LYD_PATH_STD, NULL, 0);
                if (!path) rc = LY_EMEM;
            }
            if (!rc) printf("%" PRIu32 " %u %u\n%s\n", list->hash, prefix, count, path);
            free(path);
        }
    }
    if (rc) fprintf(stderr, "native key-prefix probe: %d: %s\n", rc, ctx ? ly_errmsg(ctx) : "context creation");
    lyd_free_all(tree);
    ly_ctx_destroy(ctx);
    return rc ? 1 : 0;
}
