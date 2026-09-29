#!/usr/bin/env python3
"""Run every fixture of conformance/corpus/manifest.yaml through lyoracle.

  run_corpus.py            regenerate goldens with oracle/lyoracle
  run_corpus.py --check    compare instead of write; exit 1 on any difference
Authoritative run = inside the dev container: ./dev make oracle-check
"""
import json, os, subprocess, sys
import yaml  # PyYAML (python3-yaml)

here = os.path.dirname(os.path.abspath(__file__))
conf = os.path.dirname(here)
corpus = os.path.join(conf, "corpus")

check = "--check" in sys.argv
manifest = yaml.safe_load(open(os.path.join(corpus, "manifest.yaml")))
bad = 0
for fx in manifest["fixtures"]:
    req = dict(fx["request"], base_dir=fx["dir"])
    cmd = [os.path.join(here, "lyoracle")]
    out = subprocess.run(cmd, input=json.dumps(req), capture_output=True, text=True, cwd=corpus)
    resp = json.loads(out.stdout)
    text = json.dumps(resp, indent=2, sort_keys=True) + "\n"
    path = os.path.join(corpus, fx["dir"], fx["golden"])
    if check:
        same = os.path.exists(path) and open(path).read() == text
        bad += not same
        print(("ok  " if same else "DIFF"), fx["id"], resp.get("verdict"))
    else:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        open(path, "w").write(text)
        print("wrote", fx["id"], resp.get("verdict"))
sys.exit(1 if bad else 0)
