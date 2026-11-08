# SPDX-License-Identifier: BSD-3-Clause
"""G2 audit sensitivity controls. Run inside the normal dev container.

Every Go mutant uses a temporary compiler overlay, never a tracked edit.
Native mutants change only observer context collection/cleanup. A compile
error cannot satisfy a regression's sensitivity check.
"""
import json
import re
import os
import pathlib
import shlex
import subprocess

root = pathlib.Path(__file__).resolve().parents[3]
aux = root / "conformance/oracle/native_registration"
work = aux / ".build/g2-mutants"
work.mkdir(parents=True, exist_ok=True)
os.environ["YANG_NATIVE_REGISTRATION_DRIVER"] = str(aux / ".build/driver")
os.environ["YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER"] = str(aux / ".build/driver-unmodified")


def run(args, **kwargs):
    return subprocess.run(args, cwd=root, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=180, check=False, **kwargs)


def go_mutant(name, file, before, after, test):
    source = root / file
    text = source.read_text()
    assert before in text, name
    replacement = work / (name + ".go")
    if name == 'ignored-ENOT-code':
        body = re.search(r'func nativeG2Evidence\(.*?\n}\n', text, re.S).group()
        replacement.write_text(text.replace(body, body.replace(before, after)))
    else:
        replacement.write_text(text.replace(before, after))
    overlay = work / (name + ".json")
    overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
    result = run(["go", "test", "-tags", "oracle", "-overlay", str(overlay),
                  "./data", "-run", test, "-count=1"])
    (work / (name + ".log")).write_text(result.stdout)
    if name == 'ignored-ENOT-code':
        # G1's shared projection now retains the exact equality code too.
        assert result.returncode == 0, result.stdout
        print(json.dumps({'mutant':name, 'protected_by_shared_validation':True}), flush=True)
        return
    assert result.returncode and "--- FAIL:" in result.stdout and "[build failed]" not in result.stdout, (name, result.stdout)
    print(json.dumps({"mutant": name, "rejected": True}), flush=True)


completion = "data/native_table_oracle_test.go"
evidence = "data/native_read_evidence_test.go"
flags = aux / ".build/static/CMakeFiles/yang.dir/flags.make"
config = dict(line.split(' = ', 1) for line in flags.read_text().splitlines() if line.startswith('C_'))
driver_source = aux / 'driver.c'
go_mutant("missing-read-completion", completion, '"read-instance": "read.selection.instance",', '',
          '^TestNativeG2ReadCompletion/read-instance$')
go_mutant("ignored-ENOT-code", evidence, 'row.RC', '(row.RC != nativeOK)',
          '^TestNativeG2ExactErrorEvidence/ENOT-to-EINT$')
go_mutant("ignored-required-message", evidence,
          'v = nativeG2Location.ReplaceAllString(v.(string), "$1$2")', 'v = ""',
          '^TestNativeG2ExactErrorEvidence/required-message$')

go_mutant("ignored-command-status", evidence,
          'row.Operation, row.Event, row.RC, normalizeMessages',
          'row.Operation, row.Event, nativeOK, normalizeMessages',
          '^TestNativeCompletedCommandProjection/rc$')

# Self-contained recreation of the former equality shortcut: preserve completed
# requests and equality codes, but accept them without resolving operand state.
# This needs no historical branch and must fail a real rejecting regression.
body = re.search(r'func nativeG2Evidence\(.*?\n}\n', (root / evidence).read_text(), re.S).group()
shortcut = body[:body.index('\n')+1] + '''
    if err := nativeCompletedReplay(script, rows, ""); err != nil { return nil, err }
    var out []any
    for _, row := range rows { if row.Event == "read.equal" { out = append(out, row.RC) } }
    return out, nil
}
'''
go_mutant('empty-equality-registry', evidence, body, shortcut,
          '^TestNativeG2EqualityIdentityEvidence/(empty-registry|left|right|missing-left-generation|missing-right-generation|wrong-binding)$')

