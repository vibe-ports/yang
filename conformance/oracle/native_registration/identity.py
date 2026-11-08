# SPDX-License-Identifier: BSD-3-Clause
"""Structural evidence shared by every native replay consumer.

References are process-local generations. Cross-process keys are assigned once,
from completed-operation components, never from a mutable allocation label.
"""
import json
import sys
from collections import Counter


def requests(script):
    return [line.split() for line in script.splitlines() if line.strip() and not line.lstrip().startswith('#')]


def table_records(row):
    for entry in row.get('tables', []):
        if 'storage' in entry:
            yield 'event', entry['storage']
    for key in ('table', 'before', 'after'):
        if key in row:
            yield ('event' if key == 'table' else 'read'), row[key]
    for node in row.get('nodes', []):
        if 'table' in node:
            yield ('node', node['generation']), node['table']


def validate(script, rows, refs=None):
    req = requests(script)
    read_context_evidence = any(f[0].startswith('read-') or f[0] in ('equal', 'context', 'context-keyless') for f in req)
    recorded = {n['generation']: n for r in rows for n in r.get('nodes', [])}
    assert rows and rows[0]['event'] == 'driver.ready.actual-children-callback', 'missing bootstrap evidence'
    previous, provenance, labels, retired, table_owners, serials = {}, {}, {}, set(), {}, {}
    previous_tables, bindings, keys, indexes, creations = {}, {}, {}, Counter(), {}
    normalized, last_nodes, completed_nodes, completed_bindings = {}, {}, {}, {}
    schemas, schema_keys, schema_nodes = {}, {}, {}
    for row in rows:
        event, operation = row['event'], row.get('operation', 0)
        unsafe = event == 'boundary.retained-dangling-links'
        if unsafe:
            assert row is rows[-1] and req[operation - 1][0] in ('free', 'free-root'), 'invalid unsafe boundary'
        command = 0 < operation <= len(req) and event == req[operation - 1][0]
        # Snapshot obligations come from the protocol/request, never field presence.
        if command or event == 'driver.ready.actual-children-callback':
            assert isinstance(row.get('nodes'), list), 'missing required snapshot nodes'
        if 'nodes' not in row:
            if event in ('read.null', 'read.schema'):
                fields = req[operation - 1]
                assert fields[0] == ('schema' if event == 'read.schema' else 'null-reads'), 'read at wrong operation'
                for field, index in (('queried_parent', 1), ('target', 2)):
                    g = completed_bindings[int(fields[index])]['generation']
                    assert row[field] == g and g in keys and last_nodes[g]['alive'], 'wrong requested ' + field
            if event in ('read.instance', 'read.opaque'):
                # These are finite native dispatch controls, not a G2 general
                # comparison model. Their exact winners/order are pinned by the
                # sweep independently on each side before control comparison.
                supplied, winner, parent = row['supplied'], row['winner'], row['current_parent']
                fields = req[operation - 1]
                assert fields[0] == ('instance' if event == 'read.instance' else 'opaq-find'), 'read at wrong operation'
                handle = int(fields[1])
                expected_start = completed_bindings[handle]['generation'] if handle else 0
                assert supplied == expected_start, 'read skipped/replaced requested start'
                if event == 'read.instance':
                    target = completed_bindings[int(fields[2])]['generation']
                    assert row['target'] == target and target in keys and last_nodes[target]['alive'], 'wrong requested target'
                else:
                    assert row['name'] == fields[2], 'wrong opaque name'
                for g in (supplied, winner, parent):
                    assert not g or (g in keys and last_nodes[g]['alive']), 'invalid read identity'
                if not supplied:
                    assert (winner, row['rc'], parent) == (0, 5, 0), 'null sibling must return ENOTFOUND'
                assert row['rc'] == (0 if winner else 5), 'wrong read status'
                assert not winner or last_nodes[winner]['parent'] == parent, 'wrong current parent'
                assert row['before'] == row['after'], 'lookup mutated storage'
                owner = last_nodes[supplied]['parent'] if supplied else 0
                expected_table = last_nodes[owner].get('table', {'present': False}) if owner else {'present': False}
                assert row['before'] == expected_table, 'wrong queried storage'
                normalized[operation, event] = (operation, row['rc'], keys.get(supplied, 0), keys.get(winner, 0), keys.get(parent, 0),
                                                keys[target] if event == 'read.instance' else row['name'])
            if event == 'read.compare':
                assert 0 < operation <= len(req) and req[operation - 1][0] == 'instance', 'comparison at wrong operation'
                assert all(g in keys and last_nodes[g]['alive'] for g in (row['left'], row['right'])), 'invalid comparison operands'
            if event == 'read.null':
                parent = row['queried_parent']
                assert last_nodes[parent]['alive'] and last_nodes[parent]['first'] == 0, 'non-null supplied sibling'
                assert row['before'] == row['after'] == last_nodes[parent]['table'], 'null read changed retained history'
                for kind in ('schema', 'instance', 'value'):
                    assert (row[kind + '_winner'], row[kind + '_rc']) == (0, 5), 'null sibling must return ENOTFOUND'
                normalized[operation, event] = (operation, keys[parent], 0, 5)
            if event == 'allocation.address-reuse':
                old, new = row['old_generation'], row['new_generation']
                assert type(old) is int and type(new) is int and 0 < old < new and old in recorded and new in recorded and not recorded[old]['alive'], 'invalid reuse identities'
            if event == 'boundary.tombstoned-handle':
                g = row['generation']
                assert g in last_nodes and not last_nodes[g]['alive'] and bindings[row['handle']]['generation'] == g, 'invalid tombstone probe'
            if event == 'read.equal':
                fields = req[operation - 1]
                assert fields[0] == 'equal' and row['options'] == int(fields[3]), 'wrong comparison request'
                operands = []
                for field, handle in zip(('left_generation', 'right_generation'), map(int, fields[1:3]), strict=True):
                    g = row[field]
                    assert type(g) is int and g == (bindings[handle]['generation'] if handle else 0), 'wrong comparison operand binding'
                    assert not g or (g in last_nodes and last_nodes[g]['alive'] and g in keys), 'invalid comparison operand'
                    operands.append(keys[g] if g else 0)
                normalized[operation, event] = (operation, row['rc'], *operands, row['options'])
            if event in ('read.schema', 'read.first-sibling'):

                def live(g):
                    assert g == 0 or (g in last_nodes and last_nodes[g]['alive'] and g in keys), ('invalid read identity', g)
                    return keys[g] if g else 0
                winner = row['winner']
                live(winner)
                if event == 'read.schema':
                    parent = row['queried_parent']
                    live(parent); live(row['current_parent'])
                    assert not winner or last_nodes[winner]['parent'] == row['current_parent'], 'wrong current parent'
                    # §2.1 preflight precedes table selection and target
                    # preparation. Storage evidence is still checked below.
                    first = last_nodes[parent]['first']
                    if not first:
                        assert (winner, row['rc'], row['current_parent']) == (0, 5, 0), 'null sibling must return ENOTFOUND'
                    # Independently resolve the schema lookup through the recorded
                    # table run, or physical siblings when no table exists.
                    assert row['before'] == row['after'], 'lookup mutated storage'
                    table = row['before']
                    assert table == last_nodes[parent]['table'], 'wrong queried storage'
                    for _, t in table_records(row):
                        if not t['present']: continue
                        assert t['identity'] > 0 and t['identity'] in previous_tables and previous_tables[t['identity']]['alive'], 'invalid read table identity'
                        assert len(t['chains']) == t['used'], 'incomplete read occurrences'
                        for x in t['chains']:
                            assert x['generation'] in last_nodes and x['generation'] > 0 and x['occurrence'] > 0, 'invalid read occurrence'
                            assert x['alive'] == last_nodes[x['generation']]['alive'], 'invalid read liveness'
                            assert serials.get(x['occurrence']) == (t['identity'], x['generation'], x['hash']), 'unknown read occurrence'

                    if not first:
                        normalized[operation, event] = (operation, row['rc'], live(parent), 0, 0)
                        continue
                    target = bindings[int(req[operation - 1][2])]['generation']
                    schema = last_nodes[target]['schema_identity']
                    def matches(g):
                        n = last_nodes[g]
                        return n.get('schema_identity') == schema
                    if table['present']:
                        candidates = [x['generation'] for x in table['chains'] if x['hash'] == row['schema_hash'] and matches(x['generation'])]
                        assert not winner or winner in candidates, 'winner outside queried schema run'
                    else:
                        candidates, g = [], last_nodes[parent]['first']
                        while g and last_nodes[g].get('schema'):
                            if matches(g): candidates.append(g)
                            g = last_nodes[g]['next']
                    assert winner == (candidates[0] if candidates else 0), 'wrong schema winner'
                    assert row['rc'] == (0 if winner else 5), 'wrong schema status'
                    normalized[operation, event] = (operation, row['rc'], live(parent), live(winner), live(row['current_parent']))
                else:
                    assert req[operation - 1][0] == 'rewind', 'rewind at wrong operation'
                    h = int(req[operation - 1][1]); g = bindings[h]['generation']
                    if last_nodes[g]['parent']:
                        expected = last_nodes[last_nodes[g]['parent']]['first']
                    else:
                        expected, seen = g, set()
                        while last_nodes[expected]['prev'] and last_nodes[last_nodes[expected]['prev']]['next']:
                            assert expected not in seen, 'sibling cycle'
                            seen.add(expected); expected = last_nodes[expected]['prev']
                    assert winner == expected, 'wrong first sibling'
                    normalized[operation, event] = (operation, live(winner))
            continue
        assert 'diagnostics' in row and isinstance(row['diagnostics'], list), 'missing diagnostics'
        for diagnostic in row['diagnostics']:
            assert {'code', 'context_generation'} <= diagnostic.keys(), 'missing diagnostic context identity'
            assert type(diagnostic['code']) is int and type(diagnostic['context_generation']) is int and 0 < diagnostic['context_generation'] < 16, 'invalid diagnostic identity'
        if read_context_evidence: assert 'modules' in row, 'missing read context evidence'
        assert row['allocation_scope'] in ('every-allocation', 'survivors-and-observed-frees')
        assert type(row['node_count']) is int and 0 < row['node_count'] < 65536, 'invalid allocation count'
        assert type(row['table_count']) is int and 0 <= row['table_count'] < 65536, 'invalid table count'
        nodes = {n['generation']: n for n in row['nodes']}
        registry_schemas = {s['identity']: s for s in row['schemas']}
        assert len(registry_schemas) == len(row['schemas']), 'collapsed schema registry'
        assert set(registry_schemas) == set(range(1, len(registry_schemas) + 1)), 'invalid schema registry'
        assert schemas.keys() <= registry_schemas.keys(), 'lost schema registry'
        current_schema_keys, unique_schemas = {}, set()
        for s, record in sorted(registry_schemas.items()):
            context = record['context']
            assert type(s) is int and s > 0 and type(context) is int and 0 < context < 16, 'invalid schema identity/context'
            assert context == 1 or read_context_evidence, 'invalid schema identity/context'
            assert record['module'] and record['name'] and record['nodetype'] > 0, 'incomplete schema descriptor'
            parent = record['parent']
            assert type(parent) is int and 0 <= parent < s, 'invalid compiled schema ancestry'
            key = (record['context'], record['module'], record['name'], record['nodetype'], current_schema_keys.get(parent))
            assert key not in unique_schemas, 'split compiled schema identity'
            unique_schemas.add(key)
            assert s not in schemas or schemas[s] == record, 'schema registry drift'
            current_schema_keys[s] = key
        schemas, schema_keys = registry_schemas, current_schema_keys
        for field in ('node_generation', 'source_generation', 'working_rb_root', 'output_generation'):
            ref = row.get(field, 0)
            assert type(ref) is int and (not ref or ref in nodes), ('invalid event reference', field)
        assert len(nodes) == len(row['nodes']) == row['node_count'] and previous.keys() <= nodes.keys(), 'missing/duplicate allocation evidence'
        assert set(nodes) == set(range(1, row['node_count'] + 1)), 'generation gap'
        bootstrap = [n for n in nodes.values() if n['creation_operation'] == 0]
        assert len(bootstrap) == 5 and all(n['alive'] for n in bootstrap), 'missing bootstrap identities'
        seed = [n for n in bootstrap if n.get('schema') == 'c']
        assert len(seed) == 1, 'missing bootstrap root'
        seed_table = seed[0]['table']
        assert seed_table['present'] and seed_table['identity'] == 1 and seed_table['used'] == 5 and seed_table['size'] == 8, 'missing bootstrap table evidence'
        for g, n in nodes.items():
            assert type(g) is int and g > 0 and n['creation_ordinal'] > 0 and 0 <= n['creation_operation'] <= operation, 'invalid allocation provenance'
            creation = n['creation_operation'], n['creation_ordinal']
            assert creations.setdefault(creation, g) == g, 'ambiguous allocation provenance'
            origin = (*creation, n['source'], n['attempt'])
            assert provenance.setdefault(g, origin) == origin, 'mutable provenance'
            if n['source']: assert n['source'] in nodes and n['source'] < g, 'invalid source'
            if g in labels and labels[g]: assert labels[g] == n['id'], 'overwritten allocation label'
            if n['id']:
                assert n['creation_operation'] > 0 and n['attempt'] == n['id'], 'label not allocation provenance'
                if not labels.get(g):
                    assert n['creation_operation'] == operation, 'late alias changed provenance'
                    assert any(b['handle'] == n['id'] and b['generation'] == g for b in row['bindings']), 'incidental allocation label'
            labels[g] = n['id']
            assert g not in retired or not n['alive'], 'revived tombstone'
            if not n['alive']: retired.add(g)
            if n['alive']:
                if unsafe and n['creation_operation']:
                    assert n['current'] == 'unsafe-native-links', 'missing unsafe availability'
                    continue
                assert n.get('current') != 'unsafe-native-links', 'unsafe current state outside boundary'
                assert {'context', 'hash', 'flags', 'parent', 'prev', 'next', 'metadata'} <= n.keys(), 'missing live node state'
                if read_context_evidence or 'modules' in row:
                    assert 'context_generation' in n and type(n['context_generation']) is int and 0 < n['context_generation'] < 16, 'missing read context identity'
                if n.get('schema'):
                    assert {'nodetype', 'key'} <= n.keys(), 'missing native node kind'
                    if n['nodetype'] & 0x000c: assert 'value' in n, 'missing terminal value'
                else:
                    assert {'first', 'tail', 'prefix_format', 'prefix_bound'} <= n.keys(), 'missing opaque state'
                if n.get('schema'):
                    s = n['schema_identity']
                    assert type(s) is int and s > 0 and s in schemas, 'missing schema identity'
                    descriptor = schemas[s]
                    assert (n['schema'], n['nodetype'], n.get('context_generation', n['context'])) == (descriptor['name'], descriptor['nodetype'], descriptor['context']), 'node/schema descriptor mismatch'
                    assert 'module' not in n or n['module'] == descriptor['module'], 'node/schema module mismatch'
                    assert schema_nodes.setdefault(g, s) == s, 'node/schema identity drift'
                # libyang v5.8.6 tree_data.h:LYD_NODE_INNER / tree_schema.h.
                if n.get('nodetype', 0) & 0x0711 or n.get('schema') in ('c', 'd', 'r', 'l', 'kl'):
                    assert {'first', 'tail', 'table'} <= n.keys(), 'missing inner-node table evidence'
                for field in ('parent', 'prev', 'next', 'first', 'tail', 'rb_root', 'rb_stored_root'):
                    ref = n.get(field, 0)
                    assert ref == 0 or ref in nodes, ('missing link identity', field, ref)
                    if command: assert ref == 0 or nodes[ref]['alive'], ('live link to tombstone', field, ref)
                for member in n.get('rb', []):
                    for field in ('owner', 'node', 'parent', 'left', 'right'):
                        ref = member[field]
                        assert not ref or ref in nodes, 'missing RB identity'
                        if command: assert not ref or nodes[ref]['alive'], 'RB tombstone'
        live_labels = [n['id'] for n in nodes.values() if n['alive'] and n['id']]
        assert len(set(live_labels)) == len(live_labels), 'ambiguous allocation labels'
        registry = {t['identity']: t for t in row['tables']}
        assert len(registry) == len(row['tables']) == row['table_count'], 'missing table registry'
        assert set(registry) == set(range(1, row['table_count'] + 1)), 'invalid/collapsed table identity'
        if unsafe:
            for t, entry in registry.items():
                if entry['alive']:
                    assert entry['storage']['present'] and entry['storage']['identity'] == t, 'missing retained table evidence'
        for t, old in previous_tables.items():
            assert t in registry and (old['alive'] or not registry[t]['alive']), 'revived/lost table'
        for owner, table in table_records(row):
            if not table['present']: continue
            t = table['identity']
            assert type(t) is int and t > 0 and t in registry, 'invalid table identity'
            if table.get('retired'):
                assert not registry[t]['alive'] and not command, 'retired table referenced as live storage'
                continue
            assert registry[t]['alive'], 'live storage with retired identity'
            if owner == 'event' and command: owner = 'generic'
            if isinstance(owner, tuple) or owner == 'generic':
                assert table_owners.setdefault(t, owner) == owner, 'table identity aliases owners'
            assert table['continuity'] in ('observed', 'observed-generic', 'unknown'), 'missing continuity assurance'
            if row['allocation_scope'] == 'survivors-and-observed-frees' and isinstance(owner, tuple):
                assert table['continuity'] == 'unknown', 'unproved occurrence continuity'
            chains = table['chains']
            assert len(chains) == table['used'] and len(table['slots']) == len(table['buckets']) == table['size'], 'incomplete table'
            assert len({x['occurrence'] for x in chains}) == len(chains), 'duplicate occurrence identity'
            for x in chains:
                g, serial = x['generation'], x['occurrence']
                assert type(g) is int and type(serial) is int and g > 0 and g in nodes and serial > 0, 'missing occurrence identity'
                assert x['alive'] == nodes[g]['alive'] and x['node'] == nodes[g]['id'], 'occurrence target mismatch'
                assert 0 <= x['slot'] < table['size'], 'invalid occurrence slot'
                assert serials.setdefault(serial, (t, g, x['hash'])) == (t, g, x['hash']), 'occurrence changed ownership'
        bs = {b['handle']: b for b in row['bindings']}
        assert len(bs) == len(row['bindings']), 'ambiguous handle'
        for h, b in bs.items():
            assert 0 < h < 4096 and b['revision'] > 0 and b['operation'] <= operation, 'invalid binding'
            assert b['generation'] == 0 or b['generation'] in nodes, 'missing binding allocation'
            if h not in bindings:
                assert b['revision'] == 1 and b['operation'] == operation, 'missing binding history'
            if h in bindings:
                old = bindings[h]
                if b['generation'] == old['generation']:
                    assert b == old, 'unnecessary binding revision'
                else:
                    assert b['revision'] == old['revision'] + 1 and b['operation'] == operation, 'unstable binding'
        assert bindings.keys() <= bs.keys(), 'lost retained binding'
        if command:
            fields = req[operation - 1]
            unary = ('unlink', 'detach-siblings', 'change', 'I', 'U', 'free', 'free-root', 'rewind', 'validate', 'check-handle', 'print', 'find-next-loop', 'opaq-find')
            inputs = [int(fields[1])] if event in unary else [int(fields[1]), int(fields[2])] if event in ('attach', 'before', 'schema', 'equal', 'null-reads', 'instance') else [int(fields[2]), int(fields[3])] if event in ('dup', 'dup-to-context') else [int(fields[2])] if event in ('term', 'list', 'opaque', 'opaque-invalid-xml', 'child') else []
            if event.startswith('read-'):
                inputs = [int(fields[1]), int(fields[2]), int(fields[3]) if len(fields) > 3 else 0]
            elif event in ('record', 'record-as'):
                inputs = [int(fields[1]), int(fields[2])]
                if event == 'record-as': inputs.append(int(fields[3]))
            for h in inputs:
                if h:
                    assert h in completed_bindings, 'missing input binding'
                    g = completed_bindings[h]['generation']
                    assert g and g in completed_nodes and completed_nodes[g]['alive'], 'invalid input identity'
            outputs = ('root', 'term', 'list', 'opaque', 'opaque-invalid-xml', 'dup', 'dup-to-context', 'parse', 'parse-xml', 'child', 'rewind', 'validate')
            changed = int(fields[1]) if event in outputs else 0
            lazy = int(fields[1]) if event in ('insert', 'remove') else 0
            for h, old in completed_bindings.items():
                if h not in (changed, lazy): assert bs[h] == old, 'unrequested binding refresh'

            if event in outputs:
                h = int(fields[1])
                assert h in bs, 'missing output binding'
                assert row['output_handle'] == h and row['output_generation'] == bs[h]['generation'], 'binding differs from returned pointer'
                if row['rc'] == 0 and event != 'validate':
                    g = bs[h]['generation']
                    assert g and nodes[g]['alive'], 'invalid returned binding'
                if event in ('root', 'term', 'list', 'opaque', 'opaque-invalid-xml', 'parse', 'parse-xml') and row['rc'] == 0:
                    assert nodes[bs[h]['generation']]['creation_operation'] == operation, 'returned wrong incarnation'
                if event == 'rewind':
                    assert keys[bs[h]['generation']] == normalized[operation, 'read.first-sibling'][1], 'rewind binding not refreshed'
        # Assign injective cross-process keys in physical component order, once
        # per generation. Handles select traversal anchors; aliases do not label
        # allocations, and a reused handle gets a new allocation incarnation.
        if command or operation == 0:
            visited = set()
            def walk(g):
                if not g or g in visited or not nodes[g]['alive']: return
                visited.add(g)
                if g not in keys:
                    origin = nodes[g]['creation_operation']; indexes[origin] += 1
                    keys[g] = (origin, indexes[origin])
                walk(nodes[g].get('first', 0)); walk(nodes[g]['next'])
            def component(g):
                seen = set()
                while nodes[g]['parent']:
                    assert g not in seen, 'parent cycle'
                    seen.add(g); g = nodes[g]['parent']
                seen = set()
                while nodes[g]['prev'] and nodes[nodes[g]['prev']]['next']:
                    assert g not in seen, 'sibling cycle'
                    seen.add(g); g = nodes[g]['prev']
                walk(g)
            for h, b in sorted(bs.items()):
                if b['generation'] and nodes[b['generation']]['alive']: component(b['generation'])
            for g, n in nodes.items():
                if n['alive']: component(g)
            assert len(set(keys.values())) == len(keys), 'ambiguous normalization'
            def ref(g):
                assert not g or g in keys, 'incomplete normalization'
                return keys[g] if g else 0
            def storage(table):
                if not table or not table['present']: return None
                return (table['size'], table['used'], table['mode'], table['free'], table['buckets'], table['slots'],
                        [(x['slot'], x['hash'], ref(x['generation']), x['alive']) for x in table['chains']],
                        table['free_chain'], table['free_terminal'])
            live = {}
            for g, n in nodes.items():
                if n['alive']:
                    live[ref(g)] = (schema_keys.get(n.get('schema_identity')), n.get('module'), n.get('schema'), n.get('value'), n.get('flags'),
                                    tuple(ref(n.get(f, 0)) for f in ('parent', 'prev', 'next', 'first', 'tail')),
                                    n.get('context_generation', n.get('context')), n.get('nodetype'), n.get('key'),
                                    n.get('prefix_format'), n.get('prefix_bound'), storage(n.get('table')), n['hash'])
            if refs is not None: refs[operation] = dict(keys)
            normalized[operation, 'identity.state'] = (live, {h: (ref(b['generation']), b['revision']) for h, b in bs.items()})
        if command or operation == 0: completed_nodes, completed_bindings = nodes, bs
        previous, previous_tables, bindings, last_nodes = nodes, registry, bs, nodes
    return normalized


if __name__ == '__main__':
    payload = json.load(sys.stdin)
    refs = {} if payload.get('project') else None
    result = validate(payload['script'], payload['rows'], refs)
    if refs is not None:
        def canonical(value):
            if isinstance(value, dict):
                return [[canonical(k), canonical(v)] for k, v in sorted(value.items(), key=lambda x: repr(x[0]))]
            if isinstance(value, (tuple, list)): return [canonical(x) for x in value]
            return value
        json.dump({'states': [[op, event, canonical(value)] for (op, event), value in result.items()],
                   'refs': refs}, sys.stdout)
