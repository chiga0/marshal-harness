#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
case "$(uname -s)" in
  Darwin) TEST_TMP_BASE=/private/tmp ;;
  *) TEST_TMP_BASE="${TMPDIR:-/tmp}" ;;
esac
TMP_ROOT="$(mktemp -d "${TEST_TMP_BASE%/}/marshal-install-test.XXXXXX")"
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
  printf 'repository https://github.com/chiga0/marshal-harness.git\n'
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
  [ "$frozen_mode" != extratagline ] || frozen_mode=valid
  [ "$frozen_mode" != taginteriorblank ] || frozen_mode=valid
  [ "$frozen_mode" != tagtrailingblank ] || frozen_mode=valid
  [ "$frozen_mode" != tagnul ] || frozen_mode=valid
  manifest_digest="$(FIXTURE_MODE="$frozen_mode" write_manifest | sha256_stream)"
  candidate_digest="$(FIXTURE_MODE="$frozen_mode" write_asset | sha256_stream)"
  printf 'Marshal %s candidate\n\n' "$FIXTURE_TAG"
  printf 'marshal-candidate-schema: v1\n'
  printf 'marshal-candidate-source-head: %s\n' "$FIXTURE_PEELED_COMMIT"
  printf 'marshal-candidate-manifest-sha256: %s\n' "$manifest_digest"
  [ "$FIXTURE_MODE" != taginteriorblank ] || printf '\n'
  [ "$FIXTURE_MODE" != tagnul ] || printf '\000'
  printf 'marshal-candidate-darwin-arm64-sha256: %s\n' "$candidate_digest"
  case "$FIXTURE_MODE" in
    extratagline) printf 'unexpected tag note\n' ;;
    tagtrailingblank) printf '\n' ;;
  esac
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
case "$url" in
  https://github.com/chiga0/marshal-harness/releases/download/*|https://github.com/chiga0/marshal-harness/releases/latest/download/*) ;;
  *) printf 'non-canonical release URL: %s\n' "$url" >&2; exit 91 ;;
esac
write_tag_message >"$(dirname "$dest")/FIXTURE-TAG-MESSAGE"
case "$url" in
  */RELEASE-MANIFEST)
    [ "$FIXTURE_MODE" != missingmanifest ] || exit 22
    write_manifest >"$dest"
    ;;
  */SHA256SUMS)
    case "$FIXTURE_MODE" in
      missing) exit 22 ;;
      mismatch|valid|badexec|badversion|badprofile|badbinarycommit|badbuilddate|badgoversion|badmanifestcommit|badmanifestasset|badmanifestchecksum|missingmanifest|replacedrelease|extratagline|taginteriorblank|tagtrailingblank|tagnul)
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
    chmod 0644 "$dest"
    if [ -n "${FIXTURE_CURL_MODE_LOG:-}" ]; then
      if mode="$(/usr/bin/stat -f '%Lp' "$dest" 2>/dev/null)"; then
        :
      else
        mode="$(/usr/bin/stat -c '%a' "$dest")"
      fi
      printf '%s\n' "$mode" >"$FIXTURE_CURL_MODE_LOG"
    fi
    ;;
esac
EOF
chmod 0755 "${MOCK_BIN}/uname" "${MOCK_BIN}/curl"

cat >"${MOCK_BIN}/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = ls-remote ]; then
  [ "${3:-}" = "https://github.com/chiga0/marshal-harness.git" ] || {
    printf 'non-canonical ls-remote URL: %s\n' "${3:-}" >&2
    exit 91
  }
  tag="${FIXTURE_TAG:?}"
  printf '%040d\trefs/tags/%s\n' 1 "$tag"
  if [ "${FIXTURE_MODE:-}" != lightweighttag ]; then
    printf '%s\trefs/tags/%s^{}\n' "${FIXTURE_PEELED_COMMIT:?}" "$tag"
  fi
  exit 0
fi
if [[ " $* " == *" release-tag.git "* ]] || [[ " $* " == *"/release-tag.git "* ]]; then
  case " $* " in
    *" fetch "*)
      [[ " $* " == *" https://github.com/chiga0/marshal-harness.git "* ]] || {
        printf 'non-canonical fetch URL\n' >&2
        exit 91
      }
      exit 0
      ;;
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
      printf '\036\n'
      exit 0
      ;;
  esac
fi
exec /usr/bin/git "$@"
EOF
chmod 0755 "${MOCK_BIN}/git"

cat >"${MOCK_BIN}/stat" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
path="${@: -1}"
if values="$(/usr/bin/stat -c '%u %a %h' "$path" 2>/dev/null)"; then
  :
else
  values="$(/usr/bin/stat -f '%u %p %l' "$path")"
  read -r raw_owner raw_mode raw_links <<<"$values"
  raw_mode="${raw_mode: -4}"
  raw_mode="${raw_mode#0}"
  values="${raw_owner} ${raw_mode} ${raw_links}"
