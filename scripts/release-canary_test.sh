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
PI_NODE="${PI_ROOT}/bin/node"
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
  "${FIXTURE_ROOT}/schemas/examples/happy-path" "${FIXTURE_ROOT}/empty-hooks" \
  "$(dirname "$PI_BIN")" "$(dirname "$PI_BUNDLE")"
cp "$DRIVER_SOURCE" "${FIXTURE_ROOT}/scripts/release-canary.sh"
cp "${SCRIPT_DIR}/release-contract.sh" "${FIXTURE_ROOT}/scripts/release-contract.sh"
cp "${SCRIPT_DIR}/../go.mod" "${FIXTURE_ROOT}/go.mod"
cp "${SCRIPT_DIR}/../schemas/examples/happy-path/task-spec.json" \
  "${FIXTURE_ROOT}/schemas/examples/happy-path/task-spec.json"
cp "${SCRIPT_DIR}/../schemas/examples/happy-path/policy-snapshot.json" \
  "${FIXTURE_ROOT}/schemas/examples/happy-path/policy-snapshot.json"
chmod 0755 "${FIXTURE_ROOT}/scripts/release-canary.sh" "${FIXTURE_ROOT}/scripts/release-contract.sh"

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
cat >"$PI_NODE" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = --version ]; then
  printf 'v24.15.0\n'
  exit 0
fi
printf 'fake Node must never be launched without --version by the contract test\n' >&2
exit 99
EOF
chmod 0755 "$PI_NODE"
PI_NODE_SHA256="$(sha256_file "$PI_NODE")"

cat >"${FIXTURE_ROOT}/bin/marshal" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

mkdir -p "$MARSHAL_RELEASE_CANARY_FAKE_STATE"
printf '%s\n' "$*" >>"$MARSHAL_RELEASE_CANARY_FAKE_LOG"
[ "${MARSHAL_PI_NODE_PATH:-}" = "$MARSHAL_RELEASE_CANARY_PI_NODE" ] || {
  printf 'fixed Node runtime was not exported to Marshal\n' >&2
  exit 91
}

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
    printf '%s\n' "$4" >"${state_path}.task-path"
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
    /usr/bin/python3 -I -B - "$(cat "${state_path}.task-path")" \
      "$MARSHAL_RELEASE_CANARY_FAKE_STATE/${run_id}.acceptance" "$FAKE_EXPECTED_HEAD" <<'PY'
import json, pathlib, subprocess, sys

with open(sys.argv[1], encoding="utf-8") as handle:
    task = json.load(handle)
