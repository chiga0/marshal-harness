#!/usr/bin/env bash
set -euo pipefail

REPOSITORY_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
GO_COMMAND="${GO:-go}"

if [[ "$(/usr/bin/uname -s)" != "Darwin" ]]; then
  exec "$GO_COMMAND" test "$@"
fi

MARSHAL_RUNNER="$REPOSITORY_ROOT/bin/marshal"
SLOT_ROOT="$REPOSITORY_ROOT/bin/test"
if [[ ! -f "$MARSHAL_RUNNER" || ! -x "$MARSHAL_RUNNER" || -L "$MARSHAL_RUNNER" ]]; then
  printf '%s\n' '[stable-go-test] 固定 bin/marshal 不可用；请先运行 make build。' >&2
  exit 3
fi
case "${GOFLAGS:-}" in
  *-exec*)
    printf '%s\n' '[stable-go-test] GOFLAGS 已包含 -exec；拒绝覆盖固定执行器。' >&2
    exit 3
    ;;
esac
case "$MARSHAL_RUNNER$SLOT_ROOT" in
  *\'*|*\"*)
    printf '%s\n' '[stable-go-test] 固定执行路径含不受支持的引号。' >&2
    exit 3
    ;;
esac

EXEC_VALUE="'$MARSHAL_RUNNER' __go-test-exec --slot-root '$SLOT_ROOT'"
MARSHAL_DIGEST="$(/usr/bin/shasum -a 256 "$MARSHAL_RUNNER" | /usr/bin/awk '{print $1}')"
if [[ ! "$MARSHAL_DIGEST" =~ ^[0-9a-f]{64}$ ]]; then
  printf '%s\n' '[stable-go-test] 固定 Marshal 摘要不可用。' >&2
  exit 3
fi
EXEC_VALUE="$EXEC_VALUE --marshal-sha256 $MARSHAL_DIGEST"
exec "$GO_COMMAND" test -exec="$EXEC_VALUE" "$@"
