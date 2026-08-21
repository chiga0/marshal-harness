"""Shared fixed-Marshal executable identity and process attestation helpers."""

from __future__ import annotations

import hashlib
import hmac
import importlib.util
import os
from pathlib import Path
import stat
import subprocess


class StableMarshalError(Exception):
    pass


def _transcript_module():
    path = Path(__file__).with_name("validate-transcript-attestation-preflight.py")
    spec = importlib.util.spec_from_file_location("marshal_transcript_identity", path)
    if spec is None or spec.loader is None:
        raise StableMarshalError("identity implementation unavailable")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _stat_identity(metadata: os.stat_result) -> tuple[int, int, int, int, int, int]:
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_mode,
        metadata.st_size,
        metadata.st_mtime_ns,
        metadata.st_ctime_ns,
    )


class HeldMarshal:
    def __init__(self, path: Path, descriptor: int, before: os.stat_result, raw: bytes, digest: str, expected: tuple[str, str], module) -> None:
        self.path = path
        self.descriptor = descriptor
        self.before = before
        self.raw = raw
        self.digest = digest
        self.expected = expected
        self.module = module

    def close(self) -> None:
        if self.descriptor >= 0:
            os.close(self.descriptor)
            self.descriptor = -1


def hold(path: Path, max_bytes: int = 64 << 20) -> HeldMarshal:
    if not path.is_absolute() or path.resolve() != path:
        raise StableMarshalError("stable Marshal path must be absolute and canonical")
    try:
        metadata = path.lstat()
    except OSError as error:
        raise StableMarshalError("stable Marshal path is unavailable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o111 == 0:
        raise StableMarshalError("stable Marshal path must be an executable regular file")
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as error:
        raise StableMarshalError("stable Marshal path cannot be held without following links") from error
    try:
        before = os.fstat(descriptor)
        if _stat_identity(before) != _stat_identity(metadata) or before.st_size <= 0 or before.st_size > max_bytes:
            raise StableMarshalError("stable Marshal identity changed before execution")
        module = _transcript_module()
        raw, after = module.read_open_checker(descriptor, max_bytes, "stable Marshal changed during bounded read")
        if _stat_identity(before) != _stat_identity(after):
            raise StableMarshalError("stable Marshal changed during bounded read")
        digest = module.sha256_bytes(raw)
        module.verify_marshal_path(path, before, raw)
        expected = module.expected_checker_execution_identity(raw, digest)
        return HeldMarshal(path, descriptor, before, raw, digest, expected, module)
    except Exception:
        os.close(descriptor)
        raise


def attest(held: HeldMarshal, process: subprocess.Popen) -> None:
    try:
        actual = held.module.actual_checker_execution_identity(process)
        if process.poll() is not None:
            raise StableMarshalError("stable Marshal exited before identity attestation")
        if not hmac.compare_digest(actual[1], held.expected[1]):
            raise StableMarshalError("stable Marshal running image identity differs from held bytes")
        held.module.verify_marshal_path(held.path, held.before, held.raw)
        after = os.fstat(held.descriptor)
        if _stat_identity(held.before) != _stat_identity(after):
            raise StableMarshalError("stable Marshal changed during execution")
    except StableMarshalError:
        raise
    except Exception as error:
        raise StableMarshalError("stable Marshal process identity unavailable") from error


def bounded_environment(extra: dict[str, str] | None = None) -> dict[str, str]:
    environment = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C"}
    if extra:
        environment.update(extra)
    return environment


def execute(
    held: HeldMarshal,
    process: subprocess.Popen,
    payload: bytes,
    timeout_seconds: int | float,
    stdout_limit: int = 64 << 10,
    stderr_limit: int = 64 << 10,
    total_limit: int = 128 << 10,
) -> tuple[bytes, bytes, int]:
    """Attest a live process, then feed one bounded payload and capture it."""
    capture = held.module.BoundedProcessCapture(process, stdout_limit, stderr_limit, total_limit)
    try:
        capture.fault_before_input("stable Marshal output exceeded its bound", "stable Marshal capture failed")
        attest(held, process)
        stdout, stderr, code = capture.finish(
            payload,
            timeout_seconds,
            "stable Marshal output exceeded its bound",
            "stable Marshal exceeded its deadline",
            "stable Marshal pipe failed",
        )
        return stdout, stderr, code
    finally:
        capture.close()
