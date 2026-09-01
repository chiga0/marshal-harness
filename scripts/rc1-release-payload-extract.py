#!/usr/bin/env python3
"""Safely extract the exact four-member admitted RC1 release payload."""

from __future__ import annotations

import os
import stat
import sys
import tarfile
from pathlib import Path
from typing import NoReturn


MAX_ARCHIVE = 300 << 20
MAX_BINARY = 256 << 20
MAX_TEXT = 1 << 20
BINARY = "marshal_1.0.0-rc1_darwin_arm64"
EXPECTED = {
    "RC1-CANARY-RECEIPT.json": (0o644, MAX_TEXT),
    "RELEASE-MANIFEST": (0o644, MAX_TEXT),
    "SHA256SUMS": (0o644, MAX_TEXT),
    BINARY: (0o755, MAX_BINARY),
}


def fail(reason: str) -> NoReturn:
    raise SystemExit(f"[rc1-release-payload-extract] ERROR: {reason}")


def write_all(descriptor: int, content: bytes) -> None:
    remaining = memoryview(content)
    while remaining:
        written = os.write(descriptor, remaining)
        if written <= 0:
            fail("short write while extracting payload")
        remaining = remaining[written:]


def main(arguments: list[str]) -> int:
    if len(arguments) != 2:
        fail("expected ABS_PAYLOAD_TAR ABS_EMPTY_OUTPUT_DIR")
    archive_text, output_text = arguments
    archive = Path(archive_text)
    output = Path(output_text)
    for path, label in ((archive, "archive"), (output, "output directory")):
        if not path.is_absolute() or os.path.normpath(str(path)) != str(path) or os.path.realpath(path) != str(path):
            fail(f"{label} must be one normalized real absolute path")
    archive_meta = archive.lstat()
    output_meta = output.lstat()
    if archive.is_symlink() or not stat.S_ISREG(archive_meta.st_mode) or archive_meta.st_nlink != 1:
        fail("archive must be one regular non-linked file")
    if archive_meta.st_size <= 0 or archive_meta.st_size > MAX_ARCHIVE:
        fail("archive size is empty or excessive")
    if output.is_symlink() or not stat.S_ISDIR(output_meta.st_mode) or any(output.iterdir()):
        fail("output must be one empty real directory")
    output_fd = os.open(output, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_CLOEXEC", 0))
    try:
        with tarfile.open(archive, mode="r:") as payload:
            members = payload.getmembers()
            names = [member.name for member in members]
            if len(names) != len(EXPECTED) or len(set(names)) != len(names) or set(names) != set(EXPECTED):
                fail("payload members are not the exact four-file set")
            for member in members:
                mode, maximum = EXPECTED[member.name]
                if (
                    not member.isfile()
                    or member.issym()
                    or member.islnk()
                    or member.linkname
                    or member.mode != mode
                    or member.size <= 0
                    or member.size > maximum
                ):
                    fail(f"unsafe payload member: {member.name}")
                source = payload.extractfile(member)
                if source is None:
                    fail(f"cannot read payload member: {member.name}")
                content = source.read(maximum + 1)
                if len(content) != member.size or len(content) > maximum:
                    fail(f"payload member length mismatch: {member.name}")
                descriptor = os.open(
                    member.name,
                    os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0),
                    mode,
                    dir_fd=output_fd,
                )
                try:
                    write_all(descriptor, content)
                    os.fchmod(descriptor, mode)
                finally:
                    os.close(descriptor)
    except (OSError, tarfile.TarError) as error:
        fail(f"cannot validate/extract payload: {error}")
    finally:
        os.close(output_fd)
    if set(os.listdir(output)) != set(EXPECTED):
        fail("output directory is not the exact closed set")
    print("[rc1-release-payload-extract] PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
