#!/usr/bin/env python3
"""Validate the closed main-CI/release-check source contract.

This is intentionally not a YAML parser. GitHub workflow YAML has aliases,
flow collections, merge keys, duplicate keys, scalar coercions and expression
semantics that a small parser cannot reproduce safely. The release contract
admits the reviewed CI workflow byte-for-byte and binds the complete release
workflow to an explicitly versioned content digest. The Make target is
convenience only; CI invokes this checker directly, before any candidate
Makefile or script is executed.
"""

from __future__ import annotations

import difflib
import hashlib
import os
import pathlib
import re
import stat
import subprocess
import sys


CHECKER_RELATIVE_PATH = "scripts/release-ci-contract.py"
RELEASE_WORKFLOW_RELATIVE_PATH = ".github/workflows/release.yml"
RELEASE_WORKFLOW_DIGEST_RELATIVE_PATH = "scripts/release-workflow.sha256"
FIXED_FILES = {
    ".github/workflows/ci.yml": "100644",
    RELEASE_WORKFLOW_RELATIVE_PATH: "100644",
    "Makefile": "100644",
    CHECKER_RELATIVE_PATH: "100644",
    "scripts/release-artifact-metadata-check.py": "100644",
    "scripts/release-rc1-binary-check.py": "100755",
    RELEASE_WORKFLOW_DIGEST_RELATIVE_PATH: "100644",
    "scripts/release-contract_test.sh": "100755",
    "scripts/release-ci-gate_test.sh": "100755",
    "scripts/dist-profile_test.sh": "100755",
    "scripts/install_test.sh": "100755",
    "scripts/release-canary_test.sh": "100755",
}
MAX_FILE_BYTES = 1 << 20

EXPECTED_WORKFLOW = """name: CI

on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  quality:
    name: Quality (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    timeout-minutes: 20
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    steps:
      - name: Checkout
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1

      # Release authority runs before any candidate Make target, script, toolchain
      # setup or GITHUB_ENV write. Keep all executables and environment explicit.
      - name: Run release contract gate
        if: matrix.os == 'ubuntu-latest'
        shell: /bin/bash --noprofile --norc -euo pipefail {0}
        env:
          BASH_ENV: /dev/null
          ENV: /dev/null
          MAKEFLAGS: ''
          PATH: /usr/bin:/bin
          PYTHONHOME: ''
          PYTHONPATH: ''
        run: |
          checker="$GITHUB_WORKSPACE/scripts/release-ci-contract.py"
          run_checker() {
            /usr/bin/env -i \\
              LC_ALL=C \\
              PATH=/usr/bin:/bin \\
              /usr/bin/python3 -I -B "$checker" "$GITHUB_WORKSPACE"
          }
          run_checker
          for test_path in \\
            scripts/release-contract_test.sh \\
            scripts/release-ci-gate_test.sh \\
            scripts/dist-profile_test.sh \\
            scripts/install_test.sh \\
            scripts/release-canary_test.sh; do
            run_checker
            /usr/bin/env -i \\
              LC_ALL=C \\
              PATH=/usr/bin:/bin \\
              /bin/bash --noprofile --norc "$GITHUB_WORKSPACE/$test_path"
          done
          run_checker

      - name: Set up Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
          cache: true

      - name: Download modules
        run: go mod download

      - name: Verify modules
        run: go mod verify

      - name: Run quality gate
        run: make check

      - name: Run vulnerability scan
        run: make vuln

  secrets:
    name: Secret scan
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - name: Checkout full history
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0

      - name: Run Gitleaks
        uses: gitleaks/gitleaks-action@e0c47f4f8be36e29cdc102c57e68cb5cbf0e8d1e # v3.0.0
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
"""

EXPECTED_RELEASE_TARGET = """release-check:
\tbash scripts/release-contract_test.sh
\tbash scripts/release-ci-gate_test.sh
\tbash scripts/dist-profile_test.sh
\tbash scripts/install_test.sh
\tbash scripts/release-canary_test.sh

"""

FORBIDDEN_MAKE_DIRECTIVES = (
    re.compile(r"^\.IGNORE(?:\s*:|\s*$)"),
    re.compile(r"^\.ONESHELL(?:\s*:|\s*$)"),
    re.compile(r"^(?:override\s+|export\s+|unexport\s+)?SHELL\s*[:+?]?="),
    re.compile(r"^\.SHELLFLAGS\s*[:+?]?="),
    re.compile(r"^(?:-?include|sinclude)(?:\s|$)"),
    re.compile(r"^(?:override\s+|export\s+|unexport\s+)?MAKEFLAGS\s*[:+?]?="),
)


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"[release-ci-contract] ERROR: {message}")


