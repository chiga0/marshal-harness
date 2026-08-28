#!/usr/bin/env bash
# Release tag 与资产集合的确定性 fail-closed 校验器。

set -euo pipefail

RELEASE_ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"

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

file_size() {
  wc -c <"$1" | tr -d '[:space:]'
}

validate_source_head() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]] \
    || release_fatal "sourceHead 必须是 40 位小写 Git commit"
}

validate_build_date() {
  [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] \
    || release_fatal "buildDate 必须是 canonical UTC 秒级时间"
}

validate_go_version() {
  local required
  [[ "$1" =~ ^go[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || release_fatal "goVersion 必须是精确 goMAJOR.MINOR.PATCH"
  required="$(sed -n -E 's/^toolchain[[:space:]]+(go[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$/\1/p' "${RELEASE_ROOT}/go.mod")"
  [ -n "$required" ] && [ "$1" = "$required" ] \
    || release_fatal "goVersion 与 go.mod toolchain 不一致：期望 ${required:-missing}，实际 $1"
}

canonical_build_date() {
  local repository="$1" source_head="$2" resolved epoch
  validate_source_head "$source_head"
  [ -d "$repository" ] && [ ! -L "$repository" ] \
    || release_fatal "repository 缺失或为符号链接: ${repository}"
  resolved="$(git -C "$repository" rev-parse --verify "${source_head}^{commit}" 2>/dev/null)" \
    || release_fatal "sourceHead 不是 repository 中可解析的 commit"
  [ "$resolved" = "$source_head" ] || release_fatal "sourceHead 未精确解析到自身"
  epoch="$(git -C "$repository" show -s --format=%ct "$source_head")"
  [[ "$epoch" =~ ^[0-9]+$ ]] || release_fatal "无法读取 commit timestamp"
  python3 -I -B - "$epoch" <<'PY'
import datetime, sys
print(datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
}

expected_asset_names() {
  local version="$1"
  printf '%s\n' \
    "marshal_${version}_darwin_amd64" \
    "marshal_${version}_darwin_arm64" \
    "marshal_${version}_linux_amd64" \
    "marshal_${version}_linux_arm64"
}

create_manifest() {
  local dist_dir="$1" tag="$2" source_head="$3" build_date="$4" go_version="$5"
  local version manifest temporary name os arch profile digest size

  validate_release_tag "$tag"
  validate_source_head "$source_head"
  validate_build_date "$build_date"
  validate_go_version "$go_version"
  version="${tag#v}"
  [ -d "$dist_dir" ] && [ ! -L "$dist_dir" ] \
    || release_fatal "dist 目录缺失或为符号链接: ${dist_dir}"
  manifest="${dist_dir}/RELEASE-MANIFEST"
  temporary="${manifest}.tmp.$$"
  trap 'rm -f "$temporary"' RETURN
  {
    printf 'schemaVersion marshal.release-manifest.v1\n'
    printf 'repository https://github.com/chiga0/marshal-harness.git\n'
    printf 'tag %s\n' "$tag"
    printf 'sourceHead %s\n' "$source_head"
    printf 'buildDate %s\n' "$build_date"
    printf 'goVersion %s\n' "$go_version"
    printf 'buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=\n'
    while IFS= read -r name; do
      case "$name" in
        *_darwin_amd64) os=darwin; arch=amd64; profile=darwin-local-dogfood ;;
        *_darwin_arm64) os=darwin; arch=arm64; profile=darwin-local-dogfood ;;
        *_linux_amd64) os=linux; arch=amd64; profile=unprofiled ;;
        *_linux_arm64) os=linux; arch=arm64; profile=unprofiled ;;
        *) release_fatal "无法解析 release 资产身份: ${name}" ;;
      esac
      [ -f "${dist_dir}/${name}" ] && [ ! -L "${dist_dir}/${name}" ] \
        || release_fatal "release 资产缺失、不是普通文件或为符号链接: ${name}"
      digest="$(sha256_file "${dist_dir}/${name}")"
      size="$(file_size "${dist_dir}/${name}")"
      printf 'asset %s %s %s %s %s %s\n' "$digest" "$size" "$name" "$os" "$arch" "$profile"
    done < <(expected_asset_names "$version")
  } >"$temporary"
  chmod 0644 "$temporary"
  mv "$temporary" "$manifest"
  trap - RETURN
}

candidate_tag_message() {
  local dist_dir="$1" tag="$2" source_head="$3" manifest_digest candidate_digest version
  verify_dist "$dist_dir" "$tag" "$source_head" >/dev/null
  version="${tag#v}"
  manifest_digest="$(sha256_file "${dist_dir}/RELEASE-MANIFEST")"
  candidate_digest="$(sha256_file "${dist_dir}/marshal_${version}_darwin_arm64")"
  cat <<EOF
Marshal ${tag} candidate

marshal-candidate-schema: v1
marshal-candidate-source-head: ${source_head}
marshal-candidate-manifest-sha256: ${manifest_digest}
marshal-candidate-darwin-arm64-sha256: ${candidate_digest}
EOF
}

