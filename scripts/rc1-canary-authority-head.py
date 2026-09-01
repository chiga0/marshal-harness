#!/usr/bin/env python3
"""Print the current durable authority head of the canary ResultIngress journal.
usage: rc1-canary-authority-head.py <result-ingress.jsonl>"""
import json
import sys

with open(sys.argv[1], "rb") as handle:
    lines = handle.read().rstrip(b"\n").split(b"\n")
last = json.loads(lines[-1].decode("utf-8"))
digest = last.get("recordDigest", "")
if not isinstance(digest, str) or not digest.startswith("sha256:") or len(digest) != 71:
    raise SystemExit("[rc1-canary-authority-head] journal tail recordDigest 非法")
print(digest)
