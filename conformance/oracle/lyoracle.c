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

#include <inttypes.h>
#include <math.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/utsname.h>
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
    /* no libyang flag: libyang always compiles patterns with PCRE2; the Go engine sets
     * Options.PatternCompat (D-0031) */
    {"pattern_compat", 0},
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

/* protocol 2: the only knob for unknown data (parse_options may not carry strict/opaq) */
static const struct flag unknown_modes[] = {
    {"reject", LYD_PARSE_STRICT},
    {"skip", 0},
    {"opaque", LYD_PARSE_OPAQ},
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

/* op data: printer flags added to the with-defaults mode for `tree` and `subtree` */
static const struct flag print_flags[] = {
    {"shrink", LYD_PRINT_SHRINK}, {"empty_leaf_list", LYD_PRINT_EMPTY_LEAF_LIST}, {"empty_cont", LYD_PRINT_EMPTY_CONT}, {"json_no_nested_prefix", LYD_PRINT_JSON_NO_NESTED_PREFIX},
    {NULL, 0}
};

/* sequence step dup: LYD_DUP_* (NO_EXT and WITH_PRIV have nothing to act on) */
static const struct flag dup_flags[] = {
    {"recursive", LYD_DUP_RECURSIVE},
    {"no_meta", LYD_DUP_NO_META},
    {"with_parents", LYD_DUP_WITH_PARENTS},
    {"with_flags", LYD_DUP_WITH_FLAGS},
    {"no_lyds", LYD_DUP_NO_LYDS},
    {NULL, 0}
};

/* sequence edits insert_term, insert_list, insert_list2: LYD_NEW_VAL_* */
static const struct flag newval_flags[] = {
    {"output", LYD_NEW_VAL_OUTPUT},
    {"store_only", LYD_NEW_VAL_STORE_ONLY},
    {"canon", LYD_NEW_VAL_CANON},
    {NULL, 0}
};

/* sequence edit insert_inner: lyd_new_inner's output argument */
static const struct flag inner_flags[] = {
    {"output", 1},
    {NULL, 0}
};

/* sequence step compare: LYD_COMPARE_* */
static const struct flag compare_flags[] = {
    {"full_recursion", LYD_COMPARE_FULL_RECURSION},
    {"defaults", LYD_COMPARE_DEFAULTS},
    {"opaq", LYD_COMPARE_OPAQ},
    {NULL, 0}
};
static LY_ERR compare_other(const struct ly_ctx *, const cJSON *, struct lyd_node **, cJSON *);
static const struct flag diff_flags[] = {
    {"defaults", LYD_DIFF_DEFAULTS},
    {"meta", LYD_DIFF_META},
    {NULL, 0}
};

static const struct flag diff_merge_flags[] = {
    {"defaults", LYD_DIFF_MERGE_DEFAULTS},
    {NULL, 0}
};

/* op atoms: LYS_FIND_* (lys_find_path_atoms reads only output) */
static const struct flag atom_flags[] = {
    {"schema", LYS_FIND_XP_SCHEMA},
    {"output", LYS_FIND_XP_OUTPUT},
    {"no_match_error", LYS_FIND_NO_MATCH_ERROR},
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

/* printf into a malloc'ed string of the exact size (no fixed buffers anywhere). */
static char *
xprintf(const char *fmt, ...)
{
    va_list ap;
    int n;
    char *s;

    va_start(ap, fmt);
    n = vsnprintf(NULL, 0, fmt, ap);
    va_end(ap);
    s = malloc(n + 1);
    va_start(ap, fmt);
    vsnprintf(s, n + 1, fmt, ap);
    va_end(ap);
    return s;
}

static void
die(const char *fmt, const char *arg)
{
    cJSON_AddStringToObject(resp, "verdict", "request-error");
    cJSON_AddStringToObject(resp, "request_error", xprintf(fmt, arg ? arg : ""));
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

    if (arr && !cJSON_IsArray(arr)) {
        die("field \"%s\" must be an array of flags", key);
    }
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
    char *fkey = xprintf("%s_file", key);
    const char *s = str_of(req, key), *p = str_of(req, fkey);

    free(fkey);
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
    char *name = xprintf("%s%s", (err & LY_EPLUGIN) ? "LY_EPLUGIN|" : "",
            base < sizeof err_names / sizeof *err_names ? err_names[base] : "?");

    cJSON_AddNumberToObject(c, "err", err);
    cJSON_AddStringToObject(c, "name", name);
    free(name);
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

/* Move the stored errors/warnings of ctx logged after mark (NULL: all) into arr, tagged with phase, and clear them. */
static int
collect_after(const struct ly_ctx *ctx, const struct ly_err_item *mark, cJSON *arr, const char *phase)
{
    const struct ly_err_item *e, *from = mark ? mark->next : ly_err_first(ctx);
    int n = 0;

    for (e = from; e; e = e->next, n++) {
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
    if (from) ly_err_clean((struct ly_ctx *)ctx, (struct ly_err_item *)from); /* items up to mark stay */
    return n;
}
#define collect(ctx, arr, phase) collect_after((ctx), NULL, (arr), (phase))
/* ---------- typed dumps (protocol 2) ---------- */

/* YANG names of LY_DATA_TYPE (ly_data_type2str has descriptive names such as "16bit integer") */
static const char *type_names[LY_DATA_TYPE_COUNT] = {
    "unknown", "binary", "uint8", "uint16", "uint32", "uint64", "string", "bits", "boolean", "decimal64",
    "empty", "enumeration", "identityref", "instance-identifier", "leafref", "union", "int8", "int16",
    "int32", "int64"
};

static const char *
kind_of(uint16_t nodetype)
{
    switch (nodetype) {
    case LYS_CONTAINER:
        return "container";
    case LYS_CHOICE:
        return "choice";
    case LYS_CASE:
        return "case";
    case LYS_LEAF:
        return "leaf";
    case LYS_LEAFLIST:
        return "leaflist";
    case LYS_LIST:
        return "list";
    case LYS_ANYDATA:
        return "anydata";
    case LYS_ANYXML:
        return "anyxml";
    case LYS_RPC:
        return "rpc";
    case LYS_ACTION:
        return "action";
    case LYS_NOTIF:
        return "notif";
    case LYS_INPUT:
        return "input";
    case LYS_OUTPUT:
        return "output";
    }
    return "?";
}

static void
add_path(cJSON *o, const char *key, char *path)
{
    add_opt_str(o, key, path);
    free(path);
}

/* {"type": <base>, "typedef": <nearest typedef name or null>} */
static void
add_type_ref(cJSON *o, const struct lysc_type *t)
{
    cJSON_AddStringToObject(o, "type", type_names[t->basetype]);
    add_opt_str(o, "typedef", t->name);
}

/*
 * The union member that stored the value. libyang keeps only the stored realtype (a leafref member
 * stores its target's type), so map it back the way lyplg_type_sort_union() does: the first member
 * whose type, or leafref realtype, is that realtype. Unions are flattened at compile time.
 */
static cJSON *
union_member_json(const struct lyd_value *v)
{
    struct lysc_type **types = ((const struct lysc_type_union *)v->realtype)->types;
    const struct lysc_type *stored = v->subvalue->value.realtype;
    cJSON *o = cJSON_CreateObject();
    LY_ARRAY_COUNT_TYPE u;

    LY_ARRAY_FOR(types, u) {
        const struct lysc_type *t = types[u];

        if (t->basetype == LY_TYPE_LEAFREF) {
            t = ((const struct lysc_type_leafref *)t)->realtype;
        }
        if (t == stored) {
            cJSON_AddNumberToObject(o, "index", u);
            add_type_ref(o, types[u]);
            add_type_ref(cJSON_AddObjectToObject(o, "realtype"), stored);
            return o;
        }
    }
    /* not expected: report the stored type only */
    cJSON_AddNullToObject(o, "index");
    add_type_ref(o, stored);
    add_type_ref(cJSON_AddObjectToObject(o, "realtype"), stored);
    return o;
}

static cJSON *
value_json(const struct lyd_node *n)
{
    const struct lyd_value *v = &((const struct lyd_node_term *)n)->value;
    cJSON *o = cJSON_CreateObject();

    add_opt_str(o, "canonical", lyd_get_value(n));
    add_type_ref(o, v->realtype);
    if (v->realtype->basetype == LY_TYPE_UNION) {
        cJSON_AddItemToObject(o, "union_member", union_member_json(v));
    } else {
        cJSON_AddNullToObject(o, "union_member");
    }
    return o;
}

/* Value/node hints of an opaque node (LYD_VALHINT_*, LYD_NODEHINT_*): the JSON type it was given as. */
static cJSON *
hints_json(uint32_t hints)
{
    static const struct flag names[] = {
        {"string", LYD_VALHINT_STRING}, {"decnum", LYD_VALHINT_DECNUM}, {"octnum", LYD_VALHINT_OCTNUM},
        {"hexnum", LYD_VALHINT_HEXNUM}, {"num64", LYD_VALHINT_NUM64}, {"boolean", LYD_VALHINT_BOOLEAN},
        {"empty", LYD_VALHINT_EMPTY}, {"string_datatypes", LYD_VALHINT_STRING_DATATYPES},
        {"list", LYD_NODEHINT_LIST}, {"leaflist", LYD_NODEHINT_LEAFLIST},
        {"container", LYD_NODEHINT_CONTAINER}, {NULL, 0}
    };
    cJSON *a = cJSON_CreateArray();

    for (int i = 0; names[i].name; i++) {
        if (hints & names[i].val) {
            cJSON_AddItemToArray(a, cJSON_CreateString(names[i].name));
        }
    }
    return a;
}

static void
add_meta_item(cJSON *arr, const char *module, const char *name, const char *value)
{
    cJSON *m = cJSON_CreateObject();

    add_opt_str(m, "module", module);
    cJSON_AddStringToObject(m, "name", name);
    add_opt_str(m, "value", value);
    cJSON_AddItemToArray(arr, m);
}

/*
 * Identity of an opaque node as libyang stores it (struct ly_opaq_name): the format decides whether
 * the union holds the XML namespace or the (inherited) JSON module name. lyd_path() shows neither
 * for XML, so without this two namespaces would dump identically.
 */
static cJSON *
opaq_name_json(const struct ly_opaq_name *name, LY_VALUE_FORMAT format)
{
    cJSON *o = cJSON_CreateObject();
    int xml = format == LY_VALUE_XML;

    cJSON_AddStringToObject(o, "name", name->name);
    add_opt_str(o, "prefix", name->prefix);
    cJSON_AddStringToObject(o, "format", xml ? "xml" : (format == LY_VALUE_JSON ? "json" : "other"));
    add_opt_str(o, "namespace", xml ? name->module_ns : NULL);
    add_opt_str(o, "module", xml ? NULL : name->module_name);
    return o;
}

/* Append n and its following siblings, each followed by its subtree (pre-order). */
static void
typed_add(cJSON *arr, const struct lyd_node *n)
{
    for ( ; n; n = n->next) {
        cJSON *o = cJSON_CreateObject(), *f, *meta;
        uint16_t nt = n->schema ? n->schema->nodetype : 0;

        cJSON_AddItemToArray(arr, o);
        add_path(o, "path", lyd_path(n, LYD_PATH_STD, NULL, 0));
        add_path(o, "schema", n->schema ? lysc_path(n->schema, LYSC_PATH_LOG, NULL, 0) : NULL);
        cJSON_AddStringToObject(o, "kind", n->schema ? kind_of(nt) : "opaque");
        f = cJSON_AddObjectToObject(o, "flags");
        cJSON_AddBoolToObject(f, "default", n->flags & LYD_DEFAULT);
        cJSON_AddBoolToObject(f, "when_true", n->flags & LYD_WHEN_TRUE);
        cJSON_AddBoolToObject(f, "new", n->flags & LYD_NEW);

        meta = cJSON_CreateArray();
        if (nt & LYD_NODE_TERM) {
            cJSON_AddItemToObject(o, "value", value_json(n));
        } else if (!n->schema) {
            /* opaque: the original text, no type; attributes go to meta */
            const struct lyd_node_opaq *q = (const struct lyd_node_opaq *)n;
            cJSON *v = cJSON_AddObjectToObject(o, "value");

            add_opt_str(v, "canonical", q->value);
            cJSON_AddNullToObject(v, "type");
            cJSON_AddNullToObject(v, "typedef");
            cJSON_AddNullToObject(v, "union_member");
            cJSON_AddItemToObject(v, "hints", hints_json(q->hints));
            cJSON_AddItemToObject(o, "opaque", opaq_name_json(&q->name, q->format));
            for (const struct lyd_attr *a = q->attr; a; a = a->next) {
                /* module_ns and module_name share a union: the namespace for XML */
                add_meta_item(meta, a->name.module_name, a->name.name, a->value);
            }
        } else {
            cJSON_AddNullToObject(o, "value");
        }
        if (n->schema) {
            cJSON_AddNullToObject(o, "opaque");
        }
        for (const struct lyd_meta *m = n->meta; m; m = m->next) {
            if (lyd_meta_is_internal(m)) {
                continue;   /* e.g. yang:lyds_tree, never printed by libyang either */
            }
            add_meta_item(meta, m->annotation ? m->annotation->module->name : NULL, m->name, lyd_get_meta_value(m));
        }
        cJSON_AddItemToObject(o, "meta", meta);

        if (nt & LYD_NODE_ANY) {
            const struct lyd_node_any *a = (const struct lyd_node_any *)n;
            cJSON *any = cJSON_AddObjectToObject(o, "any");
            char *s = NULL;

            add_opt_str(any, "value_type", a->child ? "datatree" : (a->value ? "string" : NULL));
            lyd_any_value_str(n, LYD_JSON, &s);
            add_opt_str(any, "text", s);
            free(s);
        } else {
            cJSON_AddNullToObject(o, "any");
        }
        /* anydata/anyxml payload trees too: their (opaque) nodes keep namespaces the JSON text loses */
        typed_add(arr, lyd_child_any(n));
    }
}

/* lyd_path(LYD_PATH_STD) of each node of a leafref links record array */
static cJSON *
link_paths(const struct lyd_node_term **nodes)
{
    cJSON *arr = cJSON_CreateArray();
    LY_ARRAY_COUNT_TYPE u;

    LY_ARRAY_FOR(nodes, u) {
        char *path = lyd_path(&nodes[u]->node, LYD_PATH_STD, NULL, 0);

        cJSON_AddItemToArray(arr, cJSON_CreateString(path));
        free(path);
    }
    return arr;
}

/* The leafref links records (lyd_leafref_get_links) of the term nodes of the tree, in DFS order. */
static cJSON *
links_json(const struct lyd_node *tree)
{
    cJSON *arr = cJSON_CreateArray();
    const struct lyd_node *top, *n;
    const struct lyd_leafref_links_rec *rec;

    LY_LIST_FOR(tree, top) {
        LYD_TREE_DFS_BEGIN(top, n) {
            if (n->schema && (n->schema->nodetype & LYD_NODE_TERM) &&
                    !lyd_leafref_get_links((const struct lyd_node_term *)n, &rec)) {
                cJSON *o = cJSON_CreateObject();
                char *path = lyd_path(n, LYD_PATH_STD, NULL, 0);

                cJSON_AddStringToObject(o, "node", path);
                free(path);
                cJSON_AddItemToObject(o, "leafref_nodes", link_paths(rec->leafref_nodes));
                cJSON_AddItemToObject(o, "target_nodes", link_paths(rec->target_nodes));
                cJSON_AddItemToArray(arr, o);
            }
            LYD_TREE_DFS_END(top, n);
        }
    }
    return arr;
}

static cJSON *
typed_json(const struct lyd_node *tree)
{
    cJSON *arr = cJSON_CreateArray();

    typed_add(arr, tree);
    return arr;
}

/* One range/length bound (malloc'ed); unsigned for uint*, string and binary (see struct lysc_range). */
static char *
fmt_bound(const struct lysc_type *t, int64_t s, uint64_t u)
{
    if (t->basetype == LY_TYPE_DEC64) {
        int fd = ((const struct lysc_type_dec *)t)->fraction_digits;
        uint64_t a = s < 0 ? -(uint64_t)s : (uint64_t)s, p = 1;

        for (int i = 0; i < fd; i++) {
            p *= 10;
        }
        return xprintf("%s%" PRIu64 ".%0*" PRIu64, s < 0 ? "-" : "", a / p, fd, a % p);
    } else if (t->basetype < LY_TYPE_DEC64) {
        return xprintf("%" PRIu64, u);
    }
    return xprintf("%" PRId64, s);
}

/* Compiled range/length as "a..b | c" (numbers, not the original text). */
static void
add_range(cJSON *o, const char *key, const struct lysc_type *t, const struct lysc_range *r)
{
    char *out = xprintf("%s", ""), *lo, *hi, *next;
    LY_ARRAY_COUNT_TYPE i;

    if (!r) {
        cJSON_AddNullToObject(o, key);
        free(out);
        return;
    }
    LY_ARRAY_FOR(r->parts, i) {
        lo = fmt_bound(t, r->parts[i].min_64, r->parts[i].min_u64);
        hi = fmt_bound(t, r->parts[i].max_64, r->parts[i].max_u64);
        if (strcmp(lo, hi)) {
            next = xprintf("%s%s%s..%s", out, i ? " | " : "", lo, hi);
        } else {
            next = xprintf("%s%s%s", out, i ? " | " : "", lo);
        }
        free(out);
        free(lo);
        free(hi);
        out = next;
    }
    cJSON_AddStringToObject(o, key, out);
    free(out);
}

static cJSON *
ident_json(const struct lysc_ident *id)
{
    char *name = xprintf("%s:%s", id->module->name, id->name);
    cJSON *s = cJSON_CreateString(name);

    free(name);
    return s;
}

/*
 * Compiled type. target is the resolved target of a leafref type (or NULL); utargets the targets of
 * the leafref members of a union, in member order (or NULL).
 */
static cJSON *
type_json(const struct lysc_type *t, const struct lysc_node *target, const struct ly_set *utargets)
{
    cJSON *o = cJSON_CreateObject(), *a;
    const struct lysc_range *range = NULL, *length = NULL;
    LY_ARRAY_COUNT_TYPE i;

    cJSON_AddStringToObject(o, "base", type_names[t->basetype]);
    if (t->name) {
        cJSON_AddItemToArray(cJSON_AddArrayToObject(o, "typedefs"), cJSON_CreateString(t->name));
    } else {
        cJSON_AddNullToObject(o, "typedefs");
    }
    switch (t->basetype) {
    case LY_TYPE_INT8: case LY_TYPE_INT16: case LY_TYPE_INT32: case LY_TYPE_INT64:
    case LY_TYPE_UINT8: case LY_TYPE_UINT16: case LY_TYPE_UINT32: case LY_TYPE_UINT64:
        range = ((const struct lysc_type_num *)t)->range;
        break;
    case LY_TYPE_DEC64:
        range = ((const struct lysc_type_dec *)t)->range;
        break;
    case LY_TYPE_STRING:
        length = ((const struct lysc_type_str *)t)->length;
        break;
    case LY_TYPE_BINARY:
        length = ((const struct lysc_type_bin *)t)->length;
        break;
    default:
        break;
    }
    add_range(o, "range", t, range);
    add_range(o, "length", t, length);

    if ((t->basetype == LY_TYPE_STRING) && ((const struct lysc_type_str *)t)->patterns) {
        struct lysc_pattern **pats = ((const struct lysc_type_str *)t)->patterns;

        a = cJSON_AddArrayToObject(o, "patterns");
        LY_ARRAY_FOR(pats, i) {
            cJSON *p = cJSON_CreateObject();

            cJSON_AddStringToObject(p, "expr", pats[i]->expr);
            cJSON_AddBoolToObject(p, "invert", pats[i]->inverted);
            cJSON_AddItemToArray(a, p);
        }
    } else {
        cJSON_AddNullToObject(o, "patterns");
    }

    if (t->basetype == LY_TYPE_DEC64) {
        cJSON_AddNumberToObject(o, "fraction_digits", ((const struct lysc_type_dec *)t)->fraction_digits);
    } else {
        cJSON_AddNullToObject(o, "fraction_digits");
    }

    cJSON_AddNullToObject(o, "enums");
    cJSON_AddNullToObject(o, "bits");
    if ((t->basetype == LY_TYPE_ENUM) || (t->basetype == LY_TYPE_BITS)) {
        int is_enum = t->basetype == LY_TYPE_ENUM;
        const struct lysc_type_bitenum_item *items = is_enum ?
                ((const struct lysc_type_enum *)t)->enums : ((const struct lysc_type_bits *)t)->bits;

        a = cJSON_CreateArray();
        cJSON_ReplaceItemInObjectCaseSensitive(o, is_enum ? "enums" : "bits", a);
        LY_ARRAY_FOR(items, i) {
            cJSON *e = cJSON_CreateObject();

            cJSON_AddStringToObject(e, "name", items[i].name);
            if (is_enum) {
                cJSON_AddNumberToObject(e, "value", items[i].value);
            } else {
                cJSON_AddNumberToObject(e, "position", items[i].position);
            }
            cJSON_AddItemToArray(a, e);
        }
    }

    if (t->basetype == LY_TYPE_IDENT) {
        struct lysc_ident **bases = ((const struct lysc_type_identityref *)t)->bases;

        a = cJSON_AddArrayToObject(o, "bases");
        LY_ARRAY_FOR(bases, i) {
            cJSON_AddItemToArray(a, ident_json(bases[i]));
        }
    } else {
        cJSON_AddNullToObject(o, "bases");
    }

    if (t->basetype == LY_TYPE_LEAFREF) {
        const struct lysc_type_leafref *lr = (const struct lysc_type_leafref *)t;
        cJSON *l = cJSON_AddObjectToObject(o, "leafref");

        cJSON_AddStringToObject(l, "path", lyxp_get_expr(lr->path));
        cJSON_AddBoolToObject(l, "require_instance", lr->require_instance);
        add_path(l, "target", target ? lysc_path(target, LYSC_PATH_LOG, NULL, 0) : NULL);
    } else {
        cJSON_AddNullToObject(o, "leafref");
    }

    if (t->basetype == LY_TYPE_UNION) {
        struct lysc_type **types = ((const struct lysc_type_union *)t)->types;

        uint32_t k = 0;

        a = cJSON_AddArrayToObject(o, "union");
        LY_ARRAY_FOR(types, i) {
            const struct lysc_node *tg = NULL;

            if ((types[i]->basetype == LY_TYPE_LEAFREF) && utargets) {
                tg = utargets->snodes[k++];
            }
            cJSON_AddItemToArray(a, type_json(types[i], tg, NULL));
        }
    } else {
        cJSON_AddNullToObject(o, "union");
    }
    return o;
}

/*
 * Type of a leaf/leaf-list with its leafref target(s). lysc_node_lref_targets() lists the targets of
 * union leafref members in member order, skipping members whose target does not resolve, so they
 * are attributed per member only when the counts match (otherwise each member target is null).
 */
static cJSON *
leaf_type_json(const struct lysc_node *node)
{
    /* type is at the same offset in lysc_node_leaf and lysc_node_leaflist */
    const struct lysc_type *t = ((const struct lysc_node_leaf *)node)->type;
    struct lysc_type **types;
    struct ly_set *set = NULL;
    uint32_t nlref = 0;
    LY_ARRAY_COUNT_TYPE i;
    cJSON *o;

    if (t->basetype == LY_TYPE_LEAFREF) {
        return type_json(t, lysc_node_lref_target(node), NULL);
    } else if (t->basetype != LY_TYPE_UNION) {
        return type_json(t, NULL, NULL);
    }
    types = ((const struct lysc_type_union *)t)->types;
    LY_ARRAY_FOR(types, i) {
        nlref += types[i]->basetype == LY_TYPE_LEAFREF;
    }
    if (nlref && !lysc_node_lref_targets(node, &set) && (set->count != nlref)) {
        ly_set_free(set, NULL);
        set = NULL;
    }
    o = type_json(t, NULL, set);
    ly_set_free(set, NULL);
    return o;
}

/*
 * Canonical form of a schema default. LY_EINCOMPLETE (leafref, instance-identifier: the instance
 * check needs data) still yields the canonical value; any other failure gives the original text.
 */
static cJSON *
dflt_json(const struct lysc_node *node, const struct lysc_value *v)
{
    const char *canon = NULL;
    LY_ERR rc = lyd_value_validate_dflt(node, v->str, v->prefixes, NULL, NULL, &canon);
    cJSON *s;

    if ((rc && (rc != LY_EINCOMPLETE)) || !canon) {
        ly_err_clean(node->module->ctx, NULL);
        return cJSON_CreateString(v->str);
    }
    s = cJSON_CreateString(canon);
    lydict_remove(node->module->ctx, canon);
    return s;
}

static LY_ERR
snode_cb(struct lysc_node *node, void *data, ly_bool *dfs_continue)
{
    cJSON *o = cJSON_CreateObject(), *a;
    uint16_t nt = node->nodetype, fl = node->flags;
    struct lysc_when **whens = lysc_node_when(node);
    struct lysc_must *musts = lysc_node_musts(node);
    LY_ARRAY_COUNT_TYPE i;

    (void)dfs_continue;
    cJSON_AddItemToArray((cJSON *)data, o);
    add_path(o, "path", lysc_path(node, LYSC_PATH_LOG, NULL, 0));
    cJSON_AddStringToObject(o, "nodetype", kind_of(nt));
    cJSON_AddStringToObject(o, "module", node->module->name);
    if (fl & (LYS_CONFIG_W | LYS_CONFIG_R)) {
        cJSON_AddBoolToObject(o, "config", fl & LYS_CONFIG_W);
    } else {
        cJSON_AddNullToObject(o, "config");
    }
    add_opt_str(o, "status", (fl & LYS_STATUS_CURR) ? "current" : (fl & LYS_STATUS_DEPRC) ? "deprecated" :
            (fl & LYS_STATUS_OBSLT) ? "obsolete" : NULL);
    if (nt & (LYS_LEAF | LYS_LEAFLIST | LYS_LIST | LYS_CHOICE | LYS_ANYDATA | LYS_CONTAINER)) {
        cJSON_AddBoolToObject(o, "mandatory", fl & LYS_MAND_TRUE);
    } else {
        cJSON_AddNullToObject(o, "mandatory");
    }
    if (nt == LYS_CONTAINER) {
        cJSON_AddBoolToObject(o, "presence", fl & LYS_PRESENCE);
    } else {
        cJSON_AddNullToObject(o, "presence");
    }
    add_opt_str(o, "ordered_by", !(nt & (LYS_LIST | LYS_LEAFLIST)) ? NULL : (fl & LYS_ORDBY_USER) ? "user" : "system");

    if (nt == LYS_LIST) {
        /* keys are the first children */
        a = cJSON_AddArrayToObject(o, "keys");
        for (const struct lysc_node *c = lysc_node_child(node); c && lysc_is_key(c); c = c->next) {
            cJSON_AddItemToArray(a, cJSON_CreateString(c->name));
        }
    } else {
        cJSON_AddNullToObject(o, "keys");
    }
    if (nt & (LYS_LIST | LYS_LEAFLIST)) {
        uint32_t min = nt == LYS_LIST ? ((struct lysc_node_list *)node)->min : ((struct lysc_node_leaflist *)node)->min;
        uint32_t max = nt == LYS_LIST ? ((struct lysc_node_list *)node)->max : ((struct lysc_node_leaflist *)node)->max;

        cJSON_AddNumberToObject(o, "min_elements", min);
        if (max == UINT32_MAX) {
            cJSON_AddNullToObject(o, "max_elements");   /* unbounded */
        } else {
            cJSON_AddNumberToObject(o, "max_elements", max);
        }
    } else {
        cJSON_AddNullToObject(o, "min_elements");
        cJSON_AddNullToObject(o, "max_elements");
    }

    if ((nt == LYS_LEAF) && ((struct lysc_node_leaf *)node)->dflt.str) {
        a = cJSON_AddArrayToObject(o, "defaults");
        cJSON_AddItemToArray(a, dflt_json(node, &((struct lysc_node_leaf *)node)->dflt));
    } else if ((nt == LYS_LEAFLIST) && ((struct lysc_node_leaflist *)node)->dflts) {
        struct lysc_value *d = ((struct lysc_node_leaflist *)node)->dflts;

        a = cJSON_AddArrayToObject(o, "defaults");
        LY_ARRAY_FOR(d, i) {
            cJSON_AddItemToArray(a, dflt_json(node, &d[i]));
        }
    } else if ((nt == LYS_CHOICE) && ((struct lysc_node_choice *)node)->dflt) {
        /* the default case name */
        a = cJSON_AddArrayToObject(o, "defaults");
        cJSON_AddItemToArray(a, cJSON_CreateString(((struct lysc_node_choice *)node)->dflt->name));
    } else {
        cJSON_AddNullToObject(o, "defaults");
    }

    if (nt & (LYS_LEAF | LYS_LEAFLIST)) {
        /* type is at the same offset in lysc_node_leaf and lysc_node_leaflist */
        cJSON_AddItemToObject(o, "type", leaf_type_json(node));
    } else {
        cJSON_AddNullToObject(o, "type");
    }

    if (whens) {
        a = cJSON_AddArrayToObject(o, "when");
        LY_ARRAY_FOR(whens, i) {
            cJSON *w = cJSON_CreateObject();
            const char *mod = NULL;
            LY_ARRAY_COUNT_TYPE j;

            /* libyang stores the defining module as the prefix-less entry */
            LY_ARRAY_FOR(whens[i]->prefixes, j) {
                if (!whens[i]->prefixes[j].prefix) {
                    mod = whens[i]->prefixes[j].mod->name;
                }
            }
            cJSON_AddStringToObject(w, "expr", lyxp_get_expr(whens[i]->cond));
            add_path(w, "context", whens[i]->context ? lysc_path(whens[i]->context, LYSC_PATH_LOG, NULL, 0) : NULL);
            add_opt_str(w, "module", mod);
            cJSON_AddItemToArray(a, w);
        }
    } else {
        cJSON_AddNullToObject(o, "when");
    }

    if (musts) {
        a = cJSON_AddArrayToObject(o, "musts");
        LY_ARRAY_FOR(musts, i) {
            cJSON *m = cJSON_CreateObject();

            cJSON_AddStringToObject(m, "expr", lyxp_get_expr(musts[i].cond));
            add_opt_str(m, "apptag", musts[i].eapptag);
            add_opt_str(m, "message", musts[i].emsg);
            cJSON_AddItemToArray(a, m);
        }
    } else {
        cJSON_AddNullToObject(o, "musts");
    }

    if (node->exts) {
        a = cJSON_AddArrayToObject(o, "extensions");
        LY_ARRAY_FOR(node->exts, i) {
            cJSON *e = cJSON_CreateObject();

            cJSON_AddStringToObject(e, "module", node->exts[i].def->module->name);
            cJSON_AddStringToObject(e, "name", node->exts[i].def->name);
            add_opt_str(e, "argument", node->exts[i].argument);
            cJSON_AddItemToArray(a, e);
        }
    } else {
        cJSON_AddNullToObject(o, "extensions");
    }
    return LY_SUCCESS;
}

/*
 * ext_trees: the compiled schema subtrees of the module's top-level extension instances
 * (yang-data, structure). A plugin compiles them into substatement storage; every data-def
 * storage is walked from the root of its first node, so structure's virtual top-level container
 * is included. Several substatements share one storage: each root is dumped once. Omitted when
 * no instance has a subtree.
 */
static void
dump_ext_trees(cJSON *m, const struct lys_module *mod)
{
    cJSON *a = NULL;
    LY_ARRAY_COUNT_TYPE i, j, k;

    LY_ARRAY_FOR(mod->compiled->exts, i) {
        const struct lysc_ext_instance *ext = &mod->compiled->exts[i];
        const struct lysc_node *roots[16];
        LY_ARRAY_COUNT_TYPE nroots = 0;
        cJSON *e, *nodes;

        LY_ARRAY_FOR(ext->substmts, j) {
            const struct lysc_node *n;

            if (!(ext->substmts[j].stmt & (LY_STMT_DATA_NODE_MASK | LY_STMT_USES)) || !ext->substmts[j].storage_p ||
                    !(n = *ext->substmts[j].storage_p)) {
                continue;
            }
            while (n->parent) {
                n = n->parent;
            }
            for (k = 0; (k < nroots) && (roots[k] != n); k++) {}
            if (k == nroots) {
                if (nroots == sizeof roots / sizeof *roots) {
                    die("too many subtree roots in extension instance %s", ext->def->name);
                }
                roots[nroots++] = n;
            }
        }
        if (!nroots) {
            continue;
        }
        if (!a) {
            a = cJSON_AddArrayToObject(m, "ext_trees");
        }
        e = cJSON_CreateObject();
        cJSON_AddStringToObject(e, "module", ext->def->module->name);
        cJSON_AddStringToObject(e, "name", ext->def->name);
        add_opt_str(e, "argument", ext->argument);
        nodes = cJSON_AddArrayToObject(e, "schema_tree");
        for (k = 0; k < nroots; k++) {
            for (const struct lysc_node *s = roots[k]; s; s = s->next) {
                lysc_tree_dfs_full(s, snode_cb, nodes);
            }
        }
        cJSON_AddItemToArray(a, e);
    }
}

/* schema_tree, identities, features and ext_trees of one accepted module. */
static void
dump_schema(cJSON *m, const struct lys_module *mod)
{
    const struct lys_module *other;
    const struct lysp_feature *f = NULL;
    cJSON *a;
    LY_ARRAY_COUNT_TYPE i, j, k;
    uint32_t idx;

    lysc_module_dfs_full(mod, snode_cb, cJSON_AddArrayToObject(m, "schema_tree"));

    a = cJSON_AddArrayToObject(m, "identities");
    LY_ARRAY_FOR(mod->identities, i) {
        const struct lysc_ident *id = &mod->identities[i];
        cJSON *o = cJSON_CreateObject(), *bases, *derived;

        cJSON_AddItemToObject(o, "name", ident_json(id));
        /* lysc_ident only links derived identities: find the bases by scanning the context */
        bases = cJSON_AddArrayToObject(o, "bases");
        idx = 0;
        while ((other = ly_ctx_get_module_iter(mod->ctx, &idx))) {
            LY_ARRAY_FOR(other->identities, j) {
                LY_ARRAY_FOR(other->identities[j].derived, k) {
                    if (other->identities[j].derived[k] == id) {
                        cJSON_AddItemToArray(bases, ident_json(&other->identities[j]));
                    }
                }
            }
        }
        derived = cJSON_AddArrayToObject(o, "derived");
        LY_ARRAY_FOR(id->derived, j) {
            cJSON_AddItemToArray(derived, ident_json(id->derived[j]));
        }
        cJSON_AddItemToArray(a, o);
    }

    a = cJSON_AddArrayToObject(m, "features");
    idx = 0;
    while ((f = lysp_feature_next(f, mod->parsed, &idx))) {
        cJSON *o = cJSON_CreateObject();

        cJSON_AddStringToObject(o, "name", f->name);
        cJSON_AddBoolToObject(o, "enabled", lys_feature_value(mod, f->name) == LY_SUCCESS);
        cJSON_AddItemToArray(a, o);
    }

    dump_ext_trees(m, mod);
}

/* ---------- schema ---------- */

/*
 * Build a context: searchdirs + implemented modules with features. Per module: "parse" phase is
 * ly_ctx_load_module() under LY_CTX_EXPLICIT_COMPILE (parse, imports/includes, features), "compile"
 * phase is the following ly_ctx_compile(). A failed compile is reverted by libyang, so later
 * modules are still tried. Returns ctx and sets *ok to 1 if every module was accepted.
 */
static struct ly_ctx *build_ctx_in(const cJSON *req, int *ok, int dump, cJSON *into);

static struct ly_ctx *
build_ctx(const cJSON *req, int *ok, int dump)
{
    return build_ctx_in(req, ok, dump, resp);
}

/* build_ctx with its "modules" and "context_diagnostics" reported into the object into */
static struct ly_ctx *
build_ctx_in(const cJSON *req, int *ok, int dump, cJSON *into)
{
    struct ly_ctx *ctx;
    const cJSON *it, *mods = cJSON_GetObjectItemCaseSensitive(req, "modules");
    cJSON *out = cJSON_AddArrayToObject(into, "modules");
    cJSON *cdiag = cJSON_AddArrayToObject(into, "context_diagnostics");
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
            if (mod) {
                dump_schema(m, mod);
            }
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
    int keep_input;         /* rpc kept with its parsed children (keep_input) */
    const char *rpc_parent; /* optional parent path in the parsed request tree */
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
    const char *t = str_of(req, "data_type"), *u = str_of(req, "unknown");
    uint32_t extra;

    memset(p, 0, sizeof *p);
    p->fmt = fmt_of(str_of(req, "format"));
    p->type = t ? t : "data-operational";
    p->popts = enum_of(u ? u : "reject", unknown_modes, "unknown policy");
    p->vopts = LYD_VALIDATE_MULTI_ERROR;
    p->optype = LYD_TYPE_DATA_YANG;
    p->parse_only = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(req, "parse_only"));
    p->keep_input = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(req, "keep_input"));
    p->rpc_parent = str_of(req, "rpc_parent");

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
    } else if (!strcmp(p->type, "rpc-netconf") || !strcmp(p->type, "notif-netconf") || !strcmp(p->type, "reply-netconf") ||
            !strcmp(p->type, "rpc-restconf") || !strcmp(p->type, "notif-restconf") || !strcmp(p->type, "reply-restconf")) {
        p->optype = (strstr(p->type, "restconf") ? LYD_TYPE_RPC_RESTCONF : LYD_TYPE_RPC_NETCONF) +
                (p->type[0] == 'n' ? 1 : p->type[2] == 'p' ? 2 : 0); /* enum lyd_type order: rpc, notif, reply */
    } else {
        die("unknown data_type %s", p->type);
    }
    if (p->parse_only) {
        p->popts |= LYD_PARSE_ONLY;
    }
    if ((p->optype >= LYD_TYPE_RPC_NETCONF) && !p->parse_only) {
        /* lyd_validate_op takes the YANG types only */
        die("data_type %s needs parse_only", p->type);
    }
    extra = flags_of(req, "parse_options", parse_flags);
    if (extra & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ)) {
        die("parse_options strict/opaq are replaced by \"unknown\"%s", NULL);
    }
    p->popts |= extra;
    p->vopts |= flags_of(req, "validate_options", validate_flags);
}

