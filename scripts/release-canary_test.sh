#!/usr/bin/env bash
# release-canary.sh 的纯 shell contract test。只使用固定路径 fake Marshal；
# 不构建 Go、不执行真实 Pi、不创建真实 Marshal Run。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
DRIVER_SOURCE="${SCRIPT_DIR}/release-canary.sh"
TMP_RAW="$(mktemp -d "${TMPDIR:-/tmp}/release-canary-test.XXXXXX")"
TMP_ROOT="$(cd "$TMP_RAW" && pwd -P)"
trap 'rm -rf "$TMP_ROOT"' EXIT
FIXTURE_ROOT="${TMP_ROOT}/release-canary-test.root"
FAKE_STATE="${FIXTURE_ROOT}/.marshal/fake-state"
FAKE_LOG="${FIXTURE_ROOT}/.marshal/fake-marshal.log"
PI_ROOT="${TMP_ROOT}/pi"
PI_BIN="${PI_ROOT}/bin/pi"
PI_BUNDLE="${PI_ROOT}/lib/node_modules/pi/dist/bundle/cli.js"
VERSION="1.0.0-rc1"

fail() {
  printf '[release-canary-test] FAIL: %s\n' "$*" >&2
  exit 1
}

expect_fail() {
  local description="$1"
  shift
  if "$@" >"${TMP_ROOT}/last.out" 2>"${TMP_ROOT}/last.err"; then
    fail "$description：预期失败但命令成功"
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

mkdir -p "${FIXTURE_ROOT}/scripts" "${FIXTURE_ROOT}/bin" \
  "${FIXTURE_ROOT}/schemas/examples/happy-path" "$(dirname "$PI_BIN")" "$(dirname "$PI_BUNDLE")"
cp "$DRIVER_SOURCE" "${FIXTURE_ROOT}/scripts/release-canary.sh"
cp "${SCRIPT_DIR}/../schemas/examples/happy-path/task-spec.json" \
  "${FIXTURE_ROOT}/schemas/examples/happy-path/task-spec.json"
cp "${SCRIPT_DIR}/../schemas/examples/happy-path/policy-snapshot.json" \
  "${FIXTURE_ROOT}/schemas/examples/happy-path/policy-snapshot.json"
chmod 0755 "${FIXTURE_ROOT}/scripts/release-canary.sh"

cat >"$PI_BUNDLE" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = --version ]; then
  printf '0.84.3\n'
  exit 0
fi
printf 'fake Pi must never be launched by the contract test\n' >&2
exit 99
EOF
chmod 0755 "$PI_BUNDLE"
ln -s ../lib/node_modules/pi/dist/bundle/cli.js "$PI_BIN"
PI_SHA256="$(sha256_file "$PI_BUNDLE")"

cat >"${FIXTURE_ROOT}/bin/marshal" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

mkdir -p "$MARSHAL_RELEASE_CANARY_FAKE_STATE"
printf '%s\n' "$*" >>"$MARSHAL_RELEASE_CANARY_FAKE_LOG"

unexpected() {
  printf 'unexpected fake Marshal argv:' >&2
  printf ' <%s>' "$@" >&2
  printf '\n' >&2
  exit 90
}

run_id=""
previous=""
for argument in "$@"; do
  if [ "$previous" = --run ]; then
    run_id="$argument"
    break
  fi
  previous="$argument"
done
state_path="${MARSHAL_RELEASE_CANARY_FAKE_STATE}/${run_id}.state"
task_id="RELEASE-CANARY-${FAKE_EXPECTED_HEAD:0:12}"
digest1="sha256:1111111111111111111111111111111111111111111111111111111111111111"
digest2="sha256:2222222222222222222222222222222222222222222222222222222222222222"
digest3="sha256:3333333333333333333333333333333333333333333333333333333333333333"
digest4="sha256:4444444444444444444444444444444444444444444444444444444444444444"
digest5="sha256:5555555555555555555555555555555555555555555555555555555555555555"
digest6="sha256:6666666666666666666666666666666666666666666666666666666666666666"
digest7="sha256:7777777777777777777777777777777777777777777777777777777777777777"
digest8="sha256:8888888888888888888888888888888888888888888888888888888888888888"
digest9="sha256:9999999999999999999999999999999999999999999999999999999999999999"
control_root="$(dirname "$PWD")/control"