candidate_trailer() {
  local message="$1" key="$2"
  printf '%s\n' "$message" | awk -v key="$key" '
    index($0, key ": ") == 1 { count++; value=substr($0, length(key)+3) }
    END { if (count != 1 || value == "") exit 1; print value }
  '
}

verify_candidate_tag() {
  local repository="$1" dist_dir="$2" tag="$3"
  local tag_type source_head message schema declared_head declared_manifest declared_candidate
  local actual_manifest actual_candidate version
  validate_release_tag "$tag"
  tag_type="$(git -C "$repository" cat-file -t "refs/tags/${tag}" 2>/dev/null)" \
    || release_fatal "release tag ${tag} 不存在"
  [ "$tag_type" = tag ] || release_fatal "release tag ${tag} 必须是 annotated tag"
  source_head="$(git -C "$repository" rev-parse --verify "refs/tags/${tag}^{commit}" 2>/dev/null)" \
    || release_fatal "release tag ${tag} 无法 peel 到 commit"
  validate_source_head "$source_head"
  message="$(git -C "$repository" for-each-ref --format='%(contents)' "refs/tags/${tag}")"
  if printf '%s\n' "$message" | awk '
    /^marshal-candidate-/ && $0 !~ /^marshal-candidate-(schema|source-head|manifest-sha256|darwin-arm64-sha256): / { exit 1 }
  '; then :; else
    release_fatal "annotated tag 包含未知 candidate trailer"
  fi
  schema="$(candidate_trailer "$message" marshal-candidate-schema)" \
    || release_fatal "candidate schema trailer 缺失或重复"
  declared_head="$(candidate_trailer "$message" marshal-candidate-source-head)" \
    || release_fatal "candidate source-head trailer 缺失或重复"
  declared_manifest="$(candidate_trailer "$message" marshal-candidate-manifest-sha256)" \
    || release_fatal "candidate manifest trailer 缺失或重复"
  declared_candidate="$(candidate_trailer "$message" marshal-candidate-darwin-arm64-sha256)" \
    || release_fatal "candidate darwin-arm64 trailer 缺失或重复"
  [ "$schema" = v1 ] || release_fatal "candidate schema 不是 v1"
  [ "$declared_head" = "$source_head" ] \
    || release_fatal "candidate sourceHead 与 peeled tag commit 不一致"
  [[ "$declared_manifest" =~ ^[0-9a-f]{64}$ ]] \
    || release_fatal "candidate manifest SHA256 非法"
  [[ "$declared_candidate" =~ ^[0-9a-f]{64}$ ]] \
    || release_fatal "candidate Darwin arm64 SHA256 非法"
  verify_dist "$dist_dir" "$tag" "$source_head"
  version="${tag#v}"
  actual_manifest="$(sha256_file "${dist_dir}/RELEASE-MANIFEST")"
  actual_candidate="$(sha256_file "${dist_dir}/marshal_${version}_darwin_arm64")"
  [ "$declared_manifest" = "$actual_manifest" ] \
    || release_fatal "跨主机重建的 RELEASE-MANIFEST 与 canary tag 摘要不一致"
  [ "$declared_candidate" = "$actual_candidate" ] \
    || release_fatal "跨主机重建的 Darwin arm64 资产与 canary bytes 不一致"
}

