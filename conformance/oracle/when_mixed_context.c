/* SPDX-License-Identifier: BSD-3-Clause */
/* Two-context child-table setup corresponding to data/linkForeign. The public
 * insertion normally checks schema-parent identity. A transient LYD_EXT skips
 * only that setup guard; it is cleared before any lookup or validation. There
 * are no extension nodes during the measured when pass. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static unsigned
errors(const struct ly_ctx *ctx)
{
    unsigned count = 0;
    const struct ly_err_item *item;
    for (item = ly_err_first(ctx); item; item = item->next) ++count;
    return count;
}

int
main(int argc, char **argv)
{
    struct ly_ctx *a = NULL, *b = NULL;
    struct lys_module *ma = NULL, *mb = NULL;
    struct lyd_node *tree = NULL, *other = NULL, *live = NULL, *diff = NULL;
    char *json = NULL, *changes = NULL;
    LY_ERR rc;
    if (argc != 4 || strcmp(ly_version_proj_str(), "5.8.6")) return 2;
    ly_log_options(LY_LOSTORE);
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &a);
    if (!rc) rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &b);
    if (!rc) rc = lys_parse_path(a, argv[1], LYS_IN_YANG, &ma);
    if (!rc) rc = lys_parse_path(b, argv[2], LYS_IN_YANG, &mb);
    if (!rc) rc = lyd_parse_data_mem(a,
            "{\"m:c\":{\"g\":\"g\",\"l\":[{\"k\":\"dead0\"},{\"k\":\"dead1\"}],\"f1\":\"1\",\"f2\":\"2\",\"f3\":\"3\"}}",
            LYD_JSON, LYD_PARSE_ONLY | LYD_PARSE_WHEN_TRUE, 0, &tree);
    if (!rc) rc = lyd_parse_data_mem(b, "{\"m:c\":{\"l\":[{\"k\":\"live\"}]}}", LYD_JSON, LYD_PARSE_ONLY, 0, &other);
    if (!rc) {
        live = lyd_child(other);
        if (!live || !((struct lyd_node_inner *)tree)->children_ht) rc = LY_EINT;
    }
    if (!rc) {
        live->flags |= LYD_EXT;
        rc = lyd_insert_child(tree, live);
        live->flags &= ~LYD_EXT;
    }
    if (!rc && (!live->schema || live->schema->module->ctx != b || live->parent != tree)) rc = LY_EINT;
    if (rc) {
        fprintf(stderr, "native mixed-context setup failed: %d\n", rc);
        goto cleanup;
    }
    /* Only validation diagnostics are measured, after the setup warnings. */
    ly_err_clean(a, NULL);
    ly_err_clean(b, NULL);
    rc = !strcmp(argv[3], "module") ? lyd_validate_module(&tree, ma, LYD_VALIDATE_NOT_FINAL, &diff) :
            lyd_validate_all(&tree, a, LYD_VALIDATE_NOT_FINAL | LYD_VALIDATE_PRESENT, &diff);
    printf("%d %u %u\n", rc, errors(a), errors(b));
    if (lyd_print_mem(&json, tree, LYD_JSON, LYD_PRINT_SHRINK | LYD_PRINT_SIBLINGS | LYD_PRINT_WD_ALL) ||
            lyd_print_mem(&changes, diff, LYD_JSON, LYD_PRINT_SHRINK | LYD_PRINT_SIBLINGS | LYD_PRINT_WD_ALL)) {
        rc = LY_EINT;
        goto cleanup;
    }
    printf("%s\n%s\n", json ? json : "{}", changes ? changes : "{}");
cleanup:
    free(json);
    free(changes);
    lyd_free_all(diff);
    lyd_free_all(tree);
    lyd_free_all(other);
    ly_ctx_destroy(a);
    ly_ctx_destroy(b);
    return rc ? 1 : 0;
}
