/*
 * lyoracle - test-only oracle helper for the yang Go port.
 *
 * Reads one JSON request on stdin, runs it through libyang (v5.8.6) and writes one JSON
 * response on stdout. See README.md for the request/response schema.
 *
 * Errors are captured with LY_LOSTORE + ly_err_first() on the context, never from the log
 * callback, so diagnostics carry structured fields (level, code, vecode, paths, apptag, line).
 *
 * SPDX-License-Identifier: BSD-3-Clause
 */
#define _POSIX_C_SOURCE 200809L

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <libyang/libyang.h>
#include <libyang/version.h>

#include "cjson/cJSON.h"

/* ---------- small helpers ---------- */

struct flag {
    const char *name;
    uint32_t val;
};

static const struct flag ctx_flags[] = {
    {"all_implemented", LY_CTX_ALL_IMPLEMENTED},
    {"ref_implemented", LY_CTX_REF_IMPLEMENTED},
    {"no_yanglibrary", LY_CTX_NO_YANGLIBRARY},
    {"enable_imp_features", LY_CTX_ENABLE_IMP_FEATURES},
    {"compile_obsolete", LY_CTX_COMPILE_OBSOLETE},
    {"leafref_extended", LY_CTX_LEAFREF_EXTENDED},
    {"leafref_linking", LY_CTX_LEAFREF_LINKING},
    {"builtin_plugins_only", LY_CTX_BUILTIN_PLUGINS_ONLY},
    {NULL, 0}
};

static const struct flag parse_flags[] = {
    {"only", LYD_PARSE_ONLY},
    {"strict", LYD_PARSE_STRICT},
    {"opaq", LYD_PARSE_OPAQ},
    {"no_state", LYD_PARSE_NO_STATE},
    {"ordered", LYD_PARSE_ORDERED},
    {"when_true", LYD_PARSE_WHEN_TRUE},
    {"store_only", LYD_PARSE_STORE_ONLY},
    {"json_null", LYD_PARSE_JSON_NULL},
    {"json_string_datatypes", LYD_PARSE_JSON_STRING_DATATYPES},
    {"anydata_strict", LYD_PARSE_ANYDATA_STRICT},
    {NULL, 0}
};

static const struct flag validate_flags[] = {
    {"no_state", LYD_VALIDATE_NO_STATE},
    {"present", LYD_VALIDATE_PRESENT},
    {"multi_error", LYD_VALIDATE_MULTI_ERROR},
    {"operational", LYD_VALIDATE_OPERATIONAL},
    {"no_defaults", LYD_VALIDATE_NO_DEFAULTS},
    {"not_final", LYD_VALIDATE_NOT_FINAL},
    {NULL, 0}
};

static const struct flag wd_modes[] = {
    {"explicit", LYD_PRINT_WD_EXPLICIT},
    {"trim", LYD_PRINT_WD_TRIM},
    {"all", LYD_PRINT_WD_ALL},
    {"all-tagged", LYD_PRINT_WD_ALL_TAG},
    {"implicit-tagged", LYD_PRINT_WD_IMPL_TAG},
    {NULL, 0}
};

static const struct flag diff_flags[] = {
    {"defaults", LYD_DIFF_DEFAULTS},
    {"meta", LYD_DIFF_META},
    {NULL, 0}
};

static const char *err_names[] = {
    "LY_SUCCESS", "LY_EMEM", "LY_ESYS", "LY_EINVAL", "LY_EEXIST", "LY_ENOTFOUND", "LY_EINT", "LY_EVALID",
    "LY_EDENIED", "LY_EINCOMPLETE", "LY_ERECOMPILE", "LY_ENOT", "LY_EOTHER"
};

static const char *vecode_names[] = {
    "LYVE_SUCCESS", "LYVE_SYNTAX", "LYVE_SYNTAX_YANG", "LYVE_SYNTAX_YIN", "LYVE_REFERENCE", "LYVE_XPATH",
    "LYVE_SEMANTICS", "LYVE_SYNTAX_XML", "LYVE_SYNTAX_JSON", "LYVE_DATA", "LYVE_OTHER"
};

