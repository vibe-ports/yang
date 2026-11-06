# SPDX-License-Identifier: BSD-3-Clause
"""Replay local test-only scripts; retain complete JSONL separately from summaries."""
import argparse
import json
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument("--driver", required=True, type=pathlib.Path)
parser.add_argument("--comparison-driver", required=True, type=pathlib.Path)
args = parser.parse_args()
binary, plain = args.driver.resolve(), args.comparison_driver.resolve()
assert binary != plain, "comparison must be an identified unmodified executable"
for executable, instrumented in [(binary, 1), (plain, 0)]:
    abi = json.loads(subprocess.check_output([str(executable), "--abi"], timeout=5))
    assert abi == {"pointer_bytes": 8, "long_double_bytes": 16, "long_double_mantissa_bits": 64,
                   "pointer_record_bytes": 16, "rb_node_bytes": 40, "ndebug": 1,
                   "instrumented": instrumented}, (str(executable), abi)


def run(name, option="--quiet", unmodified=False):
    script = (root / "cases" / (name + ".calls")).read_bytes()
    result = subprocess.run([str(plain if unmodified else binary), option], input=script,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25, check=False)
    suffix = ".unmodified" if unmodified else (".first-sibling-control" if option == "--remote-watchdog" and name == "remote-last" else "")
    (root / ".build" / (name + suffix + ".jsonl")).write_bytes(result.stdout)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    return result.returncode, rows


def commands(rows, command):
    return [r for r in rows if r.get("event") == command]


def final_public(rows):
    return [r for r in rows if r.get("event") == "public.result"]


def lookup_results(rows):
    # Generations are process-local. Public handles identify the selected node
    # and its actual current parent independently of constructor-hook activity.
    handles = {}
    for row in rows:
        for node in row.get("nodes", []):
            if node.get("id"):
                handles[node["generation"]] = node["id"]
    def normalize(generation):
        assert not generation or generation in handles, ("unhandled lookup identity", generation)
        return handles.get(generation, 0)
    return [(r["operation"], r["rc"], normalize(r["queried_parent"]),
             normalize(r["winner"]), normalize(r["current_parent"]))
            for r in commands(rows, "read.schema")]


def control_equal(name, rc, rows):
    plain_rc, plain_rows = run(name, unmodified=True)
    assert plain_rc == rc and final_public(rows) == final_public(plain_rows), name
    assert [(r["rc"], r["diagnostics"]) for r in commands(rows, "validate")] == [(r["rc"], r["diagnostics"]) for r in commands(plain_rows, "validate")], name
    assert lookup_results(rows) == lookup_results(plain_rows), (name, "lookup result or current parent changed")


def by_handle(row):
    return {n["id"]: n for n in row["nodes"] if n["alive"] and n["id"]}


def rb_members(row):
    generation = {n["generation"]: n["id"] for n in row["nodes"]}
    return [{generation[r["node"]] for r in n.get("rb", [])} for n in row["nodes"] if n.get("rb")]


def complete_rb_topology(row):
    for node in row.get("nodes", []):
        if not node.get("rb"):
            continue
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


def invariants(rows):
    resizing = []
    for row in rows:
        for node in row.get("nodes", []):
            assert "creation_operation" in node and "creation_ordinal" in node
            if not node["alive"]:
                assert node.get("current") == "unavailable" and "hash" not in node
            else:
                assert node["context"] == 1
        if row["event"] == "read.schema":
            assert row["before"] == row["after"], "lookup changed registrations"
        table = row.get("table", {})
        if row["event"] == "lyht_resize.before" and table:
            resizing.append((table["identity"], {x["occurrence"]: (x["generation"], x["hash"]) for x in table["chains"]}))
        if row["event"] == "lyht_resize.after" and table:
            table_id, before = resizing.pop()
            assert table["identity"] == table_id, "resize replaced table identity"
            after = {x["occurrence"]: (x["generation"], x["hash"]) for x in table["chains"]}
            assert all(before.get(serial) == identity for serial, identity in after.items()), "resize revived/replaced occurrence identity"
    assert not resizing


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


