#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECKER="${ROOT}/scripts/release-contract.sh"
TMP_ROOT="$(mktemp -d)"
RC1_SOURCE_HEAD=0123456789abcdef0123456789abcdef01234567
RC1_BUILD_DATE=2026-08-28T00:00:00Z
RC1_GO_VERSION=go1.26.6
RC1_PROFILE=darwin-local-dogfood
RC1_GO_BIN="$(go env GOROOT)/bin/go"
trap 'rm -rf "$TMP_ROOT"' EXIT

fail() {
  printf '[release-contract-test] FAIL: %s\n' "$*" >&2
  exit 1
}

expect_fail() {
  local description="$1"
  shift
  if "$@" >"${TMP_ROOT}/last.out" 2>"${TMP_ROOT}/last.err"; then
    fail "${description}: 预期失败但命令成功"
  fi
}

sha256_fixture() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

make_dist() {
  local dir="$1" tag="$2" source_head="${3:-0123456789abcdef0123456789abcdef01234567}" version name
  version="${tag#v}"
  mkdir -p "$dir"
  for name in \
    "marshal_${version}_darwin_amd64" \
    "marshal_${version}_darwin_arm64" \
    "marshal_${version}_linux_amd64" \
    "marshal_${version}_linux_arm64"; do
    printf '#!/bin/sh\nprintf "fixture %s\\n"\n' "$name" >"${dir}/${name}"
    chmod 0755 "${dir}/${name}"
  done
  bash "$CHECKER" create-manifest "$dir" "$tag" \
    "$source_head" \
    2026-08-28T00:00:00Z go1.26.6
  rewrite_sums "$dir"
}

rewrite_sums() {
  local dir="$1" name
  : >"${dir}/SHA256SUMS"
  for name in RELEASE-MANIFEST \
    marshal_1.0.0-rc1_darwin_amd64 marshal_1.0.0-rc1_darwin_arm64 \
    marshal_1.0.0-rc1_linux_amd64 marshal_1.0.0-rc1_linux_arm64; do
    printf '%s  %s\n' "$(sha256_fixture "${dir}/${name}")" "$name" >>"${dir}/SHA256SUMS"
  done
}

rewrite_rc1_sums() {
  local dir="$1" name
  : >"${dir}/SHA256SUMS"
  for name in RELEASE-MANIFEST marshal_1.0.0-rc1_darwin_arm64; do
    printf '%s  %s\n' "$(sha256_fixture "${dir}/${name}")" "$name" >>"${dir}/SHA256SUMS"
  done
}

make_rc1_dist() {
  local dir="$1" source_head="${2:-$RC1_SOURCE_HEAD}"
  make -C "$ROOT" dist-rc1 GO="$RC1_GO_BIN" DIST_DIR="$dir" \
    VERSION=1.0.0-rc1 COMMIT="$source_head" BUILD_DATE="$RC1_BUILD_DATE" >/dev/null
}

verify_rc1_dist() {
  GO_BIN="$RC1_GO_BIN" bash "$CHECKER" verify-rc1-dist \
    "$1" "${2:-v1.0.0-rc1}" "${3:-$RC1_SOURCE_HEAD}" \
    "${4:-$RC1_BUILD_DATE}" "${5:-$RC1_GO_VERSION}" \
    "${6:-darwin}" "${7:-arm64}" "${8:-$RC1_PROFILE}"
}

refresh_rc1_asset_identity() {
  local dir="$1" candidate digest size temporary
  candidate="${dir}/marshal_1.0.0-rc1_darwin_arm64"
  digest="$(sha256_fixture "$candidate")"
  size="$(wc -c <"$candidate" | tr -d '[:space:]')"
  temporary="${dir}/RELEASE-MANIFEST.next"
  awk -v digest="$digest" -v size="$size" '
    NR == 8 { print "asset " digest " " size " marshal_1.0.0-rc1_darwin_arm64 darwin arm64 darwin-local-dogfood"; next }
    { print }
  ' "${dir}/RELEASE-MANIFEST" >"$temporary"
  mv "$temporary" "${dir}/RELEASE-MANIFEST"
  rewrite_rc1_sums "$dir"
}

