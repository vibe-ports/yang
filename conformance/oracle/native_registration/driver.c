/* SPDX-License-Identifier: BSD-3-Clause */
/* Ported from libyang v5.8.6 src/tree_data_sorted.c (rb_node layout only; BSD-3-Clause, © CESNET). */
/* Observation only: algorithms and callback come from the pinned static library. */
#define _POSIX_C_SOURCE 200809L
#include <assert.h>
#include <float.h>
#include <inttypes.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "libyang.h"
#include "hash_table_internal.h"
#include "tree_data_internal.h"
#include "tree_data_sorted.h"
#include "validation.h"
#include "native_observer.h"
#include "../cjson/cJSON.h"

#define LIMIT 65536
struct identity { const struct lyd_node *ptr; unsigned generation, script, operation, ordinal, source, attempt; int alive; };
struct occurrence { const struct ly_ht *ht; uint32_t slot, hash; unsigned identity, serial; int live; };
struct table_identity { const struct ly_ht *ptr; int alive; };
/* Read-only pinned ABI mirror of src/tree_data_sorted.c:rb_node, no algorithm. */
struct nr_rb { struct nr_rb *parent, *left, *right; struct lyd_node *dnode; uint8_t color; };
static struct identity identities[LIMIT];
static struct occurrence occurrences[LIMIT], resizing[LIMIT];
static unsigned identity_count, occurrence_count, serial, op, ordinal, resize_count;
static struct table_identity table_identities[LIMIT];
static unsigned table_identity_count;
#ifdef NR_UNMODIFIED
static uint32_t table_sizes[LIMIT];
#endif
static unsigned pending_script, output_handle, output_generation;
static int unsafe_snapshot;
static const struct lysc_node *schemas[256];
static unsigned schema_count;
static int event_schema_enabled;
static const struct ly_ht *retired_tables[LIMIT];
static unsigned retired_count;
static struct lyd_node *nodes[4096];
static unsigned handle_generations[4096], handle_revisions[4096], handle_operations[4096];
static struct ly_ht *generic;
static const struct nr_rb *working_rb;
static lyht_value_equal_cb child_callback;
static void *child_data;
static struct ly_ctx *ctx;
static cJSON *log_messages;
static int enabled, events = 1, hash_depth, remote_stop, remote_watch, remote_pending, ub_stop;

static unsigned identity(const void *ptr)
{
    if (!ptr) return 0;
    for (unsigned i = identity_count; i; --i) if (identities[i].ptr == ptr) return i;
    /* References never manufacture allocations, particularly at reused addresses. */
    exit(90);
}

static unsigned table_identity(const struct ly_ht *ht)
{
    if(!ht)return 0;
    for(unsigned i=table_identity_count;i;--i)if(table_identities[i].ptr==ht)return i;
    exit(98);
}

static void number(cJSON *j, const char *key, double value) { cJSON_AddNumberToObject(j, key, value); }
static unsigned schema_identity(const struct lysc_node *schema)
{
    for(unsigned i=1;i<=schema_count;++i)if(schemas[i]==schema)return i;
    if(schema_count+1==256)exit(98);
    schemas[++schema_count]=schema;return schema_count;
}
static void emit(cJSON *j)
{
    char *s = cJSON_PrintUnformatted(j);
    if (!s || puts(s) == EOF || fflush(stdout)) exit(91);
    free(s);
    cJSON_Delete(j);
}

/* Only a constructor hook or a walk of live native storage may call this.
 * Ordinary identity lookup must keep resolving freed pointers to tombstones. */
static unsigned allocation(const struct lyd_node *node, const struct lyd_node *source)
{
    if(!node)exit(90);
    unsigned old=0, i;
    for(unsigned n=identity_count;n;--n)if(identities[n].ptr==node){old=n;break;}
    if(old && identities[old].alive)return old;
    if(identity_count+1==LIMIT)exit(90);
    i=++identity_count;
    identities[i]=(struct identity){node,i,0,op,++ordinal,source?identity(source):0,pending_script,1};
    if(old) {
        cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","allocation.address-reuse");
        number(j,"old_generation",old);number(j,"new_generation",i);emit(j);
    }
    return i;
}

static void logger(LY_LOG_LEVEL level, const char *msg, const char *data_path, const char *schema_path, uint64_t line)
{
    if(!log_messages)log_messages=cJSON_CreateArray();
    cJSON *j=cJSON_CreateObject(); number(j,"level",level);number(j,"line",(double)line);
    if(msg)cJSON_AddStringToObject(j,"msg",msg);
    if(data_path)cJSON_AddStringToObject(j,"path",data_path);
    if(schema_path)cJSON_AddStringToObject(j,"schema_path",schema_path);
    cJSON_AddItemToArray(log_messages,j);
}

static int observed(const struct ly_ht *ht)
{
    return ht && enabled && (ht == generic || (child_callback && ht->val_equal == child_callback));
}

static void retire_table(const struct ly_ht *ht)
{
    if (!ht) return;
    if (retired_count == LIMIT) exit(96);
    retired_tables[retired_count++] = ht;
    table_identities[table_identity(ht)].alive = 0;
    for (unsigned i = 0; i < occurrence_count; ++i)
        if (occurrences[i].ht == ht) occurrences[i].live = 0;
}

static void allocate_table(const struct ly_ht *ht)
{
    for(unsigned t=table_identity_count;t;--t)if(table_identities[t].ptr==ht){if(table_identities[t].alive)exit(98);break;}
    if(table_identity_count+1==LIMIT)exit(98);
    table_identities[++table_identity_count]=(struct table_identity){ht,1};
    for(unsigned i=0;i<retired_count;++i)if(retired_tables[i]==ht)retired_tables[i]=NULL;
}

