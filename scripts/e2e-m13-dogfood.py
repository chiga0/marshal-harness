#!/usr/bin/env python3
"""M13 GoalLite walking-skeleton dogfood candidate evidence driver.

子命令：
  render-task     按运行环境布局渲染 TaskSpec + PolicySnapshot（policyDigest 按
                  JCS canonical JSON 空占位摘要重算）。
  assert-worktree-private 证明任务 worktree root 与 git admin dir 恰好为 0700。
  render-candidate-evidence 冻结 candidate sourceHead 与 SHA256。
  validate-evidence 在 Decision 前独立核对交付物、ReviewPacket 与 raw transcript。
  render-decision 从 review-packet.json 提取全部绑定摘要，渲染独立
                  ReviewDecision（verdict=accept）。
  stage-evidence  仅暂存显式 allowlist 中的关闭证据并生成摘要 manifest。
  archive-evidence 从已校验 staging tree 生成不含 owner-control auth material 的归档。
  verify-evidence-archive 独立复核归档 member allowlist、摘要与大小。
  failure-diagnostic 在闭环失败时输出无 secret 的阶段摘要，不复制原始 transcript。
  metrics         摘要 current Attempt 的 token 用量与时间线。

只读取显式传入的文件；不回显任何 secret 值。
"""
import argparse
import datetime
import hashlib
import io
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tarfile

M13_FINAL_PREFIX = "交付已完成，以下为唯一 WorkerResult：\n"
M13_OBJECTIVE = "产出 M13（Goal orchestration）首个工程纵切的完整落地材料（walking skeleton：simple prompt → approved proposal → one real Task）。只允许创建以下三个文件，不允许创建、修改或删除任何其它文件：\n1) `docs/m13-goal-lite-walking-skeleton.md`【中文】：描述在已确定的 Deterministic Control Plane 与 Goal admission 约束下首个产品纵切的设计。内容必须包含：walking skeleton 的目标与 Actor Flow（UserPrompt → Goal admission → DeliveryProposal → UserApproval → WorkGraph → one real Task → Integration → Outcome）、首期只允许 ONE Goal + ONE real Task 的领域范围声明、与 ADR 0019 的 plan admission、deterministic admission、revision 语义对应关系表、首批 Outcome 必须捕捉的证据面（independent Verification、reviewDigest、attemptAuthority 形成的路径）、以及下一切片序（不超过 3 个并发节点 + Integration Node）。\n2) `schemas/examples/goal-lite/approved-proposal.example.json`：一份与文档语义对应的 DeliveryProposal 样例。严格使用封闭字段集，禁止增删字段：{\"apiVersion\":\"marshal.dev/v1alpha1\",\"kind\":\"DeliveryProposal\",\"metadata\":{\"id\":\"goal-lite-approved-001\",\"title\":\"...\"},\"goal\":{\"prompt\":\"...\",\"objective\":\"...\",\"budgets\":{\"maxAttempts\":2,\"maxReworkRounds\":1,\"maxOutputBytes\":8388608}},\"userApproval\":{\"round\":1,\"actor\":\"operator:demo\",\"decision\":\"accept\",\"at\":\"<RFC3339 UTC>\"},\"workGraph\":{\"nodes\":[{\"id\":\"task-only-1\",\"kind\":\"task\",\"taskSpecDigest\":\"sha256:<64hex>\",\"dependencies\":[]}],\"integration\":{\"type\":\"collect\"}},\"outcome\":{\"state\":\"proposed\"}}。\n3) `schemas/examples/goal-lite/walking-skeleton.tasks.json`：一份与样例 proposal 对应、与现行 task-spec.schema.json 契约一致的 walking skeleton TaskSpec 实例，以该 proposal 下单一真实 Task 的完全相同语义填写（work.objective 与合作者出面背景也参考 walking skeleton 首期语气），work/worker/scope/acceptance/deliverables/budgets 均须可直接被 `marshal contract validate --stdin` 通过。\n要求：\n- 全部面向人的文字一律用中文；协议字段、CLI 命令、schema key、标识符保留英文；\n- 术语、状态名、Allowed milestone 词汇必须与 docs/roadmap-status.md 一致；\n- 不得宣称 M13 production 完成、不得宣称 v1 stable 支持；\n- 最终 assistant 的最终回复必须逐字以固定中文前缀“交付已完成，以下为唯一 WorkerResult：”加换行开始，紧接恰好一个完整 WorkerResult JSON 对象；该对象之后到回复结尾只能有空白，不得有 Markdown fence、解释或第二个 JSON 对象。"

M13_CONSTRAINTS = [
    "只允许创建 docs/m13-goal-lite-walking-skeleton.md、schemas/examples/goal-lite/approved-proposal.example.json、schemas/examples/goal-lite/walking-skeleton.tasks.json 三个文件；",
    "文档与样例只可使用中文（schema key、CLI 命令、标识符保留英文）；",
    "不得改动仓库其它任何文件；不得使用任何第三方依赖；不得提交、推送、创建 git 引用或访问网络。",
    "最终 assistant text 必须逐字以“交付已完成，以下为唯一 WorkerResult：”加换行开始，其后恰好一个完整 WorkerResult JSON object 并只允许空白尾部；",
]

ALLOW_PATHS = [
    "docs/m13-goal-lite-walking-skeleton.md",
    "schemas/examples/goal-lite/approved-proposal.example.json",
    "schemas/examples/goal-lite/walking-skeleton.tasks.json",
]

DIGEST_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
SOURCE_HEAD_PATTERN = re.compile(r"^[0-9a-f]{40}$")

