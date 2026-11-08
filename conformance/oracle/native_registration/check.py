# SPDX-License-Identifier: BSD-3-Clause
"""Replay local test-only scripts; retain complete JSONL separately from summaries."""
import argparse
import copy
from collections import Counter
import json
import os
import pathlib
import subprocess

from identity import validate

root = pathlib.Path(__file__).resolve().parent
LYS_LIST = 0x0010  # libyang v5.8.6 src/tree_schema.h
ENTRY_CASES = ("retained-null", "retained-opaque", "entry-guard", "entry-order", "entry-opaque",
               "entry-duplicate", "entry-schema-lineage")


def run(name, option="--quiet", unmodified=False):
    script = (root / "cases" / (name + ".calls")).read_bytes()
    options = [option] + (["--event-schema"] if name.startswith("entry-") or name in ("list-events", "schema-pointer", "schema-pointer-reverse", "lifetime/list-key", "lifetime/validation-delete") else [])
    env = dict(os.environ)
    if b"# reuse-" in script or name in ("handle-tombstone", "handle-tombstone-probe", "retained-address-reuse", "lifetime/stale-handle",
                "lifetime/retained-same-hash", "lifetime/retained-resize", "lifetime/retained-remove"):
        env["GLIBC_TUNABLES"] = "glibc.malloc.tcache_count=7"
    result = subprocess.run([str(plain if unmodified else binary), *options], input=script,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25, check=False, env=env)
    suffix = (".events.unmodified" if option == "--events" else ".unmodified") if unmodified else (".first-sibling-control" if option == "--remote-watchdog" and name == "remote-last" else "")
    output = root / ".build" / (name + suffix + ".jsonl")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(result.stdout)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    return result.returncode, rows


def commands(rows, command):
    return [r for r in rows if r.get("event") == command]


def retained_reuse_evidence(rows):
    name = "retained-address-reuse"
    normalized_reads(name, rows, project=False)
    completed_replay(name, rows)
    reuse = commands(rows, "allocation.address-reuse")
    assert reuse, "missing retained address reuse"
    chains = rows[-1]["table"]["chains"]
    assert any(x["generation"] == reuse[-1]["old_generation"] and not x["alive"] for x in chains)
    assert any(x["generation"] == reuse[-1]["new_generation"] and x["alive"] for x in chains)
    original = commands(rows, "insert")[0]["table"]["chains"][0]
    retained = [x for x in chains if x["generation"] == original["generation"]]
    assert len(retained) == 1 and retained[0]["occurrence"] == original["occurrence"]
    assert len({x["occurrence"] for x in chains}) == 2


def final_public(rows):
    return [r for r in rows if r.get("event") == "public.result"]


def normalized_reads(name, rows, *, project=True):
    return validate((root / "cases" / (name + ".calls")).read_text(), rows, project=project)


def lookup_results(rows, name, evidence=None):
    if evidence is None: evidence = normalized_reads(name, rows, project=False)
    return [value for (op, event), value in evidence.items() if event == "read.schema"]


def assert_lookup_handles(rows, expected):
    reads = commands(rows, "read.schema")
    assert len(reads) == len(expected), "missing lookup evidence"
    for read, (winner, parent) in zip(reads, expected, strict=True):
        snapshot = next(r for r in rows if r["event"] == "schema" and r["operation"] == read["operation"])
        handles = by_handle(snapshot)
        assert (read["winner"], read["current_parent"]) == (handles[winner]["generation"] if winner else 0, handles[parent]["generation"] if parent else 0)


# Expected stops are request facts, never inferred from the observed final row.
STOPS = {
    "lifetime/retained-links": (16, "boundary.retained-dangling-links"),
    "remote-last": (14, "boundary.remote-last-corrupting-primitive"),
    "merge2-before": (11, "boundary.merge2-before-uninitialized-continuation"),
    "merge2-successor": (13, "boundary.merge2-successor-schema-precondition"),
    "merge2-unsorted": (12, "boundary.merge2-exhausted-forward-iterator"),
    "handle-tombstone": (15, "boundary.tombstoned-handle"),
    "handle-tombstone-probe": (15, "boundary.tombstoned-handle"),
    "dead-occurrence": (5, "boundary.tombstoned-semantic-probe"),
    "find-next-watchdog": (4, "boundary.watchdog.find-next-cycle"),
    "free-slot-cap": (13, "boundary.primitive-free-slot-cap"),
    "invalid-capacity": (1, "boundary.new-null"),
    "invalid-mode": (1, "boundary.new-null"),
    "lifetime/stale-handle": (16, "boundary.tombstoned-handle"),
    "lifetime/retained-same-hash": (8, "boundary.tombstoned-semantic-probe"),
    "lifetime/retained-resize": (56, "boundary.tombstoned-semantic-probe"),
}


def request_commands(name):
    return [line.split()[0] for line in (root / "cases" / (name + ".calls")).read_text().splitlines()
            if line.strip() and not line.lstrip().startswith("#")]


def completed_replay(name, rows, stopped=True):
    script = request_commands(name)
    stop = STOPS.get(name) if stopped else None
    end = stop[0] if stop else len(script) + 1
    expected = list(enumerate(script[:end - 1], 1))
    script_commands = set(script)
    actual = [(r["operation"], r["event"]) for r in rows if r["event"] in script_commands]
    assert actual == expected, (name, "incomplete/out-of-order completed commands", actual, expected)
    terminal = stop or (len(script), script[-1])
    assert rows and (rows[-1].get("operation"), rows[-1]["event"]) == terminal, (name, "unexpected terminal", terminal)
    # An observational remote-link boundary may occur mid-replay, but an early
    # stop must never be interpreted as permission to omit subsequent commands.
    reads = {"schema": "read.schema", "rewind": "read.first-sibling", "print": "public.result",
             "instance": "read.instance", "opaq-find": "read.opaque", "null-reads": "read.null",
             "read-schema": "read.selection.schema", "read-instance": "read.selection.instance", "read-value": "read.selection.value", "read-opaque": "read.selection.opaque", "equal": "read.equal"}
    expected_results = [(operation, reads[command]) for operation, command in expected if command in reads]
    actual_results = [(r["operation"], r["event"]) for r in rows if r["event"] in reads.values()]
    assert actual_results == expected_results, (name, "missing/extra/out-of-order read/public result")
    # LOGINT text embeds build-specific C paths/line numbers. Compare command
    # status and diagnostic identity here; validation retains full diagnostics.
    return [(r["operation"], r["event"], r["rc"],
             [(d["code"], d.get("path"), d.get("apptag")) for d in r["diagnostics"]])
            for r in rows if r["event"] in script_commands]


def compared_results(name, rows, evidence=None):
    results = [r for r in rows if r['event'] in ('public.result', 'validate', 'read.schema', 'read.first-sibling', 'read.instance', 'read.opaque', 'read.null')]
    if name in ("invalid-capacity", "invalid-mode"):
        # Rejected constructors have no completed command or public read. Their
        # explicit native rejection and nonempty log are the comparison result.
        terminal = rows[-1]
        assert terminal["rc"] == 0 and terminal.get("logs"), (name, "missing rejection log")
        return [(terminal["event"], terminal["rc"], terminal["diagnostics"], terminal["logs"])]
    assert results, (name, "empty compared results")
    if evidence is None: evidence = normalized_reads(name, rows, project=False)
    return (final_public(rows),
            [(r["operation"], r["rc"], r["diagnostics"]) for r in commands(rows, "validate")],
            lookup_results(rows, name, evidence),
            [value for (op, event), value in evidence.items() if event in ("read.first-sibling", "read.instance", "read.opaque", "read.null")])


def compare_control(name, rc, rows, plain_rc, plain_rows, stopped=True):
    expected_rc = 5 if name in ("invalid-capacity", "invalid-mode") else 0
    assert rc == plain_rc == expected_rc, (name, "control exit status")
    # Reject incomplete schedules before walking their retained history. Reuse
    # the two complete projections within this call; never cache mutable traces.
    assert completed_replay(name, rows, stopped) == completed_replay(name, plain_rows, stopped), (name, "completed command results changed")
    left, right = normalized_reads(name, rows), normalized_reads(name, plain_rows)
    assert left == right, (name, "control identity evidence changed")
    entry_comparisons(name, rows)
    assert compared_results(name, rows, left) == compared_results(name, plain_rows, right), (name, "control results changed")


