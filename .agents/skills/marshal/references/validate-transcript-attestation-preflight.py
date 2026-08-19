#!/usr/bin/env python3
"""Fail-closed operator-local Qoder v5 transcript attestation preflight."""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tempfile


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
SCHEMA_NAME = "transcript-attestation-preflight.schema.json"
READ_CHUNK_BYTES = 1024 * 1024
MANIFEST_MAX_BYTES = 1024 * 1024
SCHEMA_MAX_BYTES = 1024 * 1024
CHECKER_MAX_BYTES = 64 * 1024 * 1024
CODESIGN_MAX_OUTPUT_BYTES = 64 * 1024
CODESIGN_TIMEOUT_SECONDS = 10
CHECKER_TIMEOUT_SECONDS = 120
CODESIGN_PATH = Path("/usr/bin/codesign")
CDHASH_FULL_RE = re.compile(rb"(?m)^CandidateCDHashFull sha256=([0-9a-f]{64})$")
INPUT_HARD_LIMITS = {
    "transcript": 8 * 1024 * 1024,
    "transcriptMeta": 128 * 1024,
    "workerResult": 256 * 1024,
    "workerRequest": 512 * 1024,
    "taskSpec": 2 * 1024 * 1024,
    "capabilitySnapshot": 512 * 1024,
    "profile": 256 * 1024,
}


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def sha256_bytes(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def clean_relative_path(value: object, label: str) -> str:
    if not isinstance(value, str) or not value:
        fail("path-boundary-invalid", f"{label} must be a non-empty relative path")
    if "\\" in value or "\x00" in value:
        fail("path-boundary-invalid", f"{label} contains a forbidden path character")
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        fail("path-boundary-invalid", f"{label} must be a clean relative path")
    canonical = path.as_posix()
    if canonical != value:
        fail("path-boundary-invalid", f"{label} must use its byte-for-byte canonical spelling")
    return canonical


def open_root_nofollow(root: Path) -> int:
    try:
        metadata = root.lstat()
    except OSError as error:
        fail("input-root-invalid", f"cannot lstat input root: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("input-root-invalid", "input root must be a non-symlink directory")
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW
    try:
        descriptor = os.open(root, flags)
    except OSError as error:
        fail("input-root-invalid", f"cannot open input root without following links: {error}")
    opened = os.fstat(descriptor)
    if (opened.st_dev, opened.st_ino) != (metadata.st_dev, metadata.st_ino):
        os.close(descriptor)
        fail("input-root-invalid", "input root changed during nofollow open")
    return descriptor


def read_relative_nofollow_with_identity(
    root: Path, relative: object, maximum: int, label: str
) -> tuple[bytes, tuple[int, int]]:
    path = clean_relative_path(relative, label)
    root_fd = open_root_nofollow(root)
    directory_fd = root_fd
    opened_directories: list[int] = []
    directory_identities: list[tuple[str, tuple[int, int, int]]] = []
    try:
        parts = PurePosixPath(path).parts
        for component in parts[:-1]:
            try:
                next_fd = os.open(
                    component,
                    os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
                    dir_fd=directory_fd,
                )
            except OSError as error:
                fail("input-path-invalid", f"cannot open directory for {label}: {error}")
            opened_directories.append(next_fd)
            opened = os.fstat(next_fd)
            directory_identities.append(
                (component, (opened.st_dev, opened.st_ino, opened.st_mode))
            )
            directory_fd = next_fd
        try:
            file_fd = os.open(
                parts[-1],
                os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                dir_fd=directory_fd,
            )
        except OSError as error:
            fail("input-path-invalid", f"cannot open {label} without following links: {error}")
        try:
            before = os.fstat(file_fd)
            if not stat.S_ISREG(before.st_mode):
                fail("input-file-invalid", f"{label} must be a regular file")
            if before.st_size > maximum:
                fail("input-too-large", f"{label} exceeds its {maximum}-byte bound")
            chunks: list[bytes] = []
            total = 0
            while True:
                chunk = os.read(file_fd, min(READ_CHUNK_BYTES, maximum + 1 - total))
                if not chunk:
                    break
                chunks.append(chunk)
                total += len(chunk)
                if total > maximum:
                    fail("input-too-large", f"{label} grew beyond its {maximum}-byte bound")
            after = os.fstat(file_fd)
            identity_before = (
                before.st_dev,
                before.st_ino,
                before.st_mode,
                before.st_size,
                before.st_mtime_ns,
                before.st_ctime_ns,
            )
            identity_after = (
                after.st_dev,
                after.st_ino,
                after.st_mode,
                after.st_size,
                after.st_mtime_ns,
                after.st_ctime_ns,
            )
            raw = b"".join(chunks)
            if identity_before != identity_after or len(raw) != after.st_size:
                fail("input-changed-during-read", f"{label} changed during bounded read")
            # Re-walk the lexical path from the held root dirfd. The file fd
            # already prevents redirecting bytes during the read; this second
            # walk additionally fails closed when a parent or leaf was swapped
            # while those bytes were being captured.
            recheck_parent = root_fd
            recheck_directories: list[int] = []
            recheck_file = -1
            try:
                for component, expected in directory_identities:
                    try:
                        recheck_fd = os.open(
                            component,
                            os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
                            dir_fd=recheck_parent,
                        )
                    except OSError as error:
                        fail("input-changed-during-read", f"{label} parent changed: {error}")
                    recheck_directories.append(recheck_fd)
                    current = os.fstat(recheck_fd)
                    if (current.st_dev, current.st_ino, current.st_mode) != expected:
                        fail("input-changed-during-read", f"{label} parent identity changed")
                    recheck_parent = recheck_fd
                try:
                    recheck_file = os.open(
                        parts[-1],
                        os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                        dir_fd=recheck_parent,
                    )
                except OSError as error:
                    fail("input-changed-during-read", f"{label} leaf changed: {error}")
                current_file = os.fstat(recheck_file)
                if (
                    current_file.st_dev,
                    current_file.st_ino,
                    current_file.st_mode,
                ) != (before.st_dev, before.st_ino, before.st_mode):
                    fail("input-changed-during-read", f"{label} leaf identity changed")
            finally:
                if recheck_file >= 0:
                    os.close(recheck_file)
                for descriptor in reversed(recheck_directories):
                    os.close(descriptor)
            return raw, (before.st_dev, before.st_ino)
        finally:
            os.close(file_fd)
    finally:
        for descriptor in reversed(opened_directories):
            os.close(descriptor)
        os.close(root_fd)


def read_relative_nofollow(root: Path, relative: object, maximum: int, label: str) -> bytes:
    raw, _ = read_relative_nofollow_with_identity(root, relative, maximum, label)
    return raw


def parse_json(raw: bytes, label: str) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key", f"{label} contains duplicate key {key!r}")
            result[key] = value
        return result

    try:
        value = json.loads(
            raw.decode("utf-8"),
            object_pairs_hook=reject_duplicates,
            parse_constant=lambda token: fail("invalid-json", f"{label} contains a non-finite number"),
        )
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError) as error:
        fail("invalid-json", f"cannot decode {label}: {error}")
    if not isinstance(value, dict):
        fail("invalid-json", f"{label} must be a JSON object")
    return value


SUPPORTED_SCHEMA_KEYS = {
    "$schema",
    "$id",
    "$ref",
    "$defs",
    "title",
    "description",
    "type",
    "const",
    "enum",
    "additionalProperties",
    "required",
    "properties",
    "items",
    "minItems",
    "maxItems",
    "uniqueItems",
    "minLength",
    "maxLength",
    "pattern",
    "minimum",
    "maximum",
}


def validate_schema_document(schema: dict) -> None:
    if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
        fail("schema-document-invalid", "manifest schema must declare Draft 2020-12")
    if not isinstance(schema.get("$id"), str) or "operator-local" not in schema["$id"]:
        fail("schema-document-invalid", "manifest schema must use an operator-local id")

    def walk(node: object, location: str) -> None:
        if isinstance(node, bool):
            return
        if not isinstance(node, dict):
            fail("schema-document-invalid", f"schema node {location} must be an object or boolean")
        unknown = set(node) - SUPPORTED_SCHEMA_KEYS
        if unknown:
            fail(
                "schema-document-invalid",
                f"unsupported schema keywords at {location}: {', '.join(sorted(unknown))}",
            )
        for key in ("properties", "$defs"):
            if key in node:
                if not isinstance(node[key], dict):
                    fail("schema-document-invalid", f"{location}/{key} must be an object")
                for child_key, child in node[key].items():
                    walk(child, f"{location}/{key}/{child_key}")
        if "items" in node:
            walk(node["items"], f"{location}/items")

    walk(schema, "#")


def resolve_ref(schema: dict, reference: str) -> object:
    if not reference.startswith("#/"):
        fail("schema-document-invalid", f"only local schema refs are supported: {reference}")
    current: object = schema
    for raw_part in reference[2:].split("/"):
        part = raw_part.replace("~1", "/").replace("~0", "~")
        if not isinstance(current, dict) or part not in current:
            fail("schema-document-invalid", f"unresolved schema ref {reference}")
        current = current[part]
    return current


def type_matches(value: object, declared: str) -> bool:
    if declared == "object":
        return isinstance(value, dict)
    if declared == "array":
        return isinstance(value, list)
    if declared == "string":
        return isinstance(value, str)
    if declared == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if declared == "boolean":
        return isinstance(value, bool)
    return False


def validate_schema_instance(value: object, node: object, root: dict, location: str = "$") -> None:
    if node is True:
        return
    if node is False or not isinstance(node, dict):
        fail("manifest-schema-invalid", f"schema rejected {location}")
    if "$ref" in node:
        validate_schema_instance(value, resolve_ref(root, node["$ref"]), root, location)
        return
    declared_type = node.get("type")
    if declared_type is not None and (
        not isinstance(declared_type, str) or not type_matches(value, declared_type)
    ):
        fail("manifest-schema-invalid", f"{location} must have type {declared_type}")
    if "const" in node and value != node["const"]:
        fail("manifest-schema-invalid", f"{location} does not equal its const")
    if "enum" in node and value not in node["enum"]:
        fail("manifest-schema-invalid", f"{location} is outside its enum")
    if isinstance(value, dict):
        required = node.get("required", [])
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            fail("schema-document-invalid", f"required at {location} must be a string array")
        missing = sorted(set(required) - set(value))
        if missing:
            fail("manifest-schema-invalid", f"{location} missing keys: {', '.join(missing)}")
        properties = node.get("properties", {})
        if not isinstance(properties, dict):
            fail("schema-document-invalid", f"properties at {location} must be an object")
        if node.get("additionalProperties") is False:
            unknown = sorted(set(value) - set(properties))
            if unknown:
                fail("manifest-schema-invalid", f"{location} has unknown keys: {', '.join(unknown)}")
        for key, child in value.items():
            if key in properties:
                validate_schema_instance(child, properties[key], root, f"{location}.{key}")
    if isinstance(value, list):
        if "minItems" in node and len(value) < node["minItems"]:
            fail("manifest-schema-invalid", f"{location} has too few items")
        if "maxItems" in node and len(value) > node["maxItems"]:
            fail("manifest-schema-invalid", f"{location} has too many items")
        if node.get("uniqueItems") is True:
            encoded = [json.dumps(item, ensure_ascii=False, sort_keys=True) for item in value]
            if len(encoded) != len(set(encoded)):
                fail("manifest-schema-invalid", f"{location} items must be unique")
        if "items" in node:
            for index, child in enumerate(value):
                validate_schema_instance(child, node["items"], root, f"{location}[{index}]")
    if isinstance(value, str):
        if "minLength" in node and len(value) < node["minLength"]:
            fail("manifest-schema-invalid", f"{location} is too short")
        if "maxLength" in node and len(value) > node["maxLength"]:
            fail("manifest-schema-invalid", f"{location} is too long")
        if "pattern" in node and re.search(node["pattern"], value) is None:
            fail("manifest-schema-invalid", f"{location} does not match its pattern")
    if isinstance(value, int) and not isinstance(value, bool):
        if "minimum" in node and value < node["minimum"]:
            fail("manifest-schema-invalid", f"{location} is below its minimum")
        if "maximum" in node and value > node["maximum"]:
            fail("manifest-schema-invalid", f"{location} exceeds its maximum")


def load_inputs(root: Path, manifest: dict) -> tuple[dict[str, bytes], dict[str, str]]:
    raw_inputs: dict[str, bytes] = {}
    digests: dict[str, str] = {}
    identities: set[tuple[int, int]] = set()
    for label, hard_limit in INPUT_HARD_LIMITS.items():
        descriptor = manifest["inputs"][label]
        maximum = descriptor["maxBytes"]
        if maximum > hard_limit:
            fail("input-bound-invalid", f"{label} maxBytes exceeds the validator hard limit")
        raw, identity = read_relative_nofollow_with_identity(
            root, descriptor["path"], maximum, label
        )
        if identity in identities:
            fail("input-path-invalid", "input files must have unique inode identities")
        identities.add(identity)
        digest = sha256_bytes(raw)
        if digest != descriptor["sha256"]:
            fail("input-digest-mismatch", f"{label} raw byte digest does not match manifest")
        raw_inputs[label] = raw
        digests[label] = digest
    if len({manifest["inputs"][label]["path"] for label in INPUT_HARD_LIMITS}) != len(INPUT_HARD_LIMITS):
        fail("input-path-invalid", "each input must use a distinct path")
    return raw_inputs, digests


def attestation_digest(
    manifest_digest: str,
    input_digests: dict[str, str],
    implementation_digests: dict[str, str],
    core_output_digest: str,
) -> str:
    frames = [b"marshal-transcript-attestation-v2\n", manifest_digest.encode("ascii"), b"\n"]
    for label in sorted(input_digests):
        frames.extend([label.encode("ascii"), b"\0", input_digests[label].encode("ascii"), b"\n"])
    for label in sorted(implementation_digests):
        frames.extend([label.encode("ascii"), b"\0", implementation_digests[label].encode("ascii"), b"\n"])
    frames.extend([b"coreOutput\0", core_output_digest.encode("ascii"), b"\n"])
    return sha256_bytes(b"".join(frames))


def checker_stat_identity(metadata: os.stat_result) -> tuple[int, int, int, int, int, int]:
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_mode,
        metadata.st_size,
        metadata.st_mtime_ns,
        metadata.st_ctime_ns,
    )


