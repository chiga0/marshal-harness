#!/usr/bin/env bash
# Release tag 与资产集合的确定性 fail-closed 校验器。

set -euo pipefail

release_fatal() {
  printf '[release-check] 错误: %s\n' "$*" >&2
  exit 1
}

validate_release_tag() {
  local tag="$1"
  if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc([1-9][0-9]*))?$ ]]; then
    release_fatal "tag '${tag}' 不符合 vMAJOR.MINOR.PATCH 或 vMAJOR.MINOR.PATCH-rcN 约定"
  fi
}

release_kind() {
  local tag="$1"
  validate_release_tag "$tag"
  case "$tag" in
    *-rc*) printf 'prerelease\n' ;;
    *)     printf 'stable\n' ;;
  esac
}

sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print tolower($1)}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print tolower($1)}'
    return 0
  fi
  release_fatal "缺少 sha256sum/shasum，无法校验 release 资产"
}

verify_dist() {
  local dist_dir="$1" tag="$2" version manifest entry_count
  local line line_number=0 digest name actual seen=''
  local expected_names=()

  validate_release_tag "$tag"
  version="${tag#v}"
  expected_names=(
    "marshal_${version}_darwin_amd64"
    "marshal_${version}_darwin_arm64"
    "marshal_${version}_linux_amd64"
    "marshal_${version}_linux_arm64"
  )

  [ -d "$dist_dir" ] && [ ! -L "$dist_dir" ] \
    || release_fatal "dist 目录缺失或为符号链接: ${dist_dir}"
  manifest="${dist_dir}/SHA256SUMS"
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || release_fatal "SHA256SUMS 缺失、不是普通文件或为符号链接"

  for name in "${expected_names[@]}"; do
    [ -f "${dist_dir}/${name}" ] && [ ! -L "${dist_dir}/${name}" ] \
      || release_fatal "release 资产缺失、不是普通文件或为符号链接: ${name}"
    [ -x "${dist_dir}/${name}" ] \
      || release_fatal "release 资产不可执行: ${name}"
  done

  entry_count="$(find "$dist_dir" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d '[:space:]')"
  [ "$entry_count" = "5" ] \
    || release_fatal "dist 必须且只能包含四个平台资产与 SHA256SUMS（实际 ${entry_count} 项）"

  while IFS= read -r line || [ -n "$line" ]; do
    line_number=$((line_number + 1))
    if [[ ! "$line" =~ ^([0-9A-Fa-f]{64})[[:space:]][[:space:]]([[:alnum:]_.-]+)$ ]]; then
      release_fatal "SHA256SUMS 第 ${line_number} 行格式不合法"
    fi
    digest="$(printf '%s' "${BASH_REMATCH[1]}" | tr 'A-F' 'a-f')"
    name="${BASH_REMATCH[2]}"
    case " ${expected_names[*]} " in
      *" ${name} "*) ;;
      *) release_fatal "SHA256SUMS 包含非预期资产: ${name}" ;;
    esac
    case "|${seen}|" in
      *"|${name}|"*) release_fatal "SHA256SUMS 包含重复资产: ${name}" ;;
    esac
    seen="${seen}|${name}"
    actual="$(sha256_file "${dist_dir}/${name}")"
    [ "$actual" = "$digest" ] \
      || release_fatal "SHA256 不匹配: ${name} 期望 ${digest}，实际 ${actual}"
  done < "$manifest"

  [ "$line_number" = "4" ] \
    || release_fatal "SHA256SUMS 必须且只能包含四项（实际 ${line_number} 项）"
  for name in "${expected_names[@]}"; do
    case "|${seen}|" in
      *"|${name}|"*) ;;
      *) release_fatal "SHA256SUMS 缺少资产校验项: ${name}" ;;
    esac
  done

  printf '[release-check] %s 资产集合与 SHA256SUMS 校验通过\n' "$tag"
}

usage() {
  cat >&2 <<'EOF'
用法:
  scripts/release-contract.sh classify TAG
  scripts/release-contract.sh verify-dist DIST_DIR TAG
EOF
  exit 2
}

case "${1:-}" in
  classify)
    [ "$#" = "2" ] || usage
    release_kind "$2"
    ;;
  verify-dist)
    [ "$#" = "3" ] || usage
    verify_dist "$2" "$3"
    ;;
  *) usage ;;
esac
