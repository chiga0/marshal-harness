#!/usr/bin/env bash
# m13-e2e-dogfood workflow 的纯 shell 回归测试。它只执行输入解析与
# early-failure 指标收集片段，不构建 Marshal、不读取 secret、不启动 Worker。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
WORKFLOW="${SCRIPT_DIR}/../.github/workflows/m13-e2e-dogfood.yml"
DRIVER="${SCRIPT_DIR}/e2e-m13-dogfood.py"
TMP_RAW="$(mktemp -d "${TMPDIR:-/tmp}/m13-e2e-workflow-test.XXXXXX")"
TMP_ROOT="$(cd "$TMP_RAW" && pwd -P)"
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  printf '[m13-e2e-workflow-test] FAIL: %s\n' "$*" >&2
  exit 1
}

extract_step() {
  local name="$1" out="$2"
  awk -v wanted="$name" '
    $0 == "      - name: " wanted { found=1; next }
    found && /^        run: \|$/ { body=1; next }
    body && /^      - name:/ { exit }
    body { sub(/^          /, ""); print }
  ' "$WORKFLOW" >"$out"
  [ -s "$out" ] || fail "无法提取 workflow step：$name"
}

VALIDATE_RAW="${TMP_ROOT}/validate.raw.sh"
VALIDATE_SCRIPT="${TMP_ROOT}/validate.sh"
METRICS_SCRIPT="${TMP_ROOT}/metrics.sh"
PACK_SCRIPT="${TMP_ROOT}/pack.sh"
SUMMARY_SCRIPT="${TMP_ROOT}/summary.sh"
VERIFY_SCRIPT="${TMP_ROOT}/verify.sh"
PLAN_SCRIPT="${TMP_ROOT}/plan.sh"
DRIVE_SCRIPT="${TMP_ROOT}/drive.sh"
extract_step 'Validate inputs and resolve identities' "$VALIDATE_RAW"
# canonical repository 约束由 GitHub expression 注入；本测试只执行其后的
# 纯输入解析，且单独静态断言该约束未被移除。
grep -Fq 'test "${{ github.repository }}" = "chiga0/marshal-harness"' "$VALIDATE_RAW" \
  || fail 'canonical repository 输入门禁被移除'
sed '1d' "$VALIDATE_RAW" >"$VALIDATE_SCRIPT"
extract_step 'Extract token and time metrics' "$METRICS_SCRIPT"
extract_step 'Pack dogfood evidence tarball' "$PACK_SCRIPT"
extract_step 'Write metrics summary' "$SUMMARY_SCRIPT"
extract_step 'Run independent verification' "$VERIFY_SCRIPT"
extract_step 'Plan and approve the M13 run' "$PLAN_SCRIPT"
extract_step 'Drive the sealed run to worker completion' "$DRIVE_SCRIPT"

run_validate() {
  local pi_model="$1" models="$2" output="$3"
  : >"$output"
  /usr/bin/env -i \
    PATH=/usr/bin:/bin \
    PI_MODEL="$pi_model" \
    PI_DEFAULT_PROVIDER=pai-eas \
    VARS_MODELS="$models" \
    EXPECTED_CANDIDATE_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
    REVIEWER_ID=e2e-reviewer \
    INPUT_RUN_ID=e2e-run-1 \
    GITHUB_ENV="$output" \
    /bin/bash --noprofile --norc -euo pipefail "$VALIDATE_SCRIPT"
}

expect_validate_fail() {
  local description="$1" pi_model="$2" models="$3"
  if run_validate "$pi_model" "$models" "${TMP_ROOT}/failed.env" \
      >"${TMP_ROOT}/failed.out" 2>"${TMP_ROOT}/failed.err"; then
    fail "$description：预期失败但命令成功"
  fi
  ! grep -Fq 'unbound variable' "${TMP_ROOT}/failed.err" \
    || fail "$description：失败路径触发 shell 未绑定变量"
}

run_validate '' 'qwen3.8-max,qwen3.8-flash' "${TMP_ROOT}/default.env"
grep -Fxq 'PI_MODEL=pai-eas/qwen3.8-max' "${TMP_ROOT}/default.env" \
  || fail '裸 OPENAI_MODELS 未组合默认 provider/model'

run_validate 'pai-eas/qwen3.8-flash' 'qwen3.8-max,qwen3.8-flash' "${TMP_ROOT}/explicit.env"
grep -Fxq 'PI_MODEL=pai-eas/qwen3.8-flash' "${TMP_ROOT}/explicit.env" \
  || fail '合法显式 pi-model 未被保留'

