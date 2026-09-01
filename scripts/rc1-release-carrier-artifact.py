#!/usr/bin/env python3
"""Bind a successful exact-head RC1 finalize run to one carrier artifact.

Usage:
  rc1-release-carrier-artifact.py RUN_JSON ARTIFACT_JSON ARCHIVE_ZIP ABS_OUT_DIR \
    RUN_ID ARTIFACT_ID RAW_ARTIFACT_SHA256 SOURCE_HEAD

The output directory must not exist. Only the four ``rc1-carrier/`` members
are materialized. STDOUT is the candidate evidence workflow run id encoded in
the artifact name.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import sys
import zipfile
from pathlib import Path
from typing import NoReturn


SHA256 = re.compile(r"^[0-9a-f]{64}$")
SOURCE_HEAD = re.compile(r"^[0-9a-f]{40}$")
POSITIVE = re.compile(r"^[1-9][0-9]*$")
ARTIFACT_NAME = re.compile(r"^rc1-carrier-([1-9][0-9]*)$")
MAX_JSON = 1 << 20
MAX_ARCHIVE = 1 << 30
MAX_BINARY = 256 << 20
MAX_TEXT = 1 << 20
BINARY = "marshal_1.0.0-rc1_darwin_arm64"
EXPECTED = {
    "dist/RELEASE-MANIFEST": (0o644, MAX_TEXT),
    "dist/SHA256SUMS": (0o644, MAX_TEXT),
    f"dist/{BINARY}": (0o755, MAX_BINARY),
    "rc1-carrier/RC1-CANARY-RECEIPT.json": (0o644, MAX_TEXT),
    "rc1-carrier/RELEASE-MANIFEST": (0o644, MAX_TEXT),
    "rc1-carrier/SHA256SUMS": (0o644, MAX_TEXT),
    f"rc1-carrier/{BINARY}": (0o755, MAX_BINARY),
}


def fail(reason: str) -> NoReturn:
    raise SystemExit(f"[rc1-release-carrier-artifact] ERROR: {reason}")


def no_duplicates(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON member: {key}")
        result[key] = value
    return result


def read_json(path: str, label: str) -> dict[str, object]:
    candidate = Path(path)
    try:
        metadata = candidate.lstat()
    except OSError as error:
        fail(f"cannot stat {label}: {error}")
    if candidate.is_symlink() or not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
        fail(f"{label} must be one regular non-linked file")
    if metadata.st_size <= 0 or metadata.st_size > MAX_JSON:
        fail(f"{label} size is empty or excessive")
    raw = candidate.read_bytes()
    if len(raw) != metadata.st_size or raw.startswith(b"\xef\xbb\xbf") or b"\x00" in raw or b"\r" in raw:
        fail(f"{label} bytes are not stable canonical JSON input")
    try:
        value = json.loads(raw, object_pairs_hook=no_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        fail(f"{label} is invalid JSON: {error}")
    if not isinstance(value, dict):
        fail(f"{label} root must be an object")
    return value


def positive(value: str, label: str) -> int:
    if POSITIVE.fullmatch(value) is None:
        fail(f"{label} must be a canonical positive decimal")
    return int(value)


def digest_file(path: Path) -> str:
    metadata = path.lstat()
    if path.is_symlink() or not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
        fail("artifact archive must be one regular non-linked file")
    if metadata.st_size <= 0 or metadata.st_size > MAX_ARCHIVE:
        fail("artifact archive size is empty or excessive")
    digest = hashlib.sha256()
    total = 0
    with path.open("rb") as handle:
        while chunk := handle.read(1 << 20):
            total += len(chunk)
            if total > MAX_ARCHIVE:
                fail("artifact archive exceeds bound")
            digest.update(chunk)
    after = path.lstat()
    identity = lambda value: (
        value.st_dev,
        value.st_ino,
        value.st_mode,
        value.st_nlink,
        value.st_size,
        value.st_mtime_ns,
        value.st_ctime_ns,
    )
    if total != metadata.st_size or identity(after) != identity(metadata):
        fail("artifact archive changed while hashing")
    return digest.hexdigest()


def validate_run(run: dict[str, object], run_id: int, source_head: str) -> None:
    expected = {
        "id": run_id,
        "path": ".github/workflows/rc1-canary.yml",
        "event": "workflow_dispatch",
        "status": "completed",
        "conclusion": "success",
        "head_sha": source_head,
        "head_branch": "main",
    }
    for field, value in expected.items():
        if run.get(field) != value:
            fail(f"canary finalize run mismatch: {field}")
    repository = run.get("repository")
    if not isinstance(repository, dict) or repository.get("full_name") != "chiga0/marshal-harness":
        fail("canary finalize run repository is not canonical")


def validate_artifact(
    artifact: dict[str, object], run_id: int, artifact_id: int, digest: str, source_head: str
) -> str:
    name = artifact.get("name")
    if not isinstance(name, str) or (match := ARTIFACT_NAME.fullmatch(name)) is None:
        fail("carrier artifact name is not canonical")
    expected = {
        "id": artifact_id,
        "expired": False,
        "digest": f"sha256:{digest}",
    }
    for field, value in expected.items():
        if artifact.get(field) != value:
            fail(f"carrier artifact metadata mismatch: {field}")
    workflow_run = artifact.get("workflow_run")
    if not isinstance(workflow_run, dict):
        fail("carrier artifact lacks workflow_run")
    if workflow_run.get("id") != run_id or workflow_run.get("head_sha") != source_head:
        fail("carrier artifact workflow binding mismatch")
    return match.group(1)


def prepare_output(path: str) -> tuple[Path, int]:
    output = Path(path)
    if not output.is_absolute() or os.path.normpath(path) != path or os.path.realpath(output.parent) != str(output.parent):
        fail("output directory must be a normalized absolute path below a real parent")
    try:
        output.mkdir(mode=0o700)
    except OSError as error:
        fail(f"cannot create output directory: {error}")
    descriptor = os.open(output, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_CLOEXEC", 0))
    if os.listdir(descriptor):
        os.close(descriptor)
        fail("output directory must start empty")
    return output, descriptor


def validate_member(info: zipfile.ZipInfo) -> None:
    if info.filename not in EXPECTED or info.is_dir() or info.flag_bits & 0x1:
        fail(f"unsafe or unexpected archive member: {info.filename}")
    expected_mode, maximum = EXPECTED[info.filename]
    unix_mode = info.external_attr >> 16
    file_type = stat.S_IFMT(unix_mode)
    if file_type not in (0, stat.S_IFREG) or stat.S_IMODE(unix_mode) != expected_mode:
        fail(f"archive member type/mode mismatch: {info.filename}")
    if info.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED):
        fail(f"archive member compression is unsupported: {info.filename}")
    if info.file_size <= 0 or info.file_size > maximum or info.compress_size > MAX_ARCHIVE:
        fail(f"archive member size is empty or excessive: {info.filename}")


def write_all(descriptor: int, content: bytes) -> None:
    remaining = memoryview(content)
    while remaining:
        written = os.write(descriptor, remaining)
        if written <= 0:
            fail("short write while restoring carrier")
        remaining = remaining[written:]


def main(arguments: list[str]) -> int:
    if len(arguments) != 8:
        fail("expected RUN_JSON ARTIFACT_JSON ARCHIVE ABS_OUT_DIR RUN_ID ARTIFACT_ID DIGEST SOURCE_HEAD")
    run_json, artifact_json, archive_text, output_text, run_text, artifact_text, expected_digest, source_head = arguments
    run_id = positive(run_text, "carrier run id")
    artifact_id = positive(artifact_text, "carrier artifact id")
    if SHA256.fullmatch(expected_digest) is None or SOURCE_HEAD.fullmatch(source_head) is None:
        fail("digest or sourceHead is non-canonical")
    validate_run(read_json(run_json, "run metadata"), run_id, source_head)
    evidence_run_id = validate_artifact(
        read_json(artifact_json, "artifact metadata"), run_id, artifact_id, expected_digest, source_head
    )
    archive_path = Path(archive_text)
    if digest_file(archive_path) != expected_digest:
        fail("downloaded carrier archive digest mismatch")
    output, output_fd = prepare_output(output_text)
    try:
        with zipfile.ZipFile(archive_path) as archive:
            infos = archive.infolist()
            names = [info.filename for info in infos]
            if len(names) != len(EXPECTED) or len(set(names)) != len(names) or set(names) != set(EXPECTED):
                fail("carrier archive members are not the exact seven-file set")
            contents: dict[str, bytes] = {}
            for info in infos:
                validate_member(info)
                content = archive.read(info)
                if len(content) != info.file_size:
                    fail(f"archive member length changed: {info.filename}")
                contents[info.filename] = content
            for name in ("RELEASE-MANIFEST", "SHA256SUMS", BINARY):
                if contents[f"dist/{name}"] != contents[f"rc1-carrier/{name}"]:
                    fail(f"dist and carrier copies differ: {name}")
            for source_name, target_name in (
                ("rc1-carrier/RC1-CANARY-RECEIPT.json", "RC1-CANARY-RECEIPT.json"),
                ("rc1-carrier/RELEASE-MANIFEST", "RELEASE-MANIFEST"),
                ("rc1-carrier/SHA256SUMS", "SHA256SUMS"),
                (f"rc1-carrier/{BINARY}", BINARY),
            ):
                mode = EXPECTED[source_name][0]
                descriptor = os.open(
                    target_name,
                    os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0),
                    mode,
                    dir_fd=output_fd,
                )
                try:
                    write_all(descriptor, contents[source_name])
                    os.fchmod(descriptor, mode)
                finally:
                    os.close(descriptor)
    except (OSError, zipfile.BadZipFile, RuntimeError) as error:
        fail(f"cannot validate/extract carrier archive: {error}")
    finally:
        os.close(output_fd)
    if set(os.listdir(output)) != {"RC1-CANARY-RECEIPT.json", "RELEASE-MANIFEST", "SHA256SUMS", BINARY}:
        fail("restored carrier directory is not the exact closed set")
    print(evidence_run_id)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