case "${1:-} ${2:-}" in
  "version --json")
    [ "$#" -eq 2 ] || unexpected "$@"
    printf '{"version":"%s","commit":"%s","buildDate":"2026-08-28T00:00:00Z","goVersion":"go1.test","os":"darwin","arch":"arm64","selfProfile":"darwin-local-dogfood"}\n' \
      "${FAKE_VERSION:-1.0.0-rc1}" "$FAKE_EXPECTED_HEAD"
    ;;
  "doctor --self")
    [ "$#" -eq 8 ] && [ "$3" = --repository-root ] && [ "$4" = "$PWD" ] && \
      [ "$5" = --activation-id ] && [ "$6" = "$(basename "$(dirname "$PWD")")" ] && \
      [ "$7" = --valid-for ] && [ "$8" = 4h ] || unexpected "$@"
    printf '{"schemaVersion":"marshal.local-dogfood-activation.v1","activationId":"fixture"}\n'
    ;;
  "doctor --json")
    [ "$#" -eq 2 ] || unexpected "$@"
    printf '{"status":"ok","policyEnvironmentBinding":{"schemaVersion":"marshal.local-dogfood-environment-binding.v1","selfProfile":"darwin-local-dogfood","activationDigest":"%s","identitySubjectDigest":"%s","assurance":"ordinary-user","execution":"workspace-write","production":false,"publication":"none"},"workers":[{"adapterId":"pi","outcome":"registered","compatibility":"supported","binaryVersion":"0.84.3","authorityMode":"ordinary-user"}]}\n' "$digest1" "$digest2"
    ;;
  "task scaffold")
    [ "$#" -eq 6 ] && [ "$3" = --draft ] && [ "$4" = "$control_root/task-draft.json" ] && \
      [ "$5" = --preferred-adapter ] && [ "$6" = pi ] || unexpected "$@"
    /usr/bin/python3 -I -B - "$4" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    task = json.load(handle)
task["worker"]["preferredAdapter"] = "pi"
task["worker"]["fallbackAdapters"] = []
json.dump(task, sys.stdout, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
sys.stdout.write("\n")
PY
    ;;
  "task plan")
    [ "$#" -eq 9 ] && [ "$3" = --task ] && [ "$4" = "$control_root/task-spec.json" ] && \
      [ "$5" = --policy ] && [ "$6" = "$control_root/policy-snapshot.json" ] && \
      [ "$7" = --run ] && [ "$8" = "$run_id" ] && [ "$9" = --json ] || unexpected "$@"
    /usr/bin/python3 -I -B - "$4" "$6" "$run_id" <<'PY'
import hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    task = json.load(handle)
with open(sys.argv[2], encoding="utf-8") as handle:
    policy = json.load(handle)
