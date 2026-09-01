#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKFLOW="${ROOT}/.github/workflows/release.yml"
CI_WORKFLOW="${ROOT}/.github/workflows/ci.yml"
MAKEFILE="${ROOT}/Makefile"
CI_CONTRACT="${ROOT}/scripts/release-ci-contract.py"
RELEASE_WORKFLOW_DIGEST="${ROOT}/scripts/release-workflow.sha256"
TMP_ROOT="$(cd "$(mktemp -d)" && pwd -P)"
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
  local workflow="$1" guard="${TMP_ROOT}/publication-guard-job"
  local build="${TMP_ROOT}/build-job" publish="${TMP_ROOT}/publish-job"
  local guard_line build_line setup_go_line contract_test_line dist_line publish_line
  local type_gate_line extract_line
  python3 -I -B - "$workflow" "$RELEASE_WORKFLOW_DIGEST" <<'PY' || return 1
import hashlib
import pathlib
import re
import sys

workflow, contract = map(pathlib.Path, sys.argv[1:])
match = re.fullmatch(r"sha256:([0-9a-f]{64})\n", contract.read_text(encoding="utf-8"))
if match is None or hashlib.sha256(workflow.read_bytes()).hexdigest() != match.group(1):
    raise SystemExit(1)
PY
  # The exact-byte digest above is authoritative. These positive assertions
  # remain only as localized diagnostics for an explicitly reviewed update.
  awk '/^  publication_guard:/{copy=1} /^  build:/{copy=0} copy' "$workflow" >"$guard"
  awk '/^  build:/{copy=1} /^  publish:/{copy=0} copy' "$workflow" >"$build"
  awk '/^  publish:/{copy=1} copy' "$workflow" >"$publish"
  grep -F 'name: Block v1 publication pending ADR 0068 RC1 evidence' "$guard" >/dev/null || return 1
  grep -F 'runs-on: ubuntu-latest' "$guard" >/dev/null || return 1
  grep -F 'permissions: {}' "$guard" >/dev/null || return 1
  grep -F 'shell: /bin/bash --noprofile --norc -euo pipefail {0}' "$guard" >/dev/null || return 1
  grep -F 'case "$TAG_NAME" in' "$guard" >/dev/null || return 1
  grep -F '            v1.*)' "$guard" >/dev/null || return 1
  grep -F 'ADR 0068 live canary receipt, same-bytes release, and install verification slice' "$guard" >/dev/null || return 1
  grep -F 'must remain protected by GitHub-side immutable-tag and rerun controls.' "$guard" >/dev/null || return 1
  [ "$(grep -Fc 'exit 1' "$guard")" -eq 1 ] || return 1
  ! grep -E '(^|[[:space:]])(uses:|scripts/|make([[:space:]]|$)|go([[:space:]]|$)|gh([[:space:]]|$))' "$guard" >/dev/null || return 1
  grep -F 'needs: publication_guard' "$build" >/dev/null || return 1
  [ "$(grep -Fc 'needs: publication_guard' "$workflow")" -eq 1 ] || return 1
  guard_line="$(grep -nF '  publication_guard:' "$workflow" | cut -d: -f1)"
  build_line="$(grep -nF '  build:' "$workflow" | cut -d: -f1)"
  setup_go_line="$(grep -nF '      - name: Set up Go' "$workflow" | cut -d: -f1)"
  contract_test_line="$(grep -nF '      - name: Test release and installer contracts' "$workflow" | cut -d: -f1)"
  dist_line="$(grep -nF '          make dist \' "$workflow" | cut -d: -f1)"
  publish_line="$(grep -nF '  publish:' "$workflow" | cut -d: -f1)"
  [ -n "$guard_line" ] && [ -n "$build_line" ] && [ -n "$setup_go_line" ] \
    && [ -n "$contract_test_line" ] && [ -n "$dist_line" ] && [ -n "$publish_line" ] \
    && [ "$guard_line" -lt "$build_line" ] && [ "$build_line" -lt "$setup_go_line" ] \
    && [ "$build_line" -lt "$contract_test_line" ] && [ "$build_line" -lt "$dist_line" ] \
    && [ "$build_line" -lt "$publish_line" ] || return 1
  grep -F 'actions: read' "$build" >/dev/null || return 1
  grep -F 'contents: read' "$build" >/dev/null || return 1
  grep -F 'runs-on: macos-14' "$build" >/dev/null || return 1
  grep -F 'environment: release-candidate-build' "$build" >/dev/null || return 1
  ! grep -F 'contents: write' "$build" >/dev/null || return 1
  ! grep -F 'gh release create' "$build" >/dev/null || return 1
  grep -F 'needs: build' "$publish" >/dev/null || return 1
  grep -F 'actions: read' "$publish" >/dev/null || return 1
  grep -F 'contents: write' "$publish" >/dev/null || return 1
  grep -F 'tag-object: ${{ steps.release.outputs.tag-object }}' "$build" >/dev/null || return 1
  grep -F 'artifact-id: ${{ steps.upload.outputs.artifact-id }}' "$build" >/dev/null || return 1
  grep -F 'artifact-digest: ${{ steps.upload.outputs.artifact-digest }}' "$build" >/dev/null || return 1
  grep -F 'EXPECTED_TAG_OBJECT: ${{ needs.build.outputs.tag-object }}' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_ARTIFACT_ID: ${{ needs.build.outputs.artifact-id }}' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_ARTIFACT_DIGEST: ${{ needs.build.outputs.artifact-digest }}' "$publish" >/dev/null || return 1
  grep -F 'test "$(git rev-parse --verify "refs/tags/${TAG_NAME}")" = "$EXPECTED_TAG_OBJECT"' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_PAYLOAD_SHA256' "$publish" >/dev/null || return 1
  grep -F '[[ "$EXPECTED_ARTIFACT_ID" =~ ^[1-9][0-9]*$ ]]' "$publish" >/dev/null || return 1
  grep -F '[[ "$EXPECTED_ARTIFACT_DIGEST" =~ ^[0-9a-f]{64}$ ]]' "$publish" >/dev/null || return 1
  grep -F 'metadata_file="$(mktemp "${RUNNER_TEMP}/marshal-release-artifact.XXXXXX")"' "$publish" >/dev/null || return 1
  grep -F 'archive_file="$(mktemp "${RUNNER_TEMP}/marshal-release-archive.XXXXXX")"' "$publish" >/dev/null || return 1
  grep -F 'artifact_dir="$(mktemp -d "${RUNNER_TEMP}/marshal-release-extract.XXXXXX")"' "$publish" >/dev/null || return 1
  grep -F '"repos/${{ github.repository }}/actions/artifacts/${EXPECTED_ARTIFACT_ID}"' "$publish" >/dev/null || return 1
  grep -F '"repos/${{ github.repository }}/actions/artifacts/${EXPECTED_ARTIFACT_ID}/zip"' "$publish" >/dev/null || return 1
  grep -F '> "$metadata_file"' "$publish" >/dev/null || return 1
  grep -F '> "$archive_file"' "$publish" >/dev/null || return 1
  grep -F 'python3 -I -B scripts/release-artifact-metadata-check.py' "$publish" >/dev/null || return 1
  grep -F '"$metadata_file"' "$publish" >/dev/null || return 1
  grep -F '"$archive_file"' "$publish" >/dev/null || return 1
  grep -F '"$artifact_dir"' "$publish" >/dev/null || return 1
  grep -F '"$EXPECTED_ARTIFACT_ID"' "$publish" >/dev/null || return 1
  grep -F '"$EXPECTED_ARTIFACT_DIGEST"' "$publish" >/dev/null || return 1
  grep -F '"$EXPECTED_SOURCE_HEAD"' "$publish" >/dev/null || return 1
  grep -F '"${{ github.run_id }}"' "$publish" >/dev/null || return 1
  grep -F 'sha256sum "${artifact_dir}/release-payload.tar"' "$publish" >/dev/null || return 1
  grep -F 'tar -tf "${artifact_dir}/release-payload.tar"' "$publish" >/dev/null || return 1
  grep -F 'diff -u expected-tar-files actual-tar-files' "$publish" >/dev/null || return 1
  grep -F 'len(members) != 7' "$publish" >/dev/null || return 1
  grep -F 'member.mode != expected_mode' "$publish" >/dev/null || return 1
  grep -F 'member.issym() or member.islnk() or member.linkname' "$publish" >/dev/null || return 1
  type_gate_line="$(grep -nF 'len(members) != 7' "$publish" | cut -d: -f1)"
  extract_line="$(grep -nF 'tar -xf "${artifact_dir}/release-payload.tar"' "$publish" | cut -d: -f1)"
  [ -n "$type_gate_line" ] && [ -n "$extract_line" ] && [ "$type_gate_line" -lt "$extract_line" ] || return 1
  grep -F -- "--format='%(contents)%1e'" "$publish" >/dev/null || return 1
  grep -F 'END { if (NR != 7 || !seen_marker) exit 1 }' "$publish" >/dev/null || return 1
  grep -F 'gh release create "$TAG_NAME"' "$publish" >/dev/null || return 1
  [ "$(grep -Fc 'test "${{ github.repository }}" = "chiga0/marshal-harness"' "$workflow")" -eq 2 ] || return 1
  grep -F 'scripts/release-contract.sh build-date . "$release_commit"' "$workflow" >/dev/null || return 1
  grep -F 'scripts/release-ci-gate.sh "${{ github.repository }}" "$RELEASE_COMMIT"' "$workflow" >/dev/null || return 1
  grep -F 'scripts/release-contract.sh verify-candidate-tag . dist "$TAG_NAME"' "$workflow" >/dev/null || return 1
  grep -F 'bash scripts/release-canary_test.sh' "$workflow" >/dev/null || return 1
  grep -F 'payload_sha="$(shasum -a 256 release-payload.tar' "$build" >/dev/null || return 1
  [ "$(grep -Fc 'make dist' "$workflow")" -eq 1 ] || return 1
  grep -F 'make dist' "$build" >/dev/null || return 1
  ! grep -E '(^|[[:space:]])(go build|make dist)([[:space:]\\]|$)' "$publish" >/dev/null || return 1
  ! grep -F 'BUILD_DATE="$(date ' "$workflow" >/dev/null || return 1
}