EVIDENCE_MEMBERS = frozenset({
    "control/candidate-evidence.json",
    "control/worktree-private-check.json",
    "control/evidence-check.json",
    "control/decision.json",
    "control/metrics.json",
    "run/state.json",
    "run/verification-report.json",
    "run/artifact-manifest.json",
    "run/review-packet.json",
    "attempt/worker-result.json",
    "supervisor/stdout.bin",
    "supervisor/transcript.jcs",
    "journal/digests.json",
    "manifest.json",
})
REQUIRED_EVIDENCE_MEMBERS = frozenset({
    "control/candidate-evidence.json",
    "control/evidence-check.json",
    "run/state.json",
    "run/review-packet.json",
    "attempt/worker-result.json",
    "supervisor/stdout.bin",
    "supervisor/transcript.jcs",
    "journal/digests.json",
    "manifest.json",
})
EVIDENCE_MEMBER_LIMIT = 32 << 20

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


def load_object(path, label):
    with open(path, encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{label} 必须是 JSON object")
    return value


def positive_worker_usage(worker_result, label):
    usage = worker_result.get("usage")
    if not isinstance(usage, dict):
        raise ValueError(f"{label} 缺少 usage")
    input_tokens = usage.get("inputTokens")
    output_tokens = usage.get("outputTokens")
    if (isinstance(input_tokens, bool) or not isinstance(input_tokens, int) or input_tokens <= 0 or
            isinstance(output_tokens, bool) or not isinstance(output_tokens, int) or output_tokens <= 0):
        raise ValueError(f"{label} 要求 usage.inputTokens/outputTokens 均大于 0")
    return input_tokens, output_tokens


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def require_private_directory(path, label):
    info = os.lstat(path)
    if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise ValueError(f"{label} 不是真实目录")
    if stat.S_IMODE(info.st_mode) != 0o700 or info.st_uid != os.geteuid():
        raise ValueError(f"{label} 必须属于当前 euid 且 mode 恰好为 0700")


def assert_worktree_private(args):
    state = load_object(os.path.join(args.state_root, "runs", args.run_id, "state.json"), "RunState")
    if state.get("runId") != args.run_id or state.get("state") != "READY":
        raise ValueError("Run 未在创建 worktree 后稳定处于 READY")
    worktree = state.get("worktreePath")
    if not isinstance(worktree, str) or not os.path.isabs(worktree) or os.path.normpath(worktree) != worktree:
        raise ValueError("RunState.worktreePath 不是干净绝对路径")
    require_private_directory(worktree, "task worktree root")
    dot_git = os.path.join(worktree, ".git")
    info = os.lstat(dot_git)
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise ValueError("task worktree .git 必须是普通文件")
    with open(dot_git, encoding="utf-8") as handle:
        marker = handle.read(4096).strip()
    if not marker.startswith("gitdir: "):
        raise ValueError("task worktree .git 缺少 gitdir 指向")
    admin = marker[len("gitdir: "):]
    if not os.path.isabs(admin):
        admin = os.path.join(worktree, admin)
    admin = os.path.normpath(admin)
    require_private_directory(admin, "git admin dir")
    resolved = subprocess.run(
        ["git", "-C", worktree, "rev-parse", "--path-format=absolute", "--git-dir"],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    ).stdout.strip()
    if os.path.normpath(resolved) != admin:
        raise ValueError(".git 指向与 git 观测的 admin dir 不一致")
    print(json.dumps({"runId": args.run_id, "worktreeMode": "0700", "gitAdminMode": "0700"},
                     ensure_ascii=False, sort_keys=True))


def render_candidate_evidence(args):
    if args.candidate_mode not in ("published-rc1", "build-from-head"):
        raise ValueError("candidate mode 非法")
    if not SOURCE_HEAD_PATTERN.fullmatch(args.source_head):
        raise ValueError("candidate sourceHead 非法")
    candidate = os.path.realpath(args.candidate)
    if not os.path.isfile(candidate) or os.path.islink(args.candidate):
        raise ValueError("candidate 必须是非符号链接普通文件")
    digest = sha256_file(candidate)
    if args.expected_sha256 and digest != args.expected_sha256:
        raise ValueError("candidate SHA256 与已观测值不一致")
    evidence = {
        "schemaVersion": "marshal.m13-candidate-evidence.v1",
        "candidateMode": args.candidate_mode,
        "sourceHead": args.source_head,
        "candidateSHA256": digest,
        "closureEligible": args.candidate_mode == "build-from-head",
        "publishedRC1ContainsThisFix": False,
    }
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(evidence, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    print(f"[e2e-m13] candidate evidence -> {args.out} sourceHead={args.source_head} sha256={digest}")


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
                 "argv": ["marshal-builtin:contract-task-spec:v1",
                          "deliverable:walking-skeleton-task-spec"],
                 "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 4000, "required": True, "baselinePolicy": "none"},
            ],
        },
        "deliverables": [
            {"id": "design-doc", "kind": "documentation", "required": True,
             "pathGlob": "docs/m13-goal-lite-walking-skeleton.md", "minimumCount": 1},
            {"id": "examples", "kind": "documentation", "required": True,
             "pathGlob": "schemas/examples/goal-lite/*.json", "minimumCount": 2},
            {"id": "walking-skeleton-task-spec", "kind": "documentation", "required": True,
             "pathGlob": "schemas/examples/goal-lite/walking-skeleton.tasks.json", "minimumCount": 1},
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
    packet = load_object(args.packet, "ReviewPacket")
    packet_digest = canonical_file_digest(args.packet)
    evidence_check = load_object(args.evidence_check, "M13 evidence check")
    if evidence_check.get("schemaVersion") != "marshal.m13-evidence-check.v1":
        raise ValueError("M13 evidence check schemaVersion 非法")
    if evidence_check.get("taskId") != args.task_id or evidence_check.get("runId") != args.run_id:
        raise ValueError("M13 evidence check 与 Decision identity 不一致")
    if evidence_check.get("closureEligible") is not True:
        raise ValueError("只有 build-from-head candidate 可产生 ADR 0075 关闭 Decision")
    if evidence_check.get("reviewPacketDigest") != packet_digest:
        raise ValueError("ReviewPacket 在独立检查后发生漂移")
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


def changed_paths(worktree):
    completed = subprocess.run(
        ["git", "-C", worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all"],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
    )
    entries = completed.stdout.split(b"\0")
    paths = []
    for entry in entries:
        if not entry:
            continue
        if len(entry) < 4 or entry[2:3] != b" ":
            raise ValueError("git status porcelain 形状非法")
        status_code = entry[:2]
        if b"R" in status_code or b"C" in status_code:
            raise ValueError("M13 交付不允许 rename/copy")
        paths.append(entry[3:].decode("utf-8"))
    return paths


def control_stdout_path(state_root, task_id, run_id, attempt_id):
    state_root = os.path.realpath(state_root)
    ledger_path = os.path.join(state_root, "result-ingress", "result-ingress.jsonl")
    candidates = set()
    with open(ledger_path, encoding="utf-8") as handle:
        for line in handle:
            if not line.strip():
                continue
            record = json.loads(line)
            transition = record.get("transition")
            if not isinstance(transition, dict):
                continue
            identity = transition.get("identity")
            started = transition.get("supervisorStarted")
            if not isinstance(identity, dict) or not isinstance(started, dict):
                continue
            if (identity.get("taskId"), identity.get("runId"), identity.get("attemptId")) != (task_id, run_id, attempt_id):
                continue
            control = started.get("controlDirectory")
            if isinstance(control, dict) and isinstance(control.get("canonicalPath"), str):
                candidates.add(control["canonicalPath"])
    if len(candidates) != 1:
        raise ValueError(f"当前 Attempt 必须恰好绑定一个 owner-control 目录，实际 {len(candidates)}")
    control = os.path.normpath(next(iter(candidates)))
    owner_root = os.path.normpath(os.path.join(state_root, "owner-control"))
    if not os.path.isabs(control) or os.path.commonpath((control, owner_root)) != owner_root or control == owner_root:
        raise ValueError("owner-control 目录越出 state root")
    require_private_directory(control, "owner-control Attempt dir")
    stdout_path = os.path.join(control, "stdout.bin")
    info = os.lstat(stdout_path)
    if (not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.geteuid() or
            stat.S_IMODE(info.st_mode) != 0o600 or info.st_size <= 0 or info.st_size > 32 << 20):
        raise ValueError("owner-control stdout.bin 不是有界普通文件")
    return stdout_path


def final_agent_end_result(stdout_path, task_id, run_id, attempt_id):
    agent_ends = []
    with open(stdout_path, "rb") as handle:
        for raw_line in handle:
            if not raw_line.strip():
                continue
            event = json.loads(raw_line)
            if isinstance(event, dict) and event.get("type") == "agent_end":
                agent_ends.append(event)
    if not agent_ends:
        raise ValueError("owner-control stdout 不含 agent_end")
    event = agent_ends[-1]
    if event.get("willRetry") is not False:
        raise ValueError("最后 agent_end 不是 willRetry=false")
    messages = event.get("messages")
    if not isinstance(messages, list) or not messages or not isinstance(messages[-1], dict):
        raise ValueError("最后 agent_end 不含终态 message")
    message = messages[-1]
    if message.get("role") != "assistant" or not isinstance(message.get("content"), list):
        raise ValueError("最后 agent_end 末 message 不是 assistant")
    texts = []
    for item in message["content"]:
        if not isinstance(item, dict) or item.get("type") not in ("thinking", "text"):
            raise ValueError("最后 assistant 包含非 thinking/text content")
        if item.get("type") == "text":
            texts.append(item.get("text"))
    if len(texts) != 1 or not isinstance(texts[0], str) or not texts[0].startswith(M13_FINAL_PREFIX):
        raise ValueError("最后 assistant 必须只有一个 text 且以固定中文前缀开始")
    body = texts[0][len(M13_FINAL_PREFIX):]
    if not body.startswith("{"):
        raise ValueError("固定中文前缀后必须立即开始 WorkerResult JSON object")
    decoder = json.JSONDecoder()
    result, end = decoder.raw_decode(body)
    if not isinstance(result, dict) or body[end:].strip():
        raise ValueError("固定前缀后必须恰好一个完整 JSON object 且尾部只有空白")
    if result.get("kind") != "WorkerResult" or (result.get("taskId"), result.get("runId"), result.get("attemptId")) != (task_id, run_id, attempt_id):
        raise ValueError("原始终态 WorkerResult identity 与当前 Attempt 不一致")
    return result


def validate_evidence(args):
    run_root = os.path.join(args.state_root, "runs", args.run_id)
    state = load_object(os.path.join(run_root, "state.json"), "RunState")
    if state.get("taskId") != args.task_id or state.get("runId") != args.run_id or state.get("state") != "REVIEW_PENDING":
        raise ValueError("evidence check 只接受当前 REVIEW_PENDING Run")
    attempt_id = state.get("currentAttemptId")
    worktree = state.get("worktreePath")
    if not isinstance(attempt_id, str) or not attempt_id or not isinstance(worktree, str):
        raise ValueError("RunState 缺少 Attempt/worktree identity")
    observed_paths = changed_paths(worktree)
    if len(observed_paths) != len(set(observed_paths)) or set(observed_paths) != set(ALLOW_PATHS):
        raise ValueError(f"工作树必须恰好交付三个目标文件，实际 {sorted(observed_paths)}")
    deliverables = {}
    for relative in ALLOW_PATHS:
        path = os.path.join(worktree, relative)
        info = os.lstat(path)
        if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_size <= 0:
            raise ValueError(f"交付物必须是非空普通文件：{relative}")
        if relative.endswith(".json"):
            load_object(path, relative)
        deliverables[relative] = "sha256:" + sha256_file(path)

    packet = load_object(args.packet, "ReviewPacket")
    if (packet.get("apiVersion"), packet.get("kind"), packet.get("taskId"), packet.get("runId")) != (
            "marshal.dev/v1alpha1", "ReviewPacket", args.task_id, args.run_id):
        raise ValueError("ReviewPacket identity 非法")
    for field in ("specDigest", "snapshotDigest", "diffDigest", "verificationDigest", "artifactManifestDigest", "evidenceDigest"):
        if not isinstance(packet.get(field), str) or not DIGEST_PATTERN.fullmatch(packet[field]):
            raise ValueError(f"ReviewPacket.{field} 缺失或非法")
    if packet.get("specDigest") != state.get("specDigest") or packet.get("baseSha") != state.get("baseSha"):
        raise ValueError("ReviewPacket specDigest/baseSha 与当前 RunState 不一致")
    if not isinstance(packet.get("reviewRound"), int) or packet["reviewRound"] < 1:
        raise ValueError("ReviewPacket.reviewRound 非法")
    expected_result = f"attempts/{attempt_id}/worker-result.json"
    inputs = packet.get("inputs")
    if not isinstance(inputs, dict) or inputs.get("workerResults") != [expected_result]:
        raise ValueError("ReviewPacket 未精确引用当前 Attempt WorkerResult")
    result_digests = packet.get("workerResultDigests")
    if not isinstance(result_digests, list) or len(result_digests) != 1 or not DIGEST_PATTERN.fullmatch(result_digests[0]):
        raise ValueError("ReviewPacket 必须恰好绑定一个 WorkerResult digest")

    stdout_path = control_stdout_path(args.state_root, args.task_id, args.run_id, attempt_id)
    declared = final_agent_end_result(stdout_path, args.task_id, args.run_id, attempt_id)
    worker_result_path = os.path.join(run_root, "attempts", attempt_id, "worker-result.json")
    normalized = load_object(worker_result_path, "attempt WorkerResult")
    if (normalized.get("taskId"), normalized.get("runId"), normalized.get("attemptId")) != (args.task_id, args.run_id, attempt_id):
        raise ValueError("attempt root WorkerResult identity 非法")
    input_tokens, output_tokens = positive_worker_usage(normalized, "current Attempt normalized WorkerResult")
    if result_digests[0] != canonical_file_digest(worker_result_path):
        raise ValueError("ReviewPacket WorkerResult digest 与 Attempt 根结果不一致")
    candidate = load_object(args.candidate_evidence, "candidate evidence")
    if candidate.get("schemaVersion") != "marshal.m13-candidate-evidence.v1" or candidate.get("candidateMode") != args.candidate_mode:
        raise ValueError("candidate evidence mode/schema 漂移")
    if candidate.get("sourceHead") != args.source_head or not SOURCE_HEAD_PATTERN.fullmatch(candidate.get("sourceHead", "")):
        raise ValueError("candidate evidence sourceHead 漂移")
    if not re.fullmatch(r"[0-9a-f]{64}", candidate.get("candidateSHA256", "")):
        raise ValueError("candidate evidence SHA256 非法")
    closure_eligible = candidate.get("closureEligible") is True and args.candidate_mode == "build-from-head"
    if candidate.get("publishedRC1ContainsThisFix") is not False:
        raise ValueError("candidate evidence 不得声称已发布 RC1 包含本修复")

    receipt = {
        "schemaVersion": "marshal.m13-evidence-check.v1",
        "taskId": args.task_id,
        "runId": args.run_id,
        "attemptId": attempt_id,
        "reviewPacketDigest": canonical_file_digest(args.packet),
        "candidateEvidenceDigest": canonical_file_digest(args.candidate_evidence),
        "closureEligible": closure_eligible,
        "rawTranscriptSHA256": "sha256:" + sha256_file(stdout_path),
        "workerResultSHA256": "sha256:" + sha256_file(worker_result_path),
        "workerResultUsage": {
            "attemptId": attempt_id,
            "authority": "attempt-root-normalized-worker-result",
            "inputTokens": input_tokens,
            "outputTokens": output_tokens,
        },
        "declaredWorkerResultDigest": "sha256:" + hashlib.sha256(canonical_bytes(declared)).hexdigest(),
        "deliverables": deliverables,
        "finalAssistantPrefix": M13_FINAL_PREFIX,
        "finalAssistantObjectCount": 1,
        "finalAssistantTrailingContent": "whitespace-only",
    }
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(receipt, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    print(f"[e2e-m13] independent evidence check -> {args.out} packetDigest={receipt['reviewPacketDigest']}")


def require_regular_evidence_source(path, label, limit=EVIDENCE_MEMBER_LIMIT):
    info = os.lstat(path)
    if (not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_uid != os.geteuid() or
            info.st_size < 0 or info.st_size > limit):
        raise ValueError(f"{label} 不是当前 euid 持有的有界普通文件")
    return info


def copy_evidence_member(source, stage_root, member):
    if member not in EVIDENCE_MEMBERS or member == "manifest.json":
        raise ValueError(f"evidence member 不在 allowlist：{member}")
    require_regular_evidence_source(source, member)
    target = os.path.join(stage_root, *member.split("/"))
    os.makedirs(os.path.dirname(target), mode=0o700, exist_ok=True)
    shutil.copyfile(source, target)
    os.chmod(target, 0o600)


def manifest_file_records(files):
    return [
        {"path": path, "sha256": sha256_file_from_bytes(files[path]), "bytes": len(files[path])}
        for path in sorted(files)
    ]


def sha256_file_from_bytes(data):
    return hashlib.sha256(data).hexdigest()


def validate_manifest_members(files):
    if "manifest.json" not in files:
        raise ValueError("evidence archive 缺少 manifest.json")
    manifest = json.loads(files["manifest.json"])
    if not isinstance(manifest, dict) or manifest.get("schemaVersion") != "marshal.m13-evidence-archive.v1":
        raise ValueError("evidence manifest schemaVersion 非法")
    records = manifest.get("files")
    if not isinstance(records, list):
        raise ValueError("evidence manifest.files 非法")
    described = {}
    for record in records:
        if not isinstance(record, dict) or set(record) != {"path", "sha256", "bytes"}:
            raise ValueError("evidence manifest file record 非法")
        path = record.get("path")
        if path in described or path not in EVIDENCE_MEMBERS or path == "manifest.json":
            raise ValueError("evidence manifest 包含重复或非 allowlist path")
        data = files.get(path)
        if (data is None or record.get("sha256") != sha256_file_from_bytes(data) or
                record.get("bytes") != len(data)):
            raise ValueError(f"evidence member digest/size 漂移：{path}")
        described[path] = record
    actual = set(files) - {"manifest.json"}
    if set(described) != actual or not REQUIRED_EVIDENCE_MEMBERS.issubset(set(files)):
        raise ValueError("evidence manifest 未精确覆盖 allowlisted members")
    return manifest


def tree_evidence_files(stage_root):
    stage_root = os.path.realpath(stage_root)
    if not os.path.isdir(stage_root):
        raise ValueError("evidence staging root 不存在")
    files = {}
    for current, directories, names in os.walk(stage_root, topdown=True, followlinks=False):
        for directory in directories:
            path = os.path.join(current, directory)
            if os.path.islink(path):
                raise ValueError("evidence staging 不允许 symlink directory")
        for name in names:
            path = os.path.join(current, name)
            relative = os.path.relpath(path, stage_root).replace(os.sep, "/")
            if relative not in EVIDENCE_MEMBERS:
                raise ValueError(f"evidence staging 出现非 allowlist member：{relative}")
            info = require_regular_evidence_source(path, relative)
            if stat.S_IMODE(info.st_mode) != 0o600:
                raise ValueError(f"evidence staging member mode 必须恰好为 0600：{relative}")
            with open(path, "rb") as handle:
                files[relative] = handle.read(EVIDENCE_MEMBER_LIMIT + 1)
            if len(files[relative]) > EVIDENCE_MEMBER_LIMIT:
                raise ValueError(f"evidence staging member 超界：{relative}")
    validate_manifest_members(files)
    return files


def stage_evidence(args):
    state_root = os.path.realpath(args.state_root)
    control_root = os.path.realpath(args.control_root)
    run_root = os.path.join(state_root, "runs", args.run_id)
    state = load_object(os.path.join(run_root, "state.json"), "RunState")
    attempt_id = state.get("currentAttemptId")
    if state.get("runId") != args.run_id or not isinstance(attempt_id, str) or not attempt_id:
        raise ValueError("evidence staging 缺少 current Run/Attempt identity")
    if os.path.lexists(args.out_dir):
        raise ValueError("evidence staging output 必须不存在")
    os.mkdir(args.out_dir, 0o700)
    stage_root = os.path.realpath(args.out_dir)

    required_sources = {
        "control/candidate-evidence.json": os.path.join(control_root, "candidate-evidence.json"),
        "control/evidence-check.json": os.path.join(control_root, "evidence-check.json"),
        "run/state.json": os.path.join(run_root, "state.json"),
        "run/review-packet.json": os.path.join(run_root, "review-packet.json"),
        "attempt/worker-result.json": os.path.join(run_root, "attempts", attempt_id, "worker-result.json"),
    }
    optional_sources = {
        "control/worktree-private-check.json": os.path.join(control_root, "worktree-private-check.json"),
        "control/decision.json": os.path.join(control_root, "decision.json"),
        "control/metrics.json": os.path.join(control_root, "metrics.json"),
        "run/verification-report.json": os.path.join(run_root, "verification-report.json"),
        "run/artifact-manifest.json": os.path.join(run_root, "artifact-manifest.json"),
    }
    stdout_path = control_stdout_path(state_root, state.get("taskId"), args.run_id, attempt_id)
    supervisor_root = os.path.dirname(stdout_path)
    required_sources["supervisor/stdout.bin"] = stdout_path
    required_sources["supervisor/transcript.jcs"] = os.path.join(supervisor_root, "transcript.jcs")
    for member, source in required_sources.items():
        copy_evidence_member(source, stage_root, member)
    for member, source in optional_sources.items():
        if os.path.exists(source):
            copy_evidence_member(source, stage_root, member)

    ledger_path = os.path.join(state_root, "result-ingress", "result-ingress.jsonl")
    events_path = os.path.join(run_root, "events.jsonl")
    require_regular_evidence_source(ledger_path, "ResultIngress ledger", 64 << 20)
    journal_digests = {
        "schemaVersion": "marshal.m13-journal-digests.v1",
        "resultIngress": {"sha256": sha256_file(ledger_path), "bytes": os.path.getsize(ledger_path)},
    }
    if os.path.exists(events_path):
        require_regular_evidence_source(events_path, "Run events", 64 << 20)
        journal_digests["runEvents"] = {"sha256": sha256_file(events_path), "bytes": os.path.getsize(events_path)}
    digest_path = os.path.join(stage_root, "journal", "digests.json")
    os.makedirs(os.path.dirname(digest_path), mode=0o700, exist_ok=True)
    with open(digest_path, "w", encoding="utf-8") as handle:
        json.dump(journal_digests, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    os.chmod(digest_path, 0o600)

    staged = {}
    for member in sorted(EVIDENCE_MEMBERS - {"manifest.json"}):
        path = os.path.join(stage_root, *member.split("/"))
        if os.path.isfile(path) and not os.path.islink(path):
            with open(path, "rb") as handle:
                staged[member] = handle.read(EVIDENCE_MEMBER_LIMIT + 1)
    manifest = {
        "schemaVersion": "marshal.m13-evidence-archive.v1",
        "runId": args.run_id,
        "attemptId": attempt_id,
        "files": manifest_file_records(staged),
    }
    manifest_path = os.path.join(stage_root, "manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as handle:
        json.dump(manifest, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    os.chmod(manifest_path, 0o600)
    tree_evidence_files(stage_root)
    print(f"[e2e-m13] staged allowlisted evidence -> {stage_root}")


def archive_evidence(args):
    files = tree_evidence_files(args.stage_dir)
    if os.path.lexists(args.out):
        raise ValueError("evidence archive output 必须不存在")
    with tarfile.open(args.out, "w:gz", format=tarfile.PAX_FORMAT) as archive:
        for member in sorted(files):
            data = files[member]
            info = tarfile.TarInfo(member)
            info.size = len(data)
            info.mode = 0o600
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            info.mtime = 0
            archive.addfile(info, io.BytesIO(data))
    os.chmod(args.out, 0o600)
    verify_evidence_archive_path(args.out)
    print(f"[e2e-m13] evidence archive -> {args.out}")


def verify_evidence_archive_path(path):
    require_regular_evidence_source(path, "evidence archive", 128 << 20)
    files = {}
    with tarfile.open(path, "r:gz") as archive:
        for member in archive.getmembers():
            if member.name in files or member.name not in EVIDENCE_MEMBERS or not member.isfile():
                raise ValueError(f"evidence archive 包含重复、非 regular 或非 allowlist member：{member.name}")
            if member.size < 0 or member.size > EVIDENCE_MEMBER_LIMIT:
                raise ValueError(f"evidence archive member 超界：{member.name}")
            extracted = archive.extractfile(member)
            if extracted is None:
                raise ValueError(f"evidence archive member 不可读：{member.name}")
            files[member.name] = extracted.read(EVIDENCE_MEMBER_LIMIT + 1)
    validate_manifest_members(files)
    return files


def verify_evidence_archive(args):
    files = verify_evidence_archive_path(args.archive)
    print(json.dumps({"status": "ok", "memberCount": len(files), "members": sorted(files)},
                     ensure_ascii=False, sort_keys=True))


def safe_digest_file(path, limit=64 << 20):
    info = require_regular_evidence_source(path, "diagnostic source", limit)
    return {"present": True, "bytes": info.st_size, "sha256": "sha256:" + sha256_file(path)}


def final_text_shape(stdout_path, task_id, run_id, attempt_id):
    event_counts = {}
    final = None
    known_event_types = frozenset({
        "session", "agent_start", "agent_end", "agent_settled", "turn_start", "turn_end",
        "message_start", "message_update", "message_end", "tool_execution_start",
        "tool_execution_update", "tool_execution_end", "auto_retry_start", "auto_retry_end",
        "compaction_start", "compaction_end", "summarization_retry_scheduled",
        "summarization_retry_attempt_start", "summarization_retry_finished",
    })
    with open(stdout_path, "rb") as handle:
        for raw_line in handle:
            if not raw_line.strip():
                continue
            try:
                event = json.loads(raw_line)
            except (UnicodeDecodeError, json.JSONDecodeError):
                return {"status": "invalid-jsonl"}
            if not isinstance(event, dict) or not isinstance(event.get("type"), str):
                return {"status": "invalid-event-shape"}
            event_type = event["type"]
            count_key = event_type if event_type in known_event_types else "unsupported"
            event_counts[count_key] = event_counts.get(count_key, 0) + 1
            if event_type == "agent_end" and event.get("willRetry") is False:
                final = event
    summary = {"status": "missing-final-agent-end", "eventCounts": event_counts}
    if final is None:
        return summary
    messages = final.get("messages")
    if not isinstance(messages, list) or not messages or not isinstance(messages[-1], dict):
        summary["status"] = "invalid-final-messages"
        return summary
    message = messages[-1]
    content = message.get("content")
    if message.get("role") != "assistant" or not isinstance(content, list):
        summary["status"] = "invalid-final-assistant"
        return summary
    content_types = []
    texts = []
    for item in content:
        if not isinstance(item, dict) or not isinstance(item.get("type"), str):
            summary["status"] = "invalid-final-content"
            return summary
        content_types.append(item["type"] if item["type"] in ("thinking", "text") else "unsupported")
        if item["type"] == "text":
            texts.append(item.get("text"))
    summary.update({"contentTypes": content_types, "textItemCount": len(texts)})
    if len(texts) != 1 or not isinstance(texts[0], str) or not texts[0].strip():
        summary["status"] = "invalid-text-count"
        return summary
    text = texts[0]
    decoder = json.JSONDecoder()
    matches = []
    skip_until = 0
    for index, character in enumerate(text):
        if character != "{" or index < skip_until:
            continue
        try:
            value, consumed = decoder.raw_decode(text[index:])
        except json.JSONDecodeError:
            continue
        if not isinstance(value, dict):
            continue
        end = index + consumed
        skip_until = max(skip_until, end)
        matches.append((index, end, value))
    summary["completeObjectCount"] = len(matches)
    summary["m13PrefixExact"] = text.startswith(M13_FINAL_PREFIX)
    if len(matches) != 1:
        summary["status"] = "object-count-mismatch"
        return summary
    start, end, value = matches[0]
    summary.update({
        "status": "valid-single-object" if not text[end:].strip() else "trailing-content",
        "prosePrefixPresent": bool(text[:start].strip()),
        "trailingNonWhitespace": bool(text[end:].strip()),
        "workerResultKind": value.get("kind") == "WorkerResult",
        "identityExact": (value.get("taskId"), value.get("runId"), value.get("attemptId")) ==
                         (task_id, run_id, attempt_id),
    })
    return summary


def failure_diagnostic(args):
    state_root = os.path.realpath(args.state_root)
    run_root = os.path.join(state_root, "runs", args.run_id)
    state = load_object(os.path.join(run_root, "state.json"), "RunState")
    if state.get("runId") != args.run_id:
        raise ValueError("failure diagnostic Run identity 漂移")
    ledger_path = os.path.join(state_root, "result-ingress", "result-ingress.jsonl")
    require_regular_evidence_source(ledger_path, "ResultIngress ledger", 64 << 20)
    transitions = []
    attempt_ids = []
    known_transition_kinds = frozenset({
        "attempt-reserved", "attempt-opened", "control-owner-bound",
        "existing-worktree-bind-intent", "existing-worktree-bind-receipt",
        "launch-authorized", "process-supervisor-bootstrap-prepared",
        "process-supervisor-started", "process-started", "result-admitted",
        "terminalization-barrier", "process-terminal", "existing-worktree-release-intent",
        "existing-worktree-release-receipt", "allocation-terminated",
        "process-supervisor-closed", "cleanup-completed", "cleanup-released",
        "process-supervisor-intervention-required",
    })
    known_fact_types = known_transition_kinds | frozenset({"result-admitted", "result-quarantined"})
    with open(ledger_path, encoding="utf-8") as handle:
        for line in handle:
            if not line.strip():
                continue
            record = json.loads(line)
            transition = record.get("transition")
            if not isinstance(transition, dict):
                continue
            identity = transition.get("identity")
            if not isinstance(identity, dict) or identity.get("runId") != args.run_id:
                continue
            attempt_id = identity.get("attemptId")
            if isinstance(attempt_id, str) and attempt_id and attempt_id not in attempt_ids:
                attempt_ids.append(attempt_id)
            kind = transition.get("kind")
            fact_type = record.get("factType")
            transitions.append({
                "sequence": record.get("sequence"),
                "revision": record.get("revision"),
                "factType": fact_type if fact_type in known_fact_types else "unsupported",
                "kind": kind if kind in known_transition_kinds else "unsupported",
                "digest": record.get("digest") if isinstance(record.get("digest"), str) and
                          DIGEST_PATTERN.fullmatch(record["digest"]) else None,
            })
    current_attempt = state.get("currentAttemptId")
    if not isinstance(current_attempt, str):
        current_attempt = ""
    selected_attempt = current_attempt
    selection = "current-run-state"
    if not selected_attempt and len(attempt_ids) == 1:
        selected_attempt = attempt_ids[0]
        selection = "unique-result-ingress-attempt"
    elif not selected_attempt:
        selection = "unavailable-or-ambiguous"

    events = []
    known_run_event_types = frozenset({
        "run.transition", "state.transitioned", "planning.inputs-frozen",
        "planning.spec-accepted", "worker.started", "worker.progress",
        "worker.completed", "verification.completed", "review.accept",
        "publication.reconciled", "publication.merged", "run.aborted",
        "reconciliation.snapshot-repaired",
    })
    known_run_states = frozenset({
        "CREATED", "PLANNED", "READY", "RUNNING", "RETRY_PENDING",
        "VERIFYING", "REVIEW_PENDING", "REWORK_REQUESTED", "PUBLISHING",
        "PUBLISHED", "CI_PENDING", "ACCEPTED", "REJECTED", "BLOCKED",
        "ABORTED", "NO_CHANGE",
    })
    events_path = os.path.join(run_root, "events.jsonl")
    if os.path.exists(events_path):
        require_regular_evidence_source(events_path, "Run events", 64 << 20)
        with open(events_path, encoding="utf-8") as handle:
            for line in handle:
                if not line.strip():
                    continue
                event = json.loads(line)
                event_type = event.get("type")
                state_from = event.get("stateFrom")
                state_to = event.get("stateTo")
                events.append({
                    "sequence": event.get("sequence") if isinstance(event.get("sequence"), int) else None,
                    "type": event_type if event_type in known_run_event_types else "unsupported",
                    "stateFrom": state_from if state_from in known_run_states else None,
                    "stateTo": state_to if state_to in known_run_states else None,
                    "attemptMatchesSelected": bool(selected_attempt) and event.get("attemptId") == selected_attempt,
                })

    diagnostic = {
        "schemaVersion": "marshal.m13-failure-diagnostic.v1",
        "run": {key: state.get(key) for key in
                ("taskId", "runId", "state", "sequence", "currentAttemptId", "attemptsUsed",
                 "operationalRetriesUsed", "reworkRoundsUsed", "baseSha", "specDigest")},
        "attemptSelection": selection,
        "attemptId": selected_attempt,
        "attemptIdsObserved": attempt_ids,
        "resultIngressTransitions": transitions,
        "runEvents": events,
    }
    if selected_attempt:
        try:
            stdout_path = control_stdout_path(state_root, state.get("taskId"), args.run_id, selected_attempt)
        except (OSError, ValueError, json.JSONDecodeError):
            diagnostic["supervisor"] = {"status": "unavailable-or-invalid"}
        else:
            supervisor = safe_digest_file(stdout_path, 32 << 20)
            supervisor["status"] = "available"
            supervisor["finalAssistant"] = final_text_shape(
                stdout_path, state.get("taskId"), args.run_id, selected_attempt)
            transcript_path = os.path.join(os.path.dirname(stdout_path), "transcript.jcs")
            supervisor["transcriptJCS"] = (safe_digest_file(transcript_path, 32 << 20)
                                            if os.path.exists(transcript_path) else {"present": False})
            diagnostic["supervisor"] = supervisor
        worker_result_path = os.path.join(run_root, "attempts", selected_attempt, "worker-result.json")
        if os.path.exists(worker_result_path):
            worker_result = load_object(worker_result_path, "Attempt WorkerResult")
            diagnostic["workerResult"] = {
                **safe_digest_file(worker_result_path, 16 << 20),
                "kindExact": worker_result.get("kind") == "WorkerResult",
                "identityExact": (worker_result.get("taskId"), worker_result.get("runId"),
                                  worker_result.get("attemptId")) ==
                                 (state.get("taskId"), args.run_id, selected_attempt),
            }
        else:
            diagnostic["workerResult"] = {"present": False}
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(diagnostic, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
    os.chmod(args.out, 0o600)
    print(f"[e2e-m13] failure diagnostic -> {args.out}")


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
    attempt_id = state.get("currentAttemptId")
    if not isinstance(attempt_id, str) or not attempt_id:
        raise ValueError("metrics 缺少 currentAttemptId")
    worker_result = os.path.join(runs_root, "attempts", attempt_id, "worker-result.json")
    parsed = load_object(worker_result, "current Attempt WorkerResult")
    if (parsed.get("taskId"), parsed.get("runId"), parsed.get("attemptId")) != (state.get("taskId"), args.run_id, attempt_id):
        raise ValueError("metrics current Attempt WorkerResult identity 漂移")
    input_tokens, output_tokens = positive_worker_usage(parsed, "current Attempt WorkerResult")
    wall_clock_seconds = None
    if args.wall_start:
        start = datetime.datetime.strptime(args.wall_start, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
        wall_clock_seconds = round((datetime.datetime.now(datetime.timezone.utc) - start).total_seconds(), 1)
    if state.get("state") != "ACCEPTED":
        raise ValueError(f"metrics 只接受 ACCEPTED 终态，实际 {state.get('state')}")
    if wall_clock_seconds is None or wall_clock_seconds <= 0:
        raise ValueError("metrics 要求正值 wallClockSeconds")
    result = {
        "runId": args.run_id,
        "taskId": state.get("taskId"),
        "finalState": state.get("state"),
        "attemptsUsed": state.get("attemptsUsed"),
        "attemptId": attempt_id,
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
    decision.add_argument("--evidence-check", required=True)
    decision.add_argument("--out", required=True)
    decision.set_defaults(handler=render_decision)

    private = sub.add_parser("assert-worktree-private")
    private.add_argument("--state-root", required=True)
    private.add_argument("--run-id", required=True)
    private.set_defaults(handler=assert_worktree_private)

    candidate = sub.add_parser("render-candidate-evidence")
    candidate.add_argument("--candidate-mode", required=True)
    candidate.add_argument("--source-head", required=True)
    candidate.add_argument("--candidate", required=True)
    candidate.add_argument("--expected-sha256", default="")
    candidate.add_argument("--out", required=True)
    candidate.set_defaults(handler=render_candidate_evidence)

    evidence = sub.add_parser("validate-evidence")
    evidence.add_argument("--state-root", required=True)
    evidence.add_argument("--task-id", required=True)
    evidence.add_argument("--run-id", required=True)
    evidence.add_argument("--packet", required=True)
    evidence.add_argument("--candidate-evidence", required=True)
    evidence.add_argument("--candidate-mode", required=True)
    evidence.add_argument("--source-head", required=True)
    evidence.add_argument("--out", required=True)
    evidence.set_defaults(handler=validate_evidence)

    stage = sub.add_parser("stage-evidence")
    stage.add_argument("--state-root", required=True)
    stage.add_argument("--control-root", required=True)
    stage.add_argument("--run-id", required=True)
    stage.add_argument("--out-dir", required=True)
    stage.set_defaults(handler=stage_evidence)

    archive = sub.add_parser("archive-evidence")
    archive.add_argument("--stage-dir", required=True)
    archive.add_argument("--out", required=True)
    archive.set_defaults(handler=archive_evidence)

    verify_archive = sub.add_parser("verify-evidence-archive")
    verify_archive.add_argument("--archive", required=True)
    verify_archive.set_defaults(handler=verify_evidence_archive)

    failure = sub.add_parser("failure-diagnostic")
    failure.add_argument("--state-root", required=True)
    failure.add_argument("--run-id", required=True)
    failure.add_argument("--out", required=True)
    failure.set_defaults(handler=failure_diagnostic)

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
