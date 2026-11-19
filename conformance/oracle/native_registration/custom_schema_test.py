# SPDX-License-Identifier: BSD-3-Clause
"""Deterministic, bounded live-oracle requests for custom-schema contracts."""
import argparse
import json
import os
import pathlib
import subprocess

from check import compare_custom
from drivers import comparison_driver
from identity import INPUTS, requests, validate

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


def precondition_sweep(driver, plain):
    """Exercise both operand orders and every multi-input command with live
    schema/NULL-schema nodes from retained and current contexts. The third
    input rotates independently; malformed layouts must stop before entry.
    """
    prefix = ('root 1 c\nterm 2 1 p A\nopaque 3 0 u A\n'
              'context\nroot 11 c\nterm 12 11 p A\nopaque 13 0 u A\n'
              'context-keyless\nroot 21 c\nterm 22 21 p A\nopaque 23 0 u A\n')
    tables = ''.join(f'term {base+5} {base+1} q A\nterm {base+6} {base+1} ll A\nterm {base+7} {base+1} ll B\n'
                     for base in (0, 10, 20))
    handles = (0, 1, 2, 3, 11, 12, 13, 21, 22, 23)
    templates = {
        'attach': 'attach {a} {b}', 'before': 'before {a} {b}',
        'schema': 'schema {a} {b}', 'equal': 'equal {a} {b} 1',
        'null-reads': 'null-reads {a} {b}', 'instance': 'instance {a} {b}',
        'record': 'record {a} {b} 7 1', 'record-as': 'record-as {a} {b} {c} 1',
        'dup': 'dup 90 {a} {b} 5', 'dup-to-context': 'dup-to-context 90 {a} {b} 5',
        'read-schema': 'read-schema {a} {b} {c}',
        'read-instance': 'read-instance {a} {b} {c}',
        'read-value': 'read-value {a} {b} {c}',
        'read-opaque': 'read-opaque {a} {b} {c} u',
        'frontier': 'frontier {a} {b} 2 12 22',
    }
    assert set(templates) == {cmd for cmd, positions in INPUTS.items() if len(positions) > 1}
    def expected(command, a, b, c):
        # Independent table for this fixed setup: each context owns one inner,
        # one terminal (plus its siblings), and one detached opaque node.
        def kind(h): return h % 10 if h else 0
        def context(h): return h // 10 if h else None
        def parent(h): return h - 1 if kind(h) == 2 else 0
        def comparable(a, b):
            return not a or not b or context(a) == context(b) or (kind(a) != 3 and kind(b) != 3)
        if command == 'equal': return comparable(a, b)
        if command == 'instance': return bool(b and comparable(a, b))
        if command == 'attach':
            return bool(a and b and kind(a) in (1, 3) and a != b and
                        context(a) == context(b) and (kind(a) == 3 or kind(b) == 3 or parent(b) == a))
        if command == 'before':
            return bool(a and b and a != b and kind(b) == 3 and context(a) == context(b))
        if command == 'schema': return bool(kind(a) == 1 and b and kind(b) != 3)
        if command == 'null-reads': return False  # all three owners have children
        if command in ('record', 'record-as'):
            return bool(kind(a) == 1 and b and kind(b) != 3 and (command == 'record' or c))
        if command in ('dup', 'dup-to-context'):
            destination = context(a) if command == 'dup' else 2
            return bool(a and (not b or (kind(b) in (1, 3) and context(b) == destination and
                                        (not parent(a) or kind(b) == 1))))
        if command.startswith('read-'):
            return bool(b and (not c or kind(c) == 1) and
                        (command not in ('read-schema', 'read-value') or kind(b) != 3) and
                        (command != 'read-instance' or comparable(a, b)))
        if command == 'frontier': return False  # existing p cannot be abandoned
        raise AssertionError(command)

    accepted = refused = runs = 0
    for mode in ('--quiet', '--events'):
        setup = paired(driver, plain, prefix, mode)
        table_setup = paired(driver, plain, prefix + tables, mode)
        runs += 4
        for command, template in templates.items():
            command_prefix = prefix + tables if command in ('record', 'record-as') else prefix
            command_setup = table_setup if command in ('record', 'record-as') else setup
            for i, a in enumerate(handles):
                for j, b in enumerate(handles):
                    c = handles[(i + 2*j) % len(handles)]
                    script = command_prefix + template.format(a=a, b=b, c=c) + '\n'
                    results = [execute(binary, script, mode) for binary in (driver, plain)]
                    runs += 2
                    assert results[0][0] == results[1][0] == (0 if expected(command, a, b, c) else 4), ('admission differs', script, [r[0] for r in results])
                    if results[0][0] == 4:
                        assert [rows for _, rows in results] == command_setup, ('entered rejected native call', script)
                        refused += 1
                    else:
                        for _, rows in results:
                            assert not any(r['event'].startswith('boundary.') for r in rows), script
                            assert rows[-1]['operation'] == len(requests(script)), script
                        compare_custom(script, results[0][1], results[1][1])
                        accepted += 1
        controls = (
            'root 1 c\nterm 2 1 p A\nroot 3 c\nnull-reads 3 2\n',
            'root 1 c\nroot 2 c\nroot 3 c\nroot 4 c\nroot 5 c\nfrontier 1 2 3 4 5\n',
            'opaque 1 0 u A\nroot 2 c\nequal 1 2 4\nequal 2 1 4\nequal 0 1 0\nequal 1 0 0\n',
        )
        for script in controls:
            paired(driver, plain, 'context\n' + script, mode)
            accepted += 1; runs += 2
        # Optional free/reuse operands are historical handles, so they are not
        # listed as two live INPUTS. Complete each admitted allocator control.
        reuse_prefix = prefix + 'root 31 c\nterm 32 31 p spare\n'
        setup_reuse = paired(driver, plain, reuse_prefix, mode)
        runs += 2
        for command in ('free', 'free-root'):
            for a in handles:
                for b in handles:
                    valid = bool(a and b and (a == b or (a % 10 == 1 and b == a + 1) or
                                              (command == 'free-root' and b % 10 in (1, 2) and
                                               a % 10 in (1, 2) and a // 10 == b // 10)))
                    # Opaque reuse storage has no supported allocator layout.
                    valid = valid and b % 10 in (1, 2)
                    suffix = ('root 90 c' if b % 10 == 1 else 'term 90 31 p A')
                    script = reuse_prefix + f'{command} {a} {b}\n'
                    if valid: script += '# reuse-next self\n' + suffix + '\n'
                    traces = [execute(binary, script, mode) for binary in (driver, plain)]
                    assert all(rc == (0 if valid else 4) for rc, _ in traces), (command, a, b)
                    if valid:
                        compare_custom(script, traces[0][1], traces[1][1]); accepted += 1
                    else:
                        assert [rows for _, rows in traces] == setup_reuse
                        refused += 1
                    runs += 2
        # Every newly rejected precondition has an independent named live case.
        for file in sorted((ROOT / 'cases' / 'preconditions').glob('*.calls')):
            script = 'context\n' + file.read_text()
            lines = script.rstrip().split('\n')
            before = '\n'.join(lines[:-1]) + '\n'
            traces = paired(driver, plain, before, mode)
            rejected(driver, plain, before, lines[-1], mode, traces)
            refused += 1; runs += 4
    return {'contexts': 3, 'commands': len(templates) + 2, 'valid_pairs': accepted, 'rejected_pairs': refused,
            'driver_runs': runs, 'skips': 0}


def admission_sweep(driver, plain):
    """Resolved aliases and actual destination paths, independent of source
    path lookup. Each rejected request preserves the exact completed prefix.
    """
    valid = refused = runs = 0

    def check(prefix, command, accepted):
        nonlocal valid, refused, runs
        for mode in ('--quiet', '--events'):
            traces = paired(driver, plain, prefix, mode)
            runs += 2
            if accepted:
                paired(driver, plain, prefix + command + '\n', mode)
                valid += 1
            else:
                rejected(driver, plain, prefix, command, mode, traces)
                refused += 1
            runs += 2

    # All multi-input commands have distinct handles resolving to one node.
    # Reads and pointer-only record controls permit aliases; link writers do
    # not. Duplication's compiled source cannot be its own schema parent.
    prefix = ('context module m {yang-version 1.1;namespace urn:m;prefix m;'
              'container w {container c {leaf p {type string;} leaf q {type string;}'
              'leaf-list ll {type string;ordered-by user;}list i {key k;leaf k {type string;}}}}leaf p {type string;}}\n'
              'parse 1 {"m:w":{"c":{"p":"A","q":"B","ll":["A","B"]}}}\n'
              'child 2 1 0\nchild 3 1 0\nchild 4 2 0\nchild 5 3 0\n'
              'opaque 6 2 u A\nchild 7 3 4\nchild 8 2 2\nchild 9 3 2\n')
    aliases = {
        'attach': ('attach 2 3', False), 'before': ('before 8 9', False),
        'schema': ('schema 2 3', True), 'equal': ('equal 4 5 1', True),
        'null-reads': ('null-reads 2 3', False), 'instance': ('instance 4 5', True),
        'record': ('record 2 3 7 1', True), 'record-as': ('record-as 2 4 5 1', True),
        'dup': ('dup 90 2 3 0', False), 'dup-to-context': ('dup-to-context 90 2 3 0', False),
        'read-schema': ('read-schema 4 5 2', True), 'read-instance': ('read-instance 4 5 2', True),
        'read-value': ('read-value 4 5 2', True), 'read-opaque': ('read-opaque 6 7 2 u', True),
        'frontier': ('frontier 2 4 5 8 9', False),
    }
    assert set(aliases) == {cmd for cmd, positions in INPUTS.items() if len(positions) > 1}
    for command, accepted in aliases.values():
        check(prefix, command, accepted)
    # Output/input aliases and optional historical reuse handles complete the
    # protocol inventory beyond INPUTS (which lists live inputs only).
    outputs = ('term 3 2 p A', 'list 3 2 i A', 'opaque 3 2 u A',
               'opaque-invalid-xml 3 2', 'child 3 2 0')
    for command in outputs:
        check(prefix, command, False)
    historical = (
        'free 4 5\n# reuse-next self\nterm 90 2 p A',
        'free-root 4 5\n# reuse-next self\nterm 90 0 p A',
        'free 4 5\nterm 90 2 p A 0 5',
        'new 8 1\nfree 4 5\ninsert 90 7 1 5',
    )
    for command in historical:
        check(prefix, command, True)
    # Reach every pairwise frontier check with exactly three existing children
    # and no table, rather than failing an earlier layout/coverage condition.
    frontier = 'context\nroot 1 c\nterm 2 1 p A\nchild 6 1 0\nterm 3 1 q B\nterm 4 1 ll A\n'
    for left in range(4):
        for right in range(left + 1, 4):
            ids, rest = [0] * 4, iter((3, 4))
            for index in range(4):
                ids[index] = 2 if index == left else 6 if index == right else next(rest)
            check(frontier, 'frontier 1 ' + ' '.join(map(str, ids)), False)

    def module(body):
        return 'context module m {yang-version 1.1;namespace urn:m;prefix m;' + body + '}\n'

    def node(kind):
        if kind.startswith('list'):
            count = int(kind[-1])
            keys = 'abc'[:count]
            statement = 'list p {' + ('key "' + ' '.join(keys) + '";' if count else 'config false;')
            statement += ''.join('leaf ' + k + ' {type string;}' for k in keys) + '}'
            return statement, [{k: k.upper() for k in keys}]
        if kind in ('leaf', 'leaf-list'):
            return kind + ' p {type string;}', ('A' if kind == 'leaf' else ['A'])
        return kind + ' p' + (' {presence yes;}' if kind == 'container' else ';'), {}

    # Missing source paths, misleading original paths, and a deeper actual
    # destination. No WITH_PARENTS: native resolves p directly under that parent.
    for source_kind in KINDS:
        statement, value = node(source_kind)
        source = module('container c {' + statement + '}')
        source += 'parse 1 ' + json.dumps({'m:c': {'p': value}}, separators=(',', ':')) + '\nchild 2 1 0\n'
        for destination_kind in (*KINDS, 'missing'):
            target = '' if destination_kind == 'missing' else node(destination_kind)[0]
            for ancestry in ('absent', 'shadow', 'nested'):
                body = 'container d {' + ('container w {' + target + '}' if ancestry == 'nested' else target) + '}'
                if ancestry == 'shadow':
                    # An incompatible original path must neither admit an unsafe
                    # destination nor reject the compatible actual destination.
                    shadow = 'container p;' if source_kind != 'container' else 'leaf p {type string;}'
                    body += 'container c {' + shadow + '}'
                setup = source + module(body) + 'root 3 d\n'
                parent = 3
                if ancestry == 'nested':
                    setup = source + module(body) + 'parse 3 {"m:d":{"w":{}}}\nchild 4 3 0\n'
                    parent = 4
                check(setup, f'dup-to-context 90 2 {parent} 1', source_kind == destination_kind)
            # WITH_PARENTS stops at the matching supplied c ancestor; the copied
            # child still needs a complete, compatible mapping under that c.
            setup = source + module('container c {' + target + '}') + 'root 3 c\n'
            check(setup, 'dup-to-context 90 2 3 5', source_kind == destination_kind)

    # Same-context relocation never remaps schemas. Cover both public move
    # entry points, including opaque anchors which bypass native sibling-schema
    # comparison. Only the actual c destination has the correct data ancestry.
    for kind in ('leaf', 'container', 'list0', 'leaf-list', 'missing'):
        target = '' if kind == 'missing' else node(kind)[0]
        setup = module('container c {leaf-list p {type string;ordered-by user;}}container d {' + target + '}')
        setup += 'parse 1 {"m:c":{"p":["A"]}}\nchild 2 1 0\nroot 3 d\nopaque 4 3 u A\n'
        for command in ('attach 3 2', 'before 4 2', 'dup 90 2 3 1', 'dup 90 2 3 5'):
            check(setup, command, False)
        check(setup, 'attach 1 2', True)

    for file in sorted((ROOT / 'cases' / 'admission').glob('*.calls')):
        script = 'context\n' + file.read_text()
        lines = script.rstrip().split('\n')
        check('\n'.join(lines[:-1]) + '\n', lines[-1], False)
    return {'aliased_commands': len(aliases), 'output_alias_commands': len(outputs), 'historical_alias_commands': len(historical), 'source_kinds': len(KINDS), 'destination_kinds': len(KINDS) + 1,
            'ancestries': 3, 'valid_pairs': valid, 'rejected_pairs': refused, 'driver_runs': runs, 'skips': 0}


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
    preconditions = precondition_sweep(driver, plain)
    summary = {'admission': admission_sweep(driver, plain), 'preconditions': preconditions, 'custom_schema_sweep': True, 'names': len(NAMES), 'kinds': len(KINDS),
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