static const char *level_names[] = {"error", "warning", "verbose", "debug"};

static cJSON *resp;

static void
die(const char *fmt, const char *arg)
{
    char buf[512];

    snprintf(buf, sizeof buf, fmt, arg ? arg : "");
    cJSON_AddStringToObject(resp, "verdict", "request-error");
    cJSON_AddStringToObject(resp, "request_error", buf);
    char *s = cJSON_Print(resp);
    printf("%s\n", s);
    exit(2);
}

static const char *
str_of(const cJSON *o, const char *key)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(o, key);

    if (v && !cJSON_IsNull(v) && !cJSON_IsString(v)) {
        die("field \"%s\" must be a string", key);
    }
    return cJSON_IsString(v) ? v->valuestring : NULL;
}

static uint32_t
flags_of(const cJSON *o, const char *key, const struct flag *tbl)
{
    const cJSON *arr = cJSON_GetObjectItemCaseSensitive(o, key), *it;
    uint32_t r = 0;
    int i;

    cJSON_ArrayForEach(it, arr) {
        for (i = 0; tbl[i].name && (!cJSON_IsString(it) || strcmp(tbl[i].name, it->valuestring)); i++) {}
        if (!tbl[i].name) {
            die("unknown flag in \"%s\"", key);
        }
        r |= tbl[i].val;
    }
    return r;
}

static uint32_t
enum_of(const char *val, const struct flag *tbl, const char *what)
{
    for (int i = 0; tbl[i].name; i++) {
        if (!strcmp(tbl[i].name, val)) {
            return tbl[i].val;
        }
    }
    die("unknown %s", what);
    return 0;
}

static char *
read_file(const char *path)
{
    FILE *f = fopen(path, "rb");
    char *buf;
    long n;

    if (!f) {
        die("cannot open file %s", path);
    }
    fseek(f, 0, SEEK_END);
    n = ftell(f);
    rewind(f);
    buf = malloc(n + 1);
    if (fread(buf, 1, n, f) != (size_t)n) {
        die("cannot read file %s", path);
    }
    buf[n] = '\0';
    fclose(f);
    return buf;
}

/* Input given inline as "<key>" or by path as "<key>_file". NULL if neither. */
static const char *
input_of(const cJSON *req, const char *key)
{
    char fkey[64];
    const char *s = str_of(req, key), *p;

    snprintf(fkey, sizeof fkey, "%s_file", key);
    p = str_of(req, fkey);
    if (s && p) {
        die("both inline and _file given for \"%s\"", key);
    }
    return p ? read_file(p) : s;
}

static cJSON *
code_json(LY_ERR err)
{
    cJSON *c = cJSON_CreateObject();
    unsigned base = err & ~LY_EPLUGIN;
    char name[64];

    snprintf(name, sizeof name, "%s%s", (err & LY_EPLUGIN) ? "LY_EPLUGIN|" : "",
            base < sizeof err_names / sizeof *err_names ? err_names[base] : "?");
    cJSON_AddNumberToObject(c, "err", err);
    cJSON_AddStringToObject(c, "name", name);
    return c;
}

static void
add_opt_str(cJSON *o, const char *key, const char *val)
{
    if (val) {
        cJSON_AddStringToObject(o, key, val);
    } else {
        cJSON_AddNullToObject(o, key);
    }
}

