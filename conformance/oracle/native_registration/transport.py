# SPDX-License-Identifier: BSD-3-Clause
"""Bounded subprocess transport for native evidence and its projection."""
import json
import selectors
import subprocess
import threading
import time

MAX_STDOUT = 64 << 20
MAX_STDERR = 1 << 20
MAX_RECORD = 8 << 20
MAX_RECORDS = 32768


class TransportError(ValueError):
    pass


def decode_rows(output):
    """Decode only LF-framed, strictly UTF-8 JSON objects, without autodetection."""
    if len(output) > MAX_STDOUT:
        raise TransportError('stdout byte budget exceeded')
    if isinstance(output, str):
        output = output.encode('utf-8', 'strict')
    if len(output) > MAX_STDOUT:
        raise TransportError('stdout byte budget exceeded')
    text = output.decode('utf-8', 'strict')
    if not text or not text.endswith('\n'):
        raise TransportError('missing JSONL record terminator')
    lines = text[:-1].split('\n')
    if len(lines) > MAX_RECORDS:
        raise TransportError('stdout record budget exceeded')
    result = []
    for line in lines:
        if not line or '\r' in line or '\x00' in line or len(line.encode('utf-8')) > MAX_RECORD:
            raise TransportError('invalid JSONL record framing or size')
        row = json.loads(line)
        if not isinstance(row, dict):
            raise TransportError('JSONL record must be an object')
        result.append(row)
    return result


def run(args, *, input=None, timeout=25, env=None, text=False, check=False,
        cwd=None, capture_output=False, stdout=None, stderr=None):
    """Capture both pipes under budgets, killing a producer on exhaustion."""
    if isinstance(input, str):
        input = input.encode('utf-8')
    process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, env=env, cwd=cwd)
    # A separate writer prevents a child that emits before reading stdin from
    # blocking pipe draining. Closing stdin also makes no-input ABI calls finite.
    write_errors = []
    def feed():
        try:
            if input:
                process.stdin.write(input)
            process.stdin.close()
        except BrokenPipeError:
            try:
                process.stdin.close()
            except BrokenPipeError:
                pass  # The checked process exit reports an early reader close.
        except (OSError, ValueError) as error:
            write_errors.append(error)
    writer = threading.Thread(target=feed, daemon=True)
    writer.start()
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ, 'stdout')
    selector.register(process.stderr, selectors.EVENT_READ, 'stderr')
    buffers = {'stdout': bytearray(), 'stderr': bytearray()}
    record_size = records = 0
    deadline = time.monotonic() + timeout
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(args, timeout)
            for key, _ in selector.select(min(remaining, 0.1)):
                chunk = key.fileobj.read1(65536)
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                name = key.data
                budget = MAX_STDOUT if name == 'stdout' else MAX_STDERR
                if len(buffers[name]) + len(chunk) > budget:
                    raise TransportError(name + ' byte budget exceeded')
                if name == 'stdout':
                    pieces = chunk.split(b'\n')
                    for index, part in enumerate(pieces):
                        record_size += len(part)
                        if record_size > MAX_RECORD:
                            raise TransportError('stdout record byte budget exceeded')
                        if index < len(pieces) - 1:
                            records += 1
                            record_size = 0
                            if records > MAX_RECORDS:
                                raise TransportError('stdout record budget exceeded')
                buffers[name].extend(chunk)
        code = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        writer.join(timeout=max(0.001, deadline - time.monotonic()))
        if writer.is_alive():
            raise subprocess.TimeoutExpired(args, timeout)
        if write_errors:
            raise write_errors[0]
    except BaseException:
        process.kill()
        process.wait()
        writer.join(timeout=1)
        raise
    finally:
        selector.close()
        process.stdout.close()
        process.stderr.close()
    output, errors = bytes(buffers['stdout']), bytes(buffers['stderr'])
    if text:
        output, errors = output.decode('utf-8', 'strict'), errors.decode('utf-8', 'strict')
    result = subprocess.CompletedProcess(args, code, output, errors)
    if check:
        result.check_returncode()
    return result
