# SPDX-License-Identifier: BSD-3-Clause
"""Closed JSON contracts for the test-only native driver (not libyang code)."""
import re
from functools import lru_cache


def record(value, required, optional=None):
    assert type(value) is dict, 'expected object'
    if optional:
        assert required.keys() <= value.keys() <= required.keys() | optional.keys(), 'invalid fields'
        contract = required | optional
    else:
        assert value.keys() == required.keys(), 'invalid fields'
        contract = required
    for key, kind in contract.items():
        if key not in value: continue
        item = value[key]
        if isinstance(kind, type):
            assert type(item) is kind, ('invalid type', key)
        else:
            kind(item)


def required_record(fields):
    # Compile trusted layout descriptions once; no trace data is retained.
    keys = fields.keys()
    scalars = tuple((k, t) for k, t in fields.items() if isinstance(t, type))
    nested = tuple((k, t) for k, t in fields.items() if not isinstance(t, type))
    def check(value):
        assert type(value) is dict and value.keys() == keys, 'invalid fields'
        for key, kind in scalars:
            assert type(value[key]) is kind, ('invalid type', key)
        for key, kind in nested:
            kind(value[key])
    return check


def array(kind):
    def check(value):
        assert type(value) is list, 'expected array'
        for item in value:
            if isinstance(kind, type):
                assert type(item) is kind, 'invalid array member'
            else:
                kind(item)
    return check


def nullable_string(value):
    assert value is None or type(value) is str, 'expected nullable string'


def opaque_bytes(value):
    nullable_string(value)
    assert value is None or re.fullmatch(r'(?:[0-9a-f]{2})*', value), 'invalid opaque byte string'


def opaque_name(value):
    assert type(value) is str and value, 'missing opaque name'
    opaque_bytes(value)


BUCKET_FIELDS = {'first': int, 'last': int}
SLOT_FIELDS = {'next': int, 'node': int, 'hash': int}
CHAIN_FIELDS = dict.fromkeys(('bucket', 'slot', 'ordinal', 'node', 'generation', 'occurrence', 'hash'), int) | {'alive': bool}
RB_FIELDS = dict.fromkeys(('owner', 'node', 'parent', 'left', 'right', 'color'), int)
PROVENANCE = dict.fromkeys(('generation', 'id', 'creation_operation', 'creation_ordinal', 'source', 'attempt'), int) | {'alive': bool}
UNAVAILABLE = PROVENANCE | {'current': str}
bucket = required_record(BUCKET_FIELDS)
slot = required_record(SLOT_FIELDS)
chain = required_record(CHAIN_FIELDS)
rb_member = required_record(RB_FIELDS)
unavailable = required_record(UNAVAILABLE)
ABSENT_TABLE = {'present': bool}
RETIRED_TABLE = {'present': bool, 'identity': int, 'retired': bool}
ACTIVE_TABLE = {'present': bool, 'identity': int, 'continuity': str, 'size': int, 'used': int, 'mode': int, 'free': int,
    'chains': array(chain),
    'buckets': array(bucket),
    'slots': array(slot), 'free_chain': array(int), 'free_terminal': int}

absent_table = required_record(ABSENT_TABLE)
retired_table = required_record(RETIRED_TABLE)
active_table = required_record(ACTIVE_TABLE)


def table(value):
    assert type(value) is dict and type(value.get('present')) is bool, 'missing table presence'
    if not value['present']: absent_table(value)
    elif value.get('retired'): retired_table(value)
    else: active_table(value)


@lru_cache(maxsize=128)
def node_fields(read_context, module, schema, nodetype, sorted_metadata):
    # Cache only immutable layout descriptions, never trace data or identities.
    fields = PROVENANCE | dict.fromkeys(('context', 'hash', 'flags', 'prev', 'next', 'parent'), int)
    fields['metadata'] = array(str)
    if read_context: fields['context_generation'] = int
    if schema:
        fields |= {'nodetype': int, 'key': bool, 'schema': str, 'schema_identity': int}
        if module: fields['module'] = str
        if nodetype & 0x000c: fields['value'] = str
        if nodetype & 0x0711: fields |= {'first': int, 'tail': int, 'table': table}
    else:
        fields |= {'first': int, 'tail': int, 'prefix_format': int, 'prefix_bound': bool,
                   'opaque_name': opaque_name, 'opaque_module': opaque_bytes, 'opaque_prefix': opaque_bytes, 'opaque_value': opaque_bytes}
    if sorted_metadata:
        fields |= {'rb_stored_root': int, 'rb_root': int, 'rb': array(rb_member)}
    return required_record(fields)