/*
 * Parse (and unless parse-only, validate) one input. Returns rc; *tree is the top-level tree
 * (NULL on failure). For "reply", rpc_src is the request whose output is being parsed.
 */
/* the request's tree after a failed reply parsed into it (what lyd_parse_op's cleanup left) */
static cJSON *req_typed;

/* the operation of an envelope parse (lyd_parse_op's op), reported as op_typed by op_data */
static struct lyd_node *env_op;

static LY_ERR
parse_one(struct ly_ctx *ctx, const struct dparams *p, const char *data, const char *rpc_src,
        struct lyd_node **tree, cJSON *diag, const char *phase)
{
    struct ly_in *in = NULL;
    struct lyd_node *op = NULL, *envp = NULL;
    LY_ERR rc;

    *tree = NULL;
    if (p->rpc_parent && (!rpc_src || !p->keep_input || (p->optype != LYD_TYPE_RPC_YANG))) {
        die("rpc_parent requires data_type rpc, rpc, and keep_input true%s", NULL);
    }
    ly_in_new_memory(data, &in);
    if (p->optype == LYD_TYPE_DATA_YANG) {
        rc = lyd_parse_data(ctx, NULL, in, p->fmt, p->popts, p->popts & LYD_PARSE_ONLY ? 0 : p->vopts, tree);
        goto done;
    }