# RC1 已从历史 tag-time 四平台重建切换为 ADR 0068 的 same-bytes carrier
# admission。重新定义当前权威 contract；上面的旧函数保留为历史测试上下文，
# 但不再决定 RC1 workflow 的合法性。
check_workflow() {
  local workflow="$1" admit="${TMP_ROOT}/rc1-admit-job" publish="${TMP_ROOT}/rc1-publish-job"
  python3 -I -B - "$workflow" "$RELEASE_WORKFLOW_DIGEST" <<'PY' || return 1
import hashlib
import pathlib
import re
import sys

workflow, contract = map(pathlib.Path, sys.argv[1:])
match = re.fullmatch(r"sha256:([0-9a-f]{64})\n", contract.read_text(encoding="utf-8"))
if match is None or hashlib.sha256(workflow.read_bytes()).hexdigest() != match.group(1):
    raise SystemExit(1)
PY
  awk '/^  admit:/{copy=1} /^  publish:/{copy=0} copy' "$workflow" >"$admit"
  awk '/^  publish:/{copy=1} copy' "$workflow" >"$publish"
  grep -F '  workflow_dispatch:' "$workflow" >/dev/null || return 1
  ! grep -F '    tags:' "$workflow" >/dev/null || return 1
  grep -F 'name: Admit exact same-bytes RC1 carrier' "$admit" >/dev/null || return 1
  grep -F 'runs-on: macos-14' "$admit" >/dev/null || return 1
  grep -F 'environment: release-candidate-build' "$admit" >/dev/null || return 1
  grep -F 'actions: read' "$admit" >/dev/null || return 1
  grep -F 'contents: read' "$admit" >/dev/null || return 1
  ! grep -F 'contents: write' "$admit" >/dev/null || return 1
  grep -F 'CARRIER_RUN_ID: ${{ inputs.carrier-run-id }}' "$admit" >/dev/null || return 1
  grep -F 'CARRIER_ARTIFACT_ID: ${{ inputs.carrier-artifact-id }}' "$admit" >/dev/null || return 1
  grep -F 'CARRIER_ARTIFACT_DIGEST: ${{ inputs.carrier-artifact-digest }}' "$admit" >/dev/null || return 1
  grep -F 'EXPECTED_RECEIPT_DIGEST: ${{ inputs.receipt-digest }}' "$admit" >/dev/null || return 1
  grep -F 'scripts/rc1-release-carrier-artifact.py' "$admit" >/dev/null || return 1
  grep -F 'scripts/rc1-carrier-check.py' "$admit" >/dev/null || return 1
  ! grep -F 'scripts/release-contract.sh verify-rc1-dist' "$admit" >/dev/null || return 1
  grep -F 'GO_BIN="$(go env GOROOT)/bin/go" bash scripts/release-contract.sh verify-candidate-tag' "$admit" >/dev/null || return 1
  grep -F 'scripts/release-ci-gate.sh "${{ github.repository }}" "$RELEASE_HEAD"' "$admit" >/dev/null || return 1
  grep -F 'without rebuilding candidate bytes' "$admit" >/dev/null || return 1
  grep -F 'name: release-payload-${{ steps.release.outputs.source-head }}' "$admit" >/dev/null || return 1
  grep -F 'needs: admit' "$publish" >/dev/null || return 1
  grep -F 'environment: release-publication' "$publish" >/dev/null || return 1
  grep -F 'contents: write' "$publish" >/dev/null || return 1
  grep -F 'scripts/release-artifact-metadata-check.py' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_WORKFLOW_HEAD: ${{ github.sha }}' "$publish" >/dev/null || return 1
  grep -F '"$EXPECTED_SOURCE_HEAD" "$EXPECTED_WORKFLOW_HEAD" "${{ github.run_id }}"' "$publish" >/dev/null || return 1
  grep -F 'scripts/rc1-release-payload-extract.py' "$publish" >/dev/null || return 1
  grep -F 'scripts/rc1-carrier-check.py' "$publish" >/dev/null || return 1
  ! grep -F 'rm -rf "$artifact_dir" "$release_dir"' "$publish" >/dev/null || return 1
  grep -F 'echo "RELEASE_DIR=$release_dir" >> "$GITHUB_ENV"' "$publish" >/dev/null || return 1
  grep -F 'scripts/release-contract.sh verify-candidate-tag' "$publish" >/dev/null || return 1
  grep -F 'gh release create "$TAG_NAME"' "$publish" >/dev/null || return 1
  grep -F -- '--prerelease' "$publish" >/dev/null || return 1
  grep -F '它不是 production、managed、notarized、hardened、server 或 Linux release。' "$publish" >/dev/null || return 1
  [ "$(grep -Fc 'test "${{ github.repository }}" = "chiga0/marshal-harness"' "$workflow")" -eq 2 ] || return 1
  ! grep -E '(^|[[:space:]])(go build|make([[:space:]]+-[^[:space:]]+[[:space:]]+)?dist)([[:space:]\\]|$)' "$workflow" >/dev/null || return 1
}

