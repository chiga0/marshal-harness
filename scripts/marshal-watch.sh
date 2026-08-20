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
#   --json 输出 v2 行动队列：current items/topAction、unscopedItems
#   与 historicalItems 分桶；
#   capacity 每次心跳重新读取 Mac/Linux 内存、CPU 与 Provider typed failure，
#   slots=min(memory,cpu,provider)。watchdog 只报告建议，实际 task run 仍由
#   Core 按 scope/lease 门禁派发，避免 watchdog 越权改变 Run 生命周期。
#   dedupeKey 绑定 journal sequence/phase digest/root+latest typed failure/notBefore；只输出
#   稳定状态、动作、runId、年龄与归属判定，不含 secret、命令行或绝对路径。
#
# 动作映射（priority 越小越优先）:
#   REVIEW_PENDING=review-now(10)  REWORK_REQUESTED=run-rework-now(20)
#   RUNNING 无 held lease=doctor-dead(30)  RETRY_PENDING 仅当前 typed
#   lineage=retry-or-abort(40)，否则 retry-intervention(6)
#   VERIFYING=verify-or-doctor(50)  PUBLISHING=publish-or-doctor(60)
#   READY/APPROVED=run-now(70)  CI_PENDING=check-ci(80)  RUNNING(active)=monitor(90)
#   终态（ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE）不进入行动队列。
#
# 环境变量:
#   MARSHAL_WATCH_ROOT          含 runs 目录的根，默认 .marshal（测试可指向 fixture）
#   MARSHAL_WATCH_NOTIFY=0      禁用 osascript 通知
#   MARSHAL_WATCH_LOG           循环模式日志，默认 /tmp/marshal-watch.log
#   MARSHAL_WATCH_PROCESS_FILE  进程 fixture 文件（每行 "<pid> <command>"）；
#                               仅用于 argv 诊断，不是动作所有权权威。
#   MARSHAL_WATCH_LEASE_FACTS_FILE  仅测试使用的 lease/owner 事实 JSON；生产
#                               默认直接探测 Marshal lease.lock/owner。
#   MARSHAL_WATCH_COHORT_FILE   当前 Goal/cohort JSON：goalId + runIds；未设置时
#                               只有 held-alive Run 归为 current，其余非终态进
#                               unscopedItems，不产生 topAction。
#   MARSHAL_WATCH_MEMORY_AVAILABLE_BYTES  覆盖可用内存（仅测试/诊断）；
#   MARSHAL_WATCH_SWAP_USED_BYTES         覆盖已用 swap（仅测试/诊断）；
#   MARSHAL_WATCH_SWAP_OUTPUT             覆盖 vm.swapusage 原始输出（仅测试）；
#   MARSHAL_WATCH_PRESSURE_FREE_PERCENT   覆盖当前 free percentage；值
#                                         unavailable 用于 fail-closed 测试；
#   MARSHAL_WATCH_PRESSURE_OUTPUT         覆盖 memory_pressure 原始输出（仅测试）；
#   MARSHAL_WATCH_WORKER_RESERVE_BYTES   每个新增 Worker 的保守内存预算，默认 2 GiB。
#   MARSHAL_WATCH_LOGICAL_CPUS / MARSHAL_WATCH_LOAD1M  CPU 探针覆盖（仅测试/诊断）。
cd "$(dirname "$0")/.." || exit 1

usage() {
  cat <<'EOF'
用法: scripts/marshal-watch.sh [--once] [--json] [interval_sec]

  --once        执行一次后立即退出，不进入循环、不 sleep
  --json        输出机器可读行动队列（建议与 --once 组合供 heartbeat 消费）
  interval_sec  循环模式轮询间隔秒数，默认 600（向后兼容旧入口）

环境变量: MARSHAL_WATCH_ROOT / MARSHAL_WATCH_NOTIFY / MARSHAL_WATCH_LOG /
          MARSHAL_WATCH_COHORT_FILE / MARSHAL_WATCH_PROCESS_FILE（见脚本头部注释）
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
import fcntl, json, math, os, re, stat, sys
import hashlib
from datetime import datetime, timedelta, timezone

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
    "CI_PENDING": (80, "check-ci"),
}
# 终态 ACCEPTED/REJECTED/BLOCKED/ABORTED/NO_CHANGE 不在 ACTIONABLE 中，
# 因此默认不进入行动队列（只保留在文本模式的状态行里）。
MARSHAL_SUBCOMMANDS = ("run", "verify", "publish", "supervise")
ADAPTER_BINARIES = {"qwen", "codex", "qoder", "opencode", "pi"}
STATE_VALUES = {"CREATED", "PLANNED", "READY", "RUNNING", "RETRY_PENDING",
                "VERIFYING", "REVIEW_PENDING", "REWORK_REQUESTED", "PUBLISHING",
                "PUBLISHED", "CI_PENDING", "ACCEPTED", "REJECTED", "BLOCKED",
                "ABORTED", "NO_CHANGE"}
DEFAULT_WORKER_RESERVE_BYTES = 2 * 1024 * 1024 * 1024
DEFAULT_PROVIDER_FAILURE_HOLD_SECONDS = 5 * 60
MAX_RETRY_HINT_SECONDS = 24 * 60 * 60
MAX_STATE_BYTES = 1024 * 1024
MAX_TASK_SPEC_BYTES = 1024 * 1024
MAX_OWNER_BYTES = 64 * 1024
MAX_JOURNAL_BYTES = 16 * 1024 * 1024
MAX_REVIEW_PACKET_BYTES = 8 * 1024 * 1024
MAX_CONTROL_JOURNAL_BYTES = 16 * 1024 * 1024
TYPED_FAILURE_PAIRS = {
    "quota-exhausted": "blocked",
    "rate-limited": "retryable",
    "dns-failure": "retryable",
    "connection-failure": "retryable",
    "protocol-invalid": "do-not-retry",
    "result-missing": "do-not-retry",
    "provider-terminal": "do-not-retry",
}
PROVIDER_CAPACITY_FAILURES = {"quota-exhausted", "rate-limited", "dns-failure", "connection-failure"}

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
        adapter_name = (base in ADAPTER_BINARIES or base == "qwen-code" or
                        re.fullmatch(r"qodercli(?:-[0-9]+(?:\.[0-9]+){1,3})?", base) is not None or
                        re.fullmatch(r"codex-(?:aarch64|x86_64)-(?:apple-darwin|unknown-linux-gnu)", base) is not None)
        if adapter_name:
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

