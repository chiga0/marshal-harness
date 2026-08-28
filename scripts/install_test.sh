#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
MOCK_BIN="${TMP_ROOT}/mock-bin"
FIXTURE_COMMIT="0123456789abcdef0123456789abcdef01234567"
mkdir -p "$MOCK_BIN"

fail() {
  printf '[install-test] FAIL: %s\n' "$*" >&2
  exit 1
}

cat >"${MOCK_BIN}/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf '%s\n' "${MOCK_KERNEL:-Darwin}" ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
EOF

cat >"${MOCK_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
write_asset() {
  if [ "$FIXTURE_MODE" = badexec ]; then
    printf '#!/bin/sh\nexit 23\n'
    return
  fi
  version="${FIXTURE_TAG#v}"
  profile='darwin-local-dogfood'
  if [ "$FIXTURE_MODE" = badversion ]; then
    version='9.9.9'
  fi
  if [ "$FIXTURE_MODE" = badprofile ]; then
    profile='unprofiled'
  fi
  commit="$FIXTURE_PEELED_COMMIT"
  if [ "$FIXTURE_MODE" = badbinarycommit ]; then
    commit='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  fi
  build_date='2026-08-28T00:00:00Z'
  [ "$FIXTURE_MODE" != badbuilddate ] || build_date='2026-08-29T00:00:00Z'
  go_version='go1.26.6'
  [ "$FIXTURE_MODE" != badgoversion ] || go_version='go1.26.7'
  [ "$FIXTURE_MODE" != replacedrelease ] || printf '# replacement asset set\n'
  cat <<PAYLOAD
#!/bin/sh
printf '%s\\n' '{"version":"${version}","commit":"${commit}","buildDate":"${build_date}","goVersion":"${go_version}","os":"darwin","arch":"arm64","selfProfile":"${profile}"}'
PAYLOAD
}
sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}
write_manifest() {
  version="${FIXTURE_TAG#v}"
  commit="$FIXTURE_PEELED_COMMIT"
  [ "$FIXTURE_MODE" != badmanifestcommit ] || commit='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  digest="$(write_asset | sha256_stream)"
  size="$(write_asset | wc -c | tr -d '[:space:]')"
  printf 'schemaVersion marshal.release-manifest.v1\n'
  printf 'repository https://github.com/%s.git\n' "$FIXTURE_REPO"
  printf 'tag %s\n' "$FIXTURE_TAG"
  printf 'sourceHead %s\n' "$commit"
  printf 'buildDate 2026-08-28T00:00:00Z\n'
  printf 'goVersion go1.26.6\n'
  printf 'buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=\n'
  for tuple in \
    "darwin amd64 darwin-local-dogfood" \
    "darwin arm64 darwin-local-dogfood" \
    "linux amd64 unprofiled" \
    "linux arm64 unprofiled"; do
    set -- $tuple
    current_digest="$digest"
    if [ "$FIXTURE_MODE" = badmanifestasset ] && [ "$1/$2" = darwin/arm64 ]; then
      current_digest='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
    fi
    printf 'asset %s %s marshal_%s_%s_%s %s %s %s\n' \
      "$current_digest" "$size" "$version" "$1" "$2" "$1" "$2" "$3"
  done
}
write_tag_message() {
  frozen_mode="$FIXTURE_MODE"
  [ "$frozen_mode" != replacedrelease ] || frozen_mode=valid
  manifest_digest="$(FIXTURE_MODE="$frozen_mode" write_manifest | sha256_stream)"
  candidate_digest="$(FIXTURE_MODE="$frozen_mode" write_asset | sha256_stream)"
  cat <<MESSAGE
Marshal ${FIXTURE_TAG} candidate

marshal-candidate-schema: v1
marshal-candidate-source-head: ${FIXTURE_PEELED_COMMIT}
marshal-candidate-manifest-sha256: ${manifest_digest}
marshal-candidate-darwin-arm64-sha256: ${candidate_digest}
MESSAGE
}
dest=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      [ "$#" -ge 2 ] || exit 2
      dest="$2"
      shift 2
      ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