check_workflow "$WORKFLOW" || fail 'release workflow 未保持 exact carrier admission / read-only validator / write-only publisher 合同'

ARTIFACT_METADATA_CHECKER="${ROOT}/scripts/release-artifact-metadata-check.py"
ARTIFACT_METADATA="${TMP_ROOT}/artifact-metadata.json"
ARTIFACT_ARCHIVE="${TMP_ROOT}/artifact.zip"
ARTIFACT_INPUT="${TMP_ROOT}/artifact-input"
ARTIFACT_ID=123456
ARTIFACT_SOURCE_HEAD=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
ARTIFACT_WORKFLOW_HEAD=cccccccccccccccccccccccccccccccccccccccc
ARTIFACT_RUN_ID=987654

mkdir "$ARTIFACT_INPUT"
printf 'payload\n' >"${ARTIFACT_INPUT}/release-payload.tar"
printf 'fixture checksum\n' >"${ARTIFACT_INPUT}/RELEASE-PAYLOAD.sha256"
python3 -I -B - "$ARTIFACT_INPUT" "$ARTIFACT_ARCHIVE" <<'PY'
import pathlib
import sys
import zipfile

source, target = map(pathlib.Path, sys.argv[1:])
with zipfile.ZipFile(target, mode="w", compression=zipfile.ZIP_STORED) as archive:
    for name in ("RELEASE-PAYLOAD.sha256", "release-payload.tar"):
        archive.write(source / name, arcname=name)
PY
ARTIFACT_DIGEST="$(shasum -a 256 "$ARTIFACT_ARCHIVE" | awk '{print $1}')"

write_artifact_metadata() {
  local id="${1:-$ARTIFACT_ID}" digest="${2:-sha256:$ARTIFACT_DIGEST}"
  local name="${3:-release-payload-$ARTIFACT_SOURCE_HEAD}" expired="${4:-false}"
  local run_id="${5:-$ARTIFACT_RUN_ID}" head="${6:-$ARTIFACT_WORKFLOW_HEAD}"
  printf '{"id":%s,"name":"%s","expired":%s,"digest":"%s","workflow_run":{"id":%s,"head_sha":"%s"}}\n' \
    "$id" "$name" "$expired" "$digest" "$run_id" "$head" >"$ARTIFACT_METADATA"
}

check_artifact() {
  local archive="${1:-$ARTIFACT_ARCHIVE}" digest="${2:-$ARTIFACT_DIGEST}"
  local extract
  extract="$(mktemp -d "${TMP_ROOT}/artifact-extract.XXXXXX")"
  python3 -I -B "$ARTIFACT_METADATA_CHECKER" \
    "$ARTIFACT_METADATA" "$archive" "$extract" "$ARTIFACT_ID" "$digest" \
    "$ARTIFACT_SOURCE_HEAD" "$ARTIFACT_WORKFLOW_HEAD" "$ARTIFACT_RUN_ID" >/dev/null
}

expect_artifact_fail() {
  local description="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    fail "$description 应 fail closed"
  fi
}