def _float_env(name):
    raw = os.environ.get(name, "")
    if raw == "unavailable":
        return None
    try:
        value = float(raw)
        return value if math.isfinite(value) and value >= 0 else None
    except (TypeError, ValueError):
        return None

DIR_FLAGS = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_DIRECTORY", 0)
FILE_FLAGS = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)

def _open_directory_path_nofollow(path):
    """Open every directory component without following symlinks and hold the chain."""
    if not isinstance(path, str) or not path:
        raise ValueError("directory path is required")
    raw_components = path.split("/")
    absolute_input = os.path.isabs(path)
    if any(component == "" and not (absolute_input and index == 0)
           for index, component in enumerate(raw_components)):
        raise ValueError("empty path component is forbidden")
    if any(component == ".." for component in raw_components):
        raise ValueError("parent traversal is forbidden")
    absolute_path = os.path.abspath(path)
    if not os.path.isabs(absolute_path):
        raise ValueError("absolute lexical path is required")
    lexical_components = absolute_path.split("/")
    if lexical_components[0] != "" or any(component in ("", "..") for component in lexical_components[1:]):
        raise ValueError("directory path contains a forbidden component")
    components = [component for component in lexical_components[1:] if component != "."]
    fd = os.open("/", DIR_FLAGS)
    try:
        for component in components:
            next_fd = os.open(component, DIR_FLAGS, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        held = os.fstat(fd)
        if not stat.S_ISDIR(held.st_mode):
            raise ValueError("path is not a directory")
        return fd
    except Exception:
        os.close(fd)
        raise

def _open_child_directory_bound(parent_fd, name):
    if not isinstance(name, str) or not name or name in (".", "..") or "/" in name:
        raise ValueError("invalid directory entry")
    before = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
    if not stat.S_ISDIR(before.st_mode):
        raise ValueError("entry is not a directory")
    if os.environ.get("MARSHAL_WATCH_TESTING") == "1" and os.environ.get("MARSHAL_WATCH_TEST_RUN_ENTRY_HOOK") == name:
        raise ValueError("test fixture simulated a run entry swap")
    fd = os.open(name, DIR_FLAGS, dir_fd=parent_fd)
    try:
        held = os.fstat(fd)
        after = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_mode)
        if identity(before) != identity(held) or identity(held) != identity(after):
            raise ValueError("directory entry identity changed")
        return fd
    except Exception:
        os.close(fd)
        raise

def _read_regular_bytes_at(parent_fd, name, limit):
    if not isinstance(name, str) or not name or name in (".", "..") or "/" in name:
        raise ValueError("invalid file entry")
    fd = os.open(name, FILE_FLAGS, dir_fd=parent_fd)
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > limit:
            raise ValueError("not a bounded single-link regular file")
        chunks, size = [], 0
        while size <= limit:
            chunk = os.read(fd, min(64 * 1024, limit + 1 - size))
            if not chunk:
                break
            chunks.append(chunk)
            size += len(chunk)
        raw = b"".join(chunks)
        after = os.fstat(fd)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mode, value.st_nlink)
        if len(raw) > limit or len(raw) != before.st_size or identity(before) != identity(after):
            raise ValueError("file identity changed")
        return raw
    finally:
        os.close(fd)

def _decode_json(raw):
    return json.loads(raw.decode("utf-8"), parse_constant=lambda value: (_ for _ in ()).throw(ValueError("non-finite JSON number")))

def _read_regular_json_at(parent_fd, name, limit=1024 * 1024):
    return _decode_json(_read_regular_bytes_at(parent_fd, name, limit))

def _read_regular_json(path, limit=1024 * 1024):
    """Read a small JSON file through a fully nofollow parent dirfd chain."""
    parent, name = os.path.split(path)
    parent_fd = _open_directory_path_nofollow(parent or ".")
    try:
        return _read_regular_json_at(parent_fd, name, limit)
    finally:
        os.close(parent_fd)

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
        # Free, inactive, purgeable and speculative pages are reclaimable by
        # macOS.  The previous probe omitted inactive pages and could report
        # zero slots while `memory_pressure -Q` still showed substantial
        # reclaimable capacity, needlessly serializing Mac-first work.  Keep
        # the per-Worker reserve conservative (2 GiB by default); this probe
        # only reports admission capacity and never starts a Worker.
        available = sum(pages.get(key, 0) for key in ("free", "inactive", "purgeable", "speculative")) * page_size
        if available > 0:
            return available, "darwin-vm_stat-reclaimable"
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

def _parse_swap_used_bytes(text):
    match = re.search(r"used\s*=\s*([0-9]+(?:\.[0-9]+)?)([KMGTP])(?:\s|$)", text)
    if not match:
        return None
    scale = {"K": 1024, "M": 1024 ** 2, "G": 1024 ** 3,
             "T": 1024 ** 4, "P": 1024 ** 5}[match.group(2)]
    try:
        value = float(match.group(1))
        if not math.isfinite(value):
            return None
        return int(value * scale)
    except (OverflowError, ValueError):
        return None