def read_open_checker(descriptor: int, maximum: int, reason_code: str) -> tuple[bytes, os.stat_result]:
    before = os.fstat(descriptor)
    if not stat.S_ISREG(before.st_mode) or before.st_size <= 0 or before.st_size > maximum:
        fail(reason_code, "checker descriptor is not a bounded regular file")
    os.lseek(descriptor, 0, os.SEEK_SET)
    chunks: list[bytes] = []
    total = 0
    while True:
        chunk = os.read(descriptor, min(READ_CHUNK_BYTES, maximum + 1 - total))
        if not chunk:
            break
        chunks.append(chunk)
        total += len(chunk)
        if total > maximum:
            fail(reason_code, "checker grew beyond its closed size bound")
    after = os.fstat(descriptor)
    raw = b"".join(chunks)
    if checker_stat_identity(before) != checker_stat_identity(after) or len(raw) != after.st_size:
        fail(reason_code, "checker changed during bounded held read")
    return raw, after


def verify_private_checker_path(copied_path: Path, held: os.stat_result, expected_raw: bytes) -> None:
    try:
        descriptor = os.open(
            copied_path,
            os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
        )
    except OSError:
        fail("checker-private-path-changed", "private checker path is no longer the held regular file")
    try:
        raw, opened = read_open_checker(descriptor, CHECKER_MAX_BYTES, "checker-private-path-changed")
        if (opened.st_dev, opened.st_ino) != (held.st_dev, held.st_ino):
            fail("checker-private-path-changed", "private checker path inode differs from held copy")
        if not hmac.compare_digest(hashlib.sha256(raw).digest(), hashlib.sha256(expected_raw).digest()):
            fail("checker-private-path-changed", "private checker path bytes differ from held copy")
    finally:
        os.close(descriptor)


