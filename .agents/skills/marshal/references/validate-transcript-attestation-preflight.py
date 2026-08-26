#!/usr/bin/env python3
"""Fail-closed operator-local Qoder v7 transcript attestation preflight."""

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
import struct
import subprocess
import sys
import threading
import time


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
SCHEMA_NAME = "transcript-attestation-preflight.schema.json"
RECEIPT_SCHEMA_NAME = "transcript-attestation-receipt.schema.json"
READ_CHUNK_BYTES = 1024 * 1024
MANIFEST_MAX_BYTES = 1024 * 1024
SCHEMA_MAX_BYTES = 1024 * 1024
CHECKER_MAX_BYTES = 64 * 1024 * 1024
CODESIGN_MAX_OUTPUT_BYTES = 64 * 1024
CODESIGN_TIMEOUT_SECONDS = 10
CHECKER_TIMEOUT_SECONDS = 120
CHECKER_STDOUT_MAX_BYTES = 1024 * 1024
CHECKER_STDERR_MAX_BYTES = 256 * 1024
CHECKER_TOTAL_OUTPUT_MAX_BYTES = 5 * 1024 * 1024 // 4
CHECKER_STDIN_MAX_BYTES = 32 * 1024 * 1024
PROCESS_IO_CHUNK_BYTES = 16 * 1024
CODESIGN_PATH = Path("/usr/bin/codesign")
CDHASH_FULL_RE = re.compile(rb"(?m)^CandidateCDHashFull sha256=([0-9a-f]{64})$")
MACHO_CODE_SIGNATURE_COMMAND = 0x1D
EMBEDDED_SIGNATURE_MAGIC = 0xFADE0CC0
CODE_DIRECTORY_MAGIC = 0xFADE0C02
CODE_DIRECTORY_BASE_VERSION = 0x20001
CODE_DIRECTORY_MAX_VERSION = 0x20600
CODE_DIRECTORY_SHA256 = 2
CODE_DIRECTORY_SHA256_BYTES = 32
CODE_DIRECTORY_MAX_PLATFORM = 12
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
    frames = [b"marshal-transcript-attestation-v3\n", manifest_digest.encode("ascii"), b"\n"]
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


def verify_marshal_path(marshal_path: Path, held: os.stat_result, expected_raw: bytes) -> None:
    try:
        descriptor = os.open(
            marshal_path,
            os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
        )
    except OSError:
        fail("checker-private-path-changed", "Marshal path is no longer the held regular file")
    try:
        raw, opened = read_open_checker(descriptor, CHECKER_MAX_BYTES, "checker-private-path-changed")
        if (opened.st_dev, opened.st_ino) != (held.st_dev, held.st_ino):
            fail("checker-private-path-changed", "Marshal path inode differs from the held executable")
        if not hmac.compare_digest(hashlib.sha256(raw).digest(), hashlib.sha256(expected_raw).digest()):
            fail("checker-private-path-changed", "Marshal path bytes differ from the held executable")
    finally:
        os.close(descriptor)


