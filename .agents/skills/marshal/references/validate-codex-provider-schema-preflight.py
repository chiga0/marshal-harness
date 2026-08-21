#!/usr/bin/env python3
"""Safely read inputs and render the production Codex schema checker result.

All compatibility rules live in internal/adapter/codex.  This operator-local
wrapper owns only clean relative paths, nofollow/bounded reads, checker process
invocation, and receipt rendering; it is not a second schema implementation.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import sys
from typing import NamedTuple


PROFILE_RELATIVE = ".agents/skills/marshal/references/codex-0.145-provider-schema-profile.json"
MAX_PROFILE_BYTES = 64 * 1024
MAX_SCHEMA_BYTES = 4 * 1024 * 1024
READ_CHUNK_BYTES = 64 * 1024
CHECKER_TIMEOUT_SECONDS = 30
CHECKER_FIELDS = {
    "receiptVersion",
    "status",
    "reasonCode",
    "adapterId",
    "cliCompatibilityLine",
    "authorityScope",
    "authorityClaim",
    "profileVersion",
    "profileDigest",
    "rulesDigest",
    "schemaDigest",
    "schemaBytes",
    "issueCount",
    "issues",
}


class ReadResult(NamedTuple):
    raw: bytes
    digest: str
    size: int


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def load_stable_marshal_module():
    path = Path(__file__).with_name("stable_marshal.py")
    spec = importlib.util.spec_from_file_location("marshal_stable_marshal", path)
    if spec is None or spec.loader is None:
        fail("codex-provider-checker-failed", "stable Marshal identity implementation unavailable")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def sha256_digest(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def clean_relative_path(raw: str, reason_code: str) -> str:
    if not isinstance(raw, str) or not raw or "\\" in raw or "\x00" in raw:
        fail(reason_code, "path must be a clean non-empty POSIX relative path")
    candidate = PurePosixPath(raw)
    if candidate.is_absolute() or raw != candidate.as_posix() or any(
        part in {"", ".", ".."} for part in candidate.parts
    ):
        fail(reason_code, "path must be a clean non-empty POSIX relative path")
    return raw


def open_root(root: Path) -> int:
    try:
        metadata = root.lstat()
    except OSError:
        fail("codex-provider-preflight-root-invalid", "root is unavailable")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("codex-provider-preflight-root-invalid", "root must be a real directory")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        return os.open(root, flags)
    except OSError:
        fail("codex-provider-preflight-root-invalid", "root cannot be opened safely")


def read_regular_file_at(
    root_fd: int,
    relative: str,
    limit: int,
    *,
    path_reason: str,
    unreadable_reason: str,
    too_large_reason: str,
    changed_reason: str,
) -> ReadResult:
    clean_relative_path(relative, path_reason)
    directory_fds: list[int] = []
    directory_identities: list[tuple[int, int, int]] = []
    current_fd = root_fd
    components = PurePosixPath(relative).parts
    try:
        directory_flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
        for component in components[:-1]:
            try:
                next_fd = os.open(component, directory_flags, dir_fd=current_fd)
            except OSError:
                fail(unreadable_reason, "path cannot be traversed with nofollow")
            directory_fds.append(next_fd)
            metadata = os.fstat(next_fd)
            directory_identities.append((metadata.st_dev, metadata.st_ino, metadata.st_mode))
            current_fd = next_fd
        try:
            file_fd = os.open(
                components[-1],
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0),
                dir_fd=current_fd,
            )
        except OSError:
            fail(unreadable_reason, "file cannot be opened with nofollow")
        try:
            before = os.fstat(file_fd)
            if not stat.S_ISREG(before.st_mode) or before.st_size <= 0:
                fail(unreadable_reason, "input must be a non-empty regular file")
            if before.st_size > limit:
                fail(too_large_reason, "input exceeds its bounded-read limit")
            chunks: list[bytes] = []
            remaining = limit + 1
            while remaining > 0:
                chunk = os.read(file_fd, min(READ_CHUNK_BYTES, remaining))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
            raw = b"".join(chunks)
            if len(raw) > limit:
                fail(too_large_reason, "input grew beyond its bounded-read limit")
            after = os.fstat(file_fd)
            before_identity = (before.st_dev, before.st_ino, before.st_mode, before.st_size, before.st_mtime_ns, before.st_ctime_ns)
            after_identity = (after.st_dev, after.st_ino, after.st_mode, after.st_size, after.st_mtime_ns, after.st_ctime_ns)
            if before_identity != after_identity or len(raw) != before.st_size:
                fail(changed_reason, "input identity changed during bounded read")

            recheck_fds: list[int] = []
            recheck_current = root_fd
            try:
                for index, component in enumerate(components[:-1]):
                    try:
                        descriptor = os.open(component, directory_flags, dir_fd=recheck_current)
                    except OSError:
                        fail(changed_reason, "input parent changed during bounded read")
                    recheck_fds.append(descriptor)
                    metadata = os.fstat(descriptor)
                    if (metadata.st_dev, metadata.st_ino, metadata.st_mode) != directory_identities[index]:
                        fail(changed_reason, "input parent identity changed during bounded read")
                    recheck_current = descriptor
                try:
                    rebound_fd = os.open(
                        components[-1],
                        os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0),
                        dir_fd=recheck_current,
                    )
                except OSError:
                    fail(changed_reason, "input leaf changed during bounded read")
                try:
                    rebound = os.fstat(rebound_fd)
                    if (rebound.st_dev, rebound.st_ino, rebound.st_mode) != (before.st_dev, before.st_ino, before.st_mode):
                        fail(changed_reason, "input leaf identity changed during bounded read")
                finally:
                    os.close(rebound_fd)
            finally:
                for descriptor in reversed(recheck_fds):
                    os.close(descriptor)
            return ReadResult(raw=raw, digest=sha256_digest(raw), size=len(raw))
        finally:
            os.close(file_fd)
    finally:
        for descriptor in reversed(directory_fds):
            os.close(descriptor)


def marshal_argv(explicit: str) -> tuple[list[str], Path]:
    repository_root = Path(__file__).resolve().parents[4]
    path = Path(explicit)
    if not path.is_absolute() or path != Path(os.path.normpath(path)):
        fail("codex-provider-checker-path-invalid", "checker path must be absolute and clean")
    try:
        metadata = path.lstat()
    except OSError:
        fail("codex-provider-checker-unavailable", "checker is unavailable")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or not os.access(path, os.X_OK):
        fail("codex-provider-checker-unavailable", "checker must be an executable regular file")
    return [str(path), "internal", "codex-provider-schema-check", "--attestation-ready"], repository_root


def invoke_checker(schema: ReadResult, profile: ReadResult, explicit: str) -> dict:
    argv, cwd = marshal_argv(explicit)
    try:
        before = Path(explicit).lstat()
        with Path(explicit).open("rb") as executable:
            marshal_raw = executable.read(MAX_SCHEMA_BYTES * 16 + 1)
    except OSError:
        fail("codex-provider-checker-failed", "stable Marshal path changed before execution")
    marshal_digest = sha256_digest(marshal_raw)
    request = json.dumps(
        {
            "schema": base64.b64encode(schema.raw).decode("ascii"),
            "profile": base64.b64encode(profile.raw).decode("ascii"),
        },
        sort_keys=True,
        separators=(",", ":"),
    )
    try:
        environment = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C"}
        identity = load_stable_marshal_module()
        try:
            held = identity.hold(Path(explicit))
        except Exception:
            fail("codex-provider-checker-failed", "stable Marshal identity attestation failed")
        process = subprocess.Popen(argv, cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment)
        try:
            stdout, stderr, returncode = identity.execute(
                held, process, b"\0" + request.encode("utf-8"), CHECKER_TIMEOUT_SECONDS,
                64 << 10, 64 << 10, 128 << 10,
            )
        finally:
            held.close()
        completed = subprocess.CompletedProcess(argv, returncode, stdout, stderr)
    except Exception:
        fail("codex-provider-checker-failed", "production checker did not complete")
    try:
        after = Path(explicit).lstat()
        with Path(explicit).open("rb") as executable:
            after_raw = executable.read(MAX_SCHEMA_BYTES * 16 + 1)
    except OSError:
        fail("codex-provider-checker-failed", "stable Marshal path changed")
    if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns) or sha256_digest(after_raw) != marshal_digest:
        fail("codex-provider-checker-failed", "stable Marshal executable changed during execution")
    result = None
    stdout_text = completed.stdout.decode("utf-8", errors="replace") if isinstance(completed.stdout, bytes) else completed.stdout
    stderr_text = completed.stderr.decode("utf-8", errors="replace") if isinstance(completed.stderr, bytes) else completed.stderr
    if completed.returncode == 0 and stderr_text:
        fail("codex-provider-checker-failed", "production checker emitted stderr on success")
    if completed.returncode == 2 and stdout_text:
        fail("codex-provider-checker-failed", "production checker emitted stdout on failure")
    for line in (stdout_text + "\n" + stderr_text).splitlines():
        try:
            candidate = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(candidate, dict):
            if result is not None:
                fail("codex-provider-checker-failed", "production checker returned multiple JSON documents")
            result = candidate
    if result is None:
        fail("codex-provider-checker-failed", "production checker returned invalid JSON")
    if result.get("status") == "fatal":
        if completed.returncode != 2:
            fail("codex-provider-checker-failed", "production checker fatal status differs from exit code")
        reason = result.get("reasonCode")
        if reason in {"codex-provider-schema-json-invalid", "codex-provider-profile-invalid"}:
            fail(reason, "production checker rejected an input")
        fail("codex-provider-checker-failed", "production checker failed closed")
    if completed.returncode not in {0, 1} or not isinstance(result, dict) or set(result) != CHECKER_FIELDS:
        fail("codex-provider-checker-failed", "production checker result shape is invalid")
    if result.get("schemaDigest") != schema.digest or result.get("schemaBytes") != schema.size or result.get("profileDigest") != profile.digest:
        fail("codex-provider-checker-failed", "production checker input identity differs")
    issues = result.get("issues")
    issue_count = result.get("issueCount")
    if not isinstance(issues, list) or not isinstance(issue_count, int) or isinstance(issue_count, bool) or issue_count != len(issues):
        fail("codex-provider-checker-failed", "production checker issue count differs")
    if any(
        not isinstance(issue, dict)
        or set(issue) != {"code", "jsonPointer", "keyword"}
        or any(not isinstance(issue[field], str) for field in ("code", "jsonPointer", "keyword"))
        for issue in issues
    ):
        fail("codex-provider-checker-failed", "production checker issue shape is invalid")
    encoded = [json.dumps(issue, sort_keys=True, separators=(",", ":")) for issue in issues]
    if len(encoded) != len(set(encoded)) or encoded != sorted(encoded, key=lambda raw_issue: (
        json.loads(raw_issue).get("jsonPointer", ""),
        json.loads(raw_issue).get("code", ""),
        json.loads(raw_issue).get("keyword", ""),
    )):
        fail("codex-provider-checker-failed", "production checker issues are not unique and sorted")
    expected_return = 0 if result.get("status") == "pass" else 1
    expected_reason = "codex-provider-schema-compatible" if expected_return == 0 else "codex-provider-schema-incompatible"
    if completed.returncode != expected_return or result.get("reasonCode") != expected_reason or (expected_return == 0) != (issue_count == 0):
        fail("codex-provider-checker-failed", "production checker status is inconsistent")
    return result


def build_receipt(schema_path: str, schema: ReadResult, result: dict) -> dict:
    return {
        "receiptVersion": result["receiptVersion"],
        "status": result["status"],
        "reasonCode": result["reasonCode"],
        "adapterId": result["adapterId"],
        "cliCompatibilityLine": result["cliCompatibilityLine"],
        "authorityScope": result["authorityScope"],
        "authorityClaim": result["authorityClaim"],
        "profileVersion": result["profileVersion"],
        "profileDigest": result["profileDigest"],
        "rulesDigest": result["rulesDigest"],
        "schema": {
            "path": schema_path,
            "rawDigest": result["schemaDigest"],
            "rawBytes": result["schemaBytes"],
            "nofollow": True,
            "boundedRead": True,
        },
        "issueCount": result["issueCount"],
        "issues": result["issues"],
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="trusted real directory root")
    parser.add_argument("--schema", required=True, help="clean relative provider schema path")
    parser.add_argument("--profile", default=PROFILE_RELATIVE, help="clean relative frozen profile path")
    parser.add_argument("--marshal", required=True, help="absolute stable Marshal executable")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root_fd: int | None = None
    try:
        root_fd = open_root(Path(args.root))
        profile = read_regular_file_at(
            root_fd, args.profile, MAX_PROFILE_BYTES,
            path_reason="codex-provider-profile-path-invalid",
            unreadable_reason="codex-provider-profile-unreadable",
            too_large_reason="codex-provider-profile-too-large",
            changed_reason="codex-provider-profile-identity-changed",
        )
        schema = read_regular_file_at(
            root_fd, args.schema, MAX_SCHEMA_BYTES,
            path_reason="codex-provider-schema-path-invalid",
            unreadable_reason="codex-provider-schema-unreadable",
            too_large_reason="codex-provider-schema-too-large",
            changed_reason="codex-provider-schema-identity-changed",
        )
        result = invoke_checker(schema, profile, args.marshal)
        receipt = build_receipt(args.schema, schema, result)
        stream = sys.stdout if result["status"] == "pass" else sys.stderr
        print(json.dumps(receipt, ensure_ascii=False, sort_keys=True), file=stream)
        return 0 if result["status"] == "pass" else 1
    except PreflightError as error:
        print(json.dumps({"status": "fail", "reasonCode": error.reason_code, "message": str(error)}, ensure_ascii=False, sort_keys=True), file=sys.stderr)
        return 2
    finally:
        if root_fd is not None:
            os.close(root_fd)


if __name__ == "__main__":
    raise SystemExit(main())