def codesign_identity(target: str) -> str:
    try:
        metadata = CODESIGN_PATH.lstat()
    except OSError:
        fail("checker-process-identity-unavailable", "the fixed system codesign tool is unavailable")
    if (
        stat.S_ISLNK(metadata.st_mode)
        or not stat.S_ISREG(metadata.st_mode)
        or metadata.st_uid != 0
        or metadata.st_mode & 0o111 == 0
    ):
        fail("checker-process-identity-unavailable", "the fixed system codesign tool is unavailable")
    try:
        completed = subprocess.run(
            [str(CODESIGN_PATH), "-dvvv", target],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env={"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"},
            timeout=CODESIGN_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.TimeoutExpired):
        fail("checker-process-identity-unavailable", "codesign identity probe did not complete")
    output = completed.stdout + completed.stderr
    if completed.returncode != 0 or len(output) > CODESIGN_MAX_OUTPUT_BYTES:
        fail("checker-process-identity-unavailable", "codesign identity probe failed closed")
    matches = CDHASH_FULL_RE.findall(output)
    if len(matches) != 1:
        fail("checker-process-identity-unavailable", "codesign did not return one full SHA-256 CDHash")
    return "sha256:" + matches[0].decode("ascii")


def expected_checker_execution_identity(copied_path: Path, checker_digest: str) -> tuple[str, str]:
    if sys.platform == "darwin":
        return "darwin-codesign-cdhash-full-sha256", codesign_identity(str(copied_path))
    if sys.platform.startswith("linux"):
        return "linux-proc-exe-sha256", checker_digest
    fail("checker-process-identity-unavailable", "the host has no supported process-image identity probe")


