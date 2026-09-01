#!/usr/bin/env bash
# Mac-first v1 RC Pi canary。所有权威 Marshal 调用只使用仓库内固定的
# ./bin/marshal；运行证据保存在源仓库已忽略的 .marshal/release-canary/。

set -euo pipefail
umask 077

SOURCE_ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
MARSHAL_BIN="${SOURCE_ROOT}/bin/marshal"
RELEASE_CHECKER="${SOURCE_ROOT}/scripts/release-contract.sh"
RELEASE_DIST_ROOT="${SOURCE_ROOT}/dist"
CANONICAL_REMOTE="https://github.com/chiga0/marshal-harness.git"
CANARY_REMOTE="https://example.invalid/marshal-release-canary.git"
PI_VERSION="0.84.4"
PI_NODE_VERSION="v24.15.0"
PI_MODEL="pai-eas/qwen3.7-plus"
PI_BIN_DEFAULT="/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/pi"
PI_NODE_DEFAULT="/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/bin/node"
PI_NODE_SHA256_DEFAULT="3200fbd9f7fd4410426dd541e10d1ab829d3472f270d743c7fabd1696c03fe32"
PI_BUNDLE_DEFAULT="/Users/gawain/.local/share/fnm/node-versions/v24.15.0/installation/lib/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
PI_BUNDLE_SHA256_DEFAULT="5406c369954516fb56879d685e082ff9095cd6e06e41af406f394942377fd4bf"
PYTHON_BIN="/usr/bin/python3"
GIT_BIN="/usr/bin/git"

COMMAND="${1:-}"
if [ -n "$COMMAND" ]; then
  shift
fi
RUN_ID=""
EXPECTED_HEAD=""
EXPECTED_VERSION=""
DECISION_PATH=""
EXPECTED_STATE=""

die() {
  printf '[release-canary] FAIL: %s\n' "$*" >&2
  exit 1
}

note() {
  printf '[release-canary] %s\n' "$*"
}

usage() {
  cat >&2 <<'EOF'
用法：
  scripts/release-canary.sh run \
    --run-id RUN_ID --expected-head 40_HEX --expected-version VERSION
  scripts/release-canary.sh status \
    --run-id RUN_ID --expected-head 40_HEX --expected-version VERSION \
    --expect REVIEW_PENDING|ACCEPTED
  scripts/release-canary.sh finalize \
    --run-id RUN_ID --expected-head 40_HEX --expected-version VERSION \
    --decision /absolute/path/to/review-decision.json
EOF
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --run-id)
      [ "$#" -ge 2 ] || usage
      RUN_ID="$2"
      shift 2
      ;;
    --expected-head)
      [ "$#" -ge 2 ] || usage
      EXPECTED_HEAD="$2"
      shift 2
      ;;
    --expected-version)
      [ "$#" -ge 2 ] || usage
      EXPECTED_VERSION="$2"
      shift 2
      ;;
    --decision)
      [ "$#" -ge 2 ] || usage
      DECISION_PATH="$2"
      shift 2
      ;;
    --expect)
      [ "$#" -ge 2 ] || usage
      EXPECTED_STATE="$2"
      shift 2
      ;;
    *) usage ;;
  esac
done

case "$COMMAND" in
  run|status|finalize) ;;
  *) usage ;;
esac

[ -n "$RUN_ID" ] && [ -n "$EXPECTED_HEAD" ] && [ -n "$EXPECTED_VERSION" ] || usage
if ! [[ "$RUN_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$ ]]; then
  die "Run ID 不满足 Marshal ID 契约"
fi
if ! [[ "$EXPECTED_HEAD" =~ ^[0-9a-f]{40}$ ]]; then
  die "--expected-head 必须是 40 位小写 Git commit"
fi
if ! [[ "$EXPECTED_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+-rc[1-9][0-9]*$ ]]; then
  die "--expected-version 必须是无 v 前缀的 RC 版本（例如 1.0.0-rc1）"
fi
case "$COMMAND" in
  run)
    [ -z "$DECISION_PATH" ] && [ -z "$EXPECTED_STATE" ] || usage
    ;;
  status)
    [ -z "$DECISION_PATH" ] || usage
    case "$EXPECTED_STATE" in REVIEW_PENDING|ACCEPTED) ;; *) usage ;; esac
    ;;
  finalize)
    [ -n "$DECISION_PATH" ] && [ -z "$EXPECTED_STATE" ] || usage
    ;;
esac

# 兼容用 legacy executor 会绕过 final binary 的 production composition。
# 空值与未设置等价；随后显式 unset，实际 composition 仍完全由固定 binary 决定。
if [ -n "${MARSHAL_WORKER_EXECUTOR:-}" ]; then
  die "拒绝继承 MARSHAL_WORKER_EXECUTOR；release canary 必须使用 final binary 的 production composition"
fi
unset MARSHAL_WORKER_EXECUTOR

TEST_MODE="${MARSHAL_RELEASE_CANARY_TEST_MODE:-0}"
if [ "$TEST_MODE" = 1 ]; then
  case "$SOURCE_ROOT" in
    */release-canary-test.*) ;;
    *) die "测试模式仅允许在 release-canary-test.* fixture 根运行" ;;
  esac
  PI_BIN="${MARSHAL_RELEASE_CANARY_PI_BIN:?测试模式缺少 MARSHAL_RELEASE_CANARY_PI_BIN}"
  PI_NODE="${MARSHAL_RELEASE_CANARY_PI_NODE:?测试模式缺少 MARSHAL_RELEASE_CANARY_PI_NODE}"
  PI_NODE_SHA256="${MARSHAL_RELEASE_CANARY_PI_NODE_SHA256:?测试模式缺少 MARSHAL_RELEASE_CANARY_PI_NODE_SHA256}"
  PI_BUNDLE="${MARSHAL_RELEASE_CANARY_PI_BUNDLE:?测试模式缺少 MARSHAL_RELEASE_CANARY_PI_BUNDLE}"
  PI_BUNDLE_SHA256="${MARSHAL_RELEASE_CANARY_PI_BUNDLE_SHA256:?测试模式缺少 MARSHAL_RELEASE_CANARY_PI_BUNDLE_SHA256}"