    /* operations: lyd_parse_op accepts only STRICT/OPAQ */
    if (((p->optype == LYD_TYPE_REPLY_YANG) || (p->optype == LYD_TYPE_RPC_YANG) || (p->optype == LYD_TYPE_REPLY_NETCONF) ||
            (p->optype == LYD_TYPE_RPC_RESTCONF) || (p->optype == LYD_TYPE_REPLY_RESTCONF)) && rpc_src) {
        struct ly_in *rin = NULL;
        ly_in_new_memory(rpc_src, &rin);
        rc = lyd_parse_op(ctx, NULL, rin, p->fmt, LYD_TYPE_RPC_YANG, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ),
                tree, &op);
        ly_in_free(rin, 0);
        collect(ctx, diag, "rpc");
        if (rc) {
            goto done;
        }
        if (!p->keep_input) {
            lyd_free_siblings(lyd_child(op));
        }
        /* A selected request ancestor allows cleanup probes under a keyed list parent. */
        struct lyd_node *parent = op;
        if (p->rpc_parent && (lyd_find_path(*tree, p->rpc_parent, 0, &parent) || !parent)) {
            die("rpc_parent %s not found", p->rpc_parent);
        }
        /* the reply, or (data_type rpc) the input again, parsed under the request */
        rc = lyd_parse_op(ctx, parent, in, p->fmt, p->optype, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ),
                (p->optype > LYD_TYPE_REPLY_YANG) ? &envp : NULL, NULL);
        if (rc) {
            req_typed = typed_json(*tree); lyd_free_all(*tree); *tree = envp; envp = NULL; /* report a retained envelope */
        } else if (envp) { env_op = op; *tree = envp; } /* *tree is the envelope, the request with the message op_typed */
    } else {
        rc = lyd_parse_op(ctx, NULL, in, p->fmt, p->optype, p->popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ), tree, &op);
        if (!rc && (p->optype >= LYD_TYPE_RPC_NETCONF)) {
            env_op = op; /* *tree is the envelope, the operation a tree of its own */
        }
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
    if (rc && (p->optype < LYD_TYPE_RPC_NETCONF)) {
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
    struct lyd_node *tree, *sub;
    cJSON *diag;
    struct ly_ctx *ctx = data_prelude(req, &p, &diag);
    const char *data, *subpath = str_of(req, "print_subtree");
    uint32_t popts = wd_of(req) | flags_of(req, "print_options", print_flags);
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
        cJSON_AddItemToObject(resp, "tree", print_tree(tree, popts, p.optype == LYD_TYPE_DATA_YANG));
        cJSON_AddItemToObject(resp, "typed", typed_json(tree));
        if (env_op) {
            struct lyd_node *top = env_op;

            while (top->parent) {
                top = lyd_parent(top);
            }
            cJSON_AddItemToObject(resp, "op_typed", typed_json(top));
        }
        if (subpath) {
            /* lyd_print_tree: the node and its descendants, without its siblings */
            sub = NULL;
            if (lyd_find_path(tree, subpath, 0, &sub) || !sub) {
                die("print_subtree \"%s\" not found", subpath);
            }
            cJSON_AddItemToObject(resp, "subtree", print_tree(sub, popts, 0));
        }
    } else {
        cJSON_AddNullToObject(resp, "tree");
    }
    if (req_typed) {
        cJSON_AddItemToObject(resp, "request_typed", req_typed);
    }
}

/* struct lyxml_ns of libyang v5.8.6 src/xml.h (not installed): an XML namespace declaration, the
 * prefix_data of an LY_VALUE_XML expression is a set of them (lyxml_ns_get searches it from the end) */
struct oracle_xml_ns {
    char *prefix; /* NULL for the default namespace */
    char *uri;
    uint32_t depth;
};

static void
op_xpath(const cJSON *req)
{
    struct dparams p;
    struct lyd_node *tree, *cnode = NULL;
    cJSON *diag, *res;
    struct ly_ctx *ctx = data_prelude(req, &p, &diag);
    const char *expr = str_of(req, "xpath"), *cpath = str_of(req, "context_path"), *cm = str_of(req, "cur_module");
    const struct lys_module *cur = NULL;
    struct ly_set *set = NULL, *nsset = NULL;
    struct oracle_xml_ns **owned = NULL; /* nsset may gain borrowed module pointers (ly_xml_get_prefix) */
    uint32_t nowned = 0;
    const char *fmt = str_of(req, "xpath_format");
    LY_VALUE_FORMAT xformat = LY_VALUE_JSON;
    void *prefix_data = NULL;
    const cJSON *jns = cJSON_GetObjectItemCaseSensitive(req, "namespaces"), *n;
    char *str = NULL;
    long double num = 0;
    ly_bool b = 0;
    LY_XPATH_TYPE t;
    LY_ERR rc;
    struct lyxp_var *vars = NULL;
    const cJSON *jv = cJSON_GetObjectItemCaseSensitive(req, "vars"), *v;

    if (!ctx) {
        return;
    }
    if (!expr) {
        die("missing xpath%s", NULL);
    }
    if (jv && !cJSON_IsObject(jv)) {
        die("vars must be an object%s", NULL);
    }
    cJSON_ArrayForEach(v, jv) {
        if (!cJSON_IsString(v)) {
            die("vars value of %s must be a string", v->string);
        }
        if (lyxp_vars_set(&vars, v->string, v->valuestring)) {
            die("lyxp_vars_set %s failed", v->string);
        }
    }
    rc = parse_one(ctx, &p, input_of(req, "data"), NULL, &tree, diag, "data");
    if (rc || !tree) {
        cJSON_AddStringToObject(resp, "verdict", rc ? "data-error" : "empty-tree");
        return;
    }
    if (cm && !(cur = ly_ctx_get_module_implemented(ctx, cm))) {
        die("cur_module %s not implemented", cm);
    }
    /* the expression's format and prefix_data: XML namespace declarations ("namespaces", in order;
     * an empty set without them, libyang dereferences it), or the parsed cur_module (schema) */
    if (jns && !cJSON_IsArray(jns)) {
        die("namespaces must be a list%s", NULL);
    }
    if (fmt && !strcmp(fmt, "xml")) {
        xformat = LY_VALUE_XML;
        ly_set_new(&nsset);
        cJSON_ArrayForEach(n, jns) {
            struct oracle_xml_ns *ns = calloc(1, sizeof *ns);
            const char *pfx = str_of(n, "prefix"), *uri = str_of(n, "uri");

            if (!uri) {
                die("namespaces need a uri%s", NULL);
            }
            ns->prefix = (pfx && pfx[0]) ? strdup(pfx) : NULL;
            ns->uri = strdup(uri);
            ly_set_add(nsset, ns, 1, NULL);
            owned = realloc(owned, (nowned + 1) * sizeof *owned);
            owned[nowned++] = ns;
        }
        prefix_data = nsset;
    } else if (fmt && !strcmp(fmt, "schema")) {
        xformat = LY_VALUE_SCHEMA;
        prefix_data = cur ? cur->parsed : NULL;
    } else if (fmt && strcmp(fmt, "json")) {
        die("unknown xpath_format %s", fmt);
    }
    if (jns && !nsset) {
        die("namespaces need xpath_format xml%s", NULL);
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

    rc = lyd_eval_xpath4(cnode, tree, cur, expr, xformat, prefix_data, vars, &t, &set, &str, &num, &b);
    lyxp_vars_free(vars);
    /* name() in an XML expression makes ly_xml_get_prefix() treat the namespace set as a set of
     * modules and append module pointers to it: free only the declarations this function made */
    for (uint32_t i = 0; i < nowned; i++) {
        free(owned[i]->prefix);
        free(owned[i]->uri);
        free(owned[i]);
    }
    free(owned);
    ly_set_free(nsset, NULL);
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
        cJSON_AddItemToObject(resp, "typed", typed_json(diff));
    } else {
        cJSON_AddNullToObject(resp, "diff");
    }
}

/* ---------- sequence: steps on one retained tree ---------- */

/* request-error unless every key of o is one of the space-separated tokens in allowed */
static void
keys_only(const cJSON *o, const char *allowed, const char *what)
{
    const cJSON *it;

    cJSON_ArrayForEach(it, o) {
        const char *k = it->string, *tok = allowed;
        size_t n = strlen(k), len;
        int found = 0;

        /* compare whole tokens: "data" is not "data_type", "" matches nothing */
        while (!found && *tok) {
            len = strcspn(tok, " ");
            found = n && (len == n) && !strncmp(tok, k, n);
            tok += len + (tok[len] == ' ');
        }
        if (!found) {
            die(what, k);
        }
    }
}

/*
 * Check one step's request fields (request-error on anything malformed or unknown keys), so a
 * sequence never stops at a failing step before a later malformed one is noticed.
 */
