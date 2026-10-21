#!/usr/bin/env python3
# Merge-gate decision for scripts/astra: validated codex JSON -> report and exit status.
# argv: answer.json model sha cmd trusted label. Exit 1 = changes. Tested by scripts/test-astra-gate.
import json, re, sys
r = json.load(open(sys.argv[1])); model, sha, cmd, trusted, label = sys.argv[2:7]
if r.get("verdict") not in ("approve", "changes") or not isinstance(r.get("findings"), list):
    sys.exit("invalid codex answer")
n = lambda s: re.sub(r"(?im)^(\s*)VERDICT\s*:", r"\1verdict -", str(s))  # no forged attestations
# Only blocking findings gate the merge; follow-ups are filed as issues by the maintainer.
# A design critique has no scope split: every finding counts.
blocking = [f for f in r["findings"] if cmd != "review" or f.get("scope") != "follow-up"]
follow = [f for f in r["findings"] if f not in blocking]
if cmd == "review":
    # approve needs both: the model's own verdict and no blocking finding. Anything inconsistent
    # (e.g. "changes" with no findings: an incomplete review) fails closed.
    bad = r["verdict"] != "approve" or bool(blocking)
else:
    bad = r["verdict"] != "approve" or any(f["severity"] == "high" for f in r["findings"])
verdict = "changes" if bad else "approve"
print(f"### {model} {cmd} of `{label}`: **{verdict}**\n\n{n(r['summary'])}\n")
for f in blocking:
    print(f"- **{f['severity']}** `{n(f['file'])}:{f['line']}` — {n(f['problem'])} → {n(f['fix'])}")
if follow:
    print("\nFollow-ups (not blocking; file as issues):")
    for f in follow:
        print(f"- FOLLOW-UP **{f['severity']}** `{n(f['file'])}:{f['line']}` — {n(f['problem'])} → {n(f['fix'])}")
if cmd == "review" and trusted:
    print(f"\nVERDICT: {verdict} {sha} (codex-{model})")
elif cmd == "review":
    print("\n(untrusted run: not a merge-gate attestation; use --trusted)")
sys.exit(1 if bad else 0)