/* Move all stored errors/warnings of ctx into arr, tagged with phase, and clear them. */
static int
collect(const struct ly_ctx *ctx, cJSON *arr, const char *phase)
{
    const struct ly_err_item *e;
    int n = 0;

    for (e = ly_err_first(ctx); e; e = e->next, n++) {
        cJSON *d = cJSON_CreateObject();

        cJSON_AddStringToObject(d, "phase", phase);
        cJSON_AddStringToObject(d, "level", e->level < 4 ? level_names[e->level] : "?");
        cJSON_AddItemToObject(d, "code", code_json(e->err));
        cJSON_AddNumberToObject(d, "vecode", e->vecode);
        cJSON_AddStringToObject(d, "vecode_name",
                (unsigned)e->vecode < sizeof vecode_names / sizeof *vecode_names ? vecode_names[e->vecode] : "?");
        add_opt_str(d, "data_path", e->data_path);
        add_opt_str(d, "schema_path", e->schema_path);
        add_opt_str(d, "apptag", e->apptag);
        cJSON_AddNumberToObject(d, "line", (double)e->line);
        add_opt_str(d, "msg", e->msg);
        cJSON_AddItemToArray(arr, d);
    }
    ly_err_clean((struct ly_ctx *)ctx, NULL);
    return n;
}

/* ---------- schema ---------- */

/*
 * Build a context: searchdirs + implemented modules with features. Per module: "parse" phase is
 * ly_ctx_load_module() under LY_CTX_EXPLICIT_COMPILE (parse, imports/includes, features), "compile"
 * phase is the following ly_ctx_compile(). A failed compile is reverted by libyang, so later
 * modules are still tried. Returns ctx and sets *ok to 1 if every module was accepted.
 */
static struct ly_ctx *
build_ctx(const cJSON *req, int *ok, int dump)
{
    struct ly_ctx *ctx;
    const cJSON *it, *mods = cJSON_GetObjectItemCaseSensitive(req, "modules");
    cJSON *out = cJSON_AddArrayToObject(resp, "modules");
    cJSON *cdiag = cJSON_AddArrayToObject(resp, "context_diagnostics");
    uint32_t opts = flags_of(req, "context_options", ctx_flags);

    *ok = 1;
    /* internal modules (ietf-inet-types, ...) are read from the installed module dir */
    if (ly_ctx_new(ly_yang_module_dir(), opts | LY_CTX_EXPLICIT_COMPILE | LY_CTX_DISABLE_SEARCHDIR_CWD, &ctx)) {
        die("ly_ctx_new failed: %s", ly_last_logmsg());
    }
    cJSON_ArrayForEach(it, cJSON_GetObjectItemCaseSensitive(req, "searchdirs")) {
        if (!cJSON_IsString(it) || ly_ctx_set_searchdir(ctx, it->valuestring)) {
            die("bad searchdir %s", cJSON_IsString(it) ? it->valuestring : "?");
        }
    }
    if (ly_ctx_compile(ctx)) {
        collect(ctx, cdiag, "context");
        die("compiling internal modules failed%s", NULL);
    }
    collect(ctx, cdiag, "context");

    cJSON_ArrayForEach(it, mods) {
        const char *name = str_of(it, "name"), *rev = str_of(it, "revision");
        const cJSON *fa = cJSON_GetObjectItemCaseSensitive(it, "features"), *f;
        const char **feats = NULL;
        cJSON *m = cJSON_CreateObject(), *diag;
        struct lys_module *mod;
        LY_ERR rc;
        int i = 0;

        if (!name) {
            die("module without name%s", NULL);
        }
        if (fa) {
            feats = calloc(cJSON_GetArraySize(fa) + 1, sizeof *feats);
            cJSON_ArrayForEach(f, fa) {
                feats[i++] = f->valuestring;
            }
        }
        cJSON_AddStringToObject(m, "name", name);
        cJSON_AddItemToArray(out, m);
        diag = cJSON_AddArrayToObject(m, "diagnostics");

        mod = ly_ctx_load_module(ctx, name, rev, feats);
        free(feats);
        collect(ctx, diag, "parse");
        if (!mod) {
            *ok = 0;
            cJSON_AddBoolToObject(m, "accepted", 0);
            cJSON_AddStringToObject(m, "phase", "parse");
            continue;
        }
        rc = ly_ctx_compile(ctx);
        collect(ctx, diag, "compile");
        if (rc) {
            *ok = 0;
            cJSON_AddBoolToObject(m, "accepted", 0);
            cJSON_AddStringToObject(m, "phase", "compile");
            cJSON_AddItemToObject(m, "rc", code_json(rc));
            continue;
        }
        cJSON_AddBoolToObject(m, "accepted", 1);
        add_opt_str(m, "revision", mod->revision);
    }

    if (dump) {
        cJSON *m;

        cJSON_ArrayForEach(m, out) {
            struct lys_module *mod;
            char *s = NULL;

            if (!cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(m, "accepted"))) {
                continue;
            }
            mod = ly_ctx_get_module_implemented(ctx, cJSON_GetObjectItemCaseSensitive(m, "name")->valuestring);
            if (mod && !lys_print_mem(&s, mod, LYS_OUT_YANG_COMPILED, 0)) {
                cJSON_AddStringToObject(m, "compiled", s);
            } else {
                cJSON_AddNullToObject(m, "compiled");
            }
            free(s);
        }
    }
    return ctx;
}