class BoundedProcessCapture:
    def __init__(
        self,
        process: subprocess.Popen,
        stdout_limit: int,
        stderr_limit: int,
        total_limit: int,
    ) -> None:
        self.process = process
        self.started_at = time.monotonic()
        self.buffers = {"stdout": bytearray(), "stderr": bytearray()}
        self.limits = {"stdout": stdout_limit, "stderr": stderr_limit}
        self.total_limit = total_limit
        self.total = 0
        self.overflow = ""
        self.io_error = False
        self.closing = False
        self.lock = threading.Lock()
        self.changed = threading.Event()
        self.readers: list[threading.Thread] = []
        for name, stream in (("stdout", process.stdout), ("stderr", process.stderr)):
            if stream is None:
                fail("checker-execution-failed", "owned process is missing a capture pipe")
            thread = threading.Thread(target=self._read_stream, args=(name, stream), daemon=True)
            thread.start()
            self.readers.append(thread)

    def _read_stream(self, name: str, stream) -> None:
        try:
            while True:
                chunk = os.read(stream.fileno(), PROCESS_IO_CHUNK_BYTES)
                if not chunk:
                    return
                with self.lock:
                    stream_remaining = self.limits[name] - len(self.buffers[name])
                    total_remaining = self.total_limit - self.total
                    accepted = min(len(chunk), max(stream_remaining, 0), max(total_remaining, 0))
                    if accepted:
                        self.buffers[name].extend(chunk[:accepted])
                        self.total += accepted
                    if accepted != len(chunk):
                        self.overflow = name
                        self.changed.set()
                        return
        except OSError:
            with self.lock:
                if not self.closing:
                    self.io_error = True
                    self.changed.set()
        finally:
            self.changed.set()

    def fault_before_input(self, overflow_reason: str, io_reason: str) -> None:
        with self.lock:
            overflow = bool(self.overflow)
            io_error = self.io_error
        if overflow:
            self.close()
            fail(overflow_reason, "owned process exceeded its closed output bound before input")
        if io_error:
            self.close()
            fail(io_reason, "owned process capture failed before input")

    def finish(
        self,
        input_bytes: bytes | None,
        timeout_seconds: int,
        overflow_reason: str,
        deadline_reason: str,
        io_reason: str,
    ) -> tuple[bytes, bytes, int]:
        writer = None
        writer_error: list[bool] = []
        if input_bytes is not None:
            if self.process.stdin is None:
                self.close()
                fail(io_reason, "owned process is missing its input pipe")

            def write_input() -> None:
                try:
                    offset = 0
                    descriptor = self.process.stdin.fileno()
                    while offset < len(input_bytes):
                        offset += os.write(descriptor, input_bytes[offset : offset + PROCESS_IO_CHUNK_BYTES])
                except (BrokenPipeError, OSError):
                    writer_error.append(True)
                finally:
                    try:
                        self.process.stdin.close()
                    except OSError:
                        pass
                    self.changed.set()

            writer = threading.Thread(target=write_input, daemon=True)
            writer.start()

        deadline = self.started_at + timeout_seconds
        while self.process.poll() is None:
            with self.lock:
                overflow = bool(self.overflow)
                io_error = self.io_error
            if overflow:
                self.close()
                fail(overflow_reason, "owned process exceeded its closed output bound")
            if io_error:
                self.close()
                fail(io_reason, "owned process capture failed")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.close()
                fail(deadline_reason, "owned process exceeded its closed deadline")
            self.changed.wait(min(0.05, remaining))
            self.changed.clear()

        if writer is not None:
            writer.join(timeout=2)
            if writer.is_alive():
                self.close()
                fail(deadline_reason, "owned process input writer did not stop")
        for reader in self.readers:
            reader.join(timeout=2)
        with self.lock:
            overflow = bool(self.overflow)
            io_error = self.io_error
            stdout = bytes(self.buffers["stdout"])
            stderr = bytes(self.buffers["stderr"])
        if overflow:
            self.close()
            fail(overflow_reason, "owned process exceeded its closed output bound")
        if io_error or writer_error:
            self.close()
            fail(io_reason, "owned process pipe failed")
        return stdout, stderr, self.process.returncode

    def close(self) -> None:
        with self.lock:
            self.closing = True
        stop_owned_checker(self.process)
        for reader in self.readers:
            reader.join(timeout=2)
        for stream in (self.process.stdin, self.process.stdout, self.process.stderr):
            if stream is not None and not stream.closed:
                try:
                    stream.close()
                except OSError:
                    pass