static const char *
check_step(const struct ly_ctx *ctx, const cJSON *step)
{
    const char *what = str_of(step, "do");
    struct dparams p;

    if (!cJSON_IsObject(step) || !what) {
        die("step without \"do\"%s", NULL);
    }
    keys_only(step, !strcmp(what, "parse") ? "do format data_type data data_file unknown parse_only "
            "parse_options validate_options rpc keep_input parent" : !strcmp(what, "validate") ? "do data_type validate_options module final_only" :
            !strcmp(what, "edit") ? "do merge merge_file merge_self set delete insert_term insert_inner insert_list insert_list2 "
            "insert_opaq new_meta free_meta format data_type unknown parse_options merge_into" :
            !strcmp(what, "change_term") ? "do node node_xpath child value canon" :
            !strcmp(what, "insert") ? "do node parent format data_type data data_file unknown parse_only parse_options "
            "validate_options detached target" :
            !strcmp(what, "detached_dump") ? "do with_defaults child" : !strcmp(what, "dump") ? "do with_defaults" : !strcmp(what, "print_xml") ? "do" : !strcmp(what, "any_direct") ? "do case target" : !strcmp(what, "dup") ? "do node node_xpath parent options siblings target source detached" :
            !strcmp(what, "compare") ? "do format data_type data data_file unknown parse_only parse_options "
            "validate_options first second options insert_opaq self target" :
            !strcmp(what, "diff") ? "do format data_type data data_file unknown parse_only parse_options validate_options "
            "rpc keep_input node data_node single options merge merge_options target detached" :
            !strcmp(what, "diff_parse") ? "do format data_type data data_file unknown parse_options" :
            !strcmp(what, "diff_merge") ? "do format data_type data data_file unknown parse_options options module "
            "src_node parent detached_parent" :
            !strcmp(what, "diff_apply") ? "do module" : !strcmp(what, "trim") ? "do xpath vars" :
            !strcmp(what, "xpath") ? "do xpath vars detached node" :
            !strcmp(what, "unlink") ? "do node diff_node" : !strcmp(what, "find_meta") ? "do node name" :
            !strcmp(what, "links") ? "do detached" : "do",
            "unknown key \"%s\" in a sequence step");
    if (!strcmp(what, "edit")) {
        /* the parse options belong to merge only */
        keys_only(step, cJSON_GetObjectItemCaseSensitive(step, "merge_self") ? "do merge_self" :
                cJSON_GetObjectItemCaseSensitive(step, "set") ? "do set" :
                cJSON_GetObjectItemCaseSensitive(step, "delete") ? "do delete" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_term") ? "do insert_term" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_inner") ? "do insert_inner" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_list") ? "do insert_list" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_list2") ? "do insert_list2" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_opaq") ? "do insert_opaq" :
                cJSON_GetObjectItemCaseSensitive(step, "new_meta") ? "do new_meta" :
                cJSON_GetObjectItemCaseSensitive(step, "free_meta") ? "do free_meta" :
                "do merge merge_file merge_into format data_type unknown parse_options",
                "key \"%s\" not allowed in this edit step");
    }
    if (!strcmp(what, "parse")) {
        dparams_of(step, &p);
        if ((p.optype != LYD_TYPE_DATA_YANG) && !p.parse_only) {
            /* an operation is parsed only: ValidateOp is a step of its own (none yet) */
            die("sequence parses operation data with parse_only only%s", NULL);
        }
        if (input_of(step, "rpc") && (p.optype != LYD_TYPE_REPLY_YANG)) {
            die("parse step: rpc only with data_type reply%s", NULL);
        }
        if (!input_of(step, "data")) {
            die("parse step needs data%s", NULL);
        }
    } else if (!strcmp(what, "validate")) {
        const cJSON *fo = cJSON_GetObjectItemCaseSensitive(step, "final_only");
        const char *mname = str_of(step, "module"); /* request-error unless a string */

        dparams_of(step, &p);
        if (fo && !cJSON_IsBool(fo)) {
            die("final_only must be a boolean%s", NULL);
        }
        if (cJSON_IsTrue(fo) && !mname) {
            die("final_only needs module%s", NULL);
        }
        if (mname && !ly_ctx_get_module_implemented(ctx, mname)) {
            die("validate module %s not implemented", mname);
        }
    } else if (!strcmp(what, "edit")) {
        const char *merge = input_of(step, "merge"), *del = str_of(step, "delete");
        const cJSON *set = cJSON_GetObjectItemCaseSensitive(step, "set");
        const cJSON *ins = cJSON_GetObjectItemCaseSensitive(step, "insert_term");
        const cJSON *inn = cJSON_GetObjectItemCaseSensitive(step, "insert_inner");
        const cJSON *il = cJSON_GetObjectItemCaseSensitive(step, "insert_list");
        const cJSON *il2 = cJSON_GetObjectItemCaseSensitive(step, "insert_list2");
        const cJSON *io = cJSON_GetObjectItemCaseSensitive(step, "insert_opaq");
        const cJSON *nm = cJSON_GetObjectItemCaseSensitive(step, "new_meta");
        const cJSON *fm = cJSON_GetObjectItemCaseSensitive(step, "free_meta");
        const cJSON *ms = cJSON_GetObjectItemCaseSensitive(step, "merge_self");
        const char *merge_into = input_of(step, "merge_into");

        if (!!merge + !!merge_into + !!del + !!set + !!ins + !!inn + !!il + !!il2 + !!io + !!nm + !!fm + !!ms != 1) {
            die("edit step needs exactly one of merge, merge_file, set, delete, insert_term, insert_inner, insert_list, "
                    "insert_list2, insert_opaq, new_meta, free_meta%s", NULL);
        }
        if (ms && !cJSON_IsTrue(ms)) {
            die("merge_self must be true%s", NULL);
        }
        if (nm || fm) {
            const cJSON *o = nm ? nm : fm;

            /* "module:name": the API's module argument is always NULL */
            if (!cJSON_IsObject(o) || !str_of(o, "node") || !str_of(o, "name") || !strchr(str_of(o, "name"), ':')) {
                die("new_meta/free_meta need an object with node and a module-qualified name%s", NULL);
            }
            keys_only(o, nm ? "node name value" : "node name", "unknown key \"%s\" in new_meta/free_meta");
            if (nm && !str_of(o, "value")) {
                die("new_meta needs a value%s", NULL);
            }
        }
        if (ins || inn || il || il2 || io) {
            const cJSON *o = ins ? ins : inn ? inn : il ? il : il2 ? il2 : io, *k;
            const char *ppath, *popaq; const cJSON *dp;

            if (!cJSON_IsObject(o)) {
                die("insert_* need an object with name and at most one of parent, parent_opaq%s", NULL);
            }
            /* both type-checked whatever the other fields are */
            ppath = str_of(o, "parent");
            popaq = str_of(o, "parent_opaq"); dp = (io || il2) ? NULL : cJSON_GetObjectItemCaseSensitive(o, "detached_parent"); if (dp && !cJSON_IsBool(dp)) die("detached_parent must be a boolean%s", NULL);
            if (!str_of(o, "name") || !!ppath + !!popaq + cJSON_IsTrue(dp) > 1) {
                die("insert_* need an object with name and at most one of parent, parent_opaq%s", NULL);
            }
            keys_only(o, ins ? "module parent parent_opaq detached_parent name value options" :
                    inn ? "module parent parent_opaq detached_parent name options" :
                    io ? "parent parent_opaq name value prefix module xml" : il2 ? "module parent parent_opaq name keys options" :
                    "module parent parent_opaq detached_parent name keys options", "unknown key \"%s\" in an insert_* edit");
            if (io) {
                /* lyd_new_opaq / lyd_new_opaq2: module is the module name / namespace, not a module */
                str_of(o, "value");
                str_of(o, "prefix");
                if (!str_of(o, "module")) {
                    die("insert_opaq needs a module%s", NULL);
                }
                if (cJSON_GetObjectItemCaseSensitive(o, "xml") && !cJSON_IsBool(cJSON_GetObjectItemCaseSensitive(o, "xml"))) {
                    die("insert_opaq xml must be a boolean%s", NULL);
                }
            } else {
                if (!str_of(o, "module") && !ppath && !popaq && !cJSON_IsTrue(dp)) {
                    die("insert_term/inner/list/list2 need a module or a parent%s", NULL);
                }
                flags_of(o, "options", inn ? inner_flags : newval_flags);
            }
            /* str_of() is NULL only for an absent or null field: "" is a value (ut-new/insert-60) */
            if (ins && !str_of(o, "value")) {
                die("insert_term needs a value%s", NULL);
            }
            if (il2) {
                str_of(o, "keys");
            }
            if (il && (k = cJSON_GetObjectItemCaseSensitive(o, "keys"))) {
                const cJSON *it;

                if (!cJSON_IsArray(k) || cJSON_GetArraySize(k) > 16) {
                    die("insert_list keys must be an array of at most 16 strings or nulls%s", NULL);
                }
                cJSON_ArrayForEach(it, k) {
                    if (!cJSON_IsString(it) && !cJSON_IsNull(it)) {
                        die("insert_list keys must be an array of at most 16 strings or nulls%s", NULL);
                    }
                }
            }
        }
        if (merge || merge_into) {
            dparams_of(step, &p);
        }
        if (set && (!cJSON_IsObject(set) || !str_of(set, "path"))) {
            die("set needs an object with path%s", NULL);
        }
        if (set) {
            const cJSON *update = cJSON_GetObjectItemCaseSensitive(set, "update");

            keys_only(set, "path value parent update", "unknown key \"%s\" in set");
            str_of(set, "value");
            str_of(set, "parent");
            if (update && !cJSON_IsBool(update)) {
                die("update must be a boolean%s", NULL);
            }
        }
    } else if (!strcmp(what, "change_term")) {
        const cJSON *canon = cJSON_GetObjectItemCaseSensitive(step, "canon");
        const cJSON *child = cJSON_GetObjectItemCaseSensitive(step, "child");

        /* str_of() is NULL only for an absent or null field: "" is a value (ut-new/change-09) */
        const char *node = str_of(step, "node"), *xpath = str_of(step, "node_xpath");

        if ((!node && !xpath) || !str_of(step, "value")) {
            die("change_term needs node and value%s", NULL);
        }
        if (node && xpath) {
            die("change_term takes node or node_xpath, not both%s", NULL);
        }
        if (child && (!cJSON_IsNumber(child) || child->valuedouble <= 0 ||
                child->valuedouble > INT32_MAX || child->valuedouble != child->valueint)) {
            die("change_term child must be a positive integer%s", NULL);
        }
        if (canon && !cJSON_IsBool(canon)) {
            die("change_term canon must be a boolean%s", NULL);
        }
    } else if (!strcmp(what, "dump") || !strcmp(what, "detached_dump")) {
        wd_of(step); if (!strcmp(what, "detached_dump")) { const cJSON *b = cJSON_GetObjectItemCaseSensitive(step, "child"); if (b && !cJSON_IsBool(b)) die("child must be a boolean%s", NULL); }
    } else if (!strcmp(what, "print_xml")) {
        /* no fields */
    } else if (!strcmp(what, "any_direct")) {
        const cJSON *tg = cJSON_GetObjectItemCaseSensitive(step, "target");
        const char *which = str_of(step, "case");

        if (!which) {
            die("any_direct needs a case%s", NULL);
        }
        if (strcmp(which, "nil_boundaries") && strcmp(which, "text_nul") &&
                strcmp(which, "node_then_empty") && strcmp(which, "equal_repeated_collisions") && strcmp(which, "equal_repeated_date_collisions") && strcmp(which, "equal_mixed_parent_payload") &&
                strcmp(which, "dup_payload_parent_context") && strcmp(which, "trim_payload_links") && strcmp(which, "merge_mixed_into_tree") && strcmp(which, "merge_opaque_mixed_schema_parents") &&
                strcmp(which, "dup_siblings_into_empty_anydata") &&
                strcmp(which, "dup_siblings_into_empty_anyxml") && strcmp(which, "dup_into_default_any_parent")) {
            die("unknown any_direct case %s", which);
        }
        if (tg && !cJSON_IsObject(tg)) {
            die("any_direct target must be a context object%s", NULL);
        }
        if (tg) {
            keys_only(tg, "searchdirs modules context_options", "unknown key \"%s\" in an any_direct target");
        }
    } else if (!strcmp(what, "dup")) {
        const cJSON *sib = cJSON_GetObjectItemCaseSensitive(step, "siblings");
        const cJSON *source = cJSON_GetObjectItemCaseSensitive(step, "source");
        const cJSON *det = cJSON_GetObjectItemCaseSensitive(step, "detached");

        str_of(step, "node");
        if (!str_of(step, "node") && !str_of(step, "node_xpath") && !cJSON_IsTrue(det)) {
            die("dup step needs a node path%s", NULL);
        }
        if (str_of(step, "node") && str_of(step, "node_xpath")) {
            die("dup step needs exactly one of node and node_xpath%s", NULL);
        }
        str_of(step, "parent");
        flags_of(step, "options", dup_flags);
        if (sib && !cJSON_IsBool(sib)) {
            die("dup siblings must be a boolean%s", NULL);
        }
        if (det && !cJSON_IsBool(det)) {
            die("dup detached must be a boolean%s", NULL);
        }
        if (cJSON_GetObjectItemCaseSensitive(step, "target")) {
            const cJSON *tg = cJSON_GetObjectItemCaseSensitive(step, "target");

            if (!cJSON_IsObject(tg) || str_of(step, "parent")) {
                die("dup target must be a context object, without a parent%s", NULL);
            }
            keys_only(tg, "searchdirs modules context_options", "unknown key \"%s\" in a dup target");
        }
        if (source) {
            if (!cJSON_IsObject(source) || !input_of(source, "data") || !cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(source, "parse_only"))) {
                die("dup source needs data and parse_only true%s", NULL);
            }
            keys_only(source, "format data_type data unknown parse_only rpc keep_input", "unknown key \"%s\" in a dup source");
            if (cJSON_GetObjectItemCaseSensitive(step, "target")) {
                die("dup source excludes target%s", NULL);
            }
        }
    } else if (!strcmp(what, "insert")) {
        const cJSON *det = cJSON_GetObjectItemCaseSensitive(step, "detached");
        const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");

        str_of(step, "node");
        str_of(step, "parent");
        if (det && !cJSON_IsBool(det)) {
            die("insert detached must be a boolean%s", NULL);
        }
        if (input_of(step, "data")) {
            if (cJSON_IsTrue(det)) {
                die("insert step takes data or detached, not both%s", NULL);
            }
            dparams_of(step, &p);
            if (p.optype != LYD_TYPE_DATA_YANG) {
                die("sequence supports datastore data types only%s", NULL);
            }
            if (target) {
                if (!cJSON_IsObject(target)) {
                    die("insert target must be a context object%s", NULL);
                }
                keys_only(target, "searchdirs modules context_options", "unknown key \"%s\" in an insert target");
            }
        } else if (!str_of(step, "node") && !cJSON_IsTrue(det)) {
            die("insert step needs a node, data, or detached%s", NULL);
        } else if (target) {
            die("insert target needs data%s", NULL);
        }
    } else if (!strcmp(what, "compare")) {
        const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");

        if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "self"))) {
            keys_only(step, "do self first second options", "unknown key \"%s\" in a same-tree comparison");
        } else {
            dparams_of(step, &p);
            if (p.optype != LYD_TYPE_DATA_YANG) {
                die("sequence supports datastore data types only%s", NULL);
            }
            compare_other(ctx, step, NULL, NULL);
            /* compare_other validates exactly one second-source form. */
            /* Ordinary data still has its dparams checked above. */
        }
        str_of(step, "first");
        str_of(step, "second");
        flags_of(step, "options", compare_flags);
        if (target) {
            if (!cJSON_IsObject(target)) {
                die("compare target must be a context object%s", NULL);
            }
            keys_only(target, "searchdirs modules context_options", "unknown key \"%s\" in a compare target");
        }
    } else if (!strcmp(what, "diff") || !strcmp(what, "diff_parse") || !strcmp(what, "diff_merge")) {
        const cJSON *b;

        dparams_of(step, &p);
        if (strcmp(what, "diff") && (p.optype != LYD_TYPE_DATA_YANG)) {
            die("sequence supports datastore data types only%s", NULL);
        }
        if (strcmp(what, "diff") && !input_of(step, "data")) {
            die("%s step needs data", what);
        }
        if (!strcmp(what, "diff")) {
            const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
            const cJSON *keep_input = cJSON_GetObjectItemCaseSensitive(step, "keep_input");
            const char *rpc = input_of(step, "rpc");

            if ((p.optype != LYD_TYPE_DATA_YANG) && !p.parse_only) {
                /* an operation is parsed only: ValidateOp is a step of its own (none yet) */
                die("sequence diffs operation data with parse_only only%s", NULL);
            }
            if (rpc && (p.optype != LYD_TYPE_REPLY_YANG)) {
                die("diff step: rpc only with data_type reply%s", NULL);
            }
            if (keep_input && !rpc) {
                die("diff step: keep_input only with rpc%s", NULL);
            }

            flags_of(step, "options", diff_flags);
            flags_of(step, "merge_options", diff_merge_flags);
            str_of(step, "node");
            str_of(step, "data_node");
            if (target) {
                if (!cJSON_IsObject(target)) {
                    die("diff target must be a context object%s", NULL);
                }
                /* any data type: lyd_diff() has no operation data rule, and step_diff parses
                 * operation data in the target context as without a target (a reply into its
                 * "rpc" request), so cross-context operation diffs stay testable */
                if (!input_of(step, "data")) {
                    die("diff target needs data%s", NULL);
                }
                keys_only(target, "searchdirs modules context_options", "unknown key \"%s\" in a diff target");
            }
            if (((b = cJSON_GetObjectItemCaseSensitive(step, "single")) && !cJSON_IsBool(b)) ||
                    ((b = cJSON_GetObjectItemCaseSensitive(step, "merge")) && !cJSON_IsBool(b)) ||
                    ((b = cJSON_GetObjectItemCaseSensitive(step, "detached")) && !cJSON_IsBool(b))) {
                die("diff single, merge, and detached must be booleans%s", NULL);
            }
        } else if (!strcmp(what, "diff_merge")) {
            flags_of(step, "options", diff_merge_flags); if ((b = cJSON_GetObjectItemCaseSensitive(step, "detached_parent")) && !cJSON_IsBool(b)) die("detached_parent must be a boolean%s", NULL);
            str_of(step, "module");
            if ((str_of(step, "parent") || cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached_parent"))) && !str_of(step, "src_node")) {
                die("diff_merge parent needs src_node%s", NULL);
            }
            if (str_of(step, "src_node") && str_of(step, "module")) {
                die("diff_merge takes module or src_node, not both%s", NULL);
            }
        }
    } else if (!strcmp(what, "diff_apply")) {
        str_of(step, "module");
    } else if (!strcmp(what, "trim")) {
        const cJSON *jv = cJSON_GetObjectItemCaseSensitive(step, "vars"), *v;

        if (!str_of(step, "xpath")) {
            die("trim step needs an xpath%s", NULL);
        }
        if (jv && !cJSON_IsObject(jv)) {
            die("trim vars must be an object%s", NULL);
        }
        cJSON_ArrayForEach(v, jv) {
            if (!cJSON_IsString(v)) {
                die("trim vars value of %s must be a string", v->string);
            }
        }
    } else if (!strcmp(what, "xpath")) {
        const cJSON *v = cJSON_GetObjectItemCaseSensitive(step, "detached");

        if (!str_of(step, "xpath")) {
            die("xpath step needs an xpath%s", NULL);
        }
        str_of(step, "node");
        if (str_of(step, "node") && cJSON_IsTrue(v)) {
            die("xpath node and detached are mutually exclusive%s", NULL);
        }
        if (v && !cJSON_IsBool(v)) {
            die("xpath detached must be a boolean%s", NULL);
        }
        v = cJSON_GetObjectItemCaseSensitive(step, "vars");
        if (v && !cJSON_IsObject(v)) {
            die("trim vars must be an object%s", NULL);
        }
        for (const cJSON *jv = v ? v->child : NULL; jv; jv = jv->next) {
            if (!cJSON_IsString(jv)) {
                die("trim vars value of %s must be a string", jv->string);
            }
        }
    } else if (!strcmp(what, "unlink")) {
        if (!!str_of(step, "node") == !!str_of(step, "diff_node")) {
            die("unlink step needs exactly one node or diff_node path%s", NULL);
        }
    } else if (!strcmp(what, "find_meta")) {
        str_of(step, "node");
        if (!str_of(step, "name")) {
            die("find_meta step needs a name%s", NULL);
        }
    } else if (!strcmp(what, "links")) {
        const cJSON *det = cJSON_GetObjectItemCaseSensitive(step, "detached");

        if (det && !cJSON_IsBool(det)) {
            die("links detached must be a boolean%s", NULL);
        }
    } else if (strcmp(what, "link") && strcmp(what, "diff_reverse")) {
        die("unknown step %s", what);
    }
    return what;
}