[ -n "$dest" ] && [ -n "$url" ] || exit 2
write_tag_message >"$(dirname "$dest")/FIXTURE-TAG-MESSAGE"
case "$url" in
  */RELEASE-MANIFEST)
    [ "$FIXTURE_MODE" != missingmanifest ] || exit 22
    write_manifest >"$dest"
    ;;
  */SHA256SUMS)
    case "$FIXTURE_MODE" in
      missing) exit 22 ;;
      mismatch|valid|badexec|badversion|badprofile|badbinarycommit|badbuilddate|badgoversion|badmanifestcommit|badmanifestasset|badmanifestchecksum|missingmanifest|replacedrelease)
        digest="$(write_asset | sha256_stream)"
        manifest_digest="$(write_manifest | sha256_stream)"
        [ "$FIXTURE_MODE" != badmanifestchecksum ] || manifest_digest='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
        prefix="${FIXTURE_ASSET%_darwin_arm64}"
        : >"$dest"
        printf '%s  RELEASE-MANIFEST\n' "$manifest_digest" >>"$dest"
        for name in \
          "${prefix}_darwin_amd64" \
          "${prefix}_darwin_arm64" \
          "${prefix}_linux_amd64" \
          "${prefix}_linux_arm64"; do
          if [ "$FIXTURE_MODE" = mismatch ] && [ "$name" = "$FIXTURE_ASSET" ]; then
            printf '%064d  %s\n' 0 "$name" >>"$dest"
          else
            printf '%s  %s\n' "$digest" "$name" >>"$dest"
          fi
        done
        ;;
      *) exit 2 ;;
    esac
    ;;
  *)
    write_asset >"$dest"
    chmod 0755 "$dest"
    ;;
esac
EOF
chmod 0755 "${MOCK_BIN}/uname" "${MOCK_BIN}/curl"

cat >"${MOCK_BIN}/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = ls-remote ]; then
  tag="${FIXTURE_TAG:?}"
  printf '%040d\trefs/tags/%s\n' 1 "$tag"
  if [ "${FIXTURE_MODE:-}" != lightweighttag ]; then
    printf '%s\trefs/tags/%s^{}\n' "${FIXTURE_PEELED_COMMIT:?}" "$tag"
  fi
  exit 0
fi
if [[ " $* " == *" release-tag.git "* ]] || [[ " $* " == *"/release-tag.git "* ]]; then
  case " $* " in
    *" fetch "*) exit 0 ;;
    *" cat-file -t "*)
      [ "${FIXTURE_MODE:-}" != lightweighttag ] && printf 'tag\n' || printf 'commit\n'
      exit 0
      ;;
    *" rev-parse --verify refs/tags/"*"^{commit}"*) printf '%s\n' "${FIXTURE_PEELED_COMMIT:?}"; exit 0 ;;
    *" rev-parse --verify refs/tags/"*) printf '%040d\n' 1; exit 0 ;;
    *" for-each-ref "*)
      repo=''
      while [ "$#" -gt 0 ]; do
        if [ "$1" = -C ]; then repo="$2"; break; fi
        shift
      done
      [ -n "$repo" ] || exit 2
      cat "$(dirname "$repo")/FIXTURE-TAG-MESSAGE"
      exit 0
      ;;
  esac
fi
exec /usr/bin/git "$@"
EOF
chmod 0755 "${MOCK_BIN}/git"

cat >"${MOCK_BIN}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then
  printf 'go version go1.26.5 darwin/arm64\n'
  exit 0
fi
[ "${1:-}" = build ] || exit 2
shift
out=''
ldflags=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -ldflags) ldflags="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$out" ] || exit 2
version='dev'
commit='unknown'
profile='unprofiled'
output_os="${GOOS:-}"
output_arch="${GOARCH:-arm64}"
if [ -z "$output_os" ]; then
  case "${MOCK_KERNEL:-Darwin}" in
    Darwin) output_os='darwin' ;;
    Linux) output_os='linux' ;;
    *) exit 2 ;;
  esac
fi
for token in $ldflags; do
  case "$token" in
    *.version=*) version="${token#*=}" ;;
    *.commit=*) commit="${token#*=}" ;;
    *.selfProfile=*) profile="${token#*=}" ;;
  esac
done
printf 'GOOS=%s GOARCH=%s version=%s commit=%s selfProfile=%s\n' \
  "${GOOS:-}" "${GOARCH:-}" "$version" "$commit" "$profile" >>"${FAKE_GO_LOG:?}"
cat >"$out" <<PAYLOAD
#!/bin/sh
printf '%s\\n' '{"version":"${version}","commit":"${commit}","buildDate":"fixture-date","goVersion":"fixture-go","os":"${output_os}","arch":"${output_arch}","selfProfile":"${profile}"}'
PAYLOAD
chmod 0755 "$out"
EOF
chmod 0755 "${MOCK_BIN}/go"

