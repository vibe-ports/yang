# SPDX-License-Identifier: BSD-3-Clause
"""Output-statement probes, including fields native syntax cannot make invalid.

Substitute only the argument copied at each inventoried emitter, not native
storage or algorithms. Compile the resulting observer against both libraries.
This also runs unchanged with --source pointing at the pre-fix driver.
"""
import argparse
import json
import os
import pathlib
import re
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent
# Independent inventory: (object variable, field, native expression).
FIELDS = (
    ('j', 'msg', 'msg'), ('j', 'path', 'data_path'), ('j', 'schema_path', 'schema_path'),
    ('j', 'schema', 'n->schema->name'), ('j', 'module', 'n->schema->module->name'),
    ('j', 'value', 'lyd_get_value(n)'),
    ('j', 'opaque_name', 'o->name.name'), ('j', 'opaque_module', 'o->name.module_name'),
    ('j', 'opaque_prefix', 'o->name.prefix'), ('j', 'opaque_value', 'o->value'),
    ('r', 'module', 's->module->name'), ('r', 'name', 's->name'),
    ('mj', 'name', 'm->name'), ('mj', 'namespace', 'm->ns'), ('mj', 'revision', 'm->revision'),
    ('ej', 'msg', 'e->msg'), ('ej', 'path', 'e->data_path'), ('ej', 'apptag', 'e->apptag'),
    ('j', 'name', 'name'), ('j', 'value', 'fields==4?value:NULL'),
    ('j', 'name', 'fields==4?value:"unknown"'), ('j', 'json', 'json'),
)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=pathlib.Path, default=pathlib.Path(os.environ.get('YANG_NATIVE_BYTE_SOURCE', ROOT / 'driver.c')))
    args = parser.parse_args()
    source = args.source.read_text()
    # Each real call is copied verbatim except its byte argument. This isolates
    # encoding from native grammar restrictions and optional log dispatch.
    labels = {'event', 'continuity', 'current', 'allocation_scope'}
    emitters = re.findall(r'(?:raw_bytes_or_null|bytes_or_null|cJSON_AddStringToObject)\((\w+),"([^"]+)",', source)
    assert sorted((obj, key) for obj, key in emitters if key not in labels) == sorted((obj, key) for obj, key, _ in FIELDS), 'byte emitter inventory drift'
    assert set(re.findall(r'cJSON_CreateString\(([^)]+)\)', source)) <= {'hex', 'm->name', 'value'}, 'unlisted byte array emitter'
    calls = []
    for obj, key, expression in FIELDS:
        candidates = [f'{fn}({obj},"{key}",{expression})' for fn in ('bytes_or_null', 'raw_bytes_or_null', 'cJSON_AddStringToObject')]
        matches = [c for c in candidates if re.search(r'(?<![A-Za-z0-9_])' + re.escape(c), source)]
        assert len(matches) == 1, ('missing/ambiguous emitter', obj, key, expression)
        call = matches[0].replace(f',{expression})', ',probe)')
        if call.startswith('cJSON_'): call = f'if(!{call})exit(91)'
        calls.append(f'{{cJSON *{obj}=cJSON_CreateObject();if(!{obj})exit(91);{call};emit({obj});}}')
    metadata = re.search(r'cJSON_AddItemToArray\(metas,(?:raw_bytes_value|bytes_value|cJSON_CreateString)\(m->name\)\)', source)
    assert metadata, 'missing metadata emitter'
    calls.append('{cJSON *j=cJSON_CreateObject();if(!j)exit(91);cJSON *metas=cJSON_AddArrayToObject(j,"metadata");if(!metas)exit(91);if(!' + metadata[0].replace('m->name', 'probe') + ')exit(91);emit(j);}')
    assert 'bytes_value(arg)' in source, 'missing request emitter'
    calls.append('{cJSON *j=cJSON_CreateObject();if(!j)exit(91);cJSON *item=bytes_value(probe);if(!item||!cJSON_AddItemToObject(j,"argument",item))exit(91);emit(j);}')
    # Include the real helpers and all emitters in the translation unit. main
    # only invokes extracted calls, so no native objects are modified.
    with tempfile.TemporaryDirectory(dir=ROOT / '.build') as temporary:
        work = pathlib.Path(temporary)
        (work / 'driver.c').write_text(source)
        # Relative includes are resolved from the copied source's directory.
        (work / 'native_observer.h').symlink_to(ROOT / 'native_observer.h')
        # driver.c uses ../cjson/cJSON.h; a preprocessor include path supplies it.
        counter = """#define _POSIX_C_SOURCE 200809L
#include <stdlib.h>
#include <stdio.h>
static size_t allocation_calls, allocation_bytes;
static void *probe_malloc(size_t size) {
    ++allocation_calls; allocation_bytes+=size; return malloc(size);
}
#define malloc probe_malloc
#define main nr_replay_main
#include "driver.c"
#undef main
#undef malloc
int main(void){char probe[257];const unsigned char pattern[]={1,10,13,9,34,92,255,254,192,128};for(size_t k=0;k<256;++k)probe[k]=(char)pattern[k%10];probe[256]=0;
"""
        measured = r'''cJSON_Hooks hooks={probe_malloc,free};cJSON_InitHooks(&hooks);
const size_t lengths[]={0,1,16,64,256};char input[257];
if(printf("{\"encoding_work\":[")<0)return 91;
for(unsigned i=0;i<5;++i){
    size_t len=lengths[i];memset(input,'x',len);input[len]=0;
    allocation_calls=allocation_bytes=0;
    cJSON *raw=cJSON_CreateString(input);if(!raw)return 91;
    size_t raw_calls=allocation_calls,raw_bytes=allocation_bytes;cJSON_Delete(raw);
    allocation_calls=allocation_bytes=0;
    cJSON *hex=ENCODER(input);
    size_t hex_calls=allocation_calls,hex_bytes=allocation_bytes;cJSON_Delete(hex);
    if(printf("%s[%zu,%zu,%zu,%zu,%zu]",i?",":"",len,raw_calls,raw_bytes,hex_calls,hex_bytes)<0)return 91;
}
if(puts("]}")==EOF || fflush(stdout))return 91;
cJSON_InitHooks(NULL);return 0;}'''
        encoder = 'raw_bytes_value' if 'static cJSON *raw_bytes_value' in source else 'bytes_value'
        body = counter + '\n'.join(calls) + '\n' + measured.replace('ENCODER', encoder) + '\n'
        (work / 'probe.c').write_text(body)
        failed_builds = 0
        for plain in (False, True):
            build = ROOT / '.build' / ('config' if plain else 'static')
            native = ROOT / '.build' / ('source' if plain else 'instrumented')
            library = ROOT / '.build/libyang-unmodified.a' if plain else build / 'libyang.a'
            binary = work / ('unmodified' if plain else 'instrumented')
            command = ['gcc', '-DNDEBUG', '-O2', '-std=c11', '-Wl,--wrap=free', '-Dlys_find_child=nr_entry_find_child',
                       *(['-DNR_UNMODIFIED'] if plain else []),
                       '-I' + str(ROOT), '-I' + str(native / 'src'), '-I' + str(build / 'libyang'),
                       '-I' + str(build / 'compat'), '-I' + str(native / 'src/plugins_exts'),
                       '-o', str(binary), str(work / 'probe.c'), str(ROOT / 'entry_contracts.c'),
                       str(ROOT / '../cjson/cJSON.c'), str(library), '-lpcre2-8', '-lm', '-lpthread', '-ldl']
            try:
                subprocess.run(command, check=True, capture_output=True)
            except subprocess.CalledProcessError as error:
                raise AssertionError(error.stderr.decode('utf-8')) from error
            output = subprocess.run([binary], check=True, capture_output=True).stdout
            # Strict transport, then exact byte round-trip, independently of the
            # production evidence validator's knowledge of the field names.
            lines = output.splitlines()
            assert len(lines) == len(FIELDS) + 3
            failures = []
            for line, (obj, key, expression) in zip(lines, FIELDS):
                try:
                    row = json.loads(line.decode('utf-8'))
                    assert bytes.fromhex(row[key]) == bytes((1, 10, 13, 9, 34, 92, 255, 254, 192, 128)[i % 10] for i in range(256))
                except (UnicodeError, ValueError, AssertionError):
                    failures.append((obj, key, expression))
            for line, key in ((lines[-3], 'metadata'), (lines[-2], 'argument')):
                try:
                    row = json.loads(line.decode('utf-8'))
                    value = row[key][0] if key == 'metadata' else row[key]
                    assert bytes.fromhex(value) == bytes((1, 10, 13, 9, 34, 92, 255, 254, 192, 128)[i % 10] for i in range(256))
                except (UnicodeError, ValueError, AssertionError):
                    failures.append((key, key, 'element'))
            measured = json.loads(lines[-1])['encoding_work']
            assert [row[0] for row in measured] == [0, 1, 16, 64, 256]
            allocation_failure = any(raw_calls != 2 or hex_calls != raw_calls or hex_bytes != raw_bytes
                                     for size, raw_calls, raw_bytes, hex_calls, hex_bytes in measured)
            print(json.dumps({'byte_emitters': len(FIELDS) + 2, 'unmodified': plain, 'unencoded': failures,
                              'encoding_work': measured, 'allocation_failure': allocation_failure}), flush=True)
            if allocation_failure: failed_builds += 1
            if failures: failed_builds += 1
        assert not failed_builds, 'unencoded native output fields'


if __name__ == '__main__':
    main()