def actual_checker_execution_identity(process: subprocess.Popen) -> tuple[str, str]:
    if process.poll() is not None:
        fail("checker-process-identity-unavailable", "checker exited before process identity attestation")
    if sys.platform == "darwin":
        return "darwin-codesign-cdhash-full-sha256", codesign_identity(f"+{process.pid}")
    if sys.platform.startswith("linux"):
        try:
            descriptor = os.open(f"/proc/{process.pid}/exe", os.O_RDONLY | os.O_CLOEXEC)
        except OSError:
            fail("checker-process-identity-unavailable", "cannot open the running checker image")
        try:
            raw, _ = read_open_checker(
                descriptor, CHECKER_MAX_BYTES, "checker-process-identity-unavailable"
            )
        finally:
            os.close(descriptor)
        return "linux-proc-exe-sha256", sha256_bytes(raw)
    fail("checker-process-identity-unavailable", "the host has no supported process-image identity probe")


def stop_owned_checker(process: subprocess.Popen) -> None:
    try:
        if process.poll() is None:
            try:
                process.terminate()
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                try:
                    process.kill()
                except ProcessLookupError:
                    pass
                process.wait(timeout=2)
    finally:
        for stream in (process.stdin, process.stdout, process.stderr):
            if stream is not None and not stream.closed:
                stream.close()