[ "$(bash "$CHECKER" classify v1.0.0)" = stable ] || fail 'stable tag 分类错误'
[ "$(bash "$CHECKER" classify v1.0.0-rc1)" = prerelease ] || fail 'rc tag 分类错误'
[ "$(bash "$CHECKER" classify v12.34.56-rc78)" = prerelease ] || fail '多位 rc tag 分类错误'

for invalid in v1 v1.0 v1.0.0-beta1 v1.0.0-rc v1.0.0-rc0 v1.0.0-rc01 v01.0.0 main; do
  expect_fail "非法 tag ${invalid}" bash "$CHECKER" classify "$invalid"
done

GOOD="${TMP_ROOT}/good"
make_dist "$GOOD" v1.0.0-rc1
bash "$CHECKER" verify-dist "$GOOD" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567 >/dev/null

MISSING="${TMP_ROOT}/missing"
cp -R "$GOOD" "$MISSING"
rm "${MISSING}/marshal_1.0.0-rc1_linux_arm64"
expect_fail '缺失平台资产' bash "$CHECKER" verify-dist "$MISSING" v1.0.0-rc1

EXTRA="${TMP_ROOT}/extra"
cp -R "$GOOD" "$EXTRA"
printf 'unexpected\n' >"${EXTRA}/unexpected.txt"
expect_fail '额外资产' bash "$CHECKER" verify-dist "$EXTRA" v1.0.0-rc1

MISMATCH="${TMP_ROOT}/mismatch"
cp -R "$GOOD" "$MISMATCH"
printf 'tampered\n' >>"${MISMATCH}/marshal_1.0.0-rc1_darwin_arm64"
expect_fail '摘要漂移' bash "$CHECKER" verify-dist "$MISMATCH" v1.0.0-rc1

DUPLICATE="${TMP_ROOT}/duplicate"
cp -R "$GOOD" "$DUPLICATE"
sed -n '1p' "${DUPLICATE}/SHA256SUMS" >>"${DUPLICATE}/SHA256SUMS"
expect_fail '重复校验项' bash "$CHECKER" verify-dist "$DUPLICATE" v1.0.0-rc1

SOURCE_DRIFT="${TMP_ROOT}/source-drift"
cp -R "$GOOD" "$SOURCE_DRIFT"
sed -i.bak '4s/0123456789abcdef0123456789abcdef01234567/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' "${SOURCE_DRIFT}/RELEASE-MANIFEST"
rm "${SOURCE_DRIFT}/RELEASE-MANIFEST.bak"
rewrite_sums "$SOURCE_DRIFT"
expect_fail 'manifest sourceHead 与 peeled tag 漂移' bash "$CHECKER" verify-dist "$SOURCE_DRIFT" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567

MANIFEST_ASSET_DRIFT="${TMP_ROOT}/manifest-asset-drift"
cp -R "$GOOD" "$MANIFEST_ASSET_DRIFT"
sed -i.bak '8s/[0-9a-f]\{64\}/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/' "${MANIFEST_ASSET_DRIFT}/RELEASE-MANIFEST"
rm "${MANIFEST_ASSET_DRIFT}/RELEASE-MANIFEST.bak"
rewrite_sums "$MANIFEST_ASSET_DRIFT"
expect_fail 'manifest 声明与真实资产漂移' bash "$CHECKER" verify-dist "$MANIFEST_ASSET_DRIFT" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567

