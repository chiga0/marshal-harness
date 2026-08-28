#!/usr/bin/env bash
# Marshal 一行安装脚本（README「安装」、docs/development.md「安装」小节）。
#
# 策略：
#   1. 存在 v* tag 的 GitHub release 且含当前平台匹配资产时，用 curl -fsSL 下载预编译二进制；
#      必须下载 SHA256SUMS 并校验 sha256（清单缺失或校验失败均中止）；
#   2. 否则源码构建 go build -trimpath ./cmd/marshal（Go 版本须满足 go.mod 的 go 指令；
#      无本地 checkout 时先浅克隆仓库）；
#   3. 安装到 ~/.local/bin（可用 MARSHAL_INSTALL_DIR 覆盖），并输出下一步指引。
#   全程不请求 sudo。
#
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/chiga0/marshal-harness/main/scripts/install.sh | bash
#   bash scripts/install.sh      # 仓库 checkout 内直接运行
#
# 可选环境变量:
#   MARSHAL_INSTALL_DIR   安装目录（默认 $HOME/.local/bin）
#   MARSHAL_REPO          GitHub owner/name（默认 chiga0/marshal-harness）
#   MARSHAL_TAG           固定 release tag（如 v0.1.0），跳过 latest release 查询
#   MARSHAL_FORCE_SOURCE  非空时跳过 release 下载，强制源码构建

set -euo pipefail

BIN_NAME="marshal"
REPO="${MARSHAL_REPO:-chiga0/marshal-harness}"
INSTALL_DIR="${MARSHAL_INSTALL_DIR:-$HOME/.local/bin}"
PIN_TAG="${MARSHAL_TAG:-}"
FORCE_SOURCE="${MARSHAL_FORCE_SOURCE:-}"
BUILDINFO_PKG="github.com/chiga0/marshal-harness/internal/buildinfo"

OS=""
ARCH=""
TAG=""
TMP_DIR=""
STABLE_STAGE_DIR=""
STABLE_STAGE_BIN=""

info()  { printf '[install] %s\n' "$*"; }
warn()  { printf '[install] 警告: %s\n' "$*" >&2; }
fatal() { printf '[install] 错误: %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fatal "缺少依赖: $1（请安装后重试）"
}

detect_platform() {
  local kernel machine
  kernel="$(uname -s)"
  machine="$(uname -m)"
  case "$kernel" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    *) fatal "不支持的操作系统: $kernel（仅支持 darwin/linux）" ;;
  esac
  case "$machine" in
    arm64|aarch64) ARCH="arm64" ;;
    x86_64|amd64)  ARCH="amd64" ;;
    *) fatal "不支持的 CPU 架构: $machine（仅支持 arm64/amd64）" ;;
  esac
  info "平台 ${OS}/${ARCH}"
}

# 比较点分版本号：$1 >= $2 时返回 0（只比较前三段数字）。
version_ge() {
  local IFS='.'
  local a b i av bv
  read -r -a a <<< "$1"
  read -r -a b <<< "$2"
  for i in 0 1 2; do
    av="${a[$i]:-0}"
    bv="${b[$i]:-0}"
    case "$av" in *[!0-9]*|'') av=0 ;; esac
    case "$bv" in *[!0-9]*|'') bv=0 ;; esac
    if [ "$av" -gt "$bv" ]; then return 0; fi
    if [ "$av" -lt "$bv" ]; then return 1; fi
  done
  return 0
}

fetch_latest_tag() {
  local resp tag
  info "查询 ${REPO} 的 latest release ..."
  if ! resp="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")"; then
    warn "暂无 release 或 GitHub API 不可达，回退源码构建"
    return 1
  fi
  tag="$(printf '%s\n' "$resp" | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | sed -n 1p)"
  case "$tag" in
    v*) TAG="$tag"; info "发现 release ${TAG}" ;;
    *)  warn "latest release tag '${tag}' 不符合 v* 约定，回退源码构建"; return 1 ;;
  esac
}