verify_release_manifest() {
  local dist_dir="$1" tag="$2" expected_source_head="${3:-}"
  local version manifest kind digest size name os arch profile actual_digest actual_size
  validate_release_tag "$tag"
  if [ -n "$expected_source_head" ]; then
    validate_source_head "$expected_source_head"
  fi
  version="${tag#v}"
  manifest="${dist_dir}/RELEASE-MANIFEST"
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || release_fatal "RELEASE-MANIFEST 缺失、不是普通文件或为符号链接"

  awk -v tag="$tag" -v version="$version" -v expected_head="$expected_source_head" '
    function die(message) { print "[release-check] 错误: " message > "/dev/stderr"; exit 1 }
    NR == 1 { if ($0 != "schemaVersion marshal.release-manifest.v1") die("manifest schemaVersion 非法"); next }
    NR == 2 { if ($0 != "repository https://github.com/chiga0/marshal-harness.git") die("manifest repository 非 canonical"); next }
    NR == 3 { if ($0 != "tag " tag) die("manifest tag 不匹配"); next }
    NR == 4 {
      if ($1 != "sourceHead" || NF != 2 || $2 !~ /^[0-9a-f]{40}$/) die("manifest sourceHead 非法")
      if (expected_head != "" && $2 != expected_head) die("manifest sourceHead 与 peeled tag commit 不匹配")
      next
    }
    NR == 5 { if ($1 != "buildDate" || NF != 2 || $2 !~ /^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$/) die("manifest buildDate 非 canonical UTC"); next }
    NR == 6 { if ($1 != "goVersion" || NF != 2 || $2 !~ /^go[0-9]+\.[0-9]+\.[0-9]+$/) die("manifest goVersion 非精确版本"); next }
    NR == 7 { if ($0 != "buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=") die("manifest buildFlags 不匹配"); next }
    NR >= 8 && NR <= 11 {
      idx = NR - 7
      names[1] = "marshal_" version "_darwin_amd64"
      names[2] = "marshal_" version "_darwin_arm64"
      names[3] = "marshal_" version "_linux_amd64"
      names[4] = "marshal_" version "_linux_arm64"
      oses[1] = oses[2] = "darwin"; oses[3] = oses[4] = "linux"
      arches[1] = arches[3] = "amd64"; arches[2] = arches[4] = "arm64"
      profiles[1] = profiles[2] = "darwin-local-dogfood"; profiles[3] = profiles[4] = "unprofiled"
      if ($1 != "asset" || NF != 7 || $2 !~ /^[0-9a-f]{64}$/ || $3 !~ /^[1-9][0-9]*$/ ||
          $4 != names[idx] || $5 != oses[idx] || $6 != arches[idx] || $7 != profiles[idx]) {
        die("manifest asset 第 " idx " 项非法")
      }
      next
    }
    { die("manifest 包含尾随或额外字段") }
    END { if (NR != 11) die("manifest 必须精确包含 11 行") }
  ' "$manifest"

  while read -r kind digest size name os arch profile; do
    [ "$kind" = asset ] || release_fatal "manifest asset 行类型非法"
    actual_digest="$(sha256_file "${dist_dir}/${name}")"
    actual_size="$(file_size "${dist_dir}/${name}")"
    [ "$digest" = "$actual_digest" ] \
      || release_fatal "manifest asset SHA256 不匹配: ${name}"
    [ "$size" = "$actual_size" ] \
      || release_fatal "manifest asset size 不匹配: ${name}"
  done < <(sed -n '8,11p' "$manifest")
}

verify_dist() {
  local dist_dir="$1" tag="$2" expected_source_head="${3:-}" version manifest entry_count
  local line line_number=0 digest name actual seen=''
  local expected_names=()

  validate_release_tag "$tag"
  version="${tag#v}"
  expected_names=(
    "marshal_${version}_darwin_amd64"
    "marshal_${version}_darwin_arm64"
    "marshal_${version}_linux_amd64"
    "marshal_${version}_linux_arm64"
    "RELEASE-MANIFEST"
  )

  [ -d "$dist_dir" ] && [ ! -L "$dist_dir" ] \
    || release_fatal "dist 目录缺失或为符号链接: ${dist_dir}"
  manifest="${dist_dir}/SHA256SUMS"
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || release_fatal "SHA256SUMS 缺失、不是普通文件或为符号链接"

  for name in "${expected_names[@]:0:4}"; do
    [ -f "${dist_dir}/${name}" ] && [ ! -L "${dist_dir}/${name}" ] \
      || release_fatal "release 资产缺失、不是普通文件或为符号链接: ${name}"
    [ -x "${dist_dir}/${name}" ] \
      || release_fatal "release 资产不可执行: ${name}"
  done

  entry_count="$(find "$dist_dir" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d '[:space:]')"
  [ "$entry_count" = "6" ] \
    || release_fatal "dist 必须且只能包含四个平台资产、RELEASE-MANIFEST 与 SHA256SUMS（实际 ${entry_count} 项）"

  verify_release_manifest "$dist_dir" "$tag" "$expected_source_head"

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

  [ "$line_number" = "5" ] \
    || release_fatal "SHA256SUMS 必须且只能包含四个平台资产与 RELEASE-MANIFEST（实际 ${line_number} 项）"
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
  scripts/release-contract.sh build-date REPOSITORY SOURCE_HEAD
  scripts/release-contract.sh create-manifest DIST_DIR TAG SOURCE_HEAD BUILD_DATE GO_VERSION
  scripts/release-contract.sh candidate-tag-message DIST_DIR TAG SOURCE_HEAD
  scripts/release-contract.sh verify-candidate-tag REPOSITORY DIST_DIR TAG
  scripts/release-contract.sh verify-dist DIST_DIR TAG [EXPECTED_SOURCE_HEAD]
EOF
  exit 2
}

case "${1:-}" in
  classify)
    [ "$#" = "2" ] || usage
    release_kind "$2"
    ;;
  build-date)
    [ "$#" = "3" ] || usage
    canonical_build_date "$2" "$3"
    ;;
  create-manifest)
    [ "$#" = "6" ] || usage
    create_manifest "$2" "$3" "$4" "$5" "$6"
    ;;
  candidate-tag-message)
    [ "$#" = "4" ] || usage
    candidate_tag_message "$2" "$3" "$4"
    ;;
  verify-candidate-tag)
    [ "$#" = "4" ] || usage
    verify_candidate_tag "$2" "$3" "$4"
    ;;
  verify-dist)
    [ "$#" = "3" ] || [ "$#" = "4" ] || usage
    verify_dist "$2" "$3" "${4:-}"
    ;;
  *) usage ;;
esac