MANIFEST_TAG_DRIFT="${TMP_ROOT}/manifest-tag-drift"
cp -R "$GOOD" "$MANIFEST_TAG_DRIFT"
sed -i.bak '3s/v1.0.0-rc1/v1.0.0-rc2/' "${MANIFEST_TAG_DRIFT}/RELEASE-MANIFEST"
rm "${MANIFEST_TAG_DRIFT}/RELEASE-MANIFEST.bak"
rewrite_sums "$MANIFEST_TAG_DRIFT"
expect_fail 'manifest tag 漂移' bash "$CHECKER" verify-dist "$MANIFEST_TAG_DRIFT" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567

MANIFEST_EXTRA="${TMP_ROOT}/manifest-extra"
cp -R "$GOOD" "$MANIFEST_EXTRA"
printf 'unexpected field\n' >>"${MANIFEST_EXTRA}/RELEASE-MANIFEST"
rewrite_sums "$MANIFEST_EXTRA"
expect_fail 'manifest 额外字段' bash "$CHECKER" verify-dist "$MANIFEST_EXTRA" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567

MANIFEST_DUPLICATE="${TMP_ROOT}/manifest-duplicate"
cp -R "$GOOD" "$MANIFEST_DUPLICATE"
sed -n '8p' "${MANIFEST_DUPLICATE}/RELEASE-MANIFEST" >>"${MANIFEST_DUPLICATE}/RELEASE-MANIFEST"
rewrite_sums "$MANIFEST_DUPLICATE"
expect_fail 'manifest 重复 asset' bash "$CHECKER" verify-dist "$MANIFEST_DUPLICATE" v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567

# ADR 0068 RC1 单资产合同与上述 stable dist 合同并存。新入口只允许
# exact v1.0.0-rc1 + Darwin arm64，任何平台扩展或元数据漂移都 fail closed。
RC1_GOOD="${TMP_ROOT}/rc1-good"
make_rc1_dist "$RC1_GOOD"
bash "$CHECKER" validate-rc1-inputs v1.0.0-rc1 \
  "$RC1_SOURCE_HEAD" "$RC1_BUILD_DATE" "$RC1_GO_VERSION"
verify_rc1_dist "$RC1_GOOD" >/dev/null
[ "$(find "$RC1_GOOD" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')" = 3 ] \
  || fail 'RC1 dist 未精确生成三个封闭文件'
[ "$(wc -l <"${RC1_GOOD}/RELEASE-MANIFEST" | tr -d '[:space:]')" = 8 ] \
  || fail 'RC1 manifest 不是 exact 8-line 合同'
[ "$(wc -l <"${RC1_GOOD}/SHA256SUMS" | tr -d '[:space:]')" = 2 ] \
  || fail 'RC1 SHA256SUMS 不是 exact 2-line 合同'

for rejected_tag in v1.0.0 v1.0.0-rc2 v1.0.1-rc1 v2.0.0-rc1; do
  expect_fail "RC1 拒绝 tag ${rejected_tag}" bash "$CHECKER" validate-rc1-inputs \
    "$rejected_tag" 0123456789abcdef0123456789abcdef01234567 \
    2026-08-28T00:00:00Z go1.26.6
  expect_fail "RC1 manifest 拒绝 tag ${rejected_tag}" bash "$CHECKER" create-rc1-manifest \
    "$RC1_GOOD" "$rejected_tag" 0123456789abcdef0123456789abcdef01234567 \
    2026-08-28T00:00:00Z go1.26.6
  expect_fail "RC1 verify 拒绝 tag ${rejected_tag}" verify_rc1_dist \
    "$RC1_GOOD" "$rejected_tag"
done
expect_fail 'RC1 拒绝非 canonical sourceHead' bash "$CHECKER" validate-rc1-inputs \
  v1.0.0-rc1 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2026-08-28T00:00:00Z go1.26.6
expect_fail 'RC1 拒绝非 canonical buildDate' bash "$CHECKER" validate-rc1-inputs \
  v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567 2026-08-28T00:00:00+00:00 go1.26.6
