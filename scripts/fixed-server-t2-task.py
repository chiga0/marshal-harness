#!/usr/bin/env python3
"""Render the bounded real-Pi task used by the fixed-server T2 canary."""

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

    marker_path = "fixed-server-t2-canary.txt"
    marker = f"fixed-server-t2:{args.base_ref}\n"
    task = {
        "apiVersion": "marshal.dev/v1alpha1",
        "kind": "Task",
        "metadata": {"id": args.task_id, "title": "fixed server T2 full lifecycle canary"},
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
                f"创建 `{marker_path}`，内容必须恰好为 `{marker.rstrip()}` 加一个结尾换行；"
                "最终回复必须只包含一个 WorkerResult JSON 对象。"
            ),
            "constraints": [
                f"只允许创建 `{marker_path}`，不得修改或删除其它文件。",
                "不得提交、推送、创建 Git 引用或访问网络。",
            ],
            "context": ["这是 fixed server T2 的真实 Pi 全生命周期 canary。"],
            "nonGoals": ["不执行发布或 merge。"],
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
                "id": "fixed-server-t2-marker",
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
            "id": "fixed-server-t2-marker",
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
            "attemptTimeoutSeconds": 300,
            "runTimeoutSeconds": 600,
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
    if getattr(args, "scenario", "marker") == "order-quote":
        # Oracle 来自冻结的控制仓库，不进入 Worker 的 allowPaths。
        oracle = os.path.join(repository, "scripts", "order-quote-oracle.py")
        if os.path.islink(oracle) or not os.path.isfile(oracle):
            raise SystemExit("缺少固定 order-quote oracle")
        with open(oracle, "rb") as handle:
            oracle_digest = hashlib.sha256(handle.read()).hexdigest()
        # 在冻结 Task 中绑定 oracle bytes；验证时执行已校验的同一份内存 bytes。
        oracle_command = (
            "import hashlib,pathlib,sys; "
            "p=pathlib.Path(sys.argv[1]); "
            "data=p.read_bytes() if not p.is_symlink() else b''; "
            "hashlib.sha256(data).hexdigest()==sys.argv[2] or sys.exit('oracle-drift'); "
            "sys.argv=[str(p),sys.argv[3]]; "
            "exec(compile(data,str(p),'exec'),{'__name__':'__main__','__file__':str(p)})"
        )
        task["metadata"]["title"] = "fixed server T2 订单报价业务验收"
        task["work"] = {
            "objective": (
                "实现 quote_order.py 的纯函数 quote_order(items)。items 必须是非空 list，"
                "每项为恰有 unit_price_cents 和 quantity 两个键的 dict；"
                "单价为非负 int（分），数量为正 int，bool 不算 int。"
                "非法输入统一抛 ValueError，成功或失败都不得修改输入。"
                "subtotal_cents 为单价乘数量之和；小计 >=5000 时运费为0，否则500。"
                "返回恰含 subtotal_cents、shipping_cents、total_cents 的 dict，值均为 int。"
                "支持大整数，不用浮点；不做网络、文件写入或进程操作。"
                "最终回复为一个 WorkerResult JSON 对象。"
            ),
            "constraints": ["只创建 quote_order.py，不改其它文件；不提交、推送或创建 Git 引用。"],
            "context": ["合成订单报价参考场景；验收由冻结控制仓库的独立 oracle 执行。"],
            "nonGoals": ["不增加 CLI/Web 框架、依赖、支付或真实订单集成。"],
        }
        task["scope"]["allowPaths"] = ["quote_order.py"]
        task["acceptance"]["commands"] = [{
            "id": "order-quote-business",
            "argv": ["/usr/bin/python3", "-I", "-B", "-c", oracle_command,
                     oracle, oracle_digest, "quote_order.py"],
            "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 4000,
            "required": True, "baselinePolicy": "none",
        }]
        task["deliverables"] = [{
            "id": "order-quote", "kind": "code", "required": True,
            "pathGlob": "quote_order.py", "minimumCount": 1,
        }]
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
    parser.add_argument("--scenario", choices=("marker", "order-quote"), default="marker")
    render(parser.parse_args())


if __name__ == "__main__":
    sys.exit(main())
