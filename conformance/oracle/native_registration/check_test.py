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
    return {"event": event, "operation": operation, "rc": 0, "diagnostics": [], "nodes": []}


class ControlComparisonTest(unittest.TestCase):
    def compare(self, script, rows, other=None):
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
