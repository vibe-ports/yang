# SPDX-License-Identifier: BSD-3-Clause
"""Replay local test-only scripts; retain complete JSONL separately from summaries."""
import json
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parent
binary = root / ".build/driver"
plain = root / ".build/driver-unmodified"
abi = json.loads(subprocess.check_output([str(binary), "--abi"], timeout=5))
assert abi == {"pointer_bytes": 8, "long_double_bytes": 16, "long_double_mantissa_bits": 64,
               "pointer_record_bytes": 16, "rb_node_bytes": 40, "ndebug": 1}


def run(name, option="--quiet", unmodified=False):
    script = (root / "cases" / (name + ".calls")).read_bytes()
    result = subprocess.run([str(plain if unmodified else binary), option], input=script,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25, check=False)
    suffix = ".unmodified" if unmodified else (".rewind-control" if option == "--remote-watchdog" and name == "remote-last" else "")
    (root / ".build" / (name + suffix + ".jsonl")).write_bytes(result.stdout)
    rows = [json.loads(line) for line in result.stdout.splitlines()]
    return result.returncode, rows


def commands(rows, command):
    return [r for r in rows if r.get("event") == command]


def final_public(rows):
    return [r for r in rows if r.get("event") == "public.result"]


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
summary("remote-last", rc, rows)
rc, rows = run("remote-last", "--remote-watchdog")
assert rc == 0 and rows[-1]["event"] == "rewind"
summary("remote-last-parented-rewind-terminates", rc, rows)
for name in ["remote-nonlast", "collision-build", "collision-resize", "repeated-pointer", "merge2-defined", "driver-lifetime", "internal-build", "collision-history-retained", "collision-history-shrunk", "handle-tombstone", "handle-tombstone-probe", "dead-occurrence", "free-slot-cap"]:
    rc, rows = run(name, "--remote-watchdog" if name == "remote-nonlast" else "--events")
    assert rc == 0, (name, rc)
    invariants(rows)
    if name == "remote-nonlast":
        assert any(r["event"] == "remote.nonlast.before" for r in rows)
        assert not any("boundary" in r["event"] for r in rows)
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
    # Compare observable terminating public outcomes with untouched pinned objects.
    if name not in ["driver-lifetime", "dead-occurrence"] and not name.startswith("handle-tombstone"):
        plain_rc, plain_rows = run(name, unmodified=True)
        assert plain_rc == rc and final_public(rows) == final_public(plain_rows), name
        assert [(r["rc"], r["diagnostics"]) for r in commands(rows, "validate")] == [(r["rc"], r["diagnostics"]) for r in commands(plain_rows, "validate")], name
    summary(name, rc, rows)
for name in ["merge2-successor", "merge2-before"]:
    rc, rows = run(name, "--merge2-ub-stop")
    assert rc == 0 and "boundary.merge2" in rows[-1]["event"], name
    summary(name, rc, rows)
rc, rows = run("find-next-watchdog")
assert rc == 0 and rows[-1]["event"] == "boundary.watchdog.find-next-cycle"
summary("find-next-watchdog-low-level", rc, rows)