command = task["acceptance"]["commands"][0]
argv = command["argv"]
marker = "marshal-release-canary:" + sys.argv[3]
root = pathlib.Path(sys.argv[2])
real_lf = root / "real-lf"
literal_backslash_n = root / "literal-backslash-n"
real_lf.mkdir(parents=True, exist_ok=True)
literal_backslash_n.mkdir(parents=True, exist_ok=True)
(real_lf / "release-canary.txt").write_text(marker + "\n", encoding="utf-8")
(literal_backslash_n / "release-canary.txt").write_text(marker + "\\n", encoding="utf-8")
passed = subprocess.run(argv, cwd=real_lf, stdin=subprocess.DEVNULL,
                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
rejected = subprocess.run(argv, cwd=literal_backslash_n, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
assert passed.returncode == 0, passed.stderr
assert rejected.returncode != 0
PY
    printf 'REVIEW_PENDING\n' >"$state_path"
    printf '{"status":"pass"}\n'
    ;;
  "task review")
    if [ "$#" -eq 7 ] && [ "$3" = --run ] && [ "$4" = "$run_id" ] && \
      [ "$5" = --decision ] && [ "$7" = --json ]; then
      mkdir -p "$PWD/.marshal/runs/$run_id/decisions"
      cp "$6" "$PWD/.marshal/runs/$run_id/decisions/decision-001.json"
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
dist/
EOF
"/usr/bin/git" -C "$FIXTURE_ROOT" init -q -b main
"/usr/bin/git" -C "$FIXTURE_ROOT" config core.hooksPath "${FIXTURE_ROOT}/empty-hooks"
"/usr/bin/git" -C "$FIXTURE_ROOT" config user.email release-canary-test@example.invalid
"/usr/bin/git" -C "$FIXTURE_ROOT" config user.name "Release Canary Test"
"/usr/bin/git" -C "$FIXTURE_ROOT" add .gitignore go.mod scripts schemas/examples/happy-path
"/usr/bin/git" -C "$FIXTURE_ROOT" commit -q -m "fixture: release canary driver"
"/usr/bin/git" -C "$FIXTURE_ROOT" remote add origin https://github.com/chiga0/marshal-harness.git
EXPECTED_HEAD="$("/usr/bin/git" -C "$FIXTURE_ROOT" rev-parse HEAD)"
"/usr/bin/git" -C "$FIXTURE_ROOT" update-ref refs/remotes/origin/main "$EXPECTED_HEAD"
mkdir -p "${FIXTURE_ROOT}/dist"
for name in \
  marshal_1.0.0-rc1_darwin_amd64 marshal_1.0.0-rc1_darwin_arm64 \
  marshal_1.0.0-rc1_linux_amd64 marshal_1.0.0-rc1_linux_arm64; do
  cp "${FIXTURE_ROOT}/bin/marshal" "${FIXTURE_ROOT}/dist/${name}"
done
bash "${FIXTURE_ROOT}/scripts/release-contract.sh" create-manifest \
  "${FIXTURE_ROOT}/dist" v1.0.0-rc1 "$EXPECTED_HEAD" 2026-08-28T00:00:00Z go1.26.6
(
  cd "${FIXTURE_ROOT}/dist"
  : >SHA256SUMS
  for name in RELEASE-MANIFEST marshal_*; do
    printf '%s  %s\n' "$(sha256_file "$name")" "$name" >>SHA256SUMS
  done
)

run_driver() {
  MARSHAL_RELEASE_CANARY_TEST_MODE=1 \
  MARSHAL_RELEASE_CANARY_PI_BIN="$PI_BIN" \
  MARSHAL_RELEASE_CANARY_PI_NODE="$PI_NODE" \
  MARSHAL_RELEASE_CANARY_PI_NODE_SHA256="$PI_NODE_SHA256" \
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

fake_log_lines() {
  if [ -f "$FAKE_LOG" ]; then
    wc -l <"$FAKE_LOG" | tr -d ' '
  else
    printf '0\n'
  fi
}

LEGACY_RUN="rc1-legacy-rejected"
LEGACY_LOG_BEFORE="$(fake_log_lines)"
export MARSHAL_WORKER_EXECUTOR=legacy
expect_fail 'legacy executor 污染' run_driver run --run-id "$LEGACY_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION"
unset MARSHAL_WORKER_EXECUTOR
[ ! -e "${FIXTURE_ROOT}/.marshal/release-canary/${LEGACY_RUN}" ] || fail 'legacy 拒绝后创建了 canary 状态'
[ "$(fake_log_lines)" = "$LEGACY_LOG_BEFORE" ] || fail 'legacy 拒绝前调用了 Marshal'

CANDIDATE_BYTES_RUN="rc1-candidate-bytes-drift"
cp "${FIXTURE_ROOT}/bin/marshal" "${TMP_ROOT}/marshal.saved"
printf '# fixed-bin drift\n' >>"${FIXTURE_ROOT}/bin/marshal"
expect_fail '固定 Marshal 与 candidate bytes 不同' run_driver run --run-id "$CANDIDATE_BYTES_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION"
[ ! -e "${FIXTURE_ROOT}/.marshal/release-canary/${CANDIDATE_BYTES_RUN}" ] || fail 'candidate bytes 拒绝后创建了 canary 状态'
cp "${TMP_ROOT}/marshal.saved" "${FIXTURE_ROOT}/bin/marshal"
chmod 0755 "${FIXTURE_ROOT}/bin/marshal"

MAIN_RUN="rc1-main"
run_driver run --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = REVIEW_PENDING ] || fail 'run 子命令没有停在 REVIEW_PENDING'
run_driver status --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect REVIEW_PENDING >/dev/null
MAIN_DECISION="$(make_accept_decision "$MAIN_RUN")"
run_driver finalize --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$MAIN_DECISION" >/dev/null
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = ACCEPTED ] || fail 'finalize 没有到达 ACCEPTED'
run_driver status --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect ACCEPTED >/dev/null
FINALIZE_IMPORTS_BEFORE="$(grep -Fxc "task review --run $MAIN_RUN --decision $MAIN_DECISION --json" "$FAKE_LOG")"
run_driver finalize --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$MAIN_DECISION" >/dev/null
FINALIZE_IMPORTS_AFTER="$(grep -Fxc "task review --run $MAIN_RUN --decision $MAIN_DECISION --json" "$FAKE_LOG")"
[ "$FINALIZE_IMPORTS_BEFORE" = 1 ] && [ "$FINALIZE_IMPORTS_AFTER" = 1 ] || fail '第二次 finalize 重复导入了 Decision'
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = ACCEPTED ] || fail '第二次 finalize 改变了 ACCEPTED'
MAIN_DIFFERENT_DECISION="$(dirname "$MAIN_DECISION")/different-review-decision.json"
cp "$MAIN_DECISION" "$MAIN_DIFFERENT_DECISION"
/usr/bin/python3 -I -B - "$MAIN_DIFFERENT_DECISION" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
value["summary"] = "这不是 Core 已接纳的同一 Decision。"
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
expect_fail 'ACCEPTED 后替换 Decision' run_driver finalize --run-id "$MAIN_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$MAIN_DIFFERENT_DECISION"
[ "$(grep -Fxc "task review --run $MAIN_RUN --decision $MAIN_DECISION --json" "$FAKE_LOG")" = 1 ] || fail '替换 Decision 后重复导入了已接纳 Decision'
[ "$(cat "${FAKE_STATE}/${MAIN_RUN}.state")" = ACCEPTED ] || fail '替换 Decision 改变了 ACCEPTED'

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

