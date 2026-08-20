#!/usr/bin/env bash
# scripts/marshal-watch.sh 的确定性测试（operator-runbook §11）。
# 只使用临时 MARSHAL_WATCH_ROOT 与 MARSHAL_WATCH_PROCESS_FILE fixture：
# 不读取/删除/改写真实 .marshal，不依赖网络、osascript 或真实 Worker 进程。
# 覆盖：行动队列优先级/Goal cohort/unscoped 分桶、终态过滤、
#       --once 不 sleep、v2 JSON、lease/owner 权威、argv 仅诊断、
#       typed retry lineage/root+latest failure/dedupe 与
#       slots=min(memory,cpu,provider) 的 fail-closed 建议。
set -u

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
WATCH="$SCRIPT_DIR/marshal-watch.sh"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   - %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf 'FAIL - %s\n' "$1" >&2; }
note() { printf '# %s\n' "$1"; }

TMP_RAW="$(mktemp -d "${TMPDIR:-/tmp}/marshal-watch-test.XXXXXX")"
TMP=$(cd -P "$TMP_RAW" && pwd)
LEASE_HOLDER_PID=""
cleanup() {
  if [ -n "$LEASE_HOLDER_PID" ]; then
    kill "$LEASE_HOLDER_PID" 2>/dev/null || true
    wait "$LEASE_HOLDER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

ROOT="$TMP/root"
PROCFILE="$TMP/procs.txt"
LEASEFILE="$TMP/lease-facts.json"
CONTRACT_VALIDATOR="$TMP/marshal-contract-validator"
mkdir -p "$ROOT/runs"
: > "$PROCFILE"
printf '%s\n' '{}' > "$LEASEFILE"
FAILURE_SIGNATURE="sha256:1111111111111111111111111111111111111111111111111111111111111111"
if ! go build -o "$CONTRACT_VALIDATOR" ./cmd/marshal; then
  printf 'FAIL - 无法构建同源 Marshal Core contract validator\n' >&2
  exit 1
fi

default_cohort_file() {
  local path="$TMP/default-cohort.json"
  python3 - "$ROOT/runs" "$path" <<'PYEOF'
import json, os, sys
run_ids = sorted(name for name in os.listdir(sys.argv[1])
                 if os.path.isdir(os.path.join(sys.argv[1], name)))
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    json.dump({"goalId": "goal:test-default", "runIds": run_ids}, handle,
              separators=(",", ":"))
PYEOF
  printf '%s\n' "$path"
}

# 统一以 fixture 根与受控进程文件运行，禁用通知，避免触碰真实环境。
run_watch() {
  local cohort_file
  if [ "${MARSHAL_WATCH_COHORT_FILE+x}" = x ]; then
    cohort_file="$MARSHAL_WATCH_COHORT_FILE"
  else
    cohort_file=$(default_cohort_file)
  fi
  MARSHAL_WATCH_ROOT="$ROOT" \
  MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" \
  MARSHAL_WATCH_LEASE_FACTS_FILE="$LEASEFILE" \
  MARSHAL_WATCH_COHORT_FILE="$cohort_file" \
  MARSHAL_WATCH_MARSHAL_BIN="$CONTRACT_VALIDATOR" \
  MARSHAL_WATCH_NOTIFY=0 \
  MARSHAL_WATCH_LOGICAL_CPUS="${MARSHAL_WATCH_LOGICAL_CPUS-8}" \
  MARSHAL_WATCH_LOAD1M="${MARSHAL_WATCH_LOAD1M-0}" \
  MARSHAL_WATCH_SWAP_USED_BYTES="${MARSHAL_WATCH_SWAP_USED_BYTES-0}" \
  MARSHAL_WATCH_PRESSURE_FREE_PERCENT="${MARSHAL_WATCH_PRESSURE_FREE_PERCENT-80}" \
  MARSHAL_WATCH_LOG="$TMP/watch.log" \
  bash "$WATCH" "$@"
}

run_watch_real_lease() {
  local cohort_file
  if [ "${MARSHAL_WATCH_COHORT_FILE+x}" = x ]; then
    cohort_file="$MARSHAL_WATCH_COHORT_FILE"
  else
    cohort_file=$(default_cohort_file)
  fi
  MARSHAL_WATCH_ROOT="$ROOT" \
  MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" \
  MARSHAL_WATCH_COHORT_FILE="$cohort_file" \
  MARSHAL_WATCH_MARSHAL_BIN="$CONTRACT_VALIDATOR" \
  MARSHAL_WATCH_NOTIFY=0 \
  MARSHAL_WATCH_LOGICAL_CPUS="${MARSHAL_WATCH_LOGICAL_CPUS-8}" \
  MARSHAL_WATCH_LOAD1M="${MARSHAL_WATCH_LOAD1M-0}" \
  MARSHAL_WATCH_SWAP_USED_BYTES="${MARSHAL_WATCH_SWAP_USED_BYTES-0}" \
  MARSHAL_WATCH_PRESSURE_FREE_PERCENT="${MARSHAL_WATCH_PRESSURE_FREE_PERCENT-80}" \
  MARSHAL_WATCH_LOG="$TMP/watch.log" \
  bash "$WATCH" "$@"
}

# 创建一个 Run fixture：$1=runId $2=state $3=updatedAt 距今秒数
make_run() {
  local rid="$1" state="$2" age="${3:-60}"
  local dir="$ROOT/runs/$rid"
  mkdir -p "$dir"
  local ts
  ts=$(python3 - "$age" <<'PYEOF'
import datetime, sys
age = int(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=age)
print(now.strftime("%Y-%m-%dT%H:%M:%SZ"))
PYEOF
  )
  cat > "$dir/state.json" <<EOF
{"apiVersion":"marshal.dev/v1alpha1","kind":"RunState","taskId":"task-$rid","runId":"$rid","state":"$state","sequence":1,"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","policyDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","capabilityDigest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","baseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reviewRound":0,"attemptsUsed":0,"operationalRetriesUsed":0,"reworkRoundsUsed":0,"createdAt":"$ts","updatedAt":"$ts"}
EOF
  cat > "$dir/task-spec.json" <<'EOF'
{"worker":{"preferredAdapter":"qwen","fallbackAdapters":[]}}
EOF
}

set_preferred_adapter() {
  python3 - "$ROOT/runs/$1/task-spec.json" "$2" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["worker"]["preferredAdapter"] = sys.argv[2]
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
PYEOF
}

set_current_attempt() {
  python3 - "$ROOT/runs/$1/state.json" "$2" "${3:-}" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["currentAttemptId"] = sys.argv[2]
if sys.argv[3]:
    data["sequence"] = int(sys.argv[3])
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
PYEOF
}

failure_signature() {
  python3 - "$ROOT/runs/$1/state.json" "$2" "$3" <<'PYEOF'
import hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    state = json.load(handle)
evidence = {"adapterId": "qwen", "failureKind": sys.argv[2],
            "retryDisposition": sys.argv[3]}
encoded = json.dumps(evidence, sort_keys=True, separators=(",", ":")).encode()
evidence_digest = "sha256:" + hashlib.sha256(encoded).hexdigest()
signature = {"version": 1, "sourceHead": state["baseSha"],
             "specDigest": state["specDigest"],
             "policyDigest": state["policyDigest"],
             "capabilityDigest": state["capabilityDigest"],
             "adapterId": "qwen", "failureKind": sys.argv[2],
             "failureEvidenceDigest": evidence_digest}
encoded = json.dumps(signature, sort_keys=True, separators=(",", ":")).encode()
print("sha256:" + hashlib.sha256(encoded).hexdigest())
PYEOF
}

set_review_round() {
  python3 - "$ROOT/runs/$1/state.json" "$2" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["reviewRound"] = int(sys.argv[2])
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
PYEOF
}

set_publication_head() {
  python3 - "$ROOT/runs/$1/state.json" "$2" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["publication"] = {"provider":"github","repository":"chiga0/marshal-harness",
                       "headBranch":"feat/test","baseBranch":"main","headSha":sys.argv[2]}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
PYEOF
}

make_rework_decision() {
  python3 - "$ROOT/runs/$1/state.json" "$ROOT/runs/$1" "$2" "${3:-}" "${4:-}" <<'PYEOF'
import hashlib, json, os, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    state = json.load(handle)
round_number = int(sys.argv[3])
evidence = sys.argv[4] or "sha256:" + "4" * 64
mode = sys.argv[5]
decision = {
    "apiVersion":"marshal.dev/v1alpha1", "kind":"ReviewDecision",
    "taskId":state["taskId"], "runId":state["runId"], "reviewRound":round_number,
    "reviewer":{"type":"lead-agent","id":"watchdog-fixture"},
    "specDigest":state["specDigest"], "reviewPacketDigest":"sha256:" + "1" * 64,
    "verificationDigest":"sha256:" + "2" * 64,
    "artifactManifestDigest":"sha256:" + "3" * 64, "evidenceDigest":evidence,
    "verdict":"rework", "summary":"rework required",
    "blockingFindings":[{"id":"finding-1","severity":"P1","title":"gate failed",
                         "description":"verification gate failed","requiredOutcome":"fix the gate"}],
    "nonBlockingFindings":[], "publicationRecommendation":"do-not-publish",
    "mergeRecommendation":"do-not-merge", "decidedAt":"2026-08-20T00:00:00Z"
}
if mode == "duplicate-finding-id":
    decision["nonBlockingFindings"] = [dict(decision["blockingFindings"][0])]
encoded = json.dumps(decision, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
os.makedirs(os.path.join(sys.argv[2], "decisions"), exist_ok=True)
with open(os.path.join(sys.argv[2], "decisions", "decision-%03d.json" % round_number), "wb") as handle:
    handle.write(encoded)
print("sha256:" + hashlib.sha256(encoded).hexdigest())
PYEOF
}

# 以超时守护运行命令，输出写入 $2；超时杀进程并返回 124。
timeout_run() {
  local limit="$1" outfile="$2"
  shift 2
  "$@" > "$outfile" 2>&1 &
  local pid=$! waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$limit" ]; then
      kill -9 "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
  return $?
}

note "0) 语法自检"
if bash -n "$WATCH"; then ok "marshal-watch.sh 语法通过"; else bad "marshal-watch.sh 语法错误"; fi
if bash -n "$0"; then ok "marshal-watch_test.sh 语法通过"; else bad "marshal-watch_test.sh 语法错误"; fi

note "1) 优先级排序、终态过滤、动作映射与 JSON schema"
make_run run-review  REVIEW_PENDING   120
printf '%s\n' '{}' > "$ROOT/runs/run-review/review-packet.json"
make_run run-rework  REWORK_REQUESTED 110
make_run run-retry   RETRY_PENDING    100
set_current_attempt run-retry attempt:run-retry 2
FAILURE_SIGNATURE=$(failure_signature run-retry rate-limited retryable)
INITIAL_EVENT_TS=$(python3 - <<'PYEOF'
import datetime
stamp = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=10)
print(stamp.replace(microsecond=0).isoformat().replace("+00:00", "Z"))
PYEOF
)
cat > "$ROOT/runs/run-retry/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"run-retry","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:run-retry","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","runId":"run-retry","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:run-retry","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE"}}
EOF
make_run run-verify  VERIFYING         90
make_run run-publish PUBLISHING        80
make_run run-ready   READY             70
make_run run-ci      CI_PENDING        60
make_run run-accepted ACCEPTED         50
make_run run-rejected REJECTED         40
make_run run-blocked  BLOCKED          30
make_run run-aborted  ABORTED          20
make_run run-nochange NO_CHANGE        10

OUT_JSON="$TMP/out.json"
if timeout_run 30 "$OUT_JSON" run_watch --once --json; then
  ok "--once --json 在时限内退出（不 sleep）"
else
  bad "--once --json 未在时限内退出或退出码非零"
fi

if python3 - "$OUT_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
errors = []
if "generatedAt" not in data:
    errors.append("缺少 generatedAt")
capacity = data.get("capacity")
if not isinstance(capacity, dict):
    errors.append("capacity 不是对象")
items = data.get("items")
if not isinstance(items, list):
    errors.append("items 不是数组")
    items = []
required = {"runId", "state", "priority", "action", "ageSeconds", "processOwnership", "dedupeKey"}
for it in items:
    missing = required - set(it)
    if missing:
        errors.append("item %s 缺字段 %s" % (it.get("runId"), sorted(missing)))
runids = [it["runId"] for it in items]
expected_order = ["run-review", "run-rework", "run-retry", "run-verify",
                  "run-publish", "run-ready", "run-ci"]
if runids != expected_order:
    errors.append("排序不符: got %s want %s" % (runids, expected_order))
terminal = {"run-accepted", "run-rejected", "run-blocked", "run-aborted", "run-nochange"}
leaked = terminal & set(runids)
if leaked:
    errors.append("终态泄漏进行动队列: %s" % sorted(leaked))
actions = {it["runId"]: it["action"] for it in items}
expected_actions = {
    "run-review": "review-now",
    "run-rework": "run-rework-now",
    "run-retry": "retry-or-abort",
    "run-verify": "verify-or-doctor",
    "run-publish": "publish-or-doctor",
    "run-ready": "run-now",
    "run-ci": "check-ci",
}
for rid, act in expected_actions.items():
    if actions.get(rid) != act:
        errors.append("动作映射不符 %s: got %s want %s" % (rid, actions.get(rid), act))
prio = {it["runId"]: it["priority"] for it in items}
if prio.get("run-review") != 10:
    errors.append("REVIEW_PENDING 优先级应为 10")
if prio.get("run-ready") != 70 or prio.get("run-ci") != 80:
    errors.append("READY/CI_PENDING 优先级不符")
ages = {it["runId"]: it["ageSeconds"] for it in items}
if not (100 <= ages.get("run-review", -1) <= 300):
    errors.append("run-review ageSeconds 应约等于 120，got %s" % ages.get("run-review"))
if errors:
    print("FAIL:")
    for e in errors:
        print("  " + e)
    sys.exit(1)
print("schema+排序+终态过滤+动作映射 OK")
PYEOF
then
  ok "JSON schema、排序、终态过滤与动作映射"
else
  bad "JSON schema、排序、终态过滤或动作映射不符"
fi

note "1c) 缺失 ReviewPacket 进入干预队列，避免重复失败审查"
rm -f "$ROOT/runs/run-review/review-packet.json"
OUT_INTERVENTION="$TMP/out_intervention.json"
if timeout_run 30 "$OUT_INTERVENTION" run_watch --once --json; then
  if python3 - "$OUT_INTERVENTION" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    items = json.load(f)["items"]
review = next((it for it in items if it.get("runId") == "run-review"), {})
if review.get("action") != "review-intervention" or review.get("priority") != 5:
    print("缺失 ReviewPacket 未进入 review-intervention: %r" % review)
    sys.exit(1)
print("缺失 ReviewPacket 干预动作 OK")
PYEOF
  then
    ok "缺失 ReviewPacket 进入 review-intervention"
  else
    bad "缺失 ReviewPacket 未进入 review-intervention"
  fi

note "1d) 待办 dedupeKey 对同输入稳定且随证据/控制记录变化"
OUT_INTERVENTION_REPEAT="$TMP/out_intervention_repeat.json"
run_watch --once --json > "$OUT_INTERVENTION_REPEAT"
mkdir -p "$ROOT/runs/run-review/control"
printf '%s\n' '{"kind":"ApprovalRecord","recordId":"approval:test"}' > "$ROOT/runs/run-review/control/records.jsonl"
OUT_CONTROL_CHANGED="$TMP/out_control_changed.json"
run_watch --once --json > "$OUT_CONTROL_CHANGED"
if python3 - "$OUT_JSON" "$OUT_INTERVENTION" "$OUT_INTERVENTION_REPEAT" "$OUT_CONTROL_CHANGED" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    with_packet = {it["runId"]: it["dedupeKey"] for it in json.load(f)["items"]}
with open(sys.argv[2]) as f:
    without_packet = {it["runId"]: it["dedupeKey"] for it in json.load(f)["items"]}
with open(sys.argv[3]) as f:
    repeated = {it["runId"]: it["dedupeKey"] for it in json.load(f)["items"]}
with open(sys.argv[4]) as f:
    control_changed = {it["runId"]: it["dedupeKey"] for it in json.load(f)["items"]}
if with_packet.get("run-review") == without_packet.get("run-review"):
    print("ReviewPacket 内容变化/缺失未改变 dedupeKey")
    sys.exit(1)
if without_packet.get("run-review") != repeated.get("run-review"):
    print("同输入重复运行未保持稳定 dedupeKey")
    sys.exit(1)
if repeated.get("run-review") == control_changed.get("run-review"):
    print("control record 变化未改变 dedupeKey")
    sys.exit(1)
if not with_packet.get("run-rework", "").startswith("sha256:"):
    print("run-rework 缺少稳定 sha256 dedupeKey")
    sys.exit(1)
print("dedupeKey 稳定性、证据与控制记录变化 OK")
PYEOF
then
  ok "待办 dedupeKey 稳定且随证据/控制记录变化"
else
  bad "待办 dedupeKey 稳定性或证据/控制记录变化检查失败"
fi
else
  bad "缺失 ReviewPacket 场景 watchdog 异常"
fi

note "1e) RETRY_PENDING 仅当前 typed lineage 可重试，root/latest 绑定并在新 origin 重置"
make_run retry-lineage RETRY_PENDING 10
set_current_attempt retry-lineage attempt:retry-2 4
ROOT_SIGNATURE=$(failure_signature retry-lineage rate-limited retryable)
LATEST_SIGNATURE=$(failure_signature retry-lineage connection-failure retryable)
cat > "$ROOT/runs/retry-lineage/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:retry-1","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:retry-1","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$ROOT_SIGNATURE"}}
{"sequence":3,"type":"worker.started","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RETRY_PENDING","stateTo":"RUNNING","attemptId":"attempt:retry-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":4,"type":"worker.failed","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:retry-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"connection-failure","retryDisposition":"retryable","failureSignature":"$LATEST_SIGNATURE"}}
EOF
OUT_RETRY_LINEAGE="$TMP/out_retry_lineage.json"
run_watch --once --json > "$OUT_RETRY_LINEAGE"

make_run retry-legacy RETRY_PENDING 10
set_current_attempt retry-legacy attempt:legacy
cat > "$ROOT/runs/retry-legacy/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:legacy","payload":{"error":"legacy free text"}}
EOF

