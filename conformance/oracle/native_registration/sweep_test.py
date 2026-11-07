# SPDX-License-Identifier: BSD-3-Clause
"""Live controls and trace mutants for the eight identity/lifetime findings."""
import argparse
import copy
import json
import os
import pathlib
import subprocess
import sys
import unittest
from collections import Counter

import check
from identity import validate, requests

ROOT = pathlib.Path(__file__).resolve().parent
DRIVER = None
COMPARISON_DRIVER = None


def execute(script, plain=False, verbose=False, terminal=None, event_schema=True):
    path = COMPARISON_DRIVER if plain else DRIVER
    result = subprocess.run([str(path), '--events' if verbose else '--quiet', *(['--event-schema'] if event_schema else [])],
                            input=script, text=True, capture_output=True, timeout=25, check=False)
    assert result.returncode == 0, (result.returncode, result.stderr, result.stdout[-1000:])
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    validate(script, rows)
    expected = [(i, f[0]) for i, f in enumerate(requests(script), 1)]
    actual = [(r['operation'], r['event']) for r in rows if r['event'] in {f[0] for f in requests(script)}]
    assert actual == (expected[:-1] if terminal else expected), ('incomplete replay', actual, expected)
    reads = {'schema': 'read.schema', 'instance': 'read.instance', 'opaq-find': 'read.opaque',
             'null-reads': 'read.null', 'rewind': 'read.first-sibling', 'print': 'public.result'}
    expected_reads = [(i, reads[c]) for i, c in (expected[:-1] if terminal else expected) if c in reads]
    actual_reads = [(r['operation'], r['event']) for r in rows if r['event'] in reads.values()]
    assert actual_reads == expected_reads, ('incomplete read results', actual_reads, expected_reads)
    if terminal: assert rows[-1]['event'] == terminal
    return rows


def output(rows):
    return [(r['operation'], r.get('rc'), r.get('json')) for r in rows if r['event'] == 'public.result']


