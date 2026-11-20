#!/usr/bin/env python3
# SPDX-License-Identifier: BSD-3-Clause
"""Offline regressions for the trusted review executor; no Docker needed.

Run in the dev image: ./dev python3 scripts/test-astra-exec.py </dev/null
The optional --docker integration test builds main's image and uses real Docker
on the launcher host. It never calls a model or posts a review.
"""

import importlib.util
import io
import contextlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tarfile
import tomllib
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("astra_exec", Path(__file__).with_name("astra-exec.py"))
executor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(executor)


class ExecTests(unittest.TestCase):
    def test_image_context_is_compressed_and_from_main(self):
        def fake_run(argv, **kwargs):
            self.assertEqual(argv[-1], "origin/main")
            path = Path(next(v.removeprefix("--output=") for v in argv if v.startswith("--output=")))
            with tarfile.open(path, "w") as archive:
                entry = tarfile.TarInfo("Dockerfile")
                data = b"FROM trusted AS dev\n"
                entry.size = len(data)
                entry.pax_headers = {"comment": "git commit"}
                archive.addfile(entry, io.BytesIO(data))

        def fake_git(*args):
            self.assertEqual(args[0], "show")
            if args[1] == "origin/main:Dockerfile":
                return b"FROM trusted AS dev\n"
            self.assertEqual(args[1], "origin/main:Makefile")
            return b"GO_MIN ?= go1.26.8\n"

        def build(argv, **kwargs):
            self.assertEqual(argv[:2], ["docker", "build"])
            self.assertEqual(kwargs["timeout"], executor.LIFETIME_SECONDS)
            stream = kwargs["stdin"]
            self.assertEqual(stream.read(2), b"\x1f\x8b")
            stream.seek(0)
            with tarfile.open(fileobj=stream) as archive:
                data = archive.extractfile("Dockerfile").read()
                self.assertTrue(data.startswith(b"FROM trusted AS dev\n"))
                self.assertIn(b"GOTOOLCHAIN=go1.26.8", data)
            return "sha256:trusted\n"

        with tempfile.TemporaryDirectory() as tmp, patch.object(executor, "run", side_effect=fake_run), \
                patch.object(executor, "git", side_effect=fake_git), \
                patch.object(executor.subprocess, "check_output", side_effect=build):
            self.assertEqual(executor.build_image("docker", Path(tmp)), "sha256:trusted")

    def test_fixed_dispatch(self):
        with patch.object(executor, "execute", return_value={"exit_code": 7, "output": "failure", "truncated": False}) as call:
            result = executor.dispatch({"method": "tools/call", "params": {
                "name": "exec", "arguments": {"command": "go test ./..."}}}, "/usr/bin/docker", "fixed")
            call.assert_called_once_with("/usr/bin/docker", "fixed", "go test ./...")
            self.assertTrue(result["isError"])
            self.assertEqual(json.loads(result["content"][0]["text"])["exit_code"], 7)

    def test_reject_control_arguments(self):
        with patch.object(executor, "execute") as call:
            for name, args in [("docker", {"command": "id"}), ("exec", {"command": "id", "mount": "/"}),
                               ("exec", {"command": "id", "container": "host"}), ("exec", []), ("exec", {})]:
                with self.subTest(name=name, args=args), self.assertRaises(ValueError):
                    executor.dispatch({"method": "tools/call", "params": {"name": name, "arguments": args}}, "d", "c")
            call.assert_not_called()

    def test_invalid_command_never_spawns(self):
        with patch.object(executor.subprocess, "Popen") as spawn:
            for value in [None, 3, "", "\0", "x" * (executor.MAX_OUTPUT + 1)]:
                with self.subTest(value=str(value)[:20]), self.assertRaises(ValueError):
                    executor.execute("docker", "fixed", value)
            spawn.assert_not_called()

    def test_output_bound_and_operand_boundary(self):
        original = subprocess.Popen
        argv_seen = []

        def spawn(argv, **kwargs):
            argv_seen.append(argv)
            return original([sys.executable, "-c", "import sys; print('x'*100000); sys.exit(7)"], **kwargs)

        command = "echo $(touch /tmp/forbidden) ; docker run --privileged"
        with patch.object(executor.subprocess, "Popen", side_effect=spawn):
            result = executor.execute("/usr/bin/docker", "fixed", command)
        self.assertEqual(result["exit_code"], 7)
        self.assertTrue(result["truncated"])
        self.assertEqual(len(result["output"]), executor.MAX_OUTPUT)
        self.assertEqual(argv_seen[0], ["/usr/bin/docker", "exec", "--workdir", "/work/yang", "fixed",
                                      "timeout", "--kill-after=5", "1800", "bash", "-c", command])

    def test_codex_still_sandboxed_and_mcp_fixed(self):
        with patch.object(executor.subprocess, "call", return_value=0) as call:
            self.assertEqual(executor.review("/usr/bin/docker", "fixed", "model", "xhigh", "schema", "out", "rules"), 0)
        argv = call.call_args.args[0]
        self.assertEqual(argv[argv.index("-s") + 1], "read-only")
        self.assertIn("--ignore-rules", argv)
        self.assertIn("project_doc_max_bytes=0", argv)
        config = tomllib.loads(next(v for v in argv if v.startswith("mcp_servers.astra_exec=")))
        server = config["mcp_servers"]["astra_exec"]
        self.assertEqual(server["args"][-3:], ["serve", "/usr/bin/docker", "fixed"])
        self.assertEqual(server["args"][0], "-I")
        self.assertEqual(server["enabled_tools"], ["exec"])
        self.assertEqual(server["tools"]["exec"]["approval_mode"], "approve")
        self.assertTrue(server["required"])
        self.assertEqual(call.call_args.kwargs["stdin"], subprocess.DEVNULL)
        self.assertEqual(call.call_args.kwargs["timeout"], executor.LIFETIME_SECONDS)

    def test_project_pythonpath_cannot_replace_trusted_imports(self):
        with patch.object(executor.subprocess, "call", return_value=0) as call:
            executor.review("docker", "fixed", "model", "xhigh", "schema", "out", "rules")
        config = tomllib.loads(next(v for v in call.call_args.args[0] if v.startswith("mcp_servers.astra_exec=")))
        server = config["mcp_servers"]["astra_exec"]
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "json.py").write_text('raise RuntimeError("untrusted Python import")\n')
            result = subprocess.run([server["command"], *server["args"]], check=True,
                                    env={**os.environ, "PYTHONPATH": tmp}, cwd=tmp, text=True,
                                    input='{"jsonrpc":"2.0","id":1,"method":"initialize"}\n',
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            unsafe = subprocess.run([server["command"], *server["args"][1:]],
                                    env={**os.environ, "PYTHONPATH": tmp}, cwd=tmp, text=True,
                                    input='{"jsonrpc":"2.0","id":1,"method":"initialize"}\n',
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.assertNotEqual(unsafe.returncode, 0)
            self.assertIn("untrusted Python import", unsafe.stderr)
        self.assertEqual(json.loads(result.stdout)["result"]["serverInfo"]["name"], "astra_exec")

    def test_transport_timeout_kills_and_reaps_client(self):
        original = subprocess.Popen
        clients = []

        def spawn(argv, **kwargs):
            proc = original([sys.executable, "-c", "import time; time.sleep(60)"], **kwargs)
            clients.append(proc)
            return proc

        with patch.object(executor.subprocess, "Popen", side_effect=spawn), \
                patch.object(executor.time, "monotonic", side_effect=[0, 100000]):
            with self.assertRaisesRegex(RuntimeError, "transport timed out"):
                executor.execute("docker", "fixed", "sleep 60")
        self.assertLess(clients[0].returncode, 0)

    def test_cleanup_on_setup_error_and_unwind(self):
        for fail_setup in [False, True]:
            commands = []

            def fake_run(argv, **kwargs):
                commands.append(argv)
                if argv[:3] == ["git", "bundle", "create"]:
                    Path(argv[3]).write_text("bundle")
                if fail_setup and argv[:2] == ["docker", "exec"]:
                    raise subprocess.CalledProcessError(1, argv)
                return subprocess.CompletedProcess(argv, 0, stderr=b"")

            with self.subTest(fail_setup=fail_setup), patch.object(executor.os, "getuid", return_value=1001), \
                    patch.object(executor.shutil, "which", return_value="docker"), \
                    patch.object(executor, "build_image", return_value="sha256:trusted"), \
                    patch.object(executor, "git", return_value=("a" * 40 + " HEAD\n").encode()), \
                    patch.object(executor.subprocess, "run", side_effect=fake_run):
                with self.assertRaises((RuntimeError, subprocess.CalledProcessError)):
                    with executor.container("a" * 40):
                        raise RuntimeError("model failed")
            create = next(v for v in commands if v[:2] == ["docker", "create"])
            self.assertEqual(commands[-1][:3], ["docker", "rm", "-f"])
            self.assertEqual(commands[-1][3], create[create.index("--name") + 1])
            self.assertEqual(create[create.index("--network") + 1], "none")
            self.assertEqual(create[create.index("--user") + 1], "1000:1000")
            self.assertIn("--read-only", create)
            self.assertIn("no-new-privileges=true", create)
            self.assertIn("/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777", create)
            self.assertIn("/work:rw,exec,nosuid,nodev,size=8g,mode=1777", create)
            self.assertEqual(create[create.index("--cap-drop") + 1], "ALL")
            self.assertTrue(next(v for v in create if "dst=/source" in v).endswith(",readonly"))
            self.assertFalse(any("docker.sock" in v for v in create))
            self.assertFalse(any(v[:2] == ["git", "archive"] and v[-1] == "HEAD" for v in commands))
            seed = next(v for v in commands if v[:2] == ["docker", "run"])
            self.assertIn("reset -q --hard FETCH_HEAD", seed[-1])

    def test_mount_parser_rejects_injection(self):
        with self.assertRaises(ValueError):
            executor.bind("/tmp/input,src=/", "/source")

    def test_changed_head_cannot_receive_attestation(self):
        with patch.object(executor.os, "getuid", return_value=1001), \
                patch.object(executor.shutil, "which", return_value="docker"), \
                patch.object(executor, "build_image", return_value="sha256:trusted"), \
                patch.object(executor, "git", return_value=("b" * 40 + " HEAD\n").encode()), \
                patch.object(executor.subprocess, "run") as call:
            with self.assertRaisesRegex(ValueError, "HEAD changed"):
                with executor.container("a" * 40):
                    self.fail("mismatched review started")
        self.assertFalse(any(v.args[0][0] == "docker" for v in call.call_args_list))


@contextlib.contextmanager
def disposable_head():
    original = Path.cwd()
    main_sha = executor.git("rev-parse", "origin/main").decode().strip()
    with tempfile.TemporaryDirectory(prefix="astra-test-head-") as tmp:
        fixture = Path(tmp) / "repo"
        executor.run(["git", "clone", "-q", "--no-local", "--no-checkout", str(original), str(fixture)])
        executor.run(["git", "-C", str(fixture), "checkout", "-q", "--detach", main_sha])
        executor.run(["git", "-C", str(fixture), "update-ref", "refs/remotes/origin/main", main_sha])
        with (fixture / ".gitattributes").open("a") as stream:
            stream.write("\n/astra-canary export-ignore\n")
        (fixture / "astra-canary").write_text("must survive PR export-ignore\n")
        (fixture / "Dockerfile").write_text("FROM invalid-review-image\nRUN exit 91\n")
        executor.run(["git", "-C", str(fixture), "add", ".gitattributes", "astra-canary", "Dockerfile"])
        executor.run(["git", "-C", str(fixture), "-c", "core.hooksPath=/dev/null",
                      "-c", "user.name=test", "-c", "user.email=test@example.com",
                      "commit", "-qm", "test: hostile review inputs"], env={**os.environ, "TZ": "UTC"})
        try:
            os.chdir(fixture)
            yield
        finally:
            os.chdir(original)


def docker_test():
    # This is the exact provision/execute/cleanup path used by the MCP tool.
    with executor.container() as (docker, name):
        commands = [r"""set -eu
test "$(uname -m)" = x86_64
test "$(id -u)" = 1000
git diff --quiet HEAD
test -z "$(git remote)"
test ! -e /source/.git
cmp /source/AGENTS.md AGENTS.md
cmp /source/astra-canary astra-canary
grep -q 'must survive PR export-ignore' /source/astra-canary
grep -q '^FROM invalid-review-image$' /source/Dockerfile
test ! -e /var/run/docker.sock
test ! -e /run/docker.sock
test ! -e /work/home/.codex/auth.json
test ! -e /work/home/.config/gh/hosts.yml
test "$(ls /sys/class/net)" = lo
grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status
if sudo -n true 2>/dev/null; then exit 9; fi
if touch /source/astra-write-probe 2>/dev/null; then exit 9; fi
test ! -e /source/astra-write-probe
touch /work/yang/astra-write-probe
rm /work/yang/astra-write-probe
python3 -c 'import socket; s=socket.socket(socket.AF_UNIX); s.connect("/var/run/docker.sock")' 2>/dev/null && exit 9
echo 'PASS: amd64, non-root, no capabilities/escalation/network/credentials/socket; source read-only; scratch writable'
""", "make mod-verify fmt-check vet nocgo test-gates", "make oracle && cd conformance && YANG_ORACLE_REQUIRED=1 go test -tags oracle . -run '^TestCompareDateCanonicalCollisionOracle$' -count=1"]
        for command in commands:
            result = executor.execute(docker, name, command)
            print(json.dumps(result), flush=True)
            if result["exit_code"]:
                raise RuntimeError("container integration failed")
    # The Docker daemon, not a log line, confirms cleanup.
    result = subprocess.run([docker, "inspect", name], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    if result.returncode != 1 or b"No such object" not in result.stderr:
        raise RuntimeError("container still exists or cleanup could not be verified")
    print("PASS: disposable container removed")


if __name__ == "__main__":
    if sys.argv[1:] == ["--docker"]:
        with open("/tmp/yang-heavy.lock", "a") as lock:
            executor.fcntl.flock(lock, executor.fcntl.LOCK_EX)
            with disposable_head():
                docker_test()
    else:
        unittest.main()
