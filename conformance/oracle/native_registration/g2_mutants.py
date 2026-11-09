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
(work / 'protocol.py').write_text((aux / 'protocol.py').read_text())
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
    assert "no tests to run" not in result.stdout, (name, result.stdout)
    if name in ('ignored-ENOT-code', 'ignored-command-status', 'ignored-required-message', 'missing-required-snapshot'):
        # The complete protocol projection now independently protects this removal.
        # G1's shared projection now retains the exact equality code too.
        assert result.returncode == 0, result.stdout
        print(json.dumps({'mutant':name, 'protected_by_shared_validation':True}), flush=True)
        return
    assert result.returncode and "--- FAIL:" in result.stdout and not any(
        failure in result.stdout for failure in ('[build failed]', 'ModuleNotFoundError', 'ImportError',
                                                 'SyntaxError', 'FileNotFoundError', 'NameError')), (name, result.stdout)
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
    ('ignored-current-hash', ", n['hash'],", ', 0,', '^TestNativeG2CurrentHashEvidence$'),
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


# Audit-5 contract mutants. Each disables one complete protection family; the
# unmodified positive controls must still run and the named regression must fail.
identity = (aux / 'identity.py').read_text()
shape_line = "        shape(row, command or event == 'driver.ready.actual-children-callback', read_context_evidence)"
selection_begin = identity.index("        if event.startswith('read.selection.'):")
selection_end = identity.index("        if 'nodes' not in row:", selection_begin)
selection_unchecked = identity[:selection_begin] + identity[selection_end:]
selection_unchecked = selection_unchecked.replace(shape_line, "        if not event.startswith('read.selection.'):\n    " + shape_line)
mutants = (
    ('unchecked-selection-identities', selection_unchecked, '^TestNativeG2SelectionContract/(identity|occurrence)/delete$'),
    ('payload-dispatched-equality', identity.replace(shape_line, "        if event != 'read.equal':\n    " + shape_line), '^TestNativeG2EqualityResultContract/add-nodes$'),
    ('dropped-opaque-state', identity.replace("n.get('opaque_name'), n.get('opaque_module'), n.get('opaque_prefix'), n.get('opaque_value')", 'None, None, None, None'), '^TestNativeG2OpaqueOperandContract$'),
    ('dropped-context-metadata', identity.replace("row.get('modules')", 'None').replace("n['metadata'],", '[],') , '^TestNativeG2ContextMetadataContract/.*/.*/mutate$'),
    ('dropped-bootstrap-channels', identity.replace("(row['event'], row['rc'], row.get('modules'),", "(row['event'], row['rc'] if operation else 0, row.get('modules'),").replace("messages(row['diagnostics']), messages(row.get('logs', [])),", "messages(row['diagnostics']) if operation else [], messages(row.get('logs', [])) if operation else [],"), '^TestNativeG2BootstrapChannelsContract/.*/mutate$'),
)
for name, replaced, test in mutants:
    assert replaced != identity, name
    path = work / (name + '.py')
    path.write_text(replaced)
    go_mutant(name, completion,
              'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
              json.dumps(str(path)), test)

# The generic field sweep must catch an omission outside the five named fields.
# Keep its live requests rooted in the real case directory; only the copied
# predicate drops a chain bucket. A missing fixture/import cannot be sensitivity.
field_control = work / 'field-control'
field_control.mkdir(exist_ok=True)
(field_control / 'protocol.py').write_text((aux / 'protocol.py').read_text())
(field_control / 'identity.py').write_text(identity.replace("if k != 'occurrence'", "if k not in ('occurrence', 'bucket')"))
(field_control / 'evidence_test.py').write_text((aux / 'evidence_test.py').read_text().replace(
    'ROOT = pathlib.Path(__file__).resolve().parent', 'ROOT = pathlib.Path(' + repr(str(aux)) + ')'))
