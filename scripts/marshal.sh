#!/usr/bin/env bash
set -euo pipefail
marshal_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
exec node "$marshal_root/packages/task-local/main.mjs" "$@"
