# SPDX-License-Identifier: BSD-3-Clause
"""Attribute go-test JSON results to the selected sensitivity regressions."""
import json
import re

SETUP_FAILURES = ('[build failed]', 'ModuleNotFoundError', 'ImportError', 'SyntaxError',
                  'FileNotFoundError', 'NameError', 'setup exit', 'no tests to run',
                  'native ABI/role mismatch', 'native ABI exit', 'native ABI JSON')


def selected_tests(records, selector):
    patterns = selector.split('/')
    def selected(name):
        fields = name.split('/')
        return len(fields) >= len(patterns) and all(re.search(pattern, field) for pattern, field in zip(patterns, fields))
    return {r['Test'] for r in records if r.get('Action') == 'run' and selected(r.get('Test', ''))}


def go_result(result, selector, *, passing=False, diagnostic=None, expected_tests=None):
    records = [json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
    assert records, 'missing go test JSON events'
    output = ''.join(r.get('Output', '') for r in records)
    assert not any(failure in output for failure in SETUP_FAILURES), ('unrelated setup failure', output)
    assert not any(r.get('Action') == 'skip' for r in records), ('skipped sensitivity regression', output)
    ran = selected_tests(records, selector)
    if expected_tests is not None:
        assert ran == expected_tests, ('changed intended test inventory', selector, ran, expected_tests)
    assert ran, ('selected sensitivity regression did not run', selector, output)
    terminal = {r['Test']: r['Action'] for r in records if r.get('Test') in ran and r.get('Action') in ('pass', 'fail', 'skip')}
    assert ran == terminal.keys(), ('unfinished sensitivity regression', ran, terminal)
    if passing:
        assert result.returncode == 0 and all(action == 'pass' for action in terminal.values()), output
        assert any(r.get('Action') == 'pass' and not r.get('Test') for r in records), 'missing package pass'
    else:
        failed = {name for name, action in terminal.items() if action == 'fail'}
        assert result.returncode == 1 and failed, ('mutant did not fail selected regression', output)
        assert diagnostic, 'missing intended rejection diagnostic'
        leaves = {name for name in failed if not any(other.startswith(name + '/') for other in failed)}
        for name in leaves:
            intended = ''.join(r.get('Output', '') for r in records if r.get('Test') == name)
            assert re.search(diagnostic, intended), ('missing intended rejection diagnostic', name, diagnostic, intended)
    return records