run_failure_case() {
  local tag="$1" mode="$2" expected="$3" case_dir output status asset
  case_dir="${TMP_ROOT}/${tag}-${mode}"
  asset="marshal_${tag#v}_darwin_arm64"
  mkdir -p "${case_dir}/home" "${case_dir}/install"
  set +e
  output="$(
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="${case_dir}/install" \
    MARSHAL_REPO='fixture/repo' \
    MARSHAL_TAG="$tag" \
    FIXTURE_MODE="$mode" \
    FIXTURE_ASSET="$asset" \
    FIXTURE_TAG="$tag" \
    FIXTURE_REPO='fixture/repo' \
    FIXTURE_PEELED_COMMIT="$FIXTURE_COMMIT" \
    bash "${ROOT}/scripts/install.sh" 2>&1
  )"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail "${tag}/${mode} 应 fail closed"
  printf '%s\n' "$output" | grep -F "$expected" >/dev/null \
    || fail "${tag}/${mode} 未返回预期错误: ${expected}"
  [ ! -e "${case_dir}/install/marshal" ] \
    || fail "${tag}/${mode} 失败后仍安装了 marshal"
}

run_success_case() {
  local tag="$1" case_dir asset output
  case_dir="${TMP_ROOT}/${tag}-valid"
  asset="marshal_${tag#v}_darwin_arm64"
  mkdir -p "${case_dir}/home" "${case_dir}/install"
  output="$(
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="${case_dir}/install" \
    MARSHAL_REPO='fixture/repo' \
    MARSHAL_TAG="$tag" \
    FIXTURE_MODE=valid \
    FIXTURE_ASSET="$asset" \
    FIXTURE_TAG="$tag" \
    FIXTURE_REPO='fixture/repo' \
    FIXTURE_PEELED_COMMIT="$FIXTURE_COMMIT" \
    bash "${ROOT}/scripts/install.sh" 2>&1
  )"
  [ -x "${case_dir}/install/marshal" ] || fail "${tag}/valid 未安装可执行文件"
  printf '%s\n' "$output" | grep -F 'sha256 校验通过' >/dev/null \
    || fail "${tag}/valid 未记录 checksum 成功"
}

make_source_repo() {
  local repo="$1" tag="$2"
  mkdir -p "${repo}/cmd/marshal"
  printf 'module fixture.invalid/marshal\n\ngo 1.26.0\n' >"${repo}/go.mod"
  printf 'package main\nfunc main() {}\n' >"${repo}/cmd/marshal/main.go"
  git -C "$repo" init -q
  git -C "$repo" config core.hooksPath /dev/null
  git -C "$repo" config user.name 'Marshal Installer Test'
  git -C "$repo" config user.email 'installer-test@example.invalid'
  git -C "$repo" add go.mod cmd/marshal/main.go
  git -C "$repo" commit -qm 'fixture'
  git -C "$repo" tag "$tag"
}

run_source_success_case() {
  local kernel="$1" expected_profile="$2" case_dir repo install_dir output head log
  case_dir="${TMP_ROOT}/source-${kernel}"
  repo="${case_dir}/repo"
  install_dir="${case_dir}/install"
  log="${case_dir}/go.log"
  mkdir -p "$repo" "$install_dir"
  make_source_repo "$repo" v1.0.0-rc1
  head="$(git -C "$repo" rev-parse HEAD)"
  output="$({
    cd "$repo"
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MOCK_KERNEL="$kernel" \
    FAKE_GO_LOG="$log" \
    MARSHAL_INSTALL_DIR="$install_dir" \
    MARSHAL_TAG=v1.0.0-rc1 \
    MARSHAL_FORCE_SOURCE=1 \
    bash "${ROOT}/scripts/install.sh"
  } 2>&1)"
  [ -x "${install_dir}/marshal" ] || fail "源码 ${kernel} 未安装"
  grep -F "commit=${head}" "$log" >/dev/null || fail "源码构建未绑定 HEAD"
  grep -F "selfProfile=${expected_profile}" "$log" >/dev/null || fail "源码构建 profile 错误"
  printf '%s\n' "$output" | grep -F '安装后版本自检通过' >/dev/null \
    || fail "源码 ${kernel} 缺少安装后自检"
}

