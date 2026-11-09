# SPDX-License-Identifier: BSD-3-Clause
"""Live field-by-field contract controls, invoked by TestNativeG2EveryEmittedField."""
import copy
import json
import os
import pathlib
import subprocess
import signal
import sys

from identity import request_evidence, requests, validate
from protocol import RESULTS, shape

ROOT = pathlib.Path(__file__).resolve().parent
CURRENT = None

# Independent request inventory: zero is not a native input for these positions.
MANDATORY_INPUTS = {k: (1,) for k in ('unlink', 'detach-siblings', 'change', 'I', 'U', 'free',
    'free-root', 'rewind', 'validate', 'check-handle', 'print', 'find-next-loop', 'insert', 'remove')}
MANDATORY_INPUTS |= {k: (1, 2) for k in ('attach', 'before', 'schema', 'null-reads', 'record')}
MANDATORY_INPUTS |= {'record-as': (1, 2, 3), 'frontier': (1, 2, 3, 4, 5), 'child': (2,),
                     'dup': (2,), 'dup-to-context': (2,), 'instance': (2,)}
MANDATORY_INPUTS |= {k: (2,) for k in ('read-schema', 'read-instance', 'read-value', 'read-opaque')}


def timed_out(signum, frame):
    raise TimeoutError(('evidence control exceeded budget', CURRENT))


signal.signal(signal.SIGALRM, timed_out)


def replay(script, mode='--quiet', read=True, plain=False):
    result = subprocess.run([os.environ['YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER' if plain else 'YANG_NATIVE_REGISTRATION_DRIVER'], mode,
                             '--read-schema' if read else '--event-schema'], input=script,
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=30, check=False, env=dict(os.environ, GLIBC_TUNABLES='glibc.malloc.tcache_count=7'))
    assert result.returncode == 0, result.stderr
    return [json.loads(line) for line in result.stdout.splitlines()]


def paths(value, prefix=()):
    if isinstance(value, dict):
        for k, v in value.items():
            yield prefix + (k,), v
            yield from paths(v, prefix + (k,))
    elif isinstance(value, list):
        for i, v in enumerate(value):
            yield prefix + (i,), v
            yield from paths(v, prefix + (i,))


def parent_at(value, path):
    for k in path[:-1]: value = value[k]
    return value, path[-1]


def rejected_or_changed(script, rows, expected):
    signal.setitimer(signal.ITIMER_REAL, 3)
    try:
        actual = validate(script, rows)
    except (AssertionError, KeyError, IndexError, TypeError, ValueError):
        return True
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
    return actual != expected


def invalid_contract(script, rows, project):
    try:
        validate(script, rows, project=project)
    except (AssertionError, KeyError, IndexError, TypeError, ValueError):
        return True
    return False


def request_substitutions(script, left, right):
    """Every executed line/argument, both peers, validation only (no projection)."""
    fields = requests(script)
    executed = {r['operation'] for r in left} | {r['operation'] for r in right}
    count = 0
    for index, request in enumerate(fields, 1):
        if index not in executed: continue  # Explicit native terminal prefix.
        for argument, value in enumerate(request):
            replacement = str(int(value) + 1) if value.isascii() and value.isdecimal() else value + '-substituted'
            replacements = [replacement]
            if value.isascii() and value.isdecimal() and int(value): replacements.append('0')
            for replacement in replacements:
                changed = [r[:] for r in fields]
                changed[index - 1][argument] = replacement
                substituted = '\n'.join(' '.join(r) for r in changed) + '\n'
                for peer in (left, right):
                    assert invalid_contract(substituted, peer, False), ('accepted substituted request', index, argument, request)
                count += 1
        mandatory = MANDATORY_INPUTS.get(request[0], ())
        if request[0] in ('free', 'free-root') and len(request) > 2: mandatory += (2,)
        for argument in mandatory:
            if argument >= len(request): continue
            changed = [r[:] for r in fields]
            changed[index - 1][argument] = '0'
            substituted = '\n'.join(' '.join(r) for r in changed) + '\n'
            for peer in (left, right):
                forged = list(peer)
                for row_index, original in enumerate(peer):
                    if original['operation'] != index: continue
                    row = dict(original)
                    forged[row_index] = row
                    row['request'] = request_evidence(changed[index - 1])
                    if row['event'].startswith('read.selection.') and argument == 2:
                        row['target_generation'] = 0
                assert invalid_contract(substituted, forged, False), ('accepted mandatory NULL request', index, argument, request)
            count += 1
    return count