fi
read -r owner mode links <<<"$values"
case "${FIXTURE_FS_MODE:-}" in
  nonowner)
    [ "$path" != "${FIXTURE_INSTALL_DIR:-}" ] || owner=$((owner + 1))
    ;;
  nonowner-stage)
    [ "$path" != "${FIXTURE_INSTALL_DIR:-}/.marshal-staging" ] || owner=$((owner + 1))
    ;;
  nonowner-target)
    [ "$path" != "${FIXTURE_INSTALL_DIR:-}/marshal" ] || owner=$((owner + 1))
    ;;
esac
if [ "${1:-}" = -f ]; then
  [ "${#mode}" -ge 4 ] || mode="0${mode}"
  if [ -d "$path" ]; then
    mode="4${mode}"
  else
    mode="10${mode}"
  fi
fi
printf '%s %s %s\n' "$owner" "$mode" "$links"
EOF
chmod 0755 "${MOCK_BIN}/stat"

cat >"${MOCK_BIN}/ln" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
dest="${@: -1}"
case "${FIXTURE_FS_MODE:-}" in
  race-stage)
    case "$dest" in */.marshal-staging/marshal) : >"$dest" ;; esac
    ;;
  race-install)
    case "$dest" in */.marshal.install) : >"$dest" ;; esac
    ;;
esac
exec /bin/ln "$@"
EOF
chmod 0755 "${MOCK_BIN}/ln"

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
    MARSHAL_TAG="$tag" \
    FIXTURE_MODE="$mode" \
    FIXTURE_ASSET="$asset" \
    FIXTURE_TAG="$tag" \
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
  local tag="$1" case_dir asset output installed_mode installed_links
  case_dir="${TMP_ROOT}/${tag}-valid"
  asset="marshal_${tag#v}_darwin_arm64"
  mkdir -p "${case_dir}/home" "${case_dir}/install"
  output="$(
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="${case_dir}/install" \
    MARSHAL_TAG="$tag" \
    FIXTURE_MODE=valid \
    FIXTURE_CURL_MODE_LOG="${case_dir}/curl-mode" \
    FIXTURE_ASSET="$asset" \
    FIXTURE_TAG="$tag" \
    FIXTURE_PEELED_COMMIT="$FIXTURE_COMMIT" \
    bash "${ROOT}/scripts/install.sh" 2>&1
  )"
  [ -x "${case_dir}/install/marshal" ] || fail "${tag}/valid 未安装可执行文件"
  [ "$(cat "${case_dir}/curl-mode")" = 644 ] \
    || fail "${tag}/valid 下载资产不是真实 curl 0644 fixture"
  if installed_mode="$(stat -f '%Lp' "${case_dir}/install/marshal" 2>/dev/null)"; then
    :
  else
    installed_mode="$(stat -c '%a' "${case_dir}/install/marshal")"
  fi
  [ "$installed_mode" = 755 ] || fail "${tag}/valid installer 未自行激活固定 executable"
  if installed_links="$(/usr/bin/stat -f '%l' "${case_dir}/install/marshal" 2>/dev/null)"; then
    :
  else
    installed_links="$(/usr/bin/stat -c '%h' "${case_dir}/install/marshal")"
  fi
  [ "$installed_links" = 1 ] || fail "${tag}/valid 最终 executable hardlink count 不是 1"
  [ ! -e "${case_dir}/install/.marshal-staging/marshal" ] \
    || fail "${tag}/valid staging executable 未清理"
  printf '%s\n' "$output" | grep -F 'sha256 校验通过' >/dev/null \
    || fail "${tag}/valid 未记录 checksum 成功"
}

