#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
FAKE_GO="${TMP_ROOT}/fake-go"
LOG="${TMP_ROOT}/go.log"
DIST="${TMP_ROOT}/dist"
RC1_DIST="${TMP_ROOT}/rc1-dist"

fail() {
  printf '[dist-profile-test] FAIL: %s\n' "$*" >&2
  exit 1
}

cat >"$FAKE_GO" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" != env ] || { [ "${2:-}" = GOVERSION ] && printf '%s\n' "${FAKE_GO_VERSION:-go1.26.6}"; exit; }
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
profile=''
version=''
commit=''
build_date=''
for token in $ldflags; do
  case "$token" in
    *.selfProfile=*) profile="${token#*=}" ;;
    *.version=*) version="${token#*=}" ;;
    *.commit=*) commit="${token#*=}" ;;
    *.buildDate=*) build_date="${token#*=}" ;;
  esac
done
printf '%s/%s selfProfile=%s version=%s commit=%s buildDate=%s\n' \
  "${GOOS:?}" "${GOARCH:?}" "$profile" "$version" "$commit" "$build_date" \
  >>"${DIST_PROFILE_TEST_LOG:?}"
printf '#!/bin/sh\nexit 0\n' >"$out"
chmod 0755 "$out"
FAKE
chmod 0755 "$FAKE_GO"

DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist \
  GO="$FAKE_GO" \
  DIST_DIR="$DIST" \
  VERSION=1.0.0-rc1 \
  COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00Z >/dev/null

[ "$(wc -l <"$LOG" | tr -d '[:space:]')" = 4 ] || fail 'dist 未构建四个平台资产'
for target in darwin/arm64 darwin/amd64; do
  grep -F "${target} selfProfile=darwin-local-dogfood " "$LOG" >/dev/null \
    || fail "${target} 未冻结 darwin-local-dogfood"
done
for target in linux/amd64 linux/arm64; do
  grep -F "${target} selfProfile=unprofiled " "$LOG" >/dev/null \
    || fail "${target} 未冻结 unprofiled"
done
[ "$(find "$DIST" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')" = 6 ] \
  || fail 'dist 产物集合不封闭'

if FAKE_GO_VERSION=go1.26.7 DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist \
  GO="$FAKE_GO" DIST_DIR="${TMP_ROOT}/wrong-toolchain" VERSION=1.0.0-rc1 \
  COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00Z >/dev/null 2>&1; then
  fail 'release toolchain 漂移应 fail closed'
fi

# ADR 0068 专用 target 不改变上述 stable dist 四平台语义，但自身只能
# 调用一次 Darwin arm64 build，并精确冻结四个 build metadata 输入。
: >"$LOG"
DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist-rc1 \
  GO="$FAKE_GO" \
  DIST_DIR="$RC1_DIST" \
  VERSION=1.0.0-rc1 \
  COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00Z >/dev/null

[ "$(wc -l <"$LOG" | tr -d '[:space:]')" = 1 ] || fail 'dist-rc1 未严格只构建一次'
grep -Fx 'darwin/arm64 selfProfile=darwin-local-dogfood version=1.0.0-rc1 commit=0123456789abcdef0123456789abcdef01234567 buildDate=2026-08-28T00:00:00Z' \
  "$LOG" >/dev/null || fail 'dist-rc1 build identity 没有精确冻结'
[ "$(find "$RC1_DIST" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')" = 3 ] \
  || fail 'dist-rc1 产物集必须只有 candidate/manifest/checksum'
[ -x "${RC1_DIST}/marshal_1.0.0-rc1_darwin_arm64" ] \
  || fail 'dist-rc1 缺少唯一 Darwin arm64 candidate'
find "$RC1_DIST" -mindepth 1 -maxdepth 1 -print | \
  grep -E '(linux|amd64|marshal_1\.0\.0_)' >/dev/null && fail 'dist-rc1 混入 Linux/amd64/stable 资产'

assert_rc1_admission_failure() {
  local label="$1"
  shift
  local before
  before="$(wc -l <"$LOG" | tr -d '[:space:]')"
  if DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist-rc1 \
    GO="$FAKE_GO" DIST_DIR="${TMP_ROOT}/rejected-${label}" "$@" >/dev/null 2>&1; then
    fail "dist-rc1 应拒绝 ${label}"
  fi
  [ "$(wc -l <"$LOG" | tr -d '[:space:]')" = "$before" ] \
    || fail "dist-rc1 在拒绝 ${label} 前已调用 build"
  [ ! -e "${TMP_ROOT}/rejected-${label}" ] \
    || fail "dist-rc1 在拒绝 ${label} 后留下资产"
}

for rejected_version in 1.0.0 1.0.0-rc2 1.0.1-rc1 2.0.0-rc1; do
  assert_rc1_admission_failure "version-${rejected_version}" \
    VERSION="$rejected_version" \
    COMMIT=0123456789abcdef0123456789abcdef01234567 \
    BUILD_DATE=2026-08-28T00:00:00Z
done
assert_rc1_admission_failure bad-source-head \
  VERSION=1.0.0-rc1 COMMIT=unknown BUILD_DATE=2026-08-28T00:00:00Z
assert_rc1_admission_failure bad-build-date \
  VERSION=1.0.0-rc1 COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00+00:00

if FAKE_GO_VERSION=go1.26.7 DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist-rc1 \
  GO="$FAKE_GO" DIST_DIR="${TMP_ROOT}/rc1-wrong-toolchain" VERSION=1.0.0-rc1 \
  COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00Z >/dev/null 2>&1; then
  fail 'dist-rc1 toolchain 漂移应 fail closed'
fi
[ ! -e "${TMP_ROOT}/rc1-wrong-toolchain" ] || fail 'toolchain 漂移后留下 RC1 资产'

PREEXISTING_RC1="${TMP_ROOT}/rc1-preexisting"
mkdir "$PREEXISTING_RC1"
printf 'keep\n' >"${PREEXISTING_RC1}/sentinel"
before_preexisting="$(wc -l <"$LOG" | tr -d '[:space:]')"
if DIST_PROFILE_TEST_LOG="$LOG" make -C "$ROOT" dist-rc1 \
  GO="$FAKE_GO" DIST_DIR="$PREEXISTING_RC1" VERSION=1.0.0-rc1 \
  COMMIT=0123456789abcdef0123456789abcdef01234567 \
  BUILD_DATE=2026-08-28T00:00:00Z >/dev/null 2>&1; then
  fail 'dist-rc1 应拒绝复用既有输出目录'
fi
[ "$(wc -l <"$LOG" | tr -d '[:space:]')" = "$before_preexisting" ] \
  || fail 'dist-rc1 在拒绝既有输出目录前已调用 build'
[ "$(find "$PREEXISTING_RC1" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d '[:space:]')" = 1 ] \
  && [ "$(cat "${PREEXISTING_RC1}/sentinel")" = keep ] \
  || fail 'dist-rc1 改写了既有输出目录'

printf '[dist-profile-test] PASS\n'
