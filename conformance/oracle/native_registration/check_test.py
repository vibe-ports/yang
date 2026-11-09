# SPDX-License-Identifier: BSD-3-Clause
"""Control-comparison regressions; no native executable needed for trace mutants."""
import contextlib
import copy
import io
import pathlib
import re
import tempfile
import unittest
from unittest.mock import patch

import check
from identity import request_evidence


def snapshot(event, operation):
    if event == 'public.result':
        return dict(event=event, operation=operation, request=request_evidence(['print', '1']), rc=0)
    nodes = [dict(generation=g, id=1 if g == 6 else 0, creation_operation=1 if g == 6 else 0,
                  creation_ordinal=1 if g == 6 else g, source=0, attempt=1 if g == 6 else 0,
                  alive=True, context=1, hash=g, flags=4, metadata=[], nodetype=8 if 2 <= g <= 5 else 1, key=False,
                  value=str(g) if 2 <= g <= 5 else None, parent=1 if 2 <= g <= 5 else 0, prev=5 if g == 2 else g - 1 if 3 <= g <= 5 else g,
                  next=g + 1 if 2 <= g < 5 else 0, schema="v" if 2 <= g <= 5 else "c", schema_identity=2 if 2 <= g <= 5 else 1) for g in range(1, 7)]
    chains = [dict(generation=g, occurrence=i + 1, node=0, hash=8 * (i + 1), slot=i,
                   bucket=0, ordinal=i, alive=True) for i, g in enumerate((2, 2, 3, 4, 5))]
    nodes[0].update(first=2, tail=5, table=dict(present=True, identity=1, used=5, size=8,
                    continuity="observed", mode=2, free=5, free_chain=[5, 6, 7], free_terminal=8, slots=[dict(next=0, node=0, hash=0) for _ in range(8)], buckets=[dict(first=0, last=0) for _ in range(8)], chains=chains))
    nodes[5].update(first=0, tail=0, table=dict(present=False))
    for n in nodes:
        if n['nodetype'] == 1: del n['value']
    return {"node_generation": 0, "source_generation": 0, "event": event, "operation": operation,
            "request": request_evidence(['root', '1', 'c'] if event == 'root' else [event] if operation else []),
            "rc": 0, "diagnostics": [], "logs": [], "nodes": nodes,
            "output_handle": 1 if event == "root" else 0, "output_generation": 6 if event == "root" else 0,
            "node_count": 6, "table_count": 1, "tables": [dict(identity=1, alive=True)], "allocation_scope": "every-allocation",
            "schemas": [dict(identity=1, context=1, module='m', name='c', nodetype=1, parent=0),
                        dict(identity=2, context=1, module='m', name='v', nodetype=8, parent=1)],
            "bindings": [dict(handle=1, generation=6, revision=1, operation=1)]}