/* lyd_dup_single / lyd_dup_siblings of the node at "node" (or the retained detached node) into
 * the node at "parent", or, without a parent, the duplicate replacing the tree */
static LY_ERR
step_dup(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, struct lyd_node *detached, cJSON *diag)
{
    const char *npath = str_of(step, "node"), *xpath = str_of(step, "node_xpath"), *ppath = str_of(step, "parent");
    const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
    const cJSON *source = cJSON_GetObjectItemCaseSensitive(step, "source");
    struct lyd_node *node = NULL, *parent = NULL, *dup = NULL, *other = NULL;
    struct dparams p;
    struct ly_ctx *tctx = NULL;
    uint32_t opts = flags_of(step, "options", dup_flags);
    int sib = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "siblings")), ok;
    LY_ERR rc;

    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached"))) {
        node = detached;
    } else {
        if (source) {
            dparams_of(source, &p);
            rc = parse_one(ctx, &p, input_of(source, "data"), input_of(source, "rpc"), &other, diag, "parse");
            if (rc) {
                lyd_free_all(other);
                return rc;
            }
        }
        struct lyd_node *selection = source ? other : *tree;
        if (xpath) {
            struct ly_set *set = NULL;

            if (!selection || lyd_find_xpath(selection, xpath, &set) || set->count != 1) {
                die("dup node_xpath %s must select exactly one node", xpath);
            }
            node = set->dnodes[0];
            ly_set_free(set, NULL);
        } else if (!selection || lyd_find_path(selection, npath, 0, &node)) {
            die(source ? "dup node %s not found in the source" : "dup node %s not found", npath);
        }
    }
    if (!node) {
        die("dup retained detached node not found%s", NULL);
    }
    if (ppath && (!*tree || lyd_find_path(*tree, ppath, 0, &parent))) {
        die("dup parent %s not found", ppath);
    }
    if (target) {
        /* lyd_dup_*_to_ctx into a second context built like the request's ("searchdirs", "modules",
         * "context_options"); the duplicate, then in that context, replaces the tree */
        cJSON *scratch = cJSON_CreateObject();

        if (!cJSON_IsObject(target) || ppath) {
            die("dup target must be a context object, without a parent%s", NULL);
        }
        tctx = build_ctx_in(target, &ok, 0, scratch);
        cJSON_Delete(scratch);
        if (!ok) {
            die("dup target context failed%s", NULL);
        }
        rc = sib ? lyd_dup_siblings_to_ctx(node, tctx, NULL, opts, &dup) :
                lyd_dup_single_to_ctx(node, tctx, NULL, opts, &dup);
        collect(ctx, diag, "edit");
        if (LYD_CTX(node) != ctx) { collect(LYD_CTX(node), diag, "edit"); } if (tctx != ctx && tctx != LYD_CTX(node)) { collect(tctx, diag, "edit"); }
    } else {
        rc = sib ? lyd_dup_siblings(node, parent, opts, &dup) : lyd_dup_single(node, parent, opts, &dup);
        collect(ctx, diag, "edit"); if (LYD_CTX(node) != ctx) collect(LYD_CTX(node), diag, "edit");
    }
    if (!rc && !parent) {
        while (dup->parent) {
            dup = dup->parent;
        }
        lyd_free_all(*tree);
        *tree = lyd_first_sibling(dup);
    }
    lyd_free_all(other);
    /* tctx stays: the tree may now live in it, and the process ends after the response */
    return rc;
}
static LY_ERR step_dup_into_default_any_parent(struct ly_ctx *, const struct lys_module *, cJSON *, cJSON *), step_merge_mixed_into_tree(struct ly_ctx *, const struct lys_module *, cJSON *, cJSON *), step_equal_mixed_parent_payload(struct ly_ctx *, const struct lys_module *, cJSON *, cJSON *), step_merge_opaque_mixed_schema_parents(struct ly_ctx *, cJSON *, cJSON *), step_equal_repeated_date_collisions(struct ly_ctx *, const struct lys_module *, cJSON *, cJSON *);
static LY_ERR check_equal_order_contexts(struct ly_ctx *ctx, const cJSON *step, cJSON *diag);
static LY_ERR
step_any_direct(struct ly_ctx *ctx, const cJSON *step, cJSON *out, cJSON *diag)
{
    const char *which = str_of(step, "case");
    const struct lys_module *ac = ly_ctx_get_module_implemented(ctx, "ac-r6");
    struct lyd_node *n = NULL, *n2 = NULL, *dup = NULL;
    LY_ERR rc = LY_SUCCESS;

    if (!strcmp(which, "nil_boundaries")) {
        cJSON_AddBoolToObject(out, "child_is_null", lyd_child_any(NULL) == NULL);
        cJSON_AddItemToObject(out, "copy_rc", code_json(lyd_any_copy_value(NULL, NULL, NULL, 0)));
        return LY_SUCCESS;
    }
    if (!strcmp(which, "text_nul")) {
        char bytes[] = {'a', '\0', 'b', '\0'};
        char *s = NULL;

        rc = lyd_new_any(NULL, ac, "xml", NULL, bytes, LYD_VALHINT_STRING, 0, &n);
        if (!rc) {
            lyd_any_value_str(n, LYD_JSON, &s);
            add_opt_str(out, "created", s);
            free(s);
            rc = lyd_new_any(NULL, ac, "xml", NULL, NULL, 0, 0, &n2);
        }
        if (!rc) {
            rc = lyd_any_copy_value(n2, NULL, bytes, LYD_VALHINT_STRING);
        }
        if (!rc) {
            s = NULL;
            lyd_any_value_str(n2, LYD_JSON, &s);
            add_opt_str(out, "copied", s);
            free(s);
            rc = lyd_dup_single(n, NULL, 0, &dup);
        }
        if (!rc) {
            s = NULL;
            lyd_any_value_str(dup, LYD_JSON, &s);
            add_opt_str(out, "duplicated", s);
            free(s);
            cJSON_AddItemToObject(out, "compare", code_json(lyd_compare_single(n, dup, 0)));
        }
        lyd_free_tree(n);
        lyd_free_tree(n2);
        lyd_free_tree(dup);
        collect(ctx, diag, "edit");
        return rc;
    }
    if (!strcmp(which, "node_then_empty")) {
        struct lyd_node *dup2;
        char *s = NULL;
        rc = lyd_new_any(NULL, ac, "xml", NULL, "<n/>", LYD_VALHINT_STRING, 0, &n);
        if (!rc) rc = lyd_new_any(NULL, ac, "xml", NULL, "{}", LYD_VALHINT_STRING, 0, &n2);
        if (!rc) {
            ((struct lyd_node_any *)n)->hints = 0;
            ((struct lyd_node_any *)n2)->hints = 0;
            rc = lyd_insert_sibling(n, n2, &n);
        }
        if (!rc) rc = lyd_dup_siblings(n, NULL, 0, &dup);
        dup2 = dup ? dup->next : NULL;
        cJSON_AddBoolToObject(out, "first_reparsed_as_tree", dup && lyd_child_any(dup));
        if (dup2) {
            lyd_any_value_str(dup2, LYD_JSON, &s);
        }
        add_opt_str(out, "second_retained_text", s);
        free(s);
        lyd_free_all(n);
        lyd_free_all(dup);
        collect(ctx, diag, "edit");
        return rc;
    }
    if (!strcmp(which, "dup_siblings_into_empty_anydata") ||
            !strcmp(which, "dup_siblings_into_empty_anyxml")) {
        struct lyd_node *first = NULL, *second = NULL, *single_parent = NULL, *parent = NULL, *made = NULL, *iter;
        size_t count = 0;
        rc = lyd_new_opaq(NULL, ctx, "one", "v1", NULL, "free", &first);
        if (!rc) rc = lyd_new_opaq(NULL, ctx, "two", "v2", NULL, "free", &second);
        if (!rc) rc = lyd_insert_sibling(first, second, &first);
        if (!rc) rc = lyd_new_any(NULL, ac,
                !strcmp(which, "dup_siblings_into_empty_anydata") ? "data" : "xml",
                NULL, NULL, 0, 0, &single_parent);
        if (!rc) rc = lyd_dup_single(first, single_parent, 0, &made);
        cJSON_AddBoolToObject(out, "single_copied", lyd_child_any(single_parent) != NULL);
        if (!rc) rc = lyd_new_any(NULL, ac,
                !strcmp(which, "dup_siblings_into_empty_anydata") ? "data" : "xml",
                NULL, NULL, 0, 0, &parent);
        if (!rc) rc = lyd_dup_siblings(first, parent, 0, &made);
        LY_LIST_FOR(lyd_child_any(parent), iter) {
            ++count;
        }
        cJSON_AddNumberToObject(out, "source_siblings", 2);
        cJSON_AddNumberToObject(out, "copied_siblings", count);
        cJSON_AddBoolToObject(out, "destination_was_empty", 1);
        lyd_free_all(first);
        lyd_free_tree(single_parent);
        lyd_free_tree(parent);
        collect(ctx, diag, "edit");
        return rc;
    }
    if (!strcmp(which, "merge_mixed_into_tree")) { return step_merge_mixed_into_tree(ctx, ac, out, diag); } if (!strcmp(which, "merge_opaque_mixed_schema_parents")) { return step_merge_opaque_mixed_schema_parents(ctx, out, diag); } if (!strcmp(which, "equal_repeated_date_collisions")) { return step_equal_repeated_date_collisions(ctx, ac, out, diag); }
    if (!strcmp(which, "dup_into_default_any_parent")) return step_dup_into_default_any_parent(ctx, ac, out, diag); if (!strcmp(which, "equal_mixed_parent_payload")) return step_equal_mixed_parent_payload(ctx, ac, out, diag);
    if (!strcmp(which, "equal_repeated_collisions")) {
        struct lyd_node *a = NULL, *b = NULL, *x = NULL, *aw = NULL, *bw = NULL;
        const char *av[] = {"first", "second", "collision"};
        const char *bv[] = {"second", "first", "collision"};
        const char *keys[] = {"repeat", "repeat", "Aa/BB"};

        for (size_t i = 0; !rc && i < 3; ++i) {
            rc = lyd_new_list(NULL, ac, "entry", 0, &x, keys[i]);
            if (!rc) rc = lyd_new_term(x, NULL, "value", av[i], 0, NULL);
            if (!rc) rc = lyd_insert_sibling(a, x, &a);
        }
        for (size_t i = 0; !rc && i < 3; ++i) {
            rc = lyd_new_list(NULL, ac, "entry", 0, &x, keys[i]);
            if (!rc) rc = lyd_new_term(x, NULL, "value", bv[i], 0, NULL);
            if (!rc) rc = lyd_insert_sibling(b, x, &b);
        }
        if (!rc) rc = lyd_new_any(NULL, ac, "data", a, NULL, 0, LYD_NEW_ANY_USE_VALUE, &aw);
        if (!rc) rc = lyd_new_any(NULL, ac, "data", b, NULL, 0, LYD_NEW_ANY_USE_VALUE, &bw);
        if (!rc) cJSON_AddItemToObject(out, "compare_rc", code_json(lyd_compare_single(aw, bw, 0)));
        cJSON_AddNumberToObject(out, "repeated_keys", 2);
        cJSON_AddStringToObject(out, "collision_shaped_key", "Aa/BB");
        lyd_free_tree(aw);
        lyd_free_tree(bw); if (!rc) rc = check_equal_order_contexts(ctx, step, diag);
        collect(ctx, diag, "edit");
        return rc;
    }
    if (!strcmp(which, "dup_payload_parent_context") || !strcmp(which, "trim_payload_links")) {
        const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
        struct ly_ctx *bctx;
        cJSON *scratch = cJSON_CreateObject();
        int ok;

        if (!target) {
            die("any_direct %s needs target", which);
        }
        bctx = build_ctx_in(target, &ok, 0, scratch);
        cJSON_Delete(scratch);
        if (!ok) {
            die("any_direct target context failed%s", NULL);
        }
        if (!strcmp(which, "dup_payload_parent_context")) {
            struct lyd_node *parent = NULL, *src_a = NULL, *src_b = NULL, *outer = NULL, *made = NULL;
            LY_ERR mismatch, match;

            rc = lyd_new_opaq(NULL, bctx, "parent", "", NULL, "free", &parent);
            if (!rc) rc = lyd_new_opaq(NULL, ctx, "source-a", "", NULL, "free", &src_a);
            if (!rc) rc = lyd_new_opaq(NULL, bctx, "source-b", "", NULL, "free", &src_b);
            if (!rc) rc = lyd_new_any(NULL, ac, "data", parent, NULL, 0, LYD_NEW_ANY_USE_VALUE, &outer);
            mismatch = rc ? rc : lyd_dup_single_to_ctx(src_a, ctx, parent, 0, &made);
            cJSON_AddItemToObject(out, "mismatch_rc", code_json(mismatch));
            cJSON_AddBoolToObject(out, "mismatch_unchanged", lyd_child_any(parent) == NULL);
            match = rc ? rc : lyd_dup_single_to_ctx(src_b, bctx, parent, 0, &made);
            cJSON_AddItemToObject(out, "match_rc", code_json(match));
            cJSON_AddBoolToObject(out, "match_inserted", lyd_child_any(parent) != NULL);
            lyd_free_tree(src_a);
            lyd_free_tree(src_b);
            lyd_free_tree(outer);
            collect(ctx, diag, "edit");
            collect(bctx, diag, "edit");
            ly_ctx_destroy(bctx);
            return rc;
        } else {
            const struct lys_module *ap = ly_ctx_get_module_implemented(bctx, "ac-r6");
            struct lyd_node *holder = NULL, *ref_node = NULL, *forest = NULL, *nested = NULL, *outer = NULL;
            const struct lyd_leafref_links_rec *rec = NULL;
            LY_ARRAY_COUNT_TYPE before = 0, after = 0;
            rc = lyd_new_path(NULL, bctx, "/ac-r6:outer/payload-target", "x", 0, &holder);
            if (!rc) rc = lyd_new_path(NULL, bctx, "/ac-r6:payload-ref", "x", 0, &ref_node);
            if (!rc) rc = lyd_insert_sibling(holder, ref_node, &forest);
            if (!rc) rc = lyd_leafref_link_node_tree(forest);
            /* Unlinking a non-terminal holder keeps its child's leafref link alive. */
            if (!rc) rc = lyd_unlink_tree(holder);
            if (!rc) rc = lyd_new_any(NULL, ap, "nested", holder, NULL, 0, LYD_NEW_ANY_USE_VALUE, &nested);
            if (!rc) rc = lyd_new_any(NULL, ac, "data", nested, NULL, 0, LYD_NEW_ANY_USE_VALUE, &outer);
            if (!rc && !lyd_leafref_get_links((const struct lyd_node_term *)ref_node, &rec)) {
                before = LY_ARRAY_COUNT(rec->target_nodes);
            }
            if (!rc && before != 1) die("trim_payload_links link missing before trim%s", NULL);
            if (!rc) rc = lyd_trim_xpath(&outer, "/*[false()]", NULL);
            if (!lyd_leafref_get_links((const struct lyd_node_term *)ref_node, &rec)) {
                after = LY_ARRAY_COUNT(rec->target_nodes);
            }
            if (!rc && after != 0) die("trim_payload_links link survived trim%s", NULL);
            cJSON_AddNumberToObject(out, "links_before_trim", before);
            cJSON_AddNumberToObject(out, "links_after", after);
            cJSON_AddBoolToObject(out, "outer_removed", outer == NULL);
            lyd_free_all(ref_node);
            lyd_free_all(outer);
            collect(ctx, diag, "xpath");
            collect(bctx, diag, "xpath");
            ly_ctx_destroy(bctx);
            return rc;
        }
    }
    die("unknown any_direct case %s", which);
    return LY_EINVAL;
}

/* lyd_insert_child of the node at "node" (or the retained detached node) under the node at "parent"
 * in the tree ("@top": its first top-level node, also an opaque one), or without a parent lyd_insert_sibling(tree, node, &tree). With "data" the node comes from a second tree parsed as by a
 * parse step ("node": a path in it, omitted: its first top-level node, which moves with its siblings
 * as lyd_move_nodes moves them); what is left of the second tree is freed. The step's rc is the
 * parse's, else the insert's (diagnostics phases parse and edit) */