/* ---------- data ---------- */

struct dparams {
    LYD_FORMAT fmt;
    const char *type;
    enum lyd_type optype;   /* LYD_TYPE_DATA_YANG for datastore types */
    uint32_t popts, vopts;
    int parse_only;
    struct lyd_node *oper;  /* external operational tree for operations */
};

static LYD_FORMAT
fmt_of(const char *s)
{
    if (!s || !strcmp(s, "json")) {
        return LYD_JSON;
    } else if (!strcmp(s, "xml")) {
        return LYD_XML;
    }
    die("unknown format %s", s);
    return LYD_UNKNOWN;
}

/* Data-type presets follow yanglint (tools/lint/yl_opt.c yl_opt_update_data_type). */
static void
dparams_of(const cJSON *req, struct dparams *p)
{
    const char *t = str_of(req, "data_type");

    memset(p, 0, sizeof *p);
    p->fmt = fmt_of(str_of(req, "format"));
    p->type = t ? t : "data-operational";
    p->popts = LYD_PARSE_STRICT;
    p->vopts = LYD_VALIDATE_MULTI_ERROR;
    p->optype = LYD_TYPE_DATA_YANG;
    p->parse_only = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(req, "parse_only"));

    if (!strcmp(p->type, "config")) {
        p->popts |= LYD_PARSE_NO_STATE;
        p->vopts |= LYD_VALIDATE_NO_STATE;
    } else if (!strcmp(p->type, "data-operational")) {
        p->vopts |= LYD_VALIDATE_OPERATIONAL;
    } else if (!strcmp(p->type, "data")) {
        /* plain lyd_parse_data with the defaults, no preset */
    } else if (!strcmp(p->type, "get")) {
        p->popts |= LYD_PARSE_ONLY;
    } else if (!strcmp(p->type, "getconfig") || !strcmp(p->type, "edit")) {
        p->popts |= LYD_PARSE_ONLY | LYD_PARSE_NO_STATE;
    } else if (!strcmp(p->type, "rpc")) {
        p->optype = LYD_TYPE_RPC_YANG;
    } else if (!strcmp(p->type, "reply")) {
        p->optype = LYD_TYPE_REPLY_YANG;
    } else if (!strcmp(p->type, "notif")) {
        p->optype = LYD_TYPE_NOTIF_YANG;
    } else {
        die("unknown data_type %s", p->type);
    }
    if (p->parse_only) {
        p->popts |= LYD_PARSE_ONLY;
    }
    p->popts |= flags_of(req, "parse_options", parse_flags);
    p->vopts |= flags_of(req, "validate_options", validate_flags);
}

/*
 * Parse (and unless parse-only, validate) one input. Returns rc; *tree is the top-level tree
 * (NULL on failure). For "reply", rpc_src is the request whose output is being parsed.
 */
static LY_ERR
parse_one(struct ly_ctx *ctx, const struct dparams *p, const char *data, const char *rpc_src,
        struct lyd_node **tree, cJSON *diag, const char *phase)
{
    struct ly_in *in = NULL;
    struct lyd_node *op = NULL;
    LY_ERR rc;

