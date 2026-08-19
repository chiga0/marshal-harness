#!/usr/bin/env bash
# Marshal 心跳 watchdog 与行动队列（operator-runbook §11）：
# 防"挂了毫无感知"，并消除"持续报告但没有交付动作"的空转。
#
# 循环模式（向后兼容旧入口，用法不变）:
#   nohup scripts/marshal-watch.sh [interval_sec] &
#   每 interval（默认 600 秒）轮询 $MARSHAL_WATCH_ROOT/runs/*/state.json，
#   输出一行文本状态，tee 到 $MARSHAL_WATCH_LOG 并 osascript 通知。
#
# 一次性模式（heartbeat 机器可读，执行一次立即退出，不 sleep）:
#   scripts/marshal-watch.sh --once --json
#   --json 输出按优先级排序的行动队列：generatedAt、capacity 与 items，
#   capacity 每次心跳重新读取 Mac/Linux 可用内存，并给出可安全增加的
#   Worker 槽位；watchdog 只报告建议，实际 task run 仍由 Core 按 scope/lease
#   门禁派发，避免 watchdog 越权改变 Run 生命周期。
#   只输出稳定状态、动作、runId、年龄与归属判定；不含 secret、命令行、绝对路径。
#
# 动作映射（priority 越小越优先）:
#   REVIEW_PENDING=review-now(10)  REWORK_REQUESTED=run-rework-now(20)
#   RUNNING 无可证明归属进程=doctor-dead(30)  RETRY_PENDING=retry-or-abort(40)
#   VERIFYING=verify-or-doctor(50)  PUBLISHING=publish-or-doctor(60)
#   READY/APPROVED=run-now(70)  CI_PENDING=check-ci(80)  RUNNING(active)=monitor(90)
#   终态（ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE）不进入行动队列。
#
# 环境变量:
#   MARSHAL_WATCH_ROOT          含 runs 目录的根，默认 .marshal（测试可指向 fixture）
#   MARSHAL_WATCH_NOTIFY=0      禁用 osascript 通知
#   MARSHAL_WATCH_LOG           循环模式日志，默认 /tmp/marshal-watch.log
#   MARSHAL_WATCH_PROCESS_FILE  进程 fixture 文件（每行 "<pid> <command>"）；
#                               设置后进程归属只按该文件内容判定，不触碰真实进程。
#   MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES  覆盖可用内存（仅测试/诊断）；
#   MARSHAL_WATCH_WORKER_RESERVE_BYTES   每个新增 Worker 的保守内存预算，默认 2 GiB。
cd "$(dirname "$0")/.." || exit 1

usage() {
  cat <<'EOF'
用法: scripts/marshal-watch.sh [--once] [--json] [interval_sec]

  --once        执行一次后立即退出，不进入循环、不 sleep
  --json        输出机器可读行动队列（建议与 --once 组合供 heartbeat 消费）
  interval_sec  循环模式轮询间隔秒数，默认 600（向后兼容旧入口）

环境变量: MARSHAL_WATCH_ROOT / MARSHAL_WATCH_NOTIFY / MARSHAL_WATCH_LOG /
          MARSHAL_WATCH_PROCESS_FILE（见脚本头部注释）
EOF
}

ONCE=0
JSON=0
INTERVAL=600
for arg in "$@"; do
  case "$arg" in
    --once) ONCE=1 ;;
    --json) JSON=1 ;;
    -h|--help) usage; exit 0 ;;
    ''|*[!0-9]*) echo "无效参数: $arg（期望 --once、--json 或数字间隔秒数）" >&2; exit 2 ;;
    *) INTERVAL="$arg" ;;
  esac
done

ROOT="${MARSHAL_WATCH_ROOT:-.marshal}"
RUNS_DIR="$ROOT/runs"
[ -d "$RUNS_DIR" ] || { echo "找不到 runs 目录: $RUNS_DIR（用 MARSHAL_WATCH_ROOT 指向含 runs 的根）" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "缺少依赖: python3" >&2; exit 1; }

NOTIFY_ENABLED=1
[ "${MARSHAL_WATCH_NOTIFY:-1}" = "0" ] && NOTIFY_ENABLED=0
LOG="${MARSHAL_WATCH_LOG:-/tmp/marshal-watch.log}"

notify() {
  [ "$NOTIFY_ENABLED" = "1" ] || return 0
  [ -n "$1" ] || return 0
  osascript -e "display notification \"$1\" with title \"Marshal watch\"" 2>/dev/null
  return 0
}

# 进程快照：设置了 MARSHAL_WATCH_PROCESS_FILE 时只读 fixture，不触碰真实进程。
process_snapshot() {
  if [ -n "${MARSHAL_WATCH_PROCESS_FILE:-}" ]; then
    cat "$MARSHAL_WATCH_PROCESS_FILE" 2>/dev/null
  else
    ps -eo pid=,args= 2>/dev/null
  fi
  return 0
}