def swap_used_bytes():
    raw_override = os.environ.get("MARSHAL_WATCH_SWAP_USED_BYTES", "")
    if raw_override == "unavailable":
        return 0, "unavailable"
    override = _integer_env("MARSHAL_WATCH_SWAP_USED_BYTES")
    if override is not None:
        return override, "override"
    raw_fixture = os.environ.get("MARSHAL_WATCH_SWAP_OUTPUT")
    if raw_fixture is not None:
        parsed = _parse_swap_used_bytes(raw_fixture)
        return (parsed, "fixture-vm.swapusage") if parsed is not None else (0, "unavailable")
    if sys.platform == "darwin":
        text = _command_output(["sysctl", "-n", "vm.swapusage"])
        parsed = _parse_swap_used_bytes(text)
        if parsed is None:
            return 0, "unavailable"
        return parsed, "darwin-vm.swapusage"
    try:
        with open("/proc/meminfo", "r", encoding="utf-8") as handle:
            values = {}
            for line in handle:
                key, _, rest = line.partition(":")
                if key in ("SwapTotal", "SwapFree"):
                    values[key] = int(rest.strip().split()[0]) * 1024
            return max(0, values.get("SwapTotal", 0) - values.get("SwapFree", 0)), "proc-meminfo"
    except (OSError, ValueError):
        return 0, "unavailable"

def _parse_pressure_free_percent(text):
    match = re.search(r"System-wide memory free percentage:\s*([0-9]{1,3})%(?:\s|$)", text)
    if not match:
        return None
    value = int(match.group(1))
    return value if value <= 100 else None

