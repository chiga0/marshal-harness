#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKFLOW="${ROOT}/.github/workflows/release.yml"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
FAKE_GH="${TMP_ROOT}/gh"
HEAD_SHA=0123456789abcdef0123456789abcdef01234567

fail() { printf '[release-ci-gate-test] FAIL: %s\n' "$*" >&2; exit 1; }

cat >"$FAKE_GH" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = api ] && [ "$#" = 2 ] || exit 2
url="$2"
case "$url" in
  */actions/workflows/ci.yml/runs*)
    case "${FIXTURE_MODE:?}" in
      no-run) printf '{"workflow_runs":[]}' ;;
      wrong-head) printf '{"workflow_runs":[{"id":9,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}' ;;
      failed-run) printf '{"workflow_runs":[{"id":9,"head_sha":"%s","head_branch":"main","event":"push","status":"completed","conclusion":"failure"}]}' "$FIXTURE_HEAD" ;;
      *) printf '{"workflow_runs":[{"id":9,"head_sha":"%s","head_branch":"main","event":"push","status":"completed","conclusion":"success"}]}' "$FIXTURE_HEAD" ;;
    esac
    ;;
  */actions/runs/9/jobs*)
    third='{"name":"Secret scan","head_sha":"'"$FIXTURE_HEAD"'","status":"completed","conclusion":"success"}'
    case "${FIXTURE_MODE:?}" in
      failed-job) third='{"name":"Secret scan","head_sha":"'"$FIXTURE_HEAD"'","status":"completed","conclusion":"failure"}' ;;
      duplicate-job) third='{"name":"Quality (macos-latest)","head_sha":"'"$FIXTURE_HEAD"'","status":"completed","conclusion":"success"}' ;;
      wrong-job-head) third='{"name":"Secret scan","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","status":"completed","conclusion":"success"}' ;;
    esac
    printf '{"total_count":3,"jobs":[{"name":"Quality (ubuntu-latest)","head_sha":"%s","status":"completed","conclusion":"success"},{"name":"Quality (macos-latest)","head_sha":"%s","status":"completed","conclusion":"success"},%s]}' "$FIXTURE_HEAD" "$FIXTURE_HEAD" "$third"
    ;;
  *) exit 2 ;;
esac
EOF
chmod 0755 "$FAKE_GH"

expect_fail() {
  local mode="$1"
  if GH_BIN="$FAKE_GH" FIXTURE_MODE="$mode" FIXTURE_HEAD="$HEAD_SHA" \
    bash "${ROOT}/scripts/release-ci-gate.sh" fixture/repo "$HEAD_SHA" >/dev/null 2>&1; then
    fail "$mode 应 fail closed"
  fi
}

GH_BIN="$FAKE_GH" FIXTURE_MODE=valid FIXTURE_HEAD="$HEAD_SHA" \
  bash "${ROOT}/scripts/release-ci-gate.sh" fixture/repo "$HEAD_SHA" >/dev/null
for mode in no-run wrong-head failed-run failed-job duplicate-job wrong-job-head; do
  expect_fail "$mode"
done

grep -F 'actions: read' "$WORKFLOW" >/dev/null || fail 'release job 缺少 actions:read'
grep -F 'scripts/release-contract.sh build-date . "$release_commit"' "$WORKFLOW" >/dev/null \
  || fail 'release workflow 未使用 peeled commit 的 canonical buildDate'
grep -F 'scripts/release-ci-gate.sh "${{ github.repository }}" "$RELEASE_COMMIT"' "$WORKFLOW" >/dev/null \
  || fail 'release workflow 未执行 exact-head CI gate'
grep -F 'scripts/release-contract.sh verify-candidate-tag . dist "$TAG_NAME"' "$WORKFLOW" >/dev/null \
  || fail 'release workflow 未验证 candidate tag bytes'
grep -F 'bash scripts/release-canary_test.sh' "$WORKFLOW" >/dev/null \
  || fail 'release workflow 未回归 release canary contract'
if grep -F 'BUILD_DATE="$(date ' "$WORKFLOW" >/dev/null; then
  fail 'release workflow 仍使用 wall-clock buildDate'
fi

printf '[release-ci-gate-test] PASS\n'