def excluded(row, path, script):
    """Exact field exclusions documented in README's inventory; never semantic wildcards."""
    event = row['event']
    req = requests(script)
    op = row.get('operation', 0)
    complete = event == 'driver.ready.actual-children-callback' or 0 < op <= len(req) and event == req[op - 1][0]
    if not complete and 'nodes' in row:
        return 'instrumented intermediate snapshot'
    if event in ('read.compare', 'allocation.address-reuse'):
        return 'instrumented callback/reuse notice'
    if path == ('allocation_scope',): return 'coverage claim differs by build'
    if path[0] in ('node_count', 'table_count', 'tables'): return 'observer lifetime registry'
    if path[0] == 'nodes' and len(path) > 1:
        n = row['nodes'][path[1]]
        if not n['alive'] and not n['id']: return 'observer temporary retirement history'
    if path[0] == 'nodes' and len(path) >= 3:
        if path[2] in ('creation_ordinal', 'source', 'attempt'): return 'observer allocation provenance'
    if path[-1] in ('identity', 'occurrence', 'continuity'):
        return 'validated local serial or continuity assurance'
    return None


def contract_defect(row, path, action):
    """Independent outcome inventory; do not ask the mutated schema what is required."""
    if action == 'add': return True
    if action == 'delete' and type(path[-1]) is str:
        if path[0] in ('diagnostics', 'logs') and len(path) == 3:
            return path[-1] in ('code', 'context_generation', 'level', 'line')
        if path[0] == 'modules' and len(path) == 3:
            return path[-1] not in ('namespace', 'revision')
        if path == ('json',): return row['rc'] == 0
        if path in (('table',), ('working_rb_root',), ('modules',)): return False
        if path[0] == 'tables' and path[-1] == 'storage': return False
        return True
    if action == 'mutate':
        if path[0] == 'request' or path in (('event',), ('operation',)): return True
        if row['event'] in RESULTS and row['event'] != 'read.compare' and len(path) == 1:
            # The native result's data/hash are observations. Operands, options,
            # status and derived links are bound to request/history/outcome.
            return path[0] not in ('json', 'schema_hash')
    return False


def perturb(rows, index, path, action):
    mutant = copy.deepcopy(rows)
    parent, field = parent_at(mutant[index], path)
    value = parent[field]
    if action == 'typed': parent[field] = {'invalid_type': True} if type(value) is not dict else 'invalid_type'
    elif action == 'delete': del parent[field]
    elif action == 'add':
        if type(value) is dict: parent[field]['unexpected_field'] = 1
        elif type(value) is list: parent[field].append({'unexpected_field': 1})
        else: return None
    elif type(value) is bool: parent[field] = not value
    elif type(value) is int: parent[field] = value + 1
    elif type(value) is str: parent[field] = value + '-changed'
    elif value is None: parent[field] = 'changed'
    elif type(value) is list: parent[field] = value[1:] if value else ['changed']
    else: return None
    return mutant


def counterpart(rows, row, path):
    matches = [i for i, r in enumerate(rows) if (r['event'], r['operation']) == (row['event'], row['operation'])]
    if not matches: return None
    index = matches[0]
    if path[0] == 'nodes' and len(path) > 1:
        node = row['nodes'][path[1]]
        fields = ('id', 'creation_operation', 'alive', 'schema', 'value')
        matches = [i for i, n in enumerate(rows[index]['nodes']) if all(n.get(k) == node.get(k) for k in fields)]
        if not matches: return None
        path = ('nodes', matches[0], *path[2:])
    try:
        parent, field = parent_at(rows[index], path)
        parent[field]
    except (KeyError, IndexError, TypeError): return None
    return index, path


