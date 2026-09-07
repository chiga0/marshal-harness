#!/bin/bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
PYTHON_BIN=/usr/bin/python3
MARSHAL_BIN="$ROOT/bin/marshal"
EXPECTED_HEAD=""
PI_MODEL=""
PI_NODE=""
PI_BIN=""
PI_BUNDLE=""
RUN_ID=""
EVIDENCE_ROOT=""
SCENARIO="t1-marker"
AWAIT_REVIEW=0
STOP_CRASH=0
VERIFY_PEER=0

die() {
  printf '[fixed-server-t1] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
usage: scripts/fixed-server-t1-canary.sh \
  --expected-head HEAD --pi-model PROVIDER/MODEL --pi-node PATH --pi-bin PATH \
  --pi-bundle PATH --run-id RUN_ID --evidence-root ABSOLUTE_PATH \
  [--scenario t1-marker|order-quote|order-quote-cancel|order-quote-timeout|order-quote-run-timeout|order-quote-team] [--await-review] [--stop-crash|--verify-peer]
EOF
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --expected-head) [ "$#" -ge 2 ] || usage; EXPECTED_HEAD="$2"; shift 2 ;;
    --pi-model) [ "$#" -ge 2 ] || usage; PI_MODEL="$2"; shift 2 ;;
    --pi-node) [ "$#" -ge 2 ] || usage; PI_NODE="$2"; shift 2 ;;
    --pi-bin) [ "$#" -ge 2 ] || usage; PI_BIN="$2"; shift 2 ;;
    --pi-bundle) [ "$#" -ge 2 ] || usage; PI_BUNDLE="$2"; shift 2 ;;
    --run-id) [ "$#" -ge 2 ] || usage; RUN_ID="$2"; shift 2 ;;
    --evidence-root) [ "$#" -ge 2 ] || usage; EVIDENCE_ROOT="$2"; shift 2 ;;
    --scenario) [ "$#" -ge 2 ] || usage; SCENARIO="$2"; shift 2 ;;
    --await-review) AWAIT_REVIEW=1200; shift ;;
    --stop-crash) STOP_CRASH=1; shift ;;
    --verify-peer) VERIFY_PEER=1; shift ;;
    *) usage ;;
  esac
done

[[ "$EXPECTED_HEAD" =~ ^[0-9a-f]{40}$ ]] || die 'expected-head 必须是 40 位小写 commit'
[[ "$PI_MODEL" =~ ^[A-Za-z0-9._:-]+/[A-Za-z0-9._:-]+$ ]] || die 'pi-model 必须是 provider/model'
[[ "$RUN_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{2,120}$ ]] || die 'run-id 形态非法'
case "$SCENARIO" in t1-marker|order-quote|order-quote-cancel|order-quote-timeout|order-quote-run-timeout|order-quote-team) ;; *) die 'scenario 非法' ;; esac
if [ "$SCENARIO" = order-quote-team ]; then
  [ "${#RUN_ID}" -le 112 ] || die 'team run-id 过长'
fi
[ "$AWAIT_REVIEW" -eq 0 ] || [ "$SCENARIO" = order-quote ] || die 'await-review 只适用于 order-quote'
if [ "$STOP_CRASH" -eq 1 ]; then
  case "$SCENARIO" in order-quote-timeout|order-quote-run-timeout) ;; *) die 'stop-crash 只适用于业务 timeout' ;; esac
fi
if [ "$VERIFY_PEER" -eq 1 ]; then
  [ "$SCENARIO" = order-quote-timeout ] && [ "$STOP_CRASH" -eq 0 ] && [ "$AWAIT_REVIEW" -eq 0 ] || die 'verify-peer 只允许无 crash/review 的 Attempt-timeout 场景'
  [ "${#RUN_ID}" -le 114 ] || die 'verify-peer run-id 过长'