else
  [ "$TEST_MODE" = 0 ] || die "MARSHAL_RELEASE_CANARY_TEST_MODE 非法"
  [ -z "${MARSHAL_RELEASE_CANARY_PI_BIN+x}" ] && \
    [ -z "${MARSHAL_RELEASE_CANARY_PI_NODE+x}" ] && \
    [ -z "${MARSHAL_RELEASE_CANARY_PI_NODE_SHA256+x}" ] && \
    [ -z "${MARSHAL_RELEASE_CANARY_PI_BUNDLE+x}" ] && \
    [ -z "${MARSHAL_RELEASE_CANARY_PI_BUNDLE_SHA256+x}" ] || \
    die "生产 canary 禁止覆盖冻结的 Pi 路径或摘要"
  [ "$(uname -s)" = Darwin ] || die "该 release canary 仅允许在 macOS 运行"
  case "${MARSHAL_CANARY_PI_IDENTITY:-pinned}" in
    pinned)
      PI_BIN="$PI_BIN_DEFAULT"
      PI_NODE="$PI_NODE_DEFAULT"
      PI_BUNDLE="$PI_BUNDLE_DEFAULT"
      ;;
    custom)
      # Pi 路径与 model 由供给环境带入（GH runner 等无固定用户布局的宿主）。
      # 字节身份仍由冻结的 SHA-256 常数锚定：供给路径必须解析到同 digest
      # 的对象上，任何漂移都会在 assert_pi_identity 处 fail closed。
      PI_NODE="${MARSHAL_CANARY_PI_NODE:?custom Pi identity 缺少 MARSHAL_CANARY_PI_NODE}"
      PI_BIN="${MARSHAL_CANARY_PI_BIN:?custom Pi identity 缺少 MARSHAL_CANARY_PI_BIN}"
      PI_BUNDLE="${MARSHAL_CANARY_PI_BUNDLE:?custom Pi identity 缺少 MARSHAL_CANARY_PI_BUNDLE}"
      PI_MODEL="${MARSHAL_CANARY_PI_MODEL:?custom Pi identity 缺少 MARSHAL_CANARY_PI_MODEL}"
      ;;
    *) die "MARSHAL_CANARY_PI_IDENTITY 只允许 pinned|custom" ;;
  esac
  PI_NODE_SHA256="$PI_NODE_SHA256_DEFAULT"
  PI_BUNDLE_SHA256="$PI_BUNDLE_SHA256_DEFAULT"
fi