rc, rows = run("remote-last", "--remote-stop")
assert rc == 0 and any(r["event"] == "boundary.remote-last-corrupting-primitive" for r in rows)
assert all(r["rc"] == 0 for r in rows if r["event"] in ["term", "root", "dup", "unlink", "attach"])
boundary = rows[-1]
n = by_handle(boundary)
g = {i: node["generation"] for i, node in n.items()}
assert (n[11]["prev"], n[16]["next"], n[17]["prev"], n[17]["parent"], n[16]["prev"]) == (g[17], g[17], g[16], g[2], g[16])
# Source retains the moved v3 schema occurrence before the local v2 occurrence.
schema_hashes = [x["hash"] for x in n[1]["table"]["chains"] if x["node"] == 16]
assert schema_hashes and any([x["node"] for x in n[1]["table"]["chains"] if x["hash"] == h][:2] == [16, 14] for h in schema_hashes)
summary("remote-last", rc, rows)
rc, rows = run("remote-last", "--remote-watchdog")
assert rc == 0 and rows[-1]["event"] == "rewind"
assert commands(rows, "read.first-sibling")[-1]["winner"] == by_handle(rows[-1])[2]["first"]
summary("remote-last-parented-rewind-terminates", rc, rows)
for name in ["remote-nonlast", "collision-build", "collision-resize", "repeated-pointer", "merge2-defined", "driver-lifetime", "internal-build", "collision-history-retained", "collision-history-shrunk", "handle-tombstone", "handle-tombstone-probe", "dead-occurrence", "free-slot-cap", "remote-last-results", "remote-last-release", "opaque-threshold", "opaque-separated", "opaque-eligibility", "resize-disabled", "retained-address-reuse"]:
    rc, rows = run(name, "--remote-watchdog" if name == "remote-nonlast" else "--events")
    assert rc == 0, (name, rc)
    invariants(rows)
    if name in ["opaque-threshold", "opaque-separated", "opaque-eligibility", "remote-last-results", "remote-last-release"]:
        assert all(r.get("rc", 0) == 0 for r in rows), name
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
        assert lookup_results(rows)[-1][3:] == (11, 1)
    if name == "opaque-eligibility":
        assert all(not n.get("table", {}).get("present") for n in rows[-1]["nodes"] if n["id"])
    if name == "resize-disabled":
        removed = commands(rows, "remove")
        assert [(r["table"]["size"], r["table"]["mode"], r["table"]["used"]) for r in removed] == [(16, 0, 0), (16, 1, 0)]
    if name == "retained-address-reuse":
        reuse = commands(rows, "allocation.address-reuse")
        assert reuse
        chains = rows[-1]["table"]["chains"]
        assert any(x["generation"] == reuse[-1]["old_generation"] and not x["alive"] for x in chains)
        assert any(x["generation"] == reuse[-1]["new_generation"] and x["alive"] for x in chains)
        original = commands(rows, "insert")[0]["table"]["chains"][0]
        retained = [x for x in chains if x["generation"] == original["generation"]]
        assert len(retained) == 1 and retained[0]["occurrence"] == original["occurrence"]
        assert len({x["occurrence"] for x in chains}) == 2
    if name == "remote-last-results":
        assert lookup_results(rows)[-1][3:] == (16, 2)
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
    invariants(rows)
    complete_rb_topology(rows[-1])
    expected = {21, 22, 11, 12}
    if name in ["merge2-before", "merge2-unsorted"]:
        assert expected in rb_members(rows[-1]), (name, rb_members(rows[-1]))
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
        assert {21, 22} in rb_members(prefix) and not n[12]["metadata"]
        stopped = by_handle(rows[-1])
        assert stopped[1]["first"] == stopped[21]["generation"]
        assert [stopped[i]["next"] for i in [21, 22, 11, 12]] == [stopped[i]["generation"] for i in [22, 11, 12]] + [0]
        assert rows[-1]["working_rb_root"] in [n["rb_root"] for n in rows[-1]["nodes"] if n.get("rb")]
    summary(name, rc, rows)
rc, rows = run("find-next-watchdog")
assert rc == 0 and rows[-1]["event"] == "boundary.watchdog.find-next-cycle"
summary("find-next-watchdog-low-level", rc, rows)