#ifdef NR_UNMODIFIED
/* Linker interposition observes actual frees in the untouched static objects,
 * including validation/autodeletion and failed public calls. Never inspect the
 * freed object, follow parent links, or retire retained foreign occurrences. */
void __real_free(void *ptr);
void __wrap_free(void *ptr)
{
    if(enabled && ptr) {
        for(unsigned i=identity_count;i;--i)if(identities[i].ptr==ptr) {identities[i].alive=0;break;}
        for(unsigned i=table_identity_count;i;--i)if(table_identities[i].ptr==ptr) {
            if(table_identities[i].alive)retire_table(ptr);
            break;
        }
    }
    __real_free(ptr);
}

/* The control has no constructor hooks. Discover all surviving allocations at
 * each public boundary, before handles, table records or node_json use them.
 * A visited set and the existing traversal cap bound shared/corrupt graphs.
 * Do not follow occurrence pointers: those may name an older allocation. */
static unsigned discovered[LIMIT], discovery_epoch;
static void discover(const struct lyd_node *node, unsigned *visits)
{
    if(!node)return;
    unsigned i=allocation(node,NULL);
    if(discovered[i]==discovery_epoch)return;
    discovered[i]=discovery_epoch;
    if(++*visits>4096)exit(97);
    if(node->schema && (node->schema->nodetype&LYD_NODE_INNER)) {
        const struct ly_ht *ht=((const struct lyd_node_inner*)node)->children_ht;
        unsigned ti=0;
        for(unsigned t=table_identity_count;t;--t)if(table_identities[t].ptr==ht){ti=t;break;}
        if(ht && (!ti || !table_identities[ti].alive)) {
            /* Discovery must finish before any snapshot follows live links. */
            allocate_table(ht);
        }
    }
    discover((const struct lyd_node *)node->parent,visits);
    discover(node->prev,visits);
    discover(lyd_child(node),visits);
    discover(node->next,visits);
}

static void discover_boundary(const struct lyd_node *result)
{
    unsigned visits=0;
    if(++discovery_epoch==0)exit(97);
    discover(result,&visits);
    for(unsigned i=1;i<=identity_count;++i)
        if(identities[i].alive)discover(identities[i].ptr,&visits);
}
#endif

void nr_occurrence(const void *table, unsigned slot, const void *value, unsigned hash, int insert)
{
    const struct ly_ht *ht = table;
    if (!observed(ht)) return;
    const void *node=NULL;
    if(insert)memcpy(&node,value,sizeof node);
    for (unsigned i = 0; i < occurrence_count; ++i) {
        if (occurrences[i].live && occurrences[i].ht == ht && occurrences[i].slot == slot) occurrences[i].live = 0;
    }
    if (!insert) return;
    if (occurrence_count == LIMIT) exit(92);
    struct occurrence occ = {ht, slot, hash, identity(node), ++serial, 1};
    for (unsigned i = 0; i < resize_count; ++i) {
        if (resizing[i].live && resizing[i].ht == ht && resizing[i].hash == hash && identities[resizing[i].identity].ptr == node) {
            occ.identity = resizing[i].identity;
            occ.serial = resizing[i].serial;
            resizing[i].live = 0;
            break;
        }
    }
    occurrences[occurrence_count++] = occ;
}

static struct occurrence *occurrence(const struct ly_ht *ht, uint32_t slot, const struct ly_ht_rec *rec)
{
    void *node;
    memcpy(&node, rec->val, sizeof node);
    for (unsigned i = occurrence_count; i; --i) {
        if (occurrences[i-1].live && occurrences[i-1].ht == ht && occurrences[i-1].slot == slot) {
#ifdef NR_UNMODIFIED
            /* No insertion/removal hooks: a native slot may have been reused. */
            if (occurrences[i-1].hash != rec->hash || identities[occurrences[i-1].identity].ptr != node) break;
#endif
            return &occurrences[i-1];
        }
    }
    nr_occurrence(ht, slot, &node, rec->hash, 1);
    return &occurrences[occurrence_count-1];
}