def node(value, read_context, module):
    assert type(value) is dict, 'expected node object'
    if not value.get('alive') or 'current' in value:
        unavailable(value)
        assert value['current'] in ('unavailable', 'unsafe-native-links'), 'invalid availability'
        assert value['alive'] or value['current'] == 'unavailable', 'invalid tombstone'
        return
    fields = node_fields(read_context, module, 'schema' in value, value.get('nodetype', 0), 'lyds_tree' in value.get('metadata', []))
    fields(value)


def diagnostic(value):
    record(value, {'code': int, 'context_generation': int}, dict.fromkeys(('msg', 'path', 'apptag'), str))
    assert 0 < value['context_generation'] < 16, 'invalid diagnostic context'


def log(value):
    record(value, {'level': int, 'line': int}, dict.fromkeys(('msg', 'path', 'schema_path'), str))


def module(value):
    record(value, {'name': str, 'implemented': bool, 'latest': int}, {'namespace': str, 'revision': str})
    assert value['name'], 'empty module name'


RESULTS = {
    'read.equal': dict.fromkeys(('rc', 'left_generation', 'right_generation', 'options'), int),
    'read.schema': dict.fromkeys(('rc', 'queried_parent', 'target', 'schema_hash', 'winner', 'current_parent'), int) | {'before': table, 'after': table},
    'read.instance': dict.fromkeys(('rc', 'supplied', 'winner', 'current_parent', 'target'), int) | {'before': table, 'after': table},
    'read.opaque': dict.fromkeys(('rc', 'supplied', 'winner', 'current_parent'), int) | {'name': opaque_name, 'before': table, 'after': table},
    'read.null': dict.fromkeys(('queried_parent', 'target', 'schema_rc', 'schema_winner', 'instance_rc', 'instance_winner', 'value_rc', 'value_winner'), int) | {'before': table, 'after': table},
    'read.first-sibling': {'winner': int},
    'read.compare': dict.fromkeys(('left', 'right', 'options'), int),
    'public.result': {'rc': int},
}
for kind in ('schema', 'instance', 'value', 'opaque'):
    RESULTS['read.selection.' + kind] = dict.fromkeys(('rc', 'start_generation', 'target_generation', 'queried_parent', 'winner', 'winner_script', 'current_parent', 'parent_script', 'next_generation', 'next_script'), int) | {'before': table, 'after': table}
RESULTS['read.selection.value']['value'] = opaque_bytes
RESULTS['read.selection.opaque']['name'] = opaque_name

COMMANDS = frozenset(('new', 'root', 'term', 'list', 'frontier', 'opaque', 'opaque-invalid-xml',
    'dup', 'dup-to-context', 'unlink', 'attach', 'before', 'detach-siblings', 'change', 'parse',
    'parse-xml', 'context', 'context-keyless', 'child', 'record', 'record-as', 'insert', 'remove',
    'I', 'U', 'free', 'free-root', 'validate', 'check-handle', 'dump', 'find-next-loop', 'schema',
    'instance', 'opaq-find', 'null-reads', 'read-schema', 'read-instance', 'read-value',
    'read-opaque', 'equal', 'rewind', 'print'))
BOUNDARIES = frozenset('boundary.' + x for x in ('tombstoned-handle', 'tombstoned-semantic-probe',
    'new-null', 'retained-dangling-links', 'primitive-free-slot-cap', 'watchdog.find-next-cycle',
    'remote-last-corrupting-primitive', 'merge2-before-uninitialized-continuation',
    'merge2-exhausted-forward-iterator', 'merge2-successor-schema-precondition'))