result = run(['python3', str(field_control / 'evidence_test.py')])
(work / 'uncompared-bucket.log').write_text(result.stdout)
assert result.returncode and "uncompared field" in result.stdout and "'bucket'" in result.stdout, result.stdout
print(json.dumps({'mutant': 'uncompared-bucket', 'rejected': True}), flush=True)

# Native opaque names may contain bytes outside UTF-8; neither the observer nor
# the Go transport may silently convert them into equal replacement text.
go_mutant('invalid-utf8-transport', completion, 'if !utf8.Valid(b) {',
          'if false && !utf8.Valid(b) {', '^TestNativeG2EvidenceUTF8$')
native_driver_mutant('raw-opaque-byte-name',
    'bytes_or_null(j,"opaque_name",o->name.name);',
    'if(!cJSON_AddStringToObject(j,"opaque_name",o->name.name))exit(91);',
    '^TestNativeG2OpaqueByteNames$')

# Replacing current storage with fresh, internally consistent registry IDs must
# fail even when its normalized contents match and both peers are corrupted.
path = work / 'replaced-completed-read-storage.py'
path.write_text(identity.replace('if command and event in STABLE_STORAGE_COMMANDS:',
                                 'if False and command and event in STABLE_STORAGE_COMMANDS:'))
go_mutant('replaced-completed-read-storage', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2ReadStorageContinuity$')

# Pin the measured observer-only exclusions independently in both consumers.
path = work / 'schema-coverage-history.py'
path.write_text(identity.replace("sorted({schema_keys[n['schema_identity']] for n in nodes.values() if n['alive'] and 'schema_identity' in n}, key=repr)",
                                 'sorted(schema_keys.values(), key=repr)'))
go_mutant('schema-coverage-history', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2TransientSchemaRegistry$')
name_control = work / 'function-name-control'
name_control.mkdir(exist_ok=True)
(name_control / 'identity.py').write_text(identity)
(name_control / 'protocol.py').write_text((aux / 'protocol.py').read_text().replace(
    "return OBSERVER_FUNCTION.sub(r'\\1\\2\\3', LOCATION.sub(r'\\1\\2', value))",
    "return LOCATION.sub(r'\\1\\2', value)"))
go_mutant('python-observer-function-name', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(name_control / 'identity.py')), '^TestNativeG2ObserverFunctionAnnotation$')
go_mutant('go-observer-function-name', evidence,
          'v = nativeG2ObserverFunction.ReplaceAllString(v.(string), "$1$2$3")',
          'v = fmt.Sprint(v)', '^TestNativeG2ObserverFunctionAnnotation$')
go_mutant('observer-status-obligation', evidence,
          'if raw == nil {', 'if raw == nil || raw["rc"] == nil {', '^TestNativeG2ObserverWithoutStatus$')

# Round-2 same-peer defects: keep the new wire format and positive native
# controls, but remove the complete request-binding / outcome requirement.
request_check = "        assert row['request'] == expected_requests[operation], 'wrong request arguments'\n"
operand_begin = identity.index("            for field, handle in (('start_generation', start), ('target_generation', target)):")
operand_end = identity.index("            expected = last_nodes[owner]", operand_begin)
unbound = identity[:operand_begin] + identity[operand_end:]
unbound = unbound.replace(request_check, '').replace(
    "            elif not start:\n                assert (row['rc'], winner) == (5, 0), 'null sibling must return ENOTFOUND'\n", '')
path = work / 'unbound-request.py'
path.write_text(unbound)
go_mutant('unbound-request', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2SubstitutedNullStart$')

payload_control = work / 'optional-success-payload'
payload_control.mkdir(exist_ok=True)
(payload_control / 'identity.py').write_text(identity)
(payload_control / 'protocol.py').write_text((aux / 'protocol.py').read_text().replace(
    "if event == 'public.result' and row.get('rc') == 0:", "if False and event == 'public.result':").replace(
    "if event == 'public.result' and row.get('rc') != 0 else {}", "if event == 'public.result' else {}"))
go_mutant('optional-success-payload', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(payload_control / 'identity.py')), '^TestNativeG2RequiredSuccessPayload/both$')

path = work / 'failed-read-parent.py'
path.write_text(identity.replace("                assert winner or parent == 0, 'parent without winner'\n", ''))
go_mutant('failed-read-parent', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2FailedResultLinks/read.instance$')

path = work / 'unrequested-tombstone.py'
tombstone_begin = identity.index('                requested = input_handles(req[operation - 1])')
tombstone_end = identity.index('                assert g in last_nodes', tombstone_begin)
path.write_text(identity[:tombstone_begin] + identity[tombstone_end:])
go_mutant('unrequested-tombstone', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2TombstoneRequestBinding$')

path = work / 'nullable-mandatory-input.py'
null_guard = "        assert handle or position in NULLABLE_INPUTS.get(command, ()), 'NULL mandatory input'\n"
assert null_guard in identity
path.write_text(identity.replace(null_guard, ''))
go_mutant('nullable-mandatory-input', completion,
          'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
          json.dumps(str(path)), '^TestNativeG2MandatoryInputs$')

schema_begin = identity.index("            if fields[0] in ('read-schema', 'read-value') and 'schema' not in last_nodes[row['target_generation']]:")
schema_end = identity.index("            assert row['rc'] != 0 or winner", schema_begin)
schema_unchecked = identity[:schema_begin] + "            if not start:\n                assert (row['rc'], winner) == (5, 0), 'null sibling must return ENOTFOUND'\n" + identity[schema_end:]
for name, changed, test in (
    ('occupied-output-slot', identity.replace("                assert not old or not old['generation'] or not completed_nodes[old['generation']]['alive'], 'occupied output handle'\n", ''), '^TestNativeG2OutputSlotBindings$'),
    ('unrequested-new-binding', identity.replace("        assert bs.keys() - completed_bindings.keys() <= allowed, 'unrequested new binding'\n", ''), '^TestNativeG2UnrequestedBindings$'),
    ('NULL-start-before-schema', schema_unchecked, '^TestNativeG2NullSchemaPreconditions$'),
):
    assert changed != identity, name
    path = work / (name + '.py')
    path.write_text(changed)
    go_mutant(name, completion,
              'filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py")',
              json.dumps(str(path)), test)

# The generic inventory must independently reject the identical-payload defect,
# even when no named Go regression is selected.
for name, directory in (('request-substitution-control', work / 'request-control'),
                        ('success-payload-control', payload_control),
                        ('mandatory-NULL-control', work / 'mandatory-null-control')):
    directory.mkdir(exist_ok=True)
    if name == 'request-substitution-control':
        (directory / 'identity.py').write_text(unbound)
        (directory / 'protocol.py').write_text((aux / 'protocol.py').read_text())
    if name == 'mandatory-NULL-control':
        (directory / 'identity.py').write_text(identity.replace(null_guard, ''))
        (directory / 'protocol.py').write_text((aux / 'protocol.py').read_text())
    (directory / 'evidence_test.py').write_text((aux / 'evidence_test.py').read_text().replace(
        'ROOT = pathlib.Path(__file__).resolve().parent', 'ROOT = pathlib.Path(' + repr(str(aux)) + ')'))
    # Start with the small named live request instead of rerunning the full
    # field inventory merely to establish that the generic control is reached.
    control = '''
import evidence_test as e
s = (e.ROOT / 'cases/read/read-success-payload.calls').read_text()
left, right = e.replay(s), e.replay(s, plain=True)
e.request_substitutions(s, left, right)
e.sweep(s, left)
'''
    result = subprocess.run(
        ['python3', '-c', control], cwd=directory, text=True, stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, timeout=180, check=False)
    (work / (name + '.log')).write_text(result.stdout)
    expected = {'request-substitution-control': 'accepted substituted request',
                'success-payload-control': 'accepted identical contract defect',
                'mandatory-NULL-control': 'accepted mandatory NULL request'}[name]
    assert result.returncode and expected in result.stdout, (name, result.stdout)
    print(json.dumps({'mutant': name, 'rejected': True}), flush=True)
