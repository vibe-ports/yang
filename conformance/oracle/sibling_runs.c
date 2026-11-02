/* SPDX-License-Identifier: BSD-3-Clause */
/* Live native replay for #247 part 5. Go builds the same layout through package-only placement. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static struct lyd_node *
first_node(struct lyd_node *tree)
{
    return tree && tree->schema && tree->schema->nodetype == LYS_CONTAINER ? lyd_child(tree) : tree;
}

static struct lyd_node *
child_at(struct lyd_node *parent, unsigned at)
{
    struct lyd_node *n = first_node(parent);
    while (n && at--) n = n->next;
    return n;
}

static void
print_children(struct lyd_node *parent)
{
    const struct lyd_node *n;
    LY_LIST_FOR(first_node(parent), n) {
        printf("%s", LYD_NAME(n));
        if (n->schema) printf("=%s", lyd_get_value(n));
        for (const struct lyd_meta *m = n->meta; m; m = m->next) {
            if (!strcmp(m->name, "lyds_tree")) {
                struct lyd_value_lyds_tree *lt;
                LYD_VALUE_GET(&m->value, lt);
                printf(" [rb=%s]", lt && lt->rbt ? "live" : "null");
            }
        }
        putchar('\n');
    }
    puts("--");
}

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL, *foreign = NULL;
    struct lys_module *mod = NULL;
    struct lyd_node *tree = NULL, *n = NULL;
    struct lyd_node **removed = calloc((size_t)argc, sizeof *removed);
    size_t removed_count = 0;
    if (!removed) return 2;
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
        } else if (!strncmp(argv[i], "insert=", 7) || !strncmp(argv[i], "foreign=", 8)) {
            struct lys_module *source_mod = mod;
            const char *value = argv[i] + 7;
            if (argv[i][0] == 'f') {
                value = argv[i] + 8;
                if (!foreign) rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &foreign);
                if (!rc) rc = lys_parse_path(foreign, argv[1], LYS_IN_YANG, &source_mod);
            }
            if (!rc) rc = lyd_new_term(NULL, source_mod, "ll", value, 0, &n);
            if (!rc) rc = lyd_insert_sibling(tree, n, &tree);
        } else if (!strncmp(argv[i], "opaq=", 5)) {
            struct lyd_node *anchor = child_at(tree, (unsigned)strtoul(argv[i] + 5, NULL, 10));
            if (!anchor) rc = LY_EINVAL;
            else {
                n = NULL;
                rc = lyd_new_opaq2(NULL, ctx, "zz", "u", NULL, "urn:run-test", &n);
                if (!rc) rc = lyd_insert_before(anchor, n);
                if (rc) lyd_free_tree(n);
            }
        } else if (!strcmp(argv[i], "dup")) {
            struct lyd_node *copy = NULL;
            rc = lyd_dup_single(tree, NULL, LYD_DUP_RECURSIVE | LYD_DUP_WITH_FLAGS, &copy);
            if (!rc) { lyd_free_all(tree); tree = copy; }
        } else if (!strncmp(argv[i], "del=", 4)) {
            unsigned at = (unsigned)strtoul(argv[i] + 4, NULL, 10);
            n = first_node(tree);
            while (n && at--) n = n->next;
            if (!n) rc = LY_EINVAL;
            else {
                if (n == tree) tree = n->next;
                lyd_unlink_tree(n);
                removed[removed_count++] = n;
            }
        } else rc = LY_EINVAL;
        if (!rc) print_children(tree);
    }
    lyd_free_all(tree);
    for (size_t i = 0; i < removed_count; ++i) lyd_free_tree(removed[i]);
    free(removed);
    ly_ctx_destroy(ctx);
    ly_ctx_destroy(foreign);
    return rc ? 1 : 0;
}