class ControlComparisonTest(unittest.TestCase):
    def test_command_inventory(self):
        from protocol import COMMANDS
        from identity import MIN_ARGUMENTS
        source = (pathlib.Path(__file__).resolve().parent / 'driver.c').read_text()
        self.assertEqual(COMMANDS, set(re.findall(r'!strcmp\(cmd,"([^"]+)"\)', source)))
        self.assertEqual(COMMANDS, MIN_ARGUMENTS.keys())

    def test_request_preconditions(self):
        from identity import requests, request_contract
        for script in ('read-instance 0 2 0', 'equal 0 0 0', 'term 2 0 p value', 'dup 3 2 0 66'):
            request_contract(requests(script)[0])
        for script in ('read-instance 2 0 1', 'print 0', 'unlink 0', 'term 2 1 p', 'record 1 2 3 bad',
                       'term 2 1 p value bad', 'new 40000 1', 'find-next-loop 1 1 1025', 'root 0 c',
                       'opaque 1 0 ' + 'x' * 80 + ' A'):
            with self.subTest(script=script), self.assertRaises(AssertionError):
                request_contract(requests(script)[0])

    def test_raw_payload_request_arguments(self):
        from identity import requests
        self.assertEqual(requests('parse 1 {"p":"a  b"}  \n'), [['parse', '1', '{"p":"a  b"}  ']])
        self.assertEqual(requests('parse-xml 1 <p>a  b</p>\n'), [['parse-xml', '1', '<p>a  b</p>']])
        self.assertEqual(requests('context module m {description "a  b";}\ncontext-keyless\n'),
                         [['context', 'module m {description "a  b";}'], ['context-keyless']])
        self.assertEqual(requests('opaque 1 0 a\u00a0b\u0085c A\n'), [['opaque', '1', '0', 'a\u00a0b\u0085c', 'A']])
        self.assertEqual(requests('parse 1 {"p":"a  b"}\r\n'), [['parse', '1', '{"p":"a  b"}\r']])

    def test_public_payload_outcome_contract(self):
        from protocol import shape
        for rc in (0, 7):
            row = dict(event='public.result', operation=1, request=request_evidence(['print', '1']), rc=rc)
            if rc:
                shape(row, False, False)
            else:
                with self.assertRaises(AssertionError): shape(row, False, False)
            shape(dict(row, json='{}'), False, False)
            with self.assertRaises(AssertionError): shape(dict(row, winner=0), False, False)

    def compare(self, script, rows, other=None):
        def prefix(trace):
            ready = snapshot("driver.ready.actual-children-callback", 0)
            ready.update(nodes=ready["nodes"][:5], node_count=5, bindings=[])
            return [ready, snapshot("root", 1)] + [dict(r, operation=r["operation"] + 1) for r in trace]
        rows, other = prefix(rows), prefix(other) if other is not None else None
        script = "root 1 c\n" + script
        from identity import requests
        for trace in (rows, other or []):
            for row in trace:
                if 0 < row['operation'] <= len(requests(script)): row['request'] = request_evidence(requests(script)[row['operation'] - 1])
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "cases").mkdir()
            (root / "cases" / "control.calls").write_text(script)
            with patch.object(check, "root", root), patch.object(check, "run", return_value=(0, rows if other is None else other)):
                with contextlib.redirect_stdout(io.StringIO()):
                    check.control_equal("control", 0, rows)

    def test_complete_nonempty_control(self):
        rows = [snapshot("dump", 1), dict(snapshot("public.result", 2), json='{"m:c":{}}'), snapshot("print", 2)]
        self.compare("dump\nprint 1\n", rows)

    def test_rejects_equal_early_stops(self):
        rows = [snapshot("dump", 1), snapshot("boundary.tombstoned-semantic-probe", 2)]
        with self.assertRaises(AssertionError):
            self.compare("dump\nprint 1\n", rows)

    def test_rejects_missing_command_on_either_or_both_sides(self):
        rows = [snapshot("dump", 1), dict(snapshot("public.result", 2), json='{"m:c":{}}'), snapshot("print", 2)]
        for left, right in ((rows[1:], rows), (rows, rows[1:]), (rows[1:], rows[1:])):
            with self.subTest(left=len(left), right=len(right)), self.assertRaises(AssertionError):
                self.compare("dump\nprint 1\n", left, right)

    def test_rejects_equal_truncated_replays_with_nonempty_results(self):
        rows = [dict(snapshot("public.result", 1), json='{"m:c":{}}'), snapshot("print", 1)]
        with self.assertRaises(AssertionError):
            self.compare("print 1\ndump\n", rows)

    def test_rejects_complete_but_empty_results(self):
        # All requested command snapshots and the final event are present.
        # This must reach result validation rather than a completeness check.
        with self.assertRaises(AssertionError):
            self.compare("dump\n", [snapshot("dump", 1)])

    def test_rejects_missing_public_result(self):
        with self.assertRaises(AssertionError):
            self.compare("print 1\n", [snapshot("print", 1)])

    def test_rejects_wrong_terminal_with_nonempty_results(self):
        rows = [dict(snapshot("public.result", 1), json='{"m:c":{}}'), snapshot("print", 1), snapshot("boundary.unexpected", 2)]
        with self.assertRaises(AssertionError):
            self.compare("print 1\n", rows)

    def test_rejects_reordered_or_duplicate_commands(self):
        rows = [snapshot("dump", 1), dict(snapshot("public.result", 2), json='{"m:c":{}}'), snapshot("print", 2)]
        for mutant in (rows[1:] + rows[:1], rows[:1] + rows):
            with self.subTest(events=[r["event"] for r in mutant]), self.assertRaises(AssertionError):
                self.compare("dump\nprint 1\n", mutant)

    def test_rejects_changed_command_status(self):
        rows = [snapshot("dump", 1), dict(snapshot("public.result", 2), json='{"m:c":{}}'), snapshot("print", 2)]
        mutant = copy.deepcopy(rows)
        mutant[0]["rc"] = 7
        with self.assertRaises(AssertionError):
            self.compare("dump\nprint 1\n", rows, mutant)

    def test_required_snapshots(self):
        from identity import validate
        ready = snapshot("driver.ready.actual-children-callback", 0)
        ready.update(nodes=ready["nodes"][:5], node_count=5, bindings=[])
        valid = [ready, snapshot("root", 1), snapshot("dump", 2)]
        validate("root 1 c\ndump\n", valid)
        for index in (0, 2):
            mutant = copy.deepcopy(valid)
            del mutant[index]["nodes"]
            with self.subTest(index=index), self.assertRaises(AssertionError):
                validate("root 1 c\ndump\n", mutant)

    def test_current_hash_is_distinct_from_recorded_hash(self):
        from identity import validate
        ready = snapshot("driver.ready.actual-children-callback", 0)
        ready.update(nodes=ready["nodes"][:5], node_count=5, bindings=[])
        valid = [ready, snapshot("root", 1), snapshot("dump", 2)]
        mutant = copy.deepcopy(valid)
        mutant[-1]["nodes"][1]["hash"] += 1
        # Bootstrap occurrences deliberately use hashes distinct from node hashes.
        self.assertNotEqual(validate("root 1 c\ndump\n", valid),
                            validate("root 1 c\ndump\n", mutant))

    def test_read_result_completion(self):
        for command, result in (("instance", "read.instance"),
                                ("opaq-find", "read.opaque"),
                                ("null-reads", "read.null"),
                                ("read-schema", "read.selection.schema"),
                                ("read-instance", "read.selection.instance"),
                                ("read-value", "read.selection.value"),
                                ("read-opaque", "read.selection.opaque"),
                                ("equal", "read.equal")):
            with tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                (root / "cases").mkdir()
                (root / "cases/control.calls").write_text(command + " 1 2 3\n")
                rows = [snapshot(result, 1), snapshot(command, 1)]
                with patch.object(check, "root", root):
                    check.completed_replay("control", rows)
                    for mutant in (rows[:1], rows[1:], rows[:1] + rows,
                                   [dict(rows[0], operation=2), rows[1]]):
                        with self.subTest(command=command), self.assertRaises(AssertionError):
                            check.completed_replay("control", mutant)


if __name__ == "__main__":
    unittest.main()