assert task.get("apiVersion") == "marshal.dev/v1alpha1"
assert task.get("kind") == "Task"
assert task["worker"].get("preferredAdapter") == "pi"
assert task["worker"].get("fallbackAdapters") == []
assert task["worker"].get("model") == "qwen-token-plan-cn/qwen3.6-flash"
assert policy.get("apiVersion") == "marshal.dev/v1alpha1"
assert policy.get("kind") == "PolicySnapshot"
assert policy.get("runId") == sys.argv[3]
recorded = policy["policyDigest"]
policy["policyDigest"] = ""
encoded = json.dumps(policy, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
assert recorded == "sha256:" + hashlib.sha256(encoded).hexdigest()
PY
    printf 'READY\n' >"$state_path"
    printf '{"status":"planned"}\n'
    ;;
  "task approve")
    [ "$#" -eq 9 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && \
      [ "$5" = --gate ] && [ "$6" = plan ] && [ "$7" = --actor ] && \
      [ "$8" = release-canary ] && [ "$9" = --json ] || unexpected "$@"
    printf 'APPROVED\n' >"$state_path"
    printf '{"status":"approved"}\n'
    ;;
  "task run")
    [ "$#" -eq 5 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && [ "$5" = --json ] || unexpected "$@"
    mkdir -p "$PWD/.marshal/runs/$run_id/attempts/attempt-1"
    printf '{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"%s","runId":"%s","attemptId":"attempt-1","adapter":{"id":"pi","executable":"%s","version":"0.84.3","model":"qwen-token-plan-cn/qwen3.6-flash"},"status":"completed","summary":"fixture","declaredChangedFiles":["release-canary.txt"],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"startedAt":"2026-08-28T00:00:00Z","completedAt":"2026-08-28T00:00:01Z"}\n' \
      "$task_id" "$run_id" "$MARSHAL_RELEASE_CANARY_PI_BUNDLE" \
      >"$PWD/.marshal/runs/$run_id/attempts/attempt-1/worker-result.json"
    printf 'VERIFYING\n' >"$state_path"
    printf '{"status":"worker-completed"}\n'
    ;;
  "task verify")
    [ "$#" -eq 5 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && [ "$5" = --json ] || unexpected "$@"
    printf 'REVIEW_PENDING\n' >"$state_path"
    printf '{"status":"pass"}\n'
    ;;
  "task review")
    if [ "$#" -eq 7 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && \
      [ "$5" = --decision ] && [ "$7" = --json ]; then
      printf 'ACCEPTED\n' >"$state_path"
      printf '{"status":"applied","verdict":"accept","targetState":"ACCEPTED","decisionDigest":"%s"}\n' "$digest8"
    elif [ "$#" -eq 5 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && [ "$5" = --json ]; then
      printf '{"status":"generated","packetDigest":"%s","promptVersion":"fixture","packet":{"apiVersion":"marshal.dev/v1alpha1","kind":"ReviewPacket","taskId":"%s","runId":"%s","reviewRound":1,"specDigest":"%s","baseSha":"%s","snapshotDigest":"%s","diffDigest":"%s","verificationDigest":"%s","artifactManifestDigest":"%s","workerResultDigests":["%s"],"evidenceDigest":"%s","localSelfIdentityBinding":{"schemaVersion":"marshal.local-self-identity-review-binding.v1","selfProfile":"darwin-local-dogfood","activationDigest":"%s","identitySubjectDigest":"%s","attemptId":"attempt-1","reviewRound":1,"verificationBindingDigest":"%s","verificationObservationDigest":"%s","reviewObservationDigest":"%s","applicability":{"schemaVersion":"marshal.local-dogfood-environment-binding.v1","selfProfile":"darwin-local-dogfood","activationDigest":"%s","identitySubjectDigest":"%s","assurance":"ordinary-user","execution":"workspace-write","production":false,"publication":"none"}},"inputs":{"taskSpec":"task-spec.json","patch":"observed.patch","verificationReport":"verification-report.json","artifactManifest":"artifact-manifest.json"},"previousBlockingFindings":[],"generatedAt":"2026-08-28T00:00:00Z"}}\n' \
        "$digest9" "$task_id" "$run_id" "$digest1" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" "$digest2" "$digest3" "$digest4" "$digest5" "$digest6" "$digest7" "$digest1" "$digest2" "$digest3" "$digest4" "$digest5" "$digest1" "$digest2"
    else
      unexpected "$@"
    fi
    ;;
  "task status")
    [ "$#" -eq 5 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && [ "$5" = --json ] || unexpected "$@"
    state="$(cat "$state_path")"
    base_sha="$(/usr/bin/git rev-parse HEAD)"
    printf '{"apiVersion":"marshal.dev/v1alpha1","kind":"RunState","taskId":"%s","runId":"%s","state":"%s","sequence":7,"specDigest":"%s","policyDigest":"%s","capabilityDigest":"%s","baseSha":"%s","reviewRound":1,"currentAttemptId":"attempt-1","attemptsUsed":1,"operationalRetriesUsed":0,"reworkRoundsUsed":0,"createdAt":"2026-08-28T00:00:00Z","updatedAt":"2026-08-28T00:00:01Z"}\n' \
      "$task_id" "$run_id" "$state" "$digest1" "$digest2" "$digest3" "$base_sha"
    ;;
  "init ")
    [ "$#" -eq 1 ] || unexpected "$@"
    printf '{"status":"initialized"}\n'
    ;;
  *)
    unexpected "$@"
    ;;