write_artifact_metadata
VALID_EXTRACT="$(mktemp -d "${TMP_ROOT}/artifact-valid.XXXXXX")"
python3 -I -B "$ARTIFACT_METADATA_CHECKER" \
  "$ARTIFACT_METADATA" "$ARTIFACT_ARCHIVE" "$VALID_EXTRACT" \
  "$ARTIFACT_ID" "$ARTIFACT_DIGEST" "$ARTIFACT_SOURCE_HEAD" \
  "$ARTIFACT_WORKFLOW_HEAD" "$ARTIFACT_RUN_ID" >/dev/null \
  || fail '合法 immutable artifact archive 未通过 digest/run/candidate/workflow sourceHead 绑定'
cmp "${ARTIFACT_INPUT}/release-payload.tar" "${VALID_EXTRACT}/release-payload.tar" >/dev/null \
  || fail '合法 artifact archive 未受控解包'
expect_artifact_fail '调用者替换 expected artifact digest' \
  check_artifact "$ARTIFACT_ARCHIVE" \
  cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
cp "$ARTIFACT_ARCHIVE" "${TMP_ROOT}/artifact-bytes-substituted.zip"
printf 'substituted' >>"${TMP_ROOT}/artifact-bytes-substituted.zip"
expect_artifact_fail '实际下载 artifact archive bytes 与 upload digest 不一致' \
  check_artifact "${TMP_ROOT}/artifact-bytes-substituted.zip"
write_artifact_metadata "$ARTIFACT_ID" \
  sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
expect_artifact_fail 'GitHub metadata artifact digest 替换' check_artifact
write_artifact_metadata "$ARTIFACT_ID" "sha256:$ARTIFACT_DIGEST" substituted-name
expect_artifact_fail 'artifact name 替换' check_artifact
write_artifact_metadata 123457
expect_artifact_fail 'artifact id 替换' check_artifact
write_artifact_metadata "$ARTIFACT_ID" "sha256:$ARTIFACT_DIGEST" \
  "release-payload-$ARTIFACT_SOURCE_HEAD" true
expect_artifact_fail 'expired artifact replay' check_artifact
write_artifact_metadata "$ARTIFACT_ID" "sha256:$ARTIFACT_DIGEST" \
  "release-payload-$ARTIFACT_SOURCE_HEAD" false 987655
expect_artifact_fail 'cross-run artifact replay' check_artifact
write_artifact_metadata "$ARTIFACT_ID" "sha256:$ARTIFACT_DIGEST" \
  "release-payload-$ARTIFACT_SOURCE_HEAD" false "$ARTIFACT_RUN_ID" \
  dddddddddddddddddddddddddddddddddddddddd
expect_artifact_fail 'cross-workflow revision artifact replay' check_artifact
printf '{"id":%s,"id":%s,"name":"release-payload-%s","expired":false,"digest":"sha256:%s","workflow_run":{"id":%s,"head_sha":"%s"}}\n' \
  "$ARTIFACT_ID" "$ARTIFACT_ID" "$ARTIFACT_SOURCE_HEAD" "$ARTIFACT_DIGEST" \
  "$ARTIFACT_RUN_ID" "$ARTIFACT_WORKFLOW_HEAD" >"$ARTIFACT_METADATA"
expect_artifact_fail '重复 JSON member' check_artifact
write_artifact_metadata
printf 'trailing' >>"$ARTIFACT_METADATA"
expect_artifact_fail 'metadata 尾随 bytes' check_artifact
mv "$ARTIFACT_METADATA" "${ARTIFACT_METADATA}.real"
ln -s "${ARTIFACT_METADATA}.real" "$ARTIFACT_METADATA"
expect_artifact_fail 'metadata symlink' check_artifact
rm "$ARTIFACT_METADATA"
mv "${ARTIFACT_METADATA}.real" "$ARTIFACT_METADATA"
ln -s "$ARTIFACT_ARCHIVE" "${TMP_ROOT}/artifact-symlink.zip"
expect_artifact_fail 'artifact archive symlink' \
  check_artifact "${TMP_ROOT}/artifact-symlink.zip"

make_contract_fixture() {
  local name="$1" workflow="$2" makefile="$3" root
  root="${TMP_ROOT}/contract-${name}"
  mkdir -p "${root}/.github/workflows" "${root}/scripts" \
    "${root}/schemas/release/examples/valid" \
    "${root}/schemas/release/examples/invalid"
  cp "$workflow" "${root}/.github/workflows/ci.yml"
  cp "$WORKFLOW" "${root}/.github/workflows/release.yml"
  cp "$makefile" "${root}/Makefile"
  cp "$CI_CONTRACT" "${root}/scripts/release-ci-contract.py"
  cp "${ROOT}/scripts/release-artifact-metadata-check.py" \
    "${root}/scripts/release-artifact-metadata-check.py"
  cp "${ROOT}/scripts/rc1-carrier-check.py" \
    "${root}/scripts/rc1-carrier-check.py"
  cp "${ROOT}/scripts/rc1-carrier-check_test.py" \
    "${root}/scripts/rc1-carrier-check_test.py"
  cp "${ROOT}/scripts/rc1-release-carrier-artifact.py" \
    "${root}/scripts/rc1-release-carrier-artifact.py"
  cp "${ROOT}/scripts/rc1-release-carrier-artifact_test.py" \
    "${root}/scripts/rc1-release-carrier-artifact_test.py"
  cp "${ROOT}/scripts/rc1-release-payload-extract.py" \
    "${root}/scripts/rc1-release-payload-extract.py"
  cp "${ROOT}/schemas/release_schema_test.go" \
    "${root}/schemas/release_schema_test.go"
  cp "${ROOT}/schemas/release/rc1-canary-receipt.schema.json" \
    "${root}/schemas/release/rc1-canary-receipt.schema.json"
  cp "${ROOT}/schemas/release/examples/valid/rc1-canary-receipt.json" \
    "${root}/schemas/release/examples/valid/rc1-canary-receipt.json"
  cp "${ROOT}/schemas/release/examples/invalid/rc1-canary-receipt-missing-authority.json" \
    "${root}/schemas/release/examples/invalid/rc1-canary-receipt-missing-authority.json"
  cp "${ROOT}/scripts/release-rc1-binary-check.py" \
    "${root}/scripts/release-rc1-binary-check.py"
  cp "$RELEASE_WORKFLOW_DIGEST" "${root}/scripts/release-workflow.sha256"
  for fixed_test in \
    release-contract_test.sh \
    release-ci-gate_test.sh \
    dist-profile_test.sh \
    install_test.sh \
    release-canary_test.sh; do
    cp "${ROOT}/scripts/${fixed_test}" "${root}/scripts/${fixed_test}"
  done
  git init -q "$root"
  git -C "$root" config core.hooksPath /dev/null
  git -C "$root" config user.name 'Release Contract Test'
  git -C "$root" config user.email 'release-contract@example.invalid'
  git -C "$root" add .github/workflows Makefile scripts schemas
  git -C "$root" commit -qm fixture
  printf '%s\n' "$root"
}