make_run retry-mismatch RETRY_PENDING 10
set_current_attempt retry-mismatch attempt:new 2
MISMATCH_SIGNATURE=$(failure_signature retry-mismatch rate-limited retryable)
cat > "$ROOT/runs/retry-mismatch/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:old","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:old","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$MISMATCH_SIGNATURE"}}
EOF

make_run retry-invalid RETRY_PENDING 10
set_current_attempt retry-invalid attempt:invalid 2
cat > "$ROOT/runs/retry-invalid/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:invalid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:invalid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"not-a-digest"}}
EOF

make_run retry-wrongsig RETRY_PENDING 10
set_current_attempt retry-wrongsig attempt:wrongsig 2
cat > "$ROOT/runs/retry-wrongsig/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:wrongsig","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:wrongsig","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
EOF

for rid in retry-wrong-actor retry-wrong-run retry-missing-started retry-nonadjacent retry-successor-started retry-snapshot-drift retry-sequence-gap retry-attempt-reuse; do
  make_run "$rid" RETRY_PENDING 10
done

set_current_attempt retry-wrong-actor attempt:wrong-actor 2
WRONG_ACTOR_SIGNATURE=$(failure_signature retry-wrong-actor rate-limited retryable)
cat > "$ROOT/runs/retry-wrong-actor/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:wrong-actor","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:wrong-actor","actor":{"type":"system","id":"forged-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$WRONG_ACTOR_SIGNATURE"}}
EOF

set_current_attempt retry-wrong-run attempt:wrong-run 2
WRONG_RUN_SIGNATURE=$(failure_signature retry-wrong-run rate-limited retryable)
cat > "$ROOT/runs/retry-wrong-run/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"forged-run","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:wrong-run","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","runId":"forged-run","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:wrong-run","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$WRONG_RUN_SIGNATURE"}}
EOF

set_current_attempt retry-missing-started attempt:missing 1
MISSING_STARTED_SIGNATURE=$(failure_signature retry-missing-started rate-limited retryable)
cat > "$ROOT/runs/retry-missing-started/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:missing","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$MISSING_STARTED_SIGNATURE"}}
EOF

set_current_attempt retry-nonadjacent attempt:nonadjacent 3
NONADJACENT_SIGNATURE=$(failure_signature retry-nonadjacent rate-limited retryable)
cat > "$ROOT/runs/retry-nonadjacent/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:nonadjacent","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"planning.inputs-frozen","timestamp":"$INITIAL_EVENT_TS","actor":{"type":"system","id":"marshal-planning"},"payload":{"adapterId":"qwen"}}
{"sequence":3,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:nonadjacent","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$NONADJACENT_SIGNATURE"}}
EOF

set_current_attempt retry-successor-started attempt:successor 3
SUCCESSOR_SIGNATURE=$(failure_signature retry-successor-started rate-limited retryable)
cat > "$ROOT/runs/retry-successor-started/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:failed","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:failed","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$SUCCESSOR_SIGNATURE"}}
{"sequence":3,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RETRY_PENDING","stateTo":"RUNNING","attemptId":"attempt:successor","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
EOF

set_current_attempt retry-snapshot-drift attempt:drift 1
DRIFT_SIGNATURE=$(failure_signature retry-snapshot-drift rate-limited retryable)
cat > "$ROOT/runs/retry-snapshot-drift/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:drift","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:drift","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$DRIFT_SIGNATURE"}}
EOF

set_current_attempt retry-sequence-gap attempt:gap 3
SEQUENCE_GAP_SIGNATURE=$(failure_signature retry-sequence-gap rate-limited retryable)
cat > "$ROOT/runs/retry-sequence-gap/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"retry-sequence-gap","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:gap","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":3,"type":"worker.failed","runId":"retry-sequence-gap","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:gap","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$SEQUENCE_GAP_SIGNATURE"}}
EOF

set_current_attempt retry-attempt-reuse attempt:reuse 4
REUSE_SIGNATURE=$(failure_signature retry-attempt-reuse rate-limited retryable)
cat > "$ROOT/runs/retry-attempt-reuse/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:reuse","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:reuse","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$REUSE_SIGNATURE"}}
{"sequence":3,"type":"worker.started","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RETRY_PENDING","stateTo":"RUNNING","attemptId":"attempt:reuse","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":4,"type":"worker.failed","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:reuse","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$REUSE_SIGNATURE"}}
EOF
python3 - "$ROOT/runs" <<'PYEOF'
import json, os, sys
for run_id in ("retry-legacy", "retry-mismatch", "retry-invalid", "retry-wrongsig",
               "retry-wrong-actor", "retry-missing-started", "retry-nonadjacent",
               "retry-successor-started", "retry-snapshot-drift", "retry-attempt-reuse"):
    path = os.path.join(sys.argv[1], run_id, "events.jsonl")
    events = []
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            event = json.loads(line)
            event["runId"] = run_id
            events.append(event)
    with open(path, "w", encoding="utf-8") as handle:
        for event in events:
            handle.write(json.dumps(event, separators=(",", ":")) + "\n")
PYEOF
OUT_RETRY_CLOSED="$TMP/out_retry_closed.json"
run_watch --once --json > "$OUT_RETRY_CLOSED"
if python3 - "$OUT_RETRY_LINEAGE" "$OUT_RETRY_CLOSED" "$ROOT_SIGNATURE" "$LATEST_SIGNATURE" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    first = {item["runId"]: item for item in json.load(handle)["items"]}
with open(sys.argv[2]) as handle:
    second = {item["runId"]: item for item in json.load(handle)["items"]}
lineage = first["retry-lineage"]
if lineage.get("action") != "retry-or-abort":
    raise SystemExit("valid typed lineage was not retryable: %r" % lineage)
if lineage.get("rootFailure", {}).get("failureSignature") != sys.argv[3]:
    raise SystemExit("root failure projection wrong: %r" % lineage)
if lineage.get("latestFailure", {}).get("failureSignature") != sys.argv[4] or lineage.get("latestFailure", {}).get("attemptId") != "attempt:retry-2":
    raise SystemExit("latest failure projection wrong: %r" % lineage)
for run_id in ("retry-legacy", "retry-mismatch", "retry-invalid", "retry-wrongsig",
               "retry-wrong-actor", "retry-wrong-run", "retry-missing-started", "retry-nonadjacent",
               "retry-successor-started", "retry-snapshot-drift", "retry-sequence-gap", "retry-attempt-reuse"):
    item = second[run_id]
    if item.get("action") != "retry-intervention" or item.get("interventionReason") != "typed-retry-lineage-required":
        raise SystemExit("%s did not fail closed: %r" % (run_id, item))
print("typed retry lineage and fail-closed intervention OK")
PYEOF
then
  ok "typed retry lineage 投影与 legacy/invalid/mismatch fail closed"
else
  bad "RETRY_PENDING 错误建议重试或 root/latest 投影错误"
fi