def control_equal(name, rc, rows, stopped=True):
    plain_rc, plain_rows = run(name, unmodified=True)
    compare_control(name, rc, rows, plain_rc, plain_rows, stopped)
    # Mutate each side independently AND both together: equal truncations and
    # equal empty sets were the original false-positive class.
    def rejected(label, left, right):
        try:
            compare_control(name, rc, left, plain_rc, right, stopped)
        except (AssertionError, KeyError, IndexError):
            print(json.dumps({"negative_evidence": label, "case": name, "rejected": True}))
        else:
            raise AssertionError((name, "control accepted mutant", label))
    for side in ("instrumented", "unmodified", "both"):
        def mutate(label, transform):
            rejected(label + "-" + side, transform(rows) if side != "unmodified" else rows,
                     transform(plain_rows) if side != "instrumented" else plain_rows)
        mutate("truncated-control", lambda trace: trace[:-1])
        mutate("early-stop-control", lambda trace: trace[:-1] + [dict(trace[-1], event="boundary.tombstoned-semantic-probe", operation=trace[-1]["operation"] - 1)])
        script = set(request_commands(name))
        def missing_command(trace):
            first = next((i for i, r in enumerate(trace) if r["event"] in script), len(trace) - 1)
            return trace[:first] + trace[first + 1:]
        mutate("missing-command-control", missing_command)
        # Exercise result validation independently of command completeness.
        empty = lambda trace: [r for r in trace if r["event"] not in ("public.result", "validate", "read.schema", "read.first-sibling", "read.instance", "read.opaque", "read.null")]
        if name in ("invalid-capacity", "invalid-mode"):
            empty = lambda trace: [dict(r, logs=[]) for r in trace]
        mutate("empty-results-control", empty)
        if name == 'entry-guard':
            def skipped(trace):
                return [dict(r, supplied=0, before={'present': False}, after={'present': False})
                        if r['event'] == 'read.instance' else r for r in trace]
            mutate('skipped-nonnull-probes', skipped)
    try:
        compared_results(name, empty(rows))
    except AssertionError:
        print(json.dumps({"negative_evidence": "empty-compared-results", "case": name, "rejected": True}))
    else:
        raise AssertionError((name, "accepted empty compared results"))
    print(json.dumps({"case": name, "control_completed": True, "terminal": rows[-1]["event"],
                      "commands": len(completed_replay(name, rows, stopped))}))


LIFETIME_CONTROLS = ("recreate-print", "recreate-lookup", "dup-descendant", "parse-descendant",
                     "list-key", "table-reuse", "validation-delete", "retained-same-hash", "stale-handle", "retained-resize", "retained-remove")


def lifetime_evidence(name, rows):
    # Check actual allocator reuse in BOTH processes. Merely running a sequence
    # that could reuse storage, or comparing equal stopped prefixes, is no proof.
    normalized_reads(name, rows, project=False)
    completed_replay(name, rows)
    script = request_commands(name)
    seen, pairs = {}, []
    for row in rows:
        if "nodes" in row:
            generations = [n["generation"] for n in row["nodes"]]
            assert len(set(generations)) == len(generations) and set(seen) <= set(generations), (name, "lost retained identity")
        for node in row.get("nodes", []):
            generation = node["generation"]
            if generation in seen:
                assert seen[generation]["alive"] or not node["alive"], (name, "resurrected tombstone")
            seen[generation] = node
        if row["event"] == "allocation.address-reuse":
            old, new = row["old_generation"], row["new_generation"]
            assert new > old > 0 and new not in seen and old in seen and not seen[old]["alive"], (name, "invalid reuse")
            pairs.append((old, new))
    assert pairs, (name, "no actual address reuse")
    descendants = 0
    for old, new in pairs:
        assert new in seen, (name, "unresolved replacement generation")
        def internal(generation):
            node = seen[generation]
            origin = node["creation_operation"]
            return not node["id"] and origin and script[origin - 1] in ("dup", "parse", "list")
        if internal(old) and internal(new):
            descendants += 1
    if name.endswith(("dup-descendant", "parse-descendant", "list-key")):
        assert descendants, (name, "no internally created descendant address reuse")
    assert all(r["rc"] == 0 for r in rows if r["event"] in set(script) | {"public.result", "read.schema"}), (name, "unsuccessful control")
    if name.endswith("table-reuse"):
        tables = [n["table"] for r in commands(rows, "print") for n in r["nodes"] if n["alive"] and n["id"] == 1]
        assert len({t["identity"] for t in tables[:-1]}) == 16 and all(t["identity"] > 0 for t in tables[:-1]), (name, "reused/collapsed table generation")
        assert len(tables) == 17 and all(t["present"] and not t.get("retired") and t["used"] == 5 for t in tables[:-1]) and not tables[-1]["present"], (name, "retired replacement table")
    if name.endswith("validation-delete"):
        validation = commands(rows, "validate")[0]
        assert any(n["schema"] == "def" for n in validation["nodes"] if n["alive"]), (name, "validation did not create default")
        assert any(not n["id"] and not n["alive"] and n["creation_operation"] == 2 for n in rows[-1]["nodes"]), (name, "internal deletion did not retire default")
    if name.endswith("retained-same-hash"):
        chains = rows[-1]["table"]["chains"]
        assert len(chains) == 2 and len({x["generation"] for x in chains}) == 2, (name, "collapsed generations")
        assert sorted(x["alive"] for x in chains) == [False, True], (name, "lost retained tombstone")
    if name.endswith("retained-resize"):
        original = commands(rows, "insert")[0]["table"]["chains"][0]
        chains = rows[-1]["table"]["chains"]
        assert len(chains) == 1 and not chains[0]["alive"] and chains[0]["generation"] == original["generation"], (name, "resize lost retained generation")
    if name.endswith("retained-remove"):
        chains = rows[-1]["table"]["chains"]
        assert rows[-1]["table"]["size"] == 8 and len(chains) == 1 and chains[0]["alive"], (name, "removal retained wrong generation")
    if name.endswith("stale-handle"):
        boundary = rows[-1]
        assert boundary["handle"] == 14 and seen[boundary["generation"]]["id"] == 14, (name, "wrong stale handle")
        assert any(r["old_generation"] == boundary["generation"] for r in commands(rows, "allocation.address-reuse")), (name, "stale handle address was not reused")
    print(json.dumps({"case": name, "reuse_events": len(pairs), "descendant_reuses": descendants}))


def lifetime_control(case):
    name = "lifetime/" + case
    rc, rows = run(name)
    plain_rc, plain_rows = run(name, unmodified=True)
    compare_control(name, rc, rows, plain_rc, plain_rows)
    lifetime_evidence(name, rows)
    lifetime_evidence(name, plain_rows)
    # Verbose observation must not snapshot a partially discovered live forest
    # or turn its reused descendants into retained tombstone occurrences.
    event_rc, event_rows = run(name, "--events", unmodified=True)
    compare_control(name, rc, rows, event_rc, event_rows)
    lifetime_evidence(name, event_rows)
    for label, trace in (("instrumented", rows), ("unmodified", plain_rows)):
        for mutant, mutated in (
                ("missing-reuse", [r for r in trace if r["event"] != "allocation.address-reuse"]),
                ("invalid-reuse-generation", [dict(r, new_generation=0) if r["event"] == "allocation.address-reuse" else r for r in trace])):
            try:
                lifetime_evidence(name, mutated)
            except AssertionError:
                print(json.dumps({"case": name, "negative_evidence": mutant + "-" + label, "rejected": True}))
            else:
                raise AssertionError((name, "accepted invalid reuse evidence", mutant, label))
    control_equal(name, rc, rows)


def by_handle(row):
    nodes = {n["generation"]: n for n in row["nodes"]}
    bindings = row["bindings"]
    assert len({b["handle"] for b in bindings}) == len(bindings), "ambiguous handle"
    return {b["handle"]: nodes[b["generation"]] for b in bindings if b["generation"] and nodes[b["generation"]]["alive"]}


def duplication(row, fields):
    """Native lyd_dup/lyd_dup_r allocation sets, independently of ordinals."""
    nodes = {n["generation"]: n for n in row["nodes"]}
    handles = by_handle(row)
    out, src = handles[int(fields[1])], handles[int(fields[2])]
    parent, flags = int(fields[3]), int(fields[4])
    expected = Counter()
    def copy(n, recursive):
        expected[n["generation"]] += 1
        child = n.get("first", 0) if recursive or n.get("nodetype") == LYS_LIST else 0
        while child:
            c = nodes[child]
            if not recursive and not c.get("key"): break
            copy(c, recursive)
            child = c["next"]
    local = 0 if flags & 4 else parent
    if flags & 4:
        ancestor = src["parent"]
        while ancestor:
            n = nodes[ancestor]
            if parent and n.get("schema") == handles[parent].get("schema"):
                if not local: local = parent
                break
            copy(n, False)
            if not local: local = -1  # preserve the immediate copied parent
            ancestor = n["parent"]
    if not (src.get("key") and local):
        copy(src, bool(flags & 1))
    made = [n for n in nodes.values() if n["creation_operation"] == row["operation"]]
    assert Counter(n["source"] for n in made) == expected, ("wrong duplication allocation set", fields, expected)
    if src.get("key") and local:
        assert out.get("key") and out.get("schema") == src.get("schema"), "wrong existing-key alias"
        if local > 0:
            assert out["parent"] == handles[local]["generation"]
        else:
            assert out["creation_operation"] == row["operation"] and out["source"] == src["generation"], "wrong copied-ancestor key"
            local_parent = nodes[out["parent"]]
            assert local_parent["creation_operation"] == row["operation"] and local_parent["source"] == src["parent"], "wrong immediate ancestor"
    else:
        assert out["source"] == src["generation"] and out["creation_operation"] == row["operation"], "wrong returned duplicate"
    # Child correspondence applies recursively, or to keys only in a shallow list.
    def children(n):
        result, g = [], n.get("first", 0)
        while g:
            result.append(nodes[g]); g = nodes[g]["next"]
        return result
    def correspondence(dst, source, recursive):
        wanted = children(source) if recursive or source.get("nodetype") == LYS_LIST else []
        if not recursive:
            first_nonkey = next((i for i, c in enumerate(wanted) if not c.get("key")), len(wanted))
            wanted = wanted[:first_nonkey]
        got = children(dst)
        assert [c["source"] for c in got] == [c["generation"] for c in wanted], "wrong duplicate children"
        for d, s in zip(got, wanted, strict=True): correspondence(d, s, recursive)
    if not (src.get("key") and local): correspondence(out, src, bool(flags & 1))


