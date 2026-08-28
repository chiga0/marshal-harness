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
  local dir="$1" tag="$2" version name digest
  version="${tag#v}"
  mkdir -p "$dir"
  : >"${dir}/SHA256SUMS"
  for name in \
    "marshal_${version}_darwin_amd64" \
    "marshal_${version}_darwin_arm64" \
    "marshal_${version}_linux_amd64" \
    "marshal_${version}_linux_arm64"; do
    printf '#!/bin/sh\nprintf "fixture %s\\n"\n' "$name" >"${dir}/${name}"
    chmod 0755 "${dir}/${name}"
    digest="$(sha256_fixture "${dir}/${name}")"
    printf '%s  %s\n' "$digest" "$name" >>"${dir}/SHA256SUMS"
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
bash "$CHECKER" verify-dist "$GOOD" v1.0.0-rc1 >/dev/null

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

printf '[release-contract-test] PASS\n'
