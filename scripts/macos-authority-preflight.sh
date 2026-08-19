#!/bin/sh
set -eu

# Mac-first authority 预检只读检查。
# 它不安装文件、不签名、不 bootstrap launchd，也不读取 credential。

qoder_path=${MARSHAL_QODER_PATH:-/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23}
codex_path=${MARSHAL_CODEX_PATH:-/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin}
qoder_sha256=${MARSHAL_QODER_SHA256:-b09566c33df68f8ee3e82783120f6eb885fbd9aeb5bc35beb4a85a3ea2d4219a}
codex_sha256=${MARSHAL_CODEX_SHA256:-1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590}
apap_service=${MARSHAL_APAP_SERVICE_PATH:-/Library/PrivilegedHelperTools/marshal-apap}
launcher=${MARSHAL_DARWIN_LAUNCHER_PATH:-/Library/PrivilegedHelperTools/marshal-darwin-launcher}
apap_endpoint=${MARSHAL_APAP_ENDPOINT:-/private/var/run/marshal-apap.sock}
plist_label=${MARSHAL_APAP_LABEL:-com.marshal.apap}
expected_team_id=${MARSHAL_SIGNING_TEAM_ID:-}

failures=0
check() {
    label=$1
    shift
    if "$@" >/dev/null 2>&1; then
        printf 'PASS %s\n' "$label"
    else
        printf 'BLOCKED %s\n' "$label"
        failures=$((failures + 1))
    fi
}

check_signed_team() {
    binary=$1
    [ -n "$expected_team_id" ] || return 1
    details=$(codesign -dvv "$binary" 2>&1) || return 1
    printf '%s\n' "$details" | grep -q '^Signature=adhoc$' && return 1
    team_id=$(printf '%s\n' "$details" | sed -n 's/^TeamIdentifier=//p' | head -n 1)
    [ -n "$team_id" ] && [ "$team_id" = "$expected_team_id" ]
}

check_root_private_file() {
    path=$1
    [ -f "$path" ] || return 1
    [ "$(stat -f %u "$path" 2>/dev/null)" = "0" ] || return 1
    mode=$(stat -f %Mp%Lp "$path" 2>/dev/null) || return 1
    case "$mode" in
        [0-7][2367][0-7]|[0-7][0-7][2367]) return 1 ;;
    esac
}

check_root_private_socket() {
    path=$1
    [ -S "$path" ] || return 1
    [ "$(stat -f %u "$path" 2>/dev/null)" = "0" ] || return 1
    mode=$(stat -f %Mp%Lp "$path" 2>/dev/null) || return 1
    case "$mode" in
        [0-7][2367][0-7]|[0-7][0-7][2367]) return 1 ;;
    esac
}

check_launchd_binding() {
    output=$(launchctl print "system/$plist_label" 2>/dev/null) || return 1
    printf '%s\n' "$output" | grep -Fq "$apap_service" || return 1
    printf '%s\n' "$output" | grep -Fq "$launcher"
}

check_binary_identity() {
    binary=$1
    expected_version=$2
    expected_sha256=$3
    version=$("$binary" --version 2>/dev/null) || return 1
    [ "$version" = "$expected_version" ] || return 1
    actual_sha256=$(shasum -a 256 "$binary" 2>/dev/null | awk '{print $1}') || return 1
    [ "$actual_sha256" = "$expected_sha256" ]
}

if [ "$(uname -s)" != "Darwin" ]; then
    printf 'BLOCKED platform: Darwin required\n'
    exit 2
fi

check "qoder executable" test -x "$qoder_path"
check "qoder held identity (version+sha256)" check_binary_identity "$qoder_path" "1.1.23" "$qoder_sha256"
check "codex executable" test -x "$codex_path"
check "codex held identity (version+sha256)" check_binary_identity "$codex_path" "codex-cli 0.145.0" "$codex_sha256"
check "APAP service exists" test -f "$apap_service"
check "signed launcher exists" test -f "$launcher"
check "APAP service root/private" check_root_private_file "$apap_service"
check "launcher root/private" check_root_private_file "$launcher"
check "codesigning identity" sh -c 'security find-identity -v -p codesigning | grep -Eq "[1-9][0-9]* valid identities found"'
check "APAP service signature" codesign --verify --strict "$apap_service"
check "launcher signature" codesign --verify --strict "$launcher"
check "APAP service managed Team ID" check_signed_team "$apap_service"
check "launcher managed Team ID" check_signed_team "$launcher"
check "APAP endpoint root/private socket" check_root_private_socket "$apap_endpoint"
check "root launchd service" launchctl print "system/$plist_label"
check "root launchd exact service+launcher binding" check_launchd_binding
check "noninteractive sudo" sudo -n true

if [ "$failures" -ne 0 ]; then
    printf 'Mac authority is not ready: %s external prerequisite(s) missing.\n' "$failures"
    printf 'No registry enablement is performed; run doctor only after an independent administrator provisions the signed authority.\n'
    exit 2
fi

printf 'Mac authority prerequisites are present; this script still performs no enablement.\n'
printf 'Run profile-specific credentialed conformance and independent verifier before declaring supported.\n'