check_main_ci_contract() {
  local root="$1" argument_root="${2:-$1}"
  /usr/bin/env -i LC_ALL=C PATH=/usr/bin:/bin \
    /usr/bin/python3 -I -B "${root}/scripts/release-ci-contract.py" \
    "$argument_root" >/dev/null
}

expect_main_ci_contract_fail() {
  local description="$1" workflow="$2" makefile="$3" root
  root="$(make_contract_fixture "hostile-$FIXTURE_SEQUENCE" "$workflow" "$makefile")"
  FIXTURE_SEQUENCE=$((FIXTURE_SEQUENCE + 1))
  if check_main_ci_contract "$root" 2>/dev/null; then
    fail "$description 应 fail closed"
  fi
}

FIXTURE_SEQUENCE=1
VALID_CONTRACT_ROOT="$(make_contract_fixture valid "$CI_WORKFLOW" "$MAKEFILE")"
BASH_ENV="${TMP_ROOT}/poison-bash-env" PATH=/nonexistent MAKEFLAGS='--silent --ignore-errors' \
  PYTHONHOME=/nonexistent PYTHONPATH=/nonexistent \
  check_main_ci_contract "$VALID_CONTRACT_ROOT" \
  || fail 'main/PR CI 未保持三个 job、Ubuntu-only release-check 与 RC1 carrier 封闭合同'

if false; then
awk '
  /^      - name: Run release contract gate$/ { skip=1; next }
  skip && /^      - name: Set up Go$/ { skip=0 }
  !skip { print }
' "$CI_WORKFLOW" \
  >"${TMP_ROOT}/hostile-ci-no-release-check.yml"
expect_main_ci_contract_fail '删除 release-check gate' \
  "${TMP_ROOT}/hostile-ci-no-release-check.yml" "$MAKEFILE"

sed 's/os: \[ubuntu-latest, macos-latest\]/os: [ubuntu-latest, macos-latest, windows-latest]/' \
  "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-windows.yml"
expect_main_ci_contract_fail '加入 Windows runtime' \
  "${TMP_ROOT}/hostile-ci-windows.yml" "$MAKEFILE"

awk '
  { print }
  /os: \[ubuntu-latest, macos-latest\]/ && !added { print "        go: [go1.26.6]"; added=1 }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-extra-dimension.yml"
expect_main_ci_contract_fail '加入额外 matrix dimension' \
  "${TMP_ROOT}/hostile-ci-extra-dimension.yml" "$MAKEFILE"

awk '
  { print }
  /os: \[ubuntu-latest, macos-latest\]/ && !added {
    print "        include:"
    print "          - os: windows-latest"
    added=1
  }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-matrix-include.yml"
expect_main_ci_contract_fail '加入 matrix include' \
  "${TMP_ROOT}/hostile-ci-matrix-include.yml" "$MAKEFILE"

sed 's|^              /usr/bin/python3 -I -B|              # /usr/bin/python3 -I -B|' \
  "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-commented-gate.yml"
expect_main_ci_contract_fail '注释 release-check gate' \
  "${TMP_ROOT}/hostile-ci-commented-gate.yml" "$MAKEFILE"

sed "s/matrix.os == 'ubuntu-latest'/matrix.os == 'macos-latest'/" \
  "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-moved-gate.yml"
expect_main_ci_contract_fail '把 gate 迁移到 macOS' \
  "${TMP_ROOT}/hostile-ci-moved-gate.yml" "$MAKEFILE"

awk '
  { print }
  /^        shell: \/bin\/bash --noprofile/ && !added {
    print "        continue-on-error: true"
    added=1
  }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-continue-on-error.yml"
expect_main_ci_contract_fail 'gate 设置 continue-on-error' \
  "${TMP_ROOT}/hostile-ci-continue-on-error.yml" "$MAKEFILE"

awk '
  /^  secrets:/ && !added {
    print "  diagnostic:"
    print "    name: Diagnostic"
    print "    runs-on: ubuntu-latest"
    print "    steps: []"
    added=1
  }
  { print }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-fourth-job.yml"
expect_main_ci_contract_fail '加入第四个 block-style job' \
  "${TMP_ROOT}/hostile-ci-fourth-job.yml" "$MAKEFILE"

awk '
  /^  secrets:/ && !added {
    print "  diagnostic: {name: Diagnostic, runs-on: ubuntu-latest, steps: []}"
    added=1
  }
  { print }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-flow-job.yml"
expect_main_ci_contract_fail '加入 flow-style 第四 job' \
  "${TMP_ROOT}/hostile-ci-flow-job.yml" "$MAKEFILE"

awk '
  { print }
  /uses: actions\/checkout@/ && !added {
    print ""
    print "      - name: Poison later release gate environment"
    print "        run: |"
    print "          echo BASH_ENV=/tmp/poison >> \"$GITHUB_ENV\""
    print "          echo PATH=/tmp/poison >> \"$GITHUB_ENV\""
    print "          echo MAKEFLAGS=--ignore-errors >> \"$GITHUB_ENV\""
    added=1
  }