expect_fail 'RC1 拒绝 Go toolchain 漂移' bash "$CHECKER" validate-rc1-inputs \
  v1.0.0-rc1 0123456789abcdef0123456789abcdef01234567 2026-08-28T00:00:00Z go1.26.7

RC1_MISSING="${TMP_ROOT}/rc1-missing"
cp -R "$RC1_GOOD" "$RC1_MISSING"
rm "${RC1_MISSING}/marshal_1.0.0-rc1_darwin_arm64"
expect_fail 'RC1 缺失唯一 candidate' verify_rc1_dist "$RC1_MISSING"

for extra_name in \
  marshal_1.0.0-rc1_darwin_amd64 \
  marshal_1.0.0-rc1_linux_arm64 \
  marshal_1.0.0-rc1_linux_amd64 \
  marshal_1.0.0_darwin_arm64 \
  marshal_1.0.0-rc2_darwin_arm64; do
  RC1_EXTRA="${TMP_ROOT}/rc1-extra-${extra_name}"
  cp -R "$RC1_GOOD" "$RC1_EXTRA"
  printf 'forbidden\n' >"${RC1_EXTRA}/${extra_name}"
  expect_fail "RC1 拒绝额外资产 ${extra_name}" verify_rc1_dist "$RC1_EXTRA"
done

RC1_NONEXEC="${TMP_ROOT}/rc1-nonexec"
cp -R "$RC1_GOOD" "$RC1_NONEXEC"
chmod 0644 "${RC1_NONEXEC}/marshal_1.0.0-rc1_darwin_arm64"
expect_fail 'RC1 拒绝不可执行 candidate' verify_rc1_dist "$RC1_NONEXEC"

RC1_SYMLINK="${TMP_ROOT}/rc1-symlink"
cp -R "$RC1_GOOD" "$RC1_SYMLINK"
mv "${RC1_SYMLINK}/marshal_1.0.0-rc1_darwin_arm64" "${RC1_SYMLINK}/target"
ln -s target "${RC1_SYMLINK}/marshal_1.0.0-rc1_darwin_arm64"
expect_fail 'RC1 拒绝符号链接 candidate' verify_rc1_dist "$RC1_SYMLINK"

RC1_DIGEST="${TMP_ROOT}/rc1-digest"
cp -R "$RC1_GOOD" "$RC1_DIGEST"
printf 'tampered\n' >>"${RC1_DIGEST}/marshal_1.0.0-rc1_darwin_arm64"
expect_fail 'RC1 拒绝 candidate 摘要漂移' verify_rc1_dist "$RC1_DIGEST"

RC1_SOURCE="${TMP_ROOT}/rc1-source"
cp -R "$RC1_GOOD" "$RC1_SOURCE"
expect_fail 'RC1 拒绝 expected sourceHead 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
expect_fail 'RC1 拒绝 external buildDate 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 "$RC1_SOURCE_HEAD" 2026-08-28T00:00:01Z
expect_fail 'RC1 拒绝 external goVersion 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 "$RC1_SOURCE_HEAD" "$RC1_BUILD_DATE" go1.26.5
expect_fail 'RC1 拒绝 external OS 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 "$RC1_SOURCE_HEAD" "$RC1_BUILD_DATE" \
  "$RC1_GO_VERSION" linux
expect_fail 'RC1 拒绝 external arch 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 "$RC1_SOURCE_HEAD" "$RC1_BUILD_DATE" \
  "$RC1_GO_VERSION" darwin amd64
expect_fail 'RC1 拒绝 external profile 漂移' verify_rc1_dist \
  "$RC1_SOURCE" v1.0.0-rc1 "$RC1_SOURCE_HEAD" "$RC1_BUILD_DATE" \
  "$RC1_GO_VERSION" darwin arm64 unprofiled