# release-contract 的 RC1 Mach-O/Go 检查必须使用启动前冻结的固定
# toolchain 路径。set_runtime_environment 会故意收缩 PATH，不能在其后
# 通过 command -v 发现可执行文件，也不能允许 Go 自动下载新 toolchain。
GO_BIN=""
if [ "$TEST_MODE" = 0 ]; then
  # 这些变量会改变 Go 的模块/工具链解析根，不能由调用环境注入。
  # 生产 canary 宁可短路，也不接受被覆盖的 GOPATH/GOMODCACHE/GOENV。
  [ -z "${GOPATH+x}" ] && [ -z "${GOMODCACHE+x}" ] && [ -z "${GOENV+x}" ] && \
    [ -z "${GOTOOLCHAIN+x}" ] || die "生产 canary 禁止继承 GOPATH/GOMODCACHE/GOENV/GOTOOLCHAIN"
  go_user_home="${MARSHAL_RELEASE_CANARY_GO_USER_HOME:-${SOURCE_ROOT%%/Documents/*}}"
  [[ "$go_user_home" = /Users/* ]] || die "无法从 canonical source root 推导固定用户 Home"
  required_go_version="$(/usr/bin/sed -n -E 's/^toolchain[[:space:]]+(go[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$/\1/p' "${SOURCE_ROOT}/go.mod")"
  [ -n "$required_go_version" ] || die "go.mod 缺少精确 toolchain 版本"
  go_launchers=()
  if self_go="$(command -v go 2>/dev/null)" && [ -n "$self_go" ]; then
    go_launchers+=("$self_go")
  fi
  go_launchers+=(/opt/homebrew/bin/go /usr/local/bin/go /usr/local/go/bin/go)
  for go_launcher in "${go_launchers[@]}"; do
    [ -x "$go_launcher" ] || continue
    go_path="$(/usr/bin/env -i HOME="$go_user_home" PATH="$(/usr/bin/dirname "$go_launcher"):/usr/bin:/bin:/usr/sbin:/sbin" GOTOOLCHAIN=local "$go_launcher" env GOPATH 2>/dev/null || true)"
    [ -n "$go_path" ] || continue
    # 只接受 GOPATH 中已经存在、且名称精确匹配 go.mod 的 direct
    # toolchain；不会让 go 自动下载或通过 PATH 选择临时版本。
    candidate_go="${go_path}/pkg/mod/golang.org/toolchain@v0.0.1-${required_go_version}.darwin-arm64/bin/go"
    [ -f "$candidate_go" ] && [ -x "$candidate_go" ] && [ ! -L "$candidate_go" ] || continue
    GO_BIN="$($PYTHON_BIN -I -B -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$candidate_go")"
    [ "$GO_BIN" = "$candidate_go" ] || continue
    [ "$(GOTOOLCHAIN=local "$GO_BIN" env GOVERSION 2>/dev/null)" = "$required_go_version" ] || { GO_BIN=""; continue; }
    break
  done
  [ -n "$GO_BIN" ] || die "缺少与 go.mod 精确匹配且已安装的固定 Go toolchain：$required_go_version"
fi

CANARY_ROOT="${SOURCE_ROOT}/.marshal/release-canary/${RUN_ID}"
CONTROL_ROOT="${CANARY_ROOT}/control"
REPOSITORY_ROOT="${CANARY_ROOT}/repository"
IDENTITY_PATH="${CONTROL_ROOT}/identity.json"
ACTIVATION_PATH="${CONTROL_ROOT}/local-dogfood-activation.json"
DOCTOR_PATH="${CONTROL_ROOT}/doctor.json"
TASK_DRAFT_PATH="${CONTROL_ROOT}/task-draft.json"
TASK_PATH="${CONTROL_ROOT}/task-spec.json"
POLICY_PATH="${CONTROL_ROOT}/policy-snapshot.json"
REVIEW_OUTPUT_PATH="${CONTROL_ROOT}/review-packet-output.json"

require_tools() {
  [ -x "$PYTHON_BIN" ] || die "缺少固定 Python：$PYTHON_BIN"
  [ -x "$GIT_BIN" ] || die "缺少固定 Git：$GIT_BIN"
}

canonical_path() {
  "$PYTHON_BIN" -I -B - "$1" <<'PY'
import os, sys
print(os.path.realpath(sys.argv[1]))
PY
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "缺少 sha256sum/shasum"
  fi
}

json_field() {
  "$PYTHON_BIN" -I -B - "$1" "$2" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
for part in sys.argv[2].split("."):
    value = value[part]
if isinstance(value, bool):
    print("true" if value else "false")
else:
    print(value)
PY
}

assert_source_identity() {
  local root head branch remote remote_head version_json marshal_sha candidate_asset candidate_sha manifest_sha build_date go_version
  root="$($GIT_BIN -C "$SOURCE_ROOT" rev-parse --show-toplevel 2>/dev/null)" || die "脚本不在 Git 仓库内"
  [ "$(canonical_path "$root")" = "$SOURCE_ROOT" ] || die "源仓库根身份漂移"
  head="$($GIT_BIN -C "$SOURCE_ROOT" rev-parse HEAD)"
  [ "$head" = "$EXPECTED_HEAD" ] || die "源仓库 HEAD 漂移：期望 ${EXPECTED_HEAD}，实际 ${head}"
  branch="$($GIT_BIN -C "$SOURCE_ROOT" symbolic-ref --short HEAD 2>/dev/null)" || die "源仓库必须检出 main，而非 detached HEAD"
  [ "$branch" = main ] || die "源仓库分支漂移：要求 main，实际 $branch"
  remote="$($GIT_BIN -C "$SOURCE_ROOT" config --get remote.origin.url)"
  [ "$remote" = "$CANONICAL_REMOTE" ] || die "origin 不是 canonical 仓库：$remote"
  remote_head="$($GIT_BIN -C "$SOURCE_ROOT" rev-parse --verify refs/remotes/origin/main 2>/dev/null)" || die "缺少本地 refs/remotes/origin/main；脚本不会隐式 fetch"
  [ "$remote_head" = "$EXPECTED_HEAD" ] || die "本地 origin/main 漂移：期望 ${EXPECTED_HEAD}，实际 ${remote_head}；脚本不会隐式 fetch"
  [ -z "$($GIT_BIN -C "$SOURCE_ROOT" status --porcelain --untracked-files=all)" ] || die "源仓库不是 clean final HEAD"

  [ -f "$MARSHAL_BIN" ] && [ -x "$MARSHAL_BIN" ] || die "缺少固定可执行文件：$MARSHAL_BIN"
  [ ! -L "$MARSHAL_BIN" ] || die "固定 Marshal 不得是符号链接"
  [ "$(canonical_path "$MARSHAL_BIN")" = "$MARSHAL_BIN" ] || die "Marshal 路径不是固定 canonical path"
  [ -f "$RELEASE_CHECKER" ] && [ ! -L "$RELEASE_CHECKER" ] \
    || die "缺少固定 release contract checker"
  # RC1 产物是单一 Darwin/arm64 闭集，不得用要求四平台资产的
  # verify-dist。buildDate/goVersion 从固定 candidate 的 manifest 读取，
  # 但 verify-rc1-dist 仍会对二者执行 canonical 格式与二进制 identity
  # 校验；sourceHead/tag/profile 则由本函数的冻结输入绑定。
  build_date="$(awk '$1 == "buildDate" && NF == 2 { print $2 }' "${RELEASE_DIST_ROOT}/RELEASE-MANIFEST")"
  go_version="$(awk '$1 == "goVersion" && NF == 2 { print $2 }' "${RELEASE_DIST_ROOT}/RELEASE-MANIFEST")"
  [ -n "$build_date" ] && [ -n "$go_version" ] || die "RC1 RELEASE-MANIFEST 缺少 buildDate/goVersion"
  if [ "$TEST_MODE" = 1 ]; then
    # 测试夹具使用 shell fake Marshal，无法通过真实 Mach-O/Go identity
    # 检查；二进制身份由 release-contract_test.sh 独立覆盖。
    "$RELEASE_CHECKER" verify-dist "$RELEASE_DIST_ROOT" "v${EXPECTED_VERSION}" "$EXPECTED_HEAD" >/dev/null \
      || die "测试夹具 dist/RELEASE-MANIFEST 不满足 sourceHead 合同"
  else
    GOTOOLCHAIN=local GO_BIN="$GO_BIN" "$RELEASE_CHECKER" verify-rc1-dist "$RELEASE_DIST_ROOT" "v${EXPECTED_VERSION}" "$EXPECTED_HEAD" \
      "$build_date" "$go_version" darwin arm64 darwin-local-dogfood >/dev/null \
      || die "待发布 RC1 dist/RELEASE-MANIFEST 不满足当前 sourceHead 合同"
  fi
  candidate_asset="${RELEASE_DIST_ROOT}/marshal_${EXPECTED_VERSION}_darwin_arm64"
  candidate_sha="$(sha256_file "$candidate_asset")"
  marshal_sha="$(sha256_file "$MARSHAL_BIN")"
  [ "$marshal_sha" = "$candidate_sha" ] \
    || die "固定 Marshal 不是待发布 Darwin arm64 candidate 的 exact bytes"
  manifest_sha="$(sha256_file "${RELEASE_DIST_ROOT}/RELEASE-MANIFEST")"
  if [ -f "$IDENTITY_PATH" ]; then
    [ "$candidate_sha" = "$(json_field "$IDENTITY_PATH" releaseCandidateSha256)" ] || die "待发布 Darwin arm64 candidate 在 Run 后发生漂移"
    [ "$manifest_sha" = "$(json_field "$IDENTITY_PATH" releaseManifestSha256)" ] || die "待发布 RELEASE-MANIFEST 在 Run 后发生漂移"
  fi
  RELEASE_CANDIDATE_ASSET="$candidate_asset"
  RELEASE_CANDIDATE_SHA256="$candidate_sha"
  RELEASE_MANIFEST_SHA256="$manifest_sha"
  version_json="$($MARSHAL_BIN version --json)" || die "固定 Marshal version 失败"
  VERSION_JSON="$version_json" "$PYTHON_BIN" -I -B - "$EXPECTED_HEAD" "$EXPECTED_VERSION" <<'PY' || die "Marshal build identity 漂移"
import json, os, sys
value = json.loads(os.environ["VERSION_JSON"])
assert value.get("commit") == sys.argv[1]
assert value.get("version") == sys.argv[2]
assert value.get("selfProfile") == "darwin-local-dogfood"
assert value.get("os") == "darwin"
PY
  if [ -f "$IDENTITY_PATH" ]; then
    [ "$marshal_sha" = "$(json_field "$IDENTITY_PATH" marshalSha256)" ] || die "固定 Marshal bytes 在 Run 后发生漂移"
  fi
  MARSHAL_SHA256="$marshal_sha"
}

set_runtime_environment() {
  export PATH="$(dirname "$PI_BIN"):/usr/bin:/bin:/usr/sbin:/sbin"
  export LANG="en_US.UTF-8"
  export MARSHAL_PI_PATH="$PI_BIN"
  export MARSHAL_PI_NODE_PATH="$PI_NODE"
  export MARSHAL_PI_RUNTIME="$PI_NODE"
  export MARSHAL_PI_ENTRYPOINT="$PI_BUNDLE"
  export MARSHAL_EMBEDDED_SANDBOX=1
  export MARSHAL_OPENCODE_PATH=""
  export MARSHAL_QWEN_PATH=""
  export MARSHAL_QODER_PATH=""
  export MARSHAL_CODEX_PATH=""
}

assert_pi_identity() {
  local resolved digest version node_digest node_version
  [ "$PI_NODE" = "$(canonical_path "$PI_NODE")" ] || die "Node runtime 必须是固定 canonical path"
  [ -f "$PI_NODE" ] && [ -x "$PI_NODE" ] && [ ! -L "$PI_NODE" ] \
    || die "Node runtime 不是固定 regular executable：$PI_NODE"
  node_digest="$(sha256_file "$PI_NODE")"
  [ "$node_digest" = "$PI_NODE_SHA256" ] || die "Node runtime SHA-256 漂移"
  node_version="$($PI_NODE --version)" || die "Node --version 失败"
  [ "$node_version" = "$PI_NODE_VERSION" ] || die "Node 版本漂移：期望 ${PI_NODE_VERSION}，实际 ${node_version}"
  [ "$PI_BIN" = "$(canonical_path "$PI_BIN")" ] && die "Pi 入口必须是冻结的符号链接，而不是直接 bundle 路径"
  [ -L "$PI_BIN" ] && [ -x "$PI_BIN" ] || die "Pi 入口不是可执行符号链接：$PI_BIN"
  [ -f "$PI_BUNDLE" ] && [ -x "$PI_BUNDLE" ] || die "Pi bundle 不可执行：$PI_BUNDLE"
  resolved="$(canonical_path "$PI_BIN")"
  [ "$resolved" = "$PI_BUNDLE" ] || die "Pi 符号链接目标漂移：$resolved"
  digest="$(sha256_file "$PI_BUNDLE")"
  [ "$digest" = "$PI_BUNDLE_SHA256" ] || die "Pi bundle SHA-256 漂移"
  version="$($PI_BIN --version)" || die "Pi --version 失败"
  [ "$version" = "$PI_VERSION" ] || die "Pi 版本漂移：期望 ${PI_VERSION}，实际 ${version}"
  if [ -f "$IDENTITY_PATH" ]; then
    [ "$PI_NODE" = "$(json_field "$IDENTITY_PATH" piNodePath)" ] || die "Node runtime 路径与冻结 Run 不一致"
    [ "$node_digest" = "$(json_field "$IDENTITY_PATH" piNodeSha256)" ] || die "Node runtime 与冻结 Run 不一致"
    [ "$PI_BIN" = "$(json_field "$IDENTITY_PATH" piPath)" ] || die "Pi 入口路径与冻结 Run 不一致"
    [ "$PI_BUNDLE" = "$(json_field "$IDENTITY_PATH" piBundlePath)" ] || die "Pi bundle 路径与冻结 Run 不一致"
    [ "$digest" = "$(json_field "$IDENTITY_PATH" piBundleSha256)" ] || die "Pi bundle 与冻结 Run 不一致"
  fi
}

assert_doctor() {
  "$PYTHON_BIN" -I -B - "$DOCTOR_PATH" <<'PY' || die "doctor 未放行精确 Pi 0.84.4 ordinary-user profile"
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
assert value.get("status") == "ok"
assert isinstance(value.get("policyEnvironmentBinding"), dict)
matches = [worker for worker in value.get("workers", [])
           if worker.get("adapterId") == "pi"
           and worker.get("outcome") == "registered"
           and worker.get("compatibility") == "supported"
           and worker.get("binaryVersion") == "0.84.4"
           and worker.get("authorityMode") == "ordinary-user"]
assert len(matches) == 1
PY
}

run_fixed_marshal() {
  (cd "$REPOSITORY_ROOT" && "$MARSHAL_BIN" "$@")
}

assert_canary_repository() {
  local head remote
  [ -d "$REPOSITORY_ROOT/.git" ] || die "持久 canary Git 仓库不存在"
  head="$($GIT_BIN -C "$REPOSITORY_ROOT" rev-parse HEAD)"
  [ "$head" = "$(json_field "$IDENTITY_PATH" canaryBaseSha)" ] || die "canary repository base HEAD 漂移"
  remote="$($GIT_BIN -C "$REPOSITORY_ROOT" config --get remote.origin.url)"
  [ "$remote" = "$CANARY_REMOTE" ] || die "canary repository remote 漂移"
  [ -z "$($GIT_BIN -C "$REPOSITORY_ROOT" status --porcelain --untracked-files=all)" ] || die "canary base repository 出现非 Marshal 状态变更"
}

assert_worker_identity() {
  local status_path="$1" attempt_id result_path
  attempt_id="$(json_field "$status_path" currentAttemptId)"
  result_path="${REPOSITORY_ROOT}/.marshal/runs/${RUN_ID}/attempts/${attempt_id}/worker-result.json"
  [ -f "$result_path" ] || die "缺少当前 Attempt 的 WorkerResult：$result_path"
  "$PYTHON_BIN" -I -B - "$result_path" "$RUN_ID" "$(json_field "$IDENTITY_PATH" taskId)" "$attempt_id" "$PI_BUNDLE" "$PI_VERSION" "$PI_MODEL" <<'PY' || die "WorkerResult 的 Pi executable/version/model 身份漂移"
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
assert value.get("runId") == sys.argv[2]
assert value.get("taskId") == sys.argv[3]
assert value.get("attemptId") == sys.argv[4]
assert value.get("status") == "completed"
adapter = value.get("adapter") or {}
assert adapter.get("id") == "pi"
assert adapter.get("executable") == sys.argv[5]
assert adapter.get("version") == sys.argv[6]
assert adapter.get("model") == sys.argv[7]
PY
}

assert_identity_record() {
  [ -f "$IDENTITY_PATH" ] || die "缺少持久 canary identity：$IDENTITY_PATH"
  "$PYTHON_BIN" -I -B - "$IDENTITY_PATH" "$SOURCE_ROOT" "$RUN_ID" "$EXPECTED_HEAD" "$EXPECTED_VERSION" "$MARSHAL_BIN" "$REPOSITORY_ROOT" "$PI_MODEL" "$PI_NODE" "$PI_NODE_SHA256" <<'PY' || die "canary identity 与请求不一致"
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
expected = {
    "schemaVersion": "marshal.release-canary.v1",
    "sourceRoot": sys.argv[2],
    "runId": sys.argv[3],
    "expectedHead": sys.argv[4],
    "expectedVersion": sys.argv[5],
    "marshalPath": sys.argv[6],
    "repositoryRoot": sys.argv[7],
    "piModel": sys.argv[8],
    "piNodePath": sys.argv[9],
    "piNodeSha256": sys.argv[10],
}
for key, wanted in expected.items():
    assert value.get(key) == wanted
assert value.get("phase") in {"review-pending", "accepted"}
assert value.get("releaseCandidateAsset", "").endswith("/dist/marshal_" + sys.argv[5] + "_darwin_arm64")
assert len(value.get("releaseCandidateSha256", "")) == 64
assert len(value.get("releaseManifestSha256", "")) == 64
PY
}

refresh_doctor() {
  [ -f "$ACTIVATION_PATH" ] || die "缺少冻结的 local-dogfood activation"
  export MARSHAL_LOCAL_DOGFOOD_ACTIVATION="$ACTIVATION_PATH"
  run_fixed_marshal doctor --json >"$DOCTOR_PATH"
  assert_doctor
}

assert_status_files() {
  local first="$1" second="$2" expected="$3"
  "$PYTHON_BIN" -I -B - "$first" "$second" "$RUN_ID" "$(json_field "$IDENTITY_PATH" taskId)" "$expected" <<'PY' || die "Run 重启恢复状态不精确"
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    first = json.load(handle)
with open(sys.argv[2], encoding="utf-8") as handle:
    second = json.load(handle)
assert first.get("runId") == sys.argv[3]
assert first.get("taskId") == sys.argv[4]
assert first.get("state") == sys.argv[5]
assert first.get("currentAttemptId")
assert int(first.get("attemptsUsed", 0)) >= 1
keys = ("runId", "taskId", "state", "currentAttemptId", "baseSha",
        "specDigest", "policyDigest", "reviewRound", "attemptsUsed")
assert {key: first.get(key) for key in keys} == {key: second.get(key) for key in keys}
PY
}

write_task_draft_and_policy() {
  local canary_base="$1" task_id="$2" marker="$3"
  "$PYTHON_BIN" -I -B - "$TASK_DRAFT_PATH" "$POLICY_PATH" "$DOCTOR_PATH" "$REPOSITORY_ROOT" "$CANARY_REMOTE" "$canary_base" "$task_id" "$RUN_ID" "$marker" "$PI_MODEL" "$SOURCE_ROOT/schemas/examples/happy-path/task-spec.json" "$SOURCE_ROOT/schemas/examples/happy-path/policy-snapshot.json" <<'PY'
import datetime, hashlib, json, sys

task_path, policy_path, doctor_path, repository_root, remote_url, base_sha, task_id, run_id, marker, pi_model, task_example_path, policy_example_path = sys.argv[1:]
marker_path = "release-canary.txt"
with open(task_example_path, encoding="utf-8") as handle:
    task = json.load(handle)
task.update({
    "metadata": {"id": task_id, "title": "v1 RC Pi 持久发布 canary"},
    "repository": {"path": repository_root, "remote": "origin", "baseRef": base_sha,
                   "expectedRemoteUrl": remote_url},
    "work": {
        "objective": f"创建 {marker_path}，内容恰好为单行 {marker} 加结尾换行；最终回复必须仅包含一个 WorkerResult JSON 对象。",
        "constraints": [
            f"只创建 {marker_path}；不得修改其他文件；不得提交、推送或访问网络。",
            "最终 assistant 输出必须恰好是一个 WorkerResult JSON 对象，不得添加 Markdown 或解释。",
        ],
        "context": [f"唯一允许的文件内容是 {marker} 加一个结尾换行。"],
        "nonGoals": ["不修改 Marshal 源仓库", "不自行验证或评审产物"],
    },
    "scope": {"allowPaths": [marker_path], "denyPaths": [".marshal/**"],
              "allowSubmodules": False, "maxChangedFiles": 1, "maxDiffBytes": 20000},
    "acceptance": {"allowNoChange": False, "commands": [{
        "id": "release-canary-marker",
        "argv": ["/usr/bin/python3", "-I", "-B", "-c",
                 "from pathlib import Path; s=Path('release-canary.txt').read_text(); "
                 + "assert s == " + repr(marker + "\n") + ", repr(s)"],
        "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 10000,
        "required": True, "baselinePolicy": "none",
    }]},
    "deliverables": [{"id": "release-canary-marker", "kind": "diagnostic",
                      "pathGlob": marker_path, "minimumCount": 1, "required": True}],
    # Pi 0.84.4 provider/model tuple 由 --expected-version 绑定的当次
    # identity 决定：pinned 走冻结 pai-eas tuple，custom 由供给环境注入。
    "worker": {"model": pi_model,
               "sessionPolicy": "ephemeral", "executionProfile": "workspace-write"},
    "budgets": {"attemptTimeoutSeconds": 900, "runTimeoutSeconds": 1800,
                "maxAttempts": 1, "maxOperationalRetries": 0,
                "maxReworkRounds": 0, "maxOutputBytes": 8388608},
    "publication": {"required": False, "provider": "none", "mode": "none",
                    "remote": "origin", "baseBranch": "main", "mergePolicy": "never",
                    "requiredChecks": []},
})
with open(doctor_path, encoding="utf-8") as handle:
    doctor = json.load(handle)
with open(policy_example_path, encoding="utf-8") as handle:
    policy = json.load(handle)
policy.update({
    "taskId": task_id,
    "runId": run_id,
    "sources": [{"scope": "builtin", "digest": "sha256:" + "b" * 64, "required": True}],
    "effective": {
        "minimumExecutionProfile": "workspace-write",
        "requireEnforcedNetworkPolicy": False,
        "networkPolicy": "unenforced",
        "allowFallbackWorkers": False,
        "allowWorkerSubagents": False,
        "allowPublication": False,
        "allowMerge": False,
        "allowGateWaivers": False,
        "allowedAdapters": ["pi"],
        "environmentAllowlist": ["PATH", "LANG", "TMPDIR", "HOME"],
        "retentionDays": 7,
    },
    "control": {
        "autonomyProfile": "supervised",
        "requiredApprovals": ["plan"],
        "allowMediatedSteering": False,
        "directPtyPolicy": "deny",
        "maxSteeringRounds": 0,
    },
    "environmentBinding": doctor["policyEnvironmentBinding"],
    "policyDigest": "",
    "generatedAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
})
detached = json.dumps(policy, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
policy["policyDigest"] = "sha256:" + hashlib.sha256(detached).hexdigest()
for path, value in ((task_path, task), (policy_path, policy)):
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
PY
}

write_identity() {
  local phase="$1" canary_base="$2" task_id="$3" packet_digest="${4:-}"
  "$PYTHON_BIN" -I -B - "$IDENTITY_PATH" "$phase" "$SOURCE_ROOT" "$RUN_ID" "$EXPECTED_HEAD" "$EXPECTED_VERSION" "$MARSHAL_BIN" "$MARSHAL_SHA256" "$RELEASE_CANDIDATE_ASSET" "$RELEASE_CANDIDATE_SHA256" "$RELEASE_MANIFEST_SHA256" "$PI_BIN" "$PI_NODE" "$PI_NODE_SHA256" "$PI_BUNDLE" "$PI_BUNDLE_SHA256" "$PI_MODEL" "$REPOSITORY_ROOT" "$canary_base" "$task_id" "$packet_digest" <<'PY'
import json, sys
keys = ("phase", "sourceRoot", "runId", "expectedHead", "expectedVersion",
        "marshalPath", "marshalSha256", "releaseCandidateAsset",
        "releaseCandidateSha256", "releaseManifestSha256", "piPath", "piNodePath",
        "piNodeSha256", "piBundlePath", "piBundleSha256", "piModel", "repositoryRoot", "canaryBaseSha", "taskId",
        "reviewPacketDigest")
value = {"schemaVersion": "marshal.release-canary.v1"}
value.update(dict(zip(keys, sys.argv[2:])))
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
PY
}

update_identity_phase() {
  local phase="$1"
  "$PYTHON_BIN" -I -B - "$IDENTITY_PATH" "$phase" <<'PY'
import json, os, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
value["phase"] = sys.argv[2]
staged = sys.argv[1] + ".new"
with open(staged, "w", encoding="utf-8") as handle:
    json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
os.replace(staged, sys.argv[1])
PY
}

run_canary() {
  local canary_base task_id marker packet_digest
  [ ! -e "$CANARY_ROOT" ] || die "Run ID 已存在；拒绝覆盖持久证据：$CANARY_ROOT"
  require_tools
  set_runtime_environment
  assert_source_identity
  assert_pi_identity

  mkdir -p "$CONTROL_ROOT/empty-hooks" "$REPOSITORY_ROOT"
  "$PYTHON_BIN" -I -B - "$REPOSITORY_ROOT/.gitignore" "$REPOSITORY_ROOT/README.md" <<'PY'
import sys
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    handle.write(".marshal/\n")
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write("Marshal v1 release canary disposable repository.\n")
PY
  "$GIT_BIN" -C "$REPOSITORY_ROOT" init -q -b main
  "$GIT_BIN" -C "$REPOSITORY_ROOT" config core.hooksPath "$CONTROL_ROOT/empty-hooks"
  "$GIT_BIN" -C "$REPOSITORY_ROOT" config user.email "marshal-release-canary@example.invalid"
  "$GIT_BIN" -C "$REPOSITORY_ROOT" config user.name "Marshal Release Canary"
  "$GIT_BIN" -C "$REPOSITORY_ROOT" add .gitignore README.md
  "$GIT_BIN" -C "$REPOSITORY_ROOT" commit -q -m "fixture: initialize release canary"
  "$GIT_BIN" -C "$REPOSITORY_ROOT" remote add origin "$CANARY_REMOTE"
  canary_base="$($GIT_BIN -C "$REPOSITORY_ROOT" rev-parse HEAD)"
  task_id="RELEASE-CANARY-${EXPECTED_HEAD:0:12}"
  marker="marshal-release-canary:${EXPECTED_HEAD}"

  MARSHAL_LOCAL_DOGFOOD_ACTIVATION="" run_fixed_marshal doctor --self \
    --repository-root "$REPOSITORY_ROOT" --activation-id "$RUN_ID" --valid-for 4h \
    >"$ACTIVATION_PATH"
  "$PYTHON_BIN" -I -B -m json.tool "$ACTIVATION_PATH" >/dev/null
  export MARSHAL_LOCAL_DOGFOOD_ACTIVATION="$ACTIVATION_PATH"
  run_fixed_marshal doctor --json >"$DOCTOR_PATH"
  assert_doctor

  run_fixed_marshal init >"$CONTROL_ROOT/init.json"
  write_task_draft_and_policy "$canary_base" "$task_id" "$marker"
  run_fixed_marshal task scaffold --draft "$TASK_DRAFT_PATH" --preferred-adapter pi >"$TASK_PATH"
  write_identity "running" "$canary_base" "$task_id"

  run_fixed_marshal task plan --task "$TASK_PATH" --policy "$POLICY_PATH" --run "$RUN_ID" --json \
    >"$CONTROL_ROOT/plan.json"
  run_fixed_marshal task approve --run "$RUN_ID" --gate plan --actor release-canary --json \
    >"$CONTROL_ROOT/approval.json"
  # READY 调用只启动 sealed Attempt；后续 RUNNING 调用在精确 PID 终态后
  # 收集 descriptor-bound transcript 并原子推进到 VERIFYING。
  run_fixed_marshal task run --run "$RUN_ID" --json >"$CONTROL_ROOT/run-start.json"
  local collect_attempt=0 run_state
  while :; do
    run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/run-status.json"
    run_state="$(json_field "$CONTROL_ROOT/run-status.json" state)"
    case "$run_state" in
      VERIFYING) break ;;
      RUNNING)
        collect_attempt=$((collect_attempt + 1))
        [ "$collect_attempt" -le 900 ] || die "Pi Attempt 在 900 次有界收集内未到达 VERIFYING"
        run_fixed_marshal task run --run "$RUN_ID" --json >"$CONTROL_ROOT/run-collect.json"
        [ "$TEST_MODE" = 1 ] || /bin/sleep 1
        ;;
      *) die "Pi Attempt 进入非预期状态：$run_state" ;;
    esac
  done
  run_fixed_marshal task verify --run "$RUN_ID" --json >"$CONTROL_ROOT/verification.json"
  run_fixed_marshal task review --run "$RUN_ID" --json >"$REVIEW_OUTPUT_PATH"

  packet_digest="$($PYTHON_BIN -I -B - "$REVIEW_OUTPUT_PATH" "$RUN_ID" "$task_id" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
packet = value.get("packet") or {}
assert value.get("status") == "generated"
assert value.get("packetDigest", "").startswith("sha256:")
assert packet.get("runId") == sys.argv[2]
assert packet.get("taskId") == sys.argv[3]
assert packet.get("reviewRound") == 1
assert packet.get("localSelfIdentityBinding")
print(value["packetDigest"])
PY
)" || die "ReviewPacket 输出不完整"
  write_identity "review-pending" "$canary_base" "$task_id" "$packet_digest"

  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/status-first.json"
  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/status-restarted.json"
  assert_status_files "$CONTROL_ROOT/status-first.json" "$CONTROL_ROOT/status-restarted.json" REVIEW_PENDING
  assert_worker_identity "$CONTROL_ROOT/status-restarted.json"
  assert_canary_repository

  note "PASS：真实 Pi Run 已持久停在 REVIEW_PENDING"
  note "ReviewPacket：$REVIEW_OUTPUT_PATH"
  note "Lead 独立生成 Decision 后运行 finalize；不得在本进程内自评。"
}

load_existing_canary() {
  require_tools
  assert_identity_record
  set_runtime_environment
  assert_source_identity
  assert_pi_identity
  assert_canary_repository
  refresh_doctor
  if [ "$(json_field "$IDENTITY_PATH" phase)" = review-pending ] || [ "$(json_field "$IDENTITY_PATH" phase)" = accepted ]; then
    run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/identity-status.json"
    assert_worker_identity "$CONTROL_ROOT/identity-status.json"
  fi
}

status_canary() {
  load_existing_canary
  case "$EXPECTED_STATE:$(json_field "$IDENTITY_PATH" phase)" in
    REVIEW_PENDING:review-pending|ACCEPTED:accepted) ;;
    *) die "请求状态与 canary 持久 phase 不一致" ;;
  esac
  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/status-command-first.json"
  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/status-command-restarted.json"
  assert_status_files "$CONTROL_ROOT/status-command-first.json" "$CONTROL_ROOT/status-command-restarted.json" "$EXPECTED_STATE"
  note "PASS：独立进程恢复状态为 $EXPECTED_STATE"
}

validate_accept_decision() {
  "$PYTHON_BIN" -I -B - "$REVIEW_OUTPUT_PATH" "$DECISION_PATH" <<'PY' || die "Decision 不是当前 packet 的 exact accept Decision"
import hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    output = json.load(handle)
with open(sys.argv[2], encoding="utf-8") as handle:
    decision = json.load(handle)
packet = output["packet"]
expected = {
    "taskId": packet["taskId"],
    "runId": packet["runId"],
    "reviewRound": packet["reviewRound"],
    "specDigest": packet["specDigest"],
    "reviewPacketDigest": output["packetDigest"],
    "verificationDigest": packet["verificationDigest"],
    "artifactManifestDigest": packet["artifactManifestDigest"],
    "evidenceDigest": packet["evidenceDigest"],
}
for key, wanted in expected.items():
    assert decision.get(key) == wanted
binding = packet.get("localSelfIdentityBinding")
assert binding
encoded = json.dumps(binding, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
assert decision.get("localSelfIdentityBindingDigest") == "sha256:" + hashlib.sha256(encoded).hexdigest()
assert decision.get("verdict") == "accept"
assert decision.get("blockingFindings") == []
assert isinstance(decision.get("nonBlockingFindings"), list)
assert decision.get("publicationRecommendation") == "not-applicable"
assert decision.get("mergeRecommendation") == "do-not-merge"
reviewer = decision.get("reviewer") or {}
assert reviewer.get("type") in {"lead-agent", "human"}
assert reviewer.get("id")
PY
}

assert_same_applied_decision() {
  local review_round authority_decision
  review_round="$(json_field "$REVIEW_OUTPUT_PATH" packet.reviewRound)"
  authority_decision="$(printf '%s/.marshal/runs/%s/decisions/decision-%03d.json' "$REPOSITORY_ROOT" "$RUN_ID" "$review_round")"
  [ -f "$authority_decision" ] || die "ACCEPTED Run 缺少 Core authority Decision：$authority_decision"
  "$PYTHON_BIN" -I -B - "$authority_decision" "$DECISION_PATH" <<'PY' || die "finalize 传入的不是 Core 已接纳的同一 Decision"
import copy, datetime, json, sys

with open(sys.argv[1], encoding="utf-8") as handle:
    applied = json.load(handle)
with open(sys.argv[2], encoding="utf-8") as handle:
    supplied = json.load(handle)

def split_time(value):
    document = copy.deepcopy(value)
    raw = document.pop("decidedAt")
    parsed = datetime.datetime.fromisoformat(raw.replace("Z", "+00:00"))
    assert parsed.tzinfo is not None
    return document, parsed.astimezone(datetime.timezone.utc)

applied_document, applied_time = split_time(applied)
supplied_document, supplied_time = split_time(supplied)
assert applied_document == supplied_document
assert applied_time == supplied_time
PY
}

finalize_canary() {
  local phase pre_finalize_state
  load_existing_canary
  phase="$(json_field "$IDENTITY_PATH" phase)"
  case "$phase" in
    review-pending|accepted) ;;
    *) die "只有 review-pending/accepted canary 可以 finalize" ;;
  esac
  [ -f "$DECISION_PATH" ] || die "Decision 文件不存在：$DECISION_PATH"
  [ ! -L "$DECISION_PATH" ] || die "Decision 文件不得是符号链接"
  [ "$(canonical_path "$DECISION_PATH")" = "$DECISION_PATH" ] || die "--decision 必须是 canonical absolute path"

  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/pre-finalize-status.json"
  pre_finalize_state="$($PYTHON_BIN -I -B - "$CONTROL_ROOT/pre-finalize-status.json" "$RUN_ID" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
assert value.get("runId") == sys.argv[2]
assert value.get("state") in {"REVIEW_PENDING", "ACCEPTED"}
print(value["state"])
PY
)" || die "finalize 前 Run 状态不是 REVIEW_PENDING/ACCEPTED"
  validate_accept_decision

  # Decision 已由 Core 原子接纳、但 shell 在更新 identity phase 前退出时，
  # 重跑 finalize 只恢复并核对 ACCEPTED，不重复导入 Decision。
  if [ "$pre_finalize_state" = ACCEPTED ]; then
    assert_same_applied_decision
    run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/accepted-first.json"
    run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/accepted-restarted.json"
    assert_status_files "$CONTROL_ROOT/accepted-first.json" "$CONTROL_ROOT/accepted-restarted.json" ACCEPTED
    if [ "$phase" = review-pending ]; then
      update_identity_phase accepted
      note "PASS：恢复了已由 Core 接纳的独立 Decision，Run 为 ACCEPTED"
    else
      note "PASS：同一独立 Decision 已接纳；两次 Core status 均为 ACCEPTED，finalize no-op"
    fi
    return
  fi

  run_fixed_marshal task review --run "$RUN_ID" --decision "$DECISION_PATH" --json \
    >"$CONTROL_ROOT/finalize.json"
  "$PYTHON_BIN" -I -B - "$CONTROL_ROOT/finalize.json" <<'PY' || die "Decision 导入未精确到达 ACCEPTED"
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle)
assert value.get("status") == "applied"
assert value.get("verdict") == "accept"
assert value.get("targetState") == "ACCEPTED"
assert value.get("decisionDigest", "").startswith("sha256:")
PY
  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/accepted-first.json"
  run_fixed_marshal task status --run "$RUN_ID" --json >"$CONTROL_ROOT/accepted-restarted.json"
  assert_status_files "$CONTROL_ROOT/accepted-first.json" "$CONTROL_ROOT/accepted-restarted.json" ACCEPTED
  assert_same_applied_decision
  update_identity_phase accepted
  note "PASS：独立 ReviewDecision 已导入，Run 已持久到达 ACCEPTED"
}

case "$COMMAND" in
  run) run_canary ;;
  status) status_canary ;;
  finalize) finalize_canary ;;
esac