NODE_DRIFT_RUN="rc1-node-drift"
run_driver run --run-id "$NODE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
NODE_DRIFT_DECISION="$(make_accept_decision "$NODE_DRIFT_RUN")"
cp "$PI_NODE" "${TMP_ROOT}/pi-node.saved"
printf '# drift\n' >>"$PI_NODE"
expect_fail 'Node runtime 漂移' run_driver finalize --run-id "$NODE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$NODE_DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${NODE_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail 'Node 漂移产生了状态副作用'
cp "${TMP_ROOT}/pi-node.saved" "$PI_NODE"
chmod 0755 "$PI_NODE"

STATE_DRIFT_RUN="rc1-state-drift"
run_driver run --run-id "$STATE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
printf 'ACCEPTED\n' >"${FAKE_STATE}/${STATE_DRIFT_RUN}.state"
expect_fail 'Run 状态漂移' run_driver status --run-id "$STATE_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --expect REVIEW_PENDING

REMOTE_REF_DRIFT_RUN="rc1-remote-ref-drift"
run_driver run --run-id "$REMOTE_REF_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" >/dev/null
REMOTE_REF_DRIFT_DECISION="$(make_accept_decision "$REMOTE_REF_DRIFT_RUN")"
REMOTE_REF_DRIFT_COMMIT="$(printf 'fixture remote ref drift\n' | "/usr/bin/git" -C "$FIXTURE_ROOT" commit-tree "${EXPECTED_HEAD}^{tree}" -p "$EXPECTED_HEAD")"
"/usr/bin/git" -C "$FIXTURE_ROOT" update-ref refs/remotes/origin/main "$REMOTE_REF_DRIFT_COMMIT"
expect_fail 'origin/main ref 漂移' run_driver finalize --run-id "$REMOTE_REF_DRIFT_RUN" --expected-head "$EXPECTED_HEAD" --expected-version "$VERSION" --decision "$REMOTE_REF_DRIFT_DECISION"
[ "$(cat "${FAKE_STATE}/${REMOTE_REF_DRIFT_RUN}.state")" = REVIEW_PENDING ] || fail 'origin/main 漂移产生了状态副作用'
"/usr/bin/git" -C "$FIXTURE_ROOT" update-ref refs/remotes/origin/main "$EXPECTED_HEAD"

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