def macho_code_directory_sha256(raw: bytes) -> str:
    if raw[:4] == b"\xcf\xfa\xed\xfe":
        endian = "<"
    elif raw[:4] == b"\xfe\xed\xfa\xcf":
        endian = ">"
    else:
        fail("checker-process-identity-unavailable", "held checker is not one thin 64-bit Mach-O image")
    if len(raw) < 32:
        fail("checker-process-identity-unavailable", "held Mach-O header is truncated")
    ncmds, sizeofcmds = struct.unpack_from(endian + "II", raw, 16)
    if (
        ncmds > 65536
        or sizeofcmds > len(raw) - 32
        or ncmds > sizeofcmds // 8
    ):
        fail("checker-process-identity-unavailable", "held Mach-O load command table is invalid")
    command_offset = 32
    command_end = command_offset + sizeofcmds
    signatures: list[tuple[int, int]] = []
    for _ in range(ncmds):
        if command_offset + 8 > command_end:
            fail("checker-process-identity-unavailable", "held Mach-O load command is truncated")
        command, command_size = struct.unpack_from(endian + "II", raw, command_offset)
        if command_size < 8 or command_offset + command_size > command_end:
            fail("checker-process-identity-unavailable", "held Mach-O load command size is invalid")
        if command == MACHO_CODE_SIGNATURE_COMMAND:
            if command_size < 16:
                fail("checker-process-identity-unavailable", "held code-signature command is truncated")
            data_offset, data_size = struct.unpack_from(endian + "II", raw, command_offset + 8)
            signatures.append((data_offset, data_size))
        command_offset += command_size
    if command_offset != command_end or len(signatures) != 1:
        fail("checker-process-identity-unavailable", "held Mach-O must have one code-signature command")
    signature_offset, signature_size = signatures[0]
    if (
        signature_size < 12
        or signature_offset < command_end
        or signature_offset > len(raw) - signature_size
        or signature_offset + signature_size != len(raw)
    ):
        fail("checker-process-identity-unavailable", "held Mach-O code signature is out of bounds")
    signature = raw[signature_offset : signature_offset + signature_size]
    magic, super_length, count = struct.unpack_from(">III", signature, 0)
    if (
        magic != EMBEDDED_SIGNATURE_MAGIC
        or super_length != len(signature)
        or count > (super_length - 12) // 8
    ):
        fail("checker-process-identity-unavailable", "held Mach-O superblob is invalid")
    index_end = 12 + count * 8
    blobs: list[tuple[int, int, int]] = []
    for index in range(count):
        _slot_type, blob_offset = struct.unpack_from(">II", signature, 12 + index * 8)
        if blob_offset < index_end or blob_offset > super_length - 8:
            fail("checker-process-identity-unavailable", "held embedded-signature blob offset is invalid")
        blob_magic, blob_length = struct.unpack_from(">II", signature, blob_offset)
        if blob_length < 8 or blob_offset > super_length - blob_length:
            fail("checker-process-identity-unavailable", "held embedded-signature blob length is invalid")
        blobs.append((blob_offset, blob_offset + blob_length, blob_magic))
    ordered_blobs = sorted(blobs)
    for previous, current in zip(ordered_blobs, ordered_blobs[1:]):
        if current[0] < previous[1]:
            fail("checker-process-identity-unavailable", "held embedded-signature blobs overlap")

    candidates: list[bytes] = []
    for blob_offset, blob_end, blob_magic in blobs:
        if blob_magic != CODE_DIRECTORY_MAGIC:
            continue
        blob = signature[blob_offset:blob_end]
        if len(blob) < 44:
            fail("checker-process-identity-unavailable", "held code directory is truncated")
        (
            directory_magic,
            directory_length,
            version,
            _flags,
            hash_offset,
            ident_offset,
            special_slots,
            code_slots,
            code_limit,
            hash_size,
            hash_type,
            platform,
            page_size,
            spare2,
        ) = struct.unpack_from(">9I4BI", blob, 0)
        if directory_magic != CODE_DIRECTORY_MAGIC or directory_length != len(blob):
            fail("checker-process-identity-unavailable", "held code directory header is invalid")
        if version < CODE_DIRECTORY_BASE_VERSION or version > CODE_DIRECTORY_MAX_VERSION:
            fail("checker-process-identity-unavailable", "held code directory version is unsupported")
        minimum_length = 44
        for introduced_version, introduced_length in (
            (0x20100, 48),
            (0x20200, 52),
            (0x20300, 64),
            (0x20400, 88),
            (0x20500, 96),
            (0x20600, 108),
        ):
            if version >= introduced_version:
                minimum_length = introduced_length
        if len(blob) < minimum_length:
            fail("checker-process-identity-unavailable", "held code directory version fields are truncated")
        if spare2 != 0:
            fail("checker-process-identity-unavailable", "held code directory reserved field is invalid")
        if hash_size != CODE_DIRECTORY_SHA256_BYTES or hash_type != CODE_DIRECTORY_SHA256:
            continue
        if platform > CODE_DIRECTORY_MAX_PLATFORM or page_size > 31:
            fail("checker-process-identity-unavailable", "held code directory hash geometry is invalid")

        effective_code_limit = code_limit
        if version >= 0x20300:
            spare3, code_limit64 = struct.unpack_from(">IQ", blob, 52)
            if spare3 != 0:
                fail("checker-process-identity-unavailable", "held code directory reserved field is invalid")
            if code_limit == 0:
                if code_limit64 == 0:
                    fail("checker-process-identity-unavailable", "held code directory code limit is invalid")
                effective_code_limit = code_limit64
            elif code_limit64 != 0:
                fail("checker-process-identity-unavailable", "held code directory code limits are ambiguous")
        if effective_code_limit == 0 or effective_code_limit != signature_offset:
            fail("checker-process-identity-unavailable", "held code directory code limit is invalid")
        page_bytes = 1 << page_size
        expected_code_slots = (effective_code_limit + page_bytes - 1) // page_bytes
        if code_slots != expected_code_slots:
            fail("checker-process-identity-unavailable", "held code directory code-slot count is invalid")

        if special_slots > hash_offset // hash_size or code_slots > (len(blob) - hash_offset) // hash_size:
            fail("checker-process-identity-unavailable", "held code directory hash slots are out of bounds")
        special_hash_start = hash_offset - special_slots * hash_size
        code_hash_end = hash_offset + code_slots * hash_size
        if special_hash_start < minimum_length or code_hash_end > len(blob):
            fail("checker-process-identity-unavailable", "held code directory hash slots are out of bounds")
        if ident_offset < minimum_length or ident_offset >= special_hash_start:
            fail("checker-process-identity-unavailable", "held code directory identifier offset is invalid")
        ident_end = blob.find(b"\x00", ident_offset, special_hash_start)
        if ident_end < ident_offset + 1:
            fail("checker-process-identity-unavailable", "held code directory identifier is invalid")
        candidates.append(blob)
    if len(candidates) != 1:
        fail("checker-process-identity-unavailable", "held Mach-O must have one SHA-256 CodeDirectory")
    return sha256_bytes(candidates[0])


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
    process = None
    capture = None
    try:
        process = subprocess.Popen(
            [str(CODESIGN_PATH), "-dvvv", target],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env={"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"},
        )
        capture = BoundedProcessCapture(
            process,
            CODESIGN_MAX_OUTPUT_BYTES,
            CODESIGN_MAX_OUTPUT_BYTES,
            CODESIGN_MAX_OUTPUT_BYTES,
        )
        stdout, stderr, returncode = capture.finish(
            None,
            CODESIGN_TIMEOUT_SECONDS,
            "checker-identity-probe-output-limit-exceeded",
            "checker-identity-probe-deadline-exceeded",
            "checker-process-identity-unavailable",
        )
    except OSError:
        fail("checker-process-identity-unavailable", "codesign identity probe did not complete")
    finally:
        if capture is not None:
            capture.close()
        elif process is not None:
            stop_owned_checker(process)
    output = stdout + stderr
    if returncode != 0:
        fail("checker-process-identity-unavailable", "codesign identity probe failed closed")
    matches = CDHASH_FULL_RE.findall(output)
    if len(matches) != 1:
        fail("checker-process-identity-unavailable", "codesign did not return one full SHA-256 CDHash")
    return "sha256:" + matches[0].decode("ascii")


