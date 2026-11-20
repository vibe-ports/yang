# SPDX-License-Identifier: BSD-3-Clause
"""Live input/output controls for table lifetimes and bounded tree evidence."""
import copy
import dis
import json
import pathlib
import sys
import unittest

from evidence_test import replay
from identity import validate, table_records

BASE = 'root 1 c\nterm 2 1 p A\nterm 3 1 q B\nterm 4 1 ll a\nterm 5 1 ll b\n'


def replacement(rows, generic=False):
    rows = copy.deepcopy(rows)
    row = rows[-1]
    table = row['table'] if generic else next(n['table'] for n in row['nodes'] if n['alive'] and n['id'] == 1)
    old, new = table['identity'], row['table_count'] + 1
    row['tables'][old - 1]['alive'] = False
    row['tables'].append({'identity': new, 'alive': True})
    row['table_count'] = new
    table['identity'] = new
    serial = max(x['occurrence'] for r in rows for _, t in table_records(r) for x in t.get('chains', []))
    for index, entry in enumerate(table['chains'], serial + 1):
        entry['occurrence'] = index
    return rows


def nested(rows, count):
    rows = copy.deepcopy(rows)
    row = rows[-1]
    owner = next(n for n in row['nodes'] if n['id'] == 1)
    root = owner['generation']
    template = copy.deepcopy(owner)
    for offset in range(1, count):
        generation = root + offset
        node = copy.deepcopy(template)
        node.update(generation=generation, id=0, creation_ordinal=offset + 1,
                    parent=generation - 1, prev=generation, next=0, first=0, tail=0)
        row['nodes'][-1].update(first=generation, tail=generation)
        row['nodes'].append(node)
    row['node_count'] = len(row['nodes'])
    return rows


