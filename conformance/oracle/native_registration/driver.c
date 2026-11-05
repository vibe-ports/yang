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
struct identity { const struct lyd_node *ptr; unsigned generation, script, operation, ordinal, source; int alive; };
struct occurrence { const struct ly_ht *ht; uint32_t slot, hash; unsigned identity, serial; int live; };
struct table_identity { const struct ly_ht *ptr; int alive; };
/* Read-only pinned ABI mirror of src/tree_data_sorted.c:rb_node, no algorithm. */
struct nr_rb { struct nr_rb *parent, *left, *right; struct lyd_node *dnode; uint8_t color; };
static struct identity identities[LIMIT];
static struct occurrence occurrences[LIMIT], resizing[LIMIT];
static unsigned identity_count, occurrence_count, serial, op, ordinal, resize_count;
static struct table_identity table_identities[LIMIT];
static unsigned table_identity_count;
static unsigned pending_script;
static const struct ly_ht *retired_tables[LIMIT];
static unsigned retired_count;
static struct lyd_node *nodes[4096];
static unsigned handle_generations[4096];
static struct ly_ht *generic;
static lyht_value_equal_cb child_callback;
static void *child_data;
static struct ly_ctx *ctx;
static cJSON *log_messages;
static int enabled, events = 1, hash_depth, remote_stop, remote_watch, remote_pending, ub_stop;

static unsigned identity(const void *ptr)
{
    if (!ptr) return 0;
    for (unsigned i = identity_count; i; --i) if (identities[i].ptr == ptr) return i;
    if (identity_count + 1 == LIMIT) exit(90);
    unsigned i = ++identity_count;
    identities[i] = (struct identity){ptr, i, 0, op, ++ordinal, 0, 1};
    return i;
}

static unsigned table_identity(const struct ly_ht *ht)
{
    if(!ht)return 0;
    for(unsigned i=table_identity_count;i;--i)if(table_identities[i].ptr==ht)return i;
    if(table_identity_count+1==LIMIT)exit(98);
    unsigned i=++table_identity_count;table_identities[i]=(struct table_identity){ht,1};return i;
}

static void number(cJSON *j, const char *key, double value) { cJSON_AddNumberToObject(j, key, value); }
static void emit(cJSON *j)
{
    char *s = cJSON_PrintUnformatted(j);
    if (!s || puts(s) == EOF || fflush(stdout)) exit(91);
    free(s);
    cJSON_Delete(j);
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
    for (unsigned i = occurrence_count; i; --i) {
        if (occurrences[i-1].live && occurrences[i-1].ht == ht && occurrences[i-1].slot == slot) return &occurrences[i-1];
    }
    void *node;
    memcpy(&node, rec->val, sizeof node);
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
    cJSON *free_chain=cJSON_AddArrayToObject(j,"free_chain");
    uint32_t s=ht->first_free_rec, n=0;
    while (s != LYHT_NO_RECORD && s<ht->size) {
        if(n++>=ht->size) exit(95);
        cJSON_AddItemToArray(free_chain,cJSON_CreateNumber(s)); s=lyht_get_rec(ht->recs,ht->rec_size,s)->next;
    }
    number(j,"free_terminal",s);
    return j;
}

