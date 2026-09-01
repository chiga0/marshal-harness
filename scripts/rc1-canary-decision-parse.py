#!/usr/bin/env python3
"""Validate the maintainer-provided ReviewDecision JSON and write it out.
Reads DECISION_JSON from the environment; neither prints nor forwards the value.
usage: rc1-canary-decision-parse.py <out-path>"""
import json
import os
import sys

raw = os.environ.get("DECISION_JSON", "")
if not raw.strip():
    raise SystemExit("[rc1-canary-decision-parse] DECISION_JSON missing")
try:
    value = json.loads(raw)
except json.JSONDecodeError as error:
    raise SystemExit(f"[rc1-canary-decision-parse] JSON 解析失败：{error}")
if not isinstance(value, dict):
    raise SystemExit("[rc1-canary-decision-parse] decision 不是对象")
if value.get("kind") != "ReviewDecision":
    raise SystemExit("[rc1-canary-decision-parse] kind 必须为 ReviewDecision")
decision_block = value.get("decision")
if not isinstance(decision_block, dict):
    raise SystemExit("[rc1-canary-decision-parse] 缺 decision 对象")
for key in ("verdict", "reviewer", "independent", "runId", "reviewRound", "reviewPacketDigest", "specDigest", "verificationDigest", "artifactManifestDigest", "workerResultDigests", "evidenceDigest", "localSelfIdentityBindingDigest"):
    if key not in decision_block:
        raise SystemExit(f"[rc1-canary-decision-parse] decision.{key} 缺失")
if decision_block["independent"] is not True:
    raise SystemExit("[rc1-canary-decision-parse] decision.independent 必须为 true")
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False)
print("[rc1-canary-decision-parse] decision parsed and written")