# Recreate each audit-3 defect in the actual Python projection reached by Go.
identity = (aux / 'identity.py').read_text()
for name, before, after, test in (
    ('missing-required-snapshot', "        if command or event == 'driver.ready.actual-children-callback':\n            assert isinstance(row.get('nodes'), list), 'missing required snapshot nodes'\n", '',
     '^TestNativeG2RequiredSnapshots/dump/nodes/both$'),
    ('ignored-current-hash', ", n['hash'])", ')', '^TestNativeG2CurrentHashEvidence$'),
):
    assert before in identity, name
    path = work / (name + '.py')
    path.write_text(identity.replace(before, after))
    go_mutant(name, completion,
              'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
              json.dumps(str(path)), test)

# Removing both completion checks recreates the former completion bypass.
# The transport controls call nativeG2Unmodified itself, so testing a standalone
# comparator cannot accidentally mask this bypass.
replacements = {}
for relative, old, new in (
    (completion, 'if err := nativeCompletedReplay(script, results, terminal); err != nil {',
     'if err := error(nil); err != nil {'),
    (evidence, 'if err := nativeCompletedReplay(script, rows, ""); err != nil {',
     'if err := error(nil); err != nil {'),
):
    original = root / relative
    text = original.read_text()
    assert old in text
    replaced = work / (original.stem + '-unchecked.go')
    replaced.write_text(text.replace(old,new))
    replacements[str(original)] = str(replaced)
overlay = work / 'unchecked-control.json'
overlay.write_text(json.dumps({'Replace':replacements}))
result = run(['go','test','-tags','oracle','-overlay',str(overlay),'./data','-run',
              '^TestNativeG2UnmodifiedReplayControls/(results-only|truncated|missing-result)$','-count=1'])
(work / 'unchecked-control.log').write_text(result.stdout)
# The common validator and snapshot checks independently reject these partial
# traces. Removing only completion checks no longer creates an acceptance bug.
assert result.returncode == 0, result.stdout
print(json.dumps({'mutant':'unchecked-control','protected_by_shared_validation':True}),flush=True)


def native_driver_mutant(name, before, after, test):
    text = driver_source.read_text()
    assert before in text, name
    source = work / (name + '.c')
    source.write_text(text.replace(before, after))
    binary = work / name
    result = run(['gcc', '-DNDEBUG', '-O3', '-std=c11', '-Wl,--wrap=free', *shlex.split(config['C_INCLUDES']),
                  '-I'+str(aux), '-o', str(binary), str(source), str(aux.parent / 'cjson/cJSON.c'),
                  str(aux / '.build/static/libyang.a'), '-lpcre2-8', '-lm', '-lpthread', '-ldl'])
    assert result.returncode == 0, result.stdout
    env = dict(os.environ, YANG_NATIVE_REGISTRATION_DRIVER=str(binary))
    result = run(['go', 'test', '-tags', 'oracle', './data', '-run', test, '-count=1'], env=env)
    (work / (name+'.log')).write_text(result.stdout)
    assert result.returncode and '--- FAIL:' in result.stdout and '[build failed]' not in result.stdout, (name, result.stdout)
    print(json.dumps({'mutant':name, 'rejected':True}), flush=True)


native_driver_mutant('current-context-diagnostics',
    'for(unsigned ci=1;ci<=read_context_count;++ci)for(const struct ly_err_item *e=ly_err_first(read_contexts[ci]);e;e=e->next)',
    'for(unsigned ci=context_identity(ctx);ci==context_identity(ctx);++ci)for(const struct ly_err_item *e=ly_err_first(ctx);e;e=e->next)',
    '^TestNativeG2RetainedContextDiagnostics$')
native_driver_mutant('current-context-cleanup',
    'for(unsigned i=1;i<=read_context_count;++i)ly_err_clean((struct ly_ctx*)read_contexts[i],NULL);',
    'ly_err_clean(ctx,NULL);', '^TestNativeG2RetainedContextDiagnostics$')
