# SPDX-License-Identifier: BSD-3-Clause
"""Control-comparison regressions; no native executable needed for trace mutants."""
import contextlib
import copy
import io
import pathlib
import tempfile
import unittest
from unittest.mock import patch

import check


def snapshot(event, operation):
    nodes = [dict(generation=g, id=1 if g == 6 else 0, creation_operation=1 if g == 6 else 0,
                  creation_ordinal=1 if g == 6 else g, source=0, attempt=1 if g == 6 else 0,
                  alive=True, parent=1 if 2 <= g <= 5 else 0, prev=5 if g == 2 else g - 1 if 3 <= g <= 5 else g,
                  next=g + 1 if 2 <= g < 5 else 0, schema="v" if 2 <= g <= 5 else "c",
                  schema_identity=2 if 2 <= g <= 5 else 1) for g in range(1, 7)]
    chains = [dict(generation=g, occurrence=i + 1, node=0, hash=8 * (i + 1), slot=i,
                   bucket=0, ordinal=i, alive=True) for i, g in enumerate((2, 2, 3, 4, 5))]
    nodes[0].update(first=2, tail=5, table=dict(present=True, identity=1, used=5, size=8,
                    continuity="observed", slots=[{} for _ in range(8)], buckets=[{} for _ in range(8)], chains=chains))
    nodes[5].update(first=0, tail=0, table=dict(present=False))
    return {"event": event, "operation": operation, "rc": 0, "diagnostics": [], "nodes": nodes,
            "output_handle": 1 if event == "root" else 0, "output_generation": 6 if event == "root" else 0,
            "node_count": 6, "table_count": 1, "tables": [dict(identity=1, alive=True)], "allocation_scope": "every-allocation",
            "bindings": [dict(handle=1, generation=6, revision=1, operation=1)]}


class ControlComparisonTest(unittest.TestCase):
    def compare(self, script, rows, other=None):
        def prefix(trace):
            ready = snapshot("driver.ready.actual-children-callback", 0)
            ready.update(nodes=ready["nodes"][:5], node_count=5, bindings=[])
            return [ready, snapshot("root", 1)] + [dict(r, operation=r["operation"] + 1) for r in trace]
        rows, other = prefix(rows), prefix(other) if other is not None else None
        script = "root 1 c\n" + script
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


if __name__ == "__main__":
    unittest.main()