static LY_ERR
step_insert(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, struct lyd_node **detached, cJSON *diag)
{
    const char *npath = str_of(step, "node"), *ppath = str_of(step, "parent"), *data = input_of(step, "data");
    const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
    struct lyd_node *other = NULL, *node = NULL, *parent = NULL, *top;
    struct ly_ctx *other_ctx = ctx;
    struct dparams p;
    LY_ERR rc;
    int ok;

    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached"))) {
        node = *detached;
        if (!node) {
            die("insert retained detached node not found%s", NULL);
        }
    } else if (data) {
        if (target) {
            cJSON *scratch = cJSON_CreateObject();
            other_ctx = build_ctx_in(target, &ok, 0, scratch);
            cJSON_Delete(scratch);
            if (!ok) {
                die("insert target context failed%s", NULL);
            }
        }
        dparams_of(step, &p);
        rc = parse_one(other_ctx, &p, data, NULL, &other, diag, "parse");
        if (rc) {
            return rc;
        }
        node = other;
        if (npath && (!other || lyd_find_path(other, npath, 0, &node))) {
            die("insert node %s not found in the data", npath);
        }
        if (!node) {
            die("insert data is empty%s", NULL);
        }
    } else if (!*tree || lyd_find_path(*tree, npath, 0, &node)) {
        die("insert node %s not found", npath);
    }
    if (ppath && !strcmp(ppath, "@top") && *tree) { parent = *tree; } else if (ppath && (!*tree || lyd_find_path(*tree, ppath, 0, &parent))) {
        die("insert parent %s not found", ppath);
    }
    /* libyang links a node into its own subtree without a check (D-0112): refuse the request */
    for (top = parent; top; top = top->parent) {
        /* the node, or the sibling list it starts when it moves whole (lyd_move_nodes) */
        if ((top == node) || (!top->parent && !node->parent && !node->prev->next && node->next &&
                (lyd_first_sibling(top) == node))) {
            die("insert parent %s is inside the moved node", ppath);
        }
    }
    if (parent) {
        rc = lyd_insert_child(parent, node);
        for (top = parent; top->parent; top = top->parent) {}
        *tree = lyd_first_sibling(top);
    } else {
        rc = lyd_insert_sibling(*tree, node, tree);
    }
    collect(ctx, diag, "edit"); if (LYD_CTX(node) != ctx) collect(LYD_CTX(node), diag, "edit");
    if (*tree) {
        *tree = lyd_first_sibling(*tree);
    }
    if (!rc && (node == *detached)) {
        *detached = NULL;
    }
    /* the rest of the second tree: all of it on failure, none when its first node moved */
    if (other && (rc || (node != other))) {
        lyd_free_all(other);
    }
    return rc;
}

/* lyd_compare_single of "first" in the retained tree and "second" in a parsed tree (in the
 * "target" context when given), or the retained tree with self:true (selectors omitted: first
 * top-level node). "compare" is its rc (LY_SUCCESS equal, LY_ENOT not), the step's rc that of the
 * parse or LY_SUCCESS with self:true. */
static LY_ERR
step_compare(struct ly_ctx *ctx, const cJSON *step, struct lyd_node *tree, cJSON *diag, cJSON *s)
{
    const char *p1 = str_of(step, "first"), *p2 = str_of(step, "second");
    const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
    struct lyd_node *other = NULL, *n1 = tree, *n2;
    struct ly_ctx *other_ctx = ctx;
    /* compare_other parses data or constructs an otherwise-unparseable opaque root. */
    LY_ERR rc = LY_SUCCESS;
    int self = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "self")), ok;

    if (target) {
        cJSON *scratch = cJSON_CreateObject();
        other_ctx = build_ctx_in(target, &ok, 0, scratch);
        cJSON_Delete(scratch);
        if (!ok) {
            die("compare target context failed%s", NULL);
        }
    }

    if (self) {
        other = tree;
    } else {
        rc = compare_other(other_ctx, step, &other, diag);
    }
    if (!rc) {
        n2 = other;
        if (p1 && (!tree || lyd_find_path(tree, p1, 0, &n1))) {
            die("compare first %s not found", p1);
        }
        if (p2 && (!other || lyd_find_path(other, p2, 0, &n2))) {
            die("compare second %s not found", p2);
        }
        cJSON_AddItemToObject(s, "compare", code_json(lyd_compare_single(n1, n2,
                flags_of(step, "options", compare_flags))));
    }
    if (!self) {
        lyd_free_all(other);
    }
    return rc;
}

static LY_ERR
step_parse(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag)
{
    struct dparams p;
    struct lyd_node *t;
    LY_ERR rc;

    dparams_of(step, &p);
    if (str_of(step, "parent")) {
        /* operation data parsed under a node of the retained tree: lyd_parse_op with that parent,
         * the tree kept (and changed) whatever the result */
        struct lyd_node *parent = NULL;
        struct ly_in *in = NULL;

        if ((p.optype != LYD_TYPE_RPC_YANG) && (p.optype != LYD_TYPE_REPLY_YANG) && (p.optype != LYD_TYPE_NOTIF_YANG)) {
            die("parse parent requires data_type rpc, reply or notif%s", NULL);
        }
        if (input_of(step, "rpc") || !p.parse_only) {
            die("parse parent excludes rpc and requires parse_only%s", NULL);
        }
        if (!*tree || lyd_find_path(*tree, str_of(step, "parent"), 0, &parent) || !parent) {
            die("parse parent %s not found", str_of(step, "parent"));
        }
        ly_in_new_memory(input_of(step, "data"), &in);
        rc = lyd_parse_op(ctx, parent, in, p.fmt, p.optype, p.popts & (LYD_PARSE_STRICT | LYD_PARSE_OPAQ), NULL, NULL);
        ly_in_free(in, 0);
        collect(ctx, diag, "parse");
        return rc;
    }
    /* operation data: lyd_parse_op, a reply into its "rpc" request (parse_one) */
    rc = parse_one(ctx, &p, input_of(step, "data"), input_of(step, "rpc"), &t, diag, "parse");
    if (!rc) {
        lyd_free_all(*tree);
        *tree = t;
    }
    return rc;
}

static LY_ERR
step_validate(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag, cJSON *s)
{
    struct dparams p;
    const char *mname = str_of(step, "module");
    int final = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "final_only"));
    struct lyd_node *diff = NULL;
    char *str = NULL;
    LY_ERR rc;

    dparams_of(step, &p);
    if (mname) {
        /* lyd_validate_module, or lyd_validate_module_final with final_only */
        const struct lys_module *mod = ly_ctx_get_module_implemented(ctx, mname);

        if (final) {
            rc = lyd_validate_module_final(*tree, mod, p.vopts);
        } else {
            rc = lyd_validate_module(tree, mod, p.vopts, &diff);
        }
    } else {
        rc = lyd_validate_all(tree, ctx, p.vopts, &diff);
    }
    collect(ctx, diag, "validate");
    if (diff) {
        lyd_print_mem(&str, diff, LYD_JSON, LYD_PRINT_WD_ALL | LYD_PRINT_SIBLINGS);
    }
    add_opt_str(s, "implicit_diff", str);
    free(str);
    lyd_free_all(diff);
    return rc;
}

static LY_ERR
step_edit(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, struct lyd_node *detached, cJSON *diag)
{
    const char *merge = input_of(step, "merge"), *del = str_of(step, "delete");
    const cJSON *set = cJSON_GetObjectItemCaseSensitive(step, "set");
    const cJSON *ins = cJSON_GetObjectItemCaseSensitive(step, "insert_term");
    const cJSON *inn = cJSON_GetObjectItemCaseSensitive(step, "insert_inner");
    const cJSON *il = cJSON_GetObjectItemCaseSensitive(step, "insert_list");
    const cJSON *il2 = cJSON_GetObjectItemCaseSensitive(step, "insert_list2");
    const cJSON *io = cJSON_GetObjectItemCaseSensitive(step, "insert_opaq");
    const cJSON *nm = cJSON_GetObjectItemCaseSensitive(step, "new_meta");
    const cJSON *fm = cJSON_GetObjectItemCaseSensitive(step, "free_meta");
    struct lyd_node *node = NULL;
    LY_ERR rc = LY_SUCCESS;

    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "merge_self"))) {
        /* Copy actual children, including incomplete cross-context key prefixes. */
        if (*tree) {
            rc = lyd_dup_siblings(*tree, NULL, LYD_DUP_RECURSIVE, &node);
            if (!rc) {
                rc = lyd_merge_siblings(tree, node, LYD_MERGE_DESTRUCT);
            }
        }
    } else if (nm || fm) {
        /* lyd_new_meta, or lyd_find_meta + lyd_free_meta_single, on the node at "node" */
        const cJSON *o = nm ? nm : fm;

        if (!*tree || lyd_find_path(*tree, str_of(o, "node"), 0, &node)) {
            die("metadata node %s not found", str_of(o, "node"));
        }
        if (nm) {
            rc = lyd_new_meta(NULL, node, NULL, str_of(o, "name"), str_of(o, "value"), 0, NULL);
        } else {
            lyd_free_meta_single(lyd_find_meta(node->meta, NULL, str_of(o, "name")));
        }
    } else if (ins || inn || il || il2 || io) {
        /* lyd_new_term / _inner / _list3 / _list2 / _opaq / _opaq2, then lyd_insert_sibling (top level), or a child
         * of "parent" (a path) or "parent_opaq" (the name of a top-level opaque node) */
        const cJSON *o = ins ? ins : inn ? inn : il ? il : il2 ? il2 : io;
        const char *mname = str_of(o, "module"), *ppath = str_of(o, "parent"), *popaq = str_of(o, "parent_opaq");
        const char *name = str_of(o, "name");
        const struct lys_module *mod = (mname && !io) ? ly_ctx_get_module_implemented(ctx, mname) : NULL;
        uint32_t opts = io ? 0 : flags_of(o, "options", inn ? inner_flags : newval_flags);
        struct lyd_node *parent = NULL;

        if (mname && !io && !mod) {
            die("module %s is not implemented", mname);
        }
        if (ppath && (!*tree || lyd_find_path(*tree, ppath, 0, &parent))) {
            die("insert parent %s not found", ppath);
        }
        if (popaq && (!*tree || lyd_find_sibling_opaq_next(lyd_first_sibling(*tree), popaq, &parent))) {
            die("insert parent_opaq %s not found", popaq);
        } if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(o, "detached_parent"))) { if (!detached) die("insert retained detached parent not found%s", NULL); parent = detached; }
        if (io && parent && parent->schema && (parent->schema->nodetype & LYD_NODE_TERM)) {
            die("insert_opaq under the leaf %s: libyang links it over the leaf's value", LYD_NAME(parent));
        }
        if (!io && !mod && (!parent || !parent->schema)) {
            die("insert %s under an opaque parent needs a module (libyang reads its NULL schema)", name);
        }
        if (ins) {
            rc = lyd_new_term(parent, mod, name, str_of(o, "value"), opts, &node);
        } else if (inn) {
            rc = lyd_new_inner(parent, mod, name, opts ? 1 : 0, &node);
        } else if (il2) {
            rc = lyd_new_list2(parent, mod, name, str_of(o, "keys"), opts, &node);
        } else if (il) {
            /* lyd_new_list3: "keys" absent is a NULL array; fewer values than the list has keys would be read
             * past its end, a request-error */
            const cJSON *k = cJSON_GetObjectItemCaseSensitive(o, "keys"), *it;
            const void *vals[16] = {0};
            const struct lysc_node *sl, *key;
            int n = 0;

            cJSON_ArrayForEach(it, k) {
                vals[n++] = cJSON_IsString(it) ? it->valuestring : NULL;
            }
            sl = lys_find_child(ctx, parent ? parent->schema : NULL, mod ? mod : parent->schema->module, NULL, 0, name,
                    strlen(name), (opts & LYD_NEW_VAL_OUTPUT) ? LYS_GETNEXT_OUTPUT : 0);
            for (key = (sl && (sl->nodetype == LYS_LIST)) ? lysc_node_child(sl) : NULL; k && key && (key->flags & LYS_KEY); key = key->next) {
                if (n-- <= 0) {
                    die("insert_list %s has fewer keys than the list", name);
                }
            }
            rc = lyd_new_list3(parent, mod, name, k ? vals : NULL, NULL, opts, &node);
        } else if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(o, "xml"))) {
            rc = lyd_new_opaq2(parent, ctx, name, str_of(o, "value"), str_of(o, "prefix"), mname, &node);
        } else {
            rc = lyd_new_opaq(parent, ctx, name, str_of(o, "value"), str_of(o, "prefix"), mname, &node);
        }
        if (!rc && !parent) {
            rc = lyd_insert_sibling(*tree, node, tree);
        }
        if (*tree) {
            *tree = lyd_first_sibling(*tree);
        }
    } else if (input_of(step, "merge_into")) {
        struct dparams p;
        struct ly_in *in = NULL;
        struct lyd_node *dest = NULL;

        dparams_of(step, &p);
        ly_in_new_memory(input_of(step, "merge_into"), &in);
        rc = lyd_parse_data(ctx, NULL, in, p.fmt, p.popts | LYD_PARSE_ONLY, 0, &dest);
        ly_in_free(in, 0);
        collect(ctx, diag, "edit");
        if (!rc && dest) {
            rc = lyd_merge_siblings(&dest, *tree, 0);
            collect(ctx, diag, "edit");
            if (!rc) {
                lyd_free_all(*tree);
                *tree = dest;
            } else {
                lyd_free_all(dest);
            }
        }
    } else if (merge) {
        /* parsed like data_type "edit" would be: parse-only, then merged */
        struct dparams p;
        struct ly_in *in = NULL;

        dparams_of(step, &p);
        ly_in_new_memory(merge, &in);
        rc = lyd_parse_data(ctx, NULL, in, p.fmt, p.popts | LYD_PARSE_ONLY, 0, &node);
        ly_in_free(in, 0);
        collect(ctx, diag, "edit");
        if (!rc && node) {
            rc = lyd_merge_siblings(tree, node, LYD_MERGE_DESTRUCT);
        }
    } else if (set) {
        struct lyd_node *parent = *tree, *top, *n;
        const char *ppath = str_of(set, "parent");
        uint32_t opts = cJSON_IsFalse(cJSON_GetObjectItemCaseSensitive(set, "update")) ? 0 : LYD_NEW_PATH_UPDATE;

        if (ppath && (!*tree || lyd_find_path(*tree, ppath, 0, &parent))) {
            /* lyd_find_path does not descend through opaque nodes: match lyd_path() instead */
            parent = NULL;
            LY_LIST_FOR(*tree, top) {
                LYD_TREE_DFS_BEGIN(top, n) {
                    char *path = lyd_path(n, LYD_PATH_STD, NULL, 0);

                    if (!parent && path && !strcmp(path, ppath)) {
                        parent = n;
                    }
                    free(path);
                    LYD_TREE_DFS_END(top, n);
                }
            }
            if (!parent) {
                die("set parent %s not found", ppath);
            }
        }
        rc = lyd_new_path(parent, ctx, str_of(set, "path"), str_of(set, "value"), opts, &node);
        if (!rc && !*tree && node) {
            while (node->parent) {
                node = node->parent;
            }
            *tree = node;
        }
        if (*tree) {
            *tree = lyd_first_sibling(*tree);
        }
    } else {
        rc = *tree ? lyd_find_path(*tree, del, 0, &node) : LY_ENOTFOUND;
        if (!rc) {
            if (node == *tree) {
                *tree = node->next;
            }
            /* void: refuses list keys with an LY_EINVAL error item, see op_sequence() */
            lyd_free_tree(node);
        }
    }
    collect(ctx, diag, "edit");
    return rc;
}

/* the node at path in tree (request-error when there is none) */
static struct lyd_node *
node_at(struct lyd_node *tree, const char *path, const char *what)
{
    struct lyd_node *node = NULL;

    if (!tree || lyd_find_path(tree, path, 0, &node) || !node) {
        die(what, path);
    }
    return node;
}

/* the implemented module named by the step's "module", NULL without one */
static const struct lys_module *
module_of(struct ly_ctx *ctx, const cJSON *step)
{
    const char *name = str_of(step, "module");
    const struct lys_module *mod = NULL;

    if (name && !(mod = ly_ctx_get_module_implemented(ctx, name))) {
        die("module %s is not implemented", name);
    }
    return mod;
}

/*
 * diff: lyd_diff_siblings (lyd_diff_tree with single) of the retained tree (or its node at "node",
 * or the retained detached node with "detached")
 * and the parsed data (or its node at "data_node"; no data: NULL); the result replaces the diff
 * register, or with merge is merged into it (lyd_diff_merge_all, merge_options) and reported as
 * new_diff
 */