    *tree = NULL;
    ly_in_new_memory(data, &in);
    if (p->optype == LYD_TYPE_DATA_YANG) {
        rc = lyd_parse_data(ctx, NULL, in, p->fmt, p->popts, p->popts & LYD_PARSE_ONLY ? 0 : p->vopts, tree);
        goto done;
    }

    /* operations: lyd_parse_op accepts only STRICT/OPAQ */
    if ((p->optype == LYD_TYPE_REPLY_YANG) && rpc_src) {
        struct ly_in *rin = NULL;

        ly_in_new_memory(rpc_src, &rin);
        rc = lyd_parse_op(ctx, NULL, rin, p->fmt, LYD_TYPE_RPC_YANG, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ),
                tree, &op);
        ly_in_free(rin, 0);
        collect(ctx, diag, "rpc");
        if (rc) {
            goto done;
        }
        lyd_free_siblings(lyd_child(op));
        rc = lyd_parse_op(ctx, op, in, p->fmt, LYD_TYPE_REPLY_YANG, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ),
                NULL, NULL);
    } else {
        rc = lyd_parse_op(ctx, NULL, in, p->fmt, p->optype, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ), tree, &op);
    }
    if (!rc && !p->parse_only) {
        collect(ctx, diag, phase);
        rc = lyd_validate_op(*tree, p->oper, p->optype, NULL);
        phase = "validate_op";
        if (!rc && op && op->parent) {
            /* like yanglint check_operation_parent(): a nested action/notif needs its parent in the
             * operational tree; libyang itself does not check this */
            char *path = lyd_path(op->parent, LYD_PATH_STD, NULL, 0);
            struct ly_set *set = NULL;

            collect(ctx, diag, phase);
            if (!p->oper || lyd_find_xpath(p->oper, path, &set) || !set->count) {
                cJSON *d = cJSON_CreateObject();

                cJSON_AddStringToObject(d, "phase", "operation_parent");
                cJSON_AddStringToObject(d, "level", "error");
                cJSON_AddItemToObject(d, "code", code_json(LY_EVALID));
                cJSON_AddStringToObject(d, "source", "lyoracle");
                add_opt_str(d, "data_path", path);
                cJSON_AddStringToObject(d, "msg", "operation parent not found in the operational tree");
                cJSON_AddItemToArray(diag, d);
                rc = LY_EVALID;
            }
            ly_set_free(set, NULL);
            free(path);
            phase = "operation_parent";
        }
    }

done:
    ly_in_free(in, 0);
    collect(ctx, diag, phase);
    if (rc) {
        lyd_free_all(*tree);
        *tree = NULL;
    }
    return rc;
}

static uint32_t
wd_of(const cJSON *req)
{
    const char *wd = str_of(req, "with_defaults");

    return wd ? enum_of(wd, wd_modes, "with_defaults mode") : LYD_PRINT_WD_EXPLICIT;
}

static cJSON *
print_tree(const struct lyd_node *tree, uint32_t opts, int siblings)
{
    cJSON *t = cJSON_CreateObject();
    char *s;

    if (siblings) {
        opts |= LYD_PRINT_SIBLINGS;
    }
    s = NULL;
    lyd_print_mem(&s, tree, LYD_JSON, opts);
    add_opt_str(t, "json", s ? s : "");
    free(s);
    s = NULL;
    lyd_print_mem(&s, tree, LYD_XML, opts);
    add_opt_str(t, "xml", s ? s : "");
    free(s);
    return t;
}

static void
set_verdict(LY_ERR rc)
{
    cJSON_AddStringToObject(resp, "verdict", rc ? "invalid" : "valid");
    cJSON_AddItemToObject(resp, "rc", code_json(rc));
}