static cJSON *node_json(unsigned i)
{
    cJSON *j=cJSON_CreateObject(); struct identity *id=&identities[i];
    number(j,"generation",i); number(j,"id",id->script); number(j,"creation_operation",id->operation); number(j,"creation_ordinal",id->ordinal); number(j,"source",id->source); cJSON_AddBoolToObject(j,"alive",id->alive);
    if (!id->alive) { cJSON_AddStringToObject(j,"current","unavailable"); return j; }
    const struct lyd_node *n=id->ptr;
    number(j,"context",LYD_CTX(n)==ctx?1:0);
    number(j,"hash",n->hash); number(j,"flags",n->flags); number(j,"prev",identity(n->prev)); number(j,"next",identity(n->next)); number(j,"parent",identity(n->parent));
    if(n->schema) cJSON_AddStringToObject(j,"schema",n->schema->name);
    else {const struct lyd_node_opaq *o=(const void*)n;number(j,"prefix_format",o->format);cJSON_AddBoolToObject(j,"prefix_bound",o->val_prefix_data!=NULL);}
    if(n->schema && (n->schema->nodetype & LYD_NODE_INNER)) { const struct lyd_node_inner *in=(const void*)n; number(j,"first",identity(in->child)); number(j,"tail",identity(in->child ? in->child->prev : NULL)); cJSON_AddItemToObject(j,"table",table_json(in->children_ht)); }
    cJSON *metas=cJSON_AddArrayToObject(j,"metadata"); for(const struct lyd_meta *m=n->meta;m;m=m->next) {
        cJSON_AddItemToArray(metas,cJSON_CreateString(m->name));
        if(!strcmp(m->name,"lyds_tree")) {
            struct lyd_value_lyds_tree *lt; LYD_VALUE_GET(&m->value,lt);
            cJSON *rb=cJSON_AddArrayToObject(j,"rb");
            struct nr_rb *stack[4096];unsigned count=0,visits=0;
            if(lt && lt->rbt)stack[count++]=(void*)lt->rbt;
            while(count) {if(++visits>4096)exit(97);struct nr_rb *r=stack[--count];cJSON *rj=cJSON_CreateObject();number(rj,"owner",i);number(rj,"node",identity(r->dnode));number(rj,"parent",identity(r->parent?r->parent->dnode:NULL));number(rj,"left",identity(r->left?r->left->dnode:NULL));number(rj,"right",identity(r->right?r->right->dnode:NULL));number(rj,"color",r->color);cJSON_AddItemToArray(rb,rj);if(count+2>4096)exit(97);if(r->right)stack[count++]=r->right;if(r->left)stack[count++]=r->left;}
        }
    }
    return j;
}

