/* SPDX-License-Identifier: BSD-3-Clause */
/* The sequence protocol cannot add a node under an output list instance (its paths are looked
 * up as input) nor parse RESTCONF input into a retained request. This probe prints the order a
 * failed lyd_parse_op into a retained RPC leaves behind (its cleanup frees the parsed set
 * backwards) and where later insertions land. */
#include <libyang/libyang.h>
#include <libyang/version.h>
#include <stdio.h>
#include <string.h>

static void
dump(const struct lyd_node *rpc)
{
    const struct lyd_node *l, *c;
    LY_LIST_FOR(lyd_child(rpc), l) {
        printf("(");
        LY_LIST_FOR(lyd_child(l), c) {
            printf("%s%s", c == lyd_child(l) ? "" : ",", lyd_get_value(c));
        }
        printf(")");
    }
    printf("\n");
}

/* argv: module, mode ("xml" or "json" reply, "restconf" JSON input), the data to parse, then
 * the steps: "l:A:B" adds a list instance, "a:I:V" a further "a" key leaf under the I-th
 * instance added, "p" parses the data (or "p:DATA" that data), "d" dumps. */
int
main(int argc, char **argv)
{
    struct ly_ctx *ctx = NULL;
    struct lys_module *mod = NULL;
    struct lyd_node *rpc = NULL, *made[64], *env;
    struct ly_in *in = NULL;
    char a[32], b[32];
    int i, n = 0, at, restconf;
    uint32_t out;
    LY_ERR rc;
    if (argc < 4 || strcmp(ly_version_proj_str(), "5.8.6")) return 2;
    restconf = !strcmp(argv[2], "restconf");
    out = restconf ? 0 : LYD_NEW_VAL_OUTPUT;
    rc = ly_ctx_new(ly_yang_module_dir(), LY_CTX_NO_YANGLIBRARY, &ctx);
    if (!rc) rc = lys_parse_path(ctx, argv[1], LYS_IN_YANG, &mod);
    if (!rc) rc = lyd_new_inner(NULL, mod, "r", 0, &rpc);
    if (rc) return 1;
    ly_log_options(0);
    for (i = 4; i < argc; i++) {
        if (sscanf(argv[i], "l:%31[^:]:%31s", a, b) == 2) {
            if (n == 64 || lyd_new_list(rpc, NULL, "l", out, &made[n++], a, b)) return 1;
        } else if (sscanf(argv[i], "a:%d:%31s", &at, a) == 2) {
            if (at < 0 || at >= n || lyd_new_term(made[at], NULL, "a", a, out, NULL)) return 1;
        } else if (!strcmp(argv[i], "p") || !strncmp(argv[i], "p:", 2)) {
            env = NULL;
            ly_in_new_memory(argv[i][1] ? argv[i] + 2 : argv[3], &in);
            rc = lyd_parse_op(ctx, rpc, in, !strcmp(argv[2], "xml") ? LYD_XML : LYD_JSON,
                    restconf ? LYD_TYPE_RPC_RESTCONF : LYD_TYPE_REPLY_YANG, LYD_PARSE_STRICT, &env, NULL);
            /* RESTCONF requires a tree: with a parent it is NULL or the envelope, freed here */
            if (env != rpc) lyd_free_all(env);
            ly_in_free(in, 0);
            printf("parse=%d\n", rc);
        } else if (!strcmp(argv[i], "d")) {
            dump(rpc);
        } else {
            return 3;
        }
    }
    lyd_free_all(rpc);
    ly_ctx_destroy(ctx);
    return 0;
}