/* Operational tree for operations; parsed LYD_PARSE_ONLY as yanglint -O does. */
static void
load_oper(struct ly_ctx *ctx, const cJSON *req, struct dparams *p, cJSON *diag)
{
    const char *o = input_of(req, "operational");
    const char *of = str_of(req, "operational_format");
    struct ly_in *in = NULL;

    if (!o) {
        return;
    }
    ly_in_new_memory(o, &in);
    if (lyd_parse_data(ctx, NULL, in, of ? fmt_of(of) : p->fmt, LYD_PARSE_ONLY, 0, &p->oper)) {
        collect(ctx, diag, "operational");
        cJSON_AddStringToObject(resp, "verdict", "operational-error");
        return;
    }
    collect(ctx, diag, "operational");
    ly_in_free(in, 0);
}

/* Common prelude for data/xpath/diff. Returns NULL (and sets verdict) if schema load failed. */
static struct ly_ctx *
data_prelude(const cJSON *req, struct dparams *p, cJSON **diag)
{
    int ok;
    struct ly_ctx *ctx = build_ctx(req, &ok, 0);

    *diag = cJSON_AddArrayToObject(resp, "diagnostics");
    if (!ok) {
        cJSON_AddStringToObject(resp, "verdict", "schema-error");
        return NULL;
    }
    dparams_of(req, p);
    load_oper(ctx, req, p, *diag);
    return cJSON_GetObjectItemCaseSensitive(resp, "verdict") ? NULL : ctx;
}

static void
op_data(const cJSON *req)
{
    struct dparams p;
    struct lyd_node *tree;
    cJSON *diag;
    struct ly_ctx *ctx = data_prelude(req, &p, &diag);
    const char *data;
    LY_ERR rc;

    if (!ctx) {
        return;
    }
    if (!(data = input_of(req, "data"))) {
        die("missing data%s", NULL);
    }
    rc = parse_one(ctx, &p, data, input_of(req, "rpc"), &tree, diag, "data");
    set_verdict(rc);
    if (tree) {
        cJSON_AddItemToObject(resp, "tree", print_tree(tree, wd_of(req), p.optype == LYD_TYPE_DATA_YANG));
    } else {
        cJSON_AddNullToObject(resp, "tree");
    }
}

static void
op_xpath(const cJSON *req)
{
    struct dparams p;
    struct lyd_node *tree, *cnode = NULL;
    cJSON *diag, *res;
    struct ly_ctx *ctx = data_prelude(req, &p, &diag);
    const char *expr = str_of(req, "xpath"), *cpath = str_of(req, "context_path"), *cm = str_of(req, "cur_module");
    const struct lys_module *cur = NULL;
    struct ly_set *set = NULL;
    char *str = NULL;
    long double num = 0;
    ly_bool b = 0;
    LY_XPATH_TYPE t;
    LY_ERR rc;

    if (!ctx) {
        return;
    }
    if (!expr) {
        die("missing xpath%s", NULL);
    }
    rc = parse_one(ctx, &p, input_of(req, "data"), NULL, &tree, diag, "data");
    if (rc || !tree) {
        cJSON_AddStringToObject(resp, "verdict", rc ? "data-error" : "empty-tree");
        return;
    }
    if (cm && !(cur = ly_ctx_get_module_implemented(ctx, cm))) {
        die("cur_module %s not implemented", cm);
    }
    if (cpath) {
        rc = lyd_find_xpath(tree, cpath, &set);
        collect(ctx, diag, "context_path");
        if (rc || (set->count != 1)) {
            die("context_path %s must select exactly one node", cpath);
        }
        cnode = set->dnodes[0];
        ly_set_free(set, NULL);
        set = NULL;
    }

    rc = lyd_eval_xpath4(cnode, tree, cur, expr, LY_VALUE_JSON, NULL, NULL, &t, &set, &str, &num, &b);
    collect(ctx, diag, "xpath");
    set_verdict(rc);
    if (rc) {
        cJSON_AddNullToObject(resp, "result");
        return;
    }
    res = cJSON_AddObjectToObject(resp, "result");
    switch (t) {
    case LY_XPATH_NODE_SET: {
        cJSON *nodes;

        cJSON_AddStringToObject(res, "type", "node-set");
        nodes = cJSON_AddArrayToObject(res, "nodes");
        for (uint32_t i = 0; i < set->count; i++) {
            char *path = lyd_path(set->dnodes[i], LYD_PATH_STD, NULL, 0);

            cJSON_AddItemToArray(nodes, cJSON_CreateString(path));
            free(path);
        }
        break;
    }
    case LY_XPATH_STRING:
        cJSON_AddStringToObject(res, "type", "string");
        cJSON_AddStringToObject(res, "value", str);
        break;
    case LY_XPATH_NUMBER:
        cJSON_AddStringToObject(res, "type", "number");
        if (isfinite((double)num)) {
            cJSON_AddNumberToObject(res, "value", (double)num);
        } else {
            cJSON_AddStringToObject(res, "value", isnan((double)num) ? "NaN" : (num > 0 ? "Infinity" : "-Infinity"));
        }
        break;
    case LY_XPATH_BOOLEAN:
        cJSON_AddStringToObject(res, "type", "boolean");
        cJSON_AddBoolToObject(res, "value", b);
        break;
    }
    ly_set_free(set, NULL);
    free(str);
}