fi
[ -x "$PI_NODE" ] && [ ! -L "$PI_NODE" ] || die 'pi-node 必须是固定普通 executable'
[ -x "$PI_BIN" ] || die 'pi-bin 必须是可执行入口'
[ -f "$PI_BUNDLE" ] && [ ! -L "$PI_BUNDLE" ] || die 'pi-bundle 必须是固定普通文件'
[ -x "$MARSHAL_BIN" ] && [ ! -L "$MARSHAL_BIN" ] || die '缺少 fixed bin/marshal'
[ "$(pwd -P)" = "$ROOT" ] || die '必须从 repository root 运行'
[ "$(git rev-parse HEAD)" = "$EXPECTED_HEAD" ] || die 'repository HEAD 与 expected-head 不同'
[ -z "$(git status --porcelain --untracked-files=no)" ] || die 'tracked worktree 必须 clean'

case "$EVIDENCE_ROOT" in
  "$ROOT/.marshal/fixed-server-t1-canary/$RUN_ID") ;;
  *) die 'evidence-root 必须是当前 Run 的固定 .marshal 路径' ;;
esac
[ ! -e "$EVIDENCE_ROOT" ] && [ ! -L "$EVIDENCE_ROOT" ] || die '拒绝覆盖既有 canary evidence'
umask 077
mkdir -p "$EVIDENCE_ROOT"
[ "$(cd "$EVIDENCE_ROOT" && pwd -P)" = "$EVIDENCE_ROOT" ] || die 'evidence-root 不是 canonical path'

export PATH="$(dirname "$PI_NODE"):/usr/bin:/bin:/usr/sbin:/sbin"
export MARSHAL_PI_PATH="$PI_BIN"
export MARSHAL_PI_NODE_PATH="$PI_NODE"
export MARSHAL_PI_RUNTIME="$PI_NODE"
export MARSHAL_PI_ENTRYPOINT="$PI_BUNDLE"

server1_pid=""
server2_pid=""
cleanup() {
  local pid
  for pid in "$server2_pid" "$server1_pid"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

append_audit() {
  local server="$1" operation="$2" response="$3"
  "$PYTHON_BIN" -I -B - "$EVIDENCE_ROOT/command-audit.jsonl" "$server" "$operation" "$response" <<'PY'
import json, sys
path, server, operation, response = sys.argv[1:]
value = {"afterApproval": True, "operation": operation, "response": response,
         "server": server, "surface": "control-plane"}
with open(path, "ab") as handle:
    handle.write(json.dumps(value, ensure_ascii=False, sort_keys=True,
                            separators=(",", ":")).encode() + b"\n")
PY
}

append_start_audit() {
  local server="$1" response="$2"
  "$PYTHON_BIN" -I -B - "$EVIDENCE_ROOT/command-audit.jsonl" "$server" "$response" \
    "$EVIDENCE_ROOT/start-request.json" <<'PY'
import json, sys
path, server, response, request_path = sys.argv[1:]
with open(request_path, encoding="utf-8") as handle:
    request = json.load(handle)
value = {"afterApproval": True, "operation": "start", "request": request,
         "response": response, "server": server, "surface": "control-plane"}
with open(path, "ab") as handle:
    handle.write(json.dumps(value, ensure_ascii=False, sort_keys=True,
                            separators=(",", ":")).encode() + b"\n")
PY
}

wait_ready() {
  local pid="$1" path="$2" attempt
  for attempt in $(seq 1 600); do
    kill -0 "$pid" 2>/dev/null || die "server 在 ready 前退出：$pid"
    if [ -s "$path" ] && "$PYTHON_BIN" -I -B - "$path" <<'PY' >/dev/null 2>&1
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
assert value == {"availability": "ready", "protocolRevision": "darwin-fixed-control-endpoint/v1"}
PY
    then
      return 0
    fi
    /bin/sleep 0.1
  done
  die "server ready 超时：$pid"
}

assert_server_pid() {
  local pid="$1" observed
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null || die "server PID 不存活：$pid"
  observed="$(/bin/ps -p "$pid" -o command=)"
  case "$observed" in
    "$MARSHAL_BIN control-plane serve"*) ;;
    *) die "拒绝终止非本 canary fixed server PID：$pid" ;;
  esac
}