sha256_of() {
  local f="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$f" | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$f" | awk '{print $1}'
    return 0
  fi
  return 1
}

verify_sha256() {
  local asset="$1" prefix line expected actual
  prefix="${asset%_${OS}_${ARCH}}"
  line="$(awk -v want="$asset" -v prefix="$prefix" '
    {
      if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9A-Fa-f]/) {
        invalid=1
        next
      }
      name=$2
      if (name != prefix "_darwin_amd64" &&
          name != prefix "_darwin_arm64" &&
          name != prefix "_linux_amd64" &&
          name != prefix "_linux_arm64") {
        invalid=1
        next
      }
      seen[name]++
      count++
      if (name == want) hash=$1
    }
    END {
      if (invalid || count != 4 ||
          seen[prefix "_darwin_amd64"] != 1 ||
          seen[prefix "_darwin_arm64"] != 1 ||
          seen[prefix "_linux_amd64"] != 1 ||
          seen[prefix "_linux_arm64"] != 1 ||
          seen[want] != 1) exit 1
      print tolower(hash)
    }
  ' "${TMP_DIR}/SHA256SUMS")" || fatal "SHA256SUMS 必须且只能包含当前 tag 的四个平台资产，每项恰好一次"
  if [ -z "$line" ]; then
    fatal "SHA256SUMS 缺少 ${asset} 的校验项，中止安装"
  fi
  expected="${line%%[[:space:]]*}"
  expected="$(printf '%s' "$expected" | tr 'A-F' 'a-f')"
  if ! actual="$(sha256_of "${STABLE_STAGE_BIN}")"; then
    fatal "缺少 sha256sum/shasum，无法完成校验"
  fi
  if [ "$actual" != "$expected" ]; then
    fatal "sha256 校验失败: ${asset} 期望 ${expected}，实际 ${actual}"
  fi
  info "sha256 校验通过"
}

try_release() {
  local base version_no_v asset
  command -v curl >/dev/null 2>&1 || { warn "缺少 curl，回退源码构建"; return 1; }
  if [ -n "$PIN_TAG" ]; then
    TAG="$PIN_TAG"
    base="https://github.com/${REPO}/releases/download/${TAG}"
  else
    fetch_latest_tag || return 1
    base="https://github.com/${REPO}/releases/latest/download"
  fi
  version_no_v="${TAG#v}"
  asset="marshal_${version_no_v}_${OS}_${ARCH}"
  info "下载 release 资产 ${asset} ..."
  if ! curl -fsSL -o "${STABLE_STAGE_BIN}" "${base}/${asset}"; then
    warn "release 无 ${OS}/${ARCH} 匹配资产，回退源码构建"
    return 1
  fi
  curl -fsSL -o "${TMP_DIR}/SHA256SUMS" "${base}/SHA256SUMS" \
    || fatal "release ${TAG} 缺少或无法下载 SHA256SUMS；拒绝安装已下载资产"
  verify_sha256 "$asset"
  return 0
}

find_local_root() {
  local script_dir
  if [ -f go.mod ] && [ -d cmd/marshal ]; then
    printf '%s\n' "$PWD"
    return 0
  fi
  script_dir="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"
  if [ -n "$script_dir" ] && [ -f "${script_dir}/../go.mod" ] && [ -d "${script_dir}/../cmd/marshal" ]; then
    cd "${script_dir}/.." && pwd
    return 0
  fi
  return 1
}

go_version_ok() {
  local root="$1" required actual
  require_cmd go
  required="$(sed -n -E 's/^go[[:space:]]+([0-9][0-9.]*)[[:space:]]*$/\1/p' "${root}/go.mod" | sed -n 1p)"
  [ -n "$required" ] || fatal "无法解析 ${root}/go.mod 的 go 版本指令"
  actual="$(go version | sed -n 's/^go version go\([0-9][0-9.]*\).*$/\1/p' | sed -n 1p)"
  [ -n "$actual" ] || fatal "无法解析 go version 输出"
  if version_ge "$actual" "$required"; then
    info "Go 版本满足要求: ${actual}（要求 >= ${required}）"
  else
    fatal "Go 版本过低: ${actual}（go.mod 要求 ${required}），请升级 Go 后重试"
  fi
}