def pressure_free_percent():
    raw_override = os.environ.get("MARSHAL_WATCH_PRESSURE_FREE_PERCENT", "")
    if raw_override == "unavailable":
        return None, "unavailable"
    override = _integer_env("MARSHAL_WATCH_PRESSURE_FREE_PERCENT")
    if override is not None and override <= 100:
        return override, "override"
    raw_fixture = os.environ.get("MARSHAL_WATCH_PRESSURE_OUTPUT")
    if raw_fixture is not None:
        parsed = _parse_pressure_free_percent(raw_fixture)
        return (parsed, "fixture-memory_pressure") if parsed is not None else (None, "unavailable")
    if sys.platform == "darwin":
        text = _command_output(["memory_pressure", "-Q"])
        parsed = _parse_pressure_free_percent(text)
        if parsed is not None:
            return parsed, "darwin-memory_pressure"
        return None, "unavailable"
    try:
        with open("/proc/meminfo", "r", encoding="utf-8") as handle:
            values = {}
            for line in handle:
                key, _, rest = line.partition(":")
                if key in ("MemAvailable", "MemTotal"):
                    values[key] = int(rest.strip().split()[0])
            total = values.get("MemTotal", 0)
            available = values.get("MemAvailable", 0)
            if total > 0:
                return min(100, max(0, available * 100 // total)), "proc-meminfo-ratio"
    except (OSError, ValueError):
        pass
    return None, "unavailable"

def cpu_snapshot(active_owned):
    raw_cpus = os.environ.get("MARSHAL_WATCH_LOGICAL_CPUS", "")
    if raw_cpus == "unavailable":
        logical = None
        logical_source = "unavailable"
    else:
        override = _integer_env("MARSHAL_WATCH_LOGICAL_CPUS")
        logical = override if override and override > 0 else os.cpu_count()
        logical_source = "override" if override and override > 0 else ("os.cpu_count" if logical else "unavailable")
    raw_load = os.environ.get("MARSHAL_WATCH_LOAD1M", "")
    if raw_load == "unavailable":
        load = None
        load_source = "unavailable"
    else:
        override_load = _float_env("MARSHAL_WATCH_LOAD1M")
        if raw_load != "" and override_load is not None:
            load, load_source = override_load, "override"
        else:
            try:
                load, load_source = float(os.getloadavg()[0]), "os.getloadavg"
            except (AttributeError, OSError, ValueError):
                load, load_source = None, "unavailable"
    if logical is None or load is None:
        return {"logicalCores": logical, "logicalCoresSource": logical_source,
                "load1m": load, "load1mSource": load_source,
                "cpuSlotsAvailable": 0, "cpuStatus": "unknown"}
    load_headroom = max(0, int(math.floor(logical - load)))
    ownership_headroom = max(0, logical - active_owned)
    slots = min(load_headroom, ownership_headroom)
    return {"logicalCores": logical, "logicalCoresSource": logical_source,
            "load1m": load, "load1mSource": load_source,
            "ownedWorkerHeadroom": ownership_headroom,
            "cpuSlotsAvailable": slots,
            "cpuStatus": "ok" if slots > 0 else "constrained"}

def _load_lease_fixture():
    path = os.environ.get("MARSHAL_WATCH_LEASE_FACTS_FILE", "")
    if not path:
        return None
    try:
        data = _read_regular_json(path, 64 * 1024)
        if not isinstance(data, dict) or any(not isinstance(key, str) or value not in {"held-alive", "not-held", "unknown"} for key, value in data.items()):
            return {"*": "unknown"}
        return data
    except (OSError, UnicodeError, ValueError, TypeError):
        return {"*": "unknown"}

LEASE_FIXTURE = _load_lease_fixture()

def lease_observation(run_fd, run_id):
    if LEASE_FIXTURE is not None:
        return LEASE_FIXTURE.get(run_id, LEASE_FIXTURE.get("*", "not-held")), "fixture"
    try:
        fd = os.open("lease.lock", FILE_FLAGS, dir_fd=run_fd)
    except FileNotFoundError:
        return "not-held", "marshal-lease"
    except OSError:
        return "unknown", "marshal-lease"
    try:
        lock_stat = os.fstat(fd)
        if not stat.S_ISREG(lock_stat.st_mode) or lock_stat.st_nlink != 1:
            return "unknown", "marshal-lease"
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            held = True
        except OSError:
            return "unknown", "marshal-lease"
        else:
            fcntl.flock(fd, fcntl.LOCK_UN)
            held = False
        if not held:
            return "not-held", "marshal-lease"
        try:
            owner = _read_regular_json_at(run_fd, "lease.lock.owner", MAX_OWNER_BYTES)
            pid = owner.get("pid") if isinstance(owner, dict) else None
            device = owner.get("device") if isinstance(owner, dict) else None
            inode = owner.get("inode") if isinstance(owner, dict) else None
            if (not isinstance(owner, dict) or isinstance(pid, bool) or not isinstance(pid, int) or not 1 < pid <= 2147483647 or
                    isinstance(device, bool) or not isinstance(device, int) or
                    isinstance(inode, bool) or not isinstance(inode, int)):
                return "unknown", "marshal-lease"
            if (device, inode) != (lock_stat.st_dev, lock_stat.st_ino):
                return "unknown", "marshal-lease"
            for field in ("token", "processStartedAt", "acquiredAt", "heartbeatAt"):
                if not isinstance(owner.get(field), str) or not owner[field]:
                    return "unknown", "marshal-lease"
            try:
                os.kill(pid, 0)
            except (OSError, OverflowError, TypeError, ValueError):
                return "unknown", "marshal-lease"
            return "held-alive", "marshal-lease"
        except (OSError, UnicodeError, ValueError, TypeError):
            return "unknown", "marshal-lease"
    finally:
        os.close(fd)

def _cohort_configuration():
    path = os.environ.get("MARSHAL_WATCH_COHORT_FILE", "")
    if not path:
        return None, {"source": "owned-active-only", "goalId": None}
    try:
        data = _read_regular_json(path, 256 * 1024)
        goal_id = data.get("goalId")
        run_ids = data.get("runIds")
        pattern = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$")
        if not isinstance(goal_id, str) or not pattern.fullmatch(goal_id) or not isinstance(run_ids, list) or len(run_ids) != len(set(run_ids)) or any(not isinstance(run_id, str) or not pattern.fullmatch(run_id) for run_id in run_ids):
            raise ValueError("invalid cohort")
        return set(run_ids), {"source": "explicit-goal-cohort", "goalId": goal_id, "runCount": len(run_ids)}
    except (OSError, UnicodeError, ValueError, TypeError):
        return set(), {"source": "invalid-explicit-cohort", "goalId": None, "runCount": 0}

def capacity_snapshot(active_owned, cpu, provider, queue_signal_status):
    available, source = memory_available_bytes()
    swap_used, swap_source = swap_used_bytes()
    free_percent, pressure_source = pressure_free_percent()
    reserve = _integer_env("MARSHAL_WATCH_WORKER_RESERVE_BYTES")
    if reserve is None or reserve == 0:
        reserve = DEFAULT_WORKER_RESERVE_BYTES
    memory_slots = available // reserve if reserve > 0 else 0
    pressure = "critical" if available < reserve // 2 else ("constrained" if available < reserve else "ok")
    # swapUsed 是历史/现状观测，不是当前 thrash 速率；macOS 在压力解除后
    # 也可能长期保留 swap。实际 admission 使用当前 memory_pressure 或
    # Linux MemAvailable/MemTotal 比例。信号缺失 fail closed，避免把未知
    # 错当作零压力；压力恢复后即使 swap 仍高也会重新开放槽位。
    if free_percent is None:
        memory_slots = 0
        pressure = "unknown"
    elif free_percent < 15:
        memory_slots = 0
        pressure = "critical"
    elif free_percent < 25:
        memory_slots = 0
        pressure = "constrained"
    cpu_slots = cpu["cpuSlotsAvailable"]
    provider_slots = provider["providerSlotsAvailable"]
    critical_unknown = pressure == "unknown" or cpu["cpuStatus"] == "unknown" or provider["providerStatus"] == "unknown" or queue_signal_status == "unknown"
    slots = 0 if critical_unknown else min(memory_slots, cpu_slots, provider_slots)
    result = {
        "memoryAvailableBytes": available,
        "memorySource": source,
        "swapUsedBytes": swap_used,
        "swapSource": swap_source,
        "pressureFreePercent": free_percent,
        "pressureSource": pressure_source,
        "workerReserveBytes": reserve,
        "activeOwnedWorkers": active_owned,
        "queueSignalStatus": queue_signal_status,
        "memorySlotsAvailable": memory_slots,
        **cpu,
        **provider,
        "slotsAvailable": slots,
        "recommendedMaxWorkers": active_owned + slots,
        "concurrencyAction": "increase-concurrency" if slots > 0 else "hold-concurrency",
        "pressure": pressure,
    }
    return result

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
    return parsed.astimezone(timezone.utc)

def strict_timestamp(stamp):
    if not isinstance(stamp, str) or not stamp or stamp != stamp.strip():
        return None
    cleaned = stamp.replace("Z", "+00:00")
    cleaned = re.sub(r"\.(\d{6})\d+", r".\1", cleaned)
    try:
        parsed = datetime.fromisoformat(cleaned)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return None
    return parsed.astimezone(timezone.utc)

def age_seconds(run_fd, data):
    parsed = parse_timestamp(data.get("updatedAt") if isinstance(data, dict) else None)
    if parsed is not None:
        seconds = int((now_utc - parsed).total_seconds())
        return seconds if seconds > 0 else 0
    try:
        state_stat = os.stat("state.json", dir_fd=run_fd, follow_symlinks=False)
        mtime = state_stat.st_mtime
    except OSError:
        return 0
    seconds = int(now_utc.timestamp() - mtime)
    return seconds if seconds > 0 else 0

def file_digest_at(parent_fd, name, limit):
    """Bounded digest; unsafe, oversized or changing evidence has one stable marker."""
    try:
        return hashlib.sha256(_read_regular_bytes_at(parent_fd, name, limit)).hexdigest()
    except FileNotFoundError:
        return "missing"
    except (OSError, ValueError):
        return "unknown"

def evidence_digests(run_fd):
    review = file_digest_at(run_fd, "review-packet.json", MAX_REVIEW_PACKET_BYTES)
    try:
        control_fd = _open_child_directory_bound(run_fd, "control")
    except FileNotFoundError:
        control = "missing"
    except (OSError, ValueError):
        control = "unknown"
    else:
        try:
            control = file_digest_at(control_fd, "records.jsonl", MAX_CONTROL_JOURNAL_BYTES)
        finally:
            os.close(control_fd)
    status = "unknown" if "unknown" in (review, control) else "ok"
    return review, control, status

def unknown_journal(marker=b"invalid"):
    return {"status": "unknown", "sequence": 0,
            "phaseDigest": "sha256:" + hashlib.sha256(marker).hexdigest(),
            "adapterId": None, "adapterStatus": "unknown",
            "typedFailure": None, "rootFailure": None, "latestFailure": None,
            "failureShape": "unknown", "lastSignal": None}

def _public_failure(failure):
    if not isinstance(failure, dict):
        return None
    if failure.get("valid") is not True:
        sequence = failure.get("sequence")
        return {"status": "invalid", "sequence": sequence if isinstance(sequence, int) else 0}
    keys = ("adapterId", "kind", "disposition", "failureSignature",
            "retryAfterNanoseconds", "notBefore", "attemptId", "sequence")
    return {key: failure[key] for key in keys if key in failure}

def journal_observation(run_fd, run_id):
    try:
        raw = _read_regular_bytes_at(run_fd, "events.jsonl", MAX_JOURNAL_BYTES)
    except FileNotFoundError:
        return {"status": "missing", "sequence": 0,
                "phaseDigest": "sha256:" + hashlib.sha256(b"missing").hexdigest(),
                "adapterId": None, "typedFailure": None, "rootFailure": None,
                "latestFailure": None, "failureShape": "none", "lastSignal": None}
    except (OSError, ValueError):
        return unknown_journal()
    try:
        events = []
        previous = 0
        for raw_line in raw.decode("utf-8").splitlines():
            if not raw_line.strip():
                continue
            event = json.loads(raw_line, parse_constant=lambda value: (_ for _ in ()).throw(ValueError("non-finite JSON number")))
            if not isinstance(event, dict):
                raise ValueError("journal event is not an object")
            sequence = event.get("sequence")
            if isinstance(sequence, bool) or not isinstance(sequence, int) or sequence <= previous:
                raise ValueError("non-monotonic journal")
            if not isinstance(event.get("type"), str) or not event["type"]:
                raise ValueError("journal event type is invalid")
            if strict_timestamp(event.get("timestamp")) is None:
                raise ValueError("journal event timestamp is invalid")
            if not isinstance(event.get("payload"), dict):
                raise ValueError("journal event payload is invalid")
            if "runId" in event and event["runId"] != run_id:
                raise ValueError("journal run identity mismatch")
            if "attemptId" in event and not isinstance(event["attemptId"], str):
                raise ValueError("journal attempt identity is invalid")
            for state_field in ("stateFrom", "stateTo"):
                if state_field in event and event[state_field] not in STATE_VALUES:
                    raise ValueError("journal state is invalid")
            previous = sequence
            events.append(event)
    except (UnicodeError, ValueError, TypeError):
        return unknown_journal(raw)
    if not events:
        return {"status": "ok", "sequence": 0,
                "phaseDigest": "sha256:" + hashlib.sha256(b"empty").hexdigest(),
                "adapterId": None, "typedFailure": None, "rootFailure": None,
                "latestFailure": None, "failureShape": "none", "lastSignal": None}
    adapter_id = None
    typed_failure = None
    root_failure = None
    latest_failure = None
    failure_shape = "none"
    last_signal = None
    journal_valid = True
    for event in events:
        payload = event["payload"]
        adapter_present = "adapterId" in payload
        candidate_adapter = payload.get("adapterId")
        candidate_adapter_valid = isinstance(candidate_adapter, str) and candidate_adapter in ADAPTER_BINARIES
        if adapter_present and not candidate_adapter_valid:
            journal_valid = False
        if candidate_adapter_valid:
            adapter_id = candidate_adapter
        event_type = event.get("type")
        if event_type == "worker.started":
            # RETRY_PENDING -> RUNNING 属于同一 operational-retry lineage；
            # READY/REWORK_REQUESTED 等新 origin 必须重置 root failure。
            if event.get("stateFrom") != "RETRY_PENDING":
                root_failure = None
                latest_failure = None
                failure_shape = "none"
            typed_failure = None
            last_signal = {"type": event_type, "timestamp": event.get("timestamp"),
                           "moment": strict_timestamp(event.get("timestamp")), "failure": None,
                           "sequence": event["sequence"]}
        elif event_type == "worker.failed":
            kind = payload.get("failureKind")
            disposition = payload.get("retryDisposition")
            typed_fields_present = any(key in payload for key in ("adapterId", "failureKind", "retryDisposition", "failureSignature"))
            if typed_fields_present:
                event_time = strict_timestamp(event.get("timestamp"))
                signature = payload.get("failureSignature")
                attempt_id = event.get("attemptId")
                valid = (candidate_adapter_valid and isinstance(kind, str) and
                         TYPED_FAILURE_PAIRS.get(kind) == disposition and event_time is not None and
                         isinstance(signature, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", signature) is not None and
                         isinstance(attempt_id, str) and
                         re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,255}", attempt_id) is not None)
                failure = {"adapterId": candidate_adapter if candidate_adapter_valid else "invalid", "kind": kind if isinstance(kind, str) else "invalid",
                           "disposition": disposition if isinstance(disposition, str) else "invalid",
                           "failureSignature": signature if isinstance(signature, str) else "invalid",
                           "attemptId": attempt_id if isinstance(attempt_id, str) else "invalid",
                           "sequence": event["sequence"], "valid": valid,
                           "stateFrom": event.get("stateFrom"), "stateTo": event.get("stateTo")}
                retry_after = payload.get("retryAfterNanoseconds")
                not_before = payload.get("notBefore")
                if retry_after is not None:
                    valid = valid and not isinstance(retry_after, bool) and isinstance(retry_after, int) and 0 < retry_after <= MAX_RETRY_HINT_SECONDS * 1000000000
                    if not isinstance(retry_after, bool) and isinstance(retry_after, int):
                        failure["retryAfterNanoseconds"] = retry_after
                if not_before is not None:
                    not_before_time = strict_timestamp(not_before)
                    delta = (not_before_time - event_time).total_seconds() if not_before_time is not None and event_time is not None else None
                    valid = valid and delta is not None and 0 < delta <= MAX_RETRY_HINT_SECONDS
                    if isinstance(not_before, str):
                        failure["notBefore"] = not_before
                if retry_after is not None and not_before is not None:
                    valid = False
                failure["valid"] = valid
                journal_valid = journal_valid and valid
                typed_failure = failure
                latest_failure = failure
                if root_failure is None:
                    root_failure = failure
                failure_shape = "typed" if valid else "invalid"
                last_signal = {"type": event_type, "timestamp": event.get("timestamp"), "moment": event_time, "failure": failure,
                               "sequence": event["sequence"]}
            else:
                # Legacy free-text failure is retained as evidence but never
                # upgraded into a retry recommendation by the watchdog.
                typed_failure = None
                latest_failure = None
                failure_shape = "legacy"
                last_signal = {"type": event_type, "timestamp": event.get("timestamp"),
                               "moment": strict_timestamp(event.get("timestamp")), "failure": None,
                               "sequence": event["sequence"]}
        elif event_type == "worker.completed":
            if not candidate_adapter_valid:
                journal_valid = False
            else:
                typed_failure = None
                last_signal = {"type": event_type, "timestamp": event.get("timestamp"),
                               "moment": strict_timestamp(event.get("timestamp")), "failure": None,
                               "sequence": event["sequence"]}
    last = events[-1]
    payload_digest = hashlib.sha256(json.dumps(last.get("payload", {}), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")).hexdigest()
    phase = {"sequence": last["sequence"], "type": last.get("type"), "stateFrom": last.get("stateFrom"),
             "stateTo": last.get("stateTo"), "attemptId": last.get("attemptId"), "payloadDigest": "sha256:" + payload_digest}
    phase_digest = "sha256:" + hashlib.sha256(json.dumps(phase, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")).hexdigest()
    return {"status": "ok" if journal_valid else "unknown", "sequence": last["sequence"], "phaseDigest": phase_digest,
            "adapterId": adapter_id, "typedFailure": typed_failure,
            "rootFailure": root_failure, "latestFailure": latest_failure,
            "failureShape": failure_shape, "lastSignal": last_signal}

def task_adapter_observation(run_fd, journal):
    if journal.get("adapterId") is not None:
        return journal["adapterId"], "journal"
    try:
        task = _read_regular_json_at(run_fd, "task-spec.json", MAX_TASK_SPEC_BYTES)
        worker = task.get("worker") if isinstance(task, dict) else None
        adapter_id = worker.get("preferredAdapter") if isinstance(worker, dict) else None
        if adapter_id not in ADAPTER_BINARIES:
            raise ValueError("unknown preferred adapter")
        return adapter_id, "task-spec"
    except (OSError, UnicodeError, ValueError, TypeError):
        return None, "unknown"

def observe_run(run_fd, run_id):
    """Keep malformed or racing per-Run evidence from aborting the whole scan."""
    try:
        journal = journal_observation(run_fd, run_id)
        journal["adapterId"], journal["adapterStatus"] = task_adapter_observation(run_fd, journal)
        lease_fact, lease_source = lease_observation(run_fd, run_id)
        review_digest, control_digest, evidence_status = evidence_digests(run_fd)
        return journal, lease_fact, lease_source, review_digest, control_digest, evidence_status
    except (OSError, UnicodeError, ValueError, TypeError, OverflowError, KeyError):
        return (unknown_journal(b"run-observation"), "unknown", "marshal-lease",
                "unknown", "unknown", "unknown")

def decision_key(run_id, data, state, action, ownership, journal, review_digest, control_digest):
    """为同一动作上下文生成稳定键，避免重复消费且不抑制合法恢复。"""
    fields = [
        run_id,
        state,
        action,
        ownership,
        str(data.get("specDigest", "")),
        str(data.get("policyDigest", "")),
        str(data.get("capabilityDigest", "")),
        str(data.get("baseSha", "")),
        str(data.get("currentAttemptId", "")),
        str(data.get("sequence", 0)),
        str(data.get("reviewRound", 0)),
        str(data.get("reworkRoundsUsed", 0)),
        str(journal.get("sequence", 0)),
        str(journal.get("phaseDigest", "")),
        json.dumps(journal.get("typedFailure"), sort_keys=True, separators=(",", ":")),
        json.dumps(journal.get("rootFailure"), sort_keys=True, separators=(",", ":")),
        json.dumps(journal.get("latestFailure"), sort_keys=True, separators=(",", ":")),
        str(journal.get("failureShape", "")),
        review_digest,
        control_digest,
    ]
    return "sha256:" + hashlib.sha256("\x1f".join(fields).encode("utf-8")).hexdigest()

def failure_signature_matches_state(data, failure):
    """Recompute the Core v1 signature from frozen state; never trust shape alone."""
    if not isinstance(data, dict) or not isinstance(failure, dict):
        return False
    digest_pattern = re.compile(r"sha256:[0-9a-f]{64}")
    if (not isinstance(data.get("baseSha"), str) or
            re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", data["baseSha"]) is None or
            any(not isinstance(data.get(field), str) or digest_pattern.fullmatch(data[field]) is None
                for field in ("specDigest", "policyDigest", "capabilityDigest"))):
        return False
    evidence = {
        "adapterId": failure.get("adapterId"),
        "failureKind": failure.get("kind"),
        "retryDisposition": failure.get("disposition"),
    }
    evidence_bytes = json.dumps(evidence, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    evidence_digest = "sha256:" + hashlib.sha256(evidence_bytes).hexdigest()
    signature_data = {
        "version": 1,
        "sourceHead": data["baseSha"],
        "specDigest": data["specDigest"],
        "policyDigest": data["policyDigest"],
        "capabilityDigest": data["capabilityDigest"],
        "adapterId": failure.get("adapterId"),
        "failureKind": failure.get("kind"),
        "failureEvidenceDigest": evidence_digest,
    }
    signature_bytes = json.dumps(signature_data, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    expected = "sha256:" + hashlib.sha256(signature_bytes).hexdigest()
    return failure.get("failureSignature") == expected

def validate_state_data(data, run_id):
    if not isinstance(data, dict) or data.get("runId") != run_id:
        raise ValueError("state run identity is invalid")
    state = data.get("state")
    if not isinstance(state, str) or state not in STATE_VALUES:
        raise ValueError("state enumeration is invalid")
    sequence = data.get("sequence")
    if isinstance(sequence, bool) or not isinstance(sequence, int) or sequence < 0:
        raise ValueError("state sequence is invalid")
    if strict_timestamp(data.get("createdAt")) is None or strict_timestamp(data.get("updatedAt")) is None:
        raise ValueError("state timestamps are invalid")
    for field in ("taskId", "specDigest", "policyDigest", "capabilityDigest", "baseSha", "currentAttemptId"):
        if field in data and not isinstance(data[field], str):
            raise ValueError("state string field is invalid")
    for field in ("reviewRound", "reworkRoundsUsed"):
        if field in data and (isinstance(data[field], bool) or not isinstance(data[field], int) or data[field] < 0):
            raise ValueError("state counter is invalid")
    return state

def provider_snapshot(items, observations, provisional_slots):
    required_adapters = {observations[item["runId"]]["adapterId"] for item in items
                         if item["action"] in {"run-now", "run-rework-now", "retry-or-abort"}
                         and observations[item["runId"]]["adapterId"] is not None}
    malformed_required = any(observations[item["runId"]]["status"] == "unknown" or observations[item["runId"]]["adapterStatus"] == "unknown" for item in items
                             if item["action"] in {"run-now", "run-rework-now", "retry-or-abort"})
    if malformed_required:
        return {"providerSlotsAvailable": 0, "providerStatus": "unknown", "providerSignals": []}
    signals = []
    for adapter_id in sorted(required_adapters):
        latest = None
        for observation in observations.values():
            signal = observation.get("lastSignal")
            if observation.get("adapterId") != adapter_id or signal is None:
                continue
            stamp = signal.get("moment")
            if not isinstance(stamp, datetime):
                return {"providerSlotsAvailable": 0, "providerStatus": "unknown", "providerSignals": signals + [{"adapterId": adapter_id, "status": "unknown"}]}
            key = (stamp, signal.get("sequence", 0))
            if latest is None or key > latest[0]:
                latest = (key, signal)
        if latest is None or latest[1]["type"] in {"worker.started", "worker.completed"}:
            signals.append({"adapterId": adapter_id, "status": "available"})
            continue
        failure = latest[1].get("failure")
        if not isinstance(failure, dict) or not failure.get("valid"):
            return {"providerSlotsAvailable": 0, "providerStatus": "unknown", "providerSignals": signals + [{"adapterId": adapter_id, "status": "unknown"}]}
        kind = failure["kind"]
        if kind not in PROVIDER_CAPACITY_FAILURES:
            signals.append({"adapterId": adapter_id, "status": "available"})
            continue
        event_time = latest[1].get("moment")
        until = None
        if isinstance(failure.get("notBefore"), str):
            until = strict_timestamp(failure["notBefore"])
        elif isinstance(failure.get("retryAfterNanoseconds"), int) and isinstance(event_time, datetime):
            until = event_time + timedelta(microseconds=failure["retryAfterNanoseconds"] / 1000)
        elif isinstance(event_time, datetime):
            hold = _integer_env("MARSHAL_WATCH_PROVIDER_FAILURE_HOLD_SECONDS")
            until = event_time + timedelta(seconds=hold if hold is not None else DEFAULT_PROVIDER_FAILURE_HOLD_SECONDS)
        if kind == "quota-exhausted" or until is None or until > now_utc:
            entry = {"adapterId": adapter_id, "status": "blocked" if kind == "quota-exhausted" else "backpressure", "failureKind": kind}
            if until is not None:
                entry["notBefore"] = until.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            signals.append(entry)
            return {"providerSlotsAvailable": 0, "providerStatus": entry["status"], "providerSignals": signals}
        signals.append({"adapterId": adapter_id, "status": "available", "failureKind": kind})
    return {"providerSlotsAvailable": provisional_slots, "providerStatus": "ok", "providerSignals": signals}

procs = process_lines()
explicit_cohort, cohort = _cohort_configuration()
items, historical_items, unscoped_items, text_tokens = [], [], [], []
owned_runs = set()
observations = {}
runs_fd = _open_directory_path_nofollow(runs_dir)
try:
    run_names = sorted(os.listdir(runs_fd))
except Exception:
    os.close(runs_fd)
    raise
for run_id in run_names:
    run_fd = None
    run_path_status = "ok"
    try:
        run_fd = _open_child_directory_bound(runs_fd, run_id)
    except (OSError, ValueError):
        run_path_status = "unknown"
    data = {}
    state = "UNKNOWN"
    state_status = "unknown"
    if run_fd is not None:
        try:
            data = _read_regular_json_at(run_fd, "state.json", MAX_STATE_BYTES)
            state = validate_state_data(data, run_id)
            state_status = "ok"
        except (OSError, UnicodeError, ValueError, KeyError, TypeError):
            data = {}
    if run_fd is None:
        journal = unknown_journal(b"run-path")
        lease_fact, lease_source = "unknown", "marshal-lease"
        review_digest, control_digest, evidence_status = "unknown", "unknown", "unknown"
    else:
        (journal, lease_fact, lease_source, review_digest,
         control_digest, evidence_status) = observe_run(run_fd, run_id)
    observations[run_id] = journal
    argv_matched = any(owner_present(line, run_id) for line in procs)
    owned = lease_fact == "held-alive"
    if owned and state_status == "ok" and state in {"RUNNING", "VERIFYING", "PUBLISHING"}:
        owned_runs.add(run_id)
    item = None
    if run_path_status == "unknown":
        item = {"runId": run_id, "state": "UNKNOWN", "priority": 2,
                "action": "hold-run-path-unknown", "processOwnership": "unknown"}
    elif state_status == "unknown":
        journal["status"] = "unknown"
        item = {"runId": run_id, "state": "UNKNOWN", "priority": 3,
                "action": "hold-run-invalid", "processOwnership": "unknown"}
    elif state == "RUNNING":
        if lease_fact == "held-alive":
            item = {"runId": run_id, "state": state, "priority": 90,
                    "action": "monitor", "processOwnership": "owned-active"}
        elif lease_fact == "not-held":
            item = {"runId": run_id, "state": state, "priority": 30,
                    "action": "doctor-dead", "processOwnership": "not-found"}
        else:
            item = {"runId": run_id, "state": state, "priority": 4,
                    "action": "hold-ownership-unknown", "processOwnership": "unknown"}
    elif state in ACTIONABLE:
        priority, action = ACTIONABLE[state]
        item = {"runId": run_id, "state": state, "priority": priority,
                "action": action, "processOwnership": "not-applicable"}
        if state == "REVIEW_PENDING" and review_digest in {"missing", "unknown"}:
            # A missing packet means `task review` cannot yet bind a
            # ReviewDecision. Surface this as an intervention instead of
            # repeatedly asking the lead to rerun the same doomed review.
            item["priority"] = 5
            item["action"] = "review-intervention"
        elif state == "RETRY_PENDING":
            failure = journal.get("latestFailure")
            current_attempt = data.get("currentAttemptId")
            retry_lineage_valid = (
                journal.get("status") == "ok" and journal.get("failureShape") == "typed" and
                isinstance(failure, dict) and failure.get("valid") is True and
                failure.get("disposition") == "retryable" and
                failure_signature_matches_state(data, failure) and
                failure.get("attemptId") == current_attempt and
                failure.get("stateFrom") == "RUNNING" and failure.get("stateTo") == "RETRY_PENDING"
            )
            if not retry_lineage_valid:
                item["priority"] = 6
                item["action"] = "retry-intervention"
                item["interventionReason"] = "typed-retry-lineage-required"
    # 终态与其他未映射状态一律不进入行动队列。
    if item is not None:
        age = age_seconds(run_fd, data) if run_fd is not None and state_status == "ok" else 0
        item["ageSeconds"] = age
        item["ownershipSource"] = lease_source
        item["argvMatched"] = argv_matched
        item["journalSequence"] = journal["sequence"]
        item["phaseProgressDigest"] = journal["phaseDigest"]
        item["journalStatus"] = journal["status"]
        item["evidenceStatus"] = evidence_status
        if journal.get("typedFailure") is not None:
            item["typedFailure"] = _public_failure(journal["typedFailure"])
        if journal.get("rootFailure") is not None:
            item["rootFailure"] = _public_failure(journal["rootFailure"])
        if journal.get("latestFailure") is not None:
            item["latestFailure"] = _public_failure(journal["latestFailure"])
        item["failureShape"] = journal.get("failureShape", "unknown")
        item["dedupeKey"] = decision_key(
            run_id, data, state, item["action"], item["processOwnership"], journal,
            review_digest, control_digest
        )
        if explicit_cohort is not None:
            current = run_id in explicit_cohort
            item["queueBucket"] = "current" if current else "historical"
            (items if current else historical_items).append(item)
        else:
            current = owned
            item["queueBucket"] = "current" if current else "unscoped"
            (items if current else unscoped_items).append(item)
    if mode == "text":
        if state == "RUNNING":
            text_tokens.append("%s=%s" % (run_id, "RUNNING(active)" if owned else "DEAD?"))
        else:
            text_tokens.append("%s=%s" % (run_id, state))
    if run_fd is not None:
        os.close(run_fd)

os.close(runs_fd)

items.sort(key=lambda entry: (entry["priority"], entry["runId"]))
historical_items.sort(key=lambda entry: (entry["priority"], entry["runId"]))
unscoped_items.sort(key=lambda entry: (entry["priority"], entry["runId"]))
cpu = cpu_snapshot(len(owned_runs))
provider = provider_snapshot(items, observations, cpu["cpuSlotsAvailable"])
if cohort["source"] == "invalid-explicit-cohort":
    provider = {"providerSlotsAvailable": 0, "providerStatus": "unknown", "providerSignals": []}
queue_signal_status = "unknown" if any(item["journalStatus"] == "unknown" or item["processOwnership"] == "unknown" or item["evidenceStatus"] == "unknown" for item in items) else "ok"
capacity = capacity_snapshot(len(owned_runs), cpu, provider, queue_signal_status)
if mode == "text":
    print("[%s] %s capacity=%s slots=%s current=%s unscoped=%s historical=%s" % (datetime.now().strftime("%m-%d %H:%M:%S"), " ".join(text_tokens), capacity["pressure"], capacity["slotsAvailable"], len(items), len(unscoped_items), len(historical_items)))
else:
    print(json.dumps({"queueVersion": "marshal-watch/v2", "advisoryOnly": True,
                      "generatedAt": now_utc.strftime("%Y-%m-%dT%H:%M:%SZ"),
                      "cohort": cohort, "capacity": capacity, "topAction": items[0] if items else None,
                      "items": items, "unscopedItems": unscoped_items,
                      "historicalItems": historical_items},
                     ensure_ascii=False))
if summary_path:
    try:
        with open(summary_path, "w", encoding="utf-8") as handle:
            if items:
                top = items[0]
                handle.write("当前行动队列 %d 项，未归属 %d 项，历史 %d 项，最高优先级 %s=%s" % (len(items), len(unscoped_items), len(historical_items), top["runId"], top["action"]))
            else:
                handle.write("当前行动队列无待办，未归属 %d 项，历史 %d 项" % (len(unscoped_items), len(historical_items)))
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
  exit $?
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
