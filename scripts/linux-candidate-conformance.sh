#!/usr/bin/env bash
# Build and exercise Linux release-shaped artifacts without granting Linux
# runtime, stable-release, signing, rollback/high-water, or publication
# authority. CI runs this script independently on native amd64 and arm64.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

TAG=v0.0.0-rc1
VERSION="${TAG#v}"
DIST="${TMP_ROOT}/dist"
MOCK_BIN="${TMP_ROOT}/mock-bin"
INSTALL_DIR="${TMP_ROOT}/install"
HOME_DIR="${TMP_ROOT}/home"
GO_FALLBACK_MARKER="${TMP_ROOT}/unexpected-go-fallback"
TAG_MESSAGE="${TMP_ROOT}/tag-message"
TAG_OBJECT=1111111111111111111111111111111111111111

fail() {
  printf '[linux-candidate-conformance] FAIL: %s\n' "$*" >&2
  exit 1
}

[ "$(uname -s)" = Linux ] || fail '本门禁只允许在 Linux runner 执行'
case "$(uname -m)" in
  x86_64|amd64) NATIVE_ARCH=amd64 ;;
  aarch64|arm64) NATIVE_ARCH=arm64 ;;
  *) fail '本门禁要求原生 Linux amd64 或 arm64 runner' ;;
esac

SOURCE_HEAD="$(git -C "$ROOT" rev-parse --verify 'HEAD^{commit}')"
[[ "$SOURCE_HEAD" =~ ^[0-9a-f]{40}$ ]] || fail 'HEAD 不是 40 位小写 commit'
[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ] \
  || fail 'checkout 必须 clean，才能把 candidate 绑定到 exact HEAD'

GO_BIN="${GO_BIN:-$(go env GOROOT)/bin/go}"
[ -f "$GO_BIN" ] && [ ! -L "$GO_BIN" ] && [ -x "$GO_BIN" ] \
  || fail 'GO_BIN 必须是固定可执行普通文件'
GO_VERSION="$($GO_BIN env GOVERSION)"
REQUIRED_GO_VERSION="$(sed -n -E 's/^toolchain[[:space:]]+(go[0-9]+\.[0-9]+\.[0-9]+)[[:space:]]*$/\1/p' "$ROOT/go.mod")"
[ -n "$REQUIRED_GO_VERSION" ] && [ "$GO_VERSION" = "$REQUIRED_GO_VERSION" ] \
  || fail "Go toolchain 漂移：期望 ${REQUIRED_GO_VERSION:-missing}，实际 $GO_VERSION"
BUILD_DATE="$(bash "$ROOT/scripts/release-contract.sh" build-date "$ROOT" "$SOURCE_HEAD")"

GO="$GO_BIN" make -C "$ROOT" dist \
  DIST_DIR="$DIST" VERSION="$VERSION" COMMIT="$SOURCE_HEAD" BUILD_DATE="$BUILD_DATE" >/dev/null
bash "$ROOT/scripts/release-contract.sh" verify-dist "$DIST" "$TAG" "$SOURCE_HEAD" >/dev/null

AMD64="${DIST}/marshal_${VERSION}_linux_amd64"
ARM64="${DIST}/marshal_${VERSION}_linux_arm64"
NATIVE="${DIST}/marshal_${VERSION}_linux_${NATIVE_ARCH}"
mkdir -p "$HOME_DIR"

python3 -I -B - "$GO_BIN" "$GO_VERSION" "$AMD64" amd64 "$ARM64" arm64 <<'PY'
import os
import pathlib
import stat
import struct
import subprocess
import sys

