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
    {"empty_leaf_list", LYD_PRINT_EMPTY_LEAF_LIST},
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

/* sequence step compare: LYD_COMPARE_* */
static const struct flag compare_flags[] = {
    {"full_recursion", LYD_COMPARE_FULL_RECURSION},
    {"defaults", LYD_COMPARE_DEFAULTS},
    {"opaq", LYD_COMPARE_OPAQ},
    {NULL, 0}
};

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
    if ((p->optype != LYD_TYPE_DATA_YANG) && u && !strcmp(u, "skip")) {
        die("unknown \"skip\" is not supported for operations%s", NULL);
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

    rc = lyd_eval_xpath4(cnode, tree, cur, expr, LY_VALUE_JSON, NULL, vars, &t, &set, &str, &num, &b);
    lyxp_vars_free(vars);
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
check_step(const cJSON *step)
{
    const char *what = str_of(step, "do");
    struct dparams p;

    if (!cJSON_IsObject(step) || !what) {
        die("step without \"do\"%s", NULL);
    }
    keys_only(step, !strcmp(what, "parse") ? "do format data_type data data_file unknown parse_only "
            "parse_options validate_options" : !strcmp(what, "validate") ? "do data_type validate_options" :
            !strcmp(what, "edit") ? "do merge merge_file set delete insert_term insert_inner new_meta free_meta format data_type "
            "unknown parse_options" :
            !strcmp(what, "dump") ? "do with_defaults" : !strcmp(what, "dup") ? "do node parent options siblings" :
            !strcmp(what, "compare") ? "do format data_type data data_file unknown parse_only parse_options "
            "validate_options first second options" :
            !strcmp(what, "diff") ? "do format data_type data data_file unknown parse_only parse_options validate_options "
            "node data_node single options merge merge_options" :
            !strcmp(what, "diff_parse") ? "do format data_type data data_file unknown parse_options" :
            !strcmp(what, "diff_merge") ? "do format data_type data data_file unknown parse_options options module "
            "src_node parent" :
            !strcmp(what, "diff_apply") ? "do module" : "do",
            "unknown key \"%s\" in a sequence step");
    if (!strcmp(what, "edit")) {
        /* the parse options belong to merge only */
        keys_only(step, cJSON_GetObjectItemCaseSensitive(step, "set") ? "do set" :
                cJSON_GetObjectItemCaseSensitive(step, "delete") ? "do delete" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_term") ? "do insert_term" :
                cJSON_GetObjectItemCaseSensitive(step, "insert_inner") ? "do insert_inner" :
                cJSON_GetObjectItemCaseSensitive(step, "new_meta") ? "do new_meta" :
                cJSON_GetObjectItemCaseSensitive(step, "free_meta") ? "do free_meta" :
                "do merge merge_file format data_type unknown parse_options",
                "key \"%s\" not allowed in this edit step");
    }
    if (!strcmp(what, "parse")) {
        dparams_of(step, &p);
        if (p.optype != LYD_TYPE_DATA_YANG) {
            die("sequence supports datastore data types only%s", NULL);
        }
        if (!input_of(step, "data")) {
            die("parse step needs data%s", NULL);
        }
    } else if (!strcmp(what, "validate")) {
        dparams_of(step, &p);
    } else if (!strcmp(what, "edit")) {
        const char *merge = input_of(step, "merge"), *del = str_of(step, "delete");
        const cJSON *set = cJSON_GetObjectItemCaseSensitive(step, "set");
        const cJSON *ins = cJSON_GetObjectItemCaseSensitive(step, "insert_term");
        const cJSON *inn = cJSON_GetObjectItemCaseSensitive(step, "insert_inner");
        const cJSON *nm = cJSON_GetObjectItemCaseSensitive(step, "new_meta");
        const cJSON *fm = cJSON_GetObjectItemCaseSensitive(step, "free_meta");

        if (!!merge + !!del + !!set + !!ins + !!inn + !!nm + !!fm != 1) {
            die("edit step needs exactly one of merge, merge_file, set, delete, insert_term, insert_inner, new_meta, "
                    "free_meta%s", NULL);
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
        if (ins || inn) {
            const cJSON *o = ins ? ins : inn;

            if (!cJSON_IsObject(o) || !str_of(o, "name") || (!!str_of(o, "module") + !!str_of(o, "parent") != 1)) {
                die("insert_term/insert_inner need an object with name and exactly one of module, parent%s", NULL);
            }
            keys_only(o, ins ? "module parent name value" : "module parent name", "unknown key \"%s\" in insert_term/insert_inner");
            if (ins && !str_of(o, "value")) {
                die("insert_term needs a value%s", NULL);
            }
        }
        if (merge) {
            dparams_of(step, &p);
        }
        if (set && (!cJSON_IsObject(set) || !str_of(set, "path"))) {
            die("set needs an object with path%s", NULL);
        }
        if (set) {
            keys_only(set, "path value", "unknown key \"%s\" in set");
            str_of(set, "value");
        }
    } else if (!strcmp(what, "dump")) {
        wd_of(step);
    } else if (!strcmp(what, "dup")) {
        const cJSON *sib = cJSON_GetObjectItemCaseSensitive(step, "siblings");

        if (!str_of(step, "node")) {
            die("dup step needs a node path%s", NULL);
        }
        str_of(step, "parent");
        flags_of(step, "options", dup_flags);
        if (sib && !cJSON_IsBool(sib)) {
            die("dup siblings must be a boolean%s", NULL);
        }
    } else if (!strcmp(what, "compare")) {
        dparams_of(step, &p);
        if (p.optype != LYD_TYPE_DATA_YANG) {
            die("sequence supports datastore data types only%s", NULL);
        }
        if (!input_of(step, "data")) {
            die("compare step needs data%s", NULL);
        }
        str_of(step, "first");
        str_of(step, "second");
        flags_of(step, "options", compare_flags);
    } else if (!strcmp(what, "diff") || !strcmp(what, "diff_parse") || !strcmp(what, "diff_merge")) {
        const cJSON *b;

        dparams_of(step, &p);
        if (p.optype != LYD_TYPE_DATA_YANG) {
            die("sequence supports datastore data types only%s", NULL);
        }
        if (strcmp(what, "diff") && !input_of(step, "data")) {
            die("%s step needs data", what);
        }
        if (!strcmp(what, "diff")) {
            flags_of(step, "options", diff_flags);
            flags_of(step, "merge_options", diff_merge_flags);
            str_of(step, "node");
            str_of(step, "data_node");
            if (((b = cJSON_GetObjectItemCaseSensitive(step, "single")) && !cJSON_IsBool(b)) ||
                    ((b = cJSON_GetObjectItemCaseSensitive(step, "merge")) && !cJSON_IsBool(b))) {
                die("diff single and merge must be booleans%s", NULL);
            }
        } else if (!strcmp(what, "diff_merge")) {
            flags_of(step, "options", diff_merge_flags);
            str_of(step, "module");
            if (str_of(step, "parent") && !str_of(step, "src_node")) {
                die("diff_merge parent needs src_node%s", NULL);
            }
            if (str_of(step, "src_node") && str_of(step, "module")) {
                die("diff_merge takes module or src_node, not both%s", NULL);
            }
        }
    } else if (!strcmp(what, "diff_apply")) {
        str_of(step, "module");
    } else if (strcmp(what, "link") && strcmp(what, "links") && strcmp(what, "diff_reverse")) {
        die("unknown step %s", what);
    }
    return what;
}

/* lyd_dup_single / lyd_dup_siblings of the node at "node" into the node at "parent", or, without
 * a parent, the duplicate (with its duplicated parents) replacing the tree */
static LY_ERR
step_dup(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag)
{
    const char *npath = str_of(step, "node"), *ppath = str_of(step, "parent");
    struct lyd_node *node = NULL, *parent = NULL, *dup = NULL;
    uint32_t opts = flags_of(step, "options", dup_flags);
    LY_ERR rc;

    if (!*tree || lyd_find_path(*tree, npath, 0, &node)) {
        die("dup node %s not found", npath);
    }
    if (ppath && lyd_find_path(*tree, ppath, 0, &parent)) {
        die("dup parent %s not found", ppath);
    }
    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "siblings"))) {
        rc = lyd_dup_siblings(node, parent, opts, &dup);
    } else {
        rc = lyd_dup_single(node, parent, opts, &dup);
    }
    collect(ctx, diag, "edit");
    if (!rc && !parent) {
        while (dup->parent) {
            dup = dup->parent;
        }
        lyd_free_all(*tree);
        *tree = lyd_first_sibling(dup);
    }
    return rc;
}