def rb_members(row):
    generation = {n["generation"]: n["generation"] for n in row["nodes"]}
    return [{generation[r["node"]] for r in n.get("rb", [])} for n in row["nodes"] if n.get("rb")]


def complete_rb_topology(row):
    for node in row.get("nodes", []):
        if not node["alive"]:
            continue
        if "lyds_tree" not in node["metadata"]:
            assert not any(k in node for k in ("rb", "rb_root", "rb_stored_root")), "RB without metadata"
            continue
        assert "rb" in node and "rb_root" in node and "rb_stored_root" in node, "missing RB topology"
        if not node["rb_root"]:
            assert node["rb_stored_root"] == 0 and node["rb"] == [], "missing RB root"
            continue
        assert all(RECORD_FIELDS["rb"] <= r.keys() for r in node["rb"]), "incomplete RB record"
        members = {r["node"]: r for r in node["rb"]}
        assert len(members) == len(node["rb"]), "duplicate RB member"
        roots = [r["node"] for r in members.values() if not r["parent"]]
        assert roots == [node["rb_root"]], (row["event"], roots, node["rb_root"])
        assert node["rb_stored_root"] in members
        reached, pending = set(), roots[:]
        while pending:
            current = pending.pop()
            assert current not in reached, "RB cycle or shared child"
            reached.add(current)
            r = members[current]
            for side in ("left", "right"):
                child = r[side]
                if child:
                    assert child in members and members[child]["parent"] == current, "incomplete RB child/parent topology"
                    pending.append(child)
            if r["parent"]:
                parent = members[r["parent"]]
                assert current in (parent["left"], parent["right"]), "incomplete RB parent/child topology"
        assert reached == members.keys(), "unreachable RB members"


def generation_reference_sites(row):
    """Enumerate generation fields by representation; script handles are not IDs."""
    for key in ("node_generation", "source_generation", "working_rb_root", "old_generation",
                "new_generation", "generation", "queried_parent", "winner", "current_parent", "supplied", "target", "left", "right",
                "schema_winner", "instance_winner", "value_winner"):
        if key in row:
            yield "event." + key, row, key
    for node in row.get("nodes", []):
        yield "node.source", node, "source"
        for key in ("prev", "next", "parent", "first", "tail", "rb_root", "rb_stored_root"):
            if key in node:
                yield "node." + key, node, key
        for member in node.get("rb", []):
            for key in ("owner", "node", "parent", "left", "right"):
                yield "rb." + key, member, key
    for label, table in tables(row):
        if table["present"] and not table.get("retired", False):
            for occurrence in table["chains"]:
                yield label + ".generation", occurrence, "generation"


def tables(row):
    for entry in row.get("tables", []):
        if "storage" in entry:
            yield "retained.table", entry["storage"]
    for key in ("table", "before", "after"):
        if key in row:
            yield key, row[key]
    for node in row.get("nodes", []):
        if "table" in node:
            yield "node.table", node["table"]


def internal_build_allocations(rows, creations):
    # Independent request expectations: do not derive the required identities
    # from the surviving event/link/occurrence references, or from raw generations.
    parsed = next(r for r in rows if r["event"] == "parse" and r["operation"] == 1)
    nodes = {n["generation"]: n for n in parsed["nodes"]}
    assert {(op, ordinal) for op, ordinal in creations if op == 1} == {(1, i) for i in range(1, 6)}, "missing parser allocations"
    for ordinal, schema in enumerate(("p", "q", "ll", "ll"), 2):
        child = nodes[creations[1, ordinal]]
        assert child["id"] == 0 and child["schema"] == schema and child["source"] == 0
        assert child["parent"] == creations[1, 1]
    assert (3, 1) in creations, "missing failed constructor allocation"
    failed = creations[3, 1]
    result = next(r for r in rows if r["event"] == "term" and r["operation"] == 3)
    assert result["rc"] == 7
    node = next(n for n in result["nodes"] if n["generation"] == failed)
    assert node["id"] == 0 and node["attempt"] == 70 and not node["alive"] and node["source"] == 0
    assert any(r["event"] == "node.free" and r["operation"] == 3 and
               r["node_generation"] == failed and r["rc"] == 7 for r in rows), "missing failed constructor free"


# Fixed request expectations, independent of which observer records survive.
# Zero counts are intentional: disabled resizing must remain disabled.
RESIZES = {
    "retained-null": 2, "retained-opaque": 2, "entry-guard": 1,
    "remote-last": 1, "remote-nonlast": 1, "remote-last-results": 1,
    "remote-last-release": 1, "collision-build": 4, "collision-resize": 3,
    "repeated-pointer": 4, "collision-history-retained": 2,
    "collision-history-shrunk": 4, "driver-lifetime": 1,
    "merge2-defined": 1, "merge2-successor": 1, "opaque-separated": 1,
    "schema-pointer": 1, "schema-pointer-reverse": 1,
}
DUP_INITS = {"driver-lifetime": 1}

