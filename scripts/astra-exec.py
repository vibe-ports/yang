#!/usr/bin/env python3
# SPDX-License-Identifier: BSD-3-Clause
"""Trusted, opt-in command runner for astra. Python stdlib only.

Threat model: PR files, tests, build scripts and tool arguments are hostile. Keep
Codex's existing host read-only sandbox and API credentials outside the container.
Allowlist one MCP operation: bash in ONE fixed, offline container; never accept
Docker options, mounts, environment, image names or host paths from the model.
Build only main's Dockerfile/dependencies, snapshot committed HEAD (no host .git,
untracked files or credentials), mount it read-only, and copy it to private scratch.
No socket, network, root, capabilities, sudo escalation or writable shared caches.
Main's prompt/schema/helper/gate stay host-side. Docker/kernel and trusted main
are the boundary; this does not make prompt injection or kernel exploits harmless.
"""

import contextlib
import fcntl
import io
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid

MAX_OUTPUT = 65536
MAX_REQUEST = 131072
COMMAND_SECONDS = 1800
LIFETIME_SECONDS = 7200


def run(argv, **kwargs):
    return subprocess.run(argv, check=True, **kwargs)


def git(*args):
    return subprocess.check_output(["git", *args])


def limits():
    return ["--platform", "linux/amd64", "--network", "none", "--read-only",
            "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true",
            "--pids-limit", "512", "--cpus", "2", "--memory", "12g",
            "--memory-swap", "12g"]


def bind(source, target, readonly=True):
    # Docker's --mount syntax cannot quote commas. Reject rather than reinterpret.
    if "," in str(source):
        raise ValueError("container input path contains a comma")
    return ["--mount", f"type=bind,src={source},dst={target}" + (",readonly" if readonly else "")]


def build_image(docker, inputs):
    """Build from main, never from the PR's Dockerfile or dependency declarations."""
    main_tar = inputs / "main.tar"
    run(["git", "archive", "--format=tar", f"--output={main_tar}", "origin/main"])
    dockerfile = git("show", "origin/main:Dockerfile")
    # Warm only trusted dependencies/toolchain while building, before PR execution.
    # Network-dependent vulnerability DB refreshes remain a separate CI check.
    makefile = git("show", "origin/main:Makefile").decode()
    minimum = re.search(r"^GO_MIN \?= (go[0-9]+\.[0-9]+\.[0-9]+)$", makefile, re.M)
    if not minimum:
        raise ValueError("cannot identify main's pinned minimum Go toolchain")
    dockerfile += ("\nFROM dev AS astra-review\n"
                   "COPY --chown=dev:dev . /home/dev/astra-seed\n"
                   "RUN cd /home/dev/astra-seed && go mod download"
                   " && cd conformance && go mod download"
                   f" && GOTOOLCHAIN={minimum[1]} go version\n"
                   "COPY --from=libyang /src/libyang /opt/astra-libyang\n").encode()
    context = inputs / "build.tar.gz"
    # Docker's stdin sniffer can mistake a leading PAX header for a Dockerfile.
    # Gzip has an unambiguous signature, including when git emits PAX metadata.
    with tarfile.open(main_tar) as src, tarfile.open(context, "w:gz", compresslevel=1) as dst:
        for entry in src:
            if entry.name != "Dockerfile":
                dst.addfile(entry, src.extractfile(entry) if entry.isfile() else None)
        entry = tarfile.TarInfo("Dockerfile")
        entry.size = len(dockerfile)
        dst.addfile(entry, io.BytesIO(dockerfile))
    with context.open("rb") as stream:
        return subprocess.check_output(
            [docker, "build", "--platform", "linux/amd64", "--target", "astra-review", "-q", "-"],
            stdin=stream, text=True, timeout=LIFETIME_SECONDS).strip()


