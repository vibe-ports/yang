/* SPDX-License-Identifier: BSD-3-Clause */
/* The sequence protocol cannot retain a mutated whole source forest. This probe checks
 * native metadata identity across temporary validation and whole-forest transfers. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static LY_ERR
add(struct lyd_node **tree, struct lys_module *mod, const char *key)
{
    struct lyd_node *node = NULL;
    LY_ERR rc = lyd_new_list(NULL, mod, "l", 0, &node, key);
    if (!rc) rc = lyd_insert_sibling(*tree, node, tree);
    if (rc) lyd_free_tree(node);
    return rc;
}

int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *mod = NULL;
    struct ly_in *in = NULL;
    struct lyd_node *src = NULL, *op = NULL, *dst = NULL, *owner = NULL, *dup = NULL;
    struct lyd_meta *kept = NULL, *meta;
    char *json = NULL;
    LY_ERR rc;
    const char *keys[] = {"3", "5", "7", "9", "11", "13"};
    const char *targets[][3] = {{NULL, NULL, NULL}, {"0", "4", "8"}, {"10", "12", "14"}};
    if (argc != 3 || strcmp(ly_version_proj_str(), "5.8.6")) return 2;
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx);
    if (!rc) rc = lys_parse_path(ctx, argv[1], LYS_IN_YANG, &mod);
    if (!rc && !strcmp(argv[2], "empty-unlink")) {
        struct lyd_node *a = NULL, *b = NULL, *nine = NULL, *one = NULL, *holder = NULL, *fresh = NULL;
        rc = lyd_new_inner(NULL, mod, "c", 0, &src);
        if (!rc) rc = lyd_new_list(src, NULL, "l", 0, &a, "3");
        if (!rc) rc = lyd_new_list(src, NULL, "l", 0, &b, "5");
        if (!rc) kept = a->meta;
        for (unsigned i = 0; !rc && i < 2; ++i) {
            rc = lyd_new_inner(NULL, mod, "c", 0, &holder);
            if (!rc) rc = lyd_new_list(holder, NULL, "l", 0, &fresh, i ? "1" : "9");
            if (!rc) rc = lyd_dup_single(fresh, src, LYD_DUP_RECURSIVE | LYD_DUP_NO_LYDS, i ? &one : &nine);
            lyd_free_all(holder);
            holder = NULL;
        }
        if (!rc) {
            lyd_free_tree(a);
            lyd_free_tree(b);
            if (nine->meta != kept) rc = LY_EINT;
        }
        if (!rc) {
            lyd_unlink_tree(nine);
            dup = nine;
            if (nine->meta || (one->meta != kept)) rc = LY_EINT;
        }
        if (!rc) puts("empty-unlink=ok");
        goto cleanup;
    }
    if (!rc && !strcmp(argv[2], "many")) {
        rc = add(&src, mod, "3");
        if (!rc) rc = add(&src, mod, "11");
        for (unsigned round = 0; !rc && round < 32; ++round) {
            rc = lyd_dup_single(src, NULL, LYD_DUP_RECURSIVE | LYD_DUP_NO_LYDS, &dup);
            lyd_free_all(src);
            src = NULL;
            for (unsigned i = 1; !rc && i < 4; ++i) rc = add(&src, mod, keys[i]);
            dst = dup;
            dup = NULL;
            if (!rc) rc = lyd_insert_sibling(dst, src, &dst);
            if (!rc) {src = dst; dst = NULL;}
        }
        if (!rc) {
            owner = src;
            kept = owner->meta;
            while (kept && kept->next) kept = kept->next;
        }
        goto duplicate;
    }
    if (!rc && !strcmp(argv[2], "empty")) {
        rc = add(&src, mod, "3");
        if (!rc) rc = add(&src, mod, "11");
        if (!rc) rc = lyd_dup_single(src, NULL, LYD_DUP_RECURSIVE | LYD_DUP_NO_LYDS, &dst);
        lyd_free_all(src);
        src = NULL;
        for (unsigned i = 1; !rc && i < 4; ++i) rc = add(&src, mod, keys[i]);
        if (!rc) rc = lyd_insert_sibling(dst, src, &dst);
        if (!rc) src = NULL;
        if (!rc) {
            unsigned count = 0;
            for (meta = dst->meta; meta; meta = meta->next) ++count;
            printf("dup-slots=%u\n", count);
        }
        for (unsigned i = 0; !rc && i < 3; ++i) {
            if (i) rc = add(&dst, mod, i == 1 ? "4" : "6");
            if (!rc) rc = lyd_print_mem(&json, dst, LYD_JSON, LYD_PRINT_SHRINK | LYD_PRINT_SIBLINGS);
            if (!rc) puts(json);
            free(json);
            json = NULL;
        }
        goto cleanup;
    }
    if (!rc) rc = ly_in_new_memory("<l xmlns=\"urn:stage3-extra\"><k>1</k><act><x>q</x></act></l>", &in);
    if (!rc) rc = lyd_parse_op(ctx, NULL, in, LYD_XML, LYD_TYPE_RPC_YANG, 0, &src, &op);
    ly_in_free(in, 0);
    for (unsigned i = 0; !rc && i < 6; ++i) rc = add(&src, mod, keys[i]);
    if (!rc) rc = lyd_validate_op(src, NULL, LYD_TYPE_RPC_YANG, NULL);
    if (!rc && strcmp(argv[2], "unrebuilt")) rc = add(&src, mod, "2");
    if (!rc && !strcmp(argv[2], "promote")) {
        for (unsigned i = 0; i < 2; ++i) {
            struct lyd_node *removed = src;
            src = src->next;
            lyd_free_tree(removed);
        }
        owner = src;
        kept = owner->meta ? owner->meta->next : NULL;
    } else if (!rc) {
        owner = !strcmp(argv[2], "unrebuilt") ? src->next : src->next->next;
        kept = owner->meta;
    }
duplicate:
    if (!rc && !kept) rc = LY_EINT;
    if (!rc) rc = lyd_dup_single(owner, NULL, LYD_DUP_RECURSIVE | LYD_DUP_NO_LYDS, &dup);
    if (!rc) {
        unsigned count = 0;
        for (meta = dup->meta; meta; meta = meta->next) ++count;
        printf("dup-slots=%u\n", count);
    }
    lyd_free_tree(dup);
    dup = NULL;
    for (unsigned stage = 0; !rc && stage < 3; ++stage) {
        dst = NULL;
        for (unsigned i = 0; !rc && i < 3 && targets[stage][i]; ++i) rc = add(&dst, mod, targets[stage][i]);
        if (!rc) rc = lyd_insert_sibling(dst, src, &dst);
        if (!rc) src = NULL;
        if (!rc) {
            for (meta = owner->meta; meta && meta != kept; meta = meta->next) {}
            if (!meta) rc = LY_EINT;
        }
        if (!rc) rc = lyd_print_mem(&json, dst, LYD_JSON, LYD_PRINT_SHRINK | LYD_PRINT_SIBLINGS);
        if (!rc) puts(json);
        free(json);
        json = NULL;
        if (!rc) { src = dst; dst = NULL; }
    }
cleanup:
    if (rc) fprintf(stderr, "native metadata transfer failed: %d: %s\n", rc, ctx ? ly_errmsg(ctx) : "context");
    lyd_free_all(dst);
    lyd_free_all(src);
    lyd_free_all(dup);
    ly_ctx_destroy(ctx);
    return rc ? 1 : 0;
}
