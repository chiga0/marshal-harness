#!/usr/bin/env python3
"""Render the bounded real-Pi task used by the fixed-server T1 canary."""

import argparse
import datetime
import hashlib
import json
import os
import re
import sys


ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{2,120}$")
HEAD = re.compile(r"^[0-9a-f]{40}$")
MODEL = re.compile(r"^[A-Za-z0-9._:-]+/[A-Za-z0-9._:-]+$")


def canonical_bytes(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def render(args):
    repository = os.path.realpath(args.repository)
    if repository != args.repository or not os.path.exists(os.path.join(repository, ".git")):
        raise SystemExit("repository 必须是 canonical Git worktree 根")
    if not HEAD.fullmatch(args.base_ref):
        raise SystemExit("base-ref 必须是 40 位小写 commit")
    if not ID.fullmatch(args.task_id) or not ID.fullmatch(args.run_id):
        raise SystemExit("task-id/run-id 形态非法")
    if not MODEL.fullmatch(args.model):
        raise SystemExit("model 必须是 provider/model")
    with open(args.doctor, encoding="utf-8") as handle:
        doctor = json.load(handle)
    binding = doctor.get("policyEnvironmentBinding")
    if not isinstance(binding, dict) or not binding:
        raise SystemExit("doctor 缺少 policyEnvironmentBinding")

    marker = f"fixed-server-t1:{args.base_ref}\n"
    marker_path = "fixed-server-t1-canary.txt"
    task = {
        "apiVersion": "marshal.dev/v1alpha1",
        "kind": "Task",
        "metadata": {"id": args.task_id, "title": "fixed server T1 exact-head response-loss canary"},
        "repository": {
            "path": repository,
            "remote": "origin",
            "baseRef": args.base_ref,
            "expectedRemoteUrl": "https://github.com/chiga0/marshal-harness.git",
        },
        "worker": {
            "preferredAdapter": "pi",
            "fallbackAdapters": [],
            "executionProfile": "workspace-write",
            "sessionPolicy": "ephemeral",
            "model": args.model,
        },
        "work": {
            "objective": (
                "这是 fixed server T1 恢复 canary。必须先调用 shell 执行 `/bin/sleep 300`，"
                f"sleep 正常返回后再创建 `{marker_path}`，内容必须恰好为 `{marker.rstrip()}` 加一个结尾换行；"
                "最终回复必须只包含一个 WorkerResult JSON 对象。"
            ),
            "constraints": [
                f"只允许创建 `{marker_path}`，不得修改或删除其它文件。",
                "不得提交、推送、创建 Git 引用或访问网络。",
                "不得跳过、缩短、后台化或并行化 `/bin/sleep 300`。",
            ],
            "context": ["长时间 sleep 为 server crash/recovery 留出确定的 RUNNING 观察窗口。"],
            "nonGoals": ["不完成验证、评审、发布或 merge。"],
        },
        "scope": {
            "allowPaths": [marker_path],
            "denyPaths": [".marshal/**"],
            "allowSubmodules": False,
            "maxChangedFiles": 1,
            "maxDiffBytes": 20000,
        },
        "acceptance": {
            "allowNoChange": False,
            "commands": [{
                "id": "fixed-server-t1-marker",
                "argv": [
                    "/usr/bin/python3", "-I", "-B", "-c",
                    "from pathlib import Path; import sys; "
                    f"sys.exit(0 if Path({marker_path!r}).read_text() == {marker!r} else 1)",
                ],
                "cwd": ".",
                "timeoutSeconds": 30,
                "maxLogBytes": 4000,
                "required": True,
                "baselinePolicy": "none",
            }],
        },
        "deliverables": [{
            "id": "fixed-server-t1-marker",
            "kind": "diagnostic",
            "required": True,
            "pathGlob": marker_path,
            "minimumCount": 1,
        }],
        "budgets": {
            "maxAttempts": 1,
            "maxOperationalRetries": 0,
            "maxReworkRounds": 0,
            "maxOutputBytes": 8388608,
            "attemptTimeoutSeconds": 900,
            "runTimeoutSeconds": 1200,
        },
        "publication": {
            "required": False,
            "provider": "none",
            "mode": "none",
            "remote": "origin",
            "baseBranch": "main",
            "mergePolicy": "never",
            "requiredChecks": [],
        },
    }
    policy = {
        "apiVersion": "marshal.dev/v1alpha1",
        "kind": "PolicySnapshot",
        "taskId": args.task_id,
        "runId": args.run_id,
        "sources": [{"scope": "builtin", "digest": "sha256:" + "b" * 64, "required": True}],
        "effective": {
            "minimumExecutionProfile": "workspace-write",
            "requireEnforcedNetworkPolicy": False,
            "networkPolicy": "unenforced",
            "allowFallbackWorkers": False,
            "allowWorkerSubagents": False,
            "allowPublication": False,
            "allowMerge": False,
            "allowGateWaivers": False,
            "allowedAdapters": ["pi"],
            "environmentAllowlist": ["PATH", "LANG", "TMPDIR", "HOME"],
            "retentionDays": 7,
        },
        "control": {
            "autonomyProfile": "supervised",
            "requiredApprovals": ["plan"],
            "allowMediatedSteering": False,
            "directPtyPolicy": "deny",
            "maxSteeringRounds": 0,
        },
        "environmentBinding": binding,
        "policyDigest": "",
        "generatedAt": utc_now(),
    }
    policy["policyDigest"] = "sha256:" + hashlib.sha256(canonical_bytes(policy)).hexdigest()
    for path, value in ((args.task_out, task), (args.policy_out, policy)):
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(canonical_bytes(value).decode("utf-8") + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--doctor", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--base-ref", required=True)
    parser.add_argument("--task-id", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--task-out", required=True)
    parser.add_argument("--policy-out", required=True)
    args = parser.parse_args()
    render(args)


if __name__ == "__main__":
    sys.exit(main())