# Required native boundaries for these fixed scripts, in PRIMITIVES order.
# Counts describe calls, not allocation generations or serialized snapshots.
# A pair is (before, after) only for the deliberately stopped merge prefixes.
PRIMITIVES = (
    'lyd_insert_hash',
    'lyd_unlink_hash',
    'lyd_insert_hash_add',
    '_lyht_insert_with_resize_cb',
    'lyht_remove_with_resize_cb',
    'lyht_find_next_with_collision_cb',
    'lyd_validate_duplicates',
    'lyd_insert_node_last',
    'lyd_insert_only_child',
    'lyd_insert_after_node',
    'lyd_insert_before_node',
    'lyd_unlink_ignore_lyds',
    'lyds_move_meta',
    'lyds_merge_nodes2_front',
    'lyds_merge_nodes2_among',
    'lyds_merge_nodes2_back',
)
PRIMITIVE_COUNTS = {
    'retained-null': (5, 4, 5, 16, 6, 0, 0, 4, 1, 3, 0, 4, 0, 0, 0, 0),
    'retained-opaque': (6, 4, 5, 16, 6, 0, 0, 5, 1, 4, 0, 4, 0, 0, 0, 0),
    'entry-guard': (5, 1, 4, 12, 0, 0, 0, 2, 2, 0, 2, 1, 0, 0, 0, 0),
    'entry-order': (8, 0, 8, 10, 0, 0, 0, 4, 2, 2, 4, 0, 0, 0, 0, 0),
    'entry-opaque': (6, 3, 0, 0, 0, 0, 0, 4, 1, 3, 2, 3, 0, 0, 0, 0),
    'entry-duplicate': (10, 1, 4, 5, 0, 0, 0, 9, 5, 4, 1, 1, 0, 0, 0, 0),
    'entry-schema-lineage': (8, 0, 8, 10, 0, 0, 0, 2, 2, 2, 4, 0, 0, 0, 0, 0),
    'collision-build': (22, 19, 21, 66, 37, 3, 4, 4, 1, 18, 2, 18, 15, 0, 0, 0),
    'collision-history-retained': (6, 2, 6, 19, 5, 0, 0, 3, 1, 2, 1, 1, 0, 0, 0, 0),
    'collision-history-shrunk': (22, 18, 22, 68, 37, 0, 0, 4, 1, 18, 1, 17, 15, 0, 0, 0),
    'collision-resize': (21, 17, 21, 63, 33, 4, 5, 4, 1, 18, 1, 16, 15, 0, 0, 0),
    'dead-occurrence': (1, 1, 0, 2, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0),
    'driver-lifetime': (33, 25, 32, 40, 24, 0, 0, 9, 2, 6, 24, 25, 0, 0, 0, 0),
    'find-next-watchdog': (0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
    'free-slot-cap': (1, 0, 0, 9, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0),
    'handle-tombstone-probe': (8, 4, 4, 5, 0, 0, 0, 8, 5, 3, 0, 4, 0, 0, 0, 0),
    'handle-tombstone': (8, 4, 4, 5, 0, 0, 0, 8, 5, 3, 0, 4, 0, 0, 0, 0),
    'internal-build': (5, 1, 3, 4, 0, 0, 0, 5, 1, 3, 0, 1, 0, 0, 0, 0),
    'invalid-capacity': (0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
    'invalid-mode': (0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
    'list-events': (12, 4, 5, 9, 5, 0, 0, 8, 5, 3, 1, 4, 0, 0, 0, 0),
    'merge2-before': (9, 4, 4, 5, 0, 0, 0, 4, 3, 4, 2, 4, 1, 1, 1, (1, 0)),
    'merge2-defined': (12, 6, 5, 12, 0, 0, 0, 4, 3, 8, 1, 6, 1, 1, 1, 1),
    'merge2-successor': (12, 5, 5, 12, 0, 0, 0, 5, 3, 8, 1, 5, 1, 1, (1, 0), 0),
    'merge2-unsorted': (9, 4, 4, 5, 0, 0, 0, 4, 3, 3, 3, 4, 2, 1, (1, 0), 0),
    'opaque-eligibility': (5, 4, 0, 0, 0, 0, 0, 1, 1, 0, 1, 1, 0, 0, 0, 0),
    'opaque-separated': (9, 2, 4, 12, 0, 0, 0, 4, 2, 2, 4, 1, 0, 0, 0, 0),
    'opaque-threshold': (6, 1, 4, 5, 0, 0, 0, 4, 1, 3, 1, 0, 0, 0, 0, 0),
    'remote-last-release': (9, 3, 6, 15, 4, 0, 0, 7, 3, 6, 0, 3, 1, 0, 0, 0),
    'remote-last-results': (9, 3, 6, 15, 4, 0, 0, 7, 3, 6, 0, 3, 1, 0, 0, 0),
    'remote-last': (8, 3, 6, 15, 4, 0, 0, 7, 3, 6, 0, 3, 1, 0, 0, 0),
    'remote-nonlast': (10, 3, 6, 15, 4, 0, 0, 7, 3, 7, 0, 3, 1, 0, 0, 0),
    'repeated-pointer': (21, 19, 21, 66, 36, 4, 5, 3, 1, 17, 2, 19, 15, 0, 0, 0),
    'resize-disabled': (1, 0, 0, 2, 2, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0),
    'retained-address-reuse': (2, 1, 0, 2, 0, 0, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0),
    'schema-pointer-reverse': (5, 2, 4, 12, 2, 0, 0, 4, 2, 2, 0, 1, 0, 0, 0, 0),
    'schema-pointer': (5, 2, 4, 12, 2, 0, 0, 4, 2, 2, 0, 1, 0, 0, 0, 0),
}
# Allocation/free events are separate from physical unlink and table resize.
# Address reuse depends on the allocator: require its presence in the controls,
# without prescribing the number of reused addresses.
LIFETIME_COUNTS = {
    'retained-null': (1, 0, 0, 0),
    'retained-opaque': (1, 0, 0, 0),
    'entry-guard': (1, 0, 0, 0),
    'entry-order': (2, 0, 0, 0),
    'entry-opaque': (0, 0, 0, 0),
    'entry-duplicate': (1, 0, 0, 0),
    'entry-schema-lineage': (2, 0, 0, 0),
    'collision-build': (3, 0, 0, 0),
    'collision-history-retained': (1, 0, 0, 0),
    'collision-history-shrunk': (1, 0, 0, 0),
    'collision-resize': (2, 0, 0, 0),
    'dead-occurrence': (1, 0, 1, 1),
    'driver-lifetime': (2, 1, 29, 29),
    'free-slot-cap': (1, 0, 0, 0),
    'handle-tombstone-probe': (1, 1, 8, 8),
    'handle-tombstone': (1, 1, 8, 8),
    'internal-build': (1, 1, 5, 6),
    'list-events': (1, 0, 0, 0),
    'merge2-before': (1, 0, 0, 0),
    'merge2-defined': (1, 0, 0, 0),
    'merge2-successor': (1, 0, 0, 0),
    'merge2-unsorted': (1, 0, 0, 0),
    'opaque-separated': (1, 0, 0, 0),
    'opaque-threshold': (1, 0, 0, 0),
    'remote-last-release': (1, 1, 9, 9),
    'remote-last-results': (1, 0, 0, 0),
    'remote-last': (1, 0, 0, 0),
    'remote-nonlast': (1, 0, 0, 0),
    'repeated-pointer': (3, 0, 0, 0),
    'resize-disabled': (2, 1, 0, 0),
    'retained-address-reuse': (1, 0, 1, 1),
    'schema-pointer-reverse': (1, 0, 0, 0),
    'schema-pointer': (1, 0, 0, 0),
}
REUSE_CASES = {"driver-lifetime", "handle-tombstone", "handle-tombstone-probe", "retained-address-reuse"}
IDENTITY_FIELDS = {"generation", "id", "creation_operation", "creation_ordinal", "source", "alive"}
EVENT_FIELDS = {
    "allocation.address-reuse": {"old_generation", "new_generation"},
    "read.schema": {"operation", "rc", "queried_parent", "target", "winner", "current_parent", "before", "after"},
    "read.first-sibling": {"operation", "winner"},
    "public.result": {"operation", "rc"},
    "boundary.tombstoned-handle": {"operation", "handle", "generation", "current"},
    "read.instance": {"operation", "rc", "supplied", "target", "winner", "current_parent", "before", "after"},
    "read.opaque": {"operation", "rc", "supplied", "name", "winner", "current_parent", "before", "after"},
    "read.null": {"operation", "queried_parent", "target", "before", "after", "schema_rc", "schema_winner",
                  "instance_rc", "instance_winner", "value_rc", "value_winner"},
    "read.compare": {"operation", "left", "right", "options"},
}
# Completed-command/terminal snapshots with native errors, measured separately
# for context diagnostics and the logging callback. All other requests have zero.
ERROR_COUNTS = {
    "collision-build": {5: (1, 1), 9: (0, 1), 42: (2, 4)},
    "collision-history-retained": {7: (1, 1)},
    "collision-history-shrunk": {39: (1, 1), 40: (2, 4)},
    "internal-build": {1: (1, 1), 3: (1, 1)},
    "invalid-capacity": {1: (0, 1)},
    "invalid-mode": {1: (0, 1)},
    "repeated-pointer": {6: (1, 2), 7: (1, 1)},
}
RECORD_FIELDS = {
    "occurrence": {"bucket", "slot", "ordinal", "node", "generation", "occurrence", "hash", "alive"},
    "bucket": {"first", "last"},
    "slot": {"next", "node", "hash"},
    "rb": {"owner", "node", "parent", "left", "right", "color"},
    "diagnostic": {"code"},
    "log": {"level", "line"},
}


def records(row):
    for label, key in (("diagnostic", "diagnostics"), ("log", "logs")):
        for record in row.get(key, []):
            yield label, record
    for node in row.get("nodes", []):
        for record in node.get("rb", []):
            yield "rb", record
    for _, table in tables(row):
        if table["present"] and not table.get("retired", False):
            for label, key in (("occurrence", "chains"), ("bucket", "buckets"), ("slot", "slots")):
                for record in table[key]:
                    yield label, record


def entry_comparisons(name, rows):
    """Finite §2.1 controls: operation, ordered native operands, exact flags."""
    schedules = {
        'entry-order': (0, [[(4, 11)], [(11, 4)], [(11, 4)], [(11, 4)], [(4, 11)], [(14, 5)]]),
        'entry-duplicate': (1, [[(11, 4), (11, 5)], [(11, 5)], [(11, 4)]]),
        'entry-guard': (0, [[], [], [], []]),
    }
    if name not in schedules: return
    options, groups = schedules[name]
    reads = commands(rows, 'read.instance')
    assert len(reads) == len(groups), (name, 'missing instance controls')
    expected = []
    for read, pairs in zip(reads, groups, strict=True):
        snapshot = next((r for r in rows if r['event'] == 'instance' and r['operation'] == read['operation']), None)
        assert snapshot is not None, (name, 'missing instance snapshot')
        nodes = by_handle(snapshot)
        expected.extend((read['operation'], nodes[left]['generation'], nodes[right]['generation'], options)
                        for left, right in pairs)
    observed = [(r['operation'], r['left'], r['right'], r['options']) for r in commands(rows, 'read.compare')]
    assert observed == expected, (name, 'wrong comparison operation/operands/options', observed, expected)


def invariants(rows, name, stopped=True):
    # Validate read/event shape before the model uses operation to index input.
    for row in rows:
        if row["event"] in EVENT_FIELDS:
            assert EVENT_FIELDS[row["event"]] <= row.keys(), (name, "missing event fields", row["event"])
    completed_replay(name, rows, stopped)
    counts = Counter(r["event"] for r in rows)
    assert counts["driver.ready.actual-children-callback"] == 1, (name, "missing ready")
    assert rows[0]["event"] == "driver.ready.actual-children-callback", (name, "late ready")
    for primitive, expected in zip(PRIMITIVES, PRIMITIVE_COUNTS[name], strict=True):
        # The rewind control completes the insertion that the remote-last
        # corrupting-primitive control deliberately stops before registration.
        if name == "remote-last" and "boundary" not in rows[-1]["event"] and primitive == "lyd_insert_hash":
            expected = 9
        before, after = expected if isinstance(expected, tuple) else (expected, expected)
        assert (counts[primitive + ".before"], counts[primitive + ".after"]) == (before, after), (name, "missing/extra primitive boundary", primitive)
    for event, expected in zip(("table.allocate", "table.retire", "node.retiring", "node.free"), LIFETIME_COUNTS.get(name, (0, 0, 0, 0)), strict=True):
        assert counts[event] == expected, (name, "missing/extra lifetime event", event)
    if name in REUSE_CASES:
        assert counts["allocation.address-reuse"] > 0, (name, "missing address reuse")
    for event in ("lyht_resize.before", "lyht_resize.after"):
        assert counts[event] == RESIZES.get(name, 0), (name, "missing/extra resize boundary", event)
    assert counts["dup.table.initialized"] == DUP_INITS.get(name, 0), (name, "missing/extra dup initialization")
    normalized_reads(name, rows, project=False)
    entry_comparisons(name, rows)
    script = request_commands(name)
    script_commands = set(script)

    resizing, identities, creations, next_ordinal = [], {}, {}, Counter()
    dead, occurrences, generic = set(), {}, False
    # Address-reuse notices precede the new identity's first snapshot. All other
    # snapshot references must resolve in that snapshot, including tombstones.
    recorded = {n["generation"] for r in rows for n in r.get("nodes", [])}
    for row in rows:
        if row["event"] == "new" and row["rc"] == 0:
            generic = True
        if generic and (row["event"] in script_commands or ("nodes" in row and row["event"].startswith("boundary.") and row["event"] != "boundary.new-null")):
            assert "table" in row and row["table"]["present"], (name, "missing generic command/stop table", row["event"])
        if row["event"] in EVENT_FIELDS:
            assert EVENT_FIELDS[row["event"]] <= row.keys(), (name, "missing event fields", row["event"])
            if row["event"] == "public.result" and row["rc"] == 0:
                assert "json" in row, (name, "missing public JSON")
            if row["event"] == "allocation.address-reuse":
                assert row["old_generation"] > 0 and row["new_generation"] > 0, (name, "reuse without allocation identities")
        else:
            assert {"nodes", "operation", "rc", "diagnostics", "node_generation", "source_generation"} <= row.keys(), (name, "missing snapshot fields", row["event"])
        if "nodes" in row and (row["event"] in script_commands or row is rows[-1]):
            expected = ERROR_COUNTS.get(name, {}).get(row["operation"], (0, 0))
            assert (len(row["diagnostics"]), len(row.get("logs", []))) == expected, (name, "missing/extra command diagnostics/logs", row["operation"])
        if row["event"] in ("node.free", "node.retiring"):
            assert row["node_generation"] > 0, (name, "free without allocation identity")
        for label, record in records(row):
            assert RECORD_FIELDS[label] <= record.keys(), (name, "incomplete snapshot record", label)
        nodes = {n["generation"]: n for n in row.get("nodes", [])}
        if "nodes" in row:
            assert len(nodes) == len(row["nodes"]), (name, "duplicate allocation record")
            assert identities.keys() <= nodes.keys(), (name, "lost retained identity", row["event"])
        for label, container, key in generation_reference_sites(row):
            generation = container[key]
            available = nodes if "nodes" in row else identities
            if label == "event.new_generation" and row["event"] == "allocation.address-reuse":
                available = recorded
            if label.endswith(".generation"):
                assert generation > 0, (name, "occurrence without generation")
            assert generation == 0 or generation in available, (name, "unrecorded generation", row["event"], label, generation)
        # table.allocate also reports unrelated, unobserved native tables; the
        # driver intentionally omits their storage. Retirement is filtered.
        if row["event"].startswith(("lyht_", "_lyht_")) or row["event"] in ("table.retire", "dup.table.initialized"):
            assert "table" in row, (name, "missing event table", row["event"])
        for label, table in tables(row):
            if not table["present"] or table.get("retired", False):
                continue
            assert {"identity", "size", "used", "mode", "free", "buckets", "slots", "chains", "free_chain", "free_terminal"} <= table.keys(), (name, "incomplete table snapshot", label)
            assert len(table["chains"]) == table["used"], (name, "missing occurrences", label)
            assert len(table["buckets"]) == len(table["slots"]) == table["size"], (name, "missing bucket/slot snapshots", label)
            assert len(table["free_chain"]) == table["size"] - table["used"], (name, "missing free-chain entries", label)
            serials = [x["occurrence"] for x in table["chains"]]
            assert len(set(serials)) == len(serials), (name, "duplicate occurrence")
            for x in table["chains"]:
                identity = table["identity"], x["generation"], x["hash"]
                assert x["occurrence"] > 0 and occurrences.setdefault(x["occurrence"], identity) == identity, (name, "unstable occurrence")
                if nodes:
                    assert x["alive"] == nodes[x["generation"]]["alive"], (name, "occurrence liveness")
        complete_rb_topology(row)
        for generation, node in nodes.items():
            assert IDENTITY_FIELDS <= node.keys(), (name, "incomplete allocation identity")
            assert generation > 0, (name, "invalid generation")
            creation = node["creation_operation"], node["creation_ordinal"]
            assert 0 <= creation[0] <= row.get("operation", 0) and creation[1] > 0, (name, "invalid creation", node)
            if generation not in identities:
                assert creation[0] == row["operation"], (name, "wrong allocation operation")
                next_ordinal[creation[0]] += 1
                assert creation[1] == next_ordinal[creation[0]], (name, "wrong allocation ordinal")
            provenance = (*creation, node["source"])
            assert identities.setdefault(generation, provenance) == provenance, (name, "unstable creation/source")
            assert creations.setdefault(creation, generation) == generation, (name, "duplicate creation identity")
            if creation[0]:
                assert script[creation[0] - 1] in ("root", "term", "list", "opaque", "parse", "validate", "dup", "insert", "remove"), (name, "non-creating operation", creation)
            source = node["source"]
            if source:
                assert source in nodes and source < generation, (name, "invalid duplication source")
                assert script[creation[0] - 1] in ("dup", "insert", "remove"), (name, "unexpected duplication")
                if node["alive"] and nodes[source]["alive"]:
                    assert node.get("schema") == nodes[source].get("schema"), (name, "dup schema")
            elif creation[0]:
                assert script[creation[0] - 1] != "dup", (name, "missing duplication source")
            if not node["alive"]:
                dead.add(generation)
                assert node.get("current") == "unavailable" and "hash" not in node
            else:
                assert generation not in dead, (name, "revived tombstone")
                assert node["context"] == 1
                assert {"hash", "flags", "metadata", "prev", "next", "parent"} <= node.keys(), (name, "missing current metadata")
                assert "schema" in node or {"prefix_format", "prefix_bound"} <= node.keys(), (name, "missing schema/opaque metadata")
                # These are all inner schemas in the driver's two fixed modules.
                if node.get("schema") in ("c", "d", "r", "l", "kl"):
                    assert {"first", "tail", "table"} <= node.keys(), (name, "missing child/table snapshot")
        if row["event"] == "dup" and row["rc"] == 0:
            fields = [line.split() for line in (root / "cases" / (name + ".calls")).read_text().splitlines()
                      if line.strip() and not line.lstrip().startswith("#")][row["operation"] - 1]
            duplication(row, fields)
        if row["event"] == "dup.table.initialized":
            duplicate, source = nodes[row["node_generation"]], nodes[row["source_generation"]]
            table = row["table"]
            assert duplicate["source"] == source["generation"]
            assert duplicate["creation_operation"] == row["operation"]
            assert duplicate["first"] == duplicate["tail"] == 0, (name, "dup already has children")
            assert table == duplicate["table"] and table["present"] and source["table"]["present"]
            assert table["identity"] != source["table"]["identity"]
            assert (table["size"], table["mode"], table["used"]) == (source["table"]["size"], 1, 0), (name, "wrong dup initialization")
            assert not table["chains"] and table["free_chain"] == list(range(table["size"]))
        if row["event"] == "read.schema":
            assert row["before"] == row["after"], "lookup changed registrations"
        table = row.get("table", {})
        if row["event"] == "lyht_resize.before":
            assert table.get("present"), (name, "resize without table")
            resizing.append((row["operation"], table["identity"], {x["occurrence"]: (x["generation"], x["hash"]) for x in table["chains"]}))
        if row["event"] == "lyht_resize.after":
            assert resizing and table.get("present"), (name, "unpaired resize after")
            operation, table_id, before = resizing.pop()
            assert (row["operation"], table["identity"]) == (operation, table_id), "resize replaced table identity/operation"
            after = {x["occurrence"]: (x["generation"], x["hash"]) for x in table["chains"]}
            assert len(after) == table["used"]
            assert all(before.get(serial) == identity for serial, identity in after.items()), "resize revived/replaced occurrence identity"
    assert not resizing, (name, "unpaired resize before")
    # Ordinals are contiguous per allocation operation, including the bootstrap
    # operation zero. Zeroing or changing every identity consistently still fails.
    for operation in {c[0] for c in creations}:
        ordinals = sorted(c[1] for c in creations if c[0] == operation)
        assert ordinals == list(range(1, len(ordinals) + 1)), (name, "allocation ordinal gap")
    assert {(op, ordinal) for op, ordinal in creations if op == 0} == {(0, i) for i in range(1, 6)}, (name, "missing bootstrap allocations")
    if name == "internal-build":
        internal_build_allocations(rows, creations)


def negative_evidence(name, rows, stopped=True):
    """Run every applicable deletion/reference control on each accepted replay."""
    def rejected(label, mutant, check=invariants):
        try:
            if check is invariants:
                check(mutant, name, stopped)
            else:
                check(mutant, name)
        except (AssertionError, KeyError, IndexError):
            print(json.dumps({"negative_evidence": label, "case": name, "rejected": True}))
        else:
            raise AssertionError((name, "checker accepted mutant", label))

    def without_identities(label, generations):
        if generations:
            rejected(label, [dict(r, nodes=[n for n in r["nodes"] if n["generation"] not in generations])
                             if "nodes" in r else r for r in rows])

    script = [line.split()[0] for line in (root / "cases" / (name + ".calls")).read_text().splitlines()
              if line.strip() and not line.lstrip().startswith("#")]
    script_commands = set(script)
    rejected("truncated-replay", rows[:-1])
    rejected("unexpected-terminal", rows[:-1] + [dict(rows[-1], event="boundary.unexpected")])
    allocations = {n["generation"]: n for r in rows for n in r.get("nodes", [])}
    internal = {g for g, n in allocations.items() if n["creation_operation"] and not n["id"]}
    parser_children = {g for g in internal if script[allocations[g]["creation_operation"] - 1] == "parse"}
    failed = {r["node_generation"] for r in rows if r["event"] == "node.free" and r["rc"] != 0}
    without_identities("removed-parser-child-records", parser_children)
    without_identities("removed-failed-constructor-records", failed)
    without_identities("removed-internal-allocation-records", internal)
    without_identities("removed-tombstone-records", {g for g, n in allocations.items() if not n["alive"]})
    without_identities("removed-bootstrap-records", {g for g, n in allocations.items() if not n["creation_operation"]})
    free = next((i for i, r in enumerate(rows) if r["event"] == "node.free"), None)
    if free is not None:
        mutant = rows[:]
        mutant[free] = dict(rows[free], node_generation=0)
        rejected("zero-free-generation", mutant)
    # Delete each internal/failed allocation independently, including the final
    # ordinal: earlier gaps must not be the only reason a deletion is rejected.
    for generation in sorted(internal | failed):
        node = allocations[generation]
        without_identities("removed-allocation-%d-%d" % (node["creation_operation"], node["creation_ordinal"]), {generation})
    if name == "internal-build":
        creations = {(n["creation_operation"], n["creation_ordinal"]): g for g, n in allocations.items()}
        # Exercise the independent request assertions without dangling references
        # being able to reject the input first.
        for label, missing in (("missing-expected-parser-allocations", {(1, i) for i in range(2, 6)}),
                               ("missing-expected-failed-allocation", {(3, 1)})):
            retained = {c: g for c, g in creations.items() if c not in missing}
            rejected(label, rows, lambda evidence, _: internal_build_allocations(evidence, retained))

    # Every kind of reference is tested separately. Copy only its row so that
    # these checks stay linear in the retained evidence size per control.
    sites = {}
    for i, row in enumerate(rows):
        for label, container, key in generation_reference_sites(row):
            if container[key]:
                sites.setdefault(label, i)
    for label, i in sites.items():
        mutant = rows[:]
        mutant[i] = copy.deepcopy(rows[i])
        _, container, key = next(s for s in generation_reference_sites(mutant[i]) if s[0] == label and s[1][s[2]])
        container[key] = max(allocations) + 1
        rejected("unresolved-" + label, mutant)

    snapshot = next(i for i, r in enumerate(rows) if i and "nodes" in r)
    for field in ("nodes", "diagnostics", "node_generation", "source_generation"):
        mutant = rows[:]
        mutant[snapshot] = dict(rows[snapshot])
        del mutant[snapshot][field]
        rejected("removed-snapshot-" + field, mutant)
    mutant = rows[:]
    # Remove an identity already seen in ready, rather than a new allocation
    # whose delayed first appearance could fail the creation-operation check.
    mutant[snapshot] = dict(rows[snapshot], nodes=rows[snapshot]["nodes"][1:])
    rejected("removed-retained-identity", mutant)
    for field in sorted(IDENTITY_FIELDS):
        mutant = rows[:]
        mutant[0] = copy.deepcopy(rows[0])
        del mutant[0]["nodes"][0][field]
        rejected("removed-identity-" + field, mutant)
    for field in ("hash", "flags", "metadata", "schema", "prev", "next", "parent", "first", "tail", "table"):
        mutant = rows[:]
        mutant[0] = copy.deepcopy(rows[0])
        del mutant[0]["nodes"][0][field]  # the independently required bootstrap container
        rejected("removed-current-" + field, mutant)

    events = {r["event"] for r in rows}
    for i, row in enumerate(rows):
        if "nodes" not in row or (row["event"] not in script_commands and i != len(rows) - 1):
            continue
        for field in ("diagnostics", "logs"):
            if row.get(field):
                mutant = rows[:]
                mutant[i] = dict(row, **{field: []})
                rejected("removed-command-" + field, mutant)
    record_rows = {}
    for i, row in enumerate(rows):
        for label, _ in records(row):
            record_rows.setdefault(label, i)
    for label, i in record_rows.items():
        for field in sorted(RECORD_FIELDS[label]):
            mutant = rows[:]
            mutant[i] = copy.deepcopy(rows[i])
            record = next(r for kind, r in records(mutant[i]) if kind == label)
            del record[field]
            rejected("removed-record-field-" + label + "-" + field, mutant)
    for event, fields in EVENT_FIELDS.items():
        i = next((i for i, r in enumerate(rows) if r["event"] == event), None)
        if i is None:
            continue
        required = fields | ({"json"} if event == "public.result" and rows[i]["rc"] == 0 else set())
        for field in sorted(required):
            mutant = rows[:]
            mutant[i] = dict(rows[i])
            del mutant[i][field]
            rejected("removed-event-field-" + event + "-" + field, mutant)
        if event == "allocation.address-reuse":
            for field in ("old_generation", "new_generation"):
                mutant = rows[:]
                mutant[i] = dict(rows[i], **{field: 0})
                rejected("zero-reuse-" + field, mutant)
    # Fixed primitive/lifetime counts also reject deleting *both* boundaries,
    # which a pairing-only validator would accept vacuously.
    required = {p + suffix for p in PRIMITIVES for suffix in (".before", ".after")}
    required.update(("table.allocate", "table.retire", "node.retiring", "node.free", "allocation.address-reuse",
                     "driver.ready.actual-children-callback", "public.result", "read.schema", "read.first-sibling"))
    for event in sorted(events & required):
        rejected("removed-event-" + event, [r for r in rows if r["event"] != event])
    for primitive in PRIMITIVES:
        if primitive + ".before" in events:
            rejected("removed-pair-" + primitive, [r for r in rows if r["event"] not in (primitive + ".before", primitive + ".after")])

    rb = next((i for i, r in enumerate(rows) if any(n.get("rb") for n in r.get("nodes", []))), None)
    if rb is not None:
        mutant = rows[:]
        mutant[rb] = copy.deepcopy(rows[rb])
        node = next(n for n in mutant[rb]["nodes"] if n.get("rb"))
        del node["rb"]
        rejected("removed-rb-topology", mutant)
    present = next(((i, label) for i, r in enumerate(rows) for label, t in tables(r)
                    if t["present"] and not t.get("retired", False) and t["used"]), None)
    if present is not None:
        i, label = present
        mutant = rows[:]
        mutant[i] = copy.deepcopy(rows[i])
        table = next(t for l, t in tables(mutant[i]) if l == label and t.get("used"))
        table["chains"] = []
        rejected("removed-table-occurrences", mutant)
        for field in ("buckets", "slots", "free_chain"):
            mutant = rows[:]
            mutant[i] = copy.deepcopy(rows[i])
            table = next(t for l, t in tables(mutant[i]) if l == label and t.get("used"))
            assert table[field], (name, "empty bootstrap table evidence", field)
            table[field] = []
            rejected("removed-table-" + field, mutant)
    generic = next((i for i, r in enumerate(rows) if r["event"] == "new" and r["rc"] == 0), None)
    if generic is not None:
        for i in range(generic, len(rows)):
            row = rows[i]
            if "table" not in row or (row["event"] not in script_commands and not row["event"].startswith("boundary.")):
                continue
            mutant = rows[:]
            mutant[i] = dict(row)
            del mutant[i]["table"]
            rejected("removed-generic-table-" + row["event"], mutant)
    reads = commands(rows, "read.schema")
    if reads:
        for field in ("before", "after"):
            mutant = rows[:]
            i = rows.index(reads[0])
            mutant[i] = dict(rows[i])
            del mutant[i][field]
            rejected("removed-lookup-" + field, mutant)

    if name == "driver-lifetime":
        mutant = rows[:]
        i = next(i for i, r in enumerate(rows) if r["event"] == "dup")
        mutant[i] = copy.deepcopy(rows[i])
        duplicate = by_handle(mutant[i])[2]
        duplicate["first"] = duplicate["tail"] = 0
        rejected("missing-duplicate-child-suffix", mutant)
        mutant = copy.deepcopy(rows)
        for row in mutant:
            for node in row.get("nodes", []):
                node["creation_operation"] = node["creation_ordinal"] = node["source"] = 0
        rejected("zeroed-creation-and-source", mutant)
        mutant = copy.deepcopy(rows)
        completed = commands(mutant, "dup")[0]
        for i, row in enumerate(mutant):
            if row["event"] == "dup.table.initialized":
                replacement = copy.deepcopy(completed)
                replacement.update(event=row["event"], node_generation=row["node_generation"], source_generation=row["source_generation"])
                replacement["table"] = by_handle(completed)[2]["table"]
                mutant[i] = replacement
        rejected("completed-dup-swapped-for-initialization", mutant)
        for field, value in (("size", 16), ("mode", 0), ("used", 1), ("first", 7)):
            mutant = copy.deepcopy(rows)
            row = commands(mutant, "dup.table.initialized")[0]
            duplicate = next(n for n in row["nodes"] if n["generation"] == row["node_generation"])
            if field == "first":
                duplicate["first"] = duplicate["tail"] = value
            else:
                row["table"][field] = duplicate["table"][field] = value
                if field == "size":
                    row["table"]["free_chain"] = duplicate["table"]["free_chain"] = list(range(value))
            rejected("incorrect-dup-initial-" + field, mutant)
        for field in ("creation_operation", "creation_ordinal", "source"):
            mutant = copy.deepcopy(rows)
            for row in mutant:
                for node in row.get("nodes", []):
                    if node.get("id") == 2:
                        node[field] = 0
            rejected("zeroed-dup-" + field, mutant)
        mutant = copy.deepcopy(rows)
        for row in mutant:
            for node in row.get("nodes", []):
                if node.get("id") == 2:
                    node["source"] = 1  # existing, earlier, but wrong source
        rejected("wrong-existing-dup-source", mutant)
        mutant = copy.deepcopy(rows)
        for node in mutant[-1]["nodes"]:
            if node.get("id") == 2:
                node["creation_ordinal"] += 1
        rejected("drifting-creation-identity", mutant)
        rejected("removed-dup-initialization", [r for r in rows if r["event"] != "dup.table.initialized"])
    if RESIZES.get(name, 0):
        rejected("removed-resize-pairs", [r for r in rows if not r["event"].startswith("lyht_resize.")])
        for event in ("lyht_resize.before", "lyht_resize.after"):
            mutant = copy.deepcopy(rows)
            mutant.pop(next(i for i, r in enumerate(mutant) if r["event"] == event))
            rejected("removed-" + event, mutant)
        mutant = copy.deepcopy(rows)
        after = mutant.pop(next(i for i, r in enumerate(mutant) if r["event"] == "lyht_resize.after"))
        mutant.insert(next(i for i, r in enumerate(mutant) if r["event"] == "lyht_resize.before"), after)
        rejected("resize-after-before-its-before", mutant)


def summary(name, rc, rows):
    boundaries = [r["event"] for r in rows if "boundary" in r.get("event", "")]
    phases = [r["event"] for r in rows if r.get("event") in ["lyds_merge_nodes2_front.before", "lyds_merge_nodes2_front.after", "lyds_move_meta.after", "lyds_merge_nodes2_among.after", "lyds_merge_nodes2_back.after"]]
    tables = [(r.get("operation"), n["table"].get("size"), n["table"].get("used")) for r in rows
              for n in r.get("nodes", []) if n.get("id") == 1 and n.get("table", {}).get("present") and r.get("event") in ["term", "unlink", "validate", "change", "I"]]
    validations = [{"operation": r["operation"], "rc": r["rc"], "diagnostics": r["diagnostics"],
                    "new": [n["id"] for n in r["nodes"] if n.get("flags", 0) & 4 and n.get("id")]}
                   for r in commands(rows, "validate")]
    print(json.dumps({"case": name, "exit": rc, "events": len(rows), "boundaries": boundaries,
                      "merge_phases": phases, "tables": tables, "validation": validations}, sort_keys=True))


def main():
    global binary, plain
    parser = argparse.ArgumentParser()
    parser.add_argument("--driver", type=pathlib.Path, default=os.environ.get("YANG_NATIVE_REGISTRATION_DRIVER") or None)
    parser.add_argument("--comparison-driver", type=pathlib.Path, default=os.environ.get("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER") or None)
    parser.add_argument("--lifetime-case", choices=LIFETIME_CONTROLS)
    parser.add_argument("--retained-reuse", action="store_true")
    args = parser.parse_args()
    if args.driver is None: parser.error('--driver or YANG_NATIVE_REGISTRATION_DRIVER is required')
    binary = args.driver.resolve()
    plain = args.comparison_driver.resolve() if args.comparison_driver else binary.with_name('driver-unmodified')
    assert binary != plain, "comparison must be an identified unmodified executable"
    for executable, instrumented in [(binary, 1), (plain, 0)]:
        abi = json.loads(subprocess.check_output([str(executable), "--abi"], timeout=5))
        assert abi == {"pointer_bytes": 8, "long_double_bytes": 16, "long_double_mantissa_bits": 64,
                       "pointer_record_bytes": 16, "rb_node_bytes": 40, "ndebug": 1,
                       "instrumented": instrumented}, (str(executable), abi)


    if args.retained_reuse:
        for unmodified in (False, True):
            for option in ("--quiet", "--events"):
                rc, rows = run("retained-address-reuse", option, unmodified=unmodified)
                assert rc == 0, ("retained-address-reuse", rc)
                retained_reuse_evidence(rows)
                print(json.dumps({"retained_reuse_verified": True, "unmodified": unmodified, "option": option}))
        return

    if not args.lifetime_case:
        for name in ENTRY_CASES:
            rc, rows = run(name, "--events")
            assert rc == 0, (name, rc)
            invariants(rows, name)
            negative_evidence(name, rows)
            control_equal(name, rc, rows)
            summary(name, rc, rows)

    for case in ([args.lifetime_case] if args.lifetime_case else LIFETIME_CONTROLS):
        lifetime_control(case)
    if args.lifetime_case:
        return
    rc, rows = run("lifetime/retained-links")
    control_equal("lifetime/retained-links", rc, rows)

    rc, rows = run("remote-last", "--remote-stop")
    assert rc == 0 and any(r["event"] == "boundary.remote-last-corrupting-primitive" for r in rows)
    assert all(r["rc"] == 0 for r in rows if r["event"] in ["term", "root", "dup", "unlink", "attach"])
    boundary = rows[-1]
    n = by_handle(boundary)
    n[17] = next(node for node in boundary["nodes"] if node["attempt"] == 17 and node["creation_operation"] == boundary["operation"])
    g = {i: node["generation"] for i, node in n.items()}
    assert (n[11]["prev"], n[16]["next"], n[17]["prev"], n[17]["parent"], n[16]["prev"]) == (g[17], g[17], g[16], g[2], g[16])
    # Source retains the moved v3 schema occurrence before the local v2 occurrence.
    schema_hashes = [x["hash"] for x in n[1]["table"]["chains"] if x["node"] == 16]
    assert schema_hashes and any([x["node"] for x in n[1]["table"]["chains"] if x["hash"] == h][:2] == [16, 14] for h in schema_hashes)
    invariants(rows, "remote-last")
    negative_evidence("remote-last", rows)
    summary("remote-last", rc, rows)
    rc, rows = run("remote-last", "--remote-watchdog")
    assert rc == 0 and rows[-1]["event"] == "rewind"
    assert commands(rows, "read.first-sibling")[-1]["winner"] == by_handle(rows[-1])[2]["first"]
    invariants(rows, "remote-last", stopped=False)
    negative_evidence("remote-last", rows, stopped=False)
    control_equal("remote-last", rc, rows, stopped=False)
    summary("remote-last-parented-rewind-terminates", rc, rows)
    for name in ["remote-nonlast", "collision-build", "collision-resize", "repeated-pointer", "merge2-defined", "driver-lifetime", "internal-build", "collision-history-retained", "collision-history-shrunk", "handle-tombstone", "handle-tombstone-probe", "dead-occurrence", "free-slot-cap", "remote-last-results", "remote-last-release", "opaque-threshold", "opaque-separated", "opaque-eligibility", "resize-disabled", "retained-address-reuse", "list-events", "schema-pointer", "schema-pointer-reverse"]:
        rc, rows = run(name, "--remote-watchdog" if name == "remote-nonlast" else "--events")
        assert rc == 0, (name, rc)
        invariants(rows, name)
        negative_evidence(name, rows)
        if name in ["opaque-threshold", "opaque-separated", "opaque-eligibility", "remote-last-results", "remote-last-release"]:
            assert all(r.get("rc", 0) == 0 for r in rows), name
        if name == "list-events":
            assert [by_handle(r)[1]["table"]["used"] for r in commands(rows, "list") if r["operation"] == 5] == [5]
            assert [by_handle(r)[1]["table"]["used"] for r in commands(rows, "unlink")] == [4, 4, 4]
            assert_lookup_handles(rows, [(4, 1), (5, 1), (6, 1), (5, 1)])
            assert len(final_public(rows)) == 1
        if name.startswith("schema-pointer"):
            n = by_handle(commands(rows, "I")[0])
            assert n[4]["schema_identity"] != n[5]["schema_identity"]
            assert (n[4]["module"], n[4]["schema"]) == (n[5]["module"], n[5]["schema"])
            assert n[1]["table"]["used"] == 6, "distinct schema-pointer run lost"
            assert by_handle(commands(rows, "U")[0])[1]["table"]["used"] == 4, "distinct schema successor promoted"
            assert all(r.get("rc", 0) == 0 for r in rows)
        if name == "opaque-threshold":
            opaque = commands(rows, "opaque")[-1]
            assert not by_handle(opaque)[1]["table"]["present"]
            built = commands(rows, "term")[-1]
            assert by_handle(built)[1]["table"]["present"] and by_handle(built)[1]["table"]["used"] == 5
            before = by_handle(built)[1]["table"]
            assert by_handle(commands(rows, "U")[-1])[1]["table"] == before
        if name == "opaque-separated":
            table = by_handle(commands(rows, "term")[-1])[1]["table"]
            assert table["used"] == 6 and all(x["node"] != 12 for x in table["chains"])
            # Two distinct physical run starts; checked schema insertion keeps the first.
            schema_adds = [r for r in rows if r["event"] == "lyd_insert_hash_add.before"]
            assert schema_adds and by_handle(rows[-1])[13]["prev"] == by_handle(rows[-1])[12]["generation"]
            assert_lookup_handles(rows, [(11, 1)])
        if name == "opaque-eligibility":
            assert all(not n.get("table", {}).get("present") for n in rows[-1]["nodes"] if n["id"])
        if name == "resize-disabled":
            removed = commands(rows, "remove")
            assert [(r["table"]["size"], r["table"]["mode"], r["table"]["used"]) for r in removed] == [(16, 0, 0), (16, 1, 0)]
        if name == "retained-address-reuse":
            retained_reuse_evidence(rows)
        if name == "remote-last-results":
            assert_lookup_handles(rows, [(16, 2)])
            assert len(final_public(rows)) == 2
        if name == "remote-last-release":
            assert len(commands(rows, "free-root")) == 3 and all(not n["alive"] for n in rows[-1]["nodes"] if n["id"] and n["id"] not in [13, 15])
        if name == "remote-nonlast":
            assert any(r["event"] == "remote.nonlast.before" for r in rows)
            for row in rows:
                complete_rb_topology(row)
            assert not any("boundary" in r["event"] for r in rows)
            n = by_handle(rows[-1])
            g = {i: node["generation"] for i, node in n.items()}
            assert (n[1]["first"], n[1]["tail"], n[2]["first"], n[2]["tail"]) == (g[11], g[14], g[16], g[18])
            assert (n[11]["prev"], n[14]["next"], n[16]["prev"], n[16]["next"], n[17]["prev"], n[17]["next"], n[17]["parent"], n[18]["prev"], n[18]["next"]) == (g[14], 0, g[18], g[17], g[16], g[18], g[2], g[17], 0)
        if name == "merge2-defined":
            phases = [r["event"] for r in rows]
            assert phases.index("lyds_merge_nodes2_front.after") < phases.index("lyds_merge_nodes2_among.after") < phases.index("lyds_merge_nodes2_back.after")
            front = phases.index("lyds_merge_nodes2_front.after")
            among = phases.index("lyds_merge_nodes2_among.before")
            assert "lyds_move_meta.after" in phases[front + 1:among]
        if name == "collision-build":
            assert any(r["event"] == "lyd_insert_hash_add.after" and r["rc"] == 6 for r in rows)
            assert any(r["event"] == "lyht_find_next_with_collision_cb.after" and r["rc"] == 6 for r in rows)
            validation = commands(rows, "validate")[0]
            assert validation["rc"] == 0 and validation["diagnostics"] == []
            assert any("Internal error" in x.get("msg", "") for x in validation.get("logs", []))
            assert all(n.get("flags", 0) & 4 == 0 for n in validation["nodes"] if n["id"] == 14)
        if name == "repeated-pointer":
            first, last = commands(rows, "validate")
            assert first["rc"] == 7 and last["rc"] == 0
            assert first["diagnostics"][0]["path"] == "/m:c/ll[.='Qg==']"
            assert any(n["id"] == 11 and n["flags"] & 4 for n in first["nodes"])
            assert any(n["id"] == 11 and not n["flags"] & 4 for n in last["nodes"])
        if name.startswith("collision-history-"):
            read = commands(rows, "read.schema")[-1]
            assert (read["rc"], bool(read["winner"])) == ((0, True) if name.endswith("retained") else (5, False))
        if name == "driver-lifetime":
            assert any(r["event"] == "allocation.address-reuse" for r in rows)
            assert any(r["event"] == "dup.table.initialized" for r in rows)
            assert any(r["event"] == "table.retire" for r in rows)
        if name == "internal-build":
            assert any(r["event"] == "lyd_insert_hash_add.after" and r["rc"] == 6 for r in rows)
        if name.startswith("handle-tombstone"):
            assert rows[-1]["event"] == "boundary.tombstoned-handle" and rows[-1]["handle"] == 14
            generation = rows[-1]["generation"]
            assert any(r["event"] == "allocation.address-reuse" and r["old_generation"] == generation for r in rows)
        if name == "dead-occurrence":
            assert rows[-1]["event"] == "boundary.tombstoned-semantic-probe"
            assert rows[-1]["table"]["used"] == 2 and all(not x["alive"] for x in rows[-1]["table"]["chains"])
        if name == "free-slot-cap":
            assert rows[-1]["event"] == "boundary.primitive-free-slot-cap"
            assert rows[-1]["table"]["used"] == 8 and rows[-1]["table"]["free_terminal"] == 8
            assert commands(rows, "insert")[-1]["rc"] == 4  # checked EEXIST at full capacity
        # Compare observable terminating public outcomes with untouched pinned objects.
        if name not in ["driver-lifetime", "dead-occurrence", "retained-address-reuse"] and not name.startswith("handle-tombstone"):
            control_equal(name, rc, rows)
        summary(name, rc, rows)
    for name in ["merge2-successor", "merge2-before", "merge2-unsorted"]:
        rc, rows = run(name, "--merge2-ub-stop")
        assert rc == 0 and "boundary.merge2" in rows[-1]["event"], name
        invariants(rows, name)
        negative_evidence(name, rows)
        complete_rb_topology(rows[-1])
        expected = {21, 22, 11, 12}
        if name in ["merge2-before", "merge2-unsorted"]:
            assert {by_handle(rows[-1])[h]["generation"] for h in expected} in rb_members(rows[-1]), (name, rb_members(rows[-1]))
            assert rows[-1]["working_rb_root"]
            assert any(n.get("rb_stored_root") != n.get("rb_root") for n in rows[-1]["nodes"] if n.get("rb"))
        if name == "merge2-unsorted":
            assert rows[-1]["event"] == "boundary.merge2-exhausted-forward-iterator"
            prefix = commands(rows, "dump")[-1]
            n = by_handle(prefix)
            assert not n[1]["table"]["present"] and not n[11]["metadata"] and any(x.get("rb") for x in prefix["nodes"])
            assert n[11]["next"] == n[12]["generation"]
            assert [n[i]["value"] for i in [11, 12, 21, 22]] == ["4", "1", "2", "3"]
            assert not n[21]["parent"] and n[21]["next"] == n[22]["generation"]
            assert {by_handle(prefix)[h]["generation"] for h in (21, 22)} in rb_members(prefix) and not n[12]["metadata"]
            stopped = by_handle(rows[-1])
            assert stopped[1]["first"] == stopped[21]["generation"]
            assert [stopped[i]["next"] for i in [21, 22, 11, 12]] == [stopped[i]["generation"] for i in [22, 11, 12]] + [0]
            assert rows[-1]["working_rb_root"] in [n["rb_root"] for n in rows[-1]["nodes"] if n.get("rb")]
        summary(name, rc, rows)
    for name in ("invalid-capacity", "invalid-mode"):
        rc, rows = run(name, "--events")
        assert rc == 5 and rows[-1]["event"] == "boundary.new-null", name
        invariants(rows, name)
        negative_evidence(name, rows)
        assert not commands(rows, "table.allocate"), (name, "allocation before rejection")
        control_equal(name, rc, rows)
        summary(name, rc, rows)
    rc, rows = run("find-next-watchdog")
    assert rc == 0 and rows[-1]["event"] == "boundary.watchdog.find-next-cycle"
    invariants(rows, "find-next-watchdog")
    negative_evidence("find-next-watchdog", rows)
    summary("find-next-watchdog-low-level", rc, rows)


if __name__ == "__main__":
    main()
