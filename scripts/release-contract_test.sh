#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECKER="${ROOT}/scripts/release-contract.sh"
TMP_ROOT="$(mktemp -d)"
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

git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null
git -C "$TAG_REPO" tag v1.0.0-rc1
expect_fail 'lightweight candidate tag' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1
git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null
cp "${TMP_ROOT}/tag-message" "${TMP_ROOT}/duplicate-tag-message"
sed -n '/^marshal-candidate-source-head:/p' "${TMP_ROOT}/tag-message" >>"${TMP_ROOT}/duplicate-tag-message"
git -C "$TAG_REPO" tag -a v1.0.0-rc1 -F "${TMP_ROOT}/duplicate-tag-message"
expect_fail 'candidate trailer 重复' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1
git -C "$TAG_REPO" tag -d v1.0.0-rc1 >/dev/null
git -C "$TAG_REPO" tag -a v1.0.0-rc1 -F "${TMP_ROOT}/tag-message"
printf 'cross-host drift\n' >>"${TAG_DIST}/marshal_1.0.0-rc1_darwin_arm64"
bash "$CHECKER" create-manifest "$TAG_DIST" v1.0.0-rc1 "$TAG_HEAD" 2026-08-28T00:00:00Z go1.26.6
rewrite_sums "$TAG_DIST"
expect_fail '跨主机 candidate bytes 漂移' bash "$CHECKER" verify-candidate-tag "$TAG_REPO" "$TAG_DIST" v1.0.0-rc1

printf '[release-contract-test] PASS\n'