def invoke_core_checker(
    checker: Path,
    subject: dict,
    raw_inputs: dict[str, bytes],
    *,
    before_spawn=None,
    after_spawn=None,
) -> tuple[dict, str, str, str]:
    if not checker.is_absolute() or checker.resolve() != checker:
        fail("checker-invalid", "--checker must be an absolute canonical path")
    metadata = checker.lstat()
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o111 == 0:
        fail("checker-invalid", "--checker must be a non-symlink regular file")
    if metadata.st_size <= 0 or metadata.st_size > CHECKER_MAX_BYTES:
        fail("checker-invalid", "--checker exceeds its closed size bound")
    try:
        checker_fd = os.open(
            checker,
            os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
        )
    except OSError as error:
        fail("checker-invalid", f"cannot open checker without following links: {error}")
    try:
        before = os.fstat(checker_fd)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_mode & 0o111 == 0
            or before.st_size <= 0
            or before.st_size > CHECKER_MAX_BYTES
            or (before.st_dev, before.st_ino) != (metadata.st_dev, metadata.st_ino)
        ):
            fail("checker-invalid", "checker identity changed before held open")
        checker_raw, after = read_open_checker(
            checker_fd, CHECKER_MAX_BYTES, "checker-changed-during-read"
        )
        if checker_stat_identity(before) != checker_stat_identity(after):
            fail("checker-changed-during-read", "checker changed during bounded held read")
        checker_digest = sha256_bytes(checker_raw)
        envelope = {"subject": subject}
        envelope.update(
            {label: base64.b64encode(raw_inputs[label]).decode("ascii") for label in INPUT_HARD_LIMITS}
        )
        # Darwin cannot portably fexecve a held descriptor from Python. Copy
        # the already-held bytes to a private inode, derive its expected code
        # identity, then attest the blocked child process image before sending
        # any evidence. Path/inode checks remain defense in depth, not the
        # claim that proves which image actually ran.
        with tempfile.TemporaryDirectory(prefix="marshal-attestation-checker-") as private_root:
            copied_path = Path(private_root) / "checker"
            copied_fd = os.open(copied_path, os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_CLOEXEC, 0o600)
            process = None
            try:
                offset = 0
                while offset < len(checker_raw):
                    offset += os.write(copied_fd, checker_raw[offset:])
                os.fchmod(copied_fd, 0o700)
                os.fsync(copied_fd)
                copied_before = os.fstat(copied_fd)
                verify_private_checker_path(copied_path, copied_before, checker_raw)
                identity_method, expected_execution_identity = expected_checker_execution_identity(
                    copied_path, checker_digest
                )
                verify_private_checker_path(copied_path, copied_before, checker_raw)
                if before_spawn is not None:
                    before_spawn(copied_path)
                process = subprocess.Popen(
                    [str(copied_path)],
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    env={"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"},
                )
                actual_method, actual_execution_identity = actual_checker_execution_identity(process)
                if actual_method != identity_method or not hmac.compare_digest(
                    actual_execution_identity, expected_execution_identity
                ):
                    fail(
                        "checker-process-identity-mismatch",
                        "running checker image differs from the identity derived before spawn",
                    )
                if after_spawn is not None:
                    after_spawn(copied_path, process)
                verify_private_checker_path(copied_path, copied_before, checker_raw)
                stdout, stderr = process.communicate(
                    input=json.dumps(
                        envelope, ensure_ascii=False, sort_keys=True, separators=(",", ":")
                    ),
                    timeout=CHECKER_TIMEOUT_SECONDS,
                )
                completed_returncode = process.returncode
                copied_after = os.fstat(copied_fd)
                if checker_stat_identity(copied_before) != checker_stat_identity(copied_after):
                    fail("checker-changed-during-execution", "private checker inode changed during execution")
                verify_private_checker_path(copied_path, copied_before, checker_raw)
            finally:
                if process is not None:
                    stop_owned_checker(process)
                os.close(copied_fd)
    except (OSError, subprocess.TimeoutExpired) as error:
        fail("checker-execution-failed", f"held checker execution failed: {type(error).__name__}")
    finally:
        os.close(checker_fd)
    stream = stdout if completed_returncode == 0 else stderr
    payload = parse_json(stream.encode("utf-8"), "core checker output")
    if completed_returncode != 0:
        fail(payload.get("reasonCode", "core-checker-rejected"), "production Qoder checker rejected evidence")
    if set(payload) != {"status", "reasonCode", "identity", "observation"} or payload.get("status") != "pass":
        fail("checker-output-invalid", "core checker returned an open or invalid output")
    return payload, checker_digest, identity_method, actual_execution_identity


