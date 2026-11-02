/* SPDX-License-Identifier: BSD-3-Clause */
/* Live native replay for #247 part 5. Go builds the same layout through package-only placement. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void
print_children(struct lyd_node *parent)
{
    const struct lyd_node *n;
    LY_LIST_FOR(lyd_child(parent), n) {
        printf("%s", LYD_NAME(n));
        if (n->schema) printf("=%s", lyd_get_value(n));
        putchar('\n');
    }
    puts("--");
}

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *mod = NULL;
    struct lyd_node *tree = NULL, *n = NULL;
    LY_ERR rc;
    if (argc < 3 || strcmp(ly_version_proj_str(), "5.8.6")) return 2;
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx);
    if (!rc) rc = lys_parse_path(ctx, argv[1], LYS_IN_YANG, &mod);
    if (!rc) rc = lyd_parse_data_mem(ctx, argv[2], LYD_XML,
            LYD_PARSE_ONLY | LYD_PARSE_OPAQ | LYD_PARSE_ORDERED, 0, &tree);
    if (!rc) print_children(tree);
    for (int i = 3; !rc && i < argc; ++i) {
        if (!strncmp(argv[i], "add=", 4)) {
            rc = lyd_new_term(tree, NULL, "ll", argv[i] + 4, 0, &n);
        } else if (!strncmp(argv[i], "del=", 4)) {
            unsigned at = (unsigned)strtoul(argv[i] + 4, NULL, 10);
            n = lyd_child(tree);
            while (n && at--) n = n->next;
            if (!n) rc = LY_EINVAL;
            else lyd_free_tree(n);
        } else rc = LY_EINVAL;
        if (!rc) print_children(tree);
    }
    lyd_free_all(tree);
    ly_ctx_destroy(ctx);
    return rc ? 1 : 0;
}