def sweep(script, rows, peer=None):
    global CURRENT
    expected = validate(script, rows)
    seen, counts = set(), dict(delete=0, add=0, mutate=0, typed=0, exclusions=0, both_rejected=0, native_outcome=0)
    outcomes = set()
    for index, row in enumerate(rows):
        for path, value in paths(row):
            # Exercise every field shape, avoiding repetition across homogeneous
            # records. Event and node representation stay distinct in the key.
            representation = ''
            if path[0] == 'nodes' and len(path) > 1:
                n = row['nodes'][path[1]]
                representation = ('dead' if not n['alive'] else n.get('schema', 'opaque'))
            key = row['event'], representation, tuple('*' if type(k) is int else k for k in path), type(value)
            if key in seen: continue
            seen.add(key)
            reason = excluded(row, path, script)
            CURRENT = key
            # Wrong types are always rejected, even in the excluded evidence lane.
            other = counterpart(peer, row, path) if peer is not None else (index, path)
            assert other is not None or reason, ('missing native counterpart', key)
            peer_rows = peer if peer is not None else rows
            mutant = perturb(rows, index, path, 'typed')
            assert invalid_contract(script, mutant, True) and invalid_contract(script, mutant, False), ('unvalidated type', key)
            # Two identical peers cannot rely on projection inequality. The
            # validation-only lane must reject each malformed peer itself.
            if other is not None:
                assert invalid_contract(script, perturb(peer_rows, *other, 'typed'), False), ('accepted identical type defect', key)
                counts['both_rejected'] += 1
            else:
                counts['exclusions'] += 1
            counts['typed'] += 1
            for action in ('delete', 'add', 'mutate'):
                mutant = perturb(rows, index, path, action)
                if mutant is None: continue
                invalid = invalid_contract(script, mutant, False)
                peer_invalid = other is not None and invalid_contract(script, perturb(peer_rows, *other, action), False)
                if contract_defect(row, path, action):
                    assert invalid and (peer_invalid or other is None), ('accepted identical contract defect', action, key)
                if invalid and peer_invalid:
                    counts['both_rejected'] += 1
                else:
                    # Well-formed native outcome changes need an independent
                    # outcome witness; equality of two altered peers is never
                    # such a witness. Optional fields remain optional.
                    outcomes.add((action, key))
                    counts['native_outcome'] += 1
                changed = invalid or rejected_or_changed(script, mutant, expected)
                if not changed:
                    assert reason, ('uncompared field', action, key)
                    # Shape must still reject deletion of required excluded
                    # fields and unknown additions. Semantically valid lifetime
                    # history deletion is separately covered by the G1 sweep.
                    if action == 'add': raise AssertionError(('unvalidated addition', key))
                    counts['exclusions'] += 1
                counts[action] += 1
    print(json.dumps({'native_outcome_fields': sorted(outcomes, key=repr)}, default=str), flush=True)
    return counts


def pin_exclusions():
    script = 'root 1 c\nterm 2 1 ll a\nterm 3 1 ll b\nterm 4 1 ll c\nterm 5 1 ll d\nequal 1 1 0\n'
    rows = replay(script)
    expected = validate(script, rows)
    altered = copy.deepcopy(rows)
    # The serial is local; preserve its complete binding history when renumbering.
    for row in altered:
        for _, value in paths(row):
            if type(value) is dict and 'occurrence' in value: value['occurrence'] += 10000
    assert validate(script, altered) == expected, 'local occurrence renumbering changed semantics'
    altered = copy.deepcopy(rows)
    for row in altered:
        if 'nodes' in row: row['allocation_scope'] = 'survivors-and-observed-frees'
        for _, value in paths(row):
            if type(value) is dict and 'continuity' in value: value['continuity'] = 'unknown'
    assert validate(script, altered) == expected, 'documented assurance difference changed semantics'
    # Discovery order and unobserved temporary objects differ across builds.
    altered = copy.deepcopy(rows)
    for row in altered:
        for n in row.get('nodes', []): n['creation_ordinal'] += 100
    assert validate(script, altered) == expected, 'observer birth order leaked into semantics'
    altered = copy.deepcopy(rows)
    last = altered[-1]
    last['node_count'] += 1
    last['nodes'].append(dict(generation=last['node_count'], id=0, creation_operation=last['operation'],
                              creation_ordinal=1000, source=1, attempt=0, alive=False, current='unavailable'))
    last['table_count'] += 1
    last['tables'].append(dict(identity=last['table_count'], alive=False))
    assert validate(script, altered) == expected, 'transient observer history leaked into semantics'
    # Native intermediate callbacks differ by build; their schema remains strict.
    verbose = replay(script, '--events')
    assert validate(script, verbose) == expected, 'intermediate snapshots leaked into comparison'
    # Only the embedded C location varies; the log's separate line stays exact.
    location = copy.deepcopy(rows)
    location[0]['diagnostics'] = [dict(code=6, context_generation=1, msg='Internal error (first.c:10).')]
    location[0]['logs'] = [dict(level=0, line=0, msg='Internal error (first.c:10).')]
    changed_location = copy.deepcopy(location)
    for channel in ('diagnostics', 'logs'):
        changed_location[0][channel][0]['msg'] = 'Internal error (second.h:20).'
    assert validate(script, location) == validate(script, changed_location), 'embedded C location leaked into comparison'
    # Every excluded intermediate field still has a closed, typed contract.
    checked = set()
    for row in verbose:
        for path, value in paths(row):
            key = row['event'], tuple('*' if type(k) is int else k for k in path), type(value)
            if key in checked: continue
            checked.add(key)
            mutant = copy.deepcopy(row)
            parent, field = parent_at(mutant, path)
            parent[field] = {'invalid_type': True} if type(value) is not dict else 'invalid_type'
            try:
                shape(mutant, False, True)
            except (AssertionError, KeyError, TypeError):
                continue
            raise AssertionError(('unvalidated intermediate field', key))
    print(json.dumps({'excluded_lane_typed_fields': len(checked)}), flush=True)
    # Invalid excluded serials and coverage claims are never accepted.
    for field, value in (('allocation_scope', 'invented'),):
        altered = copy.deepcopy(rows); altered[-1][field] = value
        assert rejected_or_changed(script, altered, expected)