static cJSON *table_json(const struct ly_ht *ht)
{
    cJSON *j = cJSON_CreateObject();
    cJSON_AddBoolToObject(j, "present", ht != NULL);
    if (!ht) return j;
    number(j,"identity",table_identity(ht));
    for(unsigned i=0;i<retired_count;++i) if(retired_tables[i]==ht) {cJSON_AddBoolToObject(j,"retired",1);return j;}
#ifdef NR_UNMODIFIED
    /* Slots are addresses in a particular table layout, not lifetime IDs.
     * Rehash moves records; carry old generations forward by pointer/hash in
     * FIFO order, just as the hooked resize observer does. In particular a
     * checked shrink may retain a tombstone and discard its live replacement. */
    unsigned ti=table_identity(ht);
    if(table_sizes[ti] && table_sizes[ti]!=ht->size) {
        resize_count=0;
        for(unsigned i=0;i<occurrence_count;++i)if(occurrences[i].live && occurrences[i].ht==ht) {
            if(resize_count==LIMIT)exit(92);
            resizing[resize_count++]=occurrences[i];
            occurrences[i].live=0;
        }
    }
    table_sizes[ti]=ht->size;
#endif
#ifdef NR_UNMODIFIED
    /* Reconciliation tokens are not occurrence-lifetime evidence. In
     * particular same-slot removal/reinsertion is observationally ambiguous.
     * Keep retained generation references, but never assert their continuity. */
    cJSON_AddStringToObject(j,"continuity",ht==generic?"observed-generic":"unknown");
#else
    cJSON_AddStringToObject(j,"continuity","observed");
#endif
    number(j,"size",ht->size); number(j,"used",ht->used); number(j,"mode",ht->resize); number(j,"free",ht->first_free_rec);
    cJSON *buckets=cJSON_AddArrayToObject(j,"buckets"), *slots=cJSON_AddArrayToObject(j,"slots"), *chains=cJSON_AddArrayToObject(j,"chains");
    unsigned char active[LIMIT] = {0};
    if (ht->size >= LIMIT) exit(93);
    for (uint32_t b=0; b<ht->size; ++b) {
        cJSON *bj=cJSON_CreateObject(); number(bj,"first",ht->hlists[b].first); number(bj,"last",ht->hlists[b].last); cJSON_AddItemToArray(buckets,bj);
        uint32_t s=ht->hlists[b].first, n=0;
        while (s != LYHT_NO_RECORD) {
            if (s>=ht->size || n++>=ht->size) exit(94);
            struct ly_ht_rec *rec=lyht_get_rec(ht->recs,ht->rec_size,s);
            struct occurrence *occ=occurrence(ht,s,rec);
            active[s]=1;
            cJSON *cj=cJSON_CreateObject(); number(cj,"bucket",b); number(cj,"slot",s); number(cj,"ordinal",n-1); number(cj,"node",identities[occ->identity].script); number(cj,"generation",occ->identity); number(cj,"occurrence",occ->serial); number(cj,"hash",rec->hash); cJSON_AddBoolToObject(cj,"alive",identities[occ->identity].alive); cJSON_AddItemToArray(chains,cj);
            s=rec->next;
        }
    }
    for (uint32_t s=0;s<ht->size;++s) {
        struct ly_ht_rec *rec=lyht_get_rec(ht->recs,ht->rec_size,s);
        cJSON *sj=cJSON_CreateObject(); number(sj,"next",rec->next);
        if(active[s]) { struct occurrence *occ=occurrence(ht,s,rec); number(sj,"node",identities[occ->identity].script); number(sj,"hash",rec->hash); }
        else { number(sj,"node",0); number(sj,"hash",0); }
        cJSON_AddItemToArray(slots,sj);
    }
#ifdef NR_UNMODIFIED
    /* A completed mutation's snapshot reconciles removed native slots. Keep
     * retained foreign/generic occurrences that are still physically present. */
    for (unsigned i=0;i<occurrence_count;++i)
        if(occurrences[i].ht==ht && (occurrences[i].slot>=ht->size || !active[occurrences[i].slot])) occurrences[i].live=0;
    resize_count=0;
#endif
    cJSON *free_chain=cJSON_AddArrayToObject(j,"free_chain");
    uint32_t s=ht->first_free_rec, n=0;
    while (s != LYHT_NO_RECORD && s<ht->size) {
        if(n++>=ht->size) exit(95);
        cJSON_AddItemToArray(free_chain,cJSON_CreateNumber(s)); s=lyht_get_rec(ht->recs,ht->rec_size,s)->next;
    }
    number(j,"free_terminal",s);
    return j;
}

void nr_working_rb(const void *root) { working_rb=root; }

static struct nr_rb *rb_root(struct nr_rb *r)
{
    unsigned visits=0;
    while(r && r->parent) {if(++visits>4096)exit(97);r=r->parent;}
    return r;
}

static cJSON *node_json(unsigned i)
{
    cJSON *j=cJSON_CreateObject(); struct identity *id=&identities[i];
    number(j,"generation",i); number(j,"id",id->script); number(j,"creation_operation",id->operation); number(j,"creation_ordinal",id->ordinal); number(j,"source",id->source); number(j,"attempt",id->attempt); cJSON_AddBoolToObject(j,"alive",id->alive);
    if (!id->alive) { cJSON_AddStringToObject(j,"current","unavailable"); return j; }
    if(unsafe_snapshot && id->operation) { cJSON_AddStringToObject(j,"current","unsafe-native-links");return j; }
    const struct lyd_node *n=id->ptr;
    number(j,"context",LYD_CTX(n)==ctx?1:0);
    number(j,"hash",n->hash); number(j,"flags",n->flags); number(j,"prev",identity(n->prev)); number(j,"next",identity(n->next)); number(j,"parent",identity(n->parent));
    if(n->schema) {number(j,"nodetype",n->schema->nodetype);cJSON_AddBoolToObject(j,"key",lysc_is_key(n->schema));cJSON_AddStringToObject(j,"schema",n->schema->name);if(event_schema_enabled){cJSON_AddStringToObject(j,"module",n->schema->module->name);number(j,"schema_identity",schema_identity(n->schema));}if(n->schema->nodetype&LYD_NODE_TERM)cJSON_AddStringToObject(j,"value",lyd_get_value(n));}
    else {const struct lyd_node_opaq *o=(const void*)n;number(j,"first",identity(o->child));number(j,"tail",identity(o->child?o->child->prev:NULL));number(j,"prefix_format",o->format);cJSON_AddBoolToObject(j,"prefix_bound",o->val_prefix_data!=NULL);}
    if(n->schema && (n->schema->nodetype & LYD_NODE_INNER)) { const struct lyd_node_inner *in=(const void*)n; number(j,"first",identity(in->child)); number(j,"tail",identity(in->child ? in->child->prev : NULL)); cJSON_AddItemToObject(j,"table",table_json(in->children_ht)); }
    cJSON *metas=cJSON_AddArrayToObject(j,"metadata"); for(const struct lyd_meta *m=n->meta;m;m=m->next) {
        cJSON_AddItemToArray(metas,cJSON_CreateString(m->name));
        if(!strcmp(m->name,"lyds_tree")) {
            struct lyd_value_lyds_tree *lt; LYD_VALUE_GET(&m->value,lt);
            cJSON *rb=cJSON_AddArrayToObject(j,"rb");
            struct nr_rb *stack[4096];unsigned count=0,visits=0;
            struct nr_rb *stored=lt?(void*)lt->rbt:NULL, *root=rb_root(stored);
            number(j,"rb_stored_root",identity(stored?stored->dnode:NULL));
            number(j,"rb_root",identity(root?root->dnode:NULL));
            if(root)stack[count++]=root;
            while(count) {if(++visits>4096)exit(97);struct nr_rb *r=stack[--count];cJSON *rj=cJSON_CreateObject();number(rj,"owner",i);number(rj,"node",identity(r->dnode));number(rj,"parent",identity(r->parent?r->parent->dnode:NULL));number(rj,"left",identity(r->left?r->left->dnode:NULL));number(rj,"right",identity(r->right?r->right->dnode:NULL));number(rj,"color",r->color);cJSON_AddItemToArray(rb,rj);if(count+2>4096)exit(97);if(r->right)stack[count++]=r->right;if(r->left)stack[count++]=r->left;}
        }
    }
    return j;
}