expect_validate_fail '显式裸 model' 'qwen3.8-max' 'qwen3.8-max'
expect_validate_fail '显式未知 model' 'pai-eas/not-configured' 'qwen3.8-max'
expect_validate_fail '空 OPENAI_MODELS' '' ''
grep -Fq 'OPENAI_MODELS 必须是逗号分隔的裸 model id' "${TMP_ROOT}/failed.err" \
  || fail '空 OPENAI_MODELS 未返回确定性门禁错误'
expect_validate_fail 'OPENAI_MODELS 混入 provider' '' 'pai-eas/qwen3.8-max'

mkdir -p "${TMP_ROOT}/empty-workspace"
(
  cd "${TMP_ROOT}/empty-workspace"
  /usr/bin/env -i PATH=/usr/bin:/bin \
    /bin/bash --noprofile --norc -euo pipefail "$METRICS_SCRIPT"
) >"${TMP_ROOT}/metrics.out" 2>"${TMP_ROOT}/metrics.err" \
  || fail '输入门禁提前失败后 metrics always step 不应再次失败'
! grep -Fq 'unbound variable' "${TMP_ROOT}/metrics.err" \
  || fail 'metrics always step 仍触发 shell 未绑定变量'
grep -Fq 'run state 未生成' "${TMP_ROOT}/metrics.out" \
  || fail 'metrics early-failure 路径未给出明确跳过结果'

(
  cd "${TMP_ROOT}/empty-workspace"
  /usr/bin/env -i PATH=/usr/bin:/bin \
    /bin/bash --noprofile --norc -euo pipefail "$PACK_SCRIPT"
) >"${TMP_ROOT}/pack.out" 2>"${TMP_ROOT}/pack.err" \
  || fail '输入门禁提前失败后 evidence pack always step 不应再次失败'
! grep -Fq 'unbound variable' "${TMP_ROOT}/pack.err" \
  || fail 'evidence pack always step 触发 shell 未绑定变量'

printf '{}\n' >"${TMP_ROOT}/empty-workspace/.m13-e2e/metrics.json"
(
  cd "${TMP_ROOT}/empty-workspace"
  /usr/bin/env -i \
    PATH=/usr/bin:/bin \
    GITHUB_STEP_SUMMARY="${TMP_ROOT}/summary.md" \
    /bin/bash --noprofile --norc -euo pipefail "$SUMMARY_SCRIPT"
) >"${TMP_ROOT}/summary.out" 2>"${TMP_ROOT}/summary.err" \
  || fail '缺少 RUN_ID/CONTROL_ROOT 时 metrics summary 不应失败'
! grep -Fq 'unbound variable' "${TMP_ROOT}/summary.err" \
  || fail 'metrics summary 仍触发 shell 未绑定变量'
grep -Fq 'run id: `unresolved`' "${TMP_ROOT}/summary.md" \
  || fail 'metrics summary 未使用 early-failure fallback identity'

FAKE_MARSHAL="${TMP_ROOT}/fake-marshal"
cat >"$FAKE_MARSHAL" <<'EOF'
#!/bin/bash
set -euo pipefail
case "${1:-} ${2:-}" in
  'task verify')
    cat <<'JSON'
{"status":"private-report-status-SUPERPRIVATEVALUE","observed":{"changedFileCount":3,"diffBytes":417},"gates":[{"id":"unlabelled-secret-SUPERPRIVATEVALUE","category":"command","status":"pass","summary":"must not be logged","evidence":["secret-evidence"]},{"id":"AKIAIOSFODNN7EXAMPLE","category":"scope","status":"fail","summary":"dGhpcy1pcy1hLXNlY3JldC12YWx1ZS10aGF0LW11c3Qtbm90LWxlYWs=","evidence":["artifact://must-not-log"],"command":{"argv":["env"],"env":{"TOKEN":"must-not-log"}}},{"id":"raw-id-must-not-log","category":"private-category-SUPERPRIVATEVALUE","status":"private-status-SUPERPRIVATEVALUE","summary":"unlabelled SUPERPRIVATEVALUE","evidence":["artifact://must-not-log"]}]}
JSON
    printf 'raw verifier stderr must not be logged\n' >&2
    exit 42
    ;;
  'task status')
    printf '{"state":"REVIEW_PENDING"}\n'
    ;;
  *) exit 64 ;;
esac
EOF
chmod 700 "$FAKE_MARSHAL"
mkdir -p "${TMP_ROOT}/verify-control" "${TMP_ROOT}/runner-temp"
if /usr/bin/env -i \
    PATH=/usr/bin:/bin \
    NODE_ROOT_BIN=/usr/bin \
    MARSHAL_BIN="$FAKE_MARSHAL" \
    RUN_ID=e2e-run-1 \
    CONTROL_ROOT="${TMP_ROOT}/verify-control" \
    RUNNER_TEMP="${TMP_ROOT}/runner-temp" \
    /bin/bash --noprofile --norc -euo pipefail "$VERIFY_SCRIPT" \
    >"${TMP_ROOT}/verify.out" 2>"${TMP_ROOT}/verify.err"; then
  fail 'verification report 为 fail 时 workflow step 必须保持失败语义'