' "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-environment-pollution.yml"
expect_main_ci_contract_fail '在 authority gate 前污染环境' \
  "${TMP_ROOT}/hostile-ci-environment-pollution.yml" "$MAKEFILE"

sed '/^\tbash scripts\/release-canary_test.sh$/d' "$MAKEFILE" \
  >"${TMP_ROOT}/hostile-make-missing-recipe"
expect_main_ci_contract_fail '删除 release-check recipe' \
  "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-missing-recipe"

sed 's/^\tbash scripts\/release-canary_test.sh$/\t# bash scripts\/release-canary_test.sh/' \
  "$MAKEFILE" >"${TMP_ROOT}/hostile-make-commented-recipe"
expect_main_ci_contract_fail '注释 release-check recipe' \
  "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-commented-recipe"

sed 's/^\tbash scripts\/release-canary_test.sh$/\tbash scripts\/release-canary_test.sh --moved/' \
  "$MAKEFILE" >"${TMP_ROOT}/hostile-make-moved-recipe"
expect_main_ci_contract_fail '迁移 release-check recipe' \
  "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-moved-recipe"

sed 's/^\.PHONY: \(.*\) release-check \(.*\)$/.PHONY: \1 \2/' \
  "$MAKEFILE" >"${TMP_ROOT}/hostile-make-not-phony"
expect_main_ci_contract_fail 'release-check 不再是 phony target' \
  "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-not-phony"

awk '
  { print }
  /^release-check:$/ && !added { print "release-check: bypass"; added=1 }
' "$MAKEFILE" >"${TMP_ROOT}/hostile-make-second-rule"
expect_main_ci_contract_fail '加入第二个 release-check rule' \
  "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-second-rule"

for directive in \
  '.IGNORE: release-check' \
  '.ONESHELL:' \
  'SHELL := /usr/bin/true' \
  '.SHELLFLAGS := -c' \
  'include scripts/override-release.mk'; do
  printf '\n%s\n' "$directive" >"${TMP_ROOT}/hostile-make-directive"
  cat "$MAKEFILE" >>"${TMP_ROOT}/hostile-make-directive"
  expect_main_ci_contract_fail "Make directive ${directive}" \
    "$CI_WORKFLOW" "${TMP_ROOT}/hostile-make-directive"
done

printf '\357\273\277' >"${TMP_ROOT}/hostile-ci-bom.yml"
cat "$CI_WORKFLOW" >>"${TMP_ROOT}/hostile-ci-bom.yml"
expect_main_ci_contract_fail 'workflow UTF-8 BOM' \
  "${TMP_ROOT}/hostile-ci-bom.yml" "$MAKEFILE"

awk 'NR == 1 { printf "%s\r\n", $0; next } { print }' \
  "$CI_WORKFLOW" >"${TMP_ROOT}/hostile-ci-cr.yml"
expect_main_ci_contract_fail 'workflow CR byte' \
  "${TMP_ROOT}/hostile-ci-cr.yml" "$MAKEFILE"

cp "$CI_WORKFLOW" "${TMP_ROOT}/hostile-ci-nul.yml"
printf '\0' >>"${TMP_ROOT}/hostile-ci-nul.yml"
expect_main_ci_contract_fail 'workflow NUL byte' \
  "${TMP_ROOT}/hostile-ci-nul.yml" "$MAKEFILE"

SYMLINK_CONTRACT_ROOT="$(make_contract_fixture symlink "$CI_WORKFLOW" "$MAKEFILE")"
mv "${SYMLINK_CONTRACT_ROOT}/.github/workflows/ci.yml" \
  "${SYMLINK_CONTRACT_ROOT}/.github/workflows/ci.real.yml"
ln -s ci.real.yml "${SYMLINK_CONTRACT_ROOT}/.github/workflows/ci.yml"
git -C "$SYMLINK_CONTRACT_ROOT" add .github/workflows/ci.yml .github/workflows/ci.real.yml
git -C "$SYMLINK_CONTRACT_ROOT" commit -qm symlink
if check_main_ci_contract "$SYMLINK_CONTRACT_ROOT" 2>/dev/null; then
  fail 'fixed workflow symlink 应 fail closed'
fi

MODE_CONTRACT_ROOT="$(make_contract_fixture mode "$CI_WORKFLOW" "$MAKEFILE")"
chmod 0755 "${MODE_CONTRACT_ROOT}/Makefile"
git -C "$MODE_CONTRACT_ROOT" add Makefile
git -C "$MODE_CONTRACT_ROOT" commit -qm mode
if check_main_ci_contract "$MODE_CONTRACT_ROOT" 2>/dev/null; then
  fail 'fixed path Git tree executable mode 应 fail closed'
fi

DIRTY_CONTRACT_ROOT="$(make_contract_fixture dirty "$CI_WORKFLOW" "$MAKEFILE")"
printf '\n# dirty bytes\n' >>"${DIRTY_CONTRACT_ROOT}/.github/workflows/ci.yml"
if check_main_ci_contract "$DIRTY_CONTRACT_ROOT" 2>/dev/null; then
  fail 'fixed path bytes 与 HEAD tree blob 漂移应 fail closed'
fi

if check_main_ci_contract "$VALID_CONTRACT_ROOT" . 2>/dev/null; then
  fail 'relative repository root 应 fail closed'
fi
if check_main_ci_contract "$VALID_CONTRACT_ROOT" "${VALID_CONTRACT_ROOT}/scripts" 2>/dev/null; then
  fail '非 repository root 的绝对 path 应 fail closed'
fi
ln -s "$VALID_CONTRACT_ROOT" "${TMP_ROOT}/contract-root-symlink"
if /usr/bin/python3 -I -B \
  "${TMP_ROOT}/contract-root-symlink/scripts/release-ci-contract.py" \
  "${TMP_ROOT}/contract-root-symlink" >/dev/null 2>&1; then
  fail 'symlink repository root 应 fail closed'
fi

awk '
  /^  build:/ { build=1 }
  /^  publish:/ { build=0 }
  build && !changed && $0 == "      contents: read" { print "      contents: write"; changed=1; next }
  { print }