ORIGIN_SIGNATURE=$(failure_signature retry-lineage dns-failure retryable)
set_current_attempt retry-lineage attempt:retry-2 2
cat > "$ROOT/runs/retry-lineage/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:retry-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","runId":"retry-lineage","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:retry-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"dns-failure","retryDisposition":"retryable","failureSignature":"$ORIGIN_SIGNATURE"}}
EOF
OUT_RETRY_ORIGIN_RESET="$TMP/out_retry_origin_reset.json"
run_watch --once --json > "$OUT_RETRY_ORIGIN_RESET"
if python3 - "$OUT_RETRY_LINEAGE" "$OUT_RETRY_ORIGIN_RESET" "$ORIGIN_SIGNATURE" <<'PYEOF'
import json, sys
def get(path):
    with open(path) as handle:
        return next(item for item in json.load(handle)["items"] if item["runId"] == "retry-lineage")
before, after = get(sys.argv[1]), get(sys.argv[2])
if after.get("rootFailure", {}).get("failureSignature") != sys.argv[3] or after.get("latestFailure", {}).get("failureSignature") != sys.argv[3]:
    raise SystemExit("new origin did not reset root/latest: %r" % after)
if before.get("dedupeKey") == after.get("dedupeKey"):
    raise SystemExit("root/latest origin reset did not change dedupeKey")
print("new origin resets root/latest and dedupe identity OK")
PYEOF
then
  ok "新 origin 重置 root/latest failure 并刷新 dedupe"
else
  bad "新 origin 污染了 failure lineage 或 dedupe"
fi