static LY_ERR
step_diff(struct ly_ctx *ctx, const cJSON *step, struct lyd_node *tree, struct lyd_node *detached,
        struct lyd_node **diff, cJSON *diag,
        cJSON *s)
{
    struct dparams p;
    const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
    const char *data = input_of(step, "data"), *npath = str_of(step, "node"), *dpath = str_of(step, "data_node");
    struct lyd_node *second = NULL, *first = tree, *snode, *res = NULL;
    struct ly_ctx *second_ctx = ctx;
    uint16_t opts = flags_of(step, "options", diff_flags);
    int ok;
    LY_ERR rc = LY_SUCCESS;

    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached"))) {
        first = detached;
        if (!first) {
            die("diff retained detached node not found%s", NULL);
        }
    } else if (npath) {
        first = node_at(tree, npath, "diff node %s not found");
    }
    if (data) {
        if (target) {
            cJSON *scratch = cJSON_CreateObject();

            second_ctx = build_ctx_in(target, &ok, 0, scratch);
            cJSON_Delete(scratch);
            if (!ok) {
                die("diff target context failed%s", NULL);
            }
        }
        dparams_of(step, &p);
        /* operation data: lyd_parse_op, a reply into its "rpc" request (parse_one) */
        rc = parse_one(second_ctx, &p, data, input_of(step, "rpc"), &second, diag, "parse");
        if (rc) {
            goto cleanup;
        }
    }
    snode = dpath ? node_at(second, dpath, "diff data_node %s not found") : second;
    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "single"))) {
        rc = lyd_diff_tree(first, snode, opts, &res);
    } else {
        rc = lyd_diff_siblings(first, snode, opts, &res);
    }
    collect(ctx, diag, "diff"); if (first && LYD_CTX(first) != ctx) collect(LYD_CTX(first), diag, "diff");
    if (second_ctx != ctx) {
        collect(second_ctx, diag, "diff");
    }
    if (!rc && cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "merge"))) {
        cJSON_AddItemToObject(s, "new_diff", res ? print_tree(res, LYD_PRINT_WD_ALL, 1) : cJSON_CreateNull());
        const struct ly_ctx *res_ctx = res && LYD_CTX(res) != ctx ? LYD_CTX(res) : NULL; const struct ly_err_item *mark = res_ctx ? ly_err_last(res_ctx) : NULL; rc = lyd_diff_merge_all(diff, res, flags_of(step, "merge_options", diff_merge_flags));
        collect(ctx, diag, "diff"); if (res_ctx) collect_after(res_ctx, mark, diag, "diff"); /* the merge's logs there; new_diff's printer logs stay pending */
        lyd_free_all(res);
    } else if (!rc) {
        lyd_free_all(*diff);
        *diff = res;
    }
cleanup:
    lyd_free_all(second);
    /* A retained diff may use second_ctx; like op_sequence's primary context, keep it until exit. */
    return rc;
}

/* diff_parse: the data, parsed with LYD_PARSE_ONLY as test_diff.c does, replaces the diff register */
static LY_ERR
step_diff_parse(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **diff, cJSON *diag)
{
    struct dparams p;
    struct lyd_node *t;
    LY_ERR rc;

    dparams_of(step, &p);
    p.popts |= LYD_PARSE_ONLY;
    rc = parse_one(ctx, &p, input_of(step, "data"), NULL, &t, diag, "parse");
    if (!rc) {
        lyd_free_all(*diff);
        *diff = t;
    }
    return rc;
}

/*
 * diff_merge: the data parsed like diff_parse merged into the diff register: lyd_diff_merge_module
 * (lyd_diff_merge_all without module), or with src_node lyd_diff_merge_tree of that subtree under
 * the register's node at "parent"
 */
static LY_ERR
step_diff_merge(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **diff, struct lyd_node *detached, cJSON *diag)
{
    struct dparams p;
    struct lyd_node *src = NULL, *parent = NULL;
    const char *spath = str_of(step, "src_node"), *ppath = str_of(step, "parent");
    uint16_t opts = flags_of(step, "options", diff_merge_flags);
    const struct lys_module *mod = module_of(ctx, step); const struct ly_ctx *diff_ctx = *diff ? LYD_CTX(*diff) : NULL;
    LY_ERR rc;

    dparams_of(step, &p);
    p.popts |= LYD_PARSE_ONLY;
    rc = parse_one(ctx, &p, input_of(step, "data"), NULL, &src, diag, "parse");
    if (rc) {
        return rc;
    }
    if (spath) { if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached_parent"))) { parent = detached; if (!parent) die("diff_merge retained detached parent not found%s", NULL); }
        if (ppath && !parent) {
            parent = node_at(*diff, ppath, "diff_merge parent %s not found");
        }
        rc = lyd_diff_merge_tree(diff, parent, node_at(src, spath, "diff_merge src_node %s not found"), NULL, NULL,
                opts);
    } else {
        rc = lyd_diff_merge_module(diff, src, mod, NULL, NULL, opts);
    }
    collect(ctx, diag, "diff"); if (diff_ctx && diff_ctx != ctx) collect(diff_ctx, diag, "diff"); if (parent && LYD_CTX(parent) != ctx && LYD_CTX(parent) != diff_ctx) collect(LYD_CTX(parent), diag, "diff");
    lyd_free_all(src);
    return rc;
}

/* trim: lyd_trim_xpath of the tree with the step's xpath and variables */
static LY_ERR
step_trim(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag)
{
    const cJSON *v;
    struct lyxp_var *vars = NULL;
    LY_ERR rc;

    cJSON_ArrayForEach(v, cJSON_GetObjectItemCaseSensitive(step, "vars")) {
        if (lyxp_vars_set(&vars, v->string, v->valuestring)) {
            die("lyxp_vars_set %s failed", v->string);
        }
    }
    rc = lyd_trim_xpath(tree, str_of(step, "xpath"), vars);
    lyxp_vars_free(vars);
    collect(ctx, diag, "xpath");
    return rc;
}

/* Evaluate from the node at "node", the retained unlinked node, or (neither) the retained tree's
 * first node, with the retained tree as the absolute root and the step's variables. An empty
 * retained tree is LY_EINVAL with a null result and no diagnostics. */
static LY_ERR
step_xpath(struct ly_ctx *ctx, const cJSON *step, struct lyd_node *tree, struct lyd_node *detached,
        cJSON *diag, cJSON *out)
{
    const char *path = str_of(step, "node");
    const struct lyd_node *node;
    struct ly_set *set = NULL;
    char *str = NULL;
    long double num = 0;
    ly_bool b = 0;
    LY_XPATH_TYPE type;
    LY_ERR rc;
    cJSON *res;

    struct lyxp_var *vars = NULL;
    const cJSON *v;

    if (!tree) {
        /* lyd_eval_xpath4 needs a context node or tree: LY_EINVAL before any logging */
        cJSON_AddNullToObject(out, "result");
        return LY_EINVAL;
    }
    cJSON_ArrayForEach(v, cJSON_GetObjectItemCaseSensitive(step, "vars")) {
        if (lyxp_vars_set(&vars, v->string, v->valuestring)) {
            die("lyxp_vars_set %s failed", v->string);
        }
    }
    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached")) && !detached) {
        die("xpath retained detached node not found%s", NULL);
    }
    node = path ? (!strcmp(path, "@last") ? tree->prev :
            node_at(tree, path, "xpath node %s not found")) :
            (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached")) ? detached : tree);
    rc = lyd_eval_xpath4(node, tree, NULL, str_of(step, "xpath"), LY_VALUE_JSON, NULL, vars,
            &type, &set, &str, &num, &b);
    lyxp_vars_free(vars);
    collect(ctx, diag, "xpath");
    if (rc) {
        cJSON_AddNullToObject(out, "result");
        ly_set_free(set, NULL);
        free(str);
        return rc;
    }
    res = cJSON_AddObjectToObject(out, "result");
    switch (type) {
    case LY_XPATH_NODE_SET: {
        cJSON *nodes = cJSON_AddArrayToObject(res, "nodes");
        cJSON_AddStringToObject(res, "type", "node-set");
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
    return LY_SUCCESS;
}

/* diff_reverse: lyd_diff_reverse_all of the diff register replaces it */
static LY_ERR
step_diff_reverse(struct ly_ctx *ctx, struct lyd_node **diff, cJSON *diag)
{
    struct lyd_node *res = NULL;
    LY_ERR rc = lyd_diff_reverse_all(*diff, &res);

    collect(ctx, diag, "diff"); if (*diff && LYD_CTX(*diff) != ctx) collect(LYD_CTX(*diff), diag, "diff");
    if (!rc) {
        lyd_free_all(*diff);
        *diff = res;
    }
    return rc;
}

static void
op_sequence(const cJSON *req)
{
    int ok;
    struct ly_ctx *ctx = build_ctx(req, &ok, 0), *sctx;
    const cJSON *steps = cJSON_GetObjectItemCaseSensitive(req, "steps"), *step;
    struct lyd_node *tree = NULL, *diff = NULL, *detached = NULL;
    cJSON *out;
    LY_ERR rc = LY_SUCCESS;
    int i = 0, failed = -1;

    if (!ok) {
        cJSON_AddStringToObject(resp, "verdict", "schema-error");
        return;
    }
    if (!cJSON_IsArray(steps)) {
        die("sequence needs a steps array%s", NULL);
    }
    cJSON_ArrayForEach(step, steps) {
        check_step(ctx, step);
    }
    out = cJSON_AddArrayToObject(resp, "steps");
    cJSON_ArrayForEach(step, steps) {
        const char *what = str_of(step, "do");
        cJSON *s = cJSON_CreateObject(), *diag, *d;

        cJSON_AddItemToArray(out, s);
        cJSON_AddStringToObject(s, "do", what);
        if (rc) {
            cJSON_AddBoolToObject(s, "skipped", 1);
            i++;
            continue;
        }
        diag = cJSON_AddArrayToObject(s, "diagnostics");
        /* the retained tree's context: a dup step with a target moves the tree into another one */
        sctx = tree ? (struct ly_ctx *)LYD_CTX(tree) : ctx;
        if (!strcmp(what, "parse")) {
            rc = step_parse(sctx, step, &tree, diag);
        } else if (!strcmp(what, "validate")) {
            rc = step_validate(sctx, step, &tree, diag, s);
        } else if (!strcmp(what, "edit")) {
            rc = step_edit(sctx, step, &tree, detached, diag);
        } else if (!strcmp(what, "link")) {
            rc = lyd_leafref_link_node_tree(tree);
            collect(sctx, diag, "link");
        } else if (!strcmp(what, "links")) {
            cJSON_AddItemToObject(s, "leafref_links", links_json(
                    cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "detached")) ? detached : tree));
        } else if (!strcmp(what, "dup")) {
            rc = step_dup(sctx, step, &tree, detached, diag);
        } else if (!strcmp(what, "compare")) {
            rc = step_compare(sctx, step, tree, diag, s);
        } else if (!strcmp(what, "insert")) {
            rc = step_insert(sctx, step, &tree, &detached, diag);
        } else if (!strcmp(what, "change_term")) {
            /* lyd_change_term(_canon) of the node at "node": "change" is its rc; LY_EEXIST (only the default
             * flag changed) and LY_ENOT (nothing changed) are results, not failures */
            struct lyd_node *n;
            const char *expr = str_of(step, "node_xpath");

            if (expr) {
                struct ly_set *set = NULL;

                if (!tree || lyd_find_xpath(tree, expr, &set) || set->count != 1) {
                    die("change_term node_xpath %s must select exactly one node", expr);
                }
                n = set->dnodes[0];
                ly_set_free(set, NULL);
            } else {
                n = node_at(tree, str_of(step, "node"), "change_term node %s not found");
            }
            const cJSON *child = cJSON_GetObjectItemCaseSensitive(step, "child");

            if (child) {
                int index = child->valueint;

                n = lyd_child(n);
                while (n && --index) {
                    n = n->next;
                }
                if (!n) {
                    die("change_term child does not exist%s", NULL);
                }
            }
            LY_ERR r = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "canon")) ?
                    lyd_change_term_canon(n, str_of(step, "value")) : lyd_change_term(n, str_of(step, "value"));

            collect(sctx, diag, "edit");
            /* a changed value may move the node among its siblings, the first top-level one too */
            tree = lyd_first_sibling(tree);
            cJSON_AddItemToObject(s, "change", code_json(r));
            rc = (r == LY_EEXIST) || (r == LY_ENOT) ? LY_SUCCESS : r;
        } else if (!strcmp(what, "diff")) {
            rc = step_diff(sctx, step, tree, detached, &diff, diag, s);
        } else if (!strcmp(what, "diff_parse")) {
            rc = step_diff_parse(sctx, step, &diff, diag);
        } else if (!strcmp(what, "diff_merge")) {
            rc = step_diff_merge(sctx, step, &diff, detached, diag);
        } else if (!strcmp(what, "diff_reverse")) {
            rc = step_diff_reverse(sctx, &diff, diag);
        } else if (!strcmp(what, "trim")) {
            rc = step_trim(sctx, step, &tree, diag);
        } else if (!strcmp(what, "xpath")) {
            rc = step_xpath(sctx, step, tree, detached, diag, s);
        } else if (!strcmp(what, "unlink")) {
            const char *dpath = str_of(step, "diff_node"), *path = str_of(step, "node");
            struct lyd_node *node = dpath ? node_at(diff, dpath, "unlink diff_node %s not found") :
                    (!strcmp(path, "@top") ? tree : node_at(tree, path, "unlink node %s not found"));
            if (!node) die("unlink node %s not found", path);

            struct ly_ctx *node_ctx = LYD_CTX(node);
            struct lyd_node *next = node->next;
            rc = lyd_unlink_tree(node);
            collect(node_ctx, diag, "edit");
            if (rc == LY_SUCCESS) {
                lyd_free_tree(detached); detached = node;
                if (dpath && diff == node) diff = next;
                else if (tree == node) tree = next;
            }
        } else if (!strcmp(what, "find_meta")) {
            const char *path = str_of(step, "node");
            struct lyd_node *node = path ? node_at(tree, path, "find_meta node %s not found") : detached;
            struct lyd_meta *meta;

            if (!node) {
                die("find_meta needs an attached node or a retained unlinked node%s", NULL);
            }
            meta = lyd_find_meta(node->meta, NULL, str_of(step, "name"));
            collect(node->meta ? node->meta->annotation->module->ctx : LYD_CTX(node), diag, "find_meta"); /* lyd_find_meta logs against its first metadata's context. */
            cJSON_AddBoolToObject(s, "found", meta != NULL);
        } else if (!strcmp(what, "diff_apply")) {
            rc = lyd_diff_apply_module(&tree, diff, module_of(sctx, step), NULL, NULL);
            collect(sctx, diag, "diff"); if (diff && LYD_CTX(diff) != sctx) collect(LYD_CTX(diff), diag, "diff");
        } else if (!strcmp(what, "print_xml")) {
            cJSON *t = cJSON_CreateObject();
            char *json = NULL, *xml = NULL;

            lyd_print_mem(&json, tree, LYD_JSON, LYD_PRINT_SIBLINGS | LYD_PRINT_WD_EXPLICIT);
            add_opt_str(t, "json", json ? json : "");
            free(json);
            rc = lyd_print_mem(&xml, tree, LYD_XML, LYD_PRINT_SIBLINGS | LYD_PRINT_WD_EXPLICIT);
            if (!rc) {
                add_opt_str(t, "xml", xml ? xml : "");
            }
            free(xml);
            if (rc) {
                cJSON_Delete(t);
                cJSON_AddNullToObject(s, "tree");
            } else {
                cJSON_AddItemToObject(s, "tree", t);
            }
            collect(sctx, diag, "data");
        } else if (!strcmp(what, "any_direct")) {
            cJSON *direct = cJSON_AddObjectToObject(s, "direct");
            rc = step_any_direct(sctx, step, direct, diag);
        } else if (!strcmp(what, "detached_dump")) { struct lyd_node *probe = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "child")) && detached ? lyd_child(detached) : detached; if (!probe) die("detached_dump retained detached node not found%s", NULL); char *path = lyd_path(probe, LYD_PATH_STD, NULL, 0); add_opt_str(s, "detached_path", path); free(path); cJSON_AddItemToObject(s, "detached_tree", print_tree(probe, wd_of(step), 0)); collect(LYD_CTX(probe), diag, "dump"); } else {
            cJSON_AddItemToObject(s, "tree", print_tree(tree, wd_of(step), 1));
        }
        /* an error-level item means failure even if the call returned success (void APIs such as
         * lyd_free_tree refusing a list key) */
        cJSON_ArrayForEach(d, diag) {
            if (!rc && !strcmp(cJSON_GetObjectItemCaseSensitive(d, "level")->valuestring, "error")) {
                rc = (LY_ERR)cJSON_GetObjectItemCaseSensitive(cJSON_GetObjectItemCaseSensitive(d, "code"), "err")->valueint;
            }
        }
        cJSON_AddItemToObject(s, "rc", code_json(rc));
        cJSON_AddItemToObject(s, "typed", typed_json(tree));
        if (!strncmp(what, "diff", 4)) {
            /* the diff register after the step, printed as op diff prints a diff */
            cJSON_AddItemToObject(s, "diff", diff ? print_tree(diff, LYD_PRINT_WD_ALL, 1) : cJSON_CreateNull());
            cJSON_AddItemToObject(s, "diff_typed", typed_json(diff));
        }
        if (rc) {
            failed = i;
        }
        i++;
    }
    set_verdict(rc);
    if (failed >= 0) {
        cJSON_AddNumberToObject(resp, "failed_step", failed);
    } else {
        cJSON_AddNullToObject(resp, "failed_step");
    }
}

