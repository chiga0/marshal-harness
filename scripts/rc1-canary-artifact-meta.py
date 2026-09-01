#!/usr/bin/env python3
"""Resolve the exactly one run-phase artifact identity from a workflow run.

usage: rc1-canary-artifact-meta.py <artifacts-api-json>
STDOUT: "<artifact-id> <raw-64hex-digest>".
"""
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
arts = [a for a in data.get("artifacts", []) if a.get("name", "").startswith("rc1-canary-run-")]
if len(arts) != 1:
    raise SystemExit(f"[rc1-canary-artifact-meta] run-phase artifact count={len(arts)}")
a = arts[0]
digest = a.get("digest") or ""
if digest.startswith("sha256:"):
    digest = digest.split(":", 1)[1]
if not re.fullmatch(r"[1-9][0-9]*", str(a.get("id", ""))) or not re.fullmatch(r"[0-9a-f]{64}", digest):
    raise SystemExit("[rc1-canary-artifact-meta] artifact 元数据漂移")
print(f"{a['id']} {digest}")