def validation_work():
    """Count executed validator lines on a fixed duplicate-heavy read workload.

    Run unchanged with main's validate function too. Work must scale with the
    emitted evidence, rather than repeated scans introduced by argument checks.
    """
    setup = 'root 1 c\n' + ''.join(f'term {i + 2} 1 v {i % 3}\n' for i in reversed(range(16)))
    measured = []
    sources = {validate.__code__.co_filename, shape.__code__.co_filename}
    # Nested contracts execute in protocol.py; both files retain no trace data.
    for reads in (4, 16):
        script = setup + 'print 1\n' * reads
        rows = replay(script, read=False)
        fields = sum(1 for row in rows for _ in paths(row))
        lines = 0
        def trace(frame, event, arg):
            nonlocal lines
            if event == 'line' and frame.f_code.co_filename in sources: lines += 1
            return trace
        sys.settrace(trace)
        try:
            validate(script, rows)
        finally:
            sys.settrace(None)
        assert lines < 64 * fields, ('evidence work exceeds linear field allowance', reads, lines, fields)
        measured.append((reads, fields, lines))
    assert measured[1][2] * measured[0][1] * 100 <= measured[0][2] * measured[1][1] * 105, ('read work grew superlinearly', measured)
    print(json.dumps({'validation_work': measured}), flush=True)


def main():
    total = dict(delete=0, add=0, mutate=0, typed=0, exclusions=0, both_rejected=0, native_outcome=0, request_substitutions_both=0)
    scripts = [
        ('root 1 c\nterm 2 1 p same\nterm 3 1 q same\nterm 4 1 ll a\nterm 5 1 ll b\nread-instance 2 2 1\nread-schema 2 2 1\nread-value 2 4 1 a\nread-opaque 2 2 1 u\nequal 2 3 0\n', True),
        ('opaque 1 0 u A\nopaque 2 0 u B\nequal 1 2 0\nfree 2\n', True),
        ('root 1 c\nterm 2 1 p A\n# reuse-free 2 self\nfree 2\n# reuse-next self\nterm 2 1 p B\nequal 2 2 0\n', True),
        ('root 1 c\nterm 2 1 p A\nequal 2 2 0\nfree 2\ncheck-handle 2\n', True),
        ((ROOT / 'cases/read/read-retained-diagnostics.calls').read_text(), True),
        ((ROOT / 'cases/remote-nonlast.calls').read_text(), False),
        ('root 1 c\nterm 2 1 v 1\nschema 1 2\ninstance 2 2\nopaq-find 2 u\nrewind 2\nprint 1\nunlink 2\nnull-reads 1 2\n', False),
    ]
    for script, read in scripts:
        rows = replay(script, read=read)
        plain = replay(script, read=read, plain=True)
        assert validate(script, rows) == validate(script, plain), 'positive native peers differ'
        total['request_substitutions_both'] += request_substitutions(script, rows, plain)
        counts = sweep(script, rows, plain)
        print(json.dumps({'request': script.splitlines()[0], 'controls': counts}), flush=True)
        for k, v in counts.items(): total[k] += v
    channel_script = 'equal 0 0 1\n'
    channel_rows = replay(channel_script)
    channel_rows[0]['diagnostics'] = [dict(code=7, context_generation=1, msg='sentinel', path='/m:c', apptag='sentinel')]
    channel_rows[0]['logs'] = [dict(level=0, line=3, msg='sentinel', path='/m:c', schema_path='/m:c')]
    counts = sweep(channel_script, channel_rows)
    print(json.dumps({'synthetic_optional_channels': counts}), flush=True)
    for k, v in counts.items(): total[k] += v
    pin_exclusions()
    validation_work()
    print(json.dumps({'every_emitted_field': total}, sort_keys=True))


if __name__ == '__main__': main()