' "$WORKFLOW" >"${TMP_ROOT}/hostile-build-write.yml"
if check_workflow "${TMP_ROOT}/hostile-build-write.yml"; then
  fail 'build job 获得 contents:write 时 workflow contract 应失败'
fi
sed '/^  publication_guard:/,/^  build:/ { /^  build:/!d; }' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-no-publication-guard.yml"
if check_workflow "${TMP_ROOT}/hostile-no-publication-guard.yml"; then
  fail '删除 v1 publication guard 时 workflow contract 应失败'
fi
sed 's/            v1\.\*)/            v1.0.0-rc1)/' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-rc1-only-publication-guard.yml"
if check_workflow "${TMP_ROOT}/hostile-rc1-only-publication-guard.yml"; then
  fail '把 publication guard 收窄为单个 rc1 tag 时 workflow contract 应失败'
fi
sed '/    needs: publication_guard/d' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-unordered-publication-guard.yml"
if check_workflow "${TMP_ROOT}/hostile-unordered-publication-guard.yml"; then
  fail 'build 未依赖 publication guard 时 workflow contract 应失败'
fi
sed '/tar -tf "${artifact_dir}\/release-payload.tar"/d' "$WORKFLOW" >"${TMP_ROOT}/hostile-no-tar-list.yml"
if check_workflow "${TMP_ROOT}/hostile-no-tar-list.yml"; then
  fail 'publish 删除 exact tar list 时 workflow contract 应失败'
fi
sed '/test "$(git rev-parse --verify "refs\/tags\/${TAG_NAME}")" = "$EXPECTED_TAG_OBJECT"/d' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-no-tag-object-recheck.yml"
if check_workflow "${TMP_ROOT}/hostile-no-tag-object-recheck.yml"; then
  fail 'publish 删除 exact tag object recheck 时 workflow contract 应失败'
fi
sed 's/runs-on: macos-14/runs-on: ubuntu-latest/' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-linux-candidate-build.yml"
if check_workflow "${TMP_ROOT}/hostile-linux-candidate-build.yml"; then
  fail 'Linux workflow 重建 Darwin candidate 应 fail closed'
fi
sed 's/EXPECTED_ARTIFACT_DIGEST: \${{ needs.build.outputs.artifact-digest }}/EXPECTED_ARTIFACT_DIGEST: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-artifact-digest-substitution.yml"
if check_workflow "${TMP_ROOT}/hostile-artifact-digest-substitution.yml"; then
  fail 'publish 替换 upload artifact digest 绑定应 fail closed'
fi
sed '/python3 -I -B scripts\/release-artifact-metadata-check.py/d' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-no-observed-artifact-digest.yml"
if check_workflow "${TMP_ROOT}/hostile-no-observed-artifact-digest.yml"; then
  fail 'publish 删除 GitHub artifact observed digest 对账应 fail closed'
fi
awk '
  { print }
  /^  publish:/ && !added {
    print "    # hostile rebuild"
    print "    run: make dist"
    added=1
  }
' "$WORKFLOW" >"${TMP_ROOT}/hostile-publish-rebuild.yml"
if check_workflow "${TMP_ROOT}/hostile-publish-rebuild.yml"; then
  fail 'publish 重新构建 candidate 应 fail closed'
fi
awk '
  /^  publish:/ { publish=1 }
  publish && /^    steps:/ && !added {
    print
    print "      - name: Hostile silent rebuild"
    print "        run: make -s dist"
    print "      # original marker retained: make dist"
    added=1
    next
  }
  { print }
' "$WORKFLOW" >"${TMP_ROOT}/hostile-publish-silent-rebuild-with-marker.yml"
if check_workflow "${TMP_ROOT}/hostile-publish-silent-rebuild-with-marker.yml"; then
  fail 'publish 新增 make -s dist 并保留原 marker 应被 exact contract 拒绝'
fi
awk '
  $0 == "      EXPECTED_ARTIFACT_DIGEST: ${{ needs.build.outputs.artifact-digest }}" {
    print "      # EXPECTED_ARTIFACT_DIGEST: ${{ needs.build.outputs.artifact-digest }}"
    print "      EXPECTED_ARTIFACT_DIGEST: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    next
  }
  { print }
' "$WORKFLOW" >"${TMP_ROOT}/hostile-input-digest-substitution-with-comment.yml"
if check_workflow "${TMP_ROOT}/hostile-input-digest-substitution-with-comment.yml"; then
  fail '替换 input digest 并用注释保留原行应被 exact contract 拒绝'
fi
fi

sed '/scripts\/rc1-release-carrier-artifact.py/d' "$WORKFLOW" >"${TMP_ROOT}/hostile-no-carrier-binding.yml"
if check_workflow "${TMP_ROOT}/hostile-no-carrier-binding.yml"; then
  fail '删除 carrier workflow/artifact 绑定应 fail closed'
fi
sed 's/contents: read/contents: write/' "$WORKFLOW" >"${TMP_ROOT}/hostile-admit-write.yml"
if check_workflow "${TMP_ROOT}/hostile-admit-write.yml"; then
  fail 'carrier admission job 获得写权限应 fail closed'
fi
awk '/^  publish:/ && !added { print; print "    # hostile rebuild\n    run: make dist"; added=1; next } { print }' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-publish-rebuild.yml"
if check_workflow "${TMP_ROOT}/hostile-publish-rebuild.yml"; then
  fail 'publisher 重新构建 candidate 应 fail closed'
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

verify_archive_contract() {
  python3 -I -B - "$1" "$2" <<'PY'
import sys
import tarfile

archive, tag = sys.argv[1:]
version = tag.removeprefix("v")
expected = {
    "dist": ("directory", 0o755),
    "dist/RELEASE-MANIFEST": ("regular", 0o644),
    "dist/SHA256SUMS": ("regular", 0o644),
    f"dist/marshal_{version}_darwin_amd64": ("regular", 0o755),
    f"dist/marshal_{version}_darwin_arm64": ("regular", 0o755),
    f"dist/marshal_{version}_linux_amd64": ("regular", 0o755),
    f"dist/marshal_{version}_linux_arm64": ("regular", 0o755),
}
with tarfile.open(archive, mode="r:") as payload:
    members = payload.getmembers()
if len(members) != 7 or {member.name for member in members} != set(expected):
    raise SystemExit(1)
for member in members:
    expected_type, expected_mode = expected[member.name]
    actual_type = "directory" if member.isdir() else "regular" if member.isfile() else "other"
    if actual_type != expected_type or member.mode != expected_mode:
        raise SystemExit(1)
    if expected_type == "regular" and (member.issym() or member.islnk() or member.linkname):
        raise SystemExit(1)
PY
}

