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
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
EOF

cat >"${MOCK_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
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
      mismatch|valid)
        if command -v sha256sum >/dev/null 2>&1; then
          digest="$(printf '#!/bin/sh\nprintf "marshal fixture\\n"\n' | sha256sum | awk '{print $1}')"
        else
          digest="$(printf '#!/bin/sh\nprintf "marshal fixture\\n"\n' | shasum -a 256 | awk '{print $1}')"
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
    printf '#!/bin/sh\nprintf "marshal fixture\\n"\n' >"$dest"
    chmod 0755 "$dest"
    ;;
esac
EOF
chmod 0755 "${MOCK_BIN}/uname" "${MOCK_BIN}/curl"

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
    bash "${ROOT}/scripts/install.sh" 2>&1
  )"
  [ -x "${case_dir}/install/marshal" ] || fail "${tag}/valid 未安装可执行文件"
  printf '%s\n' "$output" | grep -F 'sha256 校验通过' >/dev/null \
    || fail "${tag}/valid 未记录 checksum 成功"
}

for tag in v1.0.0 v1.0.0-rc1; do
  run_failure_case "$tag" missing '缺少或无法下载 SHA256SUMS'
  run_failure_case "$tag" mismatch 'sha256 校验失败'
done
run_success_case v1.0.0-rc1

printf '[install-test] PASS\n'
