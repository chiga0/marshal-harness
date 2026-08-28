#!/usr/bin/env bash
set -euo pipefail

stable_go_test_prepare_darwin() {
  local repository_root="$1"
  local marshal_runner="$2"
  shift 2
  marshal_runner="${marshal_runner:-$repository_root/bin/marshal}"
  case "$marshal_runner" in
    /*) ;;
    *)
      printf '%s\n' '[stable-go-test] MARSHAL_RUNNER 必须是绝对路径。' >&2
      return 3
      ;;
  esac
  if [[ ! -f "$marshal_runner" || ! -x "$marshal_runner" || -L "$marshal_runner" ]]; then
    printf '%s\n' '[stable-go-test] 指定的固定 Marshal 不可用；请先运行 make build。' >&2
    return 3
  fi
  local runner_directory slot_root marshal_digest exec_value
  runner_directory="$(cd "$(dirname "$marshal_runner")" && pwd -P)"
  marshal_runner="$runner_directory/$(basename "$marshal_runner")"
  slot_root="$runner_directory/test"
  case "${GOFLAGS:-}" in
    *-exec*)
      printf '%s\n' '[stable-go-test] GOFLAGS 已包含 -exec；拒绝覆盖固定执行器。' >&2
      return 3
      ;;
  esac
  case "$marshal_runner$slot_root" in
    *\'*|*\"*)
      printf '%s\n' '[stable-go-test] 固定执行路径含不受支持的引号。' >&2
      return 3
      ;;
  esac
  exec_value="'$marshal_runner' __go-test-exec --slot-root '$slot_root'"
  marshal_digest="$(/usr/bin/shasum -a 256 "$marshal_runner" | /usr/bin/awk '{print $1}')"
  if [[ ! "$marshal_digest" =~ ^[0-9a-f]{64}$ ]]; then
    printf '%s\n' '[stable-go-test] 固定 Marshal 摘要不可用。' >&2
    return 3
  fi
  exec_value="$exec_value --marshal-sha256 $marshal_digest"
  STABLE_GO_TEST_ARGV=(test "-exec=$exec_value" "$@")
}

stable_go_test_main() {
  local repository_root go_command
  repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
  go_command="${GO:-go}"
  if [[ "$(/usr/bin/uname -s)" != "Darwin" ]]; then
    exec "$go_command" test "$@"
  fi
  stable_go_test_prepare_darwin "$repository_root" "${MARSHAL_RUNNER:-}" "$@"
  exec "$go_command" "${STABLE_GO_TEST_ARGV[@]}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  stable_go_test_main "$@"
fi