PY_PROG='
import json, os, re, sys
from datetime import datetime, timezone

runs_dir, mode = sys.argv[1], sys.argv[2]
summary_path = sys.argv[3] if len(sys.argv) > 3 else ""
now_utc = datetime.now(timezone.utc)

# 可行动状态 -> (priority, action)；数字越小优先级越高。
ACTIONABLE = {
    "REVIEW_PENDING": (10, "review-now"),
    "REWORK_REQUESTED": (20, "run-rework-now"),
    "RETRY_PENDING": (40, "retry-or-abort"),
    "VERIFYING": (50, "verify-or-doctor"),
    "PUBLISHING": (60, "publish-or-doctor"),
    "READY": (70, "run-now"),
    "APPROVED": (70, "run-now"),
    "CI_PENDING": (80, "check-ci"),
}
# 终态 ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE 不在 ACTIONABLE 中，
# 因此默认不进入行动队列（只保留在文本模式的状态行里）。
MARSHAL_SUBCOMMANDS = ("run", "verify", "publish", "supervise")
ADAPTER_BINARIES = {"qwen", "codex", "qoder", "opencode", "pi"}
DEFAULT_WORKER_RESERVE_BYTES = 2 * 1024 * 1024 * 1024

def process_lines():
    lines = []
    for raw in sys.stdin:
        line = raw.strip()
        if line:
            lines.append(line)
    return lines

def owner_present(line, run_id):
    # 可证明归属 = marshal CLI 的 task run/verify/publish/supervise，
    # 或 qwen/codex/qoder/opencode/pi 之一，且命令行绑定精确 runId。
    tokens = line.split()
    named = False
    for index, token in enumerate(tokens):
        base = token.rsplit("/", 1)[-1]
        if base in ADAPTER_BINARIES:
            named = True
            break
        if base == "marshal":
            rest = tokens[index + 1:]
            if "task" in rest and any(sub in rest for sub in MARSHAL_SUBCOMMANDS):
                named = True
                break
    if not named:
        return False
    # runId 必须是独立的 [-\w] token，禁止前缀/子串误判其他 Run。
    pattern = r"(?<![\w-])" + re.escape(run_id) + r"(?![\w-])"
    return re.search(pattern, line) is not None

def _integer_env(name):
    try:
        value = int(os.environ.get(name, ""))
        return value if value >= 0 else None
    except (TypeError, ValueError):
        return None

def _command_output(argv):
    try:
        import subprocess
        return subprocess.check_output(argv, stderr=subprocess.DEVNULL, text=True)
    except (OSError, subprocess.SubprocessError):
        return ""

def memory_available_bytes():
    # Fixture/diagnostic override keeps the shell test deterministic while the
    # normal heartbeat always samples the host rather than trusting a cache.
    override = _integer_env("MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES")
    if override is not None:
        return override, "override"
    if sys.platform == "darwin":
        total_text = _command_output(["sysctl", "-n", "hw.memsize"]).strip()
        stat = _command_output(["vm_stat"])
        page_size = 4096
        match = re.search(r"page size of (\d+) bytes", stat)
        if match:
            page_size = int(match.group(1))
        pages = {}
        for line in stat.splitlines():
            match = re.match(r"Pages? ([^:]+):\s+(\d+)", line)
            if match:
                pages[match.group(1).strip().lower()] = int(match.group(2))
        # Purgeable/speculative pages are reclaimable; include them so a
        # healthy host is not needlessly serialized, but keep the reserve
        # budget conservative (2 GiB per additional Worker by default).
        available = sum(pages.get(key, 0) for key in ("free", "purgeable", "speculative")) * page_size
        if available > 0:
            return available, "darwin-vm_stat"
        try:
            return int(total_text), "darwin-total-fallback"
        except ValueError:
            return 0, "unavailable"
    try:
        with open("/proc/meminfo", "r", encoding="utf-8") as handle:
            values = {}
            for line in handle:
                key, _, rest = line.partition(":")
                if key in ("MemAvailable", "MemFree", "MemTotal"):
                    values[key] = int(rest.strip().split()[0]) * 1024
            return values.get("MemAvailable", values.get("MemFree", 0)), "proc-meminfo"
    except (OSError, ValueError):
        return 0, "unavailable"

def capacity_snapshot(active_owned):
    available, source = memory_available_bytes()
    reserve = _integer_env("MARSHAL_WATCH_WORKER_RESERVE_BYTES")
    if reserve is None or reserve == 0:
        reserve = DEFAULT_WORKER_RESERVE_BYTES
    slots = available // reserve if reserve > 0 else 0
    pressure = "critical" if available < reserve // 2 else ("constrained" if available < reserve else "ok")
    return {
        "memoryAvailableBytes": available,
        "memorySource": source,
        "workerReserveBytes": reserve,
        "activeOwnedWorkers": active_owned,
        "slotsAvailable": slots,
        "recommendedMaxWorkers": active_owned + slots,
        "concurrencyAction": "increase-concurrency" if slots > 0 else "hold-concurrency",
        "pressure": pressure,
    }