static void snapshot(const char *event, const void *node, const struct ly_ht *ht, int rc, const void *source)
{
    cJSON *j=cJSON_CreateObject(); cJSON_AddStringToObject(j,"event",event); number(j,"operation",op); number(j,"rc",rc); number(j,"node_generation",identity(node)); number(j,"source_generation",identity(source));
    if(ht) cJSON_AddItemToObject(j,"table",table_json(ht));
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
    if(!strcmp(event,"table.allocate") && observed(ht)) {
        if(table_identity_count+1==LIMIT)exit(98);
        table_identities[++table_identity_count]=(struct table_identity){ht,1};
    }
    if(!strcmp(event,"table.allocate")) for(unsigned i=0;i<retired_count;++i) if(retired_tables[i]==ht) retired_tables[i]=NULL;
    if(!strcmp(event,"node.allocate") || !strcmp(event,"node.dup.allocate")) {
        unsigned old=identity(node);
        if(!identities[old].alive) { if(identity_count+1==LIMIT)exit(90); unsigned i=++identity_count;identities[i]=(struct identity){node,i,0,op,++ordinal,0,1}; cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","allocation.address-reuse");number(j,"old_generation",old);number(j,"new_generation",i);emit(j); }
        unsigned i=identity(node); identities[i].source=identity(source); if(pending_script){identities[i].script=pending_script;pending_script=0;} return;
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
    if(!strcmp(event,"table.retire")) {if(retired_count==LIMIT)exit(96);retired_tables[retired_count++]=ht;table_identities[table_identity(ht)].alive=0;for(unsigned i=0;i<occurrence_count;++i) if(occurrences[i].ht==ht) occurrences[i].live=0;}
    if(!strcmp(event,"lyd_insert_hash.after") || !strcmp(event,"lyd_unlink_hash.after")) --hash_depth;
    if(remote_pending && !strcmp(event,"lyd_insert_after_node.after")) {
        snapshot("boundary.remote-last-corrupting-primitive",node,NULL,0,source);
        if(remote_stop) _Exit(0);
    }
    if(ub_stop && !strcmp(event,"merge2.back.no-successor")) { snapshot("boundary.merge2-before-uninitialized-continuation",node,NULL,0,source); _Exit(0); }
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
        int ndebug=0;
#ifdef NDEBUG
        ndebug=1;
#endif
        int written=printf("{\"pointer_bytes\":%zu,\"long_double_bytes\":%zu,\"long_double_mantissa_bits\":%d,\"pointer_record_bytes\":%zu,\"rb_node_bytes\":%zu,\"ndebug\":%d}\n",sizeof(void*),sizeof(long double),LDBL_MANT_DIG,(size_t)SIZEOF_LY_HT_REC+sizeof(struct lyd_node*),sizeof(struct nr_rb),ndebug);
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
    if(!rc) rc=lys_parse_mem(ctx,schema,LYS_IN_YANG,&mod);
    if(rc) return 2;
    enabled=1;
    /* A real children table supplies the hidden native callback. */
    struct lyd_node *seed=NULL,*child=NULL;
    events=0;
    rc=lyd_new_inner(NULL,mod,"c",0,&seed);
    for(unsigned i=0;!rc && i<4;++i) {char value[16];snprintf(value,sizeof value,"%u",i);rc=lyd_new_term(seed,mod,"v",value,0,&child);}
    if(!rc) {struct ly_ht *ht=((struct lyd_node_inner*)seed)->children_ht;if(ht){child_callback=ht->val_equal;child_data=ht->cb_data;}}
    if(rc || !child_callback) return 3;
    events=(argc>1 && !strcmp(argv[1],"--quiet"))?0:1;
    snapshot("driver.ready.actual-children-callback",NULL,NULL,0,NULL);
    char line[1024],cmd[32];
    while(fgets(line,sizeof line,stdin)) {
        unsigned a=0,b=0,c=0,d=0; int creating=0,refresh=0; char name[80]={0},value[256]={0};
        if(line[0]=='#' || sscanf(line,"%31s",cmd)!=1) continue;
        ++op; ordinal=0;pending_script=0; ly_err_clean(ctx,NULL);cJSON_Delete(log_messages);log_messages=NULL;
        rc=LY_SUCCESS;
        if(tombstoned_occurrence() && (!strcmp(cmd,"schema") || !strcmp(cmd,"validate") || !strcmp(cmd,"rewind") || !strcmp(cmd,"print") || !strcmp(cmd,"attach") || !strcmp(cmd,"change") || !strcmp(cmd,"find-next-loop") || !strcmp(cmd,"root") || !strcmp(cmd,"term") || !strcmp(cmd,"dup") || !strcmp(cmd,"unlink") || !strcmp(cmd,"detach-siblings") || !strcmp(cmd,"free") || !strcmp(cmd,"parse"))) {snapshot("boundary.tombstoned-semantic-probe",NULL,generic,0,NULL);return 0;}
        if(!strcmp(cmd,"new")) {if(sscanf(line,"%*s %u %u",&a,&b)!=2 || a>32768 || (b>1))return 4; if(generic)lyht_free(generic,NULL); generic=lyht_new(a,sizeof(struct lyd_node*),child_callback,child_data,b);if(!generic)return 5;}
        else if(!strcmp(cmd,"root")) {if(sscanf(line,"%*s %u %79s",&a,name)!=2 || !a||a>=4096)return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_inner(NULL,mod,name,0,&nodes[a]);}
        else if(!strcmp(cmd,"term")) {int fields=sscanf(line,"%*s %u %u %79s %255s %u",&a,&b,name,value,&c);if(fields<4 || !a || a>=4096 || b>=4096)return 4;if(b&&!require_handle(b))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_new_term(nodes[b],mod,name,value,c,&nodes[a]);}
        else if(!strcmp(cmd,"dup")) {if(sscanf(line,"%*s %u %u %u %u",&a,&b,&c,&d)!=4 || !a||a>=4096||b>=4096||c>=4096)return 4;if(!require_handle(b)||(c&&!require_handle(c)))return 4;if(!prepare_output(a))return 4;creating=1;pending_script=a;rc=lyd_dup_single(nodes[b],nodes[c],d,&nodes[a]);}
        else if(!strcmp(cmd,"unlink")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;rc=lyd_unlink_tree(nodes[a]);}
        else if(!strcmp(cmd,"attach")) {if(sscanf(line,"%*s %u %u",&a,&b)!=2||a>=4096||b>=4096||!require_handle(a)||!require_handle(b))return 4;rc=lyd_insert_child(nodes[a],nodes[b]);}
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
            number(j,"winner",identity(winner));number(j,"current_parent",identity(winner?winner->parent:NULL));
            cJSON_AddItemToObject(j,"before",before);cJSON_AddItemToObject(j,"after",table_json(queried));emit(j);
        }
        else if(!strcmp(cmd,"find-next-loop")) {if(sscanf(line,"%*s %u %u %u",&a,&b,&c)!=3||a>=4096||c>1024||!generic||!require_handle(a))return 4;struct lyd_node *iter=nodes[a];unsigned steps=0;for(;steps<c;++steps){void *match=NULL;rc=lyht_find_next(generic,&iter,b,&match);if(rc)break;memcpy(&iter,match,sizeof iter);}if(steps==c){snapshot("boundary.watchdog.find-next-cycle",iter,generic,0,NULL);return 0;}}
        else if(!strcmp(cmd,"insert") || !strcmp(cmd,"remove")) {if(sscanf(line,"%*s %u %u %u",&a,&b,&c)<2||!a||a>=4096||!generic)return 4; if(!nodes[a]) {pending_script=a;rc=lyd_dup_single(child,NULL,LYD_DUP_NO_META,&nodes[a]);if(rc)return 7;identities[identity(nodes[a])].script=a;handle_generations[a]=identity(nodes[a]);} if(!require_handle(a))return 4;if(strcmp(cmd,"remove") && generic->first_free_rec>=generic->size)free_slot_boundary();if(!strcmp(cmd,"remove"))rc=lyht_remove(generic,&nodes[a],b);else if(c)rc=lyht_insert(generic,&nodes[a],b,NULL);else rc=lyht_insert_no_check(generic,&nodes[a],b,NULL);}
        else if(!strcmp(cmd,"I") || !strcmp(cmd,"U")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;if(cmd[0]=='I')rc=lyd_insert_hash(nodes[a]);else lyd_unlink_hash(nodes[a]);}
        else if(!strcmp(cmd,"free")) {
            if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;
#ifdef NR_UNMODIFIED
            /* The control has no allocation hooks. Retire observer metadata
             * conservatively before freeing; only public outputs are compared. */
            for(unsigned i=1;i<=identity_count;++i)identities[i].alive=0;
#endif
            lyd_free_tree(nodes[a]);if(!identities[handle_generations[a]].alive)nodes[a]=NULL;
        }
        else if(!strcmp(cmd,"rewind")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;refresh=1;nodes[a]=lyd_first_sibling(nodes[a]);}
        else if(!strcmp(cmd,"validate")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;refresh=1;rc=lyd_validate_all(&nodes[a],ctx,LYD_VALIDATE_PRESENT,NULL);}
        else if(!strcmp(cmd,"check-handle")) {if(sscanf(line,"%*s %u",&a)!=1||!require_handle(a))return 4;}
        else if(!strcmp(cmd,"print")) {if(sscanf(line,"%*s %u",&a)!=1||a>=4096||!require_handle(a))return 4;char *json=NULL;rc=lyd_print_mem(&json,nodes[a],LYD_JSON,LYD_PRINT_SHRINK);cJSON *j=cJSON_CreateObject();cJSON_AddStringToObject(j,"event","public.result");number(j,"operation",op);number(j,"rc",rc);if(json)cJSON_AddStringToObject(j,"json",json);emit(j);free(json);}
        else if(!strcmp(cmd,"dump")) { /* Snapshot below is the requested result. */ }
        else return 4;
        if((creating||refresh) && a && a<4096 && nodes[a]) {unsigned generation=identity(nodes[a]);handle_generations[a]=generation;if(creating)identities[generation].script=a;}
        snapshot(cmd,NULL,generic,rc,NULL);
    }
    if(ferror(stdin))return 6;
    /* Each trace process owns its forest; avoid walking deliberately corrupt graphs. */
    return 0;
}
