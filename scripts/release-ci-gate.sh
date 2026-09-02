#!/usr/bin/env bash
# 在发布 tag 上传任何资产前，验证同一 peeled commit 的主分支 CI 五项全部成功。

set -euo pipefail

fail() {
  printf '[release-ci-gate] 错误: %s\n' "$*" >&2
  exit 1
}

[ "$#" = 2 ] || fail '用法: scripts/release-ci-gate.sh OWNER/REPO 40_HEX_COMMIT'
REPOSITORY="$1"
SOURCE_HEAD="$2"
[[ "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail 'repository 非法'
[[ "$SOURCE_HEAD" =~ ^[0-9a-f]{40}$ ]] || fail 'sourceHead 必须是 40 位小写 Git commit'

GH_COMMAND="${GH_BIN:-gh}"
PYTHON_COMMAND="${PYTHON_BIN:-python3}"
command -v "$GH_COMMAND" >/dev/null 2>&1 || fail '缺少 gh'
command -v "$PYTHON_COMMAND" >/dev/null 2>&1 || fail '缺少 python3'

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
RUNS_PATH="${TMP_ROOT}/runs.json"
JOBS_PATH="${TMP_ROOT}/jobs.json"

"$GH_COMMAND" api \
  "repos/${REPOSITORY}/actions/workflows/ci.yml/runs?head_sha=${SOURCE_HEAD}&event=push&per_page=100" \
  >"$RUNS_PATH" || fail '无法读取同 sourceHead 的 CI workflow runs'
[ "$(wc -c <"$RUNS_PATH" | tr -d '[:space:]')" -le 1048576 ] || fail 'CI workflow runs 响应超过 1 MiB'

RUN_ID="$($PYTHON_COMMAND -I -B - "$RUNS_PATH" "$SOURCE_HEAD" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
runs = value.get("workflow_runs")
assert isinstance(runs, list) and runs
matches = [run for run in runs
           if run.get("head_sha") == sys.argv[2]
           and run.get("head_branch") == "main"
           and run.get("event") == "push"]
assert matches
latest = max(matches, key=lambda run: int(run.get("id", 0)))
assert latest.get("status") == "completed"
assert latest.get("conclusion") == "success"
assert isinstance(latest.get("id"), int) and latest["id"] > 0
print(latest["id"])
PY
)" || fail '同 sourceHead 的最新 main push CI 未成功'

"$GH_COMMAND" api "repos/${REPOSITORY}/actions/runs/${RUN_ID}/jobs?per_page=100" \
  >"$JOBS_PATH" || fail '无法读取 CI jobs'
[ "$(wc -c <"$JOBS_PATH" | tr -d '[:space:]')" -le 1048576 ] || fail 'CI jobs 响应超过 1 MiB'

"$PYTHON_COMMAND" -I -B - "$JOBS_PATH" "$SOURCE_HEAD" <<'PY' \
  || fail 'CI 必须且只能包含成功的 Quality Linux/macOS、Linux candidate amd64/arm64 与 Secret scan'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
jobs = value.get("jobs")
assert isinstance(jobs, list)
assert value.get("total_count") == 5 and len(jobs) == 5
expected = {
    "Quality (ubuntu-latest)",
    "Quality (macos-latest)",
    "Linux candidate conformance (amd64)",
    "Linux candidate conformance (arm64)",
    "Secret scan",
}
assert {job.get("name") for job in jobs} == expected
for job in jobs:
    assert job.get("head_sha") == sys.argv[2]
    assert job.get("status") == "completed"
    assert job.get("conclusion") == "success"
PY

printf '[release-ci-gate] PASS: sourceHead=%s ciRunId=%s\n' "$SOURCE_HEAD" "$RUN_ID"