static void snapshot(const char *event, const void *node, const struct ly_ht *ht, int rc, const void *source)
{
    cJSON *j=cJSON_CreateObject(); cJSON_AddStringToObject(j,"event",event); number(j,"operation",op); number(j,"rc",rc); number(j,"node_generation",identity(node)); number(j,"source_generation",identity(source));
    if(working_rb && !unsafe_snapshot)number(j,"working_rb_root",identity(working_rb->dnode));
    if(ht) cJSON_AddItemToObject(j,"table",table_json(ht));
    number(j,"output_handle",output_handle);number(j,"output_generation",output_generation);
    number(j,"node_count",identity_count);number(j,"table_count",table_identity_count);
#ifdef NR_UNMODIFIED
    cJSON_AddStringToObject(j,"allocation_scope","survivors-and-observed-frees");
#else
    cJSON_AddStringToObject(j,"allocation_scope","every-allocation");
#endif
    cJSON *bindings=cJSON_AddArrayToObject(j,"bindings");
    for(unsigned h=1;h<4096;++h)if(handle_revisions[h]) {
        cJSON *b=cJSON_CreateObject();number(b,"handle",h);number(b,"generation",handle_generations[h]);
        number(b,"revision",handle_revisions[h]);number(b,"operation",handle_operations[h]);cJSON_AddItemToArray(bindings,b);
    }
    cJSON *registry=cJSON_AddArrayToObject(j,"tables");
    for(unsigned t=1;t<=table_identity_count;++t) {
        cJSON *r=cJSON_CreateObject();number(r,"identity",t);cJSON_AddBoolToObject(r,"alive",table_identities[t].alive);
        if(unsafe_snapshot && table_identities[t].alive)cJSON_AddItemToObject(r,"storage",table_json(table_identities[t].ptr));
        cJSON_AddItemToArray(registry,r);
    }
    cJSON *a=cJSON_AddArrayToObject(j,"nodes"); for(unsigned i=1;i<=identity_count;++i) cJSON_AddItemToArray(a,node_json(i));
    cJSON *errs=cJSON_AddArrayToObject(j,"diagnostics"); for(const struct ly_err_item *e=ctx?ly_err_first(ctx):NULL;e;e=e->next) { cJSON *ej=cJSON_CreateObject(); number(ej,"code",e->err); if(e->msg)cJSON_AddStringToObject(ej,"msg",e->msg); if(e->data_path)cJSON_AddStringToObject(ej,"path",e->data_path); if(e->apptag)cJSON_AddStringToObject(ej,"apptag",e->apptag); cJSON_AddItemToArray(errs,ej); }
    if(log_messages)cJSON_AddItemToObject(j,"logs",cJSON_Duplicate(log_messages,1));
    emit(j);
}

