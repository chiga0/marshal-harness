#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKFLOW="${ROOT}/.github/workflows/release.yml"
CI_WORKFLOW="${ROOT}/.github/workflows/ci.yml"
MAKEFILE="${ROOT}/Makefile"
CI_CONTRACT="${ROOT}/scripts/release-ci-contract.py"
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
  local workflow="$1" build="${TMP_ROOT}/build-job" publish="${TMP_ROOT}/publish-job"
  local type_gate_line extract_line
  awk '/^  build:/{copy=1} /^  publish:/{copy=0} copy' "$workflow" >"$build"
  awk '/^  publish:/{copy=1} copy' "$workflow" >"$publish"
  grep -F 'actions: read' "$build" >/dev/null || return 1
  grep -F 'contents: read' "$build" >/dev/null || return 1
  ! grep -F 'contents: write' "$build" >/dev/null || return 1
  ! grep -F 'gh release create' "$build" >/dev/null || return 1
  grep -F 'needs: build' "$publish" >/dev/null || return 1
  grep -F 'contents: write' "$publish" >/dev/null || return 1
  grep -F 'tag-object: ${{ steps.release.outputs.tag-object }}' "$build" >/dev/null || return 1
  grep -F 'EXPECTED_TAG_OBJECT: ${{ needs.build.outputs.tag-object }}' "$publish" >/dev/null || return 1
  grep -F 'test "$(git rev-parse --verify "refs/tags/${TAG_NAME}")" = "$EXPECTED_TAG_OBJECT"' "$publish" >/dev/null || return 1
  grep -F 'EXPECTED_PAYLOAD_SHA256' "$publish" >/dev/null || return 1
  grep -F 'sha256sum release-artifact/release-payload.tar' "$publish" >/dev/null || return 1
  grep -F 'tar -tf release-artifact/release-payload.tar' "$publish" >/dev/null || return 1
  grep -F 'diff -u expected-tar-files actual-tar-files' "$publish" >/dev/null || return 1
  grep -F 'len(members) != 7' "$publish" >/dev/null || return 1
  grep -F 'member.mode != expected_mode' "$publish" >/dev/null || return 1
  grep -F 'member.issym() or member.islnk() or member.linkname' "$publish" >/dev/null || return 1
  type_gate_line="$(grep -nF 'len(members) != 7' "$publish" | cut -d: -f1)"
  extract_line="$(grep -nF 'tar -xf release-artifact/release-payload.tar' "$publish" | cut -d: -f1)"
  [ -n "$type_gate_line" ] && [ -n "$extract_line" ] && [ "$type_gate_line" -lt "$extract_line" ] || return 1
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

make_contract_fixture() {
  local name="$1" workflow="$2" makefile="$3" root
  root="${TMP_ROOT}/contract-${name}"
  mkdir -p "${root}/.github/workflows" "${root}/scripts"
  cp "$workflow" "${root}/.github/workflows/ci.yml"
  cp "$makefile" "${root}/Makefile"
  cp "$CI_CONTRACT" "${root}/scripts/release-ci-contract.py"
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
  git -C "$root" add .github/workflows/ci.yml Makefile scripts
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
  || fail 'main/PR CI 未保持三个 job、Ubuntu-only release-check 与五 recipe 封闭合同'

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
sed '/tar -tf release-artifact\/release-payload.tar/d' "$WORKFLOW" >"${TMP_ROOT}/hostile-no-tar-list.yml"
if check_workflow "${TMP_ROOT}/hostile-no-tar-list.yml"; then
  fail 'publish 删除 exact tar list 时 workflow contract 应失败'
fi
sed '/test "$(git rev-parse --verify "refs\/tags\/${TAG_NAME}")" = "$EXPECTED_TAG_OBJECT"/d' \
  "$WORKFLOW" >"${TMP_ROOT}/hostile-no-tag-object-recheck.yml"
if check_workflow "${TMP_ROOT}/hostile-no-tag-object-recheck.yml"; then
  fail 'publish 删除 exact tag object recheck 时 workflow contract 应失败'
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
