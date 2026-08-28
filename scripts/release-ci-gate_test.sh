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

check_workflow() {
  local workflow="$1" build="${TMP_ROOT}/build-job" publish="${TMP_ROOT}/publish-job"
  awk '/^  build:/{copy=1} /^  publish:/{copy=0} copy' "$workflow" >"$build"
  awk '/^  publish:/{copy=1} copy' "$workflow" >"$publish"
  grep -F 'actions: read' "$build" >/dev/null || return 1
  grep -F 'contents: read' "$build" >/dev/null || return 1
  ! grep -F 'contents: write' "$build" >/dev/null || return 1
  ! grep -F 'gh release create' "$build" >/dev/null || return 1
  grep -F 'needs: build' "$publish" >/dev/null || return 1
  grep -F 'contents: write' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_PAYLOAD_SHA256' "$publish" >/dev/null || return 1
  grep -F 'sha256sum release-artifact/release-payload.tar' "$publish" >/dev/null || return 1
  grep -F 'tar -tf release-artifact/release-payload.tar' "$publish" >/dev/null || return 1
  grep -F 'diff -u expected-tar-files actual-tar-files' "$publish" >/dev/null || return 1
  grep -F -- "--format='%(contents)%1e'" "$publish" >/dev/null || return 1
  grep -F 'END { if (NR != 7 || !seen_marker) exit 1 }' "$publish" >/dev/null || return 1
  grep -F 'gh release create "$TAG_NAME"' "$publish" >/dev/null || return 1
  [ "$(grep -Fc 'test "${{ github.repository }}" = "chiga0/marshal-harness"' "$workflow")" -eq 2 ] || return 1
  grep -F 'scripts/release-contract.sh build-date . "$release_commit"' "$workflow" >/dev/null || return 1
  grep -F 'scripts/release-ci-gate.sh "${{ github.repository }}" "$RELEASE_COMMIT"' "$workflow" >/dev/null || return 1
  grep -F 'scripts/release-contract.sh verify-candidate-tag . dist "$TAG_NAME"' "$workflow" >/dev/null || return 1
  grep -F 'bash scripts/release-canary_test.sh' "$workflow" >/dev/null || return 1
  ! grep -F 'BUILD_DATE="$(date ' "$workflow" >/dev/null || return 1
  ! grep -F 'artifact-digest' "$workflow" >/dev/null || return 1
}

check_workflow "$WORKFLOW" || fail 'release workflow 未保持 read-only build / exact payload / write-only publish 分权合同'

awk '
  /^  build:/ { build=1 }
  /^  publish:/ { build=0 }
  build && !changed && $0 == "      contents: read" { print "      contents: write"; changed=1; next }
  { print }
' "$WORKFLOW" >"${TMP_ROOT}/hostile-build-write.yml"
if check_workflow "${TMP_ROOT}/hostile-build-write.yml"; then
  fail 'build job 获得 contents:write 时 workflow contract 应失败'
fi
sed '/tar -tf release-artifact\/release-payload.tar/d' "$WORKFLOW" >"${TMP_ROOT}/hostile-no-tar-list.yml"
if check_workflow "${TMP_ROOT}/hostile-no-tar-list.yml"; then
  fail 'publish 删除 exact tar list 时 workflow contract 应失败'
fi

verify_archive_names() {
  local archive="$1" tag="$2"
  cat >"${TMP_ROOT}/expected-archive" <<EOF
dist/
dist/RELEASE-MANIFEST
dist/SHA256SUMS
dist/marshal_${tag#v}_darwin_amd64
dist/marshal_${tag#v}_darwin_arm64
dist/marshal_${tag#v}_linux_amd64
dist/marshal_${tag#v}_linux_arm64
EOF
  tar -tf "$archive" | LC_ALL=C sort >"${TMP_ROOT}/actual-archive"
  LC_ALL=C sort "${TMP_ROOT}/expected-archive" -o "${TMP_ROOT}/expected-archive"
  diff -u "${TMP_ROOT}/expected-archive" "${TMP_ROOT}/actual-archive" >/dev/null
}

PAYLOAD_ROOT="${TMP_ROOT}/payload"
mkdir -p "${PAYLOAD_ROOT}/dist"
for name in RELEASE-MANIFEST SHA256SUMS \
  marshal_1.0.0-rc1_darwin_amd64 marshal_1.0.0-rc1_darwin_arm64 \
  marshal_1.0.0-rc1_linux_amd64 marshal_1.0.0-rc1_linux_arm64; do
  : >"${PAYLOAD_ROOT}/dist/${name}"
done
tar -cf "${TMP_ROOT}/valid.tar" -C "$PAYLOAD_ROOT" dist
verify_archive_names "${TMP_ROOT}/valid.tar" v1.0.0-rc1 \
  || fail '合法 payload tar 未通过 exact list'
cp "${TMP_ROOT}/valid.tar" "${TMP_ROOT}/duplicate.tar"
tar -rf "${TMP_ROOT}/duplicate.tar" -C "$PAYLOAD_ROOT" dist/RELEASE-MANIFEST
if verify_archive_names "${TMP_ROOT}/duplicate.tar" v1.0.0-rc1; then
  fail '重复 payload member 应 fail closed'
fi
printf 'escape\n' >"${PAYLOAD_ROOT}/escape"
(cd "${PAYLOAD_ROOT}/dist" && tar -cf "${TMP_ROOT}/traversal.tar" ../escape)
if verify_archive_names "${TMP_ROOT}/traversal.tar" v1.0.0-rc1; then
  fail 'path traversal payload member 应 fail closed'
fi

printf '[release-ci-gate-test] PASS\n'