else
  verify_rc=$?
fi
[ "$verify_rc" -eq 42 ] || fail "verification step 未保留原始退出码：$verify_rc"
[ -s "${TMP_ROOT}/verify-control/verify.json" ] || fail '失败 VerificationReport 未保留'
[ -s "${TMP_ROOT}/verify-control/status-review-pending.json" ] || fail '失败后未采集 Run status'
[ -s "${TMP_ROOT}/runner-temp/m13-verify.stderr" ] || fail 'verifier stderr 未隔离保存'
[ ! -s "${TMP_ROOT}/verify.err" ] || fail 'verifier 原始 stderr 泄漏到 workflow 日志'
/usr/bin/python3 -I -B - "${TMP_ROOT}/verify.out" <<'PY' || fail '失败摘要字段或脱敏不符合契约'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    lines = [line for line in handle if line.strip()]
assert len(lines) == 1, lines
summary = json.loads(lines[0])
assert summary["reportStatus"] == "unavailable", summary
assert summary["observed"] == {"changedFileCount": 3, "diffBytes": 417}, summary
assert summary["failedGateCount"] == 2, summary
assert summary["failedGatesTruncated"] is False, summary
assert [(g["ordinal"], g["category"], g["status"]) for g in summary["failedGates"]] == [
    (2, "scope", "fail"),
    (3, "other", "error"),
], summary
assert set(summary["failedGates"][0]) == {"ordinal", "category", "status"}, summary
allowed_strings = {
    "pass", "fail", "error", "cancelled", "unavailable",
    "repository", "scope", "diff", "artifact", "command", "policy", "other",
}
def assert_closed(value):
    if isinstance(value, dict):
        for item in value.values():
            assert_closed(item)
    elif isinstance(value, list):
        for item in value:
            assert_closed(item)
    elif isinstance(value, str):
        assert value in allowed_strings, value
assert_closed(summary)
rendered = lines[0]
for forbidden in (
    "SUPERPRIVATEVALUE", "AKIAIOSFODNN7EXAMPLE",
    "dGhpcy1pcy1hLXNlY3JldC12YWx1ZS10aGF0LW11c3Qtbm90LWxlYWs=",
    "secret-evidence", "artifact://", "argv", "env", "TOKEN", "raw-id-must-not-log",
):
    assert forbidden not in rendered, (forbidden, rendered)
PY

# Driver 回归：Task objective 冻结固定中文前缀，并明确要求
# 前缀后恰好一个 JSON object + whitespace-only tail。
mkdir -p "${TMP_ROOT}/render"
printf '{"policyEnvironmentBinding":{"schemaVersion":"fixture"}}\n' >"${TMP_ROOT}/render/doctor.json"
/usr/bin/python3 -I -B "$DRIVER" render-task \
  --doctor "${TMP_ROOT}/render/doctor.json" --repo-root "${TMP_ROOT}/render" \
  --base-ref aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --task-id m13-task --run-id m13-run --model pai-eas/model \
  --marshal-bin /tmp/marshal --task-out "${TMP_ROOT}/render/task.json" \
  --policy-out "${TMP_ROOT}/render/policy.json" >/dev/null
/usr/bin/python3 -I -B - "${TMP_ROOT}/render/task.json" <<'PY' \
  || fail 'render-task 未冻结最终 assistant 形状'
import json, sys
work = json.load(open(sys.argv[1], encoding="utf-8"))["work"]
objective = work["objective"]
assert "交付已完成，以下为唯一 WorkerResult：" in objective
assert "紧接恰好一个完整 WorkerResult JSON 对象" in objective
assert "对象之后到回复结尾只能有空白" in objective
assert any("恰好一个完整 WorkerResult JSON object" in item and "只允许空白尾部" in item for item in work["constraints"])
PY
printf 'candidate-bytes\n' >"${TMP_ROOT}/render/marshal"
candidate_sha="$(shasum -a 256 "${TMP_ROOT}/render/marshal" | awk '{print $1}')"
/usr/bin/python3 -I -B "$DRIVER" render-candidate-evidence \
  --candidate-mode build-from-head --source-head aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --candidate "${TMP_ROOT}/render/marshal" --expected-sha256 "$candidate_sha" \
  --out "${TMP_ROOT}/render/candidate.json" >/dev/null
/usr/bin/python3 -I -B - "${TMP_ROOT}/render/candidate.json" "$candidate_sha" <<'PY' \
  || fail 'candidate evidence 未冻结 sourceHead/SHA256 或错误宣称 published RC1'