def parse_timestamp(stamp):
    if not isinstance(stamp, str):
        return None
    cleaned = stamp.strip().replace("Z", "+00:00")
    cleaned = re.sub(r"\.(\d{6})\d+", r".\1", cleaned)
    try:
        parsed = datetime.fromisoformat(cleaned)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed

def age_seconds(state_path, data):
    parsed = parse_timestamp(data.get("updatedAt") if isinstance(data, dict) else None)
    if parsed is not None:
        seconds = int((now_utc - parsed).total_seconds())
        return seconds if seconds > 0 else 0
    try:
        mtime = os.path.getmtime(state_path)
    except OSError:
        return 0
    seconds = int(now_utc.timestamp() - mtime)
    return seconds if seconds > 0 else 0

procs = process_lines()
items, text_tokens = [], []
owned_runs = set()
for run_id in sorted(os.listdir(runs_dir)):
    run_dir = os.path.join(runs_dir, run_id)
    state_path = os.path.join(run_dir, "state.json")
    if not os.path.isfile(state_path):
        continue
    try:
        with open(state_path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
        state = data["state"]
    except (OSError, ValueError, KeyError, TypeError):
        continue
    owned = any(owner_present(line, run_id) for line in procs)
    if owned and state in {"RUNNING", "VERIFYING", "PUBLISHING"}:
        owned_runs.add(run_id)
    item = None
    if state == "RUNNING":
        if owned:
            item = {"runId": run_id, "state": state, "priority": 90,
                    "action": "monitor", "processOwnership": "owned-active"}
        else:
            item = {"runId": run_id, "state": state, "priority": 30,
                    "action": "doctor-dead", "processOwnership": "not-found"}
    elif state in ACTIONABLE:
        priority, action = ACTIONABLE[state]
        item = {"runId": run_id, "state": state, "priority": priority,
                "action": action, "processOwnership": "not-applicable"}
        if state == "REVIEW_PENDING" and not os.path.isfile(os.path.join(run_dir, "review-packet.json")):
            # A missing packet means `task review` cannot yet bind a
            # ReviewDecision. Surface this as an intervention instead of
            # repeatedly asking the lead to rerun the same doomed review.
            item["priority"] = 5
            item["action"] = "review-intervention"
    # 终态与其他未映射状态一律不进入行动队列。
    if item is not None:
        item["ageSeconds"] = age_seconds(state_path, data)
        items.append(item)
    if mode == "text":
        if state == "RUNNING":
            text_tokens.append("%s=%s" % (run_id, "RUNNING(active)" if owned else "DEAD?"))
        else:
            text_tokens.append("%s=%s" % (run_id, state))

items.sort(key=lambda entry: (entry["priority"], entry["runId"]))
capacity = capacity_snapshot(len(owned_runs))
if mode == "text":
    print("[%s] %s capacity=%s slots=%s" % (datetime.now().strftime("%m-%d %H:%M:%S"), " ".join(text_tokens), capacity["pressure"], capacity["slotsAvailable"]))
else:
    print(json.dumps({"generatedAt": now_utc.strftime("%Y-%m-%dT%H:%M:%SZ"), "capacity": capacity, "items": items},
                     ensure_ascii=False))
if summary_path:
    try:
        with open(summary_path, "w", encoding="utf-8") as handle:
            if items:
                top = items[0]
                handle.write("行动队列 %d 项，最高优先级 %s=%s" % (len(items), top["runId"], top["action"]))
            else:
                handle.write("行动队列无待办行动项")
    except OSError:
        pass
'

run_pass() {
  process_snapshot | python3 -c "$PY_PROG" "$RUNS_DIR" "$1" "${2:-}"
}

if [ "$ONCE" = "1" ]; then
  if [ "$JSON" = "1" ]; then
    run_pass json ""
  else
    run_pass text ""
  fi
  exit 0
fi

SUMMARY_FILE="${TMPDIR:-/tmp}/marshal-watch.$$.summary"
trap 'rm -f "$SUMMARY_FILE"' EXIT
while true; do
  if [ "$JSON" = "1" ]; then
    output=$(run_pass json "$SUMMARY_FILE")
    echo "$output" | tee -a "$LOG"
    notify "$(cat "$SUMMARY_FILE" 2>/dev/null)"
  else
    line=$(run_pass text "")
    echo "$line" | tee -a "$LOG"
    notify "${line#*] }"
  fi
  sleep "$INTERVAL"
done