esac
EOF
chmod 0755 "${FIXTURE_ROOT}/bin/marshal"

cat >"${FIXTURE_ROOT}/.gitignore" <<'EOF'
.marshal/
bin/
EOF
"/usr/bin/git" -C "$FIXTURE_ROOT" init -q -b main
"/usr/bin/git" -C "$FIXTURE_ROOT" config user.email release-canary-test@example.invalid
"/usr/bin/git" -C "$FIXTURE_ROOT" config user.name "Release Canary Test"
"/usr/bin/git" -C "$FIXTURE_ROOT" add .gitignore scripts/release-canary.sh schemas/examples/happy-path
"/usr/bin/git" -C "$FIXTURE_ROOT" commit -q -m "fixture: release canary driver"
"/usr/bin/git" -C "$FIXTURE_ROOT" remote add origin https://github.com/chiga0/marshal-harness.git
EXPECTED_HEAD="$("/usr/bin/git" -C "$FIXTURE_ROOT" rev-parse HEAD)"

run_driver() {
  MARSHAL_RELEASE_CANARY_TEST_MODE=1 \
  MARSHAL_RELEASE_CANARY_PI_BIN="$PI_BIN" \
  MARSHAL_RELEASE_CANARY_PI_BUNDLE="$PI_BUNDLE" \
  MARSHAL_RELEASE_CANARY_PI_BUNDLE_SHA256="$PI_SHA256" \
  MARSHAL_RELEASE_CANARY_FAKE_STATE="$FAKE_STATE" \
  MARSHAL_RELEASE_CANARY_FAKE_LOG="$FAKE_LOG" \
  FAKE_EXPECTED_HEAD="$EXPECTED_HEAD" \
  FAKE_VERSION="${FAKE_VERSION:-$VERSION}" \
  "${FIXTURE_ROOT}/scripts/release-canary.sh" "$@"
}

make_accept_decision() {
  local run_id="$1"
  local control_root="${FIXTURE_ROOT}/.marshal/release-canary/${run_id}/control"
  local packet_path="${control_root}/review-packet-output.json"
  local decision_path="${control_root}/lead-review-decision.json"
  /usr/bin/python3 -I -B - "$packet_path" "$decision_path" <<'PY'
import datetime, hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    output = json.load(handle)
packet = output["packet"]
binding = packet["localSelfIdentityBinding"]
binding_bytes = json.dumps(binding, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
decision = {
    "apiVersion": "marshal.dev/v1alpha1",
    "kind": "ReviewDecision",
    "taskId": packet["taskId"],
    "runId": packet["runId"],
    "reviewRound": packet["reviewRound"],
    "reviewer": {"type": "lead-agent", "id": "independent-release-reviewer", "model": "fixture"},
    "specDigest": packet["specDigest"],
    "reviewPacketDigest": output["packetDigest"],
    "verificationDigest": packet["verificationDigest"],
    "artifactManifestDigest": packet["artifactManifestDigest"],
    "evidenceDigest": packet["evidenceDigest"],
    "localSelfIdentityBindingDigest": "sha256:" + hashlib.sha256(binding_bytes).hexdigest(),
    "verdict": "accept",
    "summary": "独立 fixture reviewer 接受当前精确证据。",
    "blockingFindings": [],
    "nonBlockingFindings": [],
    "publicationRecommendation": "not-applicable",
    "mergeRecommendation": "do-not-merge",
    "decidedAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
}
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    json.dump(decision, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
  printf '%s\n' "$decision_path"
}

MAIN_RUN="rc1-main"
run_driver run --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = REVIEW_PENDING ] || fail 'run 子命令没有停在 REVIEW_PENDING'
run_driver status --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect REVIEW_PENDING >/dev/null
MAIN_DECISION="$(make_accept_decision "$MAIN_RUN")"
run_driver finalize --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$MAIN_DECISION" >/dev/null
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = ACCEPTED ] || fail 'finalize 没有到达 ACCEPTED'
run_driver status --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect ACCEPTED >/dev/null