@contextlib.contextmanager
def container(expected_head=None):
    docker = shutil.which("docker")
    if not docker:
        raise ValueError("docker is required on the launcher host")
    if os.getuid() == 0:
        raise ValueError("run the review launcher as an unprivileged user")
    expected_head = expected_head or git("rev-parse", "HEAD").decode().strip()
    with tempfile.TemporaryDirectory(prefix="astra-exec-") as tmp:
        root = Path(tmp)
        inputs = root / "input"
        source = root / "source"
        inputs.mkdir(mode=0o755)
        source.mkdir(mode=0o755)
        image = build_image(docker, inputs)
        run(["git", "bundle", "create", str(inputs / "repo.bundle"), "HEAD", "refs/remotes/origin/main"])
        bundled_head = git("bundle", "list-heads", str(inputs / "repo.bundle"), "HEAD").decode().split()
        if bundled_head != [expected_head, "HEAD"]:
            raise ValueError("HEAD changed while preparing the review; refusing a mismatched attestation")
        (inputs / "repo.bundle").chmod(0o644)
        # Checkout hostile symlinks only in an offline, non-root seed container.
        # git archive would let PR export-ignore attributes hide reviewed files.
        # No host .git/config, filters, hooks or credentials enter this checkout.
        run([docker, "run", "--rm", *limits(), "--user", f"{os.getuid()}:{os.getgid()}",
             "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777", "--env", "HOME=/tmp",
             "--env", "GIT_CONFIG_GLOBAL=/dev/null", "--env", "GIT_CONFIG_NOSYSTEM=1",
             *bind(inputs, "/input"), *bind(source, "/source", False),
             image, "bash", "-euc", "git init -q /source; "
             "git -C /source -c protocol.file.allow=always fetch -q /input/repo.bundle HEAD; "
             "git -C /source reset -q --hard FETCH_HEAD; rm -rf /source/.git"])
        # Only the bundle is needed by the reviewer, not build inputs or host git config.
        for path in inputs.iterdir():
            if path.name != "repo.bundle":
                path.unlink()
        name = "astra-exec-" + uuid.uuid4().hex
        try:
            run([docker, "create", "--name", name, "--rm", "--init", *limits(),
                 "--user", "1000:1000", "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777",
                 "--tmpfs", "/work:rw,exec,nosuid,nodev,size=8g,mode=1777",
                 *bind(source, "/source"), *bind(inputs, "/input"),
                 "--env", "TZ=UTC", "--env", "HOME=/work/home",
                 "--env", "GOPATH=/work/go", "--env", "GOCACHE=/work/cache",
                 "--env", "GOPROXY=off", "--env", "GOSUMDB=off",
                 "--workdir", "/work", image, "sleep", str(LIFETIME_SECONDS)], stdout=subprocess.DEVNULL)
            run([docker, "start", name], stdout=subprocess.DEVNULL)
            run([docker, "exec", name, "bash", "-euc", """
mkdir -p /work/home /work/yang
cp -a /home/dev/go /work/go
cp -a /source/. /work/yang/
cd /work/yang
git init -q -b review
git -c protocol.file.allow=always fetch -q /input/repo.bundle HEAD refs/remotes/origin/main:refs/remotes/origin/main
git reset -q --mixed FETCH_HEAD
mkdir -p .cache
cp -a /opt/astra-libyang .cache/libyang
"""
                 ])
            print(f"astra exec: ready {name} (offline linux/amd64, /source read-only, /work/yang writable)", file=sys.stderr)
            yield docker, name
        finally:
            result = subprocess.run([docker, "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            # --rm or the lifetime timer may already have removed it.
            if result.returncode and b"No such container" not in result.stderr:
                raise RuntimeError("failed to remove review container: " + result.stderr.decode(errors="replace"))
            print(f"astra exec: removed {name}", file=sys.stderr)


def execute(docker, name, command):
    """Bound output in memory; timeout the process group inside the container."""
    if not isinstance(command, str) or not command or len(command.encode()) > MAX_OUTPUT or "\0" in command:
        raise ValueError("command must be a nonempty string of at most 65536 bytes, without NUL")
    argv = [docker, "exec", "--workdir", "/work/yang", name,
            "timeout", "--kill-after=5", str(COMMAND_SECONDS), "bash", "-c", command]
    output = bytearray()
    total = 0
    deadline = time.monotonic() + COMMAND_SECONDS + 15
    with subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT) as proc:
        try:
            with selectors.DefaultSelector() as poll:
                poll.register(proc.stdout, selectors.EVENT_READ)
                while poll.get_map() or proc.poll() is None:
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Docker exec transport timed out; aborting review")
                    for key, _ in poll.select(timeout=1):
                        chunk = os.read(key.fileobj.fileno(), 8192)
                        if not chunk:
                            poll.unregister(key.fileobj)
                            continue
                        total += len(chunk)
                        output.extend(chunk[:max(0, MAX_OUTPUT - len(output))])
            rc = proc.wait()
        except BaseException:
            proc.kill()
            proc.wait()
            raise
    return {"exit_code": rc, "output": output.decode(errors="replace"), "truncated": total > MAX_OUTPUT}


def dispatch(request, docker, name):
    method = request.get("method")
    if method == "initialize":
        return {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                "serverInfo": {"name": "astra_exec", "version": "1.0"},
                "instructions": "Run PR commands only with exec in the offline dev container. "
                "Use make/go directly instead of ./dev (already inside). /source is immutable HEAD; "
                "/work/yang is a disposable copy. Main's review rules remain authoritative. "
                "Network-only checks (vulnerability DB refresh) cannot run here. "
                "Do not run full suites without the launcher's heavy-test lock."}
    if method == "ping":
        return {}
    if method == "tools/list":
        return {"tools": [{"name": "exec", "description": "Execute bash in the fixed disposable offline amd64 dev container. "
                          "Working directory /work/yang; state persists until review ends. "
                          "1800s timeout, 64KiB output cap; nonzero exit is returned explicitly.",
                          "inputSchema": {"type": "object", "properties": {"command": {"type": "string"}},
                                          "required": ["command"], "additionalProperties": False}}]}
    if method == "tools/call":
        params = request.get("params", {})
        if not isinstance(params, dict):
            raise ValueError("tool parameters must be an object")
        args = params.get("arguments", {})
        if params.get("name") != "exec" or not isinstance(args, dict) or set(args) != {"command"}:
            raise ValueError("only exec(command) is allowed")
        result = execute(docker, name, args["command"])
        return {"content": [{"type": "text", "text": json.dumps(result)}], "isError": result["exit_code"] != 0}
    raise ValueError("unsupported method")


def serve(docker, name):
    # Newline-delimited stdio MCP. No shell interpretation on the host, ever.
    while True:
        line = sys.stdin.buffer.readline(MAX_REQUEST + 1)
        if not line:
            return
        if len(line) > MAX_REQUEST:
            raise ValueError("MCP request too large")
        request = json.loads(line)
        if "id" not in request:  # initialized/cancelled notifications
            continue
        response = {"jsonrpc": "2.0", "id": request["id"]}
        try:
            response["result"] = dispatch(request, docker, name)
        except (ValueError, subprocess.CalledProcessError) as exc:
            response["error"] = {"code": -32602, "message": str(exc)}
        print(json.dumps(response), flush=True)


def review(docker, name, model, effort, schema, out, prompt):
    config = ("{command = " + json.dumps(sys.executable) + ", args = " +
              json.dumps(["-I", str(Path(__file__).resolve()), "serve", docker, name]) +
              f', required = true, tool_timeout_sec = {COMMAND_SECONDS + 30}, enabled_tools = ["exec"], ' +
              'tools = {exec = {approval_mode = "approve"}}, ' +
              'env_vars = ["DOCKER_CONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"]' + "}")
    prompt += "\nExecute reproducers and checks through astra_exec.exec, never the host shell. " \
              "It is already inside the dev container: use make/go directly, not ./dev. " \
              "The container has no network, credentials or Docker access. " \
              "Report commands, their exit codes, and checks you could not run."
    return subprocess.call(["codex", "exec", "-m", model, "-c", f"model_reasoning_effort={effort}",
                            "-c", "project_doc_max_bytes=0", "--ignore-rules",
                            "-c", "mcp_servers.astra_exec=" + config,
                            "-s", "read-only", "--ephemeral", "--output-schema", schema, "-o", out, prompt],
                           stdin=subprocess.DEVNULL, timeout=LIFETIME_SECONDS)


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "serve":
        serve(sys.argv[2], sys.argv[3])
        return 0
    if len(sys.argv) != 8 or sys.argv[1] != "review":
        raise ValueError("usage: astra-exec.py review SHA MODEL EFFORT SCHEMA OUT PROMPT")
    # The container's /tmp lock is a different inode: hold the shared HOST lock for
    # the whole review so even model-selected full suites cannot overlap dev/CI.
    with open("/tmp/yang-heavy.lock", "a") as lock:
        print("astra exec: waiting for heavy-test lock", file=sys.stderr)
        fcntl.flock(lock, fcntl.LOCK_EX)
        with container(sys.argv[2]) as (docker, name):
            return review(docker, name, *sys.argv[3:])


if __name__ == "__main__":
    def interrupted(signum, _frame):
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"astra exec failed: {exc}", file=sys.stderr)
        sys.exit(2)