def run(arguments: argparse.Namespace) -> dict:
    root = Path(arguments.root)
    if not root.is_absolute():
        fail("input-root-invalid", "--root must be absolute")
    manifest_raw = read_relative_nofollow(root, arguments.manifest, MANIFEST_MAX_BYTES, "manifest")
    manifest = parse_json(manifest_raw, "manifest")

    schema_path = Path(__file__).resolve().with_name(SCHEMA_NAME)
    schema_raw = read_relative_nofollow(schema_path.parent, schema_path.name, SCHEMA_MAX_BYTES, "schema")
    schema = parse_json(schema_raw, "schema")
    validate_schema_document(schema)
    validate_schema_instance(manifest, schema, schema)

    raw_inputs, digests = load_inputs(root, manifest)
    core, checker_digest, execution_identity_method, execution_identity = invoke_core_checker(
        Path(arguments.checker), manifest["subject"], raw_inputs
    )
    manifest_digest = sha256_bytes(manifest_raw)
    validator_digest = sha256_bytes(Path(__file__).read_bytes())
    implementation_digests = {
        "checkerExecutable": checker_digest,
        "checkerExecutionIdentity": execution_identity,
        "checkerExecutionIdentityMethod": execution_identity_method,
        "operatorSchema": sha256_bytes(schema_raw),
        "validator": validator_digest,
        "profile": digests["profile"],
    }
    core_output_digest = sha256_bytes(
        json.dumps(core, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    )
    return {
        "status": "pass",
        "reasonCode": "transcript-attestation-pass",
        "subject": manifest["subject"],
        "manifestDigest": manifest_digest,
        "inputDigests": digests,
        "implementationDigests": implementation_digests,
        "coreIdentity": core["identity"],
        "observation": core["observation"],
        "attestationDigest": attestation_digest(manifest_digest, digests, implementation_digests, core_output_digest),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="absolute compact input root")
    parser.add_argument("--manifest", required=True, help="manifest path relative to --root")
    parser.add_argument("--checker", required=True, help="absolute prebuilt production Go checker")
    arguments = parser.parse_args()
    try:
        payload = run(arguments)
    except PreflightError as error:
        print(
            json.dumps(
                {"status": "fail", "reasonCode": error.reason_code, "message": str(error)},
                ensure_ascii=False,
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        return 1
    print(json.dumps(payload, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