class IdentitySweep(unittest.TestCase):
    def pair(self, script, verbose=False, terminal=None, event_schema=True):
        left, right = execute(script, verbose=verbose, terminal=terminal, event_schema=event_schema), execute(script, plain=True, verbose=verbose, terminal=terminal, event_schema=event_schema)
        self.assertEqual(validate(script, left), validate(script, right))
        self.assertEqual(output(left), output(right))
        req = requests(script)
        def results(rows):
            return [(r['operation'], r['event'], r['rc'],
                     [(d['code'], d.get('path'), d.get('apptag')) for d in r['diagnostics']])
                    for r in rows if 0 < r.get('operation', 0) <= len(req) and r['event'] == req[r['operation'] - 1][0]]
        self.assertEqual(results(left), results(right))
        return left, right

    def rejects(self, script, rows, mutate):
        mutant = copy.deepcopy(rows)
        mutate(mutant)
        with self.assertRaises((AssertionError, KeyError, IndexError)):
            validate(script, mutant)
        return mutant

    def test_retained_null_and_opaque_entries(self):
        for name, expected in (('retained-null', (0, 5)), ('retained-opaque', (2, 0))):
            script = (ROOT / 'cases' / (name + '.calls')).read_text()
            for verbose in (False, True):
                with self.subTest(case=name, verbose=verbose):
                    for rows in self.pair(script, verbose=verbose):
                        read = check.commands(rows, 'read.schema')[0]
                        snapshot = check.commands(rows, 'schema')[0]
                        nodes = check.by_handle(snapshot)
                        winner = nodes[expected[0]]['generation'] if expected[0] else 0
                        self.assertEqual((read['winner'], read['rc']), (winner, expected[1]))
                        self.assertEqual(bool(nodes[1]['first']), name == 'retained-opaque')
                        self.assertEqual(read['before'], read['after'])
                        self.assertEqual(read['before'], nodes[1]['table'])
                        self.assertEqual(read['before']['used'], 1)
                        self.assertEqual(read['before']['chains'][0]['generation'], nodes[2]['generation'])
                        def erase_history(trace):
                            r = check.commands(trace, 'read.schema')[0]
                            r['before'] = r['after'] = {'present': False}
                        self.rejects(script, rows, erase_history)
                        if name == 'retained-null':
                            null_read = check.commands(rows, 'read.null')[0]
                            for kind in ('schema', 'instance', 'value'):
                                self.assertEqual((null_read[kind + '_winner'], null_read[kind + '_rc']), (0, 5))
                                for field, value in ((kind + '_winner', nodes[2]['generation']), (kind + '_rc', 0)):
                                    self.rejects(script, rows, lambda trace: check.commands(trace, 'read.null')[0].update({field: value}))
                            # In the instrumented build this includes transient
                            # allocations: invalid binary preparation never ran.
                            self.assertFalse(any(r['event'] == 'node.allocate' and r['operation'] == null_read['operation'] for r in rows))
                            self.assertEqual(check.commands(rows, 'schema')[0]['node_count'], check.commands(rows, 'null-reads')[0]['node_count'])
                        for field, value in (('winner', nodes[2]['generation'] if not winner else 0),
                                             ('rc', 0 if expected[1] else 5)):
                            self.rejects(script, rows, lambda trace: check.commands(trace, 'read.schema')[0].update({field: value}))

    def test_entry_dispatch_controls(self):
        controls = {
            'entry-guard': [('read.instance', 0), ('read.instance', 0), ('read.schema', 3),
                            ('read.instance', 0), ('read.instance', 0), ('read.schema', 3)],
            'entry-order': [('read.instance', 4), ('read.instance', 11), ('read.instance', 4),
                            ('read.instance', 11), ('read.instance', 11), ('read.instance', 5)],
            'entry-opaque': [('read.schema', 0), ('read.instance', 4), ('read.instance', 4), ('read.opaque', 0), ('read.opaque', 7),
                             ('read.opaque', 0), ('read.opaque', 6), ('read.opaque', 0), ('read.opaque', 0)],
            'entry-duplicate': [('read.instance', 5), ('read.instance', 11), ('read.instance', 0)],
        }
        for name, expected in controls.items():
            script = (ROOT / 'cases' / (name + '.calls')).read_text()
            for verbose in (False, True):
                with self.subTest(case=name, verbose=verbose):
                    for side, rows in enumerate(self.pair(script, verbose=verbose)):
                        def assert_winners(trace):
                            reads = [r for r in trace if r['event'] in ('read.schema', 'read.instance', 'read.opaque')]
                            self.assertEqual(len(reads), len(expected))
                            for read, (event, handle) in zip(reads, expected, strict=True):
                                snapshot = next(r for r in trace if 'nodes' in r and r['operation'] == read['operation'])
                                nodes = check.by_handle(snapshot)
                                winner = nodes[handle]['generation'] if handle else 0
                                self.assertEqual((read['event'], read['winner'], read['rc']), (event, winner, 0 if handle else 5))
                        assert_winners(rows)
                        mutant = copy.deepcopy(rows)
                        next(r for r in mutant if r['event'] in ('read.schema', 'read.instance', 'read.opaque'))['rc'] = 7
                        with self.assertRaises(AssertionError): assert_winners(mutant)
                        if side == 0:
                            comparisons = check.commands(rows, 'read.compare')
                            if name == 'entry-guard': self.assertFalse(comparisons, 'guard must precede rewind/comparison')
                            if name == 'entry-order':
                                def assert_order(trace):
                                    expected_pairs = [(4, 11), (11, 4), (11, 4), (11, 4), (4, 11), (14, 5)]
                                    observed = check.commands(trace, 'read.compare')
                                    self.assertEqual(len(observed), len(expected_pairs))
                                    for row, (left, right) in zip(observed, expected_pairs, strict=True):
                                        snapshot = next(r for r in trace if 'nodes' in r and r['operation'] == row['operation'])
                                        nodes = check.by_handle(snapshot)
                                        self.assertEqual((row['left'], row['right'], row['options']), (nodes[left]['generation'], nodes[right]['generation'], 0))
                                assert_order(rows)
                                mutant = copy.deepcopy(rows)
                                for row in check.commands(mutant, 'read.compare'): row['left'], row['right'] = row['right'], row['left']
                                with self.assertRaises(AssertionError): assert_order(mutant)
                            if name == 'entry-duplicate':
                                self.assertTrue(comparisons)
                                self.assertTrue(all(r['options'] != 0 for r in comparisons), 'duplicate-instance comparison needs full recursion')
                        if name == 'entry-duplicate':
                            def missing_inner_table(trace):
                                for row in trace:
                                    for node in row.get('nodes', []):
                                        if node.get('schema') == 'kl': node.pop('table', None)
                            self.rejects(script, rows, missing_inner_table)

    def test_schema_lineage_in_both_schema_modes(self):
        script = (ROOT / 'cases' / 'entry-schema-lineage.calls').read_text()
        for event_schema in (False, True):
            for rows in self.pair(script, event_schema=event_schema):
                reads = check.commands(rows, 'read.schema')
                self.assertEqual(len(reads), 4)
                self.assertEqual([r['before']['present'] for r in reads], [False, False, True, True])
                self.assertTrue(all((r['winner'], r['rc']) == (0, 5) for r in reads))
                snapshot = check.by_handle(rows[-1])
                self.assertNotEqual(snapshot[4]['schema_identity'], snapshot[13]['schema_identity'])
                self.assertEqual(snapshot[4]['schema'], snapshot[13]['schema'])

    def test_f1_f7_flag_matrix(self):
        # All five bits, with independent allocation sets and primitive counts.
        # Parent creation forwards only NO_META|WITH_FLAGS, never RECURSIVE.
        counts = {}
        for flags in list(range(16)) + list(range(64, 80)):
            for shape, prefix, source, parent in (
                ('container', 'root 1 c\nterm 2 1 p P\n', 1, 0),
                ('list', 'root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\n', 2, 0),
                ('leaf-parents', 'root 1 c\nterm 2 1 p P\n', 2, 0),
                ('list-parents', 'root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\n', 3, 0),
                ('existing-parent', 'root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\nroot 4 c\nlist 5 4 l y\n', 3, 5),
            ):
                script = prefix + f'dup 10 {source} {parent} {flags}\nprint 10\n'
                with self.subTest(flags=flags, shape=shape):
                    rows, plain = self.pair(script, verbose=True)
                    row = check.commands(rows, 'dup')[0]
                    check.duplication(row, ['dup', '10', str(source), str(parent), str(flags)])
                    result = check.by_handle(row)[10]
                    self.assertTrue(result['id'] == 10)
                    self.assertEqual(len([n for n in row['nodes'] if n['id'] == 10]), 1)
                    self.assertEqual([b['generation'] for b in row['bindings'] if b['handle'] == 10], [result['generation']])
                    measured = Counter(r['event'] for r in rows)
                    # Pinned primitive schedules: index order is PRIMITIVES.
                    schedules = {
                        'container': {0: (2, 2, 1, 0), 1: (3, 3, 2, 0)},
                        'leaf-parents': {0: (2, 2, 1, 0), 4: (3, 3, 2, 0)},
                        'list': {0: (7, 5, 3, 1), 1: (8, 6, 3, 2), 4: (8, 6, 4, 1), 5: (9, 7, 4, 2)},
                        'list-parents': {0: (5, 4, 2, 1), 4: (10, 8, 4, 2)},
                        'existing-parent': {0: (8, 6, 4, 2)},
                    }
                    mask = 1 if shape == 'container' else 4 if shape in ('leaf-parents', 'list-parents') else 5 if shape == 'list' else 0
                    expected = [0] * len(check.PRIMITIVES)
                    for index, count in zip((0, 7, 8, 9), schedules[shape][flags & mask], strict=True): expected[index] = count
                    for suffix in ('.before', '.after'):
                        self.assertEqual(tuple(measured[p + suffix] for p in check.PRIMITIVES), tuple(expected))
                    counts[flags, shape] = tuple(expected)
                    # F7 old checker: shallow container/list must have fewer
                    # children, WITH_PARENTS leaf need not allocate first.
                    if shape == 'leaf-parents' and flags & 4:
                        self.assertGreater(result['creation_ordinal'], 1)
                        with self.assertRaises(AssertionError):
                            assert result['creation_ordinal'] == 1, 'pre-fix requested-node ordinal assumption'
                    if shape == 'container' and not flags & 1:
                        self.assertEqual(result['first'], 0)
                        with self.assertRaises(AssertionError):
                            assert check.by_handle(row)[source]['first'] == 0, 'pre-fix recursive child-suffix assumption'
                    # F1 mutant gives the first incidental ancestor its handle.
                    if flags & 4 and shape in ('leaf-parents', 'list-parents'):
                        def wrong_first(trace):
                            for r in trace:
                                for n in r.get('nodes', []):
                                    if n['creation_operation'] == row['operation'] and n['creation_ordinal'] == 1:
                                        n['id'] = 10
                        self.rejects(script, rows, wrong_first)
        # Register the native schedules explicitly for reproducible review.
        path = ROOT / '.build' / 'sweep-primitive-counts.json'
        path.write_text(json.dumps({f'{flags}/{shape}': count for (flags, shape), count in counts.items()}, sort_keys=True, indent=2) + '\n')
        self.assertEqual(len(counts), 160)

    def test_f7_key_parent_branches(self):
        counts = {}
        for flags in list(range(16)) + list(range(64, 80)):
            for detached in (False, True):
                script = 'root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\nrewind 3\n'
                if detached: script += 'dup 6 3 0 0\n'
                script += 'root 4 c\nlist 5 4 l y\n'
                source = 6 if detached else 3
                parent = 4 if flags & 4 and not detached else 5
                script += f'dup 10 {source} {parent} {flags}\nprint 10\n'
                with self.subTest(flags=flags, detached=detached):
                    rows, plain = self.pair(script, verbose=True)
                    row = check.commands(rows, 'dup')[-1]
                    measured = Counter(r['event'] for r in rows)
                    expected = [0] * len(check.PRIMITIVES)
                    schedule = ((9, 7, 4, 1) if flags & 4 else (8, 6, 4, 1)) if detached else ((11, 8, 5, 2) if flags & 4 else (7, 5, 4, 1))
                    for index, count in zip((0, 7, 8, 9), schedule, strict=True): expected[index] = count
                    counts[flags, detached] = tuple(measured[p + '.before'] for p in check.PRIMITIVES)
                    for suffix in ('.before', '.after'):
                        self.assertEqual(tuple(measured[p + suffix] for p in check.PRIMITIVES), tuple(expected))
                    check.duplication(row, ['dup', '10', str(source), str(parent), str(flags)])
                    out = check.by_handle(row)[10]
                    if flags & 4:
                        self.assertEqual(out['creation_operation'], row['operation'])
                        self.assertEqual(out['value'], 'x')
                        self.assertEqual(out['creation_ordinal'], 1 if detached else 2)
                        if detached: self.assertEqual(out['parent'], 0)
                        else:
                            nodes = {n['generation']: n for n in row['nodes']}
                            self.assertEqual(nodes[out['parent']]['parent'], check.by_handle(row)[4]['generation'])
                    else:
                        self.assertNotEqual(out['creation_operation'], row['operation'])
                        self.assertEqual(out['value'], 'y')
                        self.assertEqual(out['parent'], check.by_handle(row)[5]['generation'])
        (ROOT / '.build' / 'sweep-key-primitive-counts.json').write_text(json.dumps({f'{f}/{d}': c for (f, d), c in counts.items()}, indent=2) + '\n')

    def test_f2_ancestor_reuse(self):
        script = 'root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\n'
        script += ('dup 10 3 0 5\nprint 10\nfree-root 10\n' * 32) + 'print 1\n'
        for plain in (False, True):
            for verbose in (False, True):
                with self.subTest(plain=plain, verbose=verbose):
                    rows = execute(script, plain, verbose)
                    final = {n['generation']: n for n in rows[-1]['nodes']}
                    ancestors = {n['generation'] for r in check.commands(rows, 'dup') for n in r['nodes']
                                 if n['alive'] and n['creation_operation'] == r['operation'] and n.get('schema') in ('c', 'l')}
                    reused = [r for r in rows if r['event'] == 'allocation.address-reuse' and r['old_generation'] in ancestors]
                    self.assertTrue(reused, 'must actually reuse an ancestor address')
                    for row in check.commands(rows, 'dup'):
                        nodes = {n['generation']: n for n in row['nodes']}
                        leaf = check.by_handle(row)[10]
                        parent = nodes[leaf['parent']]
                        self.assertTrue(parent['alive'])
                        self.assertIn('table', parent)
                        self.assertTrue(nodes[parent['first']]['key'])
                    def dead_parent(trace):
                        r = check.commands(trace, 'dup')[-1]
                        nodes = {n['generation']: n for n in r['nodes']}
                        parent = nodes[check.by_handle(r)[10]['parent']]
                        parent['alive'] = False
                    self.rejects(script, rows, dead_parent)

    def test_f3_aliases_refresh_and_release(self):
        script = ('root 1 c\nlist 2 1 l x\nopaque 3 2 extra value\nrewind 3\n'
                  'dup 4 3 2 0\ndup 5 3 2 0\ncheck-handle 4\nprint 2\nfree-root 5\ncheck-handle 4\n')
        for rows in self.pair(script, terminal='boundary.tombstoned-handle'):
            aliases = check.commands(rows, 'check-handle')[0]
            bs = {b['handle']: b['generation'] for b in aliases['bindings']}
            self.assertEqual(bs[3], bs[4]); self.assertEqual(bs[4], bs[5])
            self.assertEqual(sum(n['creation_operation'] in (5, 6) for n in aliases['nodes']), 0)
            self.assertEqual(next(n for n in aliases['nodes'] if n['generation'] == bs[3])['id'], 0)
            dead = {n['generation']: n for n in check.commands(rows, 'free-root')[-1]['nodes']}
            self.assertFalse(dead[bs[4]]['alive'])
            def overwrite_alias(trace):
                r = check.commands(trace, 'dup')[-1]
                target = check.by_handle(r)[5]['generation']
                next(n for n in r['nodes'] if n['generation'] == target)['id'] = 5
            self.rejects(script, rows, overwrite_alias)
            def stale_rewind(trace):
                r = check.commands(trace, 'rewind')[0]
                opaque = next(n['generation'] for n in r['nodes'] if n['id'] == 3)
                next(b for b in r['bindings'] if b['handle'] == 3)['generation'] = opaque
            self.rejects(script, rows, stale_rewind)
            for r in check.commands(rows, 'dup'):
                check.duplication(r, ['dup', '4' if r['operation'] == 5 else '5', '3', '2', '0'])
        # Changed top-level root: validate d creates the defaulted NP c before d.
        script = 'root 1 d\nvalidate 1\nprint 1\n'
        for rows in self.pair(script):
            root, validation = check.commands(rows, 'root')[0], check.commands(rows, 'validate')[0]
            old, new = check.by_handle(root)[1], check.by_handle(validation)[1]
            self.assertNotEqual(old['generation'], new['generation'])
            self.assertEqual(new['schema'], 'c')
            def stale_validation(trace):
                r = check.commands(trace, 'validate')[0]
                next(b for b in r['bindings'] if b['handle'] == 1)['generation'] = old['generation']
            self.rejects(script, rows, stale_validation)

    def test_f4_occurrence_continuity(self):
        script = ('root 1 c\nterm 2 1 p P\nterm 3 1 q Q\nterm 4 1 ll QQ==\n'
                  'term 5 1 ll Qg==\nterm 6 1 ll Qw==\nbefore 5 6\nattach 1 6\nchange 6 RA==\nprint 1\n')
        for plain in (False, True):
            rows = execute(script, plain)
            snaps = [r for r in rows if r['event'] in ('term', 'before', 'attach', 'change') and r['operation'] >= 6]
            previous = None
            for row in snaps:
                n = check.by_handle(row)
                table = n[1]['table']
                self.assertEqual(table['continuity'], 'unknown' if plain else 'observed')
                current = {x['occurrence'] for x in table['chains'] if x['generation'] == n[6]['generation']}
                if previous is not None and not plain: self.assertTrue(previous.isdisjoint(current), 'same-slot reinsertion retained occurrence')
                previous = current
            self.assertEqual(len({check.by_handle(r)[1]['table']['size'] for r in snaps}), 1)
            if plain:
                def false_continuity(trace):
                    for r in trace:
                        for _, t in check.tables(r):
                            if t['present']: t['continuity'] = 'observed'
                self.rejects(script, rows, false_continuity)


    def test_f5_winners_and_f6_structural_mutants(self):
        scripts = (
            'root 1 c\nterm 2 1 p old\nfree 2\nterm 2 1 p new\nschema 1 2\nprint 1\n',
            'parse 1 {"m:c":{"p":"P","q":"Q"}}\nopaque 2 1 extra value\nrewind 2\nprint 1\n',
            'root 1 c\nterm 2 1 p P\nterm 3 1 q Q\nterm 4 1 ll QQ==\nterm 5 1 ll Qg==\nprint 1\n',
        )
        for script in scripts:
            left, right = self.pair(script)
            mutations = []
            def remove_nodes(trace):
                for r in trace:
                    if 'nodes' in r: r['nodes'] = []
            mutations.append(remove_nodes)
            def remove_tables(trace):
                for r in trace:
                    if 'nodes' in r:
                        r['tables'], r['table_count'] = [], 0
                        for n in r['nodes']: n.pop('table', None)
            mutations.append(remove_tables)
            def remove_internal(trace):
                for r in trace:
                    if 'nodes' in r: r['nodes'] = [n for n in r['nodes'] if n['id'] or n['creation_operation'] == 0]
            if script.startswith('parse'): mutations.append(remove_internal)
            def lose_bootstrap(trace):
                for r in trace:
                    if 'nodes' in r: r['nodes'] = [n for n in r['nodes'] if n['creation_operation']]
            mutations.append(lose_bootstrap)
            def zero_tables(trace):
                for r in trace:
                    for owner, t in check.tables(r):
                        if t['present']: t['identity'] = 0
            if any(t['present'] for r in left for _, t in check.tables(r)):
                mutations.append(zero_tables)
                def collapse_tables(trace):
                    for r in trace:
                        for t in r.get('tables', []): t['identity'] = 1
                if max(r.get('table_count', 0) for r in left) > 1: mutations.append(collapse_tables)
            def duplicate_label(trace):
                r = next(r for r in reversed(trace) if 'nodes' in r)
                live = [n for n in r['nodes'] if n['alive'] and n['id']]
                live[-1]['id'] = live[0]['id']
            mutations.append(duplicate_label)
            def missing_binding(trace):
                for r in trace:
                    if 'bindings' in r: r['bindings'] = []
            mutations.append(missing_binding)
            if 'schema 1 2' in script:
                def old_winner(trace):
                    dead = next(n['generation'] for r in reversed(trace) for n in r.get('nodes', []) if n['id'] == 2 and not n['alive'])
                    check.commands(trace, 'read.schema')[0]['winner'] = dead
                mutations.append(old_winner)
                def dead_link(trace):
                    r = trace[-1]
                    dead = next(n['generation'] for n in r['nodes'] if n['id'] == 2 and not n['alive'])
                    next(n for n in r['nodes'] if n['id'] == 2 and n['alive'])['parent'] = dead
                mutations.append(dead_link)
            if 'rewind' in script:
                def other_internal(trace):
                    q = next(n['generation'] for r in reversed(trace) for n in r.get('nodes', []) if n.get('schema') == 'q')
                    check.commands(trace, 'read.first-sibling')[0]['winner'] = q
                mutations.append(other_internal)
            for mutate in mutations:
                for side in ('left', 'right', 'both'):
                    with self.subTest(script=script, mutant=mutate.__name__, side=side):
                        a = self.rejects(script, left, mutate) if side != 'right' else left
                        b = self.rejects(script, right, mutate) if side != 'left' else right
                        # Even equal corrupt evidence must fail before comparison.
                        with self.assertRaises((AssertionError, KeyError, IndexError)):
                            self.assertEqual(validate(script, a), validate(script, b))

    def test_retained_links_stop_before_discovery(self):
        script = (ROOT / 'cases' / 'lifetime' / 'retained-links.calls').read_text()
        for verbose in (False, True):
            for rows in self.pair(script, verbose=verbose, terminal='boundary.retained-dangling-links'):
                last = rows[-1]
                handles = {b['handle']: b['generation'] for b in last['bindings']}
                nodes = {n['generation']: n for n in last['nodes']}
                self.assertFalse(nodes[handles[16]]['alive'])
                self.assertFalse(nodes[handles[17]]['alive'])
                self.assertTrue(nodes[handles[1]]['alive'])
                self.assertEqual(nodes[handles[1]]['current'], 'unsafe-native-links')
                retained = [x for entry in last['tables'] if entry['alive'] for x in entry['storage']['chains']]
                self.assertTrue(any(x['generation'] == handles[16] and not x['alive'] for x in retained))
                self.rejects(script, rows, lambda trace: trace[-1].update(event='free-root'))
                def missing_storage(trace):
                    for entry in trace[-1]['tables']: entry.pop('storage', None)
                self.rejects(script, rows, missing_storage)

    def test_f8_transient_scope(self):
        cases = (
            ('root 1 c\nterm 2 1 v invalid\nprint 1\n', 2, 1),
            ('parse 1 {"m:c":{"p":"P","l":[{}]}}\nroot 2 c\nprint 2\n', 1, 3),
            ('parse 1 {"@m:c":{},"m:c":{"p":"P"}}\nprint 1\n', 1, 1),
        )
        for script, operation, expected_dead in cases:
            left, right = self.pair(script, verbose=True)
            def transient_evidence(rows):
                self.assertEqual(rows[-1]['allocation_scope'], 'every-allocation')
                dead = [n for n in rows[-1]['nodes'] if n['creation_operation'] == operation and not n['alive']]
                self.assertEqual(len(dead), expected_dead)
                for n in dead:
                    self.assertTrue(any(r['event'] == 'node.free' and r['node_generation'] == n['generation'] for r in rows))
                return dead
            dead = transient_evidence(left)
            self.assertEqual(right[-1]['allocation_scope'], 'survivors-and-observed-frees')
            for n in dead:
                mutant = copy.deepcopy(left)
                for r in mutant:
                    if 'nodes' in r: r['nodes'] = [x for x in r['nodes'] if x['generation'] != n['generation']]
                with self.assertRaises(AssertionError): transient_evidence(mutant)
                with self.assertRaises(AssertionError): validate(script, mutant)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--driver', type=pathlib.Path, default=os.environ.get('YANG_NATIVE_REGISTRATION_DRIVER') or None)
    parser.add_argument('--comparison-driver', type=pathlib.Path, default=os.environ.get('YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER') or None)
    args, rest = parser.parse_known_args()
    if args.driver is None: parser.error('--driver or YANG_NATIVE_REGISTRATION_DRIVER is required')
    DRIVER = args.driver.resolve()
    COMPARISON_DRIVER = args.comparison_driver.resolve() if args.comparison_driver else DRIVER.with_name('driver-unmodified')
    unittest.main(argv=[sys.argv[0], *rest])
