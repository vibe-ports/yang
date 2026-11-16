# SPDX-License-Identifier: BSD-3-Clause
"""Deterministic, bounded live-oracle requests for custom-schema contracts."""
import argparse
import json
import os
import pathlib
import subprocess

from check import compare_custom
from drivers import comparison_driver
from identity import requests, validate

ROOT = pathlib.Path(__file__).resolve().parent
# Every data/schema name in the four built-in modules, including RPC ancestors.
NAMES = ('c', 'p', 'q', 'll', 'v', 'z', 'r', 'a', 'a2', 'o', 'd', 'l', 'kl',
         'k', 'x', 'def', 'i', 'j', 'other', 'input', 'output')
KINDS = ('leaf', 'leaf-list', 'container', 'list0', 'list1', 'list2', 'list3', 'anydata', 'anyxml')


def execute(driver, script, mode):
    result = subprocess.run([str(driver), mode, '--read-schema'], input=script,
                            text=True, capture_output=True, timeout=25, check=False,
                            env=dict(os.environ, GLIBC_TUNABLES='glibc.malloc.tcache_count=7'))
    assert result.returncode in (0, 4), ('unexpected driver exit', result.returncode, result.stderr, script)
    return result.returncode, [json.loads(line) for line in result.stdout.splitlines()]


def paired(driver, plain, script, mode):
    traces = []
    for binary in (driver, plain):
        rc, rows = execute(binary, script, mode)
        assert rc == 0, ('valid custom replay rejected', script)
        # Pin successful completion independently of comparison/normalization.
        assert rows[-1]['operation'] == len(requests(script)), 'early custom stop'
        assert not any(r['event'].startswith('boundary.') for r in rows), 'unexpected custom boundary'
        traces.append(rows)
    try:
        compare_custom(script, *traces)
    except (AssertionError, KeyError, IndexError, TypeError):
        print(script, flush=True)
        raise
    return traces


def rejected(driver, plain, prefix, command, mode, traces):
    for binary, before in zip((driver, plain), traces, strict=True):
        rc, after = execute(binary, prefix + command + '\n', mode)
        assert rc == 4, ('unrepresentable request accepted', command, rc)
        assert before == after, ('rejected command entered native code', command)


def generated(name, kind, depth):
    keys = int(kind[-1]) if kind.startswith('list') else 0
    if kind.startswith('list'):
        body = ('config false;' if not keys else 'key "' + ' '.join('abc'[:keys]) + '";')
        body += ''.join('leaf ' + key + ' {type string;}' for key in 'abc'[:keys])
        body += 'leaf payload {type string;}'
        statement = 'list ' + name + ' {' + body + '}'
        value = [{**{k: k.upper() for k in 'abc'[:keys]}, 'payload': 'A'}]
    elif kind in ('leaf', 'leaf-list'):
        statement = kind + ' ' + name + ' {type string;}'
        value = 'A' if kind == 'leaf' else ['A']
    elif kind == 'container':
        statement = 'container ' + name + ' {presence yes;leaf payload {type string;}}'
        value = {'payload': 'A'}
    else:
        statement = kind + ' ' + name + ';'
        # Empty any payloads exercise node layout without extending part 1
        # into the separate embedded-data lifetime/representation model.
        value = {}
    # Unique wrapper names keep the varied name independent of path position.
    data = {name: value}
    for index in reversed(range(depth)):
        statement = 'container wrap' + str(index) + ' {' + statement + '}'
        data = {'wrap' + str(index): data}
    module = 'module m {yang-version 1.1;namespace urn:m;prefix m;' + statement + '}'
    top, value = next(iter(data.items()))
    script = 'context ' + module + '\nparse 1 ' + json.dumps({'m:' + top: value}, separators=(',', ':')) + '\n'
    parent, target = 0, 1
    for level in range(depth):
        parent, target = target, target + 1
        script += f'child {target} {parent} 0\n'
    out = target + 1
    # Both comparison orders, every selection entry, public serialization,
    # recursive duplication, and real unlink/attach for nested instances.
    script += f'dup {out} {target} 0 3\nequal {target} {out} 1\nequal {out} {target} 1\n'
    script += f'read-instance {target} {target} {parent}\nread-schema {target} {target} {parent}\n'
    script += f'read-opaque {target} {target} {parent} absent\n'
    # Part 1 covers schema-only dispatch; typed non-NULL selection is separate.
    script += f'read-value {target} {target} {parent}\nprint 1\n'
    if kind in ('leaf', 'leaf-list'):
        script += f'change {target} B\n'
    if depth:
        script += f'unlink {target}\nattach {parent} {target}\n'
    # Safe fixed-shape constructors remain native errors for mismatched kinds.
    # The list protocol cannot carry two or more keys, even when parse can.
    command = f'list {out + 1} {parent} {name} A'
    return script, command, kind.startswith('list') and keys <= 1


def sweep(driver, plain):
    valid = rejected_count = runs = 0
    for file in sorted((ROOT / 'cases' / 'custom').glob('*.calls')):
        for mode in ('--quiet', '--events'):
            paired(driver, plain, file.read_text(), mode)
            valid += 1; runs += 2
    for index, name in enumerate(NAMES):
        for variant, kind in enumerate(KINDS):
            script, command, supported = generated(name, kind, (index + variant) % 3)
            for mode in ('--quiet', '--events'):
                traces = paired(driver, plain, script, mode)
                valid += 1; runs += 2
                if supported:
                    paired(driver, plain, script + command + '\n', mode)
                    valid += 1; runs += 2
                else:
                    rejected(driver, plain, script, command, mode, traces)
                    rejected_count += 1; runs += 2
                # Exact protocol arity, including extra value tokens.
                rejected(driver, plain, script, command + ' B C', mode, traces)
                rejected_count += 1; runs += 2
    summary = {'custom_schema_sweep': True, 'names': len(NAMES), 'kinds': len(KINDS),
               'valid_pairs': valid, 'rejected_pairs': rejected_count, 'driver_runs': runs, 'skips': 0}
    print(json.dumps(summary), flush=True)
    return summary


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--driver', type=pathlib.Path, default=os.environ.get('YANG_NATIVE_REGISTRATION_DRIVER'))
    parser.add_argument('--comparison-driver', type=pathlib.Path)
    args = parser.parse_args()
    assert args.driver, 'set native driver'
    sweep(args.driver.resolve(), comparison_driver(args.driver.resolve(), args.comparison_driver))