class LifetimeIdentity(unittest.TestCase):
    def test_mutations_keep_table_identity(self):
        # The fifth insertion grows the leaf-list; a larger addition resizes.
        cases = {'insert': 'term 6 1 ll c\n', 'change': 'change 4 c\n',
                 'remove': 'unlink 4\n',
                 'resize': ''.join('term %d 1 ll v%d\n' % (n, n) for n in range(6, 18))}
        for plain in (False, True):
            for label, suffix in cases.items():
                with self.subTest(build='unmodified' if plain else 'instrumented', boundary=label):
                    script = BASE + 'equal 0 0 1\n' + suffix
                    rows = replay(script, plain=plain)
                    validate(script, rows)
                    with self.assertRaisesRegex(AssertionError, 'mutation replaced owned table identity'):
                        validate(script, replacement(rows))

    def test_generic_mutation_keeps_table_identity(self):
        for plain in (False, True):
            for suffix in ('insert 2 8 0\n', 'remove 1 7\n'):
                script = 'equal 0 0 1\nnew 8 1\ninsert 1 7 0\n' + suffix
                rows = replay(script, plain=plain)
                validate(script, rows)
                with self.assertRaisesRegex(AssertionError, 'mutation replaced generic table identity'):
                    validate(script, replacement(rows, generic=True))
            # new is the one operation which replaces the generic object.
            script = 'equal 0 0 1\nnew 8 1\nnew 16 1\n'
            validate(script, replay(script, plain=plain))

    def test_reuse_notice_cannot_borrow_future(self):
        script = 'root 1 c\nroot 2 c\nfree 1\nequal 2 2 0\n'
        for plain in (False, True):
            rows = replay(script, plain=plain)
            validate(script, rows)
            first = next(r for r in rows if r['event'] == 'root')
            roots = [r['output_generation'] for r in rows if r['event'] == 'root']
            notice = {'event': 'allocation.address-reuse', 'operation': 1,
                      'request': first['request'], 'old_generation': roots[0], 'new_generation': roots[1]}
            rows.insert(rows.index(first), notice)
            with self.assertRaises(AssertionError):
                validate(script, rows)

    def test_corrupt_sibling_rejected_before_lookup(self):
        script = 'equal 0 0 1\nroot 1 c\nterm 2 1 p A\nschema 1 2\n'
        for plain in (False, True):
            rows = replay(script, plain=plain)
            validate(script, rows)
            term = next(r for r in rows if r['event'] == 'term')
            leaf = next(n for n in term['nodes'] if n['id'] == 2)
            leaf['next'] = leaf['generation']
            with self.assertRaisesRegex(AssertionError, 'schema sibling traversal limit/cycle'):
                validate(script, rows)

    def test_retained_native_links_are_preserved(self):
        root = pathlib.Path(__file__).resolve().parent
        for name, terminal in (('remote-last-release', 'print'),
                               ('lifetime/retained-links', 'boundary.retained-dangling-links')):
            script = (root / 'cases' / (name + '.calls')).read_text()
            for plain in (False, True):
                for mode in ('--quiet', '--events'):
                    with self.subTest(case=name, plain=plain, mode=mode):
                        rows = replay(script, plain=plain, mode=mode, read=False)
                        states = validate(script, rows)
                        self.assertEqual(rows[-1]['event'], terminal)
                        # Reach the native endpoint discrepancy which caused
                        # the old validator to reject this supported replay.
                        term = next(r for r in rows if r['event'] == 'term' and r['operation'] == 14)
                        nodes = {n['generation']: n for n in term['nodes']}
                        owner = next(n for n in nodes.values() if n['alive'] and n['id'] == 1)
                        self.assertNotEqual(nodes[owner['tail']]['parent'], owner['generation'])
                        self.assertIn((14, 'identity.state'), states)
                        if terminal.startswith('boundary.'):
                            self.assertTrue(any(n.get('current') == 'unsafe-native-links' for n in rows[-1]['nodes']))
                        else:
                            self.assertEqual(sum(r['event'] == 'free-root' for r in rows), 3)

    def test_deep_walk_is_iterative_and_bounded(self):
        script = 'equal 0 0 1\nroot 1 c\n'
        rows = replay(script)
        validate(script, nested(rows, 1100))
        with self.assertRaisesRegex(AssertionError, 'normalization traversal limit'):
            validate(script, nested(rows, 4097))

    def test_quiet_reuse_work_is_bounded(self):
        # Real duplicate-heavy native parses emit many distinct reuse notices
        # before the same quiet snapshot. Count data-key reads, so rebuilding
        # the complete registry per notice exceeds a linear allowance.
        samples = []
        for plain in (False, True):
            for size in (20, 40, 80):
                xml = '<c xmlns="urn:m">' + '<ll>A</ll>' * size + '</c>'
                script = 'equal 0 0 1\nparse-xml 1 ' + xml + '\nfree-root 1\nparse-xml 1 ' + xml + '\n'
                rows = replay(script, plain=plain)
                notices = [r for r in rows if r['event'] == 'allocation.address-reuse']
                self.assertGreaterEqual(len(notices), size // 4)
                self.assertEqual(len(notices), len({(r['old_generation'], r['new_generation']) for r in notices}))
                self.assertTrue(all(r['operation'] == 4 for r in notices))
                work, subscriptions = [0, 0], {}
                filename = validate.__code__.co_filename
                def count(frame, event, argument):
                    if frame.f_code.co_filename == filename:
                        if event == 'call': frame.f_trace_opcodes = True
                        if event == 'line': work[0] += 1
                        if event == 'opcode':
                            code = id(frame.f_code)
                            if code not in subscriptions:
                                instructions = list(dis.get_instructions(frame.f_code))
                                subscriptions[code] = {right.offset for left, right in zip(instructions, instructions[1:])
                                    if left.opname == 'LOAD_CONST' and left.argval in ('generation', 'identity')
                                    and right.opname == 'BINARY_SUBSCR'}
                            if frame.f_lasti in subscriptions[code]: work[1] += 1
                    return count
                sys.settrace(count)
                try:
                    validate(script, rows)
                finally:
                    sys.settrace(None)
                entries = sum(len(r.get('nodes', [])) + len(r.get('tables', [])) for r in rows)
                self.assertLessEqual(work[1], entries * 5, 'reuse work must be bounded by registry entries, not notices times entries')
                samples.append({'plain': plain, 'nodes': size, 'notices': len(notices),
                                'work': work, 'snapshot_entries': entries})
        print(json.dumps({'quiet_reuse_work': samples}), flush=True)

    def test_nested_work_is_linear(self):
        # Count actual validator work on duplicate schema descriptors and
        # adverse deep insertion order, independently of wall-clock timings.
        script = 'equal 0 0 1\nroot 1 c\n'
        rows = replay(script)
        samples = []
        for size in (64, 128, 256):
            evidence = nested(rows, size)
            calls = 0
            def count(frame, event, argument):
                nonlocal calls
                if event == 'line' and frame.f_code.co_filename.endswith('/identity.py'):
                    calls += 1
                return count
            sys.settrace(count)
            try:
                validate(script, evidence)
            finally:
                sys.settrace(None)
            samples.append(calls)
        self.assertLess(samples[2], samples[1] * 2.3)
        # a10a82a uses the same logical trace with its pre-part-1 plain-text
        # fields. These line counts were measured from that pinned validator.
        main_work = [14553, 39353, 125817]
        self.assertLess(samples[-1], main_work[-1])
        print(json.dumps({'lifetime_nested_work_main': main_work, 'lifetime_nested_work': samples}), flush=True)


if __name__ == '__main__':
    unittest.main()