go_bin, go_version, *candidate_args = sys.argv[1:]
expected_machine = {"amd64": 62, "arm64": 183}
for index in range(0, len(candidate_args), 2):
    path = pathlib.Path(candidate_args[index])
    arch = candidate_args[index + 1]
    metadata = os.lstat(path)
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise SystemExit(f"candidate is not a regular non-symlink: {path}")
    if not metadata.st_mode & stat.S_IXUSR:
        raise SystemExit(f"candidate is not executable: {path}")
    with path.open("rb") as stream:
        header = stream.read(64)
    if len(header) != 64 or header[:6] != b"\x7fELF\x02\x01":
        raise SystemExit(f"candidate is not a little-endian ELF64 object: {path}")
    elf_type, machine = struct.unpack_from("<HH", header, 16)
    if elf_type not in (2, 3) or machine != expected_machine[arch]:
        raise SystemExit(f"ELF type/machine mismatch for {arch}: type={elf_type} machine={machine}")
    environment = {
        "GOTOOLCHAIN": "local",
        "GOCACHE": "off",
        "HOME": "/nonexistent",
        "LC_ALL": "C",
        "PATH": f"{os.path.dirname(go_bin)}:/usr/bin:/bin",
    }
    result = subprocess.run(
        [go_bin, "version", "-m", str(path)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
        env=environment,
        timeout=15,
    )
    if result.returncode != 0 or len(result.stdout) > 1 << 20 or result.stderr:
        raise SystemExit(f"go buildinfo inspection failed for {arch}: {result.stderr[:1024]!r}")
    lines = result.stdout.decode("utf-8").splitlines()
    if not lines or not lines[0].endswith(f": {go_version}"):
        raise SystemExit(f"Go version mismatch for {arch}")
    if "\tpath\tgithub.com/chiga0/marshal-harness/cmd/marshal" not in lines:
        raise SystemExit(f"main package mismatch for {arch}")
    settings = {}
    for line in lines:
        if not line.startswith("\tbuild\t"):
            continue
        key, separator, value = line.removeprefix("\tbuild\t").partition("=")
        if not separator or key in settings:
            raise SystemExit(f"malformed or duplicate Go build setting for {arch}: {line!r}")
        settings[key] = value
    required = {
        "-buildmode": "exe",
        "-compiler": "gc",
        "-trimpath": "true",
        "CGO_ENABLED": "0",
        "GOARCH": arch,
        "GOOS": "linux",
    }
    if any(settings.get(key) != value for key, value in required.items()):
        raise SystemExit(f"Go build settings mismatch for {arch}: {settings!r}")
    if any(key.startswith("vcs.") for key in settings):
        raise SystemExit(f"unexpected embedded VCS settings for {arch}")
PY

VERSION_JSON="$(env -i HOME="$HOME_DIR" LC_ALL=C PATH=/usr/bin:/bin "$NATIVE" version --json)"
python3 -I -B - "$VERSION_JSON" "$VERSION" "$SOURCE_HEAD" "$BUILD_DATE" "$GO_VERSION" "$NATIVE_ARCH" <<'PY'
import json
import sys

payload = json.loads(sys.argv[1])
expected = {
    "version": sys.argv[2],
    "commit": sys.argv[3],
    "buildDate": sys.argv[4],
    "goVersion": sys.argv[5],
    "os": "linux",
    "arch": sys.argv[6],
    "selfProfile": "unprofiled",
}
if payload != expected:
    raise SystemExit(f"native version identity mismatch: got={payload!r} want={expected!r}")
PY

HELP_OUTPUT="$(env -i HOME="$HOME_DIR" LC_ALL=C PATH=/usr/bin:/bin "$NATIVE" help)"
printf '%s\n' "$HELP_OUTPUT" | grep -F 'marshal doctor' >/dev/null \
  || fail 'native help 未包含 doctor command'

DOCTOR_JSON="$(env -i HOME="$HOME_DIR" LC_ALL=C PATH=/usr/bin:/bin "$NATIVE" doctor --json)"
python3 -I -B - "$DOCTOR_JSON" "$SOURCE_HEAD" "$NATIVE_ARCH" <<'PY'
import json
import sys

payload = json.loads(sys.argv[1])
build = payload.get("build", {})
if payload.get("status") != "ok" or build.get("commit") != sys.argv[2]:
    raise SystemExit("native doctor did not report ok with exact sourceHead")
if build.get("os") != "linux" or build.get("arch") != sys.argv[2] or build.get("selfProfile") != "unprofiled":
    raise SystemExit("native doctor overstated Linux profile")
if payload.get("selfIdentity") is not None:
    raise SystemExit("unprofiled Linux doctor unexpectedly reported self authority")
PY

set +e
env -i HOME="$HOME_DIR" LC_ALL=C PATH=/usr/bin:/bin \
  "$NATIVE" doctor --self --repository-root "$ROOT" \
  >"${TMP_ROOT}/doctor-self.stdout" 2>"${TMP_ROOT}/doctor-self.stderr"
doctor_self_status=$?
set -e
[ "$doctor_self_status" = 3 ] || fail "doctor --self exit=${doctor_self_status}，期望 3"
[ ! -s "${TMP_ROOT}/doctor-self.stdout" ] || fail 'unprofiled doctor --self 不得输出 activation'
grep -F 'self-local-profile-mismatch' "${TMP_ROOT}/doctor-self.stderr" >/dev/null \
  || fail 'unprofiled doctor --self 未返回确定性 profile mismatch'

mkdir -p "$MOCK_BIN"
bash "$ROOT/scripts/release-contract.sh" candidate-tag-message "$DIST" "$TAG" "$SOURCE_HEAD" >"$TAG_MESSAGE"

cat >"${MOCK_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) destination="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
[ -n "$destination" ] && [ -n "$url" ] || exit 2
name="${url##*/}"
[ "$url" = "https://github.com/chiga0/marshal-harness/releases/download/${FIXTURE_TAG}/${name}" ] || exit 91
if [ "${FIXTURE_MODE:-valid}" = missing-manifest ] && [ "$name" = RELEASE-MANIFEST ]; then
  exit 22
fi
cp "${FIXTURE_DIST}/${name}" "$destination"
if [ "${FIXTURE_MODE:-valid}" = tampered-asset ] && [ "$name" = "marshal_${FIXTURE_TAG#v}_linux_${FIXTURE_ARCH}" ]; then
  printf 'tampered\n' >>"$destination"
fi
EOF

cat >"${MOCK_BIN}/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = ls-remote ]; then
  printf '%s\trefs/tags/%s\n' "$FIXTURE_TAG_OBJECT" "$FIXTURE_TAG"
  printf '%s\trefs/tags/%s^{}\n' "$FIXTURE_PEELED_COMMIT" "$FIXTURE_TAG"
  exit 0
fi
if [ "${1:-}" = -C ] && [[ "${2:-}" = */release-tag.git ]]; then
  shift 2
  case " $* " in
    *' fetch '*) exit 0 ;;
    *' cat-file -t '*) printf 'tag\n'; exit 0 ;;
    *' rev-parse --verify '*)
      case "$*" in
        *'^{commit}'*) printf '%s\n' "$FIXTURE_PEELED_COMMIT" ;;
        *) printf '%s\n' "$FIXTURE_TAG_OBJECT" ;;
      esac
      exit 0
      ;;
    *' for-each-ref '*)
      sed "s/^marshal-candidate-source-head: .*/marshal-candidate-source-head: ${FIXTURE_PEELED_COMMIT}/" "$FIXTURE_TAG_MESSAGE"
      printf '\036\n'
      exit 0
      ;;
  esac
  exit 2
