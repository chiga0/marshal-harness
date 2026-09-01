#!/usr/bin/env python3
"""Fail-closed binding and extraction for a release-candidate artifact.

This validates GitHub artifact transport identity only. It does not create an
ADR 0048 build attestation and must not be used as signing/deployment authority.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import sys
import zipfile
from typing import NoReturn


MAX_METADATA_BYTES = 1 << 20
MAX_ARCHIVE_BYTES = 1 << 30
EXPECTED_ARCHIVE_MEMBERS = (
    "RELEASE-PAYLOAD.sha256",
    "release-payload.tar",
)
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SOURCE_HEAD = re.compile(r"^[0-9a-f]{40}$")
POSITIVE_DECIMAL = re.compile(r"^[1-9][0-9]*$")


def fail(reason: str) -> NoReturn:
    raise SystemExit(f"[release-artifact-metadata] ERROR: {reason}")


def positive_decimal(label: str, value: str) -> int:
    if not POSITIVE_DECIMAL.fullmatch(value):
        fail(f"{label} must be a canonical positive decimal")
    return int(value)


def normalized_absolute(path: str, label: str) -> None:
    if not path or not os.path.isabs(path) or os.path.normpath(path) != path:
        fail(f"{label} must be one normalized absolute path")


def file_identity(metadata: os.stat_result) -> tuple[int, int, int, int, int, int, int]:
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_mode,
        metadata.st_nlink,
        metadata.st_size,
        metadata.st_mtime_ns,
        metadata.st_ctime_ns,
    )


def open_held_regular(path: str, label: str, maximum: int) -> tuple[int, os.stat_result]:
    normalized_absolute(path, label)
    try:
        before = os.lstat(path)
    except OSError as error:
        fail(f"cannot lstat {label}: {error}")
    if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
        fail(f"{label} must be a regular non-symlink file")
    if before.st_nlink != 1:
        fail(f"{label} must have exactly one hard link")
    if before.st_size <= 0 or before.st_size > maximum:
        fail(f"{label} has an empty or excessive size")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open {label}: {error}")
    held = os.fstat(descriptor)
    if file_identity(before) != file_identity(held):
        os.close(descriptor)
        fail(f"{label} changed while opening")
    return descriptor, held


def require_stable(descriptor: int, held: os.stat_result, label: str) -> None:
    if file_identity(held) != file_identity(os.fstat(descriptor)):
        fail(f"{label} changed while reading")


def read_metadata(path: str) -> bytes:
    descriptor, held = open_held_regular(path, "metadata", MAX_METADATA_BYTES)
    try:
        chunks: list[bytes] = []
        remaining = MAX_METADATA_BYTES + 1
        while remaining:
            chunk = os.read(descriptor, min(65536, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        raw = b"".join(chunks)
        require_stable(descriptor, held, "metadata")
        if len(raw) != held.st_size or len(raw) > MAX_METADATA_BYTES:
            fail("metadata length is not stable and bounded")
        return raw
    finally:
        os.close(descriptor)


def no_duplicate_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON member: {key}")
        result[key] = value
    return result


def parse_metadata(raw: bytes) -> dict[str, object]:
    if raw.startswith(b"\xef\xbb\xbf") or b"\x00" in raw or b"\r" in raw:
        fail("metadata contains forbidden BOM, NUL, or CR bytes")
    try:
        value = json.loads(raw, object_pairs_hook=no_duplicate_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        fail(f"metadata is not one exact JSON object: {error}")
    if not isinstance(value, dict):
        fail("metadata root must be an object")
    return value


def validate_metadata(
    metadata: dict[str, object],
    artifact_id: int,
    digest: str,
    candidate_source_head: str,
    workflow_source_head: str,
    run_id: int,
) -> None:
    expected = {
        "id": artifact_id,
        "name": f"release-payload-{candidate_source_head}",
        "expired": False,
        "digest": f"sha256:{digest}",
    }
    for field, expected_value in expected.items():
        actual = metadata.get(field)
        if field == "id" and type(actual) is not int:
            fail("artifact metadata id has the wrong type")
        if field == "expired" and type(actual) is not bool:
            fail("artifact metadata expired has the wrong type")
        if actual != expected_value:
            fail(f"artifact metadata mismatch: {field}")

    workflow_run = metadata.get("workflow_run")
    if not isinstance(workflow_run, dict):
        fail("artifact metadata is missing workflow_run")
    observed_run_id = workflow_run.get("id")
    if type(observed_run_id) is not int or observed_run_id != run_id:
        fail("artifact workflow run mismatch")
    if workflow_run.get("head_sha") != workflow_source_head:
        fail("artifact workflow sourceHead mismatch")


def digest_archive(descriptor: int, held: os.stat_result) -> str:
    digest = hashlib.sha256()
    total = 0
    os.lseek(descriptor, 0, os.SEEK_SET)
    while True:
        chunk = os.read(descriptor, 1 << 20)
        if not chunk:
            break
        total += len(chunk)
        if total > MAX_ARCHIVE_BYTES:
            fail("artifact archive exceeds 1 GiB")
        digest.update(chunk)
    require_stable(descriptor, held, "artifact archive")
    if total != held.st_size:
        fail("artifact archive length changed while hashing")
    os.lseek(descriptor, 0, os.SEEK_SET)
    return digest.hexdigest()


def open_extract_directory(path: str) -> int:
    normalized_absolute(path, "extract directory")
    if os.path.realpath(path) != path:
        fail("extract directory or parent resolves through a symlink")
    try:
        metadata = os.lstat(path)
    except OSError as error:
        fail(f"cannot lstat extract directory: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("extract directory must be a real directory")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_DIRECTORY", 0)
    flags |= getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open extract directory: {error}")
    if file_identity(metadata) != file_identity(os.fstat(descriptor)):
        os.close(descriptor)
        fail("extract directory changed while opening")
    if os.listdir(descriptor):
        os.close(descriptor)
        fail("extract directory must start empty")
    return descriptor


def write_all(descriptor: int, content: bytes) -> None:
    view = memoryview(content)
    while view:
        written = os.write(descriptor, view)
        if written <= 0:
            fail("short write while extracting artifact")
        view = view[written:]


def extract_archive(descriptor: int, held: os.stat_result, destination: str) -> None:
    destination_fd = open_extract_directory(destination)
    stream = os.fdopen(os.dup(descriptor), "rb")
    try:
        with stream, zipfile.ZipFile(stream, mode="r") as archive:
            members = archive.infolist()
            names = [member.filename for member in members]
            if len(names) != len(EXPECTED_ARCHIVE_MEMBERS):
                fail("artifact archive member count mismatch")
            if len(set(names)) != len(names) or tuple(sorted(names)) != EXPECTED_ARCHIVE_MEMBERS:
                fail("artifact archive members are not the exact closed set")
            for member in members:
                unix_mode = member.external_attr >> 16
                file_type = stat.S_IFMT(unix_mode)
                if (
                    member.is_dir()
                    or member.flag_bits & 0x1
                    or file_type not in (0, stat.S_IFREG)
                    or member.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)
                    or member.file_size <= 0
                    or member.file_size > MAX_ARCHIVE_BYTES
                    or member.compress_size > MAX_ARCHIVE_BYTES
                ):
                    fail(f"unsafe artifact archive member: {member.filename}")
                flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
                flags |= getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
                try:
                    output = os.open(member.filename, flags, 0o600, dir_fd=destination_fd)
                except OSError as error:
                    fail(f"cannot create extracted member {member.filename}: {error}")
                total = 0
                try:
                    with archive.open(member, mode="r") as source:
                        while True:
                            chunk = source.read(1 << 20)
                            if not chunk:
                                break
                            total += len(chunk)
                            if total > member.file_size or total > MAX_ARCHIVE_BYTES:
                                fail(f"expanded member exceeds declared size: {member.filename}")
                            write_all(output, chunk)
                    extracted = os.fstat(output)
                    if (
                        not stat.S_ISREG(extracted.st_mode)
                        or extracted.st_nlink != 1
                        or total != member.file_size
                        or extracted.st_size != member.file_size
                    ):
                        fail(f"extracted member is not stable and regular: {member.filename}")
                finally:
                    os.close(output)
        if tuple(sorted(os.listdir(destination_fd))) != EXPECTED_ARCHIVE_MEMBERS:
            fail("extract directory contains unexpected members")
        require_stable(descriptor, held, "artifact archive")
    except (OSError, zipfile.BadZipFile, RuntimeError) as error:
        fail(f"cannot safely extract artifact archive: {error}")
    finally:
        os.close(destination_fd)


def main(arguments: list[str]) -> int:
    if len(arguments) != 9:
        fail(
            "usage: release-artifact-metadata-check.py ABS_METADATA ABS_ARCHIVE "
            "ABS_EXTRACT_DIR ARTIFACT_ID ARTIFACT_DIGEST CANDIDATE_SOURCE_HEAD "
            "WORKFLOW_SOURCE_HEAD RUN_ID"
        )
    (
        _,
        metadata_path,
        archive_path,
        extract_directory,
        artifact_id_text,
        digest,
        candidate_source_head,
        workflow_source_head,
        run_id_text,
    ) = arguments
    artifact_id = positive_decimal("artifact id", artifact_id_text)
    run_id = positive_decimal("workflow run id", run_id_text)
    if not SHA256.fullmatch(digest):
        fail("artifact digest must be a lowercase SHA-256")
    if not SOURCE_HEAD.fullmatch(candidate_source_head):
        fail("candidate sourceHead must be a lowercase SHA-1 commit")
    if not SOURCE_HEAD.fullmatch(workflow_source_head):
        fail("workflow sourceHead must be a lowercase SHA-1 commit")

    validate_metadata(
        parse_metadata(read_metadata(metadata_path)),
        artifact_id,
        digest,
        candidate_source_head,
        workflow_source_head,
        run_id,
    )
    archive_descriptor, held_archive = open_held_regular(
        archive_path, "artifact archive", MAX_ARCHIVE_BYTES
    )
    try:
        observed_digest = digest_archive(archive_descriptor, held_archive)
        if observed_digest != digest:
            fail("downloaded artifact archive digest mismatch")
        extract_archive(archive_descriptor, held_archive, extract_directory)
    finally:
        os.close(archive_descriptor)

    print("[release-artifact-metadata] PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