void nr_event(const char *event, const void *node, const void *table, int rc, const void *source)
{
    const struct ly_ht *ht=table;
    if(!enabled) return;
    /* lyd_insert_hash's only table allocation is its actual children table.
     * Capture its callback before the first reinsertion/record hook. */
    if(!strcmp(event,"table.allocate") && hash_depth && !child_callback) {child_callback=ht->val_equal;child_data=ht->cb_data;}
    if(!strcmp(event,"table.retire") && !observed(ht)) return;
    if(!strcmp(event,"table.allocate")) {
        if(observed(ht))allocate_table(ht);
        else for(unsigned i=0;i<retired_count;++i)if(retired_tables[i]==ht)retired_tables[i]=NULL;
    }
    if(!strcmp(event,"node.allocate") || !strcmp(event,"node.dup.allocate")) {
        allocation(node,source); return;
    }
    if(!strcmp(event,"lyd_insert_hash.before") || !strcmp(event,"lyd_unlink_hash.before")) ++hash_depth;
    if(!strcmp(event,"lyd_insert_hash.after") && node) {
        const struct lyd_node *n=node;
        if(n->parent && n->parent->schema && (n->parent->schema->nodetype & LYD_NODE_INNER)) {
            ht=((const struct lyd_node_inner*)n->parent)->children_ht;
            if(ht && !child_callback) {child_callback=ht->val_equal;child_data=ht->cb_data;}
        }
    }
    if(!strcmp(event,"lyht_resize.before") && observed(ht)) {
        resize_count=0;
        for(uint32_t b=0;b<ht->size;++b) for(uint32_t s=ht->hlists[b].first;s!=LYHT_NO_RECORD;) {
            struct ly_ht_rec *rec=lyht_get_rec(ht->recs,ht->rec_size,s); struct occurrence *occ=occurrence(ht,s,rec);
            resizing[resize_count++]=*occ; s=rec->next;
        }
    }
    int low=!strncmp(event,"lyht_",5) || !strncmp(event,"_lyht_",6);
    if(!strcmp(event,"node.retiring") || !strcmp(event,"node.free")) { unsigned i=identity(node); identities[i].alive=0; }
    if(events && (!low || observed(ht))) snapshot(event,node,observed(ht)?ht:NULL,rc,source);
    /* Keep old records observable until their before-snapshot is complete.
     * Retiring them earlier would let table_json consume resize identities. */
    if(!strcmp(event,"lyht_resize.before") && observed(ht))
        for(unsigned i=0;i<occurrence_count;++i) if(occurrences[i].ht==ht) occurrences[i].live=0;
    if(!strcmp(event,"lyht_resize.after")) resize_count=0;
    if(!strcmp(event,"table.retire")) retire_table(ht);
    if(!strcmp(event,"lyd_insert_hash.after") || !strcmp(event,"lyd_unlink_hash.after")) --hash_depth;
    if(remote_pending && !strcmp(event,"lyd_insert_after_node.after")) {
        snapshot("boundary.remote-last-corrupting-primitive",node,NULL,0,source);
        if(remote_stop) _Exit(0);
    }
    if(ub_stop && !strcmp(event,"merge2.back.no-successor")) { snapshot("boundary.merge2-before-uninitialized-continuation",node,NULL,0,source); _Exit(0); }
    if(ub_stop && !strcmp(event,"merge2.among.exhausted-forward")) { snapshot("boundary.merge2-exhausted-forward-iterator",node,NULL,0,source); _Exit(0); }
    if(ub_stop && !strcmp(event,"merge2.among.successor-schema")) { snapshot("boundary.merge2-successor-schema-precondition",node,NULL,0,source); _Exit(0); }
}

void nr_remote(const void *first, const void *sibling, const void *node)
{
    const struct lyd_node *f=first,*s=sibling;
    if(enabled && remote_watch && f && f->parent!=s->parent) {
        snapshot(s->next?"remote.nonlast.before":"remote.last.before",node,NULL,0,first);
        if(!s->next)remote_pending=1;
    }
}

static const char *schema="module m {yang-version 1.1; namespace urn:m; prefix m; container c {leaf p {type string;} leaf q {type string;} leaf-list ll {type binary; ordered-by user;} leaf-list v {type uint32;} leaf z {type string;}} rpc r {input {leaf a {type string;} leaf a2 {type string;} leaf-list v {type uint32;}} output {leaf o {type string;}}}}";
static const char *event_schema="module m {yang-version 1.1; namespace urn:m; prefix m; container c {leaf p {type string;} leaf q {type string;} leaf-list ll {type binary; ordered-by user;} leaf-list v {type uint32;} leaf z {type string;} list l {key k; ordered-by user; leaf k {type string;}} leaf def {type string; default D;}} container d {presence synthetic; leaf-list ll {type binary; ordered-by user;}} rpc r {input {leaf a {type string;} leaf a2 {type string;} leaf-list v {type uint32;}} output {leaf o {type string;}}}}";
static void watchdog(int signal_number) { (void)signal_number; const char msg[]="{\"event\":\"boundary.watchdog\",\"classification\":\"nontermination-or-unsafe-traversal\"}\n"; ssize_t written=write(STDOUT_FILENO,msg,sizeof msg-1); (void)written; _Exit(124); }

static int tombstoned_occurrence(void)
{
    for(unsigned i=0;i<occurrence_count;++i) if(occurrences[i].live && !identities[occurrences[i].identity].alive)return 1;
    return 0;
}

/* Handles retain the allocation generation, even if a descendant is freed by
 * an API invoked on its parent, or its raw address is subsequently reused. */
static int require_handle(unsigned handle)
{
    if(!handle || handle>=4096)return 0;
    unsigned generation=handle_generations[handle];
    if(generation && !identities[generation].alive) {
        cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","boundary.tombstoned-handle");
        number(j,"operation",op);number(j,"handle",handle);number(j,"generation",generation);
        cJSON_AddStringToObject(j,"current","unavailable");emit(j);exit(0);
    }
    return nodes[handle] && generation && identities[generation].ptr==nodes[handle];
}

static void bind_handle(unsigned handle)
{
    unsigned generation=nodes[handle]?identity(nodes[handle]):0;
    if(generation!=handle_generations[handle] || !handle_revisions[handle]) {
        handle_generations[handle]=generation;
        if(++handle_revisions[handle]==0)exit(98);
        handle_operations[handle]=op;
    }
}

/* Destruction creates no data allocations. A known retired link after free
 * cannot be a newly returned ancestor: never dereference it during discovery.
 * Preserve allocation/binding/table evidence at an explicitly unsafe boundary. */
static int retired_links_after_free(void)
{
    unsigned visits=0;
    for(unsigned i=1;i<=identity_count;++i)if(identities[i].alive) {
#ifdef NR_UNMODIFIED
        if(++visits>4096)exit(97);
#else
        if(++visits>=LIMIT)exit(97);
#endif
        const struct lyd_node *n=identities[i].ptr;
        if(n->schema && (n->schema->nodetype&LYD_NODE_INNER)) {
            const struct ly_ht *ht=((const struct lyd_node_inner*)n)->children_ht;
            if(ht && !table_identities[table_identity(ht)].alive)return 1;
        }
        const struct lyd_node *links[]={ (const struct lyd_node*)n->parent,n->prev,n->next,lyd_child(n) };
        for(unsigned k=0;k<sizeof links/sizeof *links;++k)
            if(links[k] && !identities[identity(links[k])].alive)return 1;
    }
    return 0;
}