note "1f) REWORK_REQUESTED origin 必须 exact 绑定 ReviewDecision 或 publication"
make_run retry-review-origin RETRY_PENDING 10
set_current_attempt retry-review-origin attempt:review-origin 8
set_review_round retry-review-origin 1
REVIEW_EVIDENCE="sha256:4444444444444444444444444444444444444444444444444444444444444444"
REVIEW_DECISION_DIGEST=$(make_rework_decision retry-review-origin 1 "$REVIEW_EVIDENCE")
REVIEW_ORIGIN_SIGNATURE=$(failure_signature retry-review-origin rate-limited retryable)
cat > "$ROOT/runs/retry-review-origin/events.jsonl" <<EOF
{"sequence":1,"type":"planning.spec-accepted","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"CREATED","stateTo":"PLANNED","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}}
{"sequence":2,"type":"planning.inputs-frozen","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"PLANNED","stateTo":"READY","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","policyDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","capabilityDigest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","baseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
{"sequence":3,"type":"worker.started","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:review-origin-initial","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":4,"type":"worker.completed","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:review-origin-initial","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{}}
{"sequence":5,"type":"verification.completed","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"VERIFYING","stateTo":"REVIEW_PENDING","actor":{"type":"system","id":"marshal-verifier"},"payload":{}}
{"sequence":6,"type":"review.rework","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REVIEW_PENDING","stateTo":"REWORK_REQUESTED","actor":{"type":"system","id":"marshal-review"},"payload":{"verdict":"rework","decisionDigest":"$REVIEW_DECISION_DIGEST","evidenceDigest":"$REVIEW_EVIDENCE"}}
{"sequence":7,"type":"worker.started","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REWORK_REQUESTED","stateTo":"RUNNING","attemptId":"attempt:review-origin","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":8,"type":"worker.failed","runId":"retry-review-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:review-origin","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$REVIEW_ORIGIN_SIGNATURE"}}
EOF

make_run retry-ci-origin RETRY_PENDING 10
set_current_attempt retry-ci-origin attempt:ci-origin 3
CI_HEAD="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
set_publication_head retry-ci-origin "$CI_HEAD"
CI_ORIGIN_SIGNATURE=$(failure_signature retry-ci-origin connection-failure retryable)
cat > "$ROOT/runs/retry-ci-origin/events.jsonl" <<EOF
{"sequence":1,"type":"publication.checks-failed","runId":"retry-ci-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"CI_PENDING","stateTo":"REWORK_REQUESTED","actor":{"type":"publisher","id":"marshal-github-publisher"},"payload":{"headSha":"$CI_HEAD"}}
{"sequence":2,"type":"worker.started","runId":"retry-ci-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REWORK_REQUESTED","stateTo":"RUNNING","attemptId":"attempt:ci-origin","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":3,"type":"worker.failed","runId":"retry-ci-origin","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:ci-origin","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"connection-failure","retryDisposition":"retryable","failureSignature":"$CI_ORIGIN_SIGNATURE"}}
EOF
OUT_VALID_REWORK_ORIGINS="$TMP/out_valid_rework_origins.json"
run_watch --once --json > "$OUT_VALID_REWORK_ORIGINS"
if python3 - "$OUT_VALID_REWORK_ORIGINS" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    items = {item["runId"]: item for item in json.load(handle)["items"]}
for run_id in ("retry-review-origin", "retry-ci-origin"):
    if items[run_id].get("action") != "retry-or-abort":
        raise SystemExit("valid rework origin rejected: %s %r" % (run_id, items[run_id]))
print("valid review/CI rework origins accepted OK")
PYEOF
then
  ok "exact ReviewDecision/publication origin 可进入 retry 建议"
else
  bad "合法 rework origin 被错误拒绝"
fi

write_rework_retry_tail() {
  local rid="$1" origin="$2" signature
  signature=$(failure_signature "$rid" rate-limited retryable)
  printf '%s\n' "$origin" > "$ROOT/runs/$rid/events.jsonl"
  cat >> "$ROOT/runs/$rid/events.jsonl" <<EOF
{"sequence":2,"type":"worker.started","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REWORK_REQUESTED","stateTo":"RUNNING","attemptId":"attempt:$rid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":3,"type":"worker.failed","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:$rid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$signature"}}
EOF
}

write_review_retry_tail() {
  local rid="$1" origin="$2" signature
  signature=$(failure_signature "$rid" rate-limited retryable)
  cat > "$ROOT/runs/$rid/events.jsonl" <<EOF
{"sequence":1,"type":"planning.spec-accepted","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"CREATED","stateTo":"PLANNED","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}}
{"sequence":2,"type":"planning.inputs-frozen","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"PLANNED","stateTo":"READY","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","policyDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","capabilityDigest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","baseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
{"sequence":3,"type":"worker.started","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:$rid-initial","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":4,"type":"worker.completed","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:$rid-initial","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{}}
{"sequence":5,"type":"verification.completed","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"VERIFYING","stateTo":"REVIEW_PENDING","actor":{"type":"system","id":"marshal-verifier"},"payload":{}}
$origin
{"sequence":7,"type":"worker.started","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REWORK_REQUESTED","stateTo":"RUNNING","attemptId":"attempt:$rid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":8,"type":"worker.failed","runId":"$rid","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:$rid","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$signature"}}
EOF
}

FORGED_ORIGIN_RUNS="retry-origin-unknown retry-origin-review-actor retry-origin-review-state retry-origin-review-attempt retry-origin-review-digest retry-origin-review-evidence retry-origin-review-identity retry-origin-review-duplicate-id retry-origin-review-invalid-replay retry-origin-ci-actor retry-origin-ci-head retry-origin-ci-publication"
for rid in $FORGED_ORIGIN_RUNS; do
  make_run "$rid" RETRY_PENDING 10
  set_current_attempt "$rid" "attempt:$rid" 3
done

write_rework_retry_tail retry-origin-unknown \
  "{\"sequence\":1,\"type\":\"verification.completed\",\"runId\":\"retry-origin-unknown\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-verifier\"},\"payload\":{}}"

for rid in retry-origin-review-actor retry-origin-review-state retry-origin-review-attempt retry-origin-review-digest retry-origin-review-evidence retry-origin-review-identity retry-origin-review-duplicate-id retry-origin-review-invalid-replay; do
  set_current_attempt "$rid" "attempt:$rid" 8
  set_review_round "$rid" 1
done
ACTOR_DIGEST=$(make_rework_decision retry-origin-review-actor 1 "$REVIEW_EVIDENCE")
write_review_retry_tail retry-origin-review-actor \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-actor\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"forged-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$ACTOR_DIGEST\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"
STATE_DIGEST=$(make_rework_decision retry-origin-review-state 1 "$REVIEW_EVIDENCE")
write_review_retry_tail retry-origin-review-state \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-state\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"VERIFYING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$STATE_DIGEST\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"
ATTEMPT_DIGEST=$(make_rework_decision retry-origin-review-attempt 1 "$REVIEW_EVIDENCE")
write_review_retry_tail retry-origin-review-attempt \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-attempt\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"attemptId\":\"attempt:forged\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$ATTEMPT_DIGEST\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"
make_rework_decision retry-origin-review-digest 1 "$REVIEW_EVIDENCE" >/dev/null
write_review_retry_tail retry-origin-review-digest \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-digest\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"
EVIDENCE_DIGEST=$(make_rework_decision retry-origin-review-evidence 1 "$REVIEW_EVIDENCE")
write_review_retry_tail retry-origin-review-evidence \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-evidence\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$EVIDENCE_DIGEST\",\"evidenceDigest\":\"sha256:9999999999999999999999999999999999999999999999999999999999999999\"}}"
IDENTITY_DIGEST=$(make_rework_decision retry-origin-review-identity 1 "$REVIEW_EVIDENCE")
python3 - "$ROOT/runs/retry-origin-review-identity/decisions/decision-001.json" "$TMP/identity-digest" <<'PYEOF'
import hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    decision = json.load(handle)
decision["runId"] = "forged-run"
encoded = json.dumps(decision, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
with open(sys.argv[1], "wb") as handle:
    handle.write(encoded)
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write("sha256:" + hashlib.sha256(encoded).hexdigest())
PYEOF
IDENTITY_DIGEST=$(cat "$TMP/identity-digest")
write_review_retry_tail retry-origin-review-identity \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-identity\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$IDENTITY_DIGEST\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"

DUPLICATE_ID_DIGEST=$(make_rework_decision retry-origin-review-duplicate-id 1 "$REVIEW_EVIDENCE" duplicate-finding-id)
write_review_retry_tail retry-origin-review-duplicate-id \
  "{\"sequence\":6,\"type\":\"review.rework\",\"runId\":\"retry-origin-review-duplicate-id\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"REVIEW_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"verdict\":\"rework\",\"decisionDigest\":\"$DUPLICATE_ID_DIGEST\",\"evidenceDigest\":\"$REVIEW_EVIDENCE\"}}"

INVALID_REPLAY_DIGEST=$(make_rework_decision retry-origin-review-invalid-replay 1 "$REVIEW_EVIDENCE")
INVALID_REPLAY_SIGNATURE=$(failure_signature retry-origin-review-invalid-replay rate-limited retryable)
cat > "$ROOT/runs/retry-origin-review-invalid-replay/events.jsonl" <<EOF
{"sequence":1,"type":"planning.spec-accepted","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"CREATED","stateTo":"PLANNED","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}}
{"sequence":2,"type":"planning.inputs-frozen","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"PLANNED","stateTo":"READY","actor":{"type":"system","id":"marshal-planning"},"payload":{"specDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","policyDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","capabilityDigest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","baseSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
{"sequence":3,"type":"verification.completed","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"READY","stateTo":"REVIEW_PENDING","actor":{"type":"system","id":"marshal-verifier"},"payload":{}}
{"sequence":4,"type":"review.rework","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REVIEW_PENDING","stateTo":"REWORK_REQUESTED","actor":{"type":"system","id":"marshal-review"},"payload":{"verdict":"rework","decisionDigest":"$INVALID_REPLAY_DIGEST","evidenceDigest":"$REVIEW_EVIDENCE"}}
{"sequence":5,"type":"worker.started","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"REWORK_REQUESTED","stateTo":"RUNNING","attemptId":"attempt:retry-origin-review-invalid-replay","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":6,"type":"worker.failed","runId":"retry-origin-review-invalid-replay","timestamp":"$INITIAL_EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:retry-origin-review-invalid-replay","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$INVALID_REPLAY_SIGNATURE"}}
EOF
set_current_attempt retry-origin-review-invalid-replay attempt:retry-origin-review-invalid-replay 6

set_publication_head retry-origin-ci-actor "$CI_HEAD"
write_rework_retry_tail retry-origin-ci-actor \
  "{\"sequence\":1,\"type\":\"publication.checks-failed\",\"runId\":\"retry-origin-ci-actor\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"CI_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"system\",\"id\":\"marshal-review\"},\"payload\":{\"headSha\":\"$CI_HEAD\"}}"
set_publication_head retry-origin-ci-head "$CI_HEAD"
write_rework_retry_tail retry-origin-ci-head \
  "{\"sequence\":1,\"type\":\"publication.checks-failed\",\"runId\":\"retry-origin-ci-head\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"CI_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"publisher\",\"id\":\"marshal-github-publisher\"},\"payload\":{\"headSha\":\"cccccccccccccccccccccccccccccccccccccccc\"}}"
write_rework_retry_tail retry-origin-ci-publication \
  "{\"sequence\":1,\"type\":\"publication.checks-failed\",\"runId\":\"retry-origin-ci-publication\",\"timestamp\":\"$INITIAL_EVENT_TS\",\"stateFrom\":\"CI_PENDING\",\"stateTo\":\"REWORK_REQUESTED\",\"actor\":{\"type\":\"publisher\",\"id\":\"marshal-github-publisher\"},\"payload\":{\"headSha\":\"$CI_HEAD\"}}"

OUT_FORGED_ORIGINS="$TMP/out_forged_origins.json"
run_watch --once --json > "$OUT_FORGED_ORIGINS"
if python3 - "$OUT_FORGED_ORIGINS" $FORGED_ORIGIN_RUNS <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    items = {item["runId"]: item for item in json.load(handle)["items"]}
for run_id in sys.argv[2:]:
    item = items[run_id]
    if item.get("action") != "retry-intervention" or item.get("interventionReason") != "typed-retry-lineage-required":
        raise SystemExit("forged origin did not fail closed: %s %r" % (run_id, item))
print("forged review/CI origin matrix fails closed OK")
PYEOF
then
  ok "forged rework-origin table 全部 fail closed"
else
  bad "伪造 rework origin 被提升为 retry"
fi
rm -rf "$ROOT/runs/retry-lineage" "$ROOT/runs/retry-legacy" "$ROOT/runs/retry-mismatch" "$ROOT/runs/retry-invalid" "$ROOT/runs/retry-wrongsig" \
  "$ROOT/runs/retry-wrong-actor" "$ROOT/runs/retry-missing-started" "$ROOT/runs/retry-nonadjacent" \
  "$ROOT/runs/retry-wrong-run" "$ROOT/runs/retry-successor-started" "$ROOT/runs/retry-snapshot-drift" "$ROOT/runs/retry-sequence-gap" "$ROOT/runs/retry-attempt-reuse" \
  "$ROOT/runs/retry-review-origin" "$ROOT/runs/retry-ci-origin"
for rid in $FORGED_ORIGIN_RUNS; do rm -rf "$ROOT/runs/$rid"; done

note "1b) 每次心跳读取内存并给出并发槽位建议"
CAPACITY_JSON="$TMP/capacity.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((3 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((1024 * 1024 * 1024)) \
   timeout_run 30 "$CAPACITY_JSON" run_watch --once --json; then
  if python3 - "$CAPACITY_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f).get("capacity", {})
if capacity.get("memoryAvailableBytes") != 3 * 1024 * 1024 * 1024:
    print("memoryAvailableBytes 未使用测试覆盖: %r" % capacity.get("memoryAvailableBytes"))
    sys.exit(1)
if capacity.get("slotsAvailable") != 3 or capacity.get("concurrencyAction") != "increase-concurrency":
    print("并发槽位建议不符: %r" % capacity)
    sys.exit(1)
print("memory probe + concurrency recommendation OK")
PYEOF
  then
    ok "内存探测与并发槽位建议"
  else
    bad "内存探测或并发槽位建议不符"
  fi
else
  bad "capacity 心跳输出异常"
fi

note "1e) 当前内存压力控制并发；历史 swap 不阻止压力恢复后的扩容"
PRESSURE_JSON="$TMP/current_pressure.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES=$((3 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT=10 \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   timeout_run 30 "$PRESSURE_JSON" run_watch --once --json; then
  if python3 - "$PRESSURE_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("swapUsedBytes") != 3 * 1024 * 1024 * 1024:
    print("swapUsedBytes 不符: %r" % capacity)
    sys.exit(1)
if capacity.get("slotsAvailable") != 0 or capacity.get("concurrencyAction") != "hold-concurrency":
    print("当前高压力未停止新增并发: %r" % capacity)
    sys.exit(1)
if capacity.get("pressure") != "critical" or capacity.get("pressureFreePercent") != 10:
    print("当前压力分类不符: %r" % capacity)
    sys.exit(1)
print("current pressure gate OK")
PYEOF
  then
    ok "当前高压力停止新增并发"
  else
    bad "当前高压力并发门禁不符"
  fi
else
  bad "当前高压力场景 watchdog 异常"
fi

PRESSURE_RECOVERED_JSON="$TMP/pressure_recovered.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES=$((3 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT=60 \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   timeout_run 30 "$PRESSURE_RECOVERED_JSON" run_watch --once --json; then
  if python3 - "$PRESSURE_RECOVERED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("swapUsedBytes") != 3 * 1024 * 1024 * 1024:
    print("swapUsedBytes 不符: %r" % capacity)
    sys.exit(1)
if capacity.get("slotsAvailable") != 4 or capacity.get("concurrencyAction") != "increase-concurrency":
    print("压力恢复后未重新开放并发: %r" % capacity)
    sys.exit(1)
if capacity.get("pressure") != "ok" or capacity.get("pressureFreePercent") != 60:
    print("压力恢复分类不符: %r" % capacity)
    sys.exit(1)
print("pressure recovery reopens capacity OK")
PYEOF
  then
    ok "压力恢复后即使 swap 仍高也重新开放并发"
  else
    bad "压力恢复后未重新开放并发"
  fi
else
  bad "压力恢复场景 watchdog 异常"
fi

PRESSURE_UNKNOWN_JSON="$TMP/pressure_unknown.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES=$((3 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT=unavailable \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   timeout_run 30 "$PRESSURE_UNKNOWN_JSON" run_watch --once --json; then
  if python3 - "$PRESSURE_UNKNOWN_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("slotsAvailable") != 0 or capacity.get("concurrencyAction") != "hold-concurrency":
    print("压力信号缺失未 fail closed: %r" % capacity)
    sys.exit(1)
if capacity.get("pressure") != "unknown":
    print("压力信号缺失分类不符: %r" % capacity)
    sys.exit(1)
if capacity.get("pressureSource") != "unavailable" or capacity.get("swapSource") != "override":
    print("探针不可用来源不符: %r" % capacity)
    sys.exit(1)
print("unavailable pressure probe fails closed OK")
PYEOF
  then
    ok "压力探针不可用时 fail closed"
  else
    bad "压力探针不可用时未 fail closed"
  fi
else
  bad "压力探针不可用场景 watchdog 异常"
fi

SWAP_UNKNOWN_JSON="$TMP/swap_unknown.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES=unavailable \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT=60 \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   timeout_run 30 "$SWAP_UNKNOWN_JSON" run_watch --once --json; then
  if python3 - "$SWAP_UNKNOWN_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("slotsAvailable") != 4 or capacity.get("concurrencyAction") != "increase-concurrency":
    print("非权威 swap 探针缺失错误冻结并发: %r" % capacity)
    sys.exit(1)
if capacity.get("pressure") != "ok" or capacity.get("pressureSource") != "override":
    print("有效实时压力未保持权威: %r" % capacity)
    sys.exit(1)
if capacity.get("swapSource") != "unavailable":
    print("swap 探针来源不符: %r" % capacity)
    sys.exit(1)
print("swap probe is informational when current pressure is available OK")
PYEOF
  then
    ok "swap 探针单独不可用不冻结实时压力已知的并发"
  else
    bad "swap 探针单独不可用错误影响并发"
  fi
else
  bad "swap 探针单独不可用场景 watchdog 异常"
fi

RAW_PROBE_JSON="$TMP/raw_probe.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES= \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT= \
   MARSHAL_WATCH_SWAP_OUTPUT='total = 8.00G  used = 3.25G  free = 4.75G' \
   MARSHAL_WATCH_PRESSURE_OUTPUT='System-wide memory free percentage: 61%' \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   timeout_run 30 "$RAW_PROBE_JSON" run_watch --once --json; then
  if python3 - "$RAW_PROBE_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("swapUsedBytes") != int(3.25 * 1024 ** 3):
    print("vm.swapusage 原始输出解析不符: %r" % capacity)
    sys.exit(1)
if capacity.get("pressureFreePercent") != 61 or capacity.get("pressure") != "ok":
    print("memory_pressure 原始输出解析不符: %r" % capacity)
    sys.exit(1)
if capacity.get("swapSource") != "fixture-vm.swapusage" or capacity.get("pressureSource") != "fixture-memory_pressure":
    print("原始探针 fixture 来源不符: %r" % capacity)
    sys.exit(1)
print("raw Darwin probe parsing OK")
PYEOF
  then
    ok "Darwin 原始探针输出解析"
  else
    bad "Darwin 原始探针输出解析失败"
  fi
else
  bad "Darwin 原始探针 fixture 场景 watchdog 异常"
fi

RAW_PROBE_BAD_JSON="$TMP/raw_probe_bad.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES= \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT= \
   MARSHAL_WATCH_SWAP_OUTPUT='format changed' \
   MARSHAL_WATCH_PRESSURE_OUTPUT='format changed' \
   timeout_run 30 "$RAW_PROBE_BAD_JSON" run_watch --once --json; then
  if python3 - "$RAW_PROBE_BAD_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("pressure") != "unknown" or capacity.get("slotsAvailable") != 0:
    print("格式漂移未 fail closed: %r" % capacity)
    sys.exit(1)
if capacity.get("pressureSource") != "unavailable" or capacity.get("swapSource") != "unavailable":
    print("格式漂移来源不符: %r" % capacity)
    sys.exit(1)
print("raw probe format drift fails closed OK")
PYEOF
  then
    ok "原始探针格式漂移时 fail closed"
  else
    bad "原始探针格式漂移时未 fail closed"
  fi
else
  bad "原始探针格式漂移场景 watchdog 异常"
fi

RAW_PROBE_MALFORMED_JSON="$TMP/raw_probe_malformed.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES= \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT= \
   MARSHAL_WATCH_SWAP_OUTPUT='used = 1..2G' \
   MARSHAL_WATCH_PRESSURE_OUTPUT='System-wide memory free percentage: 1000%' \
   timeout_run 30 "$RAW_PROBE_MALFORMED_JSON" run_watch --once --json; then
  if python3 - "$RAW_PROBE_MALFORMED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("pressure") != "unknown" or capacity.get("slotsAvailable") != 0:
    print("畸形数值未 fail closed: %r" % capacity)
    sys.exit(1)
if capacity.get("pressureSource") != "unavailable" or capacity.get("swapSource") != "unavailable":
    print("畸形数值来源不符: %r" % capacity)
    sys.exit(1)
print("malformed numeric probe fails closed with stable JSON OK")
PYEOF
  then
    ok "畸形探针数值稳定输出 JSON 并 fail closed"
  else
    bad "畸形探针数值未稳定 fail closed"
  fi
else
  bad "畸形探针数值导致 watchdog 非零退出"
fi

RAW_PROBE_OVERFLOW_JSON="$TMP/raw_probe_overflow.json"
if MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((8 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_SWAP_USED_BYTES= \
   MARSHAL_WATCH_PRESSURE_FREE_PERCENT= \
   MARSHAL_WATCH_SWAP_OUTPUT='used = 9999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999G' \
   MARSHAL_WATCH_PRESSURE_OUTPUT='format changed' \
   timeout_run 30 "$RAW_PROBE_OVERFLOW_JSON" run_watch --once --json; then
  if python3 - "$RAW_PROBE_OVERFLOW_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("pressure") != "unknown" or capacity.get("slotsAvailable") != 0:
    print("超大探针数值未 fail closed: %r" % capacity)
    sys.exit(1)
if capacity.get("swapSource") != "unavailable":
    print("超大 swap 数值未标记 unavailable: %r" % capacity)
    sys.exit(1)
print("overflow numeric probe fails closed with stable JSON OK")
PYEOF
  then
    ok "超大探针数值稳定输出 JSON 并 fail closed"
  else
    bad "超大探针数值未稳定 fail closed"
  fi
else
  bad "超大探针数值导致 watchdog 非零退出"
fi

note "2) REVIEW_PENDING 优先于 RETRY_PENDING/READY/CI_PENDING（首位断言）"
if python3 - "$OUT_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    items = json.load(f)["items"]
ids = [it["runId"] for it in items]
first = ids[0]
if first != "run-review":
    print("首位应为 REVIEW_PENDING(run-review)，got %s" % first)
    sys.exit(1)
for other in ("run-retry", "run-ready", "run-ci"):
    if ids.index("run-review") > ids.index(other):
        print("REVIEW_PENDING 应优先于 %s" % other)
        sys.exit(1)
print("REVIEW_PENDING 居首且优先于 RETRY_PENDING/READY/CI_PENDING")
PYEOF
then
  ok "REVIEW_PENDING 最高优先级"
else
  bad "REVIEW_PENDING 未保持最高优先级"
fi

note "3) RUNNING owned-active 与 doctor-dead 分支 + 精确 runId 绑定"
# 清空并重建 RUNNING 场景，进程文件只提供 run-alive 的归属与一个诱饵。
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run run-alive RUNNING 30
make_run run-dead  RUNNING 30
make_run run-al    RUNNING 30   # run-alive 的子串，验证不得误判归属
cat > "$PROCFILE" <<EOF
12345 marshal task run --run run-alive
67890 opencode some-args run-alive
11111 marshal task run --run run-decoy
EOF
cat > "$LEASEFILE" <<'EOF'
{"run-alive":"held-alive","run-dead":"not-held","run-al":"not-held"}
EOF

OUT_RUN="$TMP/out_run.json"
if timeout_run 30 "$OUT_RUN" run_watch --once --json; then
  ok "RUNNING 场景 --once --json 正常退出"
else
  bad "RUNNING 场景 --once --json 异常"
fi

# 精确相同 RunState 在 owned-active 与 not-found 间切换时，action/ownership
# 必须改变 key，确保 Worker 退出后 doctor-dead 不会被旧 monitor key 抑制。
ALIVE_KEY=$(python3 - "$OUT_RUN" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    print(next(it["dedupeKey"] for it in json.load(f)["items"] if it["runId"] == "run-alive"))
PYEOF
)
: > "$PROCFILE"
cat > "$LEASEFILE" <<'EOF'
{"run-alive":"not-held","run-dead":"not-held","run-al":"not-held"}
EOF
OUT_OWNERSHIP_CHANGED="$TMP/out_ownership_changed.json"
run_watch --once --json > "$OUT_OWNERSHIP_CHANGED"
if python3 - "$OUT_OWNERSHIP_CHANGED" "$ALIVE_KEY" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    item = next(it for it in json.load(f)["items"] if it["runId"] == "run-alive")
if item["action"] != "doctor-dead" or item["dedupeKey"] == sys.argv[2]:
    print("ownership/action 变化未刷新 dedupeKey: %r" % item)
    sys.exit(1)
print("ownership/action 变化刷新 dedupeKey OK")
PYEOF
then
  ok "ownership/action 变化刷新 dedupeKey"
else
  bad "ownership/action 变化未刷新 dedupeKey"
fi

if python3 - "$OUT_RUN" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    items = json.load(f)["items"]
by_id = {it["runId"]: it for it in items}
errors = []
alive = by_id.get("run-alive", {})
if alive.get("action") != "monitor" or alive.get("processOwnership") != "owned-active":
    errors.append("run-alive 应为 monitor/owned-active，got %s/%s"
                  % (alive.get("action"), alive.get("processOwnership")))
dead = by_id.get("run-dead", {})
if dead.get("action") != "doctor-dead" or dead.get("processOwnership") != "not-found":
    errors.append("run-dead 应为 doctor-dead/not-found，got %s/%s"
                  % (dead.get("action"), dead.get("processOwnership")))
al = by_id.get("run-al", {})
if al.get("action") != "doctor-dead" or al.get("processOwnership") != "not-found":
    errors.append("run-al（子串）不得误判归属，应为 doctor-dead/not-found，got %s/%s"
                  % (al.get("action"), al.get("processOwnership")))
if errors:
    print("FAIL:")
    for e in errors:
        print("  " + e)
    sys.exit(1)
print("RUNNING 归属分支与精确 runId 绑定 OK")
PYEOF
then
  ok "RUNNING owned-active / doctor-dead 与精确绑定"
else
  bad "RUNNING 归属分支或精确绑定不符"
fi

note "3b) argv 仅作诊断，Marshal lease/owner 才是动作所有权权威"
cat > "$PROCFILE" <<'EOF'
12345 marshal task run --run run-dead
EOF
cat > "$LEASEFILE" <<'EOF'
{"run-alive":"held-alive","run-dead":"not-held","run-al":"unknown"}
EOF
OUT_AUTHORITY="$TMP/out_authority.json"
run_watch --once --json > "$OUT_AUTHORITY"
if python3 - "$OUT_AUTHORITY" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    by_id = {item["runId"]: item for item in json.load(f)["items"]}
alive = by_id["run-alive"]
dead = by_id["run-dead"]
unknown = by_id["run-al"]
if alive.get("processOwnership") != "owned-active" or alive.get("argvMatched") is not False:
    raise SystemExit("held lease without argv must remain owned-active: %r" % alive)
if dead.get("processOwnership") != "not-found" or dead.get("action") != "doctor-dead" or dead.get("argvMatched") is not True:
    raise SystemExit("argv without held lease must remain doctor-dead: %r" % dead)
if unknown.get("processOwnership") != "unknown" or unknown.get("action") != "hold-ownership-unknown":
    raise SystemExit("unknown lease fact must fail closed: %r" % unknown)
print("lease authority wins; argv remains diagnostic")
PYEOF
then
  ok "Marshal lease/owner 优先于 argv 诊断"
else
  bad "动作所有权仍被 argv 诊断污染"
fi

note "3c) 生产路径直接探测 held lease + owner，不依赖 fixture/argv"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run run-real-lease RUNNING 10
make_run run-real-peer READY 10
: > "$PROCFILE"
READYFILE="$TMP/lease-holder-ready"
python3 - "$ROOT/runs/run-real-lease" "$READYFILE" <<'PYEOF' &
import datetime, fcntl, json, os, sys, time
run_dir, ready = sys.argv[1:]
lock_path = os.path.join(run_dir, "lease.lock")
fd = os.open(lock_path, os.O_RDONLY | os.O_CREAT, 0o600)
fcntl.flock(fd, fcntl.LOCK_EX)
st = os.fstat(fd)
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
owner = {"token":"fixture-token","pid":os.getpid(),"processStartedAt":now,
         "acquiredAt":now,"heartbeatAt":now,"device":st.st_dev,"inode":st.st_ino}
with open(os.path.join(run_dir, "lease.lock.owner"), "w", encoding="utf-8") as handle:
    json.dump(owner, handle)
with open(ready, "w", encoding="utf-8") as handle:
    handle.write("ready")
while True:
    time.sleep(1)
PYEOF
LEASE_HOLDER_PID=$!
waited=0
while [ ! -f "$READYFILE" ] && [ "$waited" -lt 30 ]; do
  sleep 0.1
  waited=$((waited + 1))
done
OUT_REAL_LEASE="$TMP/out_real_lease.json"
if run_watch_real_lease --once --json > "$OUT_REAL_LEASE" && \
   python3 - "$OUT_REAL_LEASE" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    item = next(entry for entry in json.load(f)["items"] if entry["runId"] == "run-real-lease")
if item.get("runId") != "run-real-lease" or item.get("processOwnership") != "owned-active" or item.get("ownershipSource") != "marshal-lease" or item.get("argvMatched") is not False:
    raise SystemExit("real lease probe did not establish ownership: %r" % item)
print("real held lease + owner probe OK")
PYEOF
then
  ok "生产 lease/owner 探针建立动作所有权"
else
  bad "生产 lease/owner 探针失败"
fi
python3 - "$ROOT/runs/run-real-lease/lease.lock.owner" <<'PYEOF'
import json, sys
path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    owner = json.load(handle)
owner.pop("device", None)
owner.pop("inode", None)
with open(path, "w", encoding="utf-8") as handle:
    json.dump(owner, handle)
PYEOF
OUT_REAL_LEASE_MISSING_ID="$TMP/out_real_lease_missing_identity.json"
if run_watch_real_lease --once --json > "$OUT_REAL_LEASE_MISSING_ID" && \
   python3 - "$OUT_REAL_LEASE_MISSING_ID" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    item = next(entry for entry in json.load(handle)["items"] if entry["runId"] == "run-real-lease")
if item.get("processOwnership") != "unknown" or item.get("action") != "hold-ownership-unknown":
    raise SystemExit("owner without exact dev/inode was trusted: %r" % item)
print("owner without exact dev/inode fails closed")
PYEOF
then
  ok "held owner 缺 exact dev/inode 时 fail closed"
else
  bad "held owner 缺 exact dev/inode 仍被信任"
fi
for pid_case in 'true' '2147483648' '"5192"'; do
  python3 - "$ROOT/runs/run-real-lease" "$pid_case" <<'PYEOF'
import datetime, json, os, sys
run_dir, raw_pid = sys.argv[1:]
st = os.stat(os.path.join(run_dir, "lease.lock"), follow_symlinks=False)
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
owner = {"token":"fixture-token","pid":json.loads(raw_pid),"processStartedAt":now,
         "acquiredAt":now,"heartbeatAt":now,"device":st.st_dev,"inode":st.st_ino}
with open(os.path.join(run_dir, "lease.lock.owner"), "w", encoding="utf-8") as handle:
    json.dump(owner, handle)
PYEOF
  SAFE_PID_CASE=$(printf '%s' "$pid_case" | tr -cd 'A-Za-z0-9')
  OUT_BAD_PID="$TMP/out_bad_pid_${SAFE_PID_CASE}.json"
  if run_watch_real_lease --once --json > "$OUT_BAD_PID" && \
     python3 - "$OUT_BAD_PID" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    by_id = {item["runId"]: item for item in json.load(handle)["items"]}
bad = by_id.get("run-real-lease", {})
peer = by_id.get("run-real-peer", {})
if bad.get("processOwnership") != "unknown" or bad.get("action") != "hold-ownership-unknown":
    raise SystemExit("invalid pid was trusted or crashed: %r" % bad)
if peer.get("action") != "run-now":
    raise SystemExit("invalid owner contaminated valid peer: %r" % peer)
print("invalid owner pid isolated; valid peer preserved")
PYEOF
  then
    ok "held owner 非法 pid=$pid_case 按 Run fail closed"
  else
    bad "held owner 非法 pid=$pid_case 未隔离"
  fi
done
kill "$LEASE_HOLDER_PID" 2>/dev/null || true
wait "$LEASE_HOLDER_PID" 2>/dev/null || true
LEASE_HOLDER_PID=""

note "4) doctor-dead 优先级高于一般 monitor 且低于审查/重作"
if python3 - "$OUT_RUN" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    items = json.load(f)["items"]
prio = {it["runId"]: it["priority"] for it in items}
errors = []
if prio.get("run-dead") != 30:
    errors.append("doctor-dead 优先级应为 30，got %s" % prio.get("run-dead"))
if prio.get("run-alive") != 90:
    errors.append("RUNNING(active) monitor 优先级应为 90，got %s" % prio.get("run-alive"))
if errors:
    print("FAIL:")
    for e in errors:
        print("  " + e)
    sys.exit(1)
print("RUNNING 优先级 OK")
PYEOF
then
  ok "RUNNING 优先级映射"
else
  bad "RUNNING 优先级映射不符"
fi

note "5) current Goal/cohort 与 historical backlog 分桶，历史不占 top actions"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run current-review REVIEW_PENDING 60
printf '%s\n' '{}' > "$ROOT/runs/current-review/review-packet.json"
make_run current-ready READY 30
make_run historical-review REVIEW_PENDING 120
printf '%s\n' '{}' > "$ROOT/runs/historical-review/review-packet.json"
make_run historical-dead RUNNING 120
cat > "$LEASEFILE" <<'EOF'
{"historical-dead":"not-held"}
EOF
COHORTFILE="$TMP/cohort.json"
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:marshal-v1","runIds":["current-review","current-ready"]}
EOF
OUT_COHORT="$TMP/out_cohort.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$OUT_COHORT" && \
   python3 - "$OUT_COHORT" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
if data.get("queueVersion") != "marshal-watch/v2":
    raise SystemExit("missing v2 version: %r" % data.get("queueVersion"))
if data.get("advisoryOnly") is not True:
    raise SystemExit("watchdog must remain advisory-only: %r" % data.get("advisoryOnly"))
if [item["runId"] for item in data["items"]] != ["current-review", "current-ready"]:
    raise SystemExit("current items incorrect: %r" % data["items"])
if {item["runId"] for item in data["historicalItems"]} != {"historical-review", "historical-dead"}:
    raise SystemExit("historical bucket incorrect: %r" % data["historicalItems"])
if data.get("topAction", {}).get("runId") != "current-review":
    raise SystemExit("historical backlog displaced current top action: %r" % data.get("topAction"))
cohort = data.get("cohort", {})
if cohort.get("source") != "explicit-goal-cohort" or cohort.get("goalId") != "goal:marshal-v1":
    raise SystemExit("cohort identity missing: %r" % cohort)
print("current/historical queue partition OK")
PYEOF
then
  ok "current cohort 优先且 historical backlog 保留"
else
  bad "current/historical 分桶失败"
fi

note "5b) 无显式 cohort 时只有 held-alive 进 current，其余进 unscoped"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run fallback-held RUNNING 60
make_run fallback-recent READY 10
make_run fallback-old REVIEW_PENDING 999999
printf '%s\n' '{}' > "$ROOT/runs/fallback-old/review-packet.json"
cat > "$LEASEFILE" <<'EOF'
{"fallback-held":"held-alive"}
EOF
OUT_UNSCOPED="$TMP/out_unscoped.json"
if MARSHAL_WATCH_COHORT_FILE= run_watch --once --json > "$OUT_UNSCOPED" && \
   python3 - "$OUT_UNSCOPED" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
if data.get("cohort", {}).get("source") != "owned-active-only":
    raise SystemExit("unexpected no-cohort source: %r" % data.get("cohort"))
if [item["runId"] for item in data["items"]] != ["fallback-held"]:
    raise SystemExit("held-alive current bucket incorrect: %r" % data["items"])
if {item["runId"] for item in data.get("unscopedItems", [])} != {"fallback-recent", "fallback-old"}:
    raise SystemExit("unscoped bucket incorrect: %r" % data.get("unscopedItems"))
if data.get("historicalItems") != []:
    raise SystemExit("no-cohort path fell back to historical: %r" % data["historicalItems"])
if data.get("topAction", {}).get("runId") != "fallback-held":
    raise SystemExit("unscoped run displaced held top action: %r" % data.get("topAction"))
print("no-cohort owned-only current + unscoped partition OK")
PYEOF
then
  ok "无 cohort 时 held-alive 与 unscoped 严格分桶"
else
  bad "无 cohort 时错误推断 current/topAction"
fi

note "5c) invalid 显式 cohort fail closed 到 historical，不回退 unscoped/current"
INVALID_COHORT="$TMP/invalid-cohort.json"
printf '%s\n' '{"goalId":"goal:bad","runIds":["duplicate","duplicate"]}' > "$INVALID_COHORT"
OUT_INVALID_COHORT="$TMP/out_invalid_cohort.json"
if MARSHAL_WATCH_COHORT_FILE="$INVALID_COHORT" run_watch --once --json > "$OUT_INVALID_COHORT" && \
   python3 - "$OUT_INVALID_COHORT" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
if data.get("cohort", {}).get("source") != "invalid-explicit-cohort":
    raise SystemExit("invalid cohort source missing: %r" % data.get("cohort"))
if data.get("items") or data.get("unscopedItems") or data.get("topAction") is not None:
    raise SystemExit("invalid cohort unexpectedly produced current/unscoped action: %r" % data)
if {item["runId"] for item in data.get("historicalItems", [])} != {"fallback-held", "fallback-recent", "fallback-old"}:
    raise SystemExit("invalid cohort historical bucket wrong: %r" % data.get("historicalItems"))
print("invalid explicit cohort remains historical-only")
PYEOF
then
  ok "invalid 显式 cohort historical-only fail closed"
else
  bad "invalid 显式 cohort 产生了回退行动"
fi

# 后续 journal/provider 测试继续使用显式 cohort fixture。
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run current-review REVIEW_PENDING 60
printf '%s\n' '{}' > "$ROOT/runs/current-review/review-packet.json"
make_run current-ready READY 30

EVENT_TS=$(python3 - <<'PYEOF'
import datetime
print(datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"))
PYEOF
)
VALID_NOT_BEFORE=$(python3 - "$EVENT_TS" <<'PYEOF'
import datetime, sys
stamp = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
print((stamp + datetime.timedelta(hours=1)).isoformat().replace("+00:00", "Z"))
PYEOF
)

note "5d) Provider signal 只消费 current cohort；历史同 Adapter 噪声不得污染"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run cohort-qoder-ready READY 10
set_preferred_adapter cohort-qoder-ready qoder
make_run historical-qoder READY 10
set_preferred_adapter historical-qoder qoder
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:provider-cohort","runIds":["cohort-qoder-ready"]}
EOF
for external_case in typed legacy malformed; do
  case "$external_case" in
    typed)
      cat > "$ROOT/runs/historical-qoder/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:historical","payload":{"adapterId":"qoder","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE","notBefore":"$VALID_NOT_BEFORE"}}
EOF
      ;;
    legacy)
      cat > "$ROOT/runs/historical-qoder/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:historical","payload":{"message":"legacy qoder failure"}}
EOF
      ;;
    malformed)
      cat > "$ROOT/runs/historical-qoder/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:historical","payload":{"adapterId":"qoder","failureKind":"rate-limited","retryDisposition":"terminal","failureSignature":"$FAILURE_SIGNATURE"}}
EOF
      ;;
  esac
  PROVIDER_EXTERNAL_JSON="$TMP/provider_external_${external_case}.json"
  if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" \
     MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((16 * 1024 * 1024 * 1024)) \
     MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=0 \
     run_watch --once --json > "$PROVIDER_EXTERNAL_JSON" && \
     python3 - "$PROVIDER_EXTERNAL_JSON" "$external_case" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
capacity = data["capacity"]
signals = capacity.get("providerSignals", [])
if capacity.get("providerStatus") != "ok" or capacity.get("providerSlotsAvailable", 0) < 1:
    raise SystemExit("historical %s qoder failure polluted current provider: %r" % (sys.argv[2], capacity))
if signals != [{"adapterId": "qoder", "status": "available"}]:
    raise SystemExit("current qoder availability missing: %r" % signals)
if [item["runId"] for item in data.get("items", [])] != ["cohort-qoder-ready"]:
    raise SystemExit("current cohort changed unexpectedly: %r" % data.get("items"))
if [item["runId"] for item in data.get("historicalItems", [])] != ["historical-qoder"]:
    raise SystemExit("historical qoder diagnostic missing: %r" % data.get("historicalItems"))
print("historical %s qoder signal is isolated" % sys.argv[2])
PYEOF
  then
    ok "cohort 外 $external_case Qoder failure 不污染 current Provider"
  else
    bad "cohort 外 $external_case Qoder failure 污染 current Provider"
  fi
done

rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run cohort-qoder-failure READY 10
set_preferred_adapter cohort-qoder-failure qoder
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:provider-cohort-failure","runIds":["cohort-qoder-failure"]}
EOF
for internal_case in backpressure blocked legacy malformed; do
  case "$internal_case" in
    backpressure)
      expected=backpressure
      cat > "$ROOT/runs/cohort-qoder-failure/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:current","payload":{"adapterId":"qoder","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE","notBefore":"$VALID_NOT_BEFORE"}}
EOF
      ;;
    blocked)
      expected=blocked
      cat > "$ROOT/runs/cohort-qoder-failure/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:current","payload":{"adapterId":"qoder","failureKind":"quota-exhausted","retryDisposition":"blocked","failureSignature":"$FAILURE_SIGNATURE"}}
