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
extract_step 'Validate inputs and resolve identities' "$VALIDATE_RAW"
# canonical repository 约束由 GitHub expression 注入；本测试只执行其后的
# 纯输入解析，且单独静态断言该约束未被移除。
grep -Fq 'test "${{ github.repository }}" = "chiga0/marshal-harness"' "$VALIDATE_RAW" \
  || fail 'canonical repository 输入门禁被移除'
sed '1d' "$VALIDATE_RAW" >"$VALIDATE_SCRIPT"
extract_step 'Extract token and time metrics' "$METRICS_SCRIPT"
extract_step 'Pack dogfood evidence tarball' "$PACK_SCRIPT"
extract_step 'Write metrics summary' "$SUMMARY_SCRIPT"

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

printf '[m13-e2e-workflow-test] PASS\n'