RC1_PROFILE="${TMP_ROOT}/rc1-profile"
cp -R "$RC1_GOOD" "$RC1_PROFILE"
sed -i.bak '8s/darwin-local-dogfood/unprofiled/' "${RC1_PROFILE}/RELEASE-MANIFEST"
rm "${RC1_PROFILE}/RELEASE-MANIFEST.bak"
rewrite_rc1_sums "$RC1_PROFILE"
expect_fail 'RC1 拒绝 profile 漂移' verify_rc1_dist "$RC1_PROFILE"

RC1_GO_VERSION_DRIFT="${TMP_ROOT}/rc1-go-version"
cp -R "$RC1_GOOD" "$RC1_GO_VERSION_DRIFT"
sed -i.bak '6s/go1\.26\.6/go1.26.7/' "${RC1_GO_VERSION_DRIFT}/RELEASE-MANIFEST"
rm "${RC1_GO_VERSION_DRIFT}/RELEASE-MANIFEST.bak"
rewrite_rc1_sums "$RC1_GO_VERSION_DRIFT"
expect_fail 'RC1 拒绝 manifest Go toolchain 漂移' verify_rc1_dist "$RC1_GO_VERSION_DRIFT"

RC1_BINARY_VERSION="${TMP_ROOT}/rc1-binary-version"
cp -R "$RC1_GOOD" "$RC1_BINARY_VERSION"
python3 -I -B - "${RC1_BINARY_VERSION}/marshal_1.0.0-rc1_darwin_arm64" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
data = path.read_bytes()
assert data.count(b"1.0.0-rc1") == 1
path.write_bytes(data.replace(b"1.0.0-rc1", b"1.0.0-rc9"))
PY
refresh_rc1_asset_identity "$RC1_BINARY_VERSION"
expect_fail 'RC1 拒绝 binary buildinfo version 漂移' verify_rc1_dist "$RC1_BINARY_VERSION"

RC1_BINARY_ARCH="${TMP_ROOT}/rc1-binary-arch"
cp -R "$RC1_GOOD" "$RC1_BINARY_ARCH"
python3 -I -B - "${RC1_BINARY_ARCH}/marshal_1.0.0-rc1_darwin_arm64" <<'PY'
import pathlib, struct, sys
path = pathlib.Path(sys.argv[1])
data = bytearray(path.read_bytes())
assert struct.unpack_from("<I", data, 4)[0] == 0x0100000C
struct.pack_into("<I", data, 4, 0x01000007)
path.write_bytes(data)
PY
refresh_rc1_asset_identity "$RC1_BINARY_ARCH"
expect_fail 'RC1 拒绝非 arm64 Mach-O header' verify_rc1_dist "$RC1_BINARY_ARCH"

RC1_BINARY_SHELL="${TMP_ROOT}/rc1-binary-shell"
cp -R "$RC1_GOOD" "$RC1_BINARY_SHELL"
printf '#!/bin/sh\nexit 0\n' >"${RC1_BINARY_SHELL}/marshal_1.0.0-rc1_darwin_arm64"
chmod 0755 "${RC1_BINARY_SHELL}/marshal_1.0.0-rc1_darwin_arm64"
refresh_rc1_asset_identity "$RC1_BINARY_SHELL"
expect_fail 'RC1 拒绝 executable shell fixture 冒充 candidate' verify_rc1_dist "$RC1_BINARY_SHELL"

RC1_MANIFEST_EXTRA="${TMP_ROOT}/rc1-manifest-extra"
cp -R "$RC1_GOOD" "$RC1_MANIFEST_EXTRA"
printf 'asset aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1 marshal_1.0.0-rc1_linux_arm64 linux arm64 unprofiled\n' \
  >>"${RC1_MANIFEST_EXTRA}/RELEASE-MANIFEST"
rewrite_rc1_sums "$RC1_MANIFEST_EXTRA"
expect_fail 'RC1 拒绝 manifest 第二资产' verify_rc1_dist "$RC1_MANIFEST_EXTRA"

