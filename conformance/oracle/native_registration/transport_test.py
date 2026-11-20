# SPDX-License-Identifier: BSD-3-Clause
"""Transport controls use synthetic producers, never native role evidence."""
import json
import sys
import unittest
from unittest.mock import patch

from transport import TransportError, decode_rows, run


class TransportControls(unittest.TestCase):
    def test_strict_jsonl(self):
        record = json.dumps({'event': 'control'})
        self.assertEqual(decode_rows((record + '\n').encode()), [{'event': 'control'}])
        malformed = [b'{"event":"\xff"}\n', b'{"event":"\xfe"}\n', b'{}',
                     b'{}\r\n', b'{}\n\n', b'\xef\xbb\xbf{}\n']
        for encoding in ('utf-16', 'utf-16-le', 'utf-16-be', 'utf-32', 'utf-32-le', 'utf-32-be'):
            malformed.append(record.encode(encoding) + b'\n')
        for output in malformed:
            with self.subTest(output=output):
                with self.assertRaises((ValueError, UnicodeError)):
                    decode_rows(output)

    def test_capture_budgets(self):
        cases = (
            ('stdout', {'MAX_STDOUT': 1024}, 'import os; os.write(1, b"x\\n" * 65536)'),
            ('stderr', {'MAX_STDERR': 1024}, 'import os; os.write(2, b"x" * 65536)'),
            ('record', {'MAX_RECORD': 1024}, 'import os; os.write(1, b"x" * 65536)'),
            ('records', {'MAX_RECORDS': 3}, 'import os; os.write(1, b"{}\\n" * 65536)'),
        )
        for name, budgets, program in cases:
            with self.subTest(name=name), patch.multiple('transport', **budgets):
                with self.assertRaisesRegex(TransportError, 'budget exceeded'):
                    run([sys.executable, '-c', program], timeout=5)

    def test_separate_pipes(self):
        result = run([sys.executable, '-c', 'import os; os.write(1,b"{}\\n"); os.write(2,b"diagnostic")'])
        self.assertEqual(decode_rows(result.stdout), [{}])
        self.assertEqual(result.stderr, b'diagnostic')


if __name__ == '__main__':
    unittest.main()