EOF
      ;;
    legacy)
      expected=unknown
      cat > "$ROOT/runs/cohort-qoder-failure/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:current","payload":{"message":"legacy qoder failure"}}
EOF
      ;;
    malformed)
      expected=unknown
      cat > "$ROOT/runs/cohort-qoder-failure/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:current","payload":{"adapterId":"qoder","failureKind":"quota-exhausted","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE"}}
EOF
      ;;
  esac
  PROVIDER_INTERNAL_JSON="$TMP/provider_internal_${internal_case}.json"
  if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$PROVIDER_INTERNAL_JSON" && \
     python3 - "$PROVIDER_INTERNAL_JSON" "$expected" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    capacity = json.load(handle)["capacity"]
if capacity.get("providerStatus") != sys.argv[2] or capacity.get("providerSlotsAvailable") != 0:
    raise SystemExit("current qoder failure did not hold: %r" % capacity)
print("current qoder failure remains %s" % sys.argv[2])
PYEOF
  then
    ok "cohort 内 $internal_case Qoder failure 保持 $expected"
  else
    bad "cohort 内 $internal_case Qoder failure 未保持 $expected"
  fi
done

# 恢复后续 journal/provider 测试的原始 current cohort fixture。
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run current-review REVIEW_PENDING 60
printf '%s\n' '{}' > "$ROOT/runs/current-review/review-packet.json"
make_run current-ready READY 30
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:marshal-v1","runIds":["current-review","current-ready"]}
EOF

