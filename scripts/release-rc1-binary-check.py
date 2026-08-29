#!/usr/bin/env python3
"""Non-executing RC1 Darwin/arm64 Go binary identity verifier."""

from __future__ import annotations

import os
import pathlib
import shutil
import stat
import struct
import subprocess
import sys


MAX_BINARY_BYTES = 256 << 20
MAX_TOOL_OUTPUT = 1 << 20
MACHO64_LE = 0xFEEDFACF
CPU_TYPE_ARM64 = 0x0100000C
MH_EXECUTE = 2
LC_SEGMENT_64 = 0x19
LC_SYMTAB = 0x2
BUILDINFO_PREFIX = "github.com/chiga0/marshal-harness/internal/buildinfo."


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"[release-rc1-binary-check] ERROR: {message}")


def checked_slice(data: bytes, offset: int, size: int, label: str) -> bytes:
    if offset < 0 or size < 0 or offset + size > len(data):
        fail(f"{label} exceeds bounded Mach-O bytes")
    return data[offset : offset + size]


def read_candidate(path_text: str) -> bytes:
    path = pathlib.Path(path_text)
    try:
        before = os.lstat(path)
    except OSError as error:
        fail(f"cannot lstat candidate: {error}")
    if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
        fail("candidate must be one regular non-symlink file")
    if before.st_size <= 0 or before.st_size > MAX_BINARY_BYTES:
        fail("candidate size is empty or exceeds 256 MiB")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open held candidate: {error}")
    try:
        held = os.fstat(descriptor)
        if (before.st_dev, before.st_ino, before.st_mode, before.st_size) != (
            held.st_dev,
            held.st_ino,
            held.st_mode,
            held.st_size,
        ):
            fail("candidate changed while opening")
        chunks: list[bytes] = []
        remaining = held.st_size
        while remaining:
            chunk = os.read(descriptor, min(65536, remaining))
            if not chunk:
                fail("candidate truncated while held")
            chunks.append(chunk)
            remaining -= len(chunk)
        after = os.fstat(descriptor)
        if (held.st_dev, held.st_ino, held.st_mode, held.st_size) != (
            after.st_dev,
            after.st_ino,
            after.st_mode,
            after.st_size,
        ):
            fail("candidate changed while reading")
        return b"".join(chunks)
    finally:
        os.close(descriptor)


def parse_macho_strings(data: bytes, expected: dict[str, str]) -> None:
    if len(data) < 32:
        fail("candidate is shorter than one Mach-O 64 header")
    magic, cpu_type, _cpu_subtype, file_type, ncmds, sizeofcmds, _flags, _reserved = struct.unpack_from(
        "<IiiIIIII", data, 0
    )
    if magic != MACHO64_LE or cpu_type != CPU_TYPE_ARM64 or file_type != MH_EXECUTE:
        fail("candidate must be one thin little-endian Mach-O arm64 executable")
    if ncmds == 0 or ncmds > 4096 or sizeofcmds > len(data) - 32:
        fail("Mach-O load-command bounds are invalid")

    command_offset = 32
    command_end = 32 + sizeofcmds
    segments: list[tuple[int, int, int]] = []
    nonempty_go_buildid = False
    symtab: tuple[int, int, int, int] | None = None
    for _ in range(ncmds):
        if command_offset + 8 > command_end:
            fail("Mach-O load-command header exceeds declared bounds")
        command, command_size = struct.unpack_from("<II", data, command_offset)
        if command_size < 8 or command_offset + command_size > command_end:
            fail("Mach-O load command exceeds declared bounds")
        if command == LC_SEGMENT_64:
            if command_size < 72:
                fail("LC_SEGMENT_64 is truncated")
            _cmd, _size, _name, vmaddr, _vmsize, fileoff, filesize, _maxprot, _initprot, nsects, _segflags = struct.unpack_from(
                "<II16sQQQQiiII", data, command_offset
            )
            if nsects > 4096 or command_size < 72 + nsects * 80:
                fail("Mach-O section table is truncated or unbounded")
            for section_index in range(nsects):
                section_offset = command_offset + 72 + section_index * 80
                section_name, _segment_name, _address, section_size = struct.unpack_from(
                    "<16s16sQQ", data, section_offset
                )
                if section_name.rstrip(b"\0") == b"__go_buildid" and section_size:
                    nonempty_go_buildid = True
            checked_slice(data, fileoff, filesize, "Mach-O segment")
            segments.append((vmaddr, fileoff, filesize))
        elif command == LC_SYMTAB:
            if command_size != 24 or symtab is not None:
                fail("Mach-O must contain exactly one canonical LC_SYMTAB")
            _cmd, _size, symoff, nsyms, stroff, strsize = struct.unpack_from(
                "<IIIIII", data, command_offset
            )
            symtab = (symoff, nsyms, stroff, strsize)
        command_offset += command_size
    if command_offset != command_end or symtab is None or not segments:
        fail("Mach-O load-command closure, segments, or symbol table is missing")
    if nonempty_go_buildid:
        fail("candidate Go build ID section must be absent or empty")

    def virtual_to_file(address: int, size: int, label: str) -> int:
        matches = [
            fileoff + (address - vmaddr)
            for vmaddr, fileoff, filesize in segments
            if vmaddr <= address and address + size <= vmaddr + filesize
        ]
        if len(matches) != 1:
            fail(f"{label} does not map to exactly one file-backed Mach-O segment")
        checked_slice(data, matches[0], size, label)
        return matches[0]

    symoff, nsyms, stroff, strsize = symtab
    if nsyms == 0 or nsyms > 2_000_000:
        fail("Mach-O symbol count is empty or unbounded")
    checked_slice(data, symoff, nsyms * 16, "Mach-O symbol table")
    strings = checked_slice(data, stroff, strsize, "Mach-O string table")
    found: dict[str, int] = {}
    for index in range(nsyms):
        string_index, symbol_type, _section, _description, value = struct.unpack_from(
            "<IbbHQ", data, symoff + index * 16
        )
        if string_index == 0 or string_index >= len(strings) or symbol_type & 0xE0:
            continue
        nul = strings.find(b"\0", string_index)
        if nul < 0:
            fail("Mach-O symbol name is not NUL terminated")
        try:
            name = strings[string_index:nul].decode("utf-8")
        except UnicodeDecodeError:
            continue
        name = name.removeprefix("_")
        short_name = name.removeprefix(BUILDINFO_PREFIX)
        if short_name not in expected:
            continue
        if short_name in found:
            fail(f"duplicate build identity symbol: {short_name}")
        found[short_name] = value

    if set(found) != set(expected):
        missing = ",".join(sorted(set(expected) - set(found)))
        fail(f"required unstripped build identity symbols are missing: {missing}")
    for name, address in found.items():
        header_offset = virtual_to_file(address, 16, f"{name} Go string header")
        string_address, string_length = struct.unpack_from("<QQ", data, header_offset)
        if string_length == 0 or string_length > 4096:
            fail(f"{name} Go string length is empty or unbounded")
        string_offset = virtual_to_file(string_address, string_length, f"{name} Go string data")
        try:
            actual = checked_slice(data, string_offset, string_length, name).decode("utf-8")
        except UnicodeDecodeError:
            fail(f"{name} Go string is not UTF-8")
        if actual != expected[name]:
            fail(f"binary build identity mismatch for {name}")