/* ---------- atoms: the schema nodes an expression or a path needs ---------- */

static void
op_atoms(const cJSON *req)
{
    int ok;
    struct ly_ctx *ctx = build_ctx(req, &ok, 0);
    const char *xp = str_of(req, "xpath"), *path = str_of(req, "path"), *cpath = str_of(req, "context_path");
    uint32_t opts = flags_of(req, "atom_options", atom_flags);
    const struct lysc_node *cnode = NULL;
    struct ly_set *set = NULL;
    cJSON *diag = cJSON_AddArrayToObject(resp, "diagnostics"), *atoms;
    LY_ERR rc;

    if (!ok) {
        cJSON_AddStringToObject(resp, "verdict", "schema-error");
        return;
    }
    if (!xp == !path) {
        die("atoms needs exactly one of xpath and path%s", NULL);
    }
    if (cpath) {
        /* the schema context node; output nodes for an output query */
        cnode = lys_find_path(ctx, NULL, cpath, (opts & LYS_FIND_XP_OUTPUT) ? 1 : 0);
        collect(ctx, diag, "context_path");
        if (!cnode) {
            die("context_path %s not found", cpath);
        }
    }
    if (xp) {
        rc = lys_find_xpath_atoms(ctx, cnode, xp, opts, &set);
    } else {
        rc = lys_find_path_atoms(ctx, cnode, path, (opts & LYS_FIND_XP_OUTPUT) ? 1 : 0, &set);
    }
    collect(ctx, diag, xp ? "xpath" : "path");
    set_verdict(rc);
    if (rc) {
        /* lys_find_xpath_atoms leaves an empty set behind, lys_find_path_atoms none */
        ly_set_free(set, NULL);
        cJSON_AddNullToObject(resp, "atoms");
        return;
    }
    atoms = cJSON_AddArrayToObject(resp, "atoms");
    for (uint32_t i = 0; i < set->count; i++) {
        char *p = lysc_path(set->snodes[i], LYSC_PATH_LOG, NULL, 0);

        cJSON_AddItemToArray(atoms, cJSON_CreateString(p));
        free(p);
    }
    ly_set_free(set, NULL);
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
    cJSON_AddNumberToObject(resp, "protocol", 2);
    {
        struct utsname un;
        cJSON_AddStringToObject(resp, "arch", uname(&un) == 0 ? un.machine : "unknown");
    }
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
    } else if (!strcmp(op, "sequence")) {
        op_sequence(req);
    } else if (!strcmp(op, "atoms")) {
        op_atoms(req);
    } else {
        die("unknown op %s", op);
    }

    out = cJSON_Print(resp);
    printf("%s\n", out);
    return 0;
}

/* Validate the compare step's second source. During execution, parse ordinary data or construct
 * one opaque root so values rejected by the XML parser can still exercise lyd_compare_single. */
static LY_ERR
compare_other(const struct ly_ctx *ctx, const cJSON *step, struct lyd_node **other, cJSON *diag)
{
    const cJSON *io = cJSON_GetObjectItemCaseSensitive(step, "insert_opaq"), *xml;
    const char *data = input_of(step, "data");
    struct dparams p;
    LY_ERR rc;

    if (!data && !io) {
        die("compare step needs data%s", NULL);
    }
    if (data && io) {
        die("compare step needs exactly one of data, insert_opaq%s", NULL);
    }
    if (!io) {
        if (!other) {
            return LY_SUCCESS;
        }
        dparams_of(step, &p);
        return parse_one((struct ly_ctx *)ctx, &p, data, NULL, other, diag, "parse");
    }
    if (!cJSON_IsObject(io) || !str_of(io, "name") || !str_of(io, "module")) {
        die("compare insert_opaq needs an object with name and module%s", NULL);
    }
    keys_only(io, "name value prefix module xml", "unknown key \"%s\" in compare insert_opaq");
    str_of(io, "value");
    str_of(io, "prefix");
    xml = cJSON_GetObjectItemCaseSensitive(io, "xml");
    if (xml && !cJSON_IsBool(xml)) {
        die("insert_opaq xml must be a boolean%s", NULL);
    }
    if (!other) {
        return LY_SUCCESS;
    }
    if (cJSON_IsTrue(xml)) {
        rc = lyd_new_opaq2(NULL, ctx, str_of(io, "name"), str_of(io, "value"), str_of(io, "prefix"),
                str_of(io, "module"), other);
    } else {
        rc = lyd_new_opaq(NULL, ctx, str_of(io, "name"), str_of(io, "value"), str_of(io, "prefix"),
                str_of(io, "module"), other);
    }
    collect(ctx, diag, "edit");
    return rc;
}

/* lyd_dup_r links explicit copies through lyd_insert_node. Its default-flag deletion must
 * reach an any parent supplied by the caller, unlike lyd_any_copy_value's internal forest. */
static LY_ERR
step_dup_into_default_any_parent(struct ly_ctx *ctx, const struct lys_module *ac, cJSON *out, cJSON *diag)
{
    struct lyd_node *first = NULL, *second = NULL, *outer = NULL, *parent = NULL, *made = NULL;
    const char *kinds[] = {"data", "xml"};
    cJSON *results = cJSON_AddObjectToObject(out, "default_after_dup");
    LY_ERR rc;

    rc = lyd_new_term(NULL, ac, "target", "value", 0, &first);
    if (!rc) rc = lyd_new_term(NULL, ac, "item", "second", 0, &second);
    if (!rc) rc = lyd_insert_sibling(first, second, &first);
    for (size_t k = 0; !rc && k < 2; ++k) {
        cJSON *kind = cJSON_AddObjectToObject(results, kinds[k]);
        for (size_t s = 0; !rc && s < 2; ++s) {
            cJSON *result = cJSON_AddObjectToObject(kind, s ? "siblings" : "single");

            outer = parent = made = NULL;
            rc = lyd_new_inner(NULL, ac, "outer", 0, &outer);
            if (!rc) rc = lyd_new_any(outer, ac, kinds[k], NULL, NULL, 0, 0, &parent);
            if (!rc) {
                outer->flags |= LYD_DEFAULT;
                parent->flags |= LYD_DEFAULT;
                if (s) {
                    rc = lyd_dup_siblings(first, parent, 0, &made);
                } else {
                    rc = lyd_dup_single(first, parent, 0, &made);
                }
            }
            if (!rc) {
                cJSON_AddBoolToObject(result, "parent_default", !!(parent->flags & LYD_DEFAULT));
                cJSON_AddBoolToObject(result, "ancestor_default", !!(outer->flags & LYD_DEFAULT));
            }
            lyd_free_tree(outer);
        }
    }
    lyd_free_all(first);
    collect(ctx, diag, "edit");
    return rc;
}

/* Exercise lyd_compare_siblings_'s LHS lookup rule across schema contexts. Both
 * anydata payloads lack children_ht, so the RHS scan must include user-ordered
 * instances (and, for item, state leaf-list instances). */
static LY_ERR
check_equal_order_contexts(struct ly_ctx *ctx, const cJSON *step, cJSON *diag)
{
    const cJSON *target = cJSON_GetObjectItemCaseSensitive(step, "target");
    const struct lys_module *system = ly_ctx_get_module_implemented(ctx, "ac-r6"), *user;
    struct ly_ctx *uctx;
    cJSON *scratch;
    LY_ERR rc = LY_SUCCESS;
    int ok;

    if (!target) die("equal_repeated_collisions needs target%s", NULL);
    scratch = cJSON_CreateObject();
    uctx = build_ctx_in(target, &ok, 0, scratch);
    cJSON_Delete(scratch);
    if (!ok || !system || !(user = ly_ctx_get_module_implemented(uctx, "ac-r6"))) {
        die("equal_repeated_collisions context failed%s", NULL);
    }

    for (int list = 0; list < 2 && !rc; ++list) {
        struct lyd_node *left = NULL, *right = NULL, *sys_any = NULL, *usr_any = NULL, *n = NULL;
        const char *values[] = {"b", "a"};

        for (size_t i = 0; i < 2 && !rc; ++i) {
            rc = list ? lyd_new_list(NULL, system, "entry", 0, &n, values[i]) :
                    lyd_new_term(NULL, system, "item", values[i], 0, &n);
            if (!rc) rc = lyd_insert_sibling(left, n, &left);
        }
        for (size_t i = 0; i < 2 && !rc; ++i) {
            rc = list ? lyd_new_list(NULL, user, "entry", 0, &n, values[i]) :
                    lyd_new_term(NULL, user, "item", values[i], 0, &n);
            if (!rc) rc = lyd_insert_sibling(right, n, &right);
        }
        if (!rc) {
            rc = lyd_new_any(NULL, system, "data", left, NULL, 0, LYD_NEW_ANY_USE_VALUE, &sys_any);
            if (!rc) left = NULL;
        }
        if (!rc) {
            rc = lyd_new_any(NULL, user, "data", right, NULL, 0, LYD_NEW_ANY_USE_VALUE, &usr_any);
            if (!rc) right = NULL;
        }
        if (!rc && (lyd_compare_single(sys_any, usr_any, 0) != LY_SUCCESS ||
                lyd_compare_single(usr_any, sys_any, 0) != LY_ENOT)) {
            die("cross-context comparison ordering failed for %s", list ? "list" : "leaf-list");
        }
        lyd_free_all(left);
        lyd_free_all(right);
        lyd_free_tree(sys_any);
        lyd_free_tree(usr_any);
    }
    collect(ctx, diag, "edit");
    collect(uctx, diag, "edit");
    ly_ctx_destroy(uctx);
    return rc;
}

/* A mixed anyxml can be built by duplicating a child under an existing text value.
 * The sequence edit step cannot pass this mutated node as a merge source. */
static LY_ERR
step_merge_mixed_into_tree(struct ly_ctx *ctx, const struct lys_module *ac, cJSON *out, cJSON *diag)
{
    struct lyd_node *tree = NULL, *mixed = NULL, *child = NULL, *copied = NULL;
    LY_ERR rc = LY_SUCCESS;

    rc = lyd_new_any(NULL, ac, "xml", NULL, NULL, 0, 0, &tree);
    if (!rc) rc = lyd_new_term(NULL, ac, "target", "value", 0, &child);
    if (!rc) rc = lyd_dup_single(child, tree, 0, &copied);
    lyd_free_tree(child);
    child = NULL;
    if (!rc) rc = lyd_new_any(NULL, ac, "xml", NULL, "plain text", LYD_VALHINT_STRING, 0, &mixed);
    if (!rc) rc = lyd_new_term(NULL, ac, "target", "value", 0, &child);
    if (!rc) rc = lyd_dup_single(child, mixed, 0, &copied);
    lyd_free_tree(child);
    if (!rc) {
        cJSON_AddItemToObject(out, "compare", code_json(lyd_compare_single(mixed, tree, 0)));
        cJSON_AddItemToObject(out, "compare_reverse", code_json(lyd_compare_single(tree, mixed, 0)));
        cJSON_AddItemToObject(out, "merge_rc", code_json(lyd_merge_siblings(&tree, mixed, 0)));
    }
    lyd_free_all(tree);
    lyd_free_all(mixed);
    collect(ctx, diag, "edit");
    return rc;
}

/* Regression: anydata payload roots retain distinct schema parents, and a
 * system-ordered leaf-list after the first root still compares equal. */
static LY_ERR
step_equal_mixed_parent_payload(struct ly_ctx *ctx, const struct lys_module *ac, cJSON *out, cJSON *diag)
{
    struct lyd_node *outer = NULL, *first = NULL, *inner = NULL, *any = NULL, *copy = NULL;
    LY_ERR rc;

    rc = lyd_new_term(NULL, ac, "item", "same", 0, &first);
    if (!rc) rc = lyd_new_inner(NULL, ac, "outer", 0, &outer);
    if (!rc) rc = lyd_new_term(outer, NULL, "item", "same", 0, &inner);
    if (!rc) {
        lyd_unlink_tree(inner); /* retain its schema parent while making it a payload root */
        rc = lyd_insert_sibling(first, inner, &first);
        if (!rc) inner = NULL;
    }
    if (!rc) {
        lyd_free_tree(outer);
        outer = NULL;
        rc = lyd_new_any(NULL, ac, "data", first, NULL, 0, LYD_NEW_ANY_USE_VALUE, &any);
        if (!rc) first = NULL;
    }
    if (!rc) rc = lyd_dup_single(any, NULL, 0, &copy);
    if (!rc) cJSON_AddItemToObject(out, "compare", code_json(lyd_compare_single(any, copy, 0)));
    lyd_free_tree(outer);
    lyd_free_all(first);
    lyd_free_tree(inner);
    lyd_free_tree(any);
    lyd_free_tree(copy);
    collect(ctx, diag, "edit");
    return rc;
}

/* lyd_find_sibling_first rejects a target when the first sibling has a
 * different schema parent. Duplication under an opaque node can create this
 * otherwise-invalid mixed-parent sibling forest, so merging /b/l must not
 * reuse the existing /b/l after the leading /a/x. */
static LY_ERR
step_merge_opaque_mixed_schema_parents(struct ly_ctx *ctx, cJSON *out, cJSON *diag)
{
    const struct lys_module *mod = ly_ctx_get_module_implemented(ctx, "ac-r10");
    struct lyd_node *a = NULL, *b = NULL, *x = NULL, *list = NULL;
    struct lyd_node *dst = NULL, *src = NULL, *made = NULL, *iter;
    size_t lists = 0, children = 0;
    LY_ERR rc = LY_SUCCESS;

    if (!mod) {
        die("merge_opaque_mixed_schema_parents needs ac-r10%s", NULL);
    }
    rc = lyd_new_inner(NULL, mod, "a", 0, &a);
    if (!rc) rc = lyd_new_term(a, NULL, "x", "x", 0, &x);
    if (!rc) rc = lyd_new_inner(NULL, mod, "b", 0, &b);
    if (!rc) rc = lyd_new_list(b, NULL, "l", 0, &list);
    if (!rc) rc = lyd_new_term(list, NULL, "v", "same", 0, NULL);
    if (!rc) rc = lyd_new_opaq(NULL, ctx, "parent", "", NULL, "free", &dst);
    if (!rc) rc = lyd_dup_single(list, dst, LYD_DUP_RECURSIVE, &made);
    if (!rc) rc = lyd_dup_single(x, dst, 0, &made);
    if (!rc) rc = lyd_new_opaq(NULL, ctx, "parent", "", NULL, "free", &src);
    if (!rc) rc = lyd_dup_single(list, src, LYD_DUP_RECURSIVE, &made);
    if (!rc) {
        iter = lyd_child(dst);
        cJSON_AddStringToObject(out, "first_child", iter->schema->name);
        cJSON_AddStringToObject(out, "first_schema_parent", lysc_data_parent(iter->schema)->name);
    }
    if (!rc) rc = lyd_merge_siblings(&dst, src, 0);
    if (!rc) {
        LY_LIST_FOR(lyd_child(dst), iter) {
            ++children;
            if (iter->schema && !strcmp(iter->schema->name, "l")) {
                ++lists;
            }
        }
        cJSON_AddNumberToObject(out, "children", children);
        cJSON_AddNumberToObject(out, "list_instances", lists);
    }
    lyd_free_tree(a);
    lyd_free_tree(b);
    lyd_free_all(dst);
    lyd_free_all(src);
    collect(ctx, diag, "edit");
    return rc;
}

/* Public boolean equality has no error channel. Pin libyang's verdict for a
 * no-table anydata payload whose canonical date collisions force more than
 * the Go port's private stored-work allowance of candidate comparisons. */
static LY_ERR
step_equal_repeated_date_collisions(struct ly_ctx *ctx, const struct lys_module *ac, cJSON *out, cJSON *diag)
{
    const struct lys_module *mod = ly_ctx_get_module_implemented(ctx, "ac-r10");
    const char *values[] = {"2024-01-01+01:00", "2023-12-31+00:00"};
    struct lyd_node *children = NULL, *node = NULL, *any = NULL;
    LY_ERR rc = LY_SUCCESS;

    if (!mod) {
        die("equal_repeated_date_collisions needs ac-r10%s", NULL);
    }
    for (size_t v = 0; !rc && v < 2; ++v) {
        for (size_t i = 0; !rc && i < 6000; ++i) {
            rc = lyd_new_term(NULL, mod, "dates", values[v], 0, &node);
            if (!rc) rc = lyd_insert_sibling(children, node, &children);
            if (!rc) node = NULL;
        }
    }
    if (!rc) {
        rc = lyd_new_any(NULL, ac, "data", children, NULL, 0, LYD_NEW_ANY_USE_VALUE, &any);
        if (!rc) children = NULL;
    }
    if (!rc) {
        cJSON_AddItemToObject(out, "compare_rc", code_json(lyd_compare_single(any, any, 0)));
        cJSON_AddNumberToObject(out, "copies_per_value", 6000);
    }
    lyd_free_tree(node);
    lyd_free_all(children);
    lyd_free_tree(any);
    collect(ctx, diag, "edit");
    return rc;
}
