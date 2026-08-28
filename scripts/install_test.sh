#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
MOCK_BIN="${TMP_ROOT}/mock-bin"
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
  cat <<PAYLOAD
#!/bin/sh
printf '%s\\n' '{"version":"${version}","commit":"fixture-commit","buildDate":"fixture-date","goVersion":"fixture-go","os":"darwin","arch":"arm64","selfProfile":"${profile}"}'
PAYLOAD
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
  */SHA256SUMS)
    case "$FIXTURE_MODE" in
      missing) exit 22 ;;
      mismatch|valid|badexec|badversion|badprofile)
        if command -v sha256sum >/dev/null 2>&1; then
          digest="$(write_asset | sha256sum | awk '{print $1}')"
        else
          digest="$(write_asset | shasum -a 256 | awk '{print $1}')"
        fi
        prefix="${FIXTURE_ASSET%_darwin_arm64}"
        : >"$dest"
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
printf '%s\\n' '{"version":"${version}","commit":"${commit}","buildDate":"fixture-date","goVersion":"fixture-go","os":"${GOOS:-darwin}","arch":"${GOARCH:-arm64}","selfProfile":"${profile}"}'
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
run_success_case v1.0.0-rc1
run_source_success_case Darwin darwin-local-dogfood
run_source_success_case Linux unprofiled
run_source_head_mismatch_case
run_source_dirty_case

printf '[install-test] PASS\n'
