#!/usr/bin/env python3
"""M13 GoalLite walking-skeleton dogfood driver for the published RC1 asset.

三个子命令：
  render-task     按运行环境布局渲染 TaskSpec + PolicySnapshot（policyDigest 按
                  JCS canonical JSON 空占位摘要重算）。
  render-decision 从 review-packet.json 提取全部绑定摘要，渲染独立
                  ReviewDecision（verdict=accept）。
  metrics         汇总 run 证据中的 token 用量与时间线。

只读取显式传入的文件；不回显任何 secret 值。
"""
import argparse
import datetime
import hashlib
import json
import os
import sys

M13_OBJECTIVE = "产出 M13（Goal orchestration）首个工程纵切的完整落地材料（walking skeleton：simple prompt → approved proposal → one real Task）。只允许创建以下三个文件，不允许创建、修改或删除任何其它文件：\n1) `docs/m13-goal-lite-walking-skeleton.md`【中文】：描述在已确定的 Deterministic Control Plane 与 Goal admission 约束下首个产品纵切的设计。内容必须包含：walking skeleton 的目标与 Actor Flow（UserPrompt → Goal admission → DeliveryProposal → UserApproval → WorkGraph → one real Task → Integration → Outcome）、首期只允许 ONE Goal + ONE real Task 的领域范围声明、与 ADR 0019 的 plan admission、deterministic admission、revision 语义对应关系表、首批 Outcome 必须捕捉的证据面（independent Verification、reviewDigest、attemptAuthority 形成的路径）、以及下一切片序（不超过 3 个并发节点 + Integration Node）。\n2) `schemas/examples/goal-lite/approved-proposal.example.json`：一份与文档语义对应的 DeliveryProposal 样例。严格使用封闭字段集，禁止增删字段：{\"apiVersion\":\"marshal.dev/v1alpha1\",\"kind\":\"DeliveryProposal\",\"metadata\":{\"id\":\"goal-lite-approved-001\",\"title\":\"...\"},\"goal\":{\"prompt\":\"...\",\"objective\":\"...\",\"budgets\":{\"maxAttempts\":2,\"maxReworkRounds\":1,\"maxOutputBytes\":8388608}},\"userApproval\":{\"round\":1,\"actor\":\"operator:demo\",\"decision\":\"accept\",\"at\":\"<RFC3339 UTC>\"},\"workGraph\":{\"nodes\":[{\"id\":\"task-only-1\",\"kind\":\"task\",\"taskSpecDigest\":\"sha256:<64hex>\",\"dependencies\":[]}],\"integration\":{\"type\":\"collect\"}},\"outcome\":{\"state\":\"proposed\"}}。\n3) `schemas/examples/goal-lite/walking-skeleton.tasks.json`：一份与样例 proposal 对应、与现行 task-spec.schema.json 契约一致的 walking skeleton TaskSpec 实例，以该 proposal 下单一真实 Task 的完全相同语义填写（work.objective 与合作者出面背景也参考 walking skeleton 首期语气），work/worker/scope/acceptance/deliverables/budgets 均须可直接被 `marshal contract validate --stdin` 通过。\n要求：\n- 全部面向人的文字一律用中文；协议字段、CLI 命令、schema key、标识符保留英文；\n- 术语、状态名、Allowed milestone 词汇必须与 docs/roadmap-status.md 一致；\n- 不得宣称 M13 production 完成、不得宣称 v1 stable 支持；\n- 最终 assistant 的最终回复必须恰好是一个 WorkerResult JSON 对象，不带任何 Markdown 或解释。"

M13_CONSTRAINTS = [
    "只允许创建 docs/m13-goal-lite-walking-skeleton.md、schemas/examples/goal-lite/approved-proposal.example.json、schemas/examples/goal-lite/walking-skeleton.tasks.json 三个文件；",
    "文档与样例只可使用中文（schema key、CLI 命令、标识符保留英文）；",
    "不得改动仓库其它任何文件；不得使用任何第三方依赖；不得提交、推送、创建 git 引用或访问网络。",
]

ALLOW_PATHS = [
    "docs/m13-goal-lite-walking-skeleton.md",
    "schemas/examples/goal-lite/approved-proposal.example.json",
    "schemas/examples/goal-lite/walking-skeleton.tasks.json",
]