import json, sys
c=json.load(open(sys.argv[1])); assert c["candidateMode"] == "build-from-head"; assert c["sourceHead"] == "a"*40; assert c["candidateSHA256"] == sys.argv[2]; assert c["closureEligible"] is True; assert c["publishedRC1ContainsThisFix"] is False
PY

# F1 harness 证据：不依赖当前 umask，直接检查 linked worktree
# root 与 git admin dir 恰好 0700，任一处 0755 均 fail closed。
PRIVATE_REPO="${TMP_ROOT}/private-repo"
PRIVATE_WORKTREE="${TMP_ROOT}/private-worktree"
PRIVATE_STATE="${TMP_ROOT}/private-state"
mkdir -p "$PRIVATE_REPO" "$PRIVATE_STATE/runs/private-run"
git -C "$PRIVATE_REPO" init -q -b main
git -C "$PRIVATE_REPO" config user.email fixture@example.invalid
git -C "$PRIVATE_REPO" config user.name Fixture
printf 'fixture\n' >"${PRIVATE_REPO}/README.md"
git -C "$PRIVATE_REPO" add README.md
git -C "$PRIVATE_REPO" commit -q -m fixture
git -C "$PRIVATE_REPO" worktree add -q --detach "$PRIVATE_WORKTREE" HEAD
PRIVATE_ADMIN="$(git -C "$PRIVATE_WORKTREE" rev-parse --path-format=absolute --git-dir)"
chmod 700 "$PRIVATE_WORKTREE" "$PRIVATE_ADMIN"
PRIVATE_WORKTREE="$PRIVATE_WORKTREE" /usr/bin/python3 -I -B - \
  >"${PRIVATE_STATE}/runs/private-run/state.json" <<'PY'
import json, os
print(json.dumps({"runId":"private-run","state":"READY","worktreePath":os.environ["PRIVATE_WORKTREE"]}))
PY
/usr/bin/python3 -I -B "$DRIVER" assert-worktree-private \
  --state-root "$PRIVATE_STATE" --run-id private-run >/dev/null \
  || fail '0700 worktree/admin 未通过独立检查'
chmod 755 "$PRIVATE_WORKTREE"
if /usr/bin/python3 -I -B "$DRIVER" assert-worktree-private \
    --state-root "$PRIVATE_STATE" --run-id private-run >/dev/null 2>&1; then
  fail '0755 worktree root 未 fail closed'
fi
chmod 700 "$PRIVATE_WORKTREE"
chmod 755 "$PRIVATE_ADMIN"
if /usr/bin/python3 -I -B "$DRIVER" assert-worktree-private \
    --state-root "$PRIVATE_STATE" --run-id private-run >/dev/null 2>&1; then
  fail '0755 git admin dir 未 fail closed'
fi
chmod 700 "$PRIVATE_ADMIN"

# Decision 前独立证据检查：恰好三文件、当前 ReviewPacket、
# Attempt-root WorkerResult，以及 owner-control stdout 最后 agent_end。
EVIDENCE_REPO="${TMP_ROOT}/evidence-repo"
EVIDENCE_STATE="${TMP_ROOT}/evidence-state"
mkdir -p "$EVIDENCE_REPO" "$EVIDENCE_STATE"
git -C "$EVIDENCE_REPO" init -q -b main
git -C "$EVIDENCE_REPO" config user.email fixture@example.invalid
git -C "$EVIDENCE_REPO" config user.name Fixture
printf 'fixture\n' >"${EVIDENCE_REPO}/README.md"
git -C "$EVIDENCE_REPO" add README.md
git -C "$EVIDENCE_REPO" commit -q -m fixture
EVIDENCE_REPO="$EVIDENCE_REPO" EVIDENCE_STATE="$EVIDENCE_STATE" /usr/bin/python3 -I -B - <<'PY'
import hashlib, json, os
repo, state = os.environ["EVIDENCE_REPO"], os.environ["EVIDENCE_STATE"]
task, run, attempt = "m13-task", "m13-run", "attempt-1"
paths = ["docs/m13-goal-lite-walking-skeleton.md", "schemas/examples/goal-lite/approved-proposal.example.json", "schemas/examples/goal-lite/walking-skeleton.tasks.json"]
for path in paths:
    target = os.path.join(repo, path); os.makedirs(os.path.dirname(target), exist_ok=True)
    with open(target, "w", encoding="utf-8") as f: f.write("中文交付\n" if path.endswith(".md") else '{"kind":"Task"}\n')
