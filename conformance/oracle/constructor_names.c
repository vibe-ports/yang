// SPDX-License-Identifier: BSD-3-Clause

/* Independent native name-grammar probes for the constructor adapter family. */
#include <libyang/libyang.h>
#include <stdio.h>
#include <string.h>

static int
not_found(struct ly_ctx *ctx, const char *fn, LY_ERR rc)
{
    const struct ly_err_item *err = ly_err_last(ctx);

    if ((rc != LY_ENOTFOUND) || !err || (err->err != LY_EINVAL)) {
        fprintf(stderr, "%s: unexpected rc %d or diagnostic\n", fn, rc);
        return 1;
    }
    printf("%s|%d|%d|%s\n", fn, rc, err->err, err->msg);
    ly_err_clean(ctx, NULL);
    return 0;
}

int
main(void)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *mod = NULL;
    struct lyd_node *parent = NULL, *node = NULL, *opaque = NULL;
    struct lyd_meta *meta = NULL;
    struct lyd_attr *attr = NULL;
    LY_ERR rc;
    int failed = 1;
    const char *schema = "module names { yang-version 1.1; namespace urn:names; prefix n;"
            "import ietf-yang-metadata { prefix md; } md:annotation note { type string; }"
            "container c { config false; leaf term { type string; } container inner;"
            "list list; anydata any; } }";

    ly_log_options(LY_LOSTORE);
    if (ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx)) {
        return 1;
    }
    if (lys_parse_mem(ctx, schema, LYS_IN_YANG, &mod) || lyd_new_inner(NULL, mod, "c", 0, &parent)) {
        goto cleanup;
    }
    rc = lyd_new_term(parent, NULL, "names:term", "x", 0, &node);
    if (not_found(ctx, "new_term", rc)) { goto cleanup; }
    rc = lyd_new_inner(parent, NULL, "names:inner", 0, &node);
    if (not_found(ctx, "new_inner", rc)) { goto cleanup; }
    rc = lyd_new_list(parent, NULL, "names:list", 0, &node);
    if (not_found(ctx, "new_list", rc)) { goto cleanup; }
    rc = lyd_new_list2(parent, NULL, "names:list", "", 0, &node);
    if (not_found(ctx, "new_list2", rc)) { goto cleanup; }
    rc = lyd_new_list3(parent, NULL, "names:list", NULL, NULL, 0, &node);
    if (not_found(ctx, "new_list3", rc)) { goto cleanup; }
    rc = lyd_new_any(parent, NULL, "names:any", NULL, NULL, 0, 0, &node);
    if (not_found(ctx, "new_any", rc)) { goto cleanup; }
    rc = lyd_new_meta(NULL, parent, NULL, "names:note", "x", 0, &meta);
    if (rc || !meta) { goto cleanup; }
    printf("new_meta|%d|%s|%s\n", rc, meta->annotation->module->name, meta->name);
    rc = lyd_new_opaq(NULL, ctx, "names:c", "", NULL, "names", &opaque);
    if (rc || !opaque || strcmp(LYD_NAME(opaque), "names:c")) { goto cleanup; }
    rc = lyd_new_attr(opaque, NULL, "names:note", "x", &attr);
    if (rc || !attr) { goto cleanup; }
    printf("new_attr|%d|%s|%s\n", rc, attr->name.prefix, attr->name.name);
    printf("new_opaque|%d|%s\n", LY_SUCCESS, LYD_NAME(opaque));
    failed = 0;
cleanup:
    if (failed) {
        fprintf(stderr, "native constructor probe failed: %s\n", ly_last_logmsg());
    }
    lyd_free_all(opaque);
    lyd_free_all(parent);
    ly_ctx_destroy(ctx);
    return failed;
}
