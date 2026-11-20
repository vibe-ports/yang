# SPDX-License-Identifier: BSD-3-Clause
"""Live replay controls for completion, native roles, CLI fallback and attribution."""
import copy
import ast
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import check
import evidence_test
from drivers import comparison_driver, verify_pair
from identity import completion, validate
from sensitivity import go_result
from transport import run

ROOT = pathlib.Path(__file__).resolve().parent


class EntryEvidence(unittest.TestCase):
    def test_terminal_boundary(self):
        script = (ROOT / 'cases/read/read-success-payload.calls').read_text()
        left, right = evidence_test.replay(script), evidence_test.replay(script, plain=True)
        for rows in (left, right):
            completion(script, rows)
            validate(script, rows)
        for side in ('left', 'right', 'both'):
            peers = [copy.deepcopy(left), copy.deepcopy(right)]
            for rows in peers if side == 'both' else [peers[0 if side == 'left' else 1]]:
                index = next(i for i, row in enumerate(rows) if row['event'] == 'print')
                boundary = dict(copy.deepcopy(rows[index]), event='boundary.tombstoned-semantic-probe')
                rows.insert(index + 1, boundary)
                with self.assertRaisesRegex(AssertionError, 'evidence after terminal boundary'):
                    validate(script, rows)
                with self.assertRaises(AssertionError):
                    check.completed_replay('read/read-success-payload', rows)
            with self.subTest(side=side):
                with self.assertRaises(AssertionError):
                    check.compare_control('read/read-success-payload', 0, peers[0], 0, peers[1])
        # A recognized terminal suffix cannot stand in for requested completion,
        # even when the field sweep is given two identical shortened peers.
        index = next(i for i, row in enumerate(left) if row['event'] == 'print')
        shortened = copy.deepcopy(left[:index + 1])
        shortened[-1]['event'] = 'boundary.tombstoned-semantic-probe'
        with self.assertRaises(AssertionError):
            evidence_test.sweep(script, shortened, shortened)

    def test_native_roles(self):
        primary = os.environ['YANG_NATIVE_REGISTRATION_DRIVER']
        plain = comparison_driver()
        verify_pair(primary, plain)
        with patch.dict(os.environ, {'YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER': primary}):
            evidence_test.replay('equal 0 0 1\n')
            with self.assertRaisesRegex(AssertionError, 'native ABI/role mismatch'):
                evidence_test.replay('equal 0 0 1\n', plain=True)

    def test_mutation_copy_work(self):
        # Reuse the exact previous field mutator, independently of the new path
        # copying implementation, to compare corruption and counted copy work.
        source = run(['git', 'show', '11547b7:conformance/oracle/native_registration/evidence_test.py'], cwd=ROOT, text=True)
        self.assertEqual(source.returncode, 0, source.stderr)
        tree = ast.parse(source.stdout)
        functions = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name in ('parent_at', 'perturb')]
        namespace = {'copy': copy}
        exec(compile(ast.Module(body=functions, type_ignores=[]), '<previous-field-mutator>', 'exec'), namespace)
        previous = namespace['perturb']
        script = 'root 1 c\nterm 2 1 p A\nprint 1\n'
        rows, peer = evidence_test.replay(script, read=False), evidence_test.replay(script, read=False, plain=True)
        before, peer_before = copy.deepcopy(rows), copy.deepcopy(peer)
        examined = 0
        for index, row in enumerate(rows):
            paths = list(evidence_test.paths(row))
            for path, value in paths[::max(1, len(paths) // 8)]:
                for action in ('typed', 'delete', 'add', 'mutate'):
                    expected = previous(rows, index, path, action)
                    actual = evidence_test.perturb(rows, index, path, action)
                    self.assertEqual(actual, expected, (index, path, action))
                    if actual is not None:
                        retained = copy.deepcopy(actual)
                        for project in (True, False):
                            evidence_test.invalid_contract(script, actual, project)
                        self.assertEqual(actual, retained, 'validator mutated evidence')
                    self.assertEqual(rows, before, 'field mutator changed baseline')
                    self.assertEqual(peer, peer_before, 'field mutator changed comparison peer')
                    examined += 1
        # Count every old deepcopy call and every slot copied by the new shallow
        # container copies. Increasing duplicate evidence must not rescan all
        # nested fields merely to perturb one scalar.
        measured = []
        for repeats in (1, 4):
            expanded = [copy.deepcopy(row) for _ in range(repeats) for row in rows]
            values = []
            for mutator in (previous, evidence_test.perturb):
                count = 0
                def trace(frame, event, arg):
                    nonlocal count
                    if event == 'call' and frame.f_code is copy.deepcopy.__code__: count += 1
                    if event == 'c_call' and getattr(arg, '__name__', '') == 'copy' and type(getattr(arg, '__self__', None)) in (dict, list): count += len(arg.__self__)
                sys.setprofile(trace)
                try: mutator(expanded, 0, ('nodes', 0, 'hash'), 'mutate')
                finally: sys.setprofile(None)
                values.append(count)
            measured.append((len(expanded), *values))
        self.assertTrue(all(head * 8 < base for _, base, head in measured), measured)
        print(json.dumps({'mutator_copy_work_previous_head': measured, 'equivalent_mutations': examined}), flush=True)

    def test_boundary_event_types(self):
        # Completion checks run before full shape validation. Every wrong event
        # or operation type must be a rejected contract, never an AttributeError.
        for value in ({}, [], None, 1, False):
            row = dict(event=value, operation=1)
            for consumer in (completion, validate):
                with self.subTest(value=value, consumer=consumer.__name__), self.assertRaisesRegex(AssertionError, 'invalid event type'):
                    consumer('dump\n', [row])
            self.assertTrue(evidence_test.invalid_contract('dump\n', [row], True))
        for value in ({}, [], None, '1', False):
            with self.subTest(operation=value), self.assertRaisesRegex(AssertionError, 'invalid operation type'):
                completion('dump\n', [dict(event='dump', operation=value)])

    def test_explicit_reuse_roles(self):
        from reuse_test import execute
        primary, plain = os.environ['YANG_NATIVE_REGISTRATION_DRIVER'], comparison_driver()
        # A path-taking replay helper cannot silently infer an instrumented
        # role for its unmodified argument. Missing roles fail at API entry.
        with self.assertRaises(TypeError):
            execute(plain, 'lifetime/recreate-lookup', False, 'utc')
        for instrumented, driver in ((True, primary), (False, plain)):
            result = execute(driver, 'lifetime/recreate-lookup', False, 'utc', instrumented=instrumented)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_empty_cli(self):
        primary = pathlib.Path(os.environ['YANG_NATIVE_REGISTRATION_DRIVER']).resolve()
        plain = comparison_driver().resolve()
        # Each real CLI must preserve the empty token through argparse until
        # fallback resolution; the selected sibling identifies itself via ABI.
        with tempfile.TemporaryDirectory(dir=ROOT / '.build') as directory:
            directory = pathlib.Path(directory)
            shutil.copyfile(primary, directory / 'driver')
            (directory / 'driver').chmod(0o700)
            fallback = directory / 'driver-unmodified'
            fallback.write_text('#!/bin/sh\nprintf \'{"fallback_selected":1}\\n\'\n')
            fallback.chmod(0o700)
            env = dict(os.environ, YANG_NATIVE_REGISTRATION_DRIVER=str(directory / 'driver'))
            env.pop('YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER', None)
            for entry in ('check.py', 'sweep_test.py', 'reuse_test.py', 'custom_schema_test.py'):
                result = run(['python3', str(ROOT / entry), '--comparison-driver', ''], env=env, timeout=30)
                self.assertNotEqual(result.returncode, 0, entry)
                self.assertIn(b'fallback_selected', result.stderr, (entry, result.stderr))
                self.assertIn(b'native ABI/role mismatch', result.stderr, entry)
        # The positive CLI probe runs the actual unmodified sibling control.
        env = dict(os.environ)
        env.pop('YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER', None)
        self.assertEqual(primary.with_name('driver-unmodified'), plain)
        result = run(['python3', str(ROOT / 'check.py'), '--comparison-driver', '',
                      '--lifetime-case', 'recreate-lookup'], env=env, timeout=60)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_completion_work(self):
        # main's completion consumer runs unchanged against the same actual
        # duplicate-heavy, adverse-order native evidence as the current helper.
        source = run(['git', 'show', 'a10a82a:conformance/oracle/native_registration/check.py'], cwd=ROOT, text=True)
        self.assertEqual(source.returncode, 0, source.stderr)
        directory = ROOT / '.build/round8/completion-work'
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / 'main_check.py'
        path.write_text(source.stdout)
        spec = importlib.util.spec_from_file_location('main_completion_check', path)
        baseline = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(baseline)
        (directory / 'cases').mkdir(exist_ok=True)
        measured = []
        setup = 'root 1 c\n' + ''.join(f'term {i + 2} 1 v {i % 3}\n' for i in reversed(range(16)))
        for reads in (4, 16):
            script = setup + 'print 1\n' * reads
            rows = evidence_test.replay(script, read=False)
            (directory / 'cases/control.calls').write_text(script)
            values = []
            for consumer in (baseline, check):
                files = {consumer.__file__}
                if consumer is check: files.add(sys.modules['identity'].__file__)
                count = 0
                def trace(frame, event, arg):
                    nonlocal count
                    if event == 'line' and frame.f_code.co_filename in files: count += 1
                    return trace
                with patch.object(consumer, 'root', directory):
                    sys.settrace(trace)
                    try: consumer.completed_replay('control', rows)
                    finally: sys.settrace(None)
                values.append(count)
            measured.append((reads, len(rows), *values))
        self.assertTrue(all(head <= 12 * main for _, _, main, head in measured), measured)
        a, b = measured
        self.assertLessEqual(b[3] * a[1] * 100, a[3] * b[1] * 110, measured)
        print(json.dumps({'completion_work_main_head': measured}), flush=True)

    def test_sensitivity_attribution(self):
        selector = '^TestControl$/intended$'
        def result(action='pass', text='', status=0, include_run=True):
            events = []
            if include_run: events.append(dict(Action='run', Test='TestControl/intended'))
            if text: events.append(dict(Action='output', Test='TestControl/intended', Output=text))
            events += [dict(Action=action, Test='TestControl/intended'), dict(Action=action)]
            return subprocess.CompletedProcess([], status, '\n'.join(map(json.dumps, events)))
        go_result(result(), selector, passing=True)
        with self.assertRaisesRegex(AssertionError, 'changed intended test inventory'):
            go_result(result(), selector, passing=True, expected_tests={'TestControl/intended', 'TestControl/missing'})
        go_result(result('fail', 'intended rejection', 1), selector, diagnostic='intended rejection')
        siblings = []
        for name, text in (('intended', 'intended rejection'), ('unrelated', 'unrelated runtime failure')):
            test = 'TestControl/' + name
            siblings += [dict(Action='run', Test=test), dict(Action='output', Test=test, Output=text), dict(Action='fail', Test=test)]
        mixed = subprocess.CompletedProcess([], 1, '\n'.join(map(json.dumps, siblings + [dict(Action='fail')])))
        with self.assertRaisesRegex(AssertionError, 'missing intended rejection diagnostic'):
            go_result(mixed, '^TestControl$', diagnostic='intended rejection')
        for forged, passing in ((result('skip'), True), (result('pass', include_run=False), True),
                                (result('fail', 'ModuleNotFoundError: identity', 1), False),
                                (result('fail', 'unrelated failure', 1), False),
                                (result('fail', 'intended rejection', 1, include_run=False), False)):
            with self.subTest(output=forged.stdout), self.assertRaises(AssertionError):
                go_result(forged, selector, passing=passing, diagnostic='intended rejection')
        # Exercise real Go JSON, including t.Skip and an unrelated runtime
        # setup failure, so successful command status cannot certify a test.
        with tempfile.TemporaryDirectory(dir=ROOT / '.build') as directory:
            directory = pathlib.Path(directory)
            (directory / 'go.mod').write_text('module sensitivity-control\n\ngo 1.23\n')
            for statement, status, message in (('t.Skip("control")', 0, 'skipped sensitivity regression'),
                                               ('t.Fatal("ModuleNotFoundError: control setup")', 1, 'unrelated setup failure')):
                (directory / 'control_test.go').write_text('package control\nimport "testing"\nfunc TestControl(t *testing.T) { t.Run("intended", func(t *testing.T) { ' + statement + ' }) }\n')
                actual = run(['go', 'test', '-json', '.', '-run', selector, '-count=1'], cwd=directory, text=True, timeout=60)
                self.assertEqual(actual.returncode, status, actual.stderr)
                with self.assertRaisesRegex(AssertionError, message):
                    go_result(actual, selector, passing=status == 0, diagnostic='intended rejection')



if __name__ == '__main__':
    unittest.main()
