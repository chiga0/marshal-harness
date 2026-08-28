#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
CUSTOM_BINARY="artifacts/custom-marshal"
CUSTOM_ABSOLUTE="$ROOT/$CUSTOM_BINARY"
DRY_RUN="$(make -n -C "$ROOT" test BINARY="$CUSTOM_BINARY")"
case "$DRY_RUN" in
  *"MARSHAL_RUNNER=\"$CUSTOM_ABSOLUTE\""*) ;;
  *)
    printf '%s\n' 'custom BINARY was not forwarded as an absolute MARSHAL_RUNNER' >&2
    exit 1
    ;;
esac
case "$DRY_RUN" in
  *"MARSHAL_RUNNER=\"$ROOT/bin/marshal\""*)
    printf '%s\n' 'test recipe retained the stale default runner' >&2
    exit 1
    ;;
esac

bash -n "$ROOT/scripts/stable-go-test.sh"
FIXTURE="$(mktemp -d)"
trap 'rm -rf "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/repository/bin" "$FIXTURE/custom"
printf '#!/bin/sh\nexit 99\n' >"$FIXTURE/repository/bin/marshal"
printf '#!/bin/sh\nexit 98\n' >"$FIXTURE/custom/marshal-custom"
chmod 700 "$FIXTURE/repository/bin/marshal" "$FIXTURE/custom/marshal-custom"
source "$ROOT/scripts/stable-go-test.sh"
for LEGAL_GOFLAGS in '-tags=contains-exec -count=1' '-ldflags=-X=example/path-exec -p=2' "'-tags=quoted-exec' -v"; do
  if ! stable_go_test_validate_goflags "$LEGAL_GOFLAGS"; then
    printf '%s\n' "legal GOFLAGS was rejected: $LEGAL_GOFLAGS" >&2
    exit 1
  fi
done
for CONFLICTING_GOFLAGS in '-exec=/tmp/other' '--exec /tmp/other' '"-exec=/tmp/quoted helper"'; do
  if stable_go_test_validate_goflags "$CONFLICTING_GOFLAGS" 2>/dev/null; then
    printf '%s\n' "conflicting GOFLAGS was accepted: $CONFLICTING_GOFLAGS" >&2
    exit 1
  fi
done
if stable_go_test_validate_goflags '"unterminated' 2>/dev/null; then
  printf '%s\n' 'malformed GOFLAGS quote was accepted' >&2
  exit 1
fi

GOFLAGS='-tags=contains-exec' stable_go_test_prepare_darwin "$FIXTURE/repository" "$FIXTURE/custom/marshal-custom" ./internal/stablegotest
/usr/bin/printf '%s\n' "${STABLE_GO_TEST_ARGV[@]}" >"$FIXTURE/arguments"

CUSTOM_DIRECTORY="$(cd "$FIXTURE/custom" && pwd -P)"
CUSTOM_RUNNER="$CUSTOM_DIRECTORY/marshal-custom"
CUSTOM_DIGEST="$(/usr/bin/shasum -a 256 "$CUSTOM_RUNNER" | /usr/bin/awk '{print $1}')"
EXPECTED_EXEC="'$CUSTOM_RUNNER' __go-test-exec --slot-root '$CUSTOM_DIRECTORY/test' --marshal-sha256 $CUSTOM_DIGEST"
if ! grep -Fx -- "-exec=$EXPECTED_EXEC" "$FIXTURE/arguments" >/dev/null; then
  printf '%s\n' 'custom runner or derived slot was not forwarded to go test' >&2
  exit 1
fi
if grep -F -- "$FIXTURE/repository/bin/marshal" "$FIXTURE/arguments" >/dev/null; then
  printf '%s\n' 'stale default runner leaked into go test arguments' >&2
  exit 1
fi