def shape(row, snapshot, read_context):
    event = row['event']
    common = {'event': str, 'operation': int, 'request': array(opaque_name)}
    if event == 'allocation.address-reuse':
        record(row, common | {'old_generation': int, 'new_generation': int})
    elif event == 'boundary.tombstoned-handle':
        record(row, common | {'handle': int, 'generation': int, 'current': str})
        assert row['current'] == 'unavailable', 'invalid tombstone availability'
    elif event in RESULTS:
        fields = common | RESULTS[event]
        if event == 'public.result' and row.get('rc') == 0:
            fields = fields | {'json': str}
        record(row, fields, {'json': str} if event == 'public.result' and row.get('rc') != 0 else {})
    else:
        assert event in SNAPSHOTS, 'unknown snapshot kind'
        # Native event snapshots use exactly the same contract as completed commands.
        module_evidence = read_context or any(type(x) is dict and 'module' in x for x in row.get('nodes', []))
        required = common | dict.fromkeys(('rc', 'node_generation', 'source_generation', 'output_handle', 'output_generation', 'node_count', 'table_count'), int)
        required |= {'allocation_scope': str, 'nodes': array(lambda n: node(n, read_context, module_evidence)),
                     'bindings': array(lambda b: record(b, dict.fromkeys(('handle', 'generation', 'revision', 'operation'), int))),
                     'schemas': array(lambda s: record(s, dict.fromkeys(('identity', 'context', 'parent', 'nodetype'), int) | {'module': str, 'name': str})),
                     'tables': array(lambda t: record(t, {'identity': int, 'alive': bool}, {'storage': table})),
                     'diagnostics': array(diagnostic), 'logs': array(log)}
        optional = {'table': table, 'working_rb_root': int}
        if read_context: required['modules'] = array(module)
        else: optional['modules'] = array(module)
        record(row, required, optional)
        if read_context:
            assert row['modules'] and len({(m['name'], m.get('revision')) for m in row['modules']}) == len(row['modules']), 'incomplete modules'
    if snapshot:
        assert event not in RESULTS, 'result masquerading as snapshot'


LOCATION = re.compile(r'^(Internal error) \([^()]*\.(?:c|h):[0-9]+\)(\.)$')


# Only wrap() in instrument.py adds this suffix to __func__ in LOGARG.
OBSERVER_FUNCTIONS = ('lyd_insert_hash', 'lyd_unlink_hash', 'lyd_insert_hash_add', 'lyht_resize', '_lyht_insert_with_resize_cb', 'lyht_remove_with_resize_cb', 'lyht_find_next_with_collision_cb', 'lyd_insert_after_node', 'lyd_insert_before_node', 'lyd_unlink_ignore_lyds', 'lyd_insert_only_child', 'lyd_insert_node_last', 'lyds_move_meta', 'lyds_merge_nodes2_front', 'lyds_merge_nodes2_among', 'lyds_merge_nodes2_back', 'lyd_validate_duplicates')
OBSERVER_FUNCTION = re.compile(r'^(Invalid argument .+ \()(' + '|'.join(OBSERVER_FUNCTIONS) + r')_nr_impl(\(\)\)\.)$')
SNAPSHOTS = COMMANDS | (BOUNDARIES - {'boundary.tombstoned-handle'}) | frozenset(
    name + phase for name in OBSERVER_FUNCTIONS for phase in ('.before', '.after')) | frozenset((
    'driver.ready.actual-children-callback', 'table.allocate', 'table.retire', 'node.retiring', 'node.free',
    'dup.table.initialized', 'prefix.copy.before', 'prefix.copy.after', 'prefix.replace.before',
    'prefix.replace.after', 'merge2.back.no-successor', 'merge2.among.exhausted-forward',
    'merge2.among.successor-schema', 'remote.nonlast.before', 'remote.last.before'))


def message(value):
    return OBSERVER_FUNCTION.sub(r'\1\2\3', LOCATION.sub(r'\1\2', value))


def messages(items):
    return [{k: message(v) if k == 'msg' else v for k, v in item.items()} for item in items]
