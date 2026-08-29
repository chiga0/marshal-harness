#!/usr/bin/env bash
# Release tag 与资产集合的确定性 fail-closed 校验器。

set -euo pipefail

RELEASE_ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
RELEASE_TEMP_MESSAGE=""
RC1_TAG="v1.0.0-rc1"
RC1_BINARY_CHECKER="${RELEASE_ROOT}/scripts/release-rc1-binary-check.py"

cleanup_release_temp() {
  if [ -n "$RELEASE_TEMP_MESSAGE" ]; then
    rm -f "$RELEASE_TEMP_MESSAGE"
  fi
}
trap cleanup_release_temp EXIT

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

validate_rc1_tag() {
  local tag="$1"
  [ "$tag" = "$RC1_TAG" ] \
    || release_fatal "RC1 单资产合同只允许 ${RC1_TAG}，拒绝 ${tag:-empty}"
}

validate_rc1_inputs() {
  local tag="$1" source_head="$2" build_date="$3" go_version="$4"
  validate_rc1_tag "$tag"
  validate_source_head "$source_head"
  validate_build_date "$build_date"
  validate_go_version "$go_version"
}

validate_rc1_identity() {
  local os="$1" arch="$2" profile="$3"
  [ "$os" = darwin ] && [ "$arch" = arm64 ] && [ "$profile" = darwin-local-dogfood ] \
    || release_fatal "RC1 binary identity 必须精确为 darwin/arm64 + darwin-local-dogfood"
}

rc1_asset_name() {
  printf 'marshal_1.0.0-rc1_darwin_arm64\n'
}