run_root = os.path.join(state, "runs", run); os.makedirs(os.path.join(run_root, "attempts", attempt), exist_ok=True)
base = "d"*40; spec = "sha256:" + "a"*64
with open(os.path.join(run_root, "state.json"), "w") as f: json.dump({"taskId":task,"runId":run,"state":"REVIEW_PENDING","currentAttemptId":attempt,"worktreePath":repo,"baseSha":base,"specDigest":spec}, f)
result = {"kind":"WorkerResult","taskId":task,"runId":run,"attemptId":attempt,"usage":{"inputTokens":12,"outputTokens":7}}
with open(os.path.join(run_root, "attempts", attempt, "worker-result.json"), "w") as f: json.dump(result, f)
result_digest = "sha256:" + hashlib.sha256(json.dumps(result, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
control = os.path.join(state, "owner-control", "session-1"); os.makedirs(control, exist_ok=True); os.chmod(control, 0o700)
declared = {"apiVersion":"marshal.dev/v1alpha1", **result}
end = {"type":"agent_end","willRetry":False,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"done"},{"type":"text","text":"交付已完成，以下为唯一 WorkerResult：\n"+json.dumps(declared, ensure_ascii=False)+"\n"}]}]}
stdout_path = os.path.join(control, "stdout.bin")
with open(stdout_path, "w", encoding="utf-8") as f: f.write(json.dumps(end, ensure_ascii=False)+"\n")
os.chmod(stdout_path, 0o600)
with open(os.path.join(control, "transcript.jcs"), "w", encoding="utf-8") as f: json.dump({"stdoutBytes":os.path.getsize(stdout_path),"transcriptDigest":"sha256:"+"f"*64}, f)
os.chmod(os.path.join(control, "transcript.jcs"), 0o600)
with open(os.path.join(control, "session.nonce"), "w", encoding="utf-8") as f: f.write("RAW-AUTH-MUST-NOT-ARCHIVE")
os.chmod(os.path.join(control, "session.nonce"), 0o600)
with open(os.path.join(control, "process-supervisor.sock"), "w", encoding="utf-8") as f: f.write("SOCKET-MUST-NOT-ARCHIVE")
os.chmod(os.path.join(control, "process-supervisor.sock"), 0o600)
os.makedirs(os.path.join(state, "result-ingress"), exist_ok=True)
ledger = {"transition":{"identity":{"taskId":task,"runId":run,"attemptId":attempt},"supervisorStarted":{"controlDirectory":{"canonicalPath":control}}}}
with open(os.path.join(state, "result-ingress", "result-ingress.jsonl"), "w") as f: f.write(json.dumps(ledger)+"\n")
with open(os.path.join(run_root, "events.jsonl"), "w") as f: f.write(json.dumps({"sequence":1,"type":"worker.completed"})+"\n")
d = "sha256:" + "e"*64
packet = {"apiVersion":"marshal.dev/v1alpha1","kind":"ReviewPacket","taskId":task,"runId":run,"reviewRound":1,"specDigest":spec,"baseSha":base,"snapshotDigest":d,"diffDigest":d,"verificationDigest":d,"artifactManifestDigest":d,"evidenceDigest":d,"workerResultDigests":[result_digest],"inputs":{"workerResults":[f"attempts/{attempt}/worker-result.json"]}}
with open(os.path.join(state, "packet.json"), "w") as f: json.dump(packet, f)
candidate = {"schemaVersion":"marshal.m13-candidate-evidence.v1","candidateMode":"build-from-head","sourceHead":"b"*40,"candidateSHA256":"c"*64,"closureEligible":True,"publishedRC1ContainsThisFix":False}
with open(os.path.join(state, "candidate.json"), "w") as f: json.dump(candidate, f)
PY
/usr/bin/python3 -I -B "$DRIVER" validate-evidence \
  --state-root "$EVIDENCE_STATE" --task-id m13-task --run-id m13-run \
  --packet "$EVIDENCE_STATE/packet.json" --candidate-evidence "$EVIDENCE_STATE/candidate.json" \
  --candidate-mode build-from-head --source-head bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  --out "$EVIDENCE_STATE/evidence-check.json" >/dev/null \
  || fail '合法的三文件/ReviewPacket/raw transcript 证据未通过'
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/evidence-check.json" <<'PY' \
  || fail 'evidence-check 未绑定 current Attempt normalized WorkerResult 正 token'
import json, sys
e=json.load(open(sys.argv[1])); u=e["workerResultUsage"]; assert u == {"attemptId":"attempt-1","authority":"attempt-root-normalized-worker-result","inputTokens":12,"outputTokens":7}
PY
cp "$EVIDENCE_STATE/runs/m13-run/attempts/attempt-1/worker-result.json" "$EVIDENCE_STATE/original-worker-result.json"
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/runs/m13-run/attempts/attempt-1/worker-result.json" <<'PY'
import json, sys
p=sys.argv[1]; result=json.load(open(p)); result["usage"]["outputTokens"]=0; json.dump(result, open(p,"w"))
PY
if /usr/bin/python3 -I -B "$DRIVER" validate-evidence \
    --state-root "$EVIDENCE_STATE" --task-id m13-task --run-id m13-run \
    --packet "$EVIDENCE_STATE/packet.json" --candidate-evidence "$EVIDENCE_STATE/candidate.json" \
    --candidate-mode build-from-head --source-head bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
    --out "$EVIDENCE_STATE/zero-token-evidence-check.json" >/dev/null 2>&1; then
  fail 'Decision 前 current Attempt outputTokens=0 未 fail closed'