expect_fail 'fake Marshal 接受未知 flag' env \
  MARSHAL_RELEASE_CANARY_FAKE_STATE="$FAKE_STATE" \
  MARSHAL_RELEASE_CANARY_FAKE_LOG="$FAKE_LOG" \
  FAKE_EXPECTED_HEAD="$EXPECTED_HEAD" \
  FAKE_VERSION="$VERSION" \
  /bin/bash -c 'cd "$1" && "$2" task status --run "$3" --json --unknown' \
  _ "${FIXTURE_ROOT}/.marshal/release-canary/${MAIN_RUN}/repository" \
  "${FIXTURE_ROOT}/bin/marshal" "$MAIN_RUN"

/usr/bin/python3 -I -B - "$FAKE_LOG" "$MAIN_RUN" <<'PY' || fail '权威命令序列或停点错误'
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    lines = [line.strip() for line in handle if line.strip()]
run_id = sys.argv[2]
wanted = [
    "version --json", "doctor --self", "doctor --json", "init",
    "task scaffold", "task plan", "task approve", "task run", "task verify",
    f"task review --run {run_id} --json",
    f"task status --run {run_id} --json",
    f"task status --run {run_id} --json",
]
position = 0
for prefix in wanted:
    while position < len(lines) and not lines[position].startswith(prefix):
        position += 1
    assert position < len(lines), (prefix, lines)
    position += 1
assert not any("go run" in line or "go test" in line for line in lines)
PY

DECISION_DRIFT_RUN="rc1-decision-drift"
run_driver run --run-id "$DECISION_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
DRIFT_DECISION="$(make_accept_decision "$DECISION_DRIFT_RUN")"
/usr/bin/python3 -I -B - "$DRIFT_DECISION" <<'PY'
import json, os, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
value["reviewPacketDigest"] = "sha256:" + "0" * 64
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
expect_fail '陈旧 Decision' run_driver finalize --run-id "$DECISION_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${DECISION_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail '陈旧 Decision 产生了状态副作用'

VERSION_DRIFT_RUN="rc1-version-drift"
run_driver run --run-id "$VERSION_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
VERSION_DRIFT_DECISION="$(make_accept_decision "$VERSION_DRIFT_RUN")"
FAKE_VERSION="1.0.0-rc2" expect_fail 'Marshal version 漂移' run_driver finalize --run-id "$VERSION_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$VERSION_DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${VERSION_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail 'version 漂移产生了状态副作用'

PI_DRIFT_RUN="rc1-pi-drift"
run_driver run --run-id "$PI_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
PI_DRIFT_DECISION="$(make_accept_decision "$PI_DRIFT_RUN")"
cp "$PI_BUNDLE" "${TMP_ROOT}/pi-bundle.saved"
printf '# drift\n' >>"$PI_BUNDLE"
expect_fail 'Pi bundle 漂移' run_driver finalize --run-id "$PI_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$PI_DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${PI_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail 'Pi 漂移产生了状态副作用'
cp "${TMP_ROOT}/pi-bundle.saved" "$PI_BUNDLE"

STATE_DRIFT_RUN="rc1-state-drift"
run_driver run --run-id "$STATE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
printf 'ACCEPTED\n' >"${FAKE_STATE}/${STATE_DRIFT_RUN}.state"
expect_fail 'Run 状态漂移' run_driver status --run-id "$STATE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect REVIEW_PENDING

HEAD_DRIFT_RUN="rc1-head-drift"
run_driver run --run-id "$HEAD_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
HEAD_DRIFT_DECISION="$(make_accept_decision "$HEAD_DRIFT_RUN")"
printf 'head drift\n' >"${FIXTURE_ROOT}/tracked-drift.txt"
"/usr/bin/git" -C "$FIXTURE_ROOT" add tracked-drift.txt
"/usr/bin/git" -C "$FIXTURE_ROOT" commit -q -m "fixture: drift source head"
expect_fail 'final HEAD 漂移' run_driver finalize --run-id "$HEAD_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$HEAD_DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${HEAD_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail 'HEAD 漂移产生了状态副作用'

if grep -En 'go (run|test)' "${FIXTURE_ROOT}/scripts/release-canary.sh" >/dev/null; then
  fail 'driver 包含会生成匿名 Mach-O 的 go run/go test'
fi

printf '[release-canary-test] PASS\n'
