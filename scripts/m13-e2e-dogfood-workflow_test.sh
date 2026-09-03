#!/usr/bin/env bash
# m13-e2e-dogfood workflow 的纯 shell 回归测试。它只执行输入解析与
# early-failure 指标收集片段，不构建 Marshal、不读取 secret、不启动 Worker。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
WORKFLOW="${SCRIPT_DIR}/../.github/workflows/m13-e2e-dogfood.yml"
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

printf '[m13-e2e-workflow-test] PASS\n'