static int prepare_output(unsigned handle)
{
    if(!handle || handle>=4096)return 0;
    if(handle_generations[handle] && identities[handle_generations[handle]].alive)return 0;
    nodes[handle]=NULL;
    return 1;
}

static void free_slot_boundary(void)
{
    snapshot("boundary.primitive-free-slot-cap",NULL,generic,0,NULL);
    exit(0);
}

int main(int argc, char **argv)
{
    if(argc>1 && !strcmp(argv[1],"--abi")) {
        int ndebug=0, instrumented=1;
#ifdef NR_UNMODIFIED
        instrumented=0;
#endif
#ifdef NDEBUG
        ndebug=1;
#endif
        int written=printf("{\"pointer_bytes\":%zu,\"long_double_bytes\":%zu,\"long_double_mantissa_bits\":%d,\"pointer_record_bytes\":%zu,\"rb_node_bytes\":%zu,\"ndebug\":%d,\"instrumented\":%d}\n",sizeof(void*),sizeof(long double),LDBL_MANT_DIG,(size_t)SIZEOF_LY_HT_REC+sizeof(struct lyd_node*),sizeof(struct nr_rb),ndebug,instrumented);
        return written<0 || fflush(stdout)?91:0;
    }
    if(signal(SIGALRM,watchdog)==SIG_ERR)return 99;
    alarm(15);
    if(argc>1 && !strcmp(argv[1],"--quiet")) events=0;
    if(argc>1 && !strcmp(argv[1],"--remote-stop")) remote_watch=remote_stop=1;
    if(argc>1 && !strcmp(argv[1],"--remote-watchdog")) remote_watch=1;
    if(argc>1 && !strcmp(argv[1],"--merge2-ub-stop")) ub_stop=1;
    ly_log_options(LY_LOSTORE | LY_LOLOG);
    ly_set_log_clb(logger);
    struct lys_module *mod=NULL;
    LY_ERR rc=ly_ctx_new(ly_yang_module_dir(),LY_CTX_NO_YANGLIBRARY,&ctx);
    for(int i=2;i<argc;++i)if(!strcmp(argv[i],"--event-schema"))event_schema_enabled=1;
    if(!rc) rc=lys_parse_mem(ctx,event_schema_enabled?event_schema:schema,LYS_IN_YANG,&mod);
    if(rc) return 2;
    enabled=1;
    /* A real children table supplies the hidden native callback. */
    struct lyd_node *seed=NULL,*child=NULL;
    events=0;
    rc=lyd_new_inner(NULL,mod,"c",0,&seed);
    for(unsigned i=0;!rc && i<4;++i) {char value[16];snprintf(value,sizeof value,"%u",i);rc=lyd_new_term(seed,mod,"v",value,0,&child);}
    if(!rc) {struct ly_ht *ht=((struct lyd_node_inner*)seed)->children_ht;if(ht){child_callback=ht->val_equal;child_data=ht->cb_data;}}
    if(rc || !child_callback) return 3;
#ifdef NR_UNMODIFIED
    discover_boundary(seed);
#endif
    events=(argc>1 && !strcmp(argv[1],"--quiet"))?0:1;
    snapshot("driver.ready.actual-children-callback",NULL,NULL,0,NULL);
    char line[1024],cmd[32];
    while(fgets(line,sizeof line,stdin)) {
        unsigned a=0,b=0,c=0,d=0; int creating=0,refresh=0; char name[80]={0},value[256]={0};
        if(line[0]=='#' || sscanf(line,"%31s",cmd)!=1) continue;
        ++op; ordinal=0;pending_script=0;output_handle=output_generation=0; ly_err_clean(ctx,NULL);cJSON_Delete(log_messages);log_messages=NULL;
        rc=LY_SUCCESS;
        if(tombstoned_occurrence() && (!strcmp(cmd,"schema") || !strcmp(cmd,"validate") || !strcmp(cmd,"rewind") || !strcmp(cmd,"print") || !strcmp(cmd,"attach") || !strcmp(cmd,"before") || !strcmp(cmd,"change") || !strcmp(cmd,"find-next-loop") || !strcmp(cmd,"root") || !strcmp(cmd,"term") || !strcmp(cmd,"dup") || !strcmp(cmd,"unlink") || !strcmp(cmd,"detach-siblings") || !strcmp(cmd,"free") || !strcmp(cmd,"free-root") || !strcmp(cmd,"opaque") || !strcmp(cmd,"parse") || !strcmp(cmd,"list") || !strcmp(cmd,"frontier"))) {snapshot("boundary.tombstoned-semantic-probe",NULL,generic,0,NULL);return 0;}
        if(!strcmp(cmd,"new")) {
            if(sscanf(line,"%*s %u %u",&a,&b)!=2 || a>32768 || b>65535)return 4;
            if(generic) {
                lyht_free(generic,NULL);
            }
            generic=lyht_new(a,sizeof(struct lyd_node*),child_callback,child_data,b);
            if(!generic){snapshot("boundary.new-null",NULL,NULL,0,NULL);return 5;}
#ifdef NR_UNMODIFIED
            nr_event("table.allocate",NULL,generic,0,NULL);
#endif
        }
        else if(!strcmp(cmd,"root")) {if(sscanf(line,"%*s %u %79s",&a,name)!=2 || !a||a>=4096)return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_inner(NULL,mod,name,0,&nodes[a]);}
        else if(!strcmp(cmd,"term")) {int fields=sscanf(line,"%*s %u %u %79s %255s %u",&a,&b,name,value,&c);if(fields<4 || !a || a>=4096 || b>=4096)return 4;if(b&&!require_handle(b))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_term(nodes[b],mod,name,value,c,&nodes[a]);}
        else if(!strcmp(cmd,"list")) {if(sscanf(line,"%*s %u %u %79s %255s",&a,&b,name,value)!=4 || !a || a>=4096 || b>=4096)return 4;if(b&&!require_handle(b))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_list(nodes[b],mod,name,0,&nodes[a],value);}
        else if(!strcmp(cmd,"frontier")) {
            /* Synthetic low-level evidence only. The four real allocations
             * retain their schema pointers; no public compatibility claim. */
            unsigned ids[4];
            if(sscanf(line,"%*s %u %u %u %u %u",&a,&ids[0],&ids[1],&ids[2],&ids[3])!=5 || a>=4096 || !require_handle(a))return 4;
            if(!nodes[a]->schema || nodes[a]->schema->nodetype!=LYS_CONTAINER || ((struct lyd_node_inner*)nodes[a])->children_ht)return 4;
            for(unsigned i=0;i<4;++i) {
                if(ids[i]>=4096 || !require_handle(ids[i]) || !nodes[ids[i]]->schema || (nodes[ids[i]]->parent && nodes[ids[i]]->parent!=nodes[a]))return 4;
                for(unsigned j=0;j<i;++j)if(ids[i]==ids[j])return 4;
            }
            ((struct lyd_node_inner*)nodes[a])->child=nodes[ids[0]];
            for(unsigned i=0;i<4;++i) {
                nodes[ids[i]]->parent=nodes[a];
                nodes[ids[i]]->prev=nodes[ids[(i+3)%4]];
                nodes[ids[i]]->next=i<3?nodes[ids[i+1]]:NULL;
            }
        }
        else if(!strcmp(cmd,"opaque")) {if(sscanf(line,"%*s %u %u %79s %255s",&a,&b,name,value)!=4||!a||a>=4096||b>=4096)return 4;if(b&&!require_handle(b))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_opaq(nodes[b],ctx,name,value,NULL,"m",&nodes[a]);}
        else if(!strcmp(cmd,"dup")) {if(sscanf(line,"%*s %u %u %u %u",&a,&b,&c,&d)!=4 || !a||a>=4096||b>=4096||c>=4096)return 4;if(!require_handle(b)||(c&&!require_handle(c)))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_dup_single(nodes[b],nodes[c],d,&nodes[a]);}
        else if(!strcmp(cmd,"unlink")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;rc=lyd_unlink_tree(nodes[a]);}
        else if(!strcmp(cmd,"attach")) {if(sscanf(line,"%*s %u %u",&a,&b)!=2||a>=4096||b>=4096||!require_handle(a)||!require_handle(b))return 4;rc=lyd_insert_child(nodes[a],nodes[b]);}
        else if(!strcmp(cmd,"before")) {if(sscanf(line,"%*s %u %u",&a,&b)!=2||a>=4096||b>=4096||!require_handle(a)||!require_handle(b))return 4;rc=lyd_insert_before(nodes[a],nodes[b]);}
        else if(!strcmp(cmd,"detach-siblings")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;rc=lyd_unlink_siblings(nodes[a]);}
        else if(!strcmp(cmd,"change")) {if(sscanf(line,"%*s %u %255s",&a,value)!=2||a>=4096||!require_handle(a))return 4;rc=lyd_change_term(nodes[a],value);}
        else if(!strcmp(cmd,"parse")) {int offset=0;if(sscanf(line,"%*s %u %n",&a,&offset)<1||!a||a>=4096)return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_parse_data_mem(ctx,line+offset,LYD_JSON,LYD_PARSE_ONLY|LYD_PARSE_ORDERED,0,&nodes[a]);}
        else if(!strcmp(cmd,"schema")) {
            if(sscanf(line,"%*s %u %u",&a,&b)!=2||a>=4096||b>=4096||!require_handle(a)||!require_handle(b)||!nodes[b]->schema||!nodes[a]->schema||!(nodes[a]->schema->nodetype&LYD_NODE_INNER))return 4;
            const struct ly_ht *queried=((const struct lyd_node_inner*)nodes[a])->children_ht;
            cJSON *before=table_json(queried);
            struct lyd_node *winner=NULL;
            rc=lyd_find_sibling_val(lyd_child(nodes[a]),nodes[b]->schema,NULL,0,&winner);
            cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","read.schema");
            number(j,"operation",op);number(j,"rc",rc);number(j,"queried_parent",identity(nodes[a]));
            /* Query hash observation: libyang v5.8.6 src/tree_data_common.c:lyd_find_sibling_schema. */
            uint32_t schema_hash=lyht_hash_multi(0,nodes[b]->schema->module->name,strlen(nodes[b]->schema->module->name));
            schema_hash=lyht_hash_multi(schema_hash,nodes[b]->schema->name,strlen(nodes[b]->schema->name));
            schema_hash=lyht_hash_multi(schema_hash,NULL,0);number(j,"schema_hash",schema_hash);
            number(j,"winner",identity(winner));number(j,"current_parent",identity(winner?winner->parent:NULL));
            cJSON_AddItemToObject(j,"before",before);cJSON_AddItemToObject(j,"after",table_json(queried));emit(j);
        }
        else if(!strcmp(cmd,"find-next-loop")) {if(sscanf(line,"%*s %u %u %u",&a,&b,&c)!=3||a>=4096||c>1024||!generic||!require_handle(a))return 4;struct lyd_node *iter=nodes[a];unsigned steps=0;for(;steps<c;++steps){void *match=NULL;rc=lyht_find_next(generic,&iter,b,&match);if(rc)break;memcpy(&iter,match,sizeof iter);}if(steps==c){snapshot("boundary.watchdog.find-next-cycle",iter,generic,0,NULL);return 0;}}
        else if(!strcmp(cmd,"insert") || !strcmp(cmd,"remove")) {if(sscanf(line,"%*s %u %u %u",&a,&b,&c)<2||!a||a>=4096||!generic)return 4; if(!nodes[a]) {pending_script=a;rc=lyd_dup_single(child,NULL,LYD_DUP_NO_META,&nodes[a]);if(rc)return 7;
#ifdef NR_UNMODIFIED
            discover_boundary(nodes[a]);
#endif
            if(!identities[identity(nodes[a])].script){identities[identity(nodes[a])].script=a;}bind_handle(a);} if(!require_handle(a))return 4;if(strcmp(cmd,"remove") && generic->first_free_rec>=generic->size) {
            int existing=0;
            if(c)for(uint32_t slot=generic->hlists[b&(generic->size-1)].first, steps=0;slot!=LYHT_NO_RECORD;) {
                if(slot>=generic->size || ++steps>generic->size)return 94;
                struct ly_ht_rec *rec=lyht_get_rec(generic->recs,generic->rec_size,slot);
                if(rec->hash==b && child_callback(&nodes[a],rec->val,1,child_data)){existing=1;break;}
                slot=rec->next;
            }
            if(!existing)free_slot_boundary();
        }if(!strcmp(cmd,"remove")) {
#ifdef NR_UNMODIFIED
            /* Native removal takes the earliest pointer/hash occurrence,
             * which may retain a generation older than this live handle. */
            struct occurrence *removed=NULL;
            for(uint32_t slot=generic->hlists[b&(generic->size-1)].first, steps=0;slot!=LYHT_NO_RECORD;) {
                if(slot>=generic->size || ++steps>generic->size)return 94;
                struct ly_ht_rec *rec=lyht_get_rec(generic->recs,generic->rec_size,slot);
                const struct lyd_node *candidate=NULL;memcpy(&candidate,rec->val,sizeof candidate);
                if(rec->hash==b && candidate==nodes[a]) {removed=occurrence(generic,slot,rec);break;}
                slot=rec->next;
            }
#endif
            rc=lyht_remove(generic,&nodes[a],b);
#ifdef NR_UNMODIFIED
            if(!rc && removed)removed->live=0;
#endif
        }else if(c)rc=lyht_insert(generic,&nodes[a],b,NULL);else rc=lyht_insert_no_check(generic,&nodes[a],b,NULL);}
        else if(!strcmp(cmd,"I") || !strcmp(cmd,"U")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;if(cmd[0]=='I')rc=lyd_insert_hash(nodes[a]);else lyd_unlink_hash(nodes[a]);}
        else if(!strcmp(cmd,"free") || !strcmp(cmd,"free-root")) {
            if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;
            if(!strcmp(cmd,"free-root"))lyd_free_all(nodes[a]);else lyd_free_tree(nodes[a]);if(!identities[handle_generations[a]].alive)nodes[a]=NULL;
        }
        else if(!strcmp(cmd,"rewind")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;refresh=1;nodes[a]=lyd_first_sibling(nodes[a]);cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","read.first-sibling");number(j,"operation",op);number(j,"winner",identity(nodes[a]));emit(j);}
        else if(!strcmp(cmd,"validate")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;refresh=1;rc=lyd_validate_all(&nodes[a],ctx,LYD_VALIDATE_PRESENT,NULL);}
        else if(!strcmp(cmd,"check-handle")) {if(sscanf(line,"%*s %u",&a)!=1||!require_handle(a))return 4;}
        else if(!strcmp(cmd,"print")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;char *json=NULL;rc=lyd_print_mem(&json,nodes[a],LYD_JSON,LYD_PRINT_SHRINK);cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","public.result");number(j,"operation",op);number(j,"rc",rc);if(json)cJSON_AddStringToObject(j,"json",json);emit(j);free(json);}
        else if(!strcmp(cmd,"dump")) { /* Snapshot below is the requested result. */ }
        else return 4;
        if((!strcmp(cmd,"free") || !strcmp(cmd,"free-root")) && retired_links_after_free()) {
            unsafe_snapshot=1;
            snapshot("boundary.retained-dangling-links",NULL,generic,rc,NULL);
            return 0;
        }
#ifdef NR_UNMODIFIED
        /* Free has no fresh nodes/tables to discover. The bounded link scan
         * above replaces discovery, preserving the same comparison visit cap. */
        if(strcmp(cmd,"free") && strcmp(cmd,"free-root"))discover_boundary((creating||refresh)?nodes[a]:NULL);
#endif
        if((creating||refresh) && a && a<4096) {
            bind_handle(a);output_handle=a;output_generation=nodes[a]?identity(nodes[a]):0;
            if(creating && nodes[a] && identities[identity(nodes[a])].operation==op && !identities[identity(nodes[a])].script)identities[identity(nodes[a])].script=a;
        }
        snapshot(cmd,NULL,generic,rc,NULL);
    }
    if(ferror(stdin))return 6;
    /* Each trace process owns its forest; avoid walking deliberately corrupt graphs. */
    return 0;
}