verify_rc1_binary() {
  local candidate="$1" tag="$2" source_head="$3" build_date="$4" go_version="$5"
  local os="$6" arch="$7" profile="$8" go_bin="${GO_BIN:-}"
  validate_rc1_inputs "$tag" "$source_head" "$build_date" "$go_version"
  validate_rc1_identity "$os" "$arch" "$profile"
  [ -n "$go_bin" ] || release_fatal "RC1 binary inspection 缺少固定 GO_BIN"
  python3 -I -B "$RC1_BINARY_CHECKER" "$candidate" "${tag#v}" \
    "$source_head" "$build_date" "$go_version" "$profile" "$go_bin" >/dev/null \
    || release_fatal "RC1 candidate 的 Mach-O/Go/build identity 不匹配"
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

create_rc1_manifest() {
  local dist_dir="$1" tag="$2" source_head="$3" build_date="$4" go_version="$5"
  local manifest temporary name digest size

  validate_rc1_inputs "$tag" "$source_head" "$build_date" "$go_version"
  [ -d "$dist_dir" ] && [ ! -L "$dist_dir" ] \
    || release_fatal "RC1 dist 目录缺失或为符号链接: ${dist_dir}"
  name="$(rc1_asset_name)"
  [ -f "${dist_dir}/${name}" ] && [ ! -L "${dist_dir}/${name}" ] && [ -x "${dist_dir}/${name}" ] \
    || release_fatal "RC1 唯一 Darwin arm64 candidate 缺失、不是普通可执行文件或为符号链接"
  verify_rc1_binary "${dist_dir}/${name}" "$tag" "$source_head" "$build_date" \
    "$go_version" darwin arm64 darwin-local-dogfood
  digest="$(sha256_file "${dist_dir}/${name}")"
  size="$(file_size "${dist_dir}/${name}")"
  manifest="${dist_dir}/RELEASE-MANIFEST"
  [ ! -e "$manifest" ] || [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || release_fatal "RC1 RELEASE-MANIFEST 目标不是普通文件"
  temporary="${manifest}.tmp.$$"
  trap 'rm -f "$temporary"' RETURN
  {
    printf 'schemaVersion marshal.rc1-release-manifest.v1\n'
    printf 'repository https://github.com/chiga0/marshal-harness.git\n'
    printf 'tag %s\n' "$tag"
    printf 'sourceHead %s\n' "$source_head"
    printf 'buildDate %s\n' "$build_date"
    printf 'goVersion %s\n' "$go_version"
    printf 'buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=\n'
    printf 'asset %s %s %s darwin arm64 darwin-local-dogfood\n' "$digest" "$size" "$name"
  } >"$temporary"
  chmod 0644 "$temporary"
  mv "$temporary" "$manifest"
  trap - RETURN
}

verify_rc1_manifest() {
  local dist_dir="$1" tag="$2" expected_source_head="$3" expected_build_date="$4"
  local expected_go_version="$5" expected_os="$6" expected_arch="$7" expected_profile="$8"
  local manifest name record digest size go_version actual_digest actual_size

  validate_rc1_inputs "$tag" "$expected_source_head" "$expected_build_date" "$expected_go_version"
  validate_rc1_identity "$expected_os" "$expected_arch" "$expected_profile"
  manifest="${dist_dir}/RELEASE-MANIFEST"
  name="$(rc1_asset_name)"
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || release_fatal "RC1 RELEASE-MANIFEST 缺失、不是普通文件或为符号链接"
  record="$(awk -v tag="$tag" -v expected_head="$expected_source_head" \
    -v expected_date="$expected_build_date" -v expected_go="$expected_go_version" \
    -v expected_os="$expected_os" -v expected_arch="$expected_arch" \
    -v expected_profile="$expected_profile" -v name="$name" '
    function die(message) { print "[release-check] 错误: " message > "/dev/stderr"; exit 1 }
    NR == 1 { if ($0 != "schemaVersion marshal.rc1-release-manifest.v1") die("RC1 manifest schemaVersion 非法"); next }
    NR == 2 { if ($0 != "repository https://github.com/chiga0/marshal-harness.git") die("RC1 manifest repository 非 canonical"); next }
    NR == 3 { if ($0 != "tag " tag) die("RC1 manifest tag 不匹配"); next }
    NR == 4 {
      if ($1 != "sourceHead" || NF != 2 || $2 !~ /^[0-9a-f]{40}$/) die("RC1 manifest sourceHead 非法")
      if ($2 != expected_head) die("RC1 manifest sourceHead 与期望值不匹配")
      next
    }
    NR == 5 {
      if ($1 != "buildDate" || NF != 2 || $2 != expected_date) die("RC1 manifest buildDate 与外部期望值不匹配")
      next
    }
    NR == 6 {
      if ($1 != "goVersion" || NF != 2 || $2 != expected_go) die("RC1 manifest goVersion 与外部期望值不匹配")
      go_version=$2
      next
    }
    NR == 7 { if ($0 != "buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=") die("RC1 manifest buildFlags 不匹配"); next }
    NR == 8 {
      if ($1 != "asset" || NF != 7 || $2 !~ /^[0-9a-f]{64}$/ || $3 !~ /^[1-9][0-9]*$/ ||
          $4 != name || $5 != expected_os || $6 != expected_arch || $7 != expected_profile) {
        die("RC1 manifest 必须只声明唯一 Darwin arm64 local-dogfood asset")
      }
      digest=$2
      size=$3
      next
    }
    { die("RC1 manifest 包含尾随或额外字段") }
    END {
      if (NR != 8) die("RC1 manifest 必须精确包含 8 行")
      print digest " " size " " go_version
    }
  ' "$manifest")" || release_fatal "RC1 RELEASE-MANIFEST 不满足封闭合同"
  read -r digest size go_version <<<"$record"
  validate_go_version "$go_version"
  actual_digest="$(sha256_file "${dist_dir}/${name}")"
  actual_size="$(file_size "${dist_dir}/${name}")"
  [ "$digest" = "$actual_digest" ] \
    || release_fatal "RC1 manifest candidate SHA256 不匹配"
  [ "$size" = "$actual_size" ] \
    || release_fatal "RC1 manifest candidate size 不匹配"
}

verify_rc1_dist() {
  local dist_dir="$1" tag="$2" expected_source_head="$3" expected_build_date="$4"
  local expected_go_version="$5" expected_os="$6" expected_arch="$7" expected_profile="$8"
  local name sums entry_count line_number=0 line digest file actual

  validate_rc1_inputs "$tag" "$expected_source_head" "$expected_build_date" "$expected_go_version"
  validate_rc1_identity "$expected_os" "$expected_arch" "$expected_profile"
  [ -d "$dist_dir" ] && [ ! -L "$dist_dir" ] \
    || release_fatal "RC1 dist 目录缺失或为符号链接: ${dist_dir}"
  name="$(rc1_asset_name)"
  [ -f "${dist_dir}/${name}" ] && [ ! -L "${dist_dir}/${name}" ] && [ -x "${dist_dir}/${name}" ] \
    || release_fatal "RC1 唯一 Darwin arm64 candidate 缺失、不是普通可执行文件或为符号链接"
  sums="${dist_dir}/SHA256SUMS"
  [ -f "$sums" ] && [ ! -L "$sums" ] \
    || release_fatal "RC1 SHA256SUMS 缺失、不是普通文件或为符号链接"
  entry_count="$(find "$dist_dir" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d '[:space:]')"
  [ "$entry_count" = 3 ] \
    || release_fatal "RC1 dist 必须且只能包含 Darwin arm64 candidate、RELEASE-MANIFEST 与 SHA256SUMS（实际 ${entry_count} 项）"
  verify_rc1_manifest "$dist_dir" "$tag" "$expected_source_head" "$expected_build_date" \
    "$expected_go_version" "$expected_os" "$expected_arch" "$expected_profile"
  verify_rc1_binary "${dist_dir}/${name}" "$tag" "$expected_source_head" \
    "$expected_build_date" "$expected_go_version" "$expected_os" "$expected_arch" "$expected_profile"

  while IFS= read -r line || [ -n "$line" ]; do
    line_number=$((line_number + 1))
    if [[ ! "$line" =~ ^([0-9a-f]{64})[\ ][\ ]([[:alnum:]_.-]+)$ ]]; then
      release_fatal "RC1 SHA256SUMS 第 ${line_number} 行格式不合法"
    fi
    digest="${BASH_REMATCH[1]}"
    file="${BASH_REMATCH[2]}"
    case "$line_number:$file" in
      1:RELEASE-MANIFEST|2:"$name") ;;
      *) release_fatal "RC1 SHA256SUMS 顺序、资产名或闭集不匹配: ${file}" ;;
    esac
    actual="$(sha256_file "${dist_dir}/${file}")"
    [ "$actual" = "$digest" ] \
      || release_fatal "RC1 SHA256 不匹配: ${file}"
  done <"$sums"
  [ "$line_number" = 2 ] \
    || release_fatal "RC1 SHA256SUMS 必须且只能包含两行"
  printf '[release-check] %s 唯一 Darwin arm64 candidate 合同校验通过\n' "$tag"
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

parse_candidate_message() {
  local message_file="$1" tag="$2" source_head="$3" marker
  marker="$(printf '\036')"
  awk -v tag="$tag" -v commit="$source_head" -v marker="$marker" '
    NR == 1 { if ($0 != "Marshal " tag " candidate") exit 1; next }
    NR == 2 { if ($0 != "") exit 1; next }
    NR == 3 { if ($0 != "marshal-candidate-schema: v1") exit 1; next }
    NR == 4 { if ($0 != "marshal-candidate-source-head: " commit) exit 1; next }
    NR == 5 {
      if ($0 !~ /^marshal-candidate-manifest-sha256: [0-9a-f]{64}$/) exit 1
      manifest=substr($0, length("marshal-candidate-manifest-sha256: ")+1)
      next
    }
    NR == 6 {
      if ($0 !~ /^marshal-candidate-darwin-arm64-sha256: [0-9a-f]{64}$/) exit 1
      candidate=substr($0, length("marshal-candidate-darwin-arm64-sha256: ")+1)
      next
    }
    NR == 7 { if ($0 != marker) exit 1; seen_marker=1; next }
    { exit 1 }
    END { if (NR != 7 || !seen_marker || manifest == "" || candidate == "") exit 1; print manifest " " candidate }
  ' "$message_file"
}

verify_candidate_tag() {
  local repository="$1" dist_dir="$2" tag="$3"
  local tag_type source_head metadata declared_manifest declared_candidate message_size
  local actual_manifest actual_candidate version
  validate_release_tag "$tag"
  tag_type="$(git -C "$repository" cat-file -t "refs/tags/${tag}" 2>/dev/null)" \
    || release_fatal "release tag ${tag} 不存在"
  [ "$tag_type" = tag ] || release_fatal "release tag ${tag} 必须是 annotated tag"
  source_head="$(git -C "$repository" rev-parse --verify "refs/tags/${tag}^{commit}" 2>/dev/null)" \
    || release_fatal "release tag ${tag} 无法 peel 到 commit"
  validate_source_head "$source_head"
  RELEASE_TEMP_MESSAGE="$(mktemp "${TMPDIR:-/tmp}/marshal-candidate-message.XXXXXX")" \
    || release_fatal "无法创建 candidate tag message 临时数据对象"
  git -C "$repository" for-each-ref --format='%(contents)%1e' "refs/tags/${tag}" >"$RELEASE_TEMP_MESSAGE" \
    || release_fatal "无法读取 annotated tag message"
  message_size="$(wc -c <"$RELEASE_TEMP_MESSAGE" | tr -d '[:space:]')"
  [ "$message_size" -le 65538 ] || release_fatal "annotated tag message 超过 64 KiB"
  metadata="$(parse_candidate_message "$RELEASE_TEMP_MESSAGE" "$tag" "$source_head")" \
    || release_fatal "annotated tag candidate message 必须是 exact 6-line closed 格式"
  rm -f "$RELEASE_TEMP_MESSAGE"
  RELEASE_TEMP_MESSAGE=""
  read -r declared_manifest declared_candidate <<<"$metadata"
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
  scripts/release-contract.sh validate-rc1-inputs TAG SOURCE_HEAD BUILD_DATE GO_VERSION
  scripts/release-contract.sh create-rc1-manifest DIST_DIR TAG SOURCE_HEAD BUILD_DATE GO_VERSION
  scripts/release-contract.sh verify-rc1-dist DIST_DIR TAG EXPECTED_SOURCE_HEAD EXPECTED_BUILD_DATE EXPECTED_GO_VERSION EXPECTED_OS EXPECTED_ARCH EXPECTED_PROFILE
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
  validate-rc1-inputs)
    [ "$#" = 5 ] || usage
    validate_rc1_inputs "$2" "$3" "$4" "$5"
    ;;
  create-rc1-manifest)
    [ "$#" = 6 ] || usage
    create_rc1_manifest "$2" "$3" "$4" "$5" "$6"
    ;;
  verify-rc1-dist)
    [ "$#" = 9 ] || usage
    verify_rc1_dist "$2" "$3" "$4" "$5" "$6" "$7" "$8" "$9"
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