verify_tag_identity() {
  local repository="$1" tag="$2" expected_head="$3" expected_object="$4"
  [ "$(git -C "$repository" cat-file -t "refs/tags/${tag}")" = tag ] &&
    [ "$(git -C "$repository" rev-parse --verify "refs/tags/${tag}")" = "$expected_object" ] &&
    [ "$(git -C "$repository" rev-parse --verify "refs/tags/${tag}^{commit}")" = "$expected_head" ]
}

PAYLOAD_ROOT="${TMP_ROOT}/payload"
mkdir -p "${PAYLOAD_ROOT}/dist"
for name in RELEASE-MANIFEST SHA256SUMS \
  marshal_1.0.0-rc1_darwin_amd64 marshal_1.0.0-rc1_darwin_arm64 \
  marshal_1.0.0-rc1_linux_amd64 marshal_1.0.0-rc1_linux_arm64; do
  : >"${PAYLOAD_ROOT}/dist/${name}"
done
chmod 0644 "${PAYLOAD_ROOT}/dist/RELEASE-MANIFEST" "${PAYLOAD_ROOT}/dist/SHA256SUMS"
chmod 0755 "${PAYLOAD_ROOT}"/dist/marshal_*
COPYFILE_DISABLE=1 tar -cf "${TMP_ROOT}/valid.tar" -C "$PAYLOAD_ROOT" dist
verify_archive_names "${TMP_ROOT}/valid.tar" v1.0.0-rc1 \
  || fail '合法 payload tar 未通过 exact list'
verify_archive_contract "${TMP_ROOT}/valid.tar" v1.0.0-rc1 \
  || fail '合法 payload tar 未通过 type/mode contract'
cp "${TMP_ROOT}/valid.tar" "${TMP_ROOT}/duplicate.tar"
COPYFILE_DISABLE=1 tar -rf "${TMP_ROOT}/duplicate.tar" -C "$PAYLOAD_ROOT" dist/RELEASE-MANIFEST
if verify_archive_names "${TMP_ROOT}/duplicate.tar" v1.0.0-rc1; then
  fail '重复 payload member 应 fail closed'
fi
printf 'escape\n' >"${PAYLOAD_ROOT}/escape"
(cd "${PAYLOAD_ROOT}/dist" && COPYFILE_DISABLE=1 tar -cf "${TMP_ROOT}/traversal.tar" ../escape)
if verify_archive_names "${TMP_ROOT}/traversal.tar" v1.0.0-rc1; then
  fail 'path traversal payload member 应 fail closed'
fi

SYMLINK_ROOT="${TMP_ROOT}/symlink-payload"
cp -R "$PAYLOAD_ROOT" "$SYMLINK_ROOT"
rm "${SYMLINK_ROOT}/dist/SHA256SUMS"
ln -s RELEASE-MANIFEST "${SYMLINK_ROOT}/dist/SHA256SUMS"
COPYFILE_DISABLE=1 tar -cf "${TMP_ROOT}/symlink.tar" -C "$SYMLINK_ROOT" dist
if verify_archive_contract "${TMP_ROOT}/symlink.tar" v1.0.0-rc1; then
  fail 'symlink payload member 应 fail closed'
fi

HARDLINK_ROOT="${TMP_ROOT}/hardlink-payload"
cp -R "$PAYLOAD_ROOT" "$HARDLINK_ROOT"
rm "${HARDLINK_ROOT}/dist/SHA256SUMS"
ln "${HARDLINK_ROOT}/dist/RELEASE-MANIFEST" "${HARDLINK_ROOT}/dist/SHA256SUMS"
COPYFILE_DISABLE=1 tar -cf "${TMP_ROOT}/hardlink.tar" -C "$HARDLINK_ROOT" dist
if verify_archive_contract "${TMP_ROOT}/hardlink.tar" v1.0.0-rc1; then
  fail 'hardlink payload member 应 fail closed'
fi

chmod 0777 "${PAYLOAD_ROOT}/dist/marshal_1.0.0-rc1_linux_arm64"
COPYFILE_DISABLE=1 tar -cf "${TMP_ROOT}/wide-mode.tar" -C "$PAYLOAD_ROOT" dist
if verify_archive_contract "${TMP_ROOT}/wide-mode.tar" v1.0.0-rc1; then
  fail 'wide-mode payload member 应 fail closed'
fi

TAG_REPO="${TMP_ROOT}/tag-repo"
git init -q "$TAG_REPO"
git -C "$TAG_REPO" config core.hooksPath /dev/null
git -C "$TAG_REPO" config user.name 'Release Gate Test'
git -C "$TAG_REPO" config user.email 'release-gate@example.invalid'
: >"${TAG_REPO}/fixture"
git -C "$TAG_REPO" add fixture
git -C "$TAG_REPO" commit -qm fixture
TAG_HEAD="$(git -C "$TAG_REPO" rev-parse HEAD)"
git -C "$TAG_REPO" tag -am first v1.0.0-rc1
TAG_OBJECT="$(git -C "$TAG_REPO" rev-parse refs/tags/v1.0.0-rc1)"
verify_tag_identity "$TAG_REPO" v1.0.0-rc1 "$TAG_HEAD" "$TAG_OBJECT" \
  || fail '合法 annotated tag identity 未通过'
git -C "$TAG_REPO" tag -f -am retarget v1.0.0-rc1 >/dev/null
if verify_tag_identity "$TAG_REPO" v1.0.0-rc1 "$TAG_HEAD" "$TAG_OBJECT"; then
  fail '同 commit 的 tag object retarget 应 fail closed'
fi

printf '[release-ci-gate-test] PASS\n'