fi
exec /usr/bin/git "$@"
EOF

cat >"${MOCK_BIN}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: >"${GO_FALLBACK_MARKER:?}"
printf 'unexpected source fallback\n' >&2
exit 97
EOF
chmod 0755 "${MOCK_BIN}/curl" "${MOCK_BIN}/git" "${MOCK_BIN}/go"

run_installer() {
  local mode="$1" peeled_commit="$2"
  env -i \
    HOME="$HOME_DIR" \
    LC_ALL=C \
    PATH="$MOCK_BIN:/usr/bin:/bin" \
    MARSHAL_INSTALL_DIR="$INSTALL_DIR" \
    MARSHAL_TAG="$TAG" \
    FIXTURE_MODE="$mode" \
    FIXTURE_DIST="$DIST" \
    FIXTURE_TAG="$TAG" \
    FIXTURE_TAG_OBJECT="$TAG_OBJECT" \
    FIXTURE_PEELED_COMMIT="$peeled_commit" \
    FIXTURE_TAG_MESSAGE="$TAG_MESSAGE" \
    FIXTURE_ARCH="$NATIVE_ARCH" \
    GO_FALLBACK_MARKER="$GO_FALLBACK_MARKER" \
    /bin/bash --noprofile --norc "$ROOT/scripts/install.sh"
}

run_installer valid "$SOURCE_HEAD" >/dev/null
[ -x "${INSTALL_DIR}/marshal" ] || fail 'installer 未产出 Linux amd64 executable'
[ "$(sha256sum "$NATIVE" | awk '{print $1}')" = "$(sha256sum "${INSTALL_DIR}/marshal" | awk '{print $1}')" ] \
  || fail '临时安装后的 bytes/digest 与 candidate 不一致'
[ ! -e "$GO_FALLBACK_MARKER" ] || fail '成功安装触发了源码回退'
INSTALLED_SHA="$(sha256sum "${INSTALL_DIR}/marshal" | awk '{print $1}')"

expect_install_failure_preserves_current() {
  local label="$1" mode="$2" peeled_commit="$3" expected="$4" output status
  set +e
  output="$(run_installer "$mode" "$peeled_commit" 2>&1)"
  status=$?
  set -e
  [ "$status" -ne 0 ] || fail "${label} 应 fail closed"
  printf '%s\n' "$output" | grep -F "$expected" >/dev/null \
    || fail "${label} 未返回预期原因：${expected}"
  [ "$(sha256sum "${INSTALL_DIR}/marshal" | awk '{print $1}')" = "$INSTALLED_SHA" ] \
    || fail "${label} 改写了既有安装"
  [ ! -e "$GO_FALLBACK_MARKER" ] || fail "${label} 触发了源码回退"
}

expect_install_failure_preserves_current \
  upgrade-tampered tampered-asset "$SOURCE_HEAD" 'sha256 校验失败'
expect_install_failure_preserves_current \
  incomplete-release missing-manifest "$SOURCE_HEAD" '缺少或无法下载 RELEASE-MANIFEST'
expect_install_failure_preserves_current \
  cross-head-mismatch valid 2222222222222222222222222222222222222222 \
  'RELEASE-MANIFEST 非 canonical、与 tag/peeled commit/checksum 不一致或资产集合不封闭'

printf '[linux-candidate-conformance] PASS (native=%s, artifact-only, selfProfile=unprofiled; stable rollback/high-water not closed)\n' "$NATIVE_ARCH"