fi
cp "$EVIDENCE_STATE/original-worker-result.json" "$EVIDENCE_STATE/runs/m13-run/attempts/attempt-1/worker-result.json"
/usr/bin/python3 -I -B "$DRIVER" render-decision \
  --packet "$EVIDENCE_STATE/packet.json" --task-id m13-task --run-id m13-run \
  --reviewer-id independent-reviewer --summary '独立检查通过' \
  --evidence-check "$EVIDENCE_STATE/evidence-check.json" --out "$EVIDENCE_STATE/decision.json" >/dev/null \
  || fail 'Decision 未绑定已检查 ReviewPacket'
cp "$EVIDENCE_STATE/owner-control/session-1/stdout.bin" "$EVIDENCE_STATE/original-stdout.bin"
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/owner-control/session-1/stdout.bin" <<'PY'
import json, sys
p=sys.argv[1]; event=json.load(open(p, encoding="utf-8")); event["messages"][-1]["content"][-1]["text"] += '{"second":true}'
json.dump(event, open(p, "w", encoding="utf-8"), ensure_ascii=False)
PY
if /usr/bin/python3 -I -B "$DRIVER" validate-evidence \
    --state-root "$EVIDENCE_STATE" --task-id m13-task --run-id m13-run \
    --packet "$EVIDENCE_STATE/packet.json" --candidate-evidence "$EVIDENCE_STATE/candidate.json" \
    --candidate-mode build-from-head --source-head bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
    --out "$EVIDENCE_STATE/invalid-evidence-check.json" >/dev/null 2>&1; then
  fail '最后 agent_end 含第二 JSON object 未 fail closed'
fi
cp "$EVIDENCE_STATE/original-stdout.bin" "$EVIDENCE_STATE/owner-control/session-1/stdout.bin"
EVIDENCE_CONTROL="${TMP_ROOT}/evidence-control"
mkdir -p "$EVIDENCE_CONTROL"
cp "$EVIDENCE_STATE/candidate.json" "$EVIDENCE_CONTROL/candidate-evidence.json"
cp "$EVIDENCE_STATE/evidence-check.json" "$EVIDENCE_CONTROL/evidence-check.json"
cp "$EVIDENCE_STATE/packet.json" "$EVIDENCE_STATE/runs/m13-run/review-packet.json"
/usr/bin/python3 -I -B "$DRIVER" stage-evidence \
  --state-root "$EVIDENCE_STATE" --control-root "$EVIDENCE_CONTROL" --run-id m13-run \
  --out-dir "$EVIDENCE_STATE/staged" >/dev/null \
  || fail 'allowlisted evidence staging 失败'
/usr/bin/python3 -I -B "$DRIVER" archive-evidence \
  --stage-dir "$EVIDENCE_STATE/staged" --out "$EVIDENCE_STATE/evidence.tar.gz" >/dev/null \
  || fail 'allowlisted evidence archive 生成失败'
/usr/bin/python3 -I -B "$DRIVER" verify-evidence-archive \
  --archive "$EVIDENCE_STATE/evidence.tar.gz" >"$EVIDENCE_STATE/archive-check.json" \
  || fail 'allowlisted evidence archive 后置校验失败'
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/evidence.tar.gz" <<'PY' \
  || fail 'archive 泄漏 nonce/socket/auth material 或非 allowlist path'
import tarfile, sys
with tarfile.open(sys.argv[1], "r:gz") as archive:
    names=[m.name for m in archive.getmembers()]
    payload=b"".join(archive.extractfile(m).read() for m in archive.getmembers() if m.isfile())
assert all(".marshal" not in n and "owner-control" not in n and "nonce" not in n and "sock" not in n for n in names), names
assert b"RAW-AUTH-MUST-NOT-ARCHIVE" not in payload
assert b"SOCKET-MUST-NOT-ARCHIVE" not in payload
assert "supervisor/stdout.bin" in names and "supervisor/transcript.jcs" in names and "journal/digests.json" in names
assert "run/events.jsonl" not in names
PY
mkdir -p "$EVIDENCE_STATE/staged/owner-control/session-1"
printf 'RAW-AUTH-MUST-NOT-ARCHIVE' >"$EVIDENCE_STATE/staged/owner-control/session-1/session.nonce"
chmod 0600 "$EVIDENCE_STATE/staged/owner-control/session-1/session.nonce"
if /usr/bin/python3 -I -B "$DRIVER" archive-evidence \
    --stage-dir "$EVIDENCE_STATE/staged" --out "$EVIDENCE_STATE/precheck-bypass.tar.gz" >/dev/null 2>&1; then
  fail 'archive precheck 未拒绝 staged owner-control/session.nonce'