note "6) dedupe 绑定 journal sequence、phase progress、typed failure 与 notBefore"
cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"planning.inputs-frozen","timestamp":"$EVENT_TS","payload":{"adapterId":"qwen"}}
EOF
OUT_PROGRESS_1="$TMP/out_progress_1.json"
MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$OUT_PROGRESS_1"
cat >> "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":2,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:provider-1","payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE","notBefore":"$VALID_NOT_BEFORE"}}
EOF
OUT_PROGRESS_2="$TMP/out_progress_2.json"
MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$OUT_PROGRESS_2"
if python3 - "$OUT_PROGRESS_1" "$OUT_PROGRESS_2" <<'PYEOF'
import json, sys
def item(path):
    with open(path) as f:
        return next(x for x in json.load(f)["items"] if x["runId"] == "current-ready")
a, b = item(sys.argv[1]), item(sys.argv[2])
if a["dedupeKey"] == b["dedupeKey"]:
    raise SystemExit("journal progress did not refresh dedupeKey")
if b.get("journalSequence") != 2 or not b.get("phaseProgressDigest", "").startswith("sha256:"):
    raise SystemExit("journal progress identity missing: %r" % b)
failure = b.get("typedFailure", {})
if failure.get("kind") != "rate-limited" or not isinstance(failure.get("notBefore"), str):
    raise SystemExit("typed failure/notBefore missing: %r" % failure)
print("journal + phase + typed failure dedupe binding OK")
PYEOF
then
  ok "dedupe 绑定 journal/phase/typed failure/notBefore"
else
  bad "dedupe 未绑定完整进展事实"
fi

