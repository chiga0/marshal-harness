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
for key in (
    "verdict",
    "reviewer",
    "runId",
    "reviewRound",
    "reviewPacketDigest",
    "specDigest",
    "verificationDigest",
    "artifactManifestDigest",
    "evidenceDigest",
    "localSelfIdentityBindingDigest",
):
    if key not in value:
        raise SystemExit(f"[rc1-canary-decision-parse] {key} 缺失")
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
print("[rc1-canary-decision-parse] decision parsed and written")
