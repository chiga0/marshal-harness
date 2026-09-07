#!/bin/bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
DRIVER="$ROOT/scripts/fixed-server-t1-canary.sh"
WORKFLOW="$ROOT/.github/workflows/fixed-server-t1-canary.yml"
GENERATOR="$ROOT/scripts/fixed-server-t1-task.py"
CHECKER="$ROOT/scripts/fixed-server-t1-evidence.py"

fail() {
  printf 'fixed-server-t1-canary_test: FAIL: %s\n' "$*" >&2
  exit 1
}

for path in "$DRIVER" "$WORKFLOW" "$GENERATOR" "$CHECKER"; do
  [ -f "$path" ] || fail "missing $path"
done

post_approval="$(awk 'seen { print } /T1_NO_DIRECT_CLI_MUTATION_AFTER_APPROVAL/ { seen=1 }' "$DRIVER")"
printf '%s\n' "$post_approval" | grep -E '"\$MARSHAL_BIN"[[:space:]]+task[[:space:]]' >/dev/null \
  && fail 'post-approval driver contains direct task CLI'
printf '%s\n' "$post_approval" | grep -E '"\$MARSHAL_BIN"[[:space:]]+(version|doctor|init)[[:space:]]' >/dev/null \
  && fail 'post-approval driver contains non-control-plane Marshal CLI'
printf '%s\n' "$post_approval" | grep -F 'control-plane serve' >/dev/null \
  || fail 'driver lacks fixed serve'
printf '%s\n' "$post_approval" | grep -F 'control-plane start' >/dev/null \
  || fail 'driver lacks fixed start'
grep -F 'readEndClosedBeforeSpawn' "$DRIVER" >/dev/null \
  || fail 'driver lacks deterministic caller-boundary response loss'
grep -F 'os.close(read_fd)' "$DRIVER" >/dev/null \
  || fail 'driver does not close pipe reader before spawn'
grep -F 'transportResponseLossProven' "$DRIVER" >/dev/null \
  || fail 'driver does not state the response-loss evidence boundary'
grep -F 'kill -KILL "$server1_pid"' "$DRIVER" >/dev/null \
  || fail 'driver lacks bounded server1 crash stop'
grep -F 'kill -TERM "$server2_pid"' "$DRIVER" >/dev/null \
  || fail 'driver lacks normal server2 stop'
if grep -E '(^|[[:space:]])(pkill|killall)[[:space:]]|kill[[:space:]]+-[^[:space:]]+[[:space:]]+[-$*{]*\(' "$DRIVER" >/dev/null; then
  fail 'driver contains broad process termination'
fi
if printf '%s\n' "$post_approval" | grep -E 'go[[:space:]]+(run|build)|cmd/marshal-server' >/dev/null; then
  fail 'driver contains temporary executable or legacy server'
fi

grep -F 'workflow_dispatch:' "$WORKFLOW" >/dev/null || fail 'workflow is not manual-only'
if grep -E '^[[:space:]]+(push|pull_request|schedule):' "$WORKFLOW" >/dev/null; then
  fail 'workflow has an automatic trigger'
fi
grep -F 'ref: ${{ inputs.expected-head }}' "$WORKFLOW" >/dev/null || fail 'workflow does not exact-checkout input head'
grep -F 'scripts/release-ci-gate.sh' "$WORKFLOW" >/dev/null || fail 'workflow lacks required-CI pre-gate'
"/usr/bin/python3" -I -B "$ROOT/scripts/candidate-ci-gate_test.py"
grep -F 'test "$EXPECTED_HEAD" = "$DISPATCH_HEAD"' "$WORKFLOW" >/dev/null || fail 'candidate checkout is not dispatch-bound'
grep -F 'go1.26.6.darwin-arm64.tar.gz' "$WORKFLOW" >/dev/null || fail 'workflow lacks pinned Go toolchain'
grep -F 'PHY_PI_VERSION: "0.84.4"' "$WORKFLOW" >/dev/null || fail 'workflow lacks Pi 0.84.4 pin'
grep -F 'fixed-server-t1-canary.sh' "$WORKFLOW" >/dev/null || fail 'workflow does not use the fixed driver'
grep -F '.marshal/runtime-v1/result-ingress/result-ingress.jsonl' "$WORKFLOW" >/dev/null \
  || fail 'workflow omits the production runtime-v1 ingress ledger'
if grep -F '.marshal/result-ingress/result-ingress.jsonl' "$WORKFLOW" >/dev/null; then
  fail 'workflow collects a nonexistent legacy ingress path'
fi
diagnostics="$(awk '/      - name: Upload fixed server diagnostic evidence/{copy=1} /      - name: Upload fixed server T1 evidence/{copy=0} copy' "$WORKFLOW")"
printf '%s\n' "$diagnostics" | grep -F 'name: fixed-server-diagnostics-${{ github.run_id }}' >/dev/null \
  || fail 'workflow lacks separate diagnostic artifact'