run_source_head_mismatch_case() {
  local case_dir repo install_dir output status
  case_dir="${TMP_ROOT}/source-mismatch"
  repo="${case_dir}/repo"
  install_dir="${case_dir}/install"
  mkdir -p "$repo" "$install_dir"
  make_source_repo "$repo" v1.0.0-rc1
  printf '// drift\n' >>"${repo}/cmd/marshal/main.go"
  git -C "$repo" add cmd/marshal/main.go
  git -C "$repo" commit -qm 'drift after tag'
  set +e
  output="$({
    cd "$repo"
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MOCK_KERNEL=Darwin \
    FAKE_GO_LOG="${case_dir}/go.log" \
    MARSHAL_INSTALL_DIR="$install_dir" \
    MARSHAL_TAG=v1.0.0-rc1 \
    MARSHAL_FORCE_SOURCE=1 \
    bash "${ROOT}/scripts/install.sh"
  } 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail '请求 tag 与 HEAD 漂移时应 fail closed'
  printf '%s\n' "$output" | grep -F '与请求 tag v1.0.0-rc1 指向' >/dev/null \
    || fail 'HEAD/tag 漂移未返回确定性原因'
  [ ! -e "${install_dir}/marshal" ] || fail 'HEAD/tag 漂移后仍安装了 marshal'
}

run_source_dirty_case() {
  local case_dir repo install_dir output status
  case_dir="${TMP_ROOT}/source-dirty"
  repo="${case_dir}/repo"
  install_dir="${case_dir}/install"
  mkdir -p "$repo" "$install_dir"
  make_source_repo "$repo" v1.0.0-rc1
  printf '// uncommitted drift\n' >>"${repo}/cmd/marshal/main.go"
  set +e
  output="$({
    cd "$repo"
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MOCK_KERNEL=Darwin \
    FAKE_GO_LOG="${case_dir}/go.log" \
    MARSHAL_INSTALL_DIR="$install_dir" \
    MARSHAL_TAG=v1.0.0-rc1 \
    MARSHAL_FORCE_SOURCE=1 \
    bash "${ROOT}/scripts/install.sh"
  } 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail 'dirty source checkout 应 fail closed'
  printf '%s\n' "$output" | grep -F '源码 checkout 存在未提交修改' >/dev/null \
    || fail 'dirty source checkout 未返回确定性原因'
  [ ! -e "${install_dir}/marshal" ] || fail 'dirty source checkout 仍安装了 marshal'
}

for tag in v1.0.0 v1.0.0-rc1; do
  run_failure_case "$tag" missing '缺少或无法下载 SHA256SUMS'
  run_failure_case "$tag" mismatch 'sha256 校验失败'
done
run_failure_case v1.0.0-rc1 badexec '无法通过 version --json 自检'
run_failure_case v1.0.0-rc1 badversion 'version=9.9.9，期望 1.0.0-rc1'
run_failure_case v1.0.0-rc1 badprofile 'selfProfile=unprofiled，期望 darwin-local-dogfood'
run_failure_case v1.0.0-rc1 badbinarycommit "commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa，期望 ${FIXTURE_COMMIT}"
run_failure_case v1.0.0-rc1 badbuilddate 'buildDate=2026-08-29T00:00:00Z，期望 2026-08-28T00:00:00Z'
run_failure_case v1.0.0-rc1 badgoversion 'goVersion=go1.26.7，期望 go1.26.6'
run_failure_case v1.0.0-rc1 badmanifestcommit 'RELEASE-MANIFEST 非 canonical、与 tag/peeled commit/checksum 不一致或资产集合不封闭'
run_failure_case v1.0.0-rc1 badmanifestasset 'RELEASE-MANIFEST 非 canonical、与 tag/peeled commit/checksum 不一致或资产集合不封闭'
run_failure_case v1.0.0-rc1 badmanifestchecksum 'RELEASE-MANIFEST sha256 校验失败'
run_failure_case v1.0.0-rc1 missingmanifest '缺少或无法下载 RELEASE-MANIFEST'
run_failure_case v1.0.0-rc1 lightweighttag '必须是唯一 annotated tag 且可解析唯一 peeled commit'
run_failure_case v1.0.0-rc1 replacedrelease 'RELEASE-MANIFEST 与 annotated tag 冻结摘要不一致'
run_success_case v1.0.0-rc1
run_source_success_case Darwin darwin-local-dogfood
run_source_success_case Linux unprofiled
run_source_head_mismatch_case
run_source_dirty_case

printf '[install-test] PASS\n'
