#!/bin/sh
set -eu

# Mac-first authority 预检只读检查。
# 它不安装文件、不签名、不 bootstrap launchd，也不读取 credential。

qoder_path=${MARSHAL_QODER_PATH:-/Users/gawain/.qoder/bin/qodercli/qodercli-1.1.23}
codex_path=${MARSHAL_CODEX_PATH:-/opt/homebrew/Caskroom/codex/0.145.0/codex-aarch64-apple-darwin}
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

if [ "$(uname -s)" != "Darwin" ]; then
    printf 'BLOCKED platform: Darwin required\n'
    exit 2
fi

check "qoder executable" test -x "$qoder_path"
check "codex executable" test -x "$codex_path"
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
check "noninteractive sudo" sudo -n true

if [ "$failures" -ne 0 ]; then
    printf 'Mac authority is not ready: %s external prerequisite(s) missing.\n' "$failures"
    printf 'No registry enablement is performed; run doctor only after an independent administrator provisions the signed authority.\n'
    exit 2
fi

printf 'Mac authority prerequisites are present; this script still performs no enablement.\n'
printf 'Run profile-specific credentialed conformance and independent verifier before declaring supported.\n'