def exact_root(argument: str) -> pathlib.Path:
    if not argument or not os.path.isabs(argument) or os.path.normpath(argument) != argument:
        fail("repository root must be one normalized absolute path")
    root = pathlib.Path(argument)
    try:
        metadata = os.lstat(root)
    except OSError as error:
        fail(f"cannot lstat repository root: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("repository root must be a real directory, never a symlink")
    if os.path.realpath(argument) != argument:
        fail("repository root or one of its parents resolves through a symlink")
    expected_checker = root / CHECKER_RELATIVE_PATH
    invoked_checker = pathlib.Path(os.path.abspath(__file__))
    if invoked_checker != expected_checker:
        fail("checker executable path is not the fixed repository path")
    return root


def read_fixed_file(root: pathlib.Path, relative: str) -> tuple[bytes, os.stat_result]:
    path = root / relative
    current = root
    for component in pathlib.PurePosixPath(relative).parts[:-1]:
        current = current / component
        try:
            metadata = os.lstat(current)
        except OSError as error:
            fail(f"cannot lstat fixed path parent {current}: {error}")
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
            fail(f"fixed path parent must be a real directory: {current}")
    try:
        before = os.lstat(path)
    except OSError as error:
        fail(f"cannot lstat fixed path {relative}: {error}")
    if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
        fail(f"fixed path must be a regular non-symlink: {relative}")
    expected_permissions = 0o755 if FIXED_FILES[relative] == "100755" else 0o644
    if stat.S_IMODE(before.st_mode) != expected_permissions:
        fail(f"fixed path filesystem mode mismatch: {relative}")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open fixed path {relative}: {error}")
    try:
        held = os.fstat(descriptor)
        if (
            before.st_dev,
            before.st_ino,
            before.st_mode,
            before.st_size,
        ) != (held.st_dev, held.st_ino, held.st_mode, held.st_size):
            fail(f"fixed path changed while opening: {relative}")
        if held.st_size > MAX_FILE_BYTES:
            fail(f"fixed path exceeds 1 MiB: {relative}")
        chunks = []
        remaining = MAX_FILE_BYTES + 1
        while remaining:
            chunk = os.read(descriptor, min(65536, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        data = b"".join(chunks)
        after = os.fstat(descriptor)
        if (held.st_dev, held.st_ino, held.st_mode, held.st_size) != (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_size,
        ):
            fail(f"fixed path changed while reading: {relative}")
        if len(data) != held.st_size or len(data) > MAX_FILE_BYTES:
            fail(f"fixed path length is not stable/bounded: {relative}")
        return data, held
    finally:
        os.close(descriptor)


def decode_text(relative: str, raw: bytes) -> str:
    if raw.startswith(b"\xef\xbb\xbf"):
        fail(f"UTF-8 BOM is not allowed: {relative}")
    if b"\x00" in raw or b"\r" in raw:
        fail(f"NUL/CR bytes are not allowed: {relative}")
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError as error:
        fail(f"non-UTF-8 input {relative}: {error}")


def git_command(root: pathlib.Path, *arguments: str) -> bytes:
    environment = {
        "GIT_CONFIG_NOSYSTEM": "1",
        "HOME": "/nonexistent",
        "LC_ALL": "C",
        "PATH": "/usr/bin:/bin",
    }
    try:
        result = subprocess.run(
            ["/usr/bin/git", "-C", str(root), *arguments],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env=environment,
            timeout=10,
        )
    except (OSError, subprocess.SubprocessError) as error:
        fail(f"cannot inspect Git tree: {error}")
    if result.returncode != 0 or len(result.stdout) > MAX_FILE_BYTES:
        fail(f"Git tree inspection failed: {result.stderr[:1024]!r}")
    return result.stdout


def git_blob_sha1(raw: bytes) -> str:
    header = f"blob {len(raw)}\0".encode("ascii")
    return hashlib.sha1(header + raw).hexdigest()


def check_git_bindings(root: pathlib.Path, fixed_bytes: dict[str, bytes]) -> None:
    top_level = git_command(root, "rev-parse", "--show-toplevel").rstrip(b"\n")
    if top_level != os.fsencode(root):
        fail("Git top-level does not exactly match repository root")
    if git_command(root, "rev-parse", "--show-object-format").strip() != b"sha1":
        fail("release contract currently requires the repository SHA-1 object format")
    output = git_command(root, "ls-tree", "-z", "HEAD", "--", *FIXED_FILES)
    entries = {}
    for record in output.rstrip(b"\0").split(b"\0") if output else []:
        try:
            metadata, raw_path = record.split(b"\t", 1)
            mode, kind, object_id = metadata.decode("ascii").split(" ")
            relative = raw_path.decode("utf-8")
        except (UnicodeDecodeError, ValueError):
            fail("Git tree returned a malformed fixed-path record")
        if relative in entries:
            fail(f"Git tree returned duplicate path: {relative}")
        entries[relative] = (mode, kind, object_id)
    if set(entries) != set(FIXED_FILES):
        fail("Git tree fixed-path set is incomplete or contains an alias")
    for relative, expected_mode in FIXED_FILES.items():
        mode, kind, object_id = entries[relative]
        if mode != expected_mode or kind != "blob":
            fail(f"Git tree mode/type mismatch: {relative}")
        if object_id != git_blob_sha1(fixed_bytes[relative]):
            fail(f"fixed bytes do not match HEAD tree blob: {relative}")


def extract_release_target(makefile: str) -> str:
    lines = makefile.splitlines(keepends=True)
    starts = [index for index, line in enumerate(lines) if line == "release-check:\n"]
    if len(starts) != 1:
        fail("Makefile must contain exactly one literal release-check target")
    start = starts[0]
    end = len(lines)
    for index in range(start + 1, len(lines)):
        line = lines[index]
        if line.strip() and not line.startswith((" ", "\t", "#")):
            end = index
            break
    return "".join(lines[start:end])


def logical_make_lines(makefile: str) -> list[str]:
    result = []
    pending = ""
    for raw in makefile.splitlines():
        stripped = raw.lstrip()
        if not pending and (not stripped or stripped.startswith("#") or raw.startswith("\t")):
            continue
        current = pending + stripped
        if current.endswith("\\"):
            pending = current[:-1] + " "
            continue
        result.append(current)
        pending = ""
    if pending:
        result.append(pending)
    return result


def check_makefile(makefile: str) -> None:
    definitions = []
    for line in logical_make_lines(makefile):
        active = line.split("#", 1)[0].rstrip()
        if any(pattern.match(active) for pattern in FORBIDDEN_MAKE_DIRECTIVES):
            fail(f"Makefile contains forbidden global release-check semantics: {active}")
        if ":" in active and "release-check" in active.split(":", 1)[0].split():
            definitions.append(active)
    if definitions != ["release-check:"]:
        fail("release-check must have one literal rule with no prerequisites")
    declarations = [
        line.removeprefix(".PHONY:").strip().split()
        for line in makefile.splitlines()
        if line.startswith(".PHONY:")
    ]
    occurrences = sum(words.count("release-check") for words in declarations)
    if occurrences != 1:
        fail("release-check must appear exactly once in literal .PHONY declarations")
    require_exact(
        "release-check-target",
        extract_release_target(makefile),
        EXPECTED_RELEASE_TARGET,
    )


def check_release_workflow(raw_workflow: bytes, digest_contract: str) -> None:
    match = re.fullmatch(r"sha256:([0-9a-f]{64})\n", digest_contract)
    if match is None:
        fail("release workflow digest contract is not one canonical sha256 line")
    observed = hashlib.sha256(raw_workflow).hexdigest()
    if observed != match.group(1):
        fail(
            "release workflow left its versioned exact-byte contract: "
            f"expected={match.group(1)} observed={observed}"
        )


def require_exact(label: str, actual: str, expected: str) -> None:
    if actual == expected:
        return
    diff = "".join(
        difflib.unified_diff(
            expected.splitlines(keepends=True),
            actual.splitlines(keepends=True),
            fromfile=f"expected-{label}",
            tofile=f"actual-{label}",
            n=2,
        )
    )
    fail(f"{label} left the closed release contract:\n{diff[:8192]}")


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        fail("usage: release-ci-contract.py ABSOLUTE_REPOSITORY_ROOT")
    root = exact_root(argv[1])
    fixed_bytes = {
        relative: read_fixed_file(root, relative)[0] for relative in FIXED_FILES
    }
    check_git_bindings(root, fixed_bytes)
    workflow = decode_text(".github/workflows/ci.yml", fixed_bytes[".github/workflows/ci.yml"])
    release_workflow = fixed_bytes[RELEASE_WORKFLOW_RELATIVE_PATH]
    release_workflow_digest = decode_text(
        RELEASE_WORKFLOW_DIGEST_RELATIVE_PATH,
        fixed_bytes[RELEASE_WORKFLOW_DIGEST_RELATIVE_PATH],
    )
    makefile = decode_text("Makefile", fixed_bytes["Makefile"])
    decode_text(CHECKER_RELATIVE_PATH, fixed_bytes[CHECKER_RELATIVE_PATH])
    require_exact("workflow", workflow, EXPECTED_WORKFLOW)
    check_release_workflow(release_workflow, release_workflow_digest)
    check_makefile(makefile)
    print("[release-ci-contract] PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