printf '%s\n' "$diagnostics" | grep -F '.marshal/runtime-v1/result-ingress/result-ingress.jsonl' >/dev/null \
  || fail 'diagnostic artifact lacks authoritative ledger'
if printf '%s\n' "$diagnostics" | grep -E 'dist/|review-inputs|/transcript|/activation.json|/policy.json' >/dev/null; then
  fail 'diagnostic artifact includes executable, review archive, transcript or configuration'
fi
# The generated frozen Task already belongs to the full artifact. The small
# copy enables budget/specDigest audit without downloading the executable.
for leaf in task.json 'stop-crash-*.json' 'stop-recovery-*.json' 'server*-process.json'; do
  printf '%s\n' "$diagnostics" | grep -F "/$leaf" >/dev/null || fail "missing stop evidence $leaf"
done
"/usr/bin/python3" -I -B "$ROOT/scripts/fixed-server-stop-fault_test.py"
# Both approved Runs share one fixed server; the peer command's rendezvous
# is diagnostic only and never authorizes a Decision or a Worker restart.
grep -F -- '--concurrent-stop-run "$RUN_ID"' "$DRIVER" >/dev/null || fail 'missing fixed cross-run driver'
grep -F -- '--scenario order-quote --long-verify' "$DRIVER" >/dev/null || fail 'peer business verifier was not frozen before approval'
grep -F 'verify-peer:' "$WORKFLOW" >/dev/null || fail 'missing explicit cross-run opt-in'
grep -F 'order-quote-team' "$WORKFLOW" >/dev/null || fail 'missing team candidate opt-in'
grep -F 'scripts/fixed-server-team-drive.py --evidence-root' "$DRIVER" >/dev/null || fail 'missing team client'
printf '%s\n' "$diagnostics" | grep -F '/team/*.json' >/dev/null || fail 'missing team response evidence'
# Team mode branches before legacy task plan/approve. Its observer delegates
# progress observation only; neither helper starts, collects or verifies.
/usr/bin/python3 -I -B - "$DRIVER" "$ROOT/scripts/fixed-server-team-drive.py" <<'PY'
import pathlib, sys
shell, driver = (pathlib.Path(p).read_text() for p in sys.argv[1:])
team = shell.index('if [ "$SCENARIO" = order-quote-team ]; then')
legacy = shell.index('task_id="FIXED-SERVER-T1-')
assert team < legacy and '\nelse\n' in shell[team:legacy]
assert driver.count('["team-approve",') == 1
assert '["start",' not in driver and 'start_ready(' not in driver
assert 'observe_review(' in driver and 't2.drive(' not in driver and 'processOverlapProven' in driver
assert '["collect",' not in driver and '["verify",' not in driver
assert 'server_options+=(--auto-team-progress)' in shell
PY
printf '%s\n' "$diagnostics" | grep -F '/verification-report.json' >/dev/null || fail 'missing cross-run report evidence'
for phase in t2 t2-recovery; do
  for leaf in driver-subject.json 'call-*.json' cancel-request.json; do
    printf '%s\n' "$diagnostics" | grep -F "/$phase/$leaf" >/dev/null \
      || fail 'diagnostic artifact omits bounded driver subject/call/request metadata'
  done
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf '%s\n' '{"policyEnvironmentBinding":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}' >"$tmp/doctor.json"
head="$(git -C "$ROOT" rev-parse HEAD)"
"/usr/bin/python3" -I -B "$GENERATOR" \
  --doctor "$tmp/doctor.json" --repository "$ROOT" --base-ref "$head" \
  --task-id FIXED-SERVER-T1-TEST --run-id fixed-server-t1-test \
  --model provider/model --task-out "$tmp/task.json" --policy-out "$tmp/policy.json"
"/usr/bin/python3" -I -B - "$tmp/task.json" "$tmp/policy.json" "$head" "$ROOT" <<'PY'
import hashlib, json, sys
task, policy = (json.load(open(path, encoding="utf-8")) for path in sys.argv[1:3])
assert task["repository"] == {"baseRef": sys.argv[3], "expectedRemoteUrl": "https://github.com/chiga0/marshal-harness.git", "path": sys.argv[4], "remote": "origin"}
assert task["worker"]["preferredAdapter"] == "pi" and task["worker"]["fallbackAdapters"] == []
assert task["budgets"]["maxAttempts"] == 1
assert task["scope"]["allowPaths"] == ["fixed-server-t1-canary.txt"]
assert "sleep 300" in task["work"]["objective"]
assert "/bin/sleep" not in task["work"]["objective"]
assert policy["effective"]["allowedAdapters"] == ["pi"]
detached = dict(policy)
detached["policyDigest"] = ""
raw = json.dumps(detached, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
assert policy["policyDigest"] == "sha256:" + hashlib.sha256(raw).hexdigest()
PY

printf 'fixed-server-t1-canary_test: PASS\n'