static void
op_diff(const cJSON *req)
{
    struct dparams p;
    struct lyd_node *a, *b, *diff = NULL;
    cJSON *diag;
    struct ly_ctx *ctx = data_prelude(req, &p, &diag);
    const char *fa, *fb;
    LY_ERR rc;

    if (!ctx) {
        return;
    }
    fa = input_of(req, "first");
    fb = input_of(req, "second");
    if (!fa || !fb) {
        die("diff needs first and second%s", NULL);
    }
    if (p.optype != LYD_TYPE_DATA_YANG) {
        die("diff supports datastore data types only%s", NULL);
    }
    if (parse_one(ctx, &p, fa, NULL, &a, diag, "first") | parse_one(ctx, &p, fb, NULL, &b, diag, "second")) {
        cJSON_AddStringToObject(resp, "verdict", "data-error");
        return;
    }
    rc = lyd_diff_siblings(a, b, flags_of(req, "diff_options", diff_flags), &diff);
    collect(ctx, diag, "diff");
    set_verdict(rc);
    if (diff) {
        cJSON_AddItemToObject(resp, "diff", print_tree(diff, LYD_PRINT_WD_ALL, 1));
    } else {
        cJSON_AddNullToObject(resp, "diff");
    }
}

static void
op_schema(const cJSON *req)
{
    int ok;

    build_ctx(req, &ok, 1);
    cJSON_AddStringToObject(resp, "verdict", ok ? "valid" : "invalid");
}

int
main(void)
{
    size_t cap = 1 << 16, len = 0, n;
    char *buf = malloc(cap), *out;
    cJSON *req;
    const char *op, *dir;

    while ((n = fread(buf + len, 1, cap - len - 1, stdin)) > 0) {
        len += n;
        if (len + 1 == cap) {
            buf = realloc(buf, cap *= 2);
        }
    }
    buf[len] = '\0';

    ly_log_options(LY_LOSTORE);
    ly_log_level(LY_LLWRN);

    resp = cJSON_CreateObject();
    cJSON_AddStringToObject(resp, "libyang", ly_version_proj_str());
    if (!(req = cJSON_Parse(buf))) {
        die("request is not valid JSON%s", NULL);
    }
    op = str_of(req, "op");
    cJSON_AddStringToObject(resp, "op", op ? op : "");
    if ((dir = str_of(req, "base_dir")) && chdir(dir)) {
        die("cannot chdir to %s", dir);
    }

    if (!op) {
        die("missing op%s", NULL);
    } else if (!strcmp(op, "schema")) {
        op_schema(req);
    } else if (!strcmp(op, "data")) {
        op_data(req);
    } else if (!strcmp(op, "xpath")) {
        op_xpath(req);
    } else if (!strcmp(op, "diff")) {
        op_diff(req);
    } else {
        die("unknown op %s", op);
    }

    out = cJSON_Print(resp);
    printf("%s\n", out);
    return 0;
}
