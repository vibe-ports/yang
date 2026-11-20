# SPDX-License-Identifier: BSD-3-Clause
"""Test-driver output allocation failures, with no native algorithm replacement."""
import argparse
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--protocol', type=pathlib.Path, default=ROOT / 'protocol.py')
    parser.add_argument('--normalization-only', action='store_true')
    parser.add_argument('--work-only', action='store_true')
    parser.add_argument('--source', type=pathlib.Path, default=ROOT / 'driver.c')
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location('probe_protocol', args.protocol)
    protocol = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(protocol)
    samples = ('Internal error (first.c:10).', 'Invalid argument node (lyd_insert_hash_nr_impl()).',
               'Internal error (first\xff.c:10).', 'Internal error (first.c:10).\n',
               'Invalid argument node\xff (lyd_insert_hash_nr_impl()).',
               'Invalid argument node (lyd_insert_hash_nr_impl()).\n')
    for channel in ('diagnostics', 'logs'):
        for value in samples:
            changed = value.replace('first', 'second').replace('lyd_insert_hash_nr_impl', 'lyd_insert_hash')
            record = dict(code=7, msg=protocol.encoded(value))
            other = dict(code=7, msg=protocol.encoded(changed))
            assert protocol.messages([record]) != protocol.messages([other]), ('lost user message', channel, value)
    print(json.dumps({'normalization_preserves_user_bytes': len(samples) * 2}), flush=True)
    for code, value in ((6, samples[0]), (3, samples[1])):
        changed = value.replace('first.c:10', 'second.h:20').replace('lyd_insert_hash_nr_impl', 'lyd_insert_hash')
        assert protocol.messages([dict(code=code, msg=protocol.encoded(value))]) == protocol.messages([dict(code=code, msg=protocol.encoded(changed))])
        assert protocol.messages([dict(code=code, msg=protocol.encoded(value+'\n'))])[0]['msg'] == protocol.encoded(value+'\n'), 'normalized trailing newline'
    if args.normalization_only: return
    source = args.source.read_text()
    with tempfile.TemporaryDirectory(dir=ROOT / '.build/round8') as temporary:
        work = pathlib.Path(temporary)
        (work / 'driver.c').write_text(source)
        (work / 'probe.c').write_text(r'''#define _POSIX_C_SOURCE 200809L
#include <stdlib.h>
#include <stdio.h>
static size_t calls, bytes, fail_at;
static void *probe_malloc(size_t size) {
    ++calls;bytes+=size;if(calls==fail_at)return NULL;return malloc(size);
}
#define main nr_replay_main
#include "driver.c"
#undef main
int main(int argc,char **argv) {
    fail_at=argc>1?strtoul(argv[1],NULL,10):0;
    cJSON_Hooks hooks={probe_malloc,free};
    if(argc>2) {
        struct lys_module *module=NULL;struct lyd_node *node=NULL;
        ly_set_log_clb(logger);ly_log_options(LY_LOSTORE|LY_LOLOG);
        if(ly_ctx_new(ly_yang_module_dir(),LY_CTX_NO_YANGLIBRARY,&ctx))return 103;
        context_identity(ctx);
        if(lys_parse_mem(ctx,schema,LYS_IN_YANG,&module))return 103;
        cJSON_Delete(log_messages);log_messages=NULL;
        calls=bytes=0;cJSON_InitHooks(&hooks);
        LY_ERR rc=lyd_new_inner(NULL,module,"nonexistent",0,&node);
        if(!rc)return 103;
        snapshot("native.root-rejected",NULL,NULL,rc,NULL);
        return 0;
    }
    cJSON_InitHooks(&hooks);
    logger(LY_LLERR,"native diagnostic",NULL,NULL,0);
    /* Match snapshot's NULL fallback; this used to silently lose the log. */
    cJSON *j=cJSON_CreateObject();
    cJSON_AddItemToObject(j,"logs",log_messages?cJSON_Duplicate(log_messages,1):cJSON_CreateArray());
    cJSON *a=cJSON_AddArrayToObject(j,"array");
    cJSON_AddItemToArray(a,cJSON_CreateNumber(42));
    cJSON_AddBoolToObject(j,"present",1);number(j,"number",1);
    cJSON_AddStringToObject(j,"event","probe");
    bytes_or_null(j,"null",NULL);bytes_or_null(j,"bytes","value");
    emit(j);
    fprintf(stderr,"%zu %zu\n",calls,bytes);
    return 0;
}''')
        for plain in (False, True):
            build = ROOT / '.build' / ('config' if plain else 'static')
            native = ROOT / '.build' / ('source' if plain else 'instrumented')
            library = ROOT / '.build/libyang-unmodified.a' if plain else build / 'libyang.a'
            binary = work / ('unmodified' if plain else 'instrumented')
            command = ['gcc', '-DNDEBUG', '-O2', '-std=c11', '-Wl,--wrap=free', *(['-Wl,--wrap=lyht_remove,--wrap=lyht_remove_with_resize_cb,--wrap=lyht_find_next,--wrap=lyht_find_next_with_collision_cb'] if '__wrap_lyht_remove_with_resize_cb' in source else []), '-Dlys_find_child=nr_entry_find_child',
                       *(['-DNR_UNMODIFIED'] if plain else []), '-I' + str(ROOT),
                       '-I' + str(native / 'src'), '-I' + str(build / 'libyang'),
                       '-I' + str(build / 'compat'), '-I' + str(native / 'src/plugins_exts'),
                       '-o', str(binary), str(work / 'probe.c'), str(ROOT / 'entry_contracts.c'),
                       str(ROOT / '../cjson/cJSON.c'), str(library), '-lpcre2-8', '-lm', '-lpthread', '-ldl']
            subprocess.run(command, check=True, capture_output=True)
            positive = subprocess.run([binary], check=True, capture_output=True)
            count, size = map(int, positive.stderr.split())
            live = subprocess.run([binary, '0', 'native'], check=True, capture_output=True)
            live_row = json.loads(live.stdout)
            assert live_row['rc'] != 0 and live_row['diagnostics'] and live_row['logs']
            if 'code' in live_row['logs'][-1]:
                assert live_row['logs'][-1]['code'] == live_row['diagnostics'][-1]['code']
            if not args.work_only:
                live_failed = subprocess.run([binary, '1', 'native'], capture_output=True)
                assert live_failed.returncode == 91, ('native logger allocation certified evidence', plain, live_failed.returncode, live_failed.stdout)

            assert len(json.loads(positive.stdout)['logs']) == 1
            for index in (() if args.work_only else range(1, count + 1)):
                failed = subprocess.run([binary, str(index)], capture_output=True)
                assert failed.returncode == 91, ('allocation failure certified evidence', plain, index, failed.returncode, failed.stdout)
            print(json.dumps({'unmodified': plain, 'allocation_work': [count, size], 'injected_failures': count}), flush=True)


if __name__ == '__main__':
    main()