/* lyd_compare_single of the node at "first" in the tree and the node at "second" in a second tree
 * parsed from the step's data as by a parse step ("first"/"second" omitted: the first top-level
 * node); "compare" is its rc (LY_SUCCESS equal, LY_ENOT not), the step's rc that of the parse */
static LY_ERR
step_compare(struct ly_ctx *ctx, const cJSON *step, struct lyd_node *tree, cJSON *diag, cJSON *s)
{
    const char *p1 = str_of(step, "first"), *p2 = str_of(step, "second");
    struct lyd_node *other = NULL, *n1 = tree, *n2;
    struct dparams p;
    LY_ERR rc;

    dparams_of(step, &p);
    rc = parse_one(ctx, &p, input_of(step, "data"), NULL, &other, diag, "parse");
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
    lyd_free_all(other);
    return rc;
}

static LY_ERR
step_parse(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag)
{
    struct dparams p;
    struct lyd_node *t;
    LY_ERR rc;

    dparams_of(step, &p);
    rc = parse_one(ctx, &p, input_of(step, "data"), NULL, &t, diag, "parse");
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
    struct lyd_node *diff = NULL;
    char *str = NULL;
    LY_ERR rc;

    dparams_of(step, &p);
    rc = lyd_validate_all(tree, ctx, p.vopts, &diff);
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
step_edit(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **tree, cJSON *diag)
{
    const char *merge = input_of(step, "merge"), *del = str_of(step, "delete");
    const cJSON *set = cJSON_GetObjectItemCaseSensitive(step, "set");
    const cJSON *ins = cJSON_GetObjectItemCaseSensitive(step, "insert_term");
    const cJSON *inn = cJSON_GetObjectItemCaseSensitive(step, "insert_inner");
    const cJSON *nm = cJSON_GetObjectItemCaseSensitive(step, "new_meta");
    const cJSON *fm = cJSON_GetObjectItemCaseSensitive(step, "free_meta");
    struct lyd_node *node = NULL;
    LY_ERR rc = LY_SUCCESS;

    if (nm || fm) {
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
    } else if (ins || inn) {
        /* lyd_new_term / lyd_new_inner + lyd_insert_sibling (top level) or a child of "parent" */
        const cJSON *o = ins ? ins : inn;
        const char *mname = str_of(o, "module"), *ppath = str_of(o, "parent");
        const struct lys_module *mod = mname ? ly_ctx_get_module_implemented(ctx, mname) : NULL;
        struct lyd_node *parent = NULL;

        if (mname && !mod) {
            die("module %s is not implemented", mname);
        }
        if (ppath && (!*tree || lyd_find_path(*tree, ppath, 0, &parent))) {
            die("insert parent %s not found", ppath);
        }
        if (ins) {
            rc = lyd_new_term(parent, mod, str_of(o, "name"), str_of(o, "value"), 0, &node);
        } else {
            rc = lyd_new_inner(parent, mod, str_of(o, "name"), 0, &node);
        }
        if (!rc && !parent) {
            rc = lyd_insert_sibling(*tree, node, tree);
        }
        if (*tree) {
            *tree = lyd_first_sibling(*tree);
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
        rc = lyd_new_path(*tree, ctx, str_of(set, "path"), str_of(set, "value"), LYD_NEW_PATH_UPDATE, &node);
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
 * diff: lyd_diff_siblings (lyd_diff_tree with single) of the retained tree (or its node at "node")
 * and the parsed data (or its node at "data_node"; no data: NULL); the result replaces the diff
 * register, or with merge is merged into it (lyd_diff_merge_all, merge_options) and reported as
 * new_diff
 */
static LY_ERR
step_diff(struct ly_ctx *ctx, const cJSON *step, struct lyd_node *tree, struct lyd_node **diff, cJSON *diag,
        cJSON *s)
{
    struct dparams p;
    const char *data = input_of(step, "data"), *npath = str_of(step, "node"), *dpath = str_of(step, "data_node");
    struct lyd_node *second = NULL, *first = tree, *snode, *res = NULL;
    uint16_t opts = flags_of(step, "options", diff_flags);
    LY_ERR rc = LY_SUCCESS;

    if (npath) {
        first = node_at(tree, npath, "diff node %s not found");
    }
    if (data) {
        dparams_of(step, &p);
        rc = parse_one(ctx, &p, data, NULL, &second, diag, "parse");
        if (rc) {
            return rc;
        }
    }
    snode = dpath ? node_at(second, dpath, "diff data_node %s not found") : second;
    if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "single"))) {
        rc = lyd_diff_tree(first, snode, opts, &res);
    } else {
        rc = lyd_diff_siblings(first, snode, opts, &res);
    }
    collect(ctx, diag, "diff");
    if (!rc && cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(step, "merge"))) {
        cJSON_AddItemToObject(s, "new_diff", res ? print_tree(res, LYD_PRINT_WD_ALL, 1) : cJSON_CreateNull());
        rc = lyd_diff_merge_all(diff, res, flags_of(step, "merge_options", diff_merge_flags));
        collect(ctx, diag, "diff");
        lyd_free_all(res);
    } else if (!rc) {
        lyd_free_all(*diff);
        *diff = res;
    }
    lyd_free_all(second);
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
step_diff_merge(struct ly_ctx *ctx, const cJSON *step, struct lyd_node **diff, cJSON *diag)
{
    struct dparams p;
    struct lyd_node *src = NULL, *parent = NULL;
    const char *spath = str_of(step, "src_node"), *ppath = str_of(step, "parent");
    uint16_t opts = flags_of(step, "options", diff_merge_flags);
    const struct lys_module *mod = module_of(ctx, step);
    LY_ERR rc;

    dparams_of(step, &p);
    p.popts |= LYD_PARSE_ONLY;
    rc = parse_one(ctx, &p, input_of(step, "data"), NULL, &src, diag, "parse");
    if (rc) {
        return rc;
    }
    if (spath) {
        if (ppath) {
            parent = node_at(*diff, ppath, "diff_merge parent %s not found");
        }
        rc = lyd_diff_merge_tree(diff, parent, node_at(src, spath, "diff_merge src_node %s not found"), NULL, NULL,
                opts);
    } else {
        rc = lyd_diff_merge_module(diff, src, mod, NULL, NULL, opts);
    }
    collect(ctx, diag, "diff");
    lyd_free_all(src);
    return rc;
}

/* diff_reverse: lyd_diff_reverse_all of the diff register replaces it */
static LY_ERR
step_diff_reverse(struct ly_ctx *ctx, struct lyd_node **diff, cJSON *diag)
{
    struct lyd_node *res = NULL;
    LY_ERR rc = lyd_diff_reverse_all(*diff, &res);

    collect(ctx, diag, "diff");
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
    struct ly_ctx *ctx = build_ctx(req, &ok, 0);
    const cJSON *steps = cJSON_GetObjectItemCaseSensitive(req, "steps"), *step;
    struct lyd_node *tree = NULL, *diff = NULL;
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
        check_step(step);
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
        if (!strcmp(what, "parse")) {
            rc = step_parse(ctx, step, &tree, diag);
        } else if (!strcmp(what, "validate")) {
            rc = step_validate(ctx, step, &tree, diag, s);
        } else if (!strcmp(what, "edit")) {
            rc = step_edit(ctx, step, &tree, diag);
        } else if (!strcmp(what, "link")) {
            rc = lyd_leafref_link_node_tree(tree);
            collect(ctx, diag, "link");
        } else if (!strcmp(what, "links")) {
            cJSON_AddItemToObject(s, "leafref_links", links_json(tree));
        } else if (!strcmp(what, "dup")) {
            rc = step_dup(ctx, step, &tree, diag);
        } else if (!strcmp(what, "compare")) {
            rc = step_compare(ctx, step, tree, diag, s);
        } else if (!strcmp(what, "diff")) {
            rc = step_diff(ctx, step, tree, &diff, diag, s);
        } else if (!strcmp(what, "diff_parse")) {
            rc = step_diff_parse(ctx, step, &diff, diag);
        } else if (!strcmp(what, "diff_merge")) {
            rc = step_diff_merge(ctx, step, &diff, diag);
        } else if (!strcmp(what, "diff_reverse")) {
            rc = step_diff_reverse(ctx, &diff, diag);
        } else if (!strcmp(what, "diff_apply")) {
            rc = lyd_diff_apply_module(&tree, diff, module_of(ctx, step), NULL, NULL);
            collect(ctx, diag, "diff");
        } else {
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