def run_go(go_argument: str, *arguments: str) -> str:
    resolved = shutil.which(go_argument) if os.path.sep not in go_argument else go_argument
    if not resolved:
        fail("fixed Go tool is not resolvable")
    resolved = os.path.realpath(resolved)
    try:
        metadata = os.stat(resolved)
    except OSError as error:
        fail(f"cannot stat fixed Go tool: {error}")
    if not stat.S_ISREG(metadata.st_mode) or not os.access(resolved, os.X_OK):
        fail("fixed Go tool must be one executable regular file")
    environment = {
        "GOTOOLCHAIN": "local",
        "GOCACHE": "off",
        "HOME": "/nonexistent",
        "LC_ALL": "C",
        "PATH": f"{os.path.dirname(resolved)}:/usr/bin:/bin",
    }
    try:
        result = subprocess.run(
            [resolved, *arguments],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            env=environment,
            timeout=15,
        )
    except (OSError, subprocess.SubprocessError) as error:
        fail(f"fixed Go inspection failed: {error}")
    if result.returncode != 0 or len(result.stdout) > MAX_TOOL_OUTPUT or len(result.stderr) > MAX_TOOL_OUTPUT:
        fail(f"fixed Go inspection rejected candidate: {result.stderr[:1024]!r}")
    try:
        return result.stdout.decode("utf-8")
    except UnicodeDecodeError:
        fail("fixed Go inspection output is not UTF-8")


def verify_go_build_info(go_bin: str, candidate: str, expected_go_version: str) -> None:
    if run_go(go_bin, "env", "GOVERSION").strip() != expected_go_version:
        fail("fixed Go tool version differs from external expected goVersion")
    output = run_go(go_bin, "version", "-m", candidate)
    lines = output.splitlines()
    if not lines or not lines[0].endswith(f": {expected_go_version}"):
        fail("candidate Go version differs from external expected goVersion")
    if "\tpath\tgithub.com/chiga0/marshal-harness/cmd/marshal" not in lines:
        fail("candidate Go main package is not fixed cmd/marshal")
    settings: dict[str, str] = {}
    for line in lines:
        if not line.startswith("\tbuild\t"):
            continue
        field = line.removeprefix("\tbuild\t")
        if "=" not in field:
            fail("candidate Go build setting is malformed")
        key, value = field.split("=", 1)
        if key in settings:
            fail(f"candidate Go build setting is duplicated: {key}")
        settings[key] = value
    required = {
        "-buildmode": "exe",
        "-compiler": "gc",
        "-trimpath": "true",
        "CGO_ENABLED": "0",
        "GOARCH": "arm64",
        "GOOS": "darwin",
    }
    if any(settings.get(key) != value for key, value in required.items()):
        fail("candidate Go build settings do not prove exe/gc/trimpath/CGO0/darwin/arm64")
    if any(key.startswith("vcs.") for key in settings):
        fail("candidate unexpectedly embeds VCS settings; -buildvcs=false contract drifted")


def main(argv: list[str]) -> int:
    if len(argv) != 8:
        fail(
            "usage: release-rc1-binary-check.py CANDIDATE VERSION SOURCE_HEAD "
            "BUILD_DATE GO_VERSION PROFILE GO_BIN"
        )
    _, candidate, version, source_head, build_date, go_version, profile, go_bin = argv
    expected = {
        "version": version,
        "commit": source_head,
        "buildDate": build_date,
        "selfProfile": profile,
    }
    data = read_candidate(candidate)
    parse_macho_strings(data, expected)
    verify_go_build_info(go_bin, candidate, go_version)
    print("[release-rc1-binary-check] PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