def expected_checker_execution_identity(checker_raw: bytes, checker_digest: str) -> tuple[str, str]:
    if sys.platform == "darwin":
        return "darwin-held-macho-codedirectory-sha256", macho_code_directory_sha256(checker_raw)
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
    marshal_executable: Path,
    expected_marshal: dict,
    subject: dict,
    raw_inputs: dict[str, bytes],
    *,
    before_spawn=None,
    after_process_spawn=None,
    after_spawn=None,
    checker_timeout_seconds=CHECKER_TIMEOUT_SECONDS,
) -> tuple[dict, str, str, str, str]:
    if not marshal_executable.is_absolute() or marshal_executable.resolve() != marshal_executable:
        fail("checker-invalid", "--marshal must be an absolute canonical path")
    try:
        metadata = marshal_executable.lstat()
    except OSError:
        fail("checker-invalid", "--marshal is unavailable")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o111 == 0:
        fail("checker-invalid", "--marshal must be a non-symlink regular file")
    if metadata.st_size <= 0 or metadata.st_size > CHECKER_MAX_BYTES:
        fail("checker-invalid", "--marshal exceeds its closed size bound")
    try:
        checker_fd = os.open(
            marshal_executable,
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
        if checker_digest != expected_marshal["executableSha256"]:
            fail("checker-digest-mismatch", "Marshal raw digest differs from the authorized manifest")
        envelope = {"subject": subject}
        envelope.update(
            {label: base64.b64encode(raw_inputs[label]).decode("ascii") for label in INPUT_HARD_LIMITS}
        )
        envelope_raw = json.dumps(
            envelope, ensure_ascii=False, sort_keys=True, separators=(",", ":")
        ).encode("utf-8")
        if len(envelope_raw) > CHECKER_STDIN_MAX_BYTES:
            fail("checker-input-too-large", "Marshal internal command input exceeds its closed bound")
        envelope_digest = sha256_bytes(envelope_raw)
        # The caller supplies the stable Marshal executable that it already
        # permits. Keep its file descriptor and raw bytes held, derive the
        # expected process-image identity from those bytes, and start only its
        # hidden internal subcommand. The child remains blocked on stdin until
        # PID/CDHash (Darwin) or /proc/PID/exe (Linux), path, inode and digest
        # checks all match. No executable is copied into a random temp path.
        expected_identity_method, expected_execution_identity = expected_checker_execution_identity(
            checker_raw, checker_digest
        )
        verify_marshal_path(marshal_executable, before, checker_raw)
        if before_spawn is not None:
            before_spawn(marshal_executable)
        process = None
        capture = None
        try:
            process = subprocess.Popen(
                [str(marshal_executable), "internal", "qoder-transcript-check", "--attestation-ready"],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env={"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"},
            )
            capture = BoundedProcessCapture(
                process,
                CHECKER_STDOUT_MAX_BYTES,
                CHECKER_STDERR_MAX_BYTES,
                CHECKER_TOTAL_OUTPUT_MAX_BYTES,
            )
            if after_process_spawn is not None:
                after_process_spawn(marshal_executable, process)
            actual_method, actual_execution_identity = actual_checker_execution_identity(process)
            capture.fault_before_input(
                "checker-output-limit-exceeded", "checker-execution-failed"
            )
            if process.poll() is not None:
                fail("checker-process-identity-unavailable", "Marshal exited after identity probe")
            if not hmac.compare_digest(actual_execution_identity, expected_execution_identity):
                fail(
                    "checker-process-identity-mismatch",
                    "running Marshal image differs from the identity derived before spawn",
                )
            if after_spawn is not None:
                after_spawn(marshal_executable, process)
            verify_marshal_path(marshal_executable, before, checker_raw)
            capture.fault_before_input(
                "checker-output-limit-exceeded", "checker-execution-failed"
            )
            stdout, stderr, completed_returncode = capture.finish(
                b"\0" + envelope_raw,
                checker_timeout_seconds,
                "checker-output-limit-exceeded",
                "checker-deadline-exceeded",
                "checker-execution-failed",
            )
            held_after = os.fstat(checker_fd)
            if checker_stat_identity(before) != checker_stat_identity(held_after):
                fail("checker-changed-during-execution", "held Marshal executable changed during execution")
            verify_marshal_path(marshal_executable, before, checker_raw)
        finally:
            if capture is not None:
                capture.close()
            elif process is not None:
                stop_owned_checker(process)
    except (OSError, subprocess.TimeoutExpired) as error:
        fail("checker-execution-failed", f"held checker execution failed: {type(error).__name__}")
    finally:
        os.close(checker_fd)
    if completed_returncode == 0 and stderr:
        fail("checker-output-invalid", "Marshal internal command emitted stderr on success")
    if completed_returncode != 0 and stdout:
        fail("checker-output-invalid", "Marshal internal command emitted stdout on failure")
    stream = stdout if completed_returncode == 0 else stderr
    payload = parse_json(stream, "core checker output")
    if completed_returncode != 0:
        if set(payload) != {"status", "reasonCode"} or payload.get("status") != "fail":
            fail("checker-output-invalid", "Marshal internal command returned an open failure output")
        reason = payload.get("reasonCode")
        if not isinstance(reason, str) or not re.fullmatch(r"[a-z0-9-]{1,128}", reason):
            fail("checker-output-invalid", "Marshal internal command returned an invalid reason code")
        fail(reason, "production Qoder checker rejected evidence")
    if set(payload) != {"status", "reasonCode", "identity", "marshal", "observation"} or payload.get("status") != "pass":
        fail("checker-output-invalid", "core checker returned an open or invalid output")
    marshal_identity = payload.get("marshal")
    if not isinstance(marshal_identity, dict) or set(marshal_identity) != {
        "version", "commit", "internalCommandVersion", "inputDigest"
    }:
        fail("checker-output-invalid", "Marshal internal command identity is invalid")
    if marshal_identity.get("inputDigest") != envelope_digest:
        fail("checker-input-digest-mismatch", "Marshal internal command did not bind the exact stdin")
    if marshal_identity.get("commit") != expected_marshal["sourceHead"]:
        fail("checker-source-head-mismatch", "Marshal build commit differs from the authorized manifest")
    if marshal_identity.get("version") != expected_marshal["version"]:
        fail("checker-version-mismatch", "Marshal build version differs from the authorized manifest")
    if marshal_identity.get("internalCommandVersion") != expected_marshal["internalCommandVersion"]:
        fail("checker-command-version-mismatch", "Marshal internal command version differs from the authorized manifest")
    return (
        payload,
        checker_digest,
        expected_identity_method,
        actual_method,
        actual_execution_identity,
    )


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
    receipt_schema_path = Path(__file__).resolve().with_name(RECEIPT_SCHEMA_NAME)
    receipt_schema_raw = read_relative_nofollow(
        receipt_schema_path.parent, receipt_schema_path.name, SCHEMA_MAX_BYTES, "receipt schema"
    )
    receipt_schema = parse_json(receipt_schema_raw, "receipt schema")
    validate_schema_document(receipt_schema)

    raw_inputs, digests = load_inputs(root, manifest)
    (
        core,
        checker_digest,
        expected_execution_identity_method,
        actual_execution_identity_method,
        execution_identity,
    ) = invoke_core_checker(Path(arguments.marshal), manifest["marshal"], manifest["subject"], raw_inputs)
    manifest_digest = sha256_bytes(manifest_raw)
    validator_digest = sha256_bytes(Path(__file__).read_bytes())
    implementation_digests = {
        "marshalExecutable": checker_digest,
        "marshalExecutionIdentity": execution_identity,
        "marshalExecutionExpectedIdentityMethod": expected_execution_identity_method,
        "marshalExecutionActualIdentityMethod": actual_execution_identity_method,
        "marshalInternalCommand": sha256_bytes(b"internal\0qoder-transcript-check\0--attestation-ready"),
        "marshalBuildCommit": core["marshal"]["commit"],
        "stdinEnvelopeDigest": core["marshal"]["inputDigest"],
        "marshalBuildIdentity": sha256_bytes(
            json.dumps(core["marshal"], ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
        ),
        "operatorSchema": sha256_bytes(schema_raw),
        "receiptSchema": sha256_bytes(receipt_schema_raw),
        "validator": validator_digest,
        "profile": digests["profile"],
    }
    core_output_digest = sha256_bytes(
        json.dumps(core, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    )
    receipt = {
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
    validate_schema_instance(receipt, receipt_schema, receipt_schema)
    return receipt


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="absolute compact input root")
    parser.add_argument("--manifest", required=True, help="manifest path relative to --root")
    parser.add_argument("--marshal", required=True, help="absolute stable Marshal executable")
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