run_layout_failure_case() {
  local mode="$1" expected="$2" case_dir install_dir output status asset
  case_dir="${TMP_ROOT}/layout-${mode}"
  install_dir="${case_dir}/install"
  asset='marshal_1.0.0-rc1_darwin_arm64'
  mkdir -p "${case_dir}/home" "$case_dir"
  case "$mode" in
    install-symlink)
      mkdir -m 0700 "${case_dir}/real-install"
      ln -s "${case_dir}/real-install" "$install_dir"
      ;;
    target-symlink)
      mkdir -m 0700 "$install_dir"
      : >"${case_dir}/other"
      ln -s "${case_dir}/other" "${install_dir}/marshal"
      ;;
    target-hardlink)
      mkdir -m 0700 "$install_dir"
      : >"${case_dir}/other"
      chmod 0755 "${case_dir}/other"
      ln "${case_dir}/other" "${install_dir}/marshal"
      ;;
    target-wide)
      mkdir -m 0700 "$install_dir"
      : >"${install_dir}/marshal"
      chmod 0777 "${install_dir}/marshal"
      ;;
    broad-install)
      mkdir -m 0777 "$install_dir"
      chmod 0777 "$install_dir"
      ;;
    staging-symlink)
      mkdir -m 0700 "$install_dir" "${case_dir}/other-stage"
      ln -s "${case_dir}/other-stage" "${install_dir}/.marshal-staging"
      ;;
    staging-mode)
      mkdir -m 0700 "$install_dir"
      mkdir -m 0755 "${install_dir}/.marshal-staging"
      ;;
    stale-stage)
      mkdir -m 0700 "$install_dir"
      mkdir -m 0700 "${install_dir}/.marshal-staging"
      : >"${install_dir}/.marshal-staging/marshal"
      ;;
    nonowner|race-stage|race-install)
      mkdir -m 0700 "$install_dir"
      ;;
    nonowner-stage)
      mkdir -m 0700 "$install_dir"
      mkdir -m 0700 "${install_dir}/.marshal-staging"
      ;;
    nonowner-target)
      mkdir -m 0700 "$install_dir"
      : >"${install_dir}/marshal"
      chmod 0755 "${install_dir}/marshal"
      ;;
    *) fail "未知 layout fixture: ${mode}" ;;
  esac
  set +e
  output="$(
    HOME="${case_dir}/home" \
    PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="$install_dir" \
    MARSHAL_TAG=v1.0.0-rc1 \
    FIXTURE_MODE=valid \
    FIXTURE_FS_MODE="$mode" \
    FIXTURE_INSTALL_DIR="$install_dir" \
    FIXTURE_ASSET="$asset" \
    FIXTURE_TAG=v1.0.0-rc1 \
    FIXTURE_PEELED_COMMIT="$FIXTURE_COMMIT" \
    bash "${ROOT}/scripts/install.sh" 2>&1
  )"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail "layout/${mode} 应 fail closed"
  printf '%s\n' "$output" | grep -F "$expected" >/dev/null \
    || fail "layout/${mode} 未返回预期错误 ${expected}: ${output}"
  case "$mode" in
    install-symlink) [ ! -e "${case_dir}/real-install/marshal" ] || fail "layout/${mode} 越过 symlink 安装" ;;
    race-stage) [ -e "${install_dir}/.marshal-staging/marshal" ] || fail 'race-stage 非本次对象被错误清理' ;;
    race-install) [ -e "${install_dir}/.marshal.install" ] || fail 'race-install 非本次对象被错误清理' ;;
    *) [ ! -e "${install_dir}/marshal" ] || [ "$mode" = target-symlink ] || [ "$mode" = target-hardlink ] \
      || [ "$mode" = target-wide ] || [ "$mode" = nonowner-target ] \
      || fail "layout/${mode} 失败后仍安装 marshal" ;;
  esac
}

run_repo_override_failure_case() {
  local case_dir output status
  case_dir="${TMP_ROOT}/repo-override"
  mkdir -p "${case_dir}/home" "${case_dir}/install"
  set +e
  output="$(HOME="${case_dir}/home" PATH="${MOCK_BIN}:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="${case_dir}/install" MARSHAL_REPO=attacker/example \
    bash "${ROOT}/scripts/install.sh" 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail 'MARSHAL_REPO authority override 应 fail closed'
  printf '%s\n' "$output" | grep -F 'MARSHAL_REPO authority override 已禁用' >/dev/null \
    || fail 'MARSHAL_REPO authority override 未返回确定性错误'
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
run_failure_case v1.0.0-rc1 extratagline 'annotated tag candidate message 必须是 exact 6-line closed 格式'
run_failure_case v1.0.0-rc1 taginteriorblank 'annotated tag candidate message 必须是 exact 6-line closed 格式'
run_failure_case v1.0.0-rc1 tagtrailingblank 'annotated tag candidate message 必须是 exact 6-line closed 格式'
run_failure_case v1.0.0-rc1 tagnul 'annotated tag candidate message 必须是 exact 6-line closed 格式'
run_repo_override_failure_case
run_layout_failure_case install-symlink '目录缺失、非目录或为符号链接'
run_layout_failure_case target-symlink '安装目标不得是符号链接'
run_layout_failure_case target-hardlink '文件 hardlink count 必须为 1'
run_layout_failure_case target-wide '文件存在不安全的 group/world 写权限'
run_layout_failure_case broad-install '路径段存在不安全的 group/world 写权限'
run_layout_failure_case staging-symlink '目录缺失、非目录或为符号链接'
run_layout_failure_case staging-mode '目录权限 755 不是要求的 700'
run_layout_failure_case stale-stage '固定安装暂存对象已存在，拒绝覆盖'
run_layout_failure_case nonowner '路径段 owner 非 root/当前用户'
run_layout_failure_case nonowner-stage '目录不归当前用户所有'
run_layout_failure_case nonowner-target '文件不归当前用户所有'
run_layout_failure_case race-stage '无法 no-clobber 激活固定 staging 对象'
run_layout_failure_case race-install '无法创建 no-clobber 安装对象'
run_success_case v1.0.0-rc1
run_source_success_case Darwin darwin-local-dogfood
run_source_success_case Linux unprofiled
run_source_head_mismatch_case
run_source_dirty_case

printf '[install-test] PASS\n'