write_process_evidence() {
  local path="$1" pid="$2" stop="$3" wait_status="$4"
  "$PYTHON_BIN" -I -B - "$path" "$pid" "$stop" "$wait_status" "$MARSHAL_BIN" <<'PY'
import json, sys
path, pid, stop, wait_status, binary = sys.argv[1:]
value = {"binary": binary, "pid": int(pid), "stop": stop, "waitStatus": int(wait_status)}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
}

drop_start_response_at_caller() {
  local stderr_path="$1" loss_path="$2" sequence="$3" head="$4" request_key="$5" deadline="$6"
  "$PYTHON_BIN" -I -B - "$MARSHAL_BIN" "$RUN_ID" "$sequence" "$head" "$request_key" "$deadline" "$stderr_path" "$loss_path" <<'PY'
import json, os, signal, subprocess, sys
binary, run_id, sequence, head, request_key, deadline, stderr_path, loss_path = sys.argv[1:]
read_fd, write_fd = os.pipe()
os.close(read_fd)
with open(stderr_path, "wb") as stderr:
    process = subprocess.Popen([
        binary, "control-plane", "start", "--run", run_id,
        "--expected-sequence", sequence, "--expected-authority-head", head,
        "--request-key", request_key, "--deadline", deadline,
    ], stdin=subprocess.DEVNULL, stdout=write_fd, stderr=stderr, close_fds=True)
    os.close(write_fd)
    return_code = process.wait()
if return_code == 0:
    raise SystemExit("closed caller pipe unexpectedly accepted a response")
with open(stderr_path, "rb") as handle:
    stderr_bytes = handle.read()
sigpipe = return_code == -signal.SIGPIPE and stderr_bytes == b""
reported_write_failure = stderr_bytes == "control-plane 输出失败。\n".encode()
if not sigpipe and not reported_write_failure:
    raise SystemExit("fixed CLI did not reach its stdout response write boundary")
value = {
    "boundary": "fixed-cli-stdout",
    "callerReceivedResponse": False,
    "disposition": "response-discarded-at-caller",
    "exitCode": return_code,
    "failureMode": "SIGPIPE" if sigpipe else "reported-write-failure",
    "readEndClosedBeforeSpawn": True,
    "transportResponseDecodedByCLI": True,
    "transportResponseLossProven": False,
}
with open(loss_path, "w", encoding="utf-8") as handle:
    json.dump(value, handle, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
}

activation_id="fixed-server-t1-${RUN_ID//[:]/-}"
MARSHAL_LOCAL_DOGFOOD_ACTIVATION= "$MARSHAL_BIN" doctor --self \
  --repository-root "$ROOT" --activation-id "$activation_id" --valid-for 2h \
  >"$EVIDENCE_ROOT/activation.json"
export MARSHAL_LOCAL_DOGFOOD_ACTIVATION="$EVIDENCE_ROOT/activation.json"
"$MARSHAL_BIN" init --json >"$EVIDENCE_ROOT/init.json"
"$MARSHAL_BIN" doctor --json >"$EVIDENCE_ROOT/doctor.json"
"$MARSHAL_BIN" version --json >"$EVIDENCE_ROOT/binary-version.json"

if [ "$SCENARIO" = order-quote-team ]; then
  team_deadline="$($PYTHON_BIN -I -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=8)).replace(microsecond=0).isoformat().replace("+00:00","Z"))')"
  "$PYTHON_BIN" -I -B scripts/fixed-server-team-inputs.py \
    --doctor "$EVIDENCE_ROOT/doctor.json" --repository "$ROOT" --base-ref "$EXPECTED_HEAD" \
    --model "$PI_MODEL" --goal-id "$RUN_ID" --proposal-id "$RUN_ID-plan" \
    --request-id "$RUN_ID-approve" --deadline "$team_deadline" --out "$EVIDENCE_ROOT/team-request.json"
else
task_id="FIXED-SERVER-T1-${EXPECTED_HEAD:0:12}"
task_renderer=scripts/fixed-server-t1-task.py
# Keep the array nonempty: macOS Bash 3.2 rejects an empty array expansion
# under nounset, even when quoted.
renderer_args=(--doctor "$EVIDENCE_ROOT/doctor.json" --repository "$ROOT" --base-ref "$EXPECTED_HEAD")
if [ "$SCENARIO" != t1-marker ]; then
  task_id="FIXED-SERVER-T2-${EXPECTED_HEAD:0:12}"
  task_renderer=scripts/fixed-server-t2-task.py
  if [ "$SCENARIO" = order-quote-timeout ] || [ "$SCENARIO" = order-quote-run-timeout ]; then
    renderer_args+=(--scenario "$SCENARIO")
  else
    renderer_args+=(--scenario order-quote)
  fi
fi
"$PYTHON_BIN" -I -B "$task_renderer" "${renderer_args[@]}" \
  --task-id "$task_id" --run-id "$RUN_ID" --model "$PI_MODEL" \
  --task-out "$EVIDENCE_ROOT/task.json" --policy-out "$EVIDENCE_ROOT/policy.json"
"$MARSHAL_BIN" task plan --task "$EVIDENCE_ROOT/task.json" --policy "$EVIDENCE_ROOT/policy.json" \
  --run "$RUN_ID" --json >"$EVIDENCE_ROOT/plan.json"
"$MARSHAL_BIN" task approve --run "$RUN_ID" --gate plan --actor fixed-server-t1-operator \
  --json >"$EVIDENCE_ROOT/approve.json"
fi

if [ "$VERIFY_PEER" -eq 1 ]; then
  peer_run="$RUN_ID-verify"
  peer_root="$ROOT/.marshal/fixed-server-t1-canary/$peer_run"
  [ ! -e "$peer_root" ] && [ ! -L "$peer_root" ] || die 'peer evidence 已存在'
  mkdir "$peer_root"
  "$PYTHON_BIN" -I -B scripts/fixed-server-t2-task.py \
    --doctor "$EVIDENCE_ROOT/doctor.json" --repository "$ROOT" --base-ref "$EXPECTED_HEAD" \
    --task-id "$task_id-VERIFY" --run-id "$peer_run" --model "$PI_MODEL" \
    --scenario order-quote --long-verify --task-out "$peer_root/task.json" --policy-out "$peer_root/policy.json"
  "$MARSHAL_BIN" task plan --task "$peer_root/task.json" --policy "$peer_root/policy.json" \
    --run "$peer_run" --json >"$peer_root/plan.json"
  "$MARSHAL_BIN" task approve --run "$peer_run" --gate plan --actor fixed-server-t1-operator \
    --json >"$peer_root/approve.json"
fi

# T1_NO_DIRECT_CLI_MUTATION_AFTER_APPROVAL
# From this point through evidence closure, every Marshal operation is the
# fixed control-plane surface. In particular there is no task run/verify or
# direct CLI recovery fallback.
"$PYTHON_BIN" -I -B scripts/fixed-server-t1-evidence.py observe-binary \
  --binary "$MARSHAL_BIN" --version-json "$EVIDENCE_ROOT/binary-version.json" \
  --out "$EVIDENCE_ROOT/binary-server1.json"
"$MARSHAL_BIN" control-plane serve >"$EVIDENCE_ROOT/server1-ready.json" \
  2>"$EVIDENCE_ROOT/server1.stderr" &
server1_pid=$!
wait_ready "$server1_pid" "$EVIDENCE_ROOT/server1-ready.json"
append_audit server1 serve ready
if [ "$SCENARIO" = order-quote-team ]; then
  # No task plan/approve or per-node Start. The authenticated team operation
  # is the only approval; resident Core owns creation and both first Starts.
  "$PYTHON_BIN" -I -B scripts/fixed-server-team-drive.py --evidence-root "$EVIDENCE_ROOT"
  assert_server_pid "$server1_pid"
  kill -TERM "$server1_pid"
  set +e
  wait "$server1_pid"
  team_server_status=$?
  set -e
  [ "$team_server_status" -eq 0 ] || die 'team server 未正常退出'
  write_process_evidence "$EVIDENCE_ROOT/server1-process.json" "$server1_pid" SIGTERM "$team_server_status"
  server1_pid=""
  printf '[fixed-server-team] TWO_IMPLEMENT_REVIEW_PENDING; integration and independent Decision remain open\n'
  exit 0
fi
if [ "$VERIFY_PEER" -eq 1 ]; then
  # Both Runs were frozen/approved before serve. All mutations now use the
  # fixed public surface. This experiment intentionally does not crash the
  # verifier or manufacture a Decision; it proves cross-Run scheduling only.
  "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py --run "$peer_run" \
    --evidence-dir "$peer_root/t2" --concurrent-stop-run "$RUN_ID"
  assert_server_pid "$server1_pid"
  kill -TERM "$server1_pid"
  set +e
  wait "$server1_pid"
  peer_server_status=$?
  set -e
  [ "$peer_server_status" -eq 0 ] || die 'cross-run server 未正常退出'
  write_process_evidence "$EVIDENCE_ROOT/server1-process.json" "$server1_pid" SIGTERM "$peer_server_status"
  server1_pid=""
  printf '[fixed-server-t2] CROSS_RUN_OBSERVED; independent business Decision still required\n'
  exit 0
fi
"$MARSHAL_BIN" control-plane status >"$EVIDENCE_ROOT/server1-status.json"
append_audit server1 status received
"$MARSHAL_BIN" control-plane inspect --run "$RUN_ID" >"$EVIDENCE_ROOT/server1-ready-inspect.json"
append_audit server1 inspect received-ready

read -r ready_sequence ready_head < <("$PYTHON_BIN" -I -B - "$EVIDENCE_ROOT/server1-ready-inspect.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    run = json.load(handle)
assert run.get("state") == "READY"
print(run["sequence"], run["authorityHead"])
PY
)
request_key="fixed-server-t1-${RUN_ID}-${EXPECTED_HEAD:0:12}"
deadline="$($PYTHON_BIN -I -B -c 'import datetime; print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=8)).replace(microsecond=0).isoformat().replace("+00:00","Z"))')"
"$PYTHON_BIN" -I -B - "$EVIDENCE_ROOT/start-request.json" "$RUN_ID" "$ready_sequence" "$ready_head" "$request_key" "$deadline" <<'PY'
import json, sys
path, run_id, sequence, head, request_key, deadline = sys.argv[1:]
value = {"deadline": deadline, "expectedAuthorityHead": head, "expectedSequence": int(sequence),
         "requestKey": request_key, "runId": run_id}
with open(path, "w", encoding="utf-8") as handle:
    json.dump(value, handle, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY

drop_start_response_at_caller "$EVIDENCE_ROOT/start-loss.stderr" \
  "$EVIDENCE_ROOT/start-response-loss.json" "$ready_sequence" "$ready_head" "$request_key" "$deadline"
append_start_audit server1 discarded-at-caller
"$MARSHAL_BIN" control-plane inspect --run "$RUN_ID" >"$EVIDENCE_ROOT/server1-running-inspect.json"
append_audit server1 inspect received-running

assert_server_pid "$server1_pid"
kill -KILL "$server1_pid"
set +e
wait "$server1_pid"
server1_status=$?
set -e
[ "$server1_status" -eq 137 ] || die "server1 SIGKILL wait status 非 137：$server1_status"
write_process_evidence "$EVIDENCE_ROOT/server1-process.json" "$server1_pid" SIGKILL "$server1_status"
server1_pid=""

"$PYTHON_BIN" -I -B scripts/fixed-server-t1-evidence.py observe-binary \
  --binary "$MARSHAL_BIN" --version-json "$EVIDENCE_ROOT/binary-version.json" \
  --out "$EVIDENCE_ROOT/binary-server2.json"
"$MARSHAL_BIN" control-plane serve >"$EVIDENCE_ROOT/server2-ready.json" \
  2>"$EVIDENCE_ROOT/server2.stderr" &
server2_pid=$!
wait_ready "$server2_pid" "$EVIDENCE_ROOT/server2-ready.json"
append_audit server2 serve ready
"$MARSHAL_BIN" control-plane status >"$EVIDENCE_ROOT/server2-status.json"
append_audit server2 status received
"$MARSHAL_BIN" control-plane inspect --run "$RUN_ID" >"$EVIDENCE_ROOT/server2-recovered-inspect.json"
append_audit server2 inspect received-recovered
"$MARSHAL_BIN" control-plane start --run "$RUN_ID" \
  --expected-sequence "$ready_sequence" --expected-authority-head "$ready_head" \
  --request-key "$request_key" --deadline "$deadline" >"$EVIDENCE_ROOT/server2-start-replay.json"
append_start_audit server2 received-replay
"$MARSHAL_BIN" control-plane inspect --run "$RUN_ID" >"$EVIDENCE_ROOT/server2-final-inspect.json"
append_audit server2 inspect received-final

server2_process_path="$EVIDENCE_ROOT/server2-process.json"
if [ "$STOP_CRASH" -eq 1 ]; then
  # The observer never returns a PID or a command. Only the unreaped child
  # owned by this shell is interrupted; post-wait evidence must still prove
  # that Run terminalization had not committed. A missed window is a failure.
  "$PYTHON_BIN" -I -B scripts/fixed-server-stop-fault.py wait --repository "$ROOT" --run "$RUN_ID"
  assert_server_pid "$server2_pid"
  kill -KILL "$server2_pid"
  set +e
  wait "$server2_pid"
  server2_status=$?
  set -e
  [ "$server2_status" -eq 137 ] || die "stop crash SIGKILL wait status 非 137：$server2_status"
  write_process_evidence "$server2_process_path" "$server2_pid" SIGKILL "$server2_status"
  server2_pid=""
  "$PYTHON_BIN" -I -B scripts/fixed-server-stop-fault.py after --repository "$ROOT" --run "$RUN_ID"
  "$PYTHON_BIN" -I -B scripts/fixed-server-t1-evidence.py observe-binary \
    --binary "$MARSHAL_BIN" --version-json "$EVIDENCE_ROOT/binary-version.json" \
    --out "$EVIDENCE_ROOT/binary-stop-recovery.json"
  "$MARSHAL_BIN" control-plane serve >"$EVIDENCE_ROOT/stop-recovery-ready.json" \
    2>"$EVIDENCE_ROOT/stop-recovery.stderr" &
  server2_pid=$!
  wait_ready "$server2_pid" "$EVIDENCE_ROOT/stop-recovery-ready.json"
  append_audit stop-recovery serve ready-after-interrupted-stop
  server2_process_path="$EVIDENCE_ROOT/stop-recovery-process.json"
fi

if [ "$SCENARIO" = order-quote-timeout ] || [ "$SCENARIO" = order-quote-run-timeout ]; then
  "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py \
    --run "$RUN_ID" --evidence-dir "$EVIDENCE_ROOT/t2" --observe-business-stop --timeout-seconds 180
elif [ "$SCENARIO" = order-quote-cancel ]; then
  "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py \
    --run "$RUN_ID" --evidence-dir "$EVIDENCE_ROOT/t2" --cancel
elif [ "$SCENARIO" = order-quote ]; then
  # The same post-restart server owns all T2 mutation. The driver stops at an
  # exact ReviewPacket unless an external reviewer supplies a Decision. The
  # driver cannot author it or replace current-ledger admission.
  "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py \
    --run "$RUN_ID" --evidence-dir "$EVIDENCE_ROOT/t2" --await-review-seconds "$AWAIT_REVIEW"
fi

assert_server_pid "$server2_pid"
kill -TERM "$server2_pid"
set +e
wait "$server2_pid"
server2_status=$?
set -e
[ "$server2_status" -eq 0 ] || die "server2 未正常退出：$server2_status"
write_process_evidence "$server2_process_path" "$server2_pid" SIGTERM "$server2_status"
server2_pid=""

if [ "$SCENARIO" = order-quote-cancel ] || [ "$SCENARIO" = order-quote-timeout ] || [ "$SCENARIO" = order-quote-run-timeout ]; then
  "$PYTHON_BIN" -I -B scripts/fixed-server-t1-evidence.py observe-binary \
    --binary "$MARSHAL_BIN" --version-json "$EVIDENCE_ROOT/binary-version.json" \
    --out "$EVIDENCE_ROOT/binary-server3.json"
  "$MARSHAL_BIN" control-plane serve >"$EVIDENCE_ROOT/server3-ready.json" \
    2>"$EVIDENCE_ROOT/server3.stderr" &
  # Reuse the now-empty owned child slot so the existing EXIT trap reaps it.
  server2_pid=$!
  wait_ready "$server2_pid" "$EVIDENCE_ROOT/server3-ready.json"
  if [ "$SCENARIO" = order-quote-cancel ]; then
    append_audit server3 serve ready-after-cancel
    "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py \
      --run "$RUN_ID" --evidence-dir "$EVIDENCE_ROOT/t2-recovery" --cancel-recovery
  else
    append_audit server3 serve ready-after-business-stop
    "$PYTHON_BIN" -I -B scripts/fixed-server-t2-drive.py \
      --run "$RUN_ID" --evidence-dir "$EVIDENCE_ROOT/t2-recovery" --business-stop-recovery
  fi
  assert_server_pid "$server2_pid"
  kill -TERM "$server2_pid"
  set +e
  wait "$server2_pid"
  server3_status=$?
  set -e
  [ "$server3_status" -eq 0 ] || die "server3 未正常退出：$server3_status"
  write_process_evidence "$EVIDENCE_ROOT/server3-process.json" "$server2_pid" SIGTERM "$server3_status"
  server2_pid=""
fi

if [ "$SCENARIO" = t1-marker ]; then
  "$PYTHON_BIN" -I -B scripts/fixed-server-t1-evidence.py check \
    --repository "$ROOT" --evidence-root "$EVIDENCE_ROOT" --binary "$MARSHAL_BIN" \
    --expected-head "$EXPECTED_HEAD" --run-id "$RUN_ID" --out "$EVIDENCE_ROOT/summary.json"
  printf '[fixed-server-t1] PASS run=%s evidence=%s\n' "$RUN_ID" "$EVIDENCE_ROOT"
elif [ "$SCENARIO" = order-quote-timeout ] || [ "$SCENARIO" = order-quote-run-timeout ]; then
  printf '[fixed-server-t2] RESIDENT_STOP_OBSERVED run=%s; deadline witness requires independent evidence audit, not ACCEPTED\n' "$RUN_ID"
elif [ "$SCENARIO" = order-quote-cancel ]; then
  printf '[fixed-server-t2] CANCELLED run=%s; exact replay and stopped Collect verified, not ACCEPTED\n' "$RUN_ID"
else
  if [ "$AWAIT_REVIEW" -eq 0 ]; then
    printf '[fixed-server-t2] REVIEW_PENDING run=%s; independent Decision required\n' "$RUN_ID"
  else
    printf '[fixed-server-t2] ACCEPTED run=%s; independent Decision applied through server\n' "$RUN_ID"
  fi
fi