note "7) slots=min(memory,cpu,provider)，任一关键 signal unknown 即 hold"
CPU_JSON="$TMP/cpu_capacity.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" \
   MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((16 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_WORKER_RESERVE_BYTES=$((2 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=6.2 \
   run_watch --once --json > "$CPU_JSON" && \
   python3 - "$CPU_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    c = json.load(f)["capacity"]
if c.get("memorySlotsAvailable") != 8 or c.get("cpuSlotsAvailable") != 1:
    raise SystemExit("memory/cpu slots wrong: %r" % c)
if c.get("slotsAvailable") != 0 or c.get("providerSlotsAvailable") != 0:
    raise SystemExit("future provider notBefore must hold all new slots: %r" % c)
if c.get("concurrencyAction") != "hold-concurrency":
    raise SystemExit("provider backpressure did not hold: %r" % c)
print("provider is limiting min(memory,cpu,provider) OK")
PYEOF
then
  ok "provider notBefore 限制并发槽位"
else
  bad "provider capacity 未参与最小值"
fi

for provider_case in 'dns-failure retryable' 'connection-failure retryable' 'quota-exhausted blocked'; do
  set -- $provider_case
  failure_kind="$1"
  disposition="$2"
  if [ "$failure_kind" = "quota-exhausted" ]; then
    hint=''
  else
    hint=',"notBefore":"'$VALID_NOT_BEFORE'"'
  fi
  cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"planning.inputs-frozen","timestamp":"$EVENT_TS","payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$EVENT_TS","attemptId":"attempt:provider-1","payload":{"adapterId":"qwen","failureKind":"$failure_kind","retryDisposition":"$disposition","failureSignature":"$FAILURE_SIGNATURE"$hint}}
EOF
  PROVIDER_KIND_JSON="$TMP/provider_${failure_kind}.json"
  if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$PROVIDER_KIND_JSON" && \
     python3 - "$PROVIDER_KIND_JSON" "$failure_kind" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
signal = capacity.get("providerSignals", [{}])[-1]
if capacity.get("providerSlotsAvailable") != 0 or capacity.get("slotsAvailable") != 0:
    raise SystemExit("provider failure did not hold: %r" % capacity)
if signal.get("failureKind") != sys.argv[2]:
    raise SystemExit("provider failure identity missing: %r" % signal)
print("provider failure gate OK: " + sys.argv[2])
PYEOF
  then
    ok "$failure_kind Provider 背压/额度门禁"
  else
    bad "$failure_kind 未参与 Provider 容量"
  fi
done

cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"planning.inputs-frozen","timestamp":"$EVENT_TS","actor":{"type":"system","id":"marshal-planning"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.failed","timestamp":"$EVENT_TS","stateFrom":"RUNNING","stateTo":"RETRY_PENDING","attemptId":"attempt:provider-1","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE","notBefore":"$VALID_NOT_BEFORE"}}
{"sequence":3,"type":"worker.started","runId":"current-ready","timestamp":"$EVENT_TS","stateFrom":"RETRY_PENDING","stateTo":"RUNNING","attemptId":"attempt:provider-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
EOF
PROVIDER_STARTED_JSON="$TMP/provider_started.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$PROVIDER_STARTED_JSON" && \
   python3 - "$PROVIDER_STARTED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    capacity = json.load(handle)["capacity"]
if capacity.get("providerStatus") != "backpressure" or capacity.get("providerSlotsAvailable") != 0:
    raise SystemExit("worker.started incorrectly cleared provider failure: %r" % capacity)
print("worker.started preserves provider backpressure OK")
PYEOF
then
  ok "worker.started 不冒充 Provider success"
else
  bad "worker.started 错误解除 Provider 背压"
fi

cat >> "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":4,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:provider-2","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
PROVIDER_RECOVERED_JSON="$TMP/provider_recovered.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" \
   MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((16 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=6.2 \
   run_watch --once --json > "$PROVIDER_RECOVERED_JSON" && \
   python3 - "$PROVIDER_RECOVERED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    capacity = json.load(f)["capacity"]
if capacity.get("providerStatus") != "ok" or capacity.get("providerSlotsAvailable") != 1 or capacity.get("slotsAvailable") != 1:
    raise SystemExit("newer successful provider signal did not recover slots: %r" % capacity)
print("newer provider success recovers capacity OK")
PYEOF
then
  ok "较新 worker.completed 恢复 Provider 容量"
else
  bad "Provider 恢复信号未解除旧背压"
fi

for completion_case in wrong_actor wrong_run wrong_attempt missing_started illegal_origin; do
  case "$completion_case" in
    wrong_actor)
      cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"current-ready","timestamp":"$EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:completion","actor":{"type":"system","id":"forged-runner"},"payload":{"result":"ok"}}
EOF
      ;;
    wrong_run)
      cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"forged-run","timestamp":"$EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.completed","runId":"forged-run","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
      ;;
    wrong_attempt)
      cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"current-ready","timestamp":"$EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:other","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
      ;;
    missing_started)
      cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"planning.inputs-frozen","timestamp":"$EVENT_TS","actor":{"type":"system","id":"marshal-planning"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
      ;;
    illegal_origin)
      cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"current-ready","timestamp":"$EVENT_TS","stateFrom":"BLOCKED","stateTo":"RUNNING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"qwen"}}
{"sequence":2,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
      ;;
  esac
  COMPLETION_INVALID_JSON="$TMP/completion_${completion_case}.json"
  if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$COMPLETION_INVALID_JSON" && \
     python3 - "$COMPLETION_INVALID_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
item = next(item for item in data["items"] if item["runId"] == "current-ready")
capacity = data["capacity"]
if item.get("journalStatus") != "unknown" or capacity.get("providerStatus") != "unknown" or capacity.get("slotsAvailable") != 0:
    raise SystemExit("invalid completion did not fail closed: %r %r" % (item, capacity))
print("invalid worker.completed lineage fails closed OK")
PYEOF
  then
    ok "worker.completed $completion_case fail closed"
  else
    bad "worker.completed $completion_case 未 fail closed"
  fi
done

cat > "$ROOT/runs/current-ready/events.jsonl" <<EOF
{"sequence":1,"type":"worker.started","runId":"current-ready","timestamp":"$EVENT_TS","stateFrom":"READY","stateTo":"RUNNING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"adapterId":"codex"}}
{"sequence":2,"type":"worker.completed","runId":"current-ready","timestamp":"$VALID_NOT_BEFORE","stateFrom":"RUNNING","stateTo":"VERIFYING","attemptId":"attempt:completion","actor":{"type":"system","id":"marshal-worker-runner"},"payload":{"result":"ok"}}
EOF
COMPLETION_ADAPTER_MISMATCH_JSON="$TMP/completion_adapter_mismatch.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$COMPLETION_ADAPTER_MISMATCH_JSON" && \
   python3 - "$COMPLETION_ADAPTER_MISMATCH_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    capacity = json.load(handle)["capacity"]
if capacity.get("providerStatus") != "unknown" or capacity.get("slotsAvailable") != 0:
    raise SystemExit("completion adapter drift did not fail closed: %r" % capacity)
print("completed adapter remains bound to frozen TaskSpec OK")
PYEOF
then
  ok "worker.completed Adapter 漂移 fail closed"
else
  bad "worker.completed 未绑定冻结 TaskSpec Adapter"
fi

CPU_UNKNOWN_JSON="$TMP/cpu_unknown.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" \
   MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES=$((16 * 1024 * 1024 * 1024)) \
   MARSHAL_WATCH_LOGICAL_CPUS=unavailable MARSHAL_WATCH_LOAD1M=0 \
   run_watch --once --json > "$CPU_UNKNOWN_JSON" && \
   python3 - "$CPU_UNKNOWN_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    c = json.load(f)["capacity"]
if c.get("cpuStatus") != "unknown" or c.get("slotsAvailable") != 0 or c.get("concurrencyAction") != "hold-concurrency":
    raise SystemExit("unknown CPU signal did not fail closed: %r" % c)
print("unknown critical capacity signal holds concurrency OK")
PYEOF
then
  ok "关键 CPU signal unknown 时 hold"
else
  bad "关键 signal unknown 未 fail closed"
fi

note "7b) CPU 同时考虑 logical cores、1m load 与 owned workers"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run cpu-owned RUNNING 10
cat > "$LEASEFILE" <<'EOF'
{"cpu-owned":"held-alive"}
EOF
CPU_OWNED_JSON="$TMP/cpu_owned.json"
if MARSHAL_WATCH_LOGICAL_CPUS=4 MARSHAL_WATCH_LOAD1M=0 \
   run_watch --once --json > "$CPU_OWNED_JSON" && \
   python3 - "$CPU_OWNED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    capacity = json.load(handle)["capacity"]
if capacity.get("activeOwnedWorkers") != 1 or capacity.get("ownedWorkerHeadroom") != 3 or capacity.get("cpuSlotsAvailable") != 3:
    raise SystemExit("owned workers not reflected in CPU capacity: %r" % capacity)
print("logical cores + load + owned workers CPU capacity OK")
PYEOF
then
  ok "CPU 容量纳入 owned workers"
else
  bad "CPU 容量遗漏 owned workers"
fi

note "7c) 待派发 Run 缺失 Adapter identity 时 Provider signal unknown 并 hold"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run provider-unknown READY 10
rm -f "$ROOT/runs/provider-unknown/task-spec.json"
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:provider-unknown","runIds":["provider-unknown"]}
EOF
PROVIDER_UNKNOWN_JSON="$TMP/provider_unknown.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$PROVIDER_UNKNOWN_JSON" && \
   python3 - "$PROVIDER_UNKNOWN_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    capacity = json.load(handle)["capacity"]
if capacity.get("providerStatus") != "unknown" or capacity.get("slotsAvailable") != 0 or capacity.get("concurrencyAction") != "hold-concurrency":
    raise SystemExit("unknown adapter identity did not fail closed: %r" % capacity)
print("unknown adapter identity holds provider capacity OK")
PYEOF
then
  ok "Adapter identity unknown 时 Provider hold"
else
  bad "Adapter identity unknown 未 fail closed"
fi

note "8) notBefore 严格相对 worker.failed timestamp 落在 (0,24h]"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run retry-hint READY 10
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:retry-hint","runIds":["retry-hint"]}
EOF
python3 - "$EVENT_TS" "$TMP/retry-times.json" <<'PYEOF'
import datetime, json, sys
stamp = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
def render(value):
    return value.isoformat().replace("+00:00", "Z")
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    json.dump({"exact24h": render(stamp + datetime.timedelta(hours=24)),
               "over24h": render(stamp + datetime.timedelta(hours=24, seconds=1)),
               "equal": render(stamp)}, handle)
PYEOF
EXACT_24H=$(jq -r .exact24h "$TMP/retry-times.json")
OVER_24H=$(jq -r .over24h "$TMP/retry-times.json")
EQUAL_TS=$(jq -r .equal "$TMP/retry-times.json")
check_retry_hint() {
  local case_name="$1" event_timestamp="$2" not_before_json="$3" expected="$4"
  cat > "$ROOT/runs/retry-hint/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":$event_timestamp,"attemptId":"attempt:retry-hint","payload":{"adapterId":"qwen","failureKind":"rate-limited","retryDisposition":"retryable","failureSignature":"$FAILURE_SIGNATURE","notBefore":$not_before_json}}
EOF
  local output="$TMP/retry_hint_${case_name}.json"
  if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$output" && \
     python3 - "$output" "$expected" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
item = data["items"][0]
capacity = data["capacity"]
if sys.argv[2] == "valid":
    if item.get("journalStatus") != "ok" or capacity.get("providerStatus") != "backpressure":
        raise SystemExit("valid exact-bound hint rejected: %r %r" % (item, capacity))
else:
    if item.get("journalStatus") != "unknown" or capacity.get("providerStatus") != "unknown" or capacity.get("slotsAvailable") != 0:
        raise SystemExit("invalid hint did not become run-level unknown: %r %r" % (item, capacity))
print("retry hint case OK: " + sys.argv[2])
PYEOF
  then
    ok "retry hint $case_name => $expected"
  else
    bad "retry hint $case_name 未按 $expected 处理"
  fi
}
check_retry_hint exact24h "\"$EVENT_TS\"" "\"$EXACT_24H\"" valid
check_retry_hint over24h "\"$EVENT_TS\"" "\"$OVER_24H\"" unknown
check_retry_hint equal "\"$EVENT_TS\"" "\"$EQUAL_TS\"" unknown
check_retry_hint far_future "\"$EVENT_TS\"" '"2099-01-01T00:00:00Z"' unknown
check_retry_hint missing_timestamp null "\"$VALID_NOT_BEFORE\"" unknown
check_retry_hint wrong_timestamp_type '[]' "\"$VALID_NOT_BEFORE\"" unknown
check_retry_hint wrong_notbefore_type "\"$EVENT_TS\"" '[]' unknown

note "9) 畸形 state/journal 只污染对应 Run，不崩整轮"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run bad-state READY 10
make_run bad-journal READY 10
make_run good-ready READY 10
python3 - "$ROOT/runs/bad-state/state.json" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["state"] = {}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle)
PYEOF
cat > "$ROOT/runs/bad-journal/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","payload":[]}
EOF
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:malformed-runs","runIds":["bad-state","bad-journal","good-ready"]}
EOF
MALFORMED_JSON="$TMP/malformed_runs.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$MALFORMED_JSON" && \
   python3 - "$MALFORMED_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
by_id = {item["runId"]: item for item in data["items"]}
if set(by_id) != {"bad-state", "bad-journal", "good-ready"}:
    raise SystemExit("malformed run disappeared or contaminated peers: %r" % by_id)
if by_id["bad-state"].get("action") != "hold-run-invalid" or by_id["bad-state"].get("journalStatus") != "unknown":
    raise SystemExit("bad state not isolated as unknown: %r" % by_id["bad-state"])
if by_id["bad-journal"].get("journalStatus") != "unknown":
    raise SystemExit("bad journal not isolated as unknown: %r" % by_id["bad-journal"])
if by_id["good-ready"].get("journalStatus") == "unknown":
    raise SystemExit("good peer contaminated: %r" % by_id["good-ready"])
print("malformed state/journal isolation OK")
PYEOF
then
  ok "畸形 state/journal 按 Run 隔离"
else
  bad "畸形 state/journal 导致漏项或整轮崩溃"
fi

note "10) run parent nofollow + bounded evidence digest"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs" "$TMP/external-run"
make_run bounded-review REVIEW_PENDING 10
printf '%s\n' '{}' > "$ROOT/runs/bounded-review/review-packet.json"
cp "$ROOT/runs/bounded-review/state.json" "$TMP/external-run/state.json"
cp "$ROOT/runs/bounded-review/task-spec.json" "$TMP/external-run/task-spec.json"
python3 - "$TMP/external-run/state.json" <<'PYEOF'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)
data["runId"] = "symlink-run"
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(data, handle)
PYEOF
ln -s "$TMP/external-run" "$ROOT/runs/symlink-run"
python3 - "$ROOT/runs/bounded-review/review-packet.json" <<'PYEOF'
import sys
with open(sys.argv[1], "wb") as handle:
    handle.truncate(8 * 1024 * 1024 + 1)
PYEOF
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:path-bounds","runIds":["bounded-review","symlink-run"]}
EOF
BOUNDED_JSON_1="$TMP/bounded_1.json"
MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$BOUNDED_JSON_1"
python3 - "$ROOT/runs/bounded-review/review-packet.json" <<'PYEOF'
import sys
with open(sys.argv[1], "wb") as handle:
    handle.write(b"x" * (8 * 1024 * 1024 + 1))
PYEOF
BOUNDED_JSON_2="$TMP/bounded_2.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$BOUNDED_JSON_2" && \
   python3 - "$BOUNDED_JSON_1" "$BOUNDED_JSON_2" <<'PYEOF'