RC1_SUMS_DUPLICATE="${TMP_ROOT}/rc1-sums-duplicate"
cp -R "$RC1_GOOD" "$RC1_SUMS_DUPLICATE"
sed -n '2p' "${RC1_SUMS_DUPLICATE}/SHA256SUMS" >>"${RC1_SUMS_DUPLICATE}/SHA256SUMS"
expect_fail 'RC1 拒绝重复 checksum' verify_rc1_dist "$RC1_SUMS_DUPLICATE"

RC1_SUMS_ORDER="${TMP_ROOT}/rc1-sums-order"
cp -R "$RC1_GOOD" "$RC1_SUMS_ORDER"
sed -n '2p' "${RC1_GOOD}/SHA256SUMS" >"${RC1_SUMS_ORDER}/SHA256SUMS"
sed -n '1p' "${RC1_GOOD}/SHA256SUMS" >>"${RC1_SUMS_ORDER}/SHA256SUMS"
expect_fail 'RC1 拒绝 checksum 顺序漂移' verify_rc1_dist "$RC1_SUMS_ORDER"

RC1_SUMS_UPPER="${TMP_ROOT}/rc1-sums-upper"
cp -R "$RC1_GOOD" "$RC1_SUMS_UPPER"
awk 'NR == 1 { $1=toupper($1) } { print }' "$RC1_GOOD/SHA256SUMS" \
  >"${RC1_SUMS_UPPER}/SHA256SUMS"
expect_fail 'RC1 拒绝 uppercase checksum' verify_rc1_dist "$RC1_SUMS_UPPER"

RC1_SUMS_TAB="${TMP_ROOT}/rc1-sums-tab"
cp -R "$RC1_GOOD" "$RC1_SUMS_TAB"
sed $'1s/  /\t/' "$RC1_GOOD/SHA256SUMS" >"${RC1_SUMS_TAB}/SHA256SUMS"
expect_fail 'RC1 拒绝 tab checksum separator' verify_rc1_dist "$RC1_SUMS_TAB"

RC1_SUMS_ONE_SPACE="${TMP_ROOT}/rc1-sums-one-space"
cp -R "$RC1_GOOD" "$RC1_SUMS_ONE_SPACE"
sed '1s/  / /' "$RC1_GOOD/SHA256SUMS" >"${RC1_SUMS_ONE_SPACE}/SHA256SUMS"
expect_fail 'RC1 拒绝单空格 checksum separator' verify_rc1_dist "$RC1_SUMS_ONE_SPACE"

RC1_SUMS_TRAILING="${TMP_ROOT}/rc1-sums-trailing"
cp -R "$RC1_GOOD" "$RC1_SUMS_TRAILING"
sed '1s/$/ /' "$RC1_GOOD/SHA256SUMS" >"${RC1_SUMS_TRAILING}/SHA256SUMS"
expect_fail 'RC1 拒绝 checksum 尾随空白' verify_rc1_dist "$RC1_SUMS_TRAILING"

TAG_REPO="${TMP_ROOT}/tag-repo"
mkdir -p "$TAG_REPO"
git -C "$TAG_REPO" init -q
git -C "$TAG_REPO" config user.name 'Release Contract Test'
git -C "$TAG_REPO" config user.email 'release-contract@example.invalid'
printf 'candidate\n' >"${TAG_REPO}/candidate.txt"
git -C "$TAG_REPO" add candidate.txt
GIT_AUTHOR_DATE=2026-08-28T01:02:03Z GIT_COMMITTER_DATE=2026-08-28T01:02:03Z \
  git -C "$TAG_REPO" commit -qm candidate
TAG_HEAD="$(git -C "$TAG_REPO" rev-parse HEAD)"
[ "$(bash "$CHECKER" build-date "$TAG_REPO" "$TAG_HEAD")" = 2026-08-28T01:02:03Z ] \
  || fail 'commit canonical UTC buildDate 推导错误'