build_source() {
  local root="$1" ldflags
  go_version_ok "$root"
  info "源码构建: ${root}"
  ldflags="-s -w"
  if [ -n "$TAG" ]; then
    ldflags="${ldflags} -X ${BUILDINFO_PKG}.version=${TAG}"
  fi
  if ! ( cd "$root" && go build -trimpath -ldflags "$ldflags" -o "${STABLE_STAGE_BIN}" ./cmd/marshal ); then
    fatal "go build 失败；构建需联网下载模块，受限环境请先 go mod download（见 docs/development.md）"
  fi
}

clone_build() {
  require_cmd git
  info "无本地 checkout，浅克隆 https://github.com/${REPO}.git ..."
  if [ -n "$TAG" ]; then
    git clone --depth 1 --branch "$TAG" "https://github.com/${REPO}.git" "${TMP_DIR}/src" \
      || fatal "克隆失败（tag ${TAG}）"
  else
    git clone --depth 1 "https://github.com/${REPO}.git" "${TMP_DIR}/src" \
      || fatal "克隆失败"
  fi
  build_source "${TMP_DIR}/src"
}

install_binary() {
  install -m 0755 "${STABLE_STAGE_BIN}" "${INSTALL_DIR}/${BIN_NAME}" \
    || fatal "安装到 ${INSTALL_DIR} 失败"
  info "已安装 ${INSTALL_DIR}/${BIN_NAME}"
}

cleanup() {
  if [ -n "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
  if [ -n "$STABLE_STAGE_BIN" ]; then
    rm -f "$STABLE_STAGE_BIN"
  fi
  if [ -n "$STABLE_STAGE_DIR" ]; then
    rmdir "$STABLE_STAGE_DIR" 2>/dev/null || true
  fi
}

print_next_steps() {
  local mode="$1" ver
  if ver="$("${INSTALL_DIR}/${BIN_NAME}" version 2>/dev/null)"; then
    info "版本确认: ${ver}"
  else
    warn "安装的二进制无法执行 version 自检，请用 marshal doctor 排查"
  fi
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*)
      cat <<EOF
[install] 完成（${mode} 路径）
下一步:
  marshal init             # 在任意 Git 仓库初始化 Marshal 状态（.marshal/）
  marshal doctor --json    # 只读诊断，无副作用
EOF
      ;;
    *)
      cat <<EOF
[install] 完成（${mode} 路径）
注意: ${INSTALL_DIR} 不在 PATH 中，请将其加入 shell 配置:
  export PATH="${INSTALL_DIR}:\$PATH"
下一步:
  marshal init             # 在任意 Git 仓库初始化 Marshal 状态（.marshal/）
  marshal doctor --json    # 只读诊断，无副作用
EOF
      ;;
  esac
}

main() {
  detect_platform
  TMP_DIR="$(mktemp -d)"
  mkdir -p "$INSTALL_DIR" || fatal "无法创建安装目录 ${INSTALL_DIR}"
  STABLE_STAGE_DIR="${INSTALL_DIR}/.marshal-staging"
  STABLE_STAGE_BIN="${STABLE_STAGE_DIR}/${BIN_NAME}"
  mkdir -p "$STABLE_STAGE_DIR" || fatal "无法创建稳定构建暂存目录 ${STABLE_STAGE_DIR}"
  trap cleanup EXIT

  local mode="source"
  if [ -n "$FORCE_SOURCE" ]; then
    info "MARSHAL_FORCE_SOURCE 已设置，跳过 release 下载"
  elif try_release; then
    mode="release"
  fi
  if [ "$mode" = "source" ]; then
    local root
    if root="$(find_local_root)"; then
      build_source "$root"
    else
      clone_build
    fi
  fi
  install_binary
  print_next_steps "$mode"
}

main "$@"