import json, sys
def by_id(path):
    with open(path) as handle:
        return {item["runId"]: item for item in json.load(handle)["items"]}
first, second = by_id(sys.argv[1]), by_id(sys.argv[2])
if first.get("symlink-run", {}).get("action") != "hold-run-path-unknown":
    raise SystemExit("symlink run parent was followed: %r" % first.get("symlink-run"))
bounded = first.get("bounded-review", {})
if bounded.get("evidenceStatus") != "unknown" or bounded.get("action") != "review-intervention":
    raise SystemExit("oversized review evidence not bounded: %r" % bounded)
if bounded.get("dedupeKey") != second.get("bounded-review", {}).get("dedupeKey"):
    raise SystemExit("oversized evidence marker is content-dependent")
print("nofollow run parent + stable bounded evidence marker OK")
PYEOF
then
  ok "run parent nofollow 且超限证据 stable unknown"
else
  bad "run parent 或 evidence digest 边界失效"
fi

note "11) Qoder/Codex 真实 basename 仅影响 argvMatched 诊断"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run argv-qoder RUNNING 10
make_run argv-qoder-version RUNNING 10
make_run argv-codex-platform RUNNING 10
cat > "$LEASEFILE" <<'EOF'
{"argv-qoder":"not-held","argv-qoder-version":"not-held","argv-codex-platform":"not-held"}
EOF
cat > "$PROCFILE" <<'EOF'
100 /opt/tools/qodercli --run argv-qoder
101 /opt/tools/qodercli-1.1.23 --run argv-qoder-version
102 /opt/tools/codex-aarch64-apple-darwin --run argv-codex-platform
EOF
ARGV_REAL_JSON="$TMP/argv_real_basenames.json"
if run_watch --once --json > "$ARGV_REAL_JSON" && \
   python3 - "$ARGV_REAL_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    items = json.load(handle)["items"]
for item in items:
    if item.get("argvMatched") is not True or item.get("processOwnership") != "not-found" or item.get("action") != "doctor-dead":
        raise SystemExit("argv diagnostic changed authority or missed basename: %r" % item)
print("real adapter basenames are diagnostic-only")
PYEOF
then
  ok "真实 Qoder/Codex basename 命中且不提升 authority"
else
  bad "真实 basename 诊断遗漏或污染 authority"
fi

note "12) 相对 authority root 与 basename cohort/lease 从 / nofollow 打开"
RELATIVE_WS="$TMP/relative-workspace"
mkdir -p "$RELATIVE_WS/scripts" "$RELATIVE_WS/relative-root/runs"
cp "$WATCH" "$RELATIVE_WS/scripts/marshal-watch.sh"
for relative_case in relative-ready relative-peer relative-running; do
  relative_state=READY
  [ "$relative_case" = "relative-running" ] && relative_state=RUNNING
  mkdir -p "$RELATIVE_WS/relative-root/runs/$relative_case"
  cat > "$RELATIVE_WS/relative-root/runs/$relative_case/state.json" <<EOF
{"apiVersion":"marshal.dev/v1alpha1","kind":"RunState","taskId":"task-$relative_case","runId":"$relative_case","state":"$relative_state","sequence":1,"specDigest":"sha256:spec-$relative_case","policyDigest":"sha256:policy-$relative_case","capabilityDigest":"sha256:capability-$relative_case","baseSha":"base-$relative_case","reviewRound":0,"attemptsUsed":0,"operationalRetriesUsed":0,"reworkRoundsUsed":0,"createdAt":"$EVENT_TS","updatedAt":"$EVENT_TS"}
EOF
  cat > "$RELATIVE_WS/relative-root/runs/$relative_case/task-spec.json" <<'EOF'
{"worker":{"preferredAdapter":"qwen","fallbackAdapters":[]}}
EOF
done
cat > "$RELATIVE_WS/cohort.json" <<'EOF'
{"goalId":"goal:relative-authority","runIds":["relative-ready","relative-peer","relative-running"]}
EOF
cat > "$RELATIVE_WS/facts.json" <<'EOF'
{"relative-ready":"not-held","relative-peer":"not-held","relative-running":"not-held"}
EOF
run_relative_watch() {
  (cd "$RELATIVE_WS" && \
    MARSHAL_WATCH_ROOT="${MARSHAL_WATCH_ROOT-relative-root}" \
    MARSHAL_WATCH_COHORT_FILE="${MARSHAL_WATCH_COHORT_FILE-cohort.json}" \
    MARSHAL_WATCH_LEASE_FACTS_FILE="${MARSHAL_WATCH_LEASE_FACTS_FILE-facts.json}" \
    MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" MARSHAL_WATCH_NOTIFY=0 \
    MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=0 \
    MARSHAL_WATCH_SWAP_USED_BYTES=0 MARSHAL_WATCH_PRESSURE_FREE_PERCENT=80 \
    MARSHAL_WATCH_TESTING="${MARSHAL_WATCH_TESTING-}" \
    MARSHAL_WATCH_TEST_RUN_ENTRY_HOOK="${MARSHAL_WATCH_TEST_RUN_ENTRY_HOOK-}" \
    bash scripts/marshal-watch.sh "$@")
}
RELATIVE_JSON="$TMP/relative_authority.json"
if run_relative_watch --once --json > "$RELATIVE_JSON" && \
   python3 - "$RELATIVE_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    data = json.load(handle)
by_id = {item["runId"]: item for item in data["items"]}
if data.get("cohort", {}).get("source") != "explicit-goal-cohort":
    raise SystemExit("basename cohort not loaded: %r" % data.get("cohort"))
for run_id in ("relative-ready", "relative-peer"):
    if by_id.get(run_id, {}).get("action") != "run-now" or by_id[run_id].get("ownershipSource") != "fixture":
        raise SystemExit("relative authority positive failed: %s=%r" % (run_id, by_id.get(run_id)))
running = by_id.get("relative-running", {})
if running.get("action") != "doctor-dead" or running.get("ownershipSource") != "fixture":
    raise SystemExit("basename lease facts not loaded: %r" % running)
print("relative root and basename authority files OK")
PYEOF
then
  ok "相对 root 与 basename cohort/lease 正常工作"
else
  bad "相对 root 或 basename authority 文件失效"
fi

ln -s relative-root "$RELATIVE_WS/relative-root-link"
if MARSHAL_WATCH_ROOT=relative-root-link run_relative_watch --once --json > "$TMP/relative_root_link.out" 2>/dev/null; then
  bad "相对 authority root symlink 未 fail closed"
else
  ok "相对 authority root symlink fail closed"
fi
RELATIVE_SWAP_JSON="$TMP/relative_swap.json"
if MARSHAL_WATCH_TESTING=1 MARSHAL_WATCH_TEST_RUN_ENTRY_HOOK=relative-ready \
   run_relative_watch --once --json > "$RELATIVE_SWAP_JSON" && \
   python3 - "$RELATIVE_SWAP_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    by_id = {item["runId"]: item for item in json.load(handle)["items"]}
if by_id.get("relative-ready", {}).get("action") != "hold-run-path-unknown":
    raise SystemExit("relative run race did not fail closed: %r" % by_id.get("relative-ready"))
if by_id.get("relative-peer", {}).get("action") != "run-now":
    raise SystemExit("relative run race contaminated peer: %r" % by_id.get("relative-peer"))
print("relative run race isolated")
PYEOF
then
  ok "相对 authority run race 按 Run 隔离"
else
  bad "相对 authority run race 未隔离"
fi

note "12b) authority root/runs symlink 拒绝，run entry swap 只污染该 Run"
ROOT_LINK="$TMP/root-link"
ln -s "$ROOT" "$ROOT_LINK"
ROOT_LINK_OUT="$TMP/root_link.out"
if MARSHAL_WATCH_ROOT="$ROOT_LINK" MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" \
   MARSHAL_WATCH_LEASE_FACTS_FILE="$LEASEFILE" MARSHAL_WATCH_NOTIFY=0 \
   MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=0 \
   MARSHAL_WATCH_SWAP_USED_BYTES=0 MARSHAL_WATCH_PRESSURE_FREE_PERCENT=80 \
   bash "$WATCH" --once --json > "$ROOT_LINK_OUT" 2>/dev/null; then
  bad "authority root symlink 未 fail closed"
else
  ok "authority root symlink fail closed"
fi
RUNS_LINK_ROOT="$TMP/runs-link-root"
mkdir -p "$RUNS_LINK_ROOT"
ln -s "$ROOT/runs" "$RUNS_LINK_ROOT/runs"
RUNS_LINK_OUT="$TMP/runs_link.out"
if MARSHAL_WATCH_ROOT="$RUNS_LINK_ROOT" MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" \
   MARSHAL_WATCH_LEASE_FACTS_FILE="$LEASEFILE" MARSHAL_WATCH_NOTIFY=0 \
   MARSHAL_WATCH_LOGICAL_CPUS=8 MARSHAL_WATCH_LOAD1M=0 \
   MARSHAL_WATCH_SWAP_USED_BYTES=0 MARSHAL_WATCH_PRESSURE_FREE_PERCENT=80 \
   bash "$WATCH" --once --json > "$RUNS_LINK_OUT" 2>/dev/null; then
  bad "authority runs symlink 未 fail closed"
else
  ok "authority runs symlink fail closed"
fi
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
make_run swap-run READY 10
make_run swap-peer READY 10
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:run-swap","runIds":["swap-run","swap-peer"]}
EOF
SWAP_JSON="$TMP/run_swap.json"
if MARSHAL_WATCH_TESTING=1 MARSHAL_WATCH_TEST_RUN_ENTRY_HOOK=swap-run \
   MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$SWAP_JSON" && \
   python3 - "$SWAP_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    by_id = {item["runId"]: item for item in json.load(handle)["items"]}
if by_id.get("swap-run", {}).get("action") != "hold-run-path-unknown":
    raise SystemExit("run entry swap did not fail closed: %r" % by_id.get("swap-run"))
if by_id.get("swap-peer", {}).get("action") != "run-now":
    raise SystemExit("run entry swap contaminated peer: %r" % by_id.get("swap-peer"))
print("run entry swap isolated")
PYEOF
then
  ok "run entry swap/race fixture 按 Run 隔离"
else
  bad "run entry swap/race 未隔离"
fi

note "13) payload.adapterId 非 exact string 时按 Run unknown，后续健康 Run 保留"
rm -rf "$ROOT/runs"
mkdir -p "$ROOT/runs"
for adapter_case in list dict null number; do
  make_run "adapter-$adapter_case" READY 10
done
make_run adapter-peer READY 10
cat > "$ROOT/runs/adapter-list/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","payload":{"adapterId":[],"failureKind":"rate-limited","retryDisposition":"retryable"}}
EOF
cat > "$ROOT/runs/adapter-dict/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","payload":{"adapterId":{},"failureKind":"rate-limited","retryDisposition":"retryable"}}
EOF
cat > "$ROOT/runs/adapter-null/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","payload":{"adapterId":null,"failureKind":"rate-limited","retryDisposition":"retryable"}}
EOF
cat > "$ROOT/runs/adapter-number/events.jsonl" <<EOF
{"sequence":1,"type":"worker.failed","timestamp":"$EVENT_TS","payload":{"adapterId":7,"failureKind":"rate-limited","retryDisposition":"retryable"}}
EOF
cat > "$COHORTFILE" <<'EOF'
{"goalId":"goal:adapter-types","runIds":["adapter-list","adapter-dict","adapter-null","adapter-number","adapter-peer"]}
EOF
ADAPTER_TYPES_JSON="$TMP/adapter_types.json"
if MARSHAL_WATCH_COHORT_FILE="$COHORTFILE" run_watch --once --json > "$ADAPTER_TYPES_JSON" && \
   python3 - "$ADAPTER_TYPES_JSON" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as handle:
    by_id = {item["runId"]: item for item in json.load(handle)["items"]}
for run_id in ("adapter-list", "adapter-dict", "adapter-null", "adapter-number"):
    if by_id.get(run_id, {}).get("journalStatus") != "unknown":
        raise SystemExit("malformed adapterId not isolated: %s=%r" % (run_id, by_id.get(run_id)))
if by_id.get("adapter-peer", {}).get("action") != "run-now" or by_id["adapter-peer"].get("journalStatus") == "unknown":
    raise SystemExit("malformed adapterId contaminated valid peer: %r" % by_id.get("adapter-peer"))
print("adapterId type isolation OK")
PYEOF
then
  ok "payload.adapterId 类型按 Run 隔离"
else
  bad "payload.adapterId 畸形导致崩溃或污染健康 Run"
fi

printf '\n# 汇总: %d 通过, %d 失败\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