TAG_DIST="${TMP_ROOT}/tag-dist"
make_dist "$TAG_DIST" v1.0.0-rc1 "$TAG_HEAD"
bash "$CHECKER" candidate-tag-message "$TAG_DIST" v1.0.0-rc1 "$TAG_HEAD" >"${TMP_ROOT}/tag-message"
git -C "$TAG_REPO" tag -a v1.0.0-rc1 -F "${TMP_ROOT}/tag-message"
bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

replace_candidate_tag_with_raw_message() {
  local message_file="$1" tag_object
  git -C "$TAG_REPO" update-ref -d refs/tags/v1.0.0-rc1 >/dev/null 2>&1 || true
  tag_object="$({
    printf 'object %s\n' "$TAG_HEAD"
    printf 'type commit\n'
    printf 'tag v1.0.0-rc1\n'
    printf 'tagger Release Contract Test <release-contract@example.invalid> 1787880000 +0000\n\n'
    cat "$message_file"
  } | git -C "$TAG_REPO" hash-object -t tag -w --stdin)"
  git -C "$TAG_REPO" update-ref refs/tags/v1.0.0-rc1 "$tag_object"
}

git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null
git -C "$TAG_REPO" tag v1.0.0-rc1
expect_fail 'lightweight candidate tag' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1
git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null
cp "${TMP_ROOT}/tag-message" "${TMP_ROOT}/duplicate-tag-message"
sed -n '/^marshal-candidate-source-head:/p' "${TMP_ROOT}/tag-message" >>"${TMP_ROOT}/duplicate-tag-message"
git -C "$TAG_REPO" tag -a v1.0.0-rc1 -F "${TMP_ROOT}/duplicate-tag-message"
expect_fail 'candidate trailer 重复' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1
git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null

cp "${TMP_ROOT}/tag-message" "${TMP_ROOT}/extra-tag-message"
printf 'unexpected tag note\n' >>"${TMP_ROOT}/extra-tag-message"
replace_candidate_tag_with_raw_message "${TMP_ROOT}/extra-tag-message"
expect_fail 'candidate tag 未知额外行' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

awk 'NR == 6 { print "" } { print }' "${TMP_ROOT}/tag-message" >"${TMP_ROOT}/interior-blank-tag-message"
replace_candidate_tag_with_raw_message "${TMP_ROOT}/interior-blank-tag-message"
expect_fail 'candidate tag 内部空行' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

cp "${TMP_ROOT}/tag-message" "${TMP_ROOT}/trailing-blank-tag-message"
printf '\n' >>"${TMP_ROOT}/trailing-blank-tag-message"
replace_candidate_tag_with_raw_message "${TMP_ROOT}/trailing-blank-tag-message"
expect_fail 'candidate tag 尾随空行' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

sed -n '1,5p' "${TMP_ROOT}/tag-message" >"${TMP_ROOT}/nul-tag-message"
printf '\000' >>"${TMP_ROOT}/nul-tag-message"
sed -n '6p' "${TMP_ROOT}/tag-message" >>"${TMP_ROOT}/nul-tag-message"
replace_candidate_tag_with_raw_message "${TMP_ROOT}/nul-tag-message"
expect_fail 'candidate tag NUL byte' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

git -C "$TAG_REPO" update-ref -d refs/tags/v1.0.0-rc1
git -C "$TAG_REPO" tag -a v1.0.0-rc1 -F "${TMP_ROOT}/tag-message"
printf 'cross-host drift\n' >>"${TAG_DIST}/marshal_1.0.0-rc1_darwin_arm64"
bash "$CHECKER" create-manifest "$TAG_DIST" v1.0.0-rc1 "$TAG_HEAD" 2026-08-28T00:00:00Z go1.26.6
rewrite_sums "$TAG_DIST"
expect_fail '跨主机 candidate bytes 漂移' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

printf '[release-contract-test] PASS\n'
