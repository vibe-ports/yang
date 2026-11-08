# SPDX-License-Identifier: BSD-3-Clause
"""Exact-address controls across process environments, plus real-allocator mutants."""
import argparse
import contextlib
import io
import json
import os
import pathlib
import subprocess
import unittest

import check
from identity import validate
from sweep_test import ancestor_reuse_script

ROOT = pathlib.Path(__file__).resolve().parent
DRIVER = COMPARISON_DRIVER = None
CASES = ('ancestor-reuse', 'driver-lifetime', 'handle-tombstone', 'handle-tombstone-probe',
         'retained-address-reuse', *('lifetime/' + case for case in check.LIFETIME_CONTROLS))
PROFILES = {'utc': {'TZ': 'UTC', 'LC_ALL': 'C'},
            'unset': {'LC_ALL': 'C.UTF-8'},
            'padded': {'TZ': 'UTC', 'LC_ALL': 'C.UTF-8', 'YANG_REUSE_PADDING': 'x' * 65536}}
FAILURE = 'native constructor did not reuse the prepared node chunk'


def script_for(name):
    return ancestor_reuse_script() if name == 'ancestor-reuse' else (ROOT / 'cases' / (name + '.calls')).read_text()


def execute(driver, name, verbose, profile):
    env = dict(os.environ)
    for key in ('TZ', 'LC_ALL', 'YANG_REUSE_PADDING'):
        env.pop(key, None)
    env.update(PROFILES[profile], GLIBC_TUNABLES='glibc.malloc.tcache_count=7')
    options = ['--events' if verbose else '--quiet']
    if name in ('ancestor-reuse', 'lifetime/list-key', 'lifetime/validation-delete'):
        options.append('--event-schema')
    return subprocess.run([str(driver), *options], input=script_for(name), text=True,
                          capture_output=True, timeout=25, check=False, env=env)


def evidence(name, rows):
    validate(script_for(name), rows)
    if name == 'ancestor-reuse':
        ancestors = {n['generation'] for r in check.commands(rows, 'dup') for n in r['nodes']
                     if n['alive'] and n['creation_operation'] == r['operation'] and n.get('schema') in ('c', 'l')}
        assert any(r['old_generation'] in ancestors for r in check.commands(rows, 'allocation.address-reuse')), 'must actually reuse an ancestor address'
    elif name.startswith('lifetime/'):
        check.lifetime_evidence(name, rows)
    else:
        check.completed_replay(name, rows)
        assert check.commands(rows, 'allocation.address-reuse'), 'must actually reuse a node address'
        if name == 'retained-address-reuse':
            check.retained_reuse_evidence(rows)
        elif name.startswith('handle-tombstone'):
            assert rows[-1]['event'] == 'boundary.tombstoned-handle'
            assert any(r['old_generation'] == rows[-1]['generation'] for r in check.commands(rows, 'allocation.address-reuse'))


class ReuseControls(unittest.TestCase):
    def test_environment_matrix(self):
        count = 0
        for profile in PROFILES:
            for name in CASES:
                for verbose in (False, True):
                    traces = []
                    for label, driver in (('instrumented', DRIVER), ('unmodified', COMPARISON_DRIVER)):
                        with self.subTest(profile=profile, case=name, build=label, verbose=verbose):
                            result = execute(driver, name, verbose, profile)
                            self.assertEqual(result.returncode, 0, result.stderr + result.stdout[-2000:])
                            rows = [json.loads(line) for line in result.stdout.splitlines()]
                            with contextlib.redirect_stdout(io.StringIO()):
                                evidence(name, rows)
                            traces.append(rows)
                            count += 1
                    if len(traces) == 2:
                        self.assertEqual(validate(script_for(name), traces[0]), validate(script_for(name), traces[1]))
        self.assertEqual(count, 192)
        print(json.dumps({'reuse_environment_passes': count, 'cases': list(CASES), 'profiles': list(PROFILES)}), flush=True)

    def test_forcing_disabled(self):
        # Keep the recovered target allocated across the constructor instead of
        # releasing it. Real malloc/calloc still run; no address can equal this
        # held allocation. The constructor assertion, not a build/setup failure,
        # must reject every control, including the seven inherited controls.
        directory = ROOT / '.build/reuse-disabled'
        directory.mkdir(parents=True, exist_ok=True)
        original = (ROOT / 'driver.c').read_text()
        release = '    __real_free(reuse_reserved);'
        self.assertEqual(original.count(release), 1)
        source = directory / 'driver.c'
        source.write_text(original.replace(release, '    (void)reuse_reserved; /* forcing disabled: keep target allocated */'))
        binaries = []
        for plain in (False, True):
            binary = directory / ('driver-unmodified' if plain else 'driver')
            tree, config = ('source', 'config') if plain else ('instrumented', 'static')
            args = ['gcc', *(['-DNR_UNMODIFIED'] if plain else []), '-DNDEBUG', '-O3', '-Wall', '-Wextra',
                    '-Wpedantic', '-std=c11', '-Wl,--wrap=free', '-I' + str(ROOT),
                    '-I' + str(ROOT / '.build' / tree / 'src'),
                    '-I' + str(ROOT / '.build' / config / 'libyang'),
                    '-I' + str(ROOT / '.build' / config / 'compat'),
                    '-I' + str(ROOT / '.build' / tree / 'src/plugins_exts'),
                    '-o', str(binary), str(source), str(ROOT.parent / 'cjson/cJSON.c'),
                    str(ROOT / '.build' / ('libyang-unmodified.a' if plain else 'static/libyang.a')),
                    '-lpcre2-8', '-lm', '-lpthread', '-ldl']
            build = subprocess.run(args, capture_output=True, text=True, timeout=60, check=False)
            self.assertEqual(build.returncode, 0, build.stdout + build.stderr)
            binaries.append(binary)
        count = 0
        for profile in PROFILES:
            for name in CASES:
                for verbose in (False, True):
                    for driver in binaries:
                        with self.subTest(profile=profile, case=name, build=driver.name, verbose=verbose):
                            result = execute(driver, name, verbose, profile)
                            self.assertEqual(result.returncode, 100, result.stderr + result.stdout[-2000:])
                            self.assertEqual(result.stderr.strip(), FAILURE)
                            count += 1
        self.assertEqual(count, 192)
        print(json.dumps({'reuse_forcing_disabled_rejections': count}), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--driver', type=pathlib.Path, default=os.environ.get('YANG_NATIVE_REGISTRATION_DRIVER') or None)
    parser.add_argument('--comparison-driver', type=pathlib.Path, default=os.environ.get('YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER') or None)
    args, rest = parser.parse_known_args()
    if args.driver is None:
        parser.error('--driver or YANG_NATIVE_REGISTRATION_DRIVER is required')
    DRIVER = args.driver.resolve()
    COMPARISON_DRIVER = args.comparison_driver.resolve() if args.comparison_driver else DRIVER.with_name('driver-unmodified')
    unittest.main(argv=[__file__, *rest])
