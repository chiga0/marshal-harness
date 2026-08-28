#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT
FAKE_GO="${TMP_ROOT}/fake-go"
LOG="${TMP_ROOT}/go.log"
DIST="${TMP_ROOT}/dist"

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
for token in $ldflags; do
  case "$token" in
    *.selfProfile=*) profile="${token#*=}" ;;
  esac
done
printf '%s/%s selfProfile=%s\n' "${GOOS:?}" "${GOARCH:?}" "$profile" >>"${DIST_PROFILE_TEST_LOG:?}"
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
  grep -Fx "${target} selfProfile=darwin-local-dogfood" "$LOG" >/dev/null \
    || fail "${target} 未冻结 darwin-local-dogfood"
done
for target in linux/amd64 linux/arm64; do
  grep -Fx "${target} selfProfile=unprofiled" "$LOG" >/dev/null \
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

printf '[dist-profile-test] PASS\n'