JSON_SYNTAX_CHECK = (
    "import json,sys\n"
    "for p in ('schemas/examples/goal-lite/approved-proposal.example.json','schemas/examples/goal-lite/walking-skeleton.tasks.json'):\n"
    "    v=json.load(open(p)); assert isinstance(v, dict) and v['kind'] in ('DeliveryProposal','Task'), (p, v.get('kind'))\n"
    "print('json-ok')"
)


def canonical_bytes(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def canonical_file_digest(path):
    with open(path, encoding="utf-8") as handle:
        value = json.load(handle)
    return "sha256:" + hashlib.sha256(canonical_bytes(value)).hexdigest()


def iso_now():
    return datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def render_task(args):
    with open(args.doctor, encoding="utf-8") as handle:
        doctor = json.load(handle)
    binding = doctor["policyEnvironmentBinding"]
    task = {
        "apiVersion": "marshal.dev/v1alpha1",
        "kind": "Task",
        "metadata": {"id": args.task_id, "title": "M13 GoalLite walking skeleton 首发纵切生产材料：中文设计文档 + proposal/tasks 样例"},
        "repository": {
            "path": args.repo_root,
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
        "work": {"objective": M13_OBJECTIVE, "constraints": M13_CONSTRAINTS, "context": [],
                 "nonGoals": ["Goal DAG runtime", "动态 replan", "累计预算", "production/engine 代码", "M12 SDK/SDK 协议工作", "Goal orchestration 长期弧本身"]},
        "scope": {"allowPaths": ALLOW_PATHS, "denyPaths": [".marshal/**"], "allowSubmodules": False,
                  "maxChangedFiles": 3, "maxDiffBytes": 120000},
        "acceptance": {
            "allowNoChange": False,
            "commands": [
                {"id": "json-syntax", "argv": ["/usr/bin/python3", "-I", "-B", "-c", JSON_SYNTAX_CHECK],
                 "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 4000, "required": True, "baselinePolicy": "none"},
                {"id": "taskspec-validate",
                 "argv": ["sh", "-c",
                          "cat schemas/examples/goal-lite/walking-skeleton.tasks.json | "
                          + args.marshal_bin + " contract validate --stdin >/dev/null 2>&1 && echo contract-ok"],
                 "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 4000, "required": True, "baselinePolicy": "none"},
            ],
        },
        "deliverables": [
            {"id": "design-doc", "kind": "documentation", "required": True,
             "pathGlob": "docs/m13-goal-lite-walking-skeleton.md", "minimumCount": 1},
            {"id": "examples", "kind": "documentation", "required": True,
             "pathGlob": "schemas/examples/goal-lite/*.json", "minimumCount": 2},
        ],
        "budgets": {"maxAttempts": 2, "maxOperationalRetries": 0, "maxReworkRounds": 1,
                    "maxOutputBytes": 8388608, "attemptTimeoutSeconds": 900, "runTimeoutSeconds": 1800},
        "publication": {"required": False, "provider": "none", "mode": "none", "remote": "origin",
                        "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []},
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
        "control": {"autonomyProfile": "supervised", "requiredApprovals": ["plan"],
                    "allowMediatedSteering": False, "directPtyPolicy": "deny", "maxSteeringRounds": 0},
        "environmentBinding": binding,
        "policyDigest": "",
        "generatedAt": iso_now(),
    }
    detached = canonical_bytes(policy)
    policy["policyDigest"] = "sha256:" + hashlib.sha256(detached).hexdigest()
    for path, value in ((args.task_out, task), (args.policy_out, policy)):
        with open(path, "w", encoding="utf-8") as handle:
            handle.write(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
            handle.write("\n")
    print(f"[e2e-m13] task -> {args.task_out}")
    print(f"[e2e-m13] policy -> {args.policy_out} digest={policy['policyDigest']}")


def render_decision(args):
    with open(args.packet, encoding="utf-8") as handle:
        packet = json.load(handle)
    packet_digest = canonical_file_digest(args.packet)
    decision = {
        "apiVersion": "marshal.dev/v1alpha1",
        "kind": "ReviewDecision",
        "taskId": args.task_id,
        "runId": args.run_id,
        "reviewRound": packet["reviewRound"],
        "reviewer": {"type": "lead-agent", "id": args.reviewer_id},
        "specDigest": packet["specDigest"],
        "reviewPacketDigest": packet_digest,
        "verificationDigest": packet["verificationDigest"],
        "artifactManifestDigest": packet["artifactManifestDigest"],
        "evidenceDigest": packet["evidenceDigest"],
        "verdict": "accept",
        "summary": args.summary,
        "blockingFindings": [],
        "nonBlockingFindings": [],
        "publicationRecommendation": "not-applicable",
        "mergeRecommendation": "do-not-merge",
        "decidedAt": iso_now(),
    }
    binding = packet.get("localSelfIdentityBinding")
    if binding is not None:
        decision["localSelfIdentityBindingDigest"] = "sha256:" + hashlib.sha256(canonical_bytes(binding)).hexdigest()
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(decision, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    print(f"[e2e-m13] decision -> {args.out} packetDigest={packet_digest}")


def metrics(args):
    runs_root = os.path.join(args.state_root, "runs", args.run_id)
    with open(os.path.join(runs_root, "state.json"), encoding="utf-8") as handle:
        state = json.load(handle)
    timeline = []
    events_path = os.path.join(runs_root, "events.jsonl")
    if os.path.exists(events_path):
        for line in open(events_path, encoding="utf-8"):
            record = json.loads(line)
            timeline.append({"sequence": record.get("sequence"), "type": record.get("type"),
                             "from": record.get("stateFrom"), "to": record.get("stateTo"),
                             "at": record.get("timestamp")})
    input_tokens = output_tokens = 0
    attempts_root = os.path.join(runs_root, "attempts")
    attempt_names = []
    if os.path.isdir(attempts_root):
        attempt_names = sorted(name for name in os.listdir(attempts_root)
                               if os.path.isdir(os.path.join(attempts_root, name)))
    for name in attempt_names:
        out_dir = os.path.join(attempts_root, name, "control", "output")
        worker_result = os.path.join(out_dir, "worker-result.json")
        if os.path.exists(worker_result):
            with open(worker_result, encoding="utf-8") as handle:
                parsed = json.load(handle)
            usage = parsed.get("usage") or {}
            if usage.get("input") or usage.get("output"):
                input_tokens += int(usage.get("input") or 0)
                output_tokens += int(usage.get("output") or 0)
                continue
        if os.path.isdir(out_dir):
            for meta in sorted(os.listdir(out_dir)):
                if not meta.endswith(".json"):
                    continue
                with open(os.path.join(out_dir, meta), encoding="utf-8") as handle:
                    try:
                        parsed = json.load(handle)
                    except json.JSONDecodeError:
                        continue
                for in_key, out_key in (("inputTokens", "outputTokens"), ("input_tokens", "output_tokens")):
                    if in_key in parsed or out_key in parsed:
                        input_tokens += int(parsed.get(in_key) or 0)
                        output_tokens += int(parsed.get(out_key) or 0)
                        break
                else:
                    continue
                break
    wall_clock_seconds = None
    if args.wall_start:
        start = datetime.datetime.strptime(args.wall_start, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
        wall_clock_seconds = round((datetime.datetime.now(datetime.timezone.utc) - start).total_seconds(), 1)
    result = {
        "runId": args.run_id,
        "taskId": state.get("taskId"),
        "finalState": state.get("state"),
        "attemptsUsed": state.get("attemptsUsed"),
        "attemptDirs": attempt_names,
        "inputTokens": input_tokens,
        "outputTokens": output_tokens,
        "wallClockSeconds": wall_clock_seconds,
        "timeline": timeline,
    }
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(result, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    render = sub.add_parser("render-task")
    render.add_argument("--doctor", required=True)
    render.add_argument("--repo-root", required=True)
    render.add_argument("--base-ref", required=True)
    render.add_argument("--task-id", required=True)
    render.add_argument("--run-id", required=True)
    render.add_argument("--model", required=True)
    render.add_argument("--marshal-bin", required=True)
    render.add_argument("--task-out", required=True)
    render.add_argument("--policy-out", required=True)
    render.set_defaults(handler=render_task)

    decision = sub.add_parser("render-decision")
    decision.add_argument("--packet", required=True)
    decision.add_argument("--task-id", required=True)
    decision.add_argument("--run-id", required=True)
    decision.add_argument("--reviewer-id", required=True)
    decision.add_argument("--summary", required=True)
    decision.add_argument("--out", required=True)
    decision.set_defaults(handler=render_decision)

    metric = sub.add_parser("metrics")
    metric.add_argument("--state-root", required=True)
    metric.add_argument("--run-id", required=True)
    metric.add_argument("--wall-start", default="")
    metric.add_argument("--out", required=True)
    metric.set_defaults(handler=metrics)

    args = parser.parse_args()
    args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
