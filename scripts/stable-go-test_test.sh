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
if [[ "$(/usr/bin/uname -s)" != "Darwin" ]]; then
  exit 0
fi

FIXTURE="$(mktemp -d)"
trap 'rm -rf "$FIXTURE"' EXIT
mkdir -p "$FIXTURE/repository/scripts" "$FIXTURE/repository/bin" "$FIXTURE/custom"
cp "$ROOT/scripts/stable-go-test.sh" "$FIXTURE/repository/scripts/stable-go-test.sh"
printf '#!/bin/sh\nexit 99\n' >"$FIXTURE/repository/bin/marshal"
printf '#!/bin/sh\nexit 98\n' >"$FIXTURE/custom/marshal-custom"
chmod 700 "$FIXTURE/repository/bin/marshal" "$FIXTURE/custom/marshal-custom"
cat >"$FIXTURE/fake-go" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >"$STABLE_GO_TEST_ARGUMENT_LOG"
EOF
chmod 700 "$FIXTURE/fake-go"

STABLE_GO_TEST_ARGUMENT_LOG="$FIXTURE/arguments" \
  MARSHAL_RUNNER="$FIXTURE/custom/marshal-custom" \
  GO="$FIXTURE/fake-go" \
  bash "$FIXTURE/repository/scripts/stable-go-test.sh" ./internal/stablegotest

CUSTOM_DIGEST="$(/usr/bin/shasum -a 256 "$FIXTURE/custom/marshal-custom" | /usr/bin/awk '{print $1}')"
EXPECTED_EXEC="'$FIXTURE/custom/marshal-custom' __go-test-exec --slot-root '$FIXTURE/custom/test' --marshal-sha256 $CUSTOM_DIGEST"
if ! grep -Fx -- "-exec=$EXPECTED_EXEC" "$FIXTURE/arguments" >/dev/null; then
  printf '%s\n' 'custom runner or derived slot was not forwarded to go test' >&2
  exit 1
fi
if grep -F -- "$FIXTURE/repository/bin/marshal" "$FIXTURE/arguments" >/dev/null; then
  printf '%s\n' 'stale default runner leaked into go test arguments' >&2
  exit 1
fi
