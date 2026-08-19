#!/usr/bin/env bash
# scripts/marshal-watch.sh 的确定性测试（operator-runbook §11）。
# 只使用临时 MARSHAL_WATCH_ROOT 与 MARSHAL_WATCH_PROCESS_FILE fixture：
# 不读取/删除/改写真实 .marshal，不依赖网络、osascript 或真实 Worker 进程。
# 覆盖：行动队列优先级排序、终态过滤、--once 不 sleep、JSON schema 基本字段、
#       RUNNING owned-active 与 doctor-dead 两种分支、精确 runId 绑定。
set -u

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
WATCH="$SCRIPT_DIR/marshal-watch.sh"

PASS=0
FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   - %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf 'FAIL - %s\n' "$1" >&2; }
note() { printf '# %s\n' "$1"; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/marshal-watch-test.XXXXXX")"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

ROOT="$TMP/root"
PROCFILE="$TMP/procs.txt"
mkdir -p "$ROOT/runs"
: > "$PROCFILE"

# 统一以 fixture 根与受控进程文件运行，禁用通知，避免触碰真实环境。
run_watch() {
  MARSHAL_WATCH_ROOT="$ROOT" \
  MARSHAL_WATCH_PROCESS_FILE="$PROCFILE" \
  MARSHAL_WATCH_NOTIFY=0 \
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
{"apiVersion":"marshal.dev/v1alpha1","kind":"RunState","taskId":"task-$rid","runId":"$rid","state":"$state","sequence":1,"specDigest":"sha256:spec-$rid","policyDigest":"sha256:policy-$rid","capabilityDigest":"sha256:capability-$rid","baseSha":"base-$rid","reviewRound":0,"attemptsUsed":0,"operationalRetriesUsed":0,"reworkRoundsUsed":0,"createdAt":"$ts","updatedAt":"$ts"}
EOF
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

printf '\n# 汇总: %d 通过, %d 失败\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