fi
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/malicious.tar.gz" <<'PY'
import io, tarfile, sys
with tarfile.open(sys.argv[1], "w:gz") as archive:
    data=b"RAW-AUTH-MUST-NOT-ARCHIVE"; info=tarfile.TarInfo(".marshal/owner-control/session-1/session.nonce"); info.size=len(data); archive.addfile(info, io.BytesIO(data))
PY
if /usr/bin/python3 -I -B "$DRIVER" verify-evidence-archive \
    --archive "$EVIDENCE_STATE/malicious.tar.gz" >/dev/null 2>&1; then
  fail 'archive verifier 未拒绝 owner-control/session.nonce 非 allowlist path'
fi
printf ' \n' >>"$EVIDENCE_STATE/packet.json"
# canonical digest 对空白不敏感；改变语义才必须触发漂移。
/usr/bin/python3 -I -B "$DRIVER" render-decision \
  --packet "$EVIDENCE_STATE/packet.json" --task-id m13-task --run-id m13-run \
  --reviewer-id independent-reviewer --summary '独立检查通过' \
  --evidence-check "$EVIDENCE_STATE/evidence-check.json" --out "$EVIDENCE_STATE/whitespace-decision.json" \
  >/dev/null || fail 'ReviewPacket canonical digest 错误绑定了空白 bytes'
/usr/bin/python3 -I -B - "$EVIDENCE_STATE/packet.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1])); p["reviewRound"] = 2
json.dump(p, open(sys.argv[1], "w"))
PY
if /usr/bin/python3 -I -B "$DRIVER" render-decision \
    --packet "$EVIDENCE_STATE/packet.json" --task-id m13-task --run-id m13-run \
    --reviewer-id independent-reviewer --summary '独立检查通过' \
    --evidence-check "$EVIDENCE_STATE/evidence-check.json" --out "$EVIDENCE_STATE/drifted-decision.json" \
    >/dev/null 2>&1; then
  fail '被检查后语义漂移的 ReviewPacket 仍产生 Decision'
fi

# metrics 只读 Attempt 根 worker-result.json 的 inputTokens/outputTokens，
# 且 ACCEPTED、正 token 与 wallClockSeconds 任一缺失都 fail closed。
METRIC_STATE="${TMP_ROOT}/metric-state"
mkdir -p "$METRIC_STATE/runs/metric-run/attempts/attempt-1" "$METRIC_STATE/runs/metric-run/attempts/attempt-old"
printf '{"taskId":"metric-task","runId":"metric-run","state":"ACCEPTED","currentAttemptId":"attempt-1","attemptsUsed":2}\n' >"$METRIC_STATE/runs/metric-run/state.json"
printf '{"taskId":"metric-task","runId":"metric-run","attemptId":"attempt-1","usage":{"inputTokens":11,"outputTokens":5}}\n' >"$METRIC_STATE/runs/metric-run/attempts/attempt-1/worker-result.json"
printf '{"taskId":"metric-task","runId":"metric-run","attemptId":"attempt-old","usage":{"inputTokens":999,"outputTokens":999}}\n' >"$METRIC_STATE/runs/metric-run/attempts/attempt-old/worker-result.json"
/usr/bin/python3 -I -B "$DRIVER" metrics --state-root "$METRIC_STATE" --run-id metric-run \
  --wall-start 2000-01-01T00:00:00Z --out "$METRIC_STATE/metrics.json" >/dev/null \
  || fail 'Attempt-root token metrics 未被提取'
/usr/bin/python3 -I -B - "$METRIC_STATE/metrics.json" <<'PY' \
  || fail 'metrics 未冻结 ACCEPTED/正 token/wall clock'
import json, sys
m=json.load(open(sys.argv[1])); assert m["finalState"] == "ACCEPTED"; assert m["attemptId"] == "attempt-1"; assert m["inputTokens"] == 11; assert m["outputTokens"] == 5; assert m["wallClockSeconds"] > 0
PY
printf '{"taskId":"metric-task","runId":"metric-run","attemptId":"attempt-1","usage":{"inputTokens":11,"outputTokens":0}}\n' >"$METRIC_STATE/runs/metric-run/attempts/attempt-1/worker-result.json"
if /usr/bin/python3 -I -B "$DRIVER" metrics --state-root "$METRIC_STATE" --run-id metric-run \
    --wall-start 2000-01-01T00:00:00Z --out "$METRIC_STATE/zero.json" >/dev/null 2>&1; then
  fail 'outputTokens=0 未 fail closed'
fi
printf '{"taskId":"metric-task","runId":"metric-run","attemptId":"attempt-1","usage":{"inputTokens":11,"outputTokens":5}}\n' >"$METRIC_STATE/runs/metric-run/attempts/attempt-1/worker-result.json"
printf '{"taskId":"metric-task","runId":"metric-run","state":"REVIEW_PENDING","currentAttemptId":"attempt-1","attemptsUsed":2}\n' >"$METRIC_STATE/runs/metric-run/state.json"
if /usr/bin/python3 -I -B "$DRIVER" metrics --state-root "$METRIC_STATE" --run-id metric-run \
    --wall-start 2000-01-01T00:00:00Z --out "$METRIC_STATE/nonterminal.json" >/dev/null 2>&1; then
  fail 'finalState 非 ACCEPTED 未 fail closed'
fi
printf '{"taskId":"metric-task","runId":"metric-run","state":"ACCEPTED","currentAttemptId":"attempt-1","attemptsUsed":2}\n' >"$METRIC_STATE/runs/metric-run/state.json"
if /usr/bin/python3 -I -B "$DRIVER" metrics --state-root "$METRIC_STATE" --run-id metric-run \
    --out "$METRIC_STATE/no-wall.json" >/dev/null 2>&1; then
  fail '缺少 wallClockSeconds 未 fail closed'
fi

grep -A2 -F '      - name: Pack dogfood evidence tarball' "$WORKFLOW" | grep -Fq 'if: always()' \
  || fail 'evidence pack 不再保证 always 执行'
grep -A3 -F '      - name: Upload dogfood evidence' "$WORKFLOW" | grep -Fq 'if: always()' \
  || fail 'evidence upload 不再保证 always 执行'
grep -A4 -F '      - name: Run independent verification' "$WORKFLOW" \
  | grep -Fq 'id: independent_verification' \
  || fail 'verification step 缺少稳定 outcome identity'
grep -A3 -F '      - name: Upload dogfood evidence' "$WORKFLOW" \
  | grep -Fq "continue-on-error: \${{ steps.independent_verification.outcome == 'failure' }}" \
  || fail 'artifact upload 未仅在 verification failure 时降级网络失败'
grep -Fq 'stage-evidence' "$PACK_SCRIPT" || fail 'pack step 未从显式 allowlist staging 开始'
grep -Fq 'archive-evidence' "$PACK_SCRIPT" || fail 'pack step 未使用 allowlist archive builder'
grep -Fq 'verify-evidence-archive' "$PACK_SCRIPT" || fail 'pack step 未后置复核 archive allowlist'
! grep -Fq 'members=".marshal"' "$PACK_SCRIPT" || fail 'pack step 仍整包 .marshal'
! grep -Fq -- '-czf m13-dogfood-evidence.tar.gz' "$PACK_SCRIPT" || fail 'pack step 仍直接 tar 整体状态目录'

grep -Fxq 'umask 022' "$PLAN_SCRIPT" || fail 'Plan 没有显式使用 umask 022'
grep -Fxq 'umask 022' "$DRIVE_SCRIPT" || fail 'Drive 没有显式使用 umask 022'
! grep -Fq 'umask 077' "$PLAN_SCRIPT" || fail 'Plan 仍用 umask 077 遮蔽创建端缺陷'
! grep -Fq 'umask 077' "$DRIVE_SCRIPT" || fail 'Drive 仍用 umask 077 遮蔽创建端缺陷'
grep -Fq 'assert-worktree-private' "$PLAN_SCRIPT" || fail 'Plan 后未检查 worktree/admin 0700'
grep -A2 -F 'default: build-from-head' "$WORKFLOW" >/dev/null \
  || fail 'ADR 0075 关闭 workflow 默认 candidate 不是 build-from-head'
grep -Fq -- '--candidate-mode "$CANDIDATE_MODE" --source-head "$CANDIDATE_SOURCE_HEAD"' "$WORKFLOW" \
  || fail 'candidate mode/sourceHead 未冻结进 evidence'
grep -Fq -- '--evidence-check "$CONTROL_ROOT/evidence-check.json"' "$WORKFLOW" \
  || fail 'Decision 未强制消费独立 evidence check'
grep -Fq '不声称已发布 v1.0.0-rc1 包含本修复' "$WORKFLOW" \
  || fail 'build-from-head 摘要未排除已发布 RC1 修复声称'
! grep -Fq 'control", "output", "worker-result.json' "$DRIVER" \
  || fail 'metrics 仍读取错误 control/output WorkerResult 路径'

printf '[m13-e2e-workflow-test] PASS\n'
